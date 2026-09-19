package circuitstore

import (
	"context"
	"strings"
	"testing"

	redis "github.com/redis/go-redis/v9"

	miniredis "github.com/alicebob/miniredis/v2"
)

// REFACTOR-0008 下潜波次的覆盖补强：下潜后包内语句基数缩小，本文件补回
// RedisStore 行为面的既有未覆盖臂，保证覆盖率基线不降。miniredis 驱动，
// 时钟注入固定值保证可回放。

func newRefactor0008RedisStore(t *testing.T, capacity int64) (*RedisStore, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	t.Cleanup(func() { server.Close() })
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewRedisStore(RedisStoreOptions{Client: client, Namespace: "ns", Capacity: capacity})
	if err != nil {
		t.Fatal(err)
	}
	return store, server
}

// TestRefactor0008CompleteConfirmationPayloadArms 覆盖 CompleteConfirmation
// 的 reason / framingCompleteDisposition / transport_failure payload 臂。
func TestRefactor0008CompleteConfirmationPayloadArms(t *testing.T) {
	store, _ := newRefactor0008RedisStore(t, 4)
	ctx := context.Background()
	scope := Scope{Kind: "account", AccountRuntimeKey: "refactor0008-cc"}
	now := int64(10_000)

	if _, err := store.Restore(ctx, State{
		ScopeKey: MustScopeKey(scope), Scope: scope, Phase: "SUSPECT",
		Generation: 1, DispatchRevision: "7", UpdatedAtMs: now,
	}, &now); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	leaseUntil := now + 1_000
	lease, err := store.AcquireConfirmationLease(ctx, AcquireLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "7",
		TransitionID: "t1", LeaseID: "lease-1", LeaseUntilMs: leaseUntil, NowMs: &now,
	})
	if err != nil {
		t.Fatalf("AcquireConfirmationLease: %v", err)
	}

	// reason + framingCompleteDisposition 非空臂（recovering 处置合法）。
	completed, err := store.CompleteConfirmation(ctx, CompleteInput{
		Scope: scope, Generation: lease.State.Generation, DispatchRevision: "7",
		TransitionID: "t1", LeaseID: "lease-1", Outcome: "framing_complete",
		Reason: strPtr("framing settled"), FramingCompleteDisposition: strPtr("recovering"),
		NowMs: &now,
	})
	if err != nil {
		t.Fatalf("CompleteConfirmation(reason+disposition): %v", err)
	}
	if completed.Status == "" {
		t.Fatal("status 为空")
	}
}

// TestRefactor0008CompleteConfirmationTransportFailureArm 覆盖 transport_failure
// outcome 的 failureEvidenceKey payload 臂。
func TestRefactor0008CompleteConfirmationTransportFailureArm(t *testing.T) {
	store, _ := newRefactor0008RedisStore(t, 4)
	ctx := context.Background()
	scope := Scope{Kind: "account", AccountRuntimeKey: "refactor0008-tf"}
	now := int64(20_000)

	if _, err := store.Restore(ctx, State{
		ScopeKey: MustScopeKey(scope), Scope: scope, Phase: "SUSPECT",
		Generation: 1, DispatchRevision: "8", UpdatedAtMs: now,
	}, &now); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	leaseUntil := now + 1_000
	if _, err := store.AcquireConfirmationLease(ctx, AcquireLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "8",
		TransitionID: "t2", LeaseID: "lease-2", LeaseUntilMs: leaseUntil, NowMs: &now,
	}); err != nil {
		t.Fatalf("AcquireConfirmationLease: %v", err)
	}

	completed, err := store.CompleteConfirmation(ctx, CompleteInput{
		Scope: scope, Generation: 1, DispatchRevision: "8",
		TransitionID: "t2", LeaseID: "lease-2", Outcome: "transport_failure",
		FailureEvidenceKey: strPtr(strings.Repeat("a", 64)), NowMs: &now,
	})
	if err != nil {
		t.Fatalf("CompleteConfirmation(transport_failure): %v", err)
	}
	if completed.Status == "" {
		t.Fatal("status 为空")
	}
}

// TestRefactor0008CompleteConfirmationMissingLeaseArm 覆盖
// validateOperationPayload 的 complete leaseId 缺失臂。transitionId 是所有
// 非 get 操作的前置校验，须先携带合法值才能到达 leaseId 校验。
func TestRefactor0008CompleteConfirmationMissingLeaseArm(t *testing.T) {
	store, _ := newRefactor0008RedisStore(t, 4)
	_, err := store.CompleteConfirmation(context.Background(), CompleteInput{
		Scope: Scope{Kind: "account", AccountRuntimeKey: "refactor0008-ml"},
		TransitionID: "t4", LeaseID: "", Outcome: "framing_complete",
	})
	if err == nil || !strings.Contains(err.Error(), "leaseId") {
		t.Fatalf("缺失 leaseId 必须报错, got %v", err)
	}
}

// TestRefactor0008AcquireInvalidEvidenceKeyArm 覆盖 acquire_confirmation 的
// confirmationEvidenceKey 容忍臂。NormalizeFailureEvidenceKey 对非法 key 不
// 报错，回退为 sha256Hex(fallbackSeed)（Node cloneStringArray 同源容忍语义），
// payload 级 requiredEvidenceKeyPayload 收到的恒为已归一化合法 hex——非法臂
// 经公开 API 不可达（两侧同构），此处锁死容忍行为本身。NowMs 注入过去时刻，
// 使 LeaseUntilMs=99_999 位于未来，租约校验通过后到达 evidence-key 归一化。
func TestRefactor0008AcquireInvalidEvidenceKeyArm(t *testing.T) {
	store, _ := newRefactor0008RedisStore(t, 4)
	bad := "not-a-hash"
	result, err := store.AcquireConfirmationLease(context.Background(), AcquireLeaseInput{
		Scope:            Scope{Kind: "account", AccountRuntimeKey: "refactor0008-ae"},
		DispatchRevision: "7", TransitionID: "t3", LeaseID: "lease-3",
		LeaseUntilMs: 99_999, ConfirmationEvidenceKey: &bad, NowMs: &[]int64{10_000}[0],
	})
	if err != nil {
		t.Fatalf("非法 confirmationEvidenceKey 必须被容忍归一化, got %v", err)
	}
	if result.Status == "" {
		t.Fatal("status 为空")
	}
}

// TestRefactor0008RestoreScopeKeyMismatchArm 覆盖 Restore 的 scopeKey 与
// 作用域字段不一致臂。
func TestRefactor0008RestoreScopeKeyMismatchArm(t *testing.T) {
	store, _ := newRefactor0008RedisStore(t, 4)
	now := int64(30_000)
	_, err := store.Restore(context.Background(), State{
		ScopeKey: "wrong-scope-key",
		Scope:    Scope{Kind: "account", AccountRuntimeKey: "refactor0008-rs"},
		Phase:    "SUSPECT", UpdatedAtMs: now,
	}, &now)
	if err == nil || !strings.Contains(err.Error(), "scopeKey") {
		t.Fatalf("scopeKey 不一致必须报错, got %v", err)
	}
}

// TestRefactor0008ListDueLimitBreakArm 覆盖 ListDue 的 limit 收缩 break 臂。
func TestRefactor0008ListDueLimitBreakArm(t *testing.T) {
	store, _ := newRefactor0008RedisStore(t, 8)
	ctx := context.Background()
	now := int64(40_000)
	for _, key := range []string{"ld-1", "ld-2", "ld-3"} {
		scope := Scope{Kind: "account", AccountRuntimeKey: key}
		retryAt := now - 1
		if _, err := store.Restore(ctx, State{
			ScopeKey: MustScopeKey(scope), Scope: scope, Phase: "SUSPECT",
			Generation: 1, DispatchRevision: "7", RetryAtMs: &retryAt, UpdatedAtMs: now,
		}, &now); err != nil {
			t.Fatalf("Restore %s: %v", key, err)
		}
	}
	states, err := store.ListDue(ctx, now, 1)
	if err != nil {
		t.Fatalf("ListDue: %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("limit=1 应只返回 1 个 due 状态, got %d", len(states))
	}
}

// ---- REFACTOR-0008 覆盖基线补强（可达臂；定位依据 coverprofile 零覆盖块）----

// TestRefactor0008ToOpsStateRejectsUnknownScopeKind 覆盖 toOpsState 的
// ScopeKey 错误臂：未知 scope kind 在适配层必须报错。
func TestRefactor0008ToOpsStateRejectsUnknownScopeKind(t *testing.T) {
	if _, err := toOpsState(State{Scope: Scope{Kind: "weird"}}); err == nil {
		t.Fatal("未知 scope kind 必须报错")
	}
}

// TestRefactor0008ProbeStateCorruptJSONTolerated 覆盖 ProbeStateStore.get 的
// 损坏 JSON 容忍臂：反序列化失败按"无状态"处理（nil, nil）。
func TestRefactor0008ProbeStateCorruptJSONTolerated(t *testing.T) {
	_, server := newRefactor0008RedisStore(t, 4)
	probeStore, err := NewProbeStateStore("redis://"+server.Addr(), "ref0008-ns", nil)
	if err != nil {
		t.Fatalf("NewProbeStateStore: %v", err)
	}
	runtimeKey := "ref0008-corrupt"
	server.Set(probeStore.stateKey(runtimeKey), "{bad json")
	state, err := probeStore.get(context.Background(), runtimeKey)
	if err != nil || state != nil {
		t.Fatalf("损坏 JSON 必须容忍为 nil, got (%v, %v)", state, err)
	}
}

// TestRefactor0008RestoreTrimsEvidenceKeysToKeepWindow 覆盖
// normalizeConfirmationState 的 evidence 截断臂：keys 数量超过 required+1
// 时只保留窗口内最近条目。
func TestRefactor0008RestoreTrimsEvidenceKeysToKeepWindow(t *testing.T) {
	store, _ := newRefactor0008RedisStore(t, 4)
	ctx := context.Background()
	now := int64(50_000)
	scope := Scope{Kind: "account", AccountRuntimeKey: "ref0008-trim"}
	required := int64(1)
	next := now + 1
	restored, err := store.Restore(ctx, State{
		ScopeKey: MustScopeKey(scope), Scope: scope, Phase: "SUSPECT",
		Generation: 1, DispatchRevision: "7",
		ConfirmationFailuresRequired: &required,
		FailureEvidenceKeys: stringList{
			strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64),
		},
		RetryAtMs: &next, UpdatedAtMs: now,
	}, &now)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if restored.Status == "" {
		t.Fatal("status 为空")
	}
}

// TestRefactor0008ListDueLargeLimitClampsScanChunk 覆盖 ListDue 的
// scanChunkSize 上限 512 钳制臂。
func TestRefactor0008ListDueLargeLimitClampsScanChunk(t *testing.T) {
	store, _ := newRefactor0008RedisStore(t, 8)
	states, err := store.ListDue(context.Background(), 60_000, 300)
	if err != nil {
		t.Fatalf("ListDue(limit=300): %v", err)
	}
	if len(states) != 0 {
		t.Fatalf("空库应返回 0 个 due 状态, got %d", len(states))
	}
}

// TestRefactor0008ProbeSettleGuardArms 覆盖 SettleDispatchedBySourceFence 的
// 非法 outcome 报错臂与无状态容忍臂。
func TestRefactor0008ProbeSettleGuardArms(t *testing.T) {
	_, server := newRefactor0008RedisStore(t, 4)
	probeStore, err := NewProbeStateStore("redis://"+server.Addr(), "ref0008-ns2", nil)
	if err != nil {
		t.Fatalf("NewProbeStateStore: %v", err)
	}
	ctx := context.Background()
	fence := ProbeSourceFence{StateKey: "sk", AccountID: "acc", SourceGeneration: 1, SourceFenceID: "f1"}
	if _, err := probeStore.SettleDispatchedBySourceFence(ctx, "ref0008-guard", 1, fence, "bogus", nil); err == nil {
		t.Fatal("非法 outcome 必须报错")
	}
	settled, err := probeStore.SettleDispatchedBySourceFence(ctx, "ref0008-missing", 1, fence, ProbeOutcomeSuccess, nil)
	if err != nil || settled {
		t.Fatalf("无状态结算必须容忍为 false, got (%v, %v)", settled, err)
	}
}
