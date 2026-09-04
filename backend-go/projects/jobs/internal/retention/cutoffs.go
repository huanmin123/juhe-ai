package retention

import (
	"fmt"
	"strings"
	"time"

	// Node resolves IANA names through bundled ICU; embedding tzdata keeps
	// LoadUsageStatsTimezone working on Windows/macOS production hosts that
	// ship no system zoneinfo database.
	_ "time/tzdata"
)

// isoLayout mirrors JavaScript Date.prototype.toISOString: millisecond
// precision, always UTC, always the Z suffix.
const isoLayout = "2006-01-02T15:04:05.000Z"

// ISOString formats an instant exactly like Node's toISOString.
func ISOString(t time.Time) string {
	return t.UTC().Format(isoLayout)
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
	parts := zonedDateParts(t, location)
	return fmt.Sprintf("%04d-%02d-%02d", parts.year, parts.month, parts.day)
}

// hourKey mirrors hourKey: YYYY-MM-DDTHH in the business timezone.
func hourKey(t time.Time, location *time.Location) string {
	parts := zonedDateParts(t, location)
	return fmt.Sprintf("%04d-%02d-%02dT%02d", parts.year, parts.month, parts.day, parts.hour)
}

// minuteKey mirrors minuteKey: YYYY-MM-DDTHH:MM in the business timezone.
func minuteKey(t time.Time, location *time.Location) string {
	parts := zonedDateParts(t, location)
	return fmt.Sprintf("%04d-%02d-%02dT%02d:%02d", parts.year, parts.month, parts.day, parts.hour, parts.minute)
}

// weekKey mirrors weekKey: the zoned calendar date snapped back to its
// Monday. Node performs the Monday snap with host-local Date arithmetic on
// the zoned Y/M/D parts; the weekday of a calendar date is
// timezone-independent, so the result is pure calendar math.
func weekKey(t time.Time, location *time.Location) string {
	parts := zonedDateParts(t, location)
	daysSinceMonday := weekdayIndex(parts.year, parts.month, parts.day)
	monday := time.Date(parts.year, time.Month(parts.month), parts.day-daysSinceMonday, 12, 0, 0, 0, time.UTC)
	return fmt.Sprintf("%04d-%02d-%02d", monday.Year(), int(monday.Month()), monday.Day())
}

// monthKey mirrors monthKey: YYYY-MM in the business timezone.
func monthKey(t time.Time, location *time.Location) string {
	parts := zonedDateParts(t, location)
	return fmt.Sprintf("%04d-%02d", parts.year, parts.month)
}

type zonedParts struct {
	year   int
	month  int
	day    int
	hour   int
	minute int
}

// zonedDateParts mirrors zonedDateParts: the wall-clock parts of an instant
// in the business timezone. An invalid location keeps the Node error text.
func zonedDateParts(t time.Time, location *time.Location) zonedParts {
	if location == nil {
		location = time.UTC
	}
	parts := t.In(location)
	return zonedParts{
		year:   parts.Year(),
		month:  int(parts.Month()),
		day:    parts.Day(),
		hour:   parts.Hour(),
		minute: parts.Minute(),
	}
}

// weekdayIndex returns 0 for Monday through 6 for Sunday of the given
// calendar date. Noon UTC keeps the calendar date stable.
func weekdayIndex(year, month, day int) int {
	weekday := int(time.Date(year, time.Month(month), day, 12, 0, 0, 0, time.UTC).Weekday())
	if weekday == int(time.Sunday) {
		return 6
	}
	return weekday - 1
}

// LoadUsageStatsTimezone mirrors normalizeUsageStatsTimezone: a non-empty
// IANA name that must resolve. Error text is byte-identical to Node.
func LoadUsageStatsTimezone(value string) (*time.Location, error) {
	timezone := strings.TrimSpace(value)
	if timezone == "" {
		return nil, fmt.Errorf("统计时区必须是非空字符串")
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("统计时区不存在：%s", timezone)
	}
	return location, nil
}
