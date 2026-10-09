package gatewaydispatch

// 期二（缓存率感知调度设计 5.6/7）：高并发分组最终亲和排序后的层内重排序
// （ReorderOnly）与决策摘要缓存率排序块。全部纯内存 fake，不连数据库。

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// cacherateReorderHotQuality 记录 OrderAsync / ReorderOnly 调用，重排序按
// 注入返回（固定序 + 解释或错误），Mock 优先、结果稳定可回放。
type cacherateReorderHotQuality struct {
	fakeHotQuality
	orderAsyncCalls atomic.Int32
	reorderCalls    atomic.Int32
	reorderErr      error
	// reorderAccounts 为 nil 时透传输入序。
	reorderAccounts []AccountCandidate
	explanation     *gatewayhotquality.HotQualityCandidateSelectionExplanation
	lastReorderMode string
}

func (m *cacherateReorderHotQuality) OrderAsync(ctx context.Context, input HotQualityOrderInput) (HotQualityOrder, error) {
	m.orderAsyncCalls.Add(1)
	order, err := m.fakeHotQuality.OrderAsync(ctx, input)
	order.Explanation = m.explanation
	return order, err
}

func (m *cacherateReorderHotQuality) ReorderOnly(_ context.Context, input HotQualityOrderInput) (HotQualityOrder, error) {
	m.reorderCalls.Add(1)
	m.lastReorderMode = input.Mode
	if m.reorderErr != nil {
		return HotQualityOrder{}, m.reorderErr
	}
	accounts := input.Accounts
	if m.reorderAccounts != nil {
		accounts = m.reorderAccounts
	}
	return HotQualityOrder{Accounts: accounts, Explanation: m.explanation}, nil
}

// sequencedBusyAffinity 按调用序给出高并发繁忙判定（busy → 等待 → 复查通过
// 的时序用可控真值表表达）。
type sequencedBusyAffinity struct {
	fakeAffinity
	busyByCall []bool
	calls      atomic.Int32
}

func (f *sequencedBusyAffinity) AreHighConcurrencyAccountsBusyForLaneAsync(context.Context, []AccountCandidate, HighConcurrencyBusyOptions) (bool, error) {
	index := int(f.calls.Add(1)) - 1
	if index >= len(f.busyByCall) {
		return false, nil
	}
	return f.busyByCall[index], nil
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

func cacherateHighConcurrencyInput(t *testing.T, accounts []AccountCandidate) gatewaypreauth.DispatchPreparationInput {
	t.Helper()
	input := dispatchPreparationInput(t, accounts)
	input.GroupAccess = highConcurrencyGroupAccess(&gatewayruntimecache.GroupSchedulingPolicy{})
	return input
}

// 高并发等待路径：等待 + 最终亲和排序后必须调用 ReorderOnly，最终候选序与
// 决策解释取自重排序结果；摘要携带缓存率排序块（设计 5.6 全字段有源）。
func TestHighConcurrencyReorderOnlyAppliedAfterFinalAffinityOrdering(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	hot := &cacherateReorderHotQuality{
		reorderAccounts: testAccounts("a-3", "a-1", "a-2"),
		explanation:     cacherateExplanation("a-3", "a-1", "a-2"),
	}
	engine.HotQuality = hot
	// 忙碌序列：首次 busy（846 刷新+排序）→ 905 后复查 busy（不接管）→
	// 等待前 busy（触发 WaitForCapacity + 最终亲和排序）→ 复查通过。
	engine.Affinity = &sequencedBusyAffinity{busyByCall: []bool{true, true, true, false}}
	engine.Concurrency = &mapBackedConcurrencyStore{current: map[string]int{"a-1": 0, "a-2": 0, "a-3": 0}}
	engine.ClientIPConcurrency = &configurableClientIPConcurrency{enabled: true, acquired: true}
	engine.HighConcurrencyQueue = &configurableQueue{ready: true}
	received := w1bCaptureTerminalDecision(t)

	input := cacherateHighConcurrencyInput(t, testAccounts("a-1", "a-2", "a-3"))
	result, err := pipeline.PrepareOpenAIGatewayDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != PreparationOutcomeReady {
		t.Fatalf("outcome = %s (reason %q)", result.Outcome, result.Reason)
	}
	if hot.reorderCalls.Load() != 1 {
		t.Fatalf("reorder calls = %d, want 1", hot.reorderCalls.Load())
	}
	if hot.lastReorderMode != HotQualityModeCostFirst {
		t.Fatalf("reorder mode = %q, want cost_first（沿用 hotQualityModeFor 取值）", hot.lastReorderMode)
	}
	if got := accountIDs(result.Accounts); !reflectDeepEqualIDs(got, []string{"a-3", "a-1", "a-2"}) {
		t.Fatalf("final order = %#v, want 重排序结果", got)
	}
	if result.HotQualityOrderExplanation != hot.explanation {
		t.Fatalf("explanation 必须取自 ReorderOnly 那次排序")
	}
	if len(*received) != 1 {
		t.Fatalf("events = %d", len(*received))
	}
	summary := (*received)[0].Summary
	if summary.SpeedThresholdMs != cacherateFakeSpeedWindowMs {
		t.Fatalf("speedThresholdMs = %d", summary.SpeedThresholdMs)
	}
	if summary.SpeedBaseEwmaMs == nil || *summary.SpeedBaseEwmaMs != 1200.0 {
		t.Fatalf("speedBaseEwmaMs = %#v", summary.SpeedBaseEwmaMs)
	}
	if !summary.CacheRateEnabled || !summary.CacheRateStale {
		t.Fatalf("cacheRate enabled/stale = %v/%v", summary.CacheRateEnabled, summary.CacheRateStale)
	}
	if len(summary.CandidateOrder) != 3 || summary.CandidateOrder[0].AccountID != "a-3" || !summary.CandidateOrder[0].SpeedQualified {
		t.Fatalf("candidateOrder = %#v", summary.CandidateOrder)
	}
	if summary.HotQualityReorderDegraded {
		t.Fatal("成功重排序不得标记降级")
	}
}

// 重排序失败不阻塞派发：保持等待后亲和序，决策摘要标记降级，解释保留
// 905 那次的（如无则整体缺省）， outcome 仍为 ready。
func TestHighConcurrencyReorderOnlyFailureKeepsOrderAndMarksDegraded(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	hot := &cacherateReorderHotQuality{
		reorderErr:  context.DeadlineExceeded,
		explanation: cacherateExplanation("a-1"),
	}
	engine.HotQuality = hot
	engine.Affinity = &sequencedBusyAffinity{busyByCall: []bool{true, true, true, false}}
	engine.Concurrency = &mapBackedConcurrencyStore{current: map[string]int{"a-1": 0, "a-2": 0}}
	engine.ClientIPConcurrency = &configurableClientIPConcurrency{enabled: true, acquired: true}
	engine.HighConcurrencyQueue = &configurableQueue{ready: true}
	received := w1bCaptureTerminalDecision(t)

	input := cacherateHighConcurrencyInput(t, testAccounts("a-1", "a-2"))
	result, err := pipeline.PrepareOpenAIGatewayDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != PreparationOutcomeReady {
		t.Fatalf("outcome = %s (reason %q)", result.Outcome, result.Reason)
	}
	if !result.HotQualityReorderDegraded {
		t.Fatal("重排序失败必须标记降级")
	}
	// 失败臂保持等待后亲和序（fakeAffinity 透传 = 刷新后的输入序）。
	if got := accountIDs(result.Accounts); !reflectDeepEqualIDs(got, []string{"a-1", "a-2"}) {
		t.Fatalf("final order = %#v, want 等待后亲和序", got)
	}
	// 解释保留 905 那次（OrderAsync 解释），摘要缓存率块仍有真实来源。
	if result.HotQualityOrderExplanation != hot.explanation {
		t.Fatalf("失败臂解释应保留 905 那次排序")
	}
	summary := (*received)[0].Summary
	if !summary.HotQualityReorderDegraded {
		t.Fatal("summary 必须携带 hotQualityReorderDegraded")
	}
	if summary.SpeedThresholdMs != cacherateFakeSpeedWindowMs {
		t.Fatalf("speedThresholdMs = %d", summary.SpeedThresholdMs)
	}
}

// 媒体车道豁免：高并发等待后不重排序（与 905 闭包同语义），无降级标记。
func TestHighConcurrencyReorderSkipsMediaLane(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	hot := &cacherateReorderHotQuality{}
	engine.HotQuality = hot
	engine.Affinity = &sequencedBusyAffinity{busyByCall: []bool{true, true, true, false}}
	engine.Concurrency = &mapBackedConcurrencyStore{current: map[string]int{"a-1": 0}}
	engine.ClientIPConcurrency = &configurableClientIPConcurrency{enabled: true, acquired: true}
	engine.HighConcurrencyQueue = &configurableQueue{ready: true}

	input := cacherateHighConcurrencyInput(t, testAccounts("a-1"))
	input.RequestLane = "audio"
	result, err := pipeline.PrepareOpenAIGatewayDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != PreparationOutcomeReady {
		t.Fatalf("outcome = %s", result.Outcome)
	}
	if hot.reorderCalls.Load() != 0 {
		t.Fatalf("媒体车道不得触发重排序, got %d", hot.reorderCalls.Load())
	}
	if result.HotQualityReorderDegraded {
		t.Fatal("媒体车道豁免不是降级")
	}
}

// 非高并发：最终生效排序 = applyHotQualityOrder 那次 OrderAsync，解释随
// PreparationResult 带出并进摘要；不调用 ReorderOnly。
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
	if hot.reorderCalls.Load() != 0 {
		t.Fatalf("非高并发不得调用 ReorderOnly, got %d", hot.reorderCalls.Load())
	}
	if result.HotQualityOrderExplanation != hot.explanation {
		t.Fatal("解释必须来自 OrderAsync 那次排序")
	}
	summary := (*received)[0].Summary
	if summary.SpeedThresholdMs != cacherateFakeSpeedWindowMs || len(summary.CandidateOrder) != 2 {
		t.Fatalf("summary cache-rate block = %#v/%#v", summary.SpeedThresholdMs, summary.CandidateOrder)
	}
	if summary.HotQualityReorderDegraded {
		t.Fatal("非高并发无重排序降级")
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
		Eligible:                  testAccounts("a-1"),
		HotQualityExplanation:     cacherateExplanation("a-2", "a-1"),
		HotQualityReorderDegraded: true,
	}
	input.HotQualityExplanation.CandidateOrderDetails = details
	summary := BuildDispatchDecisionSummary(input)
	if summary.SpeedThresholdMs != cacherateFakeSpeedWindowMs {
		t.Fatalf("speedThresholdMs = %d", summary.SpeedThresholdMs)
	}
	if !summary.CandidateOrderTruncated || len(summary.CandidateOrder) != dispatchDecisionSummaryListCap {
		t.Fatalf("candidateOrder truncated/len = %v/%d", summary.CandidateOrderTruncated, len(summary.CandidateOrder))
	}
	if !summary.HotQualityReorderDegraded {
		t.Fatal("reorder degraded flag lost")
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
	if decoded["hotQualityReorderDegraded"] != true {
		t.Fatalf("decoded degraded = %#v", decoded)
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
	for _, absent := range []string{"speedBaseEwmaMs", "speedThresholdMs", "cacheRateStale", "cacheRateEnabled", "candidateOrder", "hotQualityReorderDegraded"} {
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
