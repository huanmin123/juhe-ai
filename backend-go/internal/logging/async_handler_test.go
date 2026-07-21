package logging

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type blockingWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(p), nil
}

func TestNewLoggerDoesNotWaitForDestinationWrite(t *testing.T) {
	w := &blockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	runtime, err := NewRuntime("info", w, RuntimeOptions{Role: "go-test"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		runtime.Logger.Info("stage completed", "event", "gateway.request.stage")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		close(w.release)
		t.Fatal("logger call waited for destination write")
	}
	close(w.release)
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("queued record was not delivered")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeShutdownDrainsQueuedRecordsAndPreservesRole(t *testing.T) {
	var output lockedBuffer
	runtime, err := NewRuntime("info", &output, RuntimeOptions{Role: "go-worker"})
	if err != nil {
		t.Fatal(err)
	}
	runtime.Logger.Info("queued before shutdown", "event", "worker.stage")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	written := output.String()
	if !strings.Contains(written, `"event":"worker.stage"`) || !strings.Contains(written, `"role":"go-worker"`) {
		t.Fatalf("drained output missing event or role: %s", written)
	}
}

func TestRuntimeAddsTraceRequestJobContext(t *testing.T) {
	var output lockedBuffer
	runtime, err := NewRuntime("info", &output, RuntimeOptions{Role: "go-worker"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithLogContext(context.Background(), LogContext{
		TraceID:   "trace-context-1",
		RequestID: "request-context-1",
		JobID:     "job-context-1",
		ParentID:  "parent-context-1",
	})
	runtime.Logger.InfoContext(ctx, "context event", "event", "context.test")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	written := output.String()
	for _, marker := range []string{`"traceId":"trace-context-1"`, `"requestId":"request-context-1"`, `"jobId":"job-context-1"`, `"parentId":"parent-context-1"`} {
		if !strings.Contains(written, marker) {
			t.Fatalf("context output missing %s: %s", marker, written)
		}
	}
}

func TestRuntimeAddsUnexpectedFailureFallbackContext(t *testing.T) {
	var output lockedBuffer
	runtime, err := NewRuntime("info", &output, RuntimeOptions{Role: "go-worker"})
	if err != nil {
		t.Fatal(err)
	}
	runtime.Logger.Error("unclassified failure", "event", "worker.failed", "error", "boom")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	written := output.String()
	for _, marker := range []string{`"failureClass":"unexpected"`, `"stack":`, `"event":"worker.failed"`} {
		if !strings.Contains(written, marker) {
			t.Fatalf("failure output missing %s: %s", marker, written)
		}
	}
}

func TestRuntimeShutdownTimeoutDoesNotBlockCaller(t *testing.T) {
	w := &blockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	runtime, err := NewRuntime("info", w, RuntimeOptions{Role: "go-test"})
	if err != nil {
		t.Fatal(err)
	}
	runtime.Logger.Info("blocked destination", "event", "writer.blocked")
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("destination writer was not entered")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	if err := runtime.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 200*time.Millisecond {
		t.Fatalf("Shutdown() blocked for %s", elapsed)
	}
	close(w.release)
	finishCtx, finishCancel := context.WithTimeout(context.Background(), time.Second)
	defer finishCancel()
	if err := runtime.Shutdown(finishCtx); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeCountsNormalAndFailureDropsWithReservedCapacity(t *testing.T) {
	w := &blockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	runtime, err := NewRuntime("info", w, RuntimeOptions{
		Role:                 "go-test",
		NormalQueueCapacity:  1,
		FailureQueueCapacity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.Logger.Info("occupy writer", "event", "writer.occupy")
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("destination writer was not entered")
	}

	runtime.Logger.Info("normal queued", "event", "normal.queued")
	runtime.Logger.Info("normal dropped", "event", "normal.dropped")
	runtime.Logger.Error("failure queued", "event", "failure.queued")
	runtime.Logger.Error("failure dropped", "event", "failure.dropped")
	stats := runtime.Stats()
	if stats.NormalDropped != 1 || stats.FailureDropped != 1 {
		t.Fatalf("unexpected drop stats: %+v", stats)
	}
	if stats.PendingNormal != 1 || stats.PendingFailure != 1 {
		t.Fatalf("unexpected pending stats: %+v", stats)
	}

	close(w.release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}
