package gatewayusage

import (
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/timefmt"
)

// RFC3339 instant handling mirroring backend/src/shared/rfc3339.ts: the
// offset is mandatory and bare date-times are never guessed against the
// local zone.
// 解析/规范化/毫秒换算收敛到 shared/platform/timefmt（观测等价：消费方
// 只取 Y/M/D、毫秒字符串与 UnixMilli，亚毫秒小数不进位）。

// parseRFC3339Instant mirrors parseRfc3339Instant.
func parseRFC3339Instant(value string) (time.Time, bool) {
	return timefmt.ParseRFC3339Instant(value)
}

// canonicalizeRFC3339Instant mirrors canonicalizeRfc3339Instant.
func canonicalizeRFC3339Instant(value string) (string, bool) {
	return timefmt.CanonicalizeRFC3339Instant(value)
}

// requiredRFC3339Instant mirrors requiredRfc3339Instant, including the
// Chinese error copy.
func requiredRFC3339Instant(value string, label string) (string, error) {
	return timefmt.RequiredRFC3339Instant(value, label)
}

// rfc3339InstantMilliseconds mirrors rfc3339InstantMilliseconds.
func rfc3339InstantMilliseconds(value string) (int64, bool) {
	return timefmt.RFC3339Milliseconds(value)
}
