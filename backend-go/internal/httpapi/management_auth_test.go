package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/config"
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

func TestManagementCurrentUserHandlerReturnsSessionUser(t *testing.T) {
	authenticator := &managementCurrentUserAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID:    "sys_user",
			Username:           "user",
			DisplayName:        "用户",
			Role:               "user",
			MustChangePassword: true,
			SessionID:          "sess_user",
		},
	}
	handler := NewManagementCurrentUserHandler(authenticator)

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/me", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if authenticator.cookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("cookie header = %q", authenticator.cookieHeader)
	}
	var body struct {
		Data managementCurrentUserResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.ID != "sys_user" || body.Data.Role != "user" || !body.Data.MustChangePassword {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementCurrentUserHandlerWritesAuthErrors(t *testing.T) {
	handler := NewManagementCurrentUserHandler(&managementCurrentUserAuthenticatorStub{
		err: &managementauth.AuthError{StatusCode: http.StatusUnauthorized, Message: "请先登录"},
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/me", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["message"] != "请先登录" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementCurrentUserHandlerRedactsUnexpectedErrors(t *testing.T) {
	handler := NewManagementCurrentUserHandler(&managementCurrentUserAuthenticatorStub{
		err: errors.New("postgres password leaked"),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/me", nil)
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

func TestManagementLogoutHandlerRevokesSessionAndClearsCookie(t *testing.T) {
	authenticator := &managementLogoutAuthenticatorStub{}
	handler := NewManagementLogoutHandler(authenticator)

	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/logout", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if authenticator.cookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("cookie header = %q", authenticator.cookieHeader)
	}
	var body struct {
		Data managementLogoutResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Data.LoggedOut {
		t.Fatalf("body = %+v", body)
	}
	setCookie := rec.Header().Get("Set-Cookie")
	if !strings.Contains(setCookie, "juhe_ai_session=") || !strings.Contains(setCookie, "Max-Age=0") || !strings.Contains(setCookie, "Path=/") || !strings.Contains(setCookie, "HttpOnly") {
		t.Fatalf("Set-Cookie = %q", setCookie)
	}
}

func TestManagementLogoutHandlerAllowsMissingCookie(t *testing.T) {
	authenticator := &managementLogoutAuthenticatorStub{}
	handler := NewManagementLogoutHandler(authenticator)

	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/logout", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if authenticator.cookieHeader != "" {
		t.Fatalf("cookie header = %q", authenticator.cookieHeader)
	}
	var body struct {
		Data managementLogoutResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Data.LoggedOut {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementLogoutHandlerRedactsUnexpectedErrors(t *testing.T) {
	handler := NewManagementLogoutHandler(&managementLogoutAuthenticatorStub{
		err: errors.New("postgres password leaked"),
	})

	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/logout", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
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
	if rec.Header().Get("Set-Cookie") != "" {
		t.Fatalf("Set-Cookie = %q, want empty on revoke failure", rec.Header().Get("Set-Cookie"))
	}
}

func TestRouterRegistersManagementCurrentUserWhenEnabled(t *testing.T) {
	authenticator := &managementCurrentUserAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
			DisplayName:     "管理员",
			Role:            "admin",
			SessionID:       "sess_admin",
		},
	}
	router := NewRouter(RouterOptions{
		Config:                       config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware:  NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{}),
		ManagementCurrentUserHandler: NewManagementCurrentUserHandler(authenticator),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/me", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if authenticator.cookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("cookie header = %q", authenticator.cookieHeader)
	}
}

func TestRouterDoesNotRegisterManagementCurrentUserWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                       config.Config{Host: "127.0.0.1", Port: 3000},
		ManagementAPIAuthMiddleware:  NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{}),
		ManagementCurrentUserHandler: NewManagementCurrentUserHandler(&managementCurrentUserAuthenticatorStub{}),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/me", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", rec.Code)
	}
}

func TestRouterRegistersManagementLogoutWhenEnabled(t *testing.T) {
	authenticator := &managementLogoutAuthenticatorStub{}
	router := NewRouter(RouterOptions{
		Config:                       config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware:  NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{}),
		ManagementLogoutHandler:      NewManagementLogoutHandler(authenticator),
		ManagementCurrentUserHandler: NewManagementCurrentUserHandler(&managementCurrentUserAuthenticatorStub{}),
	})

	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/logout", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if authenticator.cookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("cookie header = %q", authenticator.cookieHeader)
	}
}

func TestRouterDoesNotRegisterManagementLogoutWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                       config.Config{Host: "127.0.0.1", Port: 3000},
		ManagementAPIAuthMiddleware:  NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{}),
		ManagementLogoutHandler:      NewManagementLogoutHandler(&managementLogoutAuthenticatorStub{}),
		ManagementCurrentUserHandler: NewManagementCurrentUserHandler(&managementCurrentUserAuthenticatorStub{}),
	})

	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/logout", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", rec.Code)
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

type managementCurrentUserAuthenticatorStub struct {
	cookieHeader string
	context      managementauth.Context
	err          error
}

func (s *managementCurrentUserAuthenticatorStub) AuthenticateCookieForCurrentUser(_ context.Context, cookieHeader string) (managementauth.Context, error) {
	s.cookieHeader = cookieHeader
	return s.context, s.err
}

type managementLogoutAuthenticatorStub struct {
	cookieHeader string
	err          error
}

func (s *managementLogoutAuthenticatorStub) LogoutCookie(_ context.Context, cookieHeader string) error {
	s.cookieHeader = cookieHeader
	return s.err
}
