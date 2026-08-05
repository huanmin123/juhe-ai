// Package gatewayfallbackreason owns the finite Node fallback-reason vocabulary.
// A caller that cannot prove one of these values must not request cross-group
// fallback, because capacity and degradation handling depends on the reason.
package gatewayfallbackreason

import "strings"

type Reason string

const (
	NoCandidateAccounts                         Reason = "no_candidate_accounts"
	RequestCapabilityMismatch                   Reason = "request_capability_mismatch"
	AnthropicNativeGroupOpenAICompatibleRequest Reason = "anthropic_native_group_openai_compatible_request"
	MissingModel                                Reason = "missing_model"
	UnsupportedModel                            Reason = "unsupported_model"
	LocalAccountSuppressed                      Reason = "local_account_suppressed"
	RuntimeDegraded                             Reason = "runtime_degraded"
	AuthorizationQuotaExceeded                  Reason = "authorization_quota_exceeded"
	HighConcurrencyGroupBusy                    Reason = "high_concurrency_group_busy"
	GroupCapacityBusy                           Reason = "group_capacity_busy"
	UpstreamAccountsExhausted                   Reason = "upstream_accounts_exhausted"
	NormalRouteSpeedFirstExhausted              Reason = "normal_route_speed_first_exhausted"
	AccountScopedAgentGuidanceExhausted         Reason = "account_scoped_agent_guidance_exhausted"
	ResponseInspectionServerRetryExhausted      Reason = "response_inspection_server_retry_exhausted"
	UpstreamProtocolServerRetryExhausted        Reason = "upstream_protocol_server_retry_exhausted"
	StreamServerRetryExhausted                  Reason = "stream_server_retry_exhausted"
)

func Parse(value string) (Reason, bool) {
	parsed := Reason(strings.TrimSpace(value))
	switch parsed {
	case NoCandidateAccounts, RequestCapabilityMismatch, AnthropicNativeGroupOpenAICompatibleRequest,
		MissingModel, UnsupportedModel, LocalAccountSuppressed, RuntimeDegraded,
		AuthorizationQuotaExceeded, HighConcurrencyGroupBusy, GroupCapacityBusy,
		UpstreamAccountsExhausted, NormalRouteSpeedFirstExhausted,
		AccountScopedAgentGuidanceExhausted, ResponseInspectionServerRetryExhausted,
		UpstreamProtocolServerRetryExhausted, StreamServerRetryExhausted:
		return parsed, true
	default:
		return "", false
	}
}

func Valid(value string) bool {
	_, ok := Parse(value)
	return ok
}
