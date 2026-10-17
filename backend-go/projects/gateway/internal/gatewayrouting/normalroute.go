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

	// SkipReasonSingleProvider 过渡保留（cmd 测试与 statreads 仍引用常量）：
	// 单 provider 短路已随供应商解耦拆除（供应商与上游协议面重构设计 §7/§8），
	// 路由不再发出该原因。
	SkipReasonSingleProvider = "single_provider"
)

// Failure codes emitted by resolveNormalGatewayModelRoute.
const (
	// FailCodeModelNotFound 是 models 单表目录语义的终态错误（设计 §6.1）：
	// 请求模型没有一条对当前资源可见且 status=active 的 models 目录行
	// （不存在、不可见、已禁用或已下线均属此类）→ 404 model_not_found。
	FailCodeModelNotFound = "model_not_found"

	// 以下错误码随供应商解耦退役，常量过渡保留（消费方仍引用字符串语义），
	// 路由不再发出：
	//   - model_not_routable_for_api_key / model_route_unavailable /
	//     model_target_group_not_bound：供应商目录收窄时代的 400 分类，由
	//     model_not_found（404）与 model_target_group_unavailable（503）二分
	//     取代（§6.1：迁移完成后不得再用供应商目录判定）。
	//   - model_route_ambiguous：单表目录无多供应商歧义，不再发出。
	FailCodeModelNotRoutableForAPIKey   = "model_not_routable_for_api_key"
	FailCodeModelRouteAmbiguous         = "model_route_ambiguous"
	FailCodeModelRouteUnavailable       = "model_route_unavailable"
	FailCodeModelTargetGroupNotBound    = "model_target_group_not_bound"
	FailCodeModelTargetGroupUnavailable = "model_target_group_unavailable"
)

// NormalRouteGroupSegment carries one binding group's contribution to a
// merged route pool (merge mode only; empty for other modes).
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
	// MatchedProviderCodes 过渡字段（供应商解耦后恒空）：字段与 audit 消费方
	// （gatewaypreauth preflight）保留，赋值点已随供应商目录拆除，待组合根切
	// 片一并移除。
	MatchedProviderCodes []string

	// selected
	APIKeyRecord               *APIKeyRow
	GroupID                    string
	GroupAccess                GroupUsageAccessMetadata
	Accounts                   []UpstreamAccount
	ResponseInspectionPolicies []ResponseInspectionPolicySummary
	RouteSource                NormalGatewayModelRouteSource
	// MatchedProviderCode 过渡字段（供应商解耦后恒空串）：字段与 cmd
	// chain_routing / gatewaypreauth audit 消费方保留，赋值点已随供应商目录
	// 拆除，待组合根切片一并移除。
	MatchedProviderCode string
	// GroupSegments holds the per-binding-group fragments behind the merged
	// flat pool; filled only when RouteSource == RouteSourceMerged (merge
	// mode), nil for every other mode. GroupID is the window group (the
	// first non-empty segment's group, 合并路由设计 3.4).
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

	// Merge mode (合并路由设计 3.1/B9-B13): collect every bound group's
	// surviving fragment into one flat pool.
	if apiKeyRecord.RouteStrategyMode == RouteStrategyModeMerge {
		return s.resolveMergeGatewayModelRoute(ctx, input, apiKeyRecord, bindings, requestedModel)
	}

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
			MatchedProviderCode:        "",
		}, nil
	}

	return s.resolveModelsDirectoryTerminalFailure(ctx, bindings, requestedModel, apiKeyRecord.SystemAccountID)
}

// resolveModelsDirectoryTerminalFailure 是无候选时的终态二分（设计 §6.1）：
// 请求模型在 models 目录无对当前资源可见（builtin/global 全可见、personal 属
// 主精确匹配，owner = apiKeyRecord.SystemAccountID）的 active 行 → 404
// model_not_found（不得用绑定账户改变该分类）；目录可见但分组内硬过滤后无候
// 选 → 维持 503 model_target_group_unavailable。
func (s *NormalModelRouteService) resolveModelsDirectoryTerminalFailure(ctx context.Context, bindings []GroupBindingRow, requestedModel, systemAccountID string) (NormalGatewayModelRouteResult, error) {
	present, err := s.requestedModelInModelsDirectory(ctx, bindings, requestedModel, systemAccountID)
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
// 账户可见的 active 目录行，systemAccountID 即请求 owner）。入参的
// ProviderCodes 是供应商收窄时代的过渡载体（解耦后恒空），仅随输入形状传递。
func (s *NormalModelRouteService) requestedModelInModelsDirectory(ctx context.Context, bindings []GroupBindingRow, requestedModel, systemAccountID string) (bool, error) {
	providerCodes := make([]string, 0, len(bindings))
	seen := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		if _, ok := seen[binding.ProviderCode]; ok {
			continue
		}
		seen[binding.ProviderCode] = struct{}{}
		providerCodes = append(providerCodes, binding.ProviderCode)
	}
	route, err := s.TargetGroups.RuntimeCache.ResolveCachedProviderModelRouteAsync(ctx, ProviderModelRouteInput{
		Model:           requestedModel,
		ProviderCodes:   providerCodes,
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

// resolveMergeGatewayModelRoute implements the merge-mode branch of
// ResolveNormalGatewayModelRoute (合并路由设计 3.1): every active+enabled
// binding group contributes its surviving account fragment, fragments join
// one flat pool in binding order, and the pool is dispatched as a whole.
// B9: no single-provider shortcut. B11: no model_route_ambiguous, no single
// provider narrowing (GroupBindings stay full), RouteSource=merged,
// MatchedProviderCode empty. B12: duplicate accounts dedupe by binding
// priority, the first group wins. B13: the models directory is consulted only
// to classify the all-fragments-empty failure (404/503 二分).
func (s *NormalModelRouteService) resolveMergeGatewayModelRoute(ctx context.Context, input ResolveNormalGatewayModelRouteInput, apiKeyRecord *APIKeyRow, bindings []GroupBindingRow, requestedModel string) (NormalGatewayModelRouteResult, error) {
	// Defensive only (the runtime cache SQL already loads enabled groups):
	// merge consults enabled bindings exclusively.
	enabled := make([]GroupBindingRow, 0, len(bindings))
	for _, binding := range bindings {
		if binding.GroupEnabled == 0 {
			continue
		}
		enabled = append(enabled, binding)
	}
	if len(enabled) == 0 {
		return NormalGatewayModelRouteResult{Outcome: NormalRouteOutcomeSkipped, Reason: SkipReasonEmptyBinding, RequestedModel: requestedModel}, nil
	}

	candidates, err := s.TargetGroups.CollectGatewayModelGroupSegments(ctx, ModelTargetGroupInput{
		Request:                    input.Request,
		APIKeyRecord:               apiKeyRecord,
		Bindings:                   enabled,
		TargetModel:                requestedModel,
		RequestClientCompatibility: input.RequestClientCompatibility,
	})
	if err != nil {
		return NormalGatewayModelRouteResult{}, err
	}

	segments := make([]NormalRouteGroupSegment, 0, len(candidates))
	accounts := make([]UpstreamAccount, 0)
	seenAccounts := make(map[string]struct{})
	for _, candidate := range candidates {
		fragment := make([]UpstreamAccount, 0, len(candidate.Accounts))
		for _, account := range candidate.Accounts {
			if _, ok := seenAccounts[account.ID]; ok {
				// B12: the same account bound to several groups joins the
				// pool through its highest-priority (first-seen) group.
				continue
			}
			seenAccounts[account.ID] = struct{}{}
			fragment = append(fragment, account)
		}
		if len(fragment) == 0 {
			continue
		}
		segments = append(segments, NormalRouteGroupSegment{
			GroupID:     candidate.Binding.GroupID,
			GroupAccess: candidate.GroupAccess,
			Accounts:    fragment,
		})
		accounts = append(accounts, fragment...)
	}
	if len(segments) == 0 {
		return s.resolveMergeEmptyPoolFailure(ctx, enabled, requestedModel, apiKeyRecord.SystemAccountID)
	}

	updatedRecord := *apiKeyRecord
	updatedRecord.SelectedGroupID = segments[0].GroupID
	return NormalGatewayModelRouteResult{
		Outcome:                    NormalRouteOutcomeSelected,
		APIKeyRecord:               &updatedRecord,
		GroupID:                    segments[0].GroupID,
		GroupAccess:                segments[0].GroupAccess,
		Accounts:                   accounts,
		ResponseInspectionPolicies: []ResponseInspectionPolicySummary{},
		RequestedModel:             requestedModel,
		RouteSource:                RouteSourceMerged,
		GroupSegments:              segments,
	}, nil
}

// resolveMergeEmptyPoolFailure classifies the all-fragments-empty merge
// failure against the models 单表目录 (B13, 设计 §6.1 终态二分)：目录存在对当
// 前资源可见的 active 行（builtin/global 全可见、personal 属主匹配）→ 绑定分
// 组只是无可用账号（model_target_group_unavailable，503）；目录无可见 active
// 行 → 404 model_not_found。供应商目录的 model_target_group_not_bound 分类随
// 供应商解耦退役，model_route_ambiguous 在 merge 中从不发出（B11）。
func (s *NormalModelRouteService) resolveMergeEmptyPoolFailure(ctx context.Context, bindings []GroupBindingRow, requestedModel, systemAccountID string) (NormalGatewayModelRouteResult, error) {
	return s.resolveModelsDirectoryTerminalFailure(ctx, bindings, requestedModel, systemAccountID)
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
