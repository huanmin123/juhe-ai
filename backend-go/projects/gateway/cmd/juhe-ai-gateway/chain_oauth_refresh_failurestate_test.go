package main

// S13：gateway 401 自动刷新在换发 token 前读取 jobs 共享失败状态。

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-platform/oauthfailure"
)

type fakeFailureStateReader struct {
	until        int64
	active       bool
	err          error
	calls        atomic.Int64
	lastVendor   string
	lastNow      int64
	lastRevision int64
}

func (f *fakeFailureStateReader) ActiveBackoff(_ context.Context, _, vendor string, nowMillis, configRevision int64) (int64, bool, error) {
	f.calls.Add(1)
	f.lastVendor = vendor
	f.lastNow = nowMillis
	f.lastRevision = configRevision
	return f.until, f.active, f.err
}

func TestChainOAuthRefreshSkipsWhenSharedBackoffActive(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC).UnixMilli())
	store := &fakeRefreshStore{result: refreshSuccessResult()}
	cache := &fakeRefreshCache{token: "new-access-token"}
	reader := &fakeFailureStateReader{until: now.Load() + int64(5*time.Minute/time.Millisecond), active: true}
	coordinator := newRefreshCoordinator(store, cache, &now)
	coordinator.failureState = reader

	input := oauth401Input(t, oauthAccount(map[string]any{"access_token": "old"}))
	revision := int64(7)
	input.Account.ConfigRevision = &revision
	if hydrated := coordinator.refreshForRetry(withChainOAuthRefreshOnceState(context.Background()), input); hydrated != nil {
		t.Fatal("共享退避有效时不得刷新")
	}
	if store.calls.Load() != 0 || cache.calls.Load() != 0 {
		t.Fatalf("store/cache calls = %d/%d, want 0/0", store.calls.Load(), cache.calls.Load())
	}
	if reader.lastRevision != revision {
		t.Fatalf("revision=%d, want %d", reader.lastRevision, revision)
	}
	if reader.lastNow != now.Load() {
		t.Fatalf("now=%d, want %d", reader.lastNow, now.Load())
	}
	if reader.lastVendor != "gpt" {
		t.Fatalf("vendor=%q, want gpt", reader.lastVendor)
	}
}

func TestChainOAuthRefreshProceedsWhenSharedBackoffInactive(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC).UnixMilli())
	store := &fakeRefreshStore{result: refreshSuccessResult()}
	cache := &fakeRefreshCache{token: "new-access-token"}
	coordinator := newRefreshCoordinator(store, cache, &now)
	coordinator.failureState = &fakeFailureStateReader{}

	input := oauth401Input(t, oauthAccount(map[string]any{"access_token": "old"}))
	if hydrated := coordinator.refreshForRetry(withChainOAuthRefreshOnceState(context.Background()), input); hydrated == nil {
		t.Fatal("共享退避失效后必须继续刷新")
	}
	if store.calls.Load() != 1 {
		t.Fatalf("store calls=%d, want 1", store.calls.Load())
	}
}

func TestChainOAuthRefreshProceedsOnSharedReadError(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC).UnixMilli())
	store := &fakeRefreshStore{result: refreshSuccessResult()}
	cache := &fakeRefreshCache{token: "new-access-token"}
	coordinator := newRefreshCoordinator(store, cache, &now)
	coordinator.failureState = &fakeFailureStateReader{err: errors.New("redis down")}

	input := oauth401Input(t, oauthAccount(map[string]any{"access_token": "old"}))
	if hydrated := coordinator.refreshForRetry(withChainOAuthRefreshOnceState(context.Background()), input); hydrated == nil {
		t.Fatal("共享状态读取失败不得阻断自动刷新")
	}
	if store.calls.Load() != 1 {
		t.Fatalf("store calls=%d, want 1", store.calls.Load())
	}
}

// TestGatewayOAuthRefreshFailureStateReadsJobsKey 钉死 gateway 读取键与 jobs
// 写入键同源（shared/platform/oauthfailure），并用 miniredis 验证 GET + JSON
// 解码 + revision 守卫。
func TestGatewayOAuthRefreshFailureStateReadsJobsKey(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	reader := newGatewayOAuthRefreshFailureState(client, "dev")
	if reader == nil {
		t.Fatal("reader 不得为 nil")
	}
	const accountID = "acc-shared-state"
	if err := server.Set(oauthfailure.KeyForVendor("gpt", "dev:oauth", accountID),
		`{"count":2,"backoffUntil":5000,"configRevision":3}`); err != nil {
		t.Fatal(err)
	}
	until, active, err := reader.ActiveBackoff(context.Background(), accountID, "gpt", 1000, 3)
	if err != nil || !active || until != 5000 {
		t.Fatalf("ActiveBackoff = (%d,%v,%v), want (5000,true,nil)", until, active, err)
	}
	if _, active, err := reader.ActiveBackoff(context.Background(), accountID, "gpt", 1000, 4); err != nil || active {
		t.Fatalf("revision 不匹配时必须失效: active=%v err=%v", active, err)
	}
	if _, active, err := reader.ActiveBackoff(context.Background(), "acc-missing", "gpt", 1000, 3); err != nil || active {
		t.Fatalf("缺失键必须返回 inactive: active=%v err=%v", active, err)
	}
	// keepalive 族（anthropic/gemini/xai）使用独立前缀，必须按 vendor 读对族。
	const keepaliveID = "acc-keepalive-state"
	if err := server.Set(oauthfailure.KeyForVendor("anthropic", "dev:oauth", keepaliveID),
		`{"backoffUntil":6000,"configRevision":2}`); err != nil {
		t.Fatal(err)
	}
	until, active, err = reader.ActiveBackoff(context.Background(), keepaliveID, "anthropic", 1000, 2)
	if err != nil || !active || until != 6000 {
		t.Fatalf("keepalive ActiveBackoff = (%d,%v,%v), want (6000,true,nil)", until, active, err)
	}
}
