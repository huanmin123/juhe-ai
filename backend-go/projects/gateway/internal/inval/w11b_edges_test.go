package inval

// w11b 波次：SyncFromShared 无共享存储/共享更高版本分支与 Redis key 命名空间
// 归一化、GetVersion 空键契约。

import (
	"context"
	"testing"

	"github.com/redis/go-redis/v9"
	miniredis "github.com/alicebob/miniredis/v2"
)

func TestW11BSyncFromSharedArms(t *testing.T) {
	// 无共享存储：no-op。
	bus := New(nil)
	if err := bus.SyncFromShared(context.Background(), "topic-a"); err != nil {
		t.Fatalf("err = %v", err)
	}
	// 共享版本更高时覆盖本地；本地更高时保留。
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	store := NewRedisSharedStore(client, "juhe-ai:dev")
	shared := New(nil)
	shared.SetSharedStore(store)
	if _, err := store.PublishVersion(context.Background(), "w11b-topic", 7); err != nil {
		t.Fatal(err)
	}
	if err := shared.SyncFromShared(context.Background(), "w11b-topic"); err != nil {
		t.Fatalf("err = %v", err)
	}
	if shared.Version("w11b-topic") != 7 {
		t.Fatalf("version = %d", shared.Version("w11b-topic"))
	}
	// 本地版本更高时共享不回退本地：先共享 12，再让本地失效 13 次。
	if _, err := store.PublishVersion(context.Background(), "w11b-topic", 12); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 13; i++ {
		shared.Invalidate("w11b-topic", "w11b-reason")
	}
	if err := shared.SyncFromShared(context.Background(), "w11b-topic"); err != nil {
		t.Fatalf("err = %v", err)
	}
	if version := shared.Version("w11b-topic"); version <= 12 {
		t.Fatalf("本地失效后版本必须高于共享 12，got %d", version)
	}
	// 空 namespace 的 key 形态。
	bare := NewRedisSharedStore(client, "")
	if bare.key("t") != "juhe-ai:inval:topic-version:t" {
		t.Fatalf("bare key = %q", bare.key("t"))
	}
	namespaced := NewRedisSharedStore(client, "  juhe-ai:dev:  ")
	if namespaced.key("t") != "juhe-ai:dev:inval:topic-version:t" {
		t.Fatalf("namespaced key = %q", namespaced.key("t"))
	}
	prefixed := NewRedisSharedStore(client, "prod")
	if prefixed.key("t") != "juhe-ai:prod:inval:topic-version:t" {
		t.Fatalf("prefixed key = %q", prefixed.key("t"))
	}
	// GetVersion 空键 → 0。
	if version, err := bare.GetVersion(context.Background(), "missing-w11b"); err != nil || version != 0 {
		t.Fatalf("missing = %d err = %v", version, err)
	}
}
