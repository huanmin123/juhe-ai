package jobregistry

import (
	"strings"
	"testing"
	"time"
)

// D13：调度注册表的 databaseDriver 注册分叉随 sqlite 形态退役坍缩为 PG 分支
// 恒定——ResolveScheduleForDriver/ModeConstraint/DriverPostgres 已物理删除，
// 本测试锁定新语义：
//   - sqlite-only 任务（usage-scope-range-windows-refresh）从注册表与调度表
//     一并删除，不存在永不可达注册项；
//   - usage-overview-windows-refresh interval 恒 5min（原 SQLite 30min 覆盖
//     删除）；
//   - ResolveSchedule 保持 settings 覆盖优先语义。
func TestResolveSchedulePGConstantSemantics(t *testing.T) {
	if _, ok := ScheduleFor("usage-scope-range-windows-refresh"); ok {
		t.Fatal("sqlite-only 任务必须从调度表删除（D13）")
	}
	if _, ok := Find("usage-scope-range-windows-refresh"); ok {
		t.Fatal("sqlite-only 任务必须从注册表删除（D13）")
	}
	pg, ok := ResolveSchedule("usage-overview-windows-refresh", nil)
	if !ok || pg.Interval != 5*time.Minute {
		t.Fatalf("usage-overview interval=%v, want 恒 5m", pg.Interval)
	}
	aiPerformance, ok := ResolveSchedule("ai-performance-summary-windows-refresh", nil)
	if !ok {
		t.Fatal("ai-performance-summary-windows-refresh 必须恒注册")
	}
	if aiPerformance.LeaseTTL == 0 {
		t.Fatalf("ai-performance 调度参数不得漂移: %+v", aiPerformance)
	}
	// settings 覆盖优先于注册表默认。
	overridden, ok := ResolveSchedule("usage-overview-windows-refresh", func(name string) (time.Duration, bool) {
		if name == "usage-overview-windows-refresh" {
			return 7 * time.Minute, true
		}
		return 0, false
	})
	if !ok || overridden.Interval != 7*time.Minute {
		t.Fatalf("settings 覆盖必须优先: %v %v", overridden.Interval, ok)
	}
	// 注册表与调度表之间不存在「注册了却无调度参数」的悬挂行。
	for _, entry := range ScheduledEntries() {
		if _, ok := ScheduleFor(entry.JobName); !ok {
			t.Errorf("注册条目 %s 无调度参数（悬挂行）", entry.JobName)
		}
		if strings.Contains(strings.Join(entry.Writes, ","), "usage-shards:") {
			t.Errorf("注册条目 %s 仍引用 usage-shards 写面词表", entry.JobName)
		}
	}
}
