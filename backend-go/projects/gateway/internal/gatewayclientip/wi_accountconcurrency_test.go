package gatewayclientip

import (
	"context"
	"errors"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

// newTestRedisAccountConcurrency 构造注入 miniredis 的 RedisAccountConcurrency，
// 返回实例与其拥有的 client，供 Close 语义断言复用。
func newTestRedisAccountConcurrency(t *testing.T, server *miniredis.Miniredis, namespace string) (*RedisAccountConcurrency, *redis.Client) {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	instance, err := NewRedisAccountConcurrency(RedisAccountConcurrencyOptions{
		Client:    client,
		Namespace: namespace,
	})
	if err != nil {
		t.Fatalf("NewRedisAccountConcurrency(注入 client) 失败: %v", err)
	}
	return instance, client
}

func TestWIMemoryAccountConcurrencyAcquireReleaseLifecycle(t *testing.T) {
	tracker := NewMemoryAccountConcurrency(nil)
	if tracker.clock == nil {
		t.Fatal("clock 为 nil 时必须回落到 systemClock")
	}
	ctx := context.Background()

	// 契约：Acquire 恒成功，total 与 lane 计数同步增长；lane 非法值归入 text。
	if !tracker.Acquire("acc-1", "weird-lane") {
		t.Fatal("memory Acquire 必须返回 true")
	}
	tracker.Acquire("acc-1", AccountConcurrencyLaneImage)

	byID, err := tracker.LoadAccountCurrentConcurrencyByID(ctx, []string{"acc-1", "acc-missing", "", "acc-1"})
	if err != nil {
		t.Fatalf("LoadAccountCurrentConcurrencyByID 失败: %v", err)
	}
	// 契约：空账号跳过、重复去重、未知账号读 0；"weird-lane" 计入 total。
	if byID["acc-1"] != 2 || byID["acc-missing"] != 0 {
		t.Fatalf("total 读取错误: %v", byID)
	}
	byLane, err := tracker.LoadAccountCurrentConcurrencyByLane(ctx, []string{"acc-1"}, AccountConcurrencyLaneText)
	if err != nil {
		t.Fatalf("LoadAccountCurrentConcurrencyByLane 失败: %v", err)
	}
	if byLane["acc-1"] != 1 {
		t.Fatalf("text lane 应为 1: %v", byLane)
	}
	if tracker.CurrentAccountConcurrency("acc-1", "") != 2 {
		t.Fatalf("CurrentAccountConcurrency(total)=%d want 2", tracker.CurrentAccountConcurrency("acc-1", ""))
	}
	if tracker.CurrentAccountConcurrency("acc-1", AccountConcurrencyLaneImage) != 1 {
		t.Fatalf("image lane 应为 1")
	}

	// 契约：Release 递减两个计数；减到 0 时删除键而非保留 0。
	tracker.Release("acc-1", "weird-lane")
	if tracker.CurrentAccountConcurrency("acc-1", AccountConcurrencyLaneText) != 0 {
		t.Fatalf("release 后 text lane 应为 0")
	}
	if tracker.CurrentAccountConcurrency("acc-1", "") != 1 {
		t.Fatalf("release 后 total 应为 1")
	}
	tracker.Release("acc-1", "")
	if tracker.CurrentAccountConcurrency("acc-1", "") != 0 {
		t.Fatalf("total 减到 0 后应读 0")
	}
	// 超额 Release 不得产生负数（内存 map 直接删除兜底）。
	tracker.Release("acc-1", "")
	if tracker.CurrentAccountConcurrency("acc-1", "") != 0 {
		t.Fatalf("超额 release 后 total 必须仍为 0")
	}
}

func TestWIMemoryAccountConcurrencyReleaseListeners(t *testing.T) {
	tracker := NewMemoryAccountConcurrency(newManualClock(time.UnixMilli(1_000)))

	events := make(chan AccountConcurrencyReleaseEvent, 4)
	unsubscribe := tracker.SubscribeAccountConcurrencyRelease(func(event AccountConcurrencyReleaseEvent) {
		events <- event
	})
	// panic 的监听器必须被吞掉，不能影响其他监听器（契约：listener failures are swallowed）。
	tracker.SubscribeAccountConcurrencyRelease(func(AccountConcurrencyReleaseEvent) {
		panic("监听器 panic 必须被吞掉")
	})

	tracker.Acquire("acc-1", "")
	tracker.Release("acc-1", AccountConcurrencyLaneText)

	select {
	case event := <-events:
		if event.AccountID != "acc-1" || event.Lane != AccountConcurrencyLaneText {
			t.Fatalf("release 事件内容错误: %+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("未收到 release 事件")
	}

	// 契约：退订后不再收到事件。
	unsubscribe()
	tracker.Release("acc-1", "")
	select {
	case event := <-events:
		t.Fatalf("退订后不应收到事件: %+v", event)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestWIRedisAccountConcurrencyStoreContract(t *testing.T) {
	server := miniredis.RunT(t)
	instance, _ := newTestRedisAccountConcurrency(t, server, "dev")
	ctx := context.Background()

	// 契约：AcquireAccountConcurrency 对 account hash 的 lane 字段做 HINCRBY 并续 TTL；
	// 空 lane 归入 text。
	if err := instance.AcquireAccountConcurrency(ctx, "acc-1", AccountConcurrencyLaneText, 60_000); err != nil {
		t.Fatalf("AcquireAccountConcurrency 失败: %v", err)
	}
	if err := instance.AcquireAccountConcurrency(ctx, "acc-1", "", 60_000); err != nil {
		t.Fatalf("AcquireAccountConcurrency(空 lane=text) 失败: %v", err)
	}
	key := "juhe-ai:dev:gateway-account-concurrency:account-concurrency:acc-1"
	if got := server.HGet(key, AccountConcurrencyLaneText); got != "2" {
		t.Fatalf("text lane 字段=%q want 2", got)
	}
	if !server.Exists(key) {
		t.Fatal("acquire 后 hash key 必须存在")
	}
	// w13d 修复后契约：AcquireAccountConcurrency 同时 HINCRBY total 与 lane
	// 字段（原先只写 lane，ByID 读取的 total 恒缺，属于已修复缺陷）。
	byID, err := instance.LoadAccountCurrentConcurrencyByID(ctx, []string{"acc-1", "acc-missing"})
	if err != nil {
		t.Fatalf("LoadAccountCurrentConcurrencyByID 失败: %v", err)
	}
	if byID["acc-1"] != 2 || byID["acc-missing"] != 0 {
		t.Fatalf("redis ByID 读取（total 字段）错误: %v", byID)
	}
	if instance.CurrentAccountConcurrency("acc-1", "") != 2 {
		t.Fatalf("CurrentAccountConcurrency(空 lane 回落 text 字段) 应为 2")
	}
	// lane 读取走 lane 字段。
	byLane, err := instance.LoadAccountCurrentConcurrencyByLane(ctx, []string{"acc-1"}, AccountConcurrencyLaneText)
	if err != nil || byLane["acc-1"] != 2 {
		t.Fatalf("redis lane 读取错误: %v %v", byLane, err)
	}
	if instance.CurrentAccountConcurrency("acc-1", AccountConcurrencyLaneText) != 2 {
		t.Fatalf("CurrentAccountConcurrency(text) 应为 2")
	}
	if instance.CurrentAccountConcurrency("", "") != 0 {
		t.Fatal("空账号必须直接读 0")
	}

	// 契约：ReleaseAccountConcurrency 减到 <=0 时删除整个 hash。
	if err := instance.ReleaseAccountConcurrency(ctx, "acc-1", ""); err != nil {
		t.Fatalf("ReleaseAccountConcurrency 失败: %v", err)
	}
	if err := instance.ReleaseAccountConcurrency(ctx, "acc-1", AccountConcurrencyLaneText); err != nil {
		t.Fatalf("ReleaseAccountConcurrency 失败: %v", err)
	}
	if server.Exists(key) {
		t.Fatal("计数减到 0 后 hash key 必须被删除")
	}
	if got, err := instance.LoadAccountCurrentConcurrencyByID(ctx, []string{"acc-1"}); err != nil || got["acc-1"] != 0 {
		t.Fatalf("删除后读取必须为 0 无错: %v %v", got, err)
	}

	// 空列表直读空 map；Subscribe 是运行层托管的空实现。
	empty, err := instance.LoadAccountCurrentConcurrencyByID(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("空账号列表必须返回空 map: %v %v", empty, err)
	}
	if instance.SubscribeAccountConcurrencyRelease(func(AccountConcurrencyReleaseEvent) {}) == nil {
		t.Fatal("Subscribe 必须返回非 nil 退订函数")
	}
	// 注入的 *redis.Client 由 Close 负责：关闭后 ping 必须失败。
	instance.Close()
	if err := client0Ping(instance); err == nil {
		t.Fatal("Close 后连接必须已关闭")
	}
}

// client0Ping 直接检查被 Close 的 redis 连接已不可用。
func client0Ping(instance *RedisAccountConcurrency) error {
	client, ok := instance.client.(*redis.Client)
	if !ok {
		return errors.New("非 *redis.Client")
	}
	return client.Ping(context.Background()).Err()
}

func TestWIRedisAccountConcurrencyConstructionErrors(t *testing.T) {
	if _, err := NewRedisAccountConcurrency(RedisAccountConcurrencyOptions{}); err == nil {
		t.Fatal("空 URL 必须报错")
	}
	if _, err := NewRedisAccountConcurrency(RedisAccountConcurrencyOptions{RedisURL: "://bad"}); err == nil {
		t.Fatal("非法 URL 必须报错")
	}
	// 自建 client 路径：RedisURL 合法时内部创建 client，Close 只关自建的。
	server := miniredis.RunT(t)
	instance, err := NewRedisAccountConcurrency(RedisAccountConcurrencyOptions{
		RedisURL:  "redis://" + server.Addr(),
		Name:      "  ",
		Namespace: "dev",
		Clock:     func() int64 { return 1 },
	})
	if err != nil {
		t.Fatalf("RedisURL 构造失败: %v", err)
	}
	// name 空白回落默认名。
	if want := "juhe-ai:dev:gateway-account-concurrency:account-concurrency:x"; instance.accountConcurrencyKey("x") != want {
		t.Fatalf("key=%q want %q", instance.accountConcurrencyKey("x"), want)
	}
	instance.Close()
}

func TestWIRedisAccountConcurrencyLoadByLaneAndErrors(t *testing.T) {
	server := miniredis.RunT(t)
	instance, _ := newTestRedisAccountConcurrency(t, server, "ns")
	ctx := context.Background()

	server.HSet("juhe-ai:ns:gateway-account-concurrency:account-concurrency:acc-1", "image", "3")
	byLane, err := instance.LoadAccountCurrentConcurrencyByLane(ctx, []string{"acc-1"}, AccountConcurrencyLaneImage)
	if err != nil || byLane["acc-1"] != 3 {
		t.Fatalf("lane 读取错误: %v %v", byLane, err)
	}
	// 非 redis.Nil 的错误必须透传。
	server.SetError("模拟故障")
	if _, err := instance.LoadAccountCurrentConcurrencyByLane(ctx, []string{"acc-1"}, "image"); err == nil {
		t.Fatal("Redis 故障必须透传错误")
	}
	if err := instance.AcquireAccountConcurrency(ctx, "acc-1", "", 1_000); err == nil {
		t.Fatal("Redis 故障时 acquire 必须报错")
	}
	if err := instance.ReleaseAccountConcurrency(ctx, "acc-1", ""); err == nil {
		t.Fatal("Redis 故障时 release 必须报错")
	}
}

func TestWIMemoryRuntimeStateStoreContract(t *testing.T) {
	clock := newManualClock(time.UnixMilli(1_000))
	store := NewMemoryRuntimeStateStore(clock)
	ctx := context.Background()

	type payload struct {
		Value string `json:"value"`
	}
	if found, err := store.GetJSON(ctx, "missing", &payload{}); err != nil || found {
		t.Fatalf("未写入前必须 miss: %v %v", found, err)
	}
	if err := store.SetJSON(ctx, "k1", payload{Value: "v1"}, 5_000); err != nil {
		t.Fatalf("SetJSON 失败: %v", err)
	}
	var got payload
	if found, err := store.GetJSON(ctx, "k1", &got); err != nil || !found || got.Value != "v1" {
		t.Fatalf("读回错误: found=%v err=%v value=%+v", found, err, got)
	}
	// 契约：ttl<1 归一化为 1ms（normalizeTtlMs 下限）。
	if err := store.SetJSON(ctx, "k-tiny", payload{Value: "x"}, 0); err != nil {
		t.Fatalf("SetJSON(ttl=0) 失败: %v", err)
	}
	clock.advance(2 * time.Millisecond)
	if found, _ := store.GetJSON(ctx, "k-tiny", &payload{}); found {
		t.Fatal("ttl=0 的条目推进 2ms 后必须过期")
	}
	// 过期即删除（惰性），且 Delete 直接移除。
	clock.advance(10 * time.Second)
	if found, _ := store.GetJSON(ctx, "k1", &payload{}); found {
		t.Fatal("超过 TTL 后必须过期")
	}
	if err := store.SetJSON(ctx, "k2", payload{Value: "v2"}, 60_000); err != nil {
		t.Fatalf("SetJSON 失败: %v", err)
	}
	if err := store.Delete(ctx, "k2"); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	if found, _ := store.GetJSON(ctx, "k2", &payload{}); found {
		t.Fatal("Delete 后必须 miss")
	}
	// json.Unmarshal 失败必须返回错误（不像 redis 驱动那样静默删除）。
	if err := store.SetJSON(ctx, "bad", make(chan int), 60_000); err == nil {
		t.Fatal("不可 JSON 序列化的值必须报错")
	}
}

func TestWIRedisRuntimeStateStoreContract(t *testing.T) {
	server := miniredis.RunT(t)
	store, closeFn, err := NewRedisRuntimeStateStore("redis://"+server.Addr(), "dev", "circuit-test")
	if err != nil {
		t.Fatalf("NewRedisRuntimeStateStore 失败: %v", err)
	}
	t.Cleanup(closeFn)
	ctx := context.Background()

	type payload struct {
		N int `json:"n"`
	}
	if found, err := store.GetJSON(ctx, "missing", &payload{}); err != nil || found {
		t.Fatalf("miss 读取错误: %v %v", found, err)
	}
	if err := store.SetJSON(ctx, "k1", payload{N: 7}, 60_000); err != nil {
		t.Fatalf("SetJSON 失败: %v", err)
	}
	if !server.Exists("juhe-ai:dev:state:circuit-test:k1") {
		t.Fatalf("redis key 布局错误，want juhe-ai:dev:state:circuit-test:k1")
	}
	var got payload
	if found, err := store.GetJSON(ctx, "k1", &got); err != nil || !found || got.N != 7 {
		t.Fatalf("读回错误: found=%v err=%v value=%+v", found, err, got)
	}
	// 契约：坏 JSON 读作 miss 且删除毒键（Node parse failure deletes the poisoned key）。
	if err := server.Set("juhe-ai:dev:state:circuit-test:poison", "not-json"); err != nil {
		t.Fatalf("注入坏 JSON 失败: %v", err)
	}
	if found, err := store.GetJSON(ctx, "poison", &payload{}); err != nil || found {
		t.Fatalf("坏 JSON 必须读作 miss 无错: %v %v", found, err)
	}
	if server.Exists("juhe-ai:dev:state:circuit-test:poison") {
		t.Fatal("毒键必须被删除")
	}
	if err := store.Delete(ctx, "k1"); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	if found, _ := store.GetJSON(ctx, "k1", &payload{}); found {
		t.Fatal("Delete 后必须 miss")
	}
	if err := store.SetJSON(ctx, "bad", make(chan int), 60_000); err == nil {
		t.Fatal("不可序列化值必须报错")
	}
	// ttl<1 归一化为 1ms；miniredis TTL 粒度按 ms 记录。
	if err := store.SetJSON(ctx, "k-tiny", payload{N: 1}, 0); err != nil {
		t.Fatalf("SetJSON(ttl=0) 失败: %v", err)
	}
	if ttl := server.TTL("juhe-ai:dev:state:circuit-test:k-tiny"); ttl <= 0 {
		t.Fatalf("ttl=0 必须归一化为正 TTL: %v", ttl)
	}
}

func TestWIRedisRuntimeStateStoreConstructionErrors(t *testing.T) {
	if _, _, err := NewRedisRuntimeStateStore("", "dev", "name"); err == nil {
		t.Fatal("空 URL 必须报错")
	}
	if _, _, err := NewRedisRuntimeStateStore("://bad", "dev", "name"); err == nil {
		t.Fatal("非法 URL 必须报错")
	}
	server := miniredis.RunT(t)
	// 空 namespace 无法 sanitized，必须报错并关闭自建 client。
	if _, _, err := NewRedisRuntimeStateStore("redis://"+server.Addr(), "///", "name"); err == nil {
		t.Fatal("空 namespace 必须报错")
	}
}

func TestWIRedisStateKeySanitizers(t *testing.T) {
	// 契约：namespace 只保留 [a-zA-Z0-9_.:-]，其余归 _，首尾 _ 裁剪；空报错。
	prefix, err := stateStoreKeyPrefix("prod edge", "circuit name")
	if err != nil {
		t.Fatalf("stateStoreKeyPrefix 失败: %v", err)
	}
	if want := "juhe-ai:prod_edge:state:circuit_name:"; prefix != want {
		t.Fatalf("prefix=%q want %q", prefix, want)
	}
	if _, err := sanitizeRedisNamespacePart("  "); err == nil {
		t.Fatal("空 namespace 必须报错")
	}
	// 契约：key 部分空值回落 "default"。
	if got := sanitizeRedisKeyPart(""); got != "default" {
		t.Fatalf("空 key 必须 default: %q", got)
	}
	// ttl 归一化边界。
	if normalizeTtlMsDuration(0) != time.Millisecond {
		t.Fatal("ttl 0 必须 1ms")
	}
	if normalizeTtlMsDuration(2_000) != 2*time.Second {
		t.Fatal("ttl 2000 必须 2s")
	}
}
