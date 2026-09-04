package apikeys

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// Deps bundles the M07 slice collaborators.
type Deps struct {
	Store *Store
	Auth  *authsys.Deps
	Sink  authsys.OperationLogSink
}

// Mount wires the api-keys route family: admin surface on /api-keys
// (requireAdmin) and the forceSelfAccessScope mirror on /my-api-keys (Node
// mounts the same router at both prefixes; my-* drops any systemAccountId
// query and pins the scope to the caller).
func (d *Deps) Mount(k *kernel.Kernel) {
	prefix := "/__aisys__/api"
	// Admin surface.
	k.Register("GET "+prefix+"/api-keys", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.list(w, r, adminScope(r))
	})))
	k.Register("GET "+prefix+"/api-keys/{id}", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.find(w, r, adminScope(r))
	})))
	k.Register("GET "+prefix+"/api-keys/{id}/secret", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.reveal(w, r, adminScope(r))
	})))
	k.Register("POST "+prefix+"/api-keys", d.mountGuardedCreate(false))
	k.Register("POST "+prefix+"/api-keys/{id}/refresh-key", d.mountGuardedRefresh(false))
	k.Register("DELETE "+prefix+"/api-keys/{id}", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.remove(w, r, adminScope(r))
	})))

	// Self surface (forceSelfAccessScope: role forced to user, caller-scoped).
	k.Register("GET "+prefix+"/my-api-keys", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.list(w, r, selfScope(r))
	})))
	k.Register("GET "+prefix+"/my-api-keys/{id}", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.find(w, r, selfScope(r))
	})))
	k.Register("GET "+prefix+"/my-api-keys/{id}/secret", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.reveal(w, r, selfScope(r))
	})))
	k.Register("POST "+prefix+"/my-api-keys", d.mountGuardedCreate(true))
	k.Register("POST "+prefix+"/my-api-keys/{id}/refresh-key", d.mountGuardedRefresh(true))
	k.Register("DELETE "+prefix+"/my-api-keys/{id}", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.remove(w, r, selfScope(r))
	})))
}

// adminScope mirrors getRequestAccessScope(query.systemAccountId) for admins.
func adminScope(r *http.Request) AccessScope {
	auth := authsys.AuthContextFrom(r)
	return AccessScope{ViewerID: auth.SystemAccountID, IsAdmin: true, FilterID: normalizeScopeFilter(r.URL.Query().Get("systemAccountId"))}
}

// selfScope mirrors forceSelfAccessScope: the query-scoped account id is
// dropped and the scope is pinned to the authenticated caller.
func selfScope(r *http.Request) AccessScope {
	auth := authsys.AuthContextFrom(r)
	return AccessScope{ViewerID: auth.SystemAccountID}
}

func normalizeScopeFilter(value string) string {
	filter := strings.TrimSpace(value)
	if filter == "all" {
		return ""
	}
	return filter
}

// scopeQueryOK mirrors parseRequestScopeQuery: an explicit systemAccountId
// query value must survive trimming.
func scopeQueryOK(r *http.Request) bool {
	values := r.URL.Query()["systemAccountId"]
	if len(values) == 0 {
		return true
	}
	return strings.TrimSpace(values[0]) != ""
}

func operationMode(access AccessScope) string {
	if access.IsAdmin {
		return "admin"
	}
	return "self"
}

func setNoStoreHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func (d *Deps) list(w http.ResponseWriter, r *http.Request, access AccessScope) {
	// Node's list route reads the filter directly (no scope-query parsing):
	// a blank systemAccountId is simply ignored.
	query := r.URL.Query()
	options := ListOptions{
		PageSet:         queryHasInteger(query.Get("page")),
		Page:            queryInteger(query.Get("page")),
		PageSizeSet:     queryHasInteger(query.Get("pageSize")),
		PageSize:        queryInteger(query.Get("pageSize")),
		Keyword:         strings.TrimSpace(query.Get("keyword")),
		Status:          apiKeyStatusQueryValue(query.Get("status")),
		RouteStrategyID: strings.TrimSpace(query.Get("routeStrategyId")),
	}
	result, err := d.Store.ListPage(r.Context(), access, options)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, result, "")
}

func (d *Deps) find(w http.ResponseWriter, r *http.Request, access AccessScope) {
	if !scopeQueryOK(r) {
		kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
		return
	}
	detail, err := d.Store.FindDetail(r.Context(), r.PathValue("id"), access)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if detail == nil {
		kernel.WriteError(w, http.StatusNotFound, "API Key 不存在")
		return
	}
	kernel.WriteOK(w, detail, "")
}

func (d *Deps) reveal(w http.ResponseWriter, r *http.Request, access AccessScope) {
	if !scopeQueryOK(r) {
		kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
		return
	}
	auth := authsys.AuthContextFrom(r)
	record, err := d.Store.FindSecret(r.Context(), r.PathValue("id"), access)
	if err != nil {
		var unavailable *SecretUnavailableError
		if errors.As(err, &unavailable) {
			// Seal failures never leak key material.
			kernel.WriteError(w, http.StatusInternalServerError, "API Key 密钥读取失败")
		} else {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		}
		return
	}
	if record == nil {
		kernel.WriteError(w, http.StatusNotFound, "API Key 不存在")
		return
	}
	if d.Sink != nil {
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID:          auth.SystemAccountID,
			ActorUsername:                 auth.Username,
			ActorDisplayName:              auth.DisplayName,
			ActorRole:                     auth.Role,
			OperationScopeSystemAccountID: record.SystemAccountID,
			Mode:                          operationMode(access),
			Module:                        "api_keys",
			Action:                        "reveal_secret",
			OperationKey:                  "api_keys.reveal_secret",
			ResourceType:                  "api_key",
			ResourceID:                    record.ID,
			ResourceName:                  record.Name,
			Summary:                       "查看 API Key 完整密钥：" + record.Name,
			Changes: []authsys.OperationLogChange{
				{Field: "key", Label: "密钥标识", Before: "未设置", After: "已变更", Sensitive: true},
			},
			Viewers: []authsys.OperationLogViewer{
				{SystemAccountID: record.SystemAccountID, Reason: "resource_owner"},
			},
		}, r)
	}
	setNoStoreHeaders(w)
	kernel.WriteOK(w, map[string]any{"key": record.Key}, "")
}

// mutationEnvelope mirrors res.status(201).json(ok(data, message)).
type mutationEnvelope struct {
	Data    any    `json:"data"`
	Message string `json:"message,omitempty"`
}

func writeMutationOK(w http.ResponseWriter, status int, data any, message string) {
	setNoStoreHeaders(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(mutationEnvelope{Data: data, Message: message})
}

func (d *Deps) mountGuardedCreate(selfOnly bool) http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "api_keys.create",
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
		if !scopeQueryOK(r) {
			kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
			return
		}
		var body map[string]any
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		input, message := createBody(body)
		if message != "" {
			kernel.WriteBadRequest(w, message)
			return
		}
		access := selfScope(r)
		if !selfOnly {
			access = adminScope(r)
		}
		result, meta, err := d.Store.Create(r.Context(), input, access)
		if err != nil {
			d.writeMutationError(w, err)
			return
		}
		if d.Sink != nil {
			scheduleChange := authsys.OperationLogChange{Field: "availabilitySchedule", Label: "时间计划"}
			if raw, ok := ScheduleJSON(meta.AvailabilitySchedule); ok && len(raw) <= 500 {
				scheduleChange.After = raw
			}
			d.Sink.Record(authsys.OperationLogEntry{
				ActorSystemAccountID:          auth.SystemAccountID,
				ActorUsername:                 auth.Username,
				ActorDisplayName:              auth.DisplayName,
				ActorRole:                     auth.Role,
				OperationScopeSystemAccountID: meta.OwnerSystemAccountID,
				Mode:                          operationMode(access),
				Module:                        "api_keys",
				Action:                        "create",
				OperationKey:                  "api_keys.create",
				ResourceType:                  "api_key",
				ResourceID:                    result.ID,
				ResourceName:                  meta.Name,
				Summary:                       "创建 API Key：" + meta.Name,
				Changes: []authsys.OperationLogChange{
					{Field: "name", Label: "名称", After: meta.Name},
					{Field: "status", Label: "状态", After: meta.Status},
					{Field: "routeStrategyId", Label: "策略路由", After: meta.RouteStrategyID},
					scheduleChange,
					{Field: "key", Label: "密钥标识", Before: "未设置", After: "已变更", Sensitive: true},
				},
				Viewers: []authsys.OperationLogViewer{
					{SystemAccountID: meta.OwnerSystemAccountID, Reason: "resource_owner"},
				},
			}, r)
		}
		writeMutationOK(w, http.StatusCreated, result, "API Key 已创建，请立即复制完整密钥")
	}))
	if selfOnly {
		return d.Auth.RequireSession(true)(handler)
	}
	return d.Auth.RequireAdmin(handler)
}

func (d *Deps) mountGuardedRefresh(selfOnly bool) http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "api_keys.refresh_key",
		Actor:        actorResolver,
		Scope: func(r *http.Request) (any, error) {
			return strings.TrimSpace(r.URL.Query().Get("systemAccountId")), nil
		},
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{
				"owner": strings.TrimSpace(r.URL.Query().Get("systemAccountId")),
				"id":    strings.TrimSpace(r.PathValue("id")),
			}, nil
		},
	})
	handler := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := authsys.AuthContextFrom(r)
		if auth == nil {
			kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		if !scopeQueryOK(r) {
			kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
			return
		}
		access := selfScope(r)
		if !selfOnly {
			access = adminScope(r)
		}
		outcome, err := d.Store.RefreshSecret(r.Context(), r.PathValue("id"), access)
		if err != nil {
			d.writeMutationError(w, err)
			return
		}
		if outcome == nil {
			kernel.WriteError(w, http.StatusNotFound, "API Key 不存在")
			return
		}
		if d.Sink != nil {
			d.Sink.Record(authsys.OperationLogEntry{
				ActorSystemAccountID:          auth.SystemAccountID,
				ActorUsername:                 auth.Username,
				ActorDisplayName:              auth.DisplayName,
				ActorRole:                     auth.Role,
				OperationScopeSystemAccountID: outcome.OwnerSystemAccountID,
				Mode:                          operationMode(access),
				Module:                        "api_keys",
				Action:                        "refresh_key",
				OperationKey:                  "api_keys.refresh_key",
				ResourceType:                  "api_key",
				ResourceID:                    outcome.Result.ID,
				ResourceName:                  outcome.ResourceName,
				Summary:                       "刷新 API Key 密钥：" + outcome.ResourceName,
				Changes: []authsys.OperationLogChange{
					{Field: "key", Label: "密钥标识", Before: "已设置", After: "已变更", Sensitive: true},
				},
				Viewers: []authsys.OperationLogViewer{
					{SystemAccountID: outcome.OwnerSystemAccountID, Reason: "resource_owner"},
				},
			}, r)
		}
		if outcome.ValidationCacheError != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "API Key validation cache 失效失败")
			return
		}
		writeMutationOK(w, http.StatusOK, outcome.Result, "API Key 密钥已刷新，请立即复制完整密钥")
	}))
	if selfOnly {
		return d.Auth.RequireSession(true)(handler)
	}
	return d.Auth.RequireAdmin(handler)
}

func (d *Deps) remove(w http.ResponseWriter, r *http.Request, access AccessScope) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	if !scopeQueryOK(r) {
		kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
		return
	}
	result, err := d.Store.Delete(r.Context(), r.PathValue("id"), access)
	if err != nil {
		d.writeMutationError(w, err)
		return
	}
	if !result.Deleted {
		kernel.WriteError(w, http.StatusNotFound, "API Key 不存在")
		return
	}
	resourceName := result.ResourceName
	if resourceName == "" {
		resourceName = r.PathValue("id")
	}
	if d.Sink != nil {
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID:          auth.SystemAccountID,
			ActorUsername:                 auth.Username,
			ActorDisplayName:              auth.DisplayName,
			ActorRole:                     auth.Role,
			OperationScopeSystemAccountID: result.OwnerSystemAccountID,
			Mode:                          operationMode(access),
			Module:                        "api_keys",
			Action:                        "delete",
			OperationKey:                  "api_keys.delete",
			ResourceType:                  "api_key",
			ResourceID:                    r.PathValue("id"),
			ResourceName:                  resourceName,
			Summary:                       "删除 API Key：" + resourceName,
			Changes: []authsys.OperationLogChange{
				{Field: "deleted", Label: "删除状态", Before: "false", After: "true"},
			},
			Viewers: []authsys.OperationLogViewer{
				{SystemAccountID: result.OwnerSystemAccountID, Reason: "resource_owner"},
			},
		}, r)
	}
	if result.ValidationCacheError != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "API Key validation cache 失效失败")
		return
	}
	setNoStoreHeaders(w)
	w.WriteHeader(http.StatusNoContent)
}

// writeMutationError maps store errors onto the Node route family contract:
// name duplicates and delete guards → 409; the known bad-request message set
// (strategy binding, expiry, quota/schedule normalization) → 400; the rest →
// 500.
func (d *Deps) writeMutationError(w http.ResponseWriter, err error) {
	var conflict *ConflictError
	var validation *ValidationError
	if errors.As(err, &conflict) {
		kernel.WriteError(w, http.StatusConflict, conflict.Message)
		return
	}
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

// createBody mirrors apiKeyCreateSchema.strict(): the allowed key set plus
// per-field type contracts. Returns (input, firstIssueMessage); a non-empty
// message rejects the payload with 400. The required-name issue is evaluated
// first, matching the zod shape-parse-before-strict-refine issue order.
func createBody(body map[string]any) (CreateInput, string) {
	input := CreateInput{}
	name, err := bodyString(body, "name")
	if err != nil || strings.TrimSpace(name) == "" {
		return CreateInput{}, "请填写 API Key 名称"
	}
	for key := range body {
		switch key {
		case "name", "description", "routeStrategyId", "status", "expiresAt", "quotaLimits", "availabilitySchedule":
		default:
			return CreateInput{}, "API Key 参数无效"
		}
	}
	input.Name = name
	if value, exists := body["description"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return CreateInput{}, "API Key 参数无效"
		}
		if len([]rune(strings.TrimSpace(text))) > 200 {
			return CreateInput{}, "API Key 说明不能超过 200 个字符"
		}
		input.Description = &text
	}
	if value, exists := body["routeStrategyId"]; exists {
		text, isString := value.(string)
		if !isString || strings.TrimSpace(text) == "" {
			// routeStrategyId is optional() but not nullable() in the schema.
			return CreateInput{}, "请选择策略路由"
		}
		input.RouteStrategyID = &text
	}
	if value, exists := body["status"]; exists {
		text, isString := value.(string)
		if !isString || (text != "active" && text != "disabled") {
			return CreateInput{}, "API Key 参数无效"
		}
		input.Status = &text
	}
	if value, exists := body["expiresAt"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return CreateInput{}, "API Key 参数无效"
		}
		input.ExpiresAt = &text
	}
	if value, exists := body["quotaLimits"]; exists && value != nil {
		object, isObject := value.(map[string]any)
		if !isObject {
			return CreateInput{}, "API Key 参数无效"
		}
		input.QuotaLimits = object
	}
	if value, exists := body["availabilitySchedule"]; exists && value != nil {
		object, isObject := value.(map[string]any)
		if !isObject {
			return CreateInput{}, "API Key 参数无效"
		}
		input.AvailabilitySchedule = object
	}
	return input, ""
}

func bodyString(body map[string]any, key string) (string, error) {
	value, exists := body[key]
	if !exists {
		return "", errors.New("missing")
	}
	text, isString := value.(string)
	if !isString {
		return "", errors.New("not a string")
	}
	return text, nil
}

// apiKeyStatusQueryValue mirrors apiKeyStatusQueryValue: anything beyond
// active/disabled/all is treated as absent.
func apiKeyStatusQueryValue(value string) string {
	switch strings.TrimSpace(value) {
	case "active", "disabled", "all":
		text := strings.TrimSpace(value)
		if text == "all" {
			return ""
		}
		return text
	default:
		return ""
	}
}

// queryHasInteger/queryInteger mirror integerQueryValue: blank or non-integer
// text counts as absent.
func queryHasInteger(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}
	_, err := strconv.Atoi(trimmed)
	return err == nil
}

func queryInteger(raw string) int {
	value, _ := strconv.Atoi(strings.TrimSpace(raw))
	return value
}
