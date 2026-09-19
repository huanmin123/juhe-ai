package auditlog

// w16a 覆盖收尾第二批：owner 组件的租约默认 TTL、释放失败日志、组件致命臂
// 与 maintenance 协程的 nil logger / 取消上下文 / 租约丢失臂。全部通过
// w16aOwnerFakeStore（Store 接口 fake）注入，不触碰真实数据库。

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

type w16aOwnerFakeStore struct {
	acquireLease OwnerLease
	acquireOK    bool
	acquireErr   error
	renewOK      bool
	renewErr     error
	releaseErr   error
	retentionErr error
}

func (s *w16aOwnerFakeStore) EnsureSchema(context.Context) error { return nil }

func (s *w16aOwnerFakeStore) AcquireOwnerLease(context.Context, string, time.Duration) (OwnerLease, bool, error) {
	return s.acquireLease, s.acquireOK, s.acquireErr
}

func (s *w16aOwnerFakeStore) RenewOwnerLease(context.Context, OwnerLease, time.Duration) (bool, error) {
	return s.renewOK, s.renewErr
}

func (s *w16aOwnerFakeStore) ReleaseOwnerLease(context.Context, OwnerLease) error {
	return s.releaseErr
}

func (s *w16aOwnerFakeStore) CleanupOwnedBlobTemps(context.Context, OwnerLease, time.Time) error {
	return nil
}

func (s *w16aOwnerFakeStore) CleanupOrphanedBlobTemps(context.Context, OwnerLease, time.Time) error {
	return nil
}

func (s *w16aOwnerFakeStore) Persist(context.Context, OwnerLease, AuditLogInput) (PersistResult, error) {
	return PersistResult{}, nil
}

func (s *w16aOwnerFakeStore) CleanupRetention(context.Context, OwnerLease, RetentionConfig) (RetentionResult, error) {
	if s.retentionErr != nil {
		return RetentionResult{}, s.retentionErr
	}
	return RetentionResult{}, nil
}

func (s *w16aOwnerFakeStore) AppendHotSearch(context.Context, OwnerLease, []AuditLogInput) (int, error) {
	return 0, nil
}

func (s *w16aOwnerFakeStore) CleanupHotSearch(context.Context, OwnerLease, time.Time, int) (int64, error) {
	return 0, nil
}

func (s *w16aOwnerFakeStore) SearchHotSearch(context.Context, HotSearchOptions) (HotSearchResult, error) {
	return HotSearchResult{}, nil
}

func (s *w16aOwnerFakeStore) Close() error { return nil }

func w16aOwnerConfig() Config {
	return Config{
		RetentionInterval:        time.Second,
		RetentionBatchSize:       10,
		SuccessHotRetentionHours: 1,
		SuccessSampleRate:        0.1,
		SuccessRetentionDays:     3,
		ProblemRetentionDays:     7,
	}
}

func TestW16aStartLeaseKeeperDefaultsTTLAndLogsReleaseFailure(t *testing.T) {
	fake := &w16aOwnerFakeStore{
		acquireLease: OwnerLease{OwnerID: "w16a-owner", FenceToken: 3},
		acquireOK:    true,
		releaseErr:   errors.New("w16a release boom"),
	}
	keeper, ok, err := StartLeaseKeeper(context.Background(), fake, "w16a-owner", 0, slog.Default())
	if err != nil || !ok {
		t.Fatalf("acquire: %t %v", ok, err)
	}
	defer keeper.Close()
	if keeper.TTL() != defaultOwnerLease {
		t.Fatalf("ttl<=0 必须回退到默认 TTL: %v", keeper.TTL())
	}
	if keeper.Lease().FenceToken != 3 {
		t.Fatalf("lease=%+v", keeper.Lease())
	}
	if keeper.LostError() != nil {
		t.Fatalf("持有期间不得报告丢失: %v", keeper.LostError())
	}
	// Close 触发释放失败日志臂；Close 必须幂等。
	keeper.Close()
}

func TestW16aRunOwnerSurfacesRetentionComponentFatal(t *testing.T) {
	fake := &w16aOwnerFakeStore{
		acquireLease: OwnerLease{OwnerID: "w16a-owner", FenceToken: 4},
		acquireOK:    true,
		retentionErr: errors.New("w16a retention boom"),
	}
	keeper, ok, err := StartLeaseKeeper(context.Background(), fake, "w16a-owner", time.Minute, slog.Default())
	if err != nil || !ok {
		t.Fatalf("acquire: %t %v", ok, err)
	}
	defer keeper.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunOwner(ctx, fake, keeper, w16aOwnerConfig(), slog.Default()) }()
	select {
	case err := <-done:
		if err == nil || !errors.Is(err, fake.retentionErr) {
			t.Fatalf("retention 组件异常必须作为 owner 错误返回: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunOwner 未在组件异常后退出")
	}
}

func TestW16aRunRetentionMaintenanceArms(t *testing.T) {
	lease := OwnerLease{OwnerID: "w16a-owner", FenceToken: 5}
	cfg := w16aOwnerConfig()

	// 臂 A：nil logger 必须回退到默认 logger（直接以 nil logger 调用私有函数）。
	healthy := &w16aOwnerFakeStore{}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	doneA := runRetentionMaintenance(canceled, healthy, lease, cfg, nil, nil)
	select {
	case <-doneA:
	case <-time.After(5 * time.Second):
		t.Fatal("取消上下文下 maintenance 未退出")
	}

	// 臂 B：错误 + 已取消上下文 → 静默退出，不写入 fatal。
	boom := &w16aOwnerFakeStore{retentionErr: errors.New("w16a canceled boom")}
	doneB := runRetentionMaintenance(canceled, boom, lease, cfg, slog.Default(), nil)
	select {
	case <-doneB:
	case <-time.After(5 * time.Second):
		t.Fatal("取消上下文下 maintenance 未退出")
	}

	// 臂 C：租约丢失 → fatal 收到 ErrOwnerLeaseLost 并退出。
	lost := &w16aOwnerFakeStore{retentionErr: ErrOwnerLeaseLost}
	fatal := make(chan error, 1)
	doneC := runRetentionMaintenance(context.Background(), lost, lease, cfg, slog.Default(), fatal)
	select {
	case err := <-fatal:
		if !errors.Is(err, ErrOwnerLeaseLost) {
			t.Fatalf("租约丢失必须透传 ErrOwnerLeaseLost: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("租约丢失未上报 fatal")
	}
	select {
	case <-doneC:
	case <-time.After(5 * time.Second):
		t.Fatal("租约丢失后 maintenance 未退出")
	}

	// 臂 D：普通错误 → fatal 收到包装错误（fatal 通道已满时静默丢弃）。
	generic := &w16aOwnerFakeStore{retentionErr: errors.New("w16a generic boom")}
	blocked := make(chan error, 1)
	blocked <- errors.New("pre-occupied")
	doneD := runRetentionMaintenance(context.Background(), generic, lease, cfg, slog.Default(), blocked)
	select {
	case <-doneD:
	case <-time.After(5 * time.Second):
		t.Fatal("普通错误后 maintenance 未退出")
	}
	select {
	case err := <-blocked:
		if err.Error() != "pre-occupied" {
			t.Fatalf("fatal 通道满时不得覆盖既有错误: %v", err)
		}
	default:
		t.Fatal("fatal 通道应为阻塞态")
	}
}
