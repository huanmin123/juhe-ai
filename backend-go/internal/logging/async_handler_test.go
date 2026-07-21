package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
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

type rootFailure struct {
	message string
}

func (e *rootFailure) Error() string {
	return e.message
}

func TestRuntimeExpandsWrappedErrorAndLabelsFallbackStack(t *testing.T) {
	var output lockedBuffer
	runtime, err := NewRuntime("info", &output, RuntimeOptions{Role: "go-worker"})
	if err != nil {
		t.Fatal(err)
	}
	root := &rootFailure{message: "database unavailable"}
	wrapped := fmt.Errorf("refresh failed: %w", root)
	runtime.Logger.Error("worker refresh failed", "event", "worker.refresh.failed", "error", wrapped)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}

	event := findJSONEvent(t, output.String(), "worker.refresh.failed")
	if got := event["errorMessage"]; got != wrapped.Error() {
		t.Fatalf("errorMessage = %#v, want %q", got, wrapped.Error())
	}
	if got, ok := event["errorType"].(string); !ok || got == "" {
		t.Fatalf("errorType = %#v, want non-empty string", event["errorType"])
	}
	if got := event["stackSource"]; got != "log_call_site_fallback" {
		t.Fatalf("stackSource = %#v, want log_call_site_fallback", got)
	}
	causes, ok := event["errorCauseChain"].([]any)
	if !ok || len(causes) != 1 {
		t.Fatalf("errorCauseChain = %#v, want one cause", event["errorCauseChain"])
	}
	cause, ok := causes[0].(map[string]any)
	if !ok || cause["message"] != root.Error() || cause["type"] != "*logging.rootFailure" {
		t.Fatalf("errorCauseChain[0] = %#v, want typed root cause", causes[0])
	}
}

func TestFailureErrorCauseChainExpandsJoinedErrorsWithinBound(t *testing.T) {
	joined := errors.Join(
		&rootFailure{message: "primary database failure"},
		&rootFailure{message: "secondary cache failure"},
	)
	chain := failureErrorCauseChain(fmt.Errorf("refresh failed: %w", joined))
	if len(chain) != 3 {
		t.Fatalf("cause chain length = %d, want join node plus two causes: %#v", len(chain), chain)
	}
	for _, want := range []string{"primary database failure", "secondary cache failure"} {
		found := false
		for _, cause := range chain {
			if cause.Message == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("cause chain missing %q: %#v", want, chain)
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
	w := &capturingBlockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
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
	runtime.Logger.Error("failure primary queued", "event", "failure.primary.queued")
	runtime.Logger.Error("failure emergency queued", "event", "failure.emergency.queued")
	longFailure := strings.Repeat("failure-scene-", 256) + "must-not-survive-bounded-snapshot"
	droppedFailure := fmt.Errorf("outer failure: %w", errors.New(longFailure))
	droppedContext := WithLogContext(context.Background(), LogContext{
		TraceID:   "drop-trace-1",
		RequestID: "drop-request-1",
		JobID:     "drop-job-1",
		ParentID:  "drop-parent-1",
	})
	dropCallDone := make(chan struct{})
	go func() {
		runtime.Logger.ErrorContext(droppedContext, "failure dropped with bounded scene", "event", "failure.dropped", "error", droppedFailure)
		close(dropCallDone)
	}()
	select {
	case <-dropCallDone:
	case <-time.After(100 * time.Millisecond):
		close(w.release)
		t.Fatal("failure logging waited for saturated destination")
	}
	stats := runtime.Stats()
	if stats.NormalDropped != 1 || stats.FailureDropped != 1 {
		t.Fatalf("unexpected drop stats: %+v", stats)
	}
	if stats.PendingNormal != 1 || stats.PendingFailure != 2 {
		t.Fatalf("unexpected pending stats: %+v", stats)
	}
	if written := w.String(); strings.Contains(written, `"event":"system_log_drop"`) {
		t.Fatalf("drop summary was written synchronously while destination was blocked: %s", written)
	}

	close(w.release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	written := w.String()
	for _, event := range []string{"failure.primary.queued", "failure.emergency.queued", "system_log_drop"} {
		if !strings.Contains(written, `"event":"`+event+`"`) {
			t.Fatalf("drained output missing %s: %s", event, written)
		}
	}
	dropSummary := findJSONEvent(t, written, "system_log_drop")
	lastFailure, ok := dropSummary["lastFailure"].(map[string]any)
	if !ok {
		t.Fatalf("drop summary lastFailure = %#v, want object", dropSummary["lastFailure"])
	}
	if lastFailure["event"] != "failure.dropped" || lastFailure["message"] != "failure dropped with bounded scene" {
		t.Fatalf("drop summary lost failure identity: %#v", lastFailure)
	}
	for key, want := range map[string]string{
		"failureClass": "unexpected",
		"traceId":      "drop-trace-1",
		"requestId":    "drop-request-1",
		"jobId":        "drop-job-1",
		"parentId":     "drop-parent-1",
	} {
		if got := lastFailure[key]; got != want {
			t.Fatalf("drop summary %s = %#v, want %q", key, got, want)
		}
	}
	errorMessage, ok := lastFailure["errorMessage"].(string)
	if !ok || errorMessage == "" || len(errorMessage) > 1024 || strings.Contains(errorMessage, "must-not-survive-bounded-snapshot") {
		t.Fatalf("drop summary errorMessage is not bounded: len=%d value=%q", len(errorMessage), errorMessage)
	}
	if errorType, ok := lastFailure["errorType"].(string); !ok || errorType == "" {
		t.Fatalf("drop summary errorType = %#v, want non-empty string", lastFailure["errorType"])
	}
	if stack, ok := lastFailure["stack"].(string); !ok || stack == "" {
		t.Fatalf("drop summary stack = %#v, want bounded stack", lastFailure["stack"])
	}
	if stackSource := lastFailure["stackSource"]; stackSource != "log_call_site_fallback" {
		t.Fatalf("drop summary stackSource = %#v, want log_call_site_fallback", stackSource)
	}
	causes, ok := lastFailure["errorCauseChain"].([]any)
	if !ok || len(causes) != 1 {
		t.Fatalf("drop summary errorCauseChain = %#v, want one cause", lastFailure["errorCauseChain"])
	}
}

func TestFailureEmergencyLaneHasIndependentByteBudget(t *testing.T) {
	w := &capturingBlockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	runtime, err := NewRuntime("info", w, RuntimeOptions{
		Role:                 "go-test",
		NormalQueueCapacity:  1,
		FailureQueueCapacity: 1,
		FailureQueueBytes:    1,
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

	runtime.Logger.Error("failure uses emergency byte budget", "event", "failure.emergency.bytes")
	stats := runtime.Stats()
	if stats.FailureDropped != 0 || stats.PendingFailure != 1 || stats.PendingBytes <= 0 {
		t.Fatalf("failure did not use independent emergency byte budget: %+v", stats)
	}

	close(w.release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if written := w.String(); !strings.Contains(written, `"event":"failure.emergency.bytes"`) {
		t.Fatalf("emergency failure was not delivered: %s", written)
	}
}

func TestFailureDropSnapshotTruncatesAtUTF8Boundary(t *testing.T) {
	bounded, truncated := boundedFailureSnapshotValue(strings.Repeat("错", 400), maxFailureSnapshotValueBytes)
	if !truncated {
		t.Fatal("snapshot value was not truncated")
	}
	if len(bounded) > maxFailureSnapshotValueBytes {
		t.Fatalf("bounded value uses %d bytes, want at most %d", len(bounded), maxFailureSnapshotValueBytes)
	}
	if !utf8.ValidString(bounded) {
		t.Fatalf("bounded value ended inside a UTF-8 sequence: %q", bounded)
	}
}

type panicLogValuer struct {
	calls *int
}

func (value panicLogValuer) LogValue() slog.Value {
	*value.calls = *value.calls + 1
	panic("failure queue must not resolve arbitrary LogValuer values")
}

type selfReferentialValue struct {
	Self *selfReferentialValue
}

type oversizedError struct {
	message string
	cause   error
}

func (err oversizedError) Error() string { return err.message }
func (err oversizedError) Unwrap() error { return err.cause }

func TestRuntimeBoundsFailureRecordBeforePrimaryLaneAndSkipsOpaqueValues(t *testing.T) {
	var output lockedBuffer
	runtime, err := NewRuntime("info", &output, RuntimeOptions{Role: "go-test"})
	if err != nil {
		t.Fatal(err)
	}
	logValuerCalls := 0
	cycle := &selfReferentialValue{}
	cycle.Self = cycle
	root := oversizedError{message: strings.Repeat("root-cause-", 8192)}
	failure := oversizedError{message: strings.Repeat("outer-failure-", 8192), cause: root}
	runtime.Logger.Error(
		strings.Repeat("failure message ", 8192),
		"event", "failure.primary.bounded",
		"error", failure,
		"opaque", cycle,
		"lazy", panicLogValuer{calls: &logValuerCalls},
		"stack", strings.Repeat("stack ", 8192),
	)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if logValuerCalls != 0 {
		t.Fatalf("LogValuer resolved %d times", logValuerCalls)
	}
	event := findJSONEvent(t, output.String(), "failure.primary.bounded")
	for _, key := range []string{"msg", "errorMessage", "stack"} {
		value, ok := event[key].(string)
		if !ok || len(value) == 0 || len(value) > maxFailureSnapshotStackBytes {
			t.Fatalf("%s not bounded: %#v", key, event[key])
		}
	}
	if opaque, ok := event["opaque"].(string); !ok || !strings.Contains(opaque, "opaque") {
		t.Fatalf("opaque slog.Any was not replaced by a bounded marker: %#v", event["opaque"])
	}
	if lazy, ok := event["lazy"].(string); !ok || !strings.Contains(lazy, "LogValuer") {
		t.Fatalf("lazy slog.Any was not replaced by a bounded marker: %#v", event["lazy"])
	}
	causes, ok := event["errorCauseChain"].([]any)
	if !ok || len(causes) != 1 {
		t.Fatalf("errorCauseChain = %#v, want one bounded cause", event["errorCauseChain"])
	}
	cause := causes[0].(map[string]any)
	if message, ok := cause["message"].(string); !ok || len(message) > maxFailureSnapshotValueBytes {
		t.Fatalf("bounded cause message = %#v", cause["message"])
	}
}

func TestRuntimeBoundsNormalRecordAndWithAttrsBeforeQueue(t *testing.T) {
	var output lockedBuffer
	runtime, err := NewRuntime("info", &output, RuntimeOptions{
		Role:             "go-test",
		NormalQueueBytes: 16 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	logValuerCalls := 0
	cycle := &selfReferentialValue{}
	cycle.Self = cycle
	large := strings.Repeat("normal-value-", 128*1024)
	runtime.Logger.With("boundWithAttr", large).Info(
		"normal record must be bounded before enqueue",
		"event", "normal.primary.bounded",
		"large", large,
		"opaque", cycle,
		"lazy", panicLogValuer{calls: &logValuerCalls},
	)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if logValuerCalls != 0 {
		t.Fatalf("normal LogValuer resolved %d times", logValuerCalls)
	}
	event := findJSONEvent(t, output.String(), "normal.primary.bounded")
	for _, key := range []string{"boundWithAttr", "large"} {
		value, ok := event[key].(string)
		if !ok || value == "" || len(value) > maxFailureSnapshotValueBytes {
			t.Fatalf("%s not bounded: %#v", key, event[key])
		}
	}
	if opaque, ok := event["opaque"].(string); !ok || !strings.Contains(opaque, "opaque") {
		t.Fatalf("normal slog.Any was not replaced by a bounded marker: %#v", event["opaque"])
	}
	if lazy, ok := event["lazy"].(string); !ok || !strings.Contains(lazy, "LogValuer") {
		t.Fatalf("normal LogValuer was not replaced by a bounded marker: %#v", event["lazy"])
	}
	if event["truncated"] != true || event["truncationReason"] == "" {
		t.Fatalf("normal truncation facts missing: %#v", event)
	}
	if len(output.String()) > 16*1024 {
		t.Fatalf("bounded normal record produced %d bytes", len(output.String()))
	}
}

type panicUnwrapError struct{}

func (panicUnwrapError) Error() string { return "panic unwrap" }
func (panicUnwrapError) Unwrap() error { panic("unwrap getter failed") }

func TestRuntimeFailureCauseCaptureDoesNotPropagateUnwrapPanic(t *testing.T) {
	var output lockedBuffer
	runtime, err := NewRuntime("info", &output, RuntimeOptions{Role: "go-test"})
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("logger.Error propagated Unwrap panic: %v", recovered)
			}
		}()
		runtime.Logger.Error("hostile error", "event", "failure.unwrap.panic", "error", panicUnwrapError{})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	event := findJSONEvent(t, output.String(), "failure.unwrap.panic")
	if event["failureClass"] != "unexpected" || event["errorMessage"] != "panic unwrap" {
		t.Fatalf("hostile error evidence missing: %#v", event)
	}
}

type asyncFailingWriter struct {
	err error
}

func (writer asyncFailingWriter) Write(p []byte) (int, error) {
	return 0, writer.err
}

func TestRuntimePublishesBoundedLastWriterErrorInStats(t *testing.T) {
	runtime, err := NewRuntime("info", asyncFailingWriter{err: errors.New(strings.Repeat("writer unavailable ", 1024))}, RuntimeOptions{Role: "go-test"})
	if err != nil {
		t.Fatal(err)
	}
	runtime.Logger.Error("write will fail", "event", "writer.failure")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	stats := runtime.Stats()
	if stats.WriterErrors != 1 || stats.LastWriterError == "" || len(stats.LastWriterError) > maxFailureSnapshotValueBytes || !stats.LastWriterErrorTruncated || stats.LastWriterErrorAt == "" {
		t.Fatalf("writer failure stats are not bounded/observable: %+v", stats)
	}
}

type capturingBlockingWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	output  lockedBuffer
}

func (w *capturingBlockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return w.output.Write(p)
}

func (w *capturingBlockingWriter) String() string {
	return w.output.String()
}

func findJSONEvent(t *testing.T, output string, event string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode log line: %v: %s", err, line)
		}
		if record["event"] == event {
			return record
		}
	}
	t.Fatalf("event %q not found in output: %s", event, output)
	return nil
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
