package gatewayusage

import "strings"

// GatewayUpstreamFailureClass mirrors the Node union
// ('opaque_upstream_response' | 'transport' | 'unknown').
type GatewayUpstreamFailureClass = string

// Failure class values (upstream-failure-classifier.ts).
const (
	FailureClassOpaqueUpstreamResponse GatewayUpstreamFailureClass = "opaque_upstream_response"
	FailureClassTransport              GatewayUpstreamFailureClass = "transport"
	FailureClassUnknown                GatewayUpstreamFailureClass = "unknown"
)

// GatewayUpstreamFailureMetricReasonClass mirrors the prometheus metric
// reason union ('quota' | 'rate_limit' | 'authorization' | 'protocol' |
// 'timeout' | 'transport' | 'upstream_5xx' | 'upstream_4xx' | 'unknown').
type GatewayUpstreamFailureMetricReasonClass = string

// Metric reason class values.
const (
	MetricReasonQuota        GatewayUpstreamFailureMetricReasonClass = "quota"
	MetricReasonRateLimit    GatewayUpstreamFailureMetricReasonClass = "rate_limit"
	MetricReasonAuth         GatewayUpstreamFailureMetricReasonClass = "authorization"
	MetricReasonProtocol     GatewayUpstreamFailureMetricReasonClass = "protocol"
	MetricReasonTimeout      GatewayUpstreamFailureMetricReasonClass = "timeout"
	MetricReasonTransport    GatewayUpstreamFailureMetricReasonClass = "transport"
	MetricReasonUpstream5xx  GatewayUpstreamFailureMetricReasonClass = "upstream_5xx"
	MetricReasonUpstream4xx  GatewayUpstreamFailureMetricReasonClass = "upstream_4xx"
	MetricReasonUnknownClass GatewayUpstreamFailureMetricReasonClass = "unknown"
)

// Upstream failure phases.
const (
	FailurePhaseUpstreamRequest  = "upstream_request"
	FailurePhaseUpstreamResponse = "upstream_response"
)

// GatewayUpstreamFailureClassification mirrors
// GatewayUpstreamFailureClassification.
type GatewayUpstreamFailureClassification struct {
	FailureClass         GatewayUpstreamFailureClass
	MetricReasonClass    GatewayUpstreamFailureMetricReasonClass
	ClassificationReason string
}

// GatewayUpstreamFailureClassificationInput mirrors the classifier input.
type GatewayUpstreamFailureClassificationInput struct {
	Phase      string
	StatusCode *int
	ErrorCode  string
}

// ClassifyGatewayUpstreamFailure mirrors classifyGatewayUpstreamFailure.
func ClassifyGatewayUpstreamFailure(input GatewayUpstreamFailureClassificationInput) GatewayUpstreamFailureClassification {
	if input.Phase == FailurePhaseUpstreamRequest {
		return observationFailure(FailureClassTransport, classifyMetricReason(input), "upstream_transport_failure")
	}
	if input.Phase == FailurePhaseUpstreamResponse {
		return observationFailure(FailureClassOpaqueUpstreamResponse, classifyMetricReason(input), "opaque_upstream_response_failure")
	}
	return observationFailure(FailureClassUnknown, MetricReasonUnknownClass, "unknown_failure_phase")
}

func observationFailure(class GatewayUpstreamFailureClass, reason GatewayUpstreamFailureMetricReasonClass, classificationReason string) GatewayUpstreamFailureClassification {
	return GatewayUpstreamFailureClassification{
		FailureClass:         class,
		MetricReasonClass:    reason,
		ClassificationReason: classificationReason,
	}
}

func classifyMetricReason(input GatewayUpstreamFailureClassificationInput) GatewayUpstreamFailureMetricReasonClass {
	errorCode := strings.ToLower(strings.TrimSpace(input.ErrorCode))
	switch errorCode {
	case "insufficient_user_quota", "insufficient_quota", "insufficient_balance",
		"default_group_global_quota_exhausted", "quota_exceeded", "quota_exhausted",
		"billing_hard_limit_reached":
		return MetricReasonQuota
	case "rate_limit_exceeded", "rate_limited", "too_many_requests":
		return MetricReasonRateLimit
	case "invalid_api_key", "invalid_authentication", "authentication_error",
		"access_denied", "permission_denied":
		return MetricReasonAuth
	case "upstream_protocol_failure", "upstream_protocol_error":
		return MetricReasonProtocol
	case "first_byte_timeout", "normal_route_first_byte_timeout", "etimedout", "timeout":
		return MetricReasonTimeout
	}
	if input.Phase == FailurePhaseUpstreamRequest {
		return MetricReasonTransport
	}
	if input.StatusCode != nil {
		switch {
		case *input.StatusCode == 429:
			return MetricReasonRateLimit
		case *input.StatusCode == 401 || *input.StatusCode == 403:
			return MetricReasonAuth
		case *input.StatusCode >= 500:
			return MetricReasonUpstream5xx
		case *input.StatusCode >= 400:
			return MetricReasonUpstream4xx
		}
	}
	return MetricReasonUnknownClass
}
