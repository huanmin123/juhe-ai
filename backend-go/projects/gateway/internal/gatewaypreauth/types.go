package gatewaypreauth

import (
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Shared contracts of the preflight orchestration: the decision unions of the
// runtime guard services (Node runtime/client-ip-error-circuit.service.ts,
// client-ip-policy-cache.service.ts, user-request-limit-counter.ts,
// authenticated-models-rate-limit.service.ts), the usage context payload
// (usage/records.ts) and the audit capture surface (audit/capture.service.ts).
// The concrete implementations belong to later slices; the shapes are frozen
// here so the orchestration order and copy stay testable.

// CircuitDecision mirrors GatewayCircuitDecision.
type CircuitDecision struct {
	Blocked           bool
	Reason            string
	RetryAfterSeconds *int64
	BlockedUntilMs    *int64
	FailureCount      *int64
}

// PreAuthFailureReason mirrors GatewayPreAuthFailureReason.
type PreAuthFailureReason string

const (
	PreAuthFailureMissingBearerToken PreAuthFailureReason = "missing_bearer_token"
	PreAuthFailureInvalidAPIKey      PreAuthFailureReason = "invalid_api_key"
)

// ClientIPErrorCircuitReason mirrors GatewayClientIpErrorCircuitReason.
type ClientIPErrorCircuitReason string

const (
	ClientIPErrorCircuitInvalidJSON              ClientIPErrorCircuitReason = "invalid_json"
	ClientIPErrorCircuitAdapterRequestValidation ClientIPErrorCircuitReason = "adapter_request_validation"
)

// PreAuthCircuitInput mirrors GatewayPreAuthCircuitInput.
type PreAuthCircuitInput struct {
	ClientIP      string
	Authorization string
}

// PreAuthFailureInput mirrors GatewayPreAuthFailureInput.
type PreAuthFailureInput struct {
	ClientIP      string
	Authorization string
	Reason        PreAuthFailureReason
}

// ClientIPErrorCircuitInput mirrors GatewayClientIpErrorCircuitInput.
type ClientIPErrorCircuitInput struct {
	SystemAccountID string
	GroupID         string
	APIKeyID        string
	ClientIP        string
	Endpoint        string
}

// ClientIPErrorCircuitSampleInput mirrors GatewayClientIpErrorCircuitSampleInput.
type ClientIPErrorCircuitSampleInput struct {
	SystemAccountID string
	GroupID         string
	APIKeyID        string
	ClientIP        string
	Endpoint        string
	Reason          ClientIPErrorCircuitReason
	Signature       string
}

// BlacklistPolicy mirrors the ActiveClientIpPolicy fields the pre-auth
// blacklist response consumes.
type BlacklistPolicy struct {
	ID             string
	IPHash         string
	Reason         string
	ClientIP       string
	AggregateIPKey string
}

// NormalizedClientIP mirrors normalizeClientIpForStats output consumed here.
type NormalizedClientIP struct {
	ClientIP       string
	AggregateIPKey string
}

// ClientIPPolicyDecision mirrors ClientIpPolicyDecision.
type ClientIPPolicyDecision struct {
	Blocked         bool
	Allowlisted     bool
	NormalizedIP    *NormalizedClientIP
	BlacklistPolicy *BlacklistPolicy
}

// UserRequestLimitWindow mirrors the window union.
type UserRequestLimitWindow string

const (
	UserRequestLimitPerMinute UserRequestLimitWindow = "perMinute"
	UserRequestLimitPerDay    UserRequestLimitWindow = "perDay"
	UserRequestLimitPerWeek   UserRequestLimitWindow = "perWeek"
	UserRequestLimitPerMonth  UserRequestLimitWindow = "perMonth"
)

// UserRequestLimitDecision mirrors UserRequestLimitDecision.
type UserRequestLimitDecision struct {
	Allowed           bool
	Window            UserRequestLimitWindow
	Limit             *int64
	RetryAfterSeconds *int64
}

// AuthenticatedModelsRateLimitDecision mirrors the Node union.
type AuthenticatedModelsRateLimitDecision struct {
	Allowed           bool
	Scope             string // 'api_key_ip' | 'api_key'
	Limit             *int64
	RetryAfterSeconds *int64
	Unavailable       bool
}

// OpenAIGatewayTrafficSource mirrors OpenAIGatewayTrafficSource; the gateway
// value is the default.
const TrafficSourceGateway = "gateway"

// UsageServiceTier mirrors UsageServiceTier values relevant to the snapshot.
type UsageServiceTier = string

// UsageRequestSnapshot mirrors UsageRequestSnapshot for the fields the usage
// context builder reads; the full capture stays with the audit/usage slice.
type UsageRequestSnapshot struct {
	Method                   string
	Path                     string
	OriginalURL              string
	ClientIP                 string
	TraceID                  string
	RequestedServiceTier     string
	RequestedReasoningEffort string
}

// GatewayFailureUsageContext mirrors GatewayFailureUsageContext
// (usage/records.ts).
type GatewayFailureUsageContext struct {
	TraceID                        string
	TrafficSource                  string
	ClientIP                       string
	SystemAccountID                string
	APIKeyID                       string
	GroupID                        string
	Endpoint                       string
	RequestSnapshot                UsageRequestSnapshot
	RequestedServiceTier           string
	EffectiveServiceTier           string
	RequestedReasoningEffort       string
	EffectiveReasoningEffort       string
	ProviderCode                   string
	ProviderProtocolProfileID      string
	ProtocolCode                   string
	ProtocolVersion                string
	GroupOwnerSystemAccountID      string
	GroupAccessType                string
	GroupAuthorizationID           string
	GroupAuthorizationSourceType   string
	GroupAuthorizationSourceTeamID string
}

// OpenAIGatewayRequestIdentity mirrors OpenAIGatewayRequestIdentity.
type OpenAIGatewayRequestIdentity struct {
	SystemAccountID string
	GroupID         string
	APIKeyID        string
}

// GroupUsageMetadata mirrors groupUsageMetadata(groupAccess): the usage
// context fields derived from the group access metadata.
func GroupUsageMetadata(groupAccess gatewayruntimecache.GroupUsageAccessMetadata) GroupUsageMetadataFields {
	return GroupUsageMetadataFields{
		ProviderCode:                   groupAccess.ProviderCode,
		GroupOwnerSystemAccountID:      groupAccess.GroupOwnerSystemAccountID,
		GroupAccessType:                groupAccess.GroupAccessType,
		GroupAuthorizationID:           derefString(groupAccess.GroupAuthorizationID),
		GroupAuthorizationSourceType:   derefString(groupAccess.GroupAuthorizationSourceType),
		GroupAuthorizationSourceTeamID: derefString(groupAccess.GroupAuthorizationSourceTeamID),
	}
}

// GroupUsageMetadataFields mirrors the returned pick.
type GroupUsageMetadataFields struct {
	ProviderCode                   string
	GroupOwnerSystemAccountID      string
	GroupAccessType                string
	GroupAuthorizationID           string
	GroupAuthorizationSourceType   string
	GroupAuthorizationSourceTeamID string
}

// AuditGatewayContext mirrors AuditGatewayContext: the bound identity fields.
type AuditGatewayContext struct {
	SystemAccountID   string
	APIKeyID          string
	GroupID           string
	ProviderCode      string
	TrafficSource     string
	SessionID         string
	SessionClientType string
	ConversationKey   string
}

// AuditCaptureContext mirrors the consumed AuditCaptureContext surface
// (audit/capture.service.ts, G17).
type AuditCaptureContext interface {
	// BindContext mirrors bindContext.
	BindContext(context AuditGatewayContext)
	// AddGatewayMetadata mirrors addGatewayMetadata({label, metadata}).
	AddGatewayMetadata(label string, metadata map[string]any)
	// Finalize mirrors finalize(input).
	Finalize(input AuditFinalizeInput)
}

// AuditFinalizeInput mirrors FinalizeAuditInput.
type AuditFinalizeInput struct {
	Outcome          string
	Success          bool
	StatusCode       int
	ResponseHeaders  map[string]any
	ResponseBody     string
	ResponsePartType string
	ErrorPhase       string
	ErrorCode        string
	ErrorMessage     string
}

// Audit outcome / phase unions mirror the Node string unions.
const (
	AuditOutcomeGatewayFailed = "gateway_failed"
	AuditOutcomeSuccess       = "success"

	AuditPartGatewayError    = "gateway_error"
	AuditPartGatewayResponse = "gateway_response"
)

// DroppedAuditCapture mirrors dispatchDroppedAuditCapture's input plus the
// finalized audit envelope fields the dispatcher receives.
type DroppedAuditCapture struct {
	TraceID       string
	TrafficSource string
	AuditOutcome  string
	Success       bool
	Bytes         int
	Reason        string
	Method        string
	Path          string
	QueryString   string
	StatusCode    int
	ErrorPhase    string
	ErrorCode     string
	ErrorMessage  string
	ClientIP      string
	UserAgent     string
}

// Clock injects time; tests use a fixed clock.
type Clock interface {
	Now() time.Time
}

// SystemClock is the default wall clock.
type SystemClock struct{}

// Now implements Clock.
func (SystemClock) Now() time.Time { return time.Now() }

// Logger mirrors the consumed logger.warn surface.
type Logger interface {
	Warn(event string, fields map[string]any, message string)
}

// Observability mirrors shared/request-context.ts surface consumed here.
type Observability interface {
	// Logger returns the request-bound logger (getRequestLogger).
	Logger() Logger
	// TraceID returns the current request trace id.
	TraceID() string
	// CreateTraceID mirrors createTraceId.
	CreateTraceID() string
	// SanitizeURLForLog mirrors sanitizeUrlForLog.
	SanitizeURLForLog(value string) string
	// LogRequestStage mirrors logRequestStage(stage, fields, outcome, startedAt).
	LogRequestStage(stage string, fields map[string]any, outcome string, startedAt time.Time)
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func int64Ptr(value int64) *int64 { return &value }
