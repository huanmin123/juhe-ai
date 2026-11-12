package statcalendar

import (
	"testing"
	"time"
)

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := LoadTimezone(name)
	if err != nil {
		t.Fatalf("LoadTimezone(%s): %v", name, err)
	}
	return location
}

func TestDayHourMinuteOf(t *testing.T) {
	shanghai := mustLocation(t, "Asia/Shanghai")
	instant := time.Date(2026, 11, 10, 16, 30, 0, 0, time.UTC)
	if got := DayOf(instant, shanghai); got != "2026-11-11" {
		t.Fatalf("DayOf=%s", got)
	}
	if got := HourOf(instant, shanghai); got != "2026-11-11T00" {
		t.Fatalf("HourOf=%s", got)
	}
	if got := MinuteOf(instant, shanghai); got != "2026-11-11T00:30" {
		t.Fatalf("MinuteOf=%s", got)
	}
	if got := DayOf(instant, time.UTC); got != "2026-11-10" {
		t.Fatalf("DayOf(UTC)=%s", got)
	}
}

func TestWeekOfYearBoundary(t *testing.T) {
	shanghai := mustLocation(t, "Asia/Shanghai")
	cases := []struct {
		name string
		in   time.Time
		want Week
	}{
		{"new-year-thursday", time.Date(2026, 1, 1, 12, 0, 0, 0, shanghai), "2025-12-29"},
		{"normal-wednesday", time.Date(2026, 11, 11, 12, 0, 0, 0, shanghai), "2026-11-09"},
		{"monday", time.Date(2026, 11, 9, 0, 0, 0, 0, shanghai), "2026-11-09"},
		{"sunday", time.Date(2026, 11, 15, 23, 59, 0, 0, shanghai), "2026-11-09"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WeekOf(tc.in, shanghai); got != tc.want {
				t.Fatalf("WeekOf=%s want %s", got, tc.want)
			}
			if _, ok := ParseWeek(tc.want); !ok {
				t.Fatalf("ParseWeek(%s) 失败", tc.want)
			}
		})
	}
}

func TestMonthOf(t *testing.T) {
	shanghai := mustLocation(t, "Asia/Shanghai")
	if got := MonthOf(time.Date(2026, 10, 31, 16, 0, 0, 0, time.UTC), shanghai); got != "2026-11" {
		t.Fatalf("MonthOf=%s", got)
	}
	first, ok := MonthFirstDay("2026-11")
	if !ok || first.Format("2006-01-02") != "2026-11-01" {
		t.Fatalf("MonthFirstDay=%v ok=%v", first, ok)
	}
}

func TestParseValidation(t *testing.T) {
	if _, ok := ParseDay("2026-02-30"); ok {
		t.Fatal("非法日期必须拒绝")
	}
	if _, ok := ParseDay("2024-02-29"); !ok {
		t.Fatal("闰日必须接受")
	}
	if _, ok := ParseWeek("2026-11-10"); ok {
		t.Fatal("非周一不得作为周键")
	}
	if _, ok := ParseMonth("2026-13"); ok {
		t.Fatal("非法月份必须拒绝")
	}
	if _, ok := ParseHour("2026-11-11T24"); ok {
		t.Fatal("非法小时必须拒绝")
	}
	if _, ok := ParseMinute("2026-11-11T23:60"); ok {
		t.Fatal("非法分钟必须拒绝")
	}
}

func TestStartOfDayDST(t *testing.T) {
	newYork := mustLocation(t, "America/New_York")
	// 2026-03-08 是美国 DST 开始日；当日 00:00 EST = 05:00Z。
	start, ok := StartOfDay("2026-03-08", newYork)
	if !ok {
		t.Fatal("StartOfDay 失败")
	}
	if got := start.UTC().Format(time.RFC3339); got != "2026-03-08T05:00:00Z" {
		t.Fatalf("start=%s", got)
	}
	if got := DayOf(start, newYork); got != "2026-03-08" {
		t.Fatalf("回读日=%s", got)
	}
}

func TestAddDaysAndInclusive(t *testing.T) {
	if got := NextDay("2026-12-31"); got != "2027-01-01" {
		t.Fatalf("NextDay=%s", got)
	}
	if got := AddDays("2024-02-28", 1); got != "2024-02-29" {
		t.Fatalf("AddDays leap=%s", got)
	}
	days := DaysInclusive("2024-02-28", "2024-03-01")
	if len(days) != 3 || days[1] != "2024-02-29" {
		t.Fatalf("DaysInclusive=%v", days)
	}
	if DaysInclusive("2026-03-02", "2026-03-01") != nil {
		t.Fatal("反向区间必须 nil")
	}
}

func TestLoadTimezoneRejectsEmptyAndUnknown(t *testing.T) {
	if _, err := LoadTimezone(""); err == nil {
		t.Fatal("空时区必须报错")
	}
	if _, err := LoadTimezone("Not/AZone"); err == nil {
		t.Fatal("未知时区必须报错")
	}
}
