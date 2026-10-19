package gatewaydispatch

// T10 对话与非对话媒体调度分离落地：候选准备管道的调度域切断回归
// （对话与非对话媒体调度分离设计 §3.2/§5）。全部纯内存 fake，Mock 优先、
// 结果稳定可回放：
//
//	图像纳入非对话域：热质量排序对 image lane 不再触达（OrderAsync = 0）。
//	来源级失败避让：媒体创建域不消费 ClientSourceAvoidance 排序。
//	本地恢复等待：媒体创建域不进入 ResolveLocalSuppressionFilter 等待解析
//	（硬门过滤保留，快速失败）；文本对话域照常进入。

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// t10WriteBackAffinityPort 记录写回调用的最小端口 stub。
type t10WriteBackAffinityPort struct {
	SessionAffinityPort
	GroupAffinityPort
}

func (t10WriteBackAffinityPort) RememberApiKeyFingerprintAsync(context.Context, string, string, string) {
}
func (t10WriteBackAffinityPort) RememberGroupAsync(context.Context, string, string, string) {}

// TestT10EmptyAffinityKeyDisablesSuccessWriteBack：媒体域 preflight 清空亲和
// 键后，引擎成功侧写回闭包按空键契约返回 nil（链面 nil 检查跳过）——成功后
// 没有会话/组亲和写回（设计 §8 验收：成功后也没有亲和写回）。
func TestT10EmptyAffinityKeyDisablesSuccessWriteBack(t *testing.T) {
	fingerprint := "fp_key_1"
	account := AccountCandidate{ID: "acc_1", SelectedAPIKeyFingerprint: &fingerprint}
	port := t10WriteBackAffinityPort{}
	if got := newConfirmSessionApiKeyFingerprintFunc(port, context.Background(), "", account); got != nil {
		t.Fatal("空会话键不得构造 Key 指纹写回闭包")
	}
	if got := newConfirmGroupAffinityFunc(port, context.Background(), "", "strategy_1", "grp_1"); got != nil {
		t.Fatal("空组亲和键不得构造组绑定写回闭包")
	}
	if got := newConfirmSessionApiKeyFingerprintFunc(port, context.Background(), "aff-key", account); got == nil {
		t.Fatal("非空键对照组必须构造写回闭包（对话域行为不变）")
	}
	if got := newConfirmGroupAffinityFunc(port, context.Background(), "gak", "strategy_1", "grp_1"); got == nil {
		t.Fatal("非空键对照组必须构造写回闭包（对话域行为不变）")
	}
}

// t10CountingSourceAvoidance 记录 OrderAsync 调用次数（透传输入序，结果稳定）。
type t10CountingSourceAvoidance struct {
	calls atomic.Int32
}

func (m *t10CountingSourceAvoidance) OrderAsync(_ context.Context, accounts []AccountCandidate, _ gatewaypreauth.ClientStrategyContext, _ *ModelPriority) (AvoidanceOrder, error) {
	m.calls.Add(1)
	return AvoidanceOrder{Accounts: accounts}, nil
}

// t10PartialSuppression 首趟过滤抑制首个候选（部分抑制硬门事实），并记录
// ResolveLocalSuppressionFilter（本地恢复等待解析）是否被触达。
type t10PartialSuppression struct {
	resolveCalls atomic.Int32
}

func (f *t10PartialSuppression) FilterAsync(_ context.Context, accounts []AccountCandidate, _ SuppressionFilterOptions) (SuppressionFilterResult, error) {
	if len(accounts) == 0 {
		return SuppressionFilterResult{AcquiredHalfOpenLeases: []HalfOpenLease{}}, nil
	}
	return SuppressionFilterResult{
		Accounts:               accounts[1:],
		AllSuppressed:          false,
		SuppressedCount:        1,
		SuppressedAccountIDs:   []string{accounts[0].ID},
		AcquiredHalfOpenLeases: []HalfOpenLease{},
	}, nil
}

func (f *t10PartialSuppression) ResolveLocalSuppressionFilter(_ context.Context, input LocalSuppressionPreflightInput) (*SuppressionFilterResult, bool, error) {
	f.resolveCalls.Add(1)
	result := localSuppressionBypassResult(input.Accounts)
	return &result, false, nil
}

// TestT10NonConversationLanesSkipHotQualityOrder：图像创建纳入
// StatelessMediaDispatch 域后，候选准备不触达热质量排序（audio/video 既有
// 豁免的 image 同语义扩展；设计 §7.4 移除图像创建对文本热质量的依赖）。
func TestT10NonConversationLanesSkipHotQualityOrder(t *testing.T) {
	for _, lane := range []string{"image", "audio", "video"} {
		t.Run(lane, func(t *testing.T) {
			pipeline, engine, _, _ := newPipeline(t)
			hot := &cacherateReorderHotQuality{}
			engine.HotQuality = hot

			input := dispatchPreparationInput(t, testAccounts("a-1", "a-2"))
			input.RequestLane = lane
			result, err := pipeline.PrepareOpenAIGatewayDispatchAccounts(context.Background(), input)
			if err != nil {
				t.Fatalf("PrepareDispatchAccounts: %v", err)
			}
			if result.Outcome != PreparationOutcomeReady {
				t.Fatalf("outcome = %s (reason %q)", result.Outcome, result.Reason)
			}
			if calls := hot.orderAsyncCalls.Load(); calls != 0 {
				t.Fatalf("lane %q 热质量排序调用 = %d, want 0（非对话域不进入热质量排序）", lane, calls)
			}
			if result.HotQualityOrderExplanation != nil {
				t.Fatalf("lane %q 不得携带热质量排序解释", lane)
			}
		})
	}

	// 对照组：text lane 排序照常触达（对话域行为不变）。
	pipeline, engine, _, _ := newPipeline(t)
	hot := &cacherateReorderHotQuality{}
	engine.HotQuality = hot
	input := dispatchPreparationInput(t, testAccounts("a-1", "a-2"))
	if _, err := pipeline.PrepareOpenAIGatewayDispatchAccounts(context.Background(), input); err != nil {
		t.Fatalf("text 对照组: %v", err)
	}
	if calls := hot.orderAsyncCalls.Load(); calls == 0 {
		t.Fatal("text lane 对照组必须触达热质量排序（对话域行为不变）")
	}
}

// TestT10MediaLanesSkipClientSourceAvoidanceOrdering：来源级失败避让是文本
// 对话调度维度，媒体创建域候选准备不消费其排序（设计 §3.2）。
func TestT10MediaLanesSkipClientSourceAvoidanceOrdering(t *testing.T) {
	for _, lane := range []string{"image", "audio", "video"} {
		t.Run(lane, func(t *testing.T) {
			pipeline, engine, _, _ := newPipeline(t)
			source := &t10CountingSourceAvoidance{}
			engine.ClientSourceAvoidance = source

			input := dispatchPreparationInput(t, testAccounts("a-1"))
			input.RequestLane = lane
			result, err := pipeline.PrepareOpenAIGatewayDispatchAccounts(context.Background(), input)
			if err != nil {
				t.Fatalf("PrepareDispatchAccounts: %v", err)
			}
			if result.Outcome != PreparationOutcomeReady {
				t.Fatalf("outcome = %s", result.Outcome)
			}
			if calls := source.calls.Load(); calls != 0 {
				t.Fatalf("lane %q 来源避让排序调用 = %d, want 0", lane, calls)
			}
		})
	}

	// 对照组：text lane 照常消费。
	pipeline, engine, _, _ := newPipeline(t)
	source := &t10CountingSourceAvoidance{}
	engine.ClientSourceAvoidance = source
	input := dispatchPreparationInput(t, testAccounts("a-1"))
	if _, err := pipeline.PrepareOpenAIGatewayDispatchAccounts(context.Background(), input); err != nil {
		t.Fatalf("text 对照组: %v", err)
	}
	if calls := source.calls.Load(); calls == 0 {
		t.Fatal("text lane 对照组必须消费来源避让排序")
	}
}

// TestT10MediaLanesSkipLocalSuppressionRecoverableWait：候选存在本地抑制时，
// 媒体创建域不进入本地恢复等待解析（不等待账户恢复，设计 §3.2/§4.1），硬门
// 过滤结果保留（被抑制候选保持排除）；文本对话域照常进入等待解析。
func TestT10MediaLanesSkipLocalSuppressionRecoverableWait(t *testing.T) {
	for _, lane := range []string{"image", "audio", "video"} {
		t.Run(lane, func(t *testing.T) {
			pipeline, engine, _, _ := newPipeline(t)
			suppression := &t10PartialSuppression{}
			engine.Suppression = suppression

			input := dispatchPreparationInput(t, testAccounts("a-1", "a-2"))
			input.RequestLane = lane
			result, err := pipeline.PrepareOpenAIGatewayDispatchAccounts(context.Background(), input)
			if err != nil {
				t.Fatalf("PrepareDispatchAccounts: %v", err)
			}
			if result.Outcome != PreparationOutcomeReady {
				t.Fatalf("lane %q outcome = %s (reason %q)", lane, result.Outcome, result.Reason)
			}
			if calls := suppression.resolveCalls.Load(); calls != 0 {
				t.Fatalf("lane %q 本地抑制等待解析调用 = %d, want 0（不等待账户恢复）", lane, calls)
			}
			if got := accountIDs(result.Accounts); len(got) != 1 || got[0] != "a-2" {
				t.Fatalf("lane %q 硬门过滤结果必须保留: %v", lane, got)
			}
		})
	}

	// 对照组：text lane 进入等待解析（既有行为不变）。
	pipeline, engine, _, _ := newPipeline(t)
	suppression := &t10PartialSuppression{}
	engine.Suppression = suppression
	input := dispatchPreparationInput(t, testAccounts("a-1", "a-2"))
	if _, err := pipeline.PrepareOpenAIGatewayDispatchAccounts(context.Background(), input); err != nil {
		t.Fatalf("text 对照组: %v", err)
	}
	if calls := suppression.resolveCalls.Load(); calls == 0 {
		t.Fatal("text lane 对照组必须进入本地抑制等待解析（既有行为不变）")
	}
}
