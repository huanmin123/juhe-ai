package auditlog

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestWIRenewOwnerLeaseSQLiteContract(t *testing.T) {
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)

	// 契约：正确 owner + fence 续约成功。
	renewed, err := store.RenewOwnerLease(ctx, lease, time.Minute)
	if err != nil || !renewed {
		t.Fatalf("续约=%v err=%v", renewed, err)
	}
	// 错误 fence / owner → false（不报错）。
	wrongFence := OwnerLease{OwnerID: lease.OwnerID, FenceToken: lease.FenceToken + 1}
	if renewed, err := store.RenewOwnerLease(ctx, wrongFence, time.Minute); err != nil || renewed {
		t.Fatalf("错误 fence 续约=%v err=%v", renewed, err)
	}
	wrongOwner := OwnerLease{OwnerID: "other-owner", FenceToken: lease.FenceToken}
	if renewed, err := store.RenewOwnerLease(ctx, wrongOwner, time.Minute); err != nil || renewed {
		t.Fatalf("错误 owner 续约=%v err=%v", renewed, err)
	}
	// 释放后续约失败（lease 已过期）。
	if err := store.ReleaseOwnerLease(ctx, lease); err != nil {
		t.Fatalf("释放失败: %v", err)
	}
	if renewed, err := store.RenewOwnerLease(ctx, lease, time.Minute); err != nil || renewed {
		t.Fatalf("释放后续约=%v err=%v", renewed, err)
	}
}

func TestWIRetainAliasMatchesCleanupRetention(t *testing.T) {
	cfg := sqliteConfig(t, t.TempDir())
	store := openSQLiteStore(t, cfg)
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)

	input := fixture("wi-retain-1", LifecycleFinalized)
	if _, err := store.Persist(ctx, lease, input); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	// Retain 是 CleanupRetention 的维护侧别名：同一 fence、同一删除语义。
	config := RetentionConfig{
		SuccessCutoff:                time.Now().UTC().Add(time.Hour),
		SuccessHotCutoff:             time.Now().UTC().Add(2 * time.Hour),
		FailureCutoff:                time.Now().UTC().Add(time.Hour),
		ErrorGroupCutoff:             time.Now().UTC().Add(time.Hour),
		SuccessSampleBucketThreshold: 10000,
	}
	result, err := store.(*sqlStore).Retain(ctx, lease, config)
	if err != nil {
		t.Fatalf("Retain 失败: %v", err)
	}
	if result.DeletedLogs != 1 {
		t.Fatalf("result=%+v", result)
	}
	// 配置非法（cutoff 缺失）必须报错。
	if _, err := store.(*sqlStore).Retain(ctx, lease, RetentionConfig{}); err == nil {
		t.Fatal("空配置必须报错")
	}
	// 错误 fence → 保留租约失败。
	stale := OwnerLease{OwnerID: lease.OwnerID, FenceToken: lease.FenceToken + 5}
	if _, err := store.(*sqlStore).Retain(ctx, stale, config); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("过期 fence 必须报 ErrOwnerLeaseLost: %v", err)
	}
}

func TestWILeaseKeeperRenewsAndReleases(t *testing.T) {
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	defer store.Close()
	ctx := context.Background()

	keeper, ok, err := StartLeaseKeeper(ctx, store, "wi-keeper", 3*time.Second, slog.Default())
	if err != nil || !ok {
		t.Fatalf("启动 keeper=%v err=%v", ok, err)
	}
	if got := keeper.TTL(); got != 3*time.Second {
		t.Fatalf("TTL=%v", got)
	}
	lease := keeper.Lease()
	if lease.OwnerID != "wi-keeper" || lease.FenceToken == 0 {
		t.Fatalf("lease=%+v", lease)
	}
	// 未丢失（短窗口内 LostError 保持 nil）。
	time.Sleep(200 * time.Millisecond)
	if err := keeper.LostError(); err != nil {
		t.Fatalf("正常续约不得丢失: %v", err)
	}
	// 优雅关闭：释放租约后另一个 owner 可立即接管。
	keeper.Close()
	_, ok, err = store.AcquireOwnerLease(ctx, "successor", time.Minute)
	if err != nil || !ok {
		t.Fatalf("继任接管=%v err=%v", ok, err)
	}
	keeper.Close() // 二次关闭安全。
	if err := keeper.LostError(); err != nil {
		t.Fatalf("正常关闭不得有 LostError: %v", err)
	}
}

func TestWILeaseKeeperTerminalOnRenewFailure(t *testing.T) {
	// 契约：续租传输错误对进程是终态（不是 retry-until-ttl）。
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	ctx := context.Background()
	keeper, ok, err := StartLeaseKeeper(ctx, store, "wi-doomed", 3*time.Second, slog.Default())
	if err != nil || !ok {
		t.Fatalf("启动 keeper=%v err=%v", ok, err)
	}
	// 关闭底层库使下一次续租传输失败。
	_ = store.Close()
	select {
	case <-keeper.Lost():
		lostErr := keeper.LostError()
		if lostErr == nil || !strings.Contains(lostErr.Error(), "续租 F3 audit owner lease 失败") {
			t.Fatalf("LostError=%v", lostErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("续租失败后 Lost 未关闭")
	}
	// 已丢失的 keeper Close 只停循环，不尝试释放。
	keeper.Close()
}

func TestWILeaseKeeperRefusesWhenHeld(t *testing.T) {
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	defer store.Close()
	ctx := context.Background()
	first := acquireLease(t, store)
	_ = first
	// 行已被别的 owner 持有 → ok=false，不产生 keeper。
	keeper, ok, err := StartLeaseKeeper(ctx, store, "second-owner", time.Minute, nil)
	if err != nil || ok || keeper != nil {
		t.Fatalf("被持有时应拒绝: ok=%v keeper=%v err=%v", ok, keeper, err)
	}
}

func TestWIMinDurationAndReportRetentionFatal(t *testing.T) {
	if got := minDuration(time.Second, 2*time.Second); got != time.Second {
		t.Fatalf("minDuration(1s,2s)=%v", got)
	}
	if got := minDuration(3*time.Second, 2*time.Second); got != 2*time.Second {
		t.Fatalf("minDuration(3s,2s)=%v", got)
	}
	// fatal channel 为 nil → 安全 no-op。
	reportRetentionFatal(context.Background(), nil, errors.New("x"))
	// channel 已满 → default 分支不阻塞。
	fatal := make(chan error, 1)
	fatal <- errors.New("first")
	reportRetentionFatal(context.Background(), fatal, errors.New("second"))
	// ctx 取消 → select 走 Done 分支。
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	reportRetentionFatal(cancelled, make(chan error), errors.New("third"))
}

func TestWIRunOwnerValidatesPolicyAndKeeper(t *testing.T) {
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	defer store.Close()
	cfg := sqliteConfig(t, t.TempDir())
	// keeper 缺失必须报错。
	if err := RunOwner(context.Background(), store, nil, cfg, slog.Default()); err == nil {
		t.Fatal("缺 keeper 必须报错")
	}
	// retention 配置非法必须报错（批量大小越界）。
	badCfg := cfg
	badCfg.RetentionBatchSize = 0
	if err := RunOwner(context.Background(), store, &LeaseKeeper{}, badCfg, slog.Default()); err == nil {
		t.Fatal("非法 retention 配置必须报错")
	}
	// context 取消 → 优雅 nil 返回（keeper 缺失检查在配置之后，这里补一个
	// 合法 keeper 需要 store 租约；直接验证 ctx 取消优先于组件循环）。
	keeper, ok, err := StartLeaseKeeper(context.Background(), store, "wi-owner", time.Minute, slog.Default())
	if err != nil || !ok {
		t.Fatalf("启动 keeper=%v err=%v", ok, err)
	}
	defer keeper.Close()
	runCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RunOwner(runCtx, store, keeper, cfg, slog.Default()); err != nil {
		t.Fatalf("取消后应 nil 返回: %v", err)
	}
}

func TestWIRunRetentionMaintenanceReportsFatal(t *testing.T) {
	// 每次到期执行都失败 → fatal 通道收到组件错误并停止 goroutine。
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	_ = store.Close() // 关闭底层库，让 CleanupRetention 失败。
	cfg := Config{RetentionInterval: 10 * time.Millisecond}
	fatal := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runRetentionMaintenance(ctx, store, OwnerLease{}, cfg, slog.Default(), fatal)
	select {
	case err := <-fatal:
		if err == nil {
			t.Fatal("fatal 错误不得为 nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance 失败未上报 fatal")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance goroutine 未退出")
	}
	// ctx 取消 → 正常退出且不上报 fatal。
	quietStore := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	defer quietStore.Close()
	quietFatal := make(chan error, 1)
	quietCtx, quietCancel := context.WithCancel(context.Background())
	quietDone := runRetentionMaintenance(quietCtx, quietStore, OwnerLease{}, cfg, slog.Default(), quietFatal)
	quietCancel()
	select {
	case <-quietDone:
	case <-time.After(5 * time.Second):
		t.Fatal("取消后 maintenance 未退出")
	}
	select {
	case err := <-quietFatal:
		t.Fatalf("取消路径不得上报 fatal: %v", err)
	default:
	}
}
