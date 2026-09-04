package gatewayaccounteffects

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTransientStoreForTest(t *testing.T, mutateStateTtlMs int64) (*RedisAccountApiKeyTransientStateStore, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	options := RedisAccountApiKeyTransientStateStoreOptions{
		RedisURL:                        "redis://" + server.Addr() + "/0",
		Namespace:                       "test-space",
		AllowUnsafeShortStateTtlForTest: true,
	}
	if mutateStateTtlMs > 0 {
		options.StateTtlMs = mutateStateTtlMs
	}
	store, err := NewRedisAccountApiKeyTransientStateStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, server
}

func TestTransientRedisStoreKeyLayout(t *testing.T) {
	store, _ := newTransientStoreForTest(t, 0)
	digest := sha256.Sum256([]byte("acc-1\x00fp-1"))
	want := "juhe-ai:test-space:state:gateway-account-api-key-transient-avoidance:state:" + hex.EncodeToString(digest[:])
	if store.stateKey(AccountApiKeyTransientTarget{AccountID: "acc-1", KeyFingerprint: "fp-1"}) != want {
		t.Fatalf("stateKey mismatch: %s", store.stateKey(AccountApiKeyTransientTarget{AccountID: "acc-1", KeyFingerprint: "fp-1"}))
	}
}

func TestTransientRedisStoreConstructorValidations(t *testing.T) {
	tests := []struct {
		name    string
		options RedisAccountApiKeyTransientStateStoreOptions
		wantErr string
	}{
		{
			name:    "缺少 redisUrl",
			options: RedisAccountApiKeyTransientStateStoreOptions{Namespace: "ns", AllowUnsafeShortStateTtlForTest: true},
			wantErr: "redisUrl 不能为空",
		},
		{
			name:    "缺少 namespace",
			options: RedisAccountApiKeyTransientStateStoreOptions{RedisURL: "redis://localhost:6379/0", AllowUnsafeShortStateTtlForTest: true},
			wantErr: "Redis namespace 不能为空",
		},
		{
			name:    "stateTtl 过短",
			options: RedisAccountApiKeyTransientStateStoreOptions{RedisURL: "redis://localhost:6379/0", Namespace: "ns", StateTtlMs: 60_000},
			wantErr: fmt.Sprintf("stateTtlMs 不得少于 %dms，必须覆盖网关最大在途请求", TransientMinimumStateTtlMs),
		},
		{
			name: "stateTtl 短于最大 suppression delay",
			options: RedisAccountApiKeyTransientStateStoreOptions{
				RedisURL: "redis://localhost:6379/0", Namespace: "ns", StateTtlMs: 1_000,
				SuppressionDelayMs: []int64{2_000}, AllowUnsafeShortStateTtlForTest: true,
			},
			wantErr: "stateTtlMs 不得短于最大 suppression delay",
		},
		{
			name: "suppression delay 非正数",
			options: RedisAccountApiKeyTransientStateStoreOptions{
				RedisURL: "redis://localhost:6379/0", Namespace: "ns", StateTtlMs: 1_000,
				SuppressionDelayMs: []int64{0}, AllowUnsafeShortStateTtlForTest: true,
			},
			wantErr: "suppressionDelayMs[0] 必须是正整数",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRedisAccountApiKeyTransientStateStore(tt.options)
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestTransientRedisStoreMissingStateAndGenerationTombstone(t *testing.T) {
	store, server := newTransientStoreForTest(t, 0)
	ctx := context.Background()
	target := AccountApiKeyTransientTarget{AccountID: "acc-1", KeyFingerprint: "fp-1"}

	// 缺少状态（load 未初始化）：missing_state。
	result, err := store.RecordFailure(ctx, TransientMutationInput{Target: target, Status: APIKeyStatusError, ExpectedGeneration: "gen-1"})
	if err != nil || result.Applied || result.Reason != TransientReasonMissingState {
		t.Fatalf("missing state result = %+v err = %v", result, err)
	}

	// loadMany 初始化 generation。
	states, err := store.LoadMany(ctx, "acc-1", []string{"fp-1", "fp-1", " "})
	if err != nil || len(states) != 1 {
		t.Fatalf("loadMany = %d states, err %v", len(states), err)
	}
	generation := states[0].State.Generation
	if states[0].Suppressed || states[0].State.ObservationKind != "success" {
		t.Fatalf("initial state = %+v", states[0].State)
	}

	// 失败写入按 generation 生效。
	result, err = store.RecordFailure(ctx, TransientMutationInput{Target: target, Status: APIKeyStatusRateLimited, ExpectedGeneration: generation})
	if err != nil || !result.Applied {
		t.Fatalf("failure result = %+v err = %v", result, err)
	}
	if result.State.Status != APIKeyStatusRateLimited || result.State.FailureCount != 1 {
		t.Fatalf("failure state = %+v", result.State)
	}
	if *result.State.SuppressUntilMs < time.Now().UnixMilli() {
		t.Fatal("suppressUntilMs should be in the future")
	}

	// 当前 generation 的成功写入推进 generation（墓碑）。
	result, err = store.RecordSuccess(ctx, TransientMutationInput{Target: target, ExpectedGeneration: generation})
	if err != nil || !result.Applied || result.State.ObservationKind != "success" || result.State.Generation == generation {
		t.Fatalf("success result = %+v err = %v", result, err)
	}

	// 旧 dispatch 快照的 generation 写入被成功墓碑拒绝。
	result, err = store.RecordFailure(ctx, TransientMutationInput{Target: target, Status: APIKeyStatusError, ExpectedGeneration: generation})
	if err != nil || result.Applied || result.Reason != TransientReasonStaleGeneration {
		t.Fatalf("old generation failure = %+v err = %v", result, err)
	}
	server.FastForward(time.Duration(TransientDefaultStateTtlMs) * time.Millisecond)
}

func TestTransientRedisStoreFailureCounterWindow(t *testing.T) {
	store, server := newTransientStoreForTest(t, 0)
	ctx := context.Background()
	target := AccountApiKeyTransientTarget{AccountID: "acc-1", KeyFingerprint: "fp-1"}
	states, err := store.LoadMany(ctx, "acc-1", []string{"fp-1"})
	if err != nil {
		t.Fatal(err)
	}
	generation := states[0].State.Generation

	// 窗口内连续失败：3s → 5s → 10s 阶梯，封顶在阶梯末档。
	wantCounts := []int{1, 2, 3, 3}
	for _, wantCount := range wantCounts {
		result, recordErr := store.RecordFailure(ctx, TransientMutationInput{Target: target, Status: APIKeyStatusTemporaryUnavailable, ExpectedGeneration: generation})
		if recordErr != nil || !result.Applied {
			t.Fatalf("record failure = %+v err = %v", result, recordErr)
		}
		if result.State.FailureCount != wantCount {
			t.Fatalf("failureCount = %d, want %d", result.State.FailureCount, wantCount)
		}
		suppressUntil := *result.State.SuppressUntilMs
		now := time.Now().UnixMilli()
		if suppressUntil <= now || suppressUntil > now+11_000 {
			t.Fatalf("suppressUntilMs %d outside the ladder window (now=%d)", suppressUntil, now)
		}
	}

	// 窗口外（>10min）重新从 1 开始计数。
	raw, err := store.clientForUse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("acc-1\x00fp-1"))
	key := "juhe-ai:test-space:state:gateway-account-api-key-transient-avoidance:state:" + hex.EncodeToString(digest[:])
	current, err := raw.Get(ctx, key).Result()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(current), &decoded); err != nil {
		t.Fatal(err)
	}
	decoded["lastObservedAtMs"] = time.Now().UnixMilli() - 11*60_000
	encoded, _ := json.Marshal(decoded)
	server.Set(key, string(encoded))

	result, err := store.RecordFailure(ctx, TransientMutationInput{Target: target, Status: APIKeyStatusTemporaryUnavailable, ExpectedGeneration: generation})
	if err != nil {
		t.Fatal(err)
	}
	if result.State.FailureCount != 1 {
		t.Fatalf("failureCount after window = %d, want 1", result.State.FailureCount)
	}
}

func TestTransientRedisStoreLoadManyReportsSuppression(t *testing.T) {
	store, _ := newTransientStoreForTest(t, 0)
	ctx := context.Background()
	states, err := store.LoadMany(ctx, "acc-1", []string{"fp-1", "fp-2"})
	if err != nil {
		t.Fatal(err)
	}
	generation := states[0].State.Generation
	if _, err := store.RecordFailure(ctx, TransientMutationInput{
		Target: AccountApiKeyTransientTarget{AccountID: "acc-1", KeyFingerprint: "fp-1"},
		Status: APIKeyStatusError, ExpectedGeneration: generation,
	}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.LoadMany(ctx, "acc-1", []string{"fp-1", "fp-2"})
	if err != nil {
		t.Fatal(err)
	}
	byFingerprint := map[string]AccountApiKeyTransientDispatchState{}
	for _, entry := range reloaded {
		byFingerprint[entry.State.KeyFingerprint] = entry
	}
	if !byFingerprint["fp-1"].Suppressed || byFingerprint["fp-1"].State.Status != APIKeyStatusError {
		t.Fatalf("fp-1 = %+v", byFingerprint["fp-1"])
	}
	if byFingerprint["fp-2"].Suppressed {
		t.Fatalf("fp-2 must not be suppressed: %+v", byFingerprint["fp-2"])
	}
}

func TestTransientRedisStoreCorruptStateIgnoredByLoad(t *testing.T) {
	store, server := newTransientStoreForTest(t, 0)
	ctx := context.Background()
	digest := sha256.Sum256([]byte("acc-1\x00fp-1"))
	key := "juhe-ai:test-space:state:gateway-account-api-key-transient-avoidance:state:" + hex.EncodeToString(digest[:])
	server.Set(key, `{"schemaVersion":2}`)
	states, err := store.LoadMany(ctx, "acc-1", []string{"fp-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].State.SchemaVersion != 1 {
		t.Fatalf("corrupt state must be replaced: %+v", states)
	}
}

type stubTransientStore struct {
	mu       sync.Mutex
	failures int
	succeeds int
	loads    int
}

func (s *stubTransientStore) RecordFailure(context.Context, TransientMutationInput) (AccountApiKeyTransientMutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures++
	return AccountApiKeyTransientMutationResult{Applied: true, Reason: TransientReasonApplied}, nil
}

func (s *stubTransientStore) RecordSuccess(context.Context, TransientMutationInput) (AccountApiKeyTransientMutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.succeeds++
	return AccountApiKeyTransientMutationResult{Applied: true, Reason: TransientReasonApplied}, nil
}

func (s *stubTransientStore) LoadMany(context.Context, string, []string) ([]AccountApiKeyTransientDispatchState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	return nil, nil
}

func TestTransientStoreDualDriveThroughGuard(t *testing.T) {
	// 双驱：guard 在 redis driver 下走 store.recordFailure/recordSuccess，
	// memory driver 下保持进程本地状态。
	stub := &stubTransientStore{}
	clock := NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	guard := NewAccountAPIKeyFailureGuard(SideEffectsConfig{RuntimeStateDriver: "redis"}, clock, nil, func() (AccountApiKeyTransientStateStore, error) {
		return stub, nil
	})
	ctx := context.Background()
	account := guardTestAccount("acc-1", "fp-1")
	account.SelectedAPIKeyTransientGeneration = stringPtr("gen-1")

	if applied, err := guard.RecordTransientFailure(ctx, account, APIKeyStatusRateLimited); err != nil || !applied {
		t.Fatalf("transient failure = %v %v", applied, err)
	}
	if applied, err := guard.ClearTransientFailure(ctx, account); err != nil || !applied {
		t.Fatalf("transient clear = %v %v", applied, err)
	}
	stub.mu.Lock()
	failureCalls, successCalls := stub.failures, stub.succeeds
	stub.mu.Unlock()
	if failureCalls != 1 || successCalls != 1 {
		t.Fatalf("stub calls failures=%d successes=%d", failureCalls, successCalls)
	}
	// 无 transientGeneration 的账户不得写 Redis。
	accountNoGeneration := guardTestAccount("acc-1", "fp-2")
	if applied, err := guard.RecordTransientFailure(ctx, accountNoGeneration, APIKeyStatusError); applied || err != nil {
		t.Fatalf("unexpected write without generation: %v %v", applied, err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.failures != 1 {
		t.Fatalf("failures = %d, want 1", stub.failures)
	}
	// factory 错误按原样返回。
	failingGuard := NewAccountAPIKeyFailureGuard(SideEffectsConfig{RuntimeStateDriver: "redis"}, clock, nil, func() (AccountApiKeyTransientStateStore, error) {
		return nil, errRedisStateURLRequired()
	})
	if _, err := failingGuard.RecordTransientFailure(ctx, account, APIKeyStatusError); err == nil || !strings.Contains(err.Error(), "JUHE_AI_REDIS_STATE_URL 在 Redis runtime state driver 下必须配置") {
		t.Fatalf("factory err = %v", err)
	}
}

func errRedisStateURLRequired() error {
	return &staticError{message: "JUHE_AI_REDIS_STATE_URL 在 Redis runtime state driver 下必须配置"}
}

type staticError struct{ message string }

func (e *staticError) Error() string { return e.message }

var _ = redis.NewClient
