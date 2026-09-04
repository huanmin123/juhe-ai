package accounts

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// Deps bundles the M08 slice collaborators. Authorized carries the M10
// authorized-instance reader (authz slice via AuthorizedAccountReader);
// Mount injects it into the store, so composition roots only set the field.
type Deps struct {
	Store      *Store
	Auth       *authsys.Deps
	Sink       authsys.OperationLogSink
	Authorized AuthorizedAccountReader
}

// Mount wires the accounts route family: admin surface on /accounts
// (requireAdmin) and the forceSelfAccessScope mirror on /my-accounts (Node
// mounts the same router at both prefixes; my-* pins the scope to the caller
// and drops any systemAccountId query).
func (d *Deps) Mount(k *kernel.Kernel) {
	d.Store.SetAuthorizedReader(d.Authorized)

	prefix := "/__aisys__/api"
	admin := d.Auth.RequireAdmin
	self := d.Auth.RequireSession(true)

	// Reads (Node list/options routes read the scope query without parsing).
	k.Register("GET "+prefix+"/accounts", admin(d.listHandler(false)))
	k.Register("GET "+prefix+"/accounts/options", admin(d.optionsHandler(false)))
	k.Register("GET "+prefix+"/accounts/tags", admin(d.scoped(d.tags)))
	k.Register("DELETE "+prefix+"/accounts/tags/{tagId}", admin(d.scoped(d.deleteTag)))
	k.Register("GET "+prefix+"/accounts/{id}", admin(d.scoped(d.detail)))
	k.Register("GET "+prefix+"/accounts/{id}/edit-basic", admin(d.scoped(d.detail)))
	k.Register("GET "+prefix+"/accounts/{id}/clone-context", admin(d.scoped(d.cloneContext)))
	k.Register("POST "+prefix+"/accounts", d.mountGuarded(d.create, "accounts.create", false))
	k.Register("PATCH "+prefix+"/accounts/{id}", admin(d.scoped(d.patchBasic)))
	k.Register("PATCH "+prefix+"/accounts/{id}/tags", admin(d.scoped(d.patchTags)))
	k.Register("POST "+prefix+"/accounts/{id}/lock", d.mountGuarded(d.lock(true), "accounts.lock", false))
	k.Register("POST "+prefix+"/accounts/{id}/unlock", d.mountGuarded(d.lock(false), "accounts.unlock", false))
	k.Register("POST "+prefix+"/accounts/{id}/lock-config", d.mountGuarded(d.lockConfig, "accounts.lock-config", false))
	k.Register("DELETE "+prefix+"/accounts/{id}", admin(d.scoped(d.remove)))

	// M09 batch/import/export family (registered on the shared Node router,
	// so both surfaces expose it).
	k.Register("POST "+prefix+"/accounts/batch-edit-context", admin(d.batchEditContextHandler(false)))
	k.Register("POST "+prefix+"/accounts/batch-update", admin(d.batchUpdateHandler(false)))
	k.Register("POST "+prefix+"/accounts/import/preview", admin(d.importPreviewHandler(false)))
	k.Register("POST "+prefix+"/accounts/import/confirm", d.mountGuarded(d.importConfirmHandler(false), "accounts.import", false))
	k.Register("POST "+prefix+"/accounts/export", admin(d.exportHandler(false)))

	// Self surface (forceSelfAccessScope).
	k.Register("GET "+prefix+"/my-accounts", self(d.listHandler(true)))
	k.Register("GET "+prefix+"/my-accounts/options", self(d.optionsHandler(true)))
	k.Register("GET "+prefix+"/my-accounts/tags", self(d.scoped(d.tags)))
	k.Register("DELETE "+prefix+"/my-accounts/tags/{tagId}", self(d.scoped(d.deleteTag)))
	k.Register("GET "+prefix+"/my-accounts/{id}", self(d.scoped(d.detail)))
	k.Register("GET "+prefix+"/my-accounts/{id}/edit-basic", self(d.scoped(d.detail)))
	k.Register("GET "+prefix+"/my-accounts/{id}/clone-context", self(d.scoped(d.cloneContext)))
	k.Register("POST "+prefix+"/my-accounts", d.mountGuarded(d.create, "accounts.create", true))
	k.Register("PATCH "+prefix+"/my-accounts/{id}", self(d.scoped(d.patchBasic)))
	k.Register("PATCH "+prefix+"/my-accounts/{id}/tags", self(d.scoped(d.patchTags)))
	k.Register("POST "+prefix+"/my-accounts/{id}/lock", d.mountGuarded(d.lock(true), "accounts.lock", true))
	k.Register("POST "+prefix+"/my-accounts/{id}/unlock", d.mountGuarded(d.lock(false), "accounts.unlock", true))
	k.Register("POST "+prefix+"/my-accounts/{id}/lock-config", d.mountGuarded(d.lockConfig, "accounts.lock-config", true))
	k.Register("DELETE "+prefix+"/my-accounts/{id}", self(d.scoped(d.remove)))
	k.Register("POST "+prefix+"/my-accounts/batch-edit-context", self(d.batchEditContextHandler(true)))
	k.Register("POST "+prefix+"/my-accounts/batch-update", self(d.batchUpdateHandler(true)))
	k.Register("POST "+prefix+"/my-accounts/import/preview", self(d.importPreviewHandler(true)))
	k.Register("POST "+prefix+"/my-accounts/import/confirm", d.mountGuarded(d.importConfirmHandler(true), "accounts.import", true))
	k.Register("POST "+prefix+"/my-accounts/export", self(d.exportHandler(true)))
}

// scoped wraps a handler with the parseRequestScopeQuery contract: an explicit
// blank systemAccountId query value is rejected with 400 系统账号 ID 不能为空.
func (d *Deps) cloneContext(w http.ResponseWriter, r *http.Request) {
	context, err := d.Store.FindCloneContext(r.Context(), r.PathValue("id"), requestScope(r))
	if err != nil {
		var forbidden *cloneInteractionForbiddenError
		var conflict *cloneInteractionConflictError
		if errors.As(err, &forbidden) {
			kernel.WriteError(w, http.StatusForbidden, forbidden.Message)
			return
		}
		if errors.As(err, &conflict) {
			kernel.WriteError(w, http.StatusConflict, conflict.Error())
			return
		}
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if context == nil {
		kernel.WriteError(w, http.StatusNotFound, "账户不存在")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	kernel.WriteOK(w, context, "")
}

func (d *Deps) scoped(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !scopeQueryOK(r) {
			kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
			return
		}
		next(w, r)
	})
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

func requestScope(r *http.Request) AccessScope {
	auth := authsys.AuthContextFrom(r)
	if auth != nil && (auth.Role == "admin" || auth.Role == "super_admin") {
		return adminScope(r)
	}
	return selfScope(r)
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

// listHandler pins the access scope per surface: my-* is forceSelfAccessScope
// even for admins (Node mounts the same router with the scope-forcing
// middleware).
func (d *Deps) listHandler(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		access := selfScope(r)
		if !selfOnly {
			access = requestScope(r)
		}
		d.list(w, r, access)
	}
}

// optionsHandler mirrors listHandler for the options surface.
func (d *Deps) optionsHandler(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		access := selfScope(r)
		if !selfOnly {
			access = requestScope(r)
		}
		d.options(w, r, access)
	}
}

func (d *Deps) list(w http.ResponseWriter, r *http.Request, access AccessScope) {
	query := r.URL.Query()
	options := ListOptions{
		Sorts:                     parseSortQuery(query["sorts"]),
		IDs:                       textListQuery(query["ids"]),
		Page:                      integerQueryValue(query.Get("page")),
		PageSize:                  integerQueryValue(query.Get("pageSize")),
		Keyword:                   strings.TrimSpace(query.Get("keyword")),
		ProviderCode:              strings.TrimSpace(query.Get("providerCode")),
		ProviderProtocolProfileID: strings.TrimSpace(query.Get("providerProtocolProfileId")),
		GroupID:                   strings.TrimSpace(query.Get("groupId")),
		TagIDs:                    textListQuery(query["tagIds"]),
		Type:                      strings.TrimSpace(query.Get("type")),
		Status:                    statusQueryValue(query.Get("status")),
		Schedulable:               schedulableQueryValue(query.Get("schedulable")),
	}
	result, err := d.Store.ListPage(r.Context(), access, options)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	setNoStoreHeaders(w)
	kernel.WriteOK(w, result, "")
}

func (d *Deps) options(w http.ResponseWriter, r *http.Request, access AccessScope) {
	query := r.URL.Query()
	options := ListOptions{
		IDs:                       textListQuery(query["ids"]),
		Page:                      integerQueryValue(query.Get("page")),
		PageSize:                  optionLimitValue(query.Get("limit")),
		Keyword:                   strings.TrimSpace(query.Get("keyword")),
		ProviderCode:              strings.TrimSpace(query.Get("providerCode")),
		ProviderProtocolProfileID: strings.TrimSpace(query.Get("providerProtocolProfileId")),
		GroupID:                   strings.TrimSpace(query.Get("groupId")),
		TagIDs:                    textListQuery(query["tagIds"]),
		Type:                      strings.TrimSpace(query.Get("type")),
		Status:                    statusQueryValue(query.Get("status")),
		Schedulable:               schedulableQueryValue(query.Get("schedulable")),
	}
	summaries, err := d.Store.ListOptionSummaries(r.Context(), access, options)
	if err != nil {
		d.writeError(w, err)
		return
	}
	setNoStoreHeaders(w)
	kernel.WriteOK(w, summaries, "")
}

func (d *Deps) tags(w http.ResponseWriter, r *http.Request) {
	summaries, err := d.Store.ListTags(r.Context(), requestScope(r))
	if err != nil {
		d.writeError(w, err)
		return
	}
	kernel.WriteOK(w, summaries, "")
}

func (d *Deps) deleteTag(w http.ResponseWriter, r *http.Request) {
	deleted, err := d.Store.DeleteTag(r.Context(), r.PathValue("tagId"), requestScope(r))
	if err != nil {
		d.writeError(w, err)
		return
	}
	if !deleted {
		kernel.WriteError(w, http.StatusNotFound, tagNotFoundMessage)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d *Deps) detail(w http.ResponseWriter, r *http.Request) {
	detail, err := d.Store.FindEditBasicDetail(r.Context(), r.PathValue("id"), requestScope(r))
	if err != nil {
		d.writeError(w, err)
		return
	}
	if detail == nil {
		kernel.WriteError(w, http.StatusNotFound, "账户不存在")
		return
	}
	setNoStoreHeaders(w)
	kernel.WriteOK(w, detail, "")
}

func (d *Deps) create(w http.ResponseWriter, r *http.Request) {
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
	result, err := d.Store.Create(r.Context(), input, requestScope(r))
	if err != nil {
		d.writeError(w, err)
		return
	}
	if d.Sink != nil {
		access := requestScope(r)
		credentials := safeCredentialsChange(body["credentials"])
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID:          auth.SystemAccountID,
			ActorUsername:                 auth.Username,
			ActorDisplayName:              auth.DisplayName,
			ActorRole:                     auth.Role,
			OperationScopeSystemAccountID: result.OwnerSystemAccountID,
			Mode:                          operationMode(access),
			Module:                        "accounts",
			Action:                        "create",
			OperationKey:                  "accounts.create",
			ResourceType:                  "account",
			ResourceID:                    result.ID,
			ResourceName:                  result.Name,
			Summary:                       "创建 AI 账户：" + result.Name,
			Changes: append([]authsys.OperationLogChange{
				{Field: "name", Label: "名称", After: result.Name},
				{Field: "providerCode", Label: "供应商", After: strings.TrimSpace(textString(body["providerCode"]))},
				{Field: "providerProtocolProfileId", Label: "协议档案", After: strings.TrimSpace(textString(body["providerProtocolProfileId"]))},
				{Field: "type", Label: "账户类型", After: strings.TrimSpace(textString(body["type"]))},
				{Field: "status", Label: "状态", After: result.Status},
			}, credentials),
			Viewers: []authsys.OperationLogViewer{
				{SystemAccountID: result.OwnerSystemAccountID, Reason: "resource_owner"},
			},
		}, r)
	}
	setNoStoreHeaders(w)
	kernel.WriteJSON(w, http.StatusCreated, map[string]any{
		"data":    map[string]any{"id": result.ID, "status": result.Status, "configRevision": result.ConfigRevision},
		"message": "",
	})
}

func (d *Deps) patchBasic(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	input, message := patchBody(body)
	if message != "" {
		kernel.WriteBadRequest(w, message)
		return
	}
	d.runPatch(w, r, auth, input, "update", "更新 AI 账户：", "")
}

func (d *Deps) patchTags(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	input, message := tagsPatchBody(body)
	if message != "" {
		kernel.WriteBadRequest(w, message)
		return
	}
	d.runPatch(w, r, auth, input, "update_tags", "更新账户标签：", "tags")
}

func (d *Deps) runPatch(w http.ResponseWriter, r *http.Request, auth *authsys.AuthContext, input PatchInput, action, summaryPrefix, onlyField string) {
	result, err := d.Store.Patch(r.Context(), r.PathValue("id"), input, requestScope(r))
	if err != nil {
		d.writeError(w, err)
		return
	}
	if result == nil {
		kernel.WriteError(w, http.StatusNotFound, "账户不存在")
		return
	}
	if d.Sink != nil && len(result.Changes) > 0 {
		access := requestScope(r)
		changes := []authsys.OperationLogChange{}
		for _, change := range result.Changes {
			if onlyField != "" && change.Field != onlyField {
				continue
			}
			changes = append(changes, safeChange(change.Field, accountPatchChangeLabel(change.Field), change.Before, change.After))
		}
		if len(changes) > 0 {
			d.Sink.Record(authsys.OperationLogEntry{
				ActorSystemAccountID:          auth.SystemAccountID,
				ActorUsername:                 auth.Username,
				ActorDisplayName:              auth.DisplayName,
				ActorRole:                     auth.Role,
				OperationScopeSystemAccountID: result.OwnerSystemAccountID,
				Mode:                          operationMode(access),
				Module:                        "accounts",
				Action:                        action,
				OperationKey:                  "accounts." + action,
				ResourceType:                  "account",
				ResourceID:                    result.ID,
				ResourceName:                  result.Name,
				Summary:                       summaryPrefix + result.Name,
				Changes:                       changes,
				Viewers: []authsys.OperationLogViewer{
					{SystemAccountID: result.OwnerSystemAccountID, Reason: "resource_owner"},
				},
			}, r)
		}
	}
	setNoStoreHeaders(w)
	kernel.WriteOK(w, map[string]any{
		"id":             result.ID,
		"configRevision": result.ConfigRevision,
		"changedFields":  result.ChangedFields,
	}, "")
}

// lock returns the guarded lock/unlock handler (Node shares one route body
// over [['lock', true], ['unlock', false]]).
func (d *Deps) lock(enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := authsys.AuthContextFrom(r)
		if auth == nil {
			kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		var body map[string]any
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		input, message := lockBody(body)
		if message != "" {
			kernel.WriteBadRequest(w, message)
			return
		}
		input.AccountID = r.PathValue("id")
		input.Enabled = enabled
		state, err := d.Store.SetLock(r.Context(), input, requestScope(r))
		if err != nil {
			d.writeError(w, err)
			return
		}
		if state == nil {
			kernel.WriteError(w, http.StatusNotFound, lockNotFoundMessage)
			return
		}
		if d.Sink != nil {
			access := requestScope(r)
			action := "unlock"
			summary := "解除锁死 AI 账户"
			before := "UNLOCKED"
			if enabled {
				action = "lock"
				summary = "锁死 AI 账户"
				before = "LOCKED_IDLE"
			}
			d.Sink.Record(authsys.OperationLogEntry{
				ActorSystemAccountID:          auth.SystemAccountID,
				ActorUsername:                 auth.Username,
				ActorDisplayName:              auth.DisplayName,
				ActorRole:                     auth.Role,
				OperationScopeSystemAccountID: access.manageableID(),
				Mode:                          operationMode(access),
				Module:                        "accounts",
				Action:                        action,
				OperationKey:                  "accounts." + action,
				ResourceType:                  "account",
				ResourceID:                    state.AccountID,
				Summary:                       summary,
				Changes: []authsys.OperationLogChange{
					safeChange("lockState", "锁死状态", before, state.LockState),
				},
				Viewers: []authsys.OperationLogViewer{
					{SystemAccountID: access.ViewerID, Reason: "resource_owner"},
				},
			}, r)
		}
		setNoStoreHeaders(w)
		kernel.WriteOK(w, state, "")
	}
}

func (d *Deps) lockConfig(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	input, message := lockBody(body)
	if message != "" {
		kernel.WriteBadRequest(w, message)
		return
	}
	input.AccountID = r.PathValue("id")
	if input.LockDeathTimeoutSeconds == nil && input.LockRetryIntervalSeconds == nil {
		kernel.WriteBadRequest(w, "请至少提交一项锁死配置")
		return
	}
	state, err := d.Store.LockConfig(r.Context(), input, requestScope(r))
	if err != nil {
		d.writeError(w, err)
		return
	}
	if state == nil {
		kernel.WriteError(w, http.StatusNotFound, lockNotFoundMessage)
		return
	}
	if d.Sink != nil {
		access := requestScope(r)
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID:          auth.SystemAccountID,
			ActorUsername:                 auth.Username,
			ActorDisplayName:              auth.DisplayName,
			ActorRole:                     auth.Role,
			OperationScopeSystemAccountID: access.manageableID(),
			Mode:                          operationMode(access),
			Module:                        "accounts",
			Action:                        "lock-config",
			OperationKey:                  "accounts.lock-config",
			ResourceType:                  "account",
			ResourceID:                    state.AccountID,
			Summary:                       "更新 AI 账户锁死配置",
			Changes: []authsys.OperationLogChange{
				safeChange("lockDeathTimeoutSeconds", "锁死死期", nil, state.LockDeathTimeoutSeconds),
				safeChange("lockRetryIntervalSeconds", "锁死重试间隔", nil, state.LockRetryIntervalSeconds),
			},
			Viewers: []authsys.OperationLogViewer{
				{SystemAccountID: access.ViewerID, Reason: "resource_owner"},
			},
		}, r)
	}
	setNoStoreHeaders(w)
	kernel.WriteOK(w, state, "")
}

func (d *Deps) remove(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	access := requestScope(r)
	resourceName := r.PathValue("id")
	var ownerID string
	if before, err := d.Store.FindEditBasicDetail(r.Context(), r.PathValue("id"), access); err == nil && before != nil {
		resourceName = before.Name
		ownerID = before.OwnerSystemAccountID
	}
	deleted, err := d.Store.Delete(r.Context(), r.PathValue("id"), access)
	if err != nil {
		d.writeError(w, err)
		return
	}
	if !deleted {
		kernel.WriteError(w, http.StatusNotFound, "账户不存在")
		return
	}
	if d.Sink != nil {
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID:          auth.SystemAccountID,
			ActorUsername:                 auth.Username,
			ActorDisplayName:              auth.DisplayName,
			ActorRole:                     auth.Role,
			OperationScopeSystemAccountID: ownerID,
			Mode:                          operationMode(access),
			Module:                        "accounts",
			Action:                        "delete",
			OperationKey:                  "accounts.delete",
			ResourceType:                  "account",
			ResourceID:                    r.PathValue("id"),
			ResourceName:                  resourceName,
			Summary:                       "删除 AI 账户：" + resourceName,
			Changes: []authsys.OperationLogChange{
				safeChange("deleted", "删除状态", false, true),
			},
			Viewers: []authsys.OperationLogViewer{
				{SystemAccountID: ownerID, Reason: "resource_owner"},
			},
		}, r)
	}
	setNoStoreHeaders(w)
	w.WriteHeader(http.StatusNoContent)
}

// mountGuarded wraps a write handler with kernel.MutationGuardMiddleware and
// the surface auth middleware (Node mutationGuard + requireAdmin /
// forceSelfAccessScope ordering).
func (d *Deps) mountGuarded(next http.HandlerFunc, operationKey string, selfOnly bool) http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: operationKey,
		Actor:        actorResolver,
		Scope: func(r *http.Request) (any, error) {
			return strings.TrimSpace(r.URL.Query().Get("systemAccountId")), nil
		},
		Fingerprint: func(r *http.Request) (any, error) {
			return guardFingerprint(r, operationKey), nil
		},
	})
	handler := guard(next)
	if selfOnly {
		return d.Auth.RequireSession(true)(handler)
	}
	return d.Auth.RequireAdmin(handler)
}

func actorResolver(r *http.Request) string {
	if auth := authsys.AuthContextFrom(r); auth != nil {
		return auth.SystemAccountID
	}
	return "anonymous"
}

// guardFingerprint mirrors the Node mutationGuard fingerprints: create carries
// owner/provider/profile/type/name/credential/status, the lock family carries
// the account id and the lock configuration fields.
func guardFingerprint(r *http.Request, operationKey string) map[string]any {
	if operationKey == "accounts.create" {
		return map[string]any{
			"owner":                     strings.TrimSpace(r.URL.Query().Get("systemAccountId")),
			"providerCode":              kernel.TextField(kernel.BodyField(r, "providerCode")),
			"providerProtocolProfileId": kernel.TextField(kernel.BodyField(r, "providerProtocolProfileId")),
			"type":                      kernel.TextField(kernel.BodyField(r, "type")),
			"name":                      kernel.TextField(kernel.BodyField(r, "name")),
			"credential":                credentialGuardFingerprint(kernel.BodyField(r, "credentials")),
			"status":                    AccountCreationStatusInput(kernel.BodyField(r, "status")).Status,
		}
	}
	if operationKey == "accounts.import" {
		return map[string]any{
			"owner":      strings.TrimSpace(r.URL.Query().Get("systemAccountId")),
			"data":       kernel.BodyField(r, "data"),
			"sourceMode": kernel.BodyField(r, "sourceMode"),
			"options":    kernel.BodyField(r, "options"),
		}
	}
	return map[string]any{
		"accountId": strings.TrimSpace(r.PathValue("id")),
		"timeout":   kernel.BodyField(r, "lockDeathTimeoutSeconds"),
		"interval":  kernel.BodyField(r, "lockRetryIntervalSeconds"),
	}
}

// credentialGuardFingerprint mirrors the mutation-guard credential
// fingerprint: a stable hash over the credential source (never the material
// itself).
func credentialGuardFingerprint(value any) string {
	record, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	source := apiKeyCredentialFingerprintSource(record)
	if source == "" {
		source = firstNonEmptyText(record, "identity_token", "refresh_token", "access_token", "email", "account_id")
	}
	if strings.TrimSpace(source) == "" {
		return ""
	}
	return kernel.HashStableValue(strings.TrimSpace(source))
}

func apiKeyCredentialFingerprintSource(record map[string]any) string {
	if list, ok := record["api_keys"].([]any); ok {
		keys := []string{}
		for _, item := range list {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				keys = append(keys, strings.TrimSpace(text))
			}
		}
		if len(keys) > 0 {
			return strings.Join(keys, "\n")
		}
	}
	if text, ok := record["api_key"].(string); ok {
		return text
	}
	return ""
}

func firstNonEmptyText(record map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := record[key].(string); ok && strings.TrimSpace(text) != "" {
			return text
		}
	}
	return ""
}

// writeMutationError maps store errors onto the Node route family contract.
func (d *Deps) writeError(w http.ResponseWriter, err error) {
	var conflict *ConflictError
	var revision *RevisionConflictError
	var validation *ValidationError
	var forbidden *editBasicForbiddenError
	var tagInUse *TagInUseError
	var batchAccess *batchAccessError
	var batchConflict *batchVersionConflictError
	switch {
	case errors.As(err, &batchConflict):
		kernel.WriteJSON(w, http.StatusConflict, map[string]string{"message": batchConflict.Error()})
	case errors.As(err, &batchAccess):
		status := http.StatusNotFound
		if batchAccess.sameScope() {
			status = http.StatusBadRequest
		}
		kernel.WriteError(w, status, batchAccess.Message)
	case errors.As(err, &conflict):
		kernel.WriteError(w, http.StatusConflict, conflict.Message)
	case errors.As(err, &revision):
		status := http.StatusConflict
		if revision.Message == lockNotFoundMessage {
			status = http.StatusNotFound
		}
		if status == http.StatusConflict {
			kernel.WriteJSON(w, status, map[string]string{"message": revision.Message})
		} else {
			kernel.WriteError(w, status, revision.Message)
		}
	case errors.As(err, &validation):
		kernel.WriteBadRequest(w, validation.Message)
	case errors.As(err, &forbidden):
		kernel.WriteError(w, http.StatusForbidden, forbidden.Error())
	case errors.As(err, &tagInUse):
		kernel.WriteBadRequest(w, tagInUse.Error())
	default:
		println("accounts slice internal error: " + err.Error())
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
	}
}

// scopeOwnerID mirrors resolveOperationOwner(undefined, access) for handlers
// without a resource row: admins with an explicit filter resolve to the
// filter, everything else to the caller.
func scopeOwnerID(access AccessScope) string {
	if access.IsAdmin {
		if filter := strings.TrimSpace(access.FilterID); filter != "" && filter != "all" {
			return filter
		}
	}
	return access.ViewerID
}

// safeChange mirrors operation-log.service safeChange: sensitive containers
// (credentials, keys, tokens) never carry material, only set/change markers.
func safeChange(field string, label string, before, after any) authsys.OperationLogChange {
	if isSensitiveChangeField(field) {
		entry := authsys.OperationLogChange{Field: field, Label: label, Sensitive: true}
		if before != nil && before != "" {
			entry.Before = "已设置"
		} else {
			entry.Before = "未设置"
		}
		if after != nil && after != "" {
			entry.After = "已变更"
		} else {
			entry.After = "未设置"
		}
		return entry
	}
	return authsys.OperationLogChange{
		Field: field, Label: label,
		Before: normalizeSafeValue(before), After: normalizeSafeValue(after),
	}
}

// safeCredentialsChange renders the create credentials change entry.
func safeCredentialsChange(value any) authsys.OperationLogChange {
	change := safeChange("credentials", "凭据", nil, value)
	if value == nil {
		change.After = "未设置"
	}
	return change
}

var sensitiveChangeContainers = map[string]bool{
	"credentials": true, "credential": true, "token": true, "key": true,
	"secret": true, "password": true, "apikey": true, "api_key": true,
	"apikeys": true, "api_keys": true, "accesstoken": true, "access_token": true,
}

func isSensitiveChangeField(field string) bool {
	return sensitiveChangeContainers[strings.TrimSpace(strings.ToLower(field))]
}

// normalizeSafeValue mirrors normalizeSafeValue: strings truncate at 200,
// structured values serialize truncated (the sink stores Before/After as text).
func normalizeSafeValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		if len(typed) > 200 {
			return typed[:200] + "..."
		}
		return typed
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		text := string(encoded)
		if len(text) > 500 {
			return text[:500]
		}
		return text
	}
}

func textString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

// createBody mirrors accountCreateSchema.strict(): the allowed key set plus
// per-field type contracts. Returns (input, message); a non-empty message
// rejects the payload with 400.
func createBody(body map[string]any) (CreateInput, string) {
	input := CreateInput{}
	providerCode := strings.TrimSpace(textString(body["providerCode"]))
	if providerCode == "" {
		return CreateInput{}, "账户参数无效"
	}
	profileID := strings.TrimSpace(textString(body["providerProtocolProfileId"]))
	if profileID == "" {
		return CreateInput{}, "账户参数无效"
	}
	name := strings.TrimSpace(textString(body["name"]))
	if name == "" {
		return CreateInput{}, "账户参数无效"
	}
	accountType := strings.TrimSpace(textString(body["type"]))
	if accountType == "" {
		return CreateInput{}, "账户参数无效"
	}
	for key := range body {
		switch key {
		case "providerCode", "providerProtocolProfileId", "name", "type", "credentials",
			"supportedModels", "healthCheckModel", "healthCheckEndpointMode", "modelMappings",
			"tags", "status", "skipInitialHealthCheck", "concurrencyLimit", "priority",
			"superPriorityEnabled", "fallbackEnabled", "proxyProfileId", "schedulable",
			"groupId", "accountExpiresAt", "availabilitySchedule", "balanceQueryEnabled",
			"balanceQueryConfig", "temporaryUnavailableContinuousProbeEnabled", "notes":
		default:
			return CreateInput{}, "账户参数无效"
		}
	}
	input.ProviderCode = providerCode
	input.ProviderProtocolProfileID = profileID
	input.Name = name
	input.AccountType = accountType
	if credentials, ok := body["credentials"].(map[string]any); ok {
		input.Credentials = Credentials(credentials)
	}
	if value, exists := body["supportedModels"]; exists && value != nil {
		models, err := normalizeSupportedModelsInput(value)
		if err != nil {
			return CreateInput{}, "账户参数无效"
		}
		if len(models) == 0 {
			return CreateInput{}, "账户参数无效"
		}
		input.SupportedModels = models
	}
	if value, exists := body["healthCheckModel"]; exists && value != nil {
		text, ok := value.(string)
		if !ok {
			return CreateInput{}, "账户参数无效"
		}
		input.HealthCheckModel = &text
	}
	if value, exists := body["healthCheckEndpointMode"]; exists && value != nil {
		text, ok := value.(string)
		if !ok {
			return CreateInput{}, "账户参数无效"
		}
		input.HealthCheckEndpointMode = &text
	}
	if value, exists := body["modelMappings"]; exists && value != nil {
		mappings, ok := value.([]any)
		if !ok {
			return CreateInput{}, "账户参数无效"
		}
		for _, item := range mappings {
			object, ok := item.(map[string]any)
			if !ok {
				return CreateInput{}, "账户参数无效"
			}
			mapping := ModelMapping{
				SourceModel:            strings.TrimSpace(textString(object["sourceModel"])),
				SourceEndpointFamily:   strings.TrimSpace(textString(object["sourceEndpointFamily"])),
				UpstreamModel:          strings.TrimSpace(textString(object["upstreamModel"])),
				UpstreamEndpointFamily: strings.TrimSpace(textString(object["upstreamEndpointFamily"])),
			}
			if mapping.SourceModel == "" || mapping.UpstreamModel == "" ||
				mapping.SourceEndpointFamily == "" || mapping.UpstreamEndpointFamily == "" {
				return CreateInput{}, "账户参数无效"
			}
			if enabled, ok := object["enabled"].(bool); ok {
				mapping.Enabled = &enabled
			}
			input.ModelMappings = append(input.ModelMappings, mapping)
		}
	}
	if value, exists := body["tags"]; exists && value != nil {
		if _, ok := value.([]any); !ok {
			return CreateInput{}, "账户参数无效"
		}
		if raw, err := json.Marshal(value); err == nil {
			var tags []string
			_ = json.Unmarshal(raw, &tags)
			input.Tags = tags
		}
	}
	input.Status = AccountCreationStatusInput(body["status"])
	if value, exists := body["concurrencyLimit"]; exists && value != nil {
		number, ok := value.(float64)
		if !ok || number != float64(int(number)) {
			return CreateInput{}, "账户参数无效"
		}
		limit := int(number)
		input.ConcurrencyLimit = &limit
	}
	if value, exists := body["priority"]; exists && value != nil {
		number, ok := value.(float64)
		if !ok || number != float64(int(number)) {
			return CreateInput{}, "账户参数无效"
		}
		priority := int(number)
		input.Priority = &priority
	}
	if value, exists := body["superPriorityEnabled"]; exists && value != nil {
		enabled, ok := value.(bool)
		if !ok {
			return CreateInput{}, "账户参数无效"
		}
		input.SuperPriorityEnabled = &enabled
	}
	if value, exists := body["fallbackEnabled"]; exists && value != nil {
		enabled, ok := value.(bool)
		if !ok {
			return CreateInput{}, "账户参数无效"
		}
		input.FallbackEnabled = &enabled
	}
	if value, exists := body["proxyProfileId"]; exists {
		if value == nil {
			input.ProxyProfileID = nil
		} else if text, ok := value.(string); ok {
			input.ProxyProfileID = &text
		} else {
			return CreateInput{}, "账户参数无效"
		}
	}
	if value, exists := body["groupId"]; exists {
		if value == nil {
			input.GroupID = nil
		} else if text, ok := value.(string); ok {
			input.GroupID = &text
		} else {
			return CreateInput{}, "账户参数无效"
		}
	}
	if value, exists := body["accountExpiresAt"]; exists {
		if value == nil {
			input.AccountExpiresAt = nil
		} else if text, ok := value.(string); ok {
			input.AccountExpiresAt = &text
		} else {
			return CreateInput{}, "账户参数无效"
		}
	}
	if value, exists := body["availabilitySchedule"]; exists {
		input.AvailabilitySchedule = value
	}
	if value, exists := body["balanceQueryEnabled"]; exists && value != nil {
		if enabled, ok := value.(bool); ok {
			input.BalanceQueryEnabled = enabled
		}
	}
	if value, exists := body["balanceQueryConfig"]; exists && value != nil {
		input.BalanceQueryConfig = value
	}
	if value, exists := body["notes"]; exists && value != nil {
		text, ok := value.(string)
		if !ok {
			return CreateInput{}, "账户参数无效"
		}
		input.Notes = &text
	}
	return input, ""
}

// patchBody mirrors accountUpdateSchema.strict() for the basic editable field
// set. expectedConfigRevision is required (integer >= 1).
func patchBody(body map[string]any) (PatchInput, string) {
	input := PatchInput{}
	for key := range body {
		switch key {
		case "expectedConfigRevision", "name", "notes", "status", "concurrencyLimit",
			"priority", "superPriorityEnabled", "fallbackEnabled", "schedulable",
			"credentials", "credentialsPatch", "supportedModels", "healthCheckModel",
			"healthCheckEndpointMode", "tags", "accountExpiresAt", "availabilitySchedule",
			"clearFailureState":
		default:
			return PatchInput{}, "账户更新参数无效"
		}
	}
	revision, ok := body["expectedConfigRevision"].(float64)
	if !ok || revision != float64(int64(revision)) || revision < 1 {
		return PatchInput{}, "账户配置版本无效"
	}
	input.ExpectedConfigRevision = int64(revision)
	if value, exists := body["name"]; exists && value != nil {
		text, ok := value.(string)
		if !ok {
			return PatchInput{}, "账户更新参数无效"
		}
		input.Name = &text
	}
	if value, exists := body["notes"]; exists && value != nil {
		text, ok := value.(string)
		if !ok {
			return PatchInput{}, "账户更新参数无效"
		}
		input.Notes = &text
	}
	if value, exists := body["status"]; exists && value != nil {
		text, ok := value.(string)
		if !ok {
			return PatchInput{}, "账户更新参数无效"
		}
		input.Status = &text
	}
	if value, exists := body["concurrencyLimit"]; exists && value != nil {
		number, ok := value.(float64)
		if !ok || number != float64(int(number)) {
			return PatchInput{}, "账户更新参数无效"
		}
		limit := int(number)
		input.ConcurrencyLimit = &limit
	}
	if value, exists := body["priority"]; exists && value != nil {
		number, ok := value.(float64)
		if !ok || number != float64(int(number)) {
			return PatchInput{}, "账户更新参数无效"
		}
		priority := int(number)
		input.Priority = &priority
	}
	if value, exists := body["superPriorityEnabled"]; exists && value != nil {
		enabled, ok := value.(bool)
		if !ok {
			return PatchInput{}, "账户更新参数无效"
		}
		input.SuperPriorityEnabled = &enabled
	}
	if value, exists := body["fallbackEnabled"]; exists && value != nil {
		enabled, ok := value.(bool)
		if !ok {
			return PatchInput{}, "账户更新参数无效"
		}
		input.FallbackEnabled = &enabled
	}
	if value, exists := body["schedulable"]; exists && value != nil {
		enabled, ok := value.(bool)
		if !ok {
			return PatchInput{}, "账户更新参数无效"
		}
		input.Schedulable = &enabled
	}
	if value, exists := body["credentials"]; exists && value != nil {
		credentials, ok := value.(map[string]any)
		if !ok {
			return PatchInput{}, "账户更新参数无效"
		}
		if _, conflict := body["credentialsPatch"]; conflict {
			return PatchInput{}, "credentials 与 credentialsPatch 不能同时提交"
		}
		input.Credentials = Credentials(credentials)
		input.CredentialsPresent = true
	}
	if value, exists := body["credentialsPatch"]; exists && value != nil {
		credentials, ok := value.(map[string]any)
		if !ok {
			return PatchInput{}, "账户更新参数无效"
		}
		input.Credentials = Credentials(credentials)
		input.CredentialsPresent = true
	}
	if value, exists := body["supportedModels"]; exists && value != nil {
		models, err := normalizeSupportedModelsInput(value)
		if err != nil {
			return PatchInput{}, "账户更新参数无效"
		}
		if len(models) == 0 {
			return PatchInput{}, "账户更新参数无效"
		}
		input.SupportedModels = models
		input.SupportedModelsPresent = true
	}
	if value, exists := body["healthCheckModel"]; exists && value != nil {
		text, ok := value.(string)
		if !ok {
			return PatchInput{}, "账户更新参数无效"
		}
		input.HealthCheckModel = &text
	}
	if value, exists := body["healthCheckEndpointMode"]; exists && value != nil {
		text, ok := value.(string)
		if !ok {
			return PatchInput{}, "账户更新参数无效"
		}
		input.HealthCheckEndpointMode = &text
	}
	if value, exists := body["tags"]; exists && value != nil {
		if _, ok := value.([]any); !ok {
			return PatchInput{}, "账户更新参数无效"
		}
		if raw, err := json.Marshal(value); err == nil {
			var tags []string
			_ = json.Unmarshal(raw, &tags)
			input.Tags = tags
		}
		input.TagsPresent = true
	}
	if value, exists := body["accountExpiresAt"]; exists {
		input.AccountExpiresAtPresent = true
		if value != nil {
			if text, ok := value.(string); ok {
				input.AccountExpiresAt = &text
			} else {
				return PatchInput{}, "账户更新参数无效"
			}
		}
	}
	if value, exists := body["availabilitySchedule"]; exists {
		input.AvailabilitySchedulePresent = true
		input.AvailabilitySchedule = value
	}
	if value, exists := body["clearFailureState"]; exists && value != nil {
		if enabled, ok := value.(bool); ok {
			input.ClearFailureState = enabled
		}
	}
	return input, ""
}

// tagsPatchBody mirrors accountTagsUpdateSchema.strict().
func tagsPatchBody(body map[string]any) (PatchInput, string) {
	input := PatchInput{}
	for key := range body {
		switch key {
		case "tags", "expectedConfigRevision":
		default:
			return PatchInput{}, "账户标签参数无效"
		}
	}
	revision, ok := body["expectedConfigRevision"].(float64)
	if !ok || revision != float64(int64(revision)) || revision < 1 {
		return PatchInput{}, "账户标签参数无效"
	}
	input.ExpectedConfigRevision = int64(revision)
	rawTags, ok := body["tags"].([]any)
	if !ok {
		return PatchInput{}, "账户标签参数无效"
	}
	if len(rawTags) > maxTagsPerAccount {
		return PatchInput{}, "单个账户最多配置 24 个标签"
	}
	if raw, err := json.Marshal(rawTags); err == nil {
		var tags []string
		_ = json.Unmarshal(raw, &tags)
		input.Tags = tags
	}
	input.TagsPresent = true
	return input, ""
}

// lockBody mirrors accountLockSchema.strict().
func lockBody(body map[string]any) (SetLockInput, string) {
	input := SetLockInput{}
	for key := range body {
		switch key {
		case "expectedConfigRevision", "lockDeathTimeoutSeconds", "lockRetryIntervalSeconds":
		default:
			return SetLockInput{}, "锁死参数无效"
		}
	}
	revision, ok := body["expectedConfigRevision"].(float64)
	if !ok || revision != float64(int64(revision)) || revision < 1 {
		return SetLockInput{}, "锁死参数无效"
	}
	input.ExpectedConfigRevision = int64(revision)
	if value, exists := body["lockDeathTimeoutSeconds"]; exists && value != nil {
		number, ok := value.(float64)
		if !ok || number != float64(int(number)) {
			return SetLockInput{}, "锁死死亡窗口必须是 30..3600 的整数"
		}
		timeout := int(number)
		input.LockDeathTimeoutSeconds = &timeout
	}
	if value, exists := body["lockRetryIntervalSeconds"]; exists && value != nil {
		number, ok := value.(float64)
		if !ok || number != float64(int(number)) {
			return SetLockInput{}, "锁死重试间隔必须是 5..30 的整数"
		}
		interval := int(number)
		input.LockRetryIntervalSeconds = &interval
	}
	return input, ""
}

// parseSortQuery mirrors parseAccountListSort: "field:order" pairs; unknown
// entries are dropped.
func parseSortQuery(values []string) []ListSort {
	sorts := []ListSort{}
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			text := strings.TrimSpace(item)
			if text == "" {
				continue
			}
			parts := strings.Split(text, ":")
			if len(parts) != 2 {
				continue
			}
			field := strings.TrimSpace(parts[0])
			order := strings.TrimSpace(parts[1])
			if order != "asc" && order != "desc" {
				continue
			}
			sorts = append(sorts, ListSort{Field: field, Order: order})
		}
	}
	return sorts
}

func textListQuery(values []string) []string {
	out := []string{}
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				out = append(out, trimmed)
			}
		}
	}
	return out
}

// statusQueryValue mirrors statusQueryValue: comma separated, 'all' dropped,
// deduplicated, rejoined.
func statusQueryValue(value string) string {
	statuses := accountStatusFilterValues(value)
	if len(statuses) == 0 {
		return ""
	}
	return strings.Join(statuses, ",")
}

func schedulableQueryValue(value string) string {
	text := strings.TrimSpace(value)
	switch text {
	case "all", "enabled", "disabled", "cooling":
		return text
	default:
		return ""
	}
}

// optionLimitValue mirrors optionLimitValue: 1..50, default 50.
func optionLimitValue(raw string) int {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return maxAccountOptionPageSize
	}
	value, err := parseInteger(trimmed)
	if err != nil {
		return maxAccountOptionPageSize
	}
	return minInt(maxAccountOptionPageSize, maxInt(1, value))
}

// integerQueryValue mirrors integerQueryValue: blank or non-integer text
// counts as absent (0).
func integerQueryValue(raw string) int {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0
	}
	value, err := parseInteger(trimmed)
	if err != nil {
		return 0
	}
	return value
}

func parseInteger(raw string) (int, error) {
	value := 0
	negative := false
	for index, char := range raw {
		if index == 0 && char == '-' {
			negative = true
			continue
		}
		if char < '0' || char > '9' {
			return 0, errors.New("not an integer")
		}
		value = value*10 + int(char-'0')
	}
	if negative {
		value = -value
	}
	return value, nil
}
