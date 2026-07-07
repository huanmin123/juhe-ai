package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementsystemaccounts"
)

func TestManagementSystemAccountOptionsHandlerRequiresAdminAndParsesQuery(t *testing.T) {
	service := &managementSystemAccountOptionServiceStub{
		options: []managementsystemaccounts.Option{{
			ID:          "sys_user",
			Username:    "user",
			DisplayName: "用户",
			Status:      "active",
		}},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementSystemAccountOptionsHandler(service))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/system-accounts/options?ids=sys_user,sys_disabled&ids=sys_user&limit=500&keyword=%20%E7%94%A8%E6%88%B7%20&role=super_admin", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.input.Keyword != "用户" || service.input.Limit != 500 {
		t.Fatalf("service input = %+v", service.input)
	}
	if len(service.input.IDs) != 2 || service.input.IDs[0] != "sys_user" || service.input.IDs[1] != "sys_disabled" {
		t.Fatalf("ids = %#v", service.input.IDs)
	}
	var body struct {
		Data []managementsystemaccounts.Option `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != "sys_user" || body.Data[0].DisplayName != "用户" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementSystemAccountOptionsHandlerRejectsOrdinaryUser(t *testing.T) {
	service := &managementSystemAccountOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementSystemAccountOptionsHandler(service))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/system-accounts/options", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if service.called {
		t.Fatal("service should not be called for ordinary user")
	}
}

func TestManagementSystemAccountOptionsHandlerRedactsStoreErrors(t *testing.T) {
	handler := newManagementSystemAccountOptionsHandler(&managementSystemAccountOptionServiceStub{err: errors.New("postgres password leaked")})
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/system-accounts/options", nil)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["message"] != "服务器内部错误" {
		t.Fatalf("body = %+v", body)
	}
}

func TestRouterRegistersW2ManagementSystemAccountOptions(t *testing.T) {
	service := &managementSystemAccountOptionServiceStub{
		options: []managementsystemaccounts.Option{{ID: "sys_user", Username: "user", DisplayName: "用户", Status: "active"}},
	}
	router := NewRouter(RouterOptions{
		Config:                                config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                                slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementSystemAccountOptionsHandler: newManagementSystemAccountOptionsHandler(service),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/system-accounts/options?limit=50", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestRouterDoesNotRegisterW2ManagementSystemAccountOptionsWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                                config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                                slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementSystemAccountOptionsHandler: newManagementSystemAccountOptionsHandler(&managementSystemAccountOptionServiceStub{}),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/system-accounts/options", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", rec.Code)
	}
}

type managementSystemAccountOptionServiceStub struct {
	called  bool
	input   managementsystemaccounts.OptionListInput
	options []managementsystemaccounts.Option
	err     error
}

func (s *managementSystemAccountOptionServiceStub) Options(_ *http.Request, input managementsystemaccounts.OptionListInput) ([]managementsystemaccounts.Option, error) {
	s.called = true
	s.input = input
	return s.options, s.err
}
