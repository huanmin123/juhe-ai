package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

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
