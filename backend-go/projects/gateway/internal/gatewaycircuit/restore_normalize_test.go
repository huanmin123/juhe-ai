package gatewaycircuit

import (
	"context"
	"strings"
	"testing"
)

// F2+F5（状态机专项 2026-09-25）restore 归一化补强单测。
//
// 业务库 incident 遗留行经 IncidentToRuntimeState → store.Restore 进入运行态，
// 必须在 Go 侧 restore 归一化（normalizeRestoreConfirmationState）清洗到满足
// shared/platform/circuitstate/lua.go 的硬不变式（不改 Lua）：
//   - F5：OPEN/RECOVERING 且 RetryAtMs 为 NULL（业务库 NextTransitionAtMs 为空）
//     归一化后必须带 retryAt。否则 Redis 驱动下 acquire_canary 永远判 not_due
//     （lua.go:508），due 索引缺失（lua.go:56-58）、listdue 直接 error
//     （lua.go:926-928），恢复流程永久卡死；memory Get 对 nil 视为到期，两驱动分叉。
//   - F2：confirmationFailureCount > required 钳制到 required。否则该条目进
//     Redis 后 normalize（lua.go:87-91）对所有操作直接 error。
//   - 正常数据逐字段不被改动。

func f2f5LegacyState(scope Scope, phase string, updatedAtMs int64) State {
	state := ClosedState(scope, "7", 1, "t-legacy", updatedAtMs)
	state.Phase = phase
	state.IncidentID = strPtr("inc-legacy")
	return state
}

func TestF2F5NormalizeRestoreConfirmationState(t *testing.T) {
	scope := accountScope("f2f5-unit")
	validKey := strings.Repeat("a", 64)

	t.Run("F5 open nil retryAt patched to updatedAtMs", func(t *testing.T) {
		state := f2f5LegacyState(scope, PhaseOpen, 1_000)
		normalized, err := normalizeRestoreConfirmationState(state)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		if normalized.RetryAtMs == nil || *normalized.RetryAtMs != 1_000 {
			t.Fatalf("F5: retryAt = %v, 期望 UpdatedAtMs=1000", normalized.RetryAtMs)
		}
	})

	t.Run("F5 recovering nil retryAt patched to updatedAtMs", func(t *testing.T) {
		state := f2f5LegacyState(scope, PhaseRecovering, 2_000)
		normalized, err := normalizeRestoreConfirmationState(state)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		if normalized.RetryAtMs == nil || *normalized.RetryAtMs != 2_000 {
			t.Fatalf("F5: retryAt = %v, 期望 UpdatedAtMs=2000", normalized.RetryAtMs)
		}
	})

	t.Run("F5 leased open nil retryAt patched to leaseUntilMs", func(t *testing.T) {
		state := f2f5LegacyState(scope, PhaseOpen, 1_000)
		state.Lease = &Lease{Kind: LeaseKindConfirmation, LeaseID: "lease-1", LeaseUntilMs: 9_000}
		normalized, err := normalizeRestoreConfirmationState(state)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		if normalized.RetryAtMs == nil || *normalized.RetryAtMs != 9_000 {
			t.Fatalf("F5: retryAt = %v, 期望租约截止 9000（与 SUSPECT 补法一致）", normalized.RetryAtMs)
		}
	})

	t.Run("F2 count above required clamped", func(t *testing.T) {
		state := f2f5LegacyState(scope, PhaseSuspect, 1_000)
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
		state := f2f5LegacyState(scope, PhaseSuspect, 1_000)
		// 合法 key 保留；同一 key 的大写形式 ToLower 后去重；非法 key 静默过滤
		// （types.go FailureEvidenceKeysOf，对应 memory 静默过滤语义；Lua 侧
		// lua.go:96-100 对非法 key 直接 error，故 restore 入口必须先清洗）。
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
		state := f2f5LegacyState(scope, PhaseSuspect, 1_000)
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

	t.Run("well-formed open with retryAt untouched", func(t *testing.T) {
		state := f2f5LegacyState(scope, PhaseOpen, 1_000)
		retryAt := int64(5_000)
		state.RetryAtMs = &retryAt
		normalized, err := normalizeRestoreConfirmationState(state)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		if normalized.RetryAtMs == nil || *normalized.RetryAtMs != 5_000 {
			t.Fatalf("已有 retryAt 被改动: %v", normalized.RetryAtMs)
		}
	})

	t.Run("closed state untouched", func(t *testing.T) {
		state := f2f5LegacyState(scope, PhaseClosed, 1_000)
		normalized, err := normalizeRestoreConfirmationState(state)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		if normalized.Phase != PhaseClosed || normalized.ConfirmationFailuresRequired != nil ||
			normalized.ConfirmationFailureCount != nil || normalized.RetryAtMs != nil {
			t.Fatalf("CLOSED 相被改动: %+v", normalized)
		}
	})
}

// F5 端到端（memory 驱动）：遗留 OPEN NULL retryAt 经 Restore 后带 retryAt，
// canary 立即可获取，恢复流程不再卡死。
func TestF5MemoryRestoreUnblocksLegacyOpenCanary(t *testing.T) {
	ctx := context.Background()
	now := int64(2_000)
	clock := &now
	store, err := NewMemoryStore(MemoryStoreOptions{Capacity: 100, Now: func() int64 { return *clock }})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	scope := accountScope("f5-legacy-open")
	state := f2f5LegacyState(scope, PhaseOpen, 1_000)
	if result, err := store.Restore(ctx, state, clock); err != nil || result.Status != MutationApplied {
		t.Fatalf("restore = (%s, %v)", result.Status, err)
	}
	got, err := store.Get(ctx, scope, clock)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.RetryAtMs == nil || *got.RetryAtMs != 1_000 {
		t.Fatalf("F5: restore 后 retryAt = %v, 期望 1000", got.RetryAtMs)
	}
	acquired, err := store.AcquireCanaryLease(ctx, AcquireCanaryLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "c1",
		LeaseID: "lease-c1", LeaseUntilMs: *clock + 3_000, NowMs: clock,
	})
	if err != nil || acquired.Status != MutationApplied {
		t.Fatalf("F5: canary = (%s, %v), 期望 applied", acquired.Status, err)
	}
}

// F2 端到端（memory 驱动）：遗留 count > required 经 Restore 后钳制。
func TestF2MemoryRestoreClampsLegacyCount(t *testing.T) {
	ctx := context.Background()
	now := int64(2_000)
	clock := &now
	store, err := NewMemoryStore(MemoryStoreOptions{Capacity: 100, Now: func() int64 { return *clock }})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	scope := accountScope("f2-legacy-count")
	state := f2f5LegacyState(scope, PhaseSuspect, 1_000)
	required := int64(1)
	count := int64(4)
	state.ConfirmationFailuresRequired = &required
	state.ConfirmationFailureCount = &count
	if result, err := store.Restore(ctx, state, clock); err != nil || result.Status != MutationApplied {
		t.Fatalf("restore = (%s, %v)", result.Status, err)
	}
	got, err := store.Get(ctx, scope, clock)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ConfirmationFailureCount == nil || *got.ConfirmationFailureCount != 1 {
		t.Fatalf("F2: get count = %v, 期望钳制为 1", got.ConfirmationFailureCount)
	}
}

// F2+F5 全链（memory 驱动）：业务库 incident 遗留行（NextTransitionAtMs 为
// NULL、ConsecutiveFailures > required）→ IncidentToRuntimeState → Restore。
func TestF2F5IncidentToRuntimeStateRestoreChain(t *testing.T) {
	ctx := context.Background()
	now := int64(2_000)
	clock := &now
	store, err := NewMemoryStore(MemoryStoreOptions{Capacity: 100, Now: func() int64 { return *clock }})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	scope := accountScope("f2f5-incident")
	required := int64(1)
	incident := IncidentRecord{
		CircuitScopeKey:              MustScopeKey(scope),
		AccountID:                    "f2f5",
		AccountRuntimeKey:            scope.AccountRuntimeKey,
		ScopeKind:                    IncidentScopeKindAccount,
		IncidentID:                   "inc-legacy",
		State:                        IncidentStateOpen,
		Generation:                   1,
		DispatchRevision:             7,
		TransitionID:                 "t-legacy",
		ConfirmationFailuresRequired: required,
		ConsecutiveFailures:          4,
		NextTransitionAtMs:           nil, // F5：遗留 NULL retryAt
		UpdatedAtMs:                  1_000,
	}
	legacy := IncidentToRuntimeState(incident, map[string]string{})
	if legacy.RetryAtMs != nil || *legacy.ConfirmationFailureCount != 4 {
		t.Fatalf("前置态不符: retryAt=%v count=%v", legacy.RetryAtMs, legacy.ConfirmationFailureCount)
	}
	if result, err := store.Restore(ctx, legacy, clock); err != nil || result.Status != MutationApplied {
		t.Fatalf("restore = (%s, %v)", result.Status, err)
	}
	got, err := store.Get(ctx, scope, clock)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.RetryAtMs == nil || *got.RetryAtMs != 1_000 {
		t.Fatalf("F5: retryAt = %v, 期望 1000", got.RetryAtMs)
	}
	if got.ConfirmationFailureCount == nil || *got.ConfirmationFailureCount != 1 {
		t.Fatalf("F2: count = %v, 期望 1", got.ConfirmationFailureCount)
	}
}

// F5 端到端（Redis 驱动）：遗留 OPEN NULL retryAt 经 Redis Restore 后带
// retryAt，acquire_canary 不再永远 not_due（修复前 lua.go:508 卡死路径）。
func TestF5RedisRestoreUnblocksLegacyOpenCanary(t *testing.T) {
	ctx := context.Background()
	now := int64(2_000)
	clock := &now
	store, _ := newTestRedisStore(t, 100, func() int64 { return *clock })
	scope := accountScope("f5-redis-legacy")
	state := f2f5LegacyState(scope, PhaseOpen, 1_000)
	if result, err := store.Restore(ctx, state, clock); err != nil || result.Status != MutationApplied {
		t.Fatalf("restore = (%s, %v)", result.Status, err)
	}
	got, err := store.Get(ctx, scope, clock)
	if err != nil {
		t.Fatalf("F5: 修复后 get 不应报错: %v", err)
	}
	if got.RetryAtMs == nil || *got.RetryAtMs != 1_000 {
		t.Fatalf("F5: retryAt = %v, 期望 1000", got.RetryAtMs)
	}
	*clock = 1_000
	acquired, err := store.AcquireCanaryLease(ctx, AcquireCanaryLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "c1",
		LeaseID: "lease-c1", LeaseUntilMs: *clock + 3_000, NowMs: clock,
	})
	if err != nil || acquired.Status != MutationApplied {
		t.Fatalf("F5: canary = (%s, %v), 期望 applied", acquired.Status, err)
	}
}

// F2 端到端（Redis 驱动）：遗留 count > required 经 Redis Restore 后钳制；
// 修复后 Get 不再命中 lua.go:87-91 的 'invalid confirmationFailureCount'。
func TestF2RedisRestoreClampsLegacyCount(t *testing.T) {
	ctx := context.Background()
	now := int64(2_000)
	clock := &now
	store, _ := newTestRedisStore(t, 100, func() int64 { return *clock })
	scope := accountScope("f2-redis-legacy")
	state := f2f5LegacyState(scope, PhaseSuspect, 1_000)
	required := int64(1)
	count := int64(4)
	state.ConfirmationFailuresRequired = &required
	state.ConfirmationFailureCount = &count
	state.FailureEvidenceKeys = stringList{strings.Repeat("b", 64)}
	if result, err := store.Restore(ctx, state, clock); err != nil || result.Status != MutationApplied {
		t.Fatalf("restore = (%s, %v)", result.Status, err)
	}
	got, err := store.Get(ctx, scope, clock)
	if err != nil {
		t.Fatalf("F2: 修复后 get 不应报错: %v", err)
	}
	if got.ConfirmationFailureCount == nil || *got.ConfirmationFailureCount != 1 {
		t.Fatalf("F2: count = %v, 期望 1", got.ConfirmationFailureCount)
	}
	if got.ConfirmationFailuresRequired == nil || *got.ConfirmationFailuresRequired != 1 {
		t.Fatalf("F2: required = %v, 期望 1", got.ConfirmationFailuresRequired)
	}
}
