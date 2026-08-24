package schedulejitter

import (
	"testing"
	"time"
)

func TestWindowsFollowGlobalPolicy(t *testing.T) {
	tests := []struct {
		interval string
		want     int64
	}{
		{"30s", 15_000},
		{"1m", 30_000},
		{"10m", 30_000},
		{"1h", 30 * 60_000},
		{"1d", 60 * 60_000},
		{"7d", 8 * 60 * 60_000},
	}
	for _, tt := range tests {
		interval, err := parseDuration(tt.interval)
		if err != nil {
			t.Fatal(err)
		}
		if got := Window(interval).Milliseconds(); got != tt.want {
			t.Fatalf("Window(%s)=%dms, want %dms", tt.interval, got, tt.want)
		}
	}
}

func TestDelayIsBoundedAndNotExact(t *testing.T) {
	interval := timeMinute(10)
	window := Window(interval)
	for i := 0; i < 1000; i++ {
		delay := Delay(interval)
		if delay < interval-window || delay > interval+window || delay == interval {
			t.Fatalf("delay %s outside non-exact bound [%s,%s]", delay, interval-window, interval+window)
		}
	}
}

// Small local helpers keep this package test independent of parsing APIs.
func parseDuration(value string) (duration, error) {
	switch value {
	case "30s":
		return 30 * durationSecond, nil
	case "1m":
		return 60 * durationSecond, nil
	case "10m":
		return 10 * 60 * durationSecond, nil
	case "1h":
		return 60 * 60 * durationSecond, nil
	case "1d":
		return 24 * 60 * 60 * durationSecond, nil
	case "7d":
		return 7 * 24 * 60 * 60 * durationSecond, nil
	default:
		return 0, errUnknownDuration{}
	}
}

type duration = time.Duration

const durationSecond = time.Second

func timeMinute(minutes int) time.Duration { return time.Duration(minutes) * time.Minute }

type errUnknownDuration struct{}

func (errUnknownDuration) Error() string { return "unknown duration" }
