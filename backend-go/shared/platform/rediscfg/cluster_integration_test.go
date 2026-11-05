package rediscfg

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestRealClusterRoundTrip 是对真实 Redis Cluster 的显式 opt-in 集成测试。
// 门控变量 JUHE_AI_TEST_REDIS_CLUSTER_ADDRS 用逗号分隔 seed 节点
// （host:port）；授权执行真实依赖的规则见 docs/develop/后端测试分层规则.md。
func TestRealClusterRoundTrip(t *testing.T) {
	raw := strings.TrimSpace(os.Getenv("JUHE_AI_TEST_REDIS_CLUSTER_ADDRS"))
	if raw == "" {
		t.Skip("requires JUHE_AI_TEST_REDIS_CLUSTER_ADDRS=host1:6379,host2:6379,host3:6379")
	}
	nodes := strings.Split(raw, ",")
	password := strings.TrimSpace(os.Getenv("JUHE_AI_TEST_REDIS_CLUSTER_PASSWORD"))
	base := "redis://"
	if password != "" {
		base += ":" + password + "@"
	}
	base += strings.TrimSpace(nodes[0])
	extra := make([]string, 0, len(nodes)-1)
	for _, node := range nodes[1:] {
		if node = strings.TrimSpace(node); node != "" {
			extra = append(extra, "addr="+node)
		}
	}
	if len(extra) > 0 {
		base += "?" + strings.Join(extra, "&")
	}
	client, err := NewUniversalClient(Config{URL: base, Mode: ModeCluster})
	if err != nil {
		t.Fatalf("NewUniversalClient(cluster): %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("cluster ping: %v", err)
	}
	namespace := DomainNamespace("cluster-test", DomainCircuit)
	keys := []string{
		TaggedNamespacedKey("states", namespace),
		TaggedNamespacedKey("due", namespace),
		TaggedNamespacedKey("lock:account-1", namespace),
	}
	defer client.Del(ctx, keys...)

	for i, key := range keys {
		if err := client.Set(ctx, key, i, time.Minute).Err(); err != nil {
			t.Fatalf("SET %s: %v", key, err)
		}
	}
	values, err := client.MGet(ctx, keys...).Result()
	if err != nil || len(values) != len(keys) {
		t.Fatalf("MGET same-domain keys: %v/%d", err, len(values))
	}
	// 多键 Lua：同域三个键必须同 slot 才能执行。
	script := "redis.call('SET', KEYS[1], 'lua'); return redis.call('SET', KEYS[2], 'lua')"
	if err := client.Eval(ctx, script, keys[:2]).Err(); err != nil {
		t.Fatalf("EVAL same-domain keys: %v", err)
	}
	var scanned []string
	if err := ScanAll(ctx, client, TaggedPrefix(namespace)+"*", func(batch []string) error {
		scanned = append(scanned, batch...)
		return nil
	}); err != nil {
		t.Fatalf("ScanAll cluster: %v", err)
	}
	if len(scanned) < len(keys) {
		t.Fatalf("ScanAll returned %d keys, want >= %d", len(scanned), len(keys))
	}
	// 跨 master 扫描：prod namespace 下三个热点域按 3-master 默认区间分别落
	// 在三个 master，ScanAll 必须逐 master 收集。
	crossKeys := []string{
		TaggedNamespacedKey("scan-probe", DomainNamespace("prod", DomainKeyModel)),
		TaggedNamespacedKey("scan-probe", DomainNamespace("prod", DomainCircuit)),
		TaggedNamespacedKey("scan-probe", DomainNamespace("prod", DomainRateLimit)),
	}
	for _, key := range crossKeys {
		if err := client.Set(ctx, key, "scan", time.Minute).Err(); err != nil {
			t.Fatalf("SET scan key %s: %v", key, err)
		}
		defer client.Del(ctx, key)
	}
	crossScanned := 0
	if err := ScanAll(ctx, client, "juhe-ai:{prod:*", func(batch []string) error {
		for _, key := range batch {
			for _, want := range crossKeys {
				if key == want {
					crossScanned++
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("ScanAll cross-master: %v", err)
	}
	if crossScanned != len(crossKeys) {
		t.Fatalf("ScanAll cross-master found %d/%d keys", crossScanned, len(crossKeys))
	}
}
