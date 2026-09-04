package gatewayclientip

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// rfc3339 ports shared/rfc3339.ts for the fields this family reads. The
// pattern requires a calendar datetime with a Z or numeric offset; bare
// datetimes fail exactly like the Node parser.

var rfc3339InstantPattern = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$`)

// rfc3339Millis mirrors rfc3339InstantMilliseconds: ok=false marks the
// undefined return of the Node helper (missing or malformed).
func rfc3339Millis(value string) (int64, bool) {
	if value == "" {
		return 0, false
	}
	return parseRFC3339InstantMillis(value)
}

// requiredRFC3339Millis mirrors requiredRfc3339Instant: malformed input
// throws with the Node error text.
func requiredRFC3339Millis(value string, label string) (int64, error) {
	millis, ok := parseRFC3339InstantMillis(value)
	if !ok {
		return 0, errors.New(label + " 必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	return millis, nil
}

func parseRFC3339InstantMillis(value string) (int64, bool) {
	text := strings.TrimSpace(value)
	if !rfc3339InstantPattern.MatchString(text) {
		return 0, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return 0, false
	}
	return parsed.UnixMilli(), true
}

// parseRFC3339InstantTime mirrors parseRfc3339Instant returning the Date.
func parseRFC3339InstantTime(value string) (time.Time, bool) {
	text := strings.TrimSpace(value)
	if !rfc3339InstantPattern.MatchString(text) {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

// canonicalRFC3339 mirrors canonicalizeRfc3339Instant: toISOString() —
// millisecond precision, UTC "Z" suffix.
func canonicalRFC3339(parsed time.Time) string {
	return parsed.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// isoNow mirrors new Date().toISOString().
func isoNow(clock Clock) string {
	return canonicalRFC3339(clock.Now())
}
