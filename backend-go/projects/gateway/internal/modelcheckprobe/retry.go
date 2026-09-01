package modelcheckprobe

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// DefaultRetryAttempts is the total number of transport attempts, including
// the first request. Keep this aligned with the Node J3b oracle.
const DefaultRetryAttempts = 3

type RetryOptions struct {
	AttemptTimeouts []time.Duration
	Delay           func(context.Context) error
	Now             func() time.Time
}

// DefaultRetryOptions is the bounded production policy used by Gateway.
func DefaultRetryOptions() RetryOptions {
	return RetryOptionsForProfile("full")
}

func RetryOptionsForProfile(profile string) RetryOptions {
	// Node's diagnostic oracle uses one shared per-attempt schedule for quick
	// and full profiles. Keep the schedule explicit so a slow reasoning turn
	// gets the same bounded 10s/20s/30s retry budget on both paths.
	_ = profile
	timeouts := []time.Duration{10 * time.Second, 20 * time.Second, 30 * time.Second}
	return RetryOptions{AttemptTimeouts: timeouts}
}

// ExecuteWithRetry retries transport and non-200 responses. A 200 response
// is definitive even when its content fails a quality assertion.
func ExecuteWithRetry(ctx context.Context, request Request, options Options, retry RetryOptions) (Result, error) {
	if len(retry.AttemptTimeouts) == 0 && retry.Delay == nil && retry.Now == nil {
		return Execute(ctx, request, options)
	}
	timeouts := retry.AttemptTimeouts
	if len(timeouts) == 0 {
		// A zero RetryOptions is the suite's ordinary direct-probe mode. Keep
		// it single-attempt and deterministic; production retry policy must be
		// supplied explicitly by the owner instead of silently adding 60s of
		// backoff to every diagnostic run.
		attemptTimeout := options.Timeout
		if attemptTimeout <= 0 {
			attemptTimeout = DefaultTimeout
		}
		timeouts = []time.Duration{attemptTimeout}
	}
	now := retry.Now
	if now == nil {
		now = time.Now
	}
	started := now()
	attempts := make([]Result, 0, len(timeouts))
	waits := make([]time.Duration, 0, len(timeouts)-1)
	for index, timeout := range timeouts {
		if timeout <= 0 {
			return Result{}, errors.New("J3b retry timeout must be positive")
		}
		if index > 0 {
			delay := retry.Delay
			var err error
			if delay != nil {
				waitStarted := now()
				err = delay(ctx)
				waits = append(waits, now().Sub(waitStarted))
			} else {
				waitStarted := now()
				err = defaultRetryDelay(ctx, index)
				waits = append(waits, now().Sub(waitStarted))
			}
			if err != nil {
				return attachRetry(attempts, waits, started, now, len(timeouts)), err
			}
		}
		attemptOptions := options
		attemptOptions.Timeout = timeout
		attemptStarted := now()
		result, err := Execute(ctx, request, attemptOptions)
		if err != nil {
			return attachRetry(attempts, waits, started, now, len(timeouts)), err
		}
		result.AttemptDetails = []AttemptDetail{{StartedAt: attemptStarted, Duration: result.Duration, HTTPStatus: result.HTTPStatus, Error: result.ErrorMessage}}
		attempts = append(attempts, result)
		// HTTP 200 is definitive quality evidence even when the body carries a
		// provider error envelope (for example model_not_found). Node does not
		// retry such responses; only transport/non-200 failures consume retries.
		if result.Success || result.HTTPStatus == http.StatusOK || index == len(timeouts)-1 {
			return attachRetry(attempts, waits, started, now, len(timeouts)), nil
		}
	}
	return attachRetry(attempts, waits, started, now, len(timeouts)), nil
}

// isTerminalProbeFailure reports whether a failed probe has exhausted the
// retry boundary and therefore must stop the containing probe family. A
// zero-valued RetryOptions intentionally means one direct attempt, so a
// non-200 result in that mode is terminal as well.
func isTerminalProbeFailure(result Result) bool {
	if result.Success {
		return false
	}
	if result.HTTPStatus == http.StatusOK {
		return false
	}
	if result.RetryMaxAttempts <= 0 {
		return true
	}
	attemptCount := result.RetryAttemptCount + 1
	if len(result.AttemptStatusCodes) > attemptCount {
		attemptCount = len(result.AttemptStatusCodes)
	}
	return attemptCount >= result.RetryMaxAttempts
}

func attachRetry(attempts []Result, waits []time.Duration, started time.Time, now func() time.Time, maxAttempts int) Result {
	if len(attempts) == 0 {
		return Result{}
	}
	result := attempts[len(attempts)-1]
	result.Duration = now().Sub(started)
	statuses := make([]int, 0, len(attempts))
	details := make([]AttemptDetail, 0, len(attempts))
	for _, attempt := range attempts {
		statuses = append(statuses, attempt.HTTPStatus)
		details = append(details, attempt.AttemptDetails...)
	}
	result.RetryAttemptCount = len(attempts) - 1
	result.RetryMaxAttempts = maxAttempts
	result.AttemptStatusCodes = statuses
	result.RetryWaitDurations = append([]time.Duration(nil), waits...)
	result.AttemptDetails = details
	return result
}

func defaultRetryDelay(ctx context.Context, attempt int) error {
	// Node waits a bounded random 1-3 seconds between diagnostic attempts.
	// A deterministic 2-second midpoint keeps Go tests reproducible while
	// preserving the same retry envelope and avoiding the old 5/15/30/60s
	// backoff that could outlive the claim lease.
	_ = attempt
	delay := 2 * time.Second
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
