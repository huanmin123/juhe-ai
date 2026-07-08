package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/config"
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementsystemaccounts"
)

func TestManagementSystemAccountsHandlerRequiresAdminAndParsesQuery(t *testing.T) {
	service := &managementSystemAccountOptionServiceStub{
		listResult: managementsystemaccounts.ListResult{
			Items: []managementsystemaccounts.Summary{{
				ID:          "sys_user",
				Username:    "user",
				DisplayName: "用户",
				Role:        "user",
				Status:      "active",
				CreatedAt:   "2026-07-08T10:00:00Z",
				UpdatedAt:   "2026-07-08T10:00:00Z",
			}},
			Page:     3,
			PageSize: 25,
			Total:    51,
			HasMore:  true,
		},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementSystemAccountsHandler(service))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/system-accounts?page=3&pageSize=25&keyword=%20%E7%94%A8%E6%88%B7%20", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.listInput.Keyword != "用户" || service.listInput.Page != 3 || service.listInput.PageSize != 25 {
		t.Fatalf("service list input = %+v", service.listInput)
	}
	var body struct {
		Data managementsystemaccounts.ListResult `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data.Items) != 1 || body.Data.Items[0].ID != "sys_user" || body.Data.Total != 51 || !body.Data.HasMore {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementSystemAccountsHandlerRejectsOrdinaryUser(t *testing.T) {
	service := &managementSystemAccountOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementSystemAccountsHandler(service))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/system-accounts", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if service.listCalled {
		t.Fatal("service should not be called for ordinary user")
	}
}

func TestManagementSystemAccountsHandlerRedactsStoreErrors(t *testing.T) {
	handler := newManagementSystemAccountsHandler(&managementSystemAccountOptionServiceStub{listErr: errors.New("postgres password leaked")})
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/system-accounts", nil)
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

func TestManagementSystemAccountPasswordResetHandlerRequiresSuperAdminAndWritesSafeOperationLog(t *testing.T) {
	queueStub := &operationLogQueueStub{}
	service := &managementSystemAccountOptionServiceStub{
		resetResult: managementsystemaccounts.PasswordResetResult{
			Before: managementsystemaccounts.Summary{
				ID:                 "sys_user",
				Username:           "user",
				DisplayName:        "用户",
				Role:               "user",
				Status:             "active",
				MustChangePassword: false,
			},
			Account: managementsystemaccounts.Summary{
				ID:                 "sys_user",
				Username:           "user",
				DisplayName:        "用户",
				Role:               "user",
				Status:             "active",
				MustChangePassword: true,
			},
			RevokedSessionCount: 2,
		},
	}
	handler := newManagementSystemAccountPasswordResetHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			NewLogID: func() string { return "oplog_reset_password" },
		}),
	)
	mustChangePassword := true
	req := managementSystemAccountPasswordResetRequest(
		"/__aisys__/api/system-accounts/sys_user",
		"sys_user",
		`{"password":"NewPass123","mustChangePassword":true}`,
	)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_super",
		Username:        "super",
		DisplayName:     "超级管理员",
		Role:            "super_admin",
		SessionID:       "sess_super",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	if !service.resetCalled ||
		service.resetInput.SystemAccountID != "sys_user" ||
		service.resetInput.Password != "NewPass123" ||
		service.resetInput.MustChangePassword == nil ||
		*service.resetInput.MustChangePassword != mustChangePassword {
		t.Fatalf("reset input = %+v", service.resetInput)
	}
	var body struct {
		Data managementsystemaccounts.Summary `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.ID != "sys_user" || !body.Data.MustChangePassword {
		t.Fatalf("body = %+v", body.Data)
	}
	if queueStub.calls != 1 || queueStub.taskType != operationlogjob.TaskTypeWrite {
		t.Fatalf("operation log queue calls = %d taskType = %q", queueStub.calls, queueStub.taskType)
	}
	if strings.Contains(string(queueStub.payload), "NewPass123") {
		t.Fatal("operation log payload must not contain raw password")
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("decode operation log payload: %v", err)
	}
	if logInput.OperationKey != "system_accounts.reset_password" ||
		logInput.ActorSystemAccountID != "sys_super" ||
		logInput.OperationScopeSystemAccountID != "sys_user" ||
		logInput.ResourceID != "sys_user" ||
		len(logInput.Changes) == 0 ||
		logInput.Changes[0].Field != "password" ||
		!logInput.Changes[0].Sensitive ||
		logInput.Changes[0].After != "已重置" {
		t.Fatalf("operation log input = %+v", logInput)
	}
	if len(logInput.Viewers) != 1 ||
		logInput.Viewers[0].SystemAccountID != "sys_user" ||
		logInput.Viewers[0].VisibilityReason != "admin_managed_my_resource" {
		t.Fatalf("operation log viewers = %+v", logInput.Viewers)
	}
}

func TestManagementSystemAccountPasswordResetHandlerRejectsNonSuperAdmin(t *testing.T) {
	service := &managementSystemAccountOptionServiceStub{}
	handler := newManagementSystemAccountPasswordResetHandler(service)
	req := managementSystemAccountPasswordResetRequest(
		"/__aisys__/api/system-accounts/sys_user",
		"sys_user",
		`{"password":"NewPass123"}`,
	)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_admin",
		Role:            "admin",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if service.resetCalled {
		t.Fatal("service should not be called for non-super admin")
	}
}

func TestManagementSystemAccountPasswordResetHandlerValidatesBody(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		err      error
		wantCode int
		wantMsg  string
	}{
		{name: "username cannot change", body: `{"username":"new","password":"NewPass123"}`, wantCode: http.StatusBadRequest, wantMsg: "用户账户创建后不能修改"},
		{name: "unsupported field", body: `{"password":"NewPass123","status":"disabled"}`, wantCode: http.StatusBadRequest, wantMsg: "系统账户参数无效"},
		{name: "must change password null", body: `{"password":"NewPass123","mustChangePassword":null}`, wantCode: http.StatusBadRequest, wantMsg: "系统账户参数无效"},
		{name: "must change password string", body: `{"password":"NewPass123","mustChangePassword":"false"}`, wantCode: http.StatusBadRequest, wantMsg: "系统账户参数无效"},
		{name: "password whitespace", body: `{"password":"New Pass123"}`, err: managementsystemaccounts.ErrPasswordResetWhitespace, wantCode: http.StatusBadRequest, wantMsg: "登录密码不能包含空格"},
		{name: "short password", body: `{"password":"abc"}`, err: managementsystemaccounts.ErrPasswordResetInvalid, wantCode: http.StatusBadRequest, wantMsg: "系统账户参数无效"},
		{name: "trailing json", body: `{"password":"NewPass123"} {}`, wantCode: http.StatusBadRequest, wantMsg: "请求体无效"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementSystemAccountOptionServiceStub{resetErr: tt.err}
			handler := newManagementSystemAccountPasswordResetHandler(service)
			req := managementSystemAccountPasswordResetRequest("/__aisys__/api/system-accounts/sys_user", "sys_user", tt.body)
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
				SystemAccountID: "sys_super",
				Role:            "super_admin",
			}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tt.wantCode, rec.Body.String())
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body["message"] != tt.wantMsg {
				t.Fatalf("message = %q, want %q", body["message"], tt.wantMsg)
			}
		})
	}
}

func TestManagementSystemAccountPasswordResetHandlerMapsServiceErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantMsg  string
	}{
		{name: "not found", err: managementsystemaccounts.ErrSystemAccountNotFound, wantCode: http.StatusNotFound, wantMsg: "系统账户不存在"},
		{name: "store error", err: errors.New("postgres password leaked"), wantCode: http.StatusInternalServerError, wantMsg: "服务器内部错误"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementSystemAccountOptionServiceStub{resetErr: tt.err}
			handler := newManagementSystemAccountPasswordResetHandler(service)
			req := managementSystemAccountPasswordResetRequest("/__aisys__/api/system-accounts/sys_user", "sys_user", `{"password":"NewPass123"}`)
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
				SystemAccountID: "sys_super",
				Role:            "super_admin",
			}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body["message"] != tt.wantMsg {
				t.Fatalf("message = %q, want %q", body["message"], tt.wantMsg)
			}
		})
	}
}

func TestRouterRegistersW2ManagementSystemAccountOptions(t *testing.T) {
	service := &managementSystemAccountOptionServiceStub{
		listResult: managementsystemaccounts.ListResult{
			Items: []managementsystemaccounts.Summary{{ID: "sys_user", Username: "user", DisplayName: "用户", Role: "user", Status: "active"}},
		},
		options: []managementsystemaccounts.Option{{ID: "sys_user", Username: "user", DisplayName: "用户", Status: "active"}},
	}
	router := NewRouter(RouterOptions{
		Config:                                config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                                slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementSystemAccountsHandler:       newManagementSystemAccountsHandler(service),
		ManagementSystemAccountOptionsHandler: newManagementSystemAccountOptionsHandler(service),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/system-accounts?page=1&pageSize=20", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("list Cache-Control = %q, want no-store", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/__aisys__/api/system-accounts/options?limit=50", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestRouterRegistersW3ManagementSystemAccountPasswordResetWithTouchAuth(t *testing.T) {
	service := &managementSystemAccountOptionServiceStub{
		resetResult: managementsystemaccounts.PasswordResetResult{
			Account: managementsystemaccounts.Summary{ID: "sys_user", Username: "user", DisplayName: "用户", Role: "user", Status: "active"},
		},
	}
	readAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_super", Username: "super", Role: "super_admin", SessionID: "sess_super"},
	}
	touchAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_super", Username: "super", Role: "super_admin", SessionID: "sess_super"},
	}
	router := NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger: slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementSystemAccountPasswordResetHandler: newManagementSystemAccountPasswordResetHandler(service),
		ManagementAPIAuthMiddleware:                 NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware:            NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
	})

	req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/system-accounts/sys_user", strings.NewReader(`{"password":"NewPass123"}`))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	if touchAuthenticator.touchCookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("touch cookie header = %q", touchAuthenticator.touchCookieHeader)
	}
	if readAuthenticator.cookieHeader != "" {
		t.Fatalf("read auth cookie header = %q, want empty", readAuthenticator.cookieHeader)
	}
}

func TestRouterDoesNotRegisterW2ManagementSystemAccountOptionsWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                                config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                                slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementSystemAccountsHandler:       newManagementSystemAccountsHandler(&managementSystemAccountOptionServiceStub{}),
		ManagementSystemAccountOptionsHandler: newManagementSystemAccountOptionsHandler(&managementSystemAccountOptionServiceStub{}),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/system-accounts", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("list status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/__aisys__/api/system-accounts/options", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPatch, "/__aisys__/api/system-accounts/sys_user", strings.NewReader(`{"password":"NewPass123"}`))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("reset status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", rec.Code)
	}
}

type managementSystemAccountOptionServiceStub struct {
	listCalled  bool
	listInput   managementsystemaccounts.ListInput
	listResult  managementsystemaccounts.ListResult
	listErr     error
	called      bool
	input       managementsystemaccounts.OptionListInput
	options     []managementsystemaccounts.Option
	err         error
	resetCalled bool
	resetInput  managementsystemaccounts.PasswordResetInput
	resetResult managementsystemaccounts.PasswordResetResult
	resetErr    error
}

func (s *managementSystemAccountOptionServiceStub) List(_ *http.Request, input managementsystemaccounts.ListInput) (managementsystemaccounts.ListResult, error) {
	s.listCalled = true
	s.listInput = input
	return s.listResult, s.listErr
}

func (s *managementSystemAccountOptionServiceStub) Options(_ *http.Request, input managementsystemaccounts.OptionListInput) ([]managementsystemaccounts.Option, error) {
	s.called = true
	s.input = input
	return s.options, s.err
}

func (s *managementSystemAccountOptionServiceStub) ResetPassword(_ context.Context, input managementsystemaccounts.PasswordResetInput) (managementsystemaccounts.PasswordResetResult, error) {
	s.resetCalled = true
	s.resetInput = input
	return s.resetResult, s.resetErr
}

func managementSystemAccountPasswordResetRequest(target string, systemAccountID string, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPatch, target, strings.NewReader(body))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", systemAccountID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}
