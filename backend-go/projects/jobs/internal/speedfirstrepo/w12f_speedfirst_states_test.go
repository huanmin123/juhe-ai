package speedfirstrepo

// w12f_speedfirst_states_test.go 覆盖 Discard/Defer/RecordSuccess/RecordFailure
// 状态机的剩余分支：generation 为空时的加载回填、状态缺失时的静默返回、
// generation marker 异常（损坏删除 / 规范化失败）在 mutation 锁内的传播，
// 以及 passiveJitterWindowMS 的窗口钳制分支。

import (
	"context"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// w12fCandidateOf 以指定 AccountID 生成与 state 匹配的候选。
func w12fCandidateOf(t *testing.T, store *SpeedFirstStore, state speedFirstState, accountID string) opsjobs.ProbeCandidate {
	t.Helper()
	if accountID != "" {
		state.AccountID = accountID
		state.RuntimeKey = accountID
	}
	candidate := state.candidate()
	candidate.Generation = state.Generation
	return candidate
}

// TestW12fSpeedFirstMissingStateSilentReturns 覆盖状态不存在时三个探测回调
// 的静默分支（withMutationLock 内 current == nil → 原样返回）。
func TestW12fSpeedFirstMissingStateSilentReturns(t *testing.T) {
	store, _ := newW12fSpeedFirstStore(t)
	ctx := context.Background()
	generation, err := store.LoadGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state := wfSpeedFirstState(generation)
	candidate := w12fCandidateOf(t, store, state, "w12f-missing")

	if err := store.Discard(ctx, candidate); err != nil {
		t.Fatalf("缺失状态 Discard 必须静默: %v", err)
	}
	if _, err := store.Defer(ctx, candidate); err != nil {
		t.Fatalf("缺失状态 Defer 必须静默: %v", err)
	}
	if _, err := store.RecordSuccess(ctx, candidate, opsjobs.ProbeAccountRef{AccountID: candidate.AccountID}, nil); err != nil {
		t.Fatalf("缺失状态 RecordSuccess 必须静默: %v", err)
	}
	if err := store.RecordFailure(ctx, candidate, "w12f-reason"); err != nil {
		t.Fatalf("缺失状态 RecordFailure 必须静默: %v", err)
	}
}

// TestW12fSpeedFirstEmptyGenerationBackfill 覆盖候选 generation 为空时从
// generation marker 回填的分支（Discard/RecordFailure）。
func TestW12fSpeedFirstEmptyGenerationBackfill(t *testing.T) {
	store, _ := newW12fSpeedFirstStore(t)
	ctx := context.Background()
	// 状态使用真实 generation 写入，候选不携带 generation → 回填后命中围栏。
	generation, err := store.LoadGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state := wfSpeedFirstState(generation)
	key := wfSpeedFirstSeed(t, store, state)

	candidate := state.candidate()
	candidate.Generation = ""
	if err := store.Discard(ctx, candidate); err != nil {
		t.Fatalf("空 generation Discard 失败: %v", err)
	}
	// Discard 命中围栏后状态被删除，键不存在。
	if found, err := store.loadState(ctx, key, generation); err != nil || found != nil {
		t.Fatalf("Discard 后状态必须已删除 found=%v err=%v", found, err)
	}

	// RecordFailure 空 generation 回填：状态不存在 → 静默。
	state2 := wfSpeedFirstState(generation)
	candidate2 := w12fCandidateOf(t, store, state2, "w12f-backfill-2")
	candidate2.Generation = ""
	if err := store.RecordFailure(ctx, candidate2, "w12f-reason"); err != nil {
		t.Fatalf("空 generation RecordFailure 失败: %v", err)
	}
}

// TestW12fSpeedFirstMutationLockGenerationArms 覆盖 mutation 锁内 generation
// marker 的三类分支：marker 被删（renew 失败静默跳过）、marker 语义损坏
// （renew 错误传播）、marker 正常但状态 JSON 损坏（静默跳过）。
func TestW12fSpeedFirstMutationLockGenerationArms(t *testing.T) {
	store, server := newW12fSpeedFirstStore(t)
	ctx := context.Background()
	generation, err := store.LoadGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state := wfSpeedFirstState(generation)
	key := wfSpeedFirstSeed(t, store, state)
	candidate := state.candidate()
	candidate.Generation = generation

	// 1) generation marker 被删 → renewGeneration false → 静默跳过。
	if !server.Del(store.redisKey(store.generationKey())) {
		t.Fatal("generation marker 必须存在且被删除")
	}
	if _, err := store.Defer(ctx, candidate); err != nil {
		t.Fatalf("marker 删除后 Defer 必须静默跳过: %v", err)
	}

	// 2) marker 为合法 JSON 但语义损坏（缺 version）→ renewGeneration 报错传播。
	if err := store.setJSON(ctx, store.generationKey(), map[string]any{"publishedAt": "not-a-time"}, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := store.Discard(ctx, candidate); err == nil {
		t.Fatal("marker 语义损坏时 Discard 必须报错")
	}
	result, err := store.RecordSuccess(ctx, candidate, opsjobs.ProbeAccountRef{AccountID: candidate.AccountID}, nil)
	if err == nil {
		t.Fatal("marker 语义损坏时 RecordSuccess 必须报错")
	}
	if result.Cleared {
		t.Fatal("失败路径不得产生清除结果")
	}

	// 3) marker 正常、状态 JSON 非法（损坏即删）→ loadState 找不到 → 静默。
	if err := store.setJSON(ctx, store.generationKey(), speedFirstInitialGenerationEvent, speedFirstGenerationTTL); err != nil {
		t.Fatal(err)
	}
	if err := server.Set(key, "{not-json"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordFailure(ctx, candidate, "w12f-reason"); err != nil {
		t.Fatalf("状态 JSON 损坏时 RecordFailure 必须静默: %v", err)
	}
}

// TestW12fSpeedFirstMutationLockCtxCanceled 覆盖 mutation 锁等待在 ctx 取消
// 时的退出臂。
func TestW12fSpeedFirstMutationLockCtxCanceled(t *testing.T) {
	store, _ := newW12fSpeedFirstStore(t)
	bg := context.Background()
	generation, err := store.LoadGeneration(bg)
	if err != nil {
		t.Fatal(err)
	}
	state := wfSpeedFirstState(generation)
	key := wfSpeedFirstSeed(t, store, state)
	candidate := state.candidate()
	candidate.Generation = generation
	// 预占 mutation 锁，ctx 取消后 withMutationLock 等待立即退出。
	if err := store.client.SetNX(bg, store.redisKey(store.mutationLockKey(key)), "w12f-holder", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(bg)
	cancel()
	defer cancel()
	if _, err := store.Defer(ctx, candidate); err == nil {
		t.Fatal("ctx 取消时 Defer 必须报错")
	}
}

// TestW12fPassiveJitterWindowClamp 覆盖 passiveJitterWindowMS 的窗口钳制
// 分支（半窗口小于 30 分钟时窗口钳到半窗口）。
func TestW12fPassiveJitterWindowClamp(t *testing.T) {
	// interval 2 分钟（< 60 分钟）：窗口 30 秒。
	if got := passiveJitterWindowMS(120_000); got != 30_000 {
		t.Fatalf("interval=120s 窗口=%d, want 30000", got)
	}
	// interval 30 秒：half=15s → 窗口=15s。
	if got := passiveJitterWindowMS(30_000); got != 15_000 {
		t.Fatalf("interval=30s 窗口=%d, want 15000", got)
	}
	// interval 0 → 视作 1ms → 窗口 0。
	if got := passiveJitterWindowMS(0); got != 0 {
		t.Fatalf("interval=0 窗口=%d, want 0", got)
	}
}
