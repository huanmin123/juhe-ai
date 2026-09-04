package gatewayproxyhealth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Clock mirrors the injectable time source the Node services get from
// Date.now() (and the *_ForTest overrides like
// gatewayUpstreamBucketHealthNowForTest). A nil Clock means the wall clock.
type Clock func() time.Time

// ClockNowMs returns the clock's millisecond timestamp (wall time when nil).
func ClockNowMs(clock Clock) int64 {
	if clock == nil {
		return time.Now().UnixMilli()
	}
	return clock().UnixMilli()
}

// ClockNow returns the clock's time value (wall time when nil).
func ClockNow(clock Clock) time.Time {
	if clock == nil {
		return time.Now()
	}
	return clock()
}

// NewRandomHex returns 2*byteCount hex characters (Node randomBytes(n).toString('hex')).
func NewRandomHex(byteCount int) string {
	buf := make([]byte, byteCount)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure is a process-level fault; fall back to a
		// time-derived value so instance identity stays unique enough for
		// mutation-generation ordering.
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf)
}

// NewUUID mirrors crypto.randomUUID (RFC 4122 v4).
func NewUUID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return NewRandomHex(16)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(buf[0:4]),
		hex.EncodeToString(buf[4:6]),
		hex.EncodeToString(buf[6:8]),
		hex.EncodeToString(buf[8:10]),
		hex.EncodeToString(buf[10:16]))
}

// ISOStringMs mirrors new Date(ms).toISOString(): millisecond precision, UTC.
func ISOStringMs(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

var rfc3339InstantPattern = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$`)

// ParseRfc3339Instant ports parseRfc3339Instant: the offset is mandatory and
// bare date-times are not guessed at any timezone.
func ParseRfc3339Instant(value string) (time.Time, bool) {
	text := strings.TrimSpace(value)
	match := rfc3339InstantPattern.FindStringSubmatch(text)
	if match == nil {
		return time.Time{}, false
	}
	year, _ := strconv.Atoi(match[1])
	month, _ := strconv.Atoi(match[2])
	day, _ := strconv.Atoi(match[3])
	hour, _ := strconv.Atoi(match[4])
	minute, _ := strconv.Atoi(match[5])
	second, _ := strconv.Atoi(match[6])
	offset := match[8]
	if month < 1 || month > 12 ||
		day < 1 || day > lastDayOfMonth(year, month) ||
		hour > 23 || minute > 59 || second > 59 ||
		(offset != "Z" && (mustAtoi(offset[1:3]) > 23 || mustAtoi(offset[4:6]) > 59)) {
		return time.Time{}, false
	}
	var nanos int
	if match[7] != "" {
		fraction := match[7]
		// Node Date accepts 1-9 fractional digits; normalize to nanoseconds.
		for len(fraction) < 9 {
			fraction += "0"
		}
		nanos, _ = strconv.Atoi(fraction[:9])
	}
	var loc *time.Location
	if offset == "Z" {
		loc = time.UTC
	} else {
		sign := 1
		if offset[0] == '-' {
			sign = -1
		}
		offHour := mustAtoi(offset[1:3])
		offMinute := mustAtoi(offset[4:6])
		loc = time.FixedZone("", sign*(offHour*3600+offMinute*60))
	}
	parsed := time.Date(year, time.Month(month), day, hour, minute, second, nanos, loc)
	return parsed, true
}

func lastDayOfMonth(year, month int) int {
	// Node: new Date(Date.UTC(year, month, 0)).getUTCDate() — month is 1-based
	// here, and Date.UTC month 0-based means (month) points at the last day of
	// the 1-based month.
	return time.Date(year, time.Month(month+1), 0, 0, 0, 0, 0, time.UTC).Day()
}

func mustAtoi(value string) int {
	parsed, _ := strconv.Atoi(value)
	return parsed
}

// CanonicalizeRfc3339Instant ports canonicalizeRfc3339Instant.
func CanonicalizeRfc3339Instant(value string) (string, bool) {
	parsed, ok := ParseRfc3339Instant(value)
	if !ok {
		return "", false
	}
	return ISOStringMs(parsed.UnixMilli()), true
}

// Rfc3339InstantMilliseconds ports rfc3339InstantMilliseconds.
func Rfc3339InstantMilliseconds(value string) (int64, bool) {
	parsed, ok := ParseRfc3339Instant(value)
	if !ok {
		return 0, false
	}
	return parsed.UnixMilli(), true
}

// PassiveScheduleJitterWindowMs ports passiveScheduleJitterWindowMs.
func PassiveScheduleJitterWindowMs(intervalMs int64) int64 {
	interval := intervalMs
	if interval < 1 {
		interval = 1
	}
	var windowMs int64
	const subMinuteWindowMs = int64(30_000)
	const minuteWindowMs = int64(30_000)
	const hourWindowMs = int64(30 * 60_000)
	const dayWindowMs = int64(60 * 60_000)
	const weekWindowMs = int64(8 * 60 * 60_000)
	switch {
	case interval < 60_000:
		// Never let a short interval become negative or run back-to-back.
		windowMs = minInt64(subMinuteWindowMs, interval/2)
	case interval < 60*60_000:
		windowMs = minuteWindowMs
	case interval < 24*60*60_000:
		windowMs = hourWindowMs
	case interval < 7*24*60*60_000:
		windowMs = dayWindowMs
	default:
		windowMs = weekWindowMs
	}
	half := interval / 2
	if half < 0 {
		half = 0
	}
	return minInt64(windowMs, half)
}

// PassiveScheduleOffsetMs ports passiveScheduleOffsetMs with an injectable
// random source (Node defaults to Math.random). random nil → time-seeded
// fallback that still produces the bounded offset contract.
func PassiveScheduleOffsetMs(intervalMs int64, random func() float64) int64 {
	windowMs := PassiveScheduleJitterWindowMs(intervalMs)
	if windowMs <= 0 {
		return 0
	}
	sampled := math.NaN()
	if random != nil {
		sampled = random()
	}
	unit := 0.0
	if !math.IsNaN(sampled) && !math.IsInf(sampled, 0) {
		unit = math.Min(1, math.Max(0, sampled))
	}
	offset := int64(math.Min(float64(windowMs), math.Floor(unit*float64(windowMs*2+1))-float64(windowMs)))
	if offset == 0 {
		return 1
	}
	return offset
}

// PassiveScheduleDelayMs ports passiveScheduleDelayMs: strictly positive
// interval plus a fresh bounded symmetric offset.
func PassiveScheduleDelayMs(intervalMs int64, random func() float64) int64 {
	normalized := intervalMs
	if normalized < 1 {
		normalized = 1
	}
	delay := normalized + PassiveScheduleOffsetMs(normalized, random)
	if delay < 1 {
		return 1
	}
	return delay
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// normalizePositiveInteger ports normalizePositiveInteger(value, fallback, min, max).
func normalizePositiveInteger(value *int, fallback, min, max int) int {
	if value == nil {
		return fallback
	}
	return maxInt(min, minInt(max, *value))
}

func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func clampInt64(value, min, max int64) int64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
