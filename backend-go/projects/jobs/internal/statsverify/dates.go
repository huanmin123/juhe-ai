package statsverify

import (
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/dbtime"
	"github.com/huanminabc/juhe-ai/backend-go-platform/statcalendar"
)

// Date key helpers 统一委托 shared/platform/statcalendar。
// 保留本包导出的函数名与签名，避免迁移期大范围改调用点。

// FixedRangeWindowDays mirrors FIXED_RANGE_WINDOW_DAYS
// (usage-stats-window-helpers.ts line 6).
const FixedRangeWindowDays = 31

// DateKeyIn 返回 t 在统计时区下的 "YYYY-MM-DD"。
func DateKeyIn(t time.Time, location *time.Location) string {
	return string(statcalendar.DayOf(t, location))
}

// HourKeyIn 返回 t 在统计时区下的 "YYYY-MM-DDTHH"。
func HourKeyIn(t time.Time, location *time.Location) string {
	return string(statcalendar.HourOf(t, location))
}

// NextDateKey 返回下一个日历日；非法输入原样返回。
func NextDateKey(value string) string {
	return NextDateKeyIn(value, time.Local)
}

// NextDateKeyIn 是 NextDateKey 的显式 location 版本。日期键加减是纯日历
// 运算，结果与宿主 location 无关；保留参数以兼容旧调用点。
func NextDateKeyIn(value string, _ *time.Location) string {
	return string(statcalendar.NextDay(statcalendar.Day(value)))
}

// AddCalendarDaysIn 按日历日偏移；非法输入原样返回。
func AddCalendarDaysIn(value string, days int, _ *time.Location) string {
	return string(statcalendar.AddDays(statcalendar.Day(value), days))
}

// FixedUsageStatsDateKeys 返回以 todayKey 结尾的 31 个连续日期键。
func FixedUsageStatsDateKeys(_ *time.Location, todayKey string) []string {
	if _, ok := statcalendar.ParseDay(statcalendar.Day(todayKey)); !ok {
		return nil
	}
	earliest := statcalendar.AddDays(statcalendar.Day(todayKey), -(FixedRangeWindowDays - 1))
	days := statcalendar.DaysInclusive(earliest, statcalendar.Day(todayKey))
	if len(days) == 0 {
		return nil
	}
	keys := make([]string, len(days))
	for index, day := range days {
		keys[index] = string(day)
	}
	return keys
}

// ParseRFC3339 解析必须带 Z 或数值 offset 的 RFC3339 时间，返回 UTC。
func ParseRFC3339(value string, label string) (time.Time, error) {
	return dbtime.ParseDBRequired(value, label)
}

type dateKeyParts struct {
	year  int
	month int
	day   int
}

// parseDateKeyParts 保留给本包测试的窄接口；实现委托 statcalendar。
func parseDateKeyParts(value string) (dateKeyParts, bool) {
	parsed, ok := statcalendar.ParseDay(statcalendar.Day(value))
	if !ok {
		return dateKeyParts{}, false
	}
	year, month, day := parsed.Date()
	return dateKeyParts{year: year, month: int(month), day: day}, true
}
