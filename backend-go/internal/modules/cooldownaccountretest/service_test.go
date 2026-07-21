package cooldownaccountretest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

type fakeStore struct {
	page  port.CooldownAccountRetestPage
	found port.CooldownAccountRetestCandidate
	ok    bool
	input port.CooldownAccountRetestListInput
}

func (f *fakeStore) ListDueCooldownAccountRetests(_ context.Context, input port.CooldownAccountRetestListInput) (port.CooldownAccountRetestPage, error) {
	f.input = input
	return f.page, nil
}
func (f *fakeStore) FindDueCooldownAccountRetest(_ context.Context, _ string, _ time.Time) (port.CooldownAccountRetestCandidate, bool, error) {
	return f.found, f.ok, nil
}

type fakeEnqueuer struct {
	mu                       sync.Mutex
	active, maxActive, calls int
}

func (f *fakeEnqueuer) EnqueueCooldownAccountRetest(ctx context.Context, task port.CooldownAccountRetestTask) (bool, error) {
	f.mu.Lock()
	f.calls++
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()
	select {
	case <-time.After(5 * time.Millisecond):
	case <-ctx.Done():
		return false, ctx.Err()
	}
	f.mu.Lock()
	f.active--
	f.mu.Unlock()
	return task.AccountID != "duplicate", nil
}

func TestSchedulerUsesBoundedPageAndEnqueueWorkers(t *testing.T) {
	store := &fakeStore{page: port.CooldownAccountRetestPage{Candidates: []port.CooldownAccountRetestCandidate{{ID: "a", ConfigRevision: 1}, {ID: "duplicate", ConfigRevision: 1}, {ID: "c", ConfigRevision: 1}}}}
	enqueuer := &fakeEnqueuer{}
	result, _, err := (Scheduler{Store: store, Enqueuer: enqueuer, BatchSize: 1000, EnqueueWorkers: 2}).RunPage(context.Background(), nil, time.Now())
	if err != nil {
		t.Fatalf("RunPage() error = %v", err)
	}
	if store.input.Limit != port.CooldownAccountRetestMaxPageSize {
		t.Fatalf("limit = %d", store.input.Limit)
	}
	if result.EnqueuedCount != 2 || result.DuplicateCount != 1 {
		t.Fatalf("result = %+v", result)
	}
	if enqueuer.maxActive > 2 {
		t.Fatalf("max active = %d", enqueuer.maxActive)
	}
}

type fakeProbe struct {
	calls         int
	waitForCancel bool
}

func (f *fakeProbe) Probe(ctx context.Context, _ port.CooldownAccountRetestCandidate) (port.CooldownAccountRetestProbeResult, error) {
	f.calls++
	if f.waitForCancel {
		<-ctx.Done()
		return port.CooldownAccountRetestProbeResult{}, ctx.Err()
	}
	return port.CooldownAccountRetestProbeResult{Outcome: "complete_success"}, nil
}

type fakeOutcomes struct{ success, deferred, failed int }

func (f *fakeOutcomes) RecordCooldownAccountRetestSuccess(context.Context, port.CooldownAccountRetestTask) error {
	f.success++
	return nil
}
func (f *fakeOutcomes) DeferCooldownAccountRetest(context.Context, port.CooldownAccountRetestTask, time.Duration) error {
	f.deferred++
	return nil
}
func (f *fakeOutcomes) RecordCooldownAccountRetestFailure(context.Context, port.CooldownAccountRetestTask, port.CooldownAccountRetestProbeResult) error {
	f.failed++
	return nil
}

func TestProcessorDiscardsStaleConfigAndObservation(t *testing.T) {
	started := time.Now().UTC()
	store := &fakeStore{ok: true, found: port.CooldownAccountRetestCandidate{ID: "a", ConfigRevision: 2, ObservationStartedAt: &started}}
	probe := &fakeProbe{}
	outcomes := &fakeOutcomes{}
	err := (Processor{Store: store, Outcomes: outcomes, Probe: probe}).RunTask(context.Background(), port.CooldownAccountRetestTask{AccountID: "a", ConfigRevision: 1, ObservationStartedAt: &started})
	if err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if probe.calls != 0 || outcomes.success != 0 {
		t.Fatalf("stale task executed: probe=%d success=%d", probe.calls, outcomes.success)
	}
}

func TestProcessorAppliesTaskTimeout(t *testing.T) {
	store := &fakeStore{ok: true, found: port.CooldownAccountRetestCandidate{ID: "a", ConfigRevision: 1}}
	probe := &fakeProbe{waitForCancel: true}
	err := (Processor{Store: store, Outcomes: &fakeOutcomes{}, Probe: probe, TaskTimeout: 10 * time.Millisecond}).RunTask(context.Background(), port.CooldownAccountRetestTask{AccountID: "a", ConfigRevision: 1})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline", err)
	}
}

func TestProcessorRecordsSuccess(t *testing.T) {
	store := &fakeStore{ok: true, found: port.CooldownAccountRetestCandidate{ID: "a", ConfigRevision: 1}}
	outcomes := &fakeOutcomes{}
	if err := (Processor{Store: store, Outcomes: outcomes, Probe: &fakeProbe{}}).RunTask(context.Background(), port.CooldownAccountRetestTask{AccountID: "a", ConfigRevision: 1}); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if outcomes.success != 1 {
		t.Fatalf("success calls = %d", outcomes.success)
	}
}
