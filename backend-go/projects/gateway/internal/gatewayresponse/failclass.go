package gatewayresponse

import (
	"strings"
)

// 失败分类，对齐 upstream-failure-classifier.ts、
// dispatch-exhaustion-classifier.ts 与 model-catalog-cache-policy.ts。

// GatewayUpstreamFailureClass 对齐 GatewayUpstreamFailureClass。
type GatewayUpstreamFailureClass string

const (
	FailureClassOpaqueUpstreamResponse GatewayUpstreamFailureClass = "opaque_upstream_response"
	FailureClassTransport              GatewayUpstreamFailureClass = "transport"
	FailureClassUnknown                GatewayUpstreamFailureClass = "unknown"
)

// GatewayUpstreamFailureMetricReasonClass 对齐指标 reason class union。
type GatewayUpstreamFailureMetricReasonClass string

const (
	MetricReasonQuota         GatewayUpstreamFailureMetricReasonClass = "quota"
	MetricReasonRateLimit     GatewayUpstreamFailureMetricReasonClass = "rate_limit"
	MetricReasonAuthorization GatewayUpstreamFailureMetricReasonClass = "authorization"
	MetricReasonProtocol      GatewayUpstreamFailureMetricReasonClass = "protocol"
	MetricReasonTimeout       GatewayUpstreamFailureMetricReasonClass = "timeout"
	MetricReasonTransport     GatewayUpstreamFailureMetricReasonClass = "transport"
	MetricReasonUpstream5xx   GatewayUpstreamFailureMetricReasonClass = "upstream_5xx"
	MetricReasonUpstream4xx   GatewayUpstreamFailureMetricReasonClass = "upstream_4xx"
	MetricReasonUnknown       GatewayUpstreamFailureMetricReasonClass = "unknown"
)

// GatewayUpstreamFailureClassificationInput 对齐同Node 输入。
type GatewayUpstreamFailureClassificationInput struct {
	Phase      string // 'upstream_request' | 'upstream_response'
	StatusCode *int
	ErrorCode  string
}

// GatewayUpstreamFailureClassification 对齐输出。
type GatewayUpstreamFailureClassification struct {
	FailureClass         GatewayUpstreamFailureClass
	MetricReasonClass    GatewayUpstreamFailureMetricReasonClass
	ClassificationReason string
}

// ClassifyGatewayUpstreamFailure 对齐 classifyGatewayUpstreamFailure。
func ClassifyGatewayUpstreamFailure(input GatewayUpstreamFailureClassificationInput) GatewayUpstreamFailureClassification {
	switch input.Phase {
	case "upstream_request":
		return observation(FailureClassTransport, classifyMetricReason(input), "upstream_transport_failure")
	case "upstream_response":
		return observation(FailureClassOpaqueUpstreamResponse, classifyMetricReason(input), "opaque_upstream_response_failure")
	default:
		return observation(FailureClassUnknown, MetricReasonUnknown, "unknown_failure_phase")
	}
}

func observation(failureClass GatewayUpstreamFailureClass, metric GatewayUpstreamFailureMetricReasonClass, classificationReason string) GatewayUpstreamFailureClassification {
	return GatewayUpstreamFailureClassification{
		FailureClass:         failureClass,
		MetricReasonClass:    metric,
		ClassificationReason: classificationReason,
	}
}

func classifyMetricReason(input GatewayUpstreamFailureClassificationInput) GatewayUpstreamFailureMetricReasonClass {
	errorCode := strings.ToLower(strings.TrimSpace(input.ErrorCode))
	switch errorCode {
	case "insufficient_user_quota", "insufficient_quota", "insufficient_balance",
		"default_group_global_quota_exhausted", "quota_exceeded", "quota_exhausted", "billing_hard_limit_reached":
		return MetricReasonQuota
	case "rate_limit_exceeded", "rate_limited", "too_many_requests":
		return MetricReasonRateLimit
	case "invalid_api_key", "invalid_authentication", "authentication_error", "access_denied", "permission_denied":
		return MetricReasonAuthorization
	case "upstream_protocol_failure", "upstream_protocol_error":
		return MetricReasonProtocol
	case "first_byte_timeout", "normal_route_first_byte_timeout", "etimedout", "timeout":
		return MetricReasonTimeout
	}
	if input.Phase == "upstream_request" {
		return MetricReasonTransport
	}
	if input.StatusCode != nil {
		switch {
		case *input.StatusCode == 429:
			return MetricReasonRateLimit
		case *input.StatusCode == 401 || *input.StatusCode == 403:
			return MetricReasonAuthorization
		case *input.StatusCode >= 500:
			return MetricReasonUpstream5xx
		case *input.StatusCode >= 400:
			return MetricReasonUpstream4xx
		}
	}
	return MetricReasonUnknown
}

// GatewayDispatchExhaustionReason 对齐 union。
type GatewayDispatchExhaustionReason string

const (
	DispatchExhaustionAPIKeyPoolUnavailable      GatewayDispatchExhaustionReason = "api_key_pool_unavailable"
	DispatchExhaustionAllAccountsLocallySuppressed GatewayDispatchExhaustionReason = "all_accounts_locally_suppressed"
	DispatchExhaustionAccountConcurrencyExhausted  GatewayDispatchExhaustionReason = "account_concurrency_exhausted"
	DispatchExhaustionUpstreamHTTPError            GatewayDispatchExhaustionReason = "upstream_http_error"
	DispatchExhaustionUpstreamTransportError       GatewayDispatchExhaustionReason = "upstream_transport_error"
	DispatchExhaustionNoAvailableAccount           GatewayDispatchExhaustionReason = "no_available_account"
)

// GatewayDispatchExhaustionClassification 对齐输出。
type GatewayDispatchExhaustionClassification struct {
	FailureReason  GatewayDispatchExhaustionReason
	UpstreamStatus *int
}

// UpstreamAttemptSummary 是 UpstreamAttempt 中 dispatch-exhaustion 分类消费的
// 最小投影（Node upstream/attempt.ts 的 upstreamUrl / status 字段）。
type UpstreamAttemptSummary struct {
	UpstreamURL string
	Status      *int
}

// ClassifyGatewayDispatchExhaustion 对齐 classifyGatewayDispatchExhaustion。
func ClassifyGatewayDispatchExhaustion(lastAttempt *UpstreamAttemptSummary) GatewayDispatchExhaustionClassification {
	if lastAttempt == nil {
		return GatewayDispatchExhaustionClassification{FailureReason: DispatchExhaustionNoAvailableAccount}
	}
	switch lastAttempt.UpstreamURL {
	case "account:api_key_pool_unavailable":
		return GatewayDispatchExhaustionClassification{FailureReason: DispatchExhaustionAPIKeyPoolUnavailable}
	case "account:locally_suppressed":
		return GatewayDispatchExhaustionClassification{FailureReason: DispatchExhaustionAllAccountsLocallySuppressed}
	case "concurrency:limit":
		return GatewayDispatchExhaustionClassification{FailureReason: DispatchExhaustionAccountConcurrencyExhausted}
	}
	if lastAttempt.Status != nil {
		return GatewayDispatchExhaustionClassification{
			FailureReason:  DispatchExhaustionUpstreamHTTPError,
			UpstreamStatus: lastAttempt.Status,
		}
	}
	return GatewayDispatchExhaustionClassification{FailureReason: DispatchExhaustionUpstreamTransportError}
}

// providerModelCatalogInvalidationReasons 对齐同名 Set。
var providerModelCatalogInvalidationReasons = map[string]bool{
	"custom_provider_model_saved":         true,
	"custom_provider_model_deleted":       true,
	"provider_model_configuration_updated": true,
}

// ShouldInvalidateProviderModelCatalog 对齐 shouldInvalidateProviderModelCatalog。
func ShouldInvalidateProviderModelCatalog(reason string) bool {
	return reason != "" && providerModelCatalogInvalidationReasons[reason]
}
