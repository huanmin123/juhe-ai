package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPprofMuxRegistersAllStandardPaths asserts the package-level pprof mux
// resolves every standard net/http/pprof path to a registered handler.
// ServeMux.Handler takes a request (not a method/path pair); a miss would
// still return a non-nil "page not found" handler with an EMPTY pattern, so
// both the handler and the matched pattern are asserted.
func TestPprofMuxRegistersAllStandardPaths(t *testing.T) {
	paths := []string{
		"/debug/pprof/",
		"/debug/pprof/heap",
		"/debug/pprof/allocs",
		"/debug/pprof/goroutine",
		"/debug/pprof/block",
		"/debug/pprof/mutex",
		"/debug/pprof/threadcreate",
		"/debug/pprof/cmdline",
		"/debug/pprof/profile",
		"/debug/pprof/symbol",
		"/debug/pprof/trace",
	}
	for _, path := range paths {
		handler, pattern := pprofMux.Handler(httptest.NewRequest(http.MethodGet, path, nil))
		if handler == nil {
			t.Errorf("pprofMux.Handler(GET %q) handler = nil, want non-nil", path)
		}
		if pattern == "" {
			t.Errorf("pprofMux.Handler(GET %q) pattern = \"\", want a registered pprof pattern", path)
		}
	}
}

// TestPprofEndpointsOnHealthListener covers the read-only pprof diagnostics
// surface mounted on the health listener handler: the profile index, a
// heap?debug=1 text render, and the GET-only gating (non-GET must fall
// through to the health handler and 404).
func TestPprofEndpointsOnHealthListener(t *testing.T) {
	handler := newHealthServerHandler(http.HandlerFunc(http.NotFound))

	// GET /debug/pprof/ renders the standard profile index.
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /debug/pprof/ status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if !strings.Contains(recorder.Body.String(), "Types of profiles available") {
		t.Fatalf("GET /debug/pprof/ body missing profile index heading, got:\n%s", recorder.Body.String())
	}

	// GET /debug/pprof/heap?debug=1 renders the text heap profile.
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/debug/pprof/heap?debug=1", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /debug/pprof/heap?debug=1 status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if !strings.Contains(recorder.Body.String(), "heap profile:") {
		t.Fatalf("GET /debug/pprof/heap?debug=1 body missing heap profile header, got prefix:\n%s", recorder.Body.String()[:min(len(recorder.Body.String()), 200)])
	}

	// POST does not enter the pprof branch and falls through to the health
	// handler, which 404s unknown paths.
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/debug/pprof/heap", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("POST /debug/pprof/heap status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

// TestHealthServerHandlerKeepsExistingRoutes proves the extraction of the
// inline healthServer.Handler closure did not change the pre-existing
// /health and /__aisys__/metrics behavior.
func TestHealthServerHandlerKeepsExistingRoutes(t *testing.T) {
	healthCalled := false
	healthHandler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		healthCalled = true
		if request.Method != http.MethodGet || request.URL.Path != "/health" {
			http.NotFound(response, request)
			return
		}
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("ok"))
	})
	handler := newHealthServerHandler(healthHandler)

	// GET /health still reaches the readiness handler.
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /health status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if !healthCalled {
		t.Fatalf("GET /health did not reach the health handler")
	}

	// GET /__aisys__/metrics still renders the process metrics registry.
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /__aisys__/metrics status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if !strings.Contains(recorder.Body.String(), "go_goroutines") {
		t.Fatalf("GET /__aisys__/metrics body missing go_goroutines, got:\n%s", recorder.Body.String())
	}
}
