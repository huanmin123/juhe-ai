package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestManagementAPIAuthTouchMiddlewareInjectsContext(t *testing.T) {
	authenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
			DisplayName:     "管理员",
			Role:            "admin",
			SessionID:       "sess_admin",
		},
	}
	handler := NewManagementAPIAuthTouchMiddleware(authenticator)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := ManagementAuthContextFromRequest(r); !ok {
			t.Fatal("management auth context missing")
		}
		writeData(w, http.StatusOK, map[string]string{"ok": "true"})
	}))

	req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/auth/me", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if authenticator.touchCookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("touch cookie header = %q", authenticator.touchCookieHeader)
	}
	if authenticator.cookieHeader != "" {
		t.Fatalf("read auth cookie header = %q, want empty", authenticator.cookieHeader)
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
	handler := NewManagementLogoutHandler(authenticator, config.Config{
		CookieSecure:   true,
		CookieSameSite: "none",
	})

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
	for _, part := range []string{"juhe_ai_session=", "Max-Age=0", "Path=/", "HttpOnly", "Secure", "SameSite=None"} {
		if !strings.Contains(setCookie, part) {
			t.Fatalf("Set-Cookie = %q, want contains %q", setCookie, part)
		}
	}
}

func TestManagementLogoutHandlerAllowsMissingCookie(t *testing.T) {
	authenticator := &managementLogoutAuthenticatorStub{}
	handler := NewManagementLogoutHandler(authenticator, config.Config{})

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
	}, config.Config{})

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
		ManagementLogoutHandler:      NewManagementLogoutHandler(authenticator, config.Config{}),
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

func TestRouterRegistersManagementSessionListWithReadAuth(t *testing.T) {
	authenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_user",
			Username:        "user",
			DisplayName:     "用户",
			Role:            "user",
			SessionID:       "sess_current",
		},
	}
	service := &managementSessionServiceStub{
		listResult: managementauth.SessionListResult{
			Items: []managementauth.SessionSummary{
				{
					ID:         "sess_current",
					Current:    true,
					CreatedAt:  time.Date(2026, 7, 9, 8, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
					LastSeenAt: time.Date(2026, 7, 9, 9, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
					ExpiresAt:  time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
				},
			},
			Total:    1,
			Page:     2,
			PageSize: 10,
		},
	}
	router := NewRouter(RouterOptions{
		Config:                       config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware:  NewManagementAPIAuthMiddleware(authenticator),
		ManagementSessionListHandler: newManagementSessionListHandler(service),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/sessions?page=2&pageSize=10", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if authenticator.cookieHeader != "juhe_ai_session=session-token" || authenticator.touchCookieHeader != "" {
		t.Fatalf("auth headers read=%q touch=%q", authenticator.cookieHeader, authenticator.touchCookieHeader)
	}
	if service.listInput.SystemAccountID != "sys_user" ||
		service.listInput.CurrentSessionID != "sess_current" ||
		service.listInput.Page != 2 ||
		service.listInput.PageSize != 10 {
		t.Fatalf("list input = %+v", service.listInput)
	}
	var body struct {
		Data managementauth.SessionListResult `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data.Items) != 1 || !body.Data.Items[0].Current || body.Data.Items[0].ID != "sess_current" {
		t.Fatalf("body = %+v", body.Data)
	}
}

func TestManagementSessionListHandlerMapsInvalidPagination(t *testing.T) {
	service := &managementSessionServiceStub{}
	router := NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_current"},
		}),
		ManagementSessionListHandler: newManagementSessionListHandler(service),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/sessions?page=abc", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if service.listCalls != 0 {
		t.Fatalf("list calls = %d, want 0", service.listCalls)
	}
}

func TestRouterRegistersManagementSessionRevokeWithTouchAndClearsCurrentCookie(t *testing.T) {
	authenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_user",
			Username:        "user",
			DisplayName:     "用户",
			Role:            "user",
			SessionID:       "sess_current",
		},
	}
	service := &managementSessionServiceStub{
		revokeResult: managementauth.SessionRevokeResult{ID: "sess_current", Revoked: true, Current: true},
	}
	router := NewRouter(RouterOptions{
		Config:                           config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true, CookieSecure: true, CookieSameSite: "none"},
		ManagementAPIAuthMiddleware:      NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{}),
		ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementSessionRevokeHandler:   newManagementSessionRevokeHandler(service, config.Config{CookieSecure: true, CookieSameSite: "none"}),
	})

	req := httptest.NewRequest(http.MethodDelete, "/__aisys__/api/auth/sessions/sess_current", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if authenticator.touchCookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("touch cookie header = %q", authenticator.touchCookieHeader)
	}
	if service.revokeInput.SystemAccountID != "sys_user" ||
		service.revokeInput.CurrentSessionID != "sess_current" ||
		service.revokeInput.SessionID != "sess_current" {
		t.Fatalf("revoke input = %+v", service.revokeInput)
	}
	setCookie := rec.Header().Get("Set-Cookie")
	for _, part := range []string{"juhe_ai_session=", "Max-Age=0", "Path=/", "HttpOnly", "Secure", "SameSite=None"} {
		if !strings.Contains(setCookie, part) {
			t.Fatalf("Set-Cookie = %q, want contains %q", setCookie, part)
		}
	}
}

func TestManagementSessionRevokeHandlerMapsNotFound(t *testing.T) {
	service := &managementSessionServiceStub{revokeErr: managementauth.ErrSessionNotFound}
	router := NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_current"},
		}),
		ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_current"},
		}),
		ManagementSessionRevokeHandler: newManagementSessionRevokeHandler(service, config.Config{}),
	})

	req := httptest.NewRequest(http.MethodDelete, "/__aisys__/api/auth/sessions/sess_missing", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["message"] != "会话不存在" {
		t.Fatalf("body = %+v", body)
	}
}

func TestRouterUsesTouchMiddlewareOnlyForManagementWriteRoutes(t *testing.T) {
	readAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_read"},
	}
	touchAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_touch"},
	}
	proxyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authContext, ok := ManagementAuthContextFromRequest(r); !ok || authContext.SessionID != "sess_read" {
			t.Fatalf("read route auth context = %+v ok=%v", authContext, ok)
		}
		writeData(w, http.StatusOK, map[string]string{"ok": "read"})
	})
	profileHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authContext, ok := ManagementAuthContextFromRequest(r); !ok || authContext.SessionID != "sess_touch" {
			t.Fatalf("write route auth context = %+v ok=%v", authContext, ok)
		}
		writeData(w, http.StatusOK, map[string]string{"ok": "write"})
	})
	router := NewRouter(RouterOptions{
		Config:                           config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware:      NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
		ManagementProxyOptionsHandler:    proxyHandler,
		ManagementProfileUpdateHandler:   profileHandler,
	})

	readReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options", nil)
	readReq.Header.Set("Cookie", "juhe_ai_session=session-token")
	readRec := httptest.NewRecorder()
	router.ServeHTTP(readRec, readReq)
	if readRec.Code != http.StatusOK {
		t.Fatalf("read route status = %d, body = %s", readRec.Code, readRec.Body.String())
	}
	if readAuthenticator.cookieHeader == "" || touchAuthenticator.touchCookieHeader != "" {
		t.Fatalf("read route auth headers read=%q touch=%q", readAuthenticator.cookieHeader, touchAuthenticator.touchCookieHeader)
	}

	writeReq := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/auth/me", strings.NewReader(`{}`))
	writeReq.Header.Set("Cookie", "juhe_ai_session=session-token")
	writeRec := httptest.NewRecorder()
	router.ServeHTTP(writeRec, writeReq)
	if writeRec.Code != http.StatusOK {
		t.Fatalf("write route status = %d, body = %s", writeRec.Code, writeRec.Body.String())
	}
	if touchAuthenticator.touchCookieHeader == "" {
		t.Fatal("write route did not use touch middleware")
	}
}

func TestRouterRequiresTouchMiddlewareForManagementWriteRoutes(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("NewRouter() did not panic for management write route without touch middleware")
		}
	}()

	_ = NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
		ManagementProfileUpdateHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	})
}

func TestRouterDoesNotRegisterManagementLogoutWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                       config.Config{Host: "127.0.0.1", Port: 3000},
		ManagementAPIAuthMiddleware:  NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{}),
		ManagementLogoutHandler:      NewManagementLogoutHandler(&managementLogoutAuthenticatorStub{}, config.Config{}),
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
	cookieHeader      string
	touchCookieHeader string
	context           managementauth.Context
	err               error
}

func (s *managementAPIAuthenticatorStub) AuthenticateCookie(_ context.Context, cookieHeader string) (managementauth.Context, error) {
	s.cookieHeader = cookieHeader
	return s.context, s.err
}

func (s *managementAPIAuthenticatorStub) AuthenticateCookieAndTouch(_ context.Context, cookieHeader string) (managementauth.Context, error) {
	s.touchCookieHeader = cookieHeader
	return s.context, s.err
}

type managementCurrentUserAuthenticatorStub struct {
	cookieHeader      string
	touchCookieHeader string
	context           managementauth.Context
	err               error
}

func (s *managementCurrentUserAuthenticatorStub) AuthenticateCookieForCurrentUser(_ context.Context, cookieHeader string) (managementauth.Context, error) {
	s.cookieHeader = cookieHeader
	return s.context, s.err
}

func (s *managementCurrentUserAuthenticatorStub) AuthenticateCookieForCurrentUserAndTouch(_ context.Context, cookieHeader string) (managementauth.Context, error) {
	s.touchCookieHeader = cookieHeader
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

type managementSessionServiceStub struct {
	listCalls    int
	listInput    managementauth.SessionListInput
	listResult   managementauth.SessionListResult
	listErr      error
	revokeCalls  int
	revokeInput  managementauth.SessionRevokeInput
	revokeResult managementauth.SessionRevokeResult
	revokeErr    error
}

func (s *managementSessionServiceStub) List(_ context.Context, input managementauth.SessionListInput) (managementauth.SessionListResult, error) {
	s.listCalls++
	s.listInput = input
	return s.listResult, s.listErr
}

func (s *managementSessionServiceStub) Revoke(_ context.Context, input managementauth.SessionRevokeInput) (managementauth.SessionRevokeResult, error) {
	s.revokeCalls++
	s.revokeInput = input
	return s.revokeResult, s.revokeErr
}
