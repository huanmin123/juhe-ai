package gatewayaccounteffects

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

// TestRedisRecordFailureCapacityPathExecutesLua 覆盖状态容量打满分支的真实
// Lua 执行：脚本访问 closed 索引必须使用调用方传入的 KEYS 数量范围内的下
// 标，否则 Redis 返回 "KEYS[9] out of bounds" 而不是 capacity_exhausted。
func TestRedisRecordFailureCapacityPathExecutesLua(t *testing.T) {
	server := miniredis.RunT(t)
	store, err := NewRedisKeyModelRuntimeStore(KeyModelRedisStoreOptions{
		RedisURL:  "redis://" + server.Addr(),
		Namespace: "capacity-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// 容量键直接抬到上限，让新 capability 进入清理/拒绝分支。
	if err := client.Set(ctx, store.Keys().Capacity(), "50000", 0).Err(); err != nil {
		t.Fatal(err)
	}
	result, err := store.RecordFailure(ctx, memoryIntent(testCapability(), "intent-cap", time.Now().UnixMilli()))
	if err != nil {
		t.Fatalf("capacity path Lua error: %v", err)
	}
	if result.Status != "capacity_exhausted" {
		t.Fatalf("status = %q, want capacity_exhausted", result.Status)
	}
}
