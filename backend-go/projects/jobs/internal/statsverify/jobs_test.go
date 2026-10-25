package statsverify

import (
	"context"
	"errors"
	"testing"
	"time"
)

// steppingClock advances by a fixed step on every Sleep, letting tests drive
// the 5s aggregation time budget without real waiting.
type steppingClock struct {
	FixedClock
	Step time.Duration
}

func (c *steppingClock) Sleep(d time.Duration) {
	c.Current = c.Current.Add(c.Step)
}

func TestBoundPositiveInt(t *testing.T) {
	cases := []struct {
		in, min, max, want int
	}{
		{in: 5, min: 1, max: 10, want: 5},
		{in: 0, min: 1, max: 10, want: 1},
		{in: -3, min: 1, max: 10, want: 1},
		{in: 99, min: 1, max: 10, want: 10},
	}
	for _, tc := range cases {
		if got := boundPositiveInt(tc.in, tc.min, tc.max); got != tc.want {
			t.Fatalf("boundPositiveInt(%d,%d,%d)=%d, want %d", tc.in, tc.min, tc.max, got, tc.want)
		}
	}
}

// allowIngest 恒放行的排干门控桩（Node ensureUsageRecordsSafeForStatsAggregation
// 放行路径）。
type allowIngest struct{}

func (allowIngest) EnsureUsageRecordsIngested(context.Context) error { return nil }

// denyIngest 恒拒绝的排干门控桩（Node 队列未排干失败本轮路径）。
type denyIngest struct{ calls *int }

func (d denyIngest) EnsureUsageRecordsIngested(context.Context) error {
	*d.calls++
	return errors.New("ingest-worker 使用记录队列快照不可用，本轮跳过统计聚合，避免统计游标越过排队记录")
}

