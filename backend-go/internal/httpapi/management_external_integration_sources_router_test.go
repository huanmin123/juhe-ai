package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/publicapi"
	"juhe-ai/backend-go/internal/store/port"
)

const managementExternalIntegrationSourceScopesPath = "/__aisys__/api/external-integration-sources/scopes"

func TestRouterExternalIntegrationSourceScopesRequiresFullManagementOptIn(t *testing.T) {
	handlerCalls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalls++
		w.WriteHeader(http.StatusNoContent)
	})
	readAuth := func(next http.Handler) http.Handler { return next }

	tests := []struct {
		name       string
		opts       RouterOptions
		wantStatus int
	}{
		{
			name: "management disabled",
			opts: RouterOptions{
				Config: config.Config{Host: "127.0.0.1", Port: 3000},
				ManagementExternalIntegrationSourceScopesHandler: handler,
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name: "session only",
			opts: RouterOptions{
				Config: config.Config{
					Host:                          "127.0.0.1",
					Port:                          3000,
					ManagementAuthSessionsEnabled: true,
				},
				ManagementAPIAuthMiddleware:                      readAuth,
				ManagementSessionListHandler:                     http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
				ManagementExternalIntegrationSourceScopesHandler: handler,
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name: "handler not opted in",
			opts: RouterOptions{
				Config:                       config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
				ManagementAPIAuthMiddleware:  readAuth,
				ManagementCurrentUserHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name: "management and handler enabled",
			opts: RouterOptions{
				Config:                      config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
				ManagementAPIAuthMiddleware: readAuth,
				ManagementExternalIntegrationSourceScopesHandler: handler,
			},
			wantStatus: http.StatusNoContent,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			before := handlerCalls
			rec := httptest.NewRecorder()
			NewRouter(testCase.opts).ServeHTTP(
				rec,
				httptest.NewRequest(http.MethodGet, managementExternalIntegrationSourceScopesPath, nil),
			)

			if rec.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, testCase.wantStatus, rec.Body.String())
			}
			wantCalls := before
			if testCase.wantStatus == http.StatusNoContent {
				wantCalls++
			}
			if handlerCalls != wantCalls {
				t.Fatalf("handler calls = %d, want %d", handlerCalls, wantCalls)
			}
		})
	}
}

func TestRouterExternalIntegrationSourceScopesRegistersGETOnly(t *testing.T) {
	handlerCalls := 0
	router := NewRouter(RouterOptions{
		Config:                      config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware: func(next http.Handler) http.Handler { return next },
		ManagementExternalIntegrationSourceScopesHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			handlerCalls++
			w.WriteHeader(http.StatusNoContent)
		}),
	})

	tests := []struct {
		method     string
		path       string
		wantStatus int
	}{
		{method: http.MethodGet, path: managementExternalIntegrationSourceScopesPath, wantStatus: http.StatusNoContent},
		{method: http.MethodPost, path: managementExternalIntegrationSourceScopesPath, wantStatus: http.StatusMethodNotAllowed},
		{method: http.MethodGet, path: "/__aisys__/api/external-integration-sources/api-docs", wantStatus: http.StatusNotFound},
		{method: http.MethodGet, path: "/__aisys__/api/external-integration-sources", wantStatus: http.StatusNotFound},
		{method: http.MethodGet, path: "/__aisys__/api/external-integration-sources/extsrc_1", wantStatus: http.StatusNotFound},
	}

	for _, testCase := range tests {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(testCase.method, testCase.path, nil))
		if rec.Code != testCase.wantStatus {
			t.Fatalf("%s %s status = %d, want %d; body = %s", testCase.method, testCase.path, rec.Code, testCase.wantStatus, rec.Body.String())
		}
	}
	if handlerCalls != 1 {
		t.Fatalf("handler calls = %d, want 1", handlerCalls)
	}
}

func TestRouterExternalIntegrationSourceScopesUsesReadAuthAndRateLimitsWithoutTouch(t *testing.T) {
	events := []string{}
	authenticator := &managementExternalIntegrationSourceScopesAuthenticator{
		events: &events,
		authContext: managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
			Role:            "admin",
			SessionID:       "sess_admin",
		},
	}
	ipLimiter := &managementExternalIntegrationSourceScopesIPLimiter{events: &events}
	userLimiter := &managementExternalIntegrationSourceScopesUserLimiter{events: &events}
	scopesHandler := NewManagementExternalIntegrationSourceScopesHandler()
	router := NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		SystemAPIRateLimitReader: managementExternalIntegrationSourceScopesRateLimitReader{
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
		ManagementExternalIntegrationSourceScopesHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			events = append(events, "handler")
			scopesHandler.ServeHTTP(w, r)
		}),
	})

	req := httptest.NewRequest(http.MethodGet, managementExternalIntegrationSourceScopesPath, nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var body struct {
		Data []publicapi.ScopeOption `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Data) != 16 {
		t.Fatalf("scope options = %d, want 16", len(body.Data))
	}
	if authenticator.readCalls != 1 || authenticator.touchCalls != 0 {
		t.Fatalf("read auth calls = %d, touch auth calls = %d", authenticator.readCalls, authenticator.touchCalls)
	}
	if ipLimiter.calls != 1 || ipLimiter.settings.PerMinute != 600 || ipLimiter.settings.BurstPer10Seconds != 120 {
		t.Fatalf("IP limiter calls = %d, settings = %+v", ipLimiter.calls, ipLimiter.settings)
	}
	if userLimiter.calls != 1 || userLimiter.limit != 300 {
		t.Fatalf("authenticated limiter calls = %d, limit = %d", userLimiter.calls, userLimiter.limit)
	}
	if want := []string{"ip-limit", "read-auth", "user-limit", "handler"}; !slices.Equal(events, want) {
		t.Fatalf("pipeline events = %v, want %v", events, want)
	}
	if !managementBusinessRoutesConfigured(RouterOptions{
		ManagementExternalIntegrationSourceScopesHandler: scopesHandler,
	}) {
		t.Fatal("external integration source scopes route was not classified as a management business route")
	}
	if managementWriteRoutesConfigured(RouterOptions{
		ManagementExternalIntegrationSourceScopesHandler: scopesHandler,
	}) {
		t.Fatal("external integration source scopes route was incorrectly classified as a management write route")
	}
}

type managementExternalIntegrationSourceScopesRateLimitReader struct {
	settings port.SystemAPIRateLimitSettings
}

func (s managementExternalIntegrationSourceScopesRateLimitReader) SystemAPIRateLimitSettings(context.Context) (port.SystemAPIRateLimitSettings, error) {
	return s.settings, nil
}

type managementExternalIntegrationSourceScopesAuthenticator struct {
	events      *[]string
	authContext managementauth.Context
	readCalls   int
	touchCalls  int
}

func (s *managementExternalIntegrationSourceScopesAuthenticator) AuthenticateCookie(context.Context, string) (managementauth.Context, error) {
	*s.events = append(*s.events, "read-auth")
	s.readCalls++
	return s.authContext, nil
}

func (s *managementExternalIntegrationSourceScopesAuthenticator) AuthenticateCookieAndTouch(context.Context, string) (managementauth.Context, error) {
	s.touchCalls++
	return s.authContext, nil
}

type managementExternalIntegrationSourceScopesIPLimiter struct {
	events   *[]string
	settings SystemAPIIPRateLimitSettings
	calls    int
}

func (s *managementExternalIntegrationSourceScopesIPLimiter) AllowSystemAPIIP(_ context.Context, _ string, settings SystemAPIIPRateLimitSettings) (SystemAPIRateLimitDecision, error) {
	*s.events = append(*s.events, "ip-limit")
	s.calls++
	s.settings = settings
	return SystemAPIRateLimitDecision{Allowed: true}, nil
}

type managementExternalIntegrationSourceScopesUserLimiter struct {
	events *[]string
	limit  int
	calls  int
}

func (s *managementExternalIntegrationSourceScopesUserLimiter) AllowSystemAPIAuthenticated(_ context.Context, _ string, limit int) (SystemAPIRateLimitDecision, error) {
	*s.events = append(*s.events, "user-limit")
	s.calls++
	s.limit = limit
	return SystemAPIRateLimitDecision{Allowed: true}, nil
}
