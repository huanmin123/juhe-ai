package statsverify

import (
	"fmt"
	"time"
)

// Date key helpers mirroring storage/usage-stats-helpers.ts and
// storage/usage-stats-window-helpers.ts.

// FixedRangeWindowDays mirrors FIXED_RANGE_WINDOW_DAYS
// (usage-stats-window-helpers.ts line 6).
const FixedRangeWindowDays = 31

// DateKeyIn mirrors dateKey(date, timezone)
// (usage-stats-helpers.ts lines 140-143): the calendar date of t in the
// given stats timezone, formatted "YYYY-MM-DD". Node renders through
// Intl.DateTimeFormat('en-CA', {timeZone}) which resolves IANA timezone
// names; Go resolves the same names through time.LoadLocation.
func DateKeyIn(t time.Time, location *time.Location) string {
	return t.In(location).Format("2006-01-02")
}

// HourKeyIn mirrors hourKey(date, timezone)
// (usage-stats-helpers.ts lines 237-240): "YYYY-MM-DDTHH" in the stats
// timezone, hour cycle h23.
func HourKeyIn(t time.Time, location *time.Location) string {
	return t.In(location).Format("2006-01-02T15")
}

// NextDateKey mirrors nextDateKey (usage-stats-window-helpers.ts lines
// 152-158): advances a "YYYY-MM-DD" key by one calendar day using the host
// local timezone (Node constructs `new Date(y, m-1, d)` in local time and
// reads local parts back). Invalid inputs are returned unchanged.
func NextDateKey(value string) string {
	return NextDateKeyIn(value, time.Local)
}

// NextDateKeyIn is NextDateKey with an explicit location so tests can pin
// the host timezone Node would have used.
func NextDateKeyIn(value string, location *time.Location) string {
	parts, ok := parseDateKeyParts(value)
	if !ok {
		return value
	}
	date := time.Date(parts.year, time.Month(parts.month), parts.day, 0, 0, 0, 0, location).AddDate(0, 0, 1)
	return date.Format("2006-01-02")
}

// AddCalendarDaysIn offsets a "YYYY-MM-DD" key by days without DST drift by
// constructing local midnight and using AddDate (calendar arithmetic),
// mirroring the addDays/localDateKey helpers in usage-stats-helpers.ts.
func AddCalendarDaysIn(value string, days int, location *time.Location) string {
	parts, ok := parseDateKeyParts(value)
	if !ok {
		return value
	}
	return time.Date(parts.year, time.Month(parts.month), parts.day, 0, 0, 0, 0, location).AddDate(0, 0, days).Format("2006-01-02")
}

// FixedUsageStatsDateKeys mirrors fixedUsageStatsDateKeys
// (usage-stats-window-helpers.ts lines 38-43): the 31 date keys ending at
// todayKey.
func FixedUsageStatsDateKeys(location *time.Location, todayKey string) []string {
	if _, ok := parseDateKeyParts(todayKey); !ok {
		return nil
	}
	keys := make([]string, 0, FixedRangeWindowDays)
	earliest := AddCalendarDaysIn(todayKey, -(FixedRangeWindowDays - 1), location)
	for index := 0; index < FixedRangeWindowDays; index++ {
		keys = append(keys, AddCalendarDaysIn(earliest, index, location))
	}
	return keys
}

// ParseRFC3339 mirrors requiredRfc3339Instant: an instant must carry an
// explicit "Z" or numeric offset. The parsed instant is returned in UTC.
func ParseRFC3339(value string, label string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s 必须是带 Z 或数值 offset 的 RFC3339 时间: %w", label, err)
	}
	return t.UTC(), nil
}

type dateKeyParts struct {
	year  int
	month int
	day   int
}

func parseDateKeyParts(value string) (dateKeyParts, bool) {
	if len(value) != 10 || value[4] != '-' || value[7] != '-' {
		return dateKeyParts{}, false
	}
	year, month, day := 0, 0, 0
	if _, err := fmt.Sscanf(value, "%04d-%02d-%02d", &year, &month, &day); err != nil {
		return dateKeyParts{}, false
	}
	// Mirror parseDateKeyStrict: reject impossible calendar dates (Go's
	// time.Date normalizes out-of-range components, so verify round-trip).
	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if date.Year() != year || int(date.Month()) != month || date.Day() != day {
		return dateKeyParts{}, false
	}
	return dateKeyParts{year: year, month: month, day: day}, true
}
