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
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
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
	route, err := c.cache.ResolveCachedProviderModelRouteAsync(ctx, input.Model, input.ProviderCodes, input.SystemAccountID, input.IncludeUnpriced)
	if err != nil {
		return gatewayrouting.ProviderModelRouteResolution{}, err
	}
	return gatewayrouting.ProviderModelRouteResolution{
		Outcome:              string(route.Outcome),
		ModelKey:             route.ModelKey,
		ProviderCode:         route.ProviderCode,
		MatchedProviderCodes: route.MatchedProviderCodes,
	}, nil
}

// projectGroupAccessForRouting projects the runtime-cache group access into
// the routing-layer projection (string fields only; the pointers collapse to
// their rendered values exactly like the Node selector rows).
func projectGroupAccessForRouting(meta gatewayruntimecache.GroupUsageAccessMetadata) gatewayrouting.GroupUsageAccessMetadata {
	return gatewayrouting.GroupUsageAccessMetadata{
		GroupOwnerSystemAccountID:      meta.GroupOwnerSystemAccountID,
		ProviderCode:                   meta.ProviderCode,
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
		ProviderCode:                   meta.ProviderCode,
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
// routing-layer view. ModelMappings are carried over (合并路由设计 3.1/3.2)：
// routing 层模型过滤（gatewayrouting.FilterAccountsByRequestedModel）需要映
// 射行才能承认“映射来源模型”账号——丢行会把映射账号静默过滤出合并池/目标
// 组；dispatch driver 的权威映射解析不受影响。
//
// 范围边界说明：gatewayrouting 的映射行类型（gatewayAccountModelMapping）与
// 其构造函数未导出，组合根无法以类型字面量构造该行——此处以受控反射按导出
// 字段名填充（字段签名由 cmd 端到端跨组映射测试锁定）。gatewayrouting 导出
// 映射行构造函数后，应替换为直接投影。
func projectAccountForRouting(account gatewayruntimecache.OpenAIAccountSecret) gatewayrouting.UpstreamAccount {
	projected := gatewayrouting.UpstreamAccount{
		ID:                        account.ID,
		ProviderCode:              account.ProviderCode,
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
	// quota 承载 merge 解析期逐片段配额批查（B14 权威门）。与 dispatch 窗口
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
		Outcome:              result.Outcome,
		Reason:               result.Reason,
		RequestedModel:       result.RequestedModel,
		StatusCode:           result.StatusCode,
		Type:                 result.Type,
		Code:                 result.Code,
		Message:              result.Message,
		MatchedProviderCodes: result.MatchedProviderCodes,
		APIKeyRecord:         input.APIKeyRecord,
		RouteSource:          string(result.RouteSource),
		MatchedProviderCode:  result.MatchedProviderCode,
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
	// merge（合并路由设计 3.1/3.4/B14-B16）：按片段扇出 rehydrate + 全池组标
	// + 逐片段配额批查（权威门），窗口组在幸存片段上重算。
	if result.RouteSource == gatewayrouting.RouteSourceMerged || len(result.GroupSegments) > 0 {
		return r.resolveMergeSelectedRoute(ctx, input, view, result, out)
	}
	out.GroupID = result.GroupID
	out.GroupAccess = r.rehydrateGroupAccess(ctx, result.GroupID, input.APIKeyRecord.SystemAccountID, result.GroupAccess)
	out.Accounts, out.RouteSource, out.MatchedProviderCode = r.rehydrateAccounts(
		ctx, result.GroupID, input.APIKeyRecord.SystemAccountID, result.Accounts,
		result.RequestedModel, localEndpointFamily(view), out.RouteSource, out.MatchedProviderCode,
	)
	return out, nil
}

// resolveMergeSelectedRoute 兑现 merge selected 结果的组合根语义（合并路由设
// 计 3.1/3.4/B14/B15）：
//  1. 按片段扇出 rehydrate：每个片段用其 GroupID 走现有 listFullAccounts 机
//     制取全量账号（保留 ModelMappings），按账号 ID 映射回片段；
//  2. 全池组标（3.1 第 4 条）：每个账号显式写 BoundGroupID = 所在片段组——
//     现有赋值点只覆盖 account_authorized 账号（chain_accounts_secret.go），
//     owner / group_authorized 账号恒 nil，必须全量覆盖写；
//  3. 逐片段配额批查（B14 权威门）：对每片段以该组 groupAccess 调
//     CheckBatchAsync，被否决账号从片段剔除，片段空则丢弃；全部片段被剔空
//     → 失败结果（429 + rate_limit_exceeded + 配额超限文案），由 preflight
//     的 NormalRoute 失败路径渲染；
//  4. 结果组装：Accounts = 各幸存片段扁平池（片段序），GroupID/GroupAccess =
//     首个幸存片段的组（首片段被配额剔空时窗口组后移，与“首个非空片段”口
//     径一致），APIKeyRecord.SelectedGroupID 同步窗口组。
func (r *chainRouteResolver) resolveMergeSelectedRoute(
	ctx context.Context,
	input gatewaypreauth.NormalRouteInput,
	view gatewayrouting.RequestView,
	result gatewayrouting.NormalGatewayModelRouteResult,
	out gatewaypreauth.NormalRouteResult,
) (gatewaypreauth.NormalRouteResult, error) {
	systemAccountID := input.APIKeyRecord.SystemAccountID
	endpointFamily := localEndpointFamily(view)
	type mergeSegment struct {
		groupID     string
		groupAccess *gatewayruntimecache.GroupUsageAccessMetadata
		accounts    []gatewayruntimecache.OpenAIAccountSecret
	}
	survivors := make([]mergeSegment, 0, len(result.GroupSegments))
	for _, segment := range result.GroupSegments {
		if segment.GroupID == "" {
			continue
		}
		full := r.listFullAccounts(ctx, segment.GroupID, systemAccountID, result.RequestedModel, endpointFamily)
		byID := make(map[string]gatewayruntimecache.OpenAIAccountSecret, len(full))
		for _, account := range full {
			byID[account.ID] = account
		}
		accounts := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(segment.Accounts))
		for _, projection := range segment.Accounts {
			account, ok := byID[projection.ID]
			if !ok {
				account = accountFromRoutingProjection(projection)
			}
			// 全池组标：不限于授权账号（合并路由设计 3.1 第 4 条）。
			boundGroupID := segment.GroupID
			account.BoundGroupID = &boundGroupID
			accounts = append(accounts, account)
		}
		if len(accounts) == 0 {
			continue
		}
		groupAccess := r.rehydrateGroupAccess(ctx, segment.GroupID, systemAccountID, segment.GroupAccess)
		// B14 权威门：逐片段授权配额批查。quota 未装配（部分组合测试）时
		// 不做剔除——生产组合根 fail-fast 要求 AuthorizationQuota 必装配。
		if r.quota != nil {
			decisions, err := r.quota.CheckBatchAsync(ctx, *groupAccess, accounts)
			if err != nil {
				return gatewaypreauth.NormalRouteResult{}, err
			}
			allowed := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(accounts))
			for _, account := range accounts {
				if decision, ok := decisions[account.ID]; ok && !decision.Allowed {
					continue
				}
				allowed = append(allowed, account)
			}
			accounts = allowed
		}
		if len(accounts) == 0 {
			continue
		}
		survivors = append(survivors, mergeSegment{groupID: segment.GroupID, groupAccess: groupAccess, accounts: accounts})
	}
	if len(survivors) == 0 {
		// B14：全部片段被配额剔空 → 429 终局（与 preparation.go 窗口门的
		// authorization_quota_exceeded 终局同形态），走 NormalRoute 失败路径
		// 由 preflight 渲染。
		return gatewaypreauth.NormalRouteResult{
			Outcome:              gatewaypreauth.NormalRouteOutcomeFailed,
			RequestedModel:       result.RequestedModel,
			StatusCode:           429,
			Type:                 "rate_limit_exceeded",
			Code:                 "rate_limit_exceeded",
			Message:              gatewayquota.AuthorizationQuotaExceededMessage,
			MatchedProviderCodes: result.MatchedProviderCodes,
			APIKeyRecord:         input.APIKeyRecord,
		}, nil
	}
	window := survivors[0]
	accounts := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(survivors))
	for _, segment := range survivors {
		accounts = append(accounts, segment.accounts...)
	}
	out.GroupID = window.groupID
	out.GroupAccess = window.groupAccess
	out.Accounts = accounts
	if out.APIKeyRecord != nil {
		out.APIKeyRecord.SelectedGroupID = window.groupID
	}
	return out, nil
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
	requestedModel, endpointFamily, routeSource, matchedProviderCode string,
) ([]gatewayruntimecache.OpenAIAccountSecret, string, string) {
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
	return accounts, routeSource, matchedProviderCode
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
	// The endpoint-family override stays empty: the override belongs to the
	// model-mapping source pinning that no gateway caller sets today.
	_ = record
	return view
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
			ID:              binding.ID,
			APIKeyID:        binding.APIKeyID,
			SystemAccountID: binding.SystemAccountID,
			GroupID:         binding.GroupID,
			Priority:        int64(binding.Priority),
			Weight:          &weight,
			Status:          binding.Status,
			ProviderCode:    binding.ProviderCode,
			GroupEnabled:    int64(binding.GroupEnabled),
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
			ID:              binding.ID,
			APIKeyID:        binding.APIKeyID,
			SystemAccountID: binding.SystemAccountID,
			GroupID:         binding.GroupID,
			Priority:        int(binding.Priority),
			Status:          binding.Status,
			ProviderCode:    binding.ProviderCode,
			GroupEnabled:    int(binding.GroupEnabled),
		})
	}
	return out
}

// accountFromRoutingProjection rebuilds the minimal dispatchable secret when
// the full re-hydration misses (the projection carries the identity columns
// the dispatch layer keys on).
func accountFromRoutingProjection(account gatewayrouting.UpstreamAccount) gatewayruntimecache.OpenAIAccountSecret {
	return gatewayruntimecache.OpenAIAccountSecret{
		ID:                        account.ID,
		ProviderCode:              account.ProviderCode,
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
