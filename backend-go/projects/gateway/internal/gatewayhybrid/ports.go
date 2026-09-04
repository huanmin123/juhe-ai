package gatewayhybrid

import (
	"context"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// Ports for the external capabilities hybrid routing depends on. The
// composition root wires concrete adapters (Node parity lives in the
// gateway dispatch/routing slices); tests provide the mocks in this package.

// Clock returns the current time; injected so TTL and timing logic replays.
type Clock func() time.Time

// ClientCompatibilityCapability mirrors ClientCompatibilityCapability
// (domain/types.ts), e.g. 'openai_standard'.
type ClientCompatibilityCapability = string

// GroupUsageAccessMetadata is the opaque per-group access metadata the
// target-group selector port attaches to a selection (mirrors
// GroupUsageAccessMetadata). Hybrid routing passes it through verbatim.
type GroupUsageAccessMetadata struct {
	ProviderCode     string
	SchedulingPolicy string
}

// OpenAIAccountSecret is the opaque dispatch account handle (mirrors
// OpenAIAccountSecret). Only the account id is consumed inside this package.
type OpenAIAccountSecret struct {
	ID string
}

// ResponseInspectionPolicySummary is the opaque response inspection policy
// handle (mirrors ResponseInspectionPolicySummary); passed through verbatim.
type ResponseInspectionPolicySummary struct {
	PolicyID string
}

// APIKeyRecord mirrors the GatewayApiKeyRow fields the hybrid modules read.
type APIKeyRecord struct {
	ID                   string
	SystemAccountID      string
	RouteStrategyMode    string // 'hybrid_smart' enables hybrid routing
	SelectedGroupID      string // current selected_group_id (mutated copies)
	HybridRoutingConfig  *routestrategies.HybridRoutingConfig
}

// TargetGroupSelection mirrors selectGatewayModelTargetGroup's success shape.
type TargetGroupSelection struct {
	GroupID                    string
	GroupAccess                GroupUsageAccessMetadata
	Accounts                   []OpenAIAccountSecret
	ResponseInspectionPolicies []ResponseInspectionPolicySummary
}

// TargetGroupSelector mirrors selectGatewayModelTargetGroup composed with
// orderGatewayApiKeyGroupBindingsForDispatchAsync (binding ordering belongs
// to the selector implementation).
type TargetGroupSelector interface {
	SelectTargetGroup(ctx context.Context, input TargetGroupSelectorInput) (*TargetGroupSelection, error)
}

type TargetGroupSelectorInput struct {
	View                     *GatewayRequestView
	APIKeyRecord             APIKeyRecord
	TargetModel              string
	RequestClientCompatibility ClientCompatibilityCapability
}

// AuxiliaryTrafficSourceHybridScoring / AuxiliaryTrafficSourceHybridQualityScoring
// mirror the HybridAuxiliaryTrafficSource values.
const (
	AuxiliaryTrafficSourceHybridScoring        = "hybrid_scoring"
	AuxiliaryTrafficSourceHybridQualityScoring = "hybrid_quality_scoring"
)

// AuxiliaryDispatchInput mirrors dispatchHybridAuxiliaryChatCompletion input.
type AuxiliaryDispatchInput struct {
	// Body is the synthesized chat-completions request body
	// (createHybridScoringGatewayRequest / createHybridQualityGatewayRequest).
	Body *OrderedJSON
	// RawBody is serializeGatewayJsonObject(Body).
	RawBody                    []byte
	APIKeyRecord               APIKeyRecord
	TargetModel                string
	TraceID                    string
	ClientIP                   string
	Endpoint                   string
	TrafficSource              string
	TimeoutMs                  int
	ResponseMaxBytes           int
	NoAccountErrorCode         string
	NoAccountErrorMessage      string
	DispatchErrorCode          string
	DispatchErrorMessage       string
	HTTPErrorCode              string // optional httpErrorCode
	ResponseTooLargeMessage    string
	RequestClientCompatibility ClientCompatibilityCapability
}

// AuxiliaryDispatchFinishInput mirrors HybridAuxiliaryDispatchFinishInput.
type AuxiliaryDispatchFinishInput struct {
	Success      bool
	ErrorCode    string
	ErrorMessage string
}

// AuxiliaryDispatchSuccess mirrors the success arm of
// HybridAuxiliaryDispatchResult. Finish must be invoked exactly once.
type AuxiliaryDispatchSuccess struct {
	Account               OpenAIAccountSecret
	GroupID               string
	StatusCode            int
	ResponseBody          []byte
	ResponseBodyText      string
	ResponseBodyTruncated bool
	ParsedResponseBody    NonStreamJSONBody
	Usage                 gatewayproto.ParsedUsage
	Finish                func(ctx context.Context, finish AuxiliaryDispatchFinishInput) error
}

// AuxiliaryDispatchFailure mirrors the failed arm of
// HybridAuxiliaryDispatchResult.
type AuxiliaryDispatchFailure struct {
	ErrorCode        string
	ErrorMessage     string
	Account          *OpenAIAccountSecret
	GroupID          string
	HasGroupID       bool
	StatusCode       int
	HasStatusCode    bool
	ShouldRecordUsage bool
}

// AuxiliaryDispatcher ports dispatchHybridAuxiliaryChatCompletion: select the
// auxiliary target group, prepare accounts, call the upstream once and settle
// audit/hot-quality side effects via the returned Finish callback.
type AuxiliaryDispatcher interface {
	DispatchHybridAuxiliaryChatCompletion(ctx context.Context, input AuxiliaryDispatchInput) (AuxiliaryDispatchSuccess, *AuxiliaryDispatchFailure)
}

// ScoringAttemptSnapshot mirrors the requestSnapshot/responseSnapshot fields.
type ScoringRequestSnapshot struct {
	Model        string
	ContextBytes int
}

type ScoringResponseSnapshot struct {
	StatusCode *int
	Body       string // response body snippet (failure paths)
	Parsed     any    // parsed scoring/quality payload (success path)
}

// ScoringAttemptRecord mirrors recordHybridScoringAttempt input.
type ScoringAttemptRecord struct {
	TraceID          string
	ClientIP         string
	SystemAccountID  string
	APIKeyID         string
	GroupID          string
	Account          *OpenAIAccountSecret
	Endpoint         string
	StatusCode       *int
	Success          bool
	StartedAt        time.Time
	ScoringModel     string
	Usage            gatewayproto.ParsedUsage
	ErrorCode        string
	ErrorMessage     string
	RequestSnapshot  ScoringRequestSnapshot
	ResponseSnapshot ScoringResponseSnapshot
	TrafficSource    string // optional; quality inspection passes hybrid_quality_scoring
}

// UsageRecorder ports recordHybridScoringAttempt.
type UsageRecorder interface {
	RecordHybridScoringAttempt(ctx context.Context, record ScoringAttemptRecord) error
}

// SharedJSONCache ports the Redis-backed shared JSON cache
// (createSharedJsonCache). A nil implementation means the memory-only cache
// driver (runtimeConfig.cacheDriver !== 'redis').
type SharedJSONCache interface {
	Get(ctx context.Context, key string) (*HybridScoringCacheEntry, error)
	Set(ctx context.Context, key string, entry HybridScoringCacheEntry, ttlMs int64) error
	Clear(ctx context.Context) error
}

// RuntimeStateStore ports the runtime state store used for Redis-backed
// affinity bindings (createRuntimeStateStore). A nil implementation means
// the memory runtime-state driver.
type RuntimeStateStore interface {
	GetJSON(ctx context.Context, key string, value any) (bool, error)
	SetJSON(ctx context.Context, key string, value any, ttlMs int64) error
}

// AffinityKeyScope mirrors GatewaySessionAffinityKeyScope for the derivation.
type AffinityKeyScope struct {
	SystemAccountID string
	APIKeyID        string
	RouteStrategyID string
	GroupID         string
}

// SessionIdentityPort resolves the request session identity and derives the
// affinity key (mirrors getGatewaySessionIdentity +
// deriveGatewaySessionAffinityKey, HMAC secret handled by the adapter).
// Empty result means no identity / no conversation key.
type SessionIdentityPort interface {
	HybridRouteAffinityKey(view *GatewayRequestView, scope AffinityKeyScope) string
}

// AuditMetadataCapture ports AuditCaptureContext.addGatewayMetadata.
type AuditMetadataCapture interface {
	AddGatewayMetadata(label string, metadata *OrderedJSON)
}

// RouteDiagnosticsPublisher ports the
// channel('juhe-ai:hybrid-route-decision') publish.
type RouteDiagnosticsPublisher interface {
	PublishHybridRouteDecision(metadata *OrderedJSON)
}

// RequestBodyGateway ports the mutable request body used by
// rewriteHybridRequestModel (request/body.ts ownership).
type RequestBodyGateway interface {
	// ReplaceModel mirrors replaceGatewayJsonBodyModel(req, model): false when
	// there is no mutable JSON object body or the model trims to empty.
	ReplaceModel(targetModel string) bool
	// HasRawBody mirrors `request.rawBody?.length` truthiness.
	HasRawBody() bool
	// ParseRawBody mirrors parseGatewayRequestJsonBody: the parsed JSON value
	// (object/array/string/number/bool/null), or an error when unparseable.
	ParseRawBody(ctx context.Context) (any, error)
	// ReplaceModelWithParsed mirrors replaceGatewayJsonBodyModel(req, model,
	// parsed): false when parsed is not a JSON object.
	ReplaceModelWithParsed(targetModel string, parsed *OrderedJSON) bool
}
