package main

// 自 w27_bridge_combos_test.go 拆出的非桥回归（2026-11-07 跨协议 Responses→Chat
// 桥退役：原 w27 桥 combo 套件随桥删除，本文件保留其中不依赖桥机制的
// Post 阶段标记去重语义测试）。链装配改用 chainSmokeDeps 最小组合。

import (
	"bytes"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayoauthcodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// w27DedupNoopProbe 只计数不改写（标记去重断言的干净形态）。
type w27DedupNoopProbe struct {
	calls int
}

func (p *w27DedupNoopProbe) sanitize(items []any, _ gatewayoauthcodex.SanitizeCodexHistoryOptions) gatewayoauthcodex.CodexHistorySanitizeResult {
	p.calls++
	return gatewayoauthcodex.CodexHistorySanitizeResult{Items: items, Changed: false}
}

// w27InstallDedupSanitizeHook 注入全局清理算法 hook 并返回恢复函数（hook 为
// 进程级全局；本文件测试不使用 t.Parallel，串行运行与既有套件一致）。
func w27InstallDedupSanitizeHook(t *testing.T, hook gatewayoauthcodex.SanitizeCodexHistoryItems) func() {
	t.Helper()
	orig := gatewayoauthcodex.SanitizeCodexHistory
	t.Cleanup(func() { gatewayoauthcodex.SanitizeCodexHistory = orig })
	gatewayoauthcodex.SanitizeCodexHistory = hook
	return func() { gatewayoauthcodex.SanitizeCodexHistory = orig }
}

// TestW27PostStageSkipIfProcessedMarkDedup：§5.4「后置遇到标记必须跳过」——
// Post 阶段在 policy.Post=sanitize 且 SkipIfProcessed 时以序列化 body 标记
// 去重：带标记 body 0 次清理调用且字节原样，无标记 body 恰 1 次调用。
func TestW27PostStageSkipIfProcessedMarkDedup(t *testing.T) {
	fixture := newChainFixture(t)
	chain, shutdown, err := composeGatewayChain(chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, t.TempDir()))
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	t.Cleanup(shutdown)
	account := gatewayruntimecache.OpenAIAccountSecret{
		ID: "acc-w27-dedup", ProtocolCode: "openai", ProtocolVersion: "v1",
		Type: "api_key", BaseURL: "https://w27-upstream.invalid/v1", APIKey: "sk-w27-dedup",
	}
	policy := gatewaydispatch.HistoryPrepPolicyForRequest(account, "codex_responses", "responses")
	if policy.Post != gatewaydispatch.HistoryPrepStageSanitize || !policy.SkipIfProcessed {
		t.Fatalf("前置策略形态 Post=%q SkipIfProcessed=%v，want sanitize/true", policy.Post, policy.SkipIfProcessed)
	}

	// 已处理标记按序列化 body 字节内容为键（serializedBodyKey = string(body)），
	// 两臂必须使用互不相同的 body 字节，避免标记跨臂误命中。
	markedBody := gatewaybody.SerializeGatewayJSONObject(map[string]any{
		"model": "gpt-5",
		"input": []any{map[string]any{"w27_dedup": "marked"}},
	}).Raw

	t.Run("marked_body_skips_post_sanitize", func(t *testing.T) {
		marked := gatewayoauthcodex.MarkGatewayCodexHistorySanitized(bytes.Clone(markedBody))
		probe := &w27DedupNoopProbe{}
		restore := w27InstallDedupSanitizeHook(t, probe.sanitize)
		defer restore()
		result := chain.engine.SanitizePreparedCodexResponsesHistoryForAccount(account, marked, policy)
		if !bytes.Equal(result, marked) {
			t.Fatal("带标记 body 必须原样返回（Post 阶段跳过）")
		}
		if probe.calls != 0 {
			t.Fatalf("清理调用次数 = %d，want 0（标记命中 SkipIfProcessed）", probe.calls)
		}
	})

	t.Run("unmarked_body_runs_post_sanitize_once", func(t *testing.T) {
		unmarked := gatewaybody.SerializeGatewayJSONObject(map[string]any{
			"model": "gpt-5",
			"input": []any{map[string]any{"w27_dedup": "unmarked"}},
		}).Raw
		probe := &w27DedupNoopProbe{}
		restore := w27InstallDedupSanitizeHook(t, probe.sanitize)
		defer restore()
		result := chain.engine.SanitizePreparedCodexResponsesHistoryForAccount(account, unmarked, policy)
		if probe.calls != 1 {
			t.Fatalf("清理调用次数 = %d，want 1（无标记不跳过）", probe.calls)
		}
		if !bytes.Equal(result, unmarked) {
			t.Fatal("hook 不改写时 body 必须字节原样")
		}
	})
}
