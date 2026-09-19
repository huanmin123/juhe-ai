// 验收矩阵开放项补齐（计划 §3 G3，原 Stage2 套件未覆盖；2026-09-18 补；
// R6 hybrid_smart 验收项随 hybrid 路由模式移除一并删除）。
//
//	G3 组内 tier 序：superPriority 恒先（chain_accounts.go 候选 SQL 的
//	local_super_priority_enabled DESC 先于 local_priority）；fallbackEnabled
//	仅在非 fallback 耗尽后派发（local_fallback_enabled ASC 殿后）。
//
// 独立顶层测试：不改动既有 TestFullchainStage2Matrix 的子测试注册表，复用
// 同一 fullchain fixture 与 mock 面（env 门控与 Stage2 一致）。
package acceptance

import (
	"net/http"
	"testing"

	platformmock "github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"
)

// TestFullchainStage2OpenItems G3（计划 §3 开放项验收门）。
func TestFullchainStage2OpenItems(t *testing.T) {
	f := startFullchainFixture(t)

	t.Run("G3_super_priority_first", func(t *testing.T) {
		normalKey := fullchainUpstreamKey(t, "G3S-normal")
		superKey := fullchainUpstreamKey(t, "G3S-super")
		route := f.newRoute("G3S", "normal", []fullchainGroupSpec{
			{accounts: []fullchainAccountSpec{
				// 非 super 高优先级：若 tier 序不生效，它会先被命中。
				{key: normalKey, defaultScenario: platformmock.ScenarioChatOK,
					extra: map[string]any{"priority": 100}},
				{key: superKey, defaultScenario: platformmock.ScenarioChatOK,
					extra: map[string]any{"priority": 200, "superPriorityEnabled": true}},
			}},
		}, nil, nil)

		response := f.chat(route.apiKey, chatBody(acceptanceModel, "G3-super"))
		if response.Status != http.StatusOK {
			t.Fatalf("G3 super status=%d body=%s", response.Status, response.Body)
		}
		// 按 key 维度断言（全局 protocolCalls 序列跨子测试累积，不可用）。
		if got := len(f.mock.protocolCallsByKeyModel(superKey, acceptanceModel)); got != 1 {
			t.Fatalf("G3 superPriority calls=%d want 1（superPriority 必须先于更低 priority 的普通账户被命中）", got)
		}
		if got := len(f.mock.protocolCallsByKeyModel(normalKey, acceptanceModel)); got != 0 {
			t.Fatalf("G3 normal calls=%d want 0（superPriority 存在时普通账户不应先被派发）", got)
		}
	})

	t.Run("G3_fallback_after_non_fallback_exhausted", func(t *testing.T) {
		fallbackKey := fullchainUpstreamKey(t, "G3F-fallback")
		normalKey := fullchainUpstreamKey(t, "G3F-normal")
		route := f.newRoute("G3F", "normal", []fullchainGroupSpec{
			{accounts: []fullchainAccountSpec{
				// fallback 账户虽然 priority 更高（100 < 200），tier 序要求
				// 它殿后：只有非 fallback 候选耗尽后才可派发。
				{key: fallbackKey, defaultScenario: platformmock.ScenarioChatOK,
					extra: map[string]any{"priority": 100, "fallbackEnabled": true}},
				{key: normalKey, defaultScenario: platformmock.ScenarioStatus500,
					extra: map[string]any{"priority": 200}},
			}},
		}, nil, nil)

		response := f.chat(route.apiKey, chatBody(acceptanceModel, "G3-fallback"))
		if response.Status != http.StatusOK {
			t.Fatalf("G3 fallback status=%d body=%s", response.Status, response.Body)
		}
		// 同账户瞬态重试（F1 语义）允许 normal 被尝试多次，但 fallback 必须
		// 恰好一次且晚于全部 normal 尝试（非 fallback 耗尽后才派发）。
		normalCalls := f.mock.protocolCallsByKeyModel(normalKey, acceptanceModel)
		fallbackCalls := f.mock.protocolCallsByKeyModel(fallbackKey, acceptanceModel)
		if len(normalCalls) < 1 {
			t.Fatalf("G3 normal calls=%d want >=1（非 fallback 候选应先被派发）", len(normalCalls))
		}
		if len(fallbackCalls) != 1 {
			t.Fatalf("G3 fallback calls=%d want 1（fallback 只能在非 fallback 耗尽后派发一次）", len(fallbackCalls))
		}
		if !fallbackCalls[0].At.After(normalCalls[len(normalCalls)-1].At) {
			t.Fatalf("G3 时序错误：fallback 必须晚于全部非 fallback 尝试（normal 末次=%v fallback=%v）",
				normalCalls[len(normalCalls)-1].At, fallbackCalls[0].At)
		}
	})
}
