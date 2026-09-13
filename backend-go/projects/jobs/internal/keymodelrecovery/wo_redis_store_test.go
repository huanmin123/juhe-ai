package keymodelrecovery

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newWoRedisStore 启动 miniredis 并构建真实 RedisStore（走真实 Lua 脚本）。
func newWoRedisStore(t *testing.T) (*RedisStore, *miniredis.Miniredis, *redis.Client, RedisKeys) {
	t.Helper()
	server := miniredis.RunT(t)
	t.Cleanup(func() { server.Close() })
	store, err := OpenRedisStore(RedisConfig{
		Enabled:   true,
		URL:       "redis://" + server.Addr() + "/0",
		Namespace: "wo-space",
	})
	if err != nil {
		t.Fatalf("打开 RedisStore 失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	keys, err := NewRedisKeys("wo-space")
	if err != nil {
		t.Fatal(err)
	}
	return store, server, client, keys
}

func woOpenState(t *testing.T, hash string) State {
	t.Helper()
	key := CapabilityKey{
		CredentialSourceAccountID: "src-1", KeyFingerprint: "fp-1", ClientModel: "m",
		ClientEndpointFamily: "responses", FinalUpstreamModel: "up", UpstreamEndpointMode: "responses_json",
		DispatchRevision: 1,
	}
	state, err := NewOpen(key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	state.CapabilityHash = hash
	return state
}

func TestOpenRedisStoreValidation(t *testing.T) {
	if _, err := OpenRedisStore(RedisConfig{}); err == nil {
		t.Fatalf("未启用应报错")
	}
	if _, err := OpenRedisStore(RedisConfig{Enabled: true, URL: "://bad", Namespace: "ns"}); err == nil {
		t.Fatalf("非法 URL 应报错")
	}
	server := miniredis.RunT(t)
	t.Cleanup(func() { server.Close() })
	if _, err := OpenRedisStore(RedisConfig{Enabled: true, URL: "redis://" + server.Addr(), Namespace: "bad namespace!"}); err == nil {
		t.Fatalf("非法命名空间应报错")
	}
	var nilStore *RedisStore
	if err := nilStore.Close(); err != nil {
		t.Fatalf("nil store 关闭应安全: %v", err)
	}
	// LoadRedisConfig：nil getenv 与非法命名空间分支。
	if _, err := LoadRedisConfig(nil); err == nil {
		t.Fatalf("nil getenv 应报错")
	}
	if _, err := NewRedisKeys("bad namespace!"); err == nil {
		t.Fatalf("非法命名空间应报错")
	}
}

func TestRedisStorePingAndServerNow(t *testing.T) {
	store, _, _, _ := newWoRedisStore(t)
	ctx := context.Background()
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping 不应报错: %v", err)
	}
	now, err := store.ServerNow(ctx)
	if err != nil || now.IsZero() {
		t.Fatalf("ServerNow 应返回服务器时间: %v %v", now, err)
	}
	if now.Location() != time.UTC {
		t.Fatalf("ServerNow 应返回 UTC: %v", now.Location())
	}
}

func TestRedisStoreListDueAndValidation(t *testing.T) {
	store, _, client, keys := newWoRedisStore(t)
	ctx := context.Background()
	if _, err := store.ListDue(ctx, time.Now(), 0); err == nil {
		t.Fatalf("limit<1 应报错")
	}
	now := time.Now()
	due := stateFixture(t, "hash-due")
	open := stateFixture(t, "hash-open")
	open.Phase = Open
	open.RetryAt = now.Add(-time.Second)
	due.Phase = Recovering
	due.RetryAt = now.Add(-2 * time.Second)
	seedState(t, ctx, client, keys, open)
	seedState(t, ctx, client, keys, due)
	if err := client.ZAdd(ctx, keys.Due(), redis.Z{Score: float64(now.Add(-time.Second).UnixMilli()), Member: open.CapabilityHash}).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.ZAdd(ctx, keys.Due(), redis.Z{Score: float64(now.Add(-2 * time.Second).UnixMilli()), Member: due.CapabilityHash}).Err(); err != nil {
		t.Fatal(err)
	}
	states, err := store.ListDue(ctx, now, 10)
	if err != nil {
		t.Fatalf("ListDue 不应报错: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("应返回 2 个到期状态: %d", len(states))
	}
	// 契约：RECOVERING 续跑优先于 OPEN。
	if states[0].Phase != Recovering {
		t.Fatalf("RECOVERING 应排在前: %#v", states)
	}
	// 缺失 state 的 hash 应跳过而不是报错。
	if err := client.ZAdd(ctx, keys.Due(), redis.Z{Score: float64(now.Add(-3 * time.Second).UnixMilli()), Member: "ghost"}).Err(); err != nil {
		t.Fatal(err)
	}
	states, err = store.ListDue(ctx, now, 10)
	if err != nil || len(states) != 2 {
		t.Fatalf("缺失 state 应跳过: %d %v", len(states), err)
	}
	// 状态 JSON 损坏或哈希不一致必须报完整性错误。
	if err := client.Set(ctx, keys.State("corrupt"), "{broken", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.ZAdd(ctx, keys.Due(), redis.Z{Score: float64(now.Add(-4 * time.Second).UnixMilli()), Member: "corrupt"}).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListDue(ctx, now, 10); err == nil || !strings.Contains(err.Error(), "完整性") {
		t.Fatalf("损坏状态应报完整性错误: %v", err)
	}
}

func TestRedisStoreAcquireRenewCommitLifecycle(t *testing.T) {
	store, _, client, keys := newWoRedisStore(t)
	ctx := context.Background()
	now, err := store.ServerNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state := woOpenState(t, "hash-lifecycle")
	state.RetryAt = now.Add(-time.Second)
	if err := client.ZAdd(ctx, keys.Due(), redis.Z{Score: float64(state.RetryAt.UnixMilli()), Member: state.CapabilityHash}).Err(); err != nil {
		t.Fatal(err)
	}
	// 无状态记录 → stale。
	if _, status, err := store.Acquire(ctx, state, "lease-a", false, false); err != nil || status != Stale {
		t.Fatalf("缺 state 应 stale: %s %v", status, err)
	}
	seedState(t, ctx, client, keys, state)
	// 代际不匹配 → stale。
	mutated := state
	mutated.Generation = 99
	seedState(t, ctx, client, keys, mutated)
	if _, status, err := store.Acquire(ctx, state, "lease-a", false, false); err != nil || status != Stale {
		t.Fatalf("代际不匹配应 stale: %s %v", status, err)
	}
	seedState(t, ctx, client, keys, state)
	// 正常获取 → applied 且进入 HALF_OPEN。
	acquired, status, err := store.Acquire(ctx, state, "lease-a", false, false)
	if err != nil || status != Applied {
		t.Fatalf("正常获取应 applied: %s %v", status, err)
	}
	if acquired.Phase != HalfOpen || acquired.Lease == nil || acquired.Lease.ID != "lease-a" {
		t.Fatalf("获取后状态不符: %#v", acquired)
	}
	// 二次获取：HALF_OPEN 阶段不再是可获取状态（阶段守卫先于租约检查）。
	if _, status, err := store.Acquire(ctx, state, "lease-b", false, false); err != nil || status != NotDue {
		t.Fatalf("重复获取应 not_due: %s %v", status, err)
	}
	// 续租：正确租约成功，错误租约失败。
	if ok, err := store.Renew(ctx, acquired, "lease-a"); err != nil || !ok {
		t.Fatalf("续租应成功: %v %v", ok, err)
	}
	if ok, err := store.Renew(ctx, acquired, "lease-b"); err != nil || ok {
		t.Fatalf("错误租约续租应失败: %v %v", ok, err)
	}
	// 提交恢复中结果：due 分数被更新为下一次重试时间。
	next := acquired
	next.Phase = Recovering
	next.RecoverySuccessCount = 1
	next.Lease = nil
	next.RetryAt = now.Add(10 * time.Second)
	if status, err := store.Commit(ctx, acquired, next, "lease-a"); err != nil || status != Applied {
		t.Fatalf("提交应 applied: %s %v", status, err)
	}
	score, err := client.ZScore(ctx, keys.Due(), next.CapabilityHash).Result()
	if err != nil || int64(score) != next.RetryAt.UnixMilli() {
		t.Fatalf("due 分数应更新为重试时间: %v %v", score, err)
	}
	// 提交 CLOSED：租约已在首次提交时删除，这里补写租约以满足 CAS；
	// 提交后进入 closed 有序集，脱离 due。
	closed := next
	closed.Phase = Closed
	closed.RetryAt = time.Time{}
	if err := client.Set(ctx, keys.Lease(next.CapabilityHash), "lease-a", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if status, err := store.Commit(ctx, next, closed, "lease-a"); err != nil || status != Applied {
		t.Fatalf("提交关闭应 applied: %s %v", status, err)
	}
	retained, err := client.ZRangeByScore(ctx, keys.Closed(), &redis.ZRangeBy{Min: "-inf", Max: "+inf"}).Result()
	if err != nil || len(retained) != 1 || retained[0] != closed.CapabilityHash {
		t.Fatalf("关闭后应进入 closed 有序集: %v %v", retained, err)
	}
	// CleanClosed：超限参数报错；保留期满后清理。
	if _, err := store.CleanClosed(ctx, 0); err == nil {
		t.Fatalf("limit<1 应报错")
	}
	if _, err := store.CleanClosed(ctx, 1001); err == nil {
		t.Fatalf("limit>1000 应报错")
	}
	if err := client.ZAdd(ctx, keys.Closed(), redis.Z{Score: float64(time.Now().Add(-time.Minute).UnixMilli()), Member: closed.CapabilityHash}).Err(); err != nil {
		t.Fatal(err)
	}
	removed, err := store.CleanClosed(ctx, 100)
	if err != nil || removed != 1 {
		t.Fatalf("应清理 1 条: %d %v", removed, err)
	}
	if exists, err := client.Exists(ctx, keys.State(closed.CapabilityHash)).Result(); err != nil || exists != 0 {
		t.Fatalf("关闭状态应被删除: %v %v", exists, err)
	}
}

func stateFixture(t *testing.T, hash string) State {
	t.Helper()
	state := State{CapabilityHash: hash, Generation: 1}
	state.CapabilityKey = CapabilityKey{
		CredentialSourceAccountID: "src-1", KeyFingerprint: "fp", ClientModel: "m",
		ClientEndpointFamily: "responses", FinalUpstreamModel: "up", UpstreamEndpointMode: "responses_json",
		DispatchRevision: 1,
	}
	return state
}

func seedState(t *testing.T, ctx context.Context, client *redis.Client, keys RedisKeys, state State) {
	t.Helper()
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, keys.State(state.CapabilityHash), encoded, 0).Err(); err != nil {
		t.Fatal(err)
	}
}
