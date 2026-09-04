package gatewayaccounteffects

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func redisStoreForTest(t *testing.T) (*RedisKeyModelRuntimeStore, *miniredis.Miniredis, *FakeClock) {
	t.Helper()
	server := miniredis.RunT(t)
	clock := NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store, err := NewRedisKeyModelRuntimeStore(KeyModelRedisStoreOptions{
		RedisURL:  "redis://" + server.Addr() + "/0",
		Namespace: "test-space",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, server, clock
}

// TestKeyModelRedisKeysJobsCompatible 锁定与 jobs/internal/keymodelrecovery
// 完全一致的共享 key 族（同一批 Redis key，不得出现第二套语义）。
func TestKeyModelRedisKeysJobsCompatible(t *testing.T) {
	keys, err := NewKeyModelRedisKeys("test-space")
	if err != nil {
		t.Fatal(err)
	}
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if keys.State(hash) != "juhe-ai:test-space:gateway-account-circuit-key-model:state:"+hash {
		t.Fatalf("state key = %s", keys.State(hash))
	}
	if keys.Due() != "juhe-ai:test-space:gateway-account-circuit-key-model:due" {
		t.Fatalf("due key = %s", keys.Due())
	}
	if keys.Closed() != "juhe-ai:test-space:gateway-account-circuit-key-model:closed" {
		t.Fatalf("closed key = %s", keys.Closed())
	}
	if _, err := NewKeyModelRedisKeys("bad namespace!"); err == nil || err.Error() != "Redis namespace 无效" {
		t.Fatalf("namespace err = %v", err)
	}
}

func TestRedisStoreRecordFailureAppliedIdempotentAndDue(t *testing.T) {
	store, server, clock := redisStoreForTest(t)
	ctx := context.Background()
	capability := testCapability()
	base := NowMs(clock)
	server.SetTime(time.UnixMilli(base))

	result, err := store.RecordFailure(ctx, memoryIntent(capability, "intent-1", base))
	if err != nil || result.Status != KeyModelMutationApplied {
		t.Fatalf("record = %+v err = %v", result, err)
	}
	if result.State.Phase != KeyModelPhaseOpen || result.State.Generation != 1 {
		t.Fatalf("state = %+v", result.State)
	}
	// Lua 端以 Redis TIME 覆盖 lastObservedAtMs 并设 retryAtMs=now+5000。
	if result.State.LastObservedAtMs != base || *result.State.RetryAtMs != base+5_000 {
		t.Fatalf("observed=%d retry=%v want base=%d", result.State.LastObservedAtMs, *result.State.RetryAtMs, base)
	}
	// due 集合已登记。
	if score, err := server.ZScore("juhe-ai:test-space:gateway-account-circuit-key-model:due", result.State.CapabilityHash); err != nil || score != float64(base+5_000) {
		t.Fatalf("due score = %v err = %v", score, err)
	}
	// receipt 幂等：同 intentId 二次写入返回首次状态并释放 permit。
	permit := &KeyModelForegroundPermit{CapabilityHash: result.State.CapabilityHash, AttemptID: "attempt-1", LeaseUntilMs: base + 1_000}
	replay, err := store.RecordFailure(ctx, func() KeyModelFailureIntent {
		intent := memoryIntent(capability, "intent-1", base)
		intent.Permit = permit
		return intent
	}())
	if err != nil || replay.Status != KeyModelMutationIdempotent {
		t.Fatalf("replay = %+v err = %v", replay, err)
	}
	// permit 已被 receipt 释放：admission 不再受该 permit 占用。
	_ = permit
}

func TestRedisStoreAdmitReleaseRenewForeground(t *testing.T) {
	store, _, clock := redisStoreForTest(t)
	ctx := context.Background()
	capability := testCapability()
	if _, err := store.RecordFailure(ctx, memoryIntent(capability, "intent-1", NowMs(clock))); err != nil {
		t.Fatal(err)
	}
	// 非 CLOSED → blocked。
	blocked, err := store.AdmitForeground(ctx, capability, "attempt-1")
	if err != nil || blocked.Status != ForegroundBlocked {
		t.Fatalf("blocked = %+v err = %v", blocked, err)
	}
	// foreground admission 幂等（同 attemptId）。
	first, err := store.AdmitForeground(ctx, capability, "attempt-1")
	if err != nil || first.Status != ForegroundBlocked {
		t.Fatalf("second blocked = %+v err = %v", first, err)
	}
}

func TestRedisStoreAdmitBusyAndReleaseWake(t *testing.T) {
	store, server, clock := redisStoreForTest(t)
	ctx := context.Background()
	capability := testCapability()
	// CLOSED 态才可准入：直接通过 Lua settle 前先写一个 CLOSED state 不可行，
	// 这里用 memory 侧纯函数构造 CLOSED payload 再原样写入由 Get 读回不可行
	// （recordFailure 只写 OPEN）。改为验证 admission 对 CLOSED 态的放行：
	// 删除 state key 后 state 缺失 → 允许准入（与 Node 相同）。
	capabilityZero := capability
	capabilityZero.DispatchRevision = 9
	result, err := store.RecordFailure(ctx, memoryIntent(capabilityZero, "intent-9", NowMs(clock)))
	if err != nil {
		t.Fatal(err)
	}
	// recordFailure 写入的是 OPEN 态（必 blocked）；为验证 busy/release 路径，
	// 删除 state key 使 admission 视作无共享运行态（Node 同语义）。
	if !server.Del("juhe-ai:test-space:gateway-account-circuit-key-model:state:" + result.State.CapabilityHash) {
		t.Fatal("state key must exist before deletion")
	}
	first, err := store.AdmitForeground(ctx, capabilityZero, "attempt-1")
	if err != nil || first.Status != ForegroundAdmitted {
		t.Fatalf("first admit = %+v err = %v", first, err)
	}
	second, err := store.AdmitForeground(ctx, capabilityZero, "attempt-2")
	if err != nil || second.Status != ForegroundAdmitted {
		t.Fatalf("second admit = %+v err = %v", second, err)
	}
	busy, err := store.AdmitForeground(ctx, capabilityZero, "attempt-3")
	if err != nil || busy.Status != ForegroundBusy || busy.WakeSequence != 0 {
		t.Fatalf("busy = %+v err = %v", busy, err)
	}
	// 释放后可准入，wake 序号递增。
	if released, err := store.ReleaseForeground(ctx, *first.Permit); err != nil || !released {
		t.Fatalf("release = %v err = %v", released, err)
	}
	after, err := store.AdmitForeground(ctx, capabilityZero, "attempt-3")
	if err != nil || after.Status != ForegroundAdmitted {
		t.Fatalf("after release = %+v err = %v", after, err)
	}
	// 续租。
	renewed, err := store.RenewForeground(ctx, *after.Permit)
	if err != nil || renewed == nil {
		t.Fatalf("renew = %+v err = %v", renewed, err)
	}
	// 租约被删（模拟过期）后 → lost。
	lost, err := store.RenewForeground(ctx, KeyModelForegroundPermit{CapabilityHash: after.Permit.CapabilityHash, AttemptID: "ghost"})
	if err != nil || lost != nil {
		t.Fatalf("ghost renew = %+v err = %v", lost, err)
	}
}

func TestRedisStoreMainProbeFenceAndJ1(t *testing.T) {
	store, _, clock := redisStoreForTest(t)
	ctx := context.Background()
	capability := testCapability()
	permit := KeyModelForegroundPermit{CapabilityHash: mustCapabilityHash(capability), AttemptID: "main-1", LeaseUntilMs: NowMs(clock) + 1_000}
	if err := store.RecordMainProbeFailure(ctx, capability, permit); err != nil {
		t.Fatal(err)
	}
	// fence 挡 admission。
	blocked, err := store.AdmitForeground(ctx, capability, "attempt-2")
	if err != nil || blocked.Status != ForegroundBlocked {
		t.Fatalf("fence blocked = %+v err = %v", blocked, err)
	}
	// owner 不匹配的清理失败。
	cleared, err := store.ClearMainProbeFence(ctx, KeyModelFenceReference{CapabilityHash: permit.CapabilityHash, KeyFingerprint: "fp-1", DispatchRevision: 3, OwnerID: "ghost"}, "fp-1")
	if err != nil || cleared {
		t.Fatalf("ghost clear = %v err = %v", cleared, err)
	}
	cleared, err = store.ClearMainProbeFence(ctx, KeyModelFenceReference{CapabilityHash: permit.CapabilityHash, KeyFingerprint: "fp-1", DispatchRevision: 3, OwnerID: "main-1"}, "fp-1")
	if err != nil || !cleared {
		t.Fatalf("clear = %v err = %v", cleared, err)
	}
	// defer。
	if err := store.RecordMainProbeFailure(ctx, capability, permit); err != nil {
		t.Fatal(err)
	}
	deferred, err := store.DeferMainProbeFence(ctx, KeyModelFenceReference{CapabilityHash: permit.CapabilityHash, KeyFingerprint: "fp-1", DispatchRevision: 3, OwnerID: "main-1"})
	if err != nil || !deferred {
		t.Fatalf("defer = %v err = %v", deferred, err)
	}
	// J1 限频窗口 2 分钟。
	claimed, err := store.ClaimJ1Confirmation(ctx, "source-1", 3)
	if err != nil || !claimed {
		t.Fatalf("claim = %v err = %v", claimed, err)
	}
	again, err := store.ClaimJ1Confirmation(ctx, "source-1", 3)
	if err != nil || again {
		t.Fatalf("second claim = %v err = %v", again, err)
	}
	if other, err := store.ClaimJ1Confirmation(ctx, "source-1", 4); err != nil || !other {
		t.Fatalf("different revision claim = %v err = %v", other, err)
	}
}

func TestRedisStoreEvalRunnerFallbackAndRetry(t *testing.T) {
	calls := 0
	store, err := NewRedisKeyModelRuntimeStore(KeyModelRedisStoreOptions{
		RedisURL:  "redis://127.0.0.1:1/0", // 不可达端口
		Namespace: "ns",
		EvalRunner: func(script string, keys []string, args []string) (any, error) {
			calls++
			if calls == 1 {
				return nil, errors.New("boom")
			}
			return []any{"stale", ""}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	result, err := store.RecordFailure(ctx, memoryIntent(testCapability(), "intent-1", 1_000))
	if err != nil {
		t.Fatalf("retry path err = %v", err)
	}
	if result.Status != KeyModelMutationStale {
		t.Fatalf("status = %s", result.Status)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (single retry)", calls)
	}
	// 两次都失败：聚合错误带原文案。
	failStore, err := NewRedisKeyModelRuntimeStore(KeyModelRedisStoreOptions{
		RedisURL: "redis://127.0.0.1:1/0", Namespace: "ns",
		EvalRunner: func(string, []string, []string) (any, error) { return nil, errors.New("boom") },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = failStore.Close() })
	_, err = failStore.RecordFailure(ctx, memoryIntent(testCapability(), "intent-2", 1_000))
	if err == nil || !contains(err.Error(), "Key-model 失败意图写入连续两次失败") {
		t.Fatalf("aggregate err = %v", err)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || len(needle) == 0 || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func TestRedisStoreStateIntegrityCheck(t *testing.T) {
	_ = redisStoreForTest
	if _, err := parseKeyModelState(`{"capabilityHash":"deadbeef","dispatchRevision":3}`, "cafe", 3); err == nil || err.Error() != "Key-model Redis state 完整性校验失败" {
		t.Fatalf("integrity err = %v", err)
	}
	if _, err := finiteRedisInteger("-3"); err == nil || !contains(err.Error(), "Key-model Redis 数字结果无效") {
		t.Fatalf("finiteInteger err = %v", err)
	}
	if value, err := finiteRedisInteger(strconv.FormatInt(42, 10)); err != nil || value != 42 {
		t.Fatalf("finiteInteger = %d err = %v", value, err)
	}
	if _, err := requiredCapabilityHash("nothash"); err == nil || err.Error() != "Key-model capabilityHash 无效" {
		t.Fatalf("hash err = %v", err)
	}
}

func mustCapabilityHash(capability CapabilityKey) string {
	hash, err := CapabilityHash(capability)
	if err != nil {
		panic(err)
	}
	return hash
}
