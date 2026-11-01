package main

// G20 phase-3 openai-compatible files / vector-stores mount: the two route
// families Node mounts ahead of openAIGatewayRouter in the gateway middleware
// chain (server.ts: openAICompatibleFilesRouter + openAICompatibleVectorStoresRouter,
// preResolveGatewayRuntime -> files -> vector-stores). The composition builds
// the openaicompat Store over the business database and hands the chain a
// non-protocol dispatcher covering /v1/files, /v1/containers and
// /v1/vector_stores; every other non-protocol /v1
// path keeps the Node 404 JSON contract (rejectUnrecognizedGatewayProtocolRequest).

import (
	"context"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/openaicompat/openaicompatstorage"
	"github.com/huanminabc/juhe-ai/backend-go-platform/openaicompatcore"
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
	store, err := openaicompatstorage.NewStore(composed.db)
	if err != nil {
		return err
	}
	deps := &openaicompatstorage.Deps{
		Store: store,
		Config: openaicompatcore.Config{
			FilesRoot: cfg.OpenAICompatibleFilesRoot,
		},
		Scope: chainCompatScopeResolver(services.Cache),
	}
	compat := &chainCompatMux{mux: http.NewServeMux()}
	deps.Mount(compat)
	chain.compat = &chainCompatDispatcher{mux: compat, cache: services.Cache, chain: chain}
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

// chainCompatScopeContextKey 承载 dispatcher 预解析的 scope（含 nil =
// key 缺失/无效），使 resolver 与 dispatcher 共享同一次运行时读，请求内不
// 重复走 syncInvalidationsForRuntime + 运行时读。
type chainCompatScopeContextKey struct{}

// readChainCompatScope 是 scope 的唯一读取实现：(nil, nil) = key 缺失/无效
// （沿用 openaicompatcore 的 401 契约），err 仅承载运行时缓存基础设施故障
// （DD3-D1：Redis/DB 故障窗内不再与 key 无效同渲染 401，避免
// DD2-C1 补计把故障窗内同 IP 的合法请求计入认证前熔断）。
func readChainCompatScope(cache *gatewayruntimecache.Service, r *http.Request) (*openaicompatcore.GatewayScope, error) {
	if cache == nil || r == nil {
		return nil, nil
	}
	key := strings.TrimSpace(bearerTokenOf(r))
	if key == "" {
		return nil, nil
	}
	runtime, err := cache.ReadCachedGatewayRuntimeAsync(context.Background(), key)
	if err != nil {
		return nil, err
	}
	if runtime.APIKey == nil {
		return nil, nil
	}
	return &openaicompatcore.GatewayScope{
		SystemAccountID: runtime.APIKey.SystemAccountID,
		APIKeyID:        runtime.APIKey.ID,
	}, nil
}

// chainCompatScopeResolver mirrors preResolveGatewayRuntime: resolve the raw
// bearer key over the runtime cache gateway runtime read; nil renders the
// openaicompat 401 contract. 优先消费 dispatcher 的请求内预解析结果（含 nil
// scope），无预解析（cache/chain 未装配的测试构造）时兜底直读。
func chainCompatScopeResolver(cache *gatewayruntimecache.Service) openaicompatcore.ScopeResolver {
	return func(r *http.Request) *openaicompatcore.GatewayScope {
		if r == nil {
			return nil
		}
		if scope, ok := r.Context().Value(chainCompatScopeContextKey{}).(*openaicompatcore.GatewayScope); ok {
			return scope
		}
		scope, _ := readChainCompatScope(cache, r)
		return scope
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
// rejectUnrecognizedGatewayProtocolRequest). cache 与 chain 一起构成
// DD3-D1 的 scope 预解析探针：运行时缓存基础设施 err 在进入 mux 前按主链
// 口径渲染并短路，key 无效/缺失的 (nil, nil) 预解析结果随请求上下文下发给
// resolver（同一次读）；两者任一缺失（组合测试构造）保持旧行为——不做预
// 解析，resolver 兜底直读。
type chainCompatDispatcher struct {
	mux   *chainCompatMux
	cache *gatewayruntimecache.Service
	chain *gatewayChain
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

// ServeHTTP answers matched compat routes through the mux; a family-prefix
// path without a registered route keeps the Node 404 JSON contract. Without
// this probe the net/http mux answers unknown sub-paths with its text/plain
// `404 page not found` body (and unmatched methods with text/plain 405),
// while the Node express routers fall through to
// rejectUnrecognizedGatewayProtocolRequest's 404 JSON.
func (d *chainCompatDispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if d == nil || d.mux == nil || !d.matches(r.URL.Path) {
		writeChainNotFound(w)
		return
	}
	// Handler returns an empty pattern when nothing matches (unknown
	// sub-path or method-only mismatch); those all render the 404 JSON.
	if _, pattern := d.mux.mux.Handler(r); pattern == "" {
		writeChainNotFound(w)
		return
	}
	if d.cache != nil && d.chain != nil {
		scope, infraErr := readChainCompatScope(d.cache, r)
		if infraErr != nil {
			// DD3-D1：运行时缓存基础设施故障按主链同窗同错口径渲染
			// （handleOrchestratorError 的 503/500，同 preauth.go
			// ResolveGatewayRuntimeAsync 的 err 上抛臂——主链该臂不计数），
			// 不落 401、不计入认证前熔断；mux 不再执行，requireScope 的
			// 401 出口与 DD2-C1 的 401 补计都不会发生。
			d.renderScopeInfraError(w, r, infraErr)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), chainCompatScopeContextKey{}, scope))
	}
	d.mux.mux.ServeHTTP(w, r)
}

// renderScopeInfraError 把 scope 预解析的基础设施故障交给主链同款错误渲染
// （handleOrchestratorError：db 不可用 503 / 其余 500 + 根因日志与
// failureReason）。仅在 dispatcher 持有 chain 时可达（ServeHTTP 的预解析
// 守卫保证）。
func (d *chainCompatDispatcher) renderScopeInfraError(w http.ResponseWriter, r *http.Request, err error) {
	req := gatewaypreauth.NewGatewayRequest(r)
	res, ok := w.(gatewaypreauth.GatewayResponseWriter)
	if !ok {
		res = gatewaypreauth.NewTrackingWriter(w)
	}
	d.chain.handleOrchestratorError(err, req, res, nowMillis(), gatewaypreauth.RequestEndpoint(req))
}

// writeChainNotFound mirrors the Node 404 JSON contract.
func writeChainNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"message":"资源不存在"}`))
}
