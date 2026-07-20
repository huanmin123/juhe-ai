package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestIDMiddlewarePreservesTraceAndRequestIDs(t *testing.T) {
	handler := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := traceIDFromContext(r.Context()); got != "0123456789abcdef0123456789abcdef" {
			t.Fatalf("trace id = %q", got)
		}
		if got := requestIDFromContext(r.Context()); got != "request-1" {
			t.Fatalf("request id = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("X-Request-Id", "request-1")
	req.Header.Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Trace-Id"); got != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("X-Trace-Id = %q", got)
	}
}
