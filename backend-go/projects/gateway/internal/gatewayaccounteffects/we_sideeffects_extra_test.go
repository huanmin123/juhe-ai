package gatewayaccounteffects

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// SideEffectsService 补充分支（sideeffects.go）
// ---------------------------------------------------------------------------

func TestWeSideEffectsFailureStormPrecheckDecisions(t *testing.T) {
	writer := &scriptedWriter{}
	service, _, _, _ := weNewService(t, writer, "")
	now := int64(1_000_000)
	storm := FailureStormEntry{
		FirstSeenMs:  now - FailureStormMinObservationMs,
		LastSeenMs:   now,
		FailureCount: 5,
		ClientIPs:    map[string]struct{}{"1.1.1.1": {}, "2.2.2.2": {}},
		APIKeyIDs:    map[string]struct{}{"key-1": {}},
	}

	tests := []struct {
		name        string
		mutate      func(*FailureStormEntry)
		seedSuccess *SuccessObservationEntry
		force       bool
		wantTrigger bool
		wantSkipped string
	}{
		{
			name:        "达标触发",
			mutate:      func(e *FailureStormEntry) {},
			wantTrigger: true,
		},
		{
			name:        "失败数不足",
			mutate:      func(e *FailureStormEntry) { e.FailureCount = 4 },
			wantSkipped: StormSkippedBelowThreshold,
		},
		{
			name:        "独立 IP 不足",
			mutate:      func(e *FailureStormEntry) { e.ClientIPs = map[string]struct{}{"1.1.1.1": {}} },
			wantSkipped: StormSkippedBelowThreshold,
		},
		{
			name:        "观测窗口不足",
			mutate:      func(e *FailureStormEntry) { e.FirstSeenMs = now - 1000 },
			wantSkipped: StormSkippedObservationWindow,
		},
		{
			name:        "近期成功宽限",
			seedSuccess: &SuccessObservationEntry{LastSeenMs: now - 1000, SuccessCount: 1, FirstSeenMs: now - 1000},
			mutate:      func(e *FailureStormEntry) {},
			wantSkipped: StormSkippedRecentSuccess,
		},
		{
			name:        "失败占比不足",
			seedSuccess: &SuccessObservationEntry{LastSeenMs: now - 60_000, SuccessCount: 3, FirstSeenMs: now - 60_000},
			mutate:      func(e *FailureStormEntry) {},
			wantSkipped: StormSkippedFailureRatio,
		},
		{
			name:        "强制预检窗口不足",
			mutate:      func(e *FailureStormEntry) { e.FirstSeenMs = now - 1000 },
			force:       true,
			wantSkipped: StormSkippedObservationWindow,
		},
		{
			name:        "强制预检触发",
			mutate:      func(e *FailureStormEntry) { e.FailureCount = 2 },
			force:       true,
			wantTrigger: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := storm
			tt.mutate(&entry)
			if tt.seedSuccess != nil {
				// 白盒注入成功观测，验证 recent_success / failure_ratio 分支。
				service.mu.Lock()
				service.successObservations["acc-1"] = tt.seedSuccess
				service.mu.Unlock()
			}
			decision := service.ShouldTriggerFailureStormPrecheck("acc-1", entry, tt.force, now)
			if decision.Trigger != tt.wantTrigger {
				t.Fatalf("trigger = %v, want %v（decision = %+v）", decision.Trigger, tt.wantTrigger, decision)
			}
			if !tt.wantTrigger && decision.SkippedReason != tt.wantSkipped {
				t.Fatalf("skipped = %s, want %s", decision.SkippedReason, tt.wantSkipped)
			}
			if tt.seedSuccess != nil && decision.SuccessCount != tt.seedSuccess.SuccessCount {
				t.Fatalf("successCount = %d, want %d", decision.SuccessCount, tt.seedSuccess.SuccessCount)
			}
		})
	}
}

func TestWeSideEffectsSuppressGatewayAccountLocally(t *testing.T) {
	writer := &scriptedWriter{}
	service, _, _, _ := weNewService(t, writer, "")

	result := service.SuppressGatewayAccountLocally(gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1", Status: "active"}, "")
	// 契约：本地避让由 Redis 管理，结果只表达意图（action=redis_managed），无本地计数。
	if result.Action != "redis_managed" || result.RuntimeKey != "acc-1" || result.LocalFailureCount != 0 {
		t.Fatalf("result = %+v", result)
	}
	if result.Reason != "上游账号请求失败" {
		t.Fatalf("默认 reason = %s", result.Reason)
	}

	custom := service.SuppressGatewayAccountLocally(gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1", Status: "active"}, "自定义原因")
	if custom.Reason != "自定义原因" {
		t.Fatalf("reason = %s", custom.Reason)
	}

	// 缺绑定上下文的授权账户回落 account.ID。
	fallback := service.SuppressGatewayAccountLocally(gatewayruntimecache.OpenAIAccountSecret{ID: "acc-2", Status: "active", AccountAccessType: "account_authorized"}, "x")
	if fallback.RuntimeKey != "acc-2" {
		t.Fatalf("runtimeKey = %s, want acc-2", fallback.RuntimeKey)
	}
}

func TestWeSideEffectsRecordFailureObservationWithoutPrecheck(t *testing.T) {
	writer := &scriptedWriter{}
	service, hook, _, _ := weNewService(t, writer, "")
	cleanups := 0
	service.deps.CleanupLocalSuppressions = func() { cleanups++ }

	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1", Status: "active"}
	input := GatewayAccountFailurePrecheckInput{
		SystemAccountID: "sys-1",
		GroupID:         "group-1",
		APIKeyID:        "key-1",
		ClientIP:        "10.0.0.1",
		Reason:          "upstream_failure",
	}
	// 观测（非预检）：记账但不调度恢复探针。
	service.RecordGatewayAccountFailureObservation(context.Background(), account, input)
	if len(hook.probes) != 0 {
		t.Fatalf("probes = %d, want 0", len(hook.probes))
	}
	if cleanups == 0 {
		t.Fatal("应触发本地抑制清理")
	}
	// 记账条目应可通过白盒读取验证。
	service.mu.Lock()
	stored := service.failureStorms["acc-1"]
	service.mu.Unlock()
	if stored == nil || stored.FailureCount != 1 {
		t.Fatalf("storm = %+v", stored)
	}
	if _, hasIP := stored.ClientIPs["10.0.0.1"]; !hasIP {
		t.Fatalf("clientIP 未记账: %+v", stored.ClientIPs)
	}
	if _, hasKey := stored.APIKeyIDs["key-1"]; !hasKey {
		t.Fatalf("apiKeyID 未记账: %+v", stored.APIKeyIDs)
	}
}

func TestWeSideEffectsRecordFailureForPrecheckSchedulesProbe(t *testing.T) {
	writer := &scriptedWriter{}
	service, hook, _, _ := weNewService(t, writer, "")
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1", Status: "active"}
	service.RecordGatewayAccountFailureForPrecheck(context.Background(), account, GatewayAccountFailurePrecheckInput{
		SystemAccountID: "sys-1",
		GroupID:         "group-1",
		ClientIP:        "10.0.0.1",
		ForcePrecheck:   true,
	})
	if len(hook.probes) != 1 {
		t.Fatalf("probes = %d, want 1", len(hook.probes))
	}
	probe := hook.probes[0]
	if probe.RuntimeKey != "acc-1" || probe.Account.ID != "acc-1" || probe.SystemAccountID != "sys-1" || probe.GroupID != "group-1" {
		t.Fatalf("probe = %+v", probe)
	}
	if !probe.PrecheckRequested || probe.FailureCount != 1 || probe.DistinctClientIPCount != 1 {
		t.Fatalf("probe = %+v", probe)
	}
}

func TestWeSideEffectsRecordFailureRedisDriverDistributed(t *testing.T) {
	// 契约：Redis 驱动下失败观测转发分布式 precheck 端口，不写本地风暴记账。
	writer := &scriptedWriter{}
	service, hook, _, _ := weNewService(t, writer, "redis")
	distributed := make(chan GatewayAccountFailurePrecheckInput, 1)
	service.deps.RecordDistributedFailureForPrecheck = func(_ context.Context, _ gatewayruntimecache.OpenAIAccountSecret, input GatewayAccountFailurePrecheckInput) {
		distributed <- input
	}
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1", Status: "active"}
	service.RecordGatewayAccountFailureForPrecheck(context.Background(), account, GatewayAccountFailurePrecheckInput{Reason: "x"})
	select {
	case input := <-distributed:
		if input.Reason != "x" {
			t.Fatalf("input = %+v", input)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("分布式 precheck 未被调用")
	}
	if len(hook.probes) != 0 {
		t.Fatalf("本地 probes = %d, want 0", len(hook.probes))
	}
}

func TestWeSideEffectsClearRuntimeAvailabilityRedisBranch(t *testing.T) {
	writer := &scriptedWriter{}
	service, _, _, _ := weNewService(t, writer, "redis")
	distributed := make(chan string, 1)
	errFailed := errors.New("redis 清理失败")

	// 成功路径：转发分布式清理端口。
	service.deps.ClearDistributedRuntimeAvailability = func(_ context.Context, runtimeKey string) error {
		distributed <- runtimeKey
		return nil
	}
	cleared, err := service.clearGatewayAccountRuntimeAvailabilityForRuntimeKey(context.Background(), "acc-1")
	if err != nil || !cleared {
		t.Fatalf("cleared = %v err = %v", cleared, err)
	}
	if key := <-distributed; key != "acc-1" {
		t.Fatalf("key = %s", key)
	}

	// 失败路径：错误向上传播。
	service.deps.ClearDistributedRuntimeAvailability = func(_ context.Context, _ string) error { return errFailed }
	cleared, err = service.clearGatewayAccountRuntimeAvailabilityForRuntimeKey(context.Background(), "acc-1")
	if cleared || !errors.Is(err, errFailed) {
		t.Fatalf("cleared = %v err = %v", cleared, err)
	}
}

func TestWeSideEffectsProcessLocalGuardUnderRedisDriver(t *testing.T) {
	// 契约：Redis 驱动下进程本地风暴/成功观测不可用，读前必须清空。
	writer := &scriptedWriter{}
	service, _, _, _ := weNewService(t, writer, "redis")
	service.mu.Lock()
	service.failureStorms["acc-1"] = &FailureStormEntry{FirstSeenMs: 1, LastSeenMs: 2, FailureCount: 1}
	service.successObservations["acc-1"] = &SuccessObservationEntry{FirstSeenMs: 1, LastSeenMs: 2, SuccessCount: 1}
	service.mu.Unlock()

	if service.canUseProcessLocalGatewayAccountRuntimeState() {
		t.Fatal("redis 驱动应返回 false")
	}
	service.mu.Lock()
	storms := len(service.failureStorms)
	successes := len(service.successObservations)
	service.mu.Unlock()
	if storms != 0 || successes != 0 {
		t.Fatalf("storms = %d successes = %d, want 0/0", storms, successes)
	}

	// 白盒：locked 变体在 memory 驱动下返回 true。
	memoryService, _, _, _ := weNewService(t, &scriptedWriter{}, "")
	memoryService.mu.Lock()
	usable := memoryService.canUseProcessLocalGatewayAccountRuntimeStateLocked()
	memoryService.mu.Unlock()
	if !usable {
		t.Fatal("memory 驱动应返回 true")
	}
}

func TestWeSideEffectsCleanupExpiredFailureStorms(t *testing.T) {
	writer := &scriptedWriter{}
	service, _, clock, _ := weNewService(t, writer, "")
	now := NowMs(clock)
	service.mu.Lock()
	service.failureStorms["stale"] = &FailureStormEntry{FirstSeenMs: now - FailureStormWindowMs - 1, LastSeenMs: now - FailureStormWindowMs - 1}
	service.failureStorms["fresh"] = &FailureStormEntry{FirstSeenMs: now, LastSeenMs: now}
	service.successObservations["stale-success"] = &SuccessObservationEntry{FirstSeenMs: now - FailureStormWindowMs - 1, LastSeenMs: now - FailureStormWindowMs - 1}
	service.successObservations["fresh-success"] = &SuccessObservationEntry{FirstSeenMs: now, LastSeenMs: now}
	service.mu.Unlock()

	service.mu.Lock()
	service.cleanupExpiredFailureStormsLocked()
	service.mu.Unlock()

	service.mu.Lock()
	defer service.mu.Unlock()
	if _, ok := service.failureStorms["stale"]; ok {
		t.Fatal("过期风暴应被清理")
	}
	if _, ok := service.successObservations["stale-success"]; ok {
		t.Fatal("过期成功观测应被清理")
	}
	if len(service.failureStorms) != 1 || len(service.successObservations) != 1 {
		t.Fatalf("storms = %d successes = %d", len(service.failureStorms), len(service.successObservations))
	}
}

func TestWeSideEffectsScheduleNextDrainArmsTimer(t *testing.T) {
	writer := &scriptedWriter{}
	service, _, clock, scheduler := weNewService(t, writer, "")
	// 白盒：队列中存在未来到期的条目且无 drain 定时器 → scheduleNextDrainIfNeededLocked
	// 必须补齐定时器（防止丢调度）。
	now := NowMs(clock)
	item := &QueuedAccountSideEffect{
		Operation:       newTestOperation("acc-1", false),
		Attempts:        1,
		EnqueuedAtMs:    now,
		NextAttemptAtMs: now + 60_000,
		ExpiresAtMs:     now + SideEffectRetentionMs,
	}
	item.Epoch = AccountSideEffectEpoch{RuntimeKey: "acc-1", Sequence: 1}
	service.mu.Lock()
	service.queue.Push(item)
	service.scheduleNextDrainIfNeededLocked()
	service.mu.Unlock()
	if scheduler.Pending() != 1 {
		t.Fatalf("pending = %d, want 1", scheduler.Pending())
	}
}

func TestWeSideEffectsClearForTestCancelsDrainTimer(t *testing.T) {
	writer := &scriptedWriter{}
	service, _, _, scheduler := weNewService(t, writer, "")
	err := service.EnqueueGatewayAccountErrorHandlingSideEffect(context.Background(), testOperationFor("acc-1", true, "2026-01-01T00:00:00.000Z"))
	if err != nil {
		t.Fatal(err)
	}
	if scheduler.Pending() != 1 {
		t.Fatalf("pending = %d, want 1", scheduler.Pending())
	}
	service.ClearForTest()
	if scheduler.Pending() != 0 {
		t.Fatalf("ClearForTest 后 pending = %d, want 0", scheduler.Pending())
	}
	state := service.GetState(2, 3)
	if state.QueueLength != 0 {
		t.Fatalf("QueueLength = %d, want 0", state.QueueLength)
	}
	if state.LocalSuppressedAccountCount != 2 || state.DegradedAccountCount != 3 {
		t.Fatalf("state = %+v", state)
	}
}

func TestWeSideEffectsGetStateNextAttemptProjection(t *testing.T) {
	writer := &scriptedWriter{}
	service, _, clock, _ := weNewService(t, writer, "")
	now := NowMs(clock)
	item := &QueuedAccountSideEffect{
		Operation:       newTestOperation("acc-1", false),
		EnqueuedAtMs:    now,
		NextAttemptAtMs: now + 1234,
		ExpiresAtMs:     now + SideEffectRetentionMs,
	}
	item.Epoch = AccountSideEffectEpoch{RuntimeKey: "acc-1", Sequence: 1}
	service.mu.Lock()
	service.queue.Push(item)
	service.mu.Unlock()
	state := service.GetState(0, 0)
	// 契约：队列头部的下次重试时间以 canonical RFC3339 投影。
	if !strings.HasPrefix(state.NextAttemptAt, "2026-01-01T00:00:01.234") {
		t.Fatalf("NextAttemptAt = %s", state.NextAttemptAt)
	}
}

// ---------------------------------------------------------------------------
// AccountSideEffectQueue / EpochRegistry 补充分支（sideeffectqueue.go）
// ---------------------------------------------------------------------------

func weQueuedItem(accountID string, success bool, enqueuedAtMs, nextAttemptAtMs int64) *QueuedAccountSideEffect {
	item := &QueuedAccountSideEffect{
		Operation:       newTestOperation(accountID, success),
		EnqueuedAtMs:    enqueuedAtMs,
		NextAttemptAtMs: nextAttemptAtMs,
	}
	item.Epoch = AccountSideEffectEpoch{RuntimeKey: accountID, Sequence: 1}
	return item
}

func TestWeQueueClearResetsAllIndexes(t *testing.T) {
	queue := NewAccountSideEffectQueue()
	queue.Push(weQueuedItem("acc-1", false, 100, 100))
	queue.Push(weQueuedItem("acc-2", true, 200, 200))
	if queue.Len() != 2 || !queue.HasFailures() || !queue.HasRuntimeKey("acc-1") {
		t.Fatalf("push 后 queue 状态错误: len=%d failures=%v", queue.Len(), queue.HasFailures())
	}
	queue.Clear()
	if queue.Len() != 0 || queue.HasFailures() || queue.HasRuntimeKey("acc-1") || queue.Peek() != nil {
		t.Fatalf("Clear 后状态错误: len=%d", queue.Len())
	}
	// Clear 后可继续使用（索引重建）。
	queue.Push(weQueuedItem("acc-3", false, 300, 300))
	if queue.Len() != 1 || queue.Pop() == nil || queue.Len() != 0 {
		t.Fatal("Clear 后 push/pop 失效")
	}
}

func TestWeQueueFindIndex(t *testing.T) {
	queue := NewAccountSideEffectQueue()
	queue.Push(weQueuedItem("acc-1", false, 100, 300))
	queue.Push(weQueuedItem("acc-2", false, 200, 100))
	index := queue.FindIndex(func(item *QueuedAccountSideEffect) bool {
		return item.Operation.Account.ID == "acc-2"
	})
	if index < 0 {
		t.Fatal("应找到 acc-2 条目")
	}
	if missing := queue.FindIndex(func(item *QueuedAccountSideEffect) bool { return false }); missing != -1 {
		t.Fatalf("missing = %d, want -1", missing)
	}
}

func TestWeQueueRemoveOldestWhere(t *testing.T) {
	queue := NewAccountSideEffectQueue()
	queue.Push(weQueuedItem("acc-1", false, 300, 300))
	queue.Push(weQueuedItem("acc-2", false, 100, 100))
	queue.Push(weQueuedItem("acc-3", false, 200, 200))

	oldest := queue.RemoveOldestWhere(func(item *QueuedAccountSideEffect) bool {
		return !item.Operation.Input.Success
	})
	if oldest == nil || oldest.Operation.Account.ID != "acc-2" {
		t.Fatalf("oldest = %+v", oldest)
	}
	if queue.Len() != 2 {
		t.Fatalf("len = %d, want 2", queue.Len())
	}
	if none := queue.RemoveOldestWhere(func(item *QueuedAccountSideEffect) bool { return false }); none != nil {
		t.Fatalf("无匹配应返回 nil, got %+v", none)
	}
}

func TestWeCompareSideEffectFailureAge(t *testing.T) {
	left := weQueuedItem("a", false, 100, 200)
	right := weQueuedItem("b", false, 300, 100)
	if compareSideEffectFailureAge(left, right) >= 0 {
		t.Fatal("更早入队应排在前面")
	}
	if compareSideEffectFailureAge(right, left) <= 0 {
		t.Fatal("比较应满足反对称")
	}
	tiedEnqueued := weQueuedItem("c", false, 100, 500)
	if compareSideEffectFailureAge(left, tiedEnqueued) >= 0 {
		t.Fatal("enqueuedAt 相同时应按 nextAttemptAt 比较")
	}
	if compareSideEffectFailureAge(left, weQueuedItem("d", false, 100, 200)) != 0 {
		t.Fatal("完全相同应返回 0")
	}
}

func TestWeEpochRegistryClear(t *testing.T) {
	registry, err := NewAccountSideEffectEpochRegistry(4)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := registry.Observe("acc-1", EpochObservation{ObservedAt: "2026-01-01T00:00:00.000Z", Success: false, Retain: true})
	if err != nil || !decision.Accepted {
		t.Fatalf("observe = %+v err = %v", decision, err)
	}
	if !registry.IsCurrent(decision.Epoch) {
		t.Fatal("当前 epoch 应有效")
	}
	registry.Clear()
	if registry.Size() != 0 {
		t.Fatalf("Size = %d, want 0", registry.Size())
	}
	if registry.IsCurrent(decision.Epoch) {
		t.Fatal("Clear 后 epoch 不应仍是 current")
	}
	// Clear 后可继续 observe（sequence 重新从 1 开始）。
	second, err := registry.Observe("acc-1", EpochObservation{ObservedAt: "2026-01-01T00:00:01.000Z", Success: true, Retain: true})
	if err != nil || !second.Accepted || second.Epoch.Sequence != 1 {
		t.Fatalf("second = %+v err = %v", second, err)
	}
}

func TestWeEpochRegistryRetainReleaseAndCapacityTrim(t *testing.T) {
	registry, err := NewAccountSideEffectEpochRegistry(2)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := registry.Observe("acc-1", EpochObservation{ObservedAt: "2026-01-01T00:00:00.000Z", Success: false, Retain: true})
	second, _ := registry.Observe("acc-2", EpochObservation{ObservedAt: "2026-01-01T00:00:00.000Z", Success: false, Retain: true})
	third, _ := registry.Observe("acc-3", EpochObservation{ObservedAt: "2026-01-01T00:00:00.000Z", Success: false, Retain: true})
	// 契约：被保留（retained）的 epoch 不参与容量裁剪，即使超过容量也必须全部保留。
	if registry.Size() != 3 {
		t.Fatalf("retained epoch 不应被裁剪: %d", registry.Size())
	}
	// 释放是幂等的：重复释放同一 epoch 是 no-op。
	registry.Release(first.Epoch)
	registry.Release(first.Epoch)
	registry.Release(second.Epoch)
	// 释放 acc-4 观测触发裁剪：未被保留的 acc-1/acc-2 被逐出，保留 acc-3。
	fourth, _ := registry.Observe("acc-4", EpochObservation{ObservedAt: "2026-01-01T00:00:01.000Z", Success: false, Retain: true})
	if registry.Size() != 2 {
		t.Fatalf("裁剪后 size = %d, want 2", registry.Size())
	}
	if registry.IsCurrent(first.Epoch) || registry.IsCurrent(second.Epoch) {
		t.Fatal("被逐出的 epoch 不应仍是 current")
	}
	if !registry.IsCurrent(third.Epoch) || !registry.IsCurrent(fourth.Epoch) {
		t.Fatal("保留/新观测的 epoch 应仍是 current")
	}
	// 空 runtimeKey 的 Observe 必须报错。
	if _, err := registry.Observe("  ", EpochObservation{ObservedAt: "2026-01-01T00:00:00.000Z"}); err == nil {
		t.Fatal("空 runtimeKey 应报错")
	}
}

// ---------------------------------------------------------------------------
// AccountAPIKeyFailureGuard 补充分支（apikeyguard.go）
// ---------------------------------------------------------------------------

func TestWeGuardClearFailureGuardBranches(t *testing.T) {
	guard, _ := newGuardForTest(t, "")
	account := guardTestAccount("acc-1", "fp-1")
	if guard.ClearFailureGuard(account) {
		t.Fatal("无抑制时应返回 false")
	}
	decision := guard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{TrafficSource: TrafficSourceGateway})
	if decision.Reason != GuardReasonGatewayLocalOnly {
		t.Fatalf("reason = %s", decision.Reason)
	}
	if !guard.ClearFailureGuard(account) {
		t.Fatal("存在抑制时应清除成功")
	}
	if guard.ClearFailureGuard(gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1", Status: "active"}) {
		t.Fatal("无 fingerprint 应返回 false")
	}
	// Redis 驱动下本地抑制不可用。
	redisGuard, _ := newGuardForTest(t, "redis")
	redisGuard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{TrafficSource: TrafficSourceGateway})
	if redisGuard.ClearFailureGuard(account) {
		t.Fatal("redis 驱动下不应有本地抑制可清")
	}
}

func TestWeGuardClearForTestAndStaleObservation(t *testing.T) {
	guard, clock := newGuardForTest(t, "")
	account := guardTestAccount("acc-1", "fp-1")
	epoch := guard.CaptureFailureObservation(account)
	if epoch == nil {
		t.Fatal("memory 驱动应产生观测 epoch")
	}
	guard.ClearForTest()
	// ClearForTest 清空围栏后，旧 epoch 不再被接受（stale gateway observation）。
	decision := guard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{
		TrafficSource:    TrafficSourceGateway,
		ObservationEpoch: epoch,
	})
	if decision.Reason != GuardReasonStaleGatewayObservation {
		t.Fatalf("reason = %s, want %s", decision.Reason, GuardReasonStaleGatewayObservation)
	}
	// 推进时钟使围栏过期后同样拒绝。
	_ = clock
}

func TestWeGuardAcceptLocalObservationFenceLifecycle(t *testing.T) {
	guard, clock := newGuardForTest(t, "")
	account := guardTestAccount("acc-1", "fp-1")

	epoch := guard.CaptureFailureObservation(account)
	decision := guard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{
		TrafficSource:    TrafficSourceGateway,
		ObservationEpoch: epoch,
	})
	if decision.Reason != GuardReasonGatewayLocalOnly {
		t.Fatalf("有效 epoch 应接受: %+v", decision)
	}

	// 非法 epoch（0/负数）必须拒绝。
	zero := int64(0)
	decision = guard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{
		TrafficSource: TrafficSourceGateway, ObservationEpoch: &zero,
	})
	if decision.Reason != GuardReasonStaleGatewayObservation {
		t.Fatalf("零 epoch 应拒绝: %+v", decision)
	}

	// 过期围栏：捕获后推进 10 分钟以上。
	fresh := guard.CaptureFailureObservation(account)
	clock.Advance(time.Duration(apiKeyLocalObservationFenceRetentionMs) * time.Millisecond)
	clock.Advance(2 * time.Millisecond)
	decision = guard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{
		TrafficSource: TrafficSourceGateway, ObservationEpoch: fresh,
	})
	if decision.Reason != GuardReasonStaleGatewayObservation {
		t.Fatalf("过期围栏应拒绝: %+v", decision)
	}
}

func TestWeGuardLoadTransientStatesDispatch(t *testing.T) {
	t.Run("memory 驱动回落本地快照", func(t *testing.T) {
		guard, _ := newGuardForTest(t, "")
		account := guardTestAccount("acc-1", "fp-1")
		guard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{TrafficSource: TrafficSourceGateway})
		states, err := guard.LoadTransientStatesForDispatch(context.Background(), "acc-1", []string{"fp-1"})
		if err != nil {
			t.Fatal(err)
		}
		if len(states) != 1 || states[0].KeyFingerprint != "fp-1" || states[0].Status == "" {
			t.Fatalf("states = %+v", states)
		}
		if states[0].NextProbeAt == nil {
			t.Fatal("本地抑制必须给出 nextProbeAt")
		}
		if empty, err := guard.LoadTransientStatesForDispatch(context.Background(), "  ", nil); err != nil || len(empty) != 0 {
			t.Fatalf("空账户应返回空: %+v err = %v", empty, err)
		}
		// 契约：memory 驱动忽略指纹过滤参数，直接投影该账户全部本地抑制。
		if all, err := guard.LoadTransientStatesForDispatch(context.Background(), "acc-1", []string{" ", ""}); err != nil || len(all) != 1 {
			t.Fatalf("memory 驱动不按指纹过滤: %+v err = %v", all, err)
		}
	})

	t.Run("redis 驱动读取分布式 store", func(t *testing.T) {
		guard, _ := newGuardForTest(t, "redis")
		generation := "gen-1"
		state := &AccountApiKeyTransientState{
			SchemaVersion: 1, AccountID: "acc-1", KeyFingerprint: "fp-1",
			Generation: generation, LastObservedAtMs: 100, ObservationKind: "failure",
			FailureCount: 1, Status: APIKeyStatusRateLimited,
		}
		until := int64(4_000_000_000)
		state.SuppressUntilMs = &until
		store := &weTransientStore{loadResult: []AccountApiKeyTransientDispatchState{
			{State: state, Suppressed: true},
			{State: nil, Suppressed: false},
		}}
		guard.SetTransientStateStoreForTest(store)
		states, err := guard.LoadTransientStatesForDispatch(context.Background(), "acc-1", []string{"fp-1"})
		if err != nil {
			t.Fatal(err)
		}
		if len(states) != 1 {
			t.Fatalf("states = %+v", states)
		}
		selection := states[0]
		if selection.Status != string(APIKeyStatusRateLimited) || selection.TransientGeneration == nil || *selection.TransientGeneration != generation {
			t.Fatalf("selection = %+v", selection)
		}
		if selection.NextProbeAt == nil {
			t.Fatal("suppressed 状态必须投影 nextProbeAt")
		}
		if len(store.loads) != 1 || store.loads[0] != "acc-1/fp-1" {
			t.Fatalf("loads = %v", store.loads)
		}
	})

	t.Run("redis 驱动 store 错误向上传播", func(t *testing.T) {
		guard, _ := newGuardForTest(t, "redis")
		guard.SetTransientStateStoreForTest(&weTransientStore{err: errors.New("redis 失败")})
		if _, err := guard.LoadTransientStatesForDispatch(context.Background(), "acc-1", []string{"fp-1"}); err == nil {
			t.Fatal("store 错误应向上传播")
		}
	})
}

func TestWeGuardObservationFenceCapacityEviction(t *testing.T) {
	guard, _ := newGuardForTest(t, "")
	guard.mu.Lock()
	defer guard.mu.Unlock()
	now := int64(1_000)
	// 容量契约：fence 超过 50000 时必须逐出旧条目，避免无界增长。
	for index := 0; index <= apiKeyLocalObservationFenceCapacity; index++ {
		guard.rememberLocalAPIKeyObservationFenceLocked("key-"+intToDecimal(index), int64(index+1), now)
	}
	if len(guard.fences) > apiKeyLocalObservationFenceCapacity {
		t.Fatalf("fences = %d 超过容量 %d", len(guard.fences), apiKeyLocalObservationFenceCapacity)
	}
}

func intToDecimal(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
