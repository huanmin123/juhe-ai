package oauthmgmt

// 跨进程刷新锁与凭据版本投影（计划
// docs/plans/计划-20261106T024618000Z-网关与jobs跨进程共享状态改造.md §B1/§B2）
// 的 gateway 面测试：锁占用跳过、锁后端故障回退、成功释放、手动刷新 409、
// 轮换提交后 bump 版本。
//
// 账户行直接按既有 w14d 夹具播种（不经建号路由），避免依赖模型价格目录数据。

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

type stubRefreshLock struct {
	mu          sync.Mutex
	busy        bool
	err         error
	token       string
	tryCalls    int
	unlockCalls int
	unlocked    []string
	unlockCtxOK bool
}

func (s *stubRefreshLock) TryLock(_ context.Context, _ string, _ time.Duration) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tryCalls++
	if s.err != nil {
		return "", false, s.err
	}
	if s.busy {
		return "", false, nil
	}
	s.token = "lock-token"
	return s.token, true, nil
}

func (s *stubRefreshLock) Unlock(ctx context.Context, accountID, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unlockCalls++
	s.unlocked = append(s.unlocked, accountID)
	s.unlockCtxOK = ctx.Err() == nil && token == s.token
	return nil
}

type stubCredentialBumper struct {
	mu        sync.Mutex
	calls     []string
	revisions []int64
	err       error
}

func (s *stubCredentialBumper) Bump(_ context.Context, accountID string, revision int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, accountID)
	s.revisions = append(s.revisions, revision)
	return s.err
}

func TestTryRefreshLockReleaseUsesDetachedContext(t *testing.T) {
	store := &Store{}
	lock := &stubRefreshLock{}
	store.SetRefreshLock(lock)
	ctx, cancel := context.WithCancel(context.Background())
	release, busy, err := store.tryRefreshLock(ctx, "acc-detached")
	if err != nil || busy || release == nil {
		t.Fatalf("tryRefreshLock = release present %v busy %v err %v", release != nil, busy, err)
	}
	cancel()
	release()
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.unlockCalls != 1 || !lock.unlockCtxOK {
		t.Fatalf("unlock 次数=%d ctxOK=%v, want 1 次且使用未取消的 detached ctx", lock.unlockCalls, lock.unlockCtxOK)
	}
}

func TestTryRefreshLockUnavailableReturnsErrorForFallback(t *testing.T) {
	store := &Store{}
	store.SetRefreshLock(&stubRefreshLock{err: errors.New("redis down")})
	release, busy, err := store.tryRefreshLock(context.Background(), "acc-down")
	if err == nil || busy || release != nil {
		t.Fatalf("锁后端错误应返回 err 供调用方回退: release present %v busy %v err %v", release != nil, busy, err)
	}
}

// TestAutoRefreshLockBusySkipsTokenExchange：锁被 jobs/其他实例占用时，自动
// 刷新不调用 token 端点，返回 RefreshLockBusyError（协调器按本轮跳过处理）。
func TestAutoRefreshLockBusySkipsTokenExchange(t *testing.T) {
	const accountID = "cp-busy"
	env := newTestEnv(t)
	env.w14dSeedRotatableAccount(t, accountID, "gpt", "profile_gpt_openai_v1", "openai", "v1", true)
	env.w14dLoginAdmin(t)
	env.store.SetRefreshLock(&stubRefreshLock{busy: true})
	baseline := len(env.exchanger.recorded())

	_, err := env.store.RotateAccountTokensFromStored(context.Background(), accountID)
	var busy *RefreshLockBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("err = %v, want RefreshLockBusyError", err)
	}
	if got := len(env.exchanger.recorded()) - baseline; got != 0 {
		t.Fatalf("锁占用时 token 端点调用 = %d, want 0", got)
	}
}

// TestAutoRefreshLockUnavailableFallsBack：锁后端故障不阻断刷新（PG CAS 兜底）。
func TestAutoRefreshLockUnavailableFallsBack(t *testing.T) {
	const accountID = "cp-fallback"
	env := newTestEnv(t)
	env.w14dSeedRotatableAccount(t, accountID, "gpt", "profile_gpt_openai_v1", "openai", "v1", true)
	env.w14dLoginAdmin(t)
	env.store.SetRefreshLock(&stubRefreshLock{err: errors.New("redis down")})
	w14dScriptOpenAIAuth(env, http.StatusOK, w14dOpenAITokenBody)

	result, err := env.store.RotateAccountTokensFromStored(context.Background(), accountID)
	if err != nil || result == nil || !result.Rotated {
		t.Fatalf("锁故障应按既有流程刷新: result=%+v err=%v", result, err)
	}
}

// TestAutoRefreshLockReleasedAfterSuccess：成功路径释放锁（token compare-delete）。
func TestAutoRefreshLockReleasedAfterSuccess(t *testing.T) {
	const accountID = "cp-release"
	env := newTestEnv(t)
	env.w14dSeedRotatableAccount(t, accountID, "gpt", "profile_gpt_openai_v1", "openai", "v1", true)
	env.w14dLoginAdmin(t)
	lock := &stubRefreshLock{}
	env.store.SetRefreshLock(lock)
	w14dScriptOpenAIAuth(env, http.StatusOK, w14dOpenAITokenBody)

	if _, err := env.store.RotateAccountTokensFromStored(context.Background(), accountID); err != nil {
		t.Fatalf("auto refresh: %v", err)
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.tryCalls != 1 || lock.unlockCalls != 1 || !lock.unlockCtxOK {
		t.Fatalf("lock try=%d unlock=%d ctxOK=%v, want 各 1 次", lock.tryCalls, lock.unlockCalls, lock.unlockCtxOK)
	}
}

// TestManualRefreshLockBusyReturnsConflict：管理面手动刷新遇到锁占用返回 409，
// 不重复打 token 端点。
func TestManualRefreshLockBusyReturnsConflict(t *testing.T) {
	const accountID = "cp-manual"
	env := newTestEnv(t)
	env.w14dSeedRotatableAccount(t, accountID, "gpt", "profile_gpt_openai_v1", "openai", "v1", true)
	env.w14dLoginAdmin(t)
	env.store.SetRefreshLock(&stubRefreshLock{busy: true})
	baseline := len(env.exchanger.recorded())

	code, body := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/"+accountID+"/refresh-token",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusConflict {
		t.Fatalf("refresh-token: %d %v, want 409", code, body)
	}
	if got := len(env.exchanger.recorded()) - baseline; got != 0 {
		t.Fatalf("锁占用时 token 端点调用 = %d, want 0", got)
	}
}

// TestAutoRefreshBumpsCredentialVersionAfterCommit：轮换提交成功后递增共享
// 凭据版本（gateway 自动刷新路径走 RotateCredentials 统一入口）。
func TestAutoRefreshBumpsCredentialVersionAfterCommit(t *testing.T) {
	const accountID = "cp-bump"
	env := newTestEnv(t)
	env.w14dSeedRotatableAccount(t, accountID, "gpt", "profile_gpt_openai_v1", "openai", "v1", true)
	env.w14dLoginAdmin(t)
	bumper := &stubCredentialBumper{}
	env.store.SetCredentialVersionBumper(bumper)
	w14dScriptOpenAIAuth(env, http.StatusOK, w14dOpenAITokenBody)

	if _, err := env.store.RotateAccountTokensFromStored(context.Background(), accountID); err != nil {
		t.Fatalf("auto refresh: %v", err)
	}
	bumper.mu.Lock()
	defer bumper.mu.Unlock()
	if len(bumper.calls) != 1 || bumper.calls[0] != accountID ||
		len(bumper.revisions) != 1 || bumper.revisions[0] != 2 {
		t.Fatalf("bump calls = %v revisions = %v, want [%s] revision 2", bumper.calls, bumper.revisions, accountID)
	}
}

// TestBumpFailureDoesNotBreakRotation：版本投影故障只告警，不影响已提交的轮换。
func TestBumpFailureDoesNotBreakRotation(t *testing.T) {
	const accountID = "cp-bump-fail"
	env := newTestEnv(t)
	env.w14dSeedRotatableAccount(t, accountID, "gpt", "profile_gpt_openai_v1", "openai", "v1", true)
	env.w14dLoginAdmin(t)
	env.store.SetCredentialVersionBumper(&stubCredentialBumper{err: errors.New("redis down")})
	w14dScriptOpenAIAuth(env, http.StatusOK, w14dOpenAITokenBody)

	result, err := env.store.RotateAccountTokensFromStored(context.Background(), accountID)
	if err != nil || result == nil || !result.Rotated {
		t.Fatalf("版本投影故障不得影响轮换: result=%+v err=%v", result, err)
	}
}
