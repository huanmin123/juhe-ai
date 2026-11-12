package oauthrefresh

import (
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/timeclock"
	"github.com/huanminabc/juhe-ai/backend-go-platform/timefmt"
)

// Clock mirrors the injectable time sources the Node services take for tests
// (clock parameter / Date.now) and keeps kill-restart idempotency tests
// deterministic. 统一签名收敛到 shared/platform/timeclock（类型别名）。
type Clock = timeclock.Clock

// ClockFunc adapts a function to Clock（nil 时退回 SystemClock）。
type ClockFunc = timeclock.ClockFunc

// systemClock is the production clock.
type systemClock = timeclock.SystemClock

// SystemClock returns the wall-clock Clock.
func SystemClock() Clock { return systemClock{} }

// isoMillis mirrors Node new Date(...).toISOString() millisecond precision
// (shared/rfc3339.ts rendering).
func isoMillis(t time.Time) string {
	return timefmt.FormatRFC3339Millis(t)
}

// canonicalRFC3339 mirrors canonicalizeRfc3339Instant: parse an RFC3339
// instant (Z or numeric offset) and re-render millisecond UTC. ok=false when
// the value is not a valid instant.
func canonicalRFC3339(value string) (string, bool) {
	return timefmt.CanonicalizeRFC3339Instant(value)
}

// rfc3339Millis mirrors rfc3339InstantMilliseconds: epoch milliseconds of an
// RFC3339 instant, ok=false when unparsable.
func rfc3339Millis(value string) (int64, bool) {
	return timefmt.RFC3339Milliseconds(value)
}
