package operationlog

// F4 owner 组件续租失败语义修订（2026-09-28 生产缺陷，BUG-0196 F3 同族）的
// 定向覆盖：通过 renewGraceFakeStore（Store 接口 fake，续租行为按调用次数脚
// 本化）验证——
// a) 传输类失败一次后重试成功 → keeper 存活不终态；
// b) 连续失败超过 2×TTL 放宽窗口 → 正常放弃退出；
// c) 0 行更新（过期/被接管）→ 立即按既有语义放弃；
// d) supervisor 重启（重新 RunOwner）→ 重新 AcquireOwnerLease，而不是重放
//    旧 keeper 的存储错误。
// 全部不触碰真实数据库。

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

var errRenewJitter = errors.New("renew transport jitter（模拟超时/网络抖动）")

type renewGraceFakeStore struct {
	mu           sync.Mutex
	acquireCalls int
	acquireOK    bool
	acquireErr   error
	fenceTokens  []int64
	renewCalls   int
	renewFunc    func(call int) (bool, error)
	releaseErr   error
	retentionErr error
}

func (s *renewGraceFakeStore) EnsureSchema(context.Context) error { return nil }

func (s *renewGraceFakeStore) AcquireOwnerLease(_ context.Context, _ string, _ time.Duration) (OwnerLease, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acquireCalls++
	token := int64(s.acquireCalls)
	s.fenceTokens = append(s.fenceTokens, token)
	return OwnerLease{OwnerID: "grace-owner", FenceToken: token}, s.acquireOK, s.acquireErr
}

func (s *renewGraceFakeStore) RenewOwnerLease(_ context.Context, _ OwnerLease, _ time.Duration) (bool, error) {
	s.mu.Lock()
	s.renewCalls++
	call := s.renewCalls
	renew := s.renewFunc
	s.mu.Unlock()
	if renew == nil {
		return true, nil
	}
	return renew(call)
}

func (s *renewGraceFakeStore) ReleaseOwnerLease(context.Context, OwnerLease) error {
	return s.releaseErr
}

func (s *renewGraceFakeStore) Persist(context.Context, OwnerLease, Input) (bool, error) {
	return true, nil
}

func (s *renewGraceFakeStore) List(context.Context, ListOptions) (ListResult, error) {
	return ListResult{}, nil
}

func (s *renewGraceFakeStore) Detail(context.Context, string, string) (DetailSupplement, bool, error) {
	return DetailSupplement{}, false, nil
}

func (s *renewGraceFakeStore) CleanupRetention(context.Context, OwnerLease, time.Time, int) (int64, error) {
	if s.retentionErr != nil {
		return 0, s.retentionErr
	}
	return 0, nil
}

func (s *renewGraceFakeStore) RetentionDays(context.Context, int) (int, error) {
	return 365, nil
}

func (s *renewGraceFakeStore) Close() error { return nil }

// renewGraceConfig 返回 retention 配置；TTL=3s → 续租周期=1s（下限）、
// 放宽窗口=2×3s=6s，让用例在秒级完成。
func renewGraceConfig() Config {
	return Config{Enabled: true, InstanceID: "grace-owner", OwnerLease: 3 * time.Second, RetentionInterval: time.Hour}
}

func startRenewGraceKeeper(t *testing.T, store *renewGraceFakeStore) *LeaseKeeper {
	t.Helper()
	store.mu.Lock()
	store.acquireOK = true
	store.mu.Unlock()
	keeper, ok, err := StartLeaseKeeper(context.Background(), store, "grace-owner", 3*time.Second, slog.Default())
	if err != nil || !ok {
		t.Fatalf("启动 keeper=%v err=%v", ok, err)
	}
	return keeper
}

// TestRenewGraceKeeperSurvivesSingleTransportFailure 覆盖 a)：第一次续租传输
// 失败后重试成功，keeper 必须保持存活、不终态（生产缺陷：一次 5s 超时即永久
// 关闭 Lost）。
func TestRenewGraceKeeperSurvivesSingleTransportFailure(t *testing.T) {
	store := &renewGraceFakeStore{renewFunc: func(call int) (bool, error) {
		if call == 1 {
			return false, errRenewJitter
		}
		return true, nil
	}}
	keeper := startRenewGraceKeeper(t, store)
	defer keeper.Close()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		calls := store.renewCalls
		store.mu.Unlock()
		if calls >= 3 { // 失败 1 次 + 成功 ≥2 次
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	store.mu.Lock()
	calls := store.renewCalls
	store.mu.Unlock()
	if calls < 3 {
		t.Fatalf("续租调用次数不足: %d", calls)
	}
	if err := keeper.LostError(); err != nil {
		t.Fatalf("一次传输失败后重试成功，keeper 不得终态: %v", err)
	}
	select {
	case <-keeper.Lost():
		t.Fatal("重试成功后 Lost 不得关闭")
	default:
	}
}

// TestRenewGraceKeeperGivesUpAfterGraceWindow 覆盖 b)：连续传输失败超过
// 2×TTL 放宽窗口后按终态放弃，错误包含窗口原因与原始错误。
func TestRenewGraceKeeperGivesUpAfterGraceWindow(t *testing.T) {
	store := &renewGraceFakeStore{renewFunc: func(int) (bool, error) {
		return false, errRenewJitter
	}}
	keeper := startRenewGraceKeeper(t, store)
	defer keeper.Close()

	select {
	case <-keeper.Lost():
		lostErr := keeper.LostError()
		if lostErr == nil || !strings.Contains(lostErr.Error(), "放宽窗口") {
			t.Fatalf("放弃原因必须包含放宽窗口: %v", lostErr)
		}
		if !errors.Is(lostErr, errRenewJitter) {
			t.Fatalf("放弃错误必须保留原始续租错误: %v", lostErr)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("超过放宽窗口后未放弃所有权")
	}
}

// TestRenewGraceKeeperGivesUpImmediatelyOnZeroRows 覆盖 c)：0 行更新说明租约
// 已过期或被接管，必须立即按既有语义放弃（在放宽窗口到期之前）。
func TestRenewGraceKeeperGivesUpImmediatelyOnZeroRows(t *testing.T) {
	store := &renewGraceFakeStore{renewFunc: func(int) (bool, error) {
		return false, nil
	}}
	keeper := startRenewGraceKeeper(t, store)
	defer keeper.Close()

	// 放宽窗口为 6s；若在 3s 内 Lost 关闭即为“0 行立即放弃”路径。
	select {
	case <-keeper.Lost():
		if !errors.Is(keeper.LostError(), ErrOwnerLeaseLost) {
			t.Fatalf("0 行更新必须以 ErrOwnerLeaseLost 终态: %v", keeper.LostError())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("0 行更新后未立即放弃所有权")
	}
}

// TestRunOwnerRestartReacquiresFreshLease 覆盖 d)：模拟 supervisor 重启——
// 第一次 RunOwner 因租约丢失返回错误后，重新 RunOwner 必须执行全新
// AcquireOwnerLease（新 fence token）并恢复运行，而不是从旧 keeper 的存储
// 错误直接返回（2026-09-28 生产缺陷：组件每 30s 重启一次、每次立即回放同一
// 存储错误，gateway 约 1.5 小时无 F4 owner）。
func TestRunOwnerRestartReacquiresFreshLease(t *testing.T) {
	store := &renewGraceFakeStore{acquireOK: true, renewFunc: func(int) (bool, error) {
		return false, nil // 0 行更新 → 快速终态，缩短用例时长
	}}
	keeper := startRenewGraceKeeper(t, store)
	defer keeper.Close()
	cfg := renewGraceConfig()

	// 第一轮 RunOwner：进入时 keeper 健康（reacquire no-op），随后 0 行更新
	// 终态，组件按既有语义返回租约丢失错误。
	done1 := make(chan error, 1)
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	go func() { done1 <- RunOwner(ctx1, store, keeper, cfg, slog.Default()) }()
	select {
	case err := <-done1:
		if !errors.Is(err, ErrOwnerLeaseLost) {
			t.Fatalf("租约丢失必须作为组件错误返回: %v", err)
		}
	case <-time.After(10 * time.Second):
		cancel1()
		t.Fatal("第一轮 RunOwner 未在租约丢失后退出")
	}

	// 第二轮 RunOwner（supervisor 重启）：入口必须重新 AcquireOwnerLease 并
	// 持新 fence 恢复运行；随后取消上下文应优雅 nil 返回。
	store.mu.Lock()
	firstAcquires := store.acquireCalls
	store.mu.Unlock()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	done2 := make(chan error, 1)
	go func() { done2 <- RunOwner(ctx2, store, keeper, cfg, slog.Default()) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		total := store.acquireCalls
		store.mu.Unlock()
		if total > firstAcquires {
			break
		}
		select {
		case err := <-done2:
			t.Fatalf("重启后 RunOwner 不得直接返回旧错误: %v", err)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	store.mu.Lock()
	total := store.acquireCalls
	tokens := append([]int64(nil), store.fenceTokens...)
	store.mu.Unlock()
	if total != firstAcquires+1 {
		cancel2()
		t.Fatalf("重启后必须重新执行 AcquireOwnerLease: first=%d total=%d tokens=%v", firstAcquires, total, tokens)
	}
	cancel2()
	select {
	case err := <-done2:
		if err != nil {
			t.Fatalf("取消后应 nil 返回: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("第二轮 RunOwner 未随取消退出")
	}
	if got := keeper.Lease().FenceToken; got != int64(total) {
		t.Fatalf("keeper 必须持有重取后的 fence token: got=%d want=%d", got, total)
	}
	if err := keeper.LostError(); err != nil {
		t.Fatalf("重取后终态必须清空: %v", err)
	}
}
