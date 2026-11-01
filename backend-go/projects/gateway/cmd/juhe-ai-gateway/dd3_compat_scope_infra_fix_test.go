package main

// 深挖复审 R3 回归修复批钉住测试（DD3-D1 / DD3-D3）：
//
// DD3-D1：compat scope 解析区分运行时缓存基础设施 err 与 key 无效——基础设施
// err 走主链同款错误渲染（handleOrchestratorError：db 不可用 503 / 其余 500，
// 同 preauth.go ResolveGatewayRuntimeAsync 的 err 上抛臂——主链该臂不计数），
// 仅「key 确实无效/缺失」才落 401 并计入认证前熔断（DD2-C1）。修复前两者同
// 渲染 401，Redis/DB 故障窗内同 IP 的合法 compat 请求会被 401 补计推进熔断，
// 故障恢复后被误封。
//
// DD3-D3：compat 入口守卫与 realtime token 面的 429 熔断响应补
// Connection: close（对齐主链 prepareEarlyAuthFailureResponse +
// sendPreAuthCircuitResponse 的响应契约）。

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-platform/openaicompatcore"
)

// ---------------------------------------------------------------------------
// DD3-D1 fixtures
// ---------------------------------------------------------------------------

// ddD1ScopeReadModels 是 scope 解析面的 ReadModels 替身：ReadGatewayRuntime
// 可编程返回基础设施 err / 带 APIKey 的运行时 / 空 runtime（key 无效），并
// 记录装载调用次数。
type ddD1ScopeReadModels struct {
	w1hReadModels

	mu         sync.Mutex
	runtime    *gatewayruntimecache.GatewayRuntime
	runtimeErr error
	calls      int
}

func (m *ddD1ScopeReadModels) ReadGatewayRuntime(context.Context, string) (gatewayruntimecache.GatewayRuntime, error) {
	m.mu.Lock()
	m.calls++
	runtime, runtimeErr := m.runtime, m.runtimeErr
	m.mu.Unlock()
	if runtimeErr != nil {
		return gatewayruntimecache.GatewayRuntime{}, runtimeErr
	}
	if runtime != nil {
		return *runtime, nil
	}
	return gatewayruntimecache.GatewayRuntime{}, nil
}

func (m *ddD1ScopeReadModels) runtimeCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// ddD1CompatChain 按生产装配形态构造 compat 链：dispatcher 持有 cache 与
// chain（DD3-D1 预解析探针激活），mux 出口复刻 openaicompatstorage 的
// requireScope 契约（resolver nil → invalid_api_key 401）。
func ddD1CompatChain(t *testing.T, models gatewayruntimecache.ReadModels, circuits gatewaypreauth.PreAuthCircuits) *gatewayChain {
	t.Helper()
	inner := newSlogObservability(slog.New(slog.NewTextHandler(io.Discard, nil)), gatewaypreauth.SystemClock{})
	service := &gatewaypreauth.Service{
		Responses:     &recordingFailureSink{},
		Observability: inner,
		Clock:         gatewaypreauth.SystemClock{},
		Circuits:      circuits,
		IPPolicy:      traceTestNoopIPPolicy{},
	}
	cache, err := gatewayruntimecache.New(models, gatewayruntimecache.Options{})
	if err != nil {
		t.Fatalf("gatewayruntimecache.New() error = %v", err)
	}
	t.Cleanup(cache.Close)
	mux := &chainCompatMux{mux: http.NewServeMux()}
	resolver := chainCompatScopeResolver(cache)
	mux.Register("/v1/files", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if resolver(r) == nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"缺少或无效的 API Key","error":{"code":"invalid_api_key"}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	dispatcher := &chainCompatDispatcher{mux: mux, cache: cache}
	chain := &gatewayChain{
		preauth:       service,
		observability: inner,
		clock:         gatewaypreauth.SystemClock{},
		compat:        dispatcher,
	}
	dispatcher.chain = chain
	return chain
}

// TestDD3D1CompatScopeInfraErrorRenders503WithoutCounting 钉住：运行时缓存
// 基础设施 err（db 不可用文案）→ 主链同款 503 渲染，不落 401 契约、不计数。
func TestDD3D1CompatScopeInfraErrorRenders503WithoutCounting(t *testing.T) {
	models := &ddD1ScopeReadModels{}
	models.runtimeErr = errors.New("本地数据库服务暂时不可用：dd3-d1 模拟故障窗口")
	circuits := &ddC1RecordingCircuits{}
	chain := ddD1CompatChain(t, models, circuits)
	recorder := httptest.NewRecorder()
	chain.ServeHTTP(recorder, withBearer("any-key"))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("状态码 = %d, want 503（基础设施 err 走主链 handleOrchestratorError 渲染）, body=%s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); strings.Contains(body, "invalid_api_key") {
		t.Fatalf("基础设施故障不得渲染 401 契约: %s", body)
	}
	if len(circuits.records) != 0 {
		t.Fatalf("基础设施故障不得计入认证前熔断，records = %d", len(circuits.records))
	}
}

// TestDD3D1CompatScopeInfraErrorRenders500WithoutCounting 钉住：非 db 文案的
// 基础设施 err → 主链同款 500 渲染（Express error fallback 契约），不落
// 401、不计数。
func TestDD3D1CompatScopeInfraErrorRenders500WithoutCounting(t *testing.T) {
	models := &ddD1ScopeReadModels{}
	models.runtimeErr = errors.New("dd3-d1 通用基础设施故障")
	circuits := &ddC1RecordingCircuits{}
	chain := ddD1CompatChain(t, models, circuits)
	recorder := httptest.NewRecorder()
	chain.ServeHTTP(recorder, withBearer("any-key"))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d, want 500, body=%s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); strings.Contains(body, "invalid_api_key") {
		t.Fatalf("基础设施故障不得渲染 401 契约: %s", body)
	}
	if len(circuits.records) != 0 {
		t.Fatalf("基础设施故障不得计入认证前熔断，records = %d", len(circuits.records))
	}
}

// TestDD3D1CompatInvalidKeyStill401AndCounts 钉住：key 确实无效（读成功但
// APIKey 为 nil）仍落 openaicompat 401 契约并计入认证前熔断（DD2-C1 保持）。
func TestDD3D1CompatInvalidKeyStill401AndCounts(t *testing.T) {
	models := &ddD1ScopeReadModels{}
	circuits := &ddC1RecordingCircuits{}
	chain := ddD1CompatChain(t, models, circuits)
	recorder := httptest.NewRecorder()
	chain.ServeHTTP(recorder, withBearer("bad-key"))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d, want 401（key 无效保持 openaicompat 契约）, body=%s", recorder.Code, recorder.Body.String())
	}
	if len(circuits.records) != 1 {
		t.Fatalf("key 无效必须计入认证前熔断，records = %d", len(circuits.records))
	}
	if circuits.records[0].Reason != gatewaypreauth.PreAuthFailureInvalidAPIKey {
		t.Fatalf("reason = %q, want invalid_api_key", circuits.records[0].Reason)
	}
}

// TestDD3D1CompatValidKeyResolvesAndPasses 钉住：有效 key 的 scope 经预解析
// + 请求内 memo 下发，请求照常 200、零计数。
func TestDD3D1CompatValidKeyResolvesAndPasses(t *testing.T) {
	models := &ddD1ScopeReadModels{}
	models.runtime = &gatewayruntimecache.GatewayRuntime{
		APIKey: &gatewayruntimecache.GatewayAPIKeyRow{
			ID:                "key-ddd1",
			SystemAccountID:   "sys_owner",
			RouteStrategyID:   "rs-ddd1",
			RouteStrategyMode: "normal",
			Status:            "active",
		},
		Settings: gatewayruntimecache.GatewaySettings{},
		Accounts: []gatewayruntimecache.OpenAIAccountSecret{},
	}
	circuits := &ddC1RecordingCircuits{}
	chain := ddD1CompatChain(t, models, circuits)
	recorder := httptest.NewRecorder()
	chain.ServeHTTP(recorder, withBearer("good-key"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200（有效 key scope 解析不受影响）, body=%s", recorder.Code, recorder.Body.String())
	}
	if len(circuits.records) != 0 {
		t.Fatalf("正常流量不得计数，records = %d", len(circuits.records))
	}
	if calls := models.runtimeCallCount(); calls != 1 {
		t.Fatalf("ReadGatewayRuntime 调用次数 = %d, want 1（预解析 + memo 单次装载）", calls)
	}
}

// TestDD3D1CompatScopeResolverPrefersRequestMemo 钉住 resolver 的请求内
// memo 臂：ctx 已带预解析结果时直接返回（含 nil scope），无 cache 也不回源。
func TestDD3D1CompatScopeResolverPrefersRequestMemo(t *testing.T) {
	resolver := chainCompatScopeResolver(nil)
	memoScope := &openaicompatcore.GatewayScope{SystemAccountID: "sys", APIKeyID: "key"}
	r := withBearer("any-key").WithContext(context.WithValue(context.Background(), chainCompatScopeContextKey{}, memoScope))
	if got := resolver(r); got != memoScope {
		t.Fatalf("memo 命中时必须原样返回预解析 scope, got %+v", got)
	}
	plain := withBearer("any-key")
	if got := resolver(plain); got != nil {
		t.Fatalf("无 memo 且无 cache 时兜底返回 nil, got %+v", got)
	}
}

// ---------------------------------------------------------------------------
// DD3-D3 fixtures
// ---------------------------------------------------------------------------

// ddD3RealtimeBlockedCircuits 是 realtime 计数面的替身：Record 返回已达阈值
// 的 Blocked 决策（带 Retry-After），验证 429 渲染头。
type ddD3RealtimeBlockedCircuits struct {
	ddC1RecordingCircuits
}

func (c *ddD3RealtimeBlockedCircuits) RecordPreAuthFailure(context.Context, gatewaypreauth.PreAuthFailureInput) (gatewaypreauth.CircuitDecision, error) {
	retryAfter := int64(7)
	return gatewaypreauth.CircuitDecision{
		Blocked:           true,
		Reason:            "pre_auth_failures",
		RetryAfterSeconds: &retryAfter,
	}, nil
}

// TestDD3D3RealtimeTokenCircuitBlocked429CarriesConnectionClose 钉住：
// realtime token 面 429 熔断响应携带 Connection: close（与主链
// sendPreAuthCircuitResponse 同口径）。
func TestDD3D3RealtimeTokenCircuitBlocked429CarriesConnectionClose(t *testing.T) {
	inner := newSlogObservability(slog.New(slog.NewTextHandler(io.Discard, nil)), gatewaypreauth.SystemClock{})
	chain := &gatewayChain{
		preauth: &gatewaypreauth.Service{
			Responses:     &recordingFailureSink{},
			Observability: inner,
			Clock:         gatewaypreauth.SystemClock{},
			Circuits:      &ddD3RealtimeBlockedCircuits{},
			IPPolicy:      traceTestNoopIPPolicy{},
		},
		observability: inner,
		clock:         gatewaypreauth.SystemClock{},
	}
	recorder := httptest.NewRecorder()
	res := gatewaypreauth.NewTrackingWriter(recorder)
	req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodGet, "/v1/realtime?model=mock", nil))
	blocked, failure := chain.recordRealtimeTokenPreAuthFailure(context.Background(), req, res, "bad-token", gatewaypreauth.PreAuthFailureInvalidAPIKey)
	if !blocked || failure == nil || failure.status != http.StatusTooManyRequests {
		t.Fatalf("blocked = %v, failure = %+v, want Blocked 429", blocked, failure)
	}
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("状态码 = %d, want 429", recorder.Code)
	}
	if got := recorder.Header().Get("Connection"); got != "close" {
		t.Fatalf("Connection 头 = %q, want close（DD3-D3）", got)
	}
	if got := recorder.Header().Get("Retry-After"); got != "7" {
		t.Fatalf("Retry-After 头 = %q, want 7", got)
	}
}

// TestDD3D3CompatCircuitBlocked429CarriesConnectionClose 钉住：compat 入口
// 守卫短路 429 时响应携带 Connection: close。
func TestDD3D3CompatCircuitBlocked429CarriesConnectionClose(t *testing.T) {
	circuits := &ddC1RecordingCircuits{failThreshold: 1}
	chain := ddC1Chain(t, circuits)
	first := httptest.NewRecorder()
	chain.ServeHTTP(first, withBearer("bad-key"))
	if first.Code != http.StatusUnauthorized {
		t.Fatalf("首次请求状态码 = %d, want 401（未达短路）", first.Code)
	}
	blocked := httptest.NewRecorder()
	chain.ServeHTTP(blocked, withBearer("bad-key"))
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("达阈值后状态码 = %d, want 429, body=%s", blocked.Code, blocked.Body.String())
	}
	if got := blocked.Header().Get("Connection"); got != "close" {
		t.Fatalf("Connection 头 = %q, want close（DD3-D3）", got)
	}
	if got := blocked.Header().Get("Retry-After"); got != "42" {
		t.Fatalf("Retry-After 头 = %q, want 42", got)
	}
	if body := blocked.Body.String(); !strings.Contains(body, "client_ip_pre_auth_circuit_open") {
		t.Fatalf("响应缺熔断错误码: %s", body)
	}
}
