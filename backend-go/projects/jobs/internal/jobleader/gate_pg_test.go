package jobleader

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/taskruns"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// TestGatePostgresTakeover 在隔离 PG 测试库上验证：A 持全局租约时 B 保持
// standby，A 释放后 B 在租约过期语义允许时立即接管。pgtestkit 未授权时
// 自动 skip，测试库随用例删除。
func TestGatePostgresTakeover(t *testing.T) {
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_stats`)
		return err
	})
	store := taskruns.NewStore(db)
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	leaseKey := "scheduled:jobs-global-leader-it:global"
	gateA, err := New(Options{
		Store:         store,
		LeaseKey:      leaseKey,
		OwnerID:       "it-owner-a",
		TTL:           2 * time.Second,
		RetryInterval: 20 * time.Millisecond,
		RenewInterval: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New A: %v", err)
	}
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	gateA.Start(ctxA)
	if err := gateA.Wait(ctxA); err != nil {
		t.Fatalf("A wait: %v", err)
	}

	gateB, err := New(Options{
		Store:         store,
		LeaseKey:      leaseKey,
		OwnerID:       "it-owner-b",
		TTL:           2 * time.Second,
		RetryInterval: 20 * time.Millisecond,
		RenewInterval: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New B: %v", err)
	}
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	gateB.Start(ctxB)
	waitCtx, waitCancel := context.WithTimeout(ctxB, 150*time.Millisecond)
	defer waitCancel()
	if err := gateB.Wait(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("B must stay standby while A holds lease: %v", err)
	}

	cancelA()
	select {
	case <-gateA.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("A did not stop")
	}
	takeoverCtx, takeoverCancel := context.WithTimeout(ctxB, 3*time.Second)
	defer takeoverCancel()
	if err := gateB.Wait(takeoverCtx); err != nil {
		t.Fatalf("B takeover: %v", err)
	}
	if !gateB.IsLeader() {
		t.Fatal("B must be leader after A release")
	}
	cancelB()
	select {
	case <-gateB.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("B did not stop")
	}
}
