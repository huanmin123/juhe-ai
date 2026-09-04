package oauthrefresh

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunnerFailureBackoffExponential(t *testing.T) {
	attempts := 0
	clock := &fixedClock{current: defaultNow()}
	runner := NewRunner("test-job", RunnerConfig{
		Interval:           time.Second,
		FailureBackoffBase: 10 * time.Second,
		FailureBackoffMax:  5 * time.Minute,
	}, func(context.Context) error {
		attempts++
		return errors.New("boom")
	}, clock, discardLogger())
	runner.random = func() float64 { return 1 - 1e-9 } // ~ceiling at ms granularity
	// first failure (consecutive=1) → base
	if got := runner.failureBackoffDelay(); got != 10*time.Second {
		t.Fatalf("first backoff=%s", got)
	}
	runner.consecFai = 2
	if got := runner.failureBackoffDelay(); got != 20*time.Second {
		t.Fatalf("second backoff=%s", got)
	}
	runner.consecFai = 6
	// min(maxMs, base*2^5) = min(5min, 320s) clamps to 5min.
	if got := runner.failureBackoffDelay(); got != 5*time.Minute {
		t.Fatalf("sixth backoff=%s", got)
	}
	runner.consecFai = 5
	if got := runner.failureBackoffDelay(); got != 160*time.Second {
		t.Fatalf("fifth backoff=%s", got)
	}
	// ceiling clamps at max
	runner.consecFai = 20
	if got := runner.failureBackoffDelay(); got != 5*time.Minute {
		t.Fatalf("clamped backoff=%s", got)
	}
	if attempts != 0 {
		t.Fatal("task must not run during delay assertions")
	}
}

func TestRunnerRunOnceTimeoutAndErrorSwallowing(t *testing.T) {
	clock := &fixedClock{current: defaultNow()}
	slow := NewRunner("slow-job", RunnerConfig{Interval: time.Second, RunTimeout: 20 * time.Millisecond},
		func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}, clock, discardLogger())
	// The runner consumes the error (scheduler keeps ticking); only ctx
	// cancellation of the parent propagates.
	if err := slow.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once err=%v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := slow.RunOnce(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled parent err=%v", err)
	}
}

func TestRunnerRunLoopSuccessResetsBackoff(t *testing.T) {
	runs := 0
	clock := &fixedClock{current: defaultNow()}
	runner := NewRunner("loop-job", RunnerConfig{
		Interval:           5 * time.Millisecond,
		FailureBackoffBase: time.Millisecond,
		FailureBackoffMax:  2 * time.Millisecond,
	}, func(context.Context) error {
		runs++
		if runs == 1 {
			return errors.New("first run fails")
		}
		return nil
	}, clock, discardLogger())
	runner.random = func() float64 { return 0 }
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = runner.Run(ctx)
	if runs < 2 {
		t.Fatalf("runs=%d", runs)
	}
	// After the successful second run the consecutive failure count resets.
	if runner.consecFai != 0 {
		t.Fatalf("consecutive failures=%d", runner.consecFai)
	}
}

func TestRunnerPassiveJitterBounds(t *testing.T) {
	clock := &fixedClock{current: defaultNow()}
	runner := NewRunner("jitter-job", RunnerConfig{Interval: time.Minute, PassiveJitter: true},
		func(context.Context) error { return nil }, clock, discardLogger())
	runner.random = func() float64 { return 1 }
	delay := runner.nextDelay(false)
	// The shared passive schedule deviation keeps the delay within
	// [interval, interval + window]; the platform helper decides the window.
	if delay <= 0 || delay > 2*time.Minute {
		t.Fatalf("jittered delay=%s", delay)
	}
}
