package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

func TestManagementExternalIntegrationSourceCreateMutationGuardMatchesNodeFingerprint(t *testing.T) {
	body := `{"name":" Partner ","status":"disabled","scopes":["responses:write"],"notes":"private"}`
	req := httptest.NewRequest(http.MethodPost, managementExternalIntegrationSourceListPath, strings.NewReader(body))
	guardConfig := managementExternalIntegrationSourceCreateMutationGuardConfig()

	fingerprint, err := guardConfig.fingerprint(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	got, ok := fingerprint.(map[string]any)
	if !ok || guardConfig.operationKey != "external_integration_sources.create" ||
		len(got) != 1 || got["name"] != " Partner " {
		t.Fatalf("config=%+v fingerprint=%#v", guardConfig, fingerprint)
	}
	downstreamBody, err := io.ReadAll(req.Body)
	if err != nil || string(downstreamBody) != body {
		t.Fatalf("downstream body = %q err=%v", downstreamBody, err)
	}
}

func TestRouterRegistersManagementExternalIntegrationSourceCreate(t *testing.T) {
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
		ManagementExternalIntegrationSourceCreateHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			createCalls++
			body, err := io.ReadAll(r.Body)
			if err != nil || string(body) != `{"name":"Partner"}` {
				t.Fatalf("create body = %q err=%v", body, err)
			}
			w.WriteHeader(http.StatusCreated)
		}),
	}
	router := NewRouter(opts)

	for attempt, wantStatus := range []int{http.StatusCreated, http.StatusConflict} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, managementExternalIntegrationSourceListPath, strings.NewReader(`{"name":"Partner"}`))
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
		t.Fatal("external source create must be classified as a management business write route")
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, managementExternalIntegrationSourceListPath, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("create-only list GET status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRouterManagementExternalIntegrationSourceCreateChecksAdminBeforeMutationGuard(t *testing.T) {
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
		ManagementExternalIntegrationSourceCreateHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			createCalls++
		}),
	})
	for attempt := 1; attempt <= 2; attempt++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, managementExternalIntegrationSourceListPath, strings.NewReader(`{"name":"Partner"}`))
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

func TestRouterManagementExternalIntegrationSourceCreateTransportRunsBeforeAuth(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		wantStatus  int
		wantAuth    bool
	}{
		{name: "malformed", contentType: "application/json", body: "{", wantStatus: http.StatusBadRequest},
		{name: "scalar", contentType: "application/json", body: `"source"`, wantStatus: http.StatusBadRequest},
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
				ManagementExternalIntegrationSourceCreateHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					handlerCalls++
					w.WriteHeader(http.StatusNoContent)
				}),
			})
			req := httptest.NewRequest(http.MethodPost, managementExternalIntegrationSourceListPath, strings.NewReader(test.body))
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
