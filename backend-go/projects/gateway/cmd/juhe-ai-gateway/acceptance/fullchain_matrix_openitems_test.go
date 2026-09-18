// 验收矩阵开放项补齐（计划 §3 R6/G3，原 Stage2 套件未覆盖；2026-09-18 补）。
//
//	R6 hybrid_smart：评分失败走 fallback 档——评分账户恒 500，断言请求经
//	评分尝试后被改写为 level-1 目标模型、由低档账户服务，且落 hybrid_route
//	审计（Node hybrid/routing.service.ts 的 scoringFallbackApplied 契约）。
//	G3 组内 tier 序：superPriority 恒先（chain_accounts.go 候选 SQL 的
//	local_super_priority_enabled DESC 先于 local_priority）；fallbackEnabled
//	仅在非 fallback 耗尽后派发（local_fallback_enabled ASC 殿后）。
//
// 独立顶层测试：不改动既有 TestFullchainStage2Matrix 的子测试注册表，复用
// 同一 fullchain fixture 与 mock 面（env 门控与 Stage2 一致）。
package acceptance

import (
	"net/http"
	"strings"
	"testing"

	platformmock "github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"
)

// TestFullchainStage2OpenItems R6 + G3（计划 §3 开放项验收门）。
func TestFullchainStage2OpenItems(t *testing.T) {
	f := startFullchainFixture(t)

	t.Run("R6_hybrid_smart_scoring_fallback", func(t *testing.T) {
		scoreKey := fullchainUpstreamKey(t, "R6-score")
		lowKey := fullchainUpstreamKey(t, "R6-low")
		highKey := fullchainUpstreamKey(t, "R6-high")
		route := f.newRoute("R6", "hybrid_smart", []fullchainGroupSpec{
			{accounts: []fullchainAccountSpec{
				// 评分账户：绑定分组池内唯一支持 scoringModel 的账户，恒 500
				// → 评分必然失败，触发 scoringFallback 契约。
				{key: scoreKey, defaultScenario: platformmock.ScenarioStatus500,
					extra: map[string]any{"supportedModels": []string{"hybrid-score-model", fullchainProbeModel}}},
				// 低档账户：level-1 fallback 目标。
				{key: lowKey, defaultScenario: platformmock.ScenarioChatOK,
					extra: map[string]any{"supportedModels": []string{"hybrid-low-model", fullchainProbeModel}}},
			}},
			{accounts: []fullchainAccountSpec{
				// 高档账户：level-3+ 目标（评分失败 + fallbackMaxLevel=2 时不可达）。
				{key: highKey, defaultScenario: platformmock.ScenarioChatOK,
					extra: map[string]any{"supportedModels": []string{"hybrid-high-model", fullchainProbeModel}}},
			}},
		}, nil, map[string]any{"hybridRoutingConfig": map[string]any{
			"scoringModel":            "hybrid-score-model",
			"scoringContextMode":      "full_request",
			"qualityPreference":       "balanced",
			"scoringTimeoutMs":        3000,
			"scoringFallbackMaxLevel": 2,
			"levelRoutes": []map[string]any{
				{"minLevel": 1, "maxLevel": 2, "targetModel": "hybrid-low-model", "enabled": true},
				{"minLevel": 3, "maxLevel": 10, "targetModel": "hybrid-high-model", "enabled": true},
			},
		}})

		// 客户端请求高档模型：评分失败后必须被改写为低档目标模型并由低档
		// 账户服务——若 hybrid 未介入，高档账户会直接命中（序列断言可判别）。
		response := f.chat(route.apiKey, chatBody("hybrid-high-model", "R6-评分失败降档"))
		if response.Status != http.StatusOK || !strings.Contains(response.Body, "choices") {
			t.Fatalf("R6 fallback status=%d body=%s", response.Status, response.Body)
		}

		// 断言按 key+model 维度（protocolCalls 全局序列只统计
		// acceptanceModel，且跨子测试累积；fullchainUpstreamCall.At 提供时序）。
		lowCalls := f.mock.protocolCallsByKeyModel(lowKey, "hybrid-low-model")
		if len(lowCalls) != 1 {
			t.Fatalf("R6 low target model calls=%d want 1（请求模型应被改写为 hybrid-low-model）", len(lowCalls))
		}
		scoreCalls := f.mock.protocolCallsByKeyModel(scoreKey, "hybrid-score-model")
		if len(scoreCalls) != 1 {
			t.Fatalf("R6 scoring calls=%d want 1（评分辅助调用应以 scoringModel 发起）", len(scoreCalls))
		}
		if got := len(f.mock.protocolCallsByKeyModel(highKey, "hybrid-high-model")); got != 0 {
			t.Fatalf("R6 high tier calls=%d want 0（评分失败且 fallbackMaxLevel=2 时高档不可达）", got)
		}
		if !lowCalls[0].At.After(scoreCalls[0].At) {
			t.Fatalf("R6 时序错误：评分调用应先于低档接管（score=%v low=%v）", scoreCalls[0].At, lowCalls[0].At)
		}
		// 审计：hybrid_route 决策必须落 gateway_metadata（F11 修复后 label 可解析）。
		f.waitAuditLabels(t, route.apiKeyID, []string{"hybrid_route"}, "hybrid route decision")
	})

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
