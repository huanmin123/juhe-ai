package accounts

import (
	"errors"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// Runtime-reset route (维护者 6f9739e96): POST /accounts/{id}/runtime-reset +
// the my-accounts mirror (Node registerAccountDetailRoutes). The mutation
// guard mirrors succeededTtlMs: 0 / failedTtlMs: 0 — runtime cleanup is
// explicitly retryable, so a 200 never retains a dedup entry.

// mountRuntimeResetRoutes registers the runtime-reset family on both surfaces.
func (d *Deps) mountRuntimeResetRoutes(k *kernel.Kernel, prefix string) {
	k.Register("POST "+prefix+"/accounts/{id}/runtime-reset", d.runtimeResetHandler(false))
	k.Register("POST "+prefix+"/my-accounts/{id}/runtime-reset", d.runtimeResetHandler(true))
}

func (d *Deps) runtimeResetHandler(selfOnly bool) http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "accounts.runtime_reset",
		// Runtime cleanup is explicitly retryable: a 200 response may still
		// carry per-store failures, so do not retain a dedup entry (Node
		// succeededTtlMs/failedTtlMs 0). DedupNoRetention deletes the entry
		// on completion; the kernel's 0 means "default TTL".
		SucceededTTL: kernel.DedupNoRetention,
		FailedTTL:    kernel.DedupNoRetention,
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
		d.runRuntimeReset(w, r)
	}))
	if selfOnly {
		return d.Auth.RequireSession(true)(handler)
	}
	return d.Auth.RequireAdmin(handler)
}

func (d *Deps) runRuntimeReset(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	// accountRuntimeResetSchema.strict(): { expectedConfigRevision } only.
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	for key := range body {
		switch key {
		case "expectedConfigRevision":
		default:
			kernel.WriteBadRequest(w, "清理运行状态参数无效")
			return
		}
	}
	revision, ok := body["expectedConfigRevision"].(float64)
	if !ok || revision != float64(int64(revision)) || revision < 1 {
		kernel.WriteBadRequest(w, "清理运行状态参数无效")
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
	access := requestScope(r)
	outcome, err := d.Store.ResetAccountRuntimeState(r.Context(), r.PathValue("id"), int64(revision), access)
	if err != nil {
		d.writeRuntimeResetError(w, err)
		return
	}
	if outcome == nil {
		kernel.WriteError(w, http.StatusNotFound, "账户不存在")
		return
	}
	if d.Sink != nil {
		log := outcome.Log
		changes := make([]authsys.OperationLogChange, 0, len(log.Changes))
		for _, change := range log.Changes {
			changes = append(changes, safeChange(change.Field, "运行状态", change.Before, change.After))
		}
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID:          auth.SystemAccountID,
			ActorUsername:                 auth.Username,
			ActorDisplayName:              auth.DisplayName,
			ActorRole:                     auth.Role,
			OperationScopeSystemAccountID: log.OperationScopeSystemAccountID,
			Mode:                          log.Mode,
			Module:                        log.Module,
			Action:                        log.Action,
			OperationKey:                  log.OperationKey,
			ResourceType:                  log.ResourceType,
			ResourceID:                    log.ResourceID,
			ResourceName:                  log.ResourceName,
			Summary:                       log.Summary,
			Changes:                       changes,
			Viewers: []authsys.OperationLogViewer{
				{SystemAccountID: log.ViewerSystemAccountID, Reason: "resource_owner"},
			},
		}, r)
	}
	kernel.WriteOK(w, outcome.Result, "")
}

// writeRuntimeResetError mirrors the route error mapping: revision conflicts
// render 409 with the refresh copy, 账户不存在 renders 404, every other error
// renders 400 with its message (the Node catch chain).
func (d *Deps) writeRuntimeResetError(w http.ResponseWriter, err error) {
	var revision *RevisionConflictError
	if errors.As(err, &revision) {
		kernel.WriteJSON(w, http.StatusConflict, map[string]string{"message": RevisionConflictMessage})
		return
	}
	message := strings.TrimSpace(err.Error())
	if message == "账户不存在" {
		kernel.WriteError(w, http.StatusNotFound, message)
		return
	}
	if message == "" {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteBadRequest(w, message)
}
