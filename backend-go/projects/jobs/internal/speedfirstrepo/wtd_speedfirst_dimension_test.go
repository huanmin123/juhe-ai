package speedfirstrepo

// wtd 覆盖波次：speedFirstState 维度（total_time）同步与探针恢复过滤
// （设计 6.5）——total_time 降级不探、不清理、不续租；存量无 dimension 字段
// 的状态兼容读作 first_byte 并照常进入探针候选。复用 wf 波次的 miniredis
// 助手，固定时钟/随机保证确定性。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

func wtdTotalTimeState(generation string) speedFirstState {
	state := wfSpeedFirstState(generation)
	state.Dimension = speedFirstDimensionTotalTime
	return state
}

// wtdStateJSON 读取当前存储 state 并序列化，用于前后逐字节对比。
func wtdStateJSON(t *testing.T, store *SpeedFirstStore, key, generation string) string {
	t.Helper()
	state, err := store.loadState(context.Background(), key, generation)
	if err != nil {
		t.Fatalf("读取状态失败: %v", err)
	}
	if state == nil {
		t.Fatalf("状态不存在：%s", key)
	}
	encoded, err := json.Marshal(*state)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// RecordSuccess 遇 total_time 降级：严格 no-op——不清理、不推进轮次、返回
// 零值投影（设计 6.5，候选过滤后的防御性兜底）。
func TestWtdRepoRecordSuccessSkipsTotalTimeState(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()
	ref := opsjobs.ProbeAccountRef{AccountID: "acc-1"}
	state := wtdTotalTimeState("")
	wfSpeedFirstEnsureGeneration(t, store, &state)
	key := wfSpeedFirstSeed(t, store, state)
	candidate := wfSpeedFirstCandidate(t, store, &state)
	before := wtdStateJSON(t, store, key, state.Generation)

	result, err := store.RecordSuccess(ctx, candidate, ref, nil)
	if err != nil {
		t.Fatalf("total_time RecordSuccess 失败: %v", err)
	}
	if result != (opsjobs.SpeedFirstRecoveryResult{}) {
		t.Fatalf("total_time 状态的探针成功必须返回零值投影=%+v", result)
	}
	if after := wtdStateJSON(t, store, key, state.Generation); after != before {
		t.Fatalf("状态不得变化：before=%s after=%s", before, after)
	}
}

// RecordFailure 遇 total_time 降级：不续租、不覆写 Reason、不动轮次。
func TestWtdRepoRecordFailureSkipsTotalTimeState(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()
	state := wtdTotalTimeState("")
	wfSpeedFirstEnsureGeneration(t, store, &state)
	key := wfSpeedFirstSeed(t, store, state)
	candidate := wfSpeedFirstCandidate(t, store, &state)
	before := wtdStateJSON(t, store, key, state.Generation)

	if err := store.RecordFailure(ctx, candidate, "探针失败-不得覆写"); err != nil {
		t.Fatalf("total_time RecordFailure 失败: %v", err)
	}
	if after := wtdStateJSON(t, store, key, state.Generation); after != before {
		t.Fatalf("状态不得变化：before=%s after=%s", before, after)
	}
}

// total_time 降级已过期时，探针成功/失败同样不得清理（不清理语义与维度过
// 滤优先于过期兜底；过期状态由 Redis TTL 自然回收）。
func TestWtdRepoTotalTimeExpiredStateNotTouchedByProbe(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()
	ref := opsjobs.ProbeAccountRef{AccountID: "acc-1"}
	state := wtdTotalTimeState("")
	wfSpeedFirstEnsureGeneration(t, store, &state)
	expired := wfSpeedFirstBase.Add(-time.Second).UnixMilli()
	state.DegradedUntilMS = &expired
	key := wfSpeedFirstSeed(t, store, state)
	candidate := wfSpeedFirstCandidate(t, store, &state)
	before := wtdStateJSON(t, store, key, state.Generation)

	if _, err := store.RecordSuccess(ctx, candidate, ref, nil); err != nil {
		t.Fatalf("过期 total_time RecordSuccess 失败: %v", err)
	}
	if err := store.RecordFailure(ctx, candidate, "探针失败"); err != nil {
		t.Fatalf("过期 total_time RecordFailure 失败: %v", err)
	}
	if after := wtdStateJSON(t, store, key, state.Generation); after != before {
		t.Fatalf("过期 total_time 状态不得被探针触碰：before=%s after=%s", before, after)
	}
}

// 候选扫描：total_time 降级被过滤，first_byte 降级照常返回。
func TestWtdRepoListProbeCandidatesSkipsTotalTime(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()
	due := wfSpeedFirstBase.Add(-time.Millisecond).UnixMilli()

	firstByte := wfSpeedFirstState("")
	wfSpeedFirstEnsureGeneration(t, store, &firstByte)
	firstByte.NextProbeAtMS = &due
	firstByteKey := wfSpeedFirstSeed(t, store, firstByte)

	totalTime := wtdTotalTimeState(firstByte.Generation)
	totalTime.AccountID = "acc-tt"
	totalTime.RuntimeKey = "acc-tt"
	totalTime.NextProbeAtMS = &due
	totalTimeKey := wfSpeedFirstSeed(t, store, totalTime)

	wfSpeedFirstSeedIndex(t, store, store.probeIndexKey(), []string{firstByteKey, totalTimeKey})

	candidates, err := store.ListProbeCandidates(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].AccountID != "acc-1" {
		t.Fatalf("候选必须只含 first_byte 降级=%+v", candidates)
	}
}

// 存量兼容：无 dimension 字段的 JSON 载荷视作 first_byte，探针候选照常。
func TestWtdRepoLegacyStateReadsAsFirstByte(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()
	state := wfSpeedFirstState("")
	wfSpeedFirstEnsureGeneration(t, store, &state)
	due := wfSpeedFirstBase.Add(-time.Millisecond).UnixMilli()
	state.NextProbeAtMS = &due

	// Dimension 零值 + omitempty：序列化结果即存量（无 dimension 键）载荷。
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"dimension"`) {
		t.Fatalf("存量载荷不得包含 dimension 键=%s", encoded)
	}
	if state.dimension() != "first_byte" {
		t.Fatalf("存量维度必须读作 first_byte=%q", state.dimension())
	}

	key := stateKeyFor(state.Scope, state.RuntimeKey)
	if err := store.setJSON(ctx, key, json.RawMessage(encoded), time.Hour); err != nil {
		t.Fatalf("写入存量载荷失败: %v", err)
	}
	wfSpeedFirstSeedIndex(t, store, store.probeIndexKey(), []string{key})
	candidates, err := store.ListProbeCandidates(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].AccountID != "acc-1" {
		t.Fatalf("存量状态必须照常进入探针候选=%+v", candidates)
	}
}
