package poolhealth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestNextHealthState(t *testing.T) {
	cases := []struct {
		success bool
		current int
		status  HealthState
		streak  int
	}{
		{true, 0, HealthHealthy, 0},
		{true, 2, HealthHealthy, 0},
		{false, 0, HealthDegraded, 1},
		{false, 1, HealthDegraded, 2},
		{false, 2, HealthDead, 3},
		{false, 5, HealthDead, 6},
	}
	for _, tc := range cases {
		status, streak := nextHealthState(tc.success, tc.current)
		if status != tc.status || streak != tc.streak {
			t.Fatalf("nextHealthState(%v,%d) = (%s,%d), want (%s,%d)",
				tc.success, tc.current, status, streak, tc.status, tc.streak)
		}
	}
}

type fakeDueStore struct {
	targets []Target
	err     error
	skipped map[string]bool
	mu      sync.Mutex
	records []recordedProbe
}

type recordedProbe struct {
	profileID string
	success   bool
	checkedAt time.Time
}

func (s *fakeDueStore) ListDue(context.Context, time.Time, int) ([]Target, error) {
	return s.targets, s.err
}

func (s *fakeDueStore) Record(_ context.Context, target Target, success bool, checkedAt time.Time) (RecordResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, recordedProbe{profileID: target.ProfileID, success: success, checkedAt: checkedAt})
	if s.skipped[target.ProfileID] {
		return RecordResult{ProfileID: target.ProfileID, Skipped: true}, nil
	}
	status, streak := nextHealthState(success, 0)
	return RecordResult{ProfileID: target.ProfileID, Status: status, Streak: streak, Transitioned: true}, nil
}

type fakeProber struct {
	errs map[string]error
}

func (p fakeProber) Probe(_ context.Context, target Target) error {
	return p.errs[target.ProfileID]
}

type fakeHealthWriter struct {
	mu     sync.Mutex
	writes map[string]HealthState
	err    error
}

func (w *fakeHealthWriter) Write(_ context.Context, profileID string, state HealthState) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	if w.writes == nil {
		w.writes = map[string]HealthState{}
	}
	w.writes[profileID] = state
	return nil
}

func (w *fakeHealthWriter) Delete(context.Context, string) error { return nil }

func TestRunnerRoundAccounting(t *testing.T) {
	store := &fakeDueStore{targets: []Target{
		{ProfileID: "ok"}, {ProfileID: "down"}, {ProfileID: "bad-config"}, {ProfileID: "redis-down"}, {ProfileID: "stale"},
	}, skipped: map[string]bool{"stale": true}}
	prober := fakeProber{errs: map[string]error{
		"down": errors.New("connection refused"), "bad-config": ErrProbeConfiguration,
	}}
	writer := &fakeHealthWriter{}
	runner, err := NewRunner(store, prober, writer, RunnerOptions{BatchSize: 10, Concurrency: 2})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	summary, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if summary.Scanned != 5 || summary.Succeeded != 2 || summary.Failed != 1 || summary.SkippedConfig != 1 ||
		summary.SkippedStale != 1 || summary.ProjectionFailed != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	if len(store.records) != 4 {
		t.Fatalf("records = %d, want 4 (config error must not project)", len(store.records))
	}
	// Canceled context after probe skips projection (tested separately); only
	// current snapshots reach Redis (the stale row is discarded).
	if len(writer.writes) != 3 {
		t.Fatalf("redis writes = %v", writer.writes)
	}
}

func TestRunnerRedisFailureFallsBackWithoutProjectionFailure(t *testing.T) {
	store := &fakeDueStore{targets: []Target{{ProfileID: "p1"}}}
	writer := &fakeHealthWriter{err: errors.New("redis down")}
	runner, _ := NewRunner(store, fakeProber{}, writer, RunnerOptions{})
	summary, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if summary.Succeeded != 1 || summary.RedisFailed != 1 || summary.ProjectionFailed != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	if len(store.records) != 1 {
		t.Fatalf("projection must not be rolled back by redis failure: %d", len(store.records))
	}
}

func TestRunnerCanceledContextSkipsProjection(t *testing.T) {
	store := &fakeDueStore{targets: []Target{{ProfileID: "p1"}}}
	runner, _ := NewRunner(store, fakeProber{}, nil, RunnerOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	summary, err := runner.RunOnce(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(store.records) != 0 {
		t.Fatalf("canceled run must not project: %+v", store.records)
	}
	if summary.SkippedCanceled == 0 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestHTTPProberConfigurationError(t *testing.T) {
	prober := NewHTTPProber("", DefaultEndpoint, time.Second)
	if err := prober.Probe(context.Background(), Target{ProfileID: "p", Type: "ftp", Host: "x", Port: 1}); !errors.Is(err, ErrProbeConfiguration) {
		t.Fatalf("invalid scheme error = %v, want ErrProbeConfiguration", err)
	}
	if err := prober.Probe(context.Background(), Target{ProfileID: "p", Type: "http", Host: "x", Port: 0}); !errors.Is(err, ErrProbeConfiguration) {
		t.Fatalf("invalid port error = %v, want ErrProbeConfiguration", err)
	}
}
