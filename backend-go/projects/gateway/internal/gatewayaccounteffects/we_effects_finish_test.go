package gatewayaccounteffects

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// weGateWriter 先报告已进入执行、再等待放行闸门，用于构造“执行期间状态变化”。
type weGateWriter struct {
	entered chan struct{}
	gate    chan struct{}

	mu    sync.Mutex
	calls int
}

func (w *weGateWriter) ApplyAccountErrorHandling(context.Context, AccountSideEffectOperation) (AccountErrorHandlingResult, error) {
	w.mu.Lock()
	w.calls++
	w.mu.Unlock()
	w.entered <- struct{}{}
	<-w.gate
	return AccountErrorHandlingResult{}, errors.New("注入的写入失败")
}

func (w *weGateWriter) callCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls
}

// ---------------------------------------------------------------------------
// Drain 执行期间状态变化：过期、epoch 失效、重入
// ---------------------------------------------------------------------------

func TestWeDrainExpiresItemBlockedDuringExecute(t *testing.T) {
	writer := &weGateWriter{entered: make(chan struct{}, 1), gate: make(chan struct{})}
	service, _, clock, scheduler := weNewServiceFull(t, SideEffectsConfig{}, SideEffectDeps{Writer: writer})
	ctx := context.Background()

	if err := service.EnqueueGatewayAccountErrorHandlingSideEffect(ctx, testOperationFor("acc-1", false, "2026-01-01T00:00:00.000Z")); err != nil {
		t.Fatal(err)
	}
	fireDone := make(chan struct{})
	go func() { scheduler.Fire(); close(fireDone) }()
	<-writer.entered
	// 执行期间时钟越过保留窗口：失败后的过期检查必须丢弃条目而不是安排重试。
	clock.Advance(time.Duration(SideEffectRetentionMs)*time.Millisecond + 2*time.Millisecond)
	// 排水重入是 no-op（single-flight 契约）。
	service.Drain(ctx)
	close(writer.gate)
	<-fireDone

	state := service.GetState(0, 0)
	if state.FailedAttemptCount != 1 || state.ExpiredCount != 1 || state.QueueLength != 0 {
		t.Fatalf("state = %+v", state)
	}
	if writer.callCount() != 1 {
		t.Fatalf("writer calls = %d, want 1", writer.callCount())
	}
}

func TestWeDrainStaleEpochAfterFailedExecute(t *testing.T) {
	writer := &weGateWriter{entered: make(chan struct{}, 1), gate: make(chan struct{})}
	service, _, _, scheduler := weNewServiceFull(t, SideEffectsConfig{}, SideEffectDeps{Writer: writer})
	ctx := context.Background()

	if err := service.EnqueueGatewayAccountErrorHandlingSideEffect(ctx, testOperationFor("acc-1", false, "2026-01-01T00:00:01.000Z")); err != nil {
		t.Fatal(err)
	}
	fireDone := make(chan struct{})
	go func() { scheduler.Fire(); close(fireDone) }()
	<-writer.entered
	// 执行期间同一 runtime key 出现更新的成功观测：旧 epoch 失效。
	if err := service.EnqueueGatewayAccountErrorHandlingSideEffect(ctx, testOperationFor("acc-1", true, "2026-01-01T00:00:02.000Z")); err != nil {
		t.Fatal(err)
	}
	close(writer.gate)
	<-fireDone

	state := service.GetState(0, 0)
	// 旧 epoch 的失败执行记为 stale；随后入队的成功观测被同样失败的 writer
	// 执行并按重试排队（QueueLength 1）。
	if state.StaleCount != 1 || state.FailedAttemptCount != 2 || state.QueueLength != 1 {
		t.Fatalf("state = %+v", state)
	}
}

func TestWeEnqueueStaleObservationRejected(t *testing.T) {
	service, _, _, _ := weNewServiceFull(t, SideEffectsConfig{}, SideEffectDeps{Writer: &scriptedWriter{}})
	ctx := context.Background()
	// 先接受较新的成功观测，再提交更旧的失败观测：watermark 判定为 stale。
	if err := service.EnqueueGatewayAccountErrorHandlingSideEffect(ctx, testOperationFor("acc-1", true, "2026-01-01T00:00:02.000Z")); err != nil {
		t.Fatal(err)
	}
	if err := service.EnqueueGatewayAccountErrorHandlingSideEffect(ctx, testOperationFor("acc-1", false, "2026-01-01T00:00:01.000Z")); err != nil {
		t.Fatal(err)
	}
	state := service.GetState(0, 0)
	if state.StaleCount != 1 {
		t.Fatalf("StaleCount = %d, want 1", state.StaleCount)
	}
}

func TestWeFlushCancelsPendingDrainTimer(t *testing.T) {
	writer := &scriptedWriter{}
	service, _, _, scheduler := weNewServiceFull(t, SideEffectsConfig{}, SideEffectDeps{Writer: writer})
	if err := service.EnqueueGatewayAccountErrorHandlingSideEffect(context.Background(), testOperationFor("acc-1", true, "2026-01-01T00:00:00.000Z")); err != nil {
		t.Fatal(err)
	}
	if scheduler.Pending() != 1 {
		t.Fatalf("pending = %d, want 1", scheduler.Pending())
	}
	// Flush 直接排水到队列清空并取消挂起定时器。
	service.Flush(context.Background())
	if scheduler.Pending() != 0 {
		t.Fatalf("Flush 后 pending = %d, want 0", scheduler.Pending())
	}
	state := service.GetState(0, 0)
	if state.CompletedCount != 1 {
		t.Fatalf("state = %+v", state)
	}
}

func TestWeRedisDriverGetStateAndObservationFallbackKey(t *testing.T) {
	service, _, _, _ := weNewServiceFull(t, SideEffectsConfig{RuntimeStateDriver: "redis"}, SideEffectDeps{Writer: &scriptedWriter{}})
	state := service.GetState(0, 0)
	if state.PrecheckPendingAccountCount != 0 || state.RecoveryProbePendingAccountCount != 0 {
		t.Fatalf("redis 驱动下本地计数应归零: %+v", state)
	}
	// memory 驱动下缺绑定上下文的授权账户：风暴记账回落 account.ID。
	memoryService, _, _, _ := weNewServiceFull(t, SideEffectsConfig{}, SideEffectDeps{Writer: &scriptedWriter{}})
	invalid := gatewayruntimecache.OpenAIAccountSecret{ID: "acc-invalid", Status: "active", AccountAccessType: "account_authorized"}
	memoryService.RecordGatewayAccountFailureForPrecheck(context.Background(), invalid, GatewayAccountFailurePrecheckInput{Reason: "x"})
	memoryService.mu.Lock()
	_, exists := memoryService.failureStorms["acc-invalid"]
	memoryService.mu.Unlock()
	if !exists {
		t.Fatal("应回落 account.ID 记账")
	}
}

// ---------------------------------------------------------------------------
// apikeyeffects.go：合并定时器自然触发与异步失败写错误
// ---------------------------------------------------------------------------

func TestWeAPIKeyEffectsCoalesceTimerFiresFlush(t *testing.T) {
	writer := &weAPIKeyWriter{successResult: APIKeyWriteResult{Changed: true}, successDone: make(chan struct{}, 1)}
	guard := NewAccountAPIKeyFailureGuard(SideEffectsConfig{}, nil, nil, nil)
	invalidations := 0
	effects, scheduler, _ := weNewEffects(t, "", writer, guard, func() { invalidations++ })
	effects.RecordSuccess(context.Background(), weAPIKeyAccount("acc-1"), RecordSuccessInput{
		Source: "probe", TrafficSource: string(TrafficSourceAccountHealthCheck), MutationContext: weAutomaticSuccessContext(),
	})
	if scheduler.Pending() != 1 {
		t.Fatalf("合并定时器应挂起, pending = %d", scheduler.Pending())
	}
	// 到点自然触发（不经 FlushSuccessWritesForTest）。
	scheduler.Fire()
	if writer.successCount() != 1 {
		t.Fatalf("success writes = %d, want 1", writer.successCount())
	}
	if invalidations != 1 {
		t.Fatalf("invalidations = %d, want 1", invalidations)
	}
}

func TestWeAPIKeyEffectsAsyncWriteErrorSwallowed(t *testing.T) {
	writer := &weAPIKeyWriter{failureErr: errors.New("db 失败"), failureDone: make(chan struct{}, 1)}
	guard := NewAccountAPIKeyFailureGuard(SideEffectsConfig{RuntimeStateDriver: "redis"}, nil, nil, nil)
	invalidations := 0
	effects, _, _ := weNewEffects(t, "redis", writer, guard, func() { invalidations++ })
	effects.RecordFailure(context.Background(), weAPIKeyAccount("acc-1"), RecordFailureInput{
		Status:          APIKeyStatusRateLimited,
		TrafficSource:   TrafficSourceGateway,
		MutationContext: weExplicitFailureContext(),
	})
	select {
	case <-writer.failureDone:
	case <-time.After(5 * time.Second):
		t.Fatal("异步写入未完成")
	}
	if invalidations != 0 {
		t.Fatalf("失败写入不应失效缓存: %d", invalidations)
	}
}

// ---------------------------------------------------------------------------
// keymodelattempt.go：MainProbe 围栏写失败与 Redis store 选择缓存
// ---------------------------------------------------------------------------

// weMainProbeErrorStore 让 RecordMainProbeFailure 失败。
type weMainProbeErrorStore struct {
	*mockKeyModelStore
}

func (s *weMainProbeErrorStore) RecordMainProbeFailure(context.Context, CapabilityKey, KeyModelForegroundPermit) error {
	return errors.New("围栏写入失败")
}

func TestWeAttemptMainProbeFenceFailureSettlesSafely(t *testing.T) {
	store := &weMainProbeErrorStore{mockKeyModelStore: newMockKeyModelStore()}
	route := GatewayKeyModelCapability{AccountID: "acc-1", Capability: testCapability(), IsMainProbe: true}
	attempt, preparation, scheduler := prepareAttemptForTest(t, store, route, NewGatewayKeyModelFailureBudget())
	if preparation.Status != AttemptPreparationAdmitted {
		t.Fatalf("preparation = %+v", preparation)
	}
	// MainProbe 上游未完成 + 围栏写失败：安全释放并以 nil 结束（不阻塞请求路径）。
	if err := attempt.ReportUpstreamNotComplete(context.Background()); err != nil {
		t.Fatalf("err = %v", err)
	}
	select {
	case <-attempt.WaitTerminal():
	default:
		t.Fatal("应有终态信号")
	}
	scheduler.Fire()
}

func TestWeSelectorCachesRedisStore(t *testing.T) {
	redisStore, err := NewRedisKeyModelRuntimeStore(KeyModelRedisStoreOptions{RedisURL: "redis://localhost:6379/0", Namespace: "we-cache"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = redisStore.Close() })
	calls := 0
	selector := NewKeyModelRuntimeStoreSelector(nil, func() (*RedisKeyModelRuntimeStore, error) {
		calls++
		return redisStore, nil
	})
	first, err := selector.Select("redis")
	if err != nil {
		t.Fatal(err)
	}
	second, err := selector.Select("redis")
	if err != nil || first != second {
		t.Fatalf("redis store 应缓存复用: %v vs %v err = %v", first, second, err)
	}
	if calls != 1 {
		t.Fatalf("factory 调用 = %d, want 1", calls)
	}
}

func TestWeAttemptReleaseAfterReleasedIsNoop(t *testing.T) {
	store := newMockKeyModelStore()
	route := GatewayKeyModelCapability{AccountID: "acc-1", Capability: testCapability()}
	attempt, _, _ := prepareAttemptForTest(t, store, route, NewGatewayKeyModelFailureBudget())
	if err := attempt.ReportCompleteSuccess(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 已释放后的重复 release 是幂等 no-op。
	if err := attempt.release(); err != nil {
		t.Fatalf("release = %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.releases) != 1 {
		t.Fatalf("releases = %d, want 1", len(store.releases))
	}
}

// ---------------------------------------------------------------------------
// apikeyguard.go / keymodelmemory.go 精补分支
// ---------------------------------------------------------------------------

func TestWeGuardRejectsEmptyAccountIDTarget(t *testing.T) {
	guard, _ := newGuardForTest(t, "")
	// 有指纹但账户 ID 为空：target 无效，按未选择 key 处理。
	account := gatewayruntimecache.OpenAIAccountSecret{Status: "active", SelectedAPIKeyFingerprint: stringPtr("fp-1")}
	decision := guard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{TrafficSource: TrafficSourceGateway})
	if decision.Reason != GuardReasonNotSelectedAPIKey {
		t.Fatalf("reason = %s", decision.Reason)
	}
}

func TestWeMemoryMainProbeRemovesActivePermit(t *testing.T) {
	store := NewInMemoryKeyModelRuntimeStore(nil)
	ctx := context.Background()
	capability := testCapability()
	hash := mustCapabilityHash(capability)
	admission, err := store.AdmitForeground(ctx, capability, "attempt-1")
	if err != nil || admission.Permit == nil {
		t.Fatalf("admit = %+v err = %v", admission, err)
	}
	if err := store.RecordMainProbeFailure(ctx, capability, *admission.Permit); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	remaining := len(store.permits[hash])
	wakes := store.wakes[hash]
	store.mu.Unlock()
	// 契约：MainProbe 围栏写入会顺带移除该 attempt 的前台许可并递增 wake。
	if remaining != 0 || wakes != 1 {
		t.Fatalf("permits = %d wakes = %d", remaining, wakes)
	}
}

func TestWeMemoryRecordFailureStaleRevisionAndCapacity(t *testing.T) {
	capability := testCapability()
	hash := mustCapabilityHash(capability)

	// 已有更高 dispatchRevision 的状态：失败写入判 stale。
	staleStore := NewInMemoryKeyModelRuntimeStore(nil)
	staleStore.mu.Lock()
	staleStore.states[hash] = KeyModelState{
		CapabilityKey:  CapabilityKey{CredentialSourceAccountID: "source-1", KeyFingerprint: "fp-1", ClientModel: "gpt-test", ClientEndpointFamily: "chat_completions", FinalUpstreamModel: "gpt-upstream", UpstreamEndpointMode: "chat_json", DispatchRevision: 5},
		CapabilityHash: hash, Phase: KeyModelPhaseOpen,
	}
	staleStore.mu.Unlock()
	result, err := staleStore.RecordFailure(context.Background(), memoryIntent(capability, "i-1", 1_000))
	if err != nil || result.Status != KeyModelMutationStale {
		t.Fatalf("result = %+v err = %v", result, err)
	}

	// 状态容量打满：新 capability 的失败写入返回 capacity_exhausted。
	fullStore := NewInMemoryKeyModelRuntimeStore(nil)
	fullStore.mu.Lock()
	for index := 0; index < keyModelStateCapacity; index++ {
		fullStore.states["hash-"+intToDecimal(index)] = KeyModelState{CapabilityHash: "hash", Phase: KeyModelPhaseOpen}
	}
	fullStore.mu.Unlock()
	exhausted, err := fullStore.RecordFailure(context.Background(), memoryIntent(testCapability(), "i-2", 1_000))
	if err != nil || exhausted.Status != "capacity_exhausted" {
		t.Fatalf("exhausted = %+v err = %v", exhausted, err)
	}

	// 非法 RecoveryTarget：写入报错。
	badTargetStore := NewInMemoryKeyModelRuntimeStore(nil)
	intent := memoryIntent(capability, "i-3", 1_000)
	intent.RecoveryTarget = &KeyModelRecoveryTarget{AccountID: "only-account"}
	if _, err := badTargetStore.RecordFailure(context.Background(), intent); err == nil {
		t.Fatal("非法 RecoveryTarget 应报错")
	}

	// SettleRecovery 对非法 capability 返回 stale + 空状态。
	status, state := badTargetStore.SettleRecovery(MemoryRecoverySettleInput{Capability: CapabilityKey{}, NowMs: 1})
	if status != KeyModelMutationStale || state.CapabilityHash != "" {
		t.Fatalf("status = %s state = %+v", status, state)
	}
}

// ---------------------------------------------------------------------------
// sideeffectqueue / sideeffectpolicy / runtimekeys / clock / policyavoidance 精补
// ---------------------------------------------------------------------------

func TestWeQueueMiscBranches(t *testing.T) {
	queue := NewAccountSideEffectQueue()
	if item := queue.Pop(); item != nil {
		t.Fatalf("空队列 Pop = %+v", item)
	}
	if removed := queue.RemoveRuntimeKey("missing"); len(removed) != 0 {
		t.Fatalf("缺失 key 应返回空: %v", removed)
	}
	queue.Push(weQueuedItem("acc-1", false, 100, 100))
	if replaced := queue.ReplaceAt(-1, weQueuedItem("x", false, 0, 0)); replaced != nil {
		t.Fatal("越界 ReplaceAt 应返回 nil")
	}
	if replaced := queue.ReplaceAt(5, weQueuedItem("x", false, 0, 0)); replaced != nil {
		t.Fatal("越界 ReplaceAt 应返回 nil")
	}
	if removed := queue.RemoveWhereItems(func(*QueuedAccountSideEffect) bool { return false }); len(removed) != 0 {
		t.Fatalf("无匹配应返回空: %v", removed)
	}
	// parseRfc3339Instant：非字符串与可 trim 的输入。
	if _, _, ok := parseRfc3339Instant(123); ok {
		t.Fatal("非字符串应解析失败")
	}
	ms, canonical, ok := parseRfc3339Instant("  2026-01-01T00:00:00.000Z  ")
	if !ok || ms != 1767225600000 || canonical != "2026-01-01T00:00:00.000Z" {
		t.Fatalf("ms = %d canonical = %s ok = %v", ms, canonical, ok)
	}
	// 同 nextAttemptAtMs 时按 enqueuedAtMs 决胜，均相同返回 0。
	left := weQueuedItem("a", false, 100, 200)
	right := weQueuedItem("b", false, 300, 200)
	if compareSideEffectQueueItems(left, right) >= 0 {
		t.Fatal("更早入队应更小")
	}
	if compareSideEffectQueueItems(left, weQueuedItem("c", false, 100, 200)) != 0 {
		t.Fatal("完全相同应返回 0")
	}
}

func TestWeSideEffectPolicyIsQueuedDirect(t *testing.T) {
	invalid := newTestOperation("acc-2", false)
	invalid.Account.AccountAccessType = "account_authorized"
	item := &QueuedAccountSideEffect{Operation: invalid}
	// 队列项自身的 key 推导失败 → 不参与合并。
	if isQueuedAccountErrorHandlingForRuntimeKey(item, "acc-2") {
		t.Fatal("key 推导失败应返回 false")
	}
	valid := &QueuedAccountSideEffect{Operation: newTestOperation("acc-1", false)}
	if !isQueuedAccountErrorHandlingForRuntimeKey(valid, "acc-1") {
		t.Fatal("同 key 应返回 true")
	}
}

func TestWeClearTargetDropsBaseKeyExplicitly(t *testing.T) {
	exclude := false
	keys := (GatewayAccountRuntimeClearTarget{
		AccountID:             "acc-1",
		AuthorizedBinding:     &AuthorizedBinding{SystemAccountID: "s", GroupID: "g", AccountAuthorizationID: "a"},
		IncludeBaseAccountKey: &exclude,
	}).ClearKeys()
	// 契约：仅手工授权实例重置时才显式丢弃基础账户键。
	if len(keys) != 1 || keys[0] != "acc-1:authorized:s:g:a" {
		t.Fatalf("keys = %v", keys)
	}
}

func TestWePassiveScheduleOffsetEdgeInputs(t *testing.T) {
	// 契约：窗口为 0 返回 0；随机值缺失/NaN/负数夹到 0（对称负偏移）；>1 夹到 1；
	// 计算结果恰为 0 时回落 1（保证严格非零延迟）。
	if got := passiveScheduleOffsetWithinWindowMs(0, nil); got != 0 {
		t.Fatalf("零窗口应返回 0, got %d", got)
	}
	tests := []struct {
		name   string
		random func() float64
		want   int64
	}{
		{name: "nil 随机源夹到 0", random: nil, want: -1000},
		{name: "NaN 夹到 0", random: func() float64 { return math.NaN() }, want: -1000},
		{name: "负数夹到 0", random: func() float64 { return -0.5 }, want: -1000},
		{name: "+Inf 与 NaN 同样归零", random: func() float64 { return math.Inf(1) }, want: -1000},
		{name: "大于 1 夹到 1", random: func() float64 { return 2 }, want: 1000},
		{name: "恰好 1", random: func() float64 { return 1 }, want: 1000},
		{name: "对称负偏移", random: func() float64 { return 0.25 }, want: -500},
		{name: "计算为 0 回落 1", random: func() float64 { return 0.5 }, want: 1},
	}
	for _, tt := range tests {
		if got := passiveScheduleOffsetWithinWindowMs(1000, tt.random); got != tt.want {
			t.Fatalf("%s: got %d, want %d", tt.name, got, tt.want)
		}
	}
}

// wePolicyStore 记录 GetJSON/SetJSON 调用的最小 store。
type wePolicyStore struct {
	mu    sync.Mutex
	raws  map[string]json.RawMessage
	reads int
}

func newWePolicyStore() *wePolicyStore {
	return &wePolicyStore{raws: map[string]json.RawMessage{}}
}

func (s *wePolicyStore) GetJSON(_ context.Context, key string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	return s.raws[key], nil
}

func (s *wePolicyStore) SetJSON(_ context.Context, key string, value any, _ int64) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raws[key] = encoded
	return nil
}

func TestWePolicyAvoidanceNegativeCache(t *testing.T) {
	store := newWePolicyStore()
	service := NewConfiguredPolicyAvoidanceService(store, nil, nil, NewFakeClock(time.UnixMilli(1_000_000)))
	ctx := context.Background()
	// 首次读取 miss（store 无数据）→ nil 状态写入负缓存。
	states, err := service.LoadConfiguredPolicyAvoidanceStates(ctx, []string{"acc-1", "acc-2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 || states[0] != nil || states[1] != nil {
		t.Fatalf("states = %+v", states)
	}
	// 第二次读取命中负缓存：不再触达 store。
	if _, err := service.LoadConfiguredPolicyAvoidanceStates(ctx, []string{"acc-1"}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	reads := store.reads
	store.mu.Unlock()
	if reads != 2 {
		t.Fatalf("store 读取 = %d, want 2（每 key 一次，第二次走缓存）", reads)
	}
	// 写入后读取命中正缓存。
	if err := service.SuppressGatewayAccountLocallyForSeconds(ctx, SuppressibleGatewayAccount{ID: "acc-9"}, nil, "原因"); err != nil {
		t.Fatal(err)
	}
	states, err = service.LoadConfiguredPolicyAvoidanceStates(ctx, []string{"acc-9"})
	if err != nil || len(states) != 1 || states[0] == nil || states[0].AccountID != "acc-9" {
		t.Fatalf("states = %+v err = %v", states, err)
	}
}
