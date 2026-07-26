package accountbalancerefresh

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

type fakeCandidateStore struct {
	recovery []port.AccountBalanceRefreshCandidate
	due      []port.AccountBalanceRefreshCandidate
	err      error
	limits   []int
}

func (f *fakeCandidateStore) ListAccountBalanceRefreshRecoveryCandidates(_ context.Context, limit int) ([]port.AccountBalanceRefreshCandidate, error) {
	f.limits = append(f.limits, limit)
	if f.err != nil {
		return nil, f.err
	}
	return f.recovery, nil
}

func (f *fakeCandidateStore) ListAccountBalanceRefreshDueCandidates(_ context.Context, _ time.Time, limit int) ([]port.AccountBalanceRefreshCandidate, error) {
	f.limits = append(f.limits, limit)
	if f.err != nil {
		return nil, f.err
	}
	return f.due, nil
}

type fakeRuntimeFilter struct {
	available bool
	keep      map[string]bool
	err       error
}

func (f fakeRuntimeFilter) Filter(_ context.Context, candidates []port.AccountBalanceRefreshCandidate) ([]port.AccountBalanceRefreshCandidate, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	if !f.available {
		return candidates, false, nil
	}
	filtered := make([]port.AccountBalanceRefreshCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if f.keep[candidate.ID] {
			filtered = append(filtered, candidate)
		}
	}
	return filtered, true, nil
}

func refreshCandidate(id string) port.AccountBalanceRefreshCandidate {
	return port.AccountBalanceRefreshCandidate{ID: id, SystemAccountID: "system-" + id}
}

func TestRunPrioritizesRecoveryBoundsWindowAndContinuesCandidateTimeouts(t *testing.T) {
	store := &fakeCandidateStore{
		recovery: []port.AccountBalanceRefreshCandidate{refreshCandidate("recovery-1"), refreshCandidate("recovery-2")},
		due:      []port.AccountBalanceRefreshCandidate{refreshCandidate("due-1"), refreshCandidate("due-2"), refreshCandidate("due-3")},
	}
	var mu sync.Mutex
	started := 0
	maxStarted := 0
	result, err := Run(context.Background(), Dependencies{
		Store:            store,
		RuntimeFilter:    fakeRuntimeFilter{available: true, keep: map[string]bool{"recovery-1": true, "recovery-2": true, "due-1": true, "due-2": true, "due-3": true}},
		WorkerLimit:      2,
		WindowSize:       4,
		CandidateTimeout: 10 * time.Millisecond,
		RefreshCandidate: func(ctx context.Context, candidate port.AccountBalanceRefreshCandidate) error {
			mu.Lock()
			started++
			if started > maxStarted {
				maxStarted = started
			}
			mu.Unlock()
			defer func() {
				mu.Lock()
				started--
				mu.Unlock()
			}()
			if candidate.ID == "due-1" {
				<-ctx.Done()
				return ctx.Err()
			}
			select {
			case <-time.After(5 * time.Millisecond):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Outcome != OutcomePartial || result.CandidateFailureCount != 1 {
		t.Fatalf("result = %+v, want partial with one candidate failure", result)
	}
	if result.SelectedCount != 4 || result.ProcessedCount != 3 {
		t.Fatalf("result counts = %+v, want selected=4 processed=3", result)
	}
	if maxStarted > 2 {
		t.Fatalf("max concurrent refreshes = %d, want <= 2", maxStarted)
	}
	if len(store.limits) != 2 || store.limits[0] != 4 || store.limits[1] != 2 {
		t.Fatalf("store limits = %v, want recovery=4 then due=2", store.limits)
	}
}

func TestRunReturnsCandidateInfrastructureError(t *testing.T) {
	infraErr := errors.New("persist account balance snapshot: database unavailable")
	store := &fakeCandidateStore{due: []port.AccountBalanceRefreshCandidate{refreshCandidate("due-1")}}

	_, err := Run(context.Background(), Dependencies{
		Store:            store,
		WorkerLimit:      1,
		WindowSize:       1,
		CandidateTimeout: time.Second,
		RefreshCandidate: func(context.Context, port.AccountBalanceRefreshCandidate) error {
			return infraErr
		},
	})
	if !errors.Is(err, infraErr) {
		t.Fatalf("Run() error = %v, want candidate infrastructure error", err)
	}
}

func TestRunReturnsInfrastructureError(t *testing.T) {
	infraErr := errors.New("database unavailable")
	_, err := Run(context.Background(), Dependencies{Store: &fakeCandidateStore{err: infraErr}})
	if !errors.Is(err, infraErr) {
		t.Fatalf("Run() error = %v, want infrastructure error", err)
	}
}

func TestRunFiltersRuntimeAndDefersAfterRunBudget(t *testing.T) {
	store := &fakeCandidateStore{due: []port.AccountBalanceRefreshCandidate{
		refreshCandidate("blocked"),
		refreshCandidate("slow"),
		refreshCandidate("deferred"),
	}}
	result, err := Run(context.Background(), Dependencies{
		Store:            store,
		RuntimeFilter:    fakeRuntimeFilter{available: true, keep: map[string]bool{"slow": true, "deferred": true}},
		WorkerLimit:      1,
		WindowSize:       3,
		RunBudget:        10 * time.Millisecond,
		CandidateTimeout: time.Second,
		RefreshCandidate: func(ctx context.Context, _ port.AccountBalanceRefreshCandidate) error {
			<-ctx.Done()
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Outcome != OutcomePartial || result.CandidateFailureCount != 1 {
		t.Fatalf("result = %+v, want one timed out candidate failure", result)
	}
	if result.SelectedCount != 3 || result.ProcessedCount != 0 || result.DeferredCount != 2 {
		t.Fatalf("result counts = %+v, want selected=3 processed=0 deferred=2", result)
	}
}

func TestRunReturnsRuntimeFilterInfrastructureError(t *testing.T) {
	infraErr := errors.New("runtime state unavailable")
	store := &fakeCandidateStore{due: []port.AccountBalanceRefreshCandidate{refreshCandidate("due-1")}}
	_, err := Run(context.Background(), Dependencies{
		Store:         store,
		RuntimeFilter: fakeRuntimeFilter{err: infraErr},
	})
	if !errors.Is(err, infraErr) {
		t.Fatalf("Run() error = %v, want runtime infrastructure error", err)
	}
}

func TestRunDoesNotDetachCandidateThatIgnoresContext(t *testing.T) {
	release := make(chan struct{})
	finished := make(chan struct{})
	store := &fakeCandidateStore{due: []port.AccountBalanceRefreshCandidate{refreshCandidate("stuck")}}
	timer := time.AfterFunc(40*time.Millisecond, func() { close(release) })
	t.Cleanup(func() { timer.Stop() })
	startedAt := time.Now()
	result, err := Run(context.Background(), Dependencies{
		Store:            store,
		WorkerLimit:      1,
		WindowSize:       1,
		RunBudget:        time.Second,
		CandidateTimeout: 5 * time.Millisecond,
		RefreshCandidate: func(context.Context, port.AccountBalanceRefreshCandidate) error {
			<-release
			close(finished)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed < 30*time.Millisecond {
		t.Fatalf("Run() elapsed = %v, detached candidate before its execution stopped", elapsed)
	}
	select {
	case <-finished:
	default:
		t.Fatal("Run() returned before the candidate execution stopped")
	}
	if result.Outcome != OutcomePartial || result.CandidateFailureCount != 1 {
		t.Fatalf("result = %+v, want timed out candidate failure", result)
	}
}

func TestRunCandidateConvertsPanicToInfrastructureErrorWithoutHelperGoroutine(t *testing.T) {
	want := "refresh panic"
	err := runCandidate(t.Context(), func(context.Context, port.AccountBalanceRefreshCandidate) error {
		panic(want)
	}, refreshCandidate("panic"))
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("runCandidate() error = %v, want recovered panic", err)
	}
}
