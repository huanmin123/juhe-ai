package ingestgate

// C2-H2 回归（2026-11-01）：死信/丢弃对门控与对账不可见。
//
// 缺陷：writer 死信后记录已从队列移除，DrainStatus 只有 oldest/flushFailure，
// 门控看不到死信计数 → 游标继续推进越过死信记录的 created_at，即使日后补写
// 也永久不会聚合；丢弃（溢出落盘失败等）同样只在 writer 内部计数。
//
// 修复：DrainStatus 暴露 DeadLetterCount / DroppedCount；死信 >0 时 Check
// 失败本轮（可观测降级，不静默推进安全水位）。计数归零（writer 恢复/重启）
// 后门控自动解除。

import (
	"context"
	"strings"
	"testing"
)

func TestC2H2DeadLetterBlocksAggregationGate(t *testing.T) {
	probe := func(context.Context) (*DrainStatus, error) {
		return &DrainStatus{Ready: true, SnapshotUsageRecordQueueDeadLetterCount: 2}, nil
	}
	_, err := Check(context.Background(), probe, fixedNow())
	if err == nil {
		t.Fatal("死信 >0 时门控必须失败本轮，避免游标越过丢失记录")
	}
	if !strings.Contains(err.Error(), "2 条死信记录") {
		t.Fatalf("错误文案必须携带死信计数: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "本轮跳过统计聚合") {
		t.Fatalf("错误文案必须说明跳过本轮: %q", err.Error())
	}
}

// TestC2H2DeadLetterClearedReopensGate：计数归零（writer 恢复/重启后的运行
// 态）门外自动解除，且安全水位语义回到既有积压回退逻辑。
func TestC2H2DeadLetterClearedReopensGate(t *testing.T) {
	probe := func(context.Context) (*DrainStatus, error) {
		return &DrainStatus{
			Ready:                                   true,
			SnapshotUsageRecordQueueDeadLetterCount: 0,
			SnapshotUsageRecordQueueOldestCreatedAt: "2026-09-04T11:30:00.000Z",
		}, nil
	}
	safety, err := Check(context.Background(), probe, fixedNow())
	if err != nil {
		t.Fatalf("死信计数归零后门控必须解除: %v", err)
	}
	if want := "2026-09-04T11:29:59.999Z"; safety.SafeCreatedBefore != want {
		t.Fatalf("safeCreatedBefore = %q, want %q", safety.SafeCreatedBefore, want)
	}
}

// TestC2H2DroppedCountIsVisibleButNotHardGate：丢弃计数进入快照（/health
// 观测面），但 oversize 等终态丢弃不可恢复，按累计值硬阻断会让统计永久
// 停摆；硬门控只针对死信。
func TestC2H2DroppedCountIsVisibleButNotHardGate(t *testing.T) {
	probe := func(context.Context) (*DrainStatus, error) {
		return &DrainStatus{Ready: true, SnapshotUsageRecordQueueDroppedCount: 5}, nil
	}
	safety, err := Check(context.Background(), probe, fixedNow())
	if err != nil {
		t.Fatalf("丢弃计数不应硬阻断统计: %v", err)
	}
	if safety.SafeCreatedBefore != wantDefault {
		t.Fatalf("safeCreatedBefore = %q, want default %q", safety.SafeCreatedBefore, wantDefault)
	}
}

// TestC2H2DeadLetterTakesPrecedenceOverBacklog：死信与积压同时存在时按死信
// 阻断（错误信息指向根因），不被积压回退分支掩盖。
func TestC2H2DeadLetterTakesPrecedenceOverBacklog(t *testing.T) {
	probe := func(context.Context) (*DrainStatus, error) {
		return &DrainStatus{
			Ready:                                   true,
			SnapshotUsageRecordQueueDeadLetterCount: 1,
			SnapshotUsageRecordQueueOldestCreatedAt: "2026-09-04T10:00:00.000Z",
		}, nil
	}
	_, err := Check(context.Background(), probe, fixedNow())
	if err == nil || !strings.Contains(err.Error(), "死信") {
		t.Fatalf("期望死信阻断错误，得到 %v", err)
	}
}
