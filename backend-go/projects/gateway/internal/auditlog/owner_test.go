package auditlog

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRunOwnerRejectsNilKeeper pins the shared-lease entry contract: the
// resident owner fails fast without a keeper instead of running unfenced.
func TestRunOwnerRejectsNilKeeper(t *testing.T) {
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	defer store.Close()
	if err := RunOwner(context.Background(), store, nil, Config{OwnerLease: time.Minute, RetentionInterval: time.Hour}, nil); err == nil {
		t.Fatal("resident owner must refuse a nil keeper")
	}
}

// TestRunOwnerStopsWithContextAndReleasesLease pins the graceful-stop
// contract inherited from the retired RunInputServer: context cancellation
// returns nil, the keeper releases the row, and a successor can acquire it
// immediately.
func TestRunOwnerStopsWithContextAndReleasesLease(t *testing.T) {
	cfg := sqliteConfig(t, t.TempDir())
	cfg.RetentionInterval = time.Hour
	store := openSQLiteStore(t, cfg)
	defer store.Close()

	keeper, ok, err := StartLeaseKeeper(context.Background(), store, cfg.InstanceID, time.Minute, nil)
	if err != nil || !ok {
		t.Fatalf("start keeper: ok=%v err=%v", ok, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunOwner(ctx, store, keeper, cfg, nil) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("graceful stop returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunOwner did not stop after cancellation")
	}
	keeper.Close()

	if _, ok, err := store.AcquireOwnerLease(context.Background(), "successor", time.Minute); err != nil || !ok {
		t.Fatalf("successor acquisition after close: ok=%v err=%v", ok, err)
	}
}

// TestRunOwnerReacquiresAfterRealLeaseLoss pins the revised lease-loss
// lifecycle (2026-09-27): after a genuine loss (row expired and taken by a
// rival) the component surfaces a terminal lease error instead of serving
// fenced retention, and a supervisor restart re-enters RunOwner, whose entry
// re-acquires the row fresh once it is free again — instead of replaying the
// stored error without touching the database.
func TestRunOwnerReacquiresAfterRealLeaseLoss(t *testing.T) {
	cfg := sqliteConfig(t, t.TempDir())
	cfg.RetentionInterval = time.Hour
	store := openSQLiteStore(t, cfg)
	defer store.Close()

	keeper, ok, err := StartLeaseKeeper(context.Background(), store, cfg.InstanceID, 2*time.Second, nil)
	if err != nil || !ok {
		t.Fatalf("start keeper: ok=%v err=%v", ok, err)
	}
	defer keeper.Close()

	// 真实失租：把租约行置为过期，再由 rival 接管（fence 递增）；keeper 的
	// 下一次续租 0 行更新 → 按既有语义终态 ErrOwnerLeaseLost。
	implementation := store.(*sqlStore)
	expired := dbTime(ModeSQLite, time.Now().UTC().Add(-time.Minute))
	if _, err := implementation.db.Exec(
		`UPDATE `+implementation.leaseTable()+` SET lease_until=? WHERE lease_key='f3-audit-log-persistence' AND owner_id=?`,
		expired, cfg.InstanceID); err != nil {
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
		`UPDATE `+implementation.leaseTable()+` SET lease_until=? WHERE lease_key='f3-audit-log-persistence'`,
		expired); err != nil {
		t.Fatal(err)
	}
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

// TestSameOwnerReacquiresUnexpiredLease 覆盖 2026-10-14 生产死锁的获取侧：
// 未过期租约的 owner_id 与获取方相同时直接接管并递增 fence；异 owner 的未过期
// 行仍必须拒绝。同 owner 再获取只来自本实例自己的重启/失租恢复。
func TestSameOwnerReacquiresUnexpiredLease(t *testing.T) {
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	defer store.Close()
	ctx := context.Background()

	first, ok, err := store.AcquireOwnerLease(ctx, "same-owner", time.Minute)
	if err != nil || !ok {
		t.Fatalf("首次获取失败: ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.AcquireOwnerLease(ctx, "other-owner", time.Minute); err != nil || ok {
		t.Fatalf("异 owner 未过期行必须拒绝: ok=%v err=%v", ok, err)
	}
	second, ok, err := store.AcquireOwnerLease(ctx, "same-owner", time.Minute)
	if err != nil || !ok {
		t.Fatalf("同 owner 未过期行必须接管: ok=%v err=%v", ok, err)
	}
	if second.FenceToken <= first.FenceToken {
		t.Fatalf("同 owner 接管必须递增 fence: first=%d second=%d", first.FenceToken, second.FenceToken)
	}
	renewed, err := store.RenewOwnerLease(ctx, first, time.Minute)
	if err != nil || renewed {
		t.Fatalf("旧 fence 续约必须被拒绝: renewed=%v err=%v", renewed, err)
	}
	renewed, err = store.RenewOwnerLease(ctx, second, time.Minute)
	if err != nil || !renewed {
		t.Fatalf("新 fence 续约必须成功: renewed=%v err=%v", renewed, err)
	}
}

// TestRunOwnerRunsRetentionPass pins the cadence transfer: the retention pass
// scheduled by the resident owner executes within the configured interval and
// deletes an expired success log exactly like the retired listener loop.
func TestRunOwnerRunsRetentionPass(t *testing.T) {
	cfg := sqliteConfig(t, t.TempDir())
	// Retention policy defaults (3 success days) cannot age the fixture; use
	// the zero-success-retention mode so the cutoff is the hot window (1h)
	// and the minimum valid interval (1s, validateRetentionPolicy floor) so
	// the first pass fires within the test deadline.
	cfg.SuccessRetentionDays = 0
	cfg.SuccessHotRetentionHours = 0
	cfg.SuccessSampleRate = 0
	cfg.RetentionInterval = time.Second
	store := openSQLiteStore(t, cfg)
	defer store.Close()

	keeper, ok, err := StartLeaseKeeper(context.Background(), store, cfg.InstanceID, time.Minute, nil)
	if err != nil || !ok {
		t.Fatalf("start keeper: ok=%v err=%v", ok, err)
	}
	defer keeper.Close()

	input := producerTestInput("retention-pass")
	// The keeper already holds the row; write under its fence like the
	// producer would (a second AcquireOwnerLease would be refused).
	if _, err := store.Persist(context.Background(), keeper.Lease(), input); err != nil {
		t.Fatal(err)
	}
	// Age the row past every cutoff by back-dating created_at (the retention
	// scan keys on the canonical created_at column).
	if err := ageAuditRow(t, store, input.ID); err != nil {
		t.Fatal(err)
	}
	remaining := func() int {
		t.Helper()
		implementation := store.(*sqlStore)
		var count int
		if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE id=?`, input.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if remaining() != 1 {
		t.Fatal("fixture row must exist before the retention pass")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunOwner(ctx, store, keeper, cfg, nil) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if remaining() == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("RunOwner after retention pass: %v", err)
	}
	if remaining() != 0 {
		t.Fatal("resident owner retention pass must delete the aged success row")
	}
}

func ageAuditRow(t *testing.T, store Store, id string) error {
	t.Helper()
	implementation, ok := store.(*sqlStore)
	if !ok {
		t.Skip("retention ageing helper requires the SQLite store implementation")
	}
	_, err := implementation.db.Exec(`UPDATE audit_logs SET created_at='2000-01-01T00:00:00.000000000Z' WHERE id=?`, id)
	return err
}
