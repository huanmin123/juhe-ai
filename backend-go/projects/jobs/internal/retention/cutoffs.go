package retention

import (
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/statcalendar"
	"github.com/huanminabc/juhe-ai/backend-go-platform/timefmt"
)

// ISOString formats an instant exactly like Node's toISOString.
func ISOString(t time.Time) string {
	return timefmt.FormatRFC3339Millis(t)
}

// cutoffISO mirrors cutoffIso(now, retentionDays): the RFC3339 instant
// now minus retentionDays calendar-declared 24h days.
func cutoffISO(nowMillis int64, retentionDays int64) string {
	return ISOString(time.UnixMilli(nowMillis - retentionDays*retentionDayMillis))
}

// cutoffDateKey mirrors cutoffDateKey: the usage-stats date key (business
// timezone) of now minus retentionDays*24h.
func cutoffDateKey(nowMillis int64, retentionDays int64, location *time.Location) string {
	return dateKey(time.UnixMilli(nowMillis-retentionDays*retentionDayMillis), location)
}

// cutoffHourKey mirrors cutoffHourKey.
func cutoffHourKey(nowMillis int64, retentionDays int64, location *time.Location) string {
	return hourKey(time.UnixMilli(nowMillis-retentionDays*retentionDayMillis), location)
}

// cutoffMinuteKey mirrors cutoffMinuteKey (retention in hours).
func cutoffMinuteKey(nowMillis int64, retentionHours int64, location *time.Location) string {
	return minuteKey(time.UnixMilli(nowMillis-retentionHours*retentionHourMillis), location)
}

// cutoffWeekKey mirrors cutoffWeekKey (retention in weeks of 7*24h).
func cutoffWeekKey(nowMillis int64, retentionWeeks int64, location *time.Location) string {
	return weekKey(time.UnixMilli(nowMillis-retentionWeeks*retentionWeekMillis), location)
}

// cutoffMonthKeyHost mirrors cutoffMonthKey: Node mutates the month on the
// host-local wall clock (date.setMonth(getMonth() - months)) and then reads
// the business-timezone month key. hostLocation stands in for the process
// timezone; pass time.Local in production.
func cutoffMonthKeyHost(now time.Time, retentionMonths int64, hostLocation *time.Location, location *time.Location) string {
	shifted := now.In(hostLocation).AddDate(0, int(-retentionMonths), 0)
	return monthKey(shifted, location)
}

// dateKey mirrors usage-stats-helpers dateKey: YYYY-MM-DD in the business
// timezone.
func dateKey(t time.Time, location *time.Location) string {
	return string(statcalendar.DayOf(t, nonNilLocation(location)))
}

// hourKey mirrors hourKey: YYYY-MM-DDTHH in the business timezone.
func hourKey(t time.Time, location *time.Location) string {
	return string(statcalendar.HourOf(t, nonNilLocation(location)))
}

// minuteKey mirrors minuteKey: YYYY-MM-DDTHH:MM in the business timezone.
func minuteKey(t time.Time, location *time.Location) string {
	return string(statcalendar.MinuteOf(t, nonNilLocation(location)))
}

// weekKey mirrors weekKey: the zoned calendar date snapped back to its
// Monday.
func weekKey(t time.Time, location *time.Location) string {
	return string(statcalendar.WeekOf(t, nonNilLocation(location)))
}

// monthKey mirrors monthKey: YYYY-MM in the business timezone.
func monthKey(t time.Time, location *time.Location) string {
	return string(statcalendar.MonthOf(t, nonNilLocation(location)))
}

// nonNilLocation 保留旧 zonedDateParts 的 nil→UTC 兜底；生产路径的时区
// 已由 LoadUsageStatsTimezone fail-closed 校验，nil 只出现在历史测试夹具。
func nonNilLocation(location *time.Location) *time.Location {
	if location == nil {
		return time.UTC
	}
	return location
}

// LoadUsageStatsTimezone mirrors normalizeUsageStatsTimezone: a non-empty
// IANA name that must resolve. Error text is byte-identical to Node.
func LoadUsageStatsTimezone(value string) (*time.Location, error) {
	return statcalendar.LoadTimezone(value)
}
