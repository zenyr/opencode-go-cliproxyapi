package plugin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

	"opencode-go-cliproxyapi/internal/config"
)

func TestNativeQuotaRegistrationKeepsLegacySchema(t *testing.T) {
	var registration struct {
		SchemaVersion uint32         `json:"schema_version"`
		Capabilities  map[string]any `json:"capabilities"`
	}
	decodeResult(t, mustHandle(t, NewManager(nil), pluginabi.MethodPluginRegister, lifecycleRequestBody(testValidYAML)), &registration)
	// Hosts before v7.2.159 reject plugins whose schema is newer than 3.
	if registration.SchemaVersion != 3 {
		t.Fatalf("schema_version = %d, want 3", registration.SchemaVersion)
	}
	if registration.Capabilities["quota_provider"] != true || registration.Capabilities["management_api"] != true {
		t.Fatalf("capabilities = %+v", registration.Capabilities)
	}
}

func TestNativeQuotaIdentifierAndDescribe(t *testing.T) {
	m := NewManager(nil)
	var id struct{ Identifier string }
	decodeResult(t, mustHandle(t, m, methodQuotaIdentifier, []byte(`{}`)), &id)
	if id.Identifier != ProviderID {
		t.Fatalf("identifier = %q", id.Identifier)
	}
	var desc nativeQuotaDescribeResponse
	decodeResult(t, mustHandle(t, m, methodQuotaDescribe, []byte(`{}`)), &desc)
	if len(desc.SupportedProviders) != 1 || desc.SupportedProviders[0] != ProviderID || desc.SupportsReset {
		t.Fatalf("describe = %+v", desc)
	}
}

func TestNativeQuotaFetchUsesSelectedCredential(t *testing.T) {
	const selected = "native-selected-secret"
	f := &fakeCaller{responder: func(method string, payload []byte) ([]byte, error) {
		if method != pluginabi.MethodHostHTTPDo {
			return hostOK(map[string]any{}), nil
		}
		var wire struct {
			URL     string      `json:"url"`
			Headers http.Header `json:"headers"`
		}
		if err := json.Unmarshal(payload, &wire); err != nil || wire.URL != "https://quota.test/v1/usage" || wire.Headers.Get("Authorization") != "Bearer "+selected {
			t.Fatalf("bad quota request: %s", payload)
		}
		return hostOK(pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"usage":{"rolling":{"status":"ok","percent":0,"resetsAt":"2026-10-04T15:26:49.000Z"},"weekly":{"status":"ok","percent":4,"resetsAt":"2026-10-05T00:00:00.000Z"},"monthly":{"status":"ok","percent":41,"resetsAt":"2026-10-26T10:20:31.000Z"}}}`)}), nil
	}}
	m := NewManager(NewHostBridge(f.call))
	m.cfg = config.Config{BaseURL: "https://quota.test/v1", RequestTimeout: config.DefaultRequestTimeout, APIKeys: []config.APIKey{{Value: "config-default-key"}}}
	raw := mustHandle(t, m, methodQuotaFetch, []byte(`{"auth_index":"1","provider":"opencode-go","attributes":{"api_key":"`+selected+`"},"host_callback_id":"cb-1"}`))
	var wire map[string]any
	decodeResult(t, raw, &wire)
	bucket := wire["groups"].([]any)[0].(map[string]any)["buckets"].([]any)[1].(map[string]any)
	if wire["subscription"].(map[string]any)["plan"] != "Go" || bucket["window"] != "weekly" || bucket["remainingFraction"] != 0.96 || bucket["resetTime"] != "2026-10-05T00:00:00.000Z" {
		t.Fatalf("wire response = %+v", wire)
	}
	var got nativeQuotaFetchResponse
	decodeResult(t, raw, &got)
	if got.Subscription == nil || got.Subscription.Plan != "Go" || len(got.Groups) != 1 {
		t.Fatalf("response = %+v", got)
	}
	want := []nativeQuotaBucket{
		{Window: "5h", RemainingFraction: 1, ResetTime: "2026-10-04T15:26:49.000Z"},
		{Window: "weekly", RemainingFraction: 0.96, ResetTime: "2026-10-05T00:00:00.000Z"},
		{Window: "monthly", RemainingFraction: 0.59, ResetTime: "2026-10-26T10:20:31.000Z"},
	}
	buckets := got.Groups[0].Buckets
	if len(buckets) != len(want) {
		t.Fatalf("buckets = %+v", buckets)
	}
	for i := range want {
		if buckets[i] != want[i] {
			t.Fatalf("bucket %d = %+v, want %+v", i, buckets[i], want[i])
		}
	}
}

func TestNativeQuotaSkipsMissingWindowsAndClampsPercent(t *testing.T) {
	got, err := nativeQuotaFromUsage(quotaUsage{Weekly: quotaWindow{Status: "ok", Percent: 130}})
	if err != nil || len(got.Groups[0].Buckets) != 1 || got.Groups[0].Buckets[0] != (nativeQuotaBucket{Window: "weekly", RemainingFraction: 0}) {
		t.Fatalf("response = %+v err=%v", got, err)
	}
	if _, err := nativeQuotaFromUsage(quotaUsage{}); err == nil {
		t.Fatal("empty usage did not fail")
	}
}

func TestNativeQuotaFailuresAreRedacted(t *testing.T) {
	const key = "native-error-secret"
	f := &fakeCaller{responder: func(string, []byte) ([]byte, error) {
		return hostOK(pluginapi.HTTPResponse{StatusCode: http.StatusUnauthorized, Body: []byte(key + " upstream body")}), nil
	}}
	m := NewManager(NewHostBridge(f.call))
	m.cfg = config.Config{BaseURL: "https://quota.test/v1", RequestTimeout: config.DefaultRequestTimeout}
	for name, body := range map[string]string{
		"upstream": `{"provider":"opencode-go","attributes":{"api_key":"` + key + `"}}`,
		"provider": `{"provider":"codex","attributes":{"api_key":"` + key + `"}}`,
		"no key":   `{"provider":"opencode-go"}`,
		"json":     `{`,
	} {
		t.Run(name, func(t *testing.T) {
			resp := string(mustHandle(t, m, methodQuotaFetch, []byte(body)))
			if !strings.Contains(resp, `"ok":false`) || strings.Contains(resp, key) || strings.Contains(resp, "upstream body") {
				t.Fatalf("response = %s", resp)
			}
		})
	}
}

func TestQuotaTimeoutIsBounded(t *testing.T) {
	for in, want := range map[time.Duration]time.Duration{
		0:                            maxQuotaTimeout,
		config.DefaultRequestTimeout: maxQuotaTimeout,
		5 * time.Second:              5 * time.Second,
		maxQuotaTimeout:              maxQuotaTimeout,
		-time.Second:                 maxQuotaTimeout,
	} {
		if got := quotaTimeout(in); got != want {
			t.Fatalf("quotaTimeout(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestNativeQuotaResetIsUnsupported(t *testing.T) {
	resp := string(mustHandle(t, NewManager(nil), methodQuotaReset, []byte(`{}`)))
	if !strings.Contains(resp, `"ok":false`) || !strings.Contains(resp, "unsupported") {
		t.Fatalf("response = %s", resp)
	}
}
