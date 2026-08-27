package keymodelrecovery

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
)

type runnerStore struct {
	mu        sync.Mutex
	now       time.Time
	candidate State
	committed State
}

func (s *runnerStore) ServerNow(context.Context) (time.Time, error) { return s.now, nil }
func (s *runnerStore) ListDue(context.Context, time.Time, int64) ([]State, error) {
	return []State{s.candidate}, nil
}
func (s *runnerStore) Acquire(_ context.Context, candidate State, leaseID string, _ bool) (State, MutationStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, status := Acquire(candidate, candidate.Generation, candidate.DispatchRevision, leaseID, s.now)
	return next, status, nil
}
func (s *runnerStore) Renew(context.Context, State, string) (bool, error) { return true, nil }
func (s *runnerStore) Commit(_ context.Context, _ State, next State, _ string) (MutationStatus, error) {
	s.mu.Lock()
	s.committed = next
	s.mu.Unlock()
	return Applied, nil
}

type runnerLoader struct{ input accounthealth.Input }

func (l runnerLoader) LoadAccount(context.Context, string) ([]accounthealth.Input, error) {
	return []accounthealth.Input{l.input}, nil
}

func TestRunnerKeepsRecoveryWritesOutOfAccountHealth(t *testing.T) {
	now := time.Unix(10_000, 0).UTC()
	state, err := NewOpen(key(), now)
	if err != nil {
		t.Fatal(err)
	}
	store := &runnerStore{now: state.RetryAt, candidate: state}
	input := accounthealth.Input{AccountID: state.CredentialSourceAccountID, DispatchRevision: state.DispatchRevision}
	runner := NewRunner(store, runnerLoader{input: input}, nil)
	seen := make(chan State, 1)
	runner.probe = func(_ context.Context, candidate State, supplied accounthealth.Input) Outcome {
		if supplied.AccountID != candidate.CredentialSourceAccountID || supplied.DispatchRevision != candidate.DispatchRevision {
			t.Errorf("probe received mismatched frozen input: %#v %#v", candidate, supplied)
		}
		seen <- candidate
		return CompleteSuccess
	}
	if err := runner.RunCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-seen:
	case <-time.After(time.Second):
		t.Fatal("runner did not start due key-model probe")
	}
	deadline := time.Now().Add(time.Second)
	for {
		store.mu.Lock()
		committed := store.committed
		store.mu.Unlock()
		if committed.Phase == Recovering && committed.RecoverySuccessCount == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("runner did not commit key-model recovery state: %#v", committed)
		}
		time.Sleep(time.Millisecond)
	}
}
