package recorddispatch

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/logging"
)

type mutableRecord struct {
	values []string
	labels map[string]string
}

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
	outcome := dispatcher.TrySubmit(context.Background(), 4)
	if outcome.Accepted || outcome.RejectionReason != RejectionQueueFull {
		t.Fatalf("TrySubmit() = %+v, want queue-full rejection", outcome)
	}
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("saturated Submit() blocked for %s", elapsed)
	}
	stats := dispatcher.Stats()
	if stats.Accepted != 2 || stats.Dropped != 2 || stats.DroppedQueueFull != 2 || stats.DroppedStopped != 0 || stats.Pending != 2 {
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

func TestDispatcherRecoversHandlerPanicAndContinues(t *testing.T) {
	processed := make(chan string, 1)
	dispatcher := New(Options[string]{
		Capacity: 2,
		Workers:  1,
		Timeout:  time.Second,
		Handle: func(_ context.Context, value string) error {
			if value == "panic" {
				panic("handler failed")
			}
			processed <- value
			return nil
		},
	})
	if !dispatcher.Submit(context.Background(), "panic") {
		t.Fatal("panic Submit() = false, want true")
	}
	if !dispatcher.Submit(context.Background(), "next") {
		t.Fatal("next Submit() = false, want true")
	}
	shutdownDispatcher(t, dispatcher)

	select {
	case value := <-processed:
		if value != "next" {
			t.Fatalf("processed value = %q, want next", value)
		}
	default:
		t.Fatal("worker did not continue after handler panic")
	}
	stats := dispatcher.Stats()
	if stats.Accepted != 2 || stats.Completed != 2 || stats.Failed != 1 || stats.Pending != 0 {
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

	outcome := dispatcher.TrySubmit(context.Background(), "late-record")
	if outcome.Accepted || outcome.RejectionReason != RejectionStopped {
		t.Fatalf("TrySubmit() after Shutdown() = %+v, want stopped rejection", outcome)
	}
	stats := dispatcher.Stats()
	if stats.Dropped != 1 || stats.DroppedStopped != 1 || stats.DroppedQueueFull != 0 {
		t.Fatalf("Stats() after stopped rejection = %+v", stats)
	}
	shutdownDispatcher(t, dispatcher)
}

func TestDispatcherClonesAcceptedValueBeforeCallerMutation(t *testing.T) {
	release := make(chan struct{})
	processed := make(chan mutableRecord, 1)
	dispatcher := New(Options[mutableRecord]{
		Capacity: 1,
		Workers:  1,
		Timeout:  time.Second,
		Clone: func(input mutableRecord) mutableRecord {
			cloned := mutableRecord{
				values: append([]string(nil), input.values...),
				labels: make(map[string]string, len(input.labels)),
			}
			for key, value := range input.labels {
				cloned.labels[key] = value
			}
			return cloned
		},
		Handle: func(_ context.Context, input mutableRecord) error {
			<-release
			processed <- input
			return nil
		},
	})

	record := mutableRecord{
		values: []string{"before"},
		labels: map[string]string{"state": "before"},
	}
	if !dispatcher.Submit(context.Background(), record) {
		t.Fatal("Submit() = false, want true")
	}
	record.values[0] = "after"
	record.labels["state"] = "after"
	close(release)
	shutdownDispatcher(t, dispatcher)

	got := <-processed
	if got.values[0] != "before" || got.labels["state"] != "before" {
		t.Fatalf("processed mutable value = %+v, want submit-time snapshot", got)
	}
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
	select {
	case <-dispatcher.Done():
		t.Fatal("Done() closed while handler still owned its dependency")
	default:
	}
	close(release)
	select {
	case <-dispatcher.Done():
	case <-time.After(time.Second):
		t.Fatal("Done() did not close after handler returned")
	}
	shutdownDispatcher(t, dispatcher)
}

func TestShutdownAllUsesOneConcurrentBudget(t *testing.T) {
	var started atomic.Int32
	release := make(chan struct{})
	newBlockedDispatcher := func() *Dispatcher[string] {
		dispatcher := New(Options[string]{
			Capacity: 1,
			Workers:  1,
			Timeout:  time.Second,
			Handle: func(context.Context, string) error {
				started.Add(1)
				<-release
				return nil
			},
		})
		if !dispatcher.Submit(context.Background(), "record") {
			t.Fatal("Submit() = false, want true")
		}
		return dispatcher
	}
	first := newBlockedDispatcher()
	second := newBlockedDispatcher()
	deadline := time.Now().Add(time.Second)
	for started.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if started.Load() != 2 {
		close(release)
		t.Fatal("handlers did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	err := ShutdownAll(ctx, first, second)
	elapsed := time.Since(startedAt)
	if !errors.Is(err, context.DeadlineExceeded) {
		close(release)
		t.Fatalf("ShutdownAll() error = %v, want context deadline exceeded", err)
	}
	if elapsed > 80*time.Millisecond {
		close(release)
		t.Fatalf("ShutdownAll() consumed sequential budgets: %s", elapsed)
	}

	close(release)
	shutdownDispatcher(t, first)
	shutdownDispatcher(t, second)
}

func TestNewRejectsInvalidOptions(t *testing.T) {
	valid := Options[string]{
		Capacity: 1,
		Workers:  1,
		Timeout:  time.Second,
		Handle:   func(context.Context, string) error { return nil },
	}

	tests := []struct {
		name   string
		mutate func(*Options[string])
	}{
		{name: "nonpositive capacity", mutate: func(options *Options[string]) { options.Capacity = 0 }},
		{name: "nonpositive workers", mutate: func(options *Options[string]) { options.Workers = 0 }},
		{name: "nonpositive timeout", mutate: func(options *Options[string]) { options.Timeout = 0 }},
		{name: "nil handler", mutate: func(options *Options[string]) { options.Handle = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := valid
			test.mutate(&options)
			defer func() {
				if recover() == nil {
					t.Fatal("New() did not panic for invalid options")
				}
			}()
			_ = New(options)
		})
	}
}

func shutdownDispatcher[T any](t *testing.T, dispatcher *Dispatcher[T]) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := dispatcher.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}
