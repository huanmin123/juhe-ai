package oauthrefresh

// P0 修复回归：Redis 失败状态 Read 错误不得吞成“无状态”（原实现把 Redis
// 故障读成 (nil, nil)，全部账号退避读空、无退避猛打上游）；refresh 任务对
// 读失败的账户保守跳过——本轮不刷新、不打上游、其余账户与循环不受影响。

import (
	"context"
	"errors"
	"testing"
)

// p0FailingReadStore 包一层内存版，按 accountID 注入 Read 错误（可回放、
// 结果稳定），用于驱动 selectBatchCandidates 的保守跳过分支。
type p0FailingReadStore struct {
	memory *MemoryFailureStateStore
	errFor map[string]error
}

func (s *p0FailingReadStore) Record(ctx context.Context, accountID string, backoffUntil int64, kind FailureKind, configRevision int64) (RefreshFailureState, error) {
	return s.memory.Record(ctx, accountID, backoffUntil, kind, configRevision)
}

func (s *p0FailingReadStore) Read(ctx context.Context, accountID string, now int64, configRevision int64) (*RefreshFailureState, error) {
	if err := s.errFor[accountID]; err != nil {
		return nil, err
	}
	return s.memory.Read(ctx, accountID, now, configRevision)
}

func (s *p0FailingReadStore) Clear(ctx context.Context, accountID string, guard RefreshFailureState) error {
	return s.memory.Clear(ctx, accountID, guard)
}

func (s *p0FailingReadStore) CleanupBackoff(now int64) { s.memory.CleanupBackoff(now) }

func TestP0RedisFailureStateReadErrorPropagates(t *testing.T) {
	readErr := errors.New("redis down")
	store := NewRedisFailureStateStore(&w9hScripter{getFunc: func(context.Context, string) (string, error) {
		return "", readErr
	}})
	state, err := store.Read(context.Background(), "acc", 1, 1)
	if !errors.Is(err, readErr) {
		t.Fatalf("read err=%v, want propagated redis error", err)
	}
	if state != nil {
		t.Fatalf("state=%+v", state)
	}
}

func TestP0RefreshConservativeSkipWhenFailureStateReadFails(t *testing.T) {
	job, _, db, clock, exchanger := newRefreshJobForTest(t)
	seedOpenAIOAuthAccount(t, db, "acc-broken-read", openAICredentials(expiresInMillis(0)), clock.Now())
	seedOpenAIOAuthAccount(t, db, "acc-ok", openAICredentials(expiresInMillis(0)), clock.Now())
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-new","refresh_token":"rt-new","id_token":"","expires_in":3600}`}, nil
	}
	job.failures = &p0FailingReadStore{
		memory: NewMemoryFailureStateStore(),
		errFor: map[string]error{"acc-broken-read": errors.New("redis down")},
	}
	result, err := job.RunOnce(context.Background(), RefreshOptions{})
	if err != nil {
		t.Fatalf("failure-state read error must not fail the cycle: %v", err)
	}
	// 读失败的账户被保守跳过（不进候选、不计数），可读账户正常刷新。
	if result.Due != 1 || result.Refreshed != 1 || result.Started != 1 {
		t.Fatalf("result=%+v", result)
	}
	if exchanger.callCount() != 1 {
		t.Fatalf("upstream calls=%d, want exactly the readable account", exchanger.callCount())
	}
	credentials := readAccountCredentials(t, db, "acc-ok")
	if credentials["access_token"] != "at-new" {
		t.Fatalf("readable account must refresh, credentials=%v", credentials)
	}
}

func TestP0RefreshConservativeSkipWhenAllReadsFail(t *testing.T) {
	job, _, db, clock, exchanger := newRefreshJobForTest(t)
	seedOpenAIOAuthAccount(t, db, "acc-all-1", openAICredentials(expiresInMillis(0)), clock.Now())
	seedOpenAIOAuthAccount(t, db, "acc-all-2", openAICredentials(expiresInMillis(0)), clock.Now())
	job.failures = &p0FailingReadStore{
		memory: NewMemoryFailureStateStore(),
		errFor: map[string]error{
			"acc-all-1": errors.New("redis down"),
			"acc-all-2": errors.New("redis down"),
		},
	}
	result, err := job.RunOnce(context.Background(), RefreshOptions{})
	if err != nil {
		t.Fatalf("all-read-failure must degrade to an empty round, not an error: %v", err)
	}
	// Redis 故障时宁可不刷新，也不把全部账号当“无退避”打上游。
	if result.Due != 0 || result.Started != 0 || result.Refreshed != 0 {
		t.Fatalf("result=%+v", result)
	}
	if exchanger.callCount() != 0 {
		t.Fatal("upstream must not be called when every failure state read fails")
	}
}
