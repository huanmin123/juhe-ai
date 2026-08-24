// Package schedulejitter contains the cross-job passive scheduling contract.
// It intentionally does not apply to leases, queue consumers, projection
// loops, request timeouts, heartbeats, or other control-plane timers.
package schedulejitter

import (
	"math/rand"
	"time"
)

const (
	SubMinuteWindow = 30 * time.Second
	MinuteWindow    = 30 * time.Second
	HourWindow      = 30 * time.Minute
	DayWindow       = time.Hour
	WeekWindow      = 8 * time.Hour
)

// Window returns the symmetric jitter window for a passive interval.
func Window(interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	var window time.Duration
	switch {
	case interval < time.Minute:
		window = interval / 2
		if window > SubMinuteWindow {
			window = SubMinuteWindow
		}
	case interval < time.Hour:
		window = MinuteWindow
	case interval < 24*time.Hour:
		window = HourWindow
	case interval < 7*24*time.Hour:
		window = DayWindow
	default:
		window = WeekWindow
	}
	// Keep the resulting delay strictly positive.
	if maximum := interval / 2; window > maximum {
		window = maximum
	}
	if window < 0 {
		return 0
	}
	return window
}

// Offset returns a fresh symmetric offset for one passive run. The top-level
// math/rand functions are concurrency-safe and are automatically seeded by
// supported Go versions.
func Offset(interval time.Duration) time.Duration {
	window := Window(interval)
	if window <= 0 {
		return 0
	}
	span := int64(window)*2 + 1
	offset := time.Duration(rand.Int63n(span)) - window
	if offset == 0 {
		return time.Millisecond
	}
	return offset
}

// Delay adds a fresh offset while keeping a positive delay.
func Delay(interval time.Duration) time.Duration {
	if interval <= 0 {
		interval = time.Millisecond
	}
	delay := interval + Offset(interval)
	if delay < time.Millisecond {
		return time.Millisecond
	}
	return delay
}
