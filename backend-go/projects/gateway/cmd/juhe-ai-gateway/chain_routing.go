package main

// G20 phase-2 composition-root adapter: the gatewaypreauth.RouteResolver port
// (resolverport.go) bridged onto the frozen routing cores
// gatewayrouting.NormalModelRouteService (G08) and gatewayhybrid.RouteService
// (G09).
//
// Node authority:
//   - request/preflight.ts resolveNormalGatewayModelRoute /
//     resolveHybridGatewayRoute call sites,
//   - normal-model-route.service.ts + hybrid/routing.service.ts.
//
// The two routing cores return lossy projections
// (gatewayrouting.UpstreamAccount / gatewayhybrid.OpenAIAccountSecret /
// gatewayhybrid.APIKeyRecord) while the preflight contract carries full
// runtime accounts (gatewayruntimecache.OpenAIAccountSecret, credentials
// included) and runtime-cache key rows. The adapter translates between the
// two vocabularies and re-hydrates the full accounts / group access from the
// gateway runtime cache, exactly like the Node selector-backed call sites.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhybrid"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
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
// routing-layer view. ModelMappings stay empty on purpose: the mapping row
// type (gatewayrouting.gatewayAccountModelMapping) is package-private, so the
// routing model filter resolves direct supported-model matches only; the
// dispatch driver re-applies the full mapping resolution
// (gatewayopenai.ResolveAccountModelMapping) when the request is built.
func projectAccountForRouting(account gatewayruntimecache.OpenAIAccountSecret) gatewayrouting.UpstreamAccount {
	return gatewayrouting.UpstreamAccount{
		ID:                        account.ID,
		ProviderCode:              account.ProviderCode,
		ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		ProtocolCode:              account.ProtocolCode,
		ProtocolVersion:           account.ProtocolVersion,
		SupportedModels:           append([]string(nil), account.SupportedModels...),
	}
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
	cache   *gatewayruntimecache.Service
	normal  *gatewayrouting.NormalModelRouteService
	hybrid  *gatewayhybrid.RouteService
	scoring *gatewayhybrid.ScoringService
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
	out.GroupID = result.GroupID
	out.GroupAccess = r.rehydrateGroupAccess(ctx, result.GroupID, input.APIKeyRecord.SystemAccountID, result.GroupAccess)
	out.Accounts, out.RouteSource, out.MatchedProviderCode = r.rehydrateAccounts(
		ctx, result.GroupID, input.APIKeyRecord.SystemAccountID, result.Accounts,
		result.RequestedModel, localEndpointFamily(view), out.RouteSource, out.MatchedProviderCode,
	)
	return out, nil
}

// ResolveHybridGatewayRoute mirrors resolveHybridGatewayRoute. The hybrid
// core is optional at assembly (normal-only deployments keep it nil and the
// preflight treats skipped as "not a hybrid request").
func (r *chainRouteResolver) ResolveHybridGatewayRoute(ctx context.Context, input gatewaypreauth.HybridRouteInput) (gatewaypreauth.HybridRouteResult, error) {
	if r.hybrid == nil {
		return gatewaypreauth.HybridRouteResult{Outcome: gatewaypreauth.HybridRouteOutcomeSkipped, Reason: gatewayhybrid.RouteSkipNotHybridStrategy}, nil
	}
	record, err := projectAPIKeyRowForHybrid(input.APIKeyRecord)
	if err != nil {
		return gatewaypreauth.HybridRouteResult{}, err
	}
	result, err := r.hybrid.Resolve(ctx, gatewayhybrid.RouteInput{
		View:                       hybridRequestView(input.Req),
		Body:                       hybridRequestBody{request: input.Req},
		APIKeyRecord:               *record,
		TraceID:                    input.TraceID,
		ClientIP:                   input.ClientIP,
		Endpoint:                   input.Endpoint,
		Audit:                      hybridAuditMetadata{capture: input.AuditCapture},
		RequestClientCompatibility: input.RequestClientCompatibility,
	}, r.scoring)
	if err != nil {
		return gatewaypreauth.HybridRouteResult{}, err
	}
	out := gatewaypreauth.HybridRouteResult{
		Outcome:      result.Outcome,
		Reason:       result.Reason,
		APIKeyRecord: input.APIKeyRecord,
	}
	if result.Outcome != gatewayhybrid.RouteOutcomeSelected {
		return out, nil
	}
	updated := *input.APIKeyRecord
	if result.APIKeyRecord != nil {
		updated.SelectedGroupID = result.APIKeyRecord.SelectedGroupID
	}
	out.APIKeyRecord = &updated
	out.GroupID = result.GroupID
	out.GroupAccess = r.rehydrateGroupAccess(ctx, result.GroupID, input.APIKeyRecord.SystemAccountID, gatewayrouting.GroupUsageAccessMetadata{
		ProviderCode:     result.GroupAccess.ProviderCode,
		SchedulingPolicy: result.GroupAccess.SchedulingPolicy,
		GroupAccessType:  "",
	})
	out.Accounts = r.rehydrateAccountsByID(ctx, result.GroupID, input.APIKeyRecord.SystemAccountID, hybridAccountIDs(result.Accounts), input.Req)
	out.TargetModel = result.TargetModel
	out.AffinityApplied = result.AffinityApplied
	out.ScoringFallbackApplied = result.ScoringFallbackApplied
	out.Config = hybridConfigToMap(result.Config)
	out.Scoring = hybridScoringToMap(result.Scoring)
	out.Route = hybridRouteToMap(result.Route)
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

func (r *chainRouteResolver) rehydrateAccountsByID(ctx context.Context, groupID, systemAccountID string, ids []string, req *gatewaypreauth.GatewayRequest) []gatewayruntimecache.OpenAIAccountSecret {
	full := r.listFullAccounts(ctx, groupID, systemAccountID, hybridTargetModelHint(req), "")
	byID := make(map[string]gatewayruntimecache.OpenAIAccountSecret, len(full))
	for _, account := range full {
		byID[account.ID] = account
	}
	accounts := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(ids))
	for _, id := range ids {
		if account, ok := byID[id]; ok {
			accounts = append(accounts, account)
			continue
		}
		accounts = append(accounts, gatewayruntimecache.OpenAIAccountSecret{ID: id})
	}
	return accounts
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

// hybridRequestView mirrors the express view the hybrid modules read.
func hybridRequestView(req *gatewaypreauth.GatewayRequest) *gatewayhybrid.GatewayRequestView {
	view := &gatewayhybrid.GatewayRequestView{}
	if req == nil {
		return view
	}
	view.Method = req.MethodUpper()
	view.Path = req.Path()
	view.ContentType = req.Header("content-type")
	if req.Body != nil {
		view.RawBody = req.Body.RawBody
		view.BodyAvailable = req.Body.Body != nil || len(req.Body.RawBody) > 0
		if req.Body.Body != nil {
			view.ParsedBody = req.Body.Body
		}
	}
	if state := req.BodyState(); state != nil {
		model := ""
		if state.Model != nil {
			model = *state.Model
		}
		view.OriginalModel = model
		view.OriginalModelPresent = model != ""
		view.BodyState = &gatewayhybrid.RequestBodyState{
			RawBodyBytes:            int64(len(req.Body.RawBody)),
			ContentType:             state.ContentType,
			JSONParseStatus:         string(state.JSONParseStatus),
			Model:                   model,
			Stream:                  state.Stream,
			ImageGeneration:         boolPtr(state.ImageGeneration),
			ImageGenerationForced:   boolPtr(state.ImageGenerationForced),
			StrictOutputRequirement: state.StrictOutputRequirement,
		}
	}
	view.ConversationKey = req.Header("x-conversation-key")
	return view
}

// boolPtr lifts a bool into the optional-pointer union of
// gatewayhybrid.RequestBodyState (present == defined in the Node payload).
func boolPtr(value bool) *bool { return &value }

// hybridTargetModelHint returns the parsed body model for the re-hydration
// listing (a best-effort hint only).
func hybridTargetModelHint(req *gatewaypreauth.GatewayRequest) string {
	if req == nil || req.BodyState() == nil || req.BodyState().Model == nil {
		return ""
	}
	return *req.BodyState().Model
}

func hybridAccountIDs(accounts []gatewayhybrid.OpenAIAccountSecret) []string {
	ids := make([]string, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.ID)
	}
	return ids
}

// hybridRequestBody bridges the gateway body pipeline to the
// gatewayhybrid.RequestBodyGateway port (rewriteHybridRequestModel). The
// model rewrite reuses the gatewaybody ReplaceGatewayJSONBodyModel helper so
// the serialized raw body, the parsed object and the body state stay one
// consistent unit (Node mutates req.body + rawBody through body.ts).
type hybridRequestBody struct {
	request *gatewaypreauth.GatewayRequest
}

func (b hybridRequestBody) ReplaceModel(targetModel string) bool {
	if b.request == nil || b.request.Body == nil {
		return false
	}
	return gatewaybody.ReplaceGatewayJSONBodyModel(b.request.Body, targetModel, nil)
}

func (b hybridRequestBody) HasRawBody() bool {
	return b.request != nil && b.request.Body != nil && len(b.request.Body.RawBody) > 0
}

func (b hybridRequestBody) ParseRawBody(ctx context.Context) (any, error) {
	if !b.HasRawBody() {
		return nil, fmt.Errorf("混合路由无法改写空请求体")
	}
	return gatewayhybrid.ParseJSONOrdered(b.request.Body.RawBody)
}

func (b hybridRequestBody) ReplaceModelWithParsed(targetModel string, parsed *gatewayhybrid.OrderedJSON) bool {
	if parsed == nil || b.request == nil || b.request.Body == nil {
		return false
	}
	object := gatewayhybrid.NewOrderedJSON()
	object.Set("model", targetModel)
	return gatewaybody.ReplaceGatewayJSONBodyModel(b.request.Body, targetModel, orderedJSONObjectMap(parsed))
}

// orderedJSONObjectMap converts the hybrid ordered object into the plain map
// the gatewaybody serializer consumes (key order is re-derived from the
// original raw body by the serializer's JSON round trip; Node keeps the
// mutation on the same JS object, so identity order of untouched keys is
// preserved by the raw-body replace path in ReplaceGatewayJSONBody).
func orderedJSONObjectMap(object *gatewayhybrid.OrderedJSON) map[string]any {
	out := map[string]any{}
	if object == nil {
		return out
	}
	for _, key := range object.Keys() {
		value, _ := object.Get(key)
		out[key] = orderedValueToPlain(value)
	}
	return out
}

func orderedValueToPlain(value any) any {
	switch typed := value.(type) {
	case *gatewayhybrid.OrderedJSON:
		return orderedJSONObjectMap(typed)
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = orderedValueToPlain(item)
		}
		return out
	default:
		return typed
	}
}

// hybridAuditMetadata bridges the frozen capture context to the hybrid
// diagnostics sink.
type hybridAuditMetadata struct {
	capture gatewaypreauth.AuditCaptureContext
}

func (m hybridAuditMetadata) AddGatewayMetadata(label string, metadata *gatewayhybrid.OrderedJSON) {
	if m.capture == nil || metadata == nil {
		return
	}
	rendered, ok := orderedValueToPlain(metadata).(map[string]any)
	if !ok {
		return
	}
	m.capture.AddGatewayMetadata(label, rendered)
}

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

// projectAPIKeyRowForHybrid decodes the stored hybrid routing config for the
// hybrid core. A missing / unparsable config yields the exact nil the hybrid
// skip branch expects (not_hybrid_route_strategy).
func projectAPIKeyRowForHybrid(record *gatewayruntimecache.GatewayAPIKeyRow) (*gatewayhybrid.APIKeyRecord, error) {
	if record == nil {
		return &gatewayhybrid.APIKeyRecord{RouteStrategyMode: ""}, nil
	}
	out := &gatewayhybrid.APIKeyRecord{
		ID:                record.ID,
		SystemAccountID:   record.SystemAccountID,
		RouteStrategyMode: record.RouteStrategyMode,
		SelectedGroupID:   record.SelectedGroupID,
	}
	if record.HybridRoutingConfig != nil && len(record.HybridRoutingConfig.Raw) > 0 {
		config := &routestrategies.HybridRoutingConfig{}
		if err := json.Unmarshal(record.HybridRoutingConfig.Raw, config); err != nil {
			return nil, fmt.Errorf("解析混合路由配置失败: %w", err)
		}
		out.HybridRoutingConfig = config
	}
	return out, nil
}

func hybridConfigToMap(config *routestrategies.HybridRoutingConfig) map[string]any {
	if config == nil {
		return nil
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return nil
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func hybridScoringToMap(scoring gatewayhybrid.HybridScoringResult) map[string]any {
	raw, err := json.Marshal(scoring)
	if err != nil {
		return nil
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func hybridRouteToMap(route routestrategies.HybridLevelRoute) map[string]any {
	raw, err := json.Marshal(route)
	if err != nil {
		return nil
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
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
