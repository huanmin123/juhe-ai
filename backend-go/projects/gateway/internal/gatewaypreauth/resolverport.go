package gatewaypreauth

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Route resolver port: the normal route selection seam. The routing core
// exists (gatewayrouting.NormalModelRouteService), but its Go result
// projection (gatewayrouting.UpstreamAccount) is lossy relative to the
// preflight contract, which carries full runtime accounts and runtime-cache
// key rows. The composition root adapters translate between them; the
// orchestration freezes the Node contract here.

// RouteResolver mirrors resolveNormalGatewayModelRoute (G08).
type RouteResolver interface {
	ResolveNormalGatewayModelRoute(ctx context.Context, input NormalRouteInput) (NormalRouteResult, error)
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
