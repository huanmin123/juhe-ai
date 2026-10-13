// Route family: /scheduling-preferences (admin) + /my-scheduling-preferences
// (self via RequireSession). Mirrors the routestrategies dual-surface Mount:
// pagination with bound-key counts, options with ids[] array parsing, guarded
// create, optimistic-lock patch and always-success delete with same-transaction
// binding reset to the builtin default (v2 用户裁决修订第 5 条), with operation
// logs (Module=scheduling_preferences) and the post-commit gateway runtime
// invalidation handled in the store (接口契约「调度偏好」章 12 端点).
package schedulingpreferences

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// Deps bundles the slice collaborators; all mirror the routestrategies Deps.
type Deps struct {
	Store *Store
	Auth  *authsys.Deps
	Sink  authsys.OperationLogSink
	// Log 可选（nil 安全）：mutation 兜底分支的原始错误文本经 localize 改写后
	// 客户端不可见，这里提供落点，未接线时静默。
	Log *slog.Logger
}

// Mount wires the family: admin surface on /scheduling-preferences
// (RequireAdmin) and the session-pinned mirror on /my-scheduling-preferences
// (RequireSession(true) + selfScope). Reads carry the builtin-row global
// exception; writes reject builtin rows with 403 in the store.
func (d *Deps) Mount(k *kernel.Kernel) {
	prefix := "/__aisys__/api"
	// Admin surface (requireAdmin).
	k.Register("GET "+prefix+"/scheduling-preferences", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.list(w, r, adminScope(r))
	})))
	k.Register("GET "+prefix+"/scheduling-preferences/options", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.options(w, r, adminScope(r))
	})))
	k.Register("GET "+prefix+"/scheduling-preferences/{id}", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !d.writeScopeQuery(w, r) {
			return
		}
		d.find(w, r, adminScope(r))
	})))
	k.Register("POST "+prefix+"/scheduling-preferences", d.mountGuardedCreate(false))
	k.Register("PATCH "+prefix+"/scheduling-preferences/{id}", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !d.writeScopeQuery(w, r) {
			return
		}
		d.patch(w, r, adminScope(r))
	})))
	k.Register("DELETE "+prefix+"/scheduling-preferences/{id}", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !d.writeScopeQuery(w, r) {
			return
		}
		d.remove(w, r, adminScope(r))
	})))

	// Self surface (session pinned: 伪造 systemAccountId 一律忽略).
	k.Register("GET "+prefix+"/my-scheduling-preferences", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.list(w, r, selfScope(r))
	})))
	k.Register("GET "+prefix+"/my-scheduling-preferences/options", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.options(w, r, selfScope(r))
	})))
	k.Register("GET "+prefix+"/my-scheduling-preferences/{id}", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !d.writeScopeQuery(w, r) {
			return
		}
		d.find(w, r, selfScope(r))
	})))
	k.Register("POST "+prefix+"/my-scheduling-preferences", d.mountGuardedCreate(true))
	k.Register("PATCH "+prefix+"/my-scheduling-preferences/{id}", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !d.writeScopeQuery(w, r) {
			return
		}
		d.patch(w, r, selfScope(r))
	})))
	k.Register("DELETE "+prefix+"/my-scheduling-preferences/{id}", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !d.writeScopeQuery(w, r) {
			return
		}
		d.remove(w, r, selfScope(r))
	})))
}

// writeScopeQuery mirrors the routestrategies detail/mutation probe: a
// present-but-blank ?systemAccountId= is a schema failure (400 系统账号 ID
// 不能为空) before any business query runs. List/options reads read the scope
// directly.
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

// adminScope mirrors the routestrategies admin read scope: 未传或 all 为全局
// 视图，指定 ID 时收窄（内置行始终可见，由 owner clause 的 IS NULL 臂保证）.
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

func (d *Deps) list(w http.ResponseWriter, r *http.Request, access AccessScope) {
	options := ListOptions{Keyword: strings.TrimSpace(r.URL.Query().Get("keyword"))}
	// page 默认 1、pageSize 默认 50（显式非法/越界值由 store 按 1..200 归一）。
	options.Page = 1
	options.PageSize = 50
	if page, ok := intQueryValue(r.URL.Query().Get("page")); ok {
		options.Page = page
	}
	if pageSize, ok := intQueryValue(r.URL.Query().Get("pageSize")); ok {
		options.PageSize = pageSize
	}
	switch strings.TrimSpace(r.URL.Query().Get("chatMode")) {
	case ChatModeCostFirst, ChatModeSpeedFirst:
		options.ChatMode = strings.TrimSpace(r.URL.Query().Get("chatMode"))
	}
	// status（enabled/disabled）启停过滤已随 v2 用户裁决修订第 4 条退役：查询
	// 参数不再读取（无停用态，全部行可见）。
	result, err := d.Store.ListPage(r.Context(), access, options)
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "服务器内部错误", err)
		return
	}
	kernel.WriteOK(w, result, "")
}

// optionsQuery mirrors the /options query parsing: comma-separated and
// repeated `ids`/`ids[]` params (axios 默认数组序列化 ids[]=a&ids[]=b，去空白、
// 去重、最多 50), trimmed keyword and integer limit clamped 1..100 (default
// 50). activeOnly 启停过滤已随 v2 用户裁决修订第 4 条退役：查询参数不再读取
// （无停用态，全部行可选）.
func optionsQuery(r *http.Request) OptionsQuery {
	query := OptionsQuery{Limit: 50}
	if raw := r.URL.Query().Get("keyword"); strings.TrimSpace(raw) != "" {
		query.Keyword = strings.TrimSpace(raw)
	}
	if limit, ok := intQueryValue(r.URL.Query().Get("limit")); ok {
		query.Limit = limit
	}
	seen := map[string]bool{}
	appendID := func(rawID string) {
		for _, id := range strings.Split(rawID, ",") {
			id = strings.TrimSpace(id)
			if id == "" || seen[id] || len(query.IDs) >= 50 {
				continue
			}
			seen[id] = true
			query.IDs = append(query.IDs, id)
		}
	}
	for _, rawID := range r.URL.Query()["ids"] {
		appendID(rawID)
	}
	for _, rawID := range r.URL.Query()["ids[]"] {
		appendID(rawID)
	}
	return query
}

func (d *Deps) options(w http.ResponseWriter, r *http.Request, access AccessScope) {
	result, err := d.Store.ListOptionsPage(r.Context(), access, optionsQuery(r))
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "服务器内部错误", err)
		return
	}
	kernel.WriteOK(w, result, "")
}

func (d *Deps) find(w http.ResponseWriter, r *http.Request, access AccessScope) {
	item, err := d.Store.Find(r.Context(), r.PathValue("id"), access)
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "服务器内部错误", err)
		return
	}
	if item == nil {
		kernel.WriteError(w, http.StatusNotFound, "调度偏好不存在")
		return
	}
	kernel.WriteOK(w, item, "")
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
		OperationKey: "scheduling_preferences.create",
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
			// 管理侧必须显式指定目标 owner（缺失或空值返回 400 系统账号 ID
			// 不能为空，不支持 all），不得回退为管理员自身归属。
			owner := strings.TrimSpace(r.URL.Query().Get("systemAccountId"))
			if owner == "" || owner == "all" {
				kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
				return
			}
			access = AccessScope{ViewerID: auth.SystemAccountID, IsAdmin: true, FilterID: owner}
		}
		item, err := d.Store.Create(r.Context(), input, access)
		if err != nil {
			d.writeMutationError(w, err)
			return
		}
		if d.Sink != nil {
			changes := []authsys.OperationLogChange{
				{Field: "name", Label: "名称", After: item.Name},
				{Field: "chatMode", Label: "对话形态", After: item.ChatMode},
			}
			if item.ChatSpeedFirstConfig != nil {
				changes = append(changes, authsys.OperationLogChange{
					Field: "chatSpeedFirstConfig", Label: "速度参数", After: changeText(item.ChatSpeedFirstConfig),
				})
			}
			d.Sink.Record(authsys.OperationLogEntry{
				ActorSystemAccountID:          auth.SystemAccountID,
				ActorUsername:                 auth.Username,
				ActorDisplayName:              auth.DisplayName,
				ActorRole:                     auth.Role,
				OperationScopeSystemAccountID: item.OwnerSystemAccountID,
				Mode:                          operationMode(access),
				Module:                        "scheduling_preferences",
				Action:                        "create",
				OperationKey:                  "scheduling_preferences.create",
				ResourceType:                  "scheduling_preference",
				ResourceID:                    item.ID,
				ResourceName:                  item.Name,
				Summary:                       "创建调度偏好：" + item.Name,
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
	// canonicalRFC3339Instant 归一化等价 RFC3339 表示（+08:00 offset、不同毫
	// 秒位数）到 UTC millis 再与存储版本比对。
	if !hasExpected || !isString {
		kernel.WriteBadRequest(w, "调度偏好配置版本格式不正确")
		return
	}
	expected, validInstant := canonicalRFC3339Instant(expected)
	if !validInstant {
		kernel.WriteBadRequest(w, "调度偏好配置版本格式不正确")
		return
	}
	input, problem := parsePatchBody(body)
	if problem != "" {
		kernel.WriteBadRequest(w, problem)
		return
	}
	if input.Empty() {
		kernel.WriteBadRequest(w, "请提供要修改的调度偏好内容")
		return
	}
	result, err := d.Store.Patch(r.Context(), r.PathValue("id"), input, expected, access)
	if result == nil && err == nil {
		kernel.WriteError(w, http.StatusNotFound, "调度偏好不存在")
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
			Module:                        "scheduling_preferences",
			Action:                        "update",
			OperationKey:                  "scheduling_preferences.update",
			ResourceType:                  "scheduling_preference",
			ResourceID:                    result.ID,
			ResourceName:                  result.ResourceName,
			Summary:                       "更新调度偏好：" + result.ResourceName,
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
	// 先读可见名（删除成功后行已不在，操作日志需要资源名）。
	resourceName := d.Store.PreferenceName(r.Context(), id, access)
	result, err := d.Store.Delete(r.Context(), id, access)
	if err != nil {
		d.writeMutationError(w, err)
		return
	}
	if !result.Deleted {
		kernel.WriteError(w, http.StatusNotFound, "调度偏好不存在")
		return
	}
	ownerID := result.OwnerSystemAccountID
	if resourceName == "" {
		resourceName = result.Name
	}
	if resourceName == "" {
		resourceName = id
	}
	if d.Sink != nil {
		// v2 用户裁决修订第 5 条：删除总是成功并自动解绑——操作日志记录受影
		// 响 Key 数（resetBindings，语义=这些 Key 的绑定已在同事务内重置回内
		// 置默认「默认调度偏好」）。
		changes := []authsys.OperationLogChange{
			{Field: "deleted", Label: "删除状态", After: "true"},
		}
		summary := "删除调度偏好：" + resourceName
		if result.ResetBindings > 0 {
			changes = append(changes, authsys.OperationLogChange{
				Field: "resetBindings", Label: "重置绑定 Key 数", After: strconv.Itoa(result.ResetBindings),
			})
			summary += fmt.Sprintf("（%d 个绑定 Key 重置为默认调度偏好）", result.ResetBindings)
		}
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID:          auth.SystemAccountID,
			ActorUsername:                 auth.Username,
			ActorDisplayName:              auth.DisplayName,
			ActorRole:                     auth.Role,
			OperationScopeSystemAccountID: ownerID,
			Mode:                          operationMode(access),
			Module:                        "scheduling_preferences",
			Action:                        "delete_binding_reset",
			OperationKey:                  "scheduling_preferences.delete",
			ResourceType:                  "scheduling_preference",
			ResourceID:                    id,
			ResourceName:                  resourceName,
			Summary:                       summary,
			Changes:                       changes,
			Viewers: []authsys.OperationLogViewer{
				{SystemAccountID: ownerID, Reason: "resource_owner"},
			},
		}, r)
	}
	// v2 用户裁决修订第 5 条：删除总是成功，响应 data 携带受影响 Key 数。
	kernel.WriteOK(w, map[string]any{"resetBindings": result.ResetBindings}, "")
}

// PreferenceName returns the visible name for the delete operation log (the
// row is loaded before the delete).
func (s *Store) PreferenceName(ctx context.Context, id string, access AccessScope) string {
	item, err := s.Find(ctx, id, access)
	if err != nil || item == nil {
		return ""
	}
	return item.Name
}

// writeMutationError maps store errors onto the contract: builtin write → 403;
// version conflict → 409 + currentUpdatedAt; duplicate name (已存在) → 409;
// validation messages → 400; everything else → 400 with the raw message
// (Log 非 nil 时补一条 WARN 保留原始文本，localize 中间件会把非中文兜底文案
// 改写为「请求参数无效」). 删除 409 referenceCount 拒绝路径已随 v2 用户裁决修
// 订第 5 条移除（删除总是成功并自动解绑）.
func (d *Deps) writeMutationError(w http.ResponseWriter, err error) {
	var builtin *BuiltinWriteError
	if errors.As(err, &builtin) {
		kernel.WriteError(w, http.StatusForbidden, builtin.Message)
		return
	}
	var conflict *routestrategies.VersionConflictError
	if errors.As(err, &conflict) {
		payload := map[string]any{"message": conflict.Message}
		if conflict.CurrentUpdatedAt != "" {
			payload["currentUpdatedAt"] = conflict.CurrentUpdatedAt
		}
		kernel.WriteJSON(w, http.StatusConflict, payload)
		return
	}
	var duplicate *routestrategies.ConflictError
	if errors.As(err, &duplicate) {
		kernel.WriteError(w, http.StatusConflict, duplicate.Message)
		return
	}
	var validation *routestrategies.ValidationError
	if errors.As(err, &validation) {
		kernel.WriteBadRequest(w, validation.Message)
		return
	}
	if d.Log != nil {
		d.Log.Warn("scheduling_preference_mutation_rejected", "error", err.Error())
	}
	kernel.WriteBadRequest(w, err.Error())
}

func actorResolver(r *http.Request) string {
	if auth := authsys.AuthContextFrom(r); auth != nil {
		return auth.SystemAccountID
	}
	return "anonymous"
}

// patchFieldLabel mirrors the scheduling-preference change labels.
func patchFieldLabel(field string) string {
	switch field {
	case "name":
		return "名称"
	case "chatMode":
		return "对话形态"
	case "chatSpeedFirstConfig":
		return "速度参数"
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
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}
