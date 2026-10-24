package auditlog

// w13f 波次测试：W13 幻影信封撤回。producer.Retract 把撤回命令排队到
// workerLoop（免预算、fire-and-forget），retractOne 续租后调用
// store.RetractInProgress；store 侧 DELETE 带 lifecycle_status='in_progress'
// 守卫，终态行与未知 id 都是 no-op，撤回与 finalize 竞争时终态行必胜。

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestW13FRetractDispatchesStoreDelete(t *testing.T) {
	fake := &fakeStore{renewOK: true}
	producer := NewProducer(fake, fixedLeaseSource{OwnerID: "owner-1", FenceToken: 7}, Config{OwnerLease: 30 * time.Second}, nil)
	producer.Retract("audit-envelope-1")
	fake.waitForCalls(t, 1)
	got := fake.retractions()
	if len(got) != 1 || got[0] != "audit-envelope-1" {
		t.Fatalf("worker 必须按 id 撤回一次: %v", got)
	}
}

func TestW13FRetractRenewFailureSkipsDelete(t *testing.T) {
	fake := &fakeStore{renewErr: errors.New("lease lost")}
	producer := NewProducer(fake, fixedLeaseSource{OwnerID: "owner-1", FenceToken: 7}, Config{OwnerLease: 30 * time.Second}, nil)
	producer.Retract("audit-envelope-2")
	fake.waitForCalls(t, 1)
	if got := fake.retractions(); len(got) != 0 {
		t.Fatalf("续租失败不得触达 store 删除: %v", got)
	}
}

func TestW13FRetractStoreErrorIsSwallowed(t *testing.T) {
	// store 删除失败只记 WARN，不得影响 worker 继续消费。
	fake := &fakeStore{renewOK: true, retractErr: errors.New("db down")}
	producer := NewProducer(fake, fixedLeaseSource{OwnerID: "owner-1", FenceToken: 7}, Config{OwnerLease: 30 * time.Second}, nil)
	producer.Retract("audit-envelope-3")
	fake.waitForCalls(t, 1)
	if got := fake.retractions(); len(got) != 1 {
		t.Fatalf("删除尝试必须发生: %v", got)
	}
	producer.Capture(producerTestInput("w13f-after-error"))
	// 撤回路径计 renew+retract 两次，persist 路径计 renew+persist+hot 三次。
	fake.waitForCalls(t, 5)
	if persisted := fake.persistedInputs(); len(persisted) != 1 {
		t.Fatalf("撤回失败后 producer 必须继续工作: %+v", persisted)
	}
}

func TestW13FRetractIgnoresEmptyIDAndNilProducer(t *testing.T) {
	var nilProducer *Producer
	nilProducer.Retract("audit-x") // nil 接收者必须 no-op
	fake := &fakeStore{renewOK: true}
	producer := NewProducer(fake, fixedLeaseSource{OwnerID: "owner-1", FenceToken: 7}, Config{OwnerLease: 30 * time.Second}, nil)
	producer.Retract("")
	if fake.countCalls() != 0 {
		t.Fatal("空 id 不得产生任何 store 调用")
	}
}

func w13fStore(t *testing.T) (*sqlStore, OwnerLease) {
	t.Helper()
	store, implementation := openPostgresStore(t)
	return implementation, acquireLease(t, store)
}

func TestW13FStoreRetractInProgressLifecycleGuard(t *testing.T) {
	ctx := context.Background()
	store, lease := w13fStore(t)

	// in_progress 信封：首次撤回删除成功，重复撤回 no-op。
	envelope := fixture("w13f-env-1", LifecycleInProgress)
	if _, err := store.Persist(ctx, lease, envelope); err != nil {
		t.Fatal(err)
	}
	retracted, err := store.RetractInProgress(ctx, lease, "w13f-env-1")
	if err != nil || !retracted {
		t.Fatalf("信封撤回必须命中一次: retracted=%v err=%v", retracted, err)
	}
	if retracted, _ := store.RetractInProgress(ctx, lease, "w13f-env-1"); retracted {
		t.Fatal("已删除的信封二次撤回必须 no-op")
	}

	// 终态行：lifecycle 守卫拒绝删除，行必须保留。
	final := fixture("w13f-fin-1", LifecycleFinalized)
	if _, err := store.Persist(ctx, lease, final); err != nil {
		t.Fatal(err)
	}
	if retracted, err := store.RetractInProgress(ctx, lease, "w13f-fin-1"); err != nil || retracted {
		t.Fatalf("终态行不得被撤回: retracted=%v err=%v", retracted, err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM juhe_dataset.audit_logs WHERE id = 'w13f-fin-1'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("终态行必须保留: count=%d err=%v", count, err)
	}

	// 未知 id 与空 id 都是 no-op，未知 id 不报错。
	if retracted, err := store.RetractInProgress(ctx, lease, "w13f-unknown"); err != nil || retracted {
		t.Fatalf("未知 id 必须 no-op: retracted=%v err=%v", retracted, err)
	}
	if retracted, err := store.RetractInProgress(ctx, lease, ""); err != nil || retracted {
		t.Fatalf("空 id 必须 no-op: retracted=%v err=%v", retracted, err)
	}

	// 失效租约拒绝删除（ErrOwnerLeaseLost）。
	if _, err := store.RetractInProgress(ctx, OwnerLease{}, "w13f-env-1"); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("空租约必须拒绝: %v", err)
	}
}
