package accounthealthcheck

import (
	"context"
	"testing"
	"time"

	job "juhe-ai/backend-go/internal/jobs/accounthealthcheck"
	"juhe-ai/backend-go/internal/store/port"
)

type fakeCurrentReader struct {
	candidate port.AccountHealthCheckCandidate
}

func (f fakeCurrentReader) GetAccountHealthCheckCandidate(context.Context, string, time.Time) (port.AccountHealthCheckCandidate, bool, error) {
	return f.candidate, true, nil
}

type fakeProbe struct{ called bool }

func (f *fakeProbe) Probe(ctx context.Context, _ port.AccountHealthCheckCandidate) (ProbeResult, error) {
	f.called = true
	<-ctx.Done()
	return ProbeResult{}, ctx.Err()
}

type fakeSink struct{ called bool }

func (f *fakeSink) Record(context.Context, job.Task, ProbeResult) error { f.called = true; return nil }

func TestRunDropsStaleRevisionBeforeProbe(t *testing.T) {
	probe := &fakeProbe{}
	err := NewService(nil, nil).Run(context.Background(), job.Task{AccountID: "a", ConfigRevision: 1, UniqueKey: job.UniqueKey("a", 1)}, fakeCurrentReader{candidate: candidate("a", 2)}, probe, &fakeSink{}, time.Second)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if probe.called {
		t.Fatal("probe called for stale revision")
	}
}

func TestRunAppliesSingleTaskTimeout(t *testing.T) {
	probe := &fakeProbe{}
	started := time.Now()
	err := NewService(nil, nil).Run(context.Background(), job.Task{AccountID: "a", ConfigRevision: 1, UniqueKey: job.UniqueKey("a", 1)}, fakeCurrentReader{candidate: candidate("a", 1)}, probe, &fakeSink{}, 10*time.Millisecond)
	if err == nil {
		t.Fatal("Run() error = nil, want timeout")
	}
	if time.Since(started) > time.Second {
		t.Fatal("Run() exceeded bounded timeout")
	}
}
