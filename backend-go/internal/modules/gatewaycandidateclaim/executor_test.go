package gatewaycandidateclaim

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewayattemptloop"
	platformredis "juhe-ai/backend-go/internal/platform/redis"
)

type attemptExecutorFunc func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error)

func (f attemptExecutorFunc) Execute(ctx context.Context, attempt gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
	return f(ctx, attempt)
}

func newClaimingExecutorForTest(t *testing.T, slots *claimSlots, inner gatewayattemptloop.AttemptExecutor, options ...func(*ClaimingExecutorOptions)) *ClaimingExecutor {
	t.Helper()
	row := claimRow()
	service, err := NewService(Options{
		Reader: &claimReader{row: row}, Hydrator: &claimHydrator{candidate: claimCandidate(row)}, Slots: slots,
		Now: func() time.Time { return time.Date(2026, 8, 3, 5, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	config := ClaimingExecutorOptions{Service: service, Inner: inner, RefreshInterval: time.Second, OnReleaseError: func(error) {}}
	for _, option := range options {
		option(&config)
	}
	executor, err := NewClaimingExecutor(config)
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func testAttempt() gatewayattemptloop.Attempt {
	return gatewayattemptloop.Attempt{
		Candidate: claimCandidate(claimRow()), APIKeyIndex: 2,
		RequestedModel: "gpt-test", EndpointFamily: "responses", Lane: string(platformredis.AccountSlotLaneText),
	}
}

func TestClaimingExecutorUsesFreshCandidateAndReleasesExactlyOnce(t *testing.T) {
	slots := &claimSlots{acquireResult: platformredis.AccountSlotAcquireResult{Acquired: true, Lease: platformredis.AccountSlotLease{AccountID: "resource", Lane: platformredis.AccountSlotLaneText, Token: "token"}}}
	var gotAccount atomic.Value
	executor := newClaimingExecutorForTest(t, slots, attemptExecutorFunc(func(_ context.Context, attempt gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
		gotAccount.Store(attempt.Candidate.Projection.AccountID)
		return gatewayattemptloop.AttemptResult{Success: true, Committed: true}, nil
	}))
	result, err := executor.Execute(t.Context(), testAttempt())
	if err != nil || !result.Success || gotAccount.Load() != "view" {
		t.Fatalf("Execute() = %+v, %v account=%v", result, err, gotAccount.Load())
	}
	if slots.releaseCalls != 1 {
		t.Fatalf("release calls = %d, want 1", slots.releaseCalls)
	}
}

func TestClaimingExecutorClassifiesPreAttemptClaimFailures(t *testing.T) {
	slots := &claimSlots{}
	var innerCalls atomic.Int32
	executor := newClaimingExecutorForTest(t, slots, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
		innerCalls.Add(1)
		return gatewayattemptloop.AttemptResult{Success: true, Committed: true}, nil
	}))
	result, err := executor.Execute(t.Context(), testAttempt())
	if !errors.Is(err, ErrCapacityUnavailable) || !result.RetryAllowed || result.FallbackDisposition != gatewayattemptloop.FallbackAccountRecoverable || result.Failure.ErrorCode != "capacity_unavailable" {
		t.Fatalf("capacity claim = %+v, %v", result, err)
	}
	if innerCalls.Load() != 0 {
		t.Fatalf("inner calls = %d, want 0", innerCalls.Load())
	}
}

func TestClaimingExecutorRefreshLossAllowsOnlyUncommittedRetry(t *testing.T) {
	slots := &claimSlots{
		acquireResult: platformredis.AccountSlotAcquireResult{Acquired: true, Lease: platformredis.AccountSlotLease{AccountID: "resource", Lane: platformredis.AccountSlotLaneText, Token: "token"}},
		refreshResult: platformredis.AccountSlotRefreshResult{},
	}
	executor := newClaimingExecutorForTest(t, slots, attemptExecutorFunc(func(ctx context.Context, _ gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
		<-ctx.Done()
		return gatewayattemptloop.AttemptResult{}, ctx.Err()
	}), func(options *ClaimingExecutorOptions) { options.RefreshInterval = time.Millisecond })
	result, err := executor.Execute(t.Context(), testAttempt())
	if !errors.Is(err, ErrLeaseLost) || !result.RetryAllowed || result.FallbackDisposition != gatewayattemptloop.FallbackAccountRecoverable || result.Committed || result.Success || result.Failure.ErrorCode != "candidate_lease_lost" {
		t.Fatalf("refresh loss = %+v, %v", result, err)
	}
	if slots.releaseCalls != 1 {
		t.Fatalf("release calls = %d, want 1", slots.releaseCalls)
	}
}

func TestClaimingExecutorCommittedAttemptIsNeverRetryableAfterRefreshLoss(t *testing.T) {
	slots := &claimSlots{
		acquireResult: platformredis.AccountSlotAcquireResult{Acquired: true, Lease: platformredis.AccountSlotLease{AccountID: "resource", Lane: platformredis.AccountSlotLaneText, Token: "token"}},
		refreshResult: platformredis.AccountSlotRefreshResult{},
	}
	executor := newClaimingExecutorForTest(t, slots, attemptExecutorFunc(func(ctx context.Context, _ gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
		<-ctx.Done()
		return gatewayattemptloop.AttemptResult{Committed: true}, ctx.Err()
	}), func(options *ClaimingExecutorOptions) { options.RefreshInterval = time.Millisecond })
	result, err := executor.Execute(t.Context(), testAttempt())
	if !errors.Is(err, context.Canceled) || result.RetryAllowed || !result.Committed {
		t.Fatalf("committed refresh loss = %+v, %v", result, err)
	}
}

func TestClaimingExecutorReleaseErrorIsReportedWithoutChangingSuccess(t *testing.T) {
	slots := &claimSlots{acquireResult: platformredis.AccountSlotAcquireResult{Acquired: true, Lease: platformredis.AccountSlotLease{AccountID: "resource", Lane: platformredis.AccountSlotLaneText, Token: "token"}}, releaseErr: errors.New("redis unavailable")}
	reported := make(chan error, 1)
	executor := newClaimingExecutorForTest(t, slots, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
		return gatewayattemptloop.AttemptResult{Success: true, Committed: true}, nil
	}), func(options *ClaimingExecutorOptions) { options.OnReleaseError = func(err error) { reported <- err } })
	result, err := executor.Execute(t.Context(), testAttempt())
	if err != nil || !result.Success {
		t.Fatalf("release error changed result = %+v, %v", result, err)
	}
	select {
	case releaseErr := <-reported:
		if releaseErr == nil {
			t.Fatal("reported nil release error")
		}
	case <-time.After(time.Second):
		t.Fatal("release error was not reported")
	}
}
