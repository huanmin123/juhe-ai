package kernel

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// D-190 / D-191 (BUG-0175 wave 4 W4-E): the kernel middleware drives the
// prometheus HTTP metric hooks and emits the request lifecycle events the
// runtime-log grep face parses.

type recordedEvent struct {
	level   string
	fields  map[string]any
	message string
}

type recordingSink struct {
	mu     sync.Mutex
	events []recordedEvent
}

func (s *recordingSink) EmitRequestEvent(level string, fields map[string]any, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, recordedEvent{level: level, fields: fields, message: message})
}

func (s *recordingSink) byEvent(name string) []recordedEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	matched := []recordedEvent{}
	for _, event := range s.events {
		if event.fields["event"] == name {
			matched = append(matched, event)
		}
	}
	return matched
}

type fakeMetrics struct {
	mu       sync.Mutex
	starts   []string
	finishes []string
}

// hooks mirrors the real gatewayusage seam including the observability-route
// skip (Start answers nil there, Finish receives the nil handle).
func (m *fakeMetrics) hooks() *HTTPMetricHooks {
	return &HTTPMetricHooks{
		Start: func(path string, method string, startedAtMs int64) any {
			m.mu.Lock()
			defer m.mu.Unlock()
			if path == "/__aisys__/metrics" {
				return nil
			}
			m.starts = append(m.starts, path+"|"+method)
			return path
		},
		Finish: func(request any, statusCode *int, outcome string, finishedAtMs int64, failureScope string) {
			m.mu.Lock()
			defer m.mu.Unlock()
			// Mirror FinishHTTPMetricRequest: a nil handle (unmeasured route)
			// is the accepted no-op shape, never a recorded finish.
			if request == nil {
				return
			}
			status := "nil"
			if statusCode != nil {
				status = string(rune('0' + *statusCode/100))
			}
			m.finishes = append(m.finishes, request.(string)+"|"+outcome+"|"+status+"|"+failureScope)
		},
	}
}

func resetObservability(t *testing.T, sink RequestEventSink, hooks *HTTPMetricHooks) {
	t.Helper()
	SetRequestEventSink(sink)
	SetHTTPMetricHooks(hooks)
	t.Cleanup(func() {
		SetRequestEventSink(nil)
		SetHTTPMetricHooks(nil)
	})
}

func TestMiddlewareEmitsStartedAndCompletedWithMetrics(t *testing.T) {
	sink := &recordingSink{}
	metrics := &fakeMetrics{}
	resetObservability(t, sink, metrics.hooks())

	handlerCalled := false
	handler := RequestContextMiddleware(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if !handlerCalled {
		t.Fatal("handler must run")
	}
	if started := sink.byEvent("http_request_started"); len(started) != 1 {
		t.Fatalf("started events = %d, want 1", len(started))
	} else if started[0].fields["path"] != "/v1/chat/completions" || started[0].level != "info" {
		t.Fatalf("started event = %+v", started[0])
	}
	completed := sink.byEvent("http_request_completed")
	if len(completed) != 1 {
		t.Fatalf("completed events = %d, want 1", len(completed))
	}
	if completed[0].fields["statusCode"] != 200 || completed[0].fields["failureScope"] != "none" {
		t.Fatalf("completed event = %+v", completed[0])
	}
	summary := sink.byEvent("gateway.request.timing_summary")
	if len(summary) != 1 {
		t.Fatalf("timing summary events = %d, want 1 (gateway route)", len(summary))
	}
	if summary[0].fields["outcome"] != "success" {
		t.Fatalf("timing summary outcome = %v", summary[0].fields["outcome"])
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if len(metrics.starts) != 1 || len(metrics.finishes) != 1 {
		t.Fatalf("metric starts = %v finishes = %v", metrics.starts, metrics.finishes)
	}
	if !strings.HasSuffix(metrics.finishes[0], "completed|2|none") {
		t.Fatalf("metric finish = %q", metrics.finishes[0])
	}
}

func TestMiddlewareClosedOnCanceledContext(t *testing.T) {
	sink := &recordingSink{}
	metrics := &fakeMetrics{}
	resetObservability(t, sink, metrics.hooks())

	handler := RequestContextMiddleware(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	// A pre-canceled request context mirrors the net/http contract: the server
	// cancels the context when the client connection drops before the
	// response settles, which is the Node close-without-writableEnded shape.
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request = request.WithContext(canceledContext())
	handler.ServeHTTP(httptest.NewRecorder(), request)

	if closed := sink.byEvent("http_request_closed"); len(closed) != 1 {
		t.Fatalf("closed events = %d, want 1 (%+v)", len(closed), sink.events)
	}
	if completed := sink.byEvent("http_request_completed"); len(completed) != 0 {
		t.Fatalf("aborted request must not emit completed: %+v", completed)
	}
	if summary := sink.byEvent("gateway.request.timing_summary"); len(summary) != 1 || summary[0].fields["outcome"] != "aborted" {
		t.Fatalf("aborted timing summary = %+v", summary)
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if len(metrics.finishes) != 1 || !strings.HasSuffix(metrics.finishes[0], "aborted|nil|none") {
		t.Fatalf("abort metric finish = %v", metrics.finishes)
	}
}

func TestMiddlewareFourHundredIsWarnFiveHundredIsError(t *testing.T) {
	sink := &recordingSink{}
	resetObservability(t, sink, nil)

	statuses := map[int]string{404: "warn", 502: "error", 200: "info"}
	for status, wantLevel := range statuses {
		handler := RequestContextMiddleware(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/__aisys__/api/whatever", nil))
		completed := sink.byEvent("http_request_completed")
		if len(completed) == 0 || completed[len(completed)-1].level != wantLevel {
			t.Fatalf("status %d level = %v, want %q", status, completed, wantLevel)
		}
	}
}

func TestMiddlewareHealthCompletionIsDebug(t *testing.T) {
	sink := &recordingSink{}
	resetObservability(t, sink, nil)
	handler := RequestContextMiddleware(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/__aisys__/api/health", nil))
	completed := sink.byEvent("http_request_completed")
	if len(completed) != 1 || completed[0].level != "debug" {
		t.Fatalf("health completion = %+v, want debug", completed)
	}
}

func TestUnmeasuredRouteSkipsMetricsButEmitsEvents(t *testing.T) {
	sink := &recordingSink{}
	metrics := &fakeMetrics{}
	resetObservability(t, sink, metrics.hooks())
	handler := RequestContextMiddleware(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/__aisys__/metrics", nil))
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if len(metrics.starts) != 0 || len(metrics.finishes) != 0 {
		t.Fatalf("observability route must not be measured: %v %v", metrics.starts, metrics.finishes)
	}
	if len(sink.byEvent("http_request_started")) != 1 {
		t.Fatal("started event still emitted for observability route")
	}
}

// TestSlogSinkFlattensFieldsAsTopLevelJSON locks the D-191 runtime-log
// contract: the slog JSON handler must render the event fields as top-level
// JSON keys so runtimeLogFieldsFromLine (logreads) parses event/traceId/level
// from the produced lines exactly like the archived pino records.
func TestSlogSinkFlattensFieldsAsTopLevelJSON(t *testing.T) {
	var buffer strings.Builder
	logger := slog.New(slog.NewJSONHandler(&buffer, nil))
	sink := NewSlogRequestEventSink(logger)
	sink.EmitRequestEvent("info", map[string]any{
		"event":   "http_request_started",
		"traceId": "trace-1",
		"method":  "POST",
	}, "HTTP 请求开始")

	var parsed struct {
		Time    string `json:"time"`
		Level   string `json:"level"`
		Msg     string `json:"msg"`
		Event   string `json:"event"`
		TraceID string `json:"traceId"`
		Method  string `json:"method"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(buffer.String())), &parsed); err != nil {
		t.Fatalf("line is not JSON: %v\n%q", err, buffer.String())
	}
	if parsed.Event != "http_request_started" || parsed.TraceID != "trace-1" || parsed.Method != "POST" {
		t.Fatalf("parsed = %+v", parsed)
	}
	if parsed.Level != "INFO" || parsed.Msg != "HTTP 请求开始" {
		t.Fatalf("level/msg = %q/%q", parsed.Level, parsed.Msg)
	}
}
