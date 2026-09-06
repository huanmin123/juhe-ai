package apikeys

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	k.Register("PATCH "+prefix+"/api-keys/{id}", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.patch(w, r, adminScope(r))
	})))
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
	k.Register("PATCH "+prefix+"/my-api-keys/{id}", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.patch(w, r, selfScope(r))
	})))
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

// operationMode mirrors operation-log.service.ts operationMode.
func operationMode(access AccessScope) string {
	if access.IsAdmin {
		return "admin"
	}
	return "self"
}

// operationStatusCode pins the Node statusCode value on the log entries that
// know the response outcome before it is written (api-keys create 201,
// refresh/patch validation-cache-failure 500 else 200, delete 500 else 204).
func operationStatusCode(status int) *int { return &status }

func setNoStoreHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func (d *Deps) list(w http.ResponseWriter, r *http.Request, access AccessScope) {
	// Node's list route reads the filter directly (no scope-query parsing):
	// a blank systemAccountId is simply ignored.
	query := r.URL.Query()
	pageValue, pageSet := jsQueryInteger(query.Get("page"))
	pageSizeValue, pageSizeSet := jsQueryInteger(query.Get("pageSize"))
	options := ListOptions{
		PageSet:         pageSet,
		Page:            pageValue,
		PageSizeSet:     pageSizeSet,
		PageSize:        pageSizeValue,
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
				StatusCode:                    operationStatusCode(http.StatusCreated),
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
			// Node stamps statusCode before the response outcome lands
			// (api-keys.routes.ts:122): the validation-cache failure 500s, the
			// normal path answers 200.
			statusCode := http.StatusOK
			if outcome.ValidationCacheError != nil {
				statusCode = http.StatusInternalServerError
			}
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
				StatusCode:                    operationStatusCode(statusCode),
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

// patchChangeLabels mirrors the diffSafeFields label map of the Node PATCH
// route; the slice pins the JSON key order of the labels object.
var patchChangeLabels = []struct{ Field, Label string }{
	{"name", "名称"},
	{"description", "说明"},
	{"status", "状态"},
	{"routeStrategyId", "策略路由"},
	{"expiresAt", "过期时间"},
	{"quotaLimits", "额度限制"},
	{"availabilitySchedule", "时间计划"},
}

// diffSafePatchChanges mirrors diffSafeFields + safeChange for the patch
// label set (none of the fields is sensitive): an entry is emitted when the
// JSON renderings of before/after differ, with strings verbatim (200-char
// cap) and objects rendered as JSON (500-char cap). Absent/null collapses to
// null for the comparison and drops the property for the rendered change,
// matching normalizeSafeValue(undefined) → omitted.
func diffSafePatchChanges(before, after map[string]any) []authsys.OperationLogChange {
	changes := []authsys.OperationLogChange{}
	for _, label := range patchChangeLabels {
		beforeValue, hasBefore := before[label.Field]
		afterValue, hasAfter := after[label.Field]
		if patchComparableValue(beforeValue, hasBefore) == patchComparableValue(afterValue, hasAfter) {
			continue
		}
		changes = append(changes, authsys.OperationLogChange{
			Field:  label.Field,
			Label:  label.Label,
			Before: patchSafeValue(beforeValue, hasBefore),
			After:  patchSafeValue(afterValue, hasAfter),
		})
	}
	return changes
}

func patchComparableValue(value any, present bool) string {
	if !present || value == nil {
		return "null"
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(encoded)
}

func patchSafeValue(value any, present bool) string {
	if !present || value == nil {
		return ""
	}
	if text, isString := value.(string); isString {
		runes := []rune(text)
		if len(runes) > 200 {
			return string(runes[:200]) + "..."
		}
		return text
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		encoded = []byte(fmt.Sprintf("%v", value))
	}
	runes := []rune(string(encoded))
	if len(runes) > 500 {
		return string(runes[:500])
	}
	return string(encoded)
}

// patch mirrors the Node PATCH /:id route: scope-query gate,
// apiKeyUpdateSchema parse (first issue message → 400), the store patch, the
// api_keys.update operation log (diffSafeFields over before/after, recorded
// only when changedFields is non-empty) and the error contract: revision
// conflict → 409 + currentRevision, missing key → 404, duplicate name → 409,
// known validation messages → 400, validation-cache flush failure → 500.
func (d *Deps) patch(w http.ResponseWriter, r *http.Request, access AccessScope) {
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
	input, message := parsePatchBody(body)
	if message != "" {
		kernel.WriteBadRequest(w, message)
		return
	}
	outcome, err := d.Store.Patch(r.Context(), r.PathValue("id"), input, access)
	if err != nil {
		var revisionConflict *RevisionConflictError
		if errors.As(err, &revisionConflict) {
			kernel.WriteJSON(w, http.StatusConflict, struct {
				Message         string `json:"message"`
				CurrentRevision string `json:"currentRevision"`
			}{Message: revisionConflict.Error(), CurrentRevision: revisionConflict.CurrentRevision})
			return
		}
		d.writeMutationError(w, err)
		return
	}
	if outcome == nil {
		kernel.WriteError(w, http.StatusNotFound, "API Key 不存在")
		return
	}
	if d.Sink != nil && len(outcome.Result.ChangedFields) > 0 {
		// Node stamps statusCode before the response outcome lands
		// (api-keys.routes.ts:262): the validation-cache failure 500s, the
		// normal path answers 200.
		statusCode := http.StatusOK
		if outcome.ValidationCacheError != nil {
			statusCode = http.StatusInternalServerError
		}
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID:          auth.SystemAccountID,
			ActorUsername:                 auth.Username,
			ActorDisplayName:              auth.DisplayName,
			ActorRole:                     auth.Role,
			OperationScopeSystemAccountID: outcome.OwnerSystemAccountID,
			Mode:                          operationMode(access),
			Module:                        "api_keys",
			Action:                        "update",
			OperationKey:                  "api_keys.update",
			ResourceType:                  "api_key",
			ResourceID:                    outcome.Result.ID,
			ResourceName:                  outcome.ResourceName,
			Summary:                       "更新 API Key：" + outcome.ResourceName,
			StatusCode:                    operationStatusCode(statusCode),
			Changes:                       diffSafePatchChanges(outcome.Before, outcome.After),
			Viewers: []authsys.OperationLogViewer{
				{SystemAccountID: outcome.OwnerSystemAccountID, Reason: "resource_owner"},
			},
		}, r)
	}
	if outcome.ValidationCacheError != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "API Key validation cache 失效失败")
		return
	}
	kernel.WriteOK(w, outcome.Result, "")
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
		// Node stamps statusCode before the response outcome lands
		// (api-keys.routes.ts:319): the validation-cache failure 500s, the
		// normal path answers 204.
		statusCode := http.StatusNoContent
		if result.ValidationCacheError != nil {
			statusCode = http.StatusInternalServerError
		}
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
			StatusCode:                    operationStatusCode(statusCode),
			// safeChange('deleted', '删除状态', false, true): normalizeSafeValue
			// keeps booleans native.
			Changes: []authsys.OperationLogChange{
				{Field: "deleted", Label: "删除状态", BeforeValue: false, AfterValue: true},
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
		// z.string().trim().max(200): the cap counts UTF-16 code units and the
		// issue message is the zod default the create route renders.
		if utf16Length(strings.TrimSpace(text)) > 200 {
			return CreateInput{}, "String must contain at most 200 character(s)"
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

// jsTrim trims the ECMAScript Whitespace set (String.prototype.trim) — note
// it is NOT identical to Go's unicode.IsSpace (e.g. U+0085 is JS-significant).
func jsTrim(text string) string {
	return strings.TrimFunc(text, func(r rune) bool {
		if r <= 0x20 {
			return r == '\t' || r == '\n' || r == '\v' || r == '\f' || r == '\r' || r == ' '
		}
		switch r {
		case 0x00a0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
			return true
		}
		return r >= 0x2000 && r <= 0x200a
	})
}

// jsNumber mirrors the Number(string) conversion (ECMA-262 StringToNumber):
// radix prefixes without sign, exact "Infinity" spellings, decimal literals
// (including "5.", ".5", exponents), everything else NaN.
func jsNumber(text string) (float64, bool) {
	switch text {
	case "Infinity", "+Infinity":
		return math.Inf(1), true
	case "-Infinity":
		return math.Inf(-1), true
	}
	if len(text) > 1 && text[0] == '0' {
		var base, digits string
		switch lower := strings.ToLower(text); lower[1] {
		case 'x':
			base, digits = "0123456789abcdef", text[2:]
		case 'o':
			base, digits = "01234567", text[2:]
		case 'b':
			base, digits = "01", text[2:]
		}
		if base != "" {
			if digits == "" {
				return 0, false
			}
			value, err := strconv.ParseInt(digits, len(base), 64)
			if err != nil {
				if errors.Is(err, strconv.ErrRange) {
					// JS keeps rounding huge radix literals into finite
					// integers; the saturating value stays on the same side
					// of every downstream clamp.
					return float64(math.MaxInt64), true
				}
				return 0, false
			}
			return float64(value), true
		}
	}
	if strings.ContainsAny(text, "_") {
		return 0, false
	}
	// Reject the "inf"/"infinity"/"nan" spellings strconv accepts but JS
	// Number() does not (exact Infinity forms were handled above).
	lower := strings.ToLower(text)
	if strings.ContainsAny(lower, "in") {
		return 0, false
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			// Overflow → ±Infinity (integer check then rejects it).
			return value, true
		}
		return 0, false
	}
	return value, true
}

// jsQueryInteger mirrors integerQueryValue: JS Number(text) + Number.isInteger.
// "1e2" → 100 and "1.0" → 1 are valid; blank, NaN, ±Infinity and fractional
// results count as absent.
func jsQueryInteger(raw string) (int, bool) {
	trimmed := jsTrim(raw)
	if trimmed == "" {
		return 0, false
	}
	value, ok := jsNumber(trimmed)
	if !ok || math.IsInf(value, 0) || value != math.Trunc(value) {
		return 0, false
	}
	// Saturate beyond the int range; the downstream 1..window clamps make the
	// saturated value behave exactly like Node's huge finite integers.
	const maxInt64 = float64(math.MaxInt64)
	const minInt64 = -maxInt64
	if value >= maxInt64 {
		return math.MaxInt64, true
	}
	if value <= minInt64 {
		return math.MinInt64, true
	}
	return int(value), true
}
