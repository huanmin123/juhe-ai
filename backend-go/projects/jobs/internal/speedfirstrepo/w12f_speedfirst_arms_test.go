package speedfirstrepo

// w12f_speedfirst_arms_test.go 覆盖 SpeedFirstStore 索引维护与锁臂的剩余
// 分支：client 关闭后的 fail-closed 传播、锁被占与 ctx 取消、索引 exists/
// 截断/过滤路径、候选扫描排序平局。全部使用独立 miniredis 实例，namespace
// 与数据键加 w12f- 前缀；错误臂断言保留原始错误语义。

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
)

// newW12fSpeedFirstStore 启动独立 miniredis 并打开固定时钟/随机的 Store。
func newW12fSpeedFirstStore(t *testing.T) (*SpeedFirstStore, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	t.Cleanup(server.Close)
	store, err := OpenSpeedFirstStore(SpeedFirstRedisConfig{
		Enabled: true, URL: "redis://" + server.Addr(), Namespace: "w12f-ns",
	}, nil)
	if err != nil {
		t.Fatalf("打开测试 Store 失败: %v", err)
	}
	store.now = func() time.Time { return wfSpeedFirstBase }
	store.random = func() float64 { return 0.5 }
	t.Cleanup(func() { _ = store.Close() })
	return store, server
}

// TestW12fSpeedFirstClosedClientErrorArms 覆盖 client 关闭后 generation 与
// 候选扫描入口的 fail-closed 错误传播（保留原始连接错误）。
func TestW12fSpeedFirstClosedClientErrorArms(t *testing.T) {
	store, server := newW12fSpeedFirstStore(t)
	ctx := context.Background()
	if err := store.client.Close(); err != nil {
		t.Fatal(err)
	}
	server.Close()

	// LoadGeneration → loadGenerationEvent getJSON 错误传播。
	if _, err := store.LoadGeneration(ctx); err == nil {
		t.Fatal("client 关闭后 LoadGeneration 必须报错")
	}
	// loadGenerationEvent 的 getJSON 错误直测（与 LoadGeneration 同源但独立臂）。
	if _, err := store.loadGenerationEvent(ctx); err == nil {
		t.Fatal("client 关闭后 loadGenerationEvent 必须报错")
	}
	// renewGeneration 的 loadGenerationEvent 错误传播。
	if _, err := store.renewGeneration(ctx, "[0,\"initial\"]"); err == nil {
		t.Fatal("client 关闭后 renewGeneration 必须报错")
	}
	// ListProbeCandidates → loadIndexKeys 错误传播。
	if _, err := store.ListProbeCandidates(ctx, 5); err == nil {
		t.Fatal("client 关闭后 ListProbeCandidates 必须报错")
	}
}

// TestW12fSpeedFirstIndexLockBusyTimesOut 覆盖索引锁被占时的重试耗尽臂
// （addIndexKey 与 filterIndexKeys 的 !locked 错误出口，保留原始错误文本）。
func TestW12fSpeedFirstIndexLockBusyTimesOut(t *testing.T) {
	store, _ := newW12fSpeedFirstStore(t)
	ctx := context.Background()
	// 预占 probe-index 锁并注入远过期时间，制造锁竞争。
	if err := store.client.SetNX(ctx, store.redisKey(store.indexLockKey(store.probeIndexKey())), "w12f-holder", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	err := store.addIndexKey(ctx, store.probeIndexKey(), "w12f-key")
	if err == nil || !strings.Contains(err.Error(), "索引锁获取失败") {
		t.Fatalf("锁被占时 addIndexKey 必须报索引锁获取失败: %v", err)
	}
	// 换一个独立索引锁制造 filterIndexKeys 的同一出口。
	if err := store.client.SetNX(ctx, store.redisKey(store.indexLockKey("w12f-idx-filter")), "w12f-holder", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	err = store.filterIndexKeys(ctx, "w12f-idx-filter", map[string]bool{"w12f-key": true})
	if err == nil || !strings.Contains(err.Error(), "索引锁获取失败") {
		t.Fatalf("锁被占时 filterIndexKeys 必须报索引锁获取失败: %v", err)
	}
}

// TestW12fSpeedFirstAcquireIndexLockCtxCanceled 覆盖 ctx 已取消时索引锁
// 等待循环的立即退出臂（不触发 50 次重试，测试保持快速）。
func TestW12fSpeedFirstAcquireIndexLockCtxCanceled(t *testing.T) {
	store, _ := newW12fSpeedFirstStore(t)
	if err := store.client.SetNX(context.Background(), store.redisKey(store.indexLockKey("w12f-idx-canceled")), "w12f-holder", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	locked, err := store.acquireIndexLock(ctx, "w12f-idx-canceled", "w12f-token")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ctx 取消必须返回 context.Canceled: %v", err)
	}
	if locked {
		t.Fatal("取消后不得获得锁")
	}
}

// TestW12fSpeedFirstAddIndexKeyExistingAndTruncate 覆盖 addIndexKey 的
// exists 命中、已有索引 CAS（expected=当前值）与超过上限截断。
func TestW12fSpeedFirstAddIndexKeyExistingAndTruncate(t *testing.T) {
	store, _ := newW12fSpeedFirstStore(t)
	ctx := context.Background()

	// 重复添加同一 key：exists 命中，索引保持一项。
	if err := store.setJSON(ctx, "w12f-idx-exists", map[string]any{"keys": []string{"w12f-key"}}, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := store.addIndexKey(ctx, "w12f-idx-exists", "w12f-key"); err != nil {
		t.Fatalf("重复 addIndexKey 失败: %v", err)
	}
	keys, err := store.loadIndexKeys(ctx, "w12f-idx-exists")
	if err != nil || len(keys) != 1 || keys[0] != "w12f-key" {
		t.Fatalf("重复添加后索引=%v err=%v", keys, err)
	}

	// 超过上限：追加后截断保留最后 speedFirstIndexMaxKeys 个。
	overflow := make([]string, 0, speedFirstIndexMaxKeys+1)
	for i := 0; i <= speedFirstIndexMaxKeys; i++ {
		overflow = append(overflow, "w12f-old-"+strconv.Itoa(i))
	}
	if err := store.setJSON(ctx, "w12f-idx-overflow", map[string]any{"keys": overflow}, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := store.addIndexKey(ctx, "w12f-idx-overflow", "w12f-new"); err != nil {
		t.Fatalf("超限 addIndexKey 失败: %v", err)
	}
	keys, err = store.loadIndexKeys(ctx, "w12f-idx-overflow")
	if err != nil || len(keys) != speedFirstIndexMaxKeys {
		t.Fatalf("截断后长度=%d err=%v", len(keys), err)
	}
	if keys[len(keys)-1] != "w12f-new" {
		t.Fatalf("新 key 必须保留在尾部: %v", keys[len(keys)-1])
	}
}

// TestW12fSpeedFirstFilterIndexKeysRemoves 覆盖 filterIndexKeys 的过滤循环
// （保留项 append）与已有索引 CAS expected 分支。
func TestW12fSpeedFirstFilterIndexKeysRemoves(t *testing.T) {
	store, _ := newW12fSpeedFirstStore(t)
	ctx := context.Background()
	if err := store.setJSON(ctx, "w12f-idx-filter-run", map[string]any{"keys": []string{"w12f-a", "w12f-b", "w12f-c"}}, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := store.filterIndexKeys(ctx, "w12f-idx-filter-run", map[string]bool{"w12f-b": true}); err != nil {
		t.Fatalf("filterIndexKeys 失败: %v", err)
	}
	keys, err := store.loadIndexKeys(ctx, "w12f-idx-filter-run")
	if err != nil || len(keys) != 2 || keys[0] != "w12f-a" || keys[1] != "w12f-c" {
		t.Fatalf("过滤后索引=%v err=%v", keys, err)
	}
}

// TestW12fSpeedFirstRemoveIndexKeysCleansBothIndexes 覆盖 removeIndexKeys
// 对 probe/all 两个索引的过滤（正常路径，无待移除键时直接返回）。
func TestW12fSpeedFirstRemoveIndexKeysCleansBothIndexes(t *testing.T) {
	store, _ := newW12fSpeedFirstStore(t)
	ctx := context.Background()
	for _, indexKey := range []string{store.probeIndexKey(), store.allIndexKey()} {
		if err := store.setJSON(ctx, indexKey, map[string]any{"keys": []string{"w12f-dead"}}, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.removeIndexKeys(ctx, []string{"w12f-dead"}); err != nil {
		t.Fatalf("removeIndexKeys 失败: %v", err)
	}
	for _, indexKey := range []string{store.probeIndexKey(), store.allIndexKey()} {
		keys, err := store.loadIndexKeys(ctx, indexKey)
		if err != nil || len(keys) != 0 {
			t.Fatalf("索引 %s 未清理: %v err=%v", indexKey, keys, err)
		}
	}
}

// TestW12fSpeedFirstListCandidatesSortTieBreak 覆盖候选排序在 nextProbeAtMS
// 相同时按 AccountID 的平局比较分支。
func TestW12fSpeedFirstListCandidatesSortTieBreak(t *testing.T) {
	store, _ := newW12fSpeedFirstStore(t)
	ctx := context.Background()
	generation, err := store.LoadGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	nextProbe := wfSpeedFirstBase.UnixMilli() - 1
	var probeKeys []string
	for _, accountID := range []string{"w12f-z", "w12f-a"} {
		state := wfSpeedFirstState(generation)
		state.AccountID = accountID
		state.RuntimeKey = accountID
		nextProbeAt := nextProbe
		degradedUntil := wfSpeedFirstBase.Add(10 * time.Minute).UnixMilli()
		state.DegradedUntilMS = &degradedUntil
		state.NextProbeAtMS = &nextProbeAt
		key := stateKeyFor(state.Scope, state.RuntimeKey)
		if err := store.setJSON(ctx, key, state, time.Hour); err != nil {
			t.Fatal(err)
		}
		probeKeys = append(probeKeys, key)
	}
	if err := store.setJSON(ctx, store.probeIndexKey(), map[string]any{"keys": probeKeys}, time.Hour); err != nil {
		t.Fatal(err)
	}
	candidates, err := store.ListProbeCandidates(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("候选数=%d", len(candidates))
	}
	if candidates[0].AccountID != "w12f-a" || candidates[1].AccountID != "w12f-z" {
		t.Fatalf("平局必须按 AccountID 升序: %s, %s", candidates[0].AccountID, candidates[1].AccountID)
	}
}
