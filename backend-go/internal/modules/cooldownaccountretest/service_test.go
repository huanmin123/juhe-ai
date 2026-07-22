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
	lists int
}

func (f *fakeStore) ListDueCooldownAccountRetests(_ context.Context, input port.CooldownAccountRetestListInput) (port.CooldownAccountRetestPage, error) {
	f.lists++
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

type fakeQueueCapacity struct {
	snapshot QueueSnapshot
	err      error
}

func (f fakeQueueCapacity) CooldownAccountRetestQueueSnapshot(context.Context) (QueueSnapshot, error) {
	return f.snapshot, f.err
}

func TestSchedulerLimitsPageToAvailableQueueSlots(t *testing.T) {
	store := &fakeStore{page: port.CooldownAccountRetestPage{Candidates: []port.CooldownAccountRetestCandidate{{ID: "a", ConfigRevision: 1}}}}
	result, _, err := (Scheduler{
		Store: store, Enqueuer: &fakeEnqueuer{}, BatchSize: 5,
		Capacity: fakeQueueCapacity{snapshot: QueueSnapshot{PendingCount: 2, RunningCount: 2}},
	}).RunPage(context.Background(), nil, time.Now())
	if err != nil {
		t.Fatalf("RunPage() error = %v", err)
	}
	if store.input.Limit != 1 || result.AvailableSlots != 1 {
		t.Fatalf("limit=%d result=%+v, want one available slot", store.input.Limit, result)
	}
}

func TestSchedulerKeepsCursorWhenQueueIsFull(t *testing.T) {
	cursor := &port.CooldownAccountRetestCursor{ID: "cursor-account"}
	store := &fakeStore{}
	result, next, err := (Scheduler{
		Store: store, Enqueuer: &fakeEnqueuer{}, BatchSize: 3,
		Capacity: fakeQueueCapacity{snapshot: QueueSnapshot{PendingCount: 2, RunningCount: 1}},
	}).RunPage(context.Background(), cursor, time.Now())
	if err != nil {
		t.Fatalf("RunPage() error = %v", err)
	}
	if store.lists != 0 || next != cursor || result.AvailableSlots != 0 {
		t.Fatalf("lists=%d next=%+v result=%+v", store.lists, next, result)
	}
}

type fakeProbe struct {
	calls         int
	waitForCancel bool
	result        port.CooldownAccountRetestProbeResult
	err           error
}

func (f *fakeProbe) Probe(ctx context.Context, _ port.CooldownAccountRetestCandidate) (port.CooldownAccountRetestProbeResult, error) {
	f.calls++
	if f.waitForCancel {
		<-ctx.Done()
		return port.CooldownAccountRetestProbeResult{}, ctx.Err()
	}
	if f.err != nil {
		return port.CooldownAccountRetestProbeResult{}, f.err
	}
	if f.result.Outcome != "" {
		return f.result, nil
	}
	return port.CooldownAccountRetestProbeResult{Outcome: "complete_success"}, nil
}

type fakeOutcomes struct {
	success, deferred, failed int
	deferDelay                time.Duration
	failureResult             port.CooldownAccountRetestProbeResult
	deferContextErr           error
	deferErr                  error
}

func (f *fakeOutcomes) RecordCooldownAccountRetestSuccess(context.Context, port.CooldownAccountRetestTask) error {
	f.success++
	return nil
}
func (f *fakeOutcomes) DeferCooldownAccountRetest(ctx context.Context, _ port.CooldownAccountRetestTask, delay time.Duration) error {
	f.deferred++
	f.deferDelay = delay
	f.deferContextErr = ctx.Err()
	return f.deferErr
}
func (f *fakeOutcomes) RecordCooldownAccountRetestFailure(_ context.Context, _ port.CooldownAccountRetestTask, result port.CooldownAccountRetestProbeResult) error {
	f.failed++
	f.failureResult = result
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

func TestProcessorDefersAfterProbeTimeoutWithFreshOutcomeContext(t *testing.T) {
	store := &fakeStore{ok: true, found: port.CooldownAccountRetestCandidate{ID: "a", ConfigRevision: 1}}
	probe := &fakeProbe{waitForCancel: true}
	outcomes := &fakeOutcomes{}
	err := (Processor{Store: store, Outcomes: outcomes, Probe: probe, TaskTimeout: 10 * time.Millisecond}).RunTask(context.Background(), port.CooldownAccountRetestTask{AccountID: "a", ConfigRevision: 1})
	if err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if outcomes.deferred != 1 || outcomes.deferContextErr != nil {
		t.Fatalf("outcomes = %+v, want deferred with live context", outcomes)
	}
}

func TestProcessorDefersTransientProbeErrorWithoutQueueRetry(t *testing.T) {
	probeErr := errors.New("upstream transport unavailable")
	store := &fakeStore{ok: true, found: port.CooldownAccountRetestCandidate{ID: "a", ConfigRevision: 1}}
	outcomes := &fakeOutcomes{}
	err := (Processor{Store: store, Outcomes: outcomes, Probe: &fakeProbe{err: probeErr}}).RunTask(context.Background(), port.CooldownAccountRetestTask{AccountID: "a", ConfigRevision: 1})
	if err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if outcomes.deferred != 1 || outcomes.failed != 0 {
		t.Fatalf("outcomes = %+v", outcomes)
	}
}

func TestProcessorReturnsRetryableErrorWhenDeferredOutcomeCannotPersist(t *testing.T) {
	persistErr := errors.New("database unavailable")
	store := &fakeStore{ok: true, found: port.CooldownAccountRetestCandidate{ID: "a", ConfigRevision: 1}}
	outcomes := &fakeOutcomes{deferErr: persistErr}
	err := (Processor{Store: store, Outcomes: outcomes, Probe: &fakeProbe{err: errors.New("upstream timeout")}}).RunTask(context.Background(), port.CooldownAccountRetestTask{AccountID: "a", ConfigRevision: 1})
	if !errors.Is(err, persistErr) {
		t.Fatalf("error = %v, want persistence error", err)
	}
	if outcomes.deferred != 1 {
		t.Fatalf("deferred = %d, want 1", outcomes.deferred)
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

func TestProcessorDiscardsStaleObservationGeneration(t *testing.T) {
	queued := time.Now().UTC()
	current := queued.Add(time.Second)
	store := &fakeStore{ok: true, found: port.CooldownAccountRetestCandidate{ID: "a", ConfigRevision: 1, ObservationStartedAt: &current}}
	probe := &fakeProbe{}
	err := (Processor{Store: store, Outcomes: &fakeOutcomes{}, Probe: probe}).RunTask(
		context.Background(),
		port.CooldownAccountRetestTask{AccountID: "a", ConfigRevision: 1, ObservationStartedAt: &queued},
	)
	if err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if probe.calls != 0 {
		t.Fatal("probe called for stale observation generation")
	}
}

func TestProcessorDefersUnattributableProbeFailureByTenSeconds(t *testing.T) {
	store := &fakeStore{ok: true, found: port.CooldownAccountRetestCandidate{ID: "a", ConfigRevision: 1}}
	outcomes := &fakeOutcomes{}
	probe := &fakeProbe{result: port.CooldownAccountRetestProbeResult{Outcome: "probe_task_failure"}}
	if err := (Processor{Store: store, Outcomes: outcomes, Probe: probe}).RunTask(context.Background(), port.CooldownAccountRetestTask{AccountID: "a", ConfigRevision: 1}); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if outcomes.deferred != 1 || outcomes.deferDelay != 10*time.Second || outcomes.failed != 0 {
		t.Fatalf("outcomes = %+v", outcomes)
	}
}

func TestProcessorRecordsAttributableUpstreamFailure(t *testing.T) {
	store := &fakeStore{ok: true, found: port.CooldownAccountRetestCandidate{ID: "a", ConfigRevision: 1}}
	outcomes := &fakeOutcomes{}
	want := port.CooldownAccountRetestProbeResult{Outcome: "upstream_failure", StatusCode: 429, ErrorCode: "rate_limit"}
	probe := &fakeProbe{result: want}
	if err := (Processor{Store: store, Outcomes: outcomes, Probe: probe}).RunTask(context.Background(), port.CooldownAccountRetestTask{AccountID: "a", ConfigRevision: 1}); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if outcomes.failed != 1 || outcomes.deferred != 0 || outcomes.failureResult != want {
		t.Fatalf("outcomes = %+v", outcomes)
	}
}
