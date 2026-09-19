package gatewayquota

import (
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/timefmt"
)

// RFC3339 instant handling mirroring backend/src/shared/rfc3339.ts: the
// offset is mandatory and bare date-times are never guessed against the
// local zone.
// 解析/规范化/格式化/毫秒换算收敛到 shared/platform/timefmt（观测等价：
// 消费方只取毫秒字符串与 UnixMilli，亚毫秒小数不进位）。

// parseRfc3339Instant mirrors parseRfc3339Instant. ok=false mirrors the
// undefined return.
func parseRfc3339Instant(value string) (time.Time, bool) {
	return timefmt.ParseRFC3339Instant(value)
}

// canonicalizeRfc3339Instant mirrors canonicalizeRfc3339Instant (Node
// toISOString: millisecond precision, Z suffix).
func canonicalizeRfc3339Instant(value string) (string, bool) {
	return timefmt.CanonicalizeRFC3339Instant(value)
}

// formatRFC3339Millis renders an instant like Node's Date#toISOString.
func formatRFC3339Millis(t time.Time) string {
	return timefmt.FormatRFC3339Millis(t)
}

// requiredRfc3339Instant mirrors requiredRfc3339Instant.
func requiredRfc3339Instant(value string, label string) (string, error) {
	return timefmt.RequiredRFC3339Instant(value, label)
}

// rfc3339InstantMilliseconds mirrors rfc3339InstantMilliseconds.
func rfc3339InstantMilliseconds(value string) (int64, bool) {
	return timefmt.RFC3339Milliseconds(value)
}
