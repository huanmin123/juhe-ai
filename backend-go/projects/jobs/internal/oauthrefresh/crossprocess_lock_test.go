package oauthrefresh

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeRefreshLock 记录 TryLock/Unlock 调用；busy 控制 TryLock 结果序列。
type fakeRefreshLock struct {
	mu        sync.Mutex
	acquired  bool
	err       error
	busyCount int
	calls     int
	unlocks   int
	lastToken string
	unlockErr error
}

func (f *fakeRefreshLock) TryLock(_ context.Context, _ string, _ time.Duration) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return "", false, f.err
	}
	if f.busyCount > 0 {
		f.busyCount--
		return "", false, nil
	}
	f.acquired = true
	f.lastToken = "token-1"
	return f.lastToken, true, nil
}

func (f *fakeRefreshLock) Unlock(ctx context.Context, _ string, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unlocks++
	if f.lastToken != token {
		f.unlockErr = errors.New("token mismatch")
	}
	if err := ctx.Err(); err != nil {
		f.unlockErr = err
	}
	return nil
}

func TestAccountLocksTryLockContextRemoteBusySkipsTask(t *testing.T) {
	locks := newAccountLocks()
	remote := &fakeRefreshLock{busyCount: 1}
	locks.remote = remote
	ran := false
	err := locks.TryLockContext(context.Background(), "acc-busy", func() error {
		ran = true
		return nil
	})
	var busy *LockBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("err = %v, want LockBusyError", err)
	}
	if ran {
		t.Fatalf("远端锁被占用时不应执行任务")
	}
}

func TestAccountLocksTryLockContextRemoteErrorFallsBack(t *testing.T) {
	locks := newAccountLocks()
	locks.remote = &fakeRefreshLock{err: errors.New("redis down")}
	ran := false
	err := locks.TryLockContext(context.Background(), "acc-fallback", func() error {
		ran = true
		return nil
	})
	if err != nil {
		t.Fatalf("远端锁错误应按回退处理, err=%v", err)
	}
	if !ran {
		t.Fatalf("远端锁错误时任务仍应执行（PG CAS 兜底）")
	}
}

func TestAccountLocksTryLockContextReleasesRemoteWithToken(t *testing.T) {
	locks := newAccountLocks()
	remote := &fakeRefreshLock{}
	locks.remote = remote
	if err := locks.TryLockContext(context.Background(), "acc-release", func() error { return nil }); err != nil {
		t.Fatalf("TryLockContext: %v", err)
	}
	if remote.unlocks != 1 || remote.unlockErr != nil {
		t.Fatalf("unlock 次数=%d err=%v, want 1 次且 token 匹配", remote.unlocks, remote.unlockErr)
	}
}

func TestAccountLocksTryLockLocalBusyDoesNotTouchRemote(t *testing.T) {
	locks := newAccountLocks()
	remote := &fakeRefreshLock{}
	locks.remote = remote
	// 进程内锁已占用（同进程并发）：不得先抢远端锁，否则会白占 60s。
	locks.mutexFor("acc-local").Lock()
	defer locks.mutexFor("acc-local").Unlock()
	err := locks.TryLockContext(context.Background(), "acc-local", func() error { return nil })
	var busy *LockBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("err = %v, want LockBusyError", err)
	}
	if remote.calls != 0 {
		t.Fatalf("本地锁占用时远端 TryLock 调用次数 = %d, want 0", remote.calls)
	}
}

func TestAccountLocksLockContextWaitsForRemote(t *testing.T) {
	locks := newAccountLocks()
	remote := &fakeRefreshLock{busyCount: 3}
	locks.remote = remote
	ran := false
	if err := locks.LockContext(context.Background(), "acc-wait", func() error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("LockContext: %v", err)
	}
	if !ran || remote.calls != 4 {
		t.Fatalf("ran=%v calls=%d, want 执行且第 4 次拿到锁", ran, remote.calls)
	}
}

func TestAccountLocksLockContextCanceledDuringWait(t *testing.T) {
	locks := newAccountLocks()
	locks.remote = &fakeRefreshLock{busyCount: 1_000_000}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	ran := false
	err := locks.LockContext(ctx, "acc-cancel", func() error {
		ran = true
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if ran {
		t.Fatalf("等待中被取消不应执行任务")
	}
}

func TestAccountLocksReleaseUsesDetachedContext(t *testing.T) {
	locks := newAccountLocks()
	remote := &fakeRefreshLock{}
	locks.remote = remote
	ctx, cancel := context.WithCancel(context.Background())
	err := locks.TryLockContext(ctx, "acc-detached", func() error {
		cancel()
		return nil
	})
	if err != nil {
		t.Fatalf("TryLockContext: %v", err)
	}
	if remote.unlocks != 1 || remote.unlockErr != nil {
		t.Fatalf("取消后释放应使用 detached ctx: unlocks=%d err=%v", remote.unlocks, remote.unlockErr)
	}
}

// fakeBumper 记录凭据版本写入。
type fakeBumper struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (f *fakeBumper) Bump(_ context.Context, accountID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, accountID)
	return f.err
}

func TestStoreBumpsCredentialVersionAfterRotationCommit(t *testing.T) {
	store, db, _ := newTestStore(t)
	now := time.Now().UTC()
	seedOpenAIOAuthAccount(t, db, "acc-version-rotate", openAICredentials(expiresInMillis(3_600_000)), now)
	account, err := store.FindRotationAccount(context.Background(), "acc-version-rotate")
	if err != nil || account == nil {
		t.Fatalf("find=%v err=%v", account, err)
	}
	bumper := &fakeBumper{}
	store.SetCredentialVersionBumper(bumper)
	credentials := map[string]any{}
	for key, value := range account.Credentials {
		credentials[key] = value
	}
	credentials["access_token"] = "at-rotated"
	result, err := store.RotateCredentials(context.Background(), RotateCredentialsInput{
		AccountID: "acc-version-rotate", ExpectedConfigRevision: account.ConfigRevision,
		ExpectedProviderCode: ProviderGPT, ExpectedAccountType: AccountTypeOAuth,
		Credentials: credentials,
	})
	if err != nil || result == nil || !result.Changed {
		t.Fatalf("RotateCredentials result=%+v err=%v", result, err)
	}
	if len(bumper.calls) != 1 || bumper.calls[0] != "acc-version-rotate" {
		t.Fatalf("bump calls = %v, want [acc-version-rotate]", bumper.calls)
	}
}

func TestStoreBumpsCredentialVersionAfterMergeCommit(t *testing.T) {
	store, db, _ := newTestStore(t)
	now := time.Now().UTC()
	seedOpenAIOAuthAccount(t, db, "acc-version-merge", openAICredentials(expiresInMillis(3_600_000)), now)
	account, err := store.FindRotationAccount(context.Background(), "acc-version-merge")
	if err != nil || account == nil {
		t.Fatalf("find=%v err=%v", account, err)
	}
	bumper := &fakeBumper{}
	store.SetCredentialVersionBumper(bumper)
	credentials := map[string]any{}
	for key, value := range account.Credentials {
		credentials[key] = value
	}
	credentials["access_token"] = "at-merged"
	if _, err := store.UpdateAccountCredentials(context.Background(), "acc-version-merge", credentials, account.ConfigRevision); err != nil {
		t.Fatalf("UpdateAccountCredentials: %v", err)
	}
	if len(bumper.calls) != 1 || bumper.calls[0] != "acc-version-merge" {
		t.Fatalf("bump calls = %v, want [acc-version-merge]", bumper.calls)
	}
}

func TestStoreBumpFailureDoesNotBreakCredentialWrite(t *testing.T) {
	store, db, _ := newTestStore(t)
	now := time.Now().UTC()
	seedOpenAIOAuthAccount(t, db, "acc-version-bumpfail", openAICredentials(expiresInMillis(3_600_000)), now)
	account, err := store.FindRotationAccount(context.Background(), "acc-version-bumpfail")
	if err != nil || account == nil {
		t.Fatalf("find=%v err=%v", account, err)
	}
	store.SetCredentialVersionBumper(&fakeBumper{err: errors.New("redis down")})
	credentials := map[string]any{}
	for key, value := range account.Credentials {
		credentials[key] = value
	}
	credentials["access_token"] = "at-after-bump-failure"
	result, err := store.RotateCredentials(context.Background(), RotateCredentialsInput{
		AccountID: "acc-version-bumpfail", ExpectedConfigRevision: account.ConfigRevision,
		ExpectedProviderCode: ProviderGPT, ExpectedAccountType: AccountTypeOAuth,
		Credentials: credentials,
	})
	if err != nil || result == nil || !result.Changed {
		t.Fatalf("bump 失败不得回滚已提交的业务写: result=%+v err=%v", result, err)
	}
}
