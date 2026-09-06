package groups

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// Deps bundles the M05 slice collaborators. Authz carries the authorization
// return domain the return-authorization route mounts through (Node
// returnGroupAuthorizationForGranteeAsync, exported via internal/authz).
type Deps struct {
	Store *Store
	Auth  *authsys.Deps
	Sink  authsys.OperationLogSink
	Authz *authz.Store
}

// Mount wires the groups route family: admin surface on /groups and the
// forceSelfAccessScope mirror on /my-groups (Node mounts the same router at
// both prefixes; my-* pins the scope to the caller and drops any
// systemAccountId query).
func (d *Deps) Mount(k *kernel.Kernel) {
	prefix := "/__aisys__/api"
	// Admin surface.
	k.Register("GET "+prefix+"/groups", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.list(w, r, adminScope(r))
	})))
	k.Register("GET "+prefix+"/groups/options", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.options(w, r, adminScope(r))
	})))
	k.Register("GET "+prefix+"/groups/authorization-options", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.authorizationOptions(w, r, adminScope(r))
	})))
	k.Register("GET "+prefix+"/groups/account-options", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.accountOptions(w, r, adminScope(r))
	})))
	k.Register("GET "+prefix+"/groups/route-strategy-options", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.routeStrategyOptions(w, r, adminScope(r))
	})))
	k.Register("GET "+prefix+"/groups/{id}", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.find(w, r, adminScope(r))
	})))
	k.Register("GET "+prefix+"/groups/{id}/edit-basic", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.editBasic(w, r, adminScope(r))
	})))
	k.Register("POST "+prefix+"/groups", d.mountGuardedCreate(false))
	k.Register("POST "+prefix+"/groups/{id}/return-authorization", d.mountGuardedReturnAuthorization(false))
	k.Register("PATCH "+prefix+"/groups/{id}", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.patch(w, r, adminScope(r))
	})))
	k.Register("DELETE "+prefix+"/groups/{id}", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.remove(w, r, adminScope(r))
	})))

	// Self surface (forceSelfAccessScope: role forced to user, caller-scoped).
	k.Register("GET "+prefix+"/my-groups", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.list(w, r, selfScope(r))
	})))
	k.Register("GET "+prefix+"/my-groups/options", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.options(w, r, selfScope(r))
	})))
	k.Register("GET "+prefix+"/my-groups/authorization-options", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.authorizationOptions(w, r, selfScope(r))
	})))
	k.Register("GET "+prefix+"/my-groups/account-options", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.accountOptions(w, r, selfScope(r))
	})))
	k.Register("GET "+prefix+"/my-groups/route-strategy-options", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.routeStrategyOptions(w, r, selfScope(r))
	})))
	k.Register("GET "+prefix+"/my-groups/{id}", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.find(w, r, selfScope(r))
	})))
	k.Register("GET "+prefix+"/my-groups/{id}/edit-basic", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.editBasic(w, r, selfScope(r))
	})))
	k.Register("POST "+prefix+"/my-groups", d.mountGuardedCreate(true))
	k.Register("POST "+prefix+"/my-groups/{id}/return-authorization", d.mountGuardedReturnAuthorization(true))
	k.Register("PATCH "+prefix+"/my-groups/{id}", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.patch(w, r, selfScope(r))
	})))
	k.Register("DELETE "+prefix+"/my-groups/{id}", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

// listPageQuery mirrors parseGroupListOptions: integerQueryValue turns
// absent/non-integer values into the defaults; the store clamps pageSize to
// 1..500 (default 50) and page to 1..floor(1000/pageSize).
func listPageQuery(query url.Values) (int, int) {
	return integerQueryValue(query.Get("page"), 1), integerQueryValue(query.Get("pageSize"), 50)
}

// integerQueryValue mirrors shared/query-values.ts integerQueryValue followed
// by the store-side integer clamp: non-integer text falls back to the default,
// integers below 1 clamp to 1, integers above max clamp to max.
func integerQueryValue(raw string, fallback int) int {
	text := strings.TrimSpace(raw)
	if text == "" {
		return fallback
	}
	value, err := strconv.Atoi(text)
	if err != nil {
		return fallback
	}
	if value < 1 {
		return 1
	}
	return value
}

func (d *Deps) list(w http.ResponseWriter, r *http.Request, access AccessScope) {
	page, pageSize := listPageQuery(r.URL.Query())
	result, err := d.Store.ListPage(r.Context(), access, page, pageSize, r.URL.Query().Get("keyword"))
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
		kernel.WriteError(w, http.StatusNotFound, "分组不存在")
		return
	}
	kernel.WriteOK(w, detail, "")
}

// createBody validates the route payload (Node groupSchema.strict()): every
// optional field rejects null (zod optional accepts only undefined) and the
// schedulingPolicy object runs the strict per-key int/range/enum checks.
func createBody(body map[string]any) (MutationInput, bool) {
	input := MutationInput{}
	for key := range body {
		switch key {
		case "name", "providerCode", "description", "enabled", "groupType", "schedulingPolicy":
		default:
			return input, false
		}
	}
	name, ok := bodyString(body, "name")
	if !ok || strings.TrimSpace(name) == "" {
		return input, false
	}
	providerCode, ok := bodyString(body, "providerCode")
	if !ok || strings.TrimSpace(providerCode) == "" {
		return input, false
	}
	input.Name = &name
	input.ProviderCode = &providerCode
	if value, exists := body["description"]; exists {
		text, isString := value.(string)
		if !isString {
			return MutationInput{}, false
		}
		input.Description = &text
	}
	if value, exists := body["enabled"]; exists {
		enabled, isBool := value.(bool)
		if !isBool {
			return MutationInput{}, false
		}
		input.Enabled = &enabled
	}
	if value, exists := body["groupType"]; exists {
		text, isString := value.(string)
		if !isString || (text != GroupTypePersonal && text != GroupTypeHighConcurrency) {
			return MutationInput{}, false
		}
		input.GroupType = &text
	}
	if value, exists := body["schedulingPolicy"]; exists {
		policy, isObject := value.(map[string]any)
		if !isObject || !validateSchedulingPolicyInput(policy) {
			return MutationInput{}, false
		}
		input.SchedulingPolicy = policy
	}
	return input, true
}

func bodyString(body map[string]any, key string) (string, bool) {
	value, exists := body[key]
	if !exists {
		return "", false
	}
	text, isString := value.(string)
	if !isString {
		return "", false
	}
	return text, true
}

func (d *Deps) mountGuardedCreate(selfOnly bool) http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "groups.create",
		Actor:        actorResolver,
		Scope: func(r *http.Request) (any, error) {
			return strings.TrimSpace(r.URL.Query().Get("systemAccountId")), nil
		},
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{
				"owner":        strings.TrimSpace(r.URL.Query().Get("systemAccountId")),
				"providerCode": kernel.TextField(kernel.BodyField(r, "providerCode")),
				"name":         kernel.TextField(kernel.BodyField(r, "name")),
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
		input, ok := createBody(body)
		if !ok {
			kernel.WriteBadRequest(w, "分组参数无效")
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
			d.Sink.Record(authsys.OperationLogEntry{
				ActorSystemAccountID:          auth.SystemAccountID,
				ActorUsername:                 auth.Username,
				ActorDisplayName:              auth.DisplayName,
				ActorRole:                     auth.Role,
				OperationScopeSystemAccountID: item.OwnerSystemAccountID,
				Mode:                          operationMode(access),
				Module:                        "groups",
				Action:                        "create",
				OperationKey:                  "groups.create",
				ResourceType:                  "group",
				ResourceID:                    item.ID,
				ResourceName:                  item.Name,
				Summary:                       "创建分组：" + item.Name,
				Changes: []authsys.OperationLogChange{
					{Field: "name", Label: "名称", After: item.Name},
					{Field: "providerCode", Label: "供应商", After: item.ProviderCode},
					{Field: "groupType", Label: "分组类型", After: item.GroupType},
					// safeChange('enabled', '启用状态', undefined, group.enabled):
					// normalizeSafeValue keeps booleans native.
					{Field: "enabled", Label: "启用状态", AfterValue: item.Enabled},
				},
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

// createdEnvelope mirrors the {data} success envelope at 201 (Node
// res.status(201).json(ok(group))).
type createdEnvelope struct {
	Data any `json:"data"`
}

func writeCreated(w http.ResponseWriter, item *ListItem) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(createdEnvelope{Data: item})
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
	expected, _ := body["expectedUpdatedAt"].(string)
	if strings.TrimSpace(expected) == "" || !isRFC3339Instant(expected) {
		kernel.WriteBadRequest(w, "分组参数无效")
		return
	}
	hasChange := false
	for key := range body {
		switch key {
		case "name", "providerCode", "description", "enabled", "groupType", "schedulingPolicy":
			hasChange = true
		case "expectedUpdatedAt":
		default:
			kernel.WriteBadRequest(w, "分组参数无效")
			return
		}
	}
	if !hasChange {
		kernel.WriteBadRequest(w, "请提供要修改的分组内容")
		return
	}
	input := patchBody(body)
	if input.Empty() {
		kernel.WriteBadRequest(w, "分组参数无效")
		return
	}
	result, err := d.Store.Patch(r.Context(), r.PathValue("id"), input, expected, access)
	if result == nil && err == nil {
		kernel.WriteError(w, http.StatusNotFound, "分组不存在")
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
				Field: change.Field, Label: patchFieldLabel(change.Field), Before: change.Before, After: change.After,
			})
		}
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID:          auth.SystemAccountID,
			ActorUsername:                 auth.Username,
			ActorDisplayName:              auth.DisplayName,
			ActorRole:                     auth.Role,
			OperationScopeSystemAccountID: result.OwnerSystemAccountID,
			Mode:                          operationMode(access),
			Module:                        "groups",
			Action:                        "update",
			OperationKey:                  "groups.update",
			ResourceType:                  "group",
			ResourceID:                    result.ID,
			ResourceName:                  result.Name,
			Summary:                       "更新分组：" + result.Name,
			Changes:                       changes,
			Viewers: []authsys.OperationLogViewer{
				{SystemAccountID: result.OwnerSystemAccountID, Reason: "resource_owner"},
			},
		}, r)
	}
	kernel.WriteOK(w, map[string]any{
		"id": result.ID, "changedFields": result.ChangedFields, "updatedAt": result.UpdatedAt,
	}, "")
}

// patchBody mirrors groupPatchSchema.partial(): every field is optional and
// null is rejected exactly like the create schema.
func patchBody(body map[string]any) MutationInput {
	input := MutationInput{}
	if value, exists := body["name"]; exists {
		text, isString := value.(string)
		if !isString || strings.TrimSpace(text) == "" {
			return MutationInput{}
		}
		input.Name = &text
	}
	if value, exists := body["providerCode"]; exists {
		text, isString := value.(string)
		if !isString || strings.TrimSpace(text) == "" {
			return MutationInput{}
		}
		input.ProviderCode = &text
	}
	if value, exists := body["description"]; exists {
		text, isString := value.(string)
		if !isString {
			return MutationInput{}
		}
		input.Description = &text
	}
	if value, exists := body["enabled"]; exists {
		enabled, isBool := value.(bool)
		if !isBool {
			return MutationInput{}
		}
		input.Enabled = &enabled
	}
	if value, exists := body["groupType"]; exists {
		text, isString := value.(string)
		if !isString || (text != GroupTypePersonal && text != GroupTypeHighConcurrency) {
			return MutationInput{}
		}
		input.GroupType = &text
	}
	if value, exists := body["schedulingPolicy"]; exists {
		policy, isObject := value.(map[string]any)
		if !isObject || !validateSchedulingPolicyInput(policy) {
			return MutationInput{}
		}
		input.SchedulingPolicy = policy
	}
	return input
}

func (d *Deps) remove(w http.ResponseWriter, r *http.Request, access AccessScope) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	result, err := d.Store.Delete(r.Context(), r.PathValue("id"), access)
	if err != nil {
		d.writeMutationError(w, err)
		return
	}
	if !result.Deleted {
		kernel.WriteError(w, http.StatusNotFound, "分组不存在")
		return
	}
	resourceName := result.Name
	if resourceName == "" {
		resourceName = r.PathValue("id")
	}
	if d.Sink != nil {
		changes := []authsys.OperationLogChange{
			// safeChange('deleted', '删除状态', false, true):
			// normalizeSafeValue keeps booleans native.
			{Field: "deleted", Label: "删除状态", BeforeValue: false, AfterValue: true},
		}
		if len(result.AffectedRouteStrategies) > 0 {
			changes = append(changes, authsys.OperationLogChange{
				Field: "affectedRouteStrategies", Label: "影响的策略路由",
				After: summarizeAffectedRouteStrategies(result.AffectedRouteStrategies),
			})
		}
		var metadata json.RawMessage
		var targets []authsys.OperationLogTarget
		if len(result.AffectedRouteStrategies) > 0 {
			sample := result.AffectedRouteStrategies
			if len(sample) > 20 {
				sample = sample[:20]
			}
			document, marshalErr := json.Marshal(map[string]any{
				"affectedRouteStrategyCount": len(result.AffectedRouteStrategies),
				"affectedRouteStrategies":    sample,
			})
			if marshalErr == nil {
				metadata = document
			}
			// Node delete log targets (groups.routes.ts): the affected route
			// strategies ride as relation=affected target rows (owner-scoped).
			targets = make([]authsys.OperationLogTarget, 0, len(sample))
			for _, route := range sample {
				targets = append(targets, authsys.OperationLogTarget{
					TargetType:                 "route_strategy",
					TargetID:                   route.RouteStrategyID,
					TargetName:                 route.RouteStrategyName,
					TargetOwnerSystemAccountID: result.OwnerSystemAccountID,
					Relation:                   "affected",
				})
			}
		}
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID:          auth.SystemAccountID,
			ActorUsername:                 auth.Username,
			ActorDisplayName:              auth.DisplayName,
			ActorRole:                     auth.Role,
			OperationScopeSystemAccountID: result.OwnerSystemAccountID,
			Mode:                          operationMode(access),
			Module:                        "groups",
			Action:                        "delete",
			OperationKey:                  "groups.delete",
			ResourceType:                  "group",
			ResourceID:                    r.PathValue("id"),
			ResourceName:                  resourceName,
			Summary:                       "删除分组：" + resourceName,
			Changes:                       changes,
			Metadata:                      metadata,
			Targets:                       targets,
			Viewers: []authsys.OperationLogViewer{
				{SystemAccountID: result.OwnerSystemAccountID, Reason: "resource_owner"},
			},
		}, r)
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeMutationError maps store errors onto the Node route family contract:
// conflicts (duplicate name, patch conflicts) → 409; everything else → 400.
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

// operationMode mirrors operation-log.service.ts operationMode.
func operationMode(access AccessScope) string {
	if access.IsAdmin {
		return "admin"
	}
	return "self"
}

func isRFC3339Instant(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	return err == nil
}

func patchFieldLabel(field string) string {
	switch field {
	case "name":
		return "名称"
	case "providerCode":
		return "供应商"
	case "description":
		return "说明"
	case "groupType":
		return "分组类型"
	case "schedulingPolicy":
		return "调度策略"
	case "enabled":
		return "启用状态"
	default:
		return field
	}
}

// summarizeAffectedRouteStrategies mirrors
// summarizeDeletedGroupRouteStrategyChanges.
func summarizeAffectedRouteStrategies(changes []RouteStrategyChange) string {
	sample := make([]string, 0, 3)
	for index, change := range changes {
		if index >= 3 {
			break
		}
		removedName := change.RemovedGroupName
		if removedName == "" {
			removedName = change.RemovedGroupID
		}
		removedText := "移除分组 " + removedName
		if change.RemovedBindingStatus != nil && *change.RemovedBindingStatus == "disabled" {
			removedText = "移除停用分组 " + removedName
		}
		sample = append(sample, change.RouteStrategyName+"："+removedText)
	}
	joined := joinCN(sample)
	if len(changes) > 3 {
		return joined + "；另有 " + itoa(len(changes)-3) + " 个策略路由受影响"
	}
	return joined
}

// granteeUserID mirrors userVisibleSystemAccountId (storage/access-scope.ts):
// the admin scope filter when present, otherwise the caller. The groups
// return-authorization route resolves the runtime authorization for this
// account.
func (a AccessScope) granteeUserID() string {
	if a.IsAdmin && a.FilterID != "" {
		return a.FilterID
	}
	return a.ViewerID
}

// scopeQueryOK mirrors parseRequestScopeQuery: an explicit systemAccountId
// query value must survive trimming (request-scope-query.ts: '系统账号 ID 不
// 能为空' on the 400 render).
func scopeQueryOK(r *http.Request) bool {
	values := r.URL.Query()["systemAccountId"]
	if len(values) == 0 {
		return true
	}
	return strings.TrimSpace(values[0]) != ""
}

// mountGuardedReturnAuthorization mounts POST /{id}/return-authorization
// (groups.routes.ts:352-405, both prefixes): the default mutation guard over
// groups.return_authorization, the scope-query gate, the authz return domain
// and the verbatim 404/204 contract with the authorizations return audit
// record (owner + grantee targets, authorization_owner/authorization_grantee
// viewers).
func (d *Deps) mountGuardedReturnAuthorization(selfOnly bool) http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "groups.return_authorization",
		Actor:        actorResolver,
		Scope: func(r *http.Request) (any, error) {
			return strings.TrimSpace(r.URL.Query().Get("systemAccountId")), nil
		},
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{
				"groupId": strings.TrimSpace(r.PathValue("id")),
				"grantee": strings.TrimSpace(r.URL.Query().Get("systemAccountId")),
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
		if d.Authz == nil {
			kernel.WriteError(w, http.StatusInternalServerError, "归还授权分组失败")
			return
		}
		access := selfScope(r)
		if !selfOnly {
			access = adminScope(r)
		}
		authorization, err := d.Authz.ReturnGroupForGrantee(r.Context(), r.PathValue("id"), access.granteeUserID(), auth.SystemAccountID)
		if err != nil {
			// Node's catch renders 400 with the error message; Go maps the
			// non-domain (store) failure onto the 500 contract like the other
			// families while keeping the Node fallback text.
			kernel.WriteError(w, http.StatusInternalServerError, "归还授权分组失败")
			return
		}
		if authorization == nil {
			kernel.WriteError(w, http.StatusNotFound, "授权分组不存在或不可归还")
			return
		}
		resourceName := authorization.ResourceName
		if d.Sink != nil {
			d.Sink.Record(authsys.OperationLogEntry{
				ActorSystemAccountID:          auth.SystemAccountID,
				ActorUsername:                 auth.Username,
				ActorDisplayName:              auth.DisplayName,
				ActorRole:                     auth.Role,
				OperationScopeSystemAccountID: authorization.GranteeSystemAccountID,
				Mode:                          operationMode(access),
				Module:                        "authorizations",
				Action:                        "return",
				OperationKey:                  "groups.return_authorization",
				ResourceType:                  "authorization",
				ResourceID:                    authorization.ID,
				ResourceName:                  resourceName,
				Summary:                       "归还授权分组：" + resourceName,
				// safeChange('returned', '归还授权分组', false, true):
				// normalizeSafeValue keeps booleans native.
				Changes: []authsys.OperationLogChange{
					{Field: "returned", Label: "归还授权分组", BeforeValue: false, AfterValue: true},
				},
				Targets: []authsys.OperationLogTarget{
					{
						TargetType:                 authorization.ResourceType,
						TargetID:                   authorization.ResourceID,
						TargetName:                 resourceName,
						TargetOwnerSystemAccountID: authorization.ResourceOwnerSystemAccountID,
						Relation:                   "owner",
					},
					{
						TargetType:                 "system_account",
						TargetID:                   authorization.GranteeSystemAccountID,
						TargetOwnerSystemAccountID: authorization.GranteeSystemAccountID,
						Relation:                   "grantee",
					},
				},
				Viewers: []authsys.OperationLogViewer{
					{SystemAccountID: authorization.ResourceOwnerSystemAccountID, Reason: "authorization_owner"},
					{SystemAccountID: authorization.GranteeSystemAccountID, Reason: "authorization_grantee"},
				},
			}, r)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	if selfOnly {
		return d.Auth.RequireSession(true)(handler)
	}
	return d.Auth.RequireAdmin(handler)
}
