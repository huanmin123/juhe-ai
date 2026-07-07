package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

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
		SystemAPIIPRateLimitReader: publicSettingsRateLimitReaderStub{
			settings: port.SystemAPIIPReadRateLimitSettings{
				PerMinute:         2,
				BurstPer10Seconds: 2,
			},
		},
		SystemAPIIPReadRateLimiter: NewInMemorySystemAPIIPReadRateLimiter(),
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
		decision: SystemAPIIPReadRateLimitDecision{
			Allowed:           false,
			RetryAfterSeconds: 7,
		},
	}
	router := NewRouter(RouterOptions{
		Config:                     config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                     slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		PublicSettingsService:      &service,
		SystemAPIIPRateLimitReader: publicSettingsRateLimitReaderStub{settings: port.SystemAPIIPReadRateLimitSettings{PerMinute: 600, BurstPer10Seconds: 120}},
		SystemAPIIPReadRateLimiter: limiter,
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
		decision: SystemAPIIPReadRateLimitDecision{Allowed: true},
	}
	router := NewRouter(RouterOptions{
		Config:                     config.Config{Host: "127.0.0.1", Port: 3000, TrustProxy: "true"},
		Logger:                     slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		PublicSettingsService:      &service,
		SystemAPIIPRateLimitReader: publicSettingsRateLimitReaderStub{settings: port.SystemAPIIPReadRateLimitSettings{PerMinute: 600, BurstPer10Seconds: 120}},
		SystemAPIIPReadRateLimiter: limiter,
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/settings/public", nil)
	req.RemoteAddr = "10.0.0.10:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.10, 198.51.100.20")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if limiter.key != systemAPIIPReadRateLimitKey("203.0.113.10") {
		t.Fatalf("limiter key = %q, want forwarded client IP key", limiter.key)
	}
	if limiter.key == systemAPIIPReadRateLimitKey("10.0.0.10") {
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
		SystemAPIIPRateLimitReader: publicSettingsRateLimitReaderStub{
			settings: port.SystemAPIIPReadRateLimitSettings{
				PerMinute:         1,
				BurstPer10Seconds: 1,
			},
		},
		SystemAPIIPReadRateLimiter: NewInMemorySystemAPIIPReadRateLimiter(),
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

func TestRouterRequiresIPReadRateLimiterWhenReaderConfigured(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewRouter() did not panic without SystemAPIIPReadRateLimiter")
		}
	}()

	service := publicsettings.NewService(publicSettingsReaderStub{})
	_ = NewRouter(RouterOptions{
		Config:                     config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                     slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		PublicSettingsService:      &service,
		SystemAPIIPRateLimitReader: publicSettingsRateLimitReaderStub{},
	})
}

func TestRedisSystemAPIIPReadRateLimiterBuildsFixedWindows(t *testing.T) {
	client := &redisFixedWindowClientStub{
		decision: redisplatform.FixedWindowDecision{Allowed: true},
	}
	limiter := NewRedisSystemAPIIPReadRateLimiter(client)

	decision, err := limiter.AllowSystemAPIIPRead(context.Background(), "client-key", port.SystemAPIIPReadRateLimitSettings{
		PerMinute:         600,
		BurstPer10Seconds: 120,
	})

	if err != nil {
		t.Fatalf("AllowSystemAPIIPRead() error = %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("decision = %+v, want allowed", decision)
	}
	if got, want := len(client.limits), 2; got != want {
		t.Fatalf("limits length = %d, want %d", got, want)
	}
	if client.limits[0].Key != "system-api:ip-read:client-key:minute" ||
		client.limits[0].Limit != 600 ||
		client.limits[0].Window != rateLimitMinuteWindow {
		t.Fatalf("minute limit = %+v", client.limits[0])
	}
	if client.limits[1].Key != "system-api:ip-read:client-key:burst" ||
		client.limits[1].Limit != 120 ||
		client.limits[1].Window != rateLimitBurstWindow {
		t.Fatalf("burst limit = %+v", client.limits[1])
	}
}

type publicSettingsReaderStub struct {
	settings port.PublicGlobalSettings
	err      error
}

func (s publicSettingsReaderStub) PublicGlobalSettings(context.Context) (port.PublicGlobalSettings, error) {
	return s.settings, s.err
}

type publicSettingsRateLimitReaderStub struct {
	settings port.SystemAPIIPReadRateLimitSettings
	err      error
}

func (s publicSettingsRateLimitReaderStub) SystemAPIIPReadRateLimitSettings(context.Context) (port.SystemAPIIPReadRateLimitSettings, error) {
	return s.settings, s.err
}

type publicSettingsRateLimiterStub struct {
	decision SystemAPIIPReadRateLimitDecision
	err      error
	key      string
	calls    int
}

func (s *publicSettingsRateLimiterStub) AllowSystemAPIIPRead(_ context.Context, key string, _ port.SystemAPIIPReadRateLimitSettings) (SystemAPIIPReadRateLimitDecision, error) {
	s.calls++
	s.key = key
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
