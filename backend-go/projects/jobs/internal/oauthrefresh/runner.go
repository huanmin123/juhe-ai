package oauthrefresh

import (
	"context"
	"log/slog"
	"math/rand"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/schedulejitter"
)

// Runner owns the J4 scheduling lifecycle: one lease-free in-process ticker per
// job (the jobs process holds the scheduled-job lease upstream), with the
// Node scheduler parameters carried over — passive schedule jitter, failure
// backoff with exponential ceiling and per-run timeouts.
type Runner struct {
	name   string
	task   func(ctx context.Context) error
	cfg    RunnerConfig
	clock  Clock
	logger *slog.Logger

	random    func() float64
	consecFai int
}

// RunnerConfig mirrors the scheduler.schedule parameters of the four jobs.
type RunnerConfig struct {
	// Interval is the schedule period
	// (oauthAccessTokenRefreshIntervalSeconds etc.).
	Interval time.Duration
	// InitialDelay delays the first run.
	InitialDelay time.Duration
	// RunTimeout bounds one run (openai refresh 90s, sweep lease 2min…).
	RunTimeout time.Duration
	// FailureBackoffBase/Max mirror failureBackoff {baseMs, maxMs}.
	FailureBackoffBase time.Duration
	FailureBackoffMax  time.Duration
	// PassiveJitter applies the shared passive schedule deviation policy.
	PassiveJitter bool
}

// NewRunner wires one scheduled job.
func NewRunner(name string, cfg RunnerConfig, task func(ctx context.Context) error, clock Clock, logger *slog.Logger) *Runner {
	if clock == nil {
		clock = SystemClock()
	}
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.RunTimeout <= 0 {
		cfg.RunTimeout = 0
	}
	if cfg.FailureBackoffBase <= 0 {
		cfg.FailureBackoffBase = 10 * time.Second
	}
	if cfg.FailureBackoffMax < cfg.FailureBackoffBase {
		cfg.FailureBackoffMax = 5 * time.Minute
	}
	return &Runner{name: name, task: task, cfg: cfg, clock: clock, logger: logger, random: rand.Float64}
}

// failureBackoffDelayMs mirrors failureBackoffDelayMs: exponential ceiling
// with a random fraction of [0, ceiling], computed at millisecond granularity
// exactly like the Node scheduler.
func (r *Runner) failureBackoffDelay() time.Duration {
	exponent := r.consecFai - 1
	if exponent < 0 {
		exponent = 0
	}
	if exponent > 30 {
		exponent = 30
	}
	ceiling := r.cfg.FailureBackoffBase * (1 << uint(exponent))
	if ceiling > r.cfg.FailureBackoffMax {
		ceiling = r.cfg.FailureBackoffMax
	}
	fraction := r.random()
	if fraction >= 1 {
		return ceiling
	}
	ceilingMs := ceiling.Milliseconds()
	return time.Duration(fraction*float64(ceilingMs+1)) * time.Millisecond
}

// nextDelay renders the next schedule delay: passive jitter shifts every
// delayed run by the shared window offset.
func (r *Runner) nextDelay(lastFailed bool) time.Duration {
	if lastFailed {
		return r.failureBackoffDelay()
	}
	delay := r.cfg.Interval
	if r.cfg.PassiveJitter {
		delay = schedulejitter.Delay(delay)
	}
	return delay
}

// Run executes the schedule loop until the context is cancelled. Errors from
// a run are logged and consumed by the failure backoff; only context
// cancellation ends the loop.
func (r *Runner) Run(ctx context.Context) error {
	delay := r.cfg.InitialDelay
	if r.cfg.PassiveJitter && delay > 0 {
		delay = schedulejitter.Delay(delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			runErr := r.runOnce(ctx)
			failed := runErr != nil
			if failed {
				r.consecFai++
			} else {
				r.consecFai = 0
			}
			timer.Reset(r.nextDelay(failed))
		}
	}
}

// RunOnce executes exactly one run (explicit maintenance entry, tests).
func (r *Runner) RunOnce(ctx context.Context) error { return r.runOnce(ctx) }

func (r *Runner) runOnce(ctx context.Context) error {
	attemptCtx := ctx
	cancel := func() {}
	if r.cfg.RunTimeout > 0 {
		var cancelTimeout context.CancelFunc
		attemptCtx, cancelTimeout = context.WithTimeout(ctx, r.cfg.RunTimeout)
		cancel = cancelTimeout
	}
	defer cancel()
	err := r.task(attemptCtx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.logger.Error("后台任务执行失败", "event", "background_job_failed", "jobName", r.name, "error", err)
		return nil
	}
	return nil
}
