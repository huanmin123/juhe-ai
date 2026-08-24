package schedulejitter

import (
	"testing"
	"time"
)

func TestGlobalPolicy(t *testing.T) {
	tests := []struct{ interval, want time.Duration }{
		{30 * time.Second, 15 * time.Second}, {time.Minute, 30 * time.Second},
		{10 * time.Minute, 30 * time.Second}, {time.Hour, 30 * time.Minute},
		{24 * time.Hour, time.Hour}, {7 * 24 * time.Hour, 8 * time.Hour},
	}
	for _, test := range tests {
		if got := Window(test.interval); got != test.want {
			t.Fatalf("Window(%s)=%s, want %s", test.interval, got, test.want)
		}
	}
}

func TestDelayBoundedAndNonExact(t *testing.T) {
	interval := 10 * time.Minute
	window := Window(interval)
	for i := 0; i < 1000; i++ {
		delay := Delay(interval)
		if delay < interval-window || delay > interval+window || delay == interval {
			t.Fatalf("Delay=%s outside [%s,%s]", delay, interval-window, interval+window)
		}
	}
}
