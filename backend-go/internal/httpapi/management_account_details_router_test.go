package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"juhe-ai/backend-go/internal/config"
)

func TestRouterDoesNotRegisterRevokedAccountBasicDetailRoutes(t *testing.T) {
	childCalls := 0
	childHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		childCalls++
		w.WriteHeader(http.StatusNoContent)
	})
	router := NewRouter(RouterOptions{
		Config:                                    config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware:               func(next http.Handler) http.Handler { return next },
		ManagementAccountEditBasicDetailHandler:   childHandler,
		ManagementMyAccountEditBasicDetailHandler: childHandler,
	})

	for _, path := range []string{
		"/__aisys__/api/accounts/acct_1",
		"/__aisys__/api/my-accounts/acct_1",
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, recorder.Code)
		}
	}
	if childCalls != 0 {
		t.Fatalf("revoked routes reached child handler %d times", childCalls)
	}

	for _, path := range []string{
		"/__aisys__/api/accounts/acct_1/edit-basic",
		"/__aisys__/api/my-accounts/acct_1/edit-basic",
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("%s status = %d, want 204", path, recorder.Code)
		}
	}
	if childCalls != 2 {
		t.Fatalf("child handler calls = %d, want 2", childCalls)
	}
}
