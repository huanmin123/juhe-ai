package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

const managementExternalIntegrationSourceTokenCreatePath = managementExternalIntegrationSourceDetailPath + "/tokens"

func TestManagementExternalIntegrationSourceTokenCreateMutationGuardMatchesNodeFingerprint(t *testing.T) {
	body := `{"name":" Partner Token ","status":"disabled","scopes":["ignored"],"expiresAt":"2026-08-01T00:00:00.000Z"}`
	req := managementExternalIntegrationSourceTokenCreateRequest(" raw source id ", body)
	guardConfig := managementExternalIntegrationSourceTokenCreateMutationGuardConfig()

	fingerprint, err := guardConfig.fingerprint(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	got, ok := fingerprint.(map[string]any)
	if !ok || guardConfig.operationKey != "external_integration_sources.create_token" || len(got) != 3 ||
		got["id"] != " raw source id " || got["name"] != " Partner Token " ||
		got["expiresAt"] != "2026-08-01T00:00:00.000Z" {
		t.Fatalf("config=%+v fingerprint=%#v", guardConfig, fingerprint)
	}
	if _, exists := got["status"]; exists {
		t.Fatalf("status must not participate in fingerprint: %#v", got)
	}
	if _, exists := got["scopes"]; exists {
		t.Fatalf("scopes must not participate in fingerprint: %#v", got)
	}
	downstreamBody, err := io.ReadAll(req.Body)
	if err != nil || string(downstreamBody) != body {
		t.Fatalf("downstream body = %q err=%v", downstreamBody, err)
	}
}

func TestRouterRegistersManagementExternalIntegrationSourceTokenCreate(t *testing.T) {
	touchCalls := 0
	createCalls := 0
	opts := RouterOptions{
		Config:                      config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware: func(next http.Handler) http.Handler { return next },
		ManagementAPIAuthTouchMiddleware: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				touchCalls++
				r = requestWithManagementExternalIntegrationSourceAuthContext(r, managementauth.Context{
					SystemAccountID: "sys_admin",
					Role:            "admin",
				})
				next.ServeHTTP(w, r)
			})
		},
		ManagementExternalSourceTokenCreateHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			createCalls++
			if chi.URLParam(r, "id") != "extsrc_1" {
				t.Fatalf("source id = %q", chi.URLParam(r, "id"))
			}
			body, err := io.ReadAll(r.Body)
			if err != nil || string(body) != `{"name":"Partner Token"}` {
				t.Fatalf("create body = %q err=%v", body, err)
			}
			w.WriteHeader(http.StatusCreated)
		}),
	}
	router := NewRouter(opts)

	for attempt, wantStatus := range []int{http.StatusCreated, http.StatusConflict} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, managementExternalIntegrationSourceTokenCreatePath, strings.NewReader(`{"name":"Partner Token"}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		if rec.Code != wantStatus {
			t.Fatalf("attempt %d status=%d want=%d body=%s", attempt+1, rec.Code, wantStatus, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("attempt %d Cache-Control=%q", attempt+1, rec.Header().Get("Cache-Control"))
		}
	}
	if touchCalls != 2 || createCalls != 1 {
		t.Fatalf("touch calls=%d create calls=%d", touchCalls, createCalls)
	}
	if !managementBusinessRoutesConfigured(opts) || !managementWriteRoutesConfigured(opts) {
		t.Fatal("external source token create must be classified as a management business write route")
	}
}

func TestRouterManagementExternalIntegrationSourceTokenCreateChecksAdminBeforeMutationGuard(t *testing.T) {
	createCalls := 0
	router := NewRouter(RouterOptions{
		Config:                      config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware: func(next http.Handler) http.Handler { return next },
		ManagementAPIAuthTouchMiddleware: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r = requestWithManagementExternalIntegrationSourceAuthContext(r, managementauth.Context{
					SystemAccountID: "sys_user",
					Role:            "user",
				})
				next.ServeHTTP(w, r)
			})
		},
		ManagementExternalSourceTokenCreateHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			createCalls++
		}),
	})
	for attempt := 1; attempt <= 2; attempt++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, managementExternalIntegrationSourceTokenCreatePath, strings.NewReader(`{"name":"Partner Token"}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d status=%d want=403 body=%s", attempt, rec.Code, rec.Body.String())
		}
	}
	if createCalls != 0 {
		t.Fatalf("non-admin reached create handler: calls=%d", createCalls)
	}
}

func TestRouterManagementExternalIntegrationSourceTokenCreateTransportRunsBeforeAuth(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		wantStatus  int
		wantAuth    bool
	}{
		{name: "malformed", contentType: "application/json", body: "{", wantStatus: http.StatusBadRequest},
		{name: "scalar", contentType: "application/json", body: `"token"`, wantStatus: http.StatusBadRequest},
		{name: "unsupported charset", contentType: "application/json; charset=gbk", body: `{}`, wantStatus: http.StatusUnsupportedMediaType},
		{name: "oversized", contentType: "application/json", body: `{"name":"` + strings.Repeat("x", managementGroupCreateMaxBodyBytes) + `"}`, wantStatus: http.StatusRequestEntityTooLarge},
		{name: "empty reaches authenticated handler", contentType: "application/json", body: "", wantStatus: http.StatusNoContent, wantAuth: true},
		{name: "non json becomes empty object", contentType: "text/plain", body: `{"name":"ignored"}`, wantStatus: http.StatusNoContent, wantAuth: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authCalls := 0
			handlerCalls := 0
			router := NewRouter(RouterOptions{
				Config:                      config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
				ManagementAPIAuthMiddleware: func(next http.Handler) http.Handler { return next },
				ManagementAPIAuthTouchMiddleware: func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						authCalls++
						r = requestWithManagementExternalIntegrationSourceAuthContext(r, managementauth.Context{
							SystemAccountID: "sys_admin",
							Role:            "admin",
						})
						next.ServeHTTP(w, r)
					})
				},
				ManagementExternalSourceTokenCreateHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					handlerCalls++
					w.WriteHeader(http.StatusNoContent)
				}),
			})
			req := httptest.NewRequest(http.MethodPost, managementExternalIntegrationSourceTokenCreatePath, strings.NewReader(test.body))
			req.Header.Set("Content-Type", test.contentType)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, test.wantStatus, rec.Body.String())
			}
			if (authCalls > 0) != test.wantAuth {
				t.Fatalf("auth calls=%d wantAuth=%t", authCalls, test.wantAuth)
			}
			wantHandlerCalls := 0
			if test.wantAuth {
				wantHandlerCalls = 1
			}
			if handlerCalls != wantHandlerCalls {
				t.Fatalf("handler calls=%d want=%d", handlerCalls, wantHandlerCalls)
			}
		})
	}
}

func TestRouterExternalIntegrationSourceTokenCreateRequiresIndependentFullManagementOptIn(t *testing.T) {
	writeAuth := func(next http.Handler) http.Handler { return next }
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
				Config:                           config.Config{Host: "127.0.0.1", Port: 3000, ManagementAuthSessionsEnabled: true},
				ManagementAPIAuthMiddleware:      writeAuth,
				ManagementAPIAuthTouchMiddleware: writeAuth,
				ManagementSessionListHandler:     http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
			},
			includeHandler: true,
			wantStatus:     http.StatusNotFound,
		},
		{
			name: "handler not opted in",
			opts: RouterOptions{
				Config:                       config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
				ManagementAPIAuthMiddleware:  writeAuth,
				ManagementCurrentUserHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name: "management and handler enabled",
			opts: RouterOptions{
				Config:                      config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
				ManagementAPIAuthMiddleware: writeAuth,
				ManagementAPIAuthTouchMiddleware: func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						r = requestWithManagementExternalIntegrationSourceAuthContext(r, managementauth.Context{
							SystemAccountID: "sys_admin",
							Role:            "admin",
						})
						next.ServeHTTP(w, r)
					})
				},
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
				opts.ManagementExternalSourceTokenCreateHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					handlerCalls++
					w.WriteHeader(http.StatusNoContent)
				})
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, managementExternalIntegrationSourceTokenCreatePath, strings.NewReader(`{"name":"Token"}`))
			req.Header.Set("Content-Type", "application/json")
			NewRouter(opts).ServeHTTP(rec, req)
			if rec.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, test.wantStatus, rec.Body.String())
			}
			wantCalls := 0
			if test.wantStatus == http.StatusNoContent {
				wantCalls = 1
			}
			if handlerCalls != wantCalls {
				t.Fatalf("handler calls=%d want=%d", handlerCalls, wantCalls)
			}
		})
	}
}

func TestRouterExternalIntegrationSourceTokenCreateOnlyKeepsAdjacentRoutesUnavailable(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                           config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware:      func(next http.Handler) http.Handler { return next },
		ManagementAPIAuthTouchMiddleware: func(next http.Handler) http.Handler { return next },
		ManagementExternalSourceTokenCreateHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	})

	tests := []struct {
		method     string
		path       string
		wantStatus int
		wantBody   string
	}{
		{method: http.MethodGet, path: managementExternalIntegrationSourceListPath, wantStatus: http.StatusNotFound},
		{method: http.MethodPost, path: managementExternalIntegrationSourceListPath, wantStatus: http.StatusMethodNotAllowed},
		{method: http.MethodGet, path: managementExternalIntegrationSourceDetailPath, wantStatus: http.StatusNotFound},
		{method: http.MethodPatch, path: managementExternalIntegrationSourceDetailPath, wantStatus: http.StatusNotFound},
		{method: http.MethodPost, path: managementExternalIntegrationSourceListPath + "/built-in-test-token/reset", wantStatus: http.StatusNotFound},
		{method: http.MethodGet, path: managementExternalIntegrationSourceTokenSecretPath, wantStatus: http.StatusNotFound},
		{method: http.MethodGet, path: managementExternalIntegrationSourceTokenCreatePath, wantStatus: http.StatusNotFound, wantBody: `{"success":false,"error":"接口不存在"}`},
		{method: http.MethodPut, path: managementExternalIntegrationSourceTokenCreatePath, wantStatus: http.StatusNotFound, wantBody: `{"success":false,"error":"接口不存在"}`},
		{method: http.MethodPatch, path: managementExternalIntegrationSourceTokenCreatePath, wantStatus: http.StatusNotFound, wantBody: `{"success":false,"error":"接口不存在"}`},
		{method: http.MethodDelete, path: managementExternalIntegrationSourceTokenCreatePath, wantStatus: http.StatusNotFound, wantBody: `{"success":false,"error":"接口不存在"}`},
		{method: http.MethodPatch, path: managementExternalIntegrationSourceDetailPath + "/tokens/exttok_1", wantStatus: http.StatusNotFound},
		{method: http.MethodDelete, path: managementExternalIntegrationSourceDetailPath + "/tokens/exttok_1", wantStatus: http.StatusNotFound},
	}
	for _, test := range tests {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(test.method, test.path, nil))
		if rec.Code != test.wantStatus {
			t.Fatalf("%s %s status=%d want=%d body=%s", test.method, test.path, rec.Code, test.wantStatus, rec.Body.String())
		}
		if test.wantBody != "" && strings.TrimSpace(rec.Body.String()) != test.wantBody {
			t.Fatalf("%s %s body=%q want=%q", test.method, test.path, rec.Body.String(), test.wantBody)
		}
	}
}
