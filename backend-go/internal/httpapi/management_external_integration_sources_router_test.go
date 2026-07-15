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
	managementExternalIntegrationSourceListPath    = "/__aisys__/api/external-integration-sources"
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

func TestRouterExternalIntegrationSourceListRouteRequiresFullManagementOptIn(t *testing.T) {
	readAuth := func(next http.Handler) http.Handler { return next }
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

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handlerCalls := 0
			opts := test.opts
			if test.includeHandler {
				opts.ManagementExternalIntegrationSourceListHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					handlerCalls++
					w.WriteHeader(http.StatusNoContent)
				})
			}
			rec := httptest.NewRecorder()
			NewRouter(opts).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, managementExternalIntegrationSourceListPath, nil))

			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, test.wantStatus, rec.Body.String())
			}
			wantCalls := 0
			if test.wantStatus == http.StatusNoContent {
				wantCalls = 1
			}
			if handlerCalls != wantCalls {
				t.Fatalf("handler calls = %d, want %d", handlerCalls, wantCalls)
			}
		})
	}
}

func TestRouterExternalIntegrationSourceListRouteKeepsUnmigratedRoutesUnavailable(t *testing.T) {
	readAuthCalls := 0
	listHandlerCalls := 0
	router := NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				readAuthCalls++
				next.ServeHTTP(w, r)
			})
		},
		ManagementExternalIntegrationSourceListHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			listHandlerCalls++
			w.WriteHeader(http.StatusNoContent)
		}),
	})

	tests := []struct {
		name               string
		method             string
		path               string
		wantStatus         int
		wantReadAuth       int
		wantListHandler    int
		assertJSONNotFound bool
	}{
		{name: "list GET", method: http.MethodGet, path: managementExternalIntegrationSourceListPath, wantStatus: http.StatusNoContent, wantReadAuth: 1, wantListHandler: 1},
		{name: "collection POST", method: http.MethodPost, path: managementExternalIntegrationSourceListPath, wantStatus: http.StatusNotFound, assertJSONNotFound: true},
		{name: "detail GET", method: http.MethodGet, path: managementExternalIntegrationSourceListPath + "/extsrc_1", wantStatus: http.StatusNotFound},
		{name: "detail PATCH", method: http.MethodPatch, path: managementExternalIntegrationSourceListPath + "/extsrc_1", wantStatus: http.StatusNotFound},
		{name: "detail DELETE", method: http.MethodDelete, path: managementExternalIntegrationSourceListPath + "/extsrc_1", wantStatus: http.StatusNotFound},
		{name: "built-in reset", method: http.MethodPost, path: managementExternalIntegrationSourceListPath + "/built-in-test-token/reset", wantStatus: http.StatusNotFound},
		{name: "token create", method: http.MethodPost, path: managementExternalIntegrationSourceListPath + "/extsrc_1/tokens", wantStatus: http.StatusNotFound},
		{name: "token secret", method: http.MethodGet, path: managementExternalIntegrationSourceListPath + "/extsrc_1/tokens/exttok_1/secret", wantStatus: http.StatusNotFound},
		{name: "token update", method: http.MethodPatch, path: managementExternalIntegrationSourceListPath + "/extsrc_1/tokens/exttok_1", wantStatus: http.StatusNotFound},
		{name: "scopes stays independent", method: http.MethodGet, path: managementExternalIntegrationSourceScopesPath, wantStatus: http.StatusNotFound},
		{name: "api docs stays independent", method: http.MethodGet, path: managementExternalIntegrationSourceAPIDocsPath, wantStatus: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			beforeReadAuth := readAuthCalls
			beforeListHandler := listHandlerCalls
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(test.method, test.path, nil))

			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, test.wantStatus, rec.Body.String())
			}
			if got := readAuthCalls - beforeReadAuth; got != test.wantReadAuth {
				t.Fatalf("read auth calls = %d, want %d", got, test.wantReadAuth)
			}
			if got := listHandlerCalls - beforeListHandler; got != test.wantListHandler {
				t.Fatalf("list handler calls = %d, want %d", got, test.wantListHandler)
			}
			if test.assertJSONNotFound {
				if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
					t.Fatalf("Content-Type = %q", got)
				}
				var body ErrorResponse
				if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if body.Success || body.Error != "接口不存在" {
					t.Fatalf("response = %+v", body)
				}
			}
		})
	}
}

func TestRouterExternalIntegrationSourceCatalogWrongMethodsRemain405WithListEnabled(t *testing.T) {
	handlerCalls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalls++
		w.WriteHeader(http.StatusNoContent)
	})
	router := NewRouter(RouterOptions{
		Config:                      config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware: func(next http.Handler) http.Handler { return next },
		ManagementExternalIntegrationSourceListHandler:    handler,
		ManagementExternalIntegrationSourceScopesHandler:  handler,
		ManagementExternalIntegrationSourceAPIDocsHandler: handler,
	})

	for _, test := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: managementExternalIntegrationSourceScopesPath},
		{method: http.MethodPatch, path: managementExternalIntegrationSourceAPIDocsPath},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(test.method, test.path, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s status = %d, want 405; body = %s", test.method, test.path, rec.Code, rec.Body.String())
		}
	}
	if handlerCalls != 0 {
		t.Fatalf("handler calls = %d, want 0", handlerCalls)
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

func TestRouterExternalIntegrationSourceListUsesReadAuthAndRateLimitsWithoutTouch(t *testing.T) {
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
	listHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		events = append(events, "handler")
		w.WriteHeader(http.StatusNoContent)
	})
	opts := RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		SystemAPIRateLimitReader: managementExternalIntegrationSourceCatalogRateLimitReader{
			settings: port.SystemAPIRateLimitSettings{
				IPReadPerMinute:         600,
				IPReadBurstPer10Seconds: 120,
				UserReadPerMinute:       300,
			},
		},
		SystemAPIIPRateLimiter:                         ipLimiter,
		SystemAPIAuthenticatedRateLimiter:              userLimiter,
		ManagementAPIAuthMiddleware:                    NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware:               NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementExternalIntegrationSourceListHandler: listHandler,
	}
	router := NewRouter(opts)

	req := httptest.NewRequest(http.MethodGet, managementExternalIntegrationSourceListPath, nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
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
	if !managementBusinessRoutesConfigured(opts) {
		t.Fatal("list route was not classified as a management business route")
	}
	if managementWriteRoutesConfigured(opts) {
		t.Fatal("list route was incorrectly classified as a management write route")
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
