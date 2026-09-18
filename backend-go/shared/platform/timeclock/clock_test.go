package timeclock

import (
	"testing"
	"time"
)

func TestSystemClockNow(t *testing.T) {
	before := time.Now()
	got := SystemClock{}.Now()
	after := time.Now()
	if got.Before(before.Add(-time.Second)) || got.After(after.Add(time.Second)) {
		t.Fatalf("SystemClock.Now 应返回当前墙钟附近: %v", got)
	}
}

func TestClockFuncNonNil(t *testing.T) {
	want := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)
	var clock Clock = ClockFunc(func() time.Time { return want })
	if !clock.Now().Equal(want) {
		t.Fatalf("ClockFunc 应返回注入时间: %v", clock.Now())
	}
}

func TestClockFuncNilFallsBackToSystemClock(t *testing.T) {
	var clock ClockFunc
	got := clock.Now()
	if got.IsZero() {
		t.Fatal("nil ClockFunc 应退回 SystemClock 而不是零值")
	}
}
