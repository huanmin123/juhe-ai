package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

func TestSystemAPIPostReadRoutesUseReadLimitsWithoutTouchingSession(t *testing.T) {
	settings := port.SystemAPIRateLimitSettings{
		IPReadPerMinute:          601,
		IPReadBurstPer10Seconds:  121,
		IPWritePerMinute:         181,
		IPWriteBurstPer10Seconds: 41,
		UserReadPerMinute:        301,
		UserWritePerMinute:       121,
	}
	tests := []struct {
		name      string
		path      string
		wantClass systemAPIMethodClass
		wantRead  int
		wantTouch int
	}{
		{name: "admin import preview", path: "/__aisys__/api/accounts/import/preview", wantClass: systemAPIMethodRead, wantRead: 1},
		{name: "self import preview", path: "/__aisys__/api/my-accounts/import/preview", wantClass: systemAPIMethodRead, wantRead: 1},
		{name: "import confirm remains write", path: "/__aisys__/api/accounts/import/confirm", wantClass: systemAPIMethodWrite, wantTouch: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authenticator := &systemAPIRouteSemanticAuthenticator{}
			ipLimiter := &systemAPIRouteSemanticIPLimiter{}
			userLimiter := &systemAPIRouteSemanticUserLimiter{}
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
			router := NewRouter(RouterOptions{
				Config: config.Config{
					Host:                 "127.0.0.1",
					Port:                 3000,
					ManagementAPIEnabled: true,
				},
				Logger:                                  slog.New(slog.NewTextHandler(io.Discard, nil)),
				SystemAPIRateLimitReader:                systemAPIRateLimitReaderStub{settings: settings},
				SystemAPIIPRateLimiter:                  ipLimiter,
				SystemAPIAuthenticatedRateLimiter:       userLimiter,
				ManagementAPIAuthMiddleware:             NewManagementAPIAuthMiddleware(authenticator),
				ManagementAPIAuthTouchMiddleware:        NewManagementAPIAuthTouchMiddleware(authenticator),
				ManagementAccountImportPreviewHandler:   handler,
				ManagementMyAccountImportPreviewHandler: handler,
				ManagementAccountImportConfirmHandler:   handler,
			})

			req := httptest.NewRequest(http.MethodPost, tt.path, nil)
			req.Header.Set("Cookie", "juhe_ai_session=test")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusNoContent, rec.Body.String())
			}
			if authenticator.readCalls != tt.wantRead || authenticator.touchCalls != tt.wantTouch {
				t.Fatalf("auth calls = read:%d touch:%d, want read:%d touch:%d", authenticator.readCalls, authenticator.touchCalls, tt.wantRead, tt.wantTouch)
			}
			if len(ipLimiter.settings) != 1 {
				t.Fatalf("IP limiter calls = %d, want 1", len(ipLimiter.settings))
			}
			if len(userLimiter.limits) != 1 {
				t.Fatalf("authenticated limiter calls = %d, want 1", len(userLimiter.limits))
			}
			if got, want := ipLimiter.keys[0], systemAPIIPRateLimitKey("192.0.2.1", tt.wantClass); got != want {
				t.Fatalf("IP limiter key = %q, want %s bucket key %q", got, tt.wantClass, want)
			}
			if got, want := userLimiter.keys[0], systemAPIAuthenticatedRateLimitKey("sys_admin", tt.wantClass); got != want {
				t.Fatalf("authenticated limiter key = %q, want %s bucket key %q", got, tt.wantClass, want)
			}

			if tt.wantClass == systemAPIMethodRead {
				if got := ipLimiter.settings[0]; got != (SystemAPIIPRateLimitSettings{PerMinute: 601, BurstPer10Seconds: 121}) {
					t.Fatalf("IP limiter settings = %+v, want read settings", got)
				}
				if got := userLimiter.limits[0]; got != 301 {
					t.Fatalf("authenticated limiter = %d, want read limit 301", got)
				}
				return
			}
			if got := ipLimiter.settings[0]; got != (SystemAPIIPRateLimitSettings{PerMinute: 181, BurstPer10Seconds: 41}) {
				t.Fatalf("IP limiter settings = %+v, want write settings", got)
			}
			if got := userLimiter.limits[0]; got != 121 {
				t.Fatalf("authenticated limiter = %d, want write limit 121", got)
			}
		})
	}
}

func TestRetiredPageDataConfirmRouteReturnsNotFound(t *testing.T) {
	router := NewRouter(RouterOptions{Config: config.Config{ManagementAPIEnabled: true}})
	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/data-changes/confirm", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

type systemAPIRouteSemanticAuthenticator struct {
	readCalls  int
	touchCalls int
}

func (a *systemAPIRouteSemanticAuthenticator) AuthenticateCookie(context.Context, string) (managementauth.Context, error) {
	a.readCalls++
	return managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin"}, nil
}

func (a *systemAPIRouteSemanticAuthenticator) AuthenticateCookieAndTouch(context.Context, string) (managementauth.Context, error) {
	a.touchCalls++
	return managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin"}, nil
}

type systemAPIRouteSemanticIPLimiter struct {
	keys     []string
	settings []SystemAPIIPRateLimitSettings
}

func (l *systemAPIRouteSemanticIPLimiter) AllowSystemAPIIP(
	_ context.Context,
	key string,
	settings SystemAPIIPRateLimitSettings,
) (SystemAPIRateLimitDecision, error) {
	l.keys = append(l.keys, key)
	l.settings = append(l.settings, settings)
	return SystemAPIRateLimitDecision{Allowed: true}, nil
}

type systemAPIRouteSemanticUserLimiter struct {
	keys   []string
	limits []int
}

func (l *systemAPIRouteSemanticUserLimiter) AllowSystemAPIAuthenticated(
	_ context.Context,
	key string,
	perMinute int,
) (SystemAPIRateLimitDecision, error) {
	l.keys = append(l.keys, key)
	l.limits = append(l.limits, perMinute)
	return SystemAPIRateLimitDecision{Allowed: true}, nil
}
