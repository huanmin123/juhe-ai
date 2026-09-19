package gatewayaccounteffects

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// weCountingLogger 统计 Warn 次数，用于断言丢弃/重试等告警路径。
type weCountingLogger struct {
	mu    sync.Mutex
	warns int
}

func (l *weCountingLogger) Info(map[string]any, string) {}

func (l *weCountingLogger) Warn(map[string]any, string) {
	l.mu.Lock()
	l.warns++
	l.mu.Unlock()
}

func (l *weCountingLogger) Error(map[string]any, string) {}

func (l *weCountingLogger) warnCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.warns
}

// ---------------------------------------------------------------------------
// sideeffects.go：队列满员丢弃/成功淘汰、过期与重试路径
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// keymodelmemory.go：意图校验、目标归一化、ListDue 与结算分支
// ---------------------------------------------------------------------------

func TestWeMemoryValidateFailureIntent(t *testing.T) {
	base := memoryIntent(testCapability(), "i-1", 1_000)
	if err := validateFailureIntent(base); err != nil {
		t.Fatalf("合法意图不应报错: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*KeyModelFailureIntent)
	}{
		{name: "空 intentId", mutate: func(i *KeyModelFailureIntent) { i.IntentID = " " }},
		{name: "空 requestId", mutate: func(i *KeyModelFailureIntent) { i.RequestID = "" }},
		{name: "空 attemptId", mutate: func(i *KeyModelFailureIntent) { i.AttemptID = "" }},
		{name: "空 sourceFence", mutate: func(i *KeyModelFailureIntent) { i.SourceFence = "" }},
		{name: "非法 outcome", mutate: func(i *KeyModelFailureIntent) { i.Outcome = KeyModelOutcomeCompleteSuccess }},
		{name: "observedAtMs 为 0", mutate: func(i *KeyModelFailureIntent) { i.ObservedAtMs = 0 }},
		{name: "observedAtMs 越界", mutate: func(i *KeyModelFailureIntent) { i.ObservedAtMs = safeIntegerMax + 1 }},
		{name: "非法 capability", mutate: func(i *KeyModelFailureIntent) { i.Capability.ClientModel = " " }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := base
			tt.mutate(&intent)
			if err := validateFailureIntent(intent); err == nil {
				t.Fatal("非法意图应报错")
			}
		})
	}
}

func TestWeMemoryNormalizeRecoveryTarget(t *testing.T) {
	if _, err := normalizeRecoveryTarget(KeyModelRecoveryTarget{}); err == nil {
		t.Fatal("空目标应报错")
	}
	if _, err := normalizeRecoveryTarget(KeyModelRecoveryTarget{AccountID: "a"}); err == nil {
		t.Fatal("缺 groupId 应报错")
	}
	target, err := normalizeRecoveryTarget(KeyModelRecoveryTarget{AccountID: " a ", GroupID: " g ", SystemAccountID: " s "})
	if err != nil || target.AccountID != "a" || target.GroupID != "g" || target.SystemAccountID != "s" {
		t.Fatalf("target = %+v err = %v", target, err)
	}
}

func TestWeMemoryListDueValidationAndOrdering(t *testing.T) {
	store := NewInMemoryKeyModelRuntimeStore(nil)
	if _, err := store.ListDue(0, 10); err == nil {
		t.Fatal("nowMs=0 应报错")
	}
	if _, err := store.ListDue(safeIntegerMax+1, 10); err == nil {
		t.Fatal("nowMs 越界应报错")
	}

	// 白盒播种：Recovering（优先）与 RetryAt 为 nil 的 Open（视为无穷大排后）。
	store.mu.Lock()
	store.states["recovering"] = KeyModelState{CapabilityHash: "recovering", Phase: KeyModelPhaseRecovering, RetryAtMs: nil}
	store.states["open-nil"] = KeyModelState{CapabilityHash: "open-nil", Phase: KeyModelPhaseOpen, RetryAtMs: nil}
	dueAt := int64(500)
	store.states["open-due"] = KeyModelState{CapabilityHash: "open-due", Phase: KeyModelPhaseOpen, RetryAtMs: &dueAt}
	store.mu.Unlock()

	due, err := store.ListDue(1_000, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 3 {
		t.Fatalf("due = %d, want 3", len(due))
	}
	if due[0].Phase != KeyModelPhaseRecovering {
		t.Fatalf("Recovering 应排最前: %+v", due[0])
	}
	if due[1].CapabilityHash != "open-due" {
		t.Fatalf("有 retryAt 的 Open 应排在 nil 之前: %+v", due[1])
	}
	// 负 limit 截断为 0。
	empty, err := store.ListDue(1_000, -3)
	if err != nil || len(empty) != 0 {
		t.Fatalf("负 limit 应返回空: %v err = %v", empty, err)
	}
}

func TestWeMemorySettleRecoveryOutcomes(t *testing.T) {
	// 注入固定时钟：cleanupLocked 的真实时间比较必须在合成时间线内保持稳定。
	store := NewInMemoryKeyModelRuntimeStore(NewFakeClock(time.UnixMilli(1_000_000)))
	ctx := context.Background()
	capability := testCapability()
	now := int64(1_000_000)

	if _, err := store.RecordFailure(ctx, KeyModelFailureIntent{
		IntentID: "i-1", RequestID: "r", AttemptID: "a", Capability: capability,
		ObservedAtMs: now, Outcome: KeyModelOutcomeUpstreamNotComplete, SourceFence: "f",
		RecoveryTarget: &KeyModelRecoveryTarget{AccountID: "acc-1", GroupID: "g-1", SystemAccountID: "sys-1"},
	}); err != nil {
		t.Fatal(err)
	}

	// 获取租约进入 HALF_OPEN。
	status, leased := store.AcquireRecoveryLease(MemoryRecoveryLeaseInput{
		Capability: capability, Generation: 1, DispatchRevision: capability.DispatchRevision, LeaseID: "lease-1", NowMs: now + 10_000,
	})
	if status != KeyModelMutationApplied {
		t.Fatalf("acquire = %s", status)
	}
	leaseUntil := leased.ProbeLease.LeaseUntilMs

	// generation 漂移 → stale。
	if status, _ := store.SettleRecovery(MemoryRecoverySettleInput{Capability: capability, Generation: 99, DispatchRevision: capability.DispatchRevision, LeaseID: "lease-1", Outcome: KeyModelOutcomeCompleteSuccess, NowMs: now + 11_000}); status != KeyModelMutationStale {
		t.Fatalf("generation 漂移应 stale: %s", status)
	}
	// 租约不匹配 → lease mismatch。
	if status, _ := store.SettleRecovery(MemoryRecoverySettleInput{Capability: capability, Generation: 1, DispatchRevision: capability.DispatchRevision, LeaseID: "other", Outcome: KeyModelOutcomeCompleteSuccess, NowMs: now + 11_000}); status != KeyModelMutationLeaseMismatch {
		t.Fatalf("租约不匹配应 mismatch: %s", status)
	}
	// 租约过期 → stale。
	if status, _ := store.SettleRecovery(MemoryRecoverySettleInput{Capability: capability, Generation: 1, DispatchRevision: capability.DispatchRevision, LeaseID: "lease-1", Outcome: KeyModelOutcomeCompleteSuccess, NowMs: leaseUntil + 1}); status != KeyModelMutationStale {
		t.Fatalf("过期租约应 stale: %s", status)
	}
	// unknown 结果：无历史成功 → 回 OPEN，10 秒后重试。
	status, settled := store.SettleRecovery(MemoryRecoverySettleInput{Capability: capability, Generation: 1, DispatchRevision: capability.DispatchRevision, LeaseID: "lease-1", Outcome: KeyModelOutcomeUnknown, NowMs: now + 11_000})
	if status != KeyModelMutationApplied || settled.Phase != KeyModelPhaseOpen {
		t.Fatalf("unknown = %s %+v", status, settled)
	}

	// 连续 3 次 complete_success → CLOSED（每轮重新获取租约；retryAt 间隔必须留足）。
	for round := 1; round <= 3; round++ {
		acquireAt := now + 30_000 + int64(round)*15_000
		status, _ := store.AcquireRecoveryLease(MemoryRecoveryLeaseInput{Capability: capability, Generation: 1, DispatchRevision: capability.DispatchRevision, LeaseID: "lease-" + intToDecimal(round+1), NowMs: acquireAt})
		if status != KeyModelMutationApplied {
			t.Fatalf("第 %d 轮 acquire = %s", round, status)
		}
		settleAt := acquireAt + 1_000
		status, settledState := store.SettleRecovery(MemoryRecoverySettleInput{Capability: capability, Generation: 1, DispatchRevision: capability.DispatchRevision, LeaseID: "lease-" + intToDecimal(round+1), Outcome: KeyModelOutcomeCompleteSuccess, NowMs: settleAt})
		if status != KeyModelMutationApplied {
			t.Fatalf("第 %d 轮 settle = %s", round, status)
		}
		if round < 3 {
			if settledState.Phase != KeyModelPhaseRecovering || settledState.RecoverySuccessCount != round {
				t.Fatalf("第 %d 轮状态 = %+v", round, settledState)
			}
		} else if settledState.Phase != KeyModelPhaseClosed || settledState.RetryAtMs != nil {
			t.Fatalf("第 3 轮应闭合: %+v", settledState)
		}
	}

	// 关闭态在保留窗口内仍可读（cleanupLocked 依赖注入时钟，不会提前删除）。
	state, err := store.Get(ctx, capability)
	if err != nil || state == nil || state.Phase != KeyModelPhaseClosed {
		t.Fatalf("关闭态应可读: %+v err = %v", state, err)
	}
}

func TestWeMemoryClosedStateCleanupAfterRetention(t *testing.T) {
	now := int64(2_000_000)
	clock := NewFakeClock(time.UnixMilli(now))
	store := NewInMemoryKeyModelRuntimeStore(clock)
	ctx := context.Background()
	capability := testCapability()

	if _, err := store.RecordFailure(ctx, KeyModelFailureIntent{
		IntentID: "i-1", RequestID: "r", AttemptID: "a", Capability: capability,
		ObservedAtMs: now, Outcome: KeyModelOutcomeUpstreamNotComplete, SourceFence: "f",
	}); err != nil {
		t.Fatal(err)
	}
	// 回执保留 5 分钟：窗口内同 intent 幂等。
	retry, err := store.RecordFailure(ctx, KeyModelFailureIntent{
		IntentID: "i-1", RequestID: "r", AttemptID: "a", Capability: capability,
		ObservedAtMs: now + 1_000, Outcome: KeyModelOutcomeUpstreamNotComplete, SourceFence: "f",
	})
	if err != nil || retry.Status != KeyModelMutationIdempotent {
		t.Fatalf("幂等 = %+v err = %v", retry, err)
	}
	// 推进时钟越过回执保留窗口，任一读路径触发 cleanup 后回执被删。
	clock.Advance(time.Duration(keyModelReceiptRetentionMs)*time.Millisecond + 2*time.Millisecond)
	if _, err := store.Get(ctx, capability); err != nil {
		t.Fatal(err)
	}
	later, err := store.RecordFailure(ctx, KeyModelFailureIntent{
		IntentID: "i-1", RequestID: "r", AttemptID: "a", Capability: capability,
		ObservedAtMs: now + keyModelReceiptRetentionMs + 3_000, Outcome: KeyModelOutcomeUpstreamNotComplete, SourceFence: "f",
	})
	// 回执过期后，同 dispatchRevision 的失败写入仍按状态幂等（不会重开 generation）。
	if err != nil || later.Status != KeyModelMutationIdempotent {
		t.Fatalf("过期回执后应按状态幂等: %+v err = %v", later, err)
	}
}

func TestWeMemoryRecordMainProbeFailureBranches(t *testing.T) {
	store := NewInMemoryKeyModelRuntimeStore(nil)
	ctx := context.Background()
	capability := testCapability()
	hash := mustCapabilityHash(capability)

	// permit 与 capability 不匹配 → 错误。
	if err := store.RecordMainProbeFailure(ctx, capability, KeyModelForegroundPermit{CapabilityHash: strings.Repeat("zz", 32), AttemptID: "a"}); err == nil {
		t.Fatal("permit 不匹配应报错")
	}
	// permit 不在活跃表（未准入）→ 仍写围栏但不产生 wake。
	if err := store.RecordMainProbeFailure(ctx, capability, KeyModelForegroundPermit{CapabilityHash: hash, AttemptID: "ghost"}); err != nil {
		t.Fatalf("err = %v", err)
	}
	// 围栏活跃期间准入被阻断。
	blocked, err := store.AdmitForeground(ctx, capability, "attempt-1")
	if err != nil || blocked.Status != ForegroundBlocked {
		t.Fatalf("围栏期间应阻断: %+v", blocked)
	}
	// owner 不匹配的清理/延后是 no-op。
	if cleared, err := store.ClearMainProbeFence(ctx, KeyModelFenceReference{CapabilityHash: hash, OwnerID: "other"}, ""); err != nil || cleared {
		t.Fatalf("owner 不匹配清理 = %v err = %v", cleared, err)
	}
	if deferred, err := store.DeferMainProbeFence(ctx, KeyModelFenceReference{CapabilityHash: hash, OwnerID: "other"}); err != nil || deferred {
		t.Fatalf("owner 不匹配延后 = %v err = %v", deferred, err)
	}
	// owner 匹配：延后与清理生效。
	if deferred, err := store.DeferMainProbeFence(ctx, KeyModelFenceReference{CapabilityHash: hash, OwnerID: "ghost"}); err != nil || !deferred {
		t.Fatalf("延后 = %v err = %v", deferred, err)
	}
	if cleared, err := store.ClearMainProbeFence(ctx, KeyModelFenceReference{CapabilityHash: hash, OwnerID: "ghost"}, ""); err != nil || !cleared {
		t.Fatalf("清理 = %v err = %v", cleared, err)
	}
	// 非法 hash 参数。
	if _, err := store.ReleaseForeground(ctx, KeyModelForegroundPermit{CapabilityHash: "bad-hash", AttemptID: "a"}); err == nil {
		t.Fatal("非法 hash 应报错")
	}
	if _, err := store.ReleaseForeground(ctx, KeyModelForegroundPermit{CapabilityHash: hash, AttemptID: " "}); err == nil {
		t.Fatal("空 attemptId 应报错")
	}
	if _, err := store.ClaimJ1Confirmation(ctx, " ", 1); err == nil {
		t.Fatal("空 sourceAccount 应报错")
	}
}

// ---------------------------------------------------------------------------
// keymodelrecovery.go：runner 默认值与探针异常恢复
// ---------------------------------------------------------------------------

func TestWeRecoveryRunnerDefaultsAndClamp(t *testing.T) {
	runner := NewKeyModelMemoryRecoveryRunner(KeyModelMemoryRecoveryRunnerOptions{})
	if runner.store == nil || runner.probe == nil || runner.now == nil || runner.createID == nil {
		t.Fatal("默认依赖必须补齐")
	}
	if runner.concurrency != keyModelRecoveryConcurrency {
		t.Fatalf("默认并发 = %d", runner.concurrency)
	}
	// 并发夹取：<1 → 1，>32 → 32。
	if got := NewKeyModelMemoryRecoveryRunner(KeyModelMemoryRecoveryRunnerOptions{Concurrency: -3}).concurrency; got != 1 {
		t.Fatalf("负并发应夹到 1, got %d", got)
	}
	if got := NewKeyModelMemoryRecoveryRunner(KeyModelMemoryRecoveryRunnerOptions{Concurrency: 999}).concurrency; got != keyModelRecoveryConcurrency {
		t.Fatalf("超限并发应夹到 %d, got %d", keyModelRecoveryConcurrency, got)
	}
}

func TestWeRecoveryRunnerProbePanicSettlesUnknown(t *testing.T) {
	store := NewInMemoryKeyModelRuntimeStore(nil)
	now := int64(5_000_000)
	capability := testCapability()
	if _, err := store.RecordFailure(context.Background(), KeyModelFailureIntent{
		IntentID: "i-1", RequestID: "r", AttemptID: "a", Capability: capability,
		ObservedAtMs: now, Outcome: KeyModelOutcomeUpstreamNotComplete, SourceFence: "f",
		RecoveryTarget: &KeyModelRecoveryTarget{AccountID: "acc-1", GroupID: "g", SystemAccountID: "s"},
	}); err != nil {
		t.Fatal(err)
	}
	runner := NewKeyModelMemoryRecoveryRunner(KeyModelMemoryRecoveryRunnerOptions{
		Store: store,
		Probe: func(KeyModelRecoveryProbeInput) KeyModelOutcome { panic("探针崩溃") },
		Now:   func() int64 { return now + 10_000 },
	})
	// 契约：探针 panic 不得击穿 sweep，按 unknown 结算。
	result := runner.Sweep(context.Background())
	if result.DueCount != 1 || result.StartedCount != 1 || result.SettledCount != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestWeRecoveryRunnerSkipsMissingTargetAndCancelledContext(t *testing.T) {
	store := NewInMemoryKeyModelRuntimeStore(nil)
	now := int64(5_000_000)
	capability := testCapability()
	// 不带 RecoveryTarget：候选被跳过（不启动探针）。
	if _, err := store.RecordFailure(context.Background(), KeyModelFailureIntent{
		IntentID: "i-1", RequestID: "r", AttemptID: "a", Capability: capability,
		ObservedAtMs: now, Outcome: KeyModelOutcomeUpstreamNotComplete, SourceFence: "f",
	}); err != nil {
		t.Fatal(err)
	}
	runner := NewKeyModelMemoryRecoveryRunner(KeyModelMemoryRecoveryRunnerOptions{
		Store: store,
		Now:   func() int64 { return now + 10_000 },
	})
	result := runner.Sweep(context.Background())
	if result.DueCount != 1 || result.StartedCount != 0 {
		t.Fatalf("无 target 应跳过: %+v", result)
	}

	// 已取消的 context：选择循环立即结束。
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	result = runner.Sweep(cancelled)
	if result.StartedCount != 0 {
		t.Fatalf("取消后不应启动: %+v", result)
	}

	// ListDue 失败：按空结果处理。
	runner = NewKeyModelMemoryRecoveryRunner(KeyModelMemoryRecoveryRunnerOptions{
		Store: store,
		Now:   func() int64 { return 0 },
	})
	result = runner.Sweep(context.Background())
	if result != (SweepResult{}) {
		t.Fatalf("ListDue 失败应返回空结果: %+v", result)
	}
}

// ---------------------------------------------------------------------------
// keymodelattempt.go：Prepare 拒绝分支与续租失败
// ---------------------------------------------------------------------------

// weRenewErrorStore 包装 mock store，让 RenewForeground 可失败。
type weRenewErrorStore struct {
	*mockKeyModelStore
	renewErr error
}

func (s *weRenewErrorStore) RenewForeground(context.Context, KeyModelForegroundPermit) (*KeyModelForegroundPermit, error) {
	return nil, s.renewErr
}

// weAdmitErrorStore 包装 mock store，让 AdmitForeground 可失败。
type weAdmitErrorStore struct {
	*mockKeyModelStore
}

func (s *weAdmitErrorStore) AdmitForeground(context.Context, CapabilityKey, string) (KeyModelAdmissionResult, error) {
	return KeyModelAdmissionResult{}, errors.New("准入存储失败")
}

func TestWePrepareAttemptRejections(t *testing.T) {
	route := GatewayKeyModelCapability{AccountID: "acc-1", Capability: testCapability()}
	hash := mustCapabilityHash(testCapability())

	// blocked / busy / error / 非法 capability。
	blockedStore := newMockKeyModelStore()
	blockedStore.admissions[hash+"|attempt-1"] = KeyModelAdmissionResult{Status: ForegroundBlocked, WakeSequence: 7}
	preparation, err := PrepareGatewayKeyModelAttempt(context.Background(), blockedStore, PrepareGatewayKeyModelAttemptInput{Route: route, AttemptID: "attempt-1"})
	if err != nil || preparation.Status != AttemptPreparationBlocked || preparation.WakeSequence != 7 || preparation.Attempt != nil {
		t.Fatalf("preparation = %+v err = %v", preparation, err)
	}

	busyStore := newMockKeyModelStore()
	busyStore.admissions[hash+"|attempt-1"] = KeyModelAdmissionResult{Status: ForegroundBusy}
	preparation, err = PrepareGatewayKeyModelAttempt(context.Background(), busyStore, PrepareGatewayKeyModelAttemptInput{Route: route, AttemptID: "attempt-1"})
	if err != nil || preparation.Status != AttemptPreparationBusy {
		t.Fatalf("preparation = %+v err = %v", preparation, err)
	}

	if _, err := PrepareGatewayKeyModelAttempt(context.Background(), &weAdmitErrorStore{newMockKeyModelStore()}, PrepareGatewayKeyModelAttemptInput{Route: route, AttemptID: "attempt-1"}); err == nil {
		t.Fatal("准入错误应传播")
	}
	badRoute := route
	badRoute.Capability.ClientModel = " "
	if _, err := PrepareGatewayKeyModelAttempt(context.Background(), newMockKeyModelStore(), PrepareGatewayKeyModelAttemptInput{Route: badRoute, AttemptID: "attempt-1"}); err == nil {
		t.Fatal("非法 capability 应报错")
	}
}

func TestWeAttemptRenewErrorLosesPermit(t *testing.T) {
	store := &weRenewErrorStore{mockKeyModelStore: newMockKeyModelStore(), renewErr: errors.New("续租失败")}
	route := GatewayKeyModelCapability{AccountID: "acc-1", Capability: testCapability()}
	attempt, preparation, scheduler := prepareAttemptForTest(t, store, route, NewGatewayKeyModelFailureBudget())
	if preparation.Status != AttemptPreparationAdmitted {
		t.Fatalf("preparation = %+v", preparation)
	}
	// 续租失败：permit 丢失信号关闭。
	scheduler.Fire()
	select {
	case <-attempt.PermitLost():
	case <-time.After(5 * time.Second):
		t.Fatal("续租失败应关闭 permit 丢失信号")
	}
	// 释放续租定时器，避免泄漏。
	attempt.MarkPrecommit()
}

func TestWeAttemptRenewSkipsAfterRelease(t *testing.T) {
	store := newMockKeyModelStore()
	route := GatewayKeyModelCapability{AccountID: "acc-1", Capability: testCapability()}
	attempt, _, scheduler := prepareAttemptForTest(t, store, route, NewGatewayKeyModelFailureBudget())
	// 白盒：已释放后 renew 直接返回，不触碰存储。
	attempt.mu.Lock()
	attempt.released = true
	attempt.mu.Unlock()
	attempt.renew()
	if store.renewed {
		t.Fatal("已释放后不应续租")
	}
	// terminal 关闭同样短路 renew。
	attempt2, _, scheduler2 := prepareAttemptForTest(t, newMockKeyModelStore(), route, NewGatewayKeyModelFailureBudget())
	if err := attempt2.ReportCompleteSuccess(context.Background()); err != nil {
		t.Fatal(err)
	}
	attempt2.renew()
	scheduler.Fire()
	scheduler2.Fire()
}

func TestWeAttemptObservedAtFromAttemptStart(t *testing.T) {
	// RecordFailure 的 AttemptStartedAt 优先于时钟读数。
	writer := &weAPIKeyWriter{failureResult: APIKeyWriteResult{Changed: true}}
	guard := NewAccountAPIKeyFailureGuard(SideEffectsConfig{}, nil, nil, nil)
	effects, _, _ := weNewEffects(t, "", writer, guard, nil)
	started := "2025-12-31T23:59:59.000Z"
	effects.RecordFailure(context.Background(), weAPIKeyAccount("acc-1"), RecordFailureInput{
		Status:           APIKeyStatusRateLimited,
		TrafficSource:    TrafficSourceGateway,
		MutationContext:  weExplicitFailureContext(),
		AttemptStartedAt: &started,
	})
	if writer.failureCount() != 1 {
		t.Fatalf("failure writes = %d", writer.failureCount())
	}
	if got := writer.failureWrites[0].Input.ObservedAt; got != started {
		t.Fatalf("ObservedAt = %s, want %s", got, started)
	}
}

// ---------------------------------------------------------------------------
// apikeyguard.go：ClearTransientFailure 分支与围栏 epoch 溢出复位
// ---------------------------------------------------------------------------

func TestWeGuardClearTransientFailureBranches(t *testing.T) {
	ctx := context.Background()
	account := guardTestAccount("acc-1", "fp-1")

	// memory 驱动：直接 no-op。
	memoryGuard, _ := newGuardForTest(t, "")
	if cleared, err := memoryGuard.ClearTransientFailure(ctx, account); cleared || err != nil {
		t.Fatalf("memory 驱动应 no-op: %v err = %v", cleared, err)
	}

	// redis 驱动但缺 transient generation：no-op。
	redisGuard, _ := newGuardForTest(t, "redis")
	if cleared, err := redisGuard.ClearTransientFailure(ctx, account); cleared || err != nil {
		t.Fatalf("缺 generation 应 no-op: %v err = %v", cleared, err)
	}

	// 带 generation：走 store 成功路径。
	generation := "gen-1"
	withGeneration := guardTestAccount("acc-1", "fp-1")
	withGeneration.SelectedAPIKeyTransientGeneration = &generation
	store := &weTransientStore{}
	redisGuard.SetTransientStateStoreForTest(store)
	if cleared, err := redisGuard.ClearTransientFailure(ctx, withGeneration); err != nil || !cleared {
		t.Fatalf("cleared = %v err = %v", cleared, err)
	}
	// store 错误传播。
	redisGuard.SetTransientStateStoreForTest(&weTransientStore{err: errors.New("redis 失败")})
	if _, err := redisGuard.ClearTransientFailure(ctx, withGeneration); err == nil {
		t.Fatal("store 错误应传播")
	}
	// applied=false 但状态已是 success → 视为已清理。
	redisGuard.SetTransientStateStoreForTest(&weSuccessObservationStore{})
	cleared, err := redisGuard.ClearTransientFailure(ctx, withGeneration)
	if err != nil || !cleared {
		t.Fatalf("success 状态应视为已清理: %v err = %v", cleared, err)
	}
}

// weSuccessObservationStore 的 RecordSuccess 返回 applied=false 但状态为 success。
type weSuccessObservationStore struct {
	weTransientStore
}

func (s *weSuccessObservationStore) RecordSuccess(context.Context, TransientMutationInput) (AccountApiKeyTransientMutationResult, error) {
	kind := "success"
	state := &AccountApiKeyTransientState{ObservationKind: kind, SchemaVersion: 1}
	return AccountApiKeyTransientMutationResult{Applied: false, Reason: TransientReasonApplied, State: state}, nil
}

func TestWeGuardObservationEpochOverflowResets(t *testing.T) {
	guard, _ := newGuardForTest(t, "")
	guard.mu.Lock()
	guard.observationEpoch = safeIntegerMax
	guard.fences["stale"] = &LocalApiKeyObservationFence{LatestEpoch: 1, ExpiresAtMs: 9_999_999_999}
	epoch := guard.nextLocalAPIKeyObservationEpochLocked()
	guard.mu.Unlock()
	// 契约：epoch 溢出时复位为 1 并清空全部围栏（避免 int64 溢出破坏新旧比较）。
	if epoch != 1 {
		t.Fatalf("epoch = %d, want 1", epoch)
	}
	guard.mu.Lock()
	count := len(guard.fences)
	guard.mu.Unlock()
	if count != 0 {
		t.Fatalf("溢出后围栏应清空, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// policyavoidance.go：默认时钟、缓存过期与容量淘汰
// ---------------------------------------------------------------------------

func TestWePolicyAvoidanceCacheOnlyModeAndExpiry(t *testing.T) {
	// nil store/dirty/invalidate/clock：全部回落进程内缓存语义。
	service := NewConfiguredPolicyAvoidanceService(nil, nil, nil, nil)
	account := SuppressibleGatewayAccount{ID: "acc-1"}
	if err := service.SuppressGatewayAccountLocallyForSeconds(context.Background(), account, nil, "原因"); err != nil {
		t.Fatalf("缓存模式不应报错: %v", err)
	}
	states, err := service.LoadConfiguredPolicyAvoidanceStates(context.Background(), []string{"acc-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0] == nil {
		t.Fatalf("states = %+v", states)
	}
	if states[0].UntilMs <= states[0].StartedAtMs {
		t.Fatalf("TTL 投影错误: %+v", states[0])
	}
}

func TestWePolicyAvoidanceCacheEviction(t *testing.T) {
	service := NewConfiguredPolicyAvoidanceService(nil, nil, nil, NewFakeClock(time.Unix(1000, 0)))
	// 注入超过容量上限的过期条目（只持锁写 map，remember 内部自行加锁）。
	service.mu.Lock()
	for index := 0; index < configuredPolicyAvoidanceCacheMaxEntries+5; index++ {
		service.cache["stale-"+intToDecimal(index)] = configuredPolicyAvoidanceCacheEntry{expiresAtMs: 999}
	}
	service.mu.Unlock()
	service.rememberConfiguredPolicyAvoidanceState("fresh", &ConfiguredPolicyAvoidanceState{RuntimeKey: "fresh", AccountID: "a", StartedAtMs: 1000, UntilMs: 5000}, 1000)
	service.mu.Lock()
	size := len(service.cache)
	service.mu.Unlock()
	if size > configuredPolicyAvoidanceCacheMaxEntries {
		t.Fatalf("缓存未收敛到容量内: %d", size)
	}
}

func TestWePolicyAvoidanceCacheUntilClamp(t *testing.T) {
	// UntilMs 早于缓存 TTL：缓存有效期被夹到 UntilMs，过期后重新读 store。
	service := NewConfiguredPolicyAvoidanceService(nil, nil, nil, NewFakeClock(time.UnixMilli(1_000_000)))
	account := SuppressibleGatewayAccount{ID: "acc-1"}
	if err := service.SuppressGatewayAccountLocallyForSeconds(context.Background(), account, func() *int64 { v := int64(1); return &v }(), "原因"); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	entry := service.cache["acc-1"]
	service.mu.Unlock()
	if entry.expiresAtMs != entry.state.UntilMs {
		t.Fatalf("缓存有效期应夹到 UntilMs: %+v", entry)
	}
	// 时钟推进到 UntilMs 之后：缓存条目过期删除。
	service.clock.(*FakeClock).Set(time.UnixMilli(entry.state.UntilMs + 1))
	if _, hit := service.cachedConfiguredPolicyAvoidanceState("acc-1", NowMs(service.clock)); hit {
		t.Fatal("过期条目应视为 miss")
	}
	service.mu.Lock()
	_, exists := service.cache["acc-1"]
	service.mu.Unlock()
	if exists {
		t.Fatal("过期条目应被删除")
	}
}

// ---------------------------------------------------------------------------
// 零散 helper：clock / runtimekeys / sideeffectpolicy / queue / runtime
// ---------------------------------------------------------------------------

func TestWePassiveScheduleJitterWindows(t *testing.T) {
	day := int64(24 * 60 * 60_000)
	week := 7 * day
	tests := []struct {
		interval int64
		want     int64
	}{
		{0, 0},
		{1, 0},
		{2, 1},
		{59_999, 29_999},
		{60_000, 30_000},
		{60*60_000 - 1, 30_000},
		{60 * 60_000, 30 * 60_000},
		{day - 1, 30 * 60_000},
		{day, 60 * 60_000},
		{week - 1, 60 * 60_000},
		{week, 8 * 60 * 60_000},
		{week * 10, 8 * 60 * 60_000},
	}
	for _, tt := range tests {
		if got := passiveScheduleJitterWindowMs(tt.interval); got != tt.want {
			t.Fatalf("window(%d) = %d, want %d", tt.interval, got, tt.want)
		}
	}
}

func TestWeNowMsWithNilClock(t *testing.T) {
	if NowMs(nil) <= 0 {
		t.Fatal("nil 时钟应回落系统时钟")
	}
}

func TestWeRuntimeKeyClearTargetHelpers(t *testing.T) {
	if keys := ClearKeysForAccount(SuppressibleGatewayAccount{}); len(keys) != 0 {
		t.Fatalf("空账户应返回空: %v", keys)
	}
	plain := ClearKeysForAccount(SuppressibleGatewayAccount{ID: "acc-1"})
	if len(plain) != 1 || plain[0] != "acc-1" {
		t.Fatalf("plain = %v", plain)
	}
	authorized := ClearKeysForAccount(SuppressibleGatewayAccount{
		ID: "acc-1", AccessType: "authorized",
		BindingSystemAccountID: "sys-1", BoundGroupID: "g-1", AccountAuthorizationID: "auth-1",
	})
	if len(authorized) != 2 || authorized[0] != "acc-1" || authorized[1] != "acc-1:authorized:sys-1:g-1:auth-1" {
		t.Fatalf("authorized = %v", authorized)
	}
	if keys := (GatewayAccountRuntimeClearTarget{AccountID: "acc-1", AuthorizedBinding: &AuthorizedBinding{SystemAccountID: "s", GroupID: "g", AccountAuthorizationID: "a"}}).ClearKeys(); len(keys) != 2 {
		t.Fatalf("ClearKeys = %v", keys)
	}
}

func TestWeKeyModelRuntimeMisc(t *testing.T) {
	if minInt(1, 2) != 1 || minInt(2, 1) != 1 {
		t.Fatal("minInt 语义错误")
	}
	if _, err := CanonicalCapabilityJSON(CapabilityKey{}); err == nil {
		t.Fatal("非法 capability 应报错")
	}
	// Clone 必须深拷贝指针字段。
	retry := int64(7)
	success := int64(8)
	state := KeyModelState{RetryAtMs: &retry, LastRecoverySuccessAtMs: &success, ProbeLease: &KeyModelProbeLease{LeaseID: "l"}}
	cloned := state.Clone()
	*cloned.RetryAtMs = 99
	if *state.RetryAtMs != 7 {
		t.Fatal("Clone 应深拷贝 RetryAtMs")
	}
	cloned.ProbeLease.LeaseID = "changed"
	if state.ProbeLease.LeaseID != "l" {
		t.Fatal("Clone 应深拷贝 ProbeLease")
	}
	// OpenAI runtime secret → runtime key 投影。
	secret := gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1", Status: "active"}
	if key, err := GatewayAccountRuntimeKeyForSecret(secret); err != nil || key != "acc-1" {
		t.Fatalf("key = %s err = %v", key, err)
	}
}

// 以下三个 helper 原属 sideeffectqueue.go（Node side-effect 死半区），
// 随 PLAN-20260919T000723744Z 任务 B 删除后仅本文件测试仍在使用，就近保留。
func derefStringPtr(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func SuppressibleFromSecret(secret gatewayruntimecache.OpenAIAccountSecret) SuppressibleGatewayAccount {
	return SuppressibleGatewayAccount{
		ID:                        secret.ID,
		AccountAccessType:         secret.AccountAccessType,
		BindingSystemAccountID:    derefStringPtr(secret.BindingSystemAccountID),
		BoundGroupID:              derefStringPtr(secret.BoundGroupID),
		AccountAuthorizationID:    derefStringPtr(secret.AccountAuthorizationID),
		CredentialSourceAccountID: derefStringPtr(secret.CredentialSourceAccountID),
	}
}

func GatewayAccountRuntimeKeyForSecret(secret gatewayruntimecache.OpenAIAccountSecret) (string, error) {
	return GatewayAccountRuntimeKey(SuppressibleFromSecret(secret))
}
