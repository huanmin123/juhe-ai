package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/publicapi"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	managementExternalIntegrationSourceScopesPath  = "/__aisys__/api/external-integration-sources/scopes"
	managementExternalIntegrationSourceAPIDocsPath = "/__aisys__/api/external-integration-sources/api-docs"
)

type managementExternalIntegrationSourceCatalogRoute struct {
	name             string
	path             string
	otherPath        string
	newHandler       func() http.Handler
	configureHandler func(*RouterOptions, http.Handler)
	assertBody       func(*testing.T, *httptest.ResponseRecorder)
}

func managementExternalIntegrationSourceCatalogRoutes() []managementExternalIntegrationSourceCatalogRoute {
	return []managementExternalIntegrationSourceCatalogRoute{
		{
			name:       "scopes",
			path:       managementExternalIntegrationSourceScopesPath,
			otherPath:  managementExternalIntegrationSourceAPIDocsPath,
			newHandler: NewManagementExternalIntegrationSourceScopesHandler,
			configureHandler: func(opts *RouterOptions, handler http.Handler) {
				opts.ManagementExternalIntegrationSourceScopesHandler = handler
			},
			assertBody: func(t *testing.T, rec *httptest.ResponseRecorder) {
				t.Helper()
				var body struct {
					Data []publicapi.ScopeOption `json:"data"`
				}
				if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if want := publicapi.ScopeOptions(); !reflect.DeepEqual(body.Data, want) {
					t.Fatalf("scope options = %+v, want %+v", body.Data, want)
				}
			},
		},
		{
			name:       "api docs",
			path:       managementExternalIntegrationSourceAPIDocsPath,
			otherPath:  managementExternalIntegrationSourceScopesPath,
			newHandler: NewManagementExternalIntegrationSourceAPIDocsHandler,
			configureHandler: func(opts *RouterOptions, handler http.Handler) {
				opts.ManagementExternalIntegrationSourceAPIDocsHandler = handler
			},
			assertBody: func(t *testing.T, rec *httptest.ResponseRecorder) {
				t.Helper()
				var body struct {
					Data json.RawMessage `json:"data"`
				}
				if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				var gotCatalog any
				if err := json.Unmarshal(body.Data, &gotCatalog); err != nil {
					t.Fatalf("decode response catalog: %v", err)
				}
				var wantCatalog any
				if err := json.Unmarshal(publicapi.APIDocsCatalog(), &wantCatalog); err != nil {
					t.Fatalf("decode expected catalog: %v", err)
				}
				if !reflect.DeepEqual(gotCatalog, wantCatalog) {
					t.Fatalf("api docs catalog = %#v, want %#v", gotCatalog, wantCatalog)
				}
			},
		},
	}
}

func TestRouterExternalIntegrationSourceCatalogRoutesRequireFullManagementOptIn(t *testing.T) {
	readAuth := func(next http.Handler) http.Handler { return next }

	for _, route := range managementExternalIntegrationSourceCatalogRoutes() {
		t.Run(route.name, func(t *testing.T) {
			tests := []struct {
				name           string
				opts           RouterOptions
				includeHandler bool
				wantStatus     int
			}{
				{
					name:           "management disabled",
					opts:           RouterOptions{Config: config.Config{Host: "127.0.0.1", Port: 3000}},
					includeHandler: true,
					wantStatus:     http.StatusNotFound,
				},
				{
					name: "session only",
					opts: RouterOptions{
						Config: config.Config{
							Host:                          "127.0.0.1",
							Port:                          3000,
							ManagementAuthSessionsEnabled: true,
						},
						ManagementAPIAuthMiddleware:  readAuth,
						ManagementSessionListHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
					},
					includeHandler: true,
					wantStatus:     http.StatusNotFound,
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
					},
					includeHandler: true,
					wantStatus:     http.StatusNoContent,
				},
			}

			for _, testCase := range tests {
				t.Run(testCase.name, func(t *testing.T) {
					handlerCalls := 0
					handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						handlerCalls++
						w.WriteHeader(http.StatusNoContent)
					})
					opts := testCase.opts
					if testCase.includeHandler {
						route.configureHandler(&opts, handler)
					}
					rec := httptest.NewRecorder()
					NewRouter(opts).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, route.path, nil))

					if rec.Code != testCase.wantStatus {
						t.Fatalf("status = %d, want %d; body = %s", rec.Code, testCase.wantStatus, rec.Body.String())
					}
					wantCalls := 0
					if testCase.wantStatus == http.StatusNoContent {
						wantCalls = 1
					}
					if handlerCalls != wantCalls {
						t.Fatalf("handler calls = %d, want %d", handlerCalls, wantCalls)
					}
				})
			}
		})
	}
}

func TestRouterExternalIntegrationSourceCatalogRoutesRegisterGETOnlyAndIndependently(t *testing.T) {
	for _, route := range managementExternalIntegrationSourceCatalogRoutes() {
		t.Run(route.name, func(t *testing.T) {
			handlerCalls := 0
			opts := RouterOptions{
				Config:                      config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
				ManagementAPIAuthMiddleware: func(next http.Handler) http.Handler { return next },
			}
			route.configureHandler(&opts, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				handlerCalls++
				w.WriteHeader(http.StatusNoContent)
			}))
			router := NewRouter(opts)

			tests := []struct {
				method     string
				path       string
				wantStatus int
			}{
				{method: http.MethodGet, path: route.path, wantStatus: http.StatusNoContent},
				{method: http.MethodPost, path: route.path, wantStatus: http.StatusMethodNotAllowed},
				{method: http.MethodGet, path: route.otherPath, wantStatus: http.StatusNotFound},
				{method: http.MethodGet, path: "/__aisys__/api/external-integration-sources", wantStatus: http.StatusNotFound},
				{method: http.MethodPost, path: "/__aisys__/api/external-integration-sources", wantStatus: http.StatusNotFound},
				{method: http.MethodGet, path: "/__aisys__/api/external-integration-sources/extsrc_1", wantStatus: http.StatusNotFound},
				{method: http.MethodPatch, path: "/__aisys__/api/external-integration-sources/extsrc_1", wantStatus: http.StatusNotFound},
				{method: http.MethodDelete, path: "/__aisys__/api/external-integration-sources/extsrc_1", wantStatus: http.StatusNotFound},
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
		})
	}
}

func TestRouterExternalIntegrationSourceCatalogRoutesUseReadAuthAndRateLimitsWithoutTouch(t *testing.T) {
	for _, route := range managementExternalIntegrationSourceCatalogRoutes() {
		t.Run(route.name, func(t *testing.T) {
			events := []string{}
			authenticator := &managementExternalIntegrationSourceCatalogAuthenticator{
				events: &events,
				authContext: managementauth.Context{
					SystemAccountID: "sys_admin",
					Username:        "admin",
					Role:            "admin",
					SessionID:       "sess_admin",
				},
			}
			ipLimiter := &managementExternalIntegrationSourceCatalogIPLimiter{events: &events}
			userLimiter := &managementExternalIntegrationSourceCatalogUserLimiter{events: &events}
			catalogHandler := route.newHandler()
			opts := RouterOptions{
				Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
				SystemAPIRateLimitReader: managementExternalIntegrationSourceCatalogRateLimitReader{
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
			}
			route.configureHandler(&opts, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				events = append(events, "handler")
				catalogHandler.ServeHTTP(w, r)
			}))
			router := NewRouter(opts)

			req := httptest.NewRequest(http.MethodGet, route.path, nil)
			req.Header.Set("Cookie", "juhe_ai_session=session-token")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
			route.assertBody(t, rec)
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

			configured := RouterOptions{}
			route.configureHandler(&configured, catalogHandler)
			if !managementBusinessRoutesConfigured(configured) {
				t.Fatal("catalog route was not classified as a management business route")
			}
			if managementWriteRoutesConfigured(configured) {
				t.Fatal("catalog route was incorrectly classified as a management write route")
			}
		})
	}
}

type managementExternalIntegrationSourceCatalogRateLimitReader struct {
	settings port.SystemAPIRateLimitSettings
}

func (s managementExternalIntegrationSourceCatalogRateLimitReader) SystemAPIRateLimitSettings(context.Context) (port.SystemAPIRateLimitSettings, error) {
	return s.settings, nil
}

type managementExternalIntegrationSourceCatalogAuthenticator struct {
	events      *[]string
	authContext managementauth.Context
	readCalls   int
	touchCalls  int
}

func (s *managementExternalIntegrationSourceCatalogAuthenticator) AuthenticateCookie(context.Context, string) (managementauth.Context, error) {
	*s.events = append(*s.events, "read-auth")
	s.readCalls++
	return s.authContext, nil
}

func (s *managementExternalIntegrationSourceCatalogAuthenticator) AuthenticateCookieAndTouch(context.Context, string) (managementauth.Context, error) {
	s.touchCalls++
	return s.authContext, nil
}

type managementExternalIntegrationSourceCatalogIPLimiter struct {
	events   *[]string
	settings SystemAPIIPRateLimitSettings
	calls    int
}

func (s *managementExternalIntegrationSourceCatalogIPLimiter) AllowSystemAPIIP(_ context.Context, _ string, settings SystemAPIIPRateLimitSettings) (SystemAPIRateLimitDecision, error) {
	*s.events = append(*s.events, "ip-limit")
	s.calls++
	s.settings = settings
	return SystemAPIRateLimitDecision{Allowed: true}, nil
}

type managementExternalIntegrationSourceCatalogUserLimiter struct {
	events *[]string
	limit  int
	calls  int
}

func (s *managementExternalIntegrationSourceCatalogUserLimiter) AllowSystemAPIAuthenticated(_ context.Context, _ string, limit int) (SystemAPIRateLimitDecision, error) {
	*s.events = append(*s.events, "user-limit")
	s.calls++
	s.limit = limit
	return SystemAPIRateLimitDecision{Allowed: true}, nil
}
