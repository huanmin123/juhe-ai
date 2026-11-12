// Package statcalendar 是业务日历键的统一实现（迁移中）：日期、小时、分钟、
// 周、月。存量 statsagg 的 UsageStatsTimeKeysFor 与 statreads 的窗口/区间
// 函数将在后续批次收敛到本包。
//
// 日历键不是瞬时：它表示 usageStatsTimezone 下的墙钟标签。周键固定周一起始，
// 月键固定每月 1 日；跨年周、闰日与 DST 由纯日历运算处理，不受宿主时区影响。
package statcalendar

import (
	"strings"
	"time"

	// 内嵌 IANA 时区库：distroless/Windows 宿主也能解析 usageStatsTimezone。
	_ "time/tzdata"
)

// Day 是 "YYYY-MM-DD" 日历日。
type Day string

// Hour 是 "YYYY-MM-DDTHH" 业务时区小时。
type Hour string

// Minute 是 "YYYY-MM-DDTHH:MM" 业务时区分钟。
type Minute string

// Week 是周一起始日的 "YYYY-MM-DD" 日历日。
type Week string

// Month 是 "YYYY-MM" 日历月。
type Month string

const (
	dayLayout    = "2006-01-02"
	hourLayout   = "2006-01-02T15"
	minuteLayout = "2006-01-02T15:04"
	monthLayout  = "2006-01"

	maxInclusiveDays = 3660
)

// DayOf 返回 t 在 loc 下的日历日。
func DayOf(t time.Time, loc *time.Location) Day {
	return Day(t.In(loc).Format(dayLayout))
}

// HourOf 返回 t 在 loc 下的小时键。
func HourOf(t time.Time, loc *time.Location) Hour {
	return Hour(t.In(loc).Format(hourLayout))
}

// MinuteOf 返回 t 在 loc 下的分钟键。
func MinuteOf(t time.Time, loc *time.Location) Minute {
	return Minute(t.In(loc).Format(minuteLayout))
}

// WeekOf 返回 t 在 loc 下所在周的周一日期。
// 日历运算固定在 UTC 正午，避免 DST 与宿主时区把日期推错。
func WeekOf(t time.Time, loc *time.Location) Week {
	year, month, day := t.In(loc).Date()
	noon := time.Date(year, month, day, 12, 0, 0, 0, time.UTC)
	daysSinceMonday := (int(noon.Weekday()) + 6) % 7
	return Week(noon.AddDate(0, 0, -daysSinceMonday).Format(dayLayout))
}

// MonthOf 返回 t 在 loc 下的日历月。
func MonthOf(t time.Time, loc *time.Location) Month {
	return Month(t.In(loc).Format(monthLayout))
}

// ParseDay 解析并校验一个日历日，返回 UTC 午夜用于纯日历运算。
func ParseDay(value Day) (time.Time, bool) {
	return parseDate(string(value))
}

// ParseWeek 解析并校验一个周键，要求该日期是周一。
func ParseWeek(value Week) (time.Time, bool) {
	parsed, ok := parseDate(string(value))
	if !ok || parsed.Weekday() != time.Monday {
		return time.Time{}, false
	}
	return parsed, true
}

// ParseMonth 解析并校验 "YYYY-MM"，返回该月 1 日 UTC 午夜。
func ParseMonth(value Month) (time.Time, bool) {
	text := strings.TrimSpace(string(value))
	if len(text) != 7 || text[4] != '-' {
		return time.Time{}, false
	}
	parsed, err := time.Parse(monthLayout, text)
	if err != nil || parsed.Format(monthLayout) != text {
		return time.Time{}, false
	}
	return parsed, true
}

// ParseHour 解析并校验 "YYYY-MM-DDTHH"，返回 UTC 墙钟时间。
func ParseHour(value Hour) (time.Time, bool) {
	text := strings.TrimSpace(string(value))
	if len(text) != 13 || text[10] != 'T' {
		return time.Time{}, false
	}
	parsed, err := time.Parse(hourLayout, text)
	if err != nil || parsed.Format(hourLayout) != text {
		return time.Time{}, false
	}
	return parsed, true
}

// ParseMinute 解析并校验 "YYYY-MM-DDTHH:MM"，返回 UTC 墙钟时间。
func ParseMinute(value Minute) (time.Time, bool) {
	text := strings.TrimSpace(string(value))
	if len(text) != 16 || text[10] != 'T' || text[13] != ':' {
		return time.Time{}, false
	}
	parsed, err := time.Parse(minuteLayout, text)
	if err != nil || parsed.Format(minuteLayout) != text {
		return time.Time{}, false
	}
	return parsed, true
}

// StartOfDay 返回日历日在该时区的第一个瞬时。DST 跳过 00:00 时返回实际
// 第一个可表示瞬时（例如 01:00），不会跨到前一天。
func StartOfDay(day Day, loc *time.Location) (time.Time, bool) {
	parsed, ok := ParseDay(day)
	if !ok {
		return time.Time{}, false
	}
	year, month, date := parsed.Date()
	start := time.Date(year, month, date, 0, 0, 0, 0, loc)
	if start.Year() != year || start.Month() != month || start.Day() != date {
		return time.Time{}, false
	}
	return start, true
}

// NextDay 返回下一个日历日；非法输入原样返回。
func NextDay(day Day) Day {
	return AddDays(day, 1)
}

// AddDays 返回偏移后的日历日；非法输入原样返回。
func AddDays(day Day, days int) Day {
	parsed, ok := ParseDay(day)
	if !ok {
		return day
	}
	return Day(parsed.AddDate(0, 0, days).Format(dayLayout))
}

// DaysInclusive 返回 [start, end] 的连续日历日；非法或 start > end 返回 nil。
func DaysInclusive(start, end Day) []Day {
	first, okFirst := ParseDay(start)
	last, okLast := ParseDay(end)
	if !okFirst || !okLast || first.After(last) {
		return nil
	}
	days := int(last.Sub(first).Hours()/24) + 1
	if days <= 0 || days > maxInclusiveDays {
		return nil
	}
	result := make([]Day, 0, days)
	for current := first; !current.After(last); current = current.AddDate(0, 0, 1) {
		result = append(result, Day(current.Format(dayLayout)))
	}
	return result
}

// MonthFirstDay 返回月键对应的 1 日 UTC 午夜。
func MonthFirstDay(month Month) (time.Time, bool) {
	return ParseMonth(month)
}

// FormatDay 返回 day 的 "YYYY-MM-DD" 文本。
func FormatDay(day Day) string {
	return string(day)
}

// FormatMonth 返回 month 的 "YYYY-MM" 文本。
func FormatMonth(month Month) string {
	return string(month)
}

// LoadTimezone 校验并加载 IANA 时区名。空值或未知时区返回错误，调用方
// 必须 fail-closed，不得回落到宿主 time.Local。
func LoadTimezone(name string) (*time.Location, error) {
	timezone := strings.TrimSpace(name)
	if timezone == "" {
		return nil, errEmptyTimezone
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, &TimezoneError{Name: timezone, Err: err}
	}
	return location, nil
}

var errEmptyTimezone = &TimezoneError{Name: ""}

// TimezoneError 表示统计时区名为空或无法加载。
type TimezoneError struct {
	Name string
	Err  error
}

func (e *TimezoneError) Error() string {
	if e == nil {
		return "统计时区错误"
	}
	if e.Err == nil {
		return "统计时区必须是非空字符串"
	}
	return "统计时区不存在：" + e.Name
}

func (e *TimezoneError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func parseDate(value string) (time.Time, bool) {
	text := strings.TrimSpace(value)
	if len(text) != 10 || text[4] != '-' || text[7] != '-' {
		return time.Time{}, false
	}
	parsed, err := time.Parse(dayLayout, text)
	if err != nil || parsed.Format(dayLayout) != text {
		return time.Time{}, false
	}
	return parsed, true
}
