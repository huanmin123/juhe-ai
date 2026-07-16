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

func TestManagementLoginHandlerReturnsCurrentUserAndSessionCookie(t *testing.T) {
	expiresAt := time.Date(2026, 7, 22, 12, 30, 0, 0, time.UTC)
	service := &managementLoginServiceStub{
		result: managementauth.LoginResult{
			Account: managementauth.SystemAccountSummary{
				ID:                 "sys_admin",
				Username:           "admin",
				DisplayName:        "管理员",
				Role:               "admin",
				MustChangePassword: false,
			},
			SessionToken:     "session-token",
			SessionID:        "sess_fixed",
			SessionExpiresAt: expiresAt,
		},
	}
	handler := newManagementLoginHandler(service, config.Config{
		CookieSecure:   true,
		CookieSameSite: "none",
	})

	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/login", strings.NewReader(`{"username":"admin","password":"secret","captchaId":"captcha-id","captchaCode":"ABCD2"}`))
	req.RemoteAddr = "203.0.113.10:12345"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want 200", rec.Code, rec.Body.String())
	}
	if service.input.Username != "admin" || service.input.Password != "secret" ||
		service.input.CaptchaID != "captcha-id" || service.input.CaptchaCode != "ABCD2" ||
		service.input.ClientIP != "203.0.113.10" {
		t.Fatalf("input = %+v", service.input)
	}
	var body struct {
		Data managementCurrentUserResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.ID != "sys_admin" || body.Data.Username != "admin" || body.Data.Role != "admin" {
		t.Fatalf("body = %+v", body)
	}
	if strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("login response leaks password field: %s", rec.Body.String())
	}
	setCookie := rec.Header().Get("Set-Cookie")
	for _, part := range []string{"juhe_ai_session=session-token", "Path=/", "Max-Age=1209600", "HttpOnly", "Secure", "SameSite=None"} {
		if !strings.Contains(setCookie, part) {
			t.Fatalf("Set-Cookie = %q, want contains %q", setCookie, part)
		}
	}
}

func TestManagementLoginHandlerUsesTrustProxyClientIP(t *testing.T) {
	service := &managementLoginServiceStub{
		result: managementauth.LoginResult{
			Account:          managementauth.SystemAccountSummary{ID: "sys_admin", Username: "admin", DisplayName: "管理员", Role: "admin"},
			SessionToken:     "session-token",
			SessionExpiresAt: time.Date(2026, 7, 22, 12, 30, 0, 0, time.UTC),
		},
	}
	handler := newManagementLoginHandler(service, config.Config{TrustProxy: "true"})

	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/login", strings.NewReader(`{"username":"admin","password":"secret","captchaId":"captcha-id","captchaCode":"ABCD2"}`))
	req.RemoteAddr = "10.0.0.10:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.10, 198.51.100.20")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.input.ClientIP != "203.0.113.10" {
		t.Fatalf("clientIP = %q, want trusted forwarded IP", service.input.ClientIP)
	}
}

func TestManagementLoginHandlerAllowsCaptchaFieldsToBeOmittedWhenDisabled(t *testing.T) {
	service := &managementLoginServiceStub{
		result: managementauth.LoginResult{
			Account:          managementauth.SystemAccountSummary{ID: "sys_admin", Username: "admin", DisplayName: "管理员", Role: "admin"},
			SessionToken:     "session-token",
			SessionExpiresAt: time.Date(2026, 7, 22, 12, 30, 0, 0, time.UTC),
		},
	}
	handler := newManagementLoginHandler(service, config.Config{AuthCaptchaDisabled: true})
	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/login", strings.NewReader(`{"username":"admin","password":"secret"}`))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want 200", rec.Code, rec.Body.String())
	}
	if service.input.Username != "admin" || service.input.Password != "secret" || service.input.CaptchaID != "" || service.input.CaptchaCode != "" {
		t.Fatalf("input = %+v", service.input)
	}
}

func TestManagementLoginHandlerValidatesBody(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "unknown field", body: `{"username":"admin","password":"secret","captchaId":"captcha-id","captchaCode":"ABCD2","extra":true}`, want: "登录参数无效"},
		{name: "missing captcha", body: `{"username":"admin","password":"secret","captchaId":"","captchaCode":"ABCD2"}`, want: "登录参数无效"},
		{name: "invalid json", body: `{"username":`, want: "请求体无效"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := newManagementLoginHandler(&managementLoginServiceStub{}, config.Config{})
			req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/login", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s, want 400", rec.Code, rec.Body.String())
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body["message"] != tc.want {
				t.Fatalf("body = %+v, want message %q", body, tc.want)
			}
		})
	}
}

func TestManagementLoginHandlerMapsServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantMsg    string
		wantRetry  string
	}{
		{name: "invalid", err: managementauth.ErrLoginInvalidInput, wantStatus: http.StatusBadRequest, wantMsg: "登录参数无效"},
		{name: "whitespace", err: managementauth.ErrLoginWhitespace, wantStatus: http.StatusBadRequest, wantMsg: "用户名和密码不能包含空格"},
		{name: "captcha", err: managementauth.ErrLoginCaptchaInvalid, wantStatus: http.StatusBadRequest, wantMsg: "验证码错误或已过期"},
		{name: "credentials", err: managementauth.ErrLoginCredentialsInvalid, wantStatus: http.StatusUnauthorized, wantMsg: "账号或密码错误"},
		{name: "limit", err: &managementauth.LoginLimitError{Message: managementauth.LoginGuardUsernameBlockedMessage, RetryAfterSeconds: 9}, wantStatus: http.StatusTooManyRequests, wantMsg: managementauth.LoginGuardUsernameBlockedMessage, wantRetry: "9"},
		{name: "unexpected", err: errors.New("postgres password leaked"), wantStatus: http.StatusInternalServerError, wantMsg: "服务器内部错误"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := newManagementLoginHandler(&managementLoginServiceStub{err: tc.err}, config.Config{})
			req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/login", strings.NewReader(`{"username":"admin","password":"secret","captchaId":"captcha-id","captchaCode":"ABCD2"}`))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, body = %s, want %d", rec.Code, rec.Body.String(), tc.wantStatus)
			}
			if got := rec.Header().Get("Retry-After"); got != tc.wantRetry {
				t.Fatalf("Retry-After = %q, want %q", got, tc.wantRetry)
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body["message"] != tc.wantMsg {
				t.Fatalf("body = %+v, want message %q", body, tc.wantMsg)
			}
		})
	}
}

func TestRouterRegistersManagementLoginWhenEnabled(t *testing.T) {
	service := &managementLoginServiceStub{
		result: managementauth.LoginResult{
			Account:          managementauth.SystemAccountSummary{ID: "sys_admin", Username: "admin", DisplayName: "管理员", Role: "admin"},
			SessionToken:     "session-token",
			SessionExpiresAt: time.Date(2026, 7, 22, 12, 30, 0, 0, time.UTC),
		},
	}
	router := NewRouter(RouterOptions{
		Config:                      config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{}),
		ManagementLoginHandler:      newManagementLoginHandler(service, config.Config{}),
	})

	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/login", strings.NewReader(`{"username":"admin","password":"secret","captchaId":"captcha-id","captchaCode":"ABCD2"}`))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want 200", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestRouterDoesNotRegisterManagementLoginWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                      config.Config{Host: "127.0.0.1", Port: 3000},
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{}),
		ManagementLoginHandler:      newManagementLoginHandler(&managementLoginServiceStub{}, config.Config{}),
	})

	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/login", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", rec.Code)
	}
}

type managementLoginServiceStub struct {
	input  managementauth.LoginInput
	result managementauth.LoginResult
	err    error
}

func (s *managementLoginServiceStub) Login(_ context.Context, input managementauth.LoginInput) (managementauth.LoginResult, error) {
	s.input = input
	return s.result, s.err
}
