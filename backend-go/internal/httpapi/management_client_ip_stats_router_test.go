package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

func TestRouterDoesNotRegisterManagementClientIPStatsWhenDisabled(t *testing.T) {
	handlerCalls := 0
	router := NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000},
		ManagementClientIPStatsHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			handlerCalls++
		}),
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/__aisys__/api/ip-stats", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 while management API disabled; body = %s", rec.Code, rec.Body.String())
	}
	if handlerCalls != 0 {
		t.Fatalf("handler calls = %d, want 0", handlerCalls)
	}
}

func TestRouterManagementClientIPStatsUsesReadAuthAndRateLimitsWithoutTouch(t *testing.T) {
	events := []string{}
	authenticator := &managementClientIPStatsRouterAuthenticator{
		events: &events,
		authContext: managementauth.Context{
			SystemAccountID: "sys_user",
			Username:        "user",
			Role:            "user",
			SessionID:       "sess_user",
		},
	}
	ipLimiter := &managementClientIPStatsRouterIPLimiter{
		events:   &events,
		decision: SystemAPIRateLimitDecision{Allowed: true},
	}
	userLimiter := &managementClientIPStatsRouterUserLimiter{
		events:   &events,
		decision: SystemAPIRateLimitDecision{Allowed: true},
	}
	handlerCalls := 0
	router := NewRouter(RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: true,
		},
		SystemAPIRateLimitReader: systemAPIRateLimitReaderStub{
			settings: port.SystemAPIRateLimitSettings{
				IPReadPerMinute:         600,
				IPReadBurstPer10Seconds: 120,
				UserReadPerMinute:       300,
			},
		},
		SystemAPIIPRateLimiter:            ipLimiter,
		SystemAPIAuthenticatedRateLimiter: userLimiter,
		ManagementAPIAuthMiddleware:       NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware:  NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementClientIPStatsHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			events = append(events, "handler")
			handlerCalls++
			authContext, ok := ManagementAuthContextFromRequest(r)
			if !ok || authContext.Role != "user" {
				t.Fatalf("handler auth context = %+v, ok = %v", authContext, ok)
			}
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
		}),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/ip-stats", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
	}
	if authenticator.readCalls != 1 || authenticator.touchCalls != 0 {
		t.Fatalf("read auth calls = %d, touch auth calls = %d", authenticator.readCalls, authenticator.touchCalls)
	}
	if authenticator.cookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("read auth cookie = %q", authenticator.cookieHeader)
	}
	if ipLimiter.calls != 1 ||
		ipLimiter.settings.PerMinute != 600 ||
		ipLimiter.settings.BurstPer10Seconds != 120 {
		t.Fatalf("IP limiter calls = %d, settings = %+v", ipLimiter.calls, ipLimiter.settings)
	}
	if userLimiter.calls != 1 || userLimiter.limit != 300 {
		t.Fatalf("authenticated limiter calls = %d, limit = %d", userLimiter.calls, userLimiter.limit)
	}
	if handlerCalls != 1 {
		t.Fatalf("handler calls = %d, want 1", handlerCalls)
	}
	if want := []string{"ip-limit", "read-auth", "user-limit", "handler"}; !slices.Equal(events, want) {
		t.Fatalf("pipeline events = %v, want %v", events, want)
	}
}

func TestRouterManagementClientIPStatsRequiresAuthenticatedReadLimiter(t *testing.T) {
	opts := RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: true,
		},
		SystemAPIRateLimitReader: systemAPIRateLimitReaderStub{
			settings: port.SystemAPIRateLimitSettings{UserReadPerMinute: 300},
		},
		SystemAPIIPRateLimiter:         NewInMemorySystemAPIIPRateLimiter(),
		ManagementAPIAuthMiddleware:    func(next http.Handler) http.Handler { return next },
		ManagementClientIPStatsHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	}
	if !managementBusinessRoutesConfigured(opts) {
		t.Fatal("client IP stats route was not classified as a management business route")
	}
	if managementWriteRoutesConfigured(opts) {
		t.Fatal("client IP stats route was incorrectly classified as a management write route")
	}

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("NewRouter() did not panic for client IP stats route without authenticated limiter")
		}
	}()

	_ = NewRouter(opts)
}

type managementClientIPStatsRouterAuthenticator struct {
	events       *[]string
	authContext  managementauth.Context
	cookieHeader string
	readCalls    int
	touchCalls   int
}

func (s *managementClientIPStatsRouterAuthenticator) AuthenticateCookie(
	_ context.Context,
	cookieHeader string,
) (managementauth.Context, error) {
	*s.events = append(*s.events, "read-auth")
	s.readCalls++
	s.cookieHeader = cookieHeader
	return s.authContext, nil
}

func (s *managementClientIPStatsRouterAuthenticator) AuthenticateCookieAndTouch(
	_ context.Context,
	_ string,
) (managementauth.Context, error) {
	s.touchCalls++
	return s.authContext, nil
}

type managementClientIPStatsRouterIPLimiter struct {
	events   *[]string
	decision SystemAPIRateLimitDecision
	settings SystemAPIIPRateLimitSettings
	calls    int
}

func (s *managementClientIPStatsRouterIPLimiter) AllowSystemAPIIP(
	_ context.Context,
	_ string,
	settings SystemAPIIPRateLimitSettings,
) (SystemAPIRateLimitDecision, error) {
	*s.events = append(*s.events, "ip-limit")
	s.calls++
	s.settings = settings
	return s.decision, nil
}

type managementClientIPStatsRouterUserLimiter struct {
	events   *[]string
	decision SystemAPIRateLimitDecision
	limit    int
	calls    int
}

func (s *managementClientIPStatsRouterUserLimiter) AllowSystemAPIAuthenticated(
	_ context.Context,
	_ string,
	limit int,
) (SystemAPIRateLimitDecision, error) {
	*s.events = append(*s.events, "user-limit")
	s.calls++
	s.limit = limit
	return s.decision, nil
}
