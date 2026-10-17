package gatewaysession

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
)

// 组级会话绑定服务（策略路由分组会话亲和设计 §3/§8 单元测试矩阵）：独立键
// 空间编解码、固定 HMAC 向量、双驱动读写、TTL 读不刷新、CAS 写回与无效记录
// 清理。三层删除按 §5.2 矩阵在响应层验证（本包只提供 ForgetGroupAsync 语义）。

func newTestGroupAffinityService(t *testing.T, mutate func(*GroupAffinityConfig)) (*GroupAffinityService, *miniredis.Miniredis) {
	t.Helper()
	cfg := GroupAffinityConfig{
		CacheDriver: CacheDriverMemory,
		Secret:      "group-affinity-fixed-test-secret",
	}
	var server *miniredis.Miniredis
	if mutate != nil {
		mutate(&cfg)
	}
	if cfg.CacheDriver == CacheDriverRedis && cfg.Redis == nil {
		server = miniredis.RunT(t)
		cfg.RedisCacheURL = "redis://" + server.Addr()
	}
	service, err := NewGroupAffinityService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return service, server
}

// TestDeriveGroupAffinityKeyFixedVectors 固定向量（设计 §3.1 ≥2 条）：键派生
// 是跨实例契约，密钥一致时任何实例必须得到同一键；purpose 独立性由向量锚定。
func TestDeriveGroupAffinityKeyFixedVectors(t *testing.T) {
	service, _ := newTestGroupAffinityService(t, nil)
	vectors := []struct {
		input  GroupAffinityKeyInput
		expect string
	}{
		{
			input:  GroupAffinityKeyInput{SystemAccountID: "sys_acc_1", APIKeyID: "api_key_1", RouteStrategyID: "strategy_rr_1", ConversationKey: "conv-key-alpha"},
			expect: "ce6b79bc13b2599b3d65530bb81f98559a9efff1f8b3a5f28c6bfeef5710d1d3",
		},
		{
			input:  GroupAffinityKeyInput{SystemAccountID: "sys_acc_2", APIKeyID: "api_key_2", RouteStrategyID: "strategy_w_2", ConversationKey: "conv-key-beta"},
			expect: "3db8326bf68f4e05cbb56203f4a7a353a34a6ca84d148e5d34e27c6db8ea8b6f",
		},
	}
	for i, vector := range vectors {
		got, err := service.DeriveGroupAffinityKey(vector.input)
		if err != nil {
			t.Fatalf("向量 %d 派生失败: %v", i+1, err)
		}
		if got != vector.expect {
			t.Fatalf("向量 %d 键 = %s, want %s", i+1, got, vector.expect)
		}
		// 平参形式与结构体形式同键（preauth 端口投影一致性）。
		parts, err := service.DeriveGroupAffinityKeyParts(vector.input.SystemAccountID, vector.input.APIKeyID, vector.input.RouteStrategyID, vector.input.ConversationKey)
		if err != nil || parts != vector.expect {
			t.Fatalf("向量 %d 平参派生 = %q err=%v, want %s", i+1, parts, err, vector.expect)
		}
	}

	// 字段敏感性（§3.1：改变任一字段都会改变结果）：以向量 1 为基线逐字段
	// 变异，结果键必须全部不同。
	baseline := vectors[0].input
	mutations := []GroupAffinityKeyInput{
		{SystemAccountID: "sys_acc_1X", APIKeyID: baseline.APIKeyID, RouteStrategyID: baseline.RouteStrategyID, ConversationKey: baseline.ConversationKey},
		{SystemAccountID: baseline.SystemAccountID, APIKeyID: "api_key_1X", RouteStrategyID: baseline.RouteStrategyID, ConversationKey: baseline.ConversationKey},
		{SystemAccountID: baseline.SystemAccountID, APIKeyID: baseline.APIKeyID, RouteStrategyID: "strategy_rr_1X", ConversationKey: baseline.ConversationKey},
		{SystemAccountID: baseline.SystemAccountID, APIKeyID: baseline.APIKeyID, RouteStrategyID: baseline.RouteStrategyID, ConversationKey: "conv-key-alphaX"},
	}
	seen := map[string]bool{vectors[0].expect: true}
	for i, mutation := range mutations {
		got, err := service.DeriveGroupAffinityKey(mutation)
		if err != nil {
			t.Fatalf("变异 %d 派生失败: %v", i+1, err)
		}
		if seen[got] {
			t.Fatalf("变异 %d 键与已有结果碰撞：%s", i+1, got)
		}
		seen[got] = true
	}

	// 无会话键 → 空键（调用方视作黏性不适用，零额外开销路径）。
	empty, err := service.DeriveGroupAffinityKey(GroupAffinityKeyInput{SystemAccountID: "s", APIKeyID: "k", RouteStrategyID: "r"})
	if err != nil || empty != "" {
		t.Fatalf("空会话键派生 = %q err=%v, want 空串", empty, err)
	}
}

func TestGroupAffinityMemoryRememberAndSticky(t *testing.T) {
	service, _ := newTestGroupAffinityService(t, nil)
	ctx := context.Background()
	key, err := service.DeriveGroupAffinityKeyParts("sys-1", "key-1", "strategy-1", "conv-1")
	if err != nil || key == "" {
		t.Fatalf("派生失败: %q %v", key, err)
	}

	// 未命中（首个协议成功负责创建，§5）。
	if _, hit := service.StickyGroupAsync(ctx, key); hit {
		t.Fatal("写回前不应命中")
	}

	service.RememberGroupAsync(ctx, key, "strategy-1", "grp-a")
	got, hit := service.StickyGroupAsync(ctx, key)
	if !hit || got != "grp-a" {
		t.Fatalf("黏住读 = %q hit=%v, want grp-a", got, hit)
	}

	// 写入即建即更（成功者赢）：同键新分组直接覆盖。
	service.RememberGroupAsync(ctx, key, "strategy-1", "grp-b")
	got, hit = service.StickyGroupAsync(ctx, key)
	if !hit || got != "grp-b" {
		t.Fatalf("重绑后黏住读 = %q hit=%v, want grp-b", got, hit)
	}

	// 非法入参拒绝写入：键/组为空不改状态。
	service.RememberGroupAsync(ctx, "", "strategy-1", "grp-c")
	service.RememberGroupAsync(ctx, key, "strategy-1", "")
	got, hit = service.StickyGroupAsync(ctx, key)
	if !hit || got != "grp-b" {
		t.Fatalf("非法写回后黏住读 = %q hit=%v, want grp-b 保持", got, hit)
	}
}

func TestGroupAffinityMemoryTTLReadDoesNotRefresh(t *testing.T) {
	now := time.Date(2026, 10, 17, 8, 0, 0, 0, time.UTC)
	cfg := GroupAffinityConfig{CacheDriver: CacheDriverMemory, Secret: "group-affinity-fixed-test-secret", Clock: func() time.Time { return now }}
	service, err := NewGroupAffinityService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key, err := service.DeriveGroupAffinityKeyParts("sys-1", "key-1", "strategy-1", "conv-1")
	if err != nil {
		t.Fatal(err)
	}
	service.RememberGroupAsync(ctx, key, "strategy-1", "grp-a")

	// 23h：仍命中；黏住读不刷新 TTL。
	now = now.Add(23 * time.Hour)
	if _, hit := service.StickyGroupAsync(ctx, key); !hit {
		t.Fatal("23h 黏住读应命中")
	}
	// 25h（> 24h，且期间只有读）：过期，证明读没有续期。
	now = now.Add(2 * time.Hour)
	if _, hit := service.StickyGroupAsync(ctx, key); hit {
		t.Fatal("25h 黏住读应过期（读不得刷新 TTL）")
	}
}

func TestGroupAffinityRedisRememberStickyAndTTL(t *testing.T) {
	service, server := newTestGroupAffinityService(t, func(cfg *GroupAffinityConfig) {
		cfg.CacheDriver = CacheDriverRedis
		cfg.RedisNamespace = "juhe-ai:test"
	})
	ctx := context.Background()
	key, err := service.DeriveGroupAffinityKeyParts("sys-1", "key-1", "strategy-1", "conv-1")
	if err != nil {
		t.Fatal(err)
	}

	if _, hit := service.StickyGroupAsync(ctx, key); hit {
		t.Fatal("写回前不应命中")
	}
	service.RememberGroupAsync(ctx, key, "strategy-1", "grp-a")
	got, hit := service.StickyGroupAsync(ctx, key)
	if !hit || got != "grp-a" {
		t.Fatalf("Redis 黏住读 = %q hit=%v, want grp-a", got, hit)
	}

	// Redis TTL 1h，读不刷新：30m 读一次（命中），再过 40m（累计 70m）必须
	// 过期——若读刷新了 TTL，70m 仍应命中。
	fullKey, _ := redisGroupAffinityBindingKey("juhe-ai:test", key)
	if ttl := server.TTL(fullKey); ttl <= 0 {
		t.Fatalf("Redis 绑定键应带 TTL，实际 %v", ttl)
	}
	server.FastForward(30 * time.Minute)
	if _, hit := service.StickyGroupAsync(ctx, key); !hit {
		t.Fatal("30m 黏住读应命中")
	}
	server.FastForward(40 * time.Minute)
	if _, hit := service.StickyGroupAsync(ctx, key); hit {
		t.Fatal("70m 黏住读应过期（读不得刷新 TTL）")
	}
}

func TestGroupAffinityRedisInvalidRecordDeletesAndMisses(t *testing.T) {
	service, server := newTestGroupAffinityService(t, func(cfg *GroupAffinityConfig) {
		cfg.CacheDriver = CacheDriverRedis
		cfg.RedisNamespace = "juhe-ai:test"
	})
	ctx := context.Background()
	key, err := service.DeriveGroupAffinityKeyParts("sys-1", "key-1", "strategy-1", "conv-1")
	if err != nil {
		t.Fatal(err)
	}
	fullKey, _ := redisGroupAffinityBindingKey("juhe-ai:test", key)

	// 缺 groupId 的记录不可用：删除并视同未命中（§3.2）。
	bad, _ := json.Marshal(map[string]string{"routeStrategyId": "strategy-1"})
	_ = server.Set(fullKey, string(bad))
	if _, hit := service.StickyGroupAsync(ctx, key); hit {
		t.Fatal("缺 groupId 记录不应命中")
	}
	if server.Exists(fullKey) {
		t.Fatal("非法记录应被删除")
	}

	// 非 JSON 记录同样删除。
	_ = server.Set(fullKey, "not-json")
	if _, hit := service.StickyGroupAsync(ctx, key); hit {
		t.Fatal("非 JSON 记录不应命中")
	}
	if server.Exists(fullKey) {
		t.Fatal("非 JSON 记录应被删除")
	}
}

func TestGroupAffinityRedisConcurrentRememberWinnerStable(t *testing.T) {
	service, _ := newTestGroupAffinityService(t, func(cfg *GroupAffinityConfig) {
		cfg.CacheDriver = CacheDriverRedis
		cfg.RedisNamespace = "juhe-ai:test"
	})
	ctx := context.Background()
	key, err := service.DeriveGroupAffinityKeyParts("sys-1", "key-1", "strategy-1", "conv-1")
	if err != nil {
		t.Fatal(err)
	}

	// 并发写回（成功者赢 CAS）：终值必须是两个候选之一，且后续黏住读一致。
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				service.RememberGroupAsync(ctx, key, "strategy-1", "grp-a")
			} else {
				service.RememberGroupAsync(ctx, key, "strategy-1", "grp-b")
			}
		}(i)
	}
	wg.Wait()
	got, hit := service.StickyGroupAsync(ctx, key)
	if !hit || (got != "grp-a" && got != "grp-b") {
		t.Fatalf("并发写回终值 = %q hit=%v, want grp-a|grp-b", got, hit)
	}
}

func TestGroupAffinityForget(t *testing.T) {
	service, _ := newTestGroupAffinityService(t, nil)
	ctx := context.Background()
	key, err := service.DeriveGroupAffinityKeyParts("sys-1", "key-1", "strategy-1", "conv-1")
	if err != nil {
		t.Fatal(err)
	}
	service.RememberGroupAsync(ctx, key, "strategy-1", "grp-a")
	service.ForgetGroupAsync(ctx, key)
	if _, hit := service.StickyGroupAsync(ctx, key); hit {
		t.Fatal("显式 forget 后不应命中")
	}
	// 空键 forget 无害。
	service.ForgetGroupAsync(ctx, "")
}

// TestGroupAffinityCASScriptMatchAndMismatch 确定性覆盖 CAS 脚本两分支（复审
// P2-4②）：expected 匹配（含 missing=空串）才 SET PX 返回 1，失配返回 0 且
// 现值保留——RememberGroupAsync 的「两轮冲突放弃、保留现值」语义的数据面基石。
func TestGroupAffinityCASScriptMatchAndMismatch(t *testing.T) {
	server := miniredis.RunT(t)
	client, err := DialGoRedisClient("redis://" + server.Addr())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := "juhe-ai:test:session-affinity:group-binding:cas-script"

	// missing 分支：expected=空串命中 → 写入 + PX TTL。
	ok, err := client.Eval(ctx, redisSetGroupAffinityBindingScript, []string{key},
		"", `{"routeStrategyId":"s1","groupId":"grp-a"}`, "60000")
	if err != nil {
		t.Fatal(err)
	}
	if !redisBooleanResult(ok) {
		t.Fatalf("missing 键 CAS 应命中，结果 = %v", ok)
	}
	if got, _ := server.Get(key); got != `{"routeStrategyId":"s1","groupId":"grp-a"}` {
		t.Fatalf("CAS 写入值 = %q", got)
	}
	if ttl := server.TTL(key); ttl <= 0 || ttl > time.Minute {
		t.Fatalf("CAS 写入 TTL = %v, want (0, 1m]", ttl)
	}

	// 失配分支：expected 与现值不一致 → 返回 0，现值保留。
	stale, err := client.Eval(ctx, redisSetGroupAffinityBindingScript, []string{key},
		`{"routeStrategyId":"s1","groupId":"grp-old"}`, `{"routeStrategyId":"s1","groupId":"grp-new"}`, "60000")
	if err != nil {
		t.Fatal(err)
	}
	if redisBooleanResult(stale) {
		t.Fatal("expected 失配 CAS 不得写入")
	}
	if got, _ := server.Get(key); got != `{"routeStrategyId":"s1","groupId":"grp-a"}` {
		t.Fatalf("失配后现值被改写：%q", got)
	}

	// 命中分支：expected=现值 → 覆盖写入。
	hit, err := client.Eval(ctx, redisSetGroupAffinityBindingScript, []string{key},
		`{"routeStrategyId":"s1","groupId":"grp-a"}`, `{"routeStrategyId":"s1","groupId":"grp-new"}`, "60000")
	if err != nil {
		t.Fatal(err)
	}
	if !redisBooleanResult(hit) {
		t.Fatal("expected 匹配 CAS 应命中")
	}
	if got, _ := server.Get(key); got != `{"routeStrategyId":"s1","groupId":"grp-new"}` {
		t.Fatalf("命中覆盖值 = %q", got)
	}
}
