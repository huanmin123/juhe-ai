package modelcheckprobe

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// DefaultRetryAttempts is the total number of transport attempts, including
// the first request. Keep this aligned with the Node J3b oracle.
const DefaultRetryAttempts = 5

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
	timeout := 30 * time.Second
	if profile == "quick" {
		timeout = 15 * time.Second
	}
	timeouts := make([]time.Duration, DefaultRetryAttempts)
	for index := range timeouts {
		timeouts[index] = timeout
	}
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
		if result.Success || index == len(timeouts)-1 {
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
	// A model-scoped rejection is definitive for this model after the retry
	// boundary, including providers that incorrectly return it as HTTP 200.
	if IsModelUnavailable(result, result.ExpectedModel) {
		return true
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
	delays := []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second, 60 * time.Second}
	delay := delays[len(delays)-1]
	if attempt > 0 && attempt <= len(delays) {
		delay = delays[attempt-1]
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
