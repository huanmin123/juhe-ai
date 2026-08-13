package supervisor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

var testRetryOptions = Options{InitialRetryDelay: time.Millisecond, MaxRetryDelay: 4 * time.Millisecond}

func TestRunRestartsFailedComponentWithoutCancelingPeers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var failedRuns atomic.Int32
	peersStarted := make(chan string, 2)
	recovered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- RunWithOptions(ctx, []Component{
			{Name: "F1", Run: func(runCtx context.Context) error {
				if failedRuns.Add(1) <= 2 {
					return errors.New("temporary F1 failure")
				}
				close(recovered)
				<-runCtx.Done()
				return runCtx.Err()
			}},
			{Name: "F2", Run: func(runCtx context.Context) error { peersStarted <- "F2"; <-runCtx.Done(); return runCtx.Err() }},
			{Name: "F3", Run: func(runCtx context.Context) error { peersStarted <- "F3"; <-runCtx.Done(); return runCtx.Err() }},
		}, nil, testRetryOptions)
	}()
	select {
	case <-peersStarted:
	case <-time.After(time.Second):
		t.Fatal("unaffected F2 peer was not started")
	}
	select {
	case <-peersStarted:
	case <-time.After(time.Second):
		t.Fatal("unaffected F3 peer was not started")
	}
	select {
	case <-recovered:
	case <-time.After(time.Second):
		t.Fatal("failed component did not restart and recover")
	}
	if failedRuns.Load() != 3 {
		t.Fatalf("failed component runs = %d, want 3", failedRuns.Load())
	}
	cancel()
	awaitCleanStop(t, done)
}

func TestRunRestartsUnexpectedNilExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var runs atomic.Int32
	restarted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- RunWithOptions(ctx, []Component{{Name: "F1", Run: func(runCtx context.Context) error {
			if runs.Add(1) == 1 {
				return nil
			}
			close(restarted)
			<-runCtx.Done()
			return runCtx.Err()
		}}}, nil, testRetryOptions)
	}()
	select {
	case <-restarted:
	case <-time.After(time.Second):
		t.Fatal("unexpected nil component exit did not restart")
	}
	cancel()
	awaitCleanStop(t, done)
}

func TestRunRecoversComponentPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var runs atomic.Int32
	restarted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- RunWithOptions(ctx, []Component{{Name: "F1", Run: func(runCtx context.Context) error {
			if runs.Add(1) == 1 {
				panic("temporary panic")
			}
			close(restarted)
			<-runCtx.Done()
			return runCtx.Err()
		}}}, nil, testRetryOptions)
	}()
	select {
	case <-restarted:
	case <-time.After(time.Second):
		t.Fatal("panic did not stay inside component retry boundary")
	}
	cancel()
	awaitCleanStop(t, done)
}

func TestRunExternalCancellationIsClean(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunWithOptions(ctx, []Component{{Name: "F1", Run: func(componentCtx context.Context) error { <-componentCtx.Done(); return componentCtx.Err() }}}, nil, testRetryOptions)
	}()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after external cancellation")
	}
}

func TestRunRejectsInvalidDefinitionsBeforeStartingComponents(t *testing.T) {
	started := make(chan struct{}, 1)
	err := Run(context.Background(), []Component{
		{Name: "F1", Run: func(context.Context) error { started <- struct{}{}; return nil }},
		{Name: "F2"},
	}, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want invalid component error")
	}
	select {
	case <-started:
		t.Fatal("valid component started before all definitions were validated")
	default:
	}
}

func awaitCleanStop(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after external cancellation")
	}
}
