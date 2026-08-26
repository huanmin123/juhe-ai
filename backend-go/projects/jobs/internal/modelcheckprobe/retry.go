package modelcheckprobe

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"time"
)

var DefaultAttemptTimeouts = []time.Duration{10 * time.Second, 20 * time.Second, 30 * time.Second}

type RetryOptions struct {
	AttemptTimeouts []time.Duration
	Delay           func(context.Context) error
	Now             func() time.Time
}

// ExecuteWithRetry retries only missing/HTTP failures. A 200 is definitive:
// protocol or model-content failure becomes quality evidence and is not sent
// to the upstream again.
func ExecuteWithRetry(ctx context.Context, request Request, options TransportOptions, retry RetryOptions) (ProbeResult, error) {
	timeouts := retry.AttemptTimeouts
	if len(timeouts) == 0 {
		timeouts = DefaultAttemptTimeouts
	}
	now := retry.Now
	if now == nil {
		now = time.Now
	}
	started := now()
	attempts := make([]ProbeResult, 0, len(timeouts))
	for index, timeout := range timeouts {
		if timeout <= 0 {
			return ProbeResult{}, errors.New("model check retry timeout must be positive")
		}
		if index > 0 {
			delay := retry.Delay
			if delay == nil {
				delay = defaultRetryDelay
			}
			if err := delay(ctx); err != nil {
				return attachRetry(attempts, started, now, len(timeouts)), err
			}
		}
		attemptOptions := options
		attemptOptions.Timeout = timeout
		result, err := ExecuteRequest(ctx, request, attemptOptions)
		if err != nil {
			return attachRetry(attempts, started, now, len(timeouts)), err
		}
		attempts = append(attempts, result)
		if result.HTTPStatusCode == http.StatusOK || index == len(timeouts)-1 {
			return attachRetry(attempts, started, now, len(timeouts)), nil
		}
	}
	return attachRetry(attempts, started, now, len(timeouts)), nil
}

func attachRetry(attempts []ProbeResult, started time.Time, now func() time.Time, maxAttempts int) ProbeResult {
	if len(attempts) == 0 {
		return ProbeResult{}
	}
	result := attempts[len(attempts)-1]
	result.DurationMS = now().Sub(started).Milliseconds()
	if len(attempts) == 1 {
		return result
	}
	statuses := make([]int, 0, len(attempts))
	for _, attempt := range attempts {
		statuses = append(statuses, attempt.HTTPStatusCode)
	}
	result.RetryAttemptCount = len(attempts) - 1
	result.RetryMaxAttempts = maxAttempts
	result.AttemptStatusCodes = statuses
	return result
}

func RetryEvidence(result ProbeResult) map[string]any {
	if result.RetryAttemptCount <= 0 {
		return nil
	}
	return map[string]any{"retryAttemptCount": result.RetryAttemptCount, "retryMaxAttempts": result.RetryMaxAttempts, "attemptStatusCodes": append([]int(nil), result.AttemptStatusCodes...)}
}

func defaultRetryDelay(ctx context.Context) error {
	delay := time.Second + time.Duration(rand.IntN(2001))*time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
