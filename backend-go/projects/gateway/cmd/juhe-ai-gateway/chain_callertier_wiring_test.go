package main

// 调用方分级影子观测接线测试（调用方分级调度设计 §3/§6/§8 M1）：
//   - 驱动选择跟随该链 state Redis 客户端可用性（§6.1 部署形态选择）；
//   - 身份通道与 D-119 runtime 快照同源；
//   - 认证成功每请求恰计一次分母，认证失败不计（§3 分母语义）。
// 全部断言用真 Recorder + MemoryStore（§6.1 memory 部署形态），不触网。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycallertier"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/redis/go-redis/v9"
)

// TestNewCallerTierStoreFollowsStateClient：StateClient 非 nil 选 Redis 跨进
// 程驱动，nil 选进程内 memory——双形态都是启动时选定的部署形态（设计 §6.1）。
func TestNewCallerTierStoreFollowsStateClient(t *testing.T) {
	if _, ok := newCallerTierStore(nil).(*gatewaycallertier.MemoryStore); !ok {
		t.Fatal("nil StateClient 必须选 memory 驱动")
	}
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	if _, ok := newCallerTierStore(client).(*gatewaycallertier.RedisStore); !ok {
		t.Fatal("非 nil StateClient 必须选 Redis 驱动")
	}
}

// TestChainGatewayAPIKeyIDOfReadsRuntimeContext：身份 provider 读 D-119 挂进
// 请求上下文的 runtime 快照，与 body 拒绝记录同一身份通道。
func TestChainGatewayAPIKeyIDOfReadsRuntimeContext(t *testing.T) {
	if got := chainGatewayAPIKeyIDOf(nil); got != "" {
		t.Fatalf("nil 请求 = %q，期望空串", got)
	}
	plain := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if got := chainGatewayAPIKeyIDOf(plain); got != "" {
		t.Fatalf("无快照请求 = %q，期望空串", got)
	}
	runtime := &gatewayruntimecache.GatewayRuntime{APIKey: &gatewayruntimecache.GatewayAPIKeyRow{ID: "key_1"}}
	withRuntime := plain.WithContext(context.WithValue(plain.Context(), chainGatewayRuntimeKey{}, runtime))
	if got := chainGatewayAPIKeyIDOf(withRuntime); got != "key_1" {
		t.Fatalf("身份 = %q，期望 key_1", got)
	}
	noKey := &gatewayruntimecache.GatewayRuntime{}
	withNoKey := plain.WithContext(context.WithValue(plain.Context(), chainGatewayRuntimeKey{}, noKey))
	if got := chainGatewayAPIKeyIDOf(withNoKey); got != "" {
		t.Fatalf("无 Key 快照 = %q，期望空串", got)
	}
}

// TestGatewayChainCallerTierDenominatorWiring：走真实 /v1 链（preauth → 派发
// → 响应，mock 上游）。认证成功每请求恰计一次分母（同一 Key 状态 DayRequests
// 随请求数 +1），认证失败（401）不计；响应语义与既有冒烟链完全一致。
func TestGatewayChainCallerTierDenominatorWiring(t *testing.T) {
	fixture := newChainFixture(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-upstream-account-key" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-smoke","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"链路冒烟"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	if _, err := fixture.db.Exec(fixtureBind(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`),
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key", "base_url": upstream.URL}), fixture.accountID); err != nil {
		t.Fatalf("update account credentials: %v", err)
	}
	// B4 第一批单连接事实源：上游指向同步改连接行。
	chainPointAccountAtUpstream(t, fixture.db, fixture.accountID, upstream.URL)

	chain, shutdown, err := composeGatewayChain(chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, t.TempDir()))
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	defer shutdown()

	// 组合级测试缝：把链上记录器换成接真 MemoryStore 的 Recorder（与
	// imagePreflight.preauth 回填同一先例），分母断言读同一存储快照。
	store := gatewaycallertier.NewMemoryStore(time.Now)
	callerTier, err := gatewaycallertier.NewRecorder(store, gatewaycallertier.Metrics{}, nil)
	if err != nil {
		t.Fatalf("构造 caller tier recorder: %v", err)
	}
	chain.callerTier = callerTier

	server := httptest.NewServer(chain)
	defer server.Close()
	client := &http.Client{Timeout: 15 * time.Second}
	post := func(t *testing.T, authorization string) *http.Response {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions",
			strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", authorization)
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("POST /v1/chat/completions: %v", err)
		}
		t.Cleanup(func() { _ = response.Body.Close() })
		return response
	}

	// 认证成功：请求 1 恰计一次分母，响应 200 语义不变。
	if response := post(t, "Bearer "+fixture.apiKeySecret); response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d，认证成功请求必须保持原语义", response.StatusCode)
	}
	snapshot := store.Snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("状态键数=%d，期望恰 1 个 Key", len(snapshot))
	}
	for _, state := range snapshot {
		if state.DayRequests != 1 || state.DaySignals != 0 {
			t.Fatalf("分母=%d 信号=%d，期望分母 1、信号 0", state.DayRequests, state.DaySignals)
		}
	}

	// 认证成功：请求 2 累计到同一 Key（每请求恰一次，不重复计数）。
	if response := post(t, "Bearer "+fixture.apiKeySecret); response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d，认证成功请求必须保持原语义", response.StatusCode)
	}
	for _, state := range store.Snapshot() {
		if state.DayRequests != 2 {
			t.Fatalf("分母=%d，期望 2", state.DayRequests)
		}
	}

	// 认证失败（无效 Bearer）：不计分母。
	if response := post(t, "Bearer sk-invalid"); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d，期望 401", response.StatusCode)
	}
	for _, state := range store.Snapshot() {
		if state.DayRequests != 2 {
			t.Fatalf("认证失败不得计分母: 分母=%d", state.DayRequests)
		}
	}
}
