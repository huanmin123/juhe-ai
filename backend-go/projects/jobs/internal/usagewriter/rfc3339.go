package usagewriter

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RFC3339 instant handling mirroring backend/src/shared/rfc3339.ts (same
// implementation as gateway/internal/gatewayusage/rfc3339.go; duplicated
// because the gateway and jobs Go modules cannot import each other).

// timeRFC3339Millis is the ISO string format Node produces with
// new Date(...).toISOString(): millisecond precision, Z suffix.
const timeRFC3339Millis = "2006-01-02T15:04:05.000Z07:00"

var rfc3339InstantPattern = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$`)

// parseRFC3339Instant mirrors parseRfc3339Instant: the offset is mandatory
// and bare date-times are never guessed against the local zone.
func parseRFC3339Instant(value string) (time.Time, bool) {
	match := rfc3339InstantPattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return time.Time{}, false
	}
	year, _ := strconv.Atoi(match[1])
	month, _ := strconv.Atoi(match[2])
	day, _ := strconv.Atoi(match[3])
	hour, _ := strconv.Atoi(match[4])
	minute, _ := strconv.Atoi(match[5])
	second, _ := strconv.Atoi(match[6])
	fraction := match[7]
	offset := match[8]
	if month < 1 || month > 12 || day < 1 || day > daysInMonth(year, month) ||
		hour > 23 || minute > 59 || second > 59 {
		return time.Time{}, false
	}
	var location *time.Location
	switch {
	case offset == "Z":
		location = time.UTC
	default:
		location = fixedOffsetLocation(offset)
	}
	nanos, ok := fractionNanos(fraction)
	if !ok {
		return time.Time{}, false
	}
	parsed := time.Date(year, time.Month(month), day, hour, minute, second, nanos, location)
	if parsed.Year() != year || int(parsed.Month()) != month || parsed.Day() != day {
		return time.Time{}, false
	}
	return parsed, true
}

func fixedOffsetLocation(offset string) *time.Location {
	sign := 1
	if offset[0] == '-' {
		sign = -1
	}
	hours, _ := strconv.Atoi(offset[1:3])
	minutes, _ := strconv.Atoi(offset[4:6])
	return time.FixedZone("", sign*(hours*3600+minutes*60))
}

func fractionNanos(fraction string) (int, bool) {
	if fraction == "" {
		return 0, true
	}
	padded := fraction
	for len(padded) < 9 {
		padded += "0"
	}
	nanos, err := strconv.Atoi(padded)
	if err != nil {
		return 0, false
	}
	return nanos, true
}

func daysInMonth(year, month int) int {
	if month == 12 {
		return 31
	}
	firstOfNext := time.Date(year, time.Month(month+1), 1, 0, 0, 0, 0, time.UTC)
	return firstOfNext.AddDate(0, 0, -1).Day()
}

// canonicalizeRFC3339Instant mirrors canonicalizeRfc3339Instant.
func canonicalizeRFC3339Instant(value string) (string, bool) {
	parsed, ok := parseRFC3339Instant(value)
	if !ok {
		return "", false
	}
	return parsed.UTC().Format(timeRFC3339Millis), true
}

// requiredRFC3339Instant mirrors requiredRfc3339Instant, including the
// Chinese error copy (逐字对齐 Node).
func requiredRFC3339Instant(value string, label string) (string, error) {
	normalized, ok := canonicalizeRFC3339Instant(value)
	if !ok {
		return "", fmt.Errorf("%s必须是带 Z 或数值 offset 的 RFC3339 时间", label)
	}
	return normalized, nil
}

// rfc3339InstantMilliseconds mirrors rfc3339InstantMilliseconds.
func rfc3339InstantMilliseconds(value string) (int64, bool) {
	parsed, ok := parseRFC3339Instant(value)
	if !ok {
		return 0, false
	}
	return parsed.UnixMilli(), true
}
