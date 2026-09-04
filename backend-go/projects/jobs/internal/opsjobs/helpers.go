package opsjobs

import (
	"strings"
	"sync/atomic"
	"time"
)

var randomFallbackCounter atomic.Int64

func stringsSplit(value, sep string) []string {
	return strings.Split(value, sep)
}

func trimSpaces(value string) string {
	return strings.TrimSpace(value)
}

func newTimer(durationMS int64) *time.Timer {
	if durationMS < 1 {
		durationMS = 1
	}
	return time.NewTimer(time.Duration(durationMS) * time.Millisecond)
}

func stopTimer(timer *time.Timer) {
	if timer != nil {
		timer.Stop()
	}
}
