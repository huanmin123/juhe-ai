package gatewaycircuit

// circuitaudit_capacity_softblock_test.go — 复现发现 R4（容量满后新 scope 被
// 持久软屏蔽，无自愈）。
// 状态：已知设计限制（fail-closed，Node 同构），本轮不修复，留存为行为基线。
//
// 复现的缺陷：store 容量（gateway 生产装配 Capacity=10_000，
// chain_wiring_w2c.go:254）被非 CLOSED 状态占满后：
//
//   - 写路径：Suspect 对任何新 scope 返回 capacity_exhausted +
//     CapacityExhaustedState（store_memory.go:139-141）；
//   - 读路径：getLocked 对 capacitySaturated 的新 scope 直接合成一条
//     CapacityExhaustedState（store_memory.go:834-840），Phase=SUSPECT、
//     retryAt=now+1000（types.go:423-431）；
//   - service 层把它当作普通 SUSPECT：PrepareAttempt 返回
//     PrepareBlocked（service.go:609-613），候选被静默跳过而不是报错。
//
// 且无自愈：capacitySaturated 只在 len(entries) < capacity 时清零
// （store_memory.go:1310-1312），而 SUSPECT 等非 CLOSED 条目永不过期
// （freshEntryLocked/cleanupLocked 只淘汰过期 CLOSED，store_memory.go:1232、
// 1300-1313）——一旦饱和，所有未存储 scope 永久软屏蔽，进程重启前不可恢复。
//
// 生产触发条件：长期运行的网关进程内累计 1 万个不同 scope（账号 × 模型桶 ×
// lane）的非 CLOSED 熔断条目；此后每个新账号/新模型的所有请求都被静默软屏蔽。

import (
	"context"
	"testing"
)

func TestCircuitAuditR4CapacityExhaustionSoftBlocksNewScopes(t *testing.T) {
	clock := int64(5_000_000)
	now := func() int64 { return clock }
	store, err := NewMemoryStore(MemoryStoreOptions{Capacity: 2, Now: now})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	service, err := NewCircuitService(store, ServiceOptions{Now: now})
	if err != nil {
		t.Fatalf("NewCircuitService: %v", err)
	}
	ctx := context.Background()

	// 两个不同 runtimeKey 各制造一条 SUSPECT，占满 capacity=2。
	for index, key := range []string{"circuit-audit-cap-acct-1", "circuit-audit-cap-acct-2"} {
		result, err := store.Suspect(ctx, SuspectInput{
			Scope:            accountScope(key),
			DispatchRevision: "7",
			TransitionID:     "circuit-audit-cap-" + itoaCircuitAudit(index),
			Reason:           "transport:circuit-audit capacity fixture",
			NowMs:            &clock,
		})
		if err != nil || result.Status != MutationApplied {
			t.Fatalf("fixture Suspect(%s) = (%s, %v)", key, result.Status, err)
		}
	}

	// 第 3 个新 scope：写路径返回 capacity_exhausted + CapacityExhaustedState。
	exhaustedScope := accountScope("circuit-audit-cap-acct-3")
	exhausted, err := store.Suspect(ctx, SuspectInput{
		Scope:            exhaustedScope,
		DispatchRevision: "7",
		TransitionID:     "circuit-audit-cap-3",
		Reason:           "transport:circuit-audit capacity fixture",
		NowMs:            &clock,
	})
	if err != nil {
		t.Fatalf("store.Suspect(第 3 个 scope): %v", err)
	}
	if exhausted.Status != MutationCapacityExhausted {
		t.Fatalf("第 3 个 scope Suspect status = %s, want capacity_exhausted", exhausted.Status)
	}
	assertCapacityExhaustedShapeCircuitAudit(t, exhausted.State, clock)

	// 第 3 个账号经 service.PrepareAttempt：读路径合成的 CapacityExhaustedState
	// 被 service 当作普通 SUSPECT → PrepareBlocked（候选被静默跳过）。
	capAccount := gatewayruntimecacheAccountForCircuitAudit("circuit-audit-cap-acct-3")
	model := "gpt-test"
	prepare, err := service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account:                     capAccount,
		RequestLane:                 LaneText,
		Model:                       &model,
		ConfirmationLeaseDurationMs: 30_000,
	})
	if err != nil {
		t.Fatalf("PrepareAttempt(容量满的新账号): %v", err)
	}
	if prepare.Outcome != PrepareBlocked {
		t.Fatalf("容量满后新账号 PrepareAttempt outcome = %s, want blocked（候选被软屏蔽）", prepare.Outcome)
	}
	if prepare.State == nil {
		t.Fatal("blocked 结果必须携带合成状态")
	}
	assertCapacityExhaustedShapeCircuitAudit(t, *prepare.State, clock)
	if prepare.Attempt != nil {
		t.Fatal("软屏蔽的候选不应产生尝试句柄")
	}

	// 无自愈：把时钟推进 10 分钟（远超 retryAt=+1s 和 CLOSED retention），新
	// scope 依旧被软屏蔽 —— SUSPECT 条目不过期，饱和标志永不解除。
	clock += 10 * 60_000
	freshAccount := gatewayruntimecacheAccountForCircuitAudit("circuit-audit-cap-acct-4")
	laterPrepare, err := service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account:                     freshAccount,
		RequestLane:                 LaneText,
		Model:                       &model,
		ConfirmationLeaseDurationMs: 30_000,
	})
	if err != nil {
		t.Fatalf("PrepareAttempt(10 分钟后的新账号): %v", err)
	}
	if laterPrepare.Outcome != PrepareBlocked {
		t.Fatalf("10 分钟后新账号 outcome = %s, want blocked（饱和无自愈）", laterPrepare.Outcome)
	}
	if laterPrepare.State == nil || laterPrepare.State.Phase != PhaseSuspect {
		t.Fatalf("10 分钟后合成状态 = %+v, want SUSPECT（runtime_state_capacity_exhausted）", laterPrepare.State)
	}
	if laterPrepare.State.FailureReason == nil || *laterPrepare.State.FailureReason != "runtime_state_capacity_exhausted" {
		t.Fatalf("10 分钟后 failureReason = %v, want runtime_state_capacity_exhausted", laterPrepare.State.FailureReason)
	}
}

// assertCapacityExhaustedShapeCircuitAudit 校验 CapacityExhaustedState 的精确
// 形状（types.go:423-431）：Phase=SUSPECT、transitionId=runtime-capacity-
// exhausted、failureReason=runtime_state_capacity_exhausted、retryAt=now+1000。
func assertCapacityExhaustedShapeCircuitAudit(t *testing.T, state State, nowMs int64) {
	t.Helper()
	if state.Phase != PhaseSuspect {
		t.Fatalf("CapacityExhaustedState phase = %s, want SUSPECT", state.Phase)
	}
	if state.TransitionID != "runtime-capacity-exhausted" {
		t.Fatalf("CapacityExhaustedState transitionID = %q, want runtime-capacity-exhausted", state.TransitionID)
	}
	if state.FailureReason == nil || *state.FailureReason != "runtime_state_capacity_exhausted" {
		t.Fatalf("CapacityExhaustedState failureReason = %v, want runtime_state_capacity_exhausted", state.FailureReason)
	}
	if state.RetryAtMs == nil || *state.RetryAtMs != nowMs+1_000 {
		t.Fatalf("CapacityExhaustedState retryAtMs = %v, want %d (+1s)", state.RetryAtMs, nowMs+1_000)
	}
}
