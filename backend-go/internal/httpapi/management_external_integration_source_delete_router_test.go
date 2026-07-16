package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

func TestManagementExternalIntegrationSourceDeleteMutationGuardMatchesNodeFingerprint(t *testing.T) {
	guardConfig := managementExternalIntegrationSourceDeleteMutationGuardConfig()
	req := httptest.NewRequest(http.MethodDelete, "/__aisys__/api/external-integration-sources/source_1", strings.NewReader("body-is-ignored"))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", "\ufeffsource_1\ufeff")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))

	fingerprint, err := guardConfig.fingerprint(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	got, ok := fingerprint.(map[string]any)
	if !ok || guardConfig.operationKey != "external_integration_sources.delete" || len(got) != 1 || got["id"] != "source_1" {
		t.Fatalf("config=%+v fingerprint=%#v", guardConfig, fingerprint)
	}
	downstreamBody, err := io.ReadAll(req.Body)
	if err != nil || string(downstreamBody) != "body-is-ignored" {
		t.Fatalf("downstream body = %q err=%v", downstreamBody, err)
	}
}

func TestRouterRegistersManagementExternalIntegrationSourceDelete(t *testing.T) {
	touchCalls := 0
	deleteCalls := 0
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
		ManagementExternalIntegrationSourceDeleteHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			deleteCalls++
			if chi.URLParam(r, "id") != "source_1" {
				t.Fatalf("source id = %q", chi.URLParam(r, "id"))
			}
			w.WriteHeader(http.StatusNoContent)
		}),
		ManagementExternalIntegrationSourceScopesHandler:  http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		ManagementExternalIntegrationSourceAPIDocsHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	}
	router := NewRouter(opts)

	for attempt, wantStatus := range []int{http.StatusNoContent, http.StatusConflict} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/__aisys__/api/external-integration-sources/source_1", strings.NewReader("ignored")))
		if rec.Code != wantStatus {
			t.Fatalf("attempt %d status=%d want=%d body=%s", attempt+1, rec.Code, wantStatus, rec.Body.String())
		}
		if attempt == 0 && (rec.Body.Len() != 0 || rec.Header().Get("Cache-Control") != "no-store") {
			t.Fatalf("successful delete body=%q Cache-Control=%q", rec.Body.String(), rec.Header().Get("Cache-Control"))
		}
	}
	if touchCalls != 2 || deleteCalls != 1 {
		t.Fatalf("touch calls=%d delete calls=%d", touchCalls, deleteCalls)
	}
	if !managementBusinessRoutesConfigured(opts) || !managementWriteRoutesConfigured(opts) {
		t.Fatal("external source delete must be classified as a management business write route")
	}

	for _, path := range []string{
		"/__aisys__/api/external-integration-sources/scopes",
		"/__aisys__/api/external-integration-sources/api-docs",
	} {
		for attempt := 1; attempt <= 2; attempt++ {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, path, nil))
			if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodGet {
				t.Fatalf("attempt %d DELETE %s status=%d Allow=%q body=%s", attempt, path, rec.Code, rec.Header().Get("Allow"), rec.Body.String())
			}
		}
	}
	if deleteCalls != 1 {
		t.Fatalf("static catalog paths reached delete handler: calls=%d", deleteCalls)
	}

	for _, method := range []string{http.MethodPost, http.MethodPut} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(method, "/__aisys__/api/external-integration-sources/source_1", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s detail status=%d body=%s", method, rec.Code, rec.Body.String())
		}
	}
}

func TestRouterManagementExternalIntegrationSourceDeleteChecksAdminBeforeMutationGuard(t *testing.T) {
	deleteCalls := 0
	opts := RouterOptions{
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
		ManagementExternalIntegrationSourceDeleteHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			deleteCalls++
		}),
	}
	router := NewRouter(opts)
	for attempt := 1; attempt <= 2; attempt++ {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/__aisys__/api/external-integration-sources/source_1", nil))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d status=%d want=403 body=%s", attempt, rec.Code, rec.Body.String())
		}
	}
	if deleteCalls != 0 {
		t.Fatalf("non-admin reached delete handler: calls=%d", deleteCalls)
	}
}
