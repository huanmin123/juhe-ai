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
	Outcome        string
	Reason         string
	RequestedModel string
	StatusCode     int
	Type           string
	Code           string
	Message        string

	APIKeyRecord *gatewayruntimecache.GatewayAPIKeyRow
	GroupID      string
	GroupAccess  *gatewayruntimecache.GroupUsageAccessMetadata
	Accounts     []gatewayruntimecache.OpenAIAccountSecret
	RouteSource  string
	// TargetOutcomes 是超级分组外层目标的失败分类（设计 §4.2）：仅在超级
	// 分组解析器实际尝试过的目标上填充，用于审计与验收断言。
	TargetOutcomes []NormalRouteTargetOutcome
	// MemberSkips 是最终选中超级分组目标的成员跳过原因（设计 §4.2）：
	// 成功路径也要写入审计（如某成员被成员行停用/配额拒绝，另一成员承接）。
	MemberSkips []NormalRouteMemberSkip
}

// NormalRouteTargetOutcome 是外层目标级失败分类；MemberSkips 是该目标被跳过
// 的成员及其原因（设计 §4.2 的成员级投影）。
type NormalRouteTargetOutcome struct {
	GroupID        string
	Classification string
	MemberSkips    []NormalRouteMemberSkip
}

// NormalRouteMemberSkip 是单成员跳过原因（取值域：member_disabled /
// no_candidate / capacity_exhausted / upstream_exhausted）。
type NormalRouteMemberSkip struct {
	GroupID string
	Reason  string
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
