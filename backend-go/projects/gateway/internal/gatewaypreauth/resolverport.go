package gatewaypreauth

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Route resolver port: the normal + hybrid route selection seam. The routing
// cores exist (gatewayrouting.NormalModelRouteService / gatewayhybrid), but
// their Go result projections (gatewayrouting.UpstreamAccount /
// gatewayhybrid.APIKeyRecord) are lossy relative to the preflight contract,
// which carries full runtime accounts and runtime-cache key rows. The
// composition root adapters translate between them; the orchestration freezes
// the Node contract here.

// RouteResolver mirrors resolveNormalGatewayModelRoute +
// resolveHybridGatewayRoute (G08/G09).
type RouteResolver interface {
	ResolveNormalGatewayModelRoute(ctx context.Context, input NormalRouteInput) (NormalRouteResult, error)
	ResolveHybridGatewayRoute(ctx context.Context, input HybridRouteInput) (HybridRouteResult, error)
}

// NormalRouteInput mirrors the resolve input.
type NormalRouteInput struct {
	Req                        *GatewayRequest
	APIKeyRecord               *gatewayruntimecache.GatewayAPIKeyRow
	RequestClientCompatibility string
}

// NormalRouteOutcome mirrors the outcome union.
const (
	NormalRouteOutcomeSkipped  = "skipped"
	NormalRouteOutcomeSelected = "selected"
	NormalRouteOutcomeFailed   = "failed"
)

// NormalRouteResult mirrors NormalGatewayModelRouteResult with the runtime
// account carrier.
type NormalRouteResult struct {
	Outcome              string
	Reason               string
	RequestedModel       string
	StatusCode           int
	Type                 string
	Code                 string
	Message              string
	MatchedProviderCodes []string

	APIKeyRecord        *gatewayruntimecache.GatewayAPIKeyRow
	GroupID             string
	GroupAccess         *gatewayruntimecache.GroupUsageAccessMetadata
	Accounts            []gatewayruntimecache.OpenAIAccountSecret
	RouteSource         string
	MatchedProviderCode string
}

// HybridRouteInput mirrors the resolveHybridGatewayRoute input.
type HybridRouteInput struct {
	Req                        *GatewayRequest
	APIKeyRecord               *gatewayruntimecache.GatewayAPIKeyRow
	TraceID                    string
	ClientIP                   string
	Endpoint                   string
	AuditCapture               AuditCaptureContext
	RequestClientCompatibility string
	Signal                     context.Context
}

// HybridRouteOutcome mirrors the outcome union.
const (
	HybridRouteOutcomeSkipped  = "skipped"
	HybridRouteOutcomeSelected = "selected"
	HybridRouteOutcomeFailed   = "failed"
)

// HybridRouteResult mirrors HybridGatewayRouteResult.
type HybridRouteResult struct {
	Outcome string
	Reason  string

	APIKeyRecord           *gatewayruntimecache.GatewayAPIKeyRow
	GroupID                string
	GroupAccess            *gatewayruntimecache.GroupUsageAccessMetadata
	Accounts               []gatewayruntimecache.OpenAIAccountSecret
	Config                 map[string]any
	Scoring                map[string]any
	Route                  map[string]any
	TargetModel            string
	AffinityApplied        bool
	ScoringFallbackApplied bool
}

// HybridRuntimeRoute mirrors HybridGatewayRuntimeRoute. Config / Scoring /
// Route stay opaque maps; the typed shapes belong to gatewayhybrid (G09).
type HybridRuntimeRoute struct {
	APIKeyRecord           *gatewayruntimecache.GatewayAPIKeyRow
	Config                 map[string]any
	Scoring                map[string]any
	Route                  map[string]any
	TargetModel            string
	AffinityApplied        bool
	ScoringFallbackApplied bool
	QualityRetryCount      int
}

// DownstreamCommitState is the G16 commit-state placeholder: the preflight
// only constructs and forwards it; the stream pipeline owns the semantics.
type DownstreamCommitState struct{}

// affinityBindingAlias keeps the gemini affinity binding type local to the
// contract surface.
type affinityBindingAlias = gatewaygemini.AffinityBinding

// RequestLaneText / RequestLaneImage mirror the lane union for option fields.
const (
	RequestLaneText  = gatewayproto.LaneText
	RequestLaneImage = gatewayproto.LaneImage
)
