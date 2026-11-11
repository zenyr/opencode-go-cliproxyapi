// Package errclass implements FR-009 error classification and the
// resolved §7 retry semantics (07-open-questions.md).
package errclass

import (
	"regexp"
	"strings"
)

// Class is a stable error-category identifier (FR-009) used as the
// envelope error Code.
type Class string

const (
	ClassAuth         Class = "auth_failure"
	ClassInvalidModel Class = "invalid_model"
	ClassUnsupported  Class = "unsupported_protocol_or_parameter"
	ClassRateLimit    Class = "rate_limit"
	ClassQuota        Class = "quota_exhaustion"
	ClassBilling      Class = "billing"
	ClassUpstream     Class = "upstream_server_failure"
	ClassNetwork      Class = "timeout_or_network_failure"
	ClassTranslation  Class = "translation_failure"
)

// Host routing codes recognized by CPA v8 (pluginabi.ErrorCode*). The
// pinned v7 SDK predates them, so they are mirrored here.
const (
	CodeTransientTransport  = "transient_transport"
	CodeStreamTruncated     = "stream_truncated"
	CodeConnectionLifecycle = "connection_lifecycle"
)

// Error describes classified request errors and retryability for CPA execution.
type Error struct {
	Class      Class
	Message    string
	StatusCode int
	Retryable  bool
	// RoutingCode replaces Class as the wire code when CPA needs a routing hint.
	RoutingCode string
	// CredentialScoped marks a failure that blocks the whole API key.
	CredentialScoped bool
	// RetryAfterSeconds carries the upstream Retry-After hint.
	RetryAfterSeconds int
}

// WireError is the host error shape (pluginabi.Error in CPA v8). It is used
// for RPC envelopes and stream error_detail.
type WireError struct {
	Code              string `json:"code"`
	Message           string `json:"message"`
	Retryable         bool   `json:"retryable,omitempty"`
	HTTPStatus        int    `json:"http_status,omitempty"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"`
	CredentialScoped  bool   `json:"credential_scoped,omitempty"`
}

// quotaCodes are upstream error types that report an account cap rather
// than a throttle. The list follows OpenCode's own QUOTA_CODES.
var quotaCodes = map[string]bool{
	"gousagelimiterror":   true,
	"freeusagelimiterror": true,
	"creditlimitexceeded": true,
	"insufficient_quota":  true,
	"usage_not_included":  true,
	"billing_error":       true,
}

// IsQuotaCode reports whether an upstream error type or code is an account cap.
func IsQuotaCode(code string) bool {
	return quotaCodes[strings.ToLower(strings.TrimSpace(code))]
}

func (e *Error) Error() string {
	return string(e.Class) + ": " + e.Message
}

// FromStatus classifies an upstream HTTP status per resolved §7.
// Network/timeout failures (no status) use FromNetwork instead.
func FromStatus(status int, message string) *Error {
	return FromStatusCode(status, "", message)
}

// FromStatusCode classifies like FromStatus and also reads the upstream
// error type/code. A quota code, or a redacted message containing "quota",
// upgrades 429/402/403 to ClassQuota. A 429 quota is credential-scoped: the
// OpenCode Go caps apply to the whole key across all models.
func FromStatusCode(status int, code, message string) *Error {
	e := fromStatus(status, IsQuotaCode(code), message)
	e.CredentialScoped = e.Class == ClassQuota && status == 429
	return e
}

func fromStatus(status int, quotaCode bool, message string) *Error {
	msg := Redact(message)
	quota := quotaCode || strings.Contains(strings.ToLower(msg), "quota")
	quotaClass := func(base Class) Class {
		if quota {
			return ClassQuota
		}
		return base
	}
	switch status {
	case 401:
		return &Error{Class: ClassAuth, Message: msg, StatusCode: status, Retryable: true}
	case 403:
		return &Error{Class: quotaClass(ClassAuth), Message: msg, StatusCode: status, Retryable: true}
	case 402:
		return &Error{Class: quotaClass(ClassBilling), Message: msg, StatusCode: status, Retryable: true}
	case 404:
		return &Error{Class: ClassInvalidModel, Message: msg, StatusCode: status}
	case 429:
		return &Error{Class: quotaClass(ClassRateLimit), Message: msg, StatusCode: status, Retryable: true}
	}
	if status >= 500 {
		return &Error{Class: ClassUpstream, Message: msg, StatusCode: status, Retryable: true}
	}
	return &Error{Class: ClassUnsupported, Message: msg, StatusCode: status}
}

// FromNetwork classifies a PRE-first-byte network/timeout failure; always
// retryable (§7). CPA skips credential cooldown for transient_transport.
// Failures observed after the first upstream byte must use
// PostFirstByteNetwork instead.
func FromNetwork(err error) *Error {
	msg := ""
	if err != nil {
		msg = Redact(err.Error())
	}
	return &Error{Class: ClassNetwork, Message: msg, Retryable: true, RoutingCode: CodeTransientTransport}
}

// PostFirstByteNetwork classifies a failure seen AFTER the first upstream
// byte: part of the response may already be delivered downstream, so a
// retry would risk duplicate delivery of a half stream — never retryable
// (§7). CPA neither cools the key nor replays a stream_truncated failure.
func PostFirstByteNetwork(msg string) *Error {
	return &Error{Class: ClassNetwork, Message: Redact(msg), StatusCode: 502, RoutingCode: CodeStreamTruncated}
}

// DownstreamLifecycle classifies a failed delivery to the CPA downstream
// stream. CPA skips credential cooldown for connection_lifecycle.
func DownstreamLifecycle(msg string) *Error {
	return &Error{Class: ClassNetwork, Message: Redact(msg), RoutingCode: CodeConnectionLifecycle}
}

// Translation reports an FR-005/FR-006 translation failure; never retryable.
func Translation(msg string) *Error {
	return &Error{Class: ClassTranslation, Message: Redact(msg)}
}

// UpstreamFallback builds a ClassUpstream error for the adapter fallback
// sites (chunk/failure errors) that have no upstream status code. Resolved
// §7 makes upstream server failures retryable; the message is redacted at
// construction.
func UpstreamFallback(msg string) *Error {
	return &Error{Class: ClassUpstream, Message: Redact(msg), Retryable: true}
}

// ToEnvelopeError converts to the host wire type with a redacted
// message. Re-redacting at the envelope edge is INTENTIONAL defense-in-depth:
// constructors already redact (so non-envelope consumers — logs, executor
// fail paths reading .Message directly) are safe, and Redact is idempotent.
func ToEnvelopeError(e *Error) WireError {
	status := e.StatusCode
	if status == 0 {
		switch e.Class {
		case ClassUnsupported, ClassTranslation:
			status = 400
		}
	}
	code := e.RoutingCode
	if code == "" {
		code = string(e.Class)
	}
	return WireError{
		Code:              code,
		Message:           Redact(e.Message),
		Retryable:         e.Retryable,
		HTTPStatus:        status,
		RetryAfterSeconds: e.RetryAfterSeconds,
		CredentialScoped:  e.CredentialScoped,
	}
}

// bearerRe matches a bearer token (case-insensitive scheme, optional
// space/tab separator, token terminated by whitespace, comma, or quote).
// The separator is optional so a bare "bearer" word is redacted exactly as
// the previous hand-rolled scanner did. Redact is idempotent — applying it
// twice yields the same output — which is what makes edge re-redaction safe.
var bearerRe = regexp.MustCompile(`(?i)bearer[ \t]*[^\s,"]*`)

// apiKeyRe finds x-api-key header echoes (Messages-route auth, FR-007):
// case-insensitive name, then a run of quote/colon/equals/space separators,
// then the value run. The value class spans hex/base62 plus the base64
// alphabet ("+", "/", "="), matching bearerRe/queryKeyRe sibling classes.
// The candidate is only replaced when the value looks
// like a key (contains a digit) so ordinary prose after the header name —
// "x-api-key authentication failed" — passes untouched; every real key
// alphabet (hex, base64, base62) carries digits. Idempotent: "[redacted]"
// starts with '[' which is outside the value class, so it never re-matches.
var apiKeyRe = regexp.MustCompile(`(?i)x-api-key["':= ]*[A-Za-z0-9_\-.+/=]+`)

func redactAPIKey(match string) string {
	value := strings.TrimLeft(match[len("x-api-key"):], "\"':= ")
	hasDigit := false
	for _, r := range value {
		if r >= '0' && r <= '9' {
			hasDigit = true
			break
		}
	}
	if !hasDigit || len(value) < 8 {
		return match
	}
	return match[:len(match)-len(value)] + "[redacted]"
}

// queryKeyRe redacts secrets carried in URL query strings (FR-011): transport
// failures conventionally echo the full URL, so base-url/catalog-url keys
// passed as ?api_key=… / ?token=… must not survive into envelopes or logs.
// The value run stops at whitespace, '&', and quotes so consecutive params
// survive; the replacement keeps the parameter name. Idempotent: the
// redacted value "[redacted]" still matches the value class, so re-running
// yields the same text.
var queryKeyRe = regexp.MustCompile(`(?i)([?&](?:api[_-]?key|key|access[_-]?token|token)=)[^\s&"']+`)

// Redact strips bearer tokens, x-api-key echoes, and query-string key
// parameters from a message so keys never reach logs or clients.
func Redact(msg string) string {
	msg = bearerRe.ReplaceAllString(msg, "Bearer [redacted]")
	msg = apiKeyRe.ReplaceAllStringFunc(msg, redactAPIKey)
	return queryKeyRe.ReplaceAllString(msg, "${1}[redacted]")
}
