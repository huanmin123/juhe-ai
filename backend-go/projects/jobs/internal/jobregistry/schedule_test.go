package jobregistry

import (
	"testing"
	"time"
)

func TestResolveScheduleForDriverModeFork(t *testing.T) {
	// 冻结清单 §4.1：ai-performance 仅 PG 分支注册。
	if _, ok := ResolveScheduleForDriver("ai-performance-summary-windows-refresh", nil, DriverPostgres); !ok {
		t.Fatal("PG 分支必须注册 ai-performance-summary-windows-refresh")
	}
	if _, ok := ResolveScheduleForDriver("ai-performance-summary-windows-refresh", nil, "sqlite"); ok {
		t.Fatal("默认/SQLite 分支不得注册 ai-performance-summary-windows-refresh（stage 并入 rank job）")
	}
	// 冻结清单 §4.1：usage-scope-range 仅默认/SQLite 分支注册，PG 跳过。
	if _, ok := ResolveScheduleForDriver("usage-scope-range-windows-refresh", nil, "sqlite"); !ok {
		t.Fatal("默认/SQLite 分支必须注册 usage-scope-range-windows-refresh")
	}
	if _, ok := ResolveScheduleForDriver("usage-scope-range-windows-refresh", nil, DriverPostgres); ok {
		t.Fatal("PG 分支不得注册 usage-scope-range-windows-refresh（background_cold_range_window_refresh_disabled）")
	}
	// 冻结清单 §4.1：usage-overview interval PG 5min / SQLite 30min。
	pg, ok := ResolveScheduleForDriver("usage-overview-windows-refresh", nil, DriverPostgres)
	if !ok || pg.Interval != 5*time.Minute {
		t.Fatalf("PG interval=%v, want 5m", pg.Interval)
	}
	sqlite, ok := ResolveScheduleForDriver("usage-overview-windows-refresh", nil, "sqlite")
	if !ok || sqlite.Interval != 30*time.Minute {
		t.Fatalf("SQLite interval=%v, want 30m", sqlite.Interval)
	}
	// 其余参数两分支一致（initialDelay 等）。
	if sqlite.InitialDelay != pg.InitialDelay || sqlite.LeaseTTL != pg.LeaseTTL {
		t.Fatalf("模式分叉只允许 interval 差异: pg=%+v sqlite=%+v", pg, sqlite)
	}
	// settings 覆盖优先于模式分叉。
	overridden, ok := ResolveScheduleForDriver("usage-overview-windows-refresh", func(string) (time.Duration, bool) {
		return 7 * time.Minute, true
	}, "sqlite")
	if !ok || overridden.Interval != 7*time.Minute {
		t.Fatalf("settings 覆盖后的 interval=%v, want 7m", overridden.Interval)
	}
	// 无分叉 job 两 driver 同参。
	for _, name := range []string{"client-ip-stats-aggregation", "resource-authorization-expiry-sweep"} {
		if _, ok := ResolveScheduleForDriver(name, nil, "sqlite"); !ok {
			t.Fatalf("%s 在 SQLite 分支必须可注册", name)
		}
		if _, ok := ResolveScheduleForDriver(name, nil, DriverPostgres); !ok {
			t.Fatalf("%s 在 PG 分支必须可注册", name)
		}
	}
	// 未登记调度参数的 job 不注册。
	if _, ok := ResolveScheduleForDriver("nonexistent-job", nil, DriverPostgres); ok {
		t.Fatal("未登记调度参数必须返回 !ok")
	}
}
