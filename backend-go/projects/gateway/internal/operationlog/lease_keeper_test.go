package operationlog

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// openKeeperTestStore opens an isolated SQLite F4 store for keeper tests.
func openKeeperTestStore(t *testing.T) Store {
	t.Helper()
	root := t.TempDir()
	business := filepath.Join(root, "business.sqlite3")
	createBusinessSettings(t, business, "365")
	store, err := OpenStore(Config{Enabled: true, InstanceID: "keeper-owner", Mode: ModeSQLite, DatabasePath: filepath.Join(root, "operation.sqlite3"), BusinessSettingsPath: business})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// waitKeeperPersisted polls until the producer record becomes visible or the
// deadline passes (the producer persists asynchronously).
func waitKeeperPersisted(t *testing.T, store Store, id string) ListResult {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result, err := store.List(context.Background(), ListOptions{})
		if err != nil {
			t.Fatalf("list persisted logs: %v", err)
		}
		for _, item := range result.Items {
			if item.ID == id {
				return result
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("producer record never persisted")
	return ListResult{}
}

// assertKeeperLeaseValid proves the lease row is still alive (lease_until>
// now, same owner+fence) after producer activity: the historical defect was a
// per-record renewal with a zero TTL that wrote lease_until=now and fenced
// every subsequent write out.
func assertKeeperLeaseValid(t *testing.T, store Store, lease OwnerLease, ttl time.Duration) {
	t.Helper()
	renewed, err := store.RenewOwnerLease(context.Background(), lease, ttl)
	if err != nil || !renewed {
		t.Fatalf("owner lease must stay valid after producer writes: renewed=%v err=%v", renewed, err)
	}
}

// TestLeaseKeeperSharedByProducerPersistsManagementLogs is the defect-1
// regression: the system-api producer and the F4 resident owner share one
// LeaseKeeper (single owner_id/fence_token); producer records persist and the
// lease stays live instead of being self-destructed by a zero-TTL renew.
func TestLeaseKeeperSharedByProducerPersistsManagementLogs(t *testing.T) {
	store := openKeeperTestStore(t)
	keeper, ok, err := StartLeaseKeeper(context.Background(), store, "keeper-owner", time.Minute, nil)
	if err != nil || !ok {
		t.Fatalf("start keeper: ok=%v err=%v", ok, err)
	}
	defer keeper.Close()

	// A second acquisition while the keeper holds the row must fail: the
	// historical in-process fight (producer vs sidecar) must stay impossible.
	if _, ok, err := store.AcquireOwnerLease(context.Background(), "sidecar", time.Minute); err != nil || ok {
		t.Fatalf("second holder must be refused: ok=%v err=%v", ok, err)
	}

	producer := NewProducer(store, keeper, Config{OwnerLease: keeper.TTL()}, nil)
	entry := Input{ID: "keeper-1", ActorSystemAccountID: "actor-1", ActorRole: "admin", Module: "accounts", Action: "update", OperationKey: "accounts.update", ResourceType: "account", Summary: "keeper shared lease", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	producer.Record(entry)
	waitKeeperPersisted(t, store, entry.ID)
	assertKeeperLeaseValid(t, store, keeper.Lease(), keeper.TTL())

	// Producer without a configured renewal TTL (Config{}): the zero-TTL
	// renew that used to self-destruct the fence is skipped, so writes still
	// land and the lease survives.
	bareProducer := NewProducer(store, keeper, Config{}, nil)
	bare := Input{ID: "keeper-2", ActorSystemAccountID: "actor-1", ActorRole: "admin", Module: "groups", Action: "update", OperationKey: "groups.update", ResourceType: "group", Summary: "zero ttl guard", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	bareProducer.Record(bare)
	waitKeeperPersisted(t, store, bare.ID)
	assertKeeperLeaseValid(t, store, keeper.Lease(), keeper.TTL())
}

// TestLeaseKeeperCloseReleasesLease proves the shutdown contract: after
// Close, a successor can acquire the row immediately (timely handover instead
// of a 30-60s expiry wait).
func TestLeaseKeeperCloseReleasesLease(t *testing.T) {
	store := openKeeperTestStore(t)
	keeper, ok, err := StartLeaseKeeper(context.Background(), store, "keeper-owner", time.Minute, nil)
	if err != nil || !ok {
		t.Fatalf("start keeper: ok=%v err=%v", ok, err)
	}
	keeper.Close()
	if keeper.LostError() != nil {
		t.Fatalf("clean close must not report lease loss: %v", keeper.LostError())
	}
	lease, ok, err := store.AcquireOwnerLease(context.Background(), "successor", time.Minute)
	if err != nil || !ok {
		t.Fatalf("successor acquisition after close: ok=%v err=%v", ok, err)
	}
	_ = lease
}

// TestRunOwnerRejectsNilKeeper pins the shared-lease entry contract: without
// a keeper the resident owner must fail fast instead of silently serving
// unfenced retention.
func TestRunOwnerRejectsNilKeeper(t *testing.T) {
	store := openKeeperTestStore(t)
	err := RunOwner(context.Background(), store, nil, Config{OwnerLease: time.Minute}, nil)
	if err == nil {
		t.Fatal("resident owner must refuse a nil keeper")
	}
}

// TestRunOwnerReacquiresAfterRealLeaseLoss 是 2026-09-28 生产缺陷（F3 同族）
// 的真实库回归：rival 接管租约行后组件按既有语义终态返回；supervisor 重启
// 再进入 RunOwner 时，入口必须在行空闲后重新 AcquireOwnerLease（新 fence）
// 恢复运行，而不是无限回放存储错误（旧行为：组件每 30s 重启一次、每次立即
// 失败，gateway 直到进程重启都无 F4 owner）。
func TestRunOwnerReacquiresAfterRealLeaseLoss(t *testing.T) {
	store := openKeeperTestStore(t)
	implementation := store.(*sqlStore)
	keeper, ok, err := StartLeaseKeeper(context.Background(), store, "keeper-owner", 2*time.Second, nil)
	if err != nil || !ok {
		t.Fatalf("start keeper: ok=%v err=%v", ok, err)
	}
	defer keeper.Close()
	cfg := Config{Enabled: true, InstanceID: "keeper-owner", OwnerLease: 2 * time.Second, RetentionInterval: time.Hour}

	// 真实失租：把租约行置为过期，再由 rival 接管（fence 递增）；keeper 的
	// 下一次续租 0 行更新 → 按既有语义终态 ErrOwnerLeaseLost。
	expired := storageTime(time.Now().UTC().Add(-time.Minute))
	if _, err := implementation.db.Exec(
		`UPDATE operation_log_owner_leases SET lease_until=? WHERE lease_key='f4-operation-log-persistence' AND owner_id=?`,
		expired, "keeper-owner"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.AcquireOwnerLease(context.Background(), "rival-owner", time.Minute); err != nil || !ok {
		t.Fatalf("rival takeover: ok=%v err=%v", ok, err)
	}
	select {
	case <-keeper.Lost():
		if !errors.Is(keeper.LostError(), ErrOwnerLeaseLost) {
			t.Fatalf("真实失租必须以 ErrOwnerLeaseLost 终态: %v", keeper.LostError())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("租约被接管后 Lost 未关闭")
	}

	// supervisor 第一次重启：rival 仍持有行，入口重取必须被拒并作为组件错误
	// 返回（fail-closed），而不是继续服务。
	if err := RunOwner(context.Background(), store, keeper, cfg, nil); err == nil {
		t.Fatal("rival 持有行时入口重取必须失败")
	}

	// rival 释放（行重新过期）后的下一次重启：入口重新 AcquireOwnerLease 成功，
	// keeper 清空终态并以新 fence 恢复运行；取消上下文应优雅 nil 返回。
	if _, err := implementation.db.Exec(
		`UPDATE operation_log_owner_leases SET lease_until=? WHERE lease_key='f4-operation-log-persistence'`,
		expired); err != nil {
		t.Fatal(err)
	}
	fenceBefore := keeper.Lease().FenceToken
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunOwner(ctx, store, keeper, cfg, nil) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if keeper.LostError() == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("行空闲后重启不得返回错误: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	if keeper.LostError() != nil {
		t.Fatalf("行空闲后入口重取必须成功并清空终态: %v", keeper.LostError())
	}
	if keeper.Lease().FenceToken <= fenceBefore {
		t.Fatalf("重取必须递增 fence token: before=%d after=%d", fenceBefore, keeper.Lease().FenceToken)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("恢复后取消应 nil 返回: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("恢复后的 RunOwner 未随取消退出")
	}
}

// TestProducerFollowsRotatedFence 是 producer 快照 fence 缺陷的回归：producer
// 绑定 keeper 而非冻结的 OwnerLease 快照，keeper 重取轮换 fence token 后，
// 后续写入必须以新 fence 落库（旧行为：renew 用旧 fence 0 行 → 每条记录都被
// 丢弃，直到进程重启）。
func TestProducerFollowsRotatedFence(t *testing.T) {
	store := openKeeperTestStore(t)
	implementation := store.(*sqlStore)
	// TTL=2s → 续租周期 1s（下限），接管后 Lost 秒级关闭，用例不拖长。
	keeper, ok, err := StartLeaseKeeper(context.Background(), store, "keeper-owner", 2*time.Second, nil)
	if err != nil || !ok {
		t.Fatalf("start keeper: ok=%v err=%v", ok, err)
	}
	defer keeper.Close()
	producer := NewProducer(store, keeper, Config{OwnerLease: keeper.TTL()}, nil)

	first := Input{ID: "fence-1", ActorSystemAccountID: "actor-1", ActorRole: "admin", Module: "accounts", Action: "update", OperationKey: "accounts.update", ResourceType: "account", Summary: "before rotation", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	producer.Record(first)
	waitKeeperPersisted(t, store, first.ID)

	// 真实失租 + 行空闲：expire → rival 接管 → rival 释放（行过期）。
	expired := storageTime(time.Now().UTC().Add(-time.Minute))
	if _, err := implementation.db.Exec(
		`UPDATE operation_log_owner_leases SET lease_until=? WHERE lease_key='f4-operation-log-persistence' AND owner_id=?`,
		expired, "keeper-owner"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.AcquireOwnerLease(context.Background(), "rival-owner", time.Minute); err != nil || !ok {
		t.Fatalf("rival takeover: ok=%v err=%v", ok, err)
	}
	select {
	case <-keeper.Lost():
	case <-time.After(10 * time.Second):
		t.Fatal("租约被接管后 Lost 未关闭")
	}
	if _, err := implementation.db.Exec(
		`UPDATE operation_log_owner_leases SET lease_until=? WHERE lease_key='f4-operation-log-persistence'`,
		expired); err != nil {
		t.Fatal(err)
	}
	if err := keeper.reacquire(context.Background()); err != nil {
		t.Fatalf("reacquire: %v", err)
	}

	second := Input{ID: "fence-2", ActorSystemAccountID: "actor-1", ActorRole: "admin", Module: "groups", Action: "update", OperationKey: "groups.update", ResourceType: "group", Summary: "after rotation", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	producer.Record(second)
	waitKeeperPersisted(t, store, second.ID)
	assertKeeperLeaseValid(t, store, keeper.Lease(), keeper.TTL())
}
