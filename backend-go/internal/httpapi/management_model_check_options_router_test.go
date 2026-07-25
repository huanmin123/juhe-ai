package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementmodelcheckoptions"
)

func TestRouterDoesNotRegisterManagementModelCheckOptionsWhenDisabled(t *testing.T) {
	handlerCalls := 0
	router := NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000},
		ManagementModelCheckOptionsHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			handlerCalls++
		}),
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/__aisys__/api/model-checks/options", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 while management API disabled; body = %s", rec.Code, rec.Body.String())
	}
	if handlerCalls != 0 {
		t.Fatalf("handler calls = %d, want 0", handlerCalls)
	}
}

func TestRouterRegistersManagementModelCheckOptionsWithReadAuth(t *testing.T) {
	service := managementmodelcheckoptions.NewService()
	for _, tt := range []struct {
		name string
		path string
		role string
	}{
		{name: "admin", path: "/__aisys__/api/model-checks/options", role: "admin"},
		{name: "self", path: "/__aisys__/api/my-model-checks/options", role: "user"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			readCalls := 0
			touchCalls := 0
			router := NewRouter(RouterOptions{
				Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
				ManagementAPIAuthMiddleware: func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						readCalls++
						ctx := context.WithValue(r.Context(), managementAuthContextKey, managementauth.Context{
							SystemAccountID: "sys_test",
							Role:            tt.role,
						})
						next.ServeHTTP(w, r.WithContext(ctx))
					})
				},
				ManagementAPIAuthTouchMiddleware: func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						touchCalls++
						next.ServeHTTP(w, r)
					})
				},
				ManagementModelCheckOptionsHandler:   NewManagementModelCheckOptionsHandler(service),
				ManagementMyModelCheckOptionsHandler: NewManagementMyModelCheckOptionsHandler(service),
			})

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
			if readCalls != 1 || touchCalls != 0 {
				t.Fatalf("read calls = %d, touch calls = %d; want 1, 0", readCalls, touchCalls)
			}
		})
	}
}
