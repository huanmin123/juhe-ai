package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/config"
)

type readinessProberFunc func(context.Context) error

func (f readinessProberFunc) Probe(ctx context.Context) error {
	return f(ctx)
}

type observingContext struct {
	context.Context
	doneCalled chan struct{}
	once       sync.Once
}

func (c *observingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.doneCalled) })
	return c.Context.Done()
}

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
	} {
		handler := NewReadinessHandler(cfg, slog.New(slog.NewTextHandler(testWriter{t: t}, nil)), nil)
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

func TestNodeModelCatalogBridgeDependency(t *testing.T) {
	tests := []struct {
		name           string
		cfg            config.Config
		prober         ReadinessProber
		wantConfigured bool
		wantStatus     string
		wantHTTPStatus int
	}{
		{
			name: "management success",
			cfg:  config.Config{ManagementAPIEnabled: true},
			prober: readinessProberFunc(func(context.Context) error {
				return nil
			}),
			wantConfigured: true,
			wantStatus:     "ok",
			wantHTTPStatus: http.StatusServiceUnavailable,
		},
		{
			name: "management failure",
			cfg:  config.Config{ManagementAPIEnabled: true},
			prober: readinessProberFunc(func(context.Context) error {
				return errors.New("http://127.0.0.1:3001 private response body")
			}),
			wantConfigured: true,
			wantStatus:     "error",
			wantHTTPStatus: http.StatusServiceUnavailable,
		},
		{
			name:           "management missing prober",
			cfg:            config.Config{ManagementAPIEnabled: true},
			wantConfigured: false,
			wantStatus:     "error",
			wantHTTPStatus: http.StatusServiceUnavailable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := NewRouter(RouterOptions{
				Config:                       test.cfg,
				Logger:                       slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
				ManagementCaptchaHandler:     http.NotFoundHandler(),
				ManagementAPIAuthMiddleware: func(next http.Handler) http.Handler {
					return next
				},
				NodeModelCatalogBridgeReadinessProber: test.prober,
			})
			req := httptest.NewRequest(http.MethodGet, "/__aisys__/readyz", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != test.wantHTTPStatus {
				t.Fatalf("status = %d, want %d", rec.Code, test.wantHTTPStatus)
			}
			var body HealthResponse
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			got := body.Dependencies["nodeModelCatalogBridge"]
			if got.Configured != test.wantConfigured || got.Status != test.wantStatus {
				t.Fatalf("nodeModelCatalogBridge = %+v", got)
			}
			if got.Status == "error" && got.Error != "dependency check failed" {
				t.Fatalf("error = %q, want redacted dependency error", got.Error)
			}
			if strings.Contains(rec.Body.String(), "127.0.0.1") || strings.Contains(rec.Body.String(), "private response body") {
				t.Fatalf("response leaked probe details: %s", rec.Body.String())
			}
		})
	}
}

func TestHealthReturnsDegradedForRequiredNodeModelCatalogBridgeFailure(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                   config.Config{ManagementAPIEnabled: true},
		Logger:                   slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementCaptchaHandler: http.NotFoundHandler(),
		ManagementAPIAuthMiddleware: func(next http.Handler) http.Handler {
			return next
		},
		NodeModelCatalogBridgeReadinessProber: readinessProberFunc(func(context.Context) error {
			return errors.New("private bridge failure")
		}),
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/__aisys__/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "degraded" || body.Dependencies["nodeModelCatalogBridge"].Error != "dependency check failed" {
		t.Fatalf("body = %+v", body)
	}
}

func TestReadinessCurrentCoalescesConcurrentChecks(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	checks := 0
	handler := &ReadinessHandler{
		checkDependencies: func(context.Context) (map[string]CheckResult, string) {
			checks++
			close(started)
			<-release
			return map[string]CheckResult{}, "ok"
		},
		now:      time.Now,
		cacheTTL: time.Second,
	}

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		handler.current(context.Background())
	}()
	<-started
	waiting := make(chan struct{})
	secondCtx := &observingContext{Context: context.Background(), doneCalled: waiting}
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		handler.current(secondCtx)
	}()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("second request did not enter the in-flight wait")
	}
	if checks != 1 {
		t.Fatalf("checks = %d, want one in-flight check", checks)
	}
	close(release)
	<-firstDone
	<-secondDone
}

func TestReadinessCurrentRecoversFromPanicAndRefreshesAfterTTL(t *testing.T) {
	now := time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC)
	checks := 0
	handler := &ReadinessHandler{
		checkDependencies: func(context.Context) (map[string]CheckResult, string) {
			checks++
			if checks == 1 {
				panic("private dependency panic")
			}
			return map[string]CheckResult{
				"postgres": {Configured: true, Status: "ok"},
			}, "ok"
		},
		now:      func() time.Time { return now },
		cacheTTL: 2 * time.Second,
	}

	if _, status := handler.current(context.Background()); status != "degraded" {
		t.Fatalf("panic status = %q, want degraded", status)
	}
	if _, status := handler.current(context.Background()); status != "degraded" {
		t.Fatalf("cached panic status = %q, want degraded", status)
	}
	if checks != 1 {
		t.Fatalf("checks = %d, want panic result cached", checks)
	}

	now = now.Add(3 * time.Second)
	if _, status := handler.current(context.Background()); status != "ok" {
		t.Fatalf("refresh status = %q, want ok", status)
	}
	if _, status := handler.current(context.Background()); status != "ok" {
		t.Fatalf("cached refresh status = %q, want ok", status)
	}
	if checks != 2 {
		t.Fatalf("checks = %d, want successful refresh cached", checks)
	}
}

func TestReadinessCurrentFirstCallerCancellationDoesNotCancelSharedCheck(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	checkCanceled := make(chan struct{})
	checks := 0
	handler := &ReadinessHandler{
		checkDependencies: func(ctx context.Context) (map[string]CheckResult, string) {
			checks++
			close(started)
			select {
			case <-release:
				return map[string]CheckResult{
					"postgres": {Configured: true, Status: "ok"},
				}, "ok"
			case <-ctx.Done():
				close(checkCanceled)
				return map[string]CheckResult{}, "degraded"
			}
		},
		now:      time.Now,
		cacheTTL: time.Second,
	}

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan string, 1)
	go func() {
		_, status := handler.current(firstCtx)
		firstDone <- status
	}()
	<-started
	cancelFirst()
	select {
	case status := <-firstDone:
		if status != "degraded" {
			t.Fatalf("canceled first status = %q, want degraded", status)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("canceled first request did not return promptly")
	}

	waiting := make(chan struct{})
	secondCtx := &observingContext{Context: context.Background(), doneCalled: waiting}
	secondDone := make(chan string, 1)
	go func() {
		_, status := handler.current(secondCtx)
		secondDone <- status
	}()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("second request did not wait for the shared check")
	}
	close(release)
	select {
	case status := <-secondDone:
		if status != "ok" {
			t.Fatalf("second status = %q, want ok", status)
		}
	case <-time.After(time.Second):
		t.Fatal("second request did not receive the shared result")
	}
	select {
	case <-checkCanceled:
		t.Fatal("first caller cancellation canceled the shared check")
	default:
	}
	if checks != 1 {
		t.Fatalf("checks = %d, want one shared check", checks)
	}
	if _, status := handler.current(context.Background()); status != "ok" {
		t.Fatalf("cached status = %q, want ok", status)
	}
}

func TestReadinessCurrentWaiterCancellationReturnsPromptly(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	handler := &ReadinessHandler{
		checkDependencies: func(context.Context) (map[string]CheckResult, string) {
			close(started)
			<-release
			return map[string]CheckResult{}, "ok"
		},
		now:      time.Now,
		cacheTTL: time.Second,
	}
	go handler.current(context.Background())
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan string, 1)
	go func() {
		_, status := handler.current(ctx)
		done <- status
	}()
	select {
	case status := <-done:
		if status != "degraded" {
			t.Fatalf("status = %q, want degraded", status)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("canceled waiter did not return promptly")
	}
	close(release)
}

func TestReadinessCurrentTTLStartsWhenCheckCompletes(t *testing.T) {
	startedAt := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	now := startedAt
	checks := 0
	handler := &ReadinessHandler{
		checkDependencies: func(context.Context) (map[string]CheckResult, string) {
			checks++
			now = startedAt.Add(5 * time.Second)
			return map[string]CheckResult{}, "ok"
		},
		now:      func() time.Time { return now },
		cacheTTL: 2 * time.Second,
	}
	handler.current(context.Background())
	now = startedAt.Add(6 * time.Second)
	handler.current(context.Background())
	if checks != 1 {
		t.Fatalf("checks = %d, want cached result until completion-based TTL expires", checks)
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
