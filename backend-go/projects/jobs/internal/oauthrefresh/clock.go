package oauthrefresh

import (
	"strings"
	"time"
)

// Clock mirrors the injectable time sources the Node services take for tests
// (clock parameter / Date.now) and keeps kill-restart idempotency tests
// deterministic.
type Clock interface {
	Now() time.Time
}

// ClockFunc adapts a function to Clock.
type ClockFunc func() time.Time

// Now implements Clock.
func (f ClockFunc) Now() time.Time { return f() }

// systemClock is the production clock.
type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// SystemClock returns the wall-clock Clock.
func SystemClock() Clock { return systemClock{} }

// isoMillis mirrors Node new Date(...).toISOString() millisecond precision
// (shared/rfc3339.ts rendering).
func isoMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000") + "Z"
}

// canonicalRFC3339 mirrors canonicalizeRfc3339Instant: parse an RFC3339
// instant (Z or numeric offset) and re-render millisecond UTC. ok=false when
// the value is not a valid instant.
func canonicalRFC3339(value string) (string, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return "", false
	}
	return isoMillis(parsed), true
}

// rfc3339Millis mirrors rfc3339InstantMilliseconds: epoch milliseconds of an
// RFC3339 instant, ok=false when unparsable.
func rfc3339Millis(value string) (int64, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return 0, false
	}
	return parsed.UnixMilli(), true
}
