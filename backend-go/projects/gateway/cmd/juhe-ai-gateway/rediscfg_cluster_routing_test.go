package main

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
)

// TestClusterClientRoutingAndScanAll 用 miniredis + 人造 slots 覆盖 Cluster
// 客户端路径：同域多键 MGet/Eval 不跨槽，rediscfg.ScanAll 逐 master 扫描。
func TestClusterClientRoutingAndScanAll(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{server.Addr()},
		ClusterSlots: func(context.Context) ([]redis.ClusterSlot, error) {
			return []redis.ClusterSlot{{
				Start: 0, End: 16383,
				Nodes: []redis.ClusterNode{{Addr: server.Addr()}},
			}}, nil
		},
	})
	defer client.Close()

	ctx := context.Background()
	namespace := rediscfg.DomainNamespace("cluster-routing", rediscfg.DomainCircuit)
	keys := []string{
		rediscfg.TaggedNamespacedKey("states", namespace),
		rediscfg.TaggedNamespacedKey("due", namespace),
	}
	for i, key := range keys {
		if err := client.Set(ctx, key, i, time.Minute).Err(); err != nil {
			t.Fatalf("SET %s: %v", key, err)
		}
	}
	values, err := client.MGet(ctx, keys...).Result()
	if err != nil || len(values) != 2 {
		t.Fatalf("MGET: %v/%d", err, len(values))
	}
	if err := client.Eval(ctx, "return redis.call('SET', KEYS[1], 'lua')", keys[:1]).Err(); err != nil {
		t.Fatalf("EVAL: %v", err)
	}
	var scanned int
	if err := rediscfg.ScanAll(ctx, client, rediscfg.TaggedPrefix(namespace)+"*", func(batch []string) error {
		scanned += len(batch)
		return nil
	}); err != nil {
		t.Fatalf("ScanAll cluster: %v", err)
	}
	if scanned < len(keys) {
		t.Fatalf("ScanAll scanned %d, want >= %d", scanned, len(keys))
	}
}
