package gatewaypreauth

import (
	"context"
	"net/http"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Port of request/authorization-preflight.ts: the API key availability,
// group access and quota rejection matrix. Status codes and copy mirror the
// Node source exactly; the checks delegate to gatewayquota services (G07).

// RejectUnavailableGatewayAPIKey mirrors rejectUnavailableGatewayApiKey;
// rejected=true means the 401 response has been sent.
func (s *Service) RejectUnavailableGatewayAPIKey(input UnavailableAPIKeyInput) bool {
	if !input.APIKeyUnavailable {
		return false
	}
	statusCode := http.StatusUnauthorized
	responsePayload := GatewayErrorPayloadOf("API Key 不可用或已过期", "invalid_api_key")
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
			ErrorPhase:   "authorization",
			ErrorCode:    "invalid_api_key",
			ErrorMessage: responsePayload.Error.Message,
		},
	})
	return true
}

// UnavailableAPIKeyInput mirrors the Node input.
type UnavailableAPIKeyInput struct {
	Req               *GatewayRequest
	Res               GatewayResponseWriter
	AuditCapture      AuditCaptureContext
	UsageContext      GatewayFailureUsageContext
	StartedAt         int64
	APIKeyUnavailable bool
}

// RejectMissingGatewayGroupAccess mirrors rejectMissingGatewayGroupAccess;
// rejected=true means the 403 response has been sent.
func (s *Service) RejectMissingGatewayGroupAccess(input MissingGroupAccessInput) bool {
	if input.GroupAccess != nil {
		return false
	}
	statusCode := http.StatusForbidden
	responsePayload := GatewayErrorPayloadOf("API Key 绑定的分组授权不可用", "forbidden")
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
			ErrorPhase:   "authorization",
			ErrorCode:    "forbidden",
			ErrorMessage: "API Key 绑定的分组授权不可用",
		},
	})
	return true
}

// MissingGroupAccessInput mirrors the Node input.
type MissingGroupAccessInput struct {
	Req          *GatewayRequest
	Res          GatewayResponseWriter
	AuditCapture AuditCaptureContext
	UsageContext GatewayFailureUsageContext
	StartedAt    int64
	GroupAccess  *gatewayruntimecache.GroupUsageAccessMetadata
}

// RejectGatewayAPIKeyQuotaIfExceeded mirrors
// rejectGatewayApiKeyQuotaIfExceeded: the consumed quota check followed by
// the in-flight cost reservation. rejected=true means the 429 response has
// been sent. A check error mirrors the Node throw and is returned.
func (s *Service) RejectGatewayAPIKeyQuotaIfExceeded(ctx context.Context, input APIKeyQuotaInput) (bool, error) {
	if input.APIKeyRecord != nil {
		decision, err := s.APIKeyQuota.CheckAPIKeyQuotaAsync(ctx, gatewayquota.APIKeyRow{
			ID:              input.APIKeyRecord.ID,
			SystemAccountID: input.APIKeyRecord.SystemAccountID,
			QuotaLimitsJSON: derefString(input.APIKeyRecord.QuotaLimitsJSON),
		})
		if err != nil {
			return false, err
		}
		if !decision.Allowed {
			s.sendAPIKeyQuotaExceeded(input.common(), decision.Message, "")
			return true, nil
		}
	}
	if input.APIKeyRecord == nil || input.Req.InflightQuotaReserved {
		return false, nil
	}
	inflightDecision, err := s.InflightQuota.ReserveGatewayCost(ctx, gatewayquota.GatewayReserveInput{
		APIKey: gatewayquota.APIKeyRow{
			ID:              input.APIKeyRecord.ID,
			SystemAccountID: input.APIKeyRecord.SystemAccountID,
			QuotaLimitsJSON: derefString(input.APIKeyRecord.QuotaLimitsJSON),
		},
		ProviderCode: input.UsageContext.ProviderCode,
	})
	if err != nil {
		return false, err
	}
	if !inflightDecision.Allowed {
		s.sendAPIKeyQuotaExceeded(input.common(), APIKeyQuotaExceededMessage, "api_key_inflight_quota_exceeded")
		return true, nil
	}
	if inflightDecision.Reservation != nil {
		input.Req.InflightQuotaReserved = true
		reservation := inflightDecision.Reservation
		if input.Req.HTTP != nil {
			requestCtx := input.Req.HTTP.Context()
			if requestCtx != nil {
				go func() {
					<-requestCtx.Done()
					reservation.Complete()
				}()
			}
		}
	}
	return false, nil
}

// APIKeyQuotaExceededMessage mirrors API_KEY_QUOTA_EXCEEDED_MESSAGE via the
// gatewayquota package constant.
const APIKeyQuotaExceededMessage = gatewayquota.APIKeyQuotaExceededMessage

// APIKeyQuotaInput mirrors the Node input.
type APIKeyQuotaInput struct {
	Req          *GatewayRequest
	Res          GatewayResponseWriter
	AuditCapture AuditCaptureContext
	UsageContext GatewayFailureUsageContext
	StartedAt    int64
	APIKeyRecord *gatewayruntimecache.GatewayAPIKeyRow
}

func (input APIKeyQuotaInput) common() FailureResponseInput {
	return FailureResponseInput{
		Req:          input.Req,
		Res:          input.Res,
		AuditCapture: input.AuditCapture,
		UsageContext: input.UsageContext,
		StartedAt:    input.StartedAt,
	}
}

// sendAPIKeyQuotaExceeded mirrors sendApiKeyQuotaExceeded.
func (s *Service) sendAPIKeyQuotaExceeded(base FailureResponseInput, message string, errorCode string) {
	statusCode := http.StatusTooManyRequests
	if message == "" {
		message = APIKeyQuotaExceededMessage
	}
	if errorCode == "" {
		errorCode = "rate_limit_exceeded"
	}
	responsePayload := GatewayErrorPayloadOf(message, "rate_limit_exceeded")
	base.StatusCode = statusCode
	base.ResponsePayload = responsePayload
	base.Audit = FailureAudit{
		Outcome:      AuditOutcomeGatewayFailed,
		ErrorPhase:   "quota",
		ErrorCode:    errorCode,
		ErrorMessage: responsePayload.Error.Message,
	}
	s.Responses.SendGatewayFailureResponse(base)
}

// RejectGatewayAuthorizationQuotaIfExceeded mirrors
// rejectGatewayAuthorizationQuotaIfExceeded; rejected=true means the 429
// quota response has been sent.
func (s *Service) RejectGatewayAuthorizationQuotaIfExceeded(ctx context.Context, input AuthorizationQuotaInput) (bool, error) {
	groupAuthorizationQuotaDecision, err := s.AuthorizationQuota.CheckAuthorizationQuotaAsync(ctx, gatewayquota.GroupAccessMetadata{
		GroupAuthorizationID:           derefString(input.GroupAccess.GroupAuthorizationID),
		GroupAuthorizationQuotaLimited: derefBool(input.GroupAccess.GroupAuthorizationQuotaLimited),
	}, nil)
	if err != nil {
		return false, err
	}
	if groupAuthorizationQuotaDecision.Allowed {
		return false, nil
	}
	message := groupAuthorizationQuotaDecision.Message
	if message == "" {
		message = AuthorizationQuotaExceededMessage
	}
	s.Responses.SendGatewayFailureResponse(FailureResponseInput{
		Req:             input.Req,
		Res:             input.Res,
		AuditCapture:    input.AuditCapture,
		UsageContext:    input.UsageContext,
		StartedAt:       input.StartedAt,
		StatusCode:      http.StatusTooManyRequests,
		ResponsePayload: GatewayErrorPayloadOf(message, "rate_limit_exceeded"),
		Audit: FailureAudit{
			Outcome:      AuditOutcomeGatewayFailed,
			ErrorPhase:   "quota",
			ErrorCode:    "rate_limit_exceeded",
			ErrorMessage: message,
		},
	})
	return true, nil
}

// AuthorizationQuotaExceededMessage mirrors AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE.
const AuthorizationQuotaExceededMessage = gatewayquota.AuthorizationQuotaExceededMessage

// AuthorizationQuotaInput mirrors the Node input.
type AuthorizationQuotaInput struct {
	Req          *GatewayRequest
	Res          GatewayResponseWriter
	AuditCapture AuditCaptureContext
	UsageContext GatewayFailureUsageContext
	StartedAt    int64
	GroupAccess  *gatewayruntimecache.GroupUsageAccessMetadata
}

func derefBool(value *bool) bool {
	return value != nil && *value
}
