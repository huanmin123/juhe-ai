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
	"strconv"
	"strings"
	"time"

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
// and the forceSelfAccessScope mirror on /my-route-strategies.
func (d *Deps) Mount(k *kernel.Kernel) {
	prefix := "/__aisys__/api"
	// Admin surface (requireAdmin).
	k.Register("GET "+prefix+"/route-strategies", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.list(w, r, adminScope(r))
	})))
	k.Register("GET "+prefix+"/route-strategies/{id}", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.find(w, r, adminScope(r))
	})))
	k.Register("POST "+prefix+"/route-strategies", d.mountGuardedCreate(false))
	k.Register("PATCH "+prefix+"/route-strategies/{id}", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.patch(w, r, adminScope(r))
	})))
	k.Register("DELETE "+prefix+"/route-strategies/{id}", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.remove(w, r, adminScope(r))
	})))

	// Self surface (forceSelfAccessScope: scope pinned to the caller).
	k.Register("GET "+prefix+"/my-route-strategies", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.list(w, r, selfScope(r))
	})))
	k.Register("GET "+prefix+"/my-route-strategies/{id}", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.find(w, r, selfScope(r))
	})))
	k.Register("POST "+prefix+"/my-route-strategies", d.mountGuardedCreate(true))
	k.Register("PATCH "+prefix+"/my-route-strategies/{id}", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.patch(w, r, selfScope(r))
	})))
	k.Register("DELETE "+prefix+"/my-route-strategies/{id}", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.remove(w, r, selfScope(r))
	})))
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

// queryInt mirrors integerQueryValue: blank or non-integer text → ok=false
// (caller applies the default).
func queryInt(raw string) (int, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, false
	}
	value, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, false
	}
	return value, true
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
	kernel.WriteOK(w, result, "")
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
			bindingSummary := summarizeBindings(input.Bindings)
			changes := []authsys.OperationLogChange{
				{Field: "name", Label: "名称", After: item.Name},
				{Field: "mode", Label: "路由模式", After: item.Mode},
				{Field: "status", Label: "状态", After: item.Status},
				{Field: "groupBindings", Label: "绑定分组", After: bindingSummary},
			}
			if item.NormalRoutingConfig != nil && item.NormalRoutingConfig.SchedulingPreference != defaultNormalSchedulingPreference {
				changes = append(changes, authsys.OperationLogChange{
					Field: "normalRoutingConfig", Label: "普通路由调度配置",
					After: changeText(item.NormalRoutingConfig),
				})
			}
			if input.HasHybridConfig && item.Mode == ModeHybridSmart {
				changes = append(changes, authsys.OperationLogChange{
					Field: "hybridRoutingConfig", Label: "混合智能路由配置",
					After: changeText(input.HybridConfigRaw),
				})
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
	if !hasExpected || !isString || strings.TrimSpace(expected) == "" || !isRFC3339Instant(expected) {
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
		var validation *ValidationError
		if errors.As(err, &validation) {
			kernel.WriteBadRequest(w, validation.Message)
			return
		}
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
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
// validation → 400; everything else → 500.
func (d *Deps) writeMutationError(w http.ResponseWriter, err error) {
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
	var validation *ValidationError
	if errors.As(err, &validation) {
		kernel.WriteBadRequest(w, validation.Message)
		return
	}
	kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
}

func actorResolver(r *http.Request) string {
	if auth := authsys.AuthContextFrom(r); auth != nil {
		return auth.SystemAccountID
	}
	return "anonymous"
}

func isRFC3339Instant(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	return err == nil
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

// summarizeBindings renders the groupBindings log change for create.
func summarizeBindings(bindings []BindingInput) string {
	entries := make([]map[string]any, 0, len(bindings))
	for _, binding := range bindings {
		entry := map[string]any{"groupId": binding.GroupID, "status": binding.Status}
		if binding.Priority != nil {
			entry["priority"] = *binding.Priority
		}
		if binding.Weight != nil {
			entry["weight"] = *binding.Weight
		}
		entries = append(entries, entry)
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		return ""
	}
	return string(encoded)
}
