package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/publicsettings"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementGlobalSettingsHandlerAllowsAdministratorsAndReturnsExactContract(t *testing.T) {
	for _, role := range []string{"admin", "super_admin"} {
		t.Run(role, func(t *testing.T) {
			reader := &managementGlobalSettingsReaderStub{
				settings: port.PublicGlobalSettings{
					AppName: "聚合 AI",
					AppIcon: "/__aisys__/brand-icon.svg",
				},
			}
			service := publicsettings.NewService(reader)
			handler := NewManagementGlobalSettingsHandler(&service)
			req := managementGlobalSettingsRequest(role)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
			}
			if !reader.called {
				t.Fatal("public settings service was not called")
			}
			const want = "{\"data\":{\"appName\":\"聚合 AI\",\"appIcon\":\"/__aisys__/brand-icon.svg\"}}\n"
			if got := rec.Body.String(); got != want {
				t.Fatalf("body = %q, want %q", got, want)
			}
		})
	}
}

func TestManagementGlobalSettingsHandlerRejectsOrdinaryUser(t *testing.T) {
	reader := &managementGlobalSettingsReaderStub{}
	service := publicsettings.NewService(reader)
	handler := NewManagementGlobalSettingsHandler(&service)
	req := managementGlobalSettingsRequest("user")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	if reader.called {
		t.Fatal("public settings service should not be called for ordinary user")
	}
	const want = "{\"message\":\"需要管理员权限\"}\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestManagementGlobalSettingsHandlerRedactsServiceErrors(t *testing.T) {
	reader := &managementGlobalSettingsReaderStub{
		err: errors.New("postgres password leaked"),
	}
	service := publicsettings.NewService(reader)
	handler := NewManagementGlobalSettingsHandler(&service)
	req := managementGlobalSettingsRequest("admin")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", rec.Code, rec.Body.String())
	}
	if !reader.called {
		t.Fatal("public settings service was not called")
	}
	const want = "{\"message\":\"服务器内部错误\"}\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if strings.Contains(rec.Body.String(), "postgres password leaked") {
		t.Fatalf("body leaked service error: %s", rec.Body.String())
	}
}

func TestRouterRegistersManagementGlobalSettingsAsLimitedReadRoute(t *testing.T) {
	reader := &managementGlobalSettingsReaderStub{
		settings: port.PublicGlobalSettings{
			AppName: "聚合 AI",
			AppIcon: "/__aisys__/brand-icon.svg",
		},
	}
	service := publicsettings.NewService(reader)
	readAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
			Role:            "admin",
			SessionID:       "sess_read",
		},
	}
	touchAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
			Role:            "admin",
			SessionID:       "sess_touch",
		},
	}
	ipLimiter := &publicSettingsRateLimiterStub{
		decision: SystemAPIRateLimitDecision{Allowed: true},
	}
	userLimiter := &systemAPIAuthenticatedRateLimiterStub{
		decision: SystemAPIRateLimitDecision{Allowed: true},
	}
	router := NewRouter(RouterOptions{
		Config:                            config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		SystemAPIRateLimitReader:          systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{IPReadPerMinute: 600, IPReadBurstPer10Seconds: 120, UserReadPerMinute: 300}},
		SystemAPIIPRateLimiter:            ipLimiter,
		SystemAPIAuthenticatedRateLimiter: userLimiter,
		ManagementAPIAuthMiddleware:       NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware:  NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
		ManagementGlobalSettingsHandler:   NewManagementGlobalSettingsHandler(&service),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/settings/global", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if readAuthenticator.cookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("read auth cookie = %q", readAuthenticator.cookieHeader)
	}
	if touchAuthenticator.touchCookieHeader != "" {
		t.Fatalf("touch auth cookie = %q, want empty for read route", touchAuthenticator.touchCookieHeader)
	}
	if ipLimiter.calls != 1 || ipLimiter.settings.PerMinute != 600 || ipLimiter.settings.BurstPer10Seconds != 120 {
		t.Fatalf("IP limiter calls=%d settings=%+v, want read limits", ipLimiter.calls, ipLimiter.settings)
	}
	if userLimiter.calls != 1 || userLimiter.limit != 300 {
		t.Fatalf("user limiter calls=%d limit=%d, want one read limit call", userLimiter.calls, userLimiter.limit)
	}
	if !reader.called {
		t.Fatal("global settings reader was not called")
	}
}

func TestRouterManagementGlobalSettingsRequiresAuthentication(t *testing.T) {
	reader := &managementGlobalSettingsReaderStub{}
	service := publicsettings.NewService(reader)
	userLimiter := &systemAPIAuthenticatedRateLimiterStub{
		decision: SystemAPIRateLimitDecision{Allowed: true},
	}
	router := NewRouter(RouterOptions{
		Config:                            config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		SystemAPIRateLimitReader:          systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{}},
		SystemAPIIPRateLimiter:            &publicSettingsRateLimiterStub{decision: SystemAPIRateLimitDecision{Allowed: true}},
		SystemAPIAuthenticatedRateLimiter: userLimiter,
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			err: &managementauth.AuthError{
				StatusCode: http.StatusUnauthorized,
				Message:    "请先登录",
			},
		}),
		ManagementGlobalSettingsHandler: NewManagementGlobalSettingsHandler(&service),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/settings/global", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body = %s", rec.Code, rec.Body.String())
	}
	if userLimiter.calls != 0 {
		t.Fatalf("user limiter calls = %d, want 0 before authentication", userLimiter.calls)
	}
	if reader.called {
		t.Fatal("global settings reader should not run before authentication")
	}
}

func TestRouterDoesNotRegisterManagementGlobalSettingsWhenDisabled(t *testing.T) {
	reader := &managementGlobalSettingsReaderStub{}
	service := publicsettings.NewService(reader)
	router := NewRouter(RouterOptions{
		Config:                          config.Config{Host: "127.0.0.1", Port: 3000},
		ManagementGlobalSettingsHandler: NewManagementGlobalSettingsHandler(&service),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/settings/global", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 while management API disabled", rec.Code)
	}
	if reader.called {
		t.Fatal("global settings reader should not run while management API disabled")
	}
}

func managementGlobalSettingsRequest(role string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/settings/global", nil)
	authContext := managementauth.Context{
		SystemAccountID: "sys_" + role,
		Username:        role,
		Role:            role,
		SessionID:       "sess_" + role,
	}
	return req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, authContext))
}

type managementGlobalSettingsReaderStub struct {
	called   bool
	settings port.PublicGlobalSettings
	err      error
}

func (s *managementGlobalSettingsReaderStub) PublicGlobalSettings(context.Context) (port.PublicGlobalSettings, error) {
	s.called = true
	return s.settings, s.err
}

var _ port.PublicSettingsReader = (*managementGlobalSettingsReaderStub)(nil)
