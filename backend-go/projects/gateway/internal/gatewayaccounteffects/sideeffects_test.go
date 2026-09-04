package gatewayaccounteffects

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

type scriptedWriter struct {
	mu        sync.Mutex
	failures  int
	calls     []AccountSideEffectOperation
	err       error
	gate      chan struct{}
	firstOnly bool
}

func (w *scriptedWriter) ApplyAccountErrorHandling(ctx context.Context, operation AccountSideEffectOperation) (AccountErrorHandlingResult, error) {
	w.mu.Lock()
	w.calls = append(w.calls, operation)
	shouldFail := w.failures > 0
	if shouldFail {
		w.failures--
	}
	gate := w.gate
	w.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if shouldFail {
		return AccountErrorHandlingResult{}, errors.New("db 写入失败")
	}
	return AccountErrorHandlingResult{Changed: true, AccountStatus: "active"}, nil
}

func (w *scriptedWriter) callCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.calls)
}

type recorderHook struct {
	mu        sync.Mutex
	cleared   []string
	invalidat int
	probeSchedules []RecoveryProbeScheduleInput
}

func (r *recorderHook) clearLocal(runtimeKey string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleared = append(r.cleared, runtimeKey)
	return true
}

func (r *recorderHook) invalidate() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.invalidat++
}

func (r *recorderHook) scheduleProbe(input RecoveryProbeScheduleInput) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.probeSchedules = append(r.probeSchedules, input)
}

func newSideEffectTestService(t *testing.T, writer *scriptedWriter) (*SideEffectsService, *recorderHook, *FakeClock, *ManualScheduler) {
	t.Helper()
	clock := NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	scheduler := NewManualScheduler()
	hook := &recorderHook{}
	service, err := NewSideEffectsService(SideEffectsConfig{}, SideEffectDeps{
		Clock:                          clock,
		Random:                         func() float64 { return 0.5 },
		Scheduler:                      scheduler,
		Writer:                         writer,
		ClearRuntimeAvailabilityLocal:  hook.clearLocal,
		InvalidateRuntimeCache:         hook.invalidate,
		ScheduleRecoveryProbe:          hook.scheduleProbe,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, hook, clock, scheduler
}

func testOperationFor(accountID string, success bool, observedAt string) AccountSideEffectOperation {
	operation := newTestOperation(accountID, success)
	operation.Input.ObservedAt = observedAt
	return operation
}

func TestSideEffectLifecycleSuccess(t *testing.T) {
	writer := &scriptedWriter{}
	service, hook, _, scheduler := newSideEffectTestService(t, writer)
	ctx := context.Background()

	if err := service.EnqueueGatewayAccountErrorHandlingSideEffect(ctx, testOperationFor("acc-1", true, "2026-01-01T00:00:00.000Z")); err != nil {
		t.Fatal(err)
	}
	if writer.callCount() != 0 {
		t.Fatal("drain should be deferred to the scheduler")
	}
	if scheduler.Pending() != 1 {
		t.Fatalf("pending timers = %d, want 1", scheduler.Pending())
	}
	scheduler.Fire()
	if writer.callCount() != 1 {
		t.Fatalf("writer calls = %d, want 1", writer.callCount())
	}
	state := service.GetState(0, 0)
	if state.CompletedCount != 1 || state.EnqueuedCount != 1 || state.QueueLength != 0 {
		t.Fatalf("state = %+v", state)
	}
	if len(hook.cleared) == 0 || hook.cleared[0] != "acc-1" {
		t.Fatalf("cleared keys = %v", hook.cleared)
	}
	if hook.invalidat == 0 {
		t.Fatal("runtime cache should be invalidated after changed write")
	}
}

func TestSideEffectLifecycleRetryThenTerminal(t *testing.T) {
	writer := &scriptedWriter{failures: 2}
	service, _, clock, scheduler := newSideEffectTestService(t, writer)
	ctx := context.Background()

	if err := service.EnqueueGatewayAccountErrorHandlingSideEffect(ctx, testOperationFor("acc-1", false, "2026-01-01T00:00:00.000Z")); err != nil {
		t.Fatal(err)
	}
	scheduler.Fire()
	if writer.callCount() != 1 {
		t.Fatalf("first attempt calls = %d", writer.callCount())
	}
	state := service.GetState(0, 0)
	if state.FailedAttemptCount != 1 || state.QueueLength != 1 {
		t.Fatalf("state after failure = %+v", state)
	}
	// Advance past the first retry delay (500ms base + jitter ≤ +250ms).
	clock.Advance(2 * time.Second)
	scheduler.Fire()
	if writer.callCount() != 2 {
		t.Fatalf("second attempt calls = %d", writer.callCount())
	}
	clock.Advance(5 * time.Second)
	scheduler.Fire()
	if writer.callCount() != 3 {
		t.Fatalf("third attempt calls = %d", writer.callCount())
	}
	state = service.GetState(0, 0)
	if state.CompletedCount != 1 || state.FailedAttemptCount != 2 || state.QueueLength != 0 {
		t.Fatalf("final state = %+v", state)
	}
}

func TestSideEffectRetryDelayRespectsExponentialBackoff(t *testing.T) {
	// 直接校验 retryDueAtMs 的指数退避与被动抖动下界。
	clock := NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	service, err := NewSideEffectsService(SideEffectsConfig{RetryInitialDelayMs: 500, RetryMaxDelayMs: 30_000}, SideEffectDeps{
		Clock: clock, Random: func() float64 { return 0 }, Scheduler: NewManualScheduler(),
		Writer: WriterFunc(func(context.Context, AccountSideEffectOperation) (AccountErrorHandlingResult, error) {
			return AccountErrorHandlingResult{}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	now := NowMs(clock)
	for attempt, wantBase := range map[int]int64{1: 500, 2: 1000, 3: 2000, 10: 30000} {
		due := service.retryDueAtMs(attempt, now)
		window := wantBase / 2
		if window > 30_000 {
			window = 30_000
		}
		if due < now+wantBase-window {
			t.Fatalf("attempt %d due %d < window floor %d", attempt, due, wantBase-window)
		}
		if due > now+wantBase+window {
			t.Fatalf("attempt %d due %d exceeds jitter window %d", attempt, due, wantBase+window)
		}
	}
}

func TestSideEffectExpiredTerminal(t *testing.T) {
	writer := &scriptedWriter{failures: 1}
	service, _, clock, scheduler := newSideEffectTestService(t, writer)
	ctx := context.Background()

	if err := service.EnqueueGatewayAccountErrorHandlingSideEffect(ctx, testOperationFor("acc-1", false, "2026-01-01T00:00:00.000Z")); err != nil {
		t.Fatal(err)
	}
	scheduler.Fire()
	// Retention 10min：推过保留窗后重试必须进入过期终态。
	clock.Advance(11 * time.Minute)
	scheduler.Fire()
	state := service.GetState(0, 0)
	if state.ExpiredCount != 1 || state.QueueLength != 0 || state.CompletedCount != 0 {
		t.Fatalf("state after expiry = %+v", state)
	}
	if writer.callCount() != 1 {
		t.Fatalf("expired item must not retry, calls = %d", writer.callCount())
	}
}

func TestSideEffectStaleWatermarkAfterExecutionFailure(t *testing.T) {
	// drain 弹出失败项并等待写入期间，更新的成功 watermark 使其 epoch 变 stale：
	// 写入失败后不得重试，直接进入 stale 终态。
	entered := make(chan struct{})
	release := make(chan struct{})
	writer := &scriptedWriter{failures: 1}
	baseApply := writer.ApplyAccountErrorHandling
	gated := WriterFunc(func(ctx context.Context, operation AccountSideEffectOperation) (AccountErrorHandlingResult, error) {
		if operation.Input.Success {
			return baseApply(ctx, operation)
		}
		close(entered)
		<-release
		return baseApply(ctx, operation)
	})
	service, _, _, scheduler := newSideEffectTestService(t, &scriptedWriter{})
	service.deps.Writer = gated
	ctx := context.Background()

	if err := service.EnqueueGatewayAccountErrorHandlingSideEffect(ctx, testOperationFor("acc-1", false, "2026-01-01T00:00:00.000Z")); err != nil {
		t.Fatal(err)
	}
	drained := make(chan struct{})
	go func() {
		scheduler.Fire()
		close(drained)
	}()
	<-entered
	// 排队项已被弹出，成功 watermark 只推进 epoch，不再取消。
	if err := service.EnqueueGatewayAccountErrorHandlingSideEffect(ctx, testOperationFor("acc-1", true, "2026-01-01T00:00:01.000Z")); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-drained
	state := service.GetState(0, 0)
	if state.StaleCount != 1 {
		t.Fatalf("staleCount = %d, want 1 (state %+v)", state.StaleCount, state)
	}
	if state.QueueLength != 0 {
		t.Fatalf("stale item must not requeue, state = %+v", state)
	}
}

func TestSideEffectCoalesceFailure(t *testing.T) {
	writer := &scriptedWriter{}
	service, _, _, scheduler := newSideEffectTestService(t, writer)
	ctx := context.Background()

	if err := service.EnqueueGatewayAccountErrorHandlingSideEffect(ctx, testOperationFor("acc-1", false, "2026-01-01T00:00:00.000Z")); err != nil {
		t.Fatal(err)
	}
	if err := service.EnqueueGatewayAccountErrorHandlingSideEffect(ctx, testOperationFor("acc-1", false, "2026-01-01T00:00:01.000Z")); err != nil {
		t.Fatal(err)
	}
	state := service.GetState(0, 0)
	if state.QueueLength != 1 || state.CoalescedCount != 1 {
		t.Fatalf("coalesce state = %+v", state)
	}
	scheduler.Fire()
	if writer.callCount() != 1 {
		t.Fatalf("coalesced item should execute once, calls = %d", writer.callCount())
	}
}

func TestSideEffectGatewaySourceWithoutPolicySkipped(t *testing.T) {
	writer := &scriptedWriter{}
	service, _, _, _ := newSideEffectTestService(t, writer)
	operation := testOperationFor("acc-1", false, "2026-01-01T00:00:00.000Z")
	operation.Input.TrafficSource = TrafficSourceGateway
	if err := service.EnqueueGatewayAccountErrorHandlingSideEffect(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	state := service.GetState(0, 0)
	if state.QueueLength != 0 || state.EnqueuedCount != 0 {
		t.Fatalf("gateway failure without policyDecision must be dropped, state = %+v", state)
	}
	operation.Input.PolicyDecision = map[string]any{"action": "allow"}
	if err := service.EnqueueGatewayAccountErrorHandlingSideEffect(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	if service.GetState(0, 0).QueueLength != 1 {
		t.Fatal("policyDecision authorized the enqueue")
	}
}

func TestSideEffectInvalidObservedAtRejected(t *testing.T) {
	writer := &scriptedWriter{}
	service, _, _, _ := newSideEffectTestService(t, writer)
	err := service.EnqueueGatewayAccountErrorHandlingSideEffect(context.Background(), testOperationFor("acc-1", true, "not-a-time"))
	if err == nil || err.Error() != "account side effect observedAt必须是带 Z 或数值 offset 的 RFC3339 时间" {
		t.Fatalf("err = %v", err)
	}
}

func TestSideEffectEvictsOldestFailureForSuccessAtCapacity(t *testing.T) {
	writer := &scriptedWriter{}
	clock := NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	hook := &recorderHook{}
	service, err := NewSideEffectsService(SideEffectsConfig{QueueMaxLength: 1}, SideEffectDeps{
		Clock: clock, Random: func() float64 { return 0 }, Scheduler: NewManualScheduler(),
		Writer: writer, ClearRuntimeAvailabilityLocal: hook.clearLocal, InvalidateRuntimeCache: hook.invalidate,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := service.EnqueueGatewayAccountErrorHandlingSideEffect(ctx, testOperationFor("acc-fail", false, "2026-01-01T00:00:00.000Z")); err != nil {
		t.Fatal(err)
	}
	// 队列已满：成功观测可淘汰最早失败写入。
	if err := service.EnqueueGatewayAccountErrorHandlingSideEffect(ctx, testOperationFor("acc-ok", true, "2026-01-01T00:00:01.000Z")); err != nil {
		t.Fatal(err)
	}
	state := service.GetState(0, 0)
	if state.QueueLength != 1 || state.DroppedCount != 1 || state.EvictedFailureForSuccessCount != 1 {
		t.Fatalf("eviction state = %+v", state)
	}
	// 队列满且没有失败可淘汰：新的失败观测被丢弃。
	if err := service.EnqueueGatewayAccountErrorHandlingSideEffect(ctx, testOperationFor("acc-drop", false, "2026-01-01T00:00:02.000Z")); err != nil {
		t.Fatal(err)
	}
	if service.GetState(0, 0).QueueLength != 1 {
		t.Fatal("drop should not change queue length")
	}
}

func TestFailureStormPrecheckThresholds(t *testing.T) {
	tests := []struct {
		name          string
		failures      int
		distinctIPs   int
		successes     int
		successAgeMs  int64
		firstSeenAge  int64
		force         bool
		wantTrigger   bool
		wantSkipped   string
	}{
		{name: "低于失败次数阈值", failures: 4, distinctIPs: 2, firstSeenAge: 61_000, wantTrigger: false, wantSkipped: StormSkippedBelowThreshold},
		{name: "低于独立 IP 阈值", failures: 5, distinctIPs: 1, firstSeenAge: 61_000, wantTrigger: false, wantSkipped: StormSkippedBelowThreshold},
		{name: "观察窗口不足", failures: 5, distinctIPs: 2, firstSeenAge: 59_000, wantTrigger: false, wantSkipped: StormSkippedObservationWindow},
		{name: "近期有成功", failures: 5, distinctIPs: 2, successes: 3, successAgeMs: 4_000, firstSeenAge: 61_000, wantTrigger: false, wantSkipped: StormSkippedRecentSuccess},
		{name: "失败占比不足", failures: 5, distinctIPs: 2, successes: 3, successAgeMs: 6_000, firstSeenAge: 61_000, wantTrigger: false, wantSkipped: StormSkippedFailureRatio},
		{name: "满足全部阈值", failures: 5, distinctIPs: 2, firstSeenAge: 61_000, wantTrigger: true},
		{name: "force 只看观察窗口", failures: 1, distinctIPs: 1, firstSeenAge: 61_000, force: true, wantTrigger: true},
		{name: "force 观察窗口不足", failures: 1, distinctIPs: 1, firstSeenAge: 1_000, force: true, wantTrigger: false, wantSkipped: StormSkippedObservationWindow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := int64(1_000_000)
			entry := FailureStormEntry{
				FirstSeenMs: now - tt.firstSeenAge,
				LastSeenMs:  now,
				FailureCount: tt.failures,
				ClientIPs:   map[string]struct{}{},
			}
			for index := 0; index < tt.distinctIPs; index++ {
				entry.ClientIPs[string(rune('a'+index))] = struct{}{}
			}
			var success *SuccessObservationEntry
			if tt.successes > 0 {
				success = &SuccessObservationEntry{FirstSeenMs: now - tt.successAgeMs, LastSeenMs: now - tt.successAgeMs, SuccessCount: tt.successes}
			}
			decision := shouldTriggerFailureStormPrecheck(success, entry, tt.force, now)
			if decision.Trigger != tt.wantTrigger || decision.SkippedReason != tt.wantSkipped {
				t.Fatalf("decision = %+v, want trigger=%v skipped=%q", decision, tt.wantTrigger, tt.wantSkipped)
			}
		})
	}
}

func TestRecordFailureForPrecheckSchedulesProbe(t *testing.T) {
	writer := &scriptedWriter{}
	service, hook, _, _ := newSideEffectTestService(t, writer)
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1", Status: "active"}
	service.RecordGatewayAccountFailureForPrecheck(context.Background(), account, GatewayAccountFailurePrecheckInput{
		SystemAccountID: "sys-1",
		GroupID:         "grp-1",
		Reason:          "上游 5xx",
		ClientIP:        "10.0.0.1",
		APIKeyID:        "key-1",
	})
	hook.mu.Lock()
	schedules := hook.probeSchedules
	hook.mu.Unlock()
	if len(schedules) != 1 {
		t.Fatalf("schedules = %d, want 1", len(schedules))
	}
	input := schedules[0]
	if input.RuntimeKey != "acc-1" || input.FailureCount != 1 || input.DistinctClientIPCount != 1 || input.DistinctAPIKeyCount != 1 || input.Reason != "上游 5xx" {
		t.Fatalf("schedule input = %+v", input)
	}
	// Redis driver 下不得进入进程内 storm 簿记，而是走 distributed 端口。
	writer2 := &scriptedWriter{}
	clock := NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	distributed := &recorderHook{}
	distributedCalls := 0
	var mu sync.Mutex
	service2, err := NewSideEffectsService(SideEffectsConfig{RuntimeStateDriver: "redis"}, SideEffectDeps{
		Clock: clock, Random: func() float64 { return 0 }, Scheduler: NewManualScheduler(), Writer: writer2,
		RecordDistributedFailureForPrecheck: func(ctx context.Context, account gatewayruntimecache.OpenAIAccountSecret, input GatewayAccountFailurePrecheckInput) {
			mu.Lock()
			distributedCalls++
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	service2.RecordGatewayAccountFailureForPrecheck(context.Background(), account, GatewayAccountFailurePrecheckInput{SystemAccountID: "sys", GroupID: "grp", Reason: "r"})
	if service2.GetState(0, 0).QueueLength != 0 {
		t.Fatal("redis driver must not queue local storms")
	}
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if distributedCalls != 1 {
		t.Fatalf("distributed precheck calls = %d, want 1", distributedCalls)
	}
	_ = distributed
}

func TestSideEffectConcurrentEnqueueDrain(t *testing.T) {
	writer := &scriptedWriter{}
	service, _, _, scheduler := newSideEffectTestService(t, writer)
	var wg sync.WaitGroup
	for index := 0; index < 32; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			operation := testOperationFor("acc", index%2 == 0, "2026-01-01T00:00:00.000Z")
			if index%2 == 1 {
				operation.Input.PolicyDecision = struct{}{}
			}
			if err := service.EnqueueGatewayAccountErrorHandlingSideEffect(context.Background(), operation); err != nil {
				t.Error(err)
			}
		}(index)
	}
	// 并发 drain 与 enqueue 竞争。
	done := make(chan struct{})
	go func() {
		for index := 0; index < 50; index++ {
			scheduler.Fire()
		}
		close(done)
	}()
	wg.Wait()
	<-done
	service.Flush(context.Background())
	state := service.GetState(0, 0)
	if state.QueueLength != 0 {
		t.Fatalf("queue should drain, state = %+v", state)
	}
	// Node 语义：stale 同时覆盖被拒绝的观测（不计入 enqueuedCount），所以终态
	// 不变量是"已入队项至多被执行或取消一次，队列最终清空"。
	if state.CompletedCount+state.CanceledBySuccessCount > state.EnqueuedCount {
		t.Fatalf("completed+canceled must not exceed enqueued: %+v", state)
	}
}
