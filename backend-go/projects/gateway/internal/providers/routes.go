package providers

import (
	"net/http"
	"strconv"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// Deps bundles the M11 slice collaborators.
type Deps struct {
	Store *Store
	Auth  *authsys.Deps
}

// deferredModelCatalogMessage is the verbatim deferral copy for the write
// subpaths that depend on the C03 model pricing/catalog service.
const deferredModelCatalogMessage = "模型目录服务待迁移"

// Mount wires the providers route family: admin surface on /providers and
// the force-self mirror on /my-providers (groups pattern). Providers are
// global catalog rows, so the self surface serves the same reads pinned to
// the caller identity. The Node write endpoints that depend on the C03 model
// catalog pricing service (POST/PATCH/DELETE /:code/models,
// PUT /:code/default-health-check-model) are mounted on both surfaces and
// render the documented 400 deferral until that slice migrates.
func (d *Deps) Mount(k *kernel.Kernel) {
	prefix := "/__aisys__/api"
	admin := d.Auth.RequireAdmin
	self := d.Auth.RequireSession(true)

	// Admin surface.
	k.Register("GET "+prefix+"/providers", admin(http.HandlerFunc(d.list)))
	k.Register("GET "+prefix+"/providers/{id}", admin(http.HandlerFunc(d.find)))

	// Self surface (forceSelfAccessScope mirror).
	k.Register("GET "+prefix+"/my-providers", self(http.HandlerFunc(d.list)))
	k.Register("GET "+prefix+"/my-providers/{id}", self(http.HandlerFunc(d.find)))

	// C03-deferred write subpaths (Node providers.routes.ts write family).
	for _, surface := range []struct {
		base string
		wrap func(http.Handler) http.Handler
	}{
		{"/providers", admin},
		{"/my-providers", self},
	} {
		base := prefix + surface.base
		d.mountDeferredWrite(k, "POST "+base+"/{id}/models", surface.wrap)
		d.mountDeferredWrite(k, "PATCH "+base+"/{id}/models/{modelId}", surface.wrap)
		d.mountDeferredWrite(k, "DELETE "+base+"/{id}/models/{modelId}", surface.wrap)
		d.mountDeferredWrite(k, "PUT "+base+"/{id}/default-health-check-model", surface.wrap)
	}
}

// mountDeferredWrite registers one write endpoint that stays behind the C03
// model catalog migration: authenticated clients receive the 400 deferral
// instead of a silent 404.
func (d *Deps) mountDeferredWrite(k *kernel.Kernel, pattern string, wrap func(http.Handler) http.Handler) {
	k.Register(pattern, wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kernel.WriteBadRequest(w, deferredModelCatalogMessage)
	})))
}

func parseIntOr(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return fallback
	}
	return value
}

func (d *Deps) list(w http.ResponseWriter, r *http.Request) {
	page := parseIntOr(r.URL.Query().Get("page"), 1)
	pageSize := parseIntOr(r.URL.Query().Get("pageSize"), defaultProviderListPage)
	result, err := d.Store.ListPage(r.Context(), page, pageSize, r.URL.Query().Get("keyword"))
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	setNoStoreHeaders(w)
	kernel.WriteOK(w, result, "")
}

func (d *Deps) find(w http.ResponseWriter, r *http.Request) {
	detail, err := d.Store.FindDetail(r.Context(), r.PathValue("id"))
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if detail == nil {
		kernel.WriteNotFound(w, "供应商不存在")
		return
	}
	setNoStoreHeaders(w)
	kernel.WriteOK(w, detail, "")
}

func setNoStoreHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}
