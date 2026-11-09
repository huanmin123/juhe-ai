package main

// 2026-11-09 用户裁决：网关只做调度与协议转发，不做 RAG / 文件存储 /
// 向量库。原 openai-compatible files / containers / vector_stores 本地运行时
// 不再装配（chain.compat 保持 nil，非协议 /v1 路径回落 404 JSON）；业务侧
// 自行管理文件与向量库。

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-platform/openaicompatcore"
)

// chainCompatMux adapts http.ServeMux onto the openaicompat mount interface
// (the kernel's Register(pattern, handler) shape).
type chainCompatMux struct {
	mux *http.ServeMux
}

func (m *chainCompatMux) Register(pattern string, handler http.Handler) {
	m.mux.Handle(pattern, handler)
}

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
			// 时间计划闸门（时间计划闸门与账户状态解耦设计 §5.2）：Key 有效但
			// 不在可用时段，渲染可区分的 401，不按基础设施故障处理。
			if errors.Is(infraErr, gatewayruntimecache.ErrAPIKeyOutsideSchedule) {
				openaicompatcore.WriteGatewayErrorPayload(w, http.StatusUnauthorized,
					"API Key 当前不在可用时段", "invalid_request_error", "api_key_outside_schedule")
				return
			}
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
