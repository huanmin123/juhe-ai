package gatewayaccounteffects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// ---------------------------------------------------------------------------
// keymodelattempt.go 补充分支
// ---------------------------------------------------------------------------

// weReleaseStore 包装任意 KeyModelRuntimeStore，让 ReleaseForeground 可等待、可失败。
type weReleaseStore struct {
	KeyModelRuntimeStore

	mu         sync.Mutex
	releases   []KeyModelForegroundPermit
	releaseErr error
	released   chan struct{}
}

func (s *weReleaseStore) ReleaseForeground(_ context.Context, permit KeyModelForegroundPermit) (bool, error) {
	s.mu.Lock()
	s.releases = append(s.releases, permit)
	err := s.releaseErr
	s.mu.Unlock()
	if s.released != nil {
		s.released <- struct{}{}
	}
	return err == nil, err
}

func TestWeAttemptCapabilityHashAndMarkPrecommit(t *testing.T) {
	store := &weReleaseStore{KeyModelRuntimeStore: newMockKeyModelStore(), released: make(chan struct{}, 4)}
	route := GatewayKeyModelCapability{AccountID: "acc-1", Capability: testCapability()}
	attempt, preparation, scheduler := prepareAttemptForTest(t, store, route, NewGatewayKeyModelFailureBudget())
	if preparation.Status != AttemptPreparationAdmitted {
		t.Fatalf("preparation = %+v", preparation)
	}
	hash, err := CapabilityHash(testCapability())
	if err != nil {
		t.Fatal(err)
	}
	if attempt.CapabilityHash() != hash {
		t.Fatalf("CapabilityHash = %s, want %s", attempt.CapabilityHash(), hash)
	}

	// MarkPrecommit：停止续租并异步释放 foreground permit。
	attempt.MarkPrecommit()
	select {
	case <-store.released:
	case <-time.After(5 * time.Second):
		t.Fatal("precommit 释放未执行")
	}
	if scheduler.Pending() != 0 {
		t.Fatalf("续租定时器应已停止, pending = %d", scheduler.Pending())
	}
	store.mu.Lock()
	count := len(store.releases)
	store.mu.Unlock()
	if count != 1 {
		t.Fatalf("releases = %d, want 1", count)
	}
	// WaitTerminal 未 settle 时不应关闭。
	select {
	case <-attempt.WaitTerminal():
		t.Fatal("未 settle 不应有终态信号")
	default:
	}
}

func TestWeAttemptMarkPrecommitReleaseErrorLogged(t *testing.T) {
	store := &weReleaseStore{
		KeyModelRuntimeStore: newMockKeyModelStore(),
		releaseErr:           errors.New("redis 失败"),
		released:             make(chan struct{}, 4),
	}
	route := GatewayKeyModelCapability{AccountID: "acc-1", Capability: testCapability()}
	attempt, _, _ := prepareAttemptForTest(t, store, route, NewGatewayKeyModelFailureBudget())
	attempt.MarkPrecommit()
	// 释放失败只告警：通过 release 回调次数确认错误路径执行完毕。
	select {
	case <-store.released:
	case <-time.After(5 * time.Second):
		t.Fatal("释放调用未发生")
	}
}

func TestWeAttemptReleaseSafelyErrorPathViaSettle(t *testing.T) {
	// 契约：settle 中的释放失败只告警，不改变终态语义（complete_success 仍正常返回）。
	store := &weReleaseStore{
		KeyModelRuntimeStore: newMockKeyModelStore(),
		releaseErr:           errors.New("redis 失败"),
	}
	route := GatewayKeyModelCapability{AccountID: "acc-1", Capability: testCapability()}
	attempt, _, _ := prepareAttemptForTest(t, store, route, NewGatewayKeyModelFailureBudget())
	if err := attempt.ReportCompleteSuccess(context.Background()); err != nil {
		t.Fatalf("settle 不应向外传播释放错误: %v", err)
	}
	select {
	case <-attempt.WaitTerminal():
	default:
		t.Fatal("settle 后应有终态信号")
	}
}

func TestWeKeyModelRuntimeStoreSelector(t *testing.T) {
	memory := NewInMemoryKeyModelRuntimeStore(nil)
	selector := NewKeyModelRuntimeStoreSelector(memory, nil)
	first, err := selector.Select("memory")
	if err != nil || first != memory {
		t.Fatalf("memory store = %v err = %v", first, err)
	}
	again, _ := selector.Select("memory")
	if again != memory {
		t.Fatal("memory store 应复用同一实例")
	}
	if _, err := selector.Select("redis"); err == nil || !strings.Contains(err.Error(), "JUHE_AI_REDIS_STATE_URL") {
		t.Fatalf("nil factory 应报配置错误: %v", err)
	}

	factoryErr := errors.New("redis 初始化失败")
	failing := NewKeyModelRuntimeStoreSelector(nil, func() (*RedisKeyModelRuntimeStore, error) { return nil, factoryErr })
	if _, err := failing.Select("redis"); !errors.Is(err, factoryErr) {
		t.Fatalf("factory 错误应传播: %v", err)
	}
	// 失败后 driver 复位：memory 仍可用。
	if store, err := failing.Select("memory"); err != nil || store == nil {
		t.Fatalf("失败后 memory 选择 = %v err = %v", store, err)
	}

	// nil memoryStore 自动补建。
	auto := NewKeyModelRuntimeStoreSelector(nil, nil)
	if auto.memoryStore == nil {
		t.Fatal("nil memoryStore 应自动补建")
	}
}

// ---------------------------------------------------------------------------
// keymodelcapability.go：EndpointModeForFamily 全分支
// ---------------------------------------------------------------------------

func TestWeEndpointModeForFamily(t *testing.T) {
	tests := []struct {
		family string
		stream bool
		want   string
	}{
		{"chat_completions", false, "chat_json"},
		{"chat_completions", true, "chat_sse"},
		{"responses", false, "responses_json"},
		{"responses", true, "responses_sse"},
		{"messages", false, "messages_json"},
		{"messages", true, "messages_sse"},
		{"generate_content", false, "generate_content_json"},
		{"generate_content", true, "generate_content_sse"},
		{"stream_generate_content", true, "generate_content_sse"},
		{"stream_generate_content", false, "generate_content_sse"},
		{"interactions", false, "interactions_json"},
		{"interactions", true, "interactions_sse"},
		{"unknown", false, ""},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/%v", tt.family, tt.stream), func(t *testing.T) {
			if got := EndpointModeForFamily(tt.family, tt.stream); got != tt.want {
				t.Fatalf("EndpointModeForFamily(%s, %v) = %s, want %s", tt.family, tt.stream, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// keymodelmemory.go 补充分支
// ---------------------------------------------------------------------------

func TestWeMemoryAcquireRecoveryLeaseBranches(t *testing.T) {
	store := NewInMemoryKeyModelRuntimeStore(nil)
	ctx := context.Background()
	capability := testCapability()
	now := int64(1_000_000)

	// 无状态：返回 stale + 新建 OPEN 状态（不落库）。
	status, open := store.AcquireRecoveryLease(MemoryRecoveryLeaseInput{Capability: capability, NowMs: now})
	if status != KeyModelMutationStale {
		t.Fatalf("status = %s", status)
	}
	if open.Phase != KeyModelPhaseOpen || open.BackoffAttempt != 1 {
		t.Fatalf("open = %+v", open)
	}

	// 非法 capability：返回 stale 与空状态。
	bad := capability
	bad.ClientModel = " "
	status, state := store.AcquireRecoveryLease(MemoryRecoveryLeaseInput{Capability: bad, NowMs: now})
	if status != KeyModelMutationStale || state.CapabilityHash != "" {
		t.Fatalf("非法 capability: status = %s state = %+v", status, state)
	}

	// 落一个 OPEN 状态（generation 1, retryAt now+5000）。
	if _, err := store.RecordFailure(ctx, KeyModelFailureIntent{
		IntentID: "i-1", RequestID: "r-1", AttemptID: "a-1",
		Capability: capability, ObservedAtMs: now,
		Outcome: KeyModelOutcomeUpstreamNotComplete, SourceFence: "fence",
	}); err != nil {
		t.Fatal(err)
	}

	// retryAt 未到：not due。
	status, _ = store.AcquireRecoveryLease(MemoryRecoveryLeaseInput{Capability: capability, Generation: 1, DispatchRevision: capability.DispatchRevision, NowMs: now + 100})
	if status != KeyModelMutationNotDue {
		t.Fatalf("未到期应 not_due, got %s", status)
	}

	// 到期 + 空 leaseId：lease mismatch（AcquireKeyModelRecoveryLease 报错被折叠为 mismatch）。
	status, _ = store.AcquireRecoveryLease(MemoryRecoveryLeaseInput{Capability: capability, Generation: 1, DispatchRevision: capability.DispatchRevision, NowMs: now + 10_000})
	if status != KeyModelMutationLeaseMismatch {
		t.Fatalf("空 leaseId 应 lease mismatch, got %s", status)
	}

	// 到期 + 有效 leaseId：applied，状态进入 HALF_OPEN。
	status, leased := store.AcquireRecoveryLease(MemoryRecoveryLeaseInput{Capability: capability, Generation: 1, DispatchRevision: capability.DispatchRevision, LeaseID: "lease-1", NowMs: now + 10_000})
	if status != KeyModelMutationApplied {
		t.Fatalf("status = %s", status)
	}
	if leased.Phase != KeyModelPhaseHalfOpen || leased.ProbeLease == nil || leased.ProbeLease.LeaseID != "lease-1" {
		t.Fatalf("leased = %+v", leased)
	}

	// 已有活跃租约：状态处于 HALF_OPEN 且未到期 → not due（租约检查在到期判断之后）。
	status, _ = store.AcquireRecoveryLease(MemoryRecoveryLeaseInput{Capability: capability, Generation: 1, DispatchRevision: capability.DispatchRevision, LeaseID: "lease-2", NowMs: now + 11_000})
	if status != KeyModelMutationNotDue {
		t.Fatalf("HALF_OPEN 且未到期应 not_due, got %s", status)
	}

	// generation 不匹配：stale。
	status, _ = store.AcquireRecoveryLease(MemoryRecoveryLeaseInput{Capability: capability, Generation: 99, DispatchRevision: capability.DispatchRevision, LeaseID: "lease-3", NowMs: now + 12_000})
	if status != KeyModelMutationStale {
		t.Fatalf("generation 漂移应 stale, got %s", status)
	}
}

func TestWeMemoryRecordFailureReleasesForegroundPermit(t *testing.T) {
	store := NewInMemoryKeyModelRuntimeStore(nil)
	ctx := context.Background()
	capability := testCapability()
	hash, err := CapabilityHash(capability)
	if err != nil {
		t.Fatal(err)
	}

	// 占满两个前台许可（limit=2）。
	first, err := store.AdmitForeground(ctx, capability, "attempt-1")
	if err != nil || first.Status != ForegroundAdmitted {
		t.Fatalf("admit-1 = %+v err = %v", first, err)
	}
	second, err := store.AdmitForeground(ctx, capability, "attempt-2")
	if err != nil || second.Status != ForegroundAdmitted {
		t.Fatalf("admit-2 = %+v err = %v", second, err)
	}

	// 契约：带未过期 permit 的失败意图写入必须顺带释放该前台许可（releaseForegroundIfPresentLocked）。
	result, err := store.RecordFailure(ctx, KeyModelFailureIntent{
		IntentID: "i-1", RequestID: "r-1", AttemptID: "attempt-1",
		Capability: capability, ObservedAtMs: 1_000,
		Outcome: KeyModelOutcomeUpstreamNotComplete, SourceFence: "fence",
		Permit: first.Permit,
	})
	if err != nil || result.Status != KeyModelMutationApplied {
		t.Fatalf("record = %+v err = %v", result, err)
	}

	store.mu.Lock()
	remaining := len(store.permits[hash])
	_, hasAttempt1 := store.permits[hash]["attempt-1"]
	wakes := store.wakes[hash]
	store.mu.Unlock()
	if remaining != 1 || hasAttempt1 {
		t.Fatalf("attempt-1 许可应被释放: remaining = %d hasAttempt1 = %v", remaining, hasAttempt1)
	}
	if wakes != 1 {
		t.Fatalf("wake 序列 = %d, want 1", wakes)
	}
}

func TestWeMemoryEnsureStateCapacityEvictsClosedStates(t *testing.T) {
	store := NewInMemoryKeyModelRuntimeStore(nil)
	store.mu.Lock()
	defer store.mu.Unlock()

	// 白盒填满状态容量（避免构造 5 万次真实失败写入）。
	for index := 0; index < keyModelStateCapacity; index++ {
		hash := fmt.Sprintf("%064x", index)
		store.states[hash] = KeyModelState{CapabilityHash: hash, Phase: KeyModelPhaseClosed, Generation: 1}
	}
	// 两个已关闭状态带 closedUntil → 应被逐出腾出容量。
	store.closedUntil[fmt.Sprintf("%064x", 0)] = 1
	store.closedUntil[fmt.Sprintf("%064x", 1)] = 2
	if !store.ensureStateCapacityLocked() {
		t.Fatal("存在可逐出的关闭状态时应返回 true")
	}
	if len(store.states) >= keyModelStateCapacity {
		t.Fatalf("容量未释放: %d", len(store.states))
	}

	// 无 closedUntil 时容量耗尽。
	for index := 0; index < keyModelStateCapacity; index++ {
		hash := fmt.Sprintf("%064x", index+1000)
		store.states[hash] = KeyModelState{CapabilityHash: hash, Phase: KeyModelPhaseOpen}
	}
	if store.ensureStateCapacityLocked() {
		t.Fatal("无可逐出状态时应返回 false")
	}

	// closedUntil 指向非关闭状态：清理 closedUntil 但腾不出容量。
	hash := fmt.Sprintf("%064x", 1000)
	store.states[hash] = KeyModelState{CapabilityHash: hash, Phase: KeyModelPhaseOpen}
	store.closedUntil[hash] = 1
	if store.ensureStateCapacityLocked() {
		t.Fatal("OPEN 状态不应被逐出")
	}
	if _, ok := store.closedUntil[hash]; ok {
		t.Fatal("残留 closedUntil 应被清理")
	}
}

// ---------------------------------------------------------------------------
// keymodelredis.go：Keys / Get / redisString
// ---------------------------------------------------------------------------

func TestWeRedisStoreKeysAccessor(t *testing.T) {
	server := miniredis.RunT(t)
	defer server.Close()
	store, err := NewRedisKeyModelRuntimeStore(KeyModelRedisStoreOptions{
		RedisURL: "redis://" + server.Addr() + "/0", Namespace: "we-ns",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	keys := store.Keys()
	hash := strings.Repeat("ab", 32)
	if keys.State(hash) != "juhe-ai:we-ns:"+KeyModelRedisGroup+":state:"+hash {
		t.Fatalf("state key = %s", keys.State(hash))
	}
	if keys.Admission(hash) == "" || keys.AdmissionLease(hash, "a-1") == "" || keys.AdmissionWake(hash) == "" || keys.MainProbeFence(hash) == "" {
		t.Fatal("key 族不应为空")
	}
	if keys.J1Confirmation(hash, 3) == "" || keys.Receipt("i-1") == "" || keys.Capacity() == "" || keys.AdmissionEvents() == "" {
		t.Fatal("key 族不应为空")
	}
}

func TestWeRedisStoreGetFoundMissingAndError(t *testing.T) {
	server := miniredis.RunT(t)
	store, err := NewRedisKeyModelRuntimeStore(KeyModelRedisStoreOptions{
		RedisURL: "redis://" + server.Addr() + "/0", Namespace: "we-ns",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	capability := testCapability()
	hash, err := CapabilityHash(capability)
	if err != nil {
		t.Fatal(err)
	}

	// 未写入：nil, nil（redis.Nil 归一化为缺失）。
	missing, err := store.Get(ctx, capability)
	if err != nil || missing != nil {
		t.Fatalf("missing = %v err = %v", missing, err)
	}

	// 写入合法状态 JSON：读回解析并校验 hash/revision 完整性。
	state, err := CreateKeyModelOpenState(capability, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	server.Set(store.Keys().State(hash), string(encoded))
	loaded, err := store.Get(ctx, capability)
	if err != nil || loaded == nil || loaded.Phase != KeyModelPhaseOpen {
		t.Fatalf("loaded = %+v err = %v", loaded, err)
	}

	// hash 不匹配的载荷必须拒绝（完整性校验）。
	tampered := state
	tampered.CapabilityHash = strings.Repeat("cd", 32)
	tamperedEncoded, _ := json.Marshal(tampered)
	server.Set(store.Keys().State(hash), string(tamperedEncoded))
	if _, err := store.Get(ctx, capability); err == nil || !strings.Contains(err.Error(), "完整性") {
		t.Fatalf("篡改载荷应报完整性错误: %v", err)
	}

	// 连续两次读取失败：包装错误向上传播（每次重试间隔 50ms）。
	server.Close()
	if _, err := store.Get(ctx, capability); err == nil || !strings.Contains(err.Error(), "连续两次") {
		t.Fatalf("宕机后应报连续失败: %v", err)
	}
}

func TestWeRedisStoreGetInvalidURL(t *testing.T) {
	store, err := NewRedisKeyModelRuntimeStore(KeyModelRedisStoreOptions{
		RedisURL: "not-a-valid-url", Namespace: "we-ns",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Get(context.Background(), testCapability()); err == nil {
		t.Fatal("非法 Redis URL 应报错")
	}
}

func TestWeRedisStringHelpers(t *testing.T) {
	value, err := redisString("ok")
	if err != nil || value != "ok" {
		t.Fatalf("string = %s err = %v", value, err)
	}
	value, err = redisString(int64(42))
	if err != nil || value != "42" {
		t.Fatalf("int64 = %s err = %v", value, err)
	}
	if _, err := redisString(struct{}{}); err == nil {
		t.Fatal("非字符串返回值应报错")
	}
	if redisValueString("good") != "good" {
		t.Fatal("redisValueString 应返回字符串")
	}
	if redisValueString(struct{}{}) != "" {
		t.Fatal("非字符串应回落空串")
	}
}

// ---------------------------------------------------------------------------
// keymodelruntime.go：IsKeyModelBlocked
// ---------------------------------------------------------------------------

func TestWeIsKeyModelBlocked(t *testing.T) {
	if IsKeyModelBlocked(KeyModelState{Phase: KeyModelPhaseClosed}) {
		t.Fatal("CLOSED 不应被阻断")
	}
	for _, phase := range []KeyModelPhase{KeyModelPhaseOpen, KeyModelPhaseHalfOpen, KeyModelPhaseRecovering} {
		if !IsKeyModelBlocked(KeyModelState{Phase: phase}) {
			t.Fatalf("%s 应被阻断", phase)
		}
	}
}
