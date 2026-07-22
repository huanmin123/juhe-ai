package recorddispatch

import (
	"context"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/logging"
)

func TestDispatcherSubmitDoesNotWaitForHandlerAndPreservesLogContext(t *testing.T) {
	release := make(chan struct{})
	started := make(chan logging.LogContext, 1)
	dispatcher := New(Options[string]{
		Capacity: 1,
		Workers:  1,
		Timeout:  time.Second,
		Handle: func(ctx context.Context, _ string) error {
			started <- logging.LogContextFrom(ctx)
			<-release
			return nil
		},
	})

	ctx := logging.WithLogContext(context.Background(), logging.LogContext{
		TraceID:   "trace-1",
		RequestID: "request-1",
	})
	startedAt := time.Now()
	if !dispatcher.Submit(ctx, "record-1") {
		t.Fatal("Submit() = false, want true")
	}
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("Submit() blocked for %s", elapsed)
	}

	select {
	case got := <-started:
		if got.TraceID != "trace-1" || got.RequestID != "request-1" {
			t.Fatalf("log context = %+v, want trace-1/request-1", got)
		}
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}

	close(release)
	shutdownDispatcher(t, dispatcher)
	stats := dispatcher.Stats()
	if stats.Accepted != 1 || stats.Completed != 1 || stats.Failed != 0 || stats.Dropped != 0 || stats.Pending != 0 {
		t.Fatalf("Stats() = %+v", stats)
	}
}

func TestDispatcherSubmitDropsImmediatelyWhenCapacityIsFull(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	dispatcher := New(Options[int]{
		Capacity: 1,
		Workers:  1,
		Timeout:  time.Second,
		Handle: func(context.Context, int) error {
			started <- struct{}{}
			<-release
			return nil
		},
	})

	if !dispatcher.Submit(context.Background(), 1) {
		t.Fatal("first Submit() = false, want true")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	if !dispatcher.Submit(context.Background(), 2) {
		t.Fatal("second Submit() = false, want true")
	}

	startedAt := time.Now()
	if dispatcher.Submit(context.Background(), 3) {
		t.Fatal("third Submit() = true, want false")
	}
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("saturated Submit() blocked for %s", elapsed)
	}
	stats := dispatcher.Stats()
	if stats.Accepted != 2 || stats.Dropped != 1 || stats.Pending != 2 {
		t.Fatalf("Stats() while saturated = %+v", stats)
	}

	close(release)
	shutdownDispatcher(t, dispatcher)
}

func TestDispatcherCountsHandlerErrorsAsFailed(t *testing.T) {
	dispatcher := New(Options[string]{
		Capacity: 1,
		Workers:  1,
		Timeout:  time.Second,
		Handle: func(context.Context, string) error {
			return errors.New("write failed")
		},
	})
	if !dispatcher.Submit(context.Background(), "record-1") {
		t.Fatal("Submit() = false, want true")
	}
	shutdownDispatcher(t, dispatcher)

	stats := dispatcher.Stats()
	if stats.Accepted != 1 || stats.Completed != 1 || stats.Failed != 1 || stats.Pending != 0 {
		t.Fatalf("Stats() = %+v", stats)
	}
}

func TestDispatcherAppliesPerJobTimeout(t *testing.T) {
	timedOut := make(chan error, 1)
	dispatcher := New(Options[string]{
		Capacity: 1,
		Workers:  1,
		Timeout:  20 * time.Millisecond,
		Handle: func(ctx context.Context, _ string) error {
			<-ctx.Done()
			timedOut <- ctx.Err()
			return nil
		},
	})
	if !dispatcher.Submit(context.Background(), "record-1") {
		t.Fatal("Submit() = false, want true")
	}

	select {
	case err := <-timedOut:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("handler context error = %v, want context deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("handler context did not time out")
	}
	shutdownDispatcher(t, dispatcher)
}

func TestDispatcherSubmitReturnsFalseAfterShutdown(t *testing.T) {
	dispatcher := New(Options[string]{
		Capacity: 1,
		Workers:  1,
		Timeout:  time.Second,
		Handle:   func(context.Context, string) error { return nil },
	})
	shutdownDispatcher(t, dispatcher)

	if dispatcher.Submit(context.Background(), "late-record") {
		t.Fatal("Submit() after Shutdown() = true, want false")
	}
	if got := dispatcher.Stats().Dropped; got != 1 {
		t.Fatalf("Dropped = %d, want 1", got)
	}
	shutdownDispatcher(t, dispatcher)
}

func TestDispatcherShutdownHonorsDeadlineWhenHandlerIgnoresContext(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	dispatcher := New(Options[string]{
		Capacity: 1,
		Workers:  1,
		Timeout:  10 * time.Millisecond,
		Handle: func(context.Context, string) error {
			started <- struct{}{}
			<-release
			return nil
		},
	})
	if !dispatcher.Submit(context.Background(), "record-1") {
		t.Fatal("Submit() = false, want true")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := dispatcher.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		close(release)
		t.Fatalf("Shutdown() error = %v, want context deadline exceeded", err)
	}
	close(release)
	shutdownDispatcher(t, dispatcher)
}

func shutdownDispatcher[T any](t *testing.T, dispatcher *Dispatcher[T]) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := dispatcher.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}
