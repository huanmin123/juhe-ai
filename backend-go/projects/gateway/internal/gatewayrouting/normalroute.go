package gatewayrouting

import (
	"context"
	"fmt"
)

// Normal gateway model route outcomes (normal-model-route.service.ts).
const (
	NormalRouteOutcomeSelected = "selected"
	NormalRouteOutcomeSkipped  = "skipped"
	NormalRouteOutcomeFailed   = "failed"
)

// Skip reasons emitted by resolveNormalGatewayModelRoute.
const (
	SkipReasonMissingRequestedModel = "missing_requested_model"
	SkipReasonEmptyBinding          = "empty_binding"
)

// Failure codes emitted by resolveNormalGatewayModelRoute.
const (
	// FailCodeModelNotFound 是 models 单表目录语义的终态错误（设计 §6.1）：
	// 请求模型没有一条对当前资源可见且 status=active 的 models 目录行
	// （不存在、不可见、已禁用或已下线均属此类）→ 404 model_not_found。
	FailCodeModelNotFound = "model_not_found"

	// model_not_routable_for_api_key / model_route_unavailable /
	// model_target_group_not_bound / model_route_ambiguous 已随供应商解耦退役
	// （§6.1：迁移完成后不得再用供应商目录判定）：供应商目录收窄时代的 400
	// 分类由 model_not_found（404）与 model_target_group_unavailable（503）
	// 二分取代；单表目录无多供应商歧义。路由不再发出这些错误码。
	FailCodeModelTargetGroupUnavailable = "model_target_group_unavailable"
)

// NormalRouteGroupSegment carries one member group's contribution to a super
// target's expanded candidate pool (超级分组设计 §4.2; empty for standard
// targets).
type NormalRouteGroupSegment struct {
	GroupID     string
	GroupAccess GroupUsageAccessMetadata
	Accounts    []UpstreamAccount
}

// NormalGatewayModelRouteResult mirrors the Node
// NormalGatewayModelRouteResult union: Outcome picks the active variant and
// only the matching fields carry data.
type NormalGatewayModelRouteResult struct {
	Outcome string

	// skipped
	Reason string

	// requestedModel: present for selected/failed and every skipped reason
	// except missing_requested_model (Node omits it there).
	RequestedModel string

	// failed
	StatusCode int
	Type       string
	Code       string
	Message    string

	// selected
	APIKeyRecord               *APIKeyRow
	GroupID                    string
	GroupAccess                GroupUsageAccessMetadata
	Accounts                   []UpstreamAccount
	ResponseInspectionPolicies []ResponseInspectionPolicySummary
	RouteSource                NormalGatewayModelRouteSource
	// GroupSegments holds the per-member-group fragments behind a super
	// target's expanded pool; filled only for super targets (GroupKind=super),
	// nil for standard targets. GroupID stays the super group (route_group).
	GroupSegments []NormalRouteGroupSegment
}

// ResolveNormalGatewayModelRouteInput mirrors
// ResolveNormalGatewayModelRouteInput.
type ResolveNormalGatewayModelRouteInput struct {
	Request                    RequestView
	APIKeyRecord               *APIKeyRow
	RequestClientCompatibility string
}

// NormalModelRouteService mirrors normal-model-route.service.ts
// resolveNormalGatewayModelRoute: resolve the target group for the API key's
// strategy, or produce the exact skip/failure contract.
type NormalModelRouteService struct {
	TargetGroups *TargetGroupSelector
}

// NewNormalModelRouteService wires the service onto a target group selector.
func NewNormalModelRouteService(runtimeCache RuntimeCacheReader, capabilityFilter AccountCapabilityFilter) *NormalModelRouteService {
	return &NormalModelRouteService{
		TargetGroups: &TargetGroupSelector{
			RuntimeCache:     runtimeCache,
			CapabilityFilter: capabilityFilter,
		},
	}
}

// ResolveNormalGatewayModelRoute mirrors resolveNormalGatewayModelRoute. 供应
// 商解耦后的 normal 语义：对 API Key 绑定的全部分组直接做候选过滤（无单
// provider 短路、无目录 provider 收窄），无候选时按 models 单表目录做终态二
// 分（§6.1）：目录无可见 active 行 → 404 model_not_found；目录可见但分组内
// 无候选 → 503 model_target_group_unavailable。
func (s *NormalModelRouteService) ResolveNormalGatewayModelRoute(ctx context.Context, input ResolveNormalGatewayModelRouteInput) (NormalGatewayModelRouteResult, error) {
	apiKeyRecord := input.APIKeyRecord

	requestedModel := trimSpace(input.Request.requestModel())
	if requestedModel == "" {
		return NormalGatewayModelRouteResult{Outcome: NormalRouteOutcomeSkipped, Reason: SkipReasonMissingRequestedModel}, nil
	}

	bindings := activeGatewayAPIKeyGroupBindings(apiKeyRecord)
	if len(bindings) == 0 {
		return NormalGatewayModelRouteResult{Outcome: NormalRouteOutcomeSkipped, Reason: SkipReasonEmptyBinding, RequestedModel: requestedModel}, nil
	}

	// 超级分组目标（超级分组设计 §4.2）：目标选择器已按成员组展开候选并
	// 携带 GroupSegments，此处直接透传；标准目标的片段为空。
	mappingTarget, err := s.TargetGroups.SelectGatewayModelTargetGroup(ctx, ModelTargetGroupInput{
		Request:                    input.Request,
		APIKeyRecord:               apiKeyRecord,
		Bindings:                   bindings,
		TargetModel:                requestedModel,
		RequestClientCompatibility: input.RequestClientCompatibility,
	})
	if err != nil {
		return NormalGatewayModelRouteResult{}, err
	}
	if mappingTarget != nil {
		routeSource := NormalGatewayModelRouteSourceOf(mappingTarget.ModelFilter)
		// 供应商解耦后不再按 ProviderCode 收窄：随行绑定收窄为选中分组的绑定。
		selectedGroupBindings := make([]GroupBindingRow, 0)
		for _, candidate := range bindings {
			if candidate.GroupID == mappingTarget.GroupID {
				selectedGroupBindings = append(selectedGroupBindings, candidate)
			}
		}
		updatedRecord := *apiKeyRecord
		updatedRecord.SelectedGroupID = mappingTarget.GroupID
		updatedRecord.GroupBindings = selectedGroupBindings
		return NormalGatewayModelRouteResult{
			Outcome:                    NormalRouteOutcomeSelected,
			APIKeyRecord:               &updatedRecord,
			GroupID:                    mappingTarget.GroupID,
			GroupAccess:                mappingTarget.GroupAccess,
			Accounts:                   mappingTarget.Accounts,
			ResponseInspectionPolicies: mappingTarget.ResponseInspectionPolicies,
			RequestedModel:             requestedModel,
			RouteSource:                routeSource,
			GroupSegments:              mappingTarget.GroupSegments,
		}, nil
	}

	return s.resolveModelsDirectoryTerminalFailure(ctx, requestedModel, apiKeyRecord.SystemAccountID)
}

// resolveModelsDirectoryTerminalFailure 是无候选时的终态二分（设计 §6.1）：
// 请求模型在 models 目录无对当前资源可见（builtin/global 全可见、personal 属
// 主精确匹配，owner = apiKeyRecord.SystemAccountID）的 active 行 → 404
// model_not_found（不得用绑定账户改变该分类）；目录可见但分组内硬过滤后无候
// 选 → 维持 503 model_target_group_unavailable。
func (s *NormalModelRouteService) resolveModelsDirectoryTerminalFailure(ctx context.Context, requestedModel, systemAccountID string) (NormalGatewayModelRouteResult, error) {
	present, err := s.requestedModelInModelsDirectory(ctx, requestedModel, systemAccountID)
	if err != nil {
		return NormalGatewayModelRouteResult{}, err
	}
	if present {
		return NormalGatewayModelRouteResult{
			Outcome:        NormalRouteOutcomeFailed,
			StatusCode:     503,
			Type:           "service_unavailable",
			Code:           FailCodeModelTargetGroupUnavailable,
			Message:        fmt.Sprintf("请求模型当前没有可用账号：%s", requestedModel),
			RequestedModel: requestedModel,
		}, nil
	}
	return NormalGatewayModelRouteResult{
		Outcome:        NormalRouteOutcomeFailed,
		StatusCode:     404,
		Type:           "invalid_request_error",
		Code:           FailCodeModelNotFound,
		Message:        fmt.Sprintf("请求模型不存在或不可用：%s", requestedModel),
		RequestedModel: requestedModel,
	}, nil
}

// requestedModelInModelsDirectory 通过 runtime cache 的 models 单表目录判定
// 请求模型是否对当前资源可见（Outcome matched = 存在对 systemAccountID 资源
// 账户可见的 active 目录行，systemAccountID 即请求 owner）。供应商解耦后不
// 再按绑定 provider 收窄目录判定（设计 §8）。
func (s *NormalModelRouteService) requestedModelInModelsDirectory(ctx context.Context, requestedModel, systemAccountID string) (bool, error) {
	route, err := s.TargetGroups.RuntimeCache.ResolveCachedProviderModelRouteAsync(ctx, ProviderModelRouteInput{
		Model:           requestedModel,
		SystemAccountID: systemAccountID,
		IncludeUnpriced: true,
	})
	if err != nil {
		return false, err
	}
	// models 语义下目录只会返回 matched/missing；非 missing 一律按目录可见
	// 处理（终态 503），不得把异常 outcome 降级成对客户端的 404。
	return route.Outcome != ProviderModelRouteMissing, nil
}

// NormalGatewayModelTargetPriority 过渡保留（导出符号，cmd 与测试仍引用）：
// catalogProviderMatched 维度随供应商目录拆除后不再被路由调用链消费，direct >
// mapping 的序仍是候选排序的既有语义。
func NormalGatewayModelTargetPriority(modelFilter GatewayModelAccountFilterResult, catalogProviderMatched bool) float64 {
	if modelFilter.DirectMatchedCount > 0 {
		return 2
	}
	if modelFilter.MappingMatchedCount > 0 {
		return 1
	}
	if catalogProviderMatched {
		return 0
	}
	return negInf()
}

// NormalGatewayModelRouteSourceOf mirrors normalGatewayModelRouteSource.
func NormalGatewayModelRouteSourceOf(modelFilter GatewayModelAccountFilterResult) NormalGatewayModelRouteSource {
	if modelFilter.DirectMatchedCount > 0 {
		return RouteSourceCatalogProvider
	}
	return RouteSourceAccountMapping
}

// activeGatewayAPIKeyGroupBindings mirrors activeGatewayApiKeyGroupBindings:
// status === 'active' only (group_enabled is NOT consulted here — that
// filter belongs to the dispatch ordering path).
func activeGatewayAPIKeyGroupBindings(apiKeyRecord *APIKeyRow) []GroupBindingRow {
	bindings := make([]GroupBindingRow, 0, len(apiKeyRecord.GroupBindings))
	for _, binding := range apiKeyRecord.GroupBindings {
		if binding.Status != RowStatusActive {
			continue
		}
		bindings = append(bindings, binding)
	}
	return bindings
}
