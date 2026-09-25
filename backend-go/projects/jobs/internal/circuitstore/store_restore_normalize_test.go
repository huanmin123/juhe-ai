package circuitstore

import (
	"context"
	"strings"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"
)

// F2+F5（状态机专项 2026-09-25）jobs 侧 restore 归一化补强单测。
//
// jobs 的 RedisStore.Restore 与 gateway 共享同一 Redis 键空间与同一
// circuitstate.ScriptRestore；业务库 incident 遗留行（account-circuit-recovery
// 正是恢复流程执行者）必须在写入前清洗到满足
// shared/platform/circuitstate/lua.go 的硬不变式（不改 Lua）。本包实现与
// gateway/internal/gatewaycircuit 的 normalizeRestoreConfirmationState 语义
// 一致（跨 module 不可 import，注释互指、成对修改）：
//   - F5：OPEN/RECOVERING 且 RetryAtMs 为 NULL 归一化后必须带 retryAt，
//     否则 acquire_canary 永远 not_due（lua.go:508）、listdue 直接 error
//     （lua.go:926-928），恢复流程永久卡死。
//   - F2：confirmationFailureCount > required 钳制到 required（lua.go:87-91）。
//   - 正常数据逐字段不被改动。

func f2f5JobsState(scope Scope, phase string, updatedAtMs int64) State {
	state := ClosedState(scope, "7", 1, "t-legacy", updatedAtMs)
	state.Phase = phase
	return state
}

func TestF2F5NormalizeRestoreConfirmationState(t *testing.T) {
	scope := Scope{Kind: "account", AccountRuntimeKey: "f2f5-unit"}
	validKey := strings.Repeat("a", 64)

	t.Run("F5 open nil retryAt patched to updatedAtMs", func(t *testing.T) {
		state := f2f5JobsState(scope, "OPEN", 1_000)
		normalized, err := normalizeRestoreConfirmationState(state)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		if normalized.RetryAtMs == nil || *normalized.RetryAtMs != 1_000 {
			t.Fatalf("F5: retryAt = %v, 期望 UpdatedAtMs=1000", normalized.RetryAtMs)
		}
	})

	t.Run("F5 recovering nil retryAt patched to updatedAtMs", func(t *testing.T) {
		state := f2f5JobsState(scope, "RECOVERING", 2_000)
		normalized, err := normalizeRestoreConfirmationState(state)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		if normalized.RetryAtMs == nil || *normalized.RetryAtMs != 2_000 {
			t.Fatalf("F5: retryAt = %v, 期望 UpdatedAtMs=2000", normalized.RetryAtMs)
		}
	})

	t.Run("F5 leased open nil retryAt patched to leaseUntilMs", func(t *testing.T) {
		state := f2f5JobsState(scope, "OPEN", 1_000)
		state.Lease = &Lease{Kind: "confirmation", LeaseID: "lease-1", LeaseUntilMs: 9_000}
		normalized, err := normalizeRestoreConfirmationState(state)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		if normalized.RetryAtMs == nil || *normalized.RetryAtMs != 9_000 {
			t.Fatalf("F5: retryAt = %v, 期望租约截止 9000（与 SUSPECT 补法一致）", normalized.RetryAtMs)
		}
	})

	t.Run("F2 count above required clamped", func(t *testing.T) {
		state := f2f5JobsState(scope, "SUSPECT", 1_000)
		required := int64(1)
		count := int64(4)
		state.ConfirmationFailuresRequired = &required
		state.ConfirmationFailureCount = &count
		normalized, err := normalizeRestoreConfirmationState(state)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		if *normalized.ConfirmationFailureCount != 1 {
			t.Fatalf("F2: count = %d, 期望钳制到 required=1", *normalized.ConfirmationFailureCount)
		}
		if *normalized.ConfirmationFailuresRequired != 1 {
			t.Fatalf("F2: required 被改动 = %d", *normalized.ConfirmationFailuresRequired)
		}
	})

	t.Run("invalid evidence keys filtered", func(t *testing.T) {
		otherKey := strings.Repeat("b", 64)
		state := f2f5JobsState(scope, "SUSPECT", 1_000)
		// 合法 key 保留；同一 key 的大写形式 ToLower 后去重；非法 key 静默过滤
		// （normalizeConfirmationState 内联实现；Lua 侧 lua.go:96-100 对非法
		// key 直接 error，故 restore 入口必须先清洗）。
		state.FailureEvidenceKeys = stringList{validKey, strings.ToUpper(validKey), "not-a-valid-key", strings.ToUpper(otherKey)}
		normalized, err := normalizeRestoreConfirmationState(state)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		keys := []string(normalized.FailureEvidenceKeys)
		if len(keys) != 2 || keys[0] != validKey || keys[1] != otherKey {
			t.Fatalf("evidence 清洗结果 = %v", keys)
		}
	})

	t.Run("well-formed suspect state untouched", func(t *testing.T) {
		state := f2f5JobsState(scope, "SUSPECT", 1_000)
		required := int64(2)
		count := int64(1)
		retryAt := int64(4_000)
		state.ConfirmationFailuresRequired = &required
		state.ConfirmationFailureCount = &count
		state.RetryAtMs = &retryAt
		state.FailureEvidenceKeys = stringList{validKey}
		normalized, err := normalizeRestoreConfirmationState(state)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		if *normalized.ConfirmationFailuresRequired != 2 || *normalized.ConfirmationFailureCount != 1 ||
			*normalized.RetryAtMs != 4_000 || len(normalized.FailureEvidenceKeys) != 1 {
			t.Fatalf("正常数据被改动: %+v", normalized)
		}
	})

	t.Run("closed state untouched", func(t *testing.T) {
		state := f2f5JobsState(scope, "CLOSED", 1_000)
		normalized, err := normalizeRestoreConfirmationState(state)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		if normalized.Phase != "CLOSED" || normalized.ConfirmationFailuresRequired != nil ||
			normalized.ConfirmationFailureCount != nil || normalized.RetryAtMs != nil {
			t.Fatalf("CLOSED 相被改动: %+v", normalized)
		}
	})
}

func newF2F5RedisStore(t *testing.T, now func() int64) *RedisStore {
	t.Helper()
	server := miniredis.RunT(t)
	store, err := NewRedisStore(RedisStoreOptions{
		RedisURL:  "redis://" + server.Addr(),
		Namespace: "test",
		Capacity:  100,
		Now:       now,
	})
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	return store
}

// F5+F2 端到端（Redis 驱动，miniredis 真 Lua）：遗留 OPEN 行（NULL retryAt、
// count>required）经 Restore 后带 retryAt 且 count 钳制；canary 立即可获取，
// account-circuit-recovery 的恢复流程不再卡死。
func TestF2F5RedisRestoreCleansLegacyOpenRow(t *testing.T) {
	ctx := context.Background()
	now := int64(2_000)
	clock := &now
	store := newF2F5RedisStore(t, func() int64 { return *clock })
	scope := Scope{Kind: "account", AccountRuntimeKey: "f2f5-redis-legacy"}
	state := f2f5JobsState(scope, "OPEN", 1_000)
	required := int64(1)
	count := int64(4)
	state.ConfirmationFailuresRequired = &required
	state.ConfirmationFailureCount = &count
	if result, err := store.Restore(ctx, state, clock); err != nil || result.Status != "applied" {
		t.Fatalf("restore = (%s, %v)", result.Status, err)
	}
	got, err := store.Get(ctx, scope, clock)
	if err != nil {
		t.Fatalf("修复后 get 不应报错: %v", err)
	}
	if got.RetryAtMs == nil || *got.RetryAtMs != 1_000 {
		t.Fatalf("F5: retryAt = %v, 期望 1000", got.RetryAtMs)
	}
	if got.ConfirmationFailureCount == nil || *got.ConfirmationFailureCount != 1 {
		t.Fatalf("F2: count = %v, 期望 1", got.ConfirmationFailureCount)
	}
	*clock = 1_000
	acquired, err := store.AcquireCanaryLease(ctx, AcquireLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "c1",
		LeaseID: "lease-c1", LeaseUntilMs: *clock + 3_000, NowMs: clock,
	})
	if err != nil || acquired.Status != "applied" {
		t.Fatalf("F5: canary = (%s, %v), 期望 applied", acquired.Status, err)
	}
}
