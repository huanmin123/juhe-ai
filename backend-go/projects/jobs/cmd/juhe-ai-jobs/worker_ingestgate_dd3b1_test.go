package main

// DD3-B1 回归（2026-10-31，R4 confirmed A 类即修）：statsagg 游标永久缺口。
//
// 缺陷：ingestDrainProbe 原先先读 writer.Runtime()、后读
// spoolPendingOldestCreatedAt()，两段读取之间被 drain 末批交错（入队 + 删
// 文件 + 水位清空）时，runtime 快照早于入队、水位读取晚于清空，两源双漏 →
// safeCreatedBefore 回退默认 now-15s → 聚合游标越过尚未入表的积压记录
// created_at → 记录入表后永久漏聚合。
//
// 修复：调换读序（先 spool 水位、后 writer runtime），论证见
// assembleIngestDrainStatus（记录单向迁移 文件→队列→表，水位居前即无缺口）。
//
// worker 测试基建的 writer/drain 字段是具体类型（*usagewriter.Writer /
// *usagespooldrain.Drainer），probe 闭包不可注入；本组测试钉在装配纯函数
// assembleIngestDrainStatus 上——以 thunk 模拟 T1/T2 间 drain 完成末批的
// 交错现场，时间由 ingestgate.Check 的 now 参数固定（无需 clock 注入）。

import (
	"context"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/ingestgate"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/usagewriter"
)

// dd3b1CheckThroughProbe 把装配结果按 probe 形态送入 ingestgate.Check，
// 返回游标安全截止时间。
func dd3b1CheckThroughProbe(t *testing.T, status *ingestgate.DrainStatus, now time.Time) ingestgate.Safety {
	t.Helper()
	safety, err := ingestgate.Check(context.Background(), func(context.Context) (*ingestgate.DrainStatus, error) {
		return status, nil
	}, now)
	if err != nil {
		t.Fatalf("门控必须放行: %v", err)
	}
	return safety
}

// TestDD3B1DrainFinishInterleaveStillHoldsCursorBack 钉住修复主场景：T1 读
// spool 水位时滞留旧记录仍在文件（超 15s 安全水位），T1~T2 间 drain 完成
// 末批（入队 + flush 入表 + 删文件 + 水位清空），T2 读 runtime 已排干——
// 上界仍被 T1 读到的旧记录压住（回退 oldest-1ms），游标不越过，记录入表后
// 本轮/后续聚合可见。修复前（先 runtime 后水位）同一交错两源皆空，上界跳
// now-15s。
func TestDD3B1DrainFinishInterleaveStillHoldsCursorBack(t *testing.T) {
	fixedNow := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	// 滞留 20s 的积压记录 created_at（远早于默认 now-15s 上界）。
	const staleCreatedAt = "2026-10-31T11:59:40.000Z"

	var callOrder []string
	spoolReads := 0
	readSpool := func() string {
		callOrder = append(callOrder, "spool")
		spoolReads++
		if spoolReads == 1 {
			// T1：末批记录仍滞留在 spool 文件，水位覆盖其 created_at。
			return staleCreatedAt
		}
		// T1~T2 间 drain 删除文件后水位清空（本读序下不会再读第二次，
		// 分支仅为语义完备）。
		return ""
	}
	readRuntime := func() usagewriter.Runtime {
		callOrder = append(callOrder, "runtime")
		// T2：末批已 flush 入表，队列空——runtime 观测不到积压。
		return usagewriter.Runtime{}
	}

	status := assembleIngestDrainStatus(readSpool, readRuntime)
	if len(callOrder) != 2 || callOrder[0] != "spool" || callOrder[1] != "runtime" {
		t.Fatalf("必须先读 spool 水位后读 writer runtime，实际顺序 %v", callOrder)
	}
	if status.PendingUsageRecordsOldestCreatedAt != staleCreatedAt {
		t.Fatalf("水位快照必须取自 T1 读取值: %q", status.PendingUsageRecordsOldestCreatedAt)
	}

	safety := dd3b1CheckThroughProbe(t, status, fixedNow)
	// T93-6 后默认上界是 now-5min=11:55:00（gateway 进程内 buffer 保守下界），
	// 已早于 20s 滞留记录，safeCreatedBeforeForPendingBacklog 取默认值；不变式
	// 仍是「上界早于滞留记录」——游标不越过 11:59:40 的记录。
	const wantSafeCreatedBefore = "2026-10-31T11:55:00.000Z"
	if safety.SafeCreatedBefore != wantSafeCreatedBefore {
		t.Fatalf("交错下上界必须被安全水位压住: got %q, want %q", safety.SafeCreatedBefore, wantSafeCreatedBefore)
	}
	if !(safety.SafeCreatedBefore < staleCreatedAt) {
		t.Fatalf("上界 %q 越过了滞留记录 %q", safety.SafeCreatedBefore, staleCreatedAt)
	}

	// 滞留超过 buffer 界的积压仍必须压住上界（回退 oldest-1ms）。
	const oldStaleCreatedAt = "2026-10-31T11:40:00.000Z"
	oldStatus := assembleIngestDrainStatus(
		func() string { return oldStaleCreatedAt },
		func() usagewriter.Runtime { return usagewriter.Runtime{} },
	)
	oldSafety := dd3b1CheckThroughProbe(t, oldStatus, fixedNow)
	if want := "2026-10-31T11:39:59.999Z"; oldSafety.SafeCreatedBefore != want {
		t.Fatalf("早于 buffer 界的积压必须回退到 oldest-1ms: got %q want %q", oldSafety.SafeCreatedBefore, want)
	}
}

// TestDD3B1QueueDrainedBetweenReadsStillObserved 钉住另一交错分支：T1 读
// 水位时记录已迁入 writer 队列（文件段已清空），T1~T2 间记录仍在队列，
// T2 runtime 观测到——上界同样被压住（runtime 源覆盖队列段）。
func TestDD3B1QueueDrainedBetweenReadsStillObserved(t *testing.T) {
	fixedNow := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	const queuedCreatedAt = "2026-10-31T11:59:50.000Z"

	readSpool := func() string {
		// T1：文件段已排干（记录更早已迁入队列）。
		return ""
	}
	readRuntime := func() usagewriter.Runtime {
		// T2：记录仍在内存队列，runtime 队头观测到。
		return usagewriter.Runtime{OldestCreatedAt: queuedCreatedAt}
	}

	safety := dd3b1CheckThroughProbe(t, assembleIngestDrainStatus(readSpool, readRuntime), fixedNow)
	// 积压（11:59:50）晚于默认上界（T93-6 后为 now-5min=11:55:00）→
	// safeCreatedBeforeForPendingBacklog 取默认值：游标不越过积压记录（记录
	// 入表后由下轮覆盖）。
	const wantSafeCreatedBefore = "2026-10-31T11:55:00.000Z"
	if safety.SafeCreatedBefore != wantSafeCreatedBefore {
		t.Fatalf("队列段积压必须参与上界回退: got %q, want %q", safety.SafeCreatedBefore, wantSafeCreatedBefore)
	}
}

// TestDD3B1NoBacklogKeepsDefaultSafety 锁定正常路径：两源皆空时上界保持默认
// 安全水位（now-5min，T93-6 后由 gateway 进程内 buffer 滞留下界决定），读序
// 调换不改变无积压行为。
func TestDD3B1NoBacklogKeepsDefaultSafety(t *testing.T) {
	fixedNow := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	safety := dd3b1CheckThroughProbe(t, assembleIngestDrainStatus(
		func() string { return "" },
		func() usagewriter.Runtime { return usagewriter.Runtime{} },
	), fixedNow)
	const wantSafeCreatedBefore = "2026-10-31T11:55:00.000Z"
	if safety.SafeCreatedBefore != wantSafeCreatedBefore {
		t.Fatalf("无积压时上界必须保持默认安全水位 now-5min: got %q, want %q", safety.SafeCreatedBefore, wantSafeCreatedBefore)
	}
}
