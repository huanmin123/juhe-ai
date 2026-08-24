// Package schedulejitter contains the cross-process passive scheduling policy.
// Lease renewal, request deadlines, heartbeats, and event-driven recovery are
// intentionally excluded; passive scans and periodic maintenance use it.
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
	if maximum := interval / 2; window > maximum {
		window = maximum
	}
	return window
}

func Offset(interval time.Duration) time.Duration {
	window := Window(interval)
	if window <= 0 {
		return 0
	}
	offset := time.Duration(rand.Int63n(int64(window)*2+1)) - window
	if offset == 0 {
		return time.Millisecond
	}
	return offset
}

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
