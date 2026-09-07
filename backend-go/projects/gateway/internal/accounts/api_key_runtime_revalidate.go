package accounts

// POST /{id}/api-key-runtime/revalidate (BUG-0162 revalidate slice; Node
// account-detail.routes.ts:156-233). The mutation-guarded API Key pool
// revalidation rides the same account reader as GET /api-key-runtime, enforces
// the config-revision fence at the route layer (409 with the refresh copy),
// hands over to revalidateAccountApiKeyRuntimePoolAsync — the Go port is
// RuntimeResetEffects.RevalidateAccountAPIKeyRuntimePool (composed onto
// accountkeystates.Store.RevalidatePool in compose_accounts_reset.go) — and
// renders ineligible outcomes as 409
// ACCOUNT_API_KEY_RUNTIME_REVALIDATE_NOT_EXECUTABLE with the reason copy.
// A success records the accounts.api_key_runtime_revalidate operation log and
// answers { id, configRevision, changed } (frontend
// accountsApi/myAccountsApi.revalidateApiKeyRuntime contract).

import (
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// revalidateIneligibleMessage mirrors revalidateIneligibleMessage
// (account-detail.routes.ts:269-275): the fallback arm renders
// not_supported / any unknown reason.
func revalidateIneligibleMessage(reason string) string {
	switch reason {
	case "account_not_active":
		return "账户当前未启用，不能重新验证 Key 池"
	case "account_unschedulable":
		return "账户当前不可调度，不能重新验证 Key 池"
	case "config_revision_conflict":
		return "账户配置已被其他操作更新，请刷新后重试"
	case "account_not_found":
		return "账户不存在或已删除"
	case "no_revalidatable_key":
		return "当前账户没有可重新验证的不可用 Key"
	default:
		return "当前账户不是启用中的多 Key API Key 池"
	}
}

// mountAPIKeyRuntimeRevalidateRoutes registers the revalidate route on both
// surfaces (the Node shared router mounts at /accounts and /my-accounts).
func (d *Deps) mountAPIKeyRuntimeRevalidateRoutes(k *kernel.Kernel, prefix string) {
	k.Register("POST "+prefix+"/accounts/{id}/api-key-runtime/revalidate", d.apiKeyRuntimeRevalidateRoute(false))
	k.Register("POST "+prefix+"/my-accounts/{id}/api-key-runtime/revalidate", d.apiKeyRuntimeRevalidateRoute(true))
}

// apiKeyRuntimeRevalidateRoute wraps the handler with the mutation guard and
// the surface auth (Node mutationGuard + the shared-router surface pair). The
// guard keeps the kernel default succeeded/failed TTLs exactly like the Node
// mutationGuard call, which passes no TTL overrides.
func (d *Deps) apiKeyRuntimeRevalidateRoute(selfOnly bool) http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "accounts.api_key_runtime_revalidate",
		Actor:        actorResolver,
		Scope: func(r *http.Request) (any, error) {
			return strings.TrimSpace(r.URL.Query().Get("systemAccountId")), nil
		},
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{
				"accountId":              strings.TrimSpace(r.PathValue("id")),
				"expectedConfigRevision": kernel.BodyField(r, "expectedConfigRevision"),
			}, nil
		},
	})
	handler := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.runAPIKeyRuntimeRevalidate(w, r, selfOnly)
	}))
	if selfOnly {
		return d.Auth.RequireSession(true)(handler)
	}
	return d.Auth.RequireAdmin(handler)
}

func (d *Deps) runAPIKeyRuntimeRevalidate(w http.ResponseWriter, r *http.Request, selfOnly bool) {
	setNoStoreHeaders(w)
	// accountApiKeyRuntimeRevalidateSchema.strict(): { expectedConfigRevision }
	// only, integer >= 1.
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	for key := range body {
		switch key {
		case "expectedConfigRevision":
		default:
			kernel.WriteBadRequest(w, "重新验证 API Key 池参数无效")
			return
		}
	}
	revision, ok := body["expectedConfigRevision"].(float64)
	if !ok || revision != float64(int64(revision)) || revision < 1 {
		kernel.WriteBadRequest(w, "重新验证 API Key 池参数无效")
		return
	}
	if !scopeQueryOK(r) {
		kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
		return
	}
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	access := requestScopeFor(r, selfOnly)
	// loadAccountForApiKeyRuntime → findAccountApiKeyRuntimeAccountAsync:
	// 404 for missing/out-of-scope rows, 403 for authorized instances.
	account, err := d.Store.FindAPIKeyRuntimeAccount(r.Context(), r.PathValue("id"), access)
	if err != nil {
		d.writeM11ReadError(w, err)
		return
	}
	if account == nil {
		kernel.WriteError(w, http.StatusNotFound, "账户不存在")
		return
	}
	if account.AccessType == "authorized" {
		kernel.WriteError(w, http.StatusForbidden, "授权实例不能重新验证来源账户 API Key 池")
		return
	}
	if account.ConfigRevision != int64(revision) {
		kernel.WriteJSON(w, http.StatusConflict, map[string]string{"message": RevisionConflictMessage})
		return
	}
	effects := d.Store.runtimeResetEffectsOrNil()
	if effects == nil {
		println("accounts slice runtime-reset effects port not wired for revalidate")
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	revalidated, err := effects.RevalidateAccountAPIKeyRuntimePool(r.Context(), account.ID, int64(revision))
	if err != nil {
		d.writeM11ReadError(w, err)
		return
	}
	if !revalidated.Eligible {
		reason := revalidated.Reason
		if reason == "" {
			reason = "not_supported"
		}
		kernel.WriteJSON(w, http.StatusConflict, map[string]any{
			"message": revalidateIneligibleMessage(reason),
			"code":    "ACCOUNT_API_KEY_RUNTIME_REVALIDATE_NOT_EXECUTABLE",
			"reason":  reason,
		})
		return
	}
	// runLoggedOperationAsync: the operation log lands before the response;
	// changes carry the dueProbeKeys 0 → changed diff.
	if d.Sink != nil {
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID:          auth.SystemAccountID,
			ActorUsername:                 auth.Username,
			ActorDisplayName:              auth.DisplayName,
			ActorRole:                     auth.Role,
			OperationScopeSystemAccountID: account.OwnerSystemAccountID,
			Mode:                          operationMode(access),
			Module:                        "accounts",
			Action:                        "api_key_runtime_revalidate",
			OperationKey:                  "accounts.api_key_runtime_revalidate",
			ResourceType:                  "account",
			ResourceID:                    account.ID,
			Summary:                       "重新验证账户 API Key 池：" + account.ID,
			Changes: []authsys.OperationLogChange{
				safeChange("dueProbeKeys", "标记待探测 Key 数量", 0, revalidated.Changed),
			},
			Viewers: []authsys.OperationLogViewer{
				{SystemAccountID: account.OwnerSystemAccountID, Reason: "resource_owner"},
			},
		}, r)
	}
	kernel.WriteOK(w, map[string]any{
		"id":             account.ID,
		"configRevision": account.ConfigRevision,
		"changed":        revalidated.Changed,
	}, "")
}
