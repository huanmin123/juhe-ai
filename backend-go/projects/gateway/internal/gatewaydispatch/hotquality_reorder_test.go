package gatewaydispatch

// 缓存率感知调度（设计 5.6/7）：准备期最终排序解释与决策摘要缓存率排序块。
// 高并发等待后的 ReorderOnly 分支已随等待机制退役（普通分组可选调度策略
// 设计 §3.4），排队后重排序专属测试删除。

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
)

// cacherateReorderHotQuality 记录 OrderAsync 调用并按注入返回解释，
// Mock 优先、结果稳定可回放。
type cacherateReorderHotQuality struct {
	fakeHotQuality
	orderAsyncCalls atomic.Int32
	explanation     *gatewayhotquality.HotQualityCandidateSelectionExplanation
}

func (m *cacherateReorderHotQuality) OrderAsync(ctx context.Context, input HotQualityOrderInput) (HotQualityOrder, error) {
	m.orderAsyncCalls.Add(1)
	order, err := m.fakeHotQuality.OrderAsync(ctx, input)
	order.Explanation = m.explanation
	return order, err
}

// cacherateFakeSpeedWindowMs 是 fake 解释里速度窗口字段的占位值（W 语义，
// 只断言摘要投影透传，不参与排序推导）。
const cacherateFakeSpeedWindowMs = int64(3_000)

// cacherateExplanation 构造带候选明细的排序解释（窗口字段固定占位值）。
func cacherateExplanation(accountIDs ...string) *gatewayhotquality.HotQualityCandidateSelectionExplanation {
	base := 1200.0
	details := make([]gatewayhotquality.HotQualityCandidateOrderDetail, 0, len(accountIDs))
	for index, accountID := range accountIDs {
		rate := 0.25 * float64(index+1)
		details = append(details, gatewayhotquality.HotQualityCandidateOrderDetail{
			AccountID:      accountID,
			CacheHitRate:   &rate,
			CacheQuantum:   index,
			SpeedQualified: true,
		})
	}
	return &gatewayhotquality.HotQualityCandidateSelectionExplanation{
		Mode:                  gatewayhotquality.HotQualityModeCostFirst,
		SpeedBaseEwmaMs:       &base,
		SpeedThresholdMs:      cacherateFakeSpeedWindowMs,
		CacheRateStale:        true,
		CacheRateEnabled:      true,
		CandidateOrderDetails: details,
	}
}

// 最终生效排序 = applyHotQualityOrder 那次 OrderAsync，解释随
// PreparationResult 带出并进摘要。
func TestNonHighConcurrencyExplanationFromOrderAsync(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	hot := &cacherateReorderHotQuality{explanation: cacherateExplanation("a-2", "a-1")}
	engine.HotQuality = hot
	received := w1bCaptureTerminalDecision(t)

	input := dispatchPreparationInput(t, testAccounts("a-1", "a-2"))
	result, err := pipeline.PrepareOpenAIGatewayDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != PreparationOutcomeReady {
		t.Fatalf("outcome = %s", result.Outcome)
	}
	if result.HotQualityOrderExplanation != hot.explanation {
		t.Fatal("解释必须来自 OrderAsync 那次排序")
	}
	summary := (*received)[0].Summary
	if summary.SpeedThresholdMs != cacherateFakeSpeedWindowMs || len(summary.CandidateOrder) != 2 {
		t.Fatalf("summary cache-rate block = %#v/%#v", summary.SpeedThresholdMs, summary.CandidateOrder)
	}
}

// 摘要组装：解释 → 候选明细（截断保护 + JSON 键名契约）；nil 解释整体
// 缺省，不用零值占位。
func TestBuildDispatchDecisionSummaryCacheRateBlock(t *testing.T) {
	details := make([]gatewayhotquality.HotQualityCandidateOrderDetail, 0, dispatchDecisionSummaryListCap+3)
	rate := 0.5
	for index := 0; index < dispatchDecisionSummaryListCap+3; index++ {
		details = append(details, gatewayhotquality.HotQualityCandidateOrderDetail{
			AccountID:      "c-" + intToStringTest(index),
			CacheHitRate:   &rate,
			CacheQuantum:   5,
			SpeedQualified: index%2 == 0,
		})
	}
	input := DispatchDecisionSummaryInput{
		Eligible:              testAccounts("a-1"),
		HotQualityExplanation: cacherateExplanation("a-2", "a-1"),
	}
	input.HotQualityExplanation.CandidateOrderDetails = details
	summary := BuildDispatchDecisionSummary(input)
	if summary.SpeedThresholdMs != cacherateFakeSpeedWindowMs {
		t.Fatalf("speedThresholdMs = %d", summary.SpeedThresholdMs)
	}
	if !summary.CandidateOrderTruncated || len(summary.CandidateOrder) != dispatchDecisionSummaryListCap {
		t.Fatalf("candidateOrder truncated/len = %v/%d", summary.CandidateOrderTruncated, len(summary.CandidateOrder))
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["speedThresholdMs"].(float64) != float64(cacherateFakeSpeedWindowMs) {
		t.Fatalf("decoded speedThresholdMs = %#v", decoded["speedThresholdMs"])
	}
	if decoded["speedBaseEwmaMs"].(float64) != 1200.0 || decoded["cacheRateStale"] != true || decoded["cacheRateEnabled"] != true {
		t.Fatalf("decoded decision-level block = %#v", decoded)
	}
	candidate := decoded["candidateOrder"].([]any)[0].(map[string]any)
	if candidate["id"] != "c-0" || candidate["cacheQuantum"].(float64) != 5 || candidate["speedQualified"] != true {
		t.Fatalf("decoded candidate[0] = %#v", candidate)
	}
	if _, present := candidate["cacheHitRate"]; !present {
		t.Fatalf("cacheHitRate 在场契约: %#v", candidate)
	}

	// nil 解释：缓存率块整体缺省（omitempty）。
	empty := BuildDispatchDecisionSummary(DispatchDecisionSummaryInput{Eligible: testAccounts("a-1")})
	encodedEmpty, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	for _, absent := range []string{"speedBaseEwmaMs", "speedThresholdMs", "cacheRateStale", "cacheRateEnabled", "candidateOrder"} {
		if jsonContains(encodedEmpty, absent) {
			t.Fatalf("nil 解释时 %s 必须缺省: %s", absent, encodedEmpty)
		}
	}
}

func jsonContains(encoded []byte, key string) bool {
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return false
	}
	_, present := decoded[key]
	return present
}

func reflectDeepEqualIDs(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
