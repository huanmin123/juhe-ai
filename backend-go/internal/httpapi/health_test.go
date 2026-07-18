package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/config"
)

func TestHealthRoutes(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000},
		Logger: slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
	})

	for _, path := range []string{"/__aisys__/health", "/__aisys__/api/health"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, rec.Code)
		}

		var body HealthResponse
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("%s decode: %v", path, err)
		}
		if !body.Success || body.Status != "ok" {
			t.Fatalf("%s body = %+v", path, body)
		}
	}
}

func TestHealthDependencyErrorsAreRedacted(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config: config.Config{
			Host:            "127.0.0.1",
			Port:            3000,
			PostgresURL:     "://bad-url",
			ShutdownTimeout: 1,
		},
		Logger: slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/health", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var body HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != "degraded" {
		t.Fatalf("status = %q, want degraded", body.Status)
	}
	if got := body.Dependencies["postgres"].Error; got != "dependency check failed" {
		t.Fatalf("postgres error = %q", got)
	}
}

func TestReadinessRoutesReturnServiceUnavailableWhenDependencyFails(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config: config.Config{
			Host:        "127.0.0.1",
			Port:        3000,
			PostgresURL: "://bad-url",
		},
		Logger: slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
	})

	for _, path := range []string{"/__aisys__/readyz", "/__aisys__/api/readyz"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want 503", path, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", path, got)
		}
		var body HealthResponse
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("%s decode: %v", path, err)
		}
		if body.Status != "degraded" || body.Success {
			t.Fatalf("%s body = %+v, want degraded failure", path, body)
		}
		if got := body.Dependencies["postgres"].Error; got != "dependency check failed" {
			t.Fatalf("%s postgres error = %q", path, got)
		}
	}
}

func TestReadinessRoutesReturnOKWhenDependenciesAreNotConfigured(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000},
		Logger: slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
	})

	for _, path := range []string{"/__aisys__/readyz", "/__aisys__/api/readyz"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", path, got)
		}
	}
}

func TestReadinessRequiresPostgresForEnabledBusinessRoutes(t *testing.T) {
	for _, cfg := range []config.Config{
		{Host: "127.0.0.1", Port: 3000, PublicAPIEnabled: true},
		{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		{Host: "127.0.0.1", Port: 3000, ManagementAuthSessionsEnabled: true},
	} {
		handler := NewReadinessHandler(cfg, slog.New(slog.NewTextHandler(testWriter{t: t}, nil)))
		req := httptest.NewRequest(http.MethodGet, "/__aisys__/readyz", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("config %+v status = %d, want 503", cfg, rec.Code)
		}
		var body HealthResponse
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got := body.Dependencies["postgres"]; got.Configured || got.Status != "error" {
			t.Fatalf("postgres = %+v, want required dependency error", got)
		}
	}
}

func TestReadinessCachesDependencyChecks(t *testing.T) {
	now := time.Date(2026, 7, 18, 15, 30, 0, 0, time.UTC)
	checks := 0
	handler := &ReadinessHandler{
		checkDependencies: func(context.Context) (map[string]CheckResult, string) {
			checks++
			return map[string]CheckResult{
				"postgres": {Configured: true, Status: "ok"},
			}, "ok"
		},
		now:      func() time.Time { return now },
		cacheTTL: 2 * time.Second,
	}

	request := func() {
		req := httptest.NewRequest(http.MethodGet, "/__aisys__/readyz", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	}

	request()
	request()
	if checks != 1 {
		t.Fatalf("dependency checks = %d, want 1 within cache TTL", checks)
	}

	now = now.Add(3 * time.Second)
	request()
	if checks != 2 {
		t.Fatalf("dependency checks = %d, want 2 after cache expiry", checks)
	}
}

func TestDiagnosticsRequireLoopback(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config: config.Config{
			Host:            "127.0.0.1",
			Port:            3000,
			MetricsEnabled:  true,
			PprofEnabled:    true,
			ShutdownTimeout: 1,
		},
		Logger: slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
	})

	for _, path := range []string{"/__aisys__/metrics", "/__debug/pprof"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "203.0.113.10:12345"
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s status = %d, want 403", path, rec.Code)
		}
	}
}

func TestDiagnosticsAllowLoopback(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config: config.Config{
			Host:            "127.0.0.1",
			Port:            3000,
			MetricsEnabled:  true,
			PprofEnabled:    true,
			ShutdownTimeout: 1,
		},
		Logger: slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
	})

	for _, path := range []string{"/__aisys__/metrics", "/__debug/pprof"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "127.0.0.1:12345"
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, rec.Code)
		}
	}
}

func TestDiagnosticsUseTrustProxyClientIP(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config: config.Config{
			Host:            "127.0.0.1",
			Port:            3000,
			TrustProxy:      "true",
			MetricsEnabled:  true,
			PprofEnabled:    true,
			ShutdownTimeout: 1,
		},
		Logger: slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
	})

	for _, path := range []string{"/__aisys__/metrics", "/__debug/pprof"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("X-Forwarded-For", "203.0.113.10")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s status = %d, want 403", path, rec.Code)
		}
	}
}

type testWriter struct {
	t *testing.T
}

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}
