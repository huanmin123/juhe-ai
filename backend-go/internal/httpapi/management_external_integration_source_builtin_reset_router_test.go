package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

const managementExternalIntegrationSourceBuiltInResetPath = managementExternalIntegrationSourceListPath + "/built-in-test-token/reset"

func TestManagementExternalIntegrationSourceBuiltInResetMutationGuardContract(t *testing.T) {
	config := managementExternalIntegrationSourceBuiltInResetMutationGuardConfig()
	if config.operationKey != "external_integration_sources.reset_builtin_test_token" {
		t.Fatalf("operation key = %q", config.operationKey)
	}
	fingerprint, err := config.fingerprint(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/reset", nil))
	if err != nil {
		t.Fatalf("fingerprint error = %v", err)
	}
	if want := map[string]any{"target": "built_in_test_token"}; !reflect.DeepEqual(fingerprint, want) {
		t.Fatalf("fingerprint = %#v, want %#v", fingerprint, want)
	}
}

func TestRouterRegistersManagementExternalIntegrationSourceBuiltInResetBeforeDetail(t *testing.T) {
	touchCalls, resetCalls, detailCalls := 0, 0, 0
	opts := RouterOptions{
		Config:                      config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware: func(next http.Handler) http.Handler { return next },
		ManagementAPIAuthTouchMiddleware: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				touchCalls++
				r = requestWithManagementExternalIntegrationSourceAuthContext(r, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"})
				next.ServeHTTP(w, r)
			})
		},
		ManagementExternalSourceBuiltInResetHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resetCalls++
			body, err := io.ReadAll(r.Body)
			if err != nil || string(body) != "{}" {
				t.Fatalf("reset body=%q err=%v", body, err)
			}
			w.WriteHeader(http.StatusOK)
		}),
		ManagementExternalIntegrationSourceDetailHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			detailCalls++
		}),
	}
	router := NewRouter(opts)

	for attempt, wantStatus := range []int{http.StatusOK, http.StatusConflict} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, managementExternalIntegrationSourceBuiltInResetPath, nil)
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		if rec.Code != wantStatus {
			t.Fatalf("attempt=%d status=%d want=%d body=%s", attempt+1, rec.Code, wantStatus, rec.Body.String())
		}
	}
	if touchCalls != 2 || resetCalls != 1 || detailCalls != 0 {
		t.Fatalf("touch=%d reset=%d detail=%d", touchCalls, resetCalls, detailCalls)
	}
	if !managementBusinessRoutesConfigured(opts) || !managementWriteRoutesConfigured(opts) {
		t.Fatal("built-in reset must be classified as a management business write route")
	}
}

func TestRouterManagementExternalIntegrationSourceBuiltInResetTransportAndAdmin(t *testing.T) {
	tests := []struct {
		name        string
		role        string
		body        string
		wantStatus  int
		wantAuth    bool
		wantHandler bool
	}{
		{name: "empty body", role: "admin", wantStatus: http.StatusNoContent, wantAuth: true, wantHandler: true},
		{name: "malformed json", role: "admin", body: "{", wantStatus: http.StatusBadRequest},
		{name: "oversized", role: "admin", body: `{"padding":"` + strings.Repeat("x", managementGroupCreateMaxBodyBytes) + `"}`, wantStatus: http.StatusRequestEntityTooLarge},
		{name: "ordinary user", role: "user", body: `{}`, wantStatus: http.StatusForbidden, wantAuth: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authCalls, handlerCalls := 0, 0
			router := NewRouter(RouterOptions{
				Config:                      config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
				ManagementAPIAuthMiddleware: func(next http.Handler) http.Handler { return next },
				ManagementAPIAuthTouchMiddleware: func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						authCalls++
						r = requestWithManagementExternalIntegrationSourceAuthContext(r, managementauth.Context{SystemAccountID: "sys_account", Role: test.role})
						next.ServeHTTP(w, r)
					})
				},
				ManagementExternalSourceBuiltInResetHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					handlerCalls++
					w.WriteHeader(http.StatusNoContent)
				}),
			})
			req := httptest.NewRequest(http.MethodPost, managementExternalIntegrationSourceBuiltInResetPath, strings.NewReader(test.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != test.wantStatus || (authCalls > 0) != test.wantAuth || (handlerCalls > 0) != test.wantHandler {
				t.Fatalf("status=%d auth=%d handler=%d body=%s", rec.Code, authCalls, handlerCalls, rec.Body.String())
			}
		})
	}
}
