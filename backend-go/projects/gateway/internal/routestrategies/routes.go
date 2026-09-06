// Route family: /route-strategies (admin) + /my-route-strategies (self mirror
// via forceSelfAccessScope). Mirrors modules/route-strategies/route-strategies
// .routes.ts: pagination with binding snapshot, detail, guarded create,
// optimistic-lock patch and delete protection with operation logs.
package routestrategies

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// Deps bundles the M06 slice collaborators.
type Deps struct {
	Store *Store
	Auth  *authsys.Deps
	Sink  authsys.OperationLogSink
}

// Mount wires the route-strategy family: admin surface on /route-strategies
// and the forceSelfAccessScope mirror on /my-route-strategies. The
// speed-first-runtime reads mount only when the runtime facade is wired
// (composition side), mirroring Node's facade dependency.
func (d *Deps) Mount(k *kernel.Kernel) {
	prefix := "/__aisys__/api"
	// Admin surface (requireAdmin).
	k.Register("GET "+prefix+"/route-strategies", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.list(w, r, adminScope(r))
	})))
	k.Register("GET "+prefix+"/route-strategies/options", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.options(w, r, adminScope(r))
	})))
	k.Register("GET "+prefix+"/route-strategies/{id}", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !d.writeScopeQuery(w, r) {
			return
		}
		d.find(w, r, adminScope(r))
	})))
	k.Register("GET "+prefix+"/route-strategies/{id}/edit-basic", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !d.writeScopeQuery(w, r) {
			return
		}
		d.editBasic(w, r, adminScope(r))
	})))
	if d.Store.speedFirst != nil {
		k.Register("GET "+prefix+"/route-strategies/{id}/speed-first-runtime", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !d.writeScopeQuery(w, r) {
				return
			}
			d.speedFirstRuntime(w, r, adminScope(r))
		})))
	}
	k.Register("POST "+prefix+"/route-strategies", d.mountGuardedCreate(false))
	k.Register("PATCH "+prefix+"/route-strategies/{id}", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !d.writeScopeQuery(w, r) {
			return
		}
		d.patch(w, r, adminScope(r))
	})))
	k.Register("DELETE "+prefix+"/route-strategies/{id}", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !d.writeScopeQuery(w, r) {
			return
		}
		d.remove(w, r, adminScope(r))
	})))

	// Self surface (forceSelfAccessScope: scope pinned to the caller).
	k.Register("GET "+prefix+"/my-route-strategies", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.list(w, r, selfScope(r))
	})))
	k.Register("GET "+prefix+"/my-route-strategies/options", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.options(w, r, selfScope(r))
	})))
	k.Register("GET "+prefix+"/my-route-strategies/{id}", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !d.writeScopeQuery(w, r) {
			return
		}
		d.find(w, r, selfScope(r))
	})))
	k.Register("GET "+prefix+"/my-route-strategies/{id}/edit-basic", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !d.writeScopeQuery(w, r) {
			return
		}
		d.editBasic(w, r, selfScope(r))
	})))
	if d.Store.speedFirst != nil {
		k.Register("GET "+prefix+"/my-route-strategies/{id}/speed-first-runtime", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !d.writeScopeQuery(w, r) {
				return
			}
			d.speedFirstRuntime(w, r, selfScope(r))
		})))
	}
	k.Register("POST "+prefix+"/my-route-strategies", d.mountGuardedCreate(true))
	k.Register("PATCH "+prefix+"/my-route-strategies/{id}", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !d.writeScopeQuery(w, r) {
			return
		}
		d.patch(w, r, selfScope(r))
	})))
	k.Register("DELETE "+prefix+"/my-route-strategies/{id}", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !d.writeScopeQuery(w, r) {
			return
		}
		d.remove(w, r, selfScope(r))
	})))
}

// writeScopeQuery mirrors parseRequestScopeQuery on the detail/mutation
// routes: a present-but-blank ?systemAccountId= is a schema failure (400
// 系统账号 ID 不能为空) before any business query runs. The list and options
// reads do not run this validation (Node reads the scope directly).
func (d *Deps) writeScopeQuery(w http.ResponseWriter, r *http.Request) bool {
	value, present := r.URL.Query()["systemAccountId"]
	if !present {
		return true
	}
	if strings.TrimSpace(value[0]) == "" {
		kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
		return false
	}
	return true
}

// adminScope mirrors getRequestAccessScope(query.systemAccountId) for admins.
func adminScope(r *http.Request) AccessScope {
	auth := authsys.AuthContextFrom(r)
	filter := strings.TrimSpace(r.URL.Query().Get("systemAccountId"))
	if filter == "all" {
		filter = ""
	}
	return AccessScope{ViewerID: auth.SystemAccountID, IsAdmin: true, FilterID: filter}
}

// selfScope mirrors forceSelfAccessScope: the query-scoped account id is
// dropped and the scope is pinned to the authenticated caller.
func selfScope(r *http.Request) AccessScope {
	auth := authsys.AuthContextFrom(r)
	return AccessScope{ViewerID: auth.SystemAccountID}
}

func operationMode(access AccessScope) string {
	if access.IsAdmin {
		return "admin"
	}
	return "self"
}

// queryInt mirrors integerQueryValue (via intQueryValue): blank or non-integer
// text → ok=false (caller applies the default).
func queryInt(raw string) (int, bool) {
	return intQueryValue(raw)
}

func (d *Deps) list(w http.ResponseWriter, r *http.Request, access AccessScope) {
	options := ListOptions{Keyword: strings.TrimSpace(r.URL.Query().Get("keyword"))}
	if page, ok := queryInt(r.URL.Query().Get("page")); ok {
		options.Page = page
	} else {
		options.Page = 1
	}
	if pageSize, ok := queryInt(r.URL.Query().Get("pageSize")); ok {
		options.PageSize = pageSize
	} else {
		options.PageSize = 50
	}
	switch strings.TrimSpace(r.URL.Query().Get("mode")) {
	case ModeNormal, ModeHybridSmart, ModeWeighted, ModeFailover, ModeRoundRobin:
		options.Mode = strings.TrimSpace(r.URL.Query().Get("mode"))
	}
	switch strings.TrimSpace(r.URL.Query().Get("status")) {
	case "active", "disabled":
		options.Status = strings.TrimSpace(r.URL.Query().Get("status"))
	}
	result, err := d.Store.ListPage(r.Context(), access, options)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	// summarizeRouteStrategySpeedFirstLatencyRuntimeAsync: one batched runtime
	// read per page; only speed_first normal rows carry the summary. Runtime
	// store failures never fail the list (Node renders them unavailable).
	d.enrichSpeedFirstSummaries(r.Context(), result.Items)
	kernel.WriteOK(w, result, "")
}

// isNormalSpeedFirstRouteStrategy mirrors the facade predicate.
func isNormalSpeedFirstRouteStrategy(mode string, normal *NormalRoutingConfig) bool {
	return mode == ModeNormal && normal != nil && normal.SchedulingPreference == "speed_first"
}

// enrichSpeedFirstSummaries mirrors summarizeRouteStrategySpeedFirstLatencyRuntimeAsync:
// at most one runtime query per page, capped at 50 ids, per-strategy degraded
// counts after account dedupe.
func (d *Deps) enrichSpeedFirstSummaries(ctx context.Context, list []ListItem) {
	if d.Store.speedFirst == nil || len(list) == 0 {
		return
	}
	speedFirstIDs := make([]string, 0, len(list))
	for _, item := range list {
		if isNormalSpeedFirstRouteStrategy(item.Mode, item.NormalRoutingConfig) {
			speedFirstIDs = append(speedFirstIDs, item.ID)
		}
	}
	if len(speedFirstIDs) == 0 {
		return
	}
	unavailable := &SpeedFirstRuntimeSummary{RuntimeAvailable: false, DegradedCount: 0}
	if len(speedFirstIDs) > 50 {
		for index := range list {
			if isNormalSpeedFirstRouteStrategy(list[index].Mode, list[index].NormalRoutingConfig) {
				list[index].SpeedFirstLatency = unavailable
			}
		}
		return
	}
	items, available, err := d.Store.speedFirst.ListDegradedRuntime(ctx, nil, speedFirstIDs)
	if err != nil || !available {
		for index := range list {
			if isNormalSpeedFirstRouteStrategy(list[index].Mode, list[index].NormalRoutingConfig) {
				list[index].SpeedFirstLatency = unavailable
			}
		}
		return
	}
	counts := speedFirstDegradedCounts(items)
	for index := range list {
		if isNormalSpeedFirstRouteStrategy(list[index].Mode, list[index].NormalRoutingConfig) {
			list[index].SpeedFirstLatency = &SpeedFirstRuntimeSummary{
				RuntimeAvailable: true,
				DegradedCount:    counts[list[index].ID],
			}
		}
	}
}

// speedFirstDegradedCounts mirrors the facade dedupe: per (routeStrategyId,
// accountId) keep the latest degradedUntil and count per strategy.
func speedFirstDegradedCounts(items []SpeedFirstRuntimeItem) map[string]int {
	latest := map[string]SpeedFirstRuntimeItem{}
	for _, item := range items {
		key := item.Scope.RouteStrategyID + "|" + item.AccountID
		if current, ok := latest[key]; !ok || current.DegradedUntil < item.DegradedUntil {
			latest[key] = item
		}
	}
	counts := map[string]int{}
	for _, item := range latest {
		counts[item.Scope.RouteStrategyID]++
	}
	return counts
}

// optionsOptionsQuery mirrors the /options query parsing: comma-separated ids
// (deduped, max 50), trimmed keyword, integer limit clamped 1..100 (default
// 50), boolean activeOnly (default true).
func optionsQuery(r *http.Request) OptionsQuery {
	query := OptionsQuery{Limit: 50, ActiveOnly: true}
	if raw := r.URL.Query().Get("keyword"); strings.TrimSpace(raw) != "" {
		query.Keyword = strings.TrimSpace(raw)
	}
	if limit, ok := intQueryValue(r.URL.Query().Get("limit")); ok {
		query.Limit = limit
	}
	seen := map[string]bool{}
	for _, rawID := range r.URL.Query()["ids"] {
		for _, id := range strings.Split(rawID, ",") {
			id = strings.TrimSpace(id)
			if id == "" || seen[id] || len(query.IDs) >= 50 {
				continue
			}
			seen[id] = true
			query.IDs = append(query.IDs, id)
		}
	}
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("activeOnly"))) {
	case "1", "true", "yes":
		query.ActiveOnly = true
	case "0", "false", "no":
		query.ActiveOnly = false
	}
	return query
}

func (d *Deps) options(w http.ResponseWriter, r *http.Request, access AccessScope) {
	result, err := d.Store.ListOptionsPage(r.Context(), access, optionsQuery(r))
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, result, "")
}

func (d *Deps) editBasic(w http.ResponseWriter, r *http.Request, access AccessScope) {
	detail, err := d.Store.FindEditBasic(r.Context(), r.PathValue("id"), access)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if detail == nil {
		kernel.WriteError(w, http.StatusNotFound, "策略路由不存在")
		return
	}
	kernel.WriteOK(w, detail, "")
}

// speedFirstRuntime mirrors GET /:id/speed-first-runtime: the static disabled
// payload for non-speed-first strategies, the live runtime snapshot otherwise.
func (d *Deps) speedFirstRuntime(w http.ResponseWriter, r *http.Request, access AccessScope) {
	detail, err := d.Store.FindDetail(r.Context(), r.PathValue("id"), access)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if detail == nil {
		kernel.WriteError(w, http.StatusNotFound, "策略路由不存在")
		return
	}
	payload := map[string]any{
		"routeStrategyId":  detail.ID,
		"generatedAt":      d.Store.nowISO(),
		"enabled":          false,
		"runtimeAvailable": true,
		"degradedCount":    0,
		"items":            []any{},
	}
	if isNormalSpeedFirstRouteStrategy(detail.Mode, detail.NormalRoutingConfig) {
		ownerID := detail.SystemAccountID
		items, available, runtimeErr := d.Store.speedFirst.ListDegradedRuntime(r.Context(), ownerID, []string{detail.ID})
		payload["enabled"] = true
		payload["runtimeAvailable"] = runtimeErr == nil && available
		payload["degradedCount"] = 0
		payload["items"] = []any{}
		if runtimeErr == nil && available {
			deduped := dedupeSpeedFirstItemsByAccount(items)
			counts := map[string]int{}
			for _, item := range deduped {
				counts[item.Scope.RouteStrategyID]++
			}
			payload["degradedCount"] = counts[detail.ID]
			payload["items"] = deduped
		}
	}
	kernel.WriteOK(w, payload, "")
}

// dedupeSpeedFirstItemsByAccount mirrors dedupeRuntimeItemsByAccount: keep the
// latest degradedUntil per (routeStrategyId, accountId).
func dedupeSpeedFirstItemsByAccount(items []SpeedFirstRuntimeItem) []SpeedFirstRuntimeItem {
	latest := map[string]SpeedFirstRuntimeItem{}
	order := []string{}
	for _, item := range items {
		key := item.Scope.RouteStrategyID + "|" + item.AccountID
		if current, ok := latest[key]; !ok {
			order = append(order, key)
			latest[key] = item
		} else if current.DegradedUntil < item.DegradedUntil {
			latest[key] = item
		}
	}
	out := make([]SpeedFirstRuntimeItem, 0, len(order))
	for _, key := range order {
		out = append(out, latest[key])
	}
	return out
}

func (d *Deps) find(w http.ResponseWriter, r *http.Request, access AccessScope) {
	detail, err := d.Store.FindDetail(r.Context(), r.PathValue("id"), access)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if detail == nil {
		kernel.WriteError(w, http.StatusNotFound, "策略路由不存在")
		return
	}
	kernel.WriteOK(w, detail, "")
}

// createdEnvelope mirrors the {data} success envelope at 201 (Node
// res.status(201).json(ok(item))).
type createdEnvelope struct {
	Data any `json:"data"`
}

func writeCreated(w http.ResponseWriter, item *ListItem) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(createdEnvelope{Data: item})
}

func (d *Deps) mountGuardedCreate(selfOnly bool) http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "route_strategies.create",
		Actor:        actorResolver,
		Scope: func(r *http.Request) (any, error) {
			return strings.TrimSpace(r.URL.Query().Get("systemAccountId")), nil
		},
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{
				"owner": strings.TrimSpace(r.URL.Query().Get("systemAccountId")),
				"name":  kernel.TextField(kernel.BodyField(r, "name")),
			}, nil
		},
	})
	handler := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Node create parses the scope query after the guard (400
		// 系统账号 ID 不能为空 before the body schema runs).
		if !d.writeScopeQuery(w, r) {
			return
		}
		auth := authsys.AuthContextFrom(r)
		if auth == nil {
			kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		var body map[string]any
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		input, problem := parseCreateBody(body)
		if problem != "" {
			kernel.WriteBadRequest(w, problem)
			return
		}
		access := selfScope(r)
		if !selfOnly {
			access = adminScope(r)
		}
		item, err := d.Store.Create(r.Context(), input, access)
		if err != nil {
			d.writeMutationError(w, err)
			return
		}
		if d.Sink != nil {
			// Node logs the six fixed safeChange entries; the config entries
			// render 未设置 when their value is absent.
			changes := []authsys.OperationLogChange{
				{Field: "name", Label: "名称", After: item.Name},
				{Field: "mode", Label: "路由模式", After: item.Mode},
				{Field: "status", Label: "状态", After: item.Status},
				{Field: "groupBindings", Label: "绑定分组", After: summarizeBindings(input.Bindings)},
				{Field: "normalRoutingConfig", Label: "普通路由调度配置", After: changeText(item.NormalRoutingConfig)},
				{Field: "hybridRoutingConfig", Label: "混合智能路由配置", After: changeText(input.HybridConfigRaw)},
			}
			d.Sink.Record(authsys.OperationLogEntry{
				ActorSystemAccountID:          auth.SystemAccountID,
				ActorUsername:                 auth.Username,
				ActorDisplayName:              auth.DisplayName,
				ActorRole:                     auth.Role,
				OperationScopeSystemAccountID: item.OwnerSystemAccountID,
				Mode:                          operationMode(access),
				Module:                        "route_strategies",
				Action:                        "create",
				OperationKey:                  "route_strategies.create",
				ResourceType:                  "route_strategy",
				ResourceID:                    item.ID,
				ResourceName:                  item.Name,
				Summary:                       "创建策略路由：" + item.Name,
				Changes:                       changes,
				Viewers: []authsys.OperationLogViewer{
					{SystemAccountID: item.OwnerSystemAccountID, Reason: "resource_owner"},
				},
			}, r)
		}
		writeCreated(w, item)
	}))
	if selfOnly {
		return d.Auth.RequireSession(true)(handler)
	}
	return d.Auth.RequireAdmin(handler)
}

func (d *Deps) patch(w http.ResponseWriter, r *http.Request, access AccessScope) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	rawExpected, hasExpected := body["expectedUpdatedAt"]
	expected, isString := "", false
	if hasExpected {
		expected, isString = rawExpected.(string)
	}
	// rfc3339InstantSchema canonicalizes to UTC millis before the repository
	// CAS, so equivalent RFC3339 representations (+08:00 offset, varying
	// fractional digits) all match the stored canonical version.
	if !hasExpected || !isString {
		kernel.WriteBadRequest(w, "策略路由配置版本格式不正确")
		return
	}
	expected, validInstant := canonicalRFC3339Instant(expected)
	if !validInstant {
		kernel.WriteBadRequest(w, "策略路由配置版本格式不正确")
		return
	}
	input, problem := parsePatchBody(body)
	if problem != "" {
		kernel.WriteBadRequest(w, problem)
		return
	}
	if input.Empty() {
		kernel.WriteBadRequest(w, "请提供要修改的策略路由内容")
		return
	}
	result, err := d.Store.Patch(r.Context(), r.PathValue("id"), input, expected, access)
	if result == nil && err == nil {
		kernel.WriteError(w, http.StatusNotFound, "策略路由不存在")
		return
	}
	if err != nil {
		d.writeMutationError(w, err)
		return
	}
	// clearNormalRouteSpeedFirstRuntime after a runtime-relevant patch
	// (best-effort; cleanup failures never fail the request).
	if d.Store.speedFirst != nil && intersectsAny(result.ChangedFields, normalRouteSpeedFirstRuntimeFields) {
		_, _ = d.Store.speedFirst.ClearDegradedRuntime(r.Context(), result.ID)
	}
	if len(result.ChangedFields) > 0 && d.Sink != nil {
		changes := make([]authsys.OperationLogChange, 0, len(result.Changes))
		for _, change := range result.Changes {
			changes = append(changes, authsys.OperationLogChange{
				Field: change.Field, Label: patchFieldLabel(change.Field),
				Before: changeText(change.Before), After: changeText(change.After),
			})
		}
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID:          auth.SystemAccountID,
			ActorUsername:                 auth.Username,
			ActorDisplayName:              auth.DisplayName,
			ActorRole:                     auth.Role,
			OperationScopeSystemAccountID: result.OwnerSystemAccountID,
			Mode:                          operationMode(access),
			Module:                        "route_strategies",
			Action:                        "update",
			OperationKey:                  "route_strategies.update",
			ResourceType:                  "route_strategy",
			ResourceID:                    result.ID,
			ResourceName:                  result.ResourceName,
			Summary:                       "更新策略路由：" + result.ResourceName,
			Changes:                       changes,
			Viewers: []authsys.OperationLogViewer{
				{SystemAccountID: result.OwnerSystemAccountID, Reason: "resource_owner"},
			},
		}, r)
	}
	kernel.WriteOK(w, result, "")
}

func (d *Deps) remove(w http.ResponseWriter, r *http.Request, access AccessScope) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	id := r.PathValue("id")
	// Node loads the summary first for the operation log resource name.
	resourceName := d.Store.StrategyName(r.Context(), id, access)
	ownerFallback := access.ViewerID
	result, err := d.Store.Delete(r.Context(), id, access)
	if err != nil {
		// Node delete catch-all renders 400 with the error message; only the
		// missing-resource probe maps to 404.
		kernel.WriteBadRequest(w, err.Error())
		return
	}
	if !result.Deleted {
		kernel.WriteError(w, http.StatusNotFound, "策略路由不存在")
		return
	}
	ownerID := result.OwnerSystemAccountID
	if ownerID == "" {
		ownerID = ownerFallback
	}
	if resourceName == "" {
		resourceName = id
	}
	// clearNormalRouteSpeedFirstRuntime after a successful delete
	// (best-effort).
	if d.Store.speedFirst != nil {
		_, _ = d.Store.speedFirst.ClearDegradedRuntime(r.Context(), id)
	}
	if d.Sink != nil {
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID:          auth.SystemAccountID,
			ActorUsername:                 auth.Username,
			ActorDisplayName:              auth.DisplayName,
			ActorRole:                     auth.Role,
			OperationScopeSystemAccountID: ownerID,
			Mode:                          operationMode(access),
			Module:                        "route_strategies",
			Action:                        "delete",
			OperationKey:                  "route_strategies.delete",
			ResourceType:                  "route_strategy",
			ResourceID:                    id,
			ResourceName:                  resourceName,
			Summary:                       "删除策略路由：" + resourceName,
			Changes: []authsys.OperationLogChange{
				{Field: "deleted", Label: "删除状态", After: "true"},
			},
			Viewers: []authsys.OperationLogViewer{
				{SystemAccountID: ownerID, Reason: "resource_owner"},
			},
		}, r)
	}
	w.WriteHeader(http.StatusNoContent)
}

// StrategyName returns the visible name for the delete operation log
// (Node loads the summary before deleting).
func (s *Store) StrategyName(ctx context.Context, id string, access AccessScope) string {
	detail, err := s.FindDetail(ctx, id, access)
	if err != nil || detail == nil {
		return ""
	}
	return detail.Name
}

// writeMutationError maps store errors onto the Node route family contract:
// version conflicts → 409 + currentUpdatedAt; duplicate name (已存在) → 409;
// validation-cache invalidation failure → 500 (after the row is committed);
// everything else → 400 with the error message (Node catch-all
// badRequest(message) for create/patch).
func (d *Deps) writeMutationError(w http.ResponseWriter, err error) {
	var invalidation *ValidationCacheInvalidationError
	if errors.As(err, &invalidation) {
		kernel.WriteError(w, http.StatusInternalServerError, invalidation.Message)
		return
	}
	var conflict *VersionConflictError
	if errors.As(err, &conflict) {
		payload := map[string]any{"message": conflict.Message}
		if conflict.CurrentUpdatedAt != "" {
			payload["currentUpdatedAt"] = conflict.CurrentUpdatedAt
		}
		kernel.WriteJSON(w, http.StatusConflict, payload)
		return
	}
	var duplicate *ConflictError
	if errors.As(err, &duplicate) {
		kernel.WriteError(w, http.StatusConflict, duplicate.Message)
		return
	}
	kernel.WriteBadRequest(w, err.Error())
}

// normalRouteSpeedFirstRuntimeFields mirrors normalRouteSpeedFirstRuntimeFields
// (the cleanup trigger set; hybridRoutingConfig is not part of it).
var normalRouteSpeedFirstRuntimeFields = map[string]bool{
	"mode":                true,
	"status":              true,
	"groupBindings":       true,
	"normalRoutingConfig": true,
}

func intersectsAny(fields []string, allowed map[string]bool) bool {
	for _, field := range fields {
		if allowed[field] {
			return true
		}
	}
	return false
}

func actorResolver(r *http.Request) string {
	if auth := authsys.AuthContextFrom(r); auth != nil {
		return auth.SystemAccountID
	}
	return "anonymous"
}

// patchFieldLabel mirrors routeStrategyPatchFieldLabel.
func patchFieldLabel(field string) string {
	switch field {
	case "name":
		return "名称"
	case "description":
		return "说明"
	case "mode":
		return "路由模式"
	case "status":
		return "状态"
	case "groupBindings":
		return "绑定分组"
	case "normalRoutingConfig":
		return "普通路由调度配置"
	case "hybridRoutingConfig":
		return "混合智能路由配置"
	default:
		return field
	}
}

// changeText renders log change values (Node safeChange passes raw values and
// the pipeline stringifies them).
func changeText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}

// summarizeBindings renders the groupBindings log change for create. Node logs
// parsed.data.groupBindings verbatim: groupId always, priority/weight/status
// only when the caller actually sent them, in schema declaration order.
func summarizeBindings(bindings []BindingInput) string {
	entries := make([]bindingLogEntry, 0, len(bindings))
	for _, binding := range bindings {
		entry := bindingLogEntry{GroupID: binding.GroupID}
		if binding.priorityProvided {
			entry.Priority = binding.Priority
		}
		if binding.weightProvided {
			entry.Weight = binding.Weight
		}
		if binding.statusProvided {
			entry.Status = binding.Status
		}
		entries = append(entries, entry)
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// bindingLogEntry keeps the Node field order (groupId, priority, weight,
// status) with omitted-by-presence pointers.
type bindingLogEntry struct {
	GroupID  string `json:"groupId"`
	Priority *int   `json:"priority,omitempty"`
	Weight   *int   `json:"weight,omitempty"`
	Status   string `json:"status,omitempty"`
}
