package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewIsSynchronousContextAwareAndDoesNotFallbackStackForExpectedFailures(t *testing.T) {
	writer := &blockingCaptureWriter{entered: make(chan struct{}), release: make(chan struct{})}
	logger, err := New("info", writer)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithLogContext(context.Background(), LogContext{
		TraceID:   "trace-new-1",
		RequestID: "request-new-1",
	})
	done := make(chan struct{})
	go func() {
		logger.ErrorContext(ctx, "expected failure", "event", "compat.expected", "failureClass", "expected")
		logger.ErrorContext(ctx, "aborted request", "event", "compat.aborted", "failureClass", "aborted")
		close(done)
	}()
	select {
	case <-writer.entered:
	case <-time.After(time.Second):
		t.Fatal("New logger did not synchronously write to its destination")
	}
	select {
	case <-done:
		t.Fatal("New logger returned before destination accepted the record")
	case <-time.After(20 * time.Millisecond):
	}
	close(writer.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("New logger did not finish after destination was released")
	}

	lines := strings.Split(strings.TrimSpace(writer.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("log lines = %d, want 2: %s", len(lines), writer.String())
	}
	for _, line := range lines {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode log line: %v: %s", err, line)
		}
		if event["traceId"] != "trace-new-1" || event["requestId"] != "request-new-1" {
			t.Fatalf("missing context fields: %#v", event)
		}
		if _, ok := event["stack"]; ok {
			t.Fatalf("expected/aborted compatibility log unexpectedly has stack: %#v", event)
		}
	}
}

type blockingCaptureWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	output  bytes.Buffer
	mu      sync.Mutex
}

func (w *blockingCaptureWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.output.Write(p)
}

func (w *blockingCaptureWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.output.String()
}
