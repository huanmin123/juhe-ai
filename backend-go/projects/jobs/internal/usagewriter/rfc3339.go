package usagewriter

import (
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/timefmt"
)

// RFC3339 instant handling mirroring backend/src/shared/rfc3339.ts (same
// implementation as gateway/internal/gatewayusage/rfc3339.go; duplicated
// because the gateway and jobs Go modules cannot import each other).
// 解析/规范化/毫秒换算收敛到 shared/platform/timefmt（观测等价：消费方
// 只取 Y/M/D、毫秒字符串与 UnixMilli，亚毫秒小数不进位）。

// timeRFC3339Millis is the ISO string format Node produces with
// new Date(...).toISOString(): millisecond precision, Z suffix.
const timeRFC3339Millis = "2006-01-02T15:04:05.000Z07:00"

// parseRFC3339Instant mirrors parseRfc3339Instant: the offset is mandatory
// and bare date-times are never guessed against the local zone.
func parseRFC3339Instant(value string) (time.Time, bool) {
	return timefmt.ParseRFC3339Instant(value)
}

// daysInMonth 供包内测试直测 12 月分支保留。
func daysInMonth(year, month int) int {
	if month == 12 {
		return 31
	}
	firstOfNext := time.Date(year, time.Month(month+1), 1, 0, 0, 0, 0, time.UTC)
	return firstOfNext.AddDate(0, 0, -1).Day()
}

// canonicalizeRFC3339Instant mirrors canonicalizeRfc3339Instant.
func canonicalizeRFC3339Instant(value string) (string, bool) {
	return timefmt.CanonicalizeRFC3339Instant(value)
}

// requiredRFC3339Instant mirrors requiredRfc3339Instant, including the
// Chinese error copy (逐字对齐 Node).
func requiredRFC3339Instant(value string, label string) (string, error) {
	return timefmt.RequiredRFC3339Instant(value, label)
}

// rfc3339InstantMilliseconds mirrors rfc3339InstantMilliseconds.
func rfc3339InstantMilliseconds(value string) (int64, bool) {
	return timefmt.RFC3339Milliseconds(value)
}
