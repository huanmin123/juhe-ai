package oauthrefresh

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRenewingLock 在 fakeRefreshLock 之上记录 Renew 调用（T207 C11：
// 持锁窗口周期续租）。
type fakeRenewingLock struct {
	fakeRefreshLock
	mu             sync.Mutex
	renewCalls     int
	lastRenewTTL   time.Duration
	lastRenewToken string
	renewOK        bool
	renewErr       error
}

func (f *fakeRenewingLock) Renew(_ context.Context, _ string, token string, ttl time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renewCalls++
	f.lastRenewToken = token
	f.lastRenewTTL = ttl
	if f.renewErr != nil {
		return false, f.renewErr
	}
	return f.renewOK, nil
}

func (f *fakeRenewingLock) renewSnapshot() (int, string, time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.renewCalls, f.lastRenewToken, f.lastRenewTTL
}

func renewingLocks(remote RefreshLock, interval time.Duration, logger *slog.Logger) *accountLocks {
	locks := newAccountLocks()
	locks.remote = remote
	locks.renewInterval = interval
	locks.logger = logger
	return locks
}

// TestAccountLocksRenewsRemoteWithinHeldWindow 锁定 T207 C11 核心行为：持锁
// 窗口内按注入短周期持续续租（TTL 恢复为完整 60s），释放后不再续租，Unlock
// compare-delete 语义不变。
func TestAccountLocksRenewsRemoteWithinHeldWindow(t *testing.T) {
	remote := &fakeRenewingLock{renewOK: true}
	locks := renewingLocks(remote, 10*time.Millisecond, slog.Default())
	ran := false
	if err := locks.TryLockContext(context.Background(), "acc-renew", func() error {
		time.Sleep(60 * time.Millisecond)
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("TryLockContext: %v", err)
	}
	if !ran {
		t.Fatal("任务必须执行")
	}
	calls, renewToken, renewTTL := remote.renewSnapshot()
	if calls < 2 {
		t.Fatalf("持锁 60ms/周期 10ms 续租次数 = %d, want >=2", calls)
	}
	if renewToken != "token-1" {
		t.Fatalf("续租 token = %q, want TryLock 返回值", renewToken)
	}
	if renewTTL != CrossProcessRefreshLockTTL {
		t.Fatalf("续租 TTL = %v, want 完整 %v", renewTTL, CrossProcessRefreshLockTTL)
	}
	// 释放后不再续租：stopRenewal 等待协程退出后 TryLockContext 才返回。
	time.Sleep(40 * time.Millisecond)
	if after, _, _ := remote.renewSnapshot(); after != calls {
		t.Fatalf("释放后续租次数从 %d 变为 %d，未停止续租", calls, after)
	}
	if remote.unlocks != 1 {
		t.Fatalf("unlock 次数 = %d, want 1（compare-delete 语义不变）", remote.unlocks)
	}
}

// TestAccountLocksRenewFailureWarnsAndKeepsTask 锁定失败臂：续租 Redis 错误
// 只 WARN（限流共享）不中断任务；失租（false）停止续租协程但任务照常完成。
func TestAccountLocksRenewFailureWarnsAndKeepsTask(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelWarn}))

	t.Run("续租错误", func(t *testing.T) {
		remote := &fakeRenewingLock{renewErr: errors.New("redis down")}
		locks := renewingLocks(remote, 10*time.Millisecond, logger)
		if err := locks.TryLockContext(context.Background(), "acc-renew-err", func() error {
			time.Sleep(40 * time.Millisecond)
			return nil
		}); err != nil {
			t.Fatalf("续租失败不得中断任务: %v", err)
		}
		if remote.unlocks != 1 {
			t.Fatalf("unlock 次数 = %d, want 1", remote.unlocks)
		}
	})
	t.Run("续租失租", func(t *testing.T) {
		remote := &fakeRenewingLock{renewOK: false}
		locks := renewingLocks(remote, 10*time.Millisecond, logger)
		if err := locks.TryLockContext(context.Background(), "acc-renew-lost", func() error {
			time.Sleep(40 * time.Millisecond)
			return nil
		}); err != nil {
			t.Fatalf("失租不得中断任务: %v", err)
		}
	})
	// 两臂共享一分钟限流：至少一条 renew 告警落到日志。
	if !strings.Contains(buffer.String(), "oauth_refresh_cross_process_lock_renew_error") {
		t.Fatalf("续租失败必须落 WARN 事件: %s", buffer.String())
	}
}

// TestAccountLocksRenewIntervalDefaults 锁定默认续租周期推导（TTL/3）：
// accountLocks 未注入时生产窗口为 20s；注入值优先由测试路径覆盖。
func TestAccountLocksRenewIntervalDefaults(t *testing.T) {
	if got, want := CrossProcessRefreshLockTTL/3, 20*time.Second; got != want {
		t.Fatalf("默认续租周期 = %v, want %v", got, want)
	}
}

// TestAccountLocksNonRenewingRemoteKeepsTTL 锁定向后兼容：未实现 LockRenewer
// 的装配保持既有 TTL 语义（无续租、释放正常）。
func TestAccountLocksNonRenewingRemoteKeepsTTL(t *testing.T) {
	locks := newAccountLocks()
	remote := &fakeRefreshLock{}
	locks.remote = remote
	if err := locks.TryLockContext(context.Background(), "acc-legacy", func() error {
		time.Sleep(30 * time.Millisecond)
		return nil
	}); err != nil {
		t.Fatalf("TryLockContext: %v", err)
	}
	if remote.unlocks != 1 {
		t.Fatalf("unlock 次数 = %d, want 1", remote.unlocks)
	}
}
