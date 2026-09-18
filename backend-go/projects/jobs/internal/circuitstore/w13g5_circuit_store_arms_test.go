package circuitstore

// w13g5_circuit_store_arms_test.go 覆盖 RedisStore 确认租约/恢复/替换派发
// 修订的输入校验与 Eval 错误出口。

import (
	"context"
	"errors"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestW13g5StoreConfirmationValidationArms(t *testing.T) {
	server := miniredisRunForTest(t)
	t.Cleanup(func() { server.Close() })
	store, err := NewRedisStore(RedisStoreOptions{RedisURL: "redis://" + server.Addr(), Namespace: "w13g5-ns", Capacity: 10})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	scope := Scope{Kind: "account", AccountRuntimeKey: "w13g5-acc"}
	badKey := "w13g5-not-sha"
	if _, err := store.AcquireConfirmationLease(ctx, AcquireLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "1", TransitionID: "w13g5-tr",
		LeaseID: "w13g5-lease", LeaseUntilMs: 100, ExpectedFailureEvidenceKey: &badKey,
	}); err == nil {
		t.Fatal("非法 expectedFailureEvidenceKey 必须报错")
	}
	// transport_failure + 非法 key 走 fallbackSeed 归一（不报错，Eval 失败）。
	// transport_failure + 非法 key 走 fallbackSeed 归一（Redis 上键缺失不报错）。
	_, _ = store.CompleteConfirmation(ctx, CompleteInput{
		Scope: scope, Generation: 1, DispatchRevision: "1", TransitionID: "w13g5-tr",
		LeaseID: "w13g5-lease", Outcome: "transport_failure", FailureEvidenceKey: &badKey,
	})
}

func TestW13g5StoreRestoreMismatchArm(t *testing.T) {
	server := miniredisRunForTest(t)
	t.Cleanup(func() { server.Close() })
	store, err := NewRedisStore(RedisStoreOptions{RedisURL: "redis://" + server.Addr(), Namespace: "w13g5-ns", Capacity: 10})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	state := State{Scope: Scope{Kind: "account", AccountRuntimeKey: "w13g5-acc"}, ScopeKey: "w13g5-wrong-scope-key", Generation: 1}
	if _, err := store.Restore(ctx, state, nil); err == nil {
		t.Fatal("scopeKey 不一致必须报错")
	}
}

func mustInnerRedis(t *testing.T) redis.Cmdable {
	t.Helper()
	server := miniredisRunForTest(t)
	t.Cleanup(func() { server.Close() })
	inner := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = inner.Close() })
	return inner
}

func TestW13g5StoreReplaceAndClearEvalErrorArms(t *testing.T) {
	ctx := context.Background()
	stub := &evalStubClient{inner: mustInnerRedis(t), result: redis.NewCmdResult(nil, errors.New("w13g5 conn refused"))}
	store, err := NewRedisStore(RedisStoreOptions{Client: stub, Namespace: "w13g5-ns", Capacity: 10})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.ReplaceAccountDispatchRevision(ctx, "w13g5-acc", "w13g5-rev", "w13g5-tr", nil); err == nil {
		t.Fatal("Replace Eval 失败必须传播")
	}
	if _, err := store.ClearAccountEscalationEvidence(ctx, "w13g5-acc", "w13g5-rev", "w13g5-evidence", nil); err == nil {
		t.Fatal("Clear Eval 失败必须传播")
	}
	if _, err := store.ListDue(ctx, 1000, 10); err == nil {
		t.Fatal("ListDue Eval 失败必须传播")
	}
}
