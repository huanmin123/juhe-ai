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
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

func TestManagementProfileUpdateHandlerUpdatesCurrentUserAndEnqueuesOperationLog(t *testing.T) {
	createdAt := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	queueStub := &operationLogQueueStub{}
	service := &managementProfileUpdateServiceStub{
		result: managementauth.ProfileUpdateResult{
			Before: managementauth.CurrentUserProfile{
				ID:          "sys_user",
				Username:    "user",
				DisplayName: "旧名称",
				Role:        "user",
			},
			Account: managementauth.CurrentUserProfile{
				ID:          "sys_user",
				Username:    "user",
				DisplayName: "新名称",
				Role:        "user",
			},
			Changed: true,
		},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_user",
			Username:        "user",
			DisplayName:     "旧名称",
			Role:            "user",
			SessionID:       "sess_user",
		},
	})(newManagementProfileUpdateHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			Now:      func() time.Time { return createdAt },
			NewLogID: func() string { return "oplog_profile" },
		}),
	))

	req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/auth/me", strings.NewReader(`{"displayName":"新名称"}`))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	req.RemoteAddr = "127.0.0.1:12345"
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_profile"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if service.input.DisplayName != "新名称" || service.input.AuthContext.SystemAccountID != "sys_user" {
		t.Fatalf("service input = %+v", service.input)
	}
	var body struct {
		Data managementCurrentUserResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.ID != "sys_user" || body.Data.DisplayName != "新名称" || body.Data.MustChangePassword {
		t.Fatalf("response = %+v", body.Data)
	}
	if queueStub.calls != 1 {
		t.Fatalf("queue calls = %d, want 1", queueStub.calls)
	}
	if queueStub.taskType != operationlogjob.TaskTypeWrite {
		t.Fatalf("task type = %q, want %q", queueStub.taskType, operationlogjob.TaskTypeWrite)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("DecodeWriteTaskPayload() error = %v", err)
	}
	if logInput.ID != "oplog_profile" ||
		logInput.TraceID != "req_profile" ||
		logInput.ActorSystemAccountID != "sys_user" ||
		logInput.ActorDisplayName != "旧名称" ||
		logInput.OperationScopeSystemAccountID != "sys_user" ||
		logInput.Mode != "self" ||
		logInput.Module != "system_accounts" ||
		logInput.Action != "update" ||
		logInput.OperationKey != "auth.update_profile" ||
		logInput.ResourceType != "system_account" ||
		logInput.ResourceID != "sys_user" ||
		logInput.ResourceName != "新名称" ||
		logInput.Summary != "修改显示名称：新名称" ||
		logInput.Method != http.MethodPatch ||
		logInput.Path != "/__aisys__/api/auth/me" ||
		logInput.ClientIP != "127.0.0.1" ||
		!logInput.CreatedAt.Equal(createdAt) {
		t.Fatalf("operation log input = %+v", logInput)
	}
	if logInput.StatusCode == nil || *logInput.StatusCode != http.StatusOK {
		t.Fatalf("status code = %+v, want 200", logInput.StatusCode)
	}
	if len(logInput.Changes) != 1 ||
		logInput.Changes[0].Field != "displayName" ||
		logInput.Changes[0].Label != "显示名称" ||
		logInput.Changes[0].Before != "旧名称" ||
		logInput.Changes[0].After != "新名称" {
		t.Fatalf("changes = %+v", logInput.Changes)
	}
	if len(logInput.Viewers) != 1 ||
		logInput.Viewers[0].SystemAccountID != "sys_user" ||
		logInput.Viewers[0].VisibilityReason != "resource_owner" {
		t.Fatalf("viewers = %+v", logInput.Viewers)
	}
}

func TestManagementProfileUpdateHandlerSkipsOperationLogForNoop(t *testing.T) {
	queueStub := &operationLogQueueStub{}
	service := &managementProfileUpdateServiceStub{
		result: managementauth.ProfileUpdateResult{
			Account: managementauth.CurrentUserProfile{ID: "sys_user", Username: "user", DisplayName: "当前名称", Role: "user"},
		},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", DisplayName: "当前名称", Role: "user"},
	})(newManagementProfileUpdateHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{Client: queueStub}),
	))

	req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/auth/me", strings.NewReader(`{"displayName":"当前名称"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if queueStub.calls != 0 {
		t.Fatalf("queue calls = %d, want 0 for no-op", queueStub.calls)
	}
}

func TestManagementProfileUpdateHandlerKeepsSuccessWhenOperationLogQueueFails(t *testing.T) {
	queueStub := &operationLogQueueStub{err: errors.New("redis down")}
	service := &managementProfileUpdateServiceStub{
		result: managementauth.ProfileUpdateResult{
			Before:  managementauth.CurrentUserProfile{ID: "sys_user", DisplayName: "旧名称", Role: "user"},
			Account: managementauth.CurrentUserProfile{ID: "sys_user", Username: "user", DisplayName: "新名称", Role: "user"},
			Changed: true,
		},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", DisplayName: "旧名称", Role: "user"},
	})(newManagementProfileUpdateHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{Client: queueStub}),
	))

	req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/auth/me", strings.NewReader(`{"displayName":"新名称"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if queueStub.calls != 1 {
		t.Fatalf("queue calls = %d, want 1", queueStub.calls)
	}
}

func TestManagementProfileUpdateHandlerErrors(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		serviceErr error
		wantStatus int
		wantMsg    string
		wantCalled bool
	}{
		{name: "missing field", body: `{}`, wantStatus: http.StatusBadRequest, wantMsg: "用户资料参数无效"},
		{name: "unknown field", body: `{"displayName":"新名称","role":"admin"}`, wantStatus: http.StatusBadRequest, wantMsg: "用户资料参数无效"},
		{name: "non string", body: `{"displayName":123}`, wantStatus: http.StatusBadRequest, wantMsg: "用户资料参数无效"},
		{name: "syntax error", body: `{"displayName":`, wantStatus: http.StatusBadRequest, wantMsg: "请求体无效"},
		{name: "trailing json", body: `{"displayName":"新名称"} true`, wantStatus: http.StatusBadRequest, wantMsg: "请求体无效"},
		{name: "empty display name", body: `{"displayName":""}`, serviceErr: managementauth.ErrProfileDisplayNameInvalid, wantStatus: http.StatusBadRequest, wantMsg: "用户资料参数无效", wantCalled: true},
		{name: "whitespace display name", body: `{"displayName":"bad user"}`, serviceErr: managementauth.ErrProfileDisplayNameWhitespace, wantStatus: http.StatusBadRequest, wantMsg: "显示名称不能包含空格", wantCalled: true},
		{name: "not found", body: `{"displayName":"新名称"}`, serviceErr: managementauth.ErrProfileNotFound, wantStatus: http.StatusNotFound, wantMsg: "系统账户不存在", wantCalled: true},
		{name: "duplicate", body: `{"displayName":"新名称"}`, serviceErr: managementauth.ErrProfileDisplayNameExists, wantStatus: http.StatusConflict, wantMsg: "用户名称已存在", wantCalled: true},
		{name: "store error", body: `{"displayName":"新名称"}`, serviceErr: errors.New("postgres password leaked"), wantStatus: http.StatusInternalServerError, wantMsg: "服务器内部错误", wantCalled: true},
		{name: "oversized body", body: `{"displayName":"` + strings.Repeat("x", 1<<20) + `"}`, wantStatus: http.StatusRequestEntityTooLarge, wantMsg: "请求体过大"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementProfileUpdateServiceStub{err: tt.serviceErr}
			handler := newManagementProfileUpdateHandler(service)
			req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/auth/me", strings.NewReader(tt.body))
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_user", Role: "user"}))
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

func TestRouterRegistersManagementProfileUpdateWhenEnabled(t *testing.T) {
	readAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_user",
			Username:        "user",
			DisplayName:     "旧名称",
			Role:            "user",
			SessionID:       "sess_user",
		},
	}
	touchAuthenticator := &managementAPIAuthenticatorStub{context: readAuthenticator.context}
	service := &managementProfileUpdateServiceStub{
		result: managementauth.ProfileUpdateResult{
			Account: managementauth.CurrentUserProfile{ID: "sys_user", Username: "user", DisplayName: "新名称", Role: "user"},
		},
	}
	router := NewRouter(RouterOptions{
		Config:                           config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware:      NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
		ManagementProfileUpdateHandler:   newManagementProfileUpdateHandler(service),
	})

	req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/auth/me", strings.NewReader(`{"displayName":"新名称"}`))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if touchAuthenticator.touchCookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("touch cookie header = %q", touchAuthenticator.touchCookieHeader)
	}
	if readAuthenticator.cookieHeader != "" {
		t.Fatalf("read cookie header = %q, want empty", readAuthenticator.cookieHeader)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestRouterBlocksManagementProfileUpdateForMustChangePassword(t *testing.T) {
	service := &managementProfileUpdateServiceStub{}
	touchAuthenticator := &managementAPIAuthenticatorStub{
		err: &managementauth.AuthError{
			StatusCode: http.StatusForbidden,
			Code:       managementauth.ErrorCodeMustChangePassword,
			Message:    "请先修改初始密码",
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
		ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
		ManagementProfileUpdateHandler:   newManagementProfileUpdateHandler(service),
	})

	req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/auth/me", strings.NewReader(`{"displayName":"新名称"}`))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	if service.called {
		t.Fatal("profile update service should not be called when middleware blocks must-change user")
	}
	if touchAuthenticator.touchCookieHeader == "" {
		t.Fatal("profile update must use touch middleware before must-change 403")
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["code"] != managementauth.ErrorCodeMustChangePassword {
		t.Fatalf("body = %+v", body)
	}
}

func TestRouterDoesNotRegisterManagementProfileUpdateWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                         config.Config{Host: "127.0.0.1", Port: 3000},
		ManagementAPIAuthMiddleware:    NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{}),
		ManagementProfileUpdateHandler: newManagementProfileUpdateHandler(&managementProfileUpdateServiceStub{}),
	})

	req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/auth/me", strings.NewReader(`{"displayName":"新名称"}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", rec.Code)
	}
}

type managementProfileUpdateServiceStub struct {
	called bool
	input  managementauth.ProfileUpdateInput
	result managementauth.ProfileUpdateResult
	err    error
}

func (s *managementProfileUpdateServiceStub) UpdateProfile(_ context.Context, input managementauth.ProfileUpdateInput) (managementauth.ProfileUpdateResult, error) {
	s.called = true
	s.input = input
	return s.result, s.err
}
