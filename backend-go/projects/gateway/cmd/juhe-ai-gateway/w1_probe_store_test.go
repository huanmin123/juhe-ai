package main

// w1: chain_turn_probe_store.go 的内存探针状态存储单测。锁 Node
// MemoryRuntimeProbeStateStore 的语义与 merge/union 规则；时钟注入保证确定性。

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
)

func w1Ptr[T any](value T) *T {
	out := value
	return &out
}

func w1ProbeStore(now int64) *chainMemoryProbeStateStore {
	return newChainMemoryProbeStateStore(func() int64 { return now })
}

func TestW1ProbeStoreGetExpiresStaleEntries(t *testing.T) {
	store := w1ProbeStore(1_000)
	state := gatewaycircuit.ProbeState{RuntimeKey: "acc_1", Generation: 3, NextProbeAtMs: 500}
	if got, err := store.Get(context.Background(), "acc_1"); err != nil || got != nil {
		t.Fatalf("空存储 Get = %v, %v，want nil/nil", got, err)
	}
	ok, err := store.SetIfAbsent(context.Background(), state, 500)
	if err != nil || !ok {
		t.Fatalf("SetIfAbsent = %v, %v，want true/nil", ok, err)
	}
	got, err := store.Get(context.Background(), "acc_1")
	if err != nil || got == nil || got.Generation != 3 {
		t.Fatalf("Get 未命中存活条目: %v, %v", got, err)
	}
	store.nowMs = func() int64 { return 1_501 }
	if got, err := store.Get(context.Background(), "acc_1"); err != nil || got != nil {
		t.Fatalf("过期条目 Get = %v, %v，want nil/nil", got, err)
	}
	store.mu.Lock()
	_, exists := store.entries["acc_1"]
	store.mu.Unlock()
	if exists {
		t.Fatal("过期条目必须在读取时被删除")
	}
}

func TestW1ProbeStoreNextGenerationMonotonic(t *testing.T) {
	store := w1ProbeStore(0)
	ctx := context.Background()
	for want := int64(1); want <= 3; want++ {
		got, err := store.NextGeneration(ctx, "acc_gen", 999)
		if err != nil || got != want {
			t.Fatalf("NextGeneration = %d, %v，want %d/nil", got, err, want)
		}
	}
	if got, _ := store.NextGeneration(ctx, "acc_other", 0); got != 1 {
		t.Fatalf("其他 key 首代 = %d，want 1", got)
	}
}

func TestW1ProbeStoreSetIfAbsentHonorsExistingEntry(t *testing.T) {
	store := w1ProbeStore(100)
	ctx := context.Background()
	first := gatewaycircuit.ProbeState{RuntimeKey: "acc_1", Generation: 1}
	if ok, err := store.SetIfAbsent(ctx, first, 1_000); err != nil || !ok {
		t.Fatalf("首次 SetIfAbsent = %v, %v，want true/nil", ok, err)
	}
	second := gatewaycircuit.ProbeState{RuntimeKey: "acc_1", Generation: 2}
	if ok, err := store.SetIfAbsent(ctx, second, 1_000); err != nil || ok {
		t.Fatalf("重复 SetIfAbsent = %v, %v，want false/nil", ok, err)
	}
	// retentionMs 归一化：0 视为 1ms，时间推进后可再次占用。
	store.nowMs = func() int64 { return 1_100 }
	if ok, err := store.SetIfAbsent(ctx, second, 0); err != nil || !ok {
		t.Fatalf("过期后 SetIfAbsent = %v, %v，want true/nil", ok, err)
	}
	got, _ := store.Get(ctx, "acc_1")
	if got == nil || got.Generation != 2 {
		t.Fatalf("覆盖后的条目错误: %+v", got)
	}
}

func TestW1ProbeStoreMergePreservesAndUnions(t *testing.T) {
	store := w1ProbeStore(1_000)
	ctx := context.Background()
	stored := gatewaycircuit.ProbeState{
		RuntimeKey:    "acc_1",
		Generation:    4,
		ProbeRunID:    w1Ptr("run_1"),
		Outcome:       w1Ptr("healthy"),
		CompletedAtMs: w1Ptr(int64(900)),
		SourceFences:  []string{"fence-a", " fence-a ", "fence-b"},
		NextProbeAtMs: 700,
	}
	if ok, err := store.SetIfAbsent(ctx, stored, 10_000); err != nil || !ok {
		t.Fatalf("SetIfAbsent = %v, %v，want true/nil", ok, err)
	}
	incoming := gatewaycircuit.ProbeState{RuntimeKey: "acc_1", Generation: 99, SourceFences: []string{"fence-c", "fence-b"}}
	options := gatewaycircuit.ProbeMergeOptions{
		PreserveCurrentFields: []string{"probeRunId", "probeRunUntilMs", "outcome", "completedAtMs", "unknownField"},
		UnionArrayFields:      []gatewaycircuit.ProbeUnionArrayField{{Field: "sourceFences", MaxItems: 64}},
	}
	merged, err := store.Merge(ctx, incoming, 10_000, options)
	if err != nil || merged == nil {
		t.Fatalf("Merge = %v, %v，want non-nil/nil", merged, err)
	}
	if merged.Generation != 4 {
		t.Fatalf("merged.Generation = %d，want 存量 4", merged.Generation)
	}
	if merged.ProbeRunID == nil || *merged.ProbeRunID != "run_1" {
		t.Fatalf("merged.ProbeRunID = %v，want run_1", merged.ProbeRunID)
	}
	if merged.Outcome == nil || *merged.Outcome != "healthy" {
		t.Fatalf("merged.Outcome = %v，want healthy", merged.Outcome)
	}
	if merged.CompletedAtMs == nil || *merged.CompletedAtMs != 900 {
		t.Fatalf("merged.CompletedAtMs = %v，want 900", merged.CompletedAtMs)
	}
	wantFences := []string{"fence-a", "fence-b", "fence-c"}
	if len(merged.SourceFences) != len(wantFences) {
		t.Fatalf("merged.SourceFences = %v，want %v", merged.SourceFences, wantFences)
	}
	for index, fence := range wantFences {
		if merged.SourceFences[index] != fence {
			t.Fatalf("merged.SourceFences[%d] = %q，want %q", index, merged.SourceFences[index], fence)
		}
	}
	incomingAlone := gatewaycircuit.ProbeState{RuntimeKey: "acc_new", Generation: 7}
	mergedNew, err := store.Merge(ctx, incomingAlone, 10_000, options)
	if err != nil || mergedNew == nil || mergedNew.Generation != 7 {
		t.Fatalf("无存量 Merge = %+v, %v", mergedNew, err)
	}
}

func TestW1MergeProbeStateValuesNilInputs(t *testing.T) {
	options := gatewaycircuit.ProbeMergeOptions{}
	merged := chainMergeProbeStateValues(nil, nil, options)
	if merged.RuntimeKey != "" || merged.Generation != 0 {
		t.Fatalf("双 nil merge = %+v，want 零值", merged)
	}
	incoming := &gatewaycircuit.ProbeState{RuntimeKey: "acc_1", Generation: 2}
	merged = chainMergeProbeStateValues(nil, incoming, options)
	if merged.Generation != 2 {
		t.Fatalf("仅 incoming merge = %+v", merged)
	}
	current := &gatewaycircuit.ProbeState{RuntimeKey: "acc_1", Generation: 2, SourceFences: []string{"f1"}}
	merged = chainMergeProbeStateValues(current, nil, gatewaycircuit.ProbeMergeOptions{
		UnionArrayFields: []gatewaycircuit.ProbeUnionArrayField{{Field: "sourceFences", MaxItems: 64}},
	})
	if len(merged.SourceFences) != 1 || merged.SourceFences[0] != "f1" {
		t.Fatalf("incoming nil 并集 = %v，want [f1]", merged.SourceFences)
	}
}

func TestW1ProbeStoreAcquireGenerationRun(t *testing.T) {
	store := w1ProbeStore(1_000)
	ctx := context.Background()
	state := gatewaycircuit.ProbeState{RuntimeKey: "acc_1", Generation: 2, NextProbeAtMs: 300}
	if _, err := store.SetIfAbsent(ctx, state, 10_000); err != nil {
		t.Fatalf("SetIfAbsent: %v", err)
	}
	if got, err := store.AcquireGenerationRun(ctx, "acc_1", 3, "run_x", 1_500, 10_000); err != nil || got != nil {
		t.Fatalf("代数不匹配 Acquire = %v, %v，want nil/nil", got, err)
	}
	acquired, err := store.AcquireGenerationRun(ctx, "acc_1", 2, "run_1", 1_500, 10_000)
	if err != nil || acquired == nil {
		t.Fatalf("Acquire = %v, %v，want non-nil/nil", acquired, err)
	}
	if acquired.NextProbeAtMs != 1_500 || acquired.ProbeRunID == nil || *acquired.ProbeRunID != "run_1" {
		t.Fatalf("acquired = %+v", acquired)
	}
	if got, err := store.AcquireGenerationRun(ctx, "acc_1", 2, "run_2", 1_600, 10_000); err != nil || got != nil {
		t.Fatalf("租约冲突 Acquire = %v, %v，want nil/nil", got, err)
	}
	reentered, err := store.AcquireGenerationRun(ctx, "acc_1", 2, "run_1", 1_200, 10_000)
	if err != nil || reentered == nil || reentered.NextProbeAtMs != 1_500 {
		t.Fatalf("重入 Acquire = %+v, %v，want NextProbeAtMs=1500", reentered, err)
	}
	store.nowMs = func() int64 { return 1_600 }
	taken, err := store.AcquireGenerationRun(ctx, "acc_1", 2, "run_2", 2_000, 10_000)
	if err != nil || taken == nil || taken.ProbeRunID == nil || *taken.ProbeRunID != "run_2" {
		t.Fatalf("过期接管 Acquire = %+v, %v", taken, err)
	}
	if got, err := store.AcquireGenerationRun(ctx, "acc_missing", 1, "run_3", 2_000, 10_000); err != nil || got != nil {
		t.Fatalf("缺条目 Acquire = %v, %v，want nil/nil", got, err)
	}
}

func TestW1ProbeStoreCommitGenerationRun(t *testing.T) {
	store := w1ProbeStore(1_000)
	ctx := context.Background()
	state := gatewaycircuit.ProbeState{RuntimeKey: "acc_1", Generation: 5, SourceFences: []string{"fence-old"}}
	if _, err := store.SetIfAbsent(ctx, state, 10_000); err != nil {
		t.Fatalf("SetIfAbsent: %v", err)
	}
	if _, err := store.AcquireGenerationRun(ctx, "acc_1", 5, "run_1", 1_500, 10_000); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	next := gatewaycircuit.ProbeState{RuntimeKey: "acc_1", Generation: 5, SourceFences: []string{"fence-new"}}
	if ok, err := store.CommitGenerationRun(ctx, next, "run_other", 10_000); err != nil || ok {
		t.Fatalf("错误 owner Commit = %v, %v，want false/nil", ok, err)
	}
	ok, err := store.CommitGenerationRun(ctx, next, "run_1", 10_000)
	if err != nil || !ok {
		t.Fatalf("Commit = %v, %v，want true/nil", ok, err)
	}
	got, _ := store.Get(ctx, "acc_1")
	if got.ProbeRunID != nil || got.ProbeRunUntilMs != nil {
		t.Fatalf("提交后 run 事实未清空: %+v", got)
	}
	if len(got.SourceFences) != 2 || got.SourceFences[0] != "fence-old" || got.SourceFences[1] != "fence-new" {
		t.Fatalf("提交后 fences = %v，want [fence-old fence-new]", got.SourceFences)
	}
}

func TestW1ProbeStoreReplaceSettledGeneration(t *testing.T) {
	store := w1ProbeStore(1_000)
	ctx := context.Background()
	settled := gatewaycircuit.ProbeState{RuntimeKey: "acc_1", Generation: 5, Outcome: w1Ptr("healthy")}
	if _, err := store.SetIfAbsent(ctx, settled, 10_000); err != nil {
		t.Fatalf("SetIfAbsent: %v", err)
	}
	replacement := gatewaycircuit.ProbeState{RuntimeKey: "acc_1", Generation: 6}
	if got, err := store.ReplaceSettledGeneration(ctx, replacement, 4, 10_000); err != nil || got != nil {
		t.Fatalf("代数不匹配 Replace = %v, %v，want nil/nil", got, err)
	}
	previous, err := store.ReplaceSettledGeneration(ctx, replacement, 5, 10_000)
	if err != nil || previous == nil || previous.Generation != 5 {
		t.Fatalf("Replace = %v, %v，want 旧代 5", previous, err)
	}
	got, _ := store.Get(ctx, "acc_1")
	if got.Generation != 6 {
		t.Fatalf("替换后 Generation = %d，want 6", got.Generation)
	}
	pending := gatewaycircuit.ProbeState{RuntimeKey: "acc_2", Generation: 1}
	if _, err := store.SetIfAbsent(ctx, pending, 10_000); err != nil {
		t.Fatalf("SetIfAbsent acc_2: %v", err)
	}
	if got, err := store.ReplaceSettledGeneration(ctx, gatewaycircuit.ProbeState{RuntimeKey: "acc_2", Generation: 2}, 1, 10_000); err != nil || got != nil {
		t.Fatalf("未 settled Replace = %v, %v，want nil/nil", got, err)
	}
	withRun := gatewaycircuit.ProbeState{RuntimeKey: "acc_3", Generation: 1, Outcome: w1Ptr("healthy"), ProbeRunID: w1Ptr("run_1")}
	if _, err := store.SetIfAbsent(ctx, withRun, 10_000); err != nil {
		t.Fatalf("SetIfAbsent acc_3: %v", err)
	}
	if got, err := store.ReplaceSettledGeneration(ctx, gatewaycircuit.ProbeState{RuntimeKey: "acc_3", Generation: 2}, 1, 10_000); err != nil || got != nil {
		t.Fatalf("带 run Replace = %v, %v，want nil/nil", got, err)
	}
}

func TestW1UnionStringArraysDedupesAndBounds(t *testing.T) {
	got := chainUnionStringArrays([]string{" a ", "b", ""}, []string{"a", "b", "c"}, 64)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("union = %v，want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("union[%d] = %q，want %q", index, got[index], want[index])
		}
	}
	if got := chainUnionStringArrays([]string{"x", "y"}, nil, 0); len(got) != 1 || got[0] != "x" {
		t.Fatalf("bounded union = %v，want [x]", got)
	}
	if got := chainUnionStringArrays([]string{"\tz\t"}, nil, 4); len(got) != 1 || got[0] != "z" {
		t.Fatalf("tab trim = %v，want [z]", got)
	}
}

func TestW1NormalizedTtlMsAndTrim(t *testing.T) {
	if got := chainNormalizedTtlMs(0); got != 1 {
		t.Fatalf("ttl=0 归一化 = %d，want 1", got)
	}
	if got := chainNormalizedTtlMs(-5); got != 1 {
		t.Fatalf("ttl<0 归一化 = %d，want 1", got)
	}
	if got := chainNormalizedTtlMs(42); got != 42 {
		t.Fatalf("ttl=42 归一化 = %d，want 42", got)
	}
	for _, testCase := range []struct{ in, want string }{
		{"", ""},
		{"  ", ""},
		{" fence ", "fence"},
		{"\tfence\t", "fence"},
		{"fence", "fence"},
	} {
		if got := trimProbeFenceText(testCase.in); got != testCase.want {
			t.Fatalf("trim(%q) = %q，want %q", testCase.in, got, testCase.want)
		}
	}
}
