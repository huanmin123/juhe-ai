package accounts

// M11 route family (Node accounts.routes.ts registers every family on the
// shared router, so the admin /accounts and the forceSelfAccessScope
// /my-accounts surfaces both expose them):
//
//	GET  /{id}/advanced                      (account-detail.routes.ts)
//	GET  /{id}/oauth-reauthorization-context (account-detail.routes.ts)
//	GET  /{id}/api-key-runtime               (account-detail.routes.ts)
//	GET  /{id}/balance/details               (account-balance.routes.ts)
//	POST /{id}/balance/refresh               (account-balance.routes.ts)
//	POST /balance/test-draft                 (account-balance.routes.ts)
//	POST /model-catalog/refresh              (accounts.routes.ts)
//	POST /{id}/force-activate                (account-force-activate.routes.ts)
//	POST /{id}/traffic-migration             (account-traffic-migration.routes.ts)
//	POST /{id}/return-authorization          (account-authorization-return.routes.ts)
//	PATCH /{id}/authorized-dispatch          (account-authorized-dispatch.routes.ts)
//	POST /{id}/group                         (account-group-binding.routes.ts)

import (
	"errors"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// mountM11Routes registers the M11 family on both surfaces.
func (d *Deps) mountM11Routes(k *kernel.Kernel, prefix string) {
	admin := d.Auth.RequireAdmin
	self := d.Auth.RequireSession(true)
	pairs := []struct {
		path    string
		method  string
		admin   http.Handler
		self    http.Handler
	}{
		{"/accounts/{id}/advanced", "GET", admin(d.scoped(d.advanced(false))), self(d.scoped(d.advanced(true)))},
		{"/accounts/{id}/oauth-reauthorization-context", "GET", admin(d.scoped(d.oauthReauthorizationContext(false))), self(d.scoped(d.oauthReauthorizationContext(true)))},
		{"/accounts/{id}/api-key-runtime", "GET", admin(d.scoped(d.apiKeyRuntime(false))), self(d.scoped(d.apiKeyRuntime(true)))},
		{"/accounts/{id}/balance/details", "GET", admin(d.scoped(d.balanceDetails(false))), self(d.scoped(d.balanceDetails(true)))},
		{"/accounts/{id}/balance/refresh", "POST", admin(d.scoped(d.balanceRefresh(false))), self(d.scoped(d.balanceRefresh(true)))},
		{"/accounts/balance/test-draft", "POST", admin(d.scoped(d.balanceTestDraft(false))), self(d.scoped(d.balanceTestDraft(true)))},
		{"/accounts/model-catalog/refresh", "POST", admin(d.scoped(d.modelCatalogRefresh(false))), self(d.scoped(d.modelCatalogRefresh(true)))},
		{"/accounts/{id}/traffic-migration", "POST", admin(d.scoped(d.trafficMigration(false))), self(d.scoped(d.trafficMigration(true)))},
		{"/accounts/{id}/authorized-dispatch", "PATCH", admin(d.scoped(d.authorizedDispatch(false))), self(d.scoped(d.authorizedDispatch(true)))},
		{"/accounts/{id}/group", "POST", admin(d.scoped(d.groupBinding(false))), self(d.scoped(d.groupBinding(true)))},
	}
	for _, item := range pairs {
		k.Register(item.method+" "+prefix+item.path, item.admin)
		k.Register(item.method+" "+prefix+strings.Replace(item.path, "/accounts/", "/my-accounts/", 1), item.self)
	}
	// Mutation-guarded writes.
	k.Register("POST "+prefix+"/accounts/{id}/force-activate", d.m11Guarded(d.forceActivate(false), "accounts.force_activate_pending", forceActivateFingerprint, false))
	k.Register("POST "+prefix+"/my-accounts/{id}/force-activate", d.m11Guarded(d.forceActivate(true), "accounts.force_activate_pending", forceActivateFingerprint, true))
	k.Register("POST "+prefix+"/accounts/{id}/return-authorization", d.m11Guarded(d.returnAuthorization(false), "accounts.return_authorization", returnAuthorizationFingerprint, false))
	k.Register("POST "+prefix+"/my-accounts/{id}/return-authorization", d.m11Guarded(d.returnAuthorization(true), "accounts.return_authorization", returnAuthorizationFingerprint, true))
}

// m11Guarded mirrors mountGuarded with a per-family fingerprint.
func (d *Deps) m11Guarded(next http.HandlerFunc, operationKey string, fingerprint func(r *http.Request, operationKey string) map[string]any, selfOnly bool) http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: operationKey,
		Actor:        actorResolver,
		Scope: func(r *http.Request) (any, error) {
			return strings.TrimSpace(r.URL.Query().Get("systemAccountId")), nil
		},
		Fingerprint: func(r *http.Request) (any, error) {
			return fingerprint(r, operationKey), nil
		},
	})
	handler := guard(next)
	if selfOnly {
		return d.Auth.RequireSession(true)(handler)
	}
	return d.Auth.RequireAdmin(handler)
}

func forceActivateFingerprint(r *http.Request, _ string) map[string]any {
	return map[string]any{
		"accountId":                    strings.TrimSpace(r.PathValue("id")),
		"acknowledgedAccountAvailable": kernel.BodyField(r, "acknowledgedAccountAvailable") == true,
	}
}

func returnAuthorizationFingerprint(r *http.Request, _ string) map[string]any {
	return map[string]any{
		"accountId": strings.TrimSpace(r.PathValue("id")),
		"grantee":   strings.TrimSpace(r.URL.Query().Get("systemAccountId")),
	}
}

// m11DetailHandler adapts the selfOnly flag into the request scope.
func (d *Deps) m11Scope(r *http.Request, selfOnly bool) AccessScope {
	return requestScopeFor(r, selfOnly)
}

// ---- GET /{id}/advanced ----

func (d *Deps) advanced(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setNoStoreHeaders(w)
		detail, err := d.Store.FindAdvancedDetail(r.Context(), r.PathValue("id"), d.m11Scope(r, selfOnly))
		if err != nil {
			d.writeM11ReadError(w, err)
			return
		}
		if detail == nil {
			kernel.WriteError(w, http.StatusNotFound, "账户不存在")
			return
		}
		kernel.WriteOK(w, detail, "")
	}
}

// ---- GET /{id}/oauth-reauthorization-context ----

func (d *Deps) oauthReauthorizationContext(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setNoStoreHeaders(w)
		context, err := d.Store.FindOAuthReauthorizationContext(r.Context(), r.PathValue("id"), d.m11Scope(r, selfOnly))
		if err != nil {
			var forbidden *interactionContextForbiddenError
			if errors.As(err, &forbidden) {
				kernel.WriteError(w, http.StatusForbidden, forbidden.message)
				return
			}
			d.writeM11ReadError(w, err)
			return
		}
		if context == nil {
			kernel.WriteError(w, http.StatusNotFound, "账户不存在")
			return
		}
		kernel.WriteOK(w, context, "")
	}
}

// ---- GET /{id}/api-key-runtime ----

func (d *Deps) apiKeyRuntime(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setNoStoreHeaders(w)
		account, err := d.Store.FindAPIKeyRuntimeAccount(r.Context(), r.PathValue("id"), d.m11Scope(r, selfOnly))
		if err != nil {
			d.writeM11ReadError(w, err)
			return
		}
		if account == nil {
			kernel.WriteError(w, http.StatusNotFound, "账户不存在")
			return
		}
		if account.AccessType == "authorized" {
			kernel.WriteError(w, http.StatusForbidden, "授权实例不能查看来源账户 API Key 运行明细")
			return
		}
		runtime, err := d.Store.LoadAPIKeyRuntimeResponse(r.Context(), account)
		if err != nil {
			d.writeM11ReadError(w, err)
			return
		}
		if runtime == nil {
			kernel.WriteError(w, http.StatusForbidden, "无权查看账户 API Key 运行明细")
			return
		}
		kernel.WriteOK(w, runtime, "")
	}
}

// ---- GET /{id}/balance/details ----

func (d *Deps) balanceDetails(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		details, err := d.Store.FindBalanceDetails(r.Context(), r.PathValue("id"), d.m11Scope(r, selfOnly))
		if err != nil {
			if errors.Is(err, errBalanceDetailsDisabled) {
				kernel.WriteError(w, http.StatusNotFound, err.Error())
				return
			}
			if errors.Is(err, errBalanceDetailsForbidden) {
				kernel.WriteError(w, http.StatusForbidden, err.Error())
				return
			}
			d.writeM11ReadError(w, err)
			return
		}
		if details == nil {
			kernel.WriteError(w, http.StatusNotFound, "账户不存在")
			return
		}
		setNoStoreHeaders(w)
		payload := map[string]any{
			"accountId":       details.AccountID,
			"keyCount":        details.KeyCount,
			"queriedKeyCount": details.QueriedKeyCount,
			"scope":           details.Scope,
			"aggregation":     details.Aggregation,
			"keyBalances":     details.KeyBalances,
		}
		if details.ConfigRevision > 0 {
			payload["configRevision"] = details.ConfigRevision
		}
		if details.SnapshotRevision != nil {
			payload["configRevision"] = details.SnapshotRevision
		}
		if details.UpdatedAt != nil {
			payload["updatedAt"] = *details.UpdatedAt
		}
		kernel.WriteOK(w, payload, "")
	}
}

// ---- POST /{id}/balance/refresh ----

func (d *Deps) balanceRefresh(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		access := d.m11Scope(r, selfOnly)
		account, err := d.Store.FindAPIKeyRuntimeAccount(r.Context(), r.PathValue("id"), access)
		if err != nil {
			d.writeM11ReadError(w, err)
			return
		}
		if account == nil {
			kernel.WriteError(w, http.StatusNotFound, "账户不存在")
			return
		}
		if account.AccessType == "authorized" || !d.refreshPermissionsAllow(r, access) {
			// Node guard: accessType==='authorized' or canEdit===false.
			kernel.WriteError(w, http.StatusForbidden, errBalanceRefreshForbidden.Error())
			return
		}
		candidate, err := d.Store.FindBalanceManualRefreshCandidate(r.Context(), r.PathValue("id"))
		if err != nil {
			d.writeM11ReadError(w, err)
			return
		}
		if candidate == nil {
			kernel.WriteBadRequest(w, "账户未开启余额查询或当前账户类型不支持")
			return
		}
		refresher := d.Store.wiredBalanceRefresher()
		if refresher == nil {
			println("accounts slice balance refresher port not wired")
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		result, err := refresher.RefreshManual(r.Context(), *candidate)
		if err != nil {
			d.writeM11ReadError(w, err)
			return
		}
		if !result.Persisted {
			message := "账户余额配置已变化，请刷新列表后重试"
			if result.Outcome == "lease_busy" {
				message = "余额查询正在进行，请稍后刷新"
			}
			kernel.WriteJSON(w, http.StatusConflict, map[string]any{"message": message})
			return
		}
		setNoStoreHeaders(w)
		kernel.WriteOK(w, result.Snapshot, "")
	}
}

// refreshPermissionsAllow re-checks the Node canEdit guard on the summary
// (owner rows and admins manage; instance rows never reach here).
func (d *Deps) refreshPermissionsAllow(r *http.Request, access AccessScope) bool {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		return false
	}
	return access.canAccessAll() || access.ViewerID != ""
}

// wiredBalanceRefresher returns the wired port or nil.
func (s *Store) wiredBalanceRefresher() ManualBalanceRefresher {
	return s.balanceRefresher
}

// ---- POST /balance/test-draft ----

func (d *Deps) balanceTestDraft(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		for key := range body {
			switch key {
			case "account", "balanceQueryConfig":
			default:
				kernel.WriteBadRequest(w, "余额查询测试参数无效")
				return
			}
		}
		accountInput, _ := body["account"].(map[string]any)
		if accountInput == nil {
			kernel.WriteBadRequest(w, "余额查询测试参数无效")
			return
		}
		config, present := body["balanceQueryConfig"]
		if !present {
			kernel.WriteBadRequest(w, "余额查询测试参数无效")
			return
		}
		normalizedConfig, err := NormalizeAccountBalanceConfig(config)
		if err != nil {
			kernel.WriteBadRequest(w, "余额查询测试参数无效")
			return
		}
		draft, err := d.Store.prepareBalanceDraft(r.Context(), accountInput, d.m11Scope(r, selfOnly))
		if err != nil {
			kernel.WriteBadRequest(w, pipelineErrorMessage(err, "余额查询测试失败"))
			return
		}
		// Node validateAccountBalanceCapability(preparedDraft.account, true):
		// an unsupported capability rejects with 400 before the probe; the
		// multi-Key rejection happens inside the probe and resolves to the
		// failed-snapshot shape.
		if _, err := ValidateAccountBalanceCapability(BalanceCapabilityInput{
			AccountType: draft.accountType,
			Credentials: draft.credentials,
		}, true); err != nil {
			kernel.WriteBadRequest(w, err.Error())
			return
		}
		refresher := d.Store.wiredBalanceRefresher()
		proxyProfileID := draft.proxyProfileID
		if refresher == nil {
			// Node never throws out of the draft probe: every failure resolves
			// to a failed snapshot with 200.
			setNoStoreHeaders(w)
			kernel.WriteOK(w, map[string]any{
				"status":       "failed",
				"errorMessage": "余额查询执行器未装配",
			}, "")
			return
		}
		snapshot, err := refresher.TestDraft(r.Context(), BalanceDraftProbeInput{
			ID:             "",
			Credentials:    draft.credentials,
			Config:         normalizedConfig,
			ProxyProfileID: proxyProfileID,
		})
		if err != nil {
			setNoStoreHeaders(w)
			kernel.WriteOK(w, map[string]any{
				"status":       "failed",
				"errorMessage": pipelineErrorMessage(err, "余额查询测试失败"),
			}, "")
			return
		}
		setNoStoreHeaders(w)
		kernel.WriteOK(w, snapshot, "")
	}
}

// multiKeyBalanceQueryMessage mirrors MULTI_KEY_ACCOUNT_BALANCE_QUERY_MESSAGE.
const multiKeyBalanceQueryMessage = "多 Key 账户余额将按 Key 查询并在口径明确时合计"

// ---- POST /model-catalog/refresh ----

func (d *Deps) modelCatalogRefresh(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		for key := range body {
			switch key {
			case "account":
			default:
				kernel.WriteBadRequest(w, "模型目录同步参数无效")
				return
			}
		}
		accountInput, _ := body["account"].(map[string]any)
		if accountInput == nil {
			kernel.WriteBadRequest(w, "模型目录同步参数无效")
			return
		}
		draft, err := d.Store.prepareBalanceDraft(r.Context(), accountInput, d.m11Scope(r, selfOnly))
		if err != nil {
			kernel.WriteBadRequest(w, pipelineErrorMessage(err, "获取上游模型目录失败"))
			return
		}
		refresher := d.Store.wiredModelCatalogRefresher()
		if refresher == nil {
			kernel.WriteBadRequest(w, "获取上游模型目录失败")
			return
		}
		result, err := refresher.RefreshDraftModelCatalog(r.Context(), ModelCatalogDiscoveryInput{
			OwnerSystemAccountID: draft.ownerID,
			ProviderCode:         draft.providerCode,
			ProviderProfileID:    draft.providerProfile.id,
			AccountType:          textString(accountInput["type"]),
			Credentials:          draft.credentials,
			ProxyProfileID:       draft.proxyProfileID,
			SupportedModels:      draft.supportedModels,
		})
		if err != nil {
			kernel.WriteBadRequest(w, pipelineErrorMessage(err, "获取上游模型目录失败"))
			return
		}
		setNoStoreHeaders(w)
		kernel.WriteOK(w, result, "")
	}
}

// wiredModelCatalogRefresher returns the wired port or nil.
func (s *Store) wiredModelCatalogRefresher() ModelCatalogRefresher {
	return s.modelCatalogRefresher
}

// ---- POST /{id}/force-activate ----

func (d *Deps) forceActivate(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		access := d.m11Scope(r, selfOnly)
		if !scopeQueryOK(r) {
			kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
			return
		}
		if kernel.BodyField(r, "acknowledgedAccountAvailable") != true {
			kernel.WriteBadRequest(w, "请先确认账户当前可用并接受人工恢复风险")
			return
		}
		auth := authsys.AuthContextFrom(r)
		if auth == nil {
			kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		before, err := d.Store.findTrafficSummary(r.Context(), r.PathValue("id"), access)
		if err != nil {
			d.writeM11ReadError(w, err)
			return
		}
		if before == nil {
			kernel.WriteError(w, http.StatusNotFound, "账户不存在")
			return
		}
		if before.AccessType == "authorized" {
			kernel.WriteBadRequest(w, "授权账户不能人工恢复来源账户状态")
			return
		}
		if before.Status != "pending_test" {
			kernel.WriteJSON(w, http.StatusConflict, map[string]any{"message": "只有待检查账户可以人工恢复可调度"})
			return
		}
		result, err := d.Store.ForceActivatePending(r.Context(), r.PathValue("id"), access)
		if err != nil {
			d.writeM11ReadError(w, err)
			return
		}
		if result == nil || result.Account == nil {
			kernel.WriteError(w, http.StatusNotFound, "账户不存在")
			return
		}
		if !result.Changed {
			kernel.WriteJSON(w, http.StatusConflict, map[string]any{"message": "账户状态已变化，请刷新后重试"})
			return
		}
		if d.Sink != nil && result.Changed {
			d.Sink.Record(authsys.OperationLogEntry{
				ActorSystemAccountID:          auth.SystemAccountID,
				ActorUsername:                 auth.Username,
				ActorDisplayName:              auth.DisplayName,
				ActorRole:                     auth.Role,
				OperationScopeSystemAccountID: result.Account.OwnerSystemAccountID,
				Mode:                          operationMode(access),
				Module:                        "accounts",
				Action:                        "force_activate",
				OperationKey:                  "accounts.force_activate_pending",
				ResourceType:                  "account",
				ResourceID:                    result.Account.ID,
				ResourceName:                  result.Account.Name,
				Summary:                       "人工恢复待检查 AI 账户：" + result.Account.Name,
				Changes: []authsys.OperationLogChange{
					safeChange("status", "状态", before.Status, result.Account.Status),
					safeChange("acknowledgedAccountAvailable", "确认账户当前可用", false, true),
				},
				Viewers: []authsys.OperationLogViewer{
					{SystemAccountID: result.Account.OwnerSystemAccountID, Reason: "resource_owner"},
				},
			}, r)
		}
		setNoStoreHeaders(w)
		kernel.WriteOK(w, result.Account, "")
	}
}

func containsStatusChanged(message string) bool {
	return strings.Contains(message, "状态已变化")
}

var _ = containsStatusChanged

// ---- POST /{id}/traffic-migration ----

func (d *Deps) trafficMigration(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		access := d.m11Scope(r, selfOnly)
		if !scopeQueryOK(r) {
			kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
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
		input, message := parseTrafficMigrationBody(body)
		if message != "" {
			kernel.WriteBadRequest(w, message)
			return
		}
		sourceStatus := input.SourceStatus
		if sourceStatus == "" {
			sourceStatus = trafficSourceTemporaryUnavailable
		}
		result, err := d.Store.MigrateTraffic(r.Context(), r.PathValue("id"), input, access)
		if err != nil {
			var failure *trafficMigrationFailure
			if errors.As(err, &failure) {
				kernel.WriteBadRequest(w, failure.message)
				return
			}
			if errors.Is(err, errTrafficSameAccount) {
				kernel.WriteBadRequest(w, err.Error())
				return
			}
			d.writeM11ReadError(w, err)
			return
		}
		if result == nil {
			kernel.WriteError(w, http.StatusNotFound, "账户不存在或无权迁移")
			return
		}
		// Operation log first (Node runLoggedOperationAsync lands the log
		// before the runtime handover), so a runtime migration failure keeps
		// the audit trail like the Node route.
		if d.Sink != nil {
			owner := result.SourceAccount.OwnerSystemAccountID
			targetOwner := result.TargetAccount.OwnerSystemAccountID
			if result.SourceAccount.AccessType == "authorized" {
				owner = access.viewerID()
			}
			d.Sink.Record(authsys.OperationLogEntry{
				ActorSystemAccountID:          auth.SystemAccountID,
				ActorUsername:                 auth.Username,
				ActorDisplayName:              auth.DisplayName,
				ActorRole:                     auth.Role,
				OperationScopeSystemAccountID: owner,
				Mode:                          operationMode(access),
				Module:                        "accounts",
				Action:                        "traffic_migration",
				OperationKey:                  "accounts.traffic_migration",
				ResourceType:                  "account",
				ResourceID:                    result.SourceAccount.ID,
				ResourceName:                  result.SourceAccount.Name,
				Summary:                       "迁移账户流量：" + result.SourceAccount.Name + " -> " + result.TargetAccount.Name,
				Changes: []authsys.OperationLogChange{
					safeChange("targetAccountId", "目标账户", nil, result.TargetAccount.Name),
					safeChange("sourceStatus", "源账户状态", nil, result.SourceStatus),
				},
				Viewers: []authsys.OperationLogViewer{
					{SystemAccountID: owner, Reason: "resource_owner"},
				},
			}, r)
			_ = targetOwner
		}
		// Gateway runtime session handover (Node
		// migrateServerOpenAIAccountTrafficRuntime). A wired-port failure is
		// an explicit error outlet — the Node catch renders the message —
		// never a silent zero-count degradation. A nil port (chain disabled)
		// keeps the { migratedSessionCount: 0 } fallback.
		migrated := 0
		if migrator := d.Store.wiredTrafficRuntimeMigrator(); migrator != nil {
			count, migrateErr := migrator.MigrateOpenAIAccountTrafficRuntime(r.Context(), buildRuntimeMigrationInput(result, input, access))
			if migrateErr != nil {
				kernel.WriteBadRequest(w, migrateErr.Error())
				return
			}
			migrated = count
		}
		setNoStoreHeaders(w)
		payload := map[string]any{
			"sourceAccount":        result.SourceAccount,
			"targetAccount":        result.TargetAccount,
			"migratedSessionCount": migrated,
			"sourceStatus":         result.SourceStatus,
		}
		if result.SourceCooldownUntil != nil {
			payload["sourceCooldownUntil"] = *result.SourceCooldownUntil
		}
		kernel.WriteOK(w, payload, "")
	}
}

// wiredTrafficRuntimeMigrator returns the wired port or nil.
func (s *Store) wiredTrafficRuntimeMigrator() TrafficRuntimeMigrator {
	return s.trafficRuntimeMigrator
}

// ---- POST /{id}/return-authorization ----

func (d *Deps) returnAuthorization(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		access := d.m11Scope(r, selfOnly)
		if !scopeQueryOK(r) {
			kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
			return
		}
		auth := authsys.AuthContextFrom(r)
		if auth == nil {
			kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		before, _ := d.Store.findTrafficSummary(r.Context(), r.PathValue("id"), access)
		err := d.Store.ReturnAuthorizationInstance(r.Context(), r.PathValue("id"), access)
		if err != nil {
			if errors.Is(err, errReturnAuthorizationMissing) {
				kernel.WriteError(w, http.StatusNotFound, err.Error())
				return
			}
			kernel.WriteBadRequest(w, pipelineErrorMessage(err, "归还授权账户失败"))
			return
		}
		if d.Sink != nil {
			resourceName := r.PathValue("id")
			if before != nil {
				resourceName = before.Name
			}
			d.Sink.Record(authsys.OperationLogEntry{
				ActorSystemAccountID:          auth.SystemAccountID,
				ActorUsername:                 auth.Username,
				ActorDisplayName:              auth.DisplayName,
				ActorRole:                     auth.Role,
				OperationScopeSystemAccountID: access.ViewerID,
				Mode:                          operationMode(access),
				Module:                        "authorizations",
				Action:                        "return",
				OperationKey:                  "accounts.return_authorization",
				ResourceType:                  "authorization",
				ResourceID:                    r.PathValue("id"),
				ResourceName:                  resourceName,
				Summary:                       "归还授权账户：" + resourceName,
				Changes: []authsys.OperationLogChange{
					safeChange("returned", "归还授权账户", false, true),
				},
				Viewers: []authsys.OperationLogViewer{
					{SystemAccountID: access.ViewerID, Reason: "authorization_grantee"},
				},
			}, r)
		}
		setNoStoreHeaders(w)
		w.WriteHeader(http.StatusNoContent)
	}
}

// ---- PATCH /{id}/authorized-dispatch ----

func (d *Deps) authorizedDispatch(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		access := d.m11Scope(r, selfOnly)
		if !scopeQueryOK(r) {
			kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
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
		input, message := parseAuthorizedDispatchBody(body)
		if message != "" {
			kernel.WriteBadRequest(w, message)
			return
		}
		result, err := d.Store.UpdateAuthorizedDispatch(r.Context(), r.PathValue("id"), input, access)
		if err != nil {
			var conflict *AuthorizedDispatchRevisionConflictError
			if errors.As(err, &conflict) {
				kernel.WriteJSON(w, http.StatusConflict, map[string]string{"message": RevisionConflictMessage})
				return
			}
			message := strings.TrimSpace(err.Error())
			if message == "授权账户不存在或尚未绑定分组" {
				kernel.WriteError(w, http.StatusNotFound, message)
				return
			}
			kernel.WriteBadRequest(w, message)
			return
		}
		if result == nil {
			kernel.WriteError(w, http.StatusNotFound, "授权账户不存在或尚未绑定分组")
			return
		}
		if d.Sink != nil && len(result.ChangedFields) > 0 {
			owner := result.OwnerSystemAccountID
			if owner == "" {
				owner = access.viewerID()
			}
			changes := make([]authsys.OperationLogChange, 0, len(result.Changes))
			for _, change := range result.Changes {
				label := authorizedDispatchChangeLabel(change.Field)
				changes = append(changes, safeChange(change.Field, label, change.Before, change.After))
			}
			d.Sink.Record(authsys.OperationLogEntry{
				ActorSystemAccountID:          auth.SystemAccountID,
				ActorUsername:                 auth.Username,
				ActorDisplayName:              auth.DisplayName,
				ActorRole:                     auth.Role,
				OperationScopeSystemAccountID: owner,
				Mode:                          operationMode(access),
				Module:                        "accounts",
				Action:                        "authorized_dispatch",
				OperationKey:                  "accounts.authorized_dispatch",
				ResourceType:                  "account",
				ResourceID:                    result.ID,
				ResourceName:                  result.Name,
				Summary:                       "调整授权账户使用设置：" + result.Name,
				Changes:                       changes,
				Viewers: []authsys.OperationLogViewer{
					{SystemAccountID: owner, Reason: "resource_owner"},
				},
			}, r)
		}
		setNoStoreHeaders(w)
		kernel.WriteOK(w, map[string]any{
			"id":             result.ID,
			"configRevision": result.ConfigRevision,
			"changedFields":  result.ChangedFields,
			"patch":          result.Patch,
		}, "")
	}
}

// authorizedDispatchChangeLabel mirrors authorizedDispatchChangeLabel.
func authorizedDispatchChangeLabel(field string) string {
	switch field {
	case "status":
		return "实例状态"
	case "schedulable":
		return "参与调度"
	case "priority":
		return "分组内优先级"
	case "superPriorityEnabled":
		return "分组内超级优先"
	case "fallbackEnabled":
		return "分组内降级备用"
	case "failureState":
		return "恢复实例异常状态"
	default:
		return field
	}
}

// ---- POST /{id}/group ----

func (d *Deps) groupBinding(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		access := d.m11Scope(r, selfOnly)
		if !scopeQueryOK(r) {
			kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
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
		// accountGroupSchema.strict(): { groupId, expectedConfigRevision }.
		for key := range body {
			switch key {
			case "groupId", "expectedConfigRevision":
			default:
				kernel.WriteBadRequest(w, "绑定分组参数无效")
				return
			}
		}
		groupID, _ := body["groupId"].(string)
		groupID = strings.TrimSpace(groupID)
		if groupID == "" {
			kernel.WriteBadRequest(w, "绑定分组参数无效")
			return
		}
		revision, ok := body["expectedConfigRevision"].(float64)
		if !ok || revision != float64(int64(revision)) || revision < 1 {
			kernel.WriteBadRequest(w, "绑定分组参数无效")
			return
		}
		input := PatchInput{ExpectedConfigRevision: int64(revision)}
		input.GroupIDPresent = true
		input.GroupID = &groupID
		result, err := d.Store.Patch(r.Context(), r.PathValue("id"), input, access)
		if err != nil {
			var revisionErr *RevisionConflictError
			if errors.As(err, &revisionErr) {
				kernel.WriteJSON(w, http.StatusConflict, map[string]string{"message": revisionErr.Error()})
				return
			}
			kernel.WriteBadRequest(w, pipelineErrorMessage(err, "绑定账户分组失败"))
			return
		}
		if result == nil {
			kernel.WriteBadRequest(w, "账户不存在、授权已失效或分组不可用")
			return
		}
		if d.Sink != nil {
			var groupChange *PatchChange
			for index := range result.Changes {
				if result.Changes[index].Field == "groupId" {
					groupChange = &result.Changes[index]
					break
				}
			}
			if groupChange != nil {
				d.Sink.Record(authsys.OperationLogEntry{
					ActorSystemAccountID:          auth.SystemAccountID,
					ActorUsername:                 auth.Username,
					ActorDisplayName:              auth.DisplayName,
					ActorRole:                     auth.Role,
					OperationScopeSystemAccountID: result.OwnerSystemAccountID,
					Mode:                          operationMode(access),
					Module:                        "accounts",
					Action:                        "bind_group",
					OperationKey:                  "accounts.bind_group",
					ResourceType:                  "account",
					ResourceID:                    result.ID,
					ResourceName:                  result.Name,
					Summary:                       "绑定账户分组：" + result.Name,
					Changes: []authsys.OperationLogChange{
						safeChange("groupId", "绑定分组", groupChange.Before, groupChange.After),
					},
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
}

// writeM11ReadError renders the unexpected read error as the Node 500 shape.
func (d *Deps) writeM11ReadError(w http.ResponseWriter, err error) {
	println("accounts m11 slice internal error: " + err.Error())
	kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
}
