package main

// chain_turn_probe_store.go 内存 probe 状态 store 的补充单元测试（w1g 前缀，
// TestW1G 入口）。用可控 nowMs 时钟保证确定性，不涉及并发等待；
// 覆盖 get/setIfAbsent/merge/acquire/commit/replace/nextGeneration 全分支与
// chainMergeProbeStateValues、chainUnionStringArrays、trimProbeFenceText、
// chainNormalizedTtlMs 纯 helper。

import (
	"context"
	"reflect"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
)

// w1gProbeClock 返回可控时钟与其读写句柄。
func w1gProbeClock(start int64) (func() int64, func(int64)) {
	now := start
	return func() int64 { return now }, func(value int64) { now = value }
}

func w1gProbeState(runtimeKey string) gatewaycircuit.ProbeState {
	return gatewaycircuit.ProbeState{
		RuntimeKey:    runtimeKey,
		Generation:    1,
		NextProbeAtMs: 100,
		ProbeKind:     "availability",
	}
}

func w1gStringPtr(value string) *string { return &value }

func w1gInt64Ptr2(value int64) *int64 { return &value }

func TestW1GChainNormalizedTtlMs(t *testing.T) {
	cases := []struct {
		ttl  int64
		want int64
	}{
		{0, 1},
		{-5, 1},
		{100, 100},
	}
	for _, testCase := range cases {
		if got := chainNormalizedTtlMs(testCase.ttl); got != testCase.want {
			t.Fatalf("chainNormalizedTtlMs(%d) = %d, want %d", testCase.ttl, got, testCase.want)
		}
	}
}

func TestW1GTrimProbeFenceText(t *testing.T) {
	cases := []struct{ value, want string }{
		{"  fence  ", "fence"},
		{"\t fence \t", "fence"},
		{"", ""},
		{"   ", ""},
		{"a b", "a b"},
		{"\tf", "f"},
	}
	for _, testCase := range cases {
		if got := trimProbeFenceText(testCase.value); got != testCase.want {
			t.Fatalf("trimProbeFenceText(%q) = %q, want %q", testCase.value, got, testCase.want)
		}
	}
}

func TestW1GChainUnionStringArrays(t *testing.T) {
	t.Run("first priority with trim dedupe", func(t *testing.T) {
		got := chainUnionStringArrays([]string{"a", " b "}, []string{"b", "c"}, 64)
		want := []string{"a", "b", "c"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("chainUnionStringArrays = %#v, want %#v", got, want)
		}
	})

	t.Run("blank items skipped", func(t *testing.T) {
		got := chainUnionStringArrays([]string{"  "}, nil, 64)
		if len(got) != 0 {
			t.Fatalf("chainUnionStringArrays = %#v, want 空", got)
		}
	})

	t.Run("bounded by max items", func(t *testing.T) {
		got := chainUnionStringArrays([]string{"a", "b", "c"}, []string{"d"}, 2)
		want := []string{"a", "b"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("chainUnionStringArrays = %#v, want %#v", got, want)
		}
	})

	t.Run("max items below one clamps to one", func(t *testing.T) {
		got := chainUnionStringArrays([]string{"x", "y"}, nil, 0)
		want := []string{"x"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("chainUnionStringArrays = %#v, want %#v", got, want)
		}
	})

	t.Run("nil inputs yield empty non nil slice", func(t *testing.T) {
		got := chainUnionStringArrays(nil, nil, 8)
		if got == nil || len(got) != 0 {
			t.Fatalf("chainUnionStringArrays = %#v, want 空 slice", got)
		}
	})
}

func TestW1GIncomingSourceFences(t *testing.T) {
	if incomingSourceFences(nil) != nil {
		t.Fatal("nil state 必须返回 nil")
	}
	state := &gatewaycircuit.ProbeState{SourceFences: []string{"f1"}}
	if got := incomingSourceFences(state); !reflect.DeepEqual(got, []string{"f1"}) {
		t.Fatalf("incomingSourceFences = %#v", got)
	}
}

func TestW1GChainMemoryProbeStateStoreGetAndSetIfAbsent(t *testing.T) {
	nowMs, setNow := w1gProbeClock(1000)
	store := newChainMemoryProbeStateStore(nowMs)
	ctx := context.Background()

	t.Run("get missing key", func(t *testing.T) {
		state, err := store.Get(ctx, "missing")
		if err != nil || state != nil {
			t.Fatalf("Get = %#v/%v, want nil/nil", state, err)
		}
	})

	t.Run("set if absent then duplicate fails", func(t *testing.T) {
		state := w1gProbeState("k1")
		ok, err := store.SetIfAbsent(ctx, state, 1000)
		if err != nil || !ok {
			t.Fatalf("SetIfAbsent = %v/%v, want true/nil", ok, err)
		}
		ok, err = store.SetIfAbsent(ctx, w1gProbeState("k1"), 1000)
		if err != nil || ok {
			t.Fatalf("重复 SetIfAbsent = %v/%v, want false/nil", ok, err)
		}
		got, err := store.Get(ctx, "k1")
		if err != nil || got == nil || got.NextProbeAtMs != 100 || got.Generation != 1 {
			t.Fatalf("Get = %#v/%v", got, err)
		}
	})

	t.Run("expiry deletes on read", func(t *testing.T) {
		state := w1gProbeState("k2")
		if ok, _ := store.SetIfAbsent(ctx, state, 500); !ok {
			t.Fatal("首次 SetIfAbsent 必须成功")
		}
		setNow(1500)
		if got, _ := store.Get(ctx, "k2"); got != nil {
			t.Fatalf("过期 entry 必须读为 nil, got %#v", got)
		}
		ok, err := store.SetIfAbsent(ctx, w1gProbeState("k2"), 500)
		if err != nil || !ok {
			t.Fatalf("过期后 SetIfAbsent = %v/%v, want true/nil", ok, err)
		}
	})

	t.Run("ttl normalized to at least one ms", func(t *testing.T) {
		if ok, _ := store.SetIfAbsent(ctx, w1gProbeState("k3"), 0); !ok {
			t.Fatal("SetIfAbsent 必须成功")
		}
		setNow(1500)
		if got, _ := store.Get(ctx, "k3"); got == nil {
			t.Fatal("ttl=0 归一为 1ms 后在同一毫秒仍应可读")
		}
		setNow(1501)
		if got, _ := store.Get(ctx, "k3"); got != nil {
			t.Fatal("1ms 后 entry 必须过期")
		}
	})
}

func TestW1GChainMemoryProbeStateStoreNextGeneration(t *testing.T) {
	nowMs, _ := w1gProbeClock(0)
	store := newChainMemoryProbeStateStore(nowMs)
	ctx := context.Background()
	for want := int64(1); want <= 3; want++ {
		got, err := store.NextGeneration(ctx, "runtime-a", 0)
		if err != nil || got != want {
			t.Fatalf("NextGeneration = %d/%v, want %d/nil", got, err, want)
		}
	}
	if got, _ := store.NextGeneration(ctx, "runtime-b", 0); got != 1 {
		t.Fatalf("独立 key 计数 = %d, want 1", got)
	}
}

func w1gCoordinatorMergeOptions() gatewaycircuit.ProbeMergeOptions {
	return gatewaycircuit.ProbeMergeOptions{
		PreserveCurrentFields: []string{"probeRunId", "probeRunUntilMs", "outcome", "completedAtMs"},
		UnionArrayFields:      []gatewaycircuit.ProbeUnionArrayField{{Field: "sourceFences", MaxItems: 64}},
	}
}

func TestW1GChainMergeProbeStateValues(t *testing.T) {
	options := w1gCoordinatorMergeOptions()

	t.Run("nil incoming yields zero value", func(t *testing.T) {
		merged := chainMergeProbeStateValues(nil, nil, options)
		if merged.Generation != 0 || merged.RuntimeKey != "" {
			t.Fatalf("merged = %#v", merged)
		}
	})

	t.Run("incoming spreads over current with preserved fields", func(t *testing.T) {
		current := &gatewaycircuit.ProbeState{
			RuntimeKey:      "k",
			Generation:      3,
			NextProbeAtMs:   10,
			ProbeRunID:      w1gStringPtr("run-1"),
			ProbeRunUntilMs: w1gInt64Ptr2(500),
			Outcome:         w1gStringPtr("healthy"),
			CompletedAtMs:   w1gInt64Ptr2(42),
			SourceFences:    []string{"f1", "f2"},
		}
		incoming := &gatewaycircuit.ProbeState{
			RuntimeKey:      "k",
			NextProbeAtMs:   999,
			ProbeRunID:      w1gStringPtr("run-2"),
			ProbeRunUntilMs: w1gInt64Ptr2(777),
			SourceFences:    []string{"f2", " f3 "},
		}
		merged := chainMergeProbeStateValues(current, incoming, options)
		if merged.Generation != 3 {
			t.Fatalf("generation = %d, want 3（保持 stored）", merged.Generation)
		}
		if merged.ProbeRunID == nil || *merged.ProbeRunID != "run-1" {
			t.Fatalf("probeRunId = %v, want run-1（preserve）", merged.ProbeRunID)
		}
		if merged.ProbeRunUntilMs == nil || *merged.ProbeRunUntilMs != 500 {
			t.Fatalf("probeRunUntilMs = %v, want 500", merged.ProbeRunUntilMs)
		}
		if merged.Outcome == nil || *merged.Outcome != "healthy" {
			t.Fatalf("outcome = %v, want healthy", merged.Outcome)
		}
		if merged.CompletedAtMs == nil || *merged.CompletedAtMs != 42 {
			t.Fatalf("completedAtMs = %v, want 42", merged.CompletedAtMs)
		}
		if merged.NextProbeAtMs != 999 {
			t.Fatalf("nextProbeAtMs = %d, want 999（incoming spread）", merged.NextProbeAtMs)
		}
		wantFences := []string{"f1", "f2", "f3"}
		if !reflect.DeepEqual(merged.SourceFences, wantFences) {
			t.Fatalf("sourceFences = %#v, want %#v", merged.SourceFences, wantFences)
		}
	})

	t.Run("nil current preserved fields fall back to incoming", func(t *testing.T) {
		current := &gatewaycircuit.ProbeState{RuntimeKey: "k", Generation: 0, SourceFences: []string{"f1"}}
		incoming := &gatewaycircuit.ProbeState{RuntimeKey: "k", Generation: 2, ProbeRunID: w1gStringPtr("run-9")}
		merged := chainMergeProbeStateValues(current, incoming, options)
		if merged.Generation != 2 {
			t.Fatalf("generation = %d, want 2（current 0 视为缺省）", merged.Generation)
		}
		if merged.ProbeRunID == nil || *merged.ProbeRunID != "run-9" {
			t.Fatalf("probeRunId = %v, want run-9", merged.ProbeRunID)
		}
		if !reflect.DeepEqual(merged.SourceFences, []string{"f1"}) {
			t.Fatalf("sourceFences = %#v, want [f1]", merged.SourceFences)
		}
	})

	t.Run("no union option keeps incoming fences", func(t *testing.T) {
		current := &gatewaycircuit.ProbeState{RuntimeKey: "k", Generation: 1, SourceFences: []string{"stored"}}
		incoming := &gatewaycircuit.ProbeState{RuntimeKey: "k", SourceFences: []string{"incoming"}}
		merged := chainMergeProbeStateValues(current, incoming, gatewaycircuit.ProbeMergeOptions{})
		if !reflect.DeepEqual(merged.SourceFences, []string{"incoming"}) {
			t.Fatalf("sourceFences = %#v, want [incoming]", merged.SourceFences)
		}
	})
}

func TestW1GChainMemoryProbeStateStoreMerge(t *testing.T) {
	nowMs, setNow := w1gProbeClock(2000)
	store := newChainMemoryProbeStateStore(nowMs)
	ctx := context.Background()
	options := w1gCoordinatorMergeOptions()

	t.Run("merge onto missing entry stores incoming", func(t *testing.T) {
		merged, err := store.Merge(ctx, w1gProbeState("m1"), 1000, options)
		if err != nil || merged == nil || merged.Generation != 1 {
			t.Fatalf("Merge = %#v/%v", merged, err)
		}
	})

	t.Run("merge unions fences and preserves stored run facts", func(t *testing.T) {
		base := w1gProbeState("m2")
		base.ProbeRunID = w1gStringPtr("run-a")
		if _, err := store.Merge(ctx, base, 1000, options); err != nil {
			t.Fatalf("seed merge: %v", err)
		}
		setNow(2100)
		incoming := w1gProbeState("m2")
		incoming.Generation = 9
		incoming.SourceFences = []string{" fence-2 "}
		merged, err := store.Merge(ctx, incoming, 1000, options)
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		if merged.Generation != 1 {
			t.Fatalf("generation = %d, want 1", merged.Generation)
		}
		if merged.ProbeRunID == nil || *merged.ProbeRunID != "run-a" {
			t.Fatalf("probeRunId = %v, want run-a", merged.ProbeRunID)
		}
		wantFences := []string{"fence-2"}
		if !reflect.DeepEqual(merged.SourceFences, wantFences) {
			t.Fatalf("sourceFences = %#v, want %#v", merged.SourceFences, wantFences)
		}
	})

	t.Run("expired entry merges as bare incoming", func(t *testing.T) {
		base := w1gProbeState("m3")
		base.Generation = 5
		if _, err := store.Merge(ctx, base, 1, options); err != nil {
			t.Fatalf("seed merge: %v", err)
		}
		setNow(5000)
		merged, err := store.Merge(ctx, w1gProbeState("m3"), 1000, options)
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		if merged.Generation != 1 {
			t.Fatalf("generation = %d, want 1（过期 entry 不保留 generation）", merged.Generation)
		}
	})
}

func TestW1GChainMemoryProbeStateStoreAcquireGenerationRun(t *testing.T) {
	nowMs, _ := w1gProbeClock(3000)
	store := newChainMemoryProbeStateStore(nowMs)
	ctx := context.Background()

	t.Run("acquire on missing key fails", func(t *testing.T) {
		got, err := store.AcquireGenerationRun(ctx, "nope", 1, "run-1", 3100, 1000)
		if err != nil || got != nil {
			t.Fatalf("AcquireGenerationRun = %#v/%v, want nil/nil", got, err)
		}
	})

	t.Run("generation mismatch fails", func(t *testing.T) {
		if ok, _ := store.SetIfAbsent(ctx, w1gProbeState("a1"), 10000); !ok {
			t.Fatal("seed 失败")
		}
		got, err := store.AcquireGenerationRun(ctx, "a1", 99, "run-1", 3100, 10000)
		if err != nil || got != nil {
			t.Fatalf("generation 不匹配 = %#v/%v, want nil/nil", got, err)
		}
	})

	t.Run("active foreign run blocks acquisition", func(t *testing.T) {
		if ok, _ := store.SetIfAbsent(ctx, w1gProbeState("a2"), 10000); !ok {
			t.Fatal("seed 失败")
		}
		seed, _ := store.Get(ctx, "a2")
		seed.ProbeRunID = w1gStringPtr("run-owner")
		seed.ProbeRunUntilMs = w1gInt64Ptr2(3500)
		if _, err := store.Merge(ctx, *seed, 10000, w1gCoordinatorMergeOptions()); err != nil {
			t.Fatalf("seed run: %v", err)
		}
		got, err := store.AcquireGenerationRun(ctx, "a2", 1, "run-other", 3600, 10000)
		if err != nil || got != nil {
			t.Fatalf("活跃 run 阻断 = %#v/%v, want nil/nil", got, err)
		}
		// 同 run 重入成功，NextProbeAtMs 抬到 runUntil。
		got, err = store.AcquireGenerationRun(ctx, "a2", 1, "run-owner", 3600, 10000)
		if err != nil || got == nil {
			t.Fatalf("同 run 重入 = %#v/%v, want 非 nil", got, err)
		}
		if got.NextProbeAtMs != 3600 {
			t.Fatalf("nextProbeAtMs = %d, want 3600", got.NextProbeAtMs)
		}
	})

	t.Run("expired run allows takeover and keeps larger next probe", func(t *testing.T) {
		if ok, _ := store.SetIfAbsent(ctx, w1gProbeState("a3"), 10000); !ok {
			t.Fatal("seed 失败")
		}
		seed, _ := store.Get(ctx, "a3")
		seed.ProbeRunID = w1gStringPtr("run-old")
		seed.ProbeRunUntilMs = w1gInt64Ptr2(2900)
		seed.NextProbeAtMs = 9000
		if _, err := store.Merge(ctx, *seed, 10000, w1gCoordinatorMergeOptions()); err != nil {
			t.Fatalf("seed run: %v", err)
		}
		got, err := store.AcquireGenerationRun(ctx, "a3", 1, "run-new", 3200, 10000)
		if err != nil || got == nil {
			t.Fatalf("过期 run 接管 = %#v/%v, want 非 nil", got, err)
		}
		if got.NextProbeAtMs != 9000 {
			t.Fatalf("nextProbeAtMs = %d, want 9000（保留更大的调度时间）", got.NextProbeAtMs)
		}
		if got.ProbeRunID == nil || *got.ProbeRunID != "run-new" || got.ProbeRunUntilMs == nil || *got.ProbeRunUntilMs != 3200 {
			t.Fatalf("run facts = %v/%v", got.ProbeRunID, got.ProbeRunUntilMs)
		}
	})
}

func TestW1GChainMemoryProbeStateStoreCommitGenerationRun(t *testing.T) {
	nowMs, _ := w1gProbeClock(4000)
	store := newChainMemoryProbeStateStore(nowMs)
	ctx := context.Background()
	options := w1gCoordinatorMergeOptions()

	seed := func(key string, generation int64, owner string) {
		t.Helper()
		state := w1gProbeState(key)
		state.Generation = generation
		state.ProbeRunID = w1gStringPtr(owner)
		if _, err := store.Merge(ctx, state, 10000, options); err != nil {
			t.Fatalf("seed merge: %v", err)
		}
	}

	t.Run("missing entry fails", func(t *testing.T) {
		next := w1gProbeState("c0")
		ok, err := store.CommitGenerationRun(ctx, next, "run-1", 10000)
		if err != nil || ok {
			t.Fatalf("CommitGenerationRun = %v/%v, want false/nil", ok, err)
		}
	})

	t.Run("generation and owner mismatch fail", func(t *testing.T) {
		seed("c1", 2, "run-owner")
		wrongGeneration := w1gProbeState("c1")
		wrongGeneration.Generation = 3
		if ok, _ := store.CommitGenerationRun(ctx, wrongGeneration, "run-owner", 10000); ok {
			t.Fatal("generation 不匹配必须失败")
		}
		wrongOwner := w1gProbeState("c1")
		wrongOwner.Generation = 2
		if ok, _ := store.CommitGenerationRun(ctx, wrongOwner, "run-other", 10000); ok {
			t.Fatal("owner 不匹配必须失败")
		}
	})

	t.Run("commit clears run facts and unions fences", func(t *testing.T) {
		seed("c2", 1, "run-owner")
		next := w1gProbeState("c2")
		next.Generation = 1
		next.SourceFences = []string{"f2"}
		ok, err := store.CommitGenerationRun(ctx, next, "run-owner", 10000)
		if err != nil || !ok {
			t.Fatalf("CommitGenerationRun = %v/%v, want true/nil", ok, err)
		}
		committed, err := store.Get(ctx, "c2")
		if err != nil || committed == nil {
			t.Fatalf("Get = %#v/%v", committed, err)
		}
		if committed.ProbeRunID != nil || committed.ProbeRunUntilMs != nil {
			t.Fatalf("run facts = %v/%v, want 清空", committed.ProbeRunID, committed.ProbeRunUntilMs)
		}
		if !reflect.DeepEqual(committed.SourceFences, []string{"f2"}) {
			t.Fatalf("sourceFences = %#v", committed.SourceFences)
		}

		// 已提交（ProbeRunID 为 nil）后再次 commit 同一 run 必须失败。
		if ok, _ := store.CommitGenerationRun(ctx, next, "run-owner", 10000); ok {
			t.Fatal("无 run 归属的二次 commit 必须失败")
		}
	})
}

func TestW1GChainMemoryProbeStateStoreReplaceSettledGeneration(t *testing.T) {
	nowMs, _ := w1gProbeClock(5000)
	store := newChainMemoryProbeStateStore(nowMs)
	ctx := context.Background()
	options := w1gCoordinatorMergeOptions()

	t.Run("missing entry fails", func(t *testing.T) {
		got, err := store.ReplaceSettledGeneration(ctx, w1gProbeState("r0"), 1, 10000)
		if err != nil || got != nil {
			t.Fatalf("ReplaceSettledGeneration = %#v/%v, want nil/nil", got, err)
		}
	})

	t.Run("unsettled or running generation fails", func(t *testing.T) {
		state := w1gProbeState("r1")
		if ok, _ := store.SetIfAbsent(ctx, state, 10000); !ok {
			t.Fatal("seed 失败")
		}
		if got, _ := store.ReplaceSettledGeneration(ctx, w1gProbeState("r1"), 1, 10000); got != nil {
			t.Fatalf("未 settle（无 outcome）替换 = %#v, want nil", got)
		}
		seed, _ := store.Get(ctx, "r1")
		seed.Outcome = w1gStringPtr("healthy")
		seed.ProbeRunID = w1gStringPtr("run-active")
		if _, err := store.Merge(ctx, *seed, 10000, options); err != nil {
			t.Fatalf("seed run: %v", err)
		}
		if got, _ := store.ReplaceSettledGeneration(ctx, w1gProbeState("r1"), 1, 10000); got != nil {
			t.Fatalf("run 未清空时替换 = %#v, want nil", got)
		}
	})

	t.Run("settled run-free generation replaces and returns prior snapshot", func(t *testing.T) {
		prior := w1gProbeState("r2")
		prior.Outcome = w1gStringPtr("healthy")
		prior.CompletedAtMs = w1gInt64Ptr2(4999)
		if _, err := store.Merge(ctx, prior, 10000, options); err != nil {
			t.Fatalf("seed merge: %v", err)
		}
		replacement := w1gProbeState("r2")
		replacement.Generation = 7
		returned, err := store.ReplaceSettledGeneration(ctx, replacement, 1, 10000)
		if err != nil || returned == nil {
			t.Fatalf("ReplaceSettledGeneration = %#v/%v, want 非 nil", returned, err)
		}
		if returned.Generation != 1 || returned.Outcome == nil || *returned.Outcome != "healthy" {
			t.Fatalf("prior snapshot = %#v", returned)
		}
		stored, _ := store.Get(ctx, "r2")
		if stored == nil || stored.Generation != 7 {
			t.Fatalf("替换后 state = %#v, want generation 7", stored)
		}
	})

	t.Run("expected generation mismatch fails", func(t *testing.T) {
		settled := w1gProbeState("r3")
		settled.Outcome = w1gStringPtr("failed")
		if _, err := store.Merge(ctx, settled, 10000, options); err != nil {
			t.Fatalf("seed merge: %v", err)
		}
		if got, _ := store.ReplaceSettledGeneration(ctx, w1gProbeState("r3"), 42, 10000); got != nil {
			t.Fatalf("epoch 不匹配替换 = %#v, want nil", got)
		}
	})
}

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
