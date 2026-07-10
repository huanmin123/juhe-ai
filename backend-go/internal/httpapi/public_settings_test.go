package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/publicsettings"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
)

func TestPublicSettingsRoute(t *testing.T) {
	service := publicsettings.NewService(publicSettingsReaderStub{
		settings: port.PublicGlobalSettings{
			AppName: "聚合 AI",
			AppIcon: "/__aisys__/brand-icon.svg",
		},
	})
	router := NewRouter(RouterOptions{
		Config:                config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		PublicSettingsService: &service,
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/settings/public", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}

	var body struct {
		Data publicsettings.Response `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.AppName != "聚合 AI" || body.Data.AppIcon != "/__aisys__/brand-icon.svg" {
		t.Fatalf("body = %+v", body)
	}
}

func TestPublicSettingsRouteRedactsStoreErrors(t *testing.T) {
	service := publicsettings.NewService(publicSettingsReaderStub{err: errors.New("postgres password leaked")})
	router := NewRouter(RouterOptions{
		Config:                config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		PublicSettingsService: &service,
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/settings/public", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := body["message"]; got != "服务器内部错误" {
		t.Fatalf("message = %q", got)
	}
}

func TestPublicSettingsRouteAppliesIPReadRateLimit(t *testing.T) {
	service := publicsettings.NewService(publicSettingsReaderStub{
		settings: port.PublicGlobalSettings{
			AppName: "聚合 AI",
			AppIcon: "/__aisys__/brand-icon.svg",
		},
	})
	router := NewRouter(RouterOptions{
		Config:                config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		PublicSettingsService: &service,
		SystemAPIRateLimitReader: systemAPIRateLimitReaderStub{
			settings: port.SystemAPIRateLimitSettings{
				IPReadPerMinute:         2,
				IPReadBurstPer10Seconds: 2,
			},
		},
		SystemAPIIPRateLimiter: NewInMemorySystemAPIIPRateLimiter(),
	})

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/settings/public", nil)
		req.RemoteAddr = "203.0.113.10:12345"
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200", i+1, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/settings/public", nil)
	req.RemoteAddr = "203.0.113.10:12345"
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Fatal("Retry-After header is empty")
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := body["message"]; got != "请求过于频繁，请稍后重试" {
		t.Fatalf("message = %q", got)
	}
}

func TestPublicSettingsRouteUsesInjectedIPReadRateLimiter(t *testing.T) {
	service := publicsettings.NewService(publicSettingsReaderStub{
		settings: port.PublicGlobalSettings{
			AppName: "聚合 AI",
			AppIcon: "/__aisys__/brand-icon.svg",
		},
	})
	limiter := &publicSettingsRateLimiterStub{
		decision: SystemAPIRateLimitDecision{
			Allowed:           false,
			RetryAfterSeconds: 7,
		},
	}
	router := NewRouter(RouterOptions{
		Config:                   config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                   slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		PublicSettingsService:    &service,
		SystemAPIRateLimitReader: systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{IPReadPerMinute: 600, IPReadBurstPer10Seconds: 120}},
		SystemAPIIPRateLimiter:   limiter,
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/settings/public", nil)
	req.RemoteAddr = "203.0.113.10:12345"
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "7" {
		t.Fatalf("Retry-After = %q, want 7", got)
	}
	if limiter.calls != 1 {
		t.Fatalf("limiter calls = %d, want 1", limiter.calls)
	}
	if limiter.key == "203.0.113.10:read" || limiter.key == "203.0.113.10" {
		t.Fatalf("limiter key should not expose raw ip: %q", limiter.key)
	}
}

func TestPublicSettingsRouteRateLimitUsesTrustProxyClientIP(t *testing.T) {
	service := publicsettings.NewService(publicSettingsReaderStub{
		settings: port.PublicGlobalSettings{
			AppName: "聚合 AI",
			AppIcon: "/__aisys__/brand-icon.svg",
		},
	})
	limiter := &publicSettingsRateLimiterStub{
		decision: SystemAPIRateLimitDecision{Allowed: true},
	}
	router := NewRouter(RouterOptions{
		Config:                   config.Config{Host: "127.0.0.1", Port: 3000, TrustProxy: "true"},
		Logger:                   slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		PublicSettingsService:    &service,
		SystemAPIRateLimitReader: systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{IPReadPerMinute: 600, IPReadBurstPer10Seconds: 120}},
		SystemAPIIPRateLimiter:   limiter,
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/settings/public", nil)
	req.RemoteAddr = "10.0.0.10:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.10, 198.51.100.20")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if limiter.key != systemAPIIPRateLimitKey("203.0.113.10", systemAPIMethodRead) {
		t.Fatalf("limiter key = %q, want forwarded client IP key", limiter.key)
	}
	if limiter.key == systemAPIIPRateLimitKey("10.0.0.10", systemAPIMethodRead) {
		t.Fatal("limiter used RemoteAddr instead of trusted forwarded client IP")
	}
}

func TestPublicSettingsRouteTrustProxySharesRateLimitBucket(t *testing.T) {
	service := publicsettings.NewService(publicSettingsReaderStub{
		settings: port.PublicGlobalSettings{
			AppName: "聚合 AI",
			AppIcon: "/__aisys__/brand-icon.svg",
		},
	})
	router := NewRouter(RouterOptions{
		Config:                config.Config{Host: "127.0.0.1", Port: 3000, TrustProxy: "true"},
		Logger:                slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		PublicSettingsService: &service,
		SystemAPIRateLimitReader: systemAPIRateLimitReaderStub{
			settings: port.SystemAPIRateLimitSettings{
				IPReadPerMinute:         1,
				IPReadBurstPer10Seconds: 1,
			},
		},
		SystemAPIIPRateLimiter: NewInMemorySystemAPIIPRateLimiter(),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/settings/public", nil)
	req.RemoteAddr = "10.0.0.10:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.10")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/__aisys__/api/settings/public", nil)
	req.RemoteAddr = "10.0.0.11:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.10")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want 429", rec.Code)
	}
}

func TestRouterSkipsSystemAPIRateLimitForHealth(t *testing.T) {
	limiter := &publicSettingsRateLimiterStub{
		decision: SystemAPIRateLimitDecision{
			Allowed:           false,
			RetryAfterSeconds: 7,
		},
	}
	router := NewRouter(RouterOptions{
		Config:                   config.Config{Host: "127.0.0.1", Port: 3000},
		SystemAPIRateLimitReader: systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{IPReadPerMinute: 1, IPReadBurstPer10Seconds: 1}},
		SystemAPIIPRateLimiter:   limiter,
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/health", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want 200", rec.Code, rec.Body.String())
	}
	if limiter.calls != 0 {
		t.Fatalf("system API limiter calls = %d, want 0 for health", limiter.calls)
	}
}

func TestRouterRequiresIPRateLimiterWhenReaderConfigured(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewRouter() did not panic without SystemAPIIPRateLimiter")
		}
	}()

	service := publicsettings.NewService(publicSettingsReaderStub{})
	_ = NewRouter(RouterOptions{
		Config:                   config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                   slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		PublicSettingsService:    &service,
		SystemAPIRateLimitReader: systemAPIRateLimitReaderStub{},
	})
}

func TestRedisSystemAPIIPRateLimiterBuildsFixedWindows(t *testing.T) {
	client := &redisFixedWindowClientStub{
		decision: redisplatform.FixedWindowDecision{Allowed: true},
	}
	limiter := NewRedisSystemAPIIPRateLimiter(client)

	decision, err := limiter.AllowSystemAPIIP(context.Background(), "client-key", SystemAPIIPRateLimitSettings{
		PerMinute:         600,
		BurstPer10Seconds: 120,
	})

	if err != nil {
		t.Fatalf("AllowSystemAPIIP() error = %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("decision = %+v, want allowed", decision)
	}
	if got, want := len(client.limits), 2; got != want {
		t.Fatalf("limits length = %d, want %d", got, want)
	}
	if client.limits[0].Key != "system-api:ip:client-key:minute" ||
		client.limits[0].Limit != 600 ||
		client.limits[0].Window != rateLimitMinuteWindow {
		t.Fatalf("minute limit = %+v", client.limits[0])
	}
	if client.limits[1].Key != "system-api:ip:client-key:burst" ||
		client.limits[1].Limit != 120 ||
		client.limits[1].Window != rateLimitBurstWindow {
		t.Fatalf("burst limit = %+v", client.limits[1])
	}
}

func TestRedisSystemAPIAuthenticatedRateLimiterBuildsMinuteWindow(t *testing.T) {
	client := &redisFixedWindowClientStub{
		decision: redisplatform.FixedWindowDecision{Allowed: true},
	}
	limiter := NewRedisSystemAPIAuthenticatedRateLimiter(client)

	decision, err := limiter.AllowSystemAPIAuthenticated(context.Background(), "account-key", 300)

	if err != nil {
		t.Fatalf("AllowSystemAPIAuthenticated() error = %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("decision = %+v, want allowed", decision)
	}
	if got, want := len(client.limits), 1; got != want {
		t.Fatalf("limits length = %d, want %d", got, want)
	}
	if client.limits[0].Key != "system-api:user:account-key:minute" ||
		client.limits[0].Limit != 300 ||
		client.limits[0].Window != rateLimitMinuteWindow {
		t.Fatalf("minute limit = %+v", client.limits[0])
	}
}

func TestSystemAPIMethodClassForMatchesNodeContract(t *testing.T) {
	tests := []struct {
		method string
		want   systemAPIMethodClass
	}{
		{method: http.MethodGet, want: systemAPIMethodRead},
		{method: http.MethodHead, want: systemAPIMethodRead},
		{method: http.MethodOptions, want: systemAPIMethodRead},
		{method: http.MethodPost, want: systemAPIMethodWrite},
		{method: http.MethodPut, want: systemAPIMethodWrite},
		{method: http.MethodPatch, want: systemAPIMethodWrite},
		{method: http.MethodDelete, want: systemAPIMethodWrite},
	}

	for _, tc := range tests {
		t.Run(tc.method, func(t *testing.T) {
			if got := systemAPIMethodClassFor(tc.method); got != tc.want {
				t.Fatalf("systemAPIMethodClassFor(%q) = %q, want %q", tc.method, got, tc.want)
			}
		})
	}
}

func TestSystemAPIRateLimitSettingsCacheRefreshesAfterTTL(t *testing.T) {
	reader := &countingSystemAPIRateLimitReader{
		settings: port.SystemAPIRateLimitSettings{IPReadPerMinute: 600},
	}
	cache := systemAPIRateLimitSettingsCache{}
	now := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)

	first, err := cache.current(context.Background(), reader, now)
	if err != nil {
		t.Fatalf("first current() error = %v", err)
	}
	reader.settings = port.SystemAPIRateLimitSettings{IPReadPerMinute: 900}

	cached, err := cache.current(context.Background(), reader, now.Add(systemAPISettingsCacheTTL-time.Nanosecond))
	if err != nil {
		t.Fatalf("cached current() error = %v", err)
	}
	if cached.IPReadPerMinute != first.IPReadPerMinute || reader.calls != 1 {
		t.Fatalf("cached settings = %+v calls=%d, want original settings and one read", cached, reader.calls)
	}

	refreshed, err := cache.current(context.Background(), reader, now.Add(systemAPISettingsCacheTTL))
	if err != nil {
		t.Fatalf("refreshed current() error = %v", err)
	}
	if refreshed.IPReadPerMinute != 900 || reader.calls != 2 {
		t.Fatalf("refreshed settings = %+v calls=%d, want updated settings and two reads", refreshed, reader.calls)
	}
}

type publicSettingsReaderStub struct {
	settings port.PublicGlobalSettings
	err      error
}

func (s publicSettingsReaderStub) PublicGlobalSettings(context.Context) (port.PublicGlobalSettings, error) {
	return s.settings, s.err
}

type systemAPIRateLimitReaderStub struct {
	settings port.SystemAPIRateLimitSettings
	err      error
}

func (s systemAPIRateLimitReaderStub) SystemAPIRateLimitSettings(context.Context) (port.SystemAPIRateLimitSettings, error) {
	return s.settings, s.err
}

type countingSystemAPIRateLimitReader struct {
	settings port.SystemAPIRateLimitSettings
	calls    int
}

func (s *countingSystemAPIRateLimitReader) SystemAPIRateLimitSettings(context.Context) (port.SystemAPIRateLimitSettings, error) {
	s.calls++
	return s.settings, nil
}

type publicSettingsRateLimiterStub struct {
	decision SystemAPIRateLimitDecision
	err      error
	key      string
	settings SystemAPIIPRateLimitSettings
	calls    int
}

func (s *publicSettingsRateLimiterStub) AllowSystemAPIIP(_ context.Context, key string, settings SystemAPIIPRateLimitSettings) (SystemAPIRateLimitDecision, error) {
	s.calls++
	s.key = key
	s.settings = settings
	return s.decision, s.err
}

type redisFixedWindowClientStub struct {
	limits   []redisplatform.FixedWindowLimit
	decision redisplatform.FixedWindowDecision
	err      error
}

func (s *redisFixedWindowClientStub) AllowFixedWindow(_ context.Context, limits []redisplatform.FixedWindowLimit) (redisplatform.FixedWindowDecision, error) {
	s.limits = append([]redisplatform.FixedWindowLimit(nil), limits...)
	return s.decision, s.err
}
