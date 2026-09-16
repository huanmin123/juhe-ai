package speedfirstrepo

// w12f_speedfirst_lockops_test.go 覆盖探测回调在 all-index 锁被占时的错误
// 传播臂（deleteStateAndIndexes / writeStateAndIndexes → withMutationLock →
// 各回调出口）、空 generation 回填的 Defer/RecordSuccess 分支、锁等待中途
// ctx 取消，以及候选扫描在 marker 加载失败时的错误传播。
// 占锁触发的 50 次重试每场景约 4 秒，全部场景合并到一个测试控制总耗时。

import (
	"context"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// w12fCandidateOfState 写入与候选匹配的状态并返回匹配候选。
func w12fCandidateOfState(t *testing.T, store *SpeedFirstStore, state speedFirstState, accountID string) (string, opsjobs.ProbeCandidate) {
	t.Helper()
	state.AccountID = accountID
	state.RuntimeKey = accountID
	key := wfSpeedFirstSeed(t, store, state)
	candidate := state.candidate()
	candidate.Generation = state.Generation
	return key, candidate
}

// TestW12fSpeedFirstAllIndexLockBusyPropagation 占住 all-index 锁后依次触发
// 各探测回调的写索引失败臂；每次失败都保留"索引锁获取失败"原始错误语义。
func TestW12fSpeedFirstAllIndexLockBusyPropagation(t *testing.T) {
	store, _ := newW12fSpeedFirstStore(t)
	ctx := context.Background()
	generation, err := store.LoadGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.client.SetNX(ctx, store.redisKey(store.indexLockKey(store.allIndexKey())), "w12f-holder", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	future := wfSpeedFirstBase.Add(10 * time.Minute).UnixMilli()
	past := wfSpeedFirstBase.Add(-time.Minute).UnixMilli()

	// 1) Defer：状态过期 → deleteStateAndIndexes → filterIndexKeys(all) 锁失败。
	state1 := wfSpeedFirstState(generation)
	pastUntil := past
	state1.DegradedUntilMS = &pastUntil
	_, candidate1 := w12fCandidateOfState(t, store, state1, "w12f-expired")
	if _, err := store.Defer(ctx, candidate1); err == nil {
		t.Fatal("all-index 锁被占时 Defer 过期删除必须报错")
	}

	// 2) RecordSuccess：状态过期 → 清除路径删除失败。
	state2 := wfSpeedFirstState(generation)
	futureUntil := future
	state2.DegradedUntilMS = &futureUntil
	_, candidate2 := w12fCandidateOfState(t, store, state2, "w12f-clear")
	if _, err := store.RecordSuccess(ctx, candidate2, opsjobs.ProbeAccountRef{AccountID: "w12f-clear"}, nil); err == nil {
		t.Fatal("all-index 锁被占时 RecordSuccess 清除必须报错")
	}

	// 3) RecordSuccess：双探针达成 → 清除路径删除失败。
	state3 := wfSpeedFirstState(generation)
	futureUntil3 := future
	state3.DegradedUntilMS = &futureUntil3
	state3.RecoveryProbeRoundAttemptCount = intPtr(1)
	state3.RecoveryProbeRoundSuccessCount = intPtr(1)
	_, candidate3 := w12fCandidateOfState(t, store, state3, "w12f-round")
	if _, err := store.RecordSuccess(ctx, candidate3, opsjobs.ProbeAccountRef{AccountID: "w12f-round"}, nil); err == nil {
		t.Fatal("all-index 锁被占时 RecordSuccess 双成功清除必须报错")
	}

	// 4) RecordSuccess：未达双成功 → 续降级写状态失败（writeStateAndIndexes）。
	state4 := wfSpeedFirstState(generation)
	futureUntil4 := future
	state4.DegradedUntilMS = &futureUntil4
	state4.RecoveryProbeRoundAttemptCount = intPtr(0)
	state4.RecoveryProbeRoundSuccessCount = intPtr(0)
	_, candidate4 := w12fCandidateOfState(t, store, state4, "w12f-continue")
	if _, err := store.RecordSuccess(ctx, candidate4, opsjobs.ProbeAccountRef{AccountID: "w12f-continue"}, nil); err == nil {
		t.Fatal("all-index 锁被占时 RecordSuccess 续降级必须报错")
	}

	// 5) Defer：状态未过期 → 顺延写状态失败（writeStateAndIndexes）。
	state5 := wfSpeedFirstState(generation)
	futureUntil5 := future
	state5.DegradedUntilMS = &futureUntil5
	_, candidate5 := w12fCandidateOfState(t, store, state5, "w12f-defer")
	if _, err := store.Defer(ctx, candidate5); err == nil {
		t.Fatal("all-index 锁被占时 Defer 顺延写状态必须报错")
	}

	// 6) RecordFailure：状态未过期 → 续降级写状态失败。
	state6 := wfSpeedFirstState(generation)
	futureUntil6 := future
	state6.DegradedUntilMS = &futureUntil6
	_, candidate6 := w12fCandidateOfState(t, store, state6, "w12f-failure")
	if err := store.RecordFailure(ctx, candidate6, "w12f-reason"); err == nil {
		t.Fatal("all-index 锁被占时 RecordFailure 续降级必须报错")
	}
}

// TestW12fSpeedFirstEmptyGenerationDeferAndRecordSuccess 覆盖 Defer 与
// RecordSuccess 在候选 generation 为空时的 marker 回填分支（正常完成路径）。
func TestW12fSpeedFirstEmptyGenerationDeferAndRecordSuccess(t *testing.T) {
	store, _ := newW12fSpeedFirstStore(t)
	ctx := context.Background()
	generation, err := store.LoadGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Defer 空代回填：顺延成功。
	state := wfSpeedFirstState(generation)
	_, candidate := w12fCandidateOfState(t, store, state, "w12f-backfill")
	candidate.Generation = ""
	applied, err := store.Defer(ctx, candidate)
	if err != nil || !applied {
		t.Fatalf("空 generation Defer 必须顺延成功 applied=%v err=%v", applied, err)
	}

	// RecordSuccess 空代回填：独立状态 + 单次成功不清除。
	state2 := wfSpeedFirstState(generation)
	_, candidate2 := w12fCandidateOfState(t, store, state2, "w12f-backfill-2")
	candidate2.Generation = ""
	result, err := store.RecordSuccess(ctx, candidate2, opsjobs.ProbeAccountRef{AccountID: "w12f-backfill-2"}, nil)
	if err != nil {
		t.Fatalf("空 generation RecordSuccess 失败: %v", err)
	}
	if result.Cleared {
		t.Fatal("单次成功不得清除降级状态")
	}
	if result.RecoverySuccessCount != 1 || result.RequiredRecoverySuccessCount != speedFirstRecoveryRoundSize {
		t.Fatalf("成功计数=%+v", result)
	}
}

// TestW12fSpeedFirstListCandidatesGenerationError 覆盖候选扫描在 probe 索引
// 有键但 generation marker 加载失败时的错误传播。
func TestW12fSpeedFirstListCandidatesGenerationError(t *testing.T) {
	store, _ := newW12fSpeedFirstStore(t)
	ctx := context.Background()
	generation, err := store.LoadGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state := wfSpeedFirstState(generation)
	key, _ := w12fCandidateOfState(t, store, state, "w12f-scan")
	if err := store.setJSON(ctx, store.probeIndexKey(), map[string]any{"keys": []string{key}}, time.Hour); err != nil {
		t.Fatal(err)
	}
	// marker 写成合法 JSON 但语义损坏 → LoadGeneration 报错。
	if err := store.setJSON(ctx, store.generationKey(), map[string]any{"publishedAt": "not-a-time"}, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListProbeCandidates(ctx, 5); err == nil {
		t.Fatal("marker 损坏时 ListProbeCandidates 必须报错")
	}
}

// TestW12fSpeedFirstLockWaitCancelDuringRetry 覆盖锁等待循环中途 ctx 取消
// 的 select Done 臂（acquireIndexLock 与 withMutationLock 各一次）。
func TestW12fSpeedFirstLockWaitCancelDuringRetry(t *testing.T) {
	store, _ := newW12fSpeedFirstStore(t)
	bg := context.Background()
	generation, err := store.LoadGeneration(bg)
	if err != nil {
		t.Fatal(err)
	}
	// 占住 all-index 锁，30ms 后取消 ctx：第一次等待走 time.After，
	// 第二次等待时 Done 已就绪且 After 未到 → 走 ctx.Done 臂。
	if err := store.client.SetNX(bg, store.redisKey(store.indexLockKey(store.allIndexKey())), "w12f-holder", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	state := wfSpeedFirstState(generation)
	_, candidate := w12fCandidateOfState(t, store, state, "w12f-cancel")
	ctx, cancel := context.WithCancel(bg)
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	if _, err := store.Defer(ctx, candidate); err == nil {
		t.Fatal("等待期间取消 ctx 必须报错")
	}
}
