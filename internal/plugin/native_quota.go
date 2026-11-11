package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Native quota RPC (CLIProxyAPI v7.2.159+), declared locally so the plugin
// keeps its v7.2.138 schema version and still loads on older hosts, which
// ignore the quota_provider capability.
const (
	methodQuotaIdentifier = "quota.identifier"
	methodQuotaDescribe   = "quota.describe"
	methodQuotaFetch      = "quota.fetch"
	methodQuotaReset      = "quota.reset"
)

const quotaDisplayName = "OpenCode Go"

type nativeQuotaFetchRequest struct {
	Provider   string            `json:"provider"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

type nativeQuotaDescribeResponse struct {
	SupportedProviders []string `json:"supported_providers,omitempty"`
	DisplayName        string   `json:"display_name,omitempty"`
	SupportsReset      bool     `json:"supports_reset,omitempty"`
}

type nativeQuotaSubscription struct {
	Plan string `json:"plan,omitempty"`
}

type nativeQuotaBucket struct {
	Window            string  `json:"window,omitempty"`
	RemainingFraction float64 `json:"remainingFraction"`
	ResetTime         string  `json:"resetTime,omitempty"`
}

type nativeQuotaGroup struct {
	DisplayName string              `json:"displayName,omitempty"`
	Buckets     []nativeQuotaBucket `json:"buckets,omitempty"`
}

type nativeQuotaFetchResponse struct {
	Subscription *nativeQuotaSubscription `json:"subscription,omitempty"`
	Groups       []nativeQuotaGroup       `json:"groups,omitempty"`
}

func (m *Manager) handleNativeQuota(method string, request []byte) []byte {
	switch method {
	case methodQuotaIdentifier:
		return okEnvelope(map[string]string{"identifier": ProviderID})
	case methodQuotaDescribe:
		return okEnvelope(nativeQuotaDescribeResponse{SupportedProviders: []string{ProviderID}, DisplayName: quotaDisplayName})
	case methodQuotaFetch:
		var req nativeQuotaFetchRequest
		if json.Unmarshal(request, &req) != nil {
			return ErrEnvelope("invalid_request", "malformed quota fetch request body")
		}
		resp, err := m.fetchNativeQuota(context.Background(), req)
		if err != nil {
			return ErrEnvelope("quota_failure", err.Error())
		}
		return okEnvelope(resp)
	default:
		return ErrEnvelope("unsupported", "OpenCode Go does not support quota resets")
	}
}

// fetchNativeQuota uses the credential the host selected, never a config key.
func (m *Manager) fetchNativeQuota(ctx context.Context, req nativeQuotaFetchRequest) (nativeQuotaFetchResponse, error) {
	if req.Provider != ProviderID {
		return nativeQuotaFetchResponse{}, fmt.Errorf("unsupported quota provider")
	}
	key := strings.TrimSpace(req.Attributes["api_key"])
	if key == "" {
		return nativeQuotaFetchResponse{}, fmt.Errorf("quota credential has no api key")
	}
	m.mu.RLock()
	baseURL, timeout := m.cfg.BaseURL, m.cfg.RequestTimeout
	m.mu.RUnlock()
	usage, err := fetchQuota(ctx, m.bridge, baseURL, timeout, key)
	if err != nil {
		return nativeQuotaFetchResponse{}, err
	}
	return nativeQuotaFromUsage(usage)
}

func nativeQuotaFromUsage(usage quotaUsage) (nativeQuotaFetchResponse, error) {
	// OpenCode Go's rolling window spans five hours.
	windows := []struct {
		name   string
		window quotaWindow
	}{{"5h", usage.Rolling}, {"weekly", usage.Weekly}, {"monthly", usage.Monthly}}
	buckets := make([]nativeQuotaBucket, 0, len(windows))
	for _, w := range windows {
		if w.window.Status == "" && w.window.ResetsAt == "" {
			continue
		}
		percent := min(max(w.window.Percent, 0), 100)
		buckets = append(buckets, nativeQuotaBucket{Window: w.name, RemainingFraction: float64(100-percent) / 100, ResetTime: w.window.ResetsAt})
	}
	if len(buckets) == 0 {
		return nativeQuotaFetchResponse{}, fmt.Errorf("quota response has no readings")
	}
	return nativeQuotaFetchResponse{
		Subscription: &nativeQuotaSubscription{Plan: "Go"},
		Groups:       []nativeQuotaGroup{{DisplayName: quotaDisplayName, Buckets: buckets}},
	}, nil
}
