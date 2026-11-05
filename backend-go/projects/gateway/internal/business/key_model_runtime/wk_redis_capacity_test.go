package keymodelruntime

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

// TestRedisRecordFailureCapacityPathExecutesLua 覆盖容量打满分支的真实 Lua
// 执行：脚本访问 closed 索引必须落在调用方传入的 KEYS 下标范围内，越界会
// 得到 Redis 错误而不是 capacity_exhausted。
func TestRedisRecordFailureCapacityPathExecutesLua(t *testing.T) {
	server := miniredis.RunT(t)
	store, err := NewRedisStore("redis://"+server.Addr(), "capacity", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// RecordFailureIntent 的容量上限参数是 100000（脚本 ARGV[3]）。
	if err := client.Set(ctx, store.key("capacity"), "100000", 0).Err(); err != nil {
		t.Fatal(err)
	}
	status, _, err := store.RecordFailureIntent(ctx, FailureIntent{
		IntentID: "i-cap", RequestID: "r-cap", AttemptID: "a-cap",
		Capability: testCapability(), ObservedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("capacity path Lua error: %v", err)
	}
	if string(status) != "capacity_exhausted" {
		t.Fatalf("status = %q, want capacity_exhausted", status)
	}
}
