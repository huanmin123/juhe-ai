package acceptance

// 会话名额 fullchain 验收（账户亲和同层单向粘性与会话名额设计 §10.1 验收 9）：
//  1. Redis cacheDriver 下名额表启用；同优先级 A/B 两账户，A 名额 32 个会话填满；
//  2. 第 33 个新会话的 claim 占位 owner 是 A，但 A 名额已满且该会话没有既有
//     绑定 → 不等待，直接分配到有空名额的 B，最终成功按 slot_fallback 重绑；
//  3. 同一会话下一次请求因 B 名额 active 继续落 B；
//  4. 运行中 Prometheus 面出现 slot_fallback 指标与有界 reason。

import (
	"fmt"
	"strings"
	"testing"
)

func TestFullchainSessionSlotFallback(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixtureExtraEnv(t, func(string) map[string]string {
		return map[string]string{"JUHE_AI_CACHE_DRIVER": "redis"}
	})

	keyA := fullchainUpstreamKey(t, "Slot-a")
	keyB := fullchainUpstreamKey(t, "Slot-b")
	route := f.newRoute("Slot", "normal", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{
			{key: keyA, extra: map[string]any{"concurrencyLimit": 1, "priority": 0}},
			{key: keyB, extra: map[string]any{"concurrencyLimit": 1, "priority": 1}},
		}},
	}, nil, nil)

	hits := func() (int, int) {
		return len(f.mock.protocolCallsByKey(keyA)), len(f.mock.protocolCallsByKey(keyB))
	}

	// A 的 slotCap = clamp(min(1×4, ∞), 32, 10000) = 32；32 个新会话依次占满 A。
	for i := 0; i < 32; i++ {
		session := fmt.Sprintf("sess-slot-fill-%d", i)
		requireAffinityResponseOK(t, fmt.Sprintf("slot-fill-%d", i),
			f.chatSessionAsCodex(t, route.apiKey, session, responsesSessionBody(fmt.Sprintf("名额填充%d", i))))
	}
	if a, b := hits(); a != 32 || b != 0 {
		t.Fatalf("32 个新会话应全部落 A，命中 A=%d B=%d", a, b)
	}

	// 第 33 个新会话：A 名额满且无既有绑定，不等待，直接落 B 并按名额回退重绑。
	session := "sess-slot-fallback"
	requireAffinityResponseOK(t, "slot-fallback",
		f.chatSessionAsCodex(t, route.apiKey, session, responsesSessionBody("名额回退")))
	if a, b := hits(); a != 32 || b != 1 {
		t.Fatalf("名额回退应落 B，命中 A=%d B=%d", a, b)
	}

	// 同一会话在 B 有名额，下一次请求继续落 B。
	requireAffinityResponseOK(t, "slot-stick",
		f.chatSessionAsCodex(t, route.apiKey, session, responsesSessionBody("名额粘住")))
	if a, b := hits(); a != 32 || b != 2 {
		t.Fatalf("名额活跃会话应继续落 B，命中 A=%d B=%d", a, b)
	}

	metrics := scrapeGatewayMetrics(t, f)
	requirePositiveMetric(t, metrics, "juhe_ai_gateway_session_affinity_slot_fallback_total")
	if !strings.Contains(metrics, `reason="slot_fallback"`) {
		t.Fatalf("rebind 指标缺少 slot_fallback reason 标签:\n%s", metrics)
	}
	if !strings.Contains(metrics, `reason="success"`) {
		t.Fatalf("rebind 指标缺少 success reason 标签:\n%s", metrics)
	}
}
