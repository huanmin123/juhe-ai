package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/logging"
)

func TestRequestLoggingMiddlewareUsesContextWithoutDuplicateBaseFields(t *testing.T) {
	var output bytes.Buffer
	logger, err := logging.New("info", &output)
	if err != nil {
		t.Fatal(err)
	}
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := requestIDMiddleware(requestLoggingMiddleware(logger)(terminal))
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("X-Request-Id", "request-duplicate-test")
	req.Header.Set("X-Trace-Id", "trace-duplicate-test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("log lines = %d, want 2: %s", len(lines), output.String())
	}
	for _, line := range lines {
		for _, key := range []string{"service", "role", "traceId", "requestId"} {
			if got := strings.Count(line, `"`+key+`"`); got != 1 {
				t.Fatalf("%s appears %d times in log line: %s", key, got, line)
			}
		}
	}
}

func TestRequestLoggingMiddlewareRecordsCompletionResultAndRouteTemplate(t *testing.T) {
	var output bytes.Buffer
	logger, err := logging.New("info", &output)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(requestLoggingMiddleware(logger))
	router.Get("/items/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("created"))
	})
	handler := requestIDMiddleware(router)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/items/42", nil))

	var completed map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode log line: %v: %s", err, line)
		}
		if event["event"] == "http.request.complete" {
			completed = event
		}
	}
	if completed == nil {
		t.Fatalf("completion event missing: %s", output.String())
	}
	if completed["statusCode"] != float64(http.StatusCreated) || completed["responseBytes"] != float64(len("created")) {
		t.Fatalf("completion result missing: %#v", completed)
	}
	if completed["routeTemplate"] != "/items/{id}" || completed["path"] != "/items/42" {
		t.Fatalf("route fields missing: %#v", completed)
	}
}
