package gatewayaccounteffects

import (
	"math"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/schedulejitter"
	"github.com/huanminabc/juhe-ai/backend-go-platform/timeclock"
)

// Clock injects time; tests use a fixed clock. It mirrors the Node
// Date.now()/new Date() reads in the migrated services.
// 统一签名收敛到 shared/platform/timeclock（类型别名，调用点零改动）。
type Clock = timeclock.Clock

// SystemClock is the default wall clock.
type SystemClock = timeclock.SystemClock

// NowMs returns the clock reading in unix milliseconds.
func NowMs(clock Clock) int64 {
	if clock == nil {
		return time.Now().UnixMilli()
	}
	return clock.Now().UnixMilli()
}

// Scheduler arms delayed callbacks (Node setTimeout/setInterval). The real
// implementation uses time.AfterFunc; tests drive a manual scheduler so the
// queue drain, permit renewal and recovery timers stay deterministic.
type Scheduler interface {
	After(delayMs int64, fn func()) SchedulerHandle
}

// SchedulerHandle cancels one scheduled callback (timer.clear()).
type SchedulerHandle interface {
	Cancel()
}

// RealScheduler schedules on the system clock.
type RealScheduler struct{}

// After implements Scheduler.
func (RealScheduler) After(delayMs int64, fn func()) SchedulerHandle {
	delay := time.Duration(delayMs) * time.Millisecond
	if delay < 0 {
		delay = 0
	}
	timer := time.AfterFunc(delay, fn)
	return realHandle{timer: timer}
}

type realHandle struct{ timer *time.Timer }

func (h realHandle) Cancel() { h.timer.Stop() }

// ManualScheduler collects pending callbacks; tests fire them explicitly
// after advancing the fake clock.
type ManualScheduler struct {
	mu    sync.Mutex
	order int
	items map[int]manualEntry
}

type manualEntry struct {
	fn func()
}

// NewManualScheduler returns an empty manual scheduler.
func NewManualScheduler() *ManualScheduler {
	return &ManualScheduler{items: map[int]manualEntry{}}
}

// After implements Scheduler.
func (s *ManualScheduler) After(delayMs int64, fn func()) SchedulerHandle {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.order++
	id := s.order
	s.items[id] = manualEntry{fn: fn}
	return &manualHandle{scheduler: s, id: id}
}

// Pending reports how many callbacks are armed.
func (s *ManualScheduler) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}

// Fire runs every pending callback in scheduling order and returns how many
// ran. Callbacks scheduled while firing stay pending for the next call.
func (s *ManualScheduler) Fire() int {
	s.mu.Lock()
	ids := make([]int, 0, len(s.items))
	for id, entry := range s.items {
		ids = append(ids, id)
		_ = entry
	}
	sortInts(ids)
	pending := make([]func(), 0, len(ids))
	for _, id := range ids {
		if entry, ok := s.items[id]; ok {
			pending = append(pending, entry.fn)
			delete(s.items, id)
		}
	}
	s.mu.Unlock()
	for _, fn := range pending {
		fn()
	}
	return len(pending)
}

func sortInts(values []int) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

type manualHandle struct {
	scheduler *ManualScheduler
	id        int
}

func (h *manualHandle) Cancel() {
	h.scheduler.mu.Lock()
	delete(h.scheduler.items, h.id)
	h.scheduler.mu.Unlock()
}

// FakeClock is a deterministic clock for tests.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewFakeClock starts at the given time.
func NewFakeClock(start time.Time) *FakeClock {
	return &FakeClock{now: start}
}

// Now implements Clock.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// Set moves the clock to an absolute time.
func (c *FakeClock) Set(now time.Time) {
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

// passiveScheduleJitterWindowMs mirrors shared/passive-schedule-jitter.ts.
// 实现收敛到 shared/platform/schedulejitter（全输入域等价，含 interval<1
// 钳制与 ms 整除语义；档位常量随实现移除）。
func passiveScheduleJitterWindowMs(intervalMs int64) int64 {
	return int64(schedulejitter.Window(time.Duration(intervalMs) * time.Millisecond) / time.Millisecond)
}

// passiveScheduleOffsetMs mirrors passiveScheduleOffsetMs with the random
// source injected.
func passiveScheduleOffsetMs(intervalMs int64, random func() float64) int64 {
	windowMs := passiveScheduleJitterWindowMs(intervalMs)
	return passiveScheduleOffsetWithinWindowMs(windowMs, random)
}

func passiveScheduleOffsetWithinWindowMs(windowMs int64, random func() float64) int64 {
	if windowMs <= 0 {
		return 0
	}
	sampled := 0.0
	if random != nil {
		sampled = random()
	}
	if math.IsNaN(sampled) || math.IsInf(sampled, 0) {
		sampled = 0
	}
	if sampled < 0 {
		sampled = 0
	}
	if sampled > 1 {
		sampled = 1
	}
	offset := int64(math.Min(float64(windowMs), math.Floor(sampled*float64(windowMs*2+1))-float64(windowMs)))
	if offset == 0 {
		return 1
	}
	return offset
}

// passiveScheduleDelayMs mirrors passiveScheduleDelayMs: fresh symmetric
// offset with a strictly positive result.
func passiveScheduleDelayMs(intervalMs int64, random func() float64) int64 {
	delay := int64(math.Max(1, math.Trunc(float64(intervalMs))))
	offset := passiveScheduleOffsetMs(intervalMs, random)
	result := delay + offset
	if result < 1 {
		result = 1
	}
	return result
}

// passiveScheduleNotBeforeDelayMs mirrors passiveScheduleNotBeforeDelayMs: a
// fresh delay at or after a hard external deadline.
func passiveScheduleNotBeforeDelayMs(intervalMs int64, random func() float64) int64 {
	interval := int64(math.Max(1, math.Trunc(float64(intervalMs))))
	offset := passiveScheduleOffsetMs(interval, random)
	if offset == 0 {
		return interval + 1
	}
	if offset < 0 {
		offset = -offset
	}
	return interval + offset
}
