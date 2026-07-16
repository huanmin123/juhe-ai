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

const managementExternalIntegrationSourceTokenUpdatePath = managementExternalIntegrationSourceDetailPath + "/tokens/exttok_1"

func TestRouterRegistersManagementExternalIntegrationSourceTokenUpdate(t *testing.T) {
	touchCalls, handlerCalls := 0, 0
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
		ManagementExternalSourceTokenUpdateHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handlerCalls++
			if chi.URLParam(r, "id") != "extsrc_1" || chi.URLParam(r, "tokenId") != "exttok_1" {
				t.Fatalf("params id=%q token=%q", chi.URLParam(r, "id"), chi.URLParam(r, "tokenId"))
			}
			body, _ := io.ReadAll(r.Body)
			if string(body) != `{"name":"Token"}` {
				t.Fatalf("body=%q", body)
			}
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	router := NewRouter(opts)
	for attempt, want := range []int{http.StatusNoContent, http.StatusConflict} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPatch, managementExternalIntegrationSourceTokenUpdatePath, strings.NewReader(`{"name":"Token"}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("attempt=%d status=%d want=%d body=%s", attempt+1, rec.Code, want, rec.Body.String())
		}
	}
	if touchCalls != 2 || handlerCalls != 1 || !managementBusinessRoutesConfigured(opts) || !managementWriteRoutesConfigured(opts) {
		t.Fatalf("touch=%d handler=%d", touchCalls, handlerCalls)
	}
}

func TestRouterExternalIntegrationSourceTokenUpdateOptInAndAdjacentMethods(t *testing.T) {
	base := RouterOptions{Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true}, ManagementAPIAuthMiddleware: func(next http.Handler) http.Handler { return next }, ManagementAPIAuthTouchMiddleware: func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = requestWithManagementExternalIntegrationSourceAuthContext(r, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"})
			next.ServeHTTP(w, r)
		})
	}, ManagementCurrentUserHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for _, test := range []struct {
		name string
		opts RouterOptions
		want int
	}{
		{"disabled", RouterOptions{Config: config.Config{}, ManagementExternalSourceTokenUpdateHandler: handler}, http.StatusNotFound},
		{"not opted in", base, http.StatusNotFound},
		{"enabled", func() RouterOptions { o := base; o.ManagementExternalSourceTokenUpdateHandler = handler; return o }(), http.StatusNoContent},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			NewRouter(test.opts).ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, managementExternalIntegrationSourceTokenUpdatePath, strings.NewReader(`{}`)))
			if rec.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, test.want, rec.Body.String())
			}
		})
	}

	opts := base
	opts.ManagementExternalSourceTokenUpdateHandler = handler
	router := NewRouter(opts)
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(method, managementExternalIntegrationSourceTokenUpdatePath, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s item status=%d", method, rec.Code)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(method, managementExternalIntegrationSourceTokenSecretPath, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s secret status=%d", method, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, managementExternalIntegrationSourceTokenSecretPath, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("secret GET status=%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, managementExternalIntegrationSourceTokenCreatePath, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("collection POST status=%d", rec.Code)
	}
}

func TestRouterExternalIntegrationSourceTokenUpdateChecksAdminBeforeMutationGuard(t *testing.T) {
	calls := 0
	router := NewRouter(RouterOptions{Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true}, ManagementAPIAuthMiddleware: func(next http.Handler) http.Handler { return next }, ManagementAPIAuthTouchMiddleware: func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = requestWithManagementExternalIntegrationSourceAuthContext(r, managementauth.Context{SystemAccountID: "sys_user", Role: "user"})
			next.ServeHTTP(w, r)
		})
	}, ManagementExternalSourceTokenUpdateHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ })})
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, managementExternalIntegrationSourceTokenUpdatePath, strings.NewReader(`{}`)))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status=%d", rec.Code)
		}
	}
	if calls != 0 {
		t.Fatalf("handler calls=%d", calls)
	}
}
