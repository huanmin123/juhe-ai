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
	store, _ := openPostgresStore(t)
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
	store, _, cfg := openPostgresStoreConfig(t)
	defer store.Close()
	cfg.RetentionInterval = time.Hour

	keeper, ok, err := StartLeaseKeeper(context.Background(), store, cfg.InstanceID, time.Minute, nil)
	if err != nil || !ok {
		t.Fatalf("start keeper: ok=%v err=%v", ok, err)
	}
	// 失败路径兜底：keeper 不得活过 store 关闭（t.Cleanup LIFO 保证先停
	// keeper 后关池），否则续租循环会以闭池续租噪声污染失败现场。
	t.Cleanup(keeper.Close)
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
	store, implementation, cfg := openPostgresStoreConfig(t)
	defer store.Close()
	cfg.RetentionInterval = time.Hour

	keeper, ok, err := StartLeaseKeeper(context.Background(), store, cfg.InstanceID, 2*time.Second, nil)
	if err != nil || !ok {
		t.Fatalf("start keeper: ok=%v err=%v", ok, err)
	}
	defer keeper.Close()

	// 真实失租：把租约行置为过期，再由 rival 接管（fence 递增）；keeper 的
	// 下一次续租 0 行更新 → 按既有语义终态 ErrOwnerLeaseLost。
	expired := time.Now().UTC().Add(-time.Minute)
	if _, err := implementation.db.Exec(
		implementation.bind(`UPDATE `+implementation.table("audit_log_owner_leases")+` SET lease_until=? WHERE lease_key='f3-audit-log-persistence' AND owner_id=?`),
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
		implementation.bind(`UPDATE `+implementation.table("audit_log_owner_leases")+` SET lease_until=? WHERE lease_key='f3-audit-log-persistence'`),
		expired); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunOwner(ctx, store, keeper, cfg, nil) }()
	// 截止必须大于 reacquire 内部 5s 的 AcquireOwnerLease 预算：负载下重取
	// 本身允许耗满预算，断言窗口小于内部预算会把正确的重取误判为失败。
	deadline := time.Now().Add(10 * time.Second)
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
	store, _ := openPostgresStore(t)
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
	store, _, cfg := openPostgresStoreConfig(t)
	defer store.Close()
	// Retention policy defaults (3 success days) cannot age the fixture; use
	// the zero-success-retention mode so the cutoff is the hot window (1h)
	// and the minimum valid interval (1s, validateRetentionPolicy floor) so
	// the first pass fires within the test deadline.
	cfg.SuccessRetentionDays = 0
	cfg.SuccessHotRetentionHours = 0
	cfg.SuccessSampleRate = 0
	cfg.RetentionInterval = time.Second

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
		if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM juhe_dataset.audit_logs WHERE id=$1`, input.ID).Scan(&count); err != nil {
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

	// 截止覆盖完整链路预算：schedulejitter(1s interval → ±0.5s) + 设置热读
	// 新建远程连接 + retention SQL；负载下远程 PG 往返显著变慢，5s 会把
	// 正确执行误判为超时。
	deadline := time.Now().Add(15 * time.Second)
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
		t.Skip("retention ageing helper requires the sqlStore implementation")
	}
	_, err := implementation.db.Exec(
		implementation.bind(`UPDATE `+implementation.table("audit_logs")+` SET created_at=? WHERE id=?`),
		time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), id)
	return err
}

// closeOrderFakeStore 复用 renewGraceFakeStore 的接口骨架，覆写续租与释放以
// 观测 Close 与续租循环的顺序：在途续租被通道阻塞，直到测试放行。
type closeOrderFakeStore struct {
	*renewGraceFakeStore
	renewStarted chan struct{}
	unblockRenew chan struct{}
	released     chan struct{}
	events       []string
}

func (s *closeOrderFakeStore) RenewOwnerLease(context.Context, OwnerLease, time.Duration) (bool, error) {
	select {
	case <-s.renewStarted:
	default:
		close(s.renewStarted)
	}
	<-s.unblockRenew
	s.mu.Lock()
	s.events = append(s.events, "renew-end")
	s.mu.Unlock()
	return true, nil
}

func (s *closeOrderFakeStore) ReleaseOwnerLease(context.Context, OwnerLease) error {
	s.mu.Lock()
	s.events = append(s.events, "release")
	s.mu.Unlock()
	select {
	case <-s.released:
	default:
		close(s.released)
	}
	return nil
}

// TestLeaseKeeperCloseReleasesAfterLoopQuiesces 钉住 Close 的生命周期契约
// （2026-10-26 W19 收口）：释放必须等续租循环完全静止——释放与最后一拍续租
// 竞争会把 0 行续租误判成失租终态，且 Close 返回后 keeper 不再触碰 store，
// 关池/删库的后续清理才是安全的。
func TestLeaseKeeperCloseReleasesAfterLoopQuiesces(t *testing.T) {
	store := &closeOrderFakeStore{
		renewGraceFakeStore: &renewGraceFakeStore{acquireOK: true},
		renewStarted:        make(chan struct{}),
		unblockRenew:        make(chan struct{}),
		released:            make(chan struct{}),
	}
	keeper, ok, err := StartLeaseKeeper(context.Background(), store, "close-order", 3*time.Second, nil)
	if err != nil || !ok {
		t.Fatalf("启动 keeper=%v err=%v", ok, err)
	}
	defer keeper.Close()

	// 等第一拍续租进入阻塞（续租周期为下限 1s）。
	select {
	case <-store.renewStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("续租循环未按时进入第一拍续租")
	}

	closed := make(chan struct{})
	go func() { keeper.Close(); close(closed) }()

	// Close 必须等待在途续租完成：释放不得发生在续租静止之前。
	select {
	case <-store.released:
		t.Fatal("Close 在续租循环静止前就释放了租约")
	case <-time.After(200 * time.Millisecond):
	}

	// 放行在途续租 → 循环退出 → Close 释放并返回。
	close(store.unblockRenew)
	select {
	case <-store.released:
	case <-time.After(5 * time.Second):
		t.Fatal("续租静止后 Close 未释放租约")
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close 未在释放后返回")
	}
	store.mu.Lock()
	events := append([]string(nil), store.events...)
	store.mu.Unlock()
	renewEnded, releasedAt := -1, -1
	for i, event := range events {
		if event == "renew-end" && renewEnded < 0 {
			renewEnded = i
		}
		if event == "release" && releasedAt < 0 {
			releasedAt = i
		}
	}
	if renewEnded < 0 || releasedAt < 0 || renewEnded > releasedAt {
		t.Fatalf("释放必须晚于最后一拍续租完成: events=%v", events)
	}
}
