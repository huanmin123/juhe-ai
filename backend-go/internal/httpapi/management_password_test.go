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

func TestManagementPasswordChangeHandlerChangesPassword(t *testing.T) {
	createdAt := time.Date(2026, 7, 8, 9, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	lastLoginAt := time.Date(2026, 7, 8, 9, 30, 0, 0, time.UTC)
	authenticator := &managementCurrentUserAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID:    "sys_user",
			Username:           "user",
			DisplayName:        "用户",
			Role:               "user",
			MustChangePassword: true,
			SessionID:          "sess_current",
		},
	}
	service := &managementPasswordChangeServiceStub{
		result: managementauth.PasswordChangeResult{
			Account: managementauth.SystemAccountSummary{
				ID:                     "sys_user",
				Username:               "user",
				DisplayName:            "用户",
				Description:            "普通用户",
				Role:                   "user",
				Status:                 "active",
				MustChangePassword:     false,
				ImageGenerationEnabled: true,
				LastLoginAt:            &lastLoginAt,
				CreatedAt:              createdAt,
				UpdatedAt:              updatedAt,
			},
		},
	}
	handler := newManagementPasswordChangeHandler(authenticator, service)

	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/change-password", strings.NewReader(`{"newPassword":"NewPass123"}`))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if authenticator.touchCookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("touch cookie header = %q", authenticator.touchCookieHeader)
	}
	if authenticator.cookieHeader != "" {
		t.Fatalf("read cookie header = %q, want empty", authenticator.cookieHeader)
	}
	if !service.called || service.input.NewPassword != "NewPass123" || service.input.OldPassword != nil {
		t.Fatalf("service input = %+v", service.input)
	}
	if service.input.AuthContext.SystemAccountID != "sys_user" || !service.input.AuthContext.MustChangePassword {
		t.Fatalf("service auth context = %+v", service.input.AuthContext)
	}
	var raw map[string]any
	if err := json.NewDecoder(strings.NewReader(rec.Body.String())).Decode(&raw); err != nil {
		t.Fatalf("decode raw body: %v", err)
	}
	if strings.Contains(rec.Body.String(), "passwordHash") {
		t.Fatalf("response leaked passwordHash: %s", rec.Body.String())
	}
	var body struct {
		Data managementPasswordChangeResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.ID != "sys_user" ||
		body.Data.Username != "user" ||
		body.Data.Description != "普通用户" ||
		body.Data.Status != "active" ||
		body.Data.MustChangePassword ||
		!body.Data.ImageGenerationEnabled ||
		body.Data.LastLoginAt != lastLoginAt.Format(time.RFC3339Nano) ||
		body.Data.CreatedAt != createdAt.Format(time.RFC3339Nano) ||
		body.Data.UpdatedAt != updatedAt.Format(time.RFC3339Nano) {
		t.Fatalf("response = %+v", body.Data)
	}
}

func TestManagementPasswordChangeHandlerErrors(t *testing.T) {
	oldPassword := "OldPass123"
	tests := []struct {
		name       string
		body       string
		authErr    error
		serviceErr error
		wantStatus int
		wantMsg    string
		wantCalled bool
	}{
		{name: "missing new password", body: `{}`, wantStatus: http.StatusBadRequest, wantMsg: "密码参数无效"},
		{name: "unknown field", body: `{"newPassword":"NewPass123","confirmPassword":"NewPass123"}`, wantStatus: http.StatusBadRequest, wantMsg: "密码参数无效"},
		{name: "new password null", body: `{"newPassword":null}`, wantStatus: http.StatusBadRequest, wantMsg: "密码参数无效"},
		{name: "new password non string", body: `{"newPassword":123}`, wantStatus: http.StatusBadRequest, wantMsg: "密码参数无效"},
		{name: "old password null", body: `{"oldPassword":null,"newPassword":"NewPass123"}`, wantStatus: http.StatusBadRequest, wantMsg: "密码参数无效"},
		{name: "syntax error", body: `{"newPassword":`, wantStatus: http.StatusBadRequest, wantMsg: "请求体无效"},
		{name: "trailing json", body: `{"newPassword":"NewPass123"} true`, wantStatus: http.StatusBadRequest, wantMsg: "请求体无效"},
		{name: "oversized body", body: `{"newPassword":"` + strings.Repeat("x", managementPasswordChangeMaxBodyBytes) + `"}`, wantStatus: http.StatusRequestEntityTooLarge, wantMsg: "请求体过大"},
		{name: "short new password", body: `{"newPassword":"abc"}`, serviceErr: managementauth.ErrPasswordInvalid, wantStatus: http.StatusBadRequest, wantMsg: "密码参数无效", wantCalled: true},
		{name: "empty old password", body: `{"oldPassword":"","newPassword":"NewPass123"}`, serviceErr: managementauth.ErrPasswordInvalid, wantStatus: http.StatusBadRequest, wantMsg: "密码参数无效", wantCalled: true},
		{name: "password whitespace", body: `{"oldPassword":"` + oldPassword + `","newPassword":"bad pass"}`, serviceErr: managementauth.ErrPasswordWhitespace, wantStatus: http.StatusBadRequest, wantMsg: "登录密码不能包含空格", wantCalled: true},
		{name: "old password required", body: `{"newPassword":"NewPass123"}`, serviceErr: managementauth.ErrPasswordOldRequired, wantStatus: http.StatusBadRequest, wantMsg: "请填写当前密码", wantCalled: true},
		{name: "old password wrong", body: `{"oldPassword":"WrongPass","newPassword":"NewPass123"}`, serviceErr: managementauth.ErrPasswordOldIncorrect, wantStatus: http.StatusBadRequest, wantMsg: "当前密码不正确", wantCalled: true},
		{name: "account gone", body: `{"newPassword":"NewPass123"}`, serviceErr: managementauth.ErrPasswordAccountGone, wantStatus: http.StatusNotFound, wantMsg: "系统账户不存在", wantCalled: true},
		{name: "store error", body: `{"newPassword":"NewPass123"}`, serviceErr: errors.New("postgres password leaked"), wantStatus: http.StatusInternalServerError, wantMsg: "服务器内部错误", wantCalled: true},
		{name: "auth error", body: `{"newPassword":"NewPass123"}`, authErr: &managementauth.AuthError{StatusCode: http.StatusUnauthorized, Message: "请先登录"}, wantStatus: http.StatusUnauthorized, wantMsg: "请先登录"},
		{name: "unexpected auth error", body: `{"newPassword":"NewPass123"}`, authErr: errors.New("postgres password leaked"), wantStatus: http.StatusInternalServerError, wantMsg: "服务器内部错误"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authenticator := &managementCurrentUserAuthenticatorStub{
				context: managementauth.Context{
					SystemAccountID: "sys_user",
					Username:        "user",
					Role:            "user",
					SessionID:       "sess_current",
				},
				err: tt.authErr,
			}
			service := &managementPasswordChangeServiceStub{err: tt.serviceErr}
			handler := newManagementPasswordChangeHandler(authenticator, service)
			req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/change-password", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if service.called != tt.wantCalled {
				t.Fatalf("service called = %v, want %v", service.called, tt.wantCalled)
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body["message"] != tt.wantMsg {
				t.Fatalf("body = %+v", body)
			}
		})
	}
}

func TestRouterRegistersManagementPasswordChangeWithoutMustChangeMiddleware(t *testing.T) {
	currentUserAuthenticator := &managementCurrentUserAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID:    "sys_user",
			Username:           "user",
			Role:               "user",
			MustChangePassword: true,
			SessionID:          "sess_current",
		},
	}
	service := &managementPasswordChangeServiceStub{
		result: managementauth.PasswordChangeResult{
			Account: managementauth.SystemAccountSummary{ID: "sys_user", Username: "user", Role: "user", Status: "active"},
		},
	}
	router := NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			err: &managementauth.AuthError{
				StatusCode: http.StatusForbidden,
				Code:       managementauth.ErrorCodeMustChangePassword,
				Message:    "请先修改初始密码",
			},
		}),
		ManagementPasswordChangeHandler: newManagementPasswordChangeHandler(currentUserAuthenticator, service),
	})

	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/change-password", strings.NewReader(`{"newPassword":"NewPass123"}`))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if !service.called {
		t.Fatal("password change service was not called")
	}
	if currentUserAuthenticator.touchCookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("touch cookie header = %q", currentUserAuthenticator.touchCookieHeader)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestRouterDoesNotRegisterManagementPasswordChangeWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                          config.Config{Host: "127.0.0.1", Port: 3000},
		ManagementAPIAuthMiddleware:     NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{}),
		ManagementPasswordChangeHandler: newManagementPasswordChangeHandler(&managementCurrentUserAuthenticatorStub{}, &managementPasswordChangeServiceStub{}),
	})

	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/change-password", strings.NewReader(`{"newPassword":"NewPass123"}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", rec.Code)
	}
}

type managementPasswordChangeServiceStub struct {
	called bool
	input  managementauth.PasswordChangeInput
	result managementauth.PasswordChangeResult
	err    error
}

func (s *managementPasswordChangeServiceStub) ChangePassword(_ context.Context, input managementauth.PasswordChangeInput) (managementauth.PasswordChangeResult, error) {
	s.called = true
	s.input = input
	return s.result, s.err
}
