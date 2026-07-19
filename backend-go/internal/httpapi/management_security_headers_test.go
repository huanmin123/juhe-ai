package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

var expectedManagementSecurityHeaders = map[string]string{
	"Content-Security-Policy": "default-src 'self'; base-uri 'self'; object-src 'none'; frame-ancestors 'none'; form-action 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; font-src 'self' data: https:; connect-src 'self' https: wss:; worker-src 'self' blob:; media-src 'self' data: blob: https:; manifest-src 'self'",
	"X-Frame-Options":         "DENY",
	"X-Content-Type-Options":  "nosniff",
	"Referrer-Policy":         "strict-origin-when-cross-origin",
}

func TestManagementSecurityHeadersMiddlewareScopesHeadersToSystemPrefix(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := managementSecurityHeadersMiddleware(next)

	for _, test := range []struct {
		path        string
		wantHeaders bool
	}{
		{path: "/__aisys__", wantHeaders: true},
		{path: "/__aisys__/api/auth/me", wantHeaders: true},
		{path: "/__aisys__evil", wantHeaders: false},
		{path: "/__aipublic__/api/accounts", wantHeaders: false},
		{path: "/v1/models", wantHeaders: false},
	} {
		t.Run(test.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))

			for name, want := range expectedManagementSecurityHeaders {
				if got := recorder.Header().Get(name); test.wantHeaders && got != want {
					t.Fatalf("header %s = %q, want %q", name, got, want)
				} else if !test.wantHeaders && got != "" {
					t.Fatalf("header %s = %q outside management prefix, want empty", name, got)
				}
			}
		})
	}
}

func TestRouterAppliesManagementSecurityHeadersOnlyToSystemPrefix(t *testing.T) {
	router := NewRouter(RouterOptions{})
	for _, test := range []struct {
		path        string
		wantStatus  int
		wantHeaders bool
	}{
		{path: "/__aisys__/missing", wantStatus: http.StatusNotFound, wantHeaders: true},
		{path: "/__aisys__/", wantStatus: http.StatusNotFound, wantHeaders: true},
		{path: "/__aisys__/health", wantStatus: http.StatusOK, wantHeaders: true},
		{path: "/__aipublic__/missing", wantStatus: http.StatusNotFound, wantHeaders: false},
		{path: "/v1/missing", wantStatus: http.StatusNotFound, wantHeaders: false},
		{path: "/health", wantStatus: http.StatusNotFound, wantHeaders: false},
	} {
		t.Run(test.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, test.wantStatus)
			}
			for name, want := range expectedManagementSecurityHeaders {
				if got := recorder.Header().Get(name); test.wantHeaders && got != want {
					t.Fatalf("header %s = %q, want %q", name, got, want)
				} else if !test.wantHeaders && got != "" {
					t.Fatalf("header %s = %q outside management prefix, want empty", name, got)
				}
			}
		})
	}
}
