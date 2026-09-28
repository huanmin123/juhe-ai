package main

// 迁移漏装配修复（2026-09-28 哲学裁定「功能默认开启」）的组合根回归：
// chain 组装在 CodexContextRoot + CodexContextStateStore 齐备时构造真实
// gatewaycodex.ChatBridgeStateService / CompactPreflightService，
// deps.CodexBridge 不再恒走 no-op 适配器（restore/compaction preflight
// 恒缺席的断线）；依赖缺席保持既有降级（组合测试语义不变）。

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// w1CodexBridgeAdapterOf 断言链上 codex preflight 是本组合根适配器并返回。
func w1CodexBridgeAdapterOf(t *testing.T, chain *gatewayChain) codexPreflightAdapter {
	t.Helper()
	adapter, ok := chain.preauth.Codex.(codexPreflightAdapter)
	if !ok {
		t.Fatalf("chain.preauth.Codex 类型 = %T，want codexPreflightAdapter", chain.preauth.Codex)
	}
	return adapter
}

// w1CodexBridgeChainDeps 构造装配依赖齐备/缺席两种最小链 deps。
func w1CodexBridgeChainDeps(t *testing.T, wired bool) chainRuntimeDeps {
	t.Helper()
	fixture := newChainFixture(t)
	deps := chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, w1CodexBridgeSpoolDir(t))
	if !wired {
		return deps
	}
	store, err := gatewaycodex.NewSQLiteShardContextStateStore(gatewaycodex.SQLiteShardStoreConfig{
		Root:       w1CodexBridgeTempDir(t, "state-shards"),
		ShardCount: 1,
	})
	if err != nil {
		t.Fatalf("create sqlite shard store: %v", err)
	}
	deps.CodexContextRoot = w1CodexBridgeTempDir(t, "codex-context")
	deps.CodexContextStateStore = store
	return deps
}

// w1CodexBridgeTempDir 建独立临时目录（避免与 spool 目录共用同层）。
func w1CodexBridgeTempDir(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name)
}

// w1CodexBridgeSpoolDir 给 chainSmokeDeps 一个独立 spool 目录。
func w1CodexBridgeSpoolDir(t *testing.T) string {
	t.Helper()
	return w1CodexBridgeTempDir(t, "spool")
}

// w1CodexBridgeRequest 构造带 JSON body 的 GatewayRequest。
func w1CodexBridgeRequest(method, path string, body map[string]any) (*gatewaypreauth.GatewayRequest, gatewaypreauth.GatewayResponseWriter) {
	raw := gatewaybody.SerializeGatewayJSONObject(body).Raw
	httpReq := httptest.NewRequest(method, path, bytes.NewReader(raw))
	httpReq.Header.Set("Content-Type", "application/json")
	req := gatewaypreauth.NewGatewayRequest(httpReq)
	req.Body = &gatewaybody.Request{RawBody: raw, Body: body}
	return req, gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())
}

// TestW1CodexBridgeComposeWiresRealPreflight: 依赖齐备（segments 根 + 双模
// 行存储）时链组装构造真实桥——适配器 bridge/compact/registry 非 nil，
// preflight 不再是 no-op。
func TestW1CodexBridgeComposeWiresRealPreflight(t *testing.T) {
	deps := w1CodexBridgeChainDeps(t, true)
	chain, shutdown, assembleErr := composeGatewayChain(deps)
	if assembleErr != nil {
		t.Fatalf("composeGatewayChain: %v", assembleErr)
	}
	defer shutdown()
	adapter := w1CodexBridgeAdapterOf(t, chain)
	if adapter.bridge == nil {
		t.Fatal("依赖齐备时适配器必须持有真实 ChatBridgeStateService（迁移漏装配未修复）")
	}
	if adapter.compact == nil {
		t.Fatal("依赖齐备时适配器必须持有真实 CompactPreflightService")
	}
	if adapter.registry == nil {
		t.Fatal("依赖齐备时适配器必须持有共享 ContextRequestStateRegistry")
	}
}

// TestW1CodexBridgeComposeKeepsNoopDegrade: 依赖缺席（组合测试默认 deps）
// 保持既有 no-op 降级——bridge/compact/registry 全空，preflight 直通。
func TestW1CodexBridgeComposeKeepsNoopDegrade(t *testing.T) {
	deps := w1CodexBridgeChainDeps(t, false)
	chain, shutdown, assembleErr := composeGatewayChain(deps)
	if assembleErr != nil {
		t.Fatalf("composeGatewayChain: %v", assembleErr)
	}
	defer shutdown()
	adapter := w1CodexBridgeAdapterOf(t, chain)
	if adapter.bridge != nil || adapter.compact != nil || adapter.registry != nil {
		t.Fatalf("依赖缺席必须保持 no-op 降级，got bridge=%v compact=%v registry=%v",
			adapter.bridge != nil, adapter.compact != nil, adapter.registry != nil)
	}
}

// TestW1CodexBridgePreflightRealPathNewSession: 真实装配下 context state
// preflight 走 gatewaycodex 真实路径——/v1/responses 新会话（无
// previous_response_id）不完成请求，registry 记录请求态，审计元数据带
// new_session 模式；请求信号终结后 registry entry 被 Release（对齐 Node
// request symbol GC 语义，防强键 map 泄漏）。
func TestW1CodexBridgePreflightRealPathNewSession(t *testing.T) {
	deps := w1CodexBridgeChainDeps(t, true)
	chain, shutdown, assembleErr := composeGatewayChain(deps)
	if assembleErr != nil {
		t.Fatalf("composeGatewayChain: %v", assembleErr)
	}
	defer shutdown()
	adapter := w1CodexBridgeAdapterOf(t, chain)

	req, res := w1CodexBridgeRequest(http.MethodPost, "/v1/responses", map[string]any{
		"model": "gpt-test",
		"input": "你好",
	})
	audit := &chainTestAuditCapture{}
	signal, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed, err := adapter.ApplyContextStatePreflight(context.Background(), gatewaypreauth.CodexContextStateInput{
		Req:             req,
		Res:             res,
		AuditCapture:    audit,
		UsageContext:    gatewaypreauth.GatewayFailureUsageContext{TraceID: "trace-w1", TrafficSource: "gateway"},
		StartedAt:       1,
		SystemAccountID: "sys",
		APIKeyID:        "key",
		GroupID:         "group",
		GroupAccess:     gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai"},
		Signal:          signal,
	})
	if err != nil {
		t.Fatalf("ApplyContextStatePreflight: %v", err)
	}
	if completed {
		t.Fatal("新会话 preflight 不得完成请求（应继续走 dispatch）")
	}
	state, ok := adapter.registry.Get(req)
	if !ok {
		t.Fatal("真实路径必须在 registry 记录请求态（no-op 不会记录）")
	}
	if state.RequestKind != gatewaycodex.RequestKindResponses || state.Restored {
		t.Fatalf("请求态 kind=%q restored=%v；want responses / false", state.RequestKind, state.Restored)
	}
	if len(audit.metadata) == 0 {
		t.Fatal("真实路径必须写审计元数据（codex_responses_chat_bridge_state）")
	}

	// 请求信号终结 → AfterFunc Release（异步，轮询等待）。
	cancel()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, still := adapter.registry.Get(req); !still {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("请求信号终结后 registry entry 必须被 Release（防强键 map 泄漏）")
}

// TestW1CodexBridgeCompactPreflightRealPathPassthrough: 真实装配下非 compact
// 请求的 compact preflight 直通（Completed=false，账户序列原样保留）——
// no-op 与真实路径在该面的可观察契约一致，断言真实路径不改变直通语义。
func TestW1CodexBridgeCompactPreflightRealPathPassthrough(t *testing.T) {
	deps := w1CodexBridgeChainDeps(t, true)
	chain, shutdown, assembleErr := composeGatewayChain(deps)
	if assembleErr != nil {
		t.Fatalf("composeGatewayChain: %v", assembleErr)
	}
	defer shutdown()
	adapter := w1CodexBridgeAdapterOf(t, chain)

	req, res := w1CodexBridgeRequest(http.MethodPost, "/v1/chat/completions", map[string]any{
		"model":    "gpt-test",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	accounts := []gatewayruntimecache.OpenAIAccountSecret{{ID: "acc-1"}, {ID: "acc-2"}}
	result, err := adapter.ApplyChatBridgeCompactPreflight(context.Background(), gatewaypreauth.CodexCompactPreflightInput{
		Req:              req,
		Res:              res,
		AuditCapture:     &chainTestAuditCapture{},
		DispatchAccounts: accounts,
		Signal:           context.Background(),
	})
	if err != nil {
		t.Fatalf("ApplyChatBridgeCompactPreflight: %v", err)
	}
	if result.Completed {
		t.Fatal("非 compact 请求的 compact preflight 必须直通（Completed=false）")
	}
	if len(result.Accounts) != len(accounts) {
		t.Fatalf("直通账户数 = %d，want %d", len(result.Accounts), len(accounts))
	}
}
