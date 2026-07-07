package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"juhe-ai/backend-go/internal/modules/managementauth"
)

func TestManagementAPIAuthMiddlewareInjectsContext(t *testing.T) {
	authenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
			DisplayName:     "管理员",
			Role:            "admin",
			SessionID:       "sess_admin",
		},
	}
	var got managementauth.Context
	handler := NewManagementAPIAuthMiddleware(authenticator)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ok bool
		got, ok = ManagementAuthContextFromRequest(r)
		if !ok {
			t.Fatal("management auth context missing")
		}
		writeData(w, http.StatusOK, map[string]string{"ok": "true"})
	}))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if authenticator.cookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("cookie header = %q", authenticator.cookieHeader)
	}
	if got.SystemAccountID != "sys_admin" || got.Role != "admin" || got.SessionID != "sess_admin" {
		t.Fatalf("context = %+v", got)
	}
}

func TestManagementAPIAuthMiddlewareWritesAuthErrors(t *testing.T) {
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		err: &managementauth.AuthError{
			StatusCode: http.StatusForbidden,
			Code:       managementauth.ErrorCodeMustChangePassword,
			Message:    "请先修改初始密码",
		},
	})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["message"] != "请先修改初始密码" || body["code"] != managementauth.ErrorCodeMustChangePassword {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementAPIAuthMiddlewareRedactsUnexpectedErrors(t *testing.T) {
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		err: errors.New("postgres password leaked"),
	})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options", nil)
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

type managementAPIAuthenticatorStub struct {
	cookieHeader string
	context      managementauth.Context
	err          error
}

func (s *managementAPIAuthenticatorStub) AuthenticateCookie(_ context.Context, cookieHeader string) (managementauth.Context, error) {
	s.cookieHeader = cookieHeader
	return s.context, s.err
}
