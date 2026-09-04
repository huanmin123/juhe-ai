package gatewaycircuit

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

type fakeTimerClock struct {
	mu      sync.Mutex
	nowMs   int64
	timers  []*fakeTimer
	waiters []chan struct{}
}

type fakeTimer struct {
	clock *fakeTimerClock
	due   int64
	fire  func()
	done  chan struct{}
}

func (c *fakeTimerClock) now() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nowMs
}

func (c *fakeTimerClock) newTimer(delay time.Duration) (<-chan struct{}, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeTimer{clock: c, due: c.nowMs + durationToMs(delay), done: make(chan struct{})}
	c.timers = append(c.timers, timer)
	return timer.done, func() {}
}

// advance walks time forward by deltaMs total and keeps firing timers that
// became due, including follow-up timers scheduled asynchronously by fired
// callbacks.
func (c *fakeTimerClock) advance(deltaMs int64) {
	c.mu.Lock()
	c.nowMs += deltaMs
	c.mu.Unlock()
	fired := true
	for fired {
		fired = false
		c.mu.Lock()
		var due []*fakeTimer
		for _, timer := range c.timers {
			if timer.due <= c.nowMs {
				due = append(due, timer)
			}
		}
		if len(due) > 0 {
			fired = true
			for _, timer := range due {
				// Closing done is what the coordinator's goroutine waits on.
				close(timer.done)
			}
			var remaining []*fakeTimer
			for _, timer := range c.timers {
				if timer.due > c.nowMs {
					remaining = append(remaining, timer)
				}
			}
			c.timers = remaining
		}
		c.mu.Unlock()
		if fired {
			// Give the coordinator's scheduler goroutine a chance to run and
			// schedule follow-up timers.
			time.Sleep(2 * time.Millisecond)
		}
	}
}

func waitForWaiters(t *testing.T, coordinator *WaitCoordinator, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if coordinator.Snapshot().WaiterCount >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("waiters never reached %d: %+v", want, coordinator.Snapshot())
}

func TestNextRecoverableWaitDelay(t *testing.T) {
	tests := []struct {
		name                  string
		nextRetryAfterMs      int64
		hasRetryAfter         bool
		remainingMs           int64
		checkIntervalMs       int64
		dueRetryDelayMs       int64
		waitWithoutRetryAfter bool
		wantDelay             int64
		wantSkip              string
		wantWait              bool
	}{
		{name: "no retry time and not allowed", remainingMs: 1000, checkIntervalMs: 500, dueRetryDelayMs: 250, wantSkip: WaitSkippedNoRetryTime},
		{name: "no retry time but allowed", remainingMs: 1000, checkIntervalMs: 500, dueRetryDelayMs: 250, waitWithoutRetryAfter: true, wantDelay: 500, wantWait: true},
		{name: "retry after beyond window", nextRetryAfterMs: 2000, hasRetryAfter: true, remainingMs: 1000, checkIntervalMs: 500, dueRetryDelayMs: 250, wantSkip: WaitSkippedRetryAfterExceedsWindow},
		{name: "due retry uses the floor", nextRetryAfterMs: 0, hasRetryAfter: true, remainingMs: 1000, checkIntervalMs: 500, dueRetryDelayMs: 250, wantDelay: 250, wantWait: true},
		{name: "clamped by remaining", nextRetryAfterMs: 5000, hasRetryAfter: true, remainingMs: 800, checkIntervalMs: 5000, dueRetryDelayMs: 250, waitWithoutRetryAfter: true, wantDelay: 800, wantWait: true},
		{name: "minimum 50ms", nextRetryAfterMs: 10, hasRetryAfter: true, remainingMs: 1000, checkIntervalMs: 500, dueRetryDelayMs: 250, wantDelay: 50, wantWait: true},
		{name: "zero remaining", remainingMs: 0, checkIntervalMs: 500, dueRetryDelayMs: 250, wantSkip: WaitSkippedRetryAfterExceedsWindow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delay, skip, wait := nextRecoverableWaitDelayMs(waitDelayInput{
				nextRetryAfterMs: tt.nextRetryAfterMs, hasRetryAfter: tt.hasRetryAfter,
				remainingMs: tt.remainingMs, checkIntervalMs: tt.checkIntervalMs,
				dueRetryDelayMs: tt.dueRetryDelayMs, waitWithoutRetryAfter: tt.waitWithoutRetryAfter,
			})
			if wait != tt.wantWait || skip != tt.wantSkip || (wait && delay != tt.wantDelay) {
				t.Fatalf("got (%d, %s, %v), want (%d, %s, %v)", delay, skip, wait, tt.wantDelay, tt.wantSkip, tt.wantWait)
			}
		})
	}
}

func TestWaitEngineReadyAndSkipPaths(t *testing.T) {
	audit := &captureAudit{}
	now := int64(1000)
	engine := func(input waitInput) waitOutcome {
		input.auditCapture = audit
		input.now = func() int64 { return now }
		outcome, engineErr := waitForRecoverableUnavailableState(context.Background(), input)
		if engineErr != nil {
			t.Fatalf("engine error: %v", engineErr)
		}
		return outcome
	}
	outcome := engine(waitInput{
		reason: "r", scopeKey: "scope",
		isReady:          func() bool { return true },
		nextRetryAfterMs: func() (int64, bool) { return 0, false },
		maxWaitMs:        30_000, checkIntervalMs: 5_000,
	})
	if !outcome.ready || outcome.checkCount != 0 {
		t.Fatalf("ready outcome = %+v", outcome)
	}
	// Deadline passed before starting.
	outcome = engine(waitInput{
		reason: "r", scopeKey: "scope",
		isReady:          func() bool { return false },
		nextRetryAfterMs: func() (int64, bool) { return 0, false },
		maxWaitMs:        30_000, checkIntervalMs: 5_000,
		deadlineAtMs: int64Ptr(500),
	})
	if outcome.ready || !outcome.timedOut || outcome.skippedReason != WaitSkippedDeadlineExceeded {
		t.Fatalf("deadline outcome = %+v", outcome)
	}
	// retry_after beyond the window without any prior check.
	outcome = engine(waitInput{
		reason: "r", scopeKey: "scope",
		isReady:          func() bool { return false },
		nextRetryAfterMs: func() (int64, bool) { return 60_000, true },
		maxWaitMs:        30_000, checkIntervalMs: 5_000,
	})
	if outcome.timedOut || outcome.skippedReason != WaitSkippedRetryAfterExceedsWindow {
		t.Fatalf("exceeds-window outcome = %+v", outcome)
	}
	// The immediate-ready path skips the wait metadata; the other two log the
	// scheduling metadata before their result.
	if len(audit.labels) != 4 || audit.labels[2] != "recoverable_unavailable_wait" {
		t.Fatalf("audit labels = %v", audit.labels)
	}
}

type captureAudit struct {
	labels []string
}

func (a *captureAudit) AddGatewayMetadata(label string, metadata map[string]any) {
	a.labels = append(a.labels, label)
}

func TestWaitEngineRefreshesThroughCoordinator(t *testing.T) {
	clock := &fakeTimerClock{nowMs: 0}
	coordinator := NewWaitCoordinator(WaitCoordinatorOptions{NewTimer: clock.newTimer, Now: clock.now})
	ready := false
	checks := 0
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// The engine runs in a goroutine; the timer clock drives turns.
	}()
	// Run the engine synchronously but drive the timer from another
	// goroutine because the engine blocks on WaitForTurn.
	done := make(chan waitOutcome, 1)
	go func() {
		outcome, err := waitForRecoverableUnavailableState(context.Background(), waitInput{
			reason: "local_account_suppression", scopeKey: "scope",
			refresh: func(ctx context.Context) error {
				checks++
				if checks >= 2 {
					ready = true
				}
				return nil
			},
			isReady:          func() bool { return ready },
			nextRetryAfterMs: func() (int64, bool) { return 0, false },
			waitWithoutRetryAfter: true,
			maxWaitMs:        30_000, checkIntervalMs: 100,
			coordinator: coordinator,
			now:         clock.now,
		})
		if err != nil {
			t.Errorf("engine: %v", err)
		}
		done <- outcome
	}()
	// Pump the fake clock until the engine finishes.
	for {
		select {
		case outcome := <-done:
			if !outcome.ready || outcome.checkCount != 2 {
				t.Fatalf("outcome = %+v", outcome)
			}
			return
		case <-time.After(50 * time.Millisecond):
			clock.advance(200)
		}
	}
}

func TestRouteCoordinationBudgetIntegration(t *testing.T) {
	budget, err := gatewayrouting.NewRouteCoordinationBudget(gatewayrouting.RouteCoordinationBudgetOptions{
		RequestID: "req-1", BudgetMs: int64Ptr(10_000), Now: func() int64 { return 0 },
	})
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	clock := &fakeTimerClock{nowMs: 0}
	coordinator := NewWaitCoordinator(WaitCoordinatorOptions{NewTimer: clock.newTimer, Now: clock.now})
	done := make(chan waitOutcome, 1)
	go func() {
		outcome, _ := waitForRecoverableUnavailableState(context.Background(), waitInput{
			reason: "r", scopeKey: "scope",
			refresh: func(ctx context.Context) error { return nil },
			isReady:          func() bool { return true },
			nextRetryAfterMs: func() (int64, bool) { return 0, false },
			waitWithoutRetryAfter: true,
			maxWaitMs:        5_000, checkIntervalMs: 100,
			coordinator:             coordinator,
			routeCoordinationBudget: budget,
			now:                     clock.now,
		})
		done <- outcome
	}()
	outcome := <-done
	if !outcome.ready {
		t.Fatalf("ready-first must skip coordination entirely: %+v", outcome)
	}
	_ = time.Second
}
