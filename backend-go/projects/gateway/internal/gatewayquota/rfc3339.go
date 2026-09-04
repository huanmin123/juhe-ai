package gatewayquota

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// rfc3339InstantPattern mirrors shared/rfc3339.ts: the offset is mandatory
// and bare date-times are never guessed against the local zone.
var rfc3339InstantPattern = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$`)

// parseRfc3339Instant mirrors parseRfc3339Instant. ok=false mirrors the
// undefined return.
func parseRfc3339Instant(value string) (time.Time, bool) {
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
	case offset[0] == '+':
		location = fixedOffsetLocation(offset)
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
	return time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// canonicalizeRfc3339Instant mirrors canonicalizeRfc3339Instant (Node
// toISOString: millisecond precision, Z suffix).
func canonicalizeRfc3339Instant(value string) (string, bool) {
	parsed, ok := parseRfc3339Instant(value)
	if !ok {
		return "", false
	}
	return formatRFC3339Millis(parsed), true
}

// formatRFC3339Millis renders an instant like Node's Date#toISOString.
func formatRFC3339Millis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// requiredRfc3339Instant mirrors requiredRfc3339Instant.
func requiredRfc3339Instant(value string, label string) (string, error) {
	normalized, ok := canonicalizeRfc3339Instant(value)
	if !ok {
		return "", fmt.Errorf("%s必须是带 Z 或数值 offset 的 RFC3339 时间", label)
	}
	return normalized, nil
}

// rfc3339InstantMilliseconds mirrors rfc3339InstantMilliseconds.
func rfc3339InstantMilliseconds(value string) (int64, bool) {
	parsed, ok := parseRfc3339Instant(value)
	if !ok {
		return 0, false
	}
	return parsed.UnixMilli(), true
}
