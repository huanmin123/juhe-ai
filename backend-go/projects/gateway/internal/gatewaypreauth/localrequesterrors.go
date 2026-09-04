package gatewaypreauth

import (
	"context"
	"net/http"
)

// Port of request/local-request-errors.ts: the local invalid-JSON failure
// response and the client-ip error circuit sampling hook.

// InvalidJSONGatewayResponse mirrors sendInvalidJsonGatewayResponse.
func (s *Service) SendInvalidJSONGatewayResponse(ctx context.Context, input InvalidJSONResponseInput) {
	statusCode := http.StatusBadRequest
	responsePayload := GatewayErrorPayloadOf("请求体不是合法 JSON", "invalid_request_error")
	s.RecordClientIPRequestErrorSample(ctx, input.AuditCapture, ClientIPErrorSampleInput{
		SystemAccountID: input.SystemAccountID,
		APIKeyID:        input.APIKeyID,
		GroupID:         input.GroupID,
		ClientIP:        input.ClientIP,
		Endpoint:        input.Endpoint,
		Reason:          ClientIPErrorCircuitInvalidJSON,
		Signature:       "invalid_json",
	})
	s.Responses.SendGatewayFailureResponse(FailureResponseInput{
		Req:             input.Req,
		Res:             input.Res,
		AuditCapture:    input.AuditCapture,
		UsageContext:    input.UsageContext,
		StartedAt:       input.StartedAt,
		StatusCode:      statusCode,
		ResponsePayload: responsePayload,
		Audit: FailureAudit{
			Outcome:      AuditOutcomeGatewayFailed,
			ErrorPhase:   "request_validation",
			ErrorCode:    "invalid_json",
			ErrorMessage: responsePayload.Error.Message,
		},
	})
}

// InvalidJSONResponseInput mirrors sendInvalidJsonGatewayResponse's input.
type InvalidJSONResponseInput struct {
	Req             *GatewayRequest
	Res             GatewayResponseWriter
	AuditCapture    AuditCaptureContext
	UsageContext    GatewayFailureUsageContext
	StartedAt       int64
	SystemAccountID string
	APIKeyID        string
	GroupID         string
	ClientIP        string
	Endpoint        string
}

// ClientIPErrorSampleInput mirrors recordClientIpRequestErrorSample's input.
type ClientIPErrorSampleInput struct {
	SystemAccountID string
	APIKeyID        string
	GroupID         string
	ClientIP        string
	Endpoint        string
	Reason          ClientIPErrorCircuitReason
	Signature       string
}

// RecordClientIPRequestErrorSample mirrors recordClientIpRequestErrorSample:
// record the sample and surface the opened-circuit diagnostics when the
// sample crosses the threshold.
func (s *Service) RecordClientIPRequestErrorSample(ctx context.Context, auditCapture AuditCaptureContext, input ClientIPErrorSampleInput) {
	result, err := s.Circuits.RecordClientIPErrorCircuitSample(ctx, ClientIPErrorCircuitSampleInput{
		SystemAccountID: input.SystemAccountID,
		APIKeyID:        input.APIKeyID,
		GroupID:         input.GroupID,
		ClientIP:        input.ClientIP,
		Endpoint:        input.Endpoint,
		Reason:          input.Reason,
		Signature:       input.Signature,
	})
	if err != nil || !result.Blocked {
		return
	}
	s.Observability.Logger().Warn("gateway_client_ip_error_circuit_opened", map[string]any{
		"reason":            string(input.Reason),
		"retryAfterSeconds": result.RetryAfterSeconds,
		"failureCount":      result.FailureCount,
		"systemAccountId":   input.SystemAccountID,
		"apiKeyId":          input.APIKeyID,
		"groupId":           input.GroupID,
		"clientIp":          input.ClientIP,
	}, "客户端 IP 级错误熔断已打开")
	auditCapture.AddGatewayMetadata("client_ip_error_circuit", map[string]any{
		"opened":            true,
		"reason":            string(input.Reason),
		"retryAfterSeconds": result.RetryAfterSeconds,
		"failureCount":      result.FailureCount,
	})
}
