package gatewaycandidateclaim

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewayattemptloop"
	platformredis "juhe-ai/backend-go/internal/platform/redis"
)

const (
	defaultRefreshInterval = platformredis.AccountSlotLeaseTTL / 3
	defaultReleaseTimeout  = 5 * time.Second
)

// ErrLeaseLost identifies an attempt whose token-fenced lease could no longer
// be refreshed before the attempt committed a response.
var ErrLeaseLost = errors.New("gateway candidate lease lost")

// ClaimingExecutorOptions configures the final candidate recheck adapter.
// OnReleaseError observes best-effort cleanup failures without changing a
// successfully completed upstream attempt.
type ClaimingExecutorOptions struct {
	Service        *Service
	Inner          gatewayattemptloop.AttemptExecutor
	OnReleaseError func(error)

	// RefreshInterval and ReleaseTimeout are test seams. Zero selects the
	// production defaults of one third of the slot TTL and five seconds.
	RefreshInterval time.Duration
	ReleaseTimeout  time.Duration
}

// ClaimingExecutor claims a fresh candidate immediately before delegating one
// attempt to Inner. It owns no retry loop, listener, or route registration.
type ClaimingExecutor struct {
	service         *Service
	inner           gatewayattemptloop.AttemptExecutor
	onReleaseError  func(error)
	refreshInterval time.Duration
	releaseTimeout  time.Duration
}

func NewClaimingExecutor(options ClaimingExecutorOptions) (*ClaimingExecutor, error) {
	if options.Service == nil {
		return nil, fmt.Errorf("gateway candidate claim service is required")
	}
	if options.Inner == nil {
		return nil, fmt.Errorf("gateway candidate claim inner executor is required")
	}
	if options.OnReleaseError == nil {
		return nil, fmt.Errorf("gateway candidate claim release error reporter is required")
	}
	refreshInterval := options.RefreshInterval
	if refreshInterval == 0 {
		refreshInterval = defaultRefreshInterval
	}
	if refreshInterval <= 0 {
		return nil, fmt.Errorf("gateway candidate claim refresh interval must be positive")
	}
	releaseTimeout := options.ReleaseTimeout
	if releaseTimeout == 0 {
		releaseTimeout = defaultReleaseTimeout
	}
	if releaseTimeout <= 0 {
		return nil, fmt.Errorf("gateway candidate claim release timeout must be positive")
	}
	return &ClaimingExecutor{
		service: options.Service, inner: options.Inner, onReleaseError: options.OnReleaseError,
		refreshInterval: refreshInterval, releaseTimeout: releaseTimeout,
	}, nil
}

// Execute rechecks and claims the original candidate, passes only the fresh
// candidate to Inner, and keeps the lease alive until the inner call returns.
func (e *ClaimingExecutor) Execute(ctx context.Context, attempt gatewayattemptloop.Attempt) (result gatewayattemptloop.AttemptResult, resultErr error) {
	if e == nil || e.service == nil || e.inner == nil {
		return result, fmt.Errorf("gateway candidate claiming executor is not initialized")
	}
	if ctx == nil {
		return result, fmt.Errorf("gateway candidate claiming executor context is required")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	lease, err := e.service.Claim(ctx, Input{
		Candidate: attempt.Candidate, RequestedModel: attempt.RequestedModel,
		EndpointFamily: attempt.EndpointFamily, Lane: platformredis.AccountSlotLane(attempt.Lane),
		APIKeyIndex: attempt.APIKeyIndex,
	})
	if err != nil {
		return claimFailureResult(err), err
	}
	defer e.release(ctx, lease)

	innerCtx, cancelInner := context.WithCancel(ctx)
	defer cancelInner()
	refreshCtx, stopRefresh := context.WithCancel(context.WithoutCancel(ctx))
	defer stopRefresh()
	leaseLoss := newLeaseLoss()
	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		e.refreshUntilDone(refreshCtx, lease, cancelInner, leaseLoss)
	}()

	attempt.Candidate = lease.Candidate()
	result, resultErr = e.inner.Execute(innerCtx, attempt)
	stopRefresh()
	<-refreshDone

	if loss := leaseLoss.err(); loss != nil && !result.Committed {
		result.Success = false
		result.RetryAllowed = true
		result.FallbackDisposition = gatewayattemptloop.FallbackAccountRecoverable
		result.Failure.ErrorCode = "candidate_lease_lost"
		result.Failure.Message = loss.Error()
		resultErr = errors.Join(resultErr, loss)
	}
	if result.Committed {
		result.RetryAllowed = false
	}
	return result, resultErr
}

func (e *ClaimingExecutor) refreshUntilDone(ctx context.Context, lease *Lease, cancelInner context.CancelFunc, loss *leaseLoss) {
	ticker := time.NewTicker(e.refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshed, err := lease.Refresh(ctx)
			if ctx.Err() != nil {
				return
			}
			if err == nil && refreshed {
				continue
			}
			if err != nil {
				loss.record(fmt.Errorf("%w: %v", ErrLeaseLost, err))
			} else {
				loss.record(ErrLeaseLost)
			}
			cancelInner()
			return
		}
	}
}

func claimFailureResult(err error) gatewayattemptloop.AttemptResult {
	result := gatewayattemptloop.AttemptResult{Failure: gatewayattemptloop.FailureFacts{Message: err.Error(), ErrorCode: "candidate_claim_failed"}}
	switch {
	case errors.Is(err, ErrCandidateStale):
		result.RetryAllowed = true
		result.FallbackDisposition = gatewayattemptloop.FallbackAccountRecoverable
		result.Failure.ErrorCode = "candidate_stale"
	case errors.Is(err, ErrCapacityUnavailable):
		result.RetryAllowed = true
		result.FallbackDisposition = gatewayattemptloop.FallbackAccountRecoverable
		result.Failure.ErrorCode = "capacity_unavailable"
	case errors.Is(err, ErrAPIKeyIndexUnavailable):
		result.RetryAllowed = true
		result.KeyScopedFailure = true
		result.FallbackDisposition = gatewayattemptloop.FallbackAccountExcluded
		result.Failure.ErrorCode = "api_key_unavailable"
	}
	return result
}

func (e *ClaimingExecutor) release(requestCtx context.Context, lease *Lease) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(requestCtx), e.releaseTimeout)
	defer cancel()
	_, err := lease.Release(cleanupCtx)
	if err != nil {
		defer func() { _ = recover() }()
		if e.onReleaseError != nil {
			e.onReleaseError(err)
		}
	}
}

type leaseLoss struct {
	mu    sync.Mutex
	value error
}

func newLeaseLoss() *leaseLoss { return &leaseLoss{} }

func (l *leaseLoss) record(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.value == nil {
		l.value = err
	}
}

func (l *leaseLoss) err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.value
}

var _ gatewayattemptloop.AttemptExecutor = (*ClaimingExecutor)(nil)
