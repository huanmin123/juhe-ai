package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
