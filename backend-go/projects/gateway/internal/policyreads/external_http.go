// external_http.go owns the /external-integration-sources route family:
// dependency bundle, route mounting and the read-only handlers.
package policyreads

import (
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// ---------------------------------------------------------------------------
// M16b route family (mounted behind requireAdmin).
// ---------------------------------------------------------------------------

// ExternalDeps bundles the M16b collaborators.
type ExternalDeps struct {
	Store *ExternalStore
	Auth  *authsys.Deps
	Sink  authsys.OperationLogSink
}

// Mount wires the external-integration-sources route family.
func (d *ExternalDeps) Mount(k *kernel.Kernel) {
	k.Register("GET "+externalPrefix+"/scopes", d.Auth.RequireAdmin(http.HandlerFunc(d.scopes)))
	k.Register("GET "+externalPrefix+"/api-docs", d.Auth.RequireAdmin(http.HandlerFunc(d.apiDocs)))
	k.Register("GET "+externalPrefix, d.Auth.RequireAdmin(http.HandlerFunc(d.list)))
	k.Register("POST "+externalPrefix+"/built-in-test-token/reset", d.Auth.RequireAdmin(d.guardedResetBuiltInTestToken()))
	k.Register("POST "+externalPrefix, d.Auth.RequireAdmin(d.guardedCreate()))
	k.Register("GET "+externalPrefix+"/{id}", d.Auth.RequireAdmin(http.HandlerFunc(d.detail)))
	k.Register("PATCH "+externalPrefix+"/{id}", d.Auth.RequireAdmin(d.guardedPatchSource()))
	k.Register("DELETE "+externalPrefix+"/{id}", d.Auth.RequireAdmin(d.guardedDeleteSource()))
	k.Register("POST "+externalPrefix+"/{id}/tokens", d.Auth.RequireAdmin(d.guardedCreateToken()))
	k.Register("GET "+externalPrefix+"/{id}/tokens/{tokenId}/secret", d.Auth.RequireAdmin(http.HandlerFunc(d.tokenSecret)))
	k.Register("PATCH "+externalPrefix+"/{id}/tokens/{tokenId}", d.Auth.RequireAdmin(d.guardedPatchToken()))
}

func (d *ExternalDeps) scopes(w http.ResponseWriter, _ *http.Request) {
	kernel.WriteOK(w, d.Store.Scopes(), "")
}

func (d *ExternalDeps) apiDocs(w http.ResponseWriter, _ *http.Request) {
	kernel.WriteOK(w, externalPublicAPICatalog(), "")
}

func (d *ExternalDeps) list(w http.ResponseWriter, r *http.Request) {
	page, pageSize, keyword, status, message := parseExternalListQuery(r.URL.Query())
	if message != "" {
		kernel.WriteBadRequest(w, message)
		return
	}
	result, err := d.Store.ListPage(r.Context(), page, pageSize, keyword, status)
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "服务器内部错误", err)
		return
	}
	kernel.WriteOK(w, result, "")
}

func (d *ExternalDeps) detail(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		kernel.WriteBadRequest(w, "来源系统不存在")
		return
	}
	source, err := d.Store.FindSource(r.Context(), id)
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "服务器内部错误", err)
		return
	}
	if source == nil {
		kernel.WriteError(w, http.StatusNotFound, "来源系统不存在")
		return
	}
	kernel.WriteOK(w, source, "")
}

func (d *ExternalDeps) tokenSecret(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	tokenID := strings.TrimSpace(r.PathValue("tokenId"))
	if id == "" {
		kernel.WriteBadRequest(w, "来源系统不存在")
		return
	}
	if tokenID == "" {
		kernel.WriteBadRequest(w, "Token 不存在")
		return
	}
	token, err := d.Store.FindTokenSecret(r.Context(), id, tokenID)
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "服务器内部错误", err)
		return
	}
	if token == nil {
		kernel.WriteError(w, http.StatusNotFound, "Token 不存在")
		return
	}
	setNoStoreHeaders(w)
	kernel.WriteOK(w, map[string]any{"token": *token}, "")
}

// setSecretNoStoreHeaders mirrors setSecretResponseHeaders for token-bearing
// responses.
func setNoStoreHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}
