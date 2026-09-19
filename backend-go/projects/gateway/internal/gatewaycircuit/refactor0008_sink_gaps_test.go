package gatewaycircuit

import (
	"context"
	"testing"
)

// REFACTOR-0008 下潜波次的覆盖补强：下潜后包内语句基数缩小，本文件补回
// 既有低覆盖臂（淘汰循环、backoff 零基准窗口臂），保证覆盖率基线不降。
// 只补可经正常输入到达的臂；不可达防御臂（如 delay<1、keep<0）不在列。

// TestRefactor0008ReplayEvictionArm 覆盖 rememberReplayLocked 的淘汰循环：
// replayLimitPerScope=1 时连续不同 transitionID 触发 replayOrder 收缩。
// suspect 的 revision 语义要求每次推进先 ReplaceDispatchRevision；
// 每次 mutation 必须携带全新 transitionID（复用已消费 id 会被
// idempotentLocked 重放为 idempotent，见 w11c 权威守卫测试）。
func TestRefactor0008ReplayEvictionArm(t *testing.T) {
	store, err := NewMemoryStore(MemoryStoreOptions{
		Capacity:            4,
		ReplayLimitPerScope: 1,
		Now:                 func() int64 { return 10_000 },
		Random:              func() float64 { return 0.5 },
	})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	scope := accountScope("refactor0008-replay")
	ctx := context.Background()
	for i, revision := range []string{"7", "8", "9"} {
		result, err := store.Suspect(ctx, SuspectInput{
			Scope: scope, DispatchRevision: revision, TransitionID: string(rune('a' + i)),
			Reason: "transport:connect failed", NowMs: int64Ptr(10_000),
		})
		if err != nil {
			t.Fatalf("Suspect #%d: %v", i, err)
		}
		if result.Status != MutationApplied {
			t.Fatalf("Suspect #%d status = %s", i, result.Status)
		}
		if i < 2 {
			replaced, err := store.ReplaceDispatchRevision(ctx, ReplaceDispatchRevisionInput{
				Scope: scope, DispatchRevision: string(rune('8' + i)),
				TransitionID: "r" + string(rune('0'+i)), NowMs: int64Ptr(10_000),
			})
			if err != nil {
				t.Fatalf("ReplaceDispatchRevision #%d: %v", i, err)
			}
			if replaced.Status != MutationApplied {
				t.Fatalf("ReplaceDispatchRevision #%d status = %s", i, replaced.Status)
			}
		}
	}
}

// TestRefactor0008BackoffZeroBaseWindowArm 覆盖 accountCircuitBackoffDelayMs
// 的 windowMs<=0 臂：第 5 级（attempt 为 1-based）基准为 0ms 时确定性
// offset 退回基准值。
func TestRefactor0008BackoffZeroBaseWindowArm(t *testing.T) {
	settings := Settings{AccountCircuitBackoffMs: []int64{1, 1, 1, 1, 0}}
	if got := settings.accountCircuitBackoffDelayMs(5, "refactor0008-seed", nil); got != 0 {
		t.Fatalf("zero base window arm = %d, want 0", got)
	}
}
