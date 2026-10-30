package main

// G20 phase-2 composition-root adapter: the gatewaypreauth.RouteResolver port
// (resolverport.go) bridged onto the frozen routing core
// gatewayrouting.NormalModelRouteService (G08).
//
// Node authority:
//   - request/preflight.ts resolveNormalGatewayModelRoute call sites,
//   - normal-model-route.service.ts.
//
// The routing core returns a lossy projection (gatewayrouting.UpstreamAccount)
// while the preflight contract carries full runtime accounts
// (gatewayruntimecache.OpenAIAccountSecret, credentials included) and
// runtime-cache key rows. The adapter translates between the two vocabularies
// and re-hydrates the full accounts / group access from the gateway runtime
// cache, exactly like the Node selector-backed call sites.

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// chainRoutingCache adapts *gatewayruntimecache.Service to the
// gatewayrouting.RuntimeCacheReader port. gatewayrouting cannot import the
// runtime cache directly (its read models return its own projections), so the
// composition root owns this bridge.
type chainRoutingCache struct {
	cache *gatewayruntimecache.Service
}

func (c chainRoutingCache) ResolveCachedGroupUsageAccessMetadataAsync(ctx context.Context, groupID, systemAccountID string) (gatewayrouting.GroupUsageAccessMetadata, bool, error) {
	meta, err := c.cache.ResolveCachedGroupUsageAccessMetadataAsync(ctx, groupID, systemAccountID)
	if err != nil {
		return gatewayrouting.GroupUsageAccessMetadata{}, false, err
	}
	if meta == nil {
		return gatewayrouting.GroupUsageAccessMetadata{}, false, nil
	}
	return projectGroupAccessForRouting(*meta), true, nil
}

func (c chainRoutingCache) ListCachedOpenAIAccountsForGroupAsync(ctx context.Context, groupID, systemAccountID string, options gatewayrouting.CachedAccountsForGroupOptions) ([]gatewayrouting.UpstreamAccount, error) {
	accounts, err := c.cache.ListCachedOpenAIAccountsForGroupAsync(ctx, groupID, systemAccountID, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions{
		RequestedModel:          options.RequestedModel,
		RequestedEndpointFamily: options.RequestedEndpointFamily,
	})
	if err != nil {
		return nil, err
	}
	projected := make([]gatewayrouting.UpstreamAccount, 0, len(accounts))
	for _, account := range accounts {
		projected = append(projected, projectAccountForRouting(account))
	}
	return projected, nil
}

func (c chainRoutingCache) ResolveCachedProviderModelRouteAsync(ctx context.Context, input gatewayrouting.ProviderModelRouteInput) (gatewayrouting.ProviderModelRouteResolution, error) {
	route, err := c.cache.ResolveCachedProviderModelRouteAsync(ctx, input.Model, input.SystemAccountID, input.IncludeUnpriced)
	if err != nil {
		return gatewayrouting.ProviderModelRouteResolution{}, err
	}
	return gatewayrouting.ProviderModelRouteResolution{
		Outcome:  string(route.Outcome),
		ModelKey: route.ModelKey,
	}, nil
}

// projectGroupAccessForRouting projects the runtime-cache group access into
// the routing-layer projection (string fields only; the pointers collapse to
// their rendered values exactly like the Node selector rows).
func projectGroupAccessForRouting(meta gatewayruntimecache.GroupUsageAccessMetadata) gatewayrouting.GroupUsageAccessMetadata {
	return gatewayrouting.GroupUsageAccessMetadata{
		GroupOwnerSystemAccountID:      meta.GroupOwnerSystemAccountID,
		GroupAccessType:                meta.GroupAccessType,
		GroupType:                      deref(meta.GroupType),
		SchedulingPolicy:               renderSchedulingPolicy(meta.SchedulingPolicy),
		GroupAuthorizationID:           deref(meta.GroupAuthorizationID),
		GroupAuthorizationExpiresAt:    deref(meta.GroupAuthorizationExpiresAt),
		GroupAuthorizationQuotaLimited: meta.GroupAuthorizationQuotaLimited,
		GroupAuthorizationSourceType:   deref(meta.GroupAuthorizationSourceType),
		GroupAuthorizationSourceTeamID: deref(meta.GroupAuthorizationSourceTeamID),
	}
}

// projectGroupAccessForRuntime is the inverse projection used when the
// re-hydration read fails: the lossy routing metadata is carried forward so
// the request keeps its identity fields instead of losing the group scope.
func projectGroupAccessForRuntime(meta gatewayrouting.GroupUsageAccessMetadata) gatewayruntimecache.GroupUsageAccessMetadata {
	out := gatewayruntimecache.GroupUsageAccessMetadata{
		GroupOwnerSystemAccountID:      meta.GroupOwnerSystemAccountID,
		GroupAccessType:                meta.GroupAccessType,
		GroupAuthorizationID:           stringPtr(meta.GroupAuthorizationID),
		GroupAuthorizationSourceType:   stringPtr(meta.GroupAuthorizationSourceType),
		GroupAuthorizationSourceTeamID: stringPtr(meta.GroupAuthorizationSourceTeamID),
	}
	if meta.GroupType != "" {
		out.GroupType = &meta.GroupType
	}
	if meta.SchedulingPolicy != "" {
		policy := gatewayruntimecache.GroupSchedulingPolicy{}
		if err := json.Unmarshal([]byte(meta.SchedulingPolicy), &policy); err == nil {
			out.SchedulingPolicy = &policy
		} else {
			out.SchedulingPolicy = &gatewayruntimecache.GroupSchedulingPolicy{"schedulingPreference": meta.SchedulingPolicy}
		}
	}
	return out
}

// projectAccountForRouting projects the full runtime account into the
// routing-layer view. ModelMappings are carried over（超级分组/普通目标共用）：
// routing 层模型过滤（gatewayrouting.FilterAccountsByRequestedModel）需要映
// 射行才能承认“映射来源模型”账号——丢行会把映射账号静默过滤出候选池；
// dispatch driver 的权威映射解析不受影响。
//
// 范围边界说明：gatewayrouting 的映射行类型（gatewayAccountModelMapping）与
// 其构造函数未导出，组合根无法以类型字面量构造该行——此处以受控反射按导出
// 字段名填充（字段签名由 cmd 端到端跨组映射测试锁定）。gatewayrouting 导出
// 映射行构造函数后，应替换为直接投影。
func projectAccountForRouting(account gatewayruntimecache.OpenAIAccountSecret) gatewayrouting.UpstreamAccount {
	projected := gatewayrouting.UpstreamAccount{
		ID: account.ID,
		// B4 W2（设计 §8：行为键=vendor）：routing 层 ProviderCode 槽位承载
		// vendor 身份（值域同名）；本投影取 VendorCode（B4②：secret 侧
		// ProviderCode 冗余槽位已退役，连接身份唯一承载于 VendorCode）。
		ProviderCode:              account.VendorCode,
		ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		ProtocolCode:              account.ProtocolCode,
		ProtocolVersion:           account.ProtocolVersion,
		SupportedModels:           append([]string(nil), account.SupportedModels...),
	}
	appendRoutingModelMappings(&projected, account)
	return projected
}

// appendRoutingModelMappings 把 secret.ModelMappings 反射投影到 routing 层
// UpstreamAccount.ModelMappings。字段缺失或类型漂移时静默返回（路由层退回
// “仅直配模型”语义），不改变请求结果形状。
func appendRoutingModelMappings(account *gatewayrouting.UpstreamAccount, secret gatewayruntimecache.OpenAIAccountSecret) {
	if len(secret.ModelMappings) == 0 {
		return
	}
	field := reflect.ValueOf(account).Elem().FieldByName("ModelMappings")
	if !field.IsValid() || field.Kind() != reflect.Slice || !field.CanSet() {
		return
	}
	rowType := field.Type().Elem()
	for _, mapping := range secret.ModelMappings {
		row := reflect.New(rowType).Elem()
		enabled := mapping.Enabled
		if !setRoutingMappingField(row, "SourceModel", mapping.SourceModel) ||
			!setRoutingMappingField(row, "SourceEndpointFamily", mapping.SourceEndpointFamily) ||
			!setRoutingMappingField(row, "UpstreamModel", mapping.UpstreamModel) ||
			!setRoutingMappingField(row, "UpstreamEndpointFamily", mapping.UpstreamEndpointFamily) {
			return
		}
		enabledField := row.FieldByName("Enabled")
		if enabledField.IsValid() && enabledField.Kind() == reflect.Pointer && enabledField.CanSet() {
			enabledField.Set(reflect.ValueOf(&enabled))
		}
		field.Set(reflect.Append(field, row))
	}
}

// setRoutingMappingField 设置映射行的导出 string 字段；字段不存在或类型漂
// 移返回 false（调用方放弃整行投影）。
func setRoutingMappingField(row reflect.Value, name, value string) bool {
	field := row.FieldByName(name)
	if !field.IsValid() || field.Kind() != reflect.String || !field.CanSet() {
		return false
	}
	field.SetString(value)
	return true
}

// chainCapabilityFilter is the routing-side capability probe. The routing
// projection drops clientCompatibility and credentials, so the authoritative
// capability gate runs in the dispatch driver
// (chainProviderDriver.AccountSupportsGatewayRequest) over the full accounts;
// the routing layer keeps a pass-through filter so group selection is not
// silently emptied by the projection loss. Node behaves the same way in
// effect: selectGatewayModelTargetGroup re-verifies capability per dispatch
// account in prepareGatewayUpstreamAccount.
type chainCapabilityFilter struct{}

func (chainCapabilityFilter) FilterAccountsByRequestCapability(_ context.Context, accounts []gatewayrouting.UpstreamAccount, _ gatewayrouting.CapabilityFilterInput) gatewayrouting.CapabilityFilterResult {
	return gatewayrouting.CapabilityFilterResult{Accounts: accounts}
}

// chainRouteResolver implements gatewaypreauth.RouteResolver.
type chainRouteResolver struct {
	cache  *gatewayruntimecache.Service
	normal *gatewayrouting.NormalModelRouteService
	// quota 承载超级分组解析期逐片段配额批查（权威门）。与 dispatch 窗口
	// 门同一底层服务（chainDispatchQuota）；nil（部分组合测试零值）时片段
	// 配额剔除不生效，生产组合根 fail-fast 要求必装配。
	quota gatewaydispatch.AuthorizationQuotaChecker
}

// ResolveNormalGatewayModelRoute mirrors resolveNormalGatewayModelRoute with
// the G05 projection: skipped/failed reasons stay verbatim; the selected
// variant re-hydrates full runtime accounts + group access so the dispatch
// candidate pipeline receives credential-carrying accounts.
func (r *chainRouteResolver) ResolveNormalGatewayModelRoute(ctx context.Context, input gatewaypreauth.NormalRouteInput) (gatewaypreauth.NormalRouteResult, error) {
	if r.normal == nil {
		return gatewaypreauth.NormalRouteResult{}, fmt.Errorf("RouteResolver 适配器缺少 gatewayrouting.NormalModelRouteService")
	}
	view := routingRequestView(input.Req, input.APIKeyRecord)
	result, err := r.normal.ResolveNormalGatewayModelRoute(ctx, gatewayrouting.ResolveNormalGatewayModelRouteInput{
		Request:                    view,
		APIKeyRecord:               projectAPIKeyRowForRouting(input.APIKeyRecord),
		RequestClientCompatibility: input.RequestClientCompatibility,
	})
	if err != nil {
		return gatewaypreauth.NormalRouteResult{}, err
	}
	out := gatewaypreauth.NormalRouteResult{
		Outcome:        result.Outcome,
		Reason:         result.Reason,
		RequestedModel: result.RequestedModel,
		StatusCode:     result.StatusCode,
		Type:           result.Type,
		Code:           result.Code,
		Message:        result.Message,
		APIKeyRecord:   input.APIKeyRecord,
		RouteSource:    string(result.RouteSource),
	}
	if result.Outcome != gatewayrouting.NormalRouteOutcomeSelected {
		return out, nil
	}
	// selected: carry the selector's updated row (SelectedGroupID +
	// provider-filtered bindings) over the full runtime row.
	if result.APIKeyRecord != nil {
		updated := *input.APIKeyRecord
		updated.SelectedGroupID = result.APIKeyRecord.SelectedGroupID
		updated.GroupBindings = projectBindingsForRuntime(result.APIKeyRecord.GroupBindings, input.APIKeyRecord.GroupBindings)
		out.APIKeyRecord = &updated
	}
	// 选中目标之前评估为空的超级分组绑定记 no_candidate（设计 §4.2）：
	// 外层终态聚合与审计都按全体已尝试目标计算。
	if len(result.SkippedSuperGroupIDs) > 0 {
		out.TargetOutcomes = skippedSuperTargetOutcomes(result.SkippedSuperGroupIDs)
	}
	// 超级分组目标（超级分组设计 §4.2）：候选已按成员展开并携带成员片段，
	// 逐片段重水化 + 成员级配额批查，窗口组保留超级分组本身
	// （route_group），账号组标为实际成员分组（member_group）。GroupSegments
	// 只有超级分组目标会填充；目标身份按解析结果的 GroupID 判定，不能取
	// 加载行的 SelectedGroupID（failover 外层目标可能已轮转到其他绑定）。
	if len(result.GroupSegments) > 0 && isSuperGroupRouteTarget(input.APIKeyRecord, result.GroupID) {
		return r.resolveSuperGroupSelectedRoute(ctx, input, view, result, out)
	}
	out.GroupID = result.GroupID
	out.GroupAccess = r.rehydrateGroupAccess(ctx, result.GroupID, input.APIKeyRecord.SystemAccountID, result.GroupAccess)
	out.Accounts, out.RouteSource = r.rehydrateAccounts(
		ctx, result.GroupID, input.APIKeyRecord.SystemAccountID, result.Accounts,
		result.RequestedModel, localEndpointFamily(view), out.RouteSource,
	)
	return out, nil
}

// isSuperGroupRouteTarget 判定解析结果的目标分组是否为超级分组（绑定行
// groupKind）。使用解析结果的 GroupID 而不是加载行的 SelectedGroupID：
// failover/weighted 等外层模式在解析前可能已轮转/重排选中组。
func isSuperGroupRouteTarget(apiKey *gatewayruntimecache.GatewayAPIKeyRow, groupID string) bool {
	if apiKey == nil || groupID == "" {
		return false
	}
	for _, binding := range apiKey.GroupBindings {
		if binding.GroupID == groupID && binding.GroupKind == "super" {
			return true
		}
	}
	return false
}

// resolveSuperGroupSelectedRoute 兑现超级分组目标的组合根语义（超级分组设计
// §4.2/§4.3）：
//  1. 逐成员片段重水化，账号显式组标 BoundGroupID = 成员分组（member_group）；
//  2. 逐片段成员级授权配额批查，被否决账号剔除；
//  3. 窗口组保持超级分组本身（route_group_id），不随成员切换；
//  4. 全部成员被剔空的目标记 capacity_exhausted，并沿一次性目标快照继续尝试
//     快照内其余 active 绑定目标（含普通分组，设计 §4.2「沿一次性目标快照
//     继续下一个目标」）；
//  5. 快照耗尽时按固定优先级聚合终态：任一 capacity → 429
//     rate_limit_exceeded；全部 no_candidate → 503 model_target_group_unavailable；
//     其余 → 503 upstream_candidates_exhausted。
func (r *chainRouteResolver) resolveSuperGroupSelectedRoute(
	ctx context.Context,
	input gatewaypreauth.NormalRouteInput,
	view gatewayrouting.RequestView,
	result gatewayrouting.NormalGatewayModelRouteResult,
	out gatewaypreauth.NormalRouteResult,
) (gatewaypreauth.NormalRouteResult, error) {
	systemAccountID := input.APIKeyRecord.SystemAccountID
	endpointFamily := localEndpointFamily(view)
	selectedBinding, _ := bindingForGroup(input.APIKeyRecord, result.GroupID)

	accounts, memberSkips, err := r.superGroupSegmentAccounts(ctx, systemAccountID, endpointFamily, selectedBinding, result)
	if err != nil {
		return gatewaypreauth.NormalRouteResult{}, err
	}
	if len(accounts) > 0 {
		out.GroupID = result.GroupID
		out.GroupAccess = r.rehydrateGroupAccess(ctx, result.GroupID, systemAccountID, result.GroupAccess)
		out.Accounts = accounts
		out.MemberSkips = memberSkips
		if out.APIKeyRecord != nil {
			out.APIKeyRecord.SelectedGroupID = result.GroupID
			// 绑定行必须与选中目标一致：superTargetMemberIDs / 窗口配额跳过 /
			// 候选与可恢复 loader 都按绑定行的 GroupKind+MemberGroupIDs 扇出。
			if selectedBinding, ok := bindingForGroup(input.APIKeyRecord, result.GroupID); ok {
				out.APIKeyRecord.GroupBindings = []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{selectedBinding}
			}
		}
		return out, nil
	}
	// 当前超级分组目标全部成员被剔除：按成员原因聚合目标分类（任一成员
	// capacity → capacity_exhausted；否则 no_candidate），继续外层快照。
	classification := "no_candidate"
	for _, skip := range memberSkips {
		if skip.Reason == "capacity_exhausted" {
			classification = "capacity_exhausted"
			break
		}
	}
	outcomes := append([]gatewaypreauth.NormalRouteTargetOutcome(nil), out.TargetOutcomes...)
	outcomes = append(outcomes, gatewaypreauth.NormalRouteTargetOutcome{
		GroupID: result.GroupID, Classification: classification, MemberSkips: memberSkips,
	})
	// 目标快照 = 绑定序中当前目标之后的全部 active 绑定（普通分组与超级分组
	// 一视同仁）；成员不提升为外层目标，同组不重复尝试。
	seen := map[string]struct{}{result.GroupID: {}}
	for _, target := range r.orderedFallbackTargets(result.GroupID, input.APIKeyRecord) {
		if _, dup := seen[target.GroupID]; dup {
			continue
		}
		seen[target.GroupID] = struct{}{}
		narrowed := *input.APIKeyRecord
		narrowed.GroupBindings = []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{target}
		narrowed.SelectedGroupID = target.GroupID
		nextResult, resolveErr := r.normal.ResolveNormalGatewayModelRoute(ctx, gatewayrouting.ResolveNormalGatewayModelRouteInput{
			Request:                    routingRequestView(input.Req, &narrowed),
			APIKeyRecord:               projectAPIKeyRowForRouting(&narrowed),
			RequestClientCompatibility: input.RequestClientCompatibility,
		})
		if resolveErr != nil {
			return gatewaypreauth.NormalRouteResult{}, resolveErr
		}
		if nextResult.Outcome != gatewayrouting.NormalRouteOutcomeSelected {
			classification := "no_candidate"
			if nextResult.Code == "rate_limit_exceeded" {
				classification = "capacity_exhausted"
			}
			outcomes = append(outcomes, gatewaypreauth.NormalRouteTargetOutcome{
				GroupID: target.GroupID, Classification: classification,
			})
			continue
		}
		for _, skippedGroupID := range nextResult.SkippedSuperGroupIDs {
			outcomes = append(outcomes, gatewaypreauth.NormalRouteTargetOutcome{
				GroupID: skippedGroupID, Classification: "no_candidate",
			})
		}
		var nextAccounts []gatewayruntimecache.OpenAIAccountSecret
		var nextSkips []gatewaypreauth.NormalRouteMemberSkip
		nextRouteSource := string(nextResult.RouteSource)
		if len(nextResult.GroupSegments) > 0 {
			// 后续超级分组目标：同样逐成员展开 + 成员级配额批查。
			expanded, skips, expandErr := r.superGroupSegmentAccounts(ctx, systemAccountID, endpointFamily, target, nextResult)
			if expandErr != nil {
				return gatewaypreauth.NormalRouteResult{}, expandErr
			}
			nextAccounts = expanded
			nextSkips = skips
		} else {
			// 普通分组目标：账号无组标，窗口组即该分组。
			nextAccounts, nextRouteSource = r.rehydrateAccounts(
				ctx, target.GroupID, systemAccountID, nextResult.Accounts,
				nextResult.RequestedModel, endpointFamily, nextRouteSource,
			)
		}
		if len(nextAccounts) == 0 {
			outcomes = append(outcomes, gatewaypreauth.NormalRouteTargetOutcome{
				GroupID: target.GroupID, Classification: "capacity_exhausted",
			})
			continue
		}
		selected := *input.APIKeyRecord
		selected.SelectedGroupID = target.GroupID
		selected.GroupBindings = []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{target}
		out.Outcome = gatewaypreauth.NormalRouteOutcomeSelected
		out.APIKeyRecord = &selected
		out.GroupID = target.GroupID
		out.GroupAccess = r.rehydrateGroupAccess(ctx, target.GroupID, systemAccountID, nextResult.GroupAccess)
		out.Accounts = nextAccounts
		out.RouteSource = nextRouteSource
		out.MemberSkips = nextSkips
		out.TargetOutcomes = outcomes
		return out, nil
	}
	return superTargetExhaustedResult(result.RequestedModel, input.APIKeyRecord, outcomes), nil
}

// orderedFallbackTargets 返回绑定序中当前目标之后的 active 绑定；current 之前
// 的绑定不进入本请求快照（一次性目标快照语义，避免回切已尝试目标）。
func (r *chainRouteResolver) orderedFallbackTargets(currentGroupID string, record *gatewayruntimecache.GatewayAPIKeyRow) []gatewayruntimecache.GatewayAPIKeyGroupBindingRow {
	if record == nil {
		return nil
	}
	index := -1
	for i, binding := range record.GroupBindings {
		if binding.GroupID == currentGroupID {
			index = i
			break
		}
	}
	if index < 0 {
		return nil
	}
	targets := make([]gatewayruntimecache.GatewayAPIKeyGroupBindingRow, 0, len(record.GroupBindings))
	for _, binding := range record.GroupBindings[index+1:] {
		if binding.Status != "active" || binding.GroupEnabled == 0 {
			continue
		}
		targets = append(targets, binding)
	}
	return targets
}

// skippedSuperTargetOutcomes 把选中目标前评估为空的超级分组绑定投影为
// no_candidate 目标分类（设计 §4.2），按绑定序去重。
func skippedSuperTargetOutcomes(groupIDs []string) []gatewaypreauth.NormalRouteTargetOutcome {
	outcomes := make([]gatewaypreauth.NormalRouteTargetOutcome, 0, len(groupIDs))
	seen := make(map[string]struct{}, len(groupIDs))
	for _, groupID := range groupIDs {
		if groupID == "" {
			continue
		}
		if _, dup := seen[groupID]; dup {
			continue
		}
		seen[groupID] = struct{}{}
		outcomes = append(outcomes, gatewaypreauth.NormalRouteTargetOutcome{
			GroupID: groupID, Classification: "no_candidate",
		})
	}
	return outcomes
}

// superTargetExhaustedResult 按设计 §4.2 的固定优先级构造外层终态：
// 任一 capacity → 429；全部 no_candidate → 503 model_target_group_unavailable；
// 其余 → 503 upstream_candidates_exhausted。
func superTargetExhaustedResult(requestedModel string, record *gatewayruntimecache.GatewayAPIKeyRow, outcomes []gatewaypreauth.NormalRouteTargetOutcome) gatewaypreauth.NormalRouteResult {
	hasCapacity := false
	allNoCandidate := len(outcomes) > 0
	for _, outcome := range outcomes {
		switch outcome.Classification {
		case "capacity_exhausted":
			hasCapacity = true
			allNoCandidate = false
		case "no_candidate":
		default:
			allNoCandidate = false
		}
	}
	base := gatewaypreauth.NormalRouteResult{
		Outcome:        gatewaypreauth.NormalRouteOutcomeFailed,
		RequestedModel: requestedModel,
		APIKeyRecord:   record,
		TargetOutcomes: outcomes,
	}
	switch {
	case hasCapacity:
		base.StatusCode = 429
		base.Type = "rate_limit_exceeded"
		base.Code = "rate_limit_exceeded"
		base.Message = gatewayquota.AuthorizationQuotaExceededMessage
	case allNoCandidate:
		base.StatusCode = 503
		base.Type = "service_unavailable"
		base.Code = "model_target_group_unavailable"
		base.Message = "请求模型当前没有可用账号：" + requestedModel
	default:
		base.StatusCode = 503
		base.Type = "service_unavailable"
		base.Code = "upstream_candidates_exhausted"
		base.Message = "上游候选已耗尽，请重试"
	}
	return base
}

// superGroupSegmentAccounts 展开一个超级分组目标的成员片段并执行成员级配额
// 批查；被否决账号剔除，返回幸存账号（全局 route_rank 序）与逐成员跳过原因
// （设计 §4.2：member_disabled / no_candidate / capacity_exhausted）。
func (r *chainRouteResolver) superGroupSegmentAccounts(
	ctx context.Context,
	systemAccountID, endpointFamily string,
	binding gatewayruntimecache.GatewayAPIKeyGroupBindingRow,
	result gatewayrouting.NormalGatewayModelRouteResult,
) ([]gatewayruntimecache.OpenAIAccountSecret, []gatewaypreauth.NormalRouteMemberSkip, error) {
	accounts := make([]gatewayruntimecache.OpenAIAccountSecret, 0)
	skips := make([]gatewaypreauth.NormalRouteMemberSkip, 0, len(result.GroupSegments)+len(binding.DisabledMemberGroupIDs))
	for _, memberID := range binding.DisabledMemberGroupIDs {
		skips = append(skips, gatewaypreauth.NormalRouteMemberSkip{GroupID: memberID, Reason: "member_disabled"})
	}
	segmented := make(map[string]struct{}, len(result.GroupSegments))
	for _, segment := range result.GroupSegments {
		if segment.GroupID == "" {
			continue
		}
		segmented[segment.GroupID] = struct{}{}
		full := r.listFullAccounts(ctx, segment.GroupID, systemAccountID, result.RequestedModel, endpointFamily)
		byID := make(map[string]gatewayruntimecache.OpenAIAccountSecret, len(full))
		for _, account := range full {
			byID[account.ID] = account
		}
		fragment := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(segment.Accounts))
		for _, projection := range segment.Accounts {
			account, ok := byID[projection.ID]
			if !ok {
				account = accountFromRoutingProjection(projection)
			}
			boundGroupID := segment.GroupID
			account.BoundGroupID = &boundGroupID
			fragment = append(fragment, account)
		}
		if len(fragment) == 0 || r.quota == nil {
			accounts = append(accounts, fragment...)
			continue
		}
		groupAccess := r.rehydrateGroupAccess(ctx, segment.GroupID, systemAccountID, segment.GroupAccess)
		decisions, err := r.quota.CheckBatchAsync(ctx, *groupAccess, fragment)
		if err != nil {
			return nil, nil, err
		}
		survivors := 0
		for _, account := range fragment {
			if decision, ok := decisions[account.ID]; ok && !decision.Allowed {
				continue
			}
			accounts = append(accounts, account)
			survivors++
		}
		if survivors == 0 {
			skips = append(skips, gatewaypreauth.NormalRouteMemberSkip{GroupID: segment.GroupID, Reason: "capacity_exhausted"})
		}
	}
	// enabled 成员未进入片段：被能力/模型过滤后没有候选（设计 §4.2）。
	for _, memberID := range binding.MemberGroupIDs {
		if _, ok := segmented[memberID]; !ok {
			skips = append(skips, gatewaypreauth.NormalRouteMemberSkip{GroupID: memberID, Reason: "no_candidate"})
		}
	}
	// 片段按成员聚合会打散全局 route_rank 序：以解析池的全局序为准重排，
	// 保证后成员的 direct 命中排在前成员的 mapping 命中之前（设计 §4.2）。
	return orderSuperGroupAccountsByPool(accounts, result.Accounts), skips, nil
}

// orderSuperGroupAccountsByPool 按解析池顺序稳定重排超级分组幸存账号；
// 池外的账号（防御性）保留在尾部。
func orderSuperGroupAccountsByPool(accounts []gatewayruntimecache.OpenAIAccountSecret, pool []gatewayrouting.UpstreamAccount) []gatewayruntimecache.OpenAIAccountSecret {
	if len(accounts) < 2 || len(pool) == 0 {
		return accounts
	}
	position := make(map[string]int, len(pool))
	for index, projection := range pool {
		if _, exists := position[projection.ID]; !exists {
			position[projection.ID] = index
		}
	}
	ordered := append([]gatewayruntimecache.OpenAIAccountSecret(nil), accounts...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, leftOK := position[ordered[i].ID]
		right, rightOK := position[ordered[j].ID]
		switch {
		case leftOK && rightOK:
			return left < right
		case leftOK:
			return true
		case rightOK:
			return false
		default:
			return false
		}
	})
	return ordered
}

// rehydrateGroupAccess re-reads the full group access metadata; the lossy
// routing projection is the documented fallback when the cache read fails.
func (r *chainRouteResolver) rehydrateGroupAccess(ctx context.Context, groupID, systemAccountID string, fallback gatewayrouting.GroupUsageAccessMetadata) *gatewayruntimecache.GroupUsageAccessMetadata {
	if groupID == "" {
		projected := projectGroupAccessForRuntime(fallback)
		return &projected
	}
	meta, err := r.cache.ResolveCachedGroupUsageAccessMetadataAsync(ctx, groupID, systemAccountID)
	if err == nil && meta != nil {
		return meta
	}
	projected := projectGroupAccessForRuntime(fallback)
	return &projected
}

// rehydrateAccounts maps the routing projections back to full runtime
// accounts by ID. Source of truth is the runtime cache group listing for the
// selected group (Node: the selector returns full secrets); the request
// runtime snapshot (when already resolved) participates as a fallback so the
// accounts resolved once per request are reused.
func (r *chainRouteResolver) rehydrateAccounts(
	ctx context.Context,
	groupID, systemAccountID string,
	projected []gatewayrouting.UpstreamAccount,
	requestedModel, endpointFamily, routeSource string,
) ([]gatewayruntimecache.OpenAIAccountSecret, string) {
	full := r.listFullAccounts(ctx, groupID, systemAccountID, requestedModel, endpointFamily)
	byID := make(map[string]gatewayruntimecache.OpenAIAccountSecret, len(full))
	for _, account := range full {
		byID[account.ID] = account
	}
	accounts := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(projected))
	for _, projection := range projected {
		if account, ok := byID[projection.ID]; ok {
			accounts = append(accounts, account)
			continue
		}
		accounts = append(accounts, accountFromRoutingProjection(projection))
	}
	return accounts, routeSource
}

func (r *chainRouteResolver) listFullAccounts(ctx context.Context, groupID, systemAccountID, requestedModel, endpointFamily string) []gatewayruntimecache.OpenAIAccountSecret {
	if groupID == "" || r.cache == nil {
		return nil
	}
	accounts, err := r.cache.ListCachedOpenAIAccountsForGroupAsync(ctx, groupID, systemAccountID, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions{
		RequestedModel:          requestedModel,
		RequestedEndpointFamily: endpointFamily,
	})
	if err != nil {
		return nil
	}
	return accounts
}

// ---------------------------------------------------------------------------
// request view projections
// ---------------------------------------------------------------------------

// routingRequestView mirrors the requestModel/endpoint reads of the routing
// layer: method + originalUrl + path + body model.
func routingRequestView(req *gatewaypreauth.GatewayRequest, record *gatewayruntimecache.GatewayAPIKeyRow) gatewayrouting.RequestView {
	view := gatewayrouting.RequestView{}
	if req == nil {
		return view
	}
	view.Method = req.MethodUpper()
	view.OriginalURL = req.PathAndQuery()
	view.Path = req.Path()
	if state := req.BodyState(); state != nil && state.Model != nil {
		view.BodyModel = *state.Model
	}
	// 媒体映射族 override（供应商与上游协议面重构切片联修）：路由层
	// requestEndpointFamily 的路径词表只覆盖 chat/responses/messages/gemini，
	// /v1/videos 与 /v1/audio/speech 落空族——账户媒体映射（source family
	// video_generation / tts，媒体设计 §9）在路由模型过滤不可见，媒体映射
	// 承载的模型把 normal 路由推进 models 目录终局误判。此处按派发侧
	// requestMappingSourceFamilyOf（chain_driver.go）同词表补媒体族 override，
	// 两层映射解析恢复同源。
	if family := routingMediaEndpointFamilyOverrideOf(req); family != "" {
		view.EndpointFamilyOverride = family
	}
	_ = record
	return view
}

// routingMediaEndpointFamilyOverrideOf 把媒体创建形态的请求路径归一到账户
// 映射存储族（与 requestMappingSourceFamilyOf 的媒体臂同词表）：POST
// /v1/videos → video_generation；POST /v1/audio/speech → tts。非媒体形态
// 返回空串（族由路由层路径词表推导，行为不变）。
func routingMediaEndpointFamilyOverrideOf(req *gatewaypreauth.GatewayRequest) string {
	if req == nil || req.MethodUpper() != "POST" {
		return ""
	}
	path := chainStripGatewayVersionPrefix(gatewaypreauth.RequestPathWithoutQuery(req))
	switch path {
	case "/videos":
		return gatewayopenai.FamilyVideoGeneration
	case "/audio/speech":
		return gatewayopenai.FamilyTts
	}
	return ""
}

// boolPtr lifts a bool into an optional pointer.
func boolPtr(value bool) *bool { return &value }

// ---------------------------------------------------------------------------
// key row / config projections
// ---------------------------------------------------------------------------

// projectAPIKeyRowForRouting maps the runtime-cache row onto the routing
// projection (int widths widened; weight restored to its pointer union).
func projectAPIKeyRowForRouting(record *gatewayruntimecache.GatewayAPIKeyRow) *gatewayrouting.APIKeyRow {
	if record == nil {
		return nil
	}
	configJSON := ""
	if record.RouteStrategyConfigJSON != nil {
		configJSON = *record.RouteStrategyConfigJSON
	}
	bindings := make([]gatewayrouting.GroupBindingRow, 0, len(record.GroupBindings))
	for _, binding := range record.GroupBindings {
		weight := int64(binding.Weight)
		bindings = append(bindings, gatewayrouting.GroupBindingRow{
			ID:                     binding.ID,
			APIKeyID:               binding.APIKeyID,
			SystemAccountID:        binding.SystemAccountID,
			GroupID:                binding.GroupID,
			Priority:               int64(binding.Priority),
			Weight:                 &weight,
			Status:                 binding.Status,
			GroupEnabled:           int64(binding.GroupEnabled),
			GroupKind:              binding.GroupKind,
			MemberGroupIDs:         append([]string(nil), binding.MemberGroupIDs...),
			DisabledMemberGroupIDs: append([]string(nil), binding.DisabledMemberGroupIDs...),
		})
	}
	return &gatewayrouting.APIKeyRow{
		ID:                      record.ID,
		SystemAccountID:         record.SystemAccountID,
		RouteStrategyID:         record.RouteStrategyID,
		RouteStrategyMode:       record.RouteStrategyMode,
		RouteStrategyConfigJSON: configJSON,
		SelectedGroupID:         record.SelectedGroupID,
		Status:                  record.Status,
		GroupBindings:           bindings,
	}
}

// projectBindingsForRuntime carries the selector's binding filter back onto
// the runtime row, matching rows by ID (the routing projection widens the
// numeric columns; identity columns are stable).
func projectBindingsForRuntime(projected []gatewayrouting.GroupBindingRow, original []gatewayruntimecache.GatewayAPIKeyGroupBindingRow) []gatewayruntimecache.GatewayAPIKeyGroupBindingRow {
	if len(projected) == len(original) {
		return append([]gatewayruntimecache.GatewayAPIKeyGroupBindingRow(nil), original...)
	}
	byID := make(map[string]gatewayruntimecache.GatewayAPIKeyGroupBindingRow, len(original))
	for _, binding := range original {
		byID[binding.ID] = binding
	}
	out := make([]gatewayruntimecache.GatewayAPIKeyGroupBindingRow, 0, len(projected))
	for _, binding := range projected {
		if original, ok := byID[binding.ID]; ok {
			out = append(out, original)
			continue
		}
		out = append(out, gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
			ID:                     binding.ID,
			APIKeyID:               binding.APIKeyID,
			SystemAccountID:        binding.SystemAccountID,
			GroupID:                binding.GroupID,
			Priority:               int(binding.Priority),
			Status:                 binding.Status,
			GroupEnabled:           int(binding.GroupEnabled),
			GroupKind:              binding.GroupKind,
			MemberGroupIDs:         append([]string(nil), binding.MemberGroupIDs...),
			DisabledMemberGroupIDs: append([]string(nil), binding.DisabledMemberGroupIDs...),
		})
	}
	return out
}

// accountFromRoutingProjection rebuilds the minimal dispatchable secret when
// the full re-hydration misses (the projection carries the identity columns
// the dispatch layer keys on)。routing 层的 ProviderCode 槽位承载的是 vendor
// 身份（见 projectAccountForRouting），secret 侧统一以 VendorCode 承载该身份
// （B4②：secret.ProviderCode 冗余槽位已退役）——下游有多处按 VendorCode 判身
// 份：切号冻结门（gatewaydispatch/switchtarget.go 门 2）在空 vendor 时与任何
// 非空目标都不相等，同供应商约束会失效；而 CodexOAuthVendorEligible /
// codexOAuthVendorBucketEligible 只认 gpt——空 vendor 不再按 GPT 处理；
// 缺 VendorCode 会让回退路径账户失去供应商身份，必须在读面水合真实 vendor。
func accountFromRoutingProjection(account gatewayrouting.UpstreamAccount) gatewayruntimecache.OpenAIAccountSecret {
	return gatewayruntimecache.OpenAIAccountSecret{
		ID:                        account.ID,
		VendorCode:                account.ProviderCode,
		ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		ProtocolCode:              account.ProtocolCode,
		ProtocolVersion:           account.ProtocolVersion,
		SupportedModels:           append([]string(nil), account.SupportedModels...),
	}
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// localEndpointFamily re-derives gatewayRequestEndpointFamily for the
// re-hydration listing (the routing RequestView method is package-private).
func localEndpointFamily(view gatewayrouting.RequestView) string {
	source := view.OriginalURL
	if source == "" {
		source = view.Path
	}
	path := strings.ToLower(strings.TrimSpace(source))
	if index := strings.Index(path, "?"); index >= 0 {
		path = path[:index]
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	// OpenAI families (the /v1 prefix is stripped like the Node helper).
	normalizedOpenAI := path
	if strings.HasPrefix(normalizedOpenAI, "/v1") {
		rest := normalizedOpenAI[3:]
		if rest == "" || rest[0] == '/' {
			normalizedOpenAI = rest
		}
	}
	if strings.Contains(normalizedOpenAI, "/chat/completions") {
		return gatewayrouting.EndpointFamilyChatCompletions
	}
	if strings.Contains(normalizedOpenAI, "/responses") {
		return gatewayrouting.EndpointFamilyResponses
	}
	// Anthropic messages family.
	if strings.EqualFold(view.Method, "POST") {
		normalizedAnthropic := path
		if strings.HasPrefix(normalizedAnthropic, "/v1") {
			rest := normalizedAnthropic[3:]
			if rest == "" || rest[0] == '/' {
				normalizedAnthropic = rest
			}
		}
		if normalizedAnthropic == "/messages" {
			return gatewayrouting.EndpointFamilyMessages
		}
	}
	// Gemini families.
	if strings.EqualFold(view.Method, "POST") {
		normalizedGemini := path
		if strings.HasPrefix(normalizedGemini, "/v1beta") {
			rest := normalizedGemini[len("/v1beta"):]
			if rest == "" || rest[0] == '/' {
				normalizedGemini = rest
			}
		}
		switch {
		case normalizedGemini == "/models":
			return ""
		case strings.HasSuffix(normalizedGemini, ":generatecontent"):
			return gatewayrouting.EndpointFamilyGenerateContent
		case strings.HasSuffix(normalizedGemini, ":streamgeneratecontent"):
			return gatewayrouting.EndpointFamilyStreamGenerate
		case strings.HasSuffix(normalizedGemini, ":counttokens"):
			return gatewayrouting.EndpointFamilyCountTokens
		case strings.HasSuffix(normalizedGemini, ":embedcontent"):
			return gatewayrouting.EndpointFamilyEmbedContent
		}
	}
	return ""
}

func stringPtr(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

// renderSchedulingPolicy keeps the opaque policy payload verbatim (the
// routing projection stores it as a string).
func renderSchedulingPolicy(policy *gatewayruntimecache.GroupSchedulingPolicy) string {
	if policy == nil {
		return ""
	}
	raw, err := json.Marshal(*policy)
	if err != nil {
		return ""
	}
	return string(raw)
}

// bindingForGroup 返回记录中指定分组的绑定行（用于把选中目标的绑定写回
// APIKeyRecord，保证成员扇出消费点看到正确 GroupKind/MemberGroupIDs）。
func bindingForGroup(record *gatewayruntimecache.GatewayAPIKeyRow, groupID string) (gatewayruntimecache.GatewayAPIKeyGroupBindingRow, bool) {
	if record == nil {
		return gatewayruntimecache.GatewayAPIKeyGroupBindingRow{}, false
	}
	for _, binding := range record.GroupBindings {
		if binding.GroupID == groupID {
			return binding, true
		}
	}
	return gatewayruntimecache.GatewayAPIKeyGroupBindingRow{}, false
}
