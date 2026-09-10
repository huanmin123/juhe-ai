package speedfirstrepo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// 本文件覆盖 SpeedFirstStore 的错误传播路径：Redis client 关闭后的统一
// fail-closed 行为、损坏 generation 事件的规范化失败传播，以及 mutation
// 锁被占时的静默跳过语义。所有错误都断言保留原始错误语义，不静默降级。

func TestWFSpeedFirstClosedClientFailsClosed(t *testing.T) {
	server := miniredis.RunT(t)
	t.Cleanup(server.Close)
	store, err := OpenSpeedFirstStore(SpeedFirstRedisConfig{
		Enabled: true, URL: "redis://" + server.Addr(), Namespace: "wf-test",
	}, func() time.Time { return wfSpeedFirstBase })
	if err != nil {
		t.Fatalf("打开测试 Store 失败: %v", err)
	}
	ctx := context.Background()
	if err := store.client.Close(); err != nil {
		t.Fatalf("关闭 client 失败: %v", err)
	}

	// JSON 编码失败必须原样返回（不可编码值触发 Marshal 错误分支）。
	if _, err := store.compareSetJSON(ctx, "k", map[string]any{"bad": make(chan int)}, map[string]any{"v": 1}, time.Minute); err == nil {
		t.Fatal("expected 编码失败必须报错")
	}
	if _, err := store.compareSetJSON(ctx, "k", nil, map[string]any{"bad": make(chan int)}, time.Minute); err == nil {
		t.Fatal("next 编码失败必须报错")
	}
	if _, err := store.compareDeleteJSON(ctx, "k", map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatal("compareDeleteJSON 编码失败必须报错")
	}
	if err := store.setJSON(ctx, "k", map[string]any{"bad": make(chan int)}, time.Minute); err == nil {
		t.Fatal("setJSON 编码失败必须报错")
	}

	// client 关闭后的命令必须全部失败（fail-closed 传播）。
	if _, err := store.compareSetJSON(ctx, "k", nil, map[string]any{"v": 1}, time.Minute); err == nil {
		t.Fatal("compareSetJSON 关闭后必须报错")
	}
	if _, err := store.compareDeleteJSON(ctx, "k", map[string]any{"v": 1}); err == nil {
		t.Fatal("compareDeleteJSON 关闭后必须报错")
	}
	if _, err := store.renewLock(ctx, "k", "token", time.Minute); err == nil {
		t.Fatal("renewLock 关闭后必须报错")
	}
	var sink map[string]any
	if _, err := store.getJSON(ctx, "k", &sink); err == nil {
		t.Fatal("getJSON 关闭后必须报错")
	}
	if err := store.writeStateAndIndexes(ctx, "k", wfSpeedFirstState("g"), time.Minute, true); err == nil {
		t.Fatal("writeStateAndIndexes 关闭后必须报错")
	}
	if err := store.addIndexKey(ctx, "v1:closed-idx", "k"); err == nil {
		t.Fatal("addIndexKey 关闭后必须报错")
	}
	if err := store.filterIndexKeys(ctx, "v1:closed-idx", map[string]bool{"k": true}); err == nil {
		t.Fatal("filterIndexKeys 关闭后必须报错")
	}
	if err := store.removeIndexKeys(ctx, []string{"k"}); err == nil {
		t.Fatal("removeIndexKeys 关闭后必须报错")
	}
	state := wfSpeedFirstState("gen-closed")
	candidate := state.candidate()
	if _, err := store.AcquireClaim(ctx, candidate); err == nil {
		t.Fatal("AcquireClaim 关闭后必须报错")
	}
	if err := store.Discard(ctx, candidate); err == nil {
		t.Fatal("Discard 关闭后必须报错")
	}
	if _, err := store.ListProbeCandidates(ctx, 1); err == nil {
		t.Fatal("ListProbeCandidates 关闭后必须报错")
	}
	if _, err := store.LoadGeneration(ctx); err == nil {
		t.Fatal("LoadGeneration 关闭后必须报错")
	}
	if _, err := store.renewGeneration(ctx, `[0,"initial"]`); err == nil {
		t.Fatal("renewGeneration 关闭后必须报错")
	}
}

func TestWFSpeedFirstCorruptGenerationPropagates(t *testing.T) {
	store, server := newWFSpeedFirstStore(t)
	ctx := context.Background()
	// 缺少 version 的 generation 事件必须在读取时 fail closed。
	if err := server.Set(store.redisKey(store.generationKey()), `{}`); err != nil {
		t.Fatalf("seed 损坏 generation 失败: %v", err)
	}
	if _, err := store.LoadGeneration(ctx); err == nil {
		t.Fatal("损坏 generation 读取必须报错")
	}
	if _, err := store.renewGeneration(ctx, `[0,"initial"]`); err == nil {
		t.Fatal("损坏 generation 续租必须报错")
	}

	state := wfSpeedFirstState("")
	wfSpeedFirstSeed(t, store, state)
	state.Generation = ""
	candidate := state.candidate()
	// 空 generation 需要读取标记，标记损坏时全部操作必须报错。
	if err := store.Discard(ctx, candidate); err == nil {
		t.Fatal("Discard 空 generation 读取失败必须报错")
	}
	if _, err := store.Defer(ctx, candidate); err == nil {
		t.Fatal("Defer 空 generation 读取失败必须报错")
	}
	ref := opsjobs.ProbeAccountRef{AccountID: "acc-1"}
	if _, err := store.RecordSuccess(ctx, candidate, ref, nil); err == nil {
		t.Fatal("RecordSuccess 空 generation 读取失败必须报错")
	}
	if err := store.RecordFailure(ctx, candidate, "原因"); err == nil {
		t.Fatal("RecordFailure 空 generation 读取失败必须报错")
	}
	wfSpeedFirstSeedIndex(t, store, store.probeIndexKey(), []string{"v1:sys-1:strategy-1:group-1:acc-1"})
	if _, err := store.ListProbeCandidates(ctx, 1); err == nil {
		t.Fatal("ListProbeCandidates generation 读取失败必须报错")
	}
}

func TestWFSpeedFirstCancelledContextFailsFast(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	state := wfSpeedFirstState("")
	wfSpeedFirstEnsureGeneration(t, store, &state)
	wfSpeedFirstSeed(t, store, state)
	candidate := wfSpeedFirstCandidate(t, store, &state)
	cancel()
	// ctx 已取消：任何需要 Redis 的操作必须立即失败并保留 context.Canceled。
	if err := store.Discard(ctx, candidate); !errors.Is(err, context.Canceled) {
		t.Fatalf("Discard 取消后错误=%v，必须保留 context.Canceled", err)
	}
	if _, err := store.Defer(ctx, candidate); !errors.Is(err, context.Canceled) {
		t.Fatalf("Defer 取消后错误=%v，必须保留 context.Canceled", err)
	}
}

func TestWFSpeedFirstMutationLockHeldSkipsSilently(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()
	state := wfSpeedFirstState("")
	wfSpeedFirstEnsureGeneration(t, store, &state)
	key := wfSpeedFirstSeed(t, store, state)
	candidate := wfSpeedFirstCandidate(t, store, &state)

	// 预占 mutation 锁：等待循环耗尽后必须静默跳过（Node undefined 语义）。
	lockKey := store.mutationLockKey(candidate.StateKey)
	if ok, err := store.acquireLock(ctx, lockKey, "holder", time.Minute); err != nil || !ok {
		t.Fatalf("预占 mutation 锁失败 ok=%v err=%v", ok, err)
	}
	if err := store.Discard(ctx, candidate); err != nil {
		t.Fatalf("mutation 锁被占时 Discard 必须静默跳过: %v", err)
	}
	if found, _ := store.loadState(ctx, key, state.Generation); found == nil {
		t.Fatal("跳过时不得删除状态")
	}
}

func TestWFSpeedFirstWriteStateWithoutProbeIndex(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()
	state := wfSpeedFirstState("gen-1")
	key := stateKeyFor(state.Scope, state.RuntimeKey)

	// addProbeIndex=false 只登记 all-index（写探针侧状态时的语义分支）。
	if err := store.writeStateAndIndexes(ctx, key, state, time.Hour, false); err != nil {
		t.Fatalf("writeStateAndIndexes(false) 失败: %v", err)
	}
	allKeys, err := store.loadIndexKeys(ctx, store.allIndexKey())
	if err != nil || len(allKeys) != 1 || allKeys[0] != key {
		t.Fatalf("all-index=%v err=%v 必须包含状态键", allKeys, err)
	}
	probeKeys, err := store.loadIndexKeys(ctx, store.probeIndexKey())
	if err != nil || len(probeKeys) != 0 {
		t.Fatalf("probe-index=%v err=%v 必须保持为空", probeKeys, err)
	}
}
