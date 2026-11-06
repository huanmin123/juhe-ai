package crossprocessstate

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeExecutor 模拟本包用到的 SET NX / EVAL / MGET 语义。
type fakeExecutor struct {
	mu     sync.Mutex
	values map[string]string
	ttls   map[string]time.Duration

	setNXCalls int
	mgetCalls  int
	mgetKeys   []string
	lastEval   string
	lastArgs   []any

	setNXErr error
	evalErr  error
	mgetErr  error
}

func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{values: map[string]string{}, ttls: map[string]time.Duration{}}
}

func (f *fakeExecutor) SetNX(_ context.Context, key, value string, ttl time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setNXCalls++
	if f.setNXErr != nil {
		return false, f.setNXErr
	}
	if _, exists := f.values[key]; exists {
		return false, nil
	}
	f.values[key] = value
	f.ttls[key] = ttl
	return true, nil
}

func (f *fakeExecutor) Eval(_ context.Context, script string, keys []string, args ...any) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastEval = script
	f.lastArgs = args
	if f.evalErr != nil {
		return nil, f.evalErr
	}
	switch {
	case strings.Contains(script, "INCR"):
		key := keys[0]
		current, _ := strconv.ParseInt(f.values[key], 10, 64)
		current++
		f.values[key] = strconv.FormatInt(current, 10)
		f.ttls[key] = time.Duration(args[0].(int64)) * time.Millisecond
		return current, nil
	case strings.Contains(script, "DEL"):
		key := keys[0]
		if len(args) > 0 && f.values[key] == args[0].(string) {
			delete(f.values, key)
			return int64(1), nil
		}
		return int64(0), nil
	default:
		return nil, errors.New("unknown script")
	}
}

func (f *fakeExecutor) MGet(_ context.Context, keys ...string) ([]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mgetCalls++
	f.mgetKeys = append([]string(nil), keys...)
	if f.mgetErr != nil {
		return nil, f.mgetErr
	}
	values := make([]any, len(keys))
	for index, key := range keys {
		if value, exists := f.values[key]; exists {
			values[index] = value
		}
	}
	return values, nil
}

func newTestClient(t *testing.T) (*Client, *fakeExecutor) {
	t.Helper()
	exec := newFakeExecutor()
	client, err := New(exec, "prod:oauth")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client, exec
}

func TestKeyShapeIsClusterTaggedAndStable(t *testing.T) {
	client, _ := newTestClient(t)
	if got, want := client.LockKey(EntityOAuthRefreshLock, "acc-1"), "juhe-ai:{prod:oauth}:lock:oauth-refresh:acc-1"; got != want {
		t.Fatalf("LockKey = %q, want %q", got, want)
	}
	if got, want := client.VersionKey(EntityAccountCredentialsVersion, "acc-1"), "juhe-ai:{prod:oauth}:version:account-credentials:acc-1"; got != want {
		t.Fatalf("VersionKey = %q, want %q", got, want)
	}
	// 同一 namespace 下的所有键共享同一 hash tag（Cluster 同 slot）。
	if !strings.Contains(client.Key(EntityOAuthRefreshLock, "acc-1"), "{prod:oauth}") {
		t.Fatalf("key 缺少 hash tag: %q", client.Key(EntityOAuthRefreshLock, "acc-1"))
	}
}

func TestKeyPanicsOnEmptyParts(t *testing.T) {
	client, _ := newTestClient(t)
	for _, parts := range [][2]string{{"", "id"}, {"entity", ""}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("Key(%q,%q) 未 panic", parts[0], parts[1])
				}
			}()
			client.Key(parts[0], parts[1])
		}()
	}
}

func TestTryLockAcquireBusyAndRelease(t *testing.T) {
	client, exec := newTestClient(t)
	ctx := context.Background()

	token, acquired, err := client.TryLock(ctx, EntityOAuthRefreshLock, "acc-1", time.Minute)
	if err != nil || !acquired || token == "" {
		t.Fatalf("首次 TryLock = (%q,%v,%v), want acquired with token", token, acquired, err)
	}
	if ttl := exec.ttls[client.LockKey(EntityOAuthRefreshLock, "acc-1")]; ttl != time.Minute {
		t.Fatalf("锁 TTL = %v, want 1m", ttl)
	}

	if second, acquired, err := client.TryLock(ctx, EntityOAuthRefreshLock, "acc-1", time.Minute); err != nil || acquired || second != "" {
		t.Fatalf("第二次 TryLock = (%q,%v,%v), want busy", second, acquired, err)
	}

	if err := client.Unlock(ctx, EntityOAuthRefreshLock, "acc-1", token); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if _, acquired, err := client.TryLock(ctx, EntityOAuthRefreshLock, "acc-1", time.Minute); err != nil || !acquired {
		t.Fatalf("释放后 TryLock = acquired %v err %v, want true", acquired, err)
	}
}

func TestUnlockWithWrongTokenKeepsLock(t *testing.T) {
	client, _ := newTestClient(t)
	ctx := context.Background()
	token, acquired, err := client.TryLock(ctx, EntityOAuthRefreshLock, "acc-2", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("TryLock: %v/%v", acquired, err)
	}
	if err := client.Unlock(ctx, EntityOAuthRefreshLock, "acc-2", token+"-stale"); err != nil {
		t.Fatalf("Unlock wrong token: %v", err)
	}
	if _, acquired, err := client.TryLock(ctx, EntityOAuthRefreshLock, "acc-2", time.Minute); err != nil || acquired {
		t.Fatalf("错误 token 释放后仍应占用, acquired=%v err=%v", acquired, err)
	}
}

func TestUnlockEmptyTokenIsNoop(t *testing.T) {
	client, exec := newTestClient(t)
	if err := client.Unlock(context.Background(), EntityOAuthRefreshLock, "acc-3", "  "); err != nil {
		t.Fatalf("Unlock empty: %v", err)
	}
	if exec.lastEval != "" {
		t.Fatalf("空 token 不应触发 EVAL")
	}
}

func TestBumpVersionMonotonic(t *testing.T) {
	client, _ := newTestClient(t)
	ctx := context.Background()
	for index := int64(1); index <= 3; index++ {
		value, err := client.BumpVersion(ctx, EntityAccountCredentialsVersion, "acc-4")
		if err != nil {
			t.Fatalf("BumpVersion: %v", err)
		}
		if value != index {
			t.Fatalf("BumpVersion = %d, want %d", value, index)
		}
	}
}

func TestVersionsReadsOneBatchAndDefaultsMissingToZero(t *testing.T) {
	client, exec := newTestClient(t)
	ctx := context.Background()
	if _, err := client.BumpVersion(ctx, EntityAccountCredentialsVersion, "acc-a"); err != nil {
		t.Fatalf("BumpVersion: %v", err)
	}
	if _, err := client.BumpVersion(ctx, EntityAccountCredentialsVersion, "acc-b"); err != nil {
		t.Fatalf("BumpVersion: %v", err)
	}
	// 非法值按“无版本”处理。
	exec.values[client.Key(EntityAccountCredentialsVersion, "acc-bad")] = "not-a-number"

	versions, err := client.Versions(ctx, EntityAccountCredentialsVersion, []string{"acc-a", "acc-b", "acc-bad", "missing", "acc-a", "  "})
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if exec.mgetCalls != 1 {
		t.Fatalf("MGET 调用次数 = %d, want 1", exec.mgetCalls)
	}
	if len(exec.mgetKeys) != 4 {
		t.Fatalf("MGET 键数 = %d, want 4（去重后）: %v", len(exec.mgetKeys), exec.mgetKeys)
	}
	want := map[string]int64{"acc-a": 1, "acc-b": 1, "acc-bad": 0, "missing": 0}
	for id, expected := range want {
		if versions[id] != expected {
			t.Fatalf("Versions[%q] = %d, want %d", id, versions[id], expected)
		}
	}
}

func TestVersionsEmptyInputSkipsRedis(t *testing.T) {
	client, exec := newTestClient(t)
	versions, err := client.Versions(context.Background(), EntityAccountCredentialsVersion, nil)
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(versions) != 0 || exec.mgetCalls != 0 {
		t.Fatalf("空输入不应触发 MGET: versions=%v calls=%d", versions, exec.mgetCalls)
	}
}

func TestErrorsSurfaceForCallerFallback(t *testing.T) {
	client, exec := newTestClient(t)
	ctx := context.Background()
	exec.setNXErr = errors.New("redis down")
	if _, _, err := client.TryLock(ctx, EntityOAuthRefreshLock, "acc-5", time.Minute); err == nil {
		t.Fatalf("TryLock 应返回 Redis 错误")
	}
	exec.evalErr = errors.New("redis down")
	if _, err := client.BumpVersion(ctx, EntityAccountCredentialsVersion, "acc-5"); err == nil {
		t.Fatalf("BumpVersion 应返回 Redis 错误")
	}
	exec.mgetErr = errors.New("redis down")
	if _, err := client.Versions(ctx, EntityAccountCredentialsVersion, []string{"acc-5"}); err == nil {
		t.Fatalf("Versions 应返回 Redis 错误")
	}
}

func TestReadContextBudget(t *testing.T) {
	ctx, cancel := ReadContext(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatalf("ReadContext 未设置 deadline")
	}
	remaining := time.Until(deadline)
	if remaining > DefaultReadTimeout || remaining <= 0 {
		t.Fatalf("ReadContext 预算 = %v, want (0, %v]", remaining, DefaultReadTimeout)
	}
}

func TestDetachedContextIgnoresParentCancel(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent()
	detached, cancelDetached := DetachedContext(parent, time.Second)
	defer cancelDetached()
	if err := detached.Err(); err != nil {
		t.Fatalf("DetachedContext 不应继承父取消: %v", err)
	}
	deadline, ok := detached.Deadline()
	if !ok {
		t.Fatalf("DetachedContext 未设置 deadline")
	}
	if time.Until(deadline) > time.Second {
		t.Fatalf("DetachedContext 超时预算超过指定值")
	}
}

func TestNewRejectsNilDependencies(t *testing.T) {
	if _, err := New(nil, "prod:oauth"); err == nil {
		t.Fatalf("New(nil) 应返回错误")
	}
	if _, err := NewRedis(nil, "prod:oauth"); err == nil {
		t.Fatalf("NewRedis(nil) 应返回错误")
	}
}
