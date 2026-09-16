package opsjobs

import (
	"context"
	"strings"
	"testing"
	"time"
)

// w9h_memory_store_test.go 覆盖内存电路存储的剩余状态机分支：容量驱逐、
// 租约过期归一化、派发版本替换、升级证据清理、Due 列表与父子投影。

func w9hScope(id string) CircuitScope { return accountScope(id) }

func TestW9HMemoryStoreCapacityEvictionAndListDue(t *testing.T) {
	store := w7dMemoryStore(t, 2)
	// 容量满且全部活跃 → 最旧条目被驱逐。
	for _, id := range []string{"acc-a", "acc-b"} {
		raw := w7dSuspectRaw(t, w9hScope(id), 1)
		if _, err := store.Restore(context.Background(), raw, 1_000); err != nil {
			t.Fatal(err)
		}
	}
	third := w7dSuspectRaw(t, w9hScope("acc-c"), 1)
	if _, err := store.Restore(context.Background(), third, 1_100); err != nil {
		t.Fatal(err)
	}
	if size := store.Size(1_100); size > 2 {
		t.Fatalf("容量上限被突破: %d", size)
	}
	// Due 列表：有 RetryAt 的条目到期才出现。
	due, err := store.ListDue(context.Background(), 5_000, 10)
	if err != nil || len(due) == 0 {
		t.Fatalf("due=%d err=%v", len(due), err)
	}
	limited, err := store.ListDue(context.Background(), 5_000, 1)
	if err != nil || len(limited) != 1 {
		t.Fatalf("limited due=%d err=%v", len(limited), err)
	}
}

func TestW9HMemoryStoreReplaceDispatchRevisionArms(t *testing.T) {
	store := w7dMemoryStore(t, 4)
	scope := w9hScope("acc-rev")
	raw := w7dSuspectRaw(t, scope, 2)
	if _, err := store.Restore(context.Background(), raw, 1_000); err != nil {
		t.Fatal(err)
	}
	// 派发版本替换：同代更新 revision。
	result, err := store.ReplaceDispatchRevision(context.Background(), scope, "9", "tx-replace", 2_000)
	if err != nil || result.Status != CircuitMutationApplied || result.State.DispatchRevision != "9" {
		t.Fatalf("replace result=%#v err=%v", result, err)
	}
	// 幂等重放同一 transition。
	replay, err := store.ReplaceDispatchRevision(context.Background(), scope, "9", "tx-replace", 2_100)
	if err != nil || replay.Status != CircuitMutationIdempotent {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	// 未知 scope → fresh CLOSED 条目按新 revision 落地。
	missing, err := store.ReplaceDispatchRevision(context.Background(), w9hScope("acc-absent"), "9", "tx-2", 2_200)
	if err != nil || missing.Status != CircuitMutationApplied || missing.State.Phase != CircuitPhaseClosed || missing.State.DispatchRevision != "9" {
		t.Fatalf("fresh replace=%#v err=%v", missing, err)
	}
	// 账户级派发版本替换（账户运行键不存在时返回当前 revision）。
	revision, err := store.ReplaceAccountDispatchRevision(context.Background(), "acc-rev", "10", "tx-account", 2_300)
	if err != nil || revision < 1 {
		t.Fatalf("account replace revision=%d err=%v", revision, err)
	}
}

func TestW9HMemoryStoreClearEscalationEvidenceAndGuards(t *testing.T) {
	store := w7dMemoryStore(t, 4)
	scope := w9hScope("acc-esc")
	raw := w7dSuspectRaw(t, scope, 3)
	if _, err := store.Restore(context.Background(), raw, 1_000); err != nil {
		t.Fatal(err)
	}
	// 无证据 → false。
	cleared, err := store.ClearAccountEscalationEvidence(context.Background(), "acc-esc", "7", "evidence-w9h", 2_000)
	if err != nil || cleared {
		t.Fatalf("cleared=%t err=%v", cleared, err)
	}
	// 缺失账户 → false。
	cleared, err = store.ClearAccountEscalationEvidence(context.Background(), "acc-absent", "7", "evidence-w9h", 2_000)
	if err != nil || cleared {
		t.Fatalf("absent cleared=%t err=%v", cleared, err)
	}
	// Get 缺失账户返回空白 CLOSED（freshEntry）。
	state, err := store.Get(context.Background(), w9hScope("acc-absent"), 2_000)
	if err != nil || state.Phase != CircuitPhaseClosed {
		t.Fatalf("blank state=%#v err=%v", state, err)
	}
}

func TestW9HMemoryStoreExpiredLeaseNormalized(t *testing.T) {
	store := w7dMemoryStore(t, 4)
	scope := w9hScope("acc-lease")
	raw := w7dSuspectRaw(t, scope, 2)
	if _, err := store.Restore(context.Background(), raw, 1_000); err != nil {
		t.Fatal(err)
	}
	identity := CircuitTransitionIdentity{Scope: scope, Generation: 2, DispatchRevision: "7", TransitionID: "tx-lease", NowMS: 2_000}
	acquired, err := store.AcquireConfirmationLease(context.Background(), identity, CircuitLeaseSpec{LeaseID: "l-w9h", LeaseUntilMS: 3_000})
	if err != nil || acquired.Status != CircuitMutationApplied {
		t.Fatalf("acquire=%#v err=%v", acquired, err)
	}
	// 租约过期后 normalizeExpiredLease 清掉租约，新 transition 可以拿租约。
	expiredIdentity := identity
	expiredIdentity.TransitionID = "tx-lease-2"
	expiredIdentity.NowMS = 9_000
	acquired, err = store.AcquireConfirmationLease(context.Background(), expiredIdentity, CircuitLeaseSpec{LeaseID: "l-w9h-2", LeaseUntilMS: 12_000})
	if err != nil || acquired.Status != CircuitMutationApplied {
		t.Fatalf("expired lease re-acquire=%#v err=%v", acquired, err)
	}
}

func TestW9HMemoryStoreCanaryCompletionArms(t *testing.T) {
	store := w7dMemoryStore(t, 4)
	scope := w9hScope("acc-canary")
	// OPEN 状态 + 未来 RetryAt → not_due（RecoveringCanaryNotDue 分支）。
	open := openState(t, scope, 4, "7", 1_000)
	open.RetryAtMS = int64Ptr(50_000)
	if _, err := store.Restore(context.Background(), open, 1_000); err != nil {
		t.Fatal(err)
	}
	identity := CircuitTransitionIdentity{Scope: scope, Generation: 4, DispatchRevision: "7", TransitionID: "tx-canary-early", NowMS: 2_000}
	early, err := store.AcquireCanaryLease(context.Background(), identity, CircuitLeaseSpec{LeaseID: "canary-early", LeaseUntilMS: 9_000})
	if err != nil || early.Status != CircuitMutationNotDue {
		t.Fatalf("early canary=%#v err=%v", early, err)
	}
	// RetryAt 到期后：canary 租约进入 HALF_OPEN。
	dueIdentity := identity
	dueIdentity.TransitionID = "tx-canary"
	dueIdentity.NowMS = 51_000
	result, err := store.AcquireCanaryLease(context.Background(), dueIdentity, CircuitLeaseSpec{LeaseID: "canary-1", LeaseUntilMS: 59_000})
	if err != nil || result.Status != CircuitMutationApplied || result.State.Phase != CircuitPhaseHalfOpen {
		t.Fatalf("canary lease=%#v err=%v", result, err)
	}
	// framing_complete 成功使 OPEN 进入 RECOVERING（恢复计数未满不关闭）。
	completeIdentity := dueIdentity
	completeIdentity.TransitionID = "tx-canary-complete"
	completed, err := store.CompleteCanary(context.Background(), completeIdentity, "canary-1", CircuitCompletion{Outcome: CircuitVerdictFramingComplete, FramingCompleteDisposition: "closed"})
	if err != nil || completed.Status != CircuitMutationApplied || completed.State.Phase != CircuitPhaseRecovering {
		t.Fatalf("canary recovering=%#v err=%v", completed, err)
	}
	// 完成后 RECOVERING 状态仍在存储中可读。
	state, err := store.Get(context.Background(), scope, 52_000)
	if err != nil || state.Phase != CircuitPhaseRecovering {
		t.Fatalf("post-canary state=%#v err=%v", state, err)
	}
}

func TestW9HMemoryStoreRestoreGuards(t *testing.T) {
	store := w7dMemoryStore(t, 4)
	scope := w9hScope("acc-restore")
	raw := w7dSuspectRaw(t, scope, 2)
	if _, err := store.Restore(context.Background(), raw, 1_000); err != nil {
		t.Fatal(err)
	}
	// 同代旧 UpdatedAt 覆盖被拒绝（保留新条目）。
	stale := raw
	stale.UpdatedAtMS = 500
	if _, err := store.Restore(context.Background(), stale, 1_500); err != nil {
		t.Fatal(err)
	}
	// 更新一代覆盖成功。
	newer := raw
	newer.Generation = 5
	newer.UpdatedAtMS = 3_000
	if _, err := store.Restore(context.Background(), newer, 3_000); err != nil {
		t.Fatal(err)
	}
	state, err := store.Get(context.Background(), scope, 3_100)
	if err != nil || state.Generation != 5 {
		t.Fatalf("generation=%d err=%v", state.Generation, err)
	}
	// helpers：containsString / firstNonEmpty / normalizeClockMS。
	if !containsString([]string{"a", "b"}, "b") || containsString(nil, "a") {
		t.Fatal("containsString")
	}
	if firstNonEmpty("", "", "x") != "x" || firstNonEmpty() != "" {
		t.Fatal("firstNonEmpty")
	}
	if normalizeClockMS(-1) != 0 || normalizeClockMS(42) != 42 {
		t.Fatal("normalizeClockMS")
	}
	if runtimeKeyMatchesDispatchRevisionTarget("acc|rev-1", "acc|rev-1") != true {
		t.Fatal("runtime key match")
	}
	_ = time.Now
	_ = strings.Contains
}
