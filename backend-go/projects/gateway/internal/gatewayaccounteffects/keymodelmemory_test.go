package gatewayaccounteffects

import (
	"context"
	"sync"
	"testing"
	"time"
)

func memoryStoreForTest(t *testing.T) (*InMemoryKeyModelRuntimeStore, *FakeClock) {
	t.Helper()
	clock := NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	return NewInMemoryKeyModelRuntimeStore(clock), clock
}

func memoryIntent(capability CapabilityKey, intentID string, nowMs int64) KeyModelFailureIntent {
	return KeyModelFailureIntent{
		IntentID:     intentID,
		RequestID:    "req-1",
		AttemptID:    "attempt-1",
		Capability:   capability,
		ObservedAtMs: nowMs,
		Outcome:      KeyModelOutcomeUpstreamNotComplete,
		SourceFence:  "fence",
	}
}

func TestMemoryStoreRecordFailureLifecycle(t *testing.T) {
	store, clock := memoryStoreForTest(t)
	ctx := context.Background()
	capability := testCapability()
	now := NowMs(clock)

	// 首次写入：applied + OPEN + retry 5s。
	result, err := store.RecordFailure(ctx, memoryIntent(capability, "intent-1", now))
	if err != nil || result.Status != KeyModelMutationApplied || result.State.Phase != KeyModelPhaseOpen || *result.State.RetryAtMs != now+5_000 {
		t.Fatalf("first record = %+v err = %v", result, err)
	}
	if result.State.Generation != 1 {
		t.Fatalf("generation = %d", result.State.Generation)
	}

	// 相同 revision 且非 CLOSED：idempotent（同一 receipt intent）。
	replay, err := store.RecordFailure(ctx, memoryIntent(capability, "intent-1", now))
	if err != nil || replay.Status != KeyModelMutationIdempotent || replay.State.CapabilityHash != result.State.CapabilityHash {
		t.Fatalf("replay = %+v err = %v", replay, err)
	}

	// 新 intentId、同 revision 非 CLOSED：idempotent 更新观测。
	updated, err := store.RecordFailure(ctx, memoryIntent(capability, "intent-2", now+1_000))
	if err != nil || updated.Status != KeyModelMutationIdempotent || updated.State.LastObservedAtMs != now+1_000 {
		t.Fatalf("update = %+v err = %v", updated, err)
	}

	// 更高 revision 的 canonical hash 不同：按 hash 键控语义生成独立条目
	//（与 Node 一致，generation 重置为 1）。
	nextRevision := testCapability()
	nextRevision.DispatchRevision = 4
	reopened, err := store.RecordFailure(ctx, memoryIntent(nextRevision, "intent-3", now+2_000))
	if err != nil || reopened.Status != KeyModelMutationApplied || reopened.State.Generation != 1 || reopened.State.DispatchRevision != 4 {
		t.Fatalf("reopen = %+v err = %v", reopened, err)
	}

	// 旧 revision 条目仍按 hash 键控独立存在：同 revision 非 CLOSED → idempotent。
	stale, err := store.RecordFailure(ctx, memoryIntent(capability, "intent-4", now+3_000))
	if err != nil || stale.Status != KeyModelMutationIdempotent {
		t.Fatalf("old revision replay = %+v err = %v", stale, err)
	}

	// 非法 intent。
	badOutcome := memoryIntent(capability, "intent-5", now)
	badOutcome.Outcome = KeyModelOutcomeCompleteSuccess
	if _, err := store.RecordFailure(ctx, badOutcome); err == nil || err.Error() != "Key-model 失败意图 outcome 只能为 upstream_not_complete" {
		t.Fatalf("outcome err = %v", err)
	}
	zeroTime := memoryIntent(capability, "intent-6", 0)
	if _, err := store.RecordFailure(ctx, zeroTime); err == nil || err.Error() != "Key-model observedAtMs 无效" {
		t.Fatalf("observedAt err = %v", err)
	}
}

func TestMemoryStoreForegroundAdmissionAndPermits(t *testing.T) {
	store, clock := memoryStoreForTest(t)
	ctx := context.Background()
	capability := testCapability()
	if _, err := store.RecordFailure(ctx, memoryIntent(capability, "intent-1", NowMs(clock))); err != nil {
		t.Fatal(err)
	}

	// 非 CLOSED：blocked。
	blocked, err := store.AdmitForeground(ctx, capability, "attempt-1")
	if err != nil || blocked.Status != ForegroundBlocked {
		t.Fatalf("blocked = %+v err = %v", blocked, err)
	}

	// 成功 settle 到 CLOSED 后才可准入。
	state, err := store.Get(ctx, capability)
	if err != nil || state == nil {
		t.Fatal("state should exist")
	}
	status, closed := SettleKeyModelRecovery(KeyModelState{CapabilityKey: capability, CapabilityHash: state.CapabilityHash, Generation: 1, Phase: KeyModelPhaseHalfOpen, BackoffAttempt: 1, RecoverySuccessCount: 2, LastRecoverySuccessAtMs: int64Ptr(0)}, SettleKeyModelRecoveryInput{Generation: 1, DispatchRevision: 3, LeaseID: "lease", Outcome: KeyModelOutcomeCompleteSuccess, NowMs: 1})
	_ = status
	// 直接把状态迁移为 CLOSED（模拟恢复闭环后的驻留）。
	closed.Phase = KeyModelPhaseClosed
	store.mu.Lock()
	store.states[state.CapabilityHash] = closed
	store.closedUntil[state.CapabilityHash] = NowMs(clock) + keyModelClosedRetentionMs
	store.mu.Unlock()

	first, err := store.AdmitForeground(ctx, capability, "attempt-1")
	if err != nil || first.Status != ForegroundAdmitted || first.Permit == nil {
		t.Fatalf("first admit = %+v err = %v", first, err)
	}
	// 同 attemptId 幂等。
	replay, err := store.AdmitForeground(ctx, capability, "attempt-1")
	if err != nil || replay.Status != ForegroundAdmitted || replay.Permit.LeaseUntilMs != first.Permit.LeaseUntilMs {
		t.Fatalf("replay admit = %+v err = %v", replay, err)
	}
	// 第二个 permit：admitted（上限 2）。
	second, err := store.AdmitForeground(ctx, capability, "attempt-2")
	if err != nil || second.Status != ForegroundAdmitted {
		t.Fatalf("second admit = %+v err = %v", second, err)
	}
	// 第三个：busy。
	busy, err := store.AdmitForeground(ctx, capability, "attempt-3")
	if err != nil || busy.Status != ForegroundBusy {
		t.Fatalf("busy = %+v err = %v", busy, err)
	}
	// 释放后可再准入，wake 序号递增。
	if released, err := store.ReleaseForeground(ctx, *first.Permit); err != nil || !released {
		t.Fatalf("release = %v err = %v", released, err)
	}
	after, err := store.AdmitForeground(ctx, capability, "attempt-3")
	if err != nil || after.Status != ForegroundAdmitted {
		t.Fatalf("after release admit = %+v err = %v", after, err)
	}
	// 两个 permit 都占满后：busy 返回唤醒序号（release 已递增）。
	third, err := store.AdmitForeground(ctx, capability, "attempt-4")
	if err != nil || third.Status != ForegroundBusy || third.WakeSequence < 1 {
		t.Fatalf("busy wake = %+v err = %v", third, err)
	}
	// 续租。
	renewed, err := store.RenewForeground(ctx, *after.Permit)
	if err != nil || renewed == nil || renewed.LeaseUntilMs < after.Permit.LeaseUntilMs {
		t.Fatalf("renew = %+v err = %v", renewed, err)
	}
	// 丢失租约（未知 attemptId）。
	lost, err := store.RenewForeground(ctx, KeyModelForegroundPermit{CapabilityHash: capability.KeyFingerprint, AttemptID: "ghost"})
	if err == nil {
		t.Fatal("invalid hash must error")
	}
	if lost != nil {
		t.Fatal("ghost renew must not renew")
	}
}

func TestMemoryStoreMainProbeFenceAndJ1(t *testing.T) {
	store, clock := memoryStoreForTest(t)
	ctx := context.Background()
	capability := testCapability()
	hash, err := CapabilityHash(capability)
	if err != nil {
		t.Fatal(err)
	}
	permit := KeyModelForegroundPermit{CapabilityHash: hash, AttemptID: "main-1", LeaseUntilMs: NowMs(clock) + 1_000}

	if err := store.RecordMainProbeFailure(ctx, capability, permit); err != nil {
		t.Fatal(err)
	}
	// 主探针 fence 挡住 admission。
	blocked, err := store.AdmitForeground(ctx, capability, "attempt-9")
	if err != nil || blocked.Status != ForegroundBlocked {
		t.Fatalf("fence blocked = %+v err = %v", blocked, err)
	}
	// 指纹不一致的清理被拒绝。
	cleared, err := store.ClearMainProbeFence(ctx, KeyModelFenceReference{CapabilityHash: hash, KeyFingerprint: "other", DispatchRevision: 3, OwnerID: "main-1"}, "fp-1")
	if err != nil || cleared {
		t.Fatalf("fingerprint mismatch clear = %v err = %v", cleared, err)
	}
	// owner 匹配清理成功。
	cleared, err = store.ClearMainProbeFence(ctx, KeyModelFenceReference{CapabilityHash: hash, KeyFingerprint: "fp-1", DispatchRevision: 3, OwnerID: "main-1"}, "fp-1")
	if err != nil || !cleared {
		t.Fatalf("clear = %v err = %v", cleared, err)
	}
	// defer：owner 不匹配拒绝，匹配则延长。
	ok, err := store.DeferMainProbeFence(ctx, KeyModelFenceReference{CapabilityHash: hash, KeyFingerprint: "fp-1", DispatchRevision: 3, OwnerID: "ghost"})
	if err != nil || ok {
		t.Fatalf("ghost defer = %v err = %v", ok, err)
	}
	if err := store.RecordMainProbeFailure(ctx, capability, permit); err != nil {
		t.Fatal(err)
	}
	deferred, err := store.DeferMainProbeFence(ctx, KeyModelFenceReference{CapabilityHash: hash, KeyFingerprint: "fp-1", DispatchRevision: 3, OwnerID: "main-1"})
	if err != nil || !deferred {
		t.Fatalf("defer = %v err = %v", deferred, err)
	}
	// J1 限频：首次成功，窗口内重复被拒。
	claimed, err := store.ClaimJ1Confirmation(ctx, "source-1", 3)
	if err != nil || !claimed {
		t.Fatalf("claim = %v err = %v", claimed, err)
	}
	again, err := store.ClaimJ1Confirmation(ctx, "source-1", 3)
	if err != nil || again {
		t.Fatalf("second claim = %v err = %v", again, err)
	}
	clock.Advance(3 * time.Minute)
	reclaimed, err := store.ClaimJ1Confirmation(ctx, "source-1", 3)
	if err != nil || !reclaimed {
		t.Fatalf("reclaim after window = %v err = %v", reclaimed, err)
	}
}

func TestMemoryStoreRecoverySweepSupport(t *testing.T) {
	store, clock := memoryStoreForTest(t)
	ctx := context.Background()
	capability := testCapability()
	now := NowMs(clock)
	intent := memoryIntent(capability, "intent-1", now)
	target := &KeyModelRecoveryTarget{AccountID: "source-1", GroupID: "grp-1", SystemAccountID: "sys-1"}
	intent.RecoveryTarget = target
	if _, err := store.RecordFailure(ctx, intent); err != nil {
		t.Fatal(err)
	}

	// 未到期：listDue 为空。
	due, err := store.ListDue(now, 128)
	if err != nil || len(due) != 0 {
		t.Fatalf("early due = %d err = %v", len(due), err)
	}
	// 到期后可见，且 target 命中。
	due, err = store.ListDue(now+6_000, 128)
	if err != nil || len(due) != 1 {
		t.Fatalf("due = %d err = %v", len(due), err)
	}
	recovered := store.GetRecoveryTarget(capability)
	if recovered == nil || recovered.GroupID != "grp-1" {
		t.Fatalf("target = %+v", recovered)
	}
	// 抢占租约 → HALF_OPEN。
	status, leased := store.AcquireRecoveryLease(MemoryRecoveryLeaseInput{Capability: capability, Generation: 1, DispatchRevision: 3, LeaseID: "lease-1", NowMs: now + 6_500})
	if status != KeyModelMutationApplied || leased.Phase != KeyModelPhaseHalfOpen {
		t.Fatalf("acquire = %s %s", status, leased.Phase)
	}
	// 续租。
	if !store.RenewRecoveryLease(MemoryRecoveryRenewInput{CapabilityHash: leased.CapabilityHash, Generation: 1, DispatchRevision: 3, LeaseID: "lease-1", NowMs: now + 30_000}) {
		t.Fatal("renew should succeed within the lease")
	}
	// 非租约持有人续租失败。
	if store.RenewRecoveryLease(MemoryRecoveryRenewInput{CapabilityHash: leased.CapabilityHash, Generation: 1, DispatchRevision: 3, LeaseID: "other", NowMs: now + 30_000}) {
		t.Fatal("foreign renew must fail")
	}
	// settle 第一次成功 → RECOVERING。
	status, settled := store.SettleRecovery(MemoryRecoverySettleInput{Capability: capability, Generation: 1, DispatchRevision: 3, LeaseID: "lease-1", Outcome: KeyModelOutcomeCompleteSuccess, NowMs: now + 30_500})
	if status != KeyModelMutationApplied || settled.Phase != KeyModelPhaseRecovering || settled.RecoverySuccessCount != 1 {
		t.Fatalf("settle = %s %s %d", status, settled.Phase, settled.RecoverySuccessCount)
	}
	// 连续第三次成功 → CLOSED + 5min 驻留。
	for index := 0; index < 2; index++ {
		at := now + 41_000 + int64(index)*11_000
		s, _ := store.AcquireRecoveryLease(MemoryRecoveryLeaseInput{Capability: capability, Generation: 1, DispatchRevision: 3, LeaseID: "lease-x", NowMs: at})
		if s != KeyModelMutationApplied {
			t.Fatalf("cycle %d acquire = %s", index, s)
		}
		status, _ = store.SettleRecovery(MemoryRecoverySettleInput{Capability: capability, Generation: 1, DispatchRevision: 3, LeaseID: "lease-x", Outcome: KeyModelOutcomeCompleteSuccess, NowMs: at + 500})
		if status != KeyModelMutationApplied {
			t.Fatalf("cycle %d settle = %s", index, status)
		}
	}
	closedState, err := store.Get(ctx, capability)
	if err != nil || closedState == nil || closedState.Phase != KeyModelPhaseClosed {
		t.Fatalf("closed state = %+v err = %v", closedState, err)
	}
	// 驻留窗口内仍在容量里，窗口外被清理。
	clock.Set(time.UnixMilli(now + keyModelClosedRetentionMs + 60_000))
	store.SettleRecovery(MemoryRecoverySettleInput{Capability: capability, Generation: 1, DispatchRevision: 3, LeaseID: "noop", Outcome: KeyModelOutcomeUnknown, NowMs: now + keyModelClosedRetentionMs + 60_000})
	gone, err := store.Get(ctx, capability)
	if err != nil || gone != nil {
		t.Fatalf("state after retention = %+v err = %v", gone, err)
	}
	if store.GetRecoveryTarget(capability) != nil {
		t.Fatal("recovery target should be dropped with the state")
	}
}

func TestMemoryStoreConcurrentRecordAndAdmit(t *testing.T) {
	store, clock := memoryStoreForTest(t)
	ctx := context.Background()
	capability := testCapability()
	var wg sync.WaitGroup
	for index := 0; index < 24; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			intent := memoryIntent(capability, "intent", NowMs(clock))
			intent.AttemptID = "attempt"
			if _, err := store.RecordFailure(ctx, intent); err != nil {
				t.Error(err)
			}
			store.AdmitForeground(ctx, capability, "attempt")
			store.ListDue(NowMs(clock)+10_000, 128)
		}(index)
	}
	wg.Wait()
}
