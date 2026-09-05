package main

// G20 phase-3 openai-compatible files / vector-stores mount: the two route
// families Node mounts ahead of openAIGatewayRouter in the gateway middleware
// chain (server.ts: openAICompatibleFilesRouter + openAICompatibleVectorStoresRouter,
// preResolveGatewayRuntime -> files -> vector-stores). The composition builds
// the openaicompat Store over the business database and hands the chain a
// non-protocol dispatcher so /v1/files, /v1/containers and /v1/vector_stores
// no longer fall through to the legacy bridge; every other non-protocol /v1
// path keeps the Node 404 JSON contract (rejectUnrecognizedGatewayProtocolRequest).

import (
	"context"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/openaicompat"
)

// mountChainOpenAICompatFamilies builds the openai-compatible families over
// the business database and attaches them to the chain's non-protocol path
// dispatcher. It fails fast when the runtime cache is missing (the scope
// resolver needs the gateway runtime read).
func mountChainOpenAICompatFamilies(composed *composition, chain *gatewayChain, cfg runtimeConfig, services *chainRuntimeServices) error {
	if composed == nil || composed.db == nil {
		return errChainCompat("openai-compatible 组合缺少业务数据库句柄")
	}
	if services == nil || services.Cache == nil {
		return errChainCompat("openai-compatible 组合缺少网关链 runtime cache")
	}
	store, err := openaicompat.NewStore(composed.db, composed.pgDialect)
	if err != nil {
		return err
	}
	deps := &openaicompat.Deps{
		Store: store,
		Config: openaicompat.Config{
			FilesRoot: cfg.OpenAICompatibleFilesRoot,
		},
		Scope: chainCompatScopeResolver(services.Cache),
	}
	compat := &chainCompatMux{mux: http.NewServeMux()}
	deps.Mount(compat)
	chain.compat = &chainCompatDispatcher{mux: compat}
	return nil
}

// chainCompatMux adapts http.ServeMux onto the openaicompat mount interface
// (the kernel's Register(pattern, handler) shape).
type chainCompatMux struct {
	mux *http.ServeMux
}

func (m *chainCompatMux) Register(pattern string, handler http.Handler) {
	m.mux.Handle(pattern, handler)
}

func errChainCompat(message string) error {
	return &chainCompatError{message: message}
}

type chainCompatError struct {
	message string
}

func (e *chainCompatError) Error() string { return e.message }

// chainCompatScopeResolver mirrors preResolveGatewayRuntime: resolve the raw
// bearer key over the runtime cache gateway runtime read; nil renders the
// openaicompat 401 contract.
func chainCompatScopeResolver(cache *gatewayruntimecache.Service) openaicompat.ScopeResolver {
	return func(r *http.Request) *openaicompat.GatewayScope {
		if cache == nil || r == nil {
			return nil
		}
		key := strings.TrimSpace(bearerTokenOf(r))
		if key == "" {
			return nil
		}
		runtime, err := cache.ReadCachedGatewayRuntimeAsync(context.Background(), key)
		if err != nil || runtime.APIKey == nil {
			return nil
		}
		return &openaicompat.GatewayScope{
			SystemAccountID: runtime.APIKey.SystemAccountID,
			APIKeyID:        runtime.APIKey.ID,
		}
	}
}

// bearerTokenOf mirrors the Authorization bearer extraction.
func bearerTokenOf(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("authorization"))
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// chainCompatDispatcher answers the openai-compatible families inside the
// chain: matched routes render through the mux; everything else keeps the
// Node 404 JSON contract (the express routers fall through to
// rejectUnrecognizedGatewayProtocolRequest).
type chainCompatDispatcher struct {
	mux *chainCompatMux
}

var chainCompatFamilies = []string{
	"/v1/files",
	"/v1/containers",
	"/v1/vector_stores",
}

// matches reports whether the request path belongs to a mounted family
// prefix (the express routers only ever match their own subtree).
func (d *chainCompatDispatcher) matches(path string) bool {
	for _, family := range chainCompatFamilies {
		if path == family || strings.HasPrefix(path, family+"/") {
			return true
		}
	}
	return false
}

func (d *chainCompatDispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if d == nil || d.mux == nil || !d.matches(r.URL.Path) {
		writeChainNotFound(w)
		return
	}
	d.mux.mux.ServeHTTP(w, r)
}

// writeChainNotFound mirrors the Node 404 JSON contract.
func writeChainNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"message":"资源不存在"}`))
}
