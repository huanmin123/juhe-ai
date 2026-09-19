package gatewaycircuit

// w14e 覆盖率补强（第五批）：降级计数推进臂、探针加入/替换错误臂、
// due 计时辅助与确认释放重放。

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// TestW14ESuppressionDegradeCountAdvanceArms 已随 DegradeForGatewayFailure
// 写面退场删除（生产写面退场，见 suppression.go 顶部注记）。

func TestW14EPrecheckSummaryContextFallback(t *testing.T) {
	// 授权账户缺绑定但上下文带系统账户：回退到上下文。
	authorized := gatewayruntimecache.OpenAIAccountSecret{ID: "w14e-auth", AccountAccessType: "account_authorized"}
	got, err := gatewayAccountSummarySystemAccountID(authorized, PrecheckSummaryContext{SystemAccountID: "ctx-sys"})
	if err != nil || got != "ctx-sys" {
		t.Fatalf("context fallback = (%q, %v)", got, err)
	}
	// 普通账户缺系统账户但上下文带系统账户：回退到上下文。
	plain := gatewayruntimecache.OpenAIAccountSecret{ID: "w14e-plain"}
	got, err = gatewayAccountSummarySystemAccountID(plain, PrecheckSummaryContext{SystemAccountID: "ctx-sys-2"})
	if err != nil || got != "ctx-sys-2" {
		t.Fatalf("plain fallback = (%q, %v)", got, err)
	}
}

func TestW14EProbeJoinAfterLostRaceWithoutState(t *testing.T) {
	ctx := context.Background()
	store := &w14eVanishingProbeStore{mockProbeStore: newMockProbeStore()}
	coordinator := NewProbeCoordinator(store, func() int64 { return 1_000 }, func() string { return "w14e-owner" })
	result, err := coordinator.Acquire(ctx, ProbeAcquireInput{AccountRuntimeScope: "w14e-vanish"})
	if err != nil || result.Disposition != ProbeDispositionJoined {
		t.Fatalf("join = (%s, %v)", result.Disposition, err)
	}
	if result.Generation != 0 {
		t.Fatalf("generation = %d", result.Generation)
	}
}

// w14eVanishingProbeStore 模拟竞态丢失且读回为空的场景。
type w14eVanishingProbeStore struct {
	*mockProbeStore
}

func (m *w14eVanishingProbeStore) SetIfAbsent(ctx context.Context, state ProbeState, retention int64) (bool, error) {
	return false, nil
}

func (m *w14eVanishingProbeStore) Get(ctx context.Context, runtimeKey string) (*ProbeState, error) {
	return nil, nil
}

func TestW14EProbeReplaceSettledNextGenerationError(t *testing.T) {
	ctx := context.Background()
	store := &w11cFailingProbeStore{mockProbeStore: newMockProbeStore()}
	coordinator := NewProbeCoordinator(store, func() int64 { return 1_000 }, func() string { return "w14e-owner" })
	fence := testFence("w14e-replace-err")
	acquired, err := coordinator.Acquire(ctx, ProbeAcquireInput{
		AccountRuntimeScope: "w14e-probe-err", SourceFence: &fence, ExecutionRole: "source_dispatch",
	})
	if err != nil || acquired.Disposition != ProbeDispositionOwner {
		t.Fatalf("acquire = (%s, %v)", acquired.Disposition, err)
	}
	if _, err := coordinator.Settle(ctx, SettleProbeInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, OwnerToken: acquired.OwnerToken,
		Outcome: ProbeOutcomeSuccess, NowMs: int64Ptr(2_000),
	}); err != nil {
		t.Fatalf("settle: %v", err)
	}
	// 替换已结算代时 NextGeneration 失败必须向上传播。
	store.failNextGen = true
	nextFence := testFence("w14e-replace-err-2")
	if _, err := coordinator.Acquire(ctx, ProbeAcquireInput{
		AccountRuntimeScope: "w14e-probe-err", SourceFence: &nextFence, ExecutionRole: "source_dispatch",
		NowMs: int64Ptr(3_000),
	}); err == nil {
		t.Fatal("next generation failure must propagate")
	}
}

func TestW14EAccountCircuitDueAtMsArms(t *testing.T) {
	if got := accountCircuitDueAtMs(State{Phase: PhaseSuspect}); got != maxInt64W14E() {
		t.Fatalf("suspect without retry = %d", got)
	}
	if got := accountCircuitDueAtMs(State{Phase: PhaseClosed}); got != maxInt64W14E() {
		t.Fatalf("closed due = %d", got)
	}
	if got := accountCircuitDueAtMs(State{Phase: PhaseSuspect, Lease: &Lease{LeaseUntilMs: 42}}); got != 42 {
		t.Fatalf("leased due = %d", got)
	}
	if got := accountCircuitDueAtMs(State{Phase: PhaseOpen, RetryAtMs: int64Ptr(7)}); got != 7 {
		t.Fatalf("open due = %d", got)
	}
}

func maxInt64W14E() int64 {
	value := int64(^uint64(0) >> 1)
	return value
}
