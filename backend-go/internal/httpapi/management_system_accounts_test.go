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
	handler := newManagementSystemAccountPatchHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			NewLogID: func() string { return "oplog_reset_password" },
		}),
	)
	mustChangePassword := true
	req := managementSystemAccountPatchRequest(
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
	handler := newManagementSystemAccountPatchHandler(service)
	req := managementSystemAccountPatchRequest(
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
			handler := newManagementSystemAccountPatchHandler(service)
			req := managementSystemAccountPatchRequest("/__aisys__/api/system-accounts/sys_user", "sys_user", tt.body)
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
			handler := newManagementSystemAccountPatchHandler(service)
			req := managementSystemAccountPatchRequest("/__aisys__/api/system-accounts/sys_user", "sys_user", `{"password":"NewPass123"}`)
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

func TestManagementSystemAccountStatusUpdateHandlerWritesOperationLog(t *testing.T) {
	queueStub := &operationLogQueueStub{}
	service := &managementSystemAccountOptionServiceStub{
		statusResult: managementsystemaccounts.StatusUpdateResult{
			Before: managementsystemaccounts.Summary{
				ID:          "sys_user",
				Username:    "user",
				DisplayName: "用户",
				Role:        "user",
				Status:      "active",
			},
			Account: managementsystemaccounts.Summary{
				ID:          "sys_user",
				Username:    "user",
				DisplayName: "用户",
				Role:        "user",
				Status:      "disabled",
			},
			RevokedSessionCount: 2,
		},
	}
	handler := newManagementSystemAccountPatchHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			NewLogID: func() string { return "oplog_update_status" },
		}),
	)
	req := managementSystemAccountPatchRequest(
		"/__aisys__/api/system-accounts/sys_user",
		"sys_user",
		`{"status":"disabled"}`,
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
	if !service.statusCalled ||
		service.statusInput.SystemAccountID != "sys_user" ||
		service.statusInput.Status != "disabled" ||
		service.resetCalled {
		t.Fatalf("service inputs: statusCalled=%v statusInput=%+v resetCalled=%v", service.statusCalled, service.statusInput, service.resetCalled)
	}
	var body struct {
		Data managementsystemaccounts.Summary `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.ID != "sys_user" || body.Data.Status != "disabled" {
		t.Fatalf("body = %+v", body.Data)
	}
	if queueStub.calls != 1 || queueStub.taskType != operationlogjob.TaskTypeWrite {
		t.Fatalf("operation log queue calls = %d taskType = %q", queueStub.calls, queueStub.taskType)
	}
	if strings.Contains(string(queueStub.payload), "password") {
		t.Fatal("status update operation log payload must not contain password fields")
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("decode operation log payload: %v", err)
	}
	if logInput.OperationKey != "system_accounts.update" ||
		logInput.ActorSystemAccountID != "sys_super" ||
		logInput.OperationScopeSystemAccountID != "sys_user" ||
		logInput.ResourceID != "sys_user" ||
		len(logInput.Changes) != 1 ||
		logInput.Changes[0].Field != "status" ||
		logInput.Changes[0].Before != "active" ||
		logInput.Changes[0].After != "disabled" {
		t.Fatalf("operation log input = %+v", logInput)
	}
	if len(logInput.Viewers) != 1 ||
		logInput.Viewers[0].SystemAccountID != "sys_user" ||
		logInput.Viewers[0].VisibilityReason != "admin_managed_my_resource" {
		t.Fatalf("operation log viewers = %+v", logInput.Viewers)
	}
}

func TestManagementSystemAccountStatusUpdateHandlerValidatesBody(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "password and status cannot mix", body: `{"password":"NewPass123","status":"disabled"}`},
		{name: "status-only rejects full patch field", body: `{"status":"disabled","displayName":"用户"}`},
		{name: "status-only rejects must change password", body: `{"status":"disabled","mustChangePassword":true}`},
		{name: "status null", body: `{"status":null}`},
		{name: "empty object", body: `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementSystemAccountOptionServiceStub{}
			handler := newManagementSystemAccountPatchHandler(service)
			req := managementSystemAccountPatchRequest("/__aisys__/api/system-accounts/sys_user", "sys_user", tt.body)
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
				SystemAccountID: "sys_super",
				Role:            "super_admin",
			}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
			}
			if service.statusCalled || service.resetCalled || service.imageCalled {
				t.Fatalf("service should not be called, statusCalled=%v resetCalled=%v imageCalled=%v", service.statusCalled, service.resetCalled, service.imageCalled)
			}
		})
	}
}

func TestManagementSystemAccountStatusUpdateHandlerMapsServiceErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantMsg  string
	}{
		{name: "invalid status", err: managementsystemaccounts.ErrStatusUpdateInvalid, wantCode: http.StatusBadRequest, wantMsg: "系统账户参数无效"},
		{name: "not found", err: managementsystemaccounts.ErrSystemAccountNotFound, wantCode: http.StatusNotFound, wantMsg: "系统账户不存在"},
		{name: "last active super admin", err: managementsystemaccounts.ErrActiveSuperAdminRequired, wantCode: http.StatusConflict, wantMsg: "至少保留一个启用的超级管理员"},
		{name: "store error", err: errors.New("postgres password leaked"), wantCode: http.StatusInternalServerError, wantMsg: "服务器内部错误"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementSystemAccountOptionServiceStub{statusErr: tt.err}
			handler := newManagementSystemAccountPatchHandler(service)
			req := managementSystemAccountPatchRequest("/__aisys__/api/system-accounts/sys_user", "sys_user", `{"status":"disabled"}`)
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

func TestManagementSystemAccountImageGenerationUpdateHandlerWritesOperationLog(t *testing.T) {
	queueStub := &operationLogQueueStub{}
	service := &managementSystemAccountOptionServiceStub{
		imageResult: managementsystemaccounts.ImageGenerationUpdateResult{
			Before: managementsystemaccounts.Summary{
				ID:                     "sys_user",
				Username:               "user",
				DisplayName:            "用户",
				Role:                   "user",
				Status:                 "active",
				ImageGenerationEnabled: false,
			},
			Account: managementsystemaccounts.Summary{
				ID:                     "sys_user",
				Username:               "user",
				DisplayName:            "用户",
				Role:                   "user",
				Status:                 "active",
				ImageGenerationEnabled: true,
			},
			Changed: true,
		},
	}
	handler := newManagementSystemAccountPatchHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			NewLogID: func() string { return "oplog_update_image_generation" },
		}),
	)
	req := managementSystemAccountPatchRequest(
		"/__aisys__/api/system-accounts/sys_user",
		"sys_user",
		`{"imageGenerationEnabled":true}`,
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
	if !service.imageCalled ||
		service.imageInput.SystemAccountID != "sys_user" ||
		!service.imageInput.ImageGenerationEnabled ||
		service.profileCalled ||
		service.statusCalled ||
		service.resetCalled {
		t.Fatalf("service inputs: imageCalled=%v imageInput=%+v profileCalled=%v statusCalled=%v resetCalled=%v", service.imageCalled, service.imageInput, service.profileCalled, service.statusCalled, service.resetCalled)
	}
	var body struct {
		Data managementsystemaccounts.Summary `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.ID != "sys_user" || !body.Data.ImageGenerationEnabled {
		t.Fatalf("body = %+v", body.Data)
	}
	if queueStub.calls != 1 || queueStub.taskType != operationlogjob.TaskTypeWrite {
		t.Fatalf("operation log queue calls = %d taskType = %q", queueStub.calls, queueStub.taskType)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("decode operation log payload: %v", err)
	}
	if logInput.OperationKey != "system_accounts.update" ||
		logInput.ActorSystemAccountID != "sys_super" ||
		logInput.OperationScopeSystemAccountID != "sys_user" ||
		logInput.ResourceID != "sys_user" ||
		len(logInput.Changes) != 1 ||
		logInput.Changes[0].Field != "imageGenerationEnabled" ||
		logInput.Changes[0].Before != false ||
		logInput.Changes[0].After != true {
		t.Fatalf("operation log input = %+v", logInput)
	}
}

func TestManagementSystemAccountImageGenerationUpdateHandlerValidatesBody(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "image and profile cannot mix", body: `{"imageGenerationEnabled":true,"displayName":"用户"}`},
		{name: "image and status cannot mix", body: `{"imageGenerationEnabled":true,"status":"active"}`},
		{name: "image null", body: `{"imageGenerationEnabled":null}`},
		{name: "image string", body: `{"imageGenerationEnabled":"true"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementSystemAccountOptionServiceStub{}
			handler := newManagementSystemAccountPatchHandler(service)
			req := managementSystemAccountPatchRequest("/__aisys__/api/system-accounts/sys_user", "sys_user", tt.body)
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
				SystemAccountID: "sys_super",
				Role:            "super_admin",
			}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
			}
			if service.imageCalled || service.profileCalled || service.statusCalled || service.resetCalled {
				t.Fatalf("service should not be called, imageCalled=%v profileCalled=%v statusCalled=%v resetCalled=%v", service.imageCalled, service.profileCalled, service.statusCalled, service.resetCalled)
			}
		})
	}
}

func TestManagementSystemAccountImageGenerationUpdateHandlerMapsServiceErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantMsg  string
	}{
		{name: "invalid", err: managementsystemaccounts.ErrImageGenerationUpdateInvalid, wantCode: http.StatusBadRequest, wantMsg: "系统账户参数无效"},
		{name: "not found", err: managementsystemaccounts.ErrSystemAccountNotFound, wantCode: http.StatusNotFound, wantMsg: "系统账户不存在"},
		{name: "store error", err: errors.New("postgres password leaked"), wantCode: http.StatusInternalServerError, wantMsg: "服务器内部错误"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementSystemAccountOptionServiceStub{imageErr: tt.err}
			handler := newManagementSystemAccountPatchHandler(service)
			req := managementSystemAccountPatchRequest("/__aisys__/api/system-accounts/sys_user", "sys_user", `{"imageGenerationEnabled":true}`)
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

func TestManagementSystemAccountProfileUpdateHandlerWritesOperationLog(t *testing.T) {
	queueStub := &operationLogQueueStub{}
	service := &managementSystemAccountOptionServiceStub{
		profileResult: managementsystemaccounts.ProfileUpdateResult{
			Before: managementsystemaccounts.Summary{
				ID:                 "sys_user",
				Username:           "user",
				DisplayName:        "旧名称",
				Description:        "旧说明",
				Role:               "user",
				Status:             "active",
				MustChangePassword: false,
			},
			Account: managementsystemaccounts.Summary{
				ID:                 "sys_user",
				Username:           "user",
				DisplayName:        "新名称",
				Description:        "新说明",
				Role:               "admin",
				Status:             "active",
				MustChangePassword: false,
			},
			Changed: true,
		},
	}
	handler := newManagementSystemAccountPatchHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			NewLogID: func() string { return "oplog_update_profile" },
		}),
	)
	req := managementSystemAccountPatchRequest(
		"/__aisys__/api/system-accounts/sys_user",
		"sys_user",
		`{"displayName":"新名称","description":"新说明","role":"admin","mustChangePassword":true}`,
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
	if !service.profileCalled ||
		service.profileInput.SystemAccountID != "sys_user" ||
		service.profileInput.DisplayName == nil ||
		*service.profileInput.DisplayName != "新名称" ||
		!service.profileInput.HasDescription ||
		service.profileInput.Description == nil ||
		*service.profileInput.Description != "新说明" ||
		service.profileInput.Role == nil ||
		*service.profileInput.Role != "admin" ||
		service.profileInput.MustChangePassword == nil ||
		!*service.profileInput.MustChangePassword ||
		service.statusCalled ||
		service.resetCalled {
		t.Fatalf("service inputs: profile=%+v statusCalled=%v resetCalled=%v", service.profileInput, service.statusCalled, service.resetCalled)
	}
	if queueStub.calls != 1 || queueStub.taskType != operationlogjob.TaskTypeWrite {
		t.Fatalf("operation log queue calls = %d taskType = %q", queueStub.calls, queueStub.taskType)
	}
	payload := string(queueStub.payload)
	if strings.Contains(payload, "password") || strings.Contains(payload, "imageGenerationEnabled") || strings.Contains(payload, "status") {
		t.Fatalf("profile update operation log payload leaked unsupported fields: %s", payload)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("decode operation log payload: %v", err)
	}
	if logInput.OperationKey != "system_accounts.update" ||
		logInput.ActorSystemAccountID != "sys_super" ||
		logInput.OperationScopeSystemAccountID != "sys_user" ||
		logInput.ResourceID != "sys_user" ||
		len(logInput.Changes) != 3 {
		t.Fatalf("operation log input = %+v", logInput)
	}
	if logInput.Changes[0].Field != "displayName" ||
		logInput.Changes[1].Field != "description" ||
		logInput.Changes[2].Field != "role" {
		t.Fatalf("operation log changes = %+v", logInput.Changes)
	}
}

func TestManagementSystemAccountProfileUpdateHandlerValidatesBody(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "display name null", body: `{"displayName":null}`},
		{name: "description number", body: `{"description":123}`},
		{name: "role null", body: `{"role":null}`},
		{name: "must change password null", body: `{"mustChangePassword":null}`},
		{name: "image generation not in profile slice", body: `{"displayName":"用户","imageGenerationEnabled":true}`},
		{name: "unknown field", body: `{"displayName":"用户","unknown":true}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementSystemAccountOptionServiceStub{}
			handler := newManagementSystemAccountPatchHandler(service)
			req := managementSystemAccountPatchRequest("/__aisys__/api/system-accounts/sys_user", "sys_user", tt.body)
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
				SystemAccountID: "sys_super",
				Role:            "super_admin",
			}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
			}
			if service.profileCalled || service.statusCalled || service.resetCalled || service.imageCalled {
				t.Fatalf("service should not be called, profileCalled=%v statusCalled=%v resetCalled=%v imageCalled=%v", service.profileCalled, service.statusCalled, service.resetCalled, service.imageCalled)
			}
		})
	}
}

func TestManagementSystemAccountProfileUpdateHandlerMapsServiceErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantMsg  string
	}{
		{name: "display whitespace", err: managementsystemaccounts.ErrProfileUpdateWhitespace, wantCode: http.StatusBadRequest, wantMsg: "用户名称不能包含空格"},
		{name: "invalid", err: managementsystemaccounts.ErrProfileUpdateInvalid, wantCode: http.StatusBadRequest, wantMsg: "系统账户参数无效"},
		{name: "duplicate display name", err: managementsystemaccounts.ErrProfileUpdateDisplayNameDup, wantCode: http.StatusConflict, wantMsg: "用户名称已存在"},
		{name: "not found", err: managementsystemaccounts.ErrSystemAccountNotFound, wantCode: http.StatusNotFound, wantMsg: "系统账户不存在"},
		{name: "last active super admin", err: managementsystemaccounts.ErrActiveSuperAdminRequired, wantCode: http.StatusConflict, wantMsg: "至少保留一个启用的超级管理员"},
		{name: "store error", err: errors.New("postgres password leaked"), wantCode: http.StatusInternalServerError, wantMsg: "服务器内部错误"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementSystemAccountOptionServiceStub{profileErr: tt.err}
			handler := newManagementSystemAccountPatchHandler(service)
			req := managementSystemAccountPatchRequest("/__aisys__/api/system-accounts/sys_user", "sys_user", `{"displayName":"用户"}`)
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
		Config:                              config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                              slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementSystemAccountPatchHandler: newManagementSystemAccountPatchHandler(service),
		ManagementAPIAuthMiddleware:         NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware:    NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
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
	listCalled    bool
	listInput     managementsystemaccounts.ListInput
	listResult    managementsystemaccounts.ListResult
	listErr       error
	called        bool
	input         managementsystemaccounts.OptionListInput
	options       []managementsystemaccounts.Option
	err           error
	resetCalled   bool
	resetInput    managementsystemaccounts.PasswordResetInput
	resetResult   managementsystemaccounts.PasswordResetResult
	resetErr      error
	statusCalled  bool
	statusInput   managementsystemaccounts.StatusUpdateInput
	statusResult  managementsystemaccounts.StatusUpdateResult
	statusErr     error
	imageCalled   bool
	imageInput    managementsystemaccounts.ImageGenerationUpdateInput
	imageResult   managementsystemaccounts.ImageGenerationUpdateResult
	imageErr      error
	profileCalled bool
	profileInput  managementsystemaccounts.ProfileUpdateInput
	profileResult managementsystemaccounts.ProfileUpdateResult
	profileErr    error
	createCalled  bool
	createInput   managementsystemaccounts.CreateInput
	createResult  managementsystemaccounts.CreateResult
	createErr     error
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

func (s *managementSystemAccountOptionServiceStub) UpdateStatus(_ context.Context, input managementsystemaccounts.StatusUpdateInput) (managementsystemaccounts.StatusUpdateResult, error) {
	s.statusCalled = true
	s.statusInput = input
	return s.statusResult, s.statusErr
}

func (s *managementSystemAccountOptionServiceStub) UpdateImageGeneration(_ context.Context, input managementsystemaccounts.ImageGenerationUpdateInput) (managementsystemaccounts.ImageGenerationUpdateResult, error) {
	s.imageCalled = true
	s.imageInput = input
	return s.imageResult, s.imageErr
}

func (s *managementSystemAccountOptionServiceStub) UpdateProfile(_ context.Context, input managementsystemaccounts.ProfileUpdateInput) (managementsystemaccounts.ProfileUpdateResult, error) {
	s.profileCalled = true
	s.profileInput = input
	return s.profileResult, s.profileErr
}

func (s *managementSystemAccountOptionServiceStub) Create(_ context.Context, input managementsystemaccounts.CreateInput) (managementsystemaccounts.CreateResult, error) {
	s.createCalled = true
	s.createInput = input
	return s.createResult, s.createErr
}

func managementSystemAccountPatchRequest(target string, systemAccountID string, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPatch, target, strings.NewReader(body))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", systemAccountID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

func TestManagementSystemAccountCreateHandlerWritesOperationLog(t *testing.T) {
	queueStub := &operationLogQueueStub{}
	service := &managementSystemAccountOptionServiceStub{
		createResult: managementsystemaccounts.CreateResult{
			Account: managementsystemaccounts.Summary{
				ID:                     "sys_new",
				Username:               "new_user",
				DisplayName:            "新用户",
				Role:                   "user",
				Status:                 "active",
				MustChangePassword:     true,
				ImageGenerationEnabled: true,
			},
			DefaultGroupIDs:  []string{"grp_1"},
			DefaultAPIKeyIDs: []string{"key_1"},
		},
	}
	handler := newManagementSystemAccountCreateHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			NewLogID: func() string { return "oplog_create_system_account" },
		}),
	)
	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/system-accounts", strings.NewReader(`{"username":"new_user","displayName":"新用户","password":"Secret123","imageGenerationEnabled":true}`))
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_super",
		Username:        "super",
		DisplayName:     "超级管理员",
		Role:            "super_admin",
		SessionID:       "sess_super",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body = %s", rec.Code, rec.Body.String())
	}
	if !service.createCalled || service.createInput.Username != "new_user" || service.createInput.DisplayName != "新用户" || service.createInput.Password != "Secret123" || service.createInput.ImageGenerationEnabled == nil || !*service.createInput.ImageGenerationEnabled {
		t.Fatalf("create input = %+v", service.createInput)
	}
	var body struct {
		Data managementsystemaccounts.Summary `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.ID != "sys_new" || body.Data.Username != "new_user" || !body.Data.ImageGenerationEnabled {
		t.Fatalf("response data = %+v", body.Data)
	}
	if strings.Contains(rec.Body.String(), "Secret123") {
		t.Fatalf("response leaked password: %s", rec.Body.String())
	}
	if queueStub.calls != 1 || queueStub.taskType != operationlogjob.TaskTypeWrite {
		t.Fatalf("operation log queue calls = %d taskType = %q", queueStub.calls, queueStub.taskType)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("decode operation log payload: %v", err)
	}
	if logInput.OperationKey != "system_accounts.create" || logInput.Action != "create" || logInput.ResourceID != "sys_new" || logInput.StatusCode == nil || *logInput.StatusCode != http.StatusCreated {
		t.Fatalf("operation log input = %+v", logInput)
	}
	if len(logInput.Changes) != 6 || logInput.Changes[5].Field != "password" || logInput.Changes[5].After != "已设置" || !logInput.Changes[5].Sensitive {
		t.Fatalf("operation log changes = %+v", logInput.Changes)
	}
	if raw := string(queueStub.payload); strings.Contains(raw, "Secret123") {
		t.Fatalf("operation log payload leaked password: %s", raw)
	}
}

func TestManagementSystemAccountCreateHandlerRejectsNonSuperAdmin(t *testing.T) {
	service := &managementSystemAccountOptionServiceStub{}
	handler := newManagementSystemAccountCreateHandler(service, managementOperationLogOptions{})
	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/system-accounts", strings.NewReader(`{"username":"user","displayName":"用户","password":"Secret123"}`))
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_admin",
		Role:            "admin",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if service.createCalled {
		t.Fatal("service should not be called for non-super admin")
	}
}

func TestManagementSystemAccountCreateHandlerValidatesBody(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantMsg string
	}{
		{name: "missing username", body: `{"displayName":"用户","password":"Secret123"}`},
		{name: "syntax error", body: `{"username":`},
		{name: "trailing json", body: `{"username":"user","displayName":"用户","password":"Secret123"} true`},
		{name: "username surrounding spaces", body: `{"username":" user ","displayName":"用户","password":"Secret123"}`, wantMsg: "用户名不能包含空格"},
		{name: "display name surrounding spaces", body: `{"username":"user","displayName":" 用户 ","password":"Secret123"}`, wantMsg: "用户名称不能包含空格"},
		{name: "password whitespace", body: `{"username":"user","displayName":"用户","password":"Secret 123"}`, wantMsg: "登录密码不能包含空格"},
		{name: "unknown field", body: `{"username":"user","displayName":"用户","password":"Secret123","extra":true}`},
		{name: "bad image type", body: `{"username":"user","displayName":"用户","password":"Secret123","imageGenerationEnabled":"true"}`},
		{name: "image null", body: `{"username":"user","displayName":"用户","password":"Secret123","imageGenerationEnabled":null}`},
		{name: "must change null", body: `{"username":"user","displayName":"用户","password":"Secret123","mustChangePassword":null}`},
		{name: "invalid role", body: `{"username":"user","displayName":"用户","password":"Secret123","role":"super_admin"}`},
		{name: "role null", body: `{"username":"user","displayName":"用户","password":"Secret123","role":null}`},
		{name: "invalid status", body: `{"username":"user","displayName":"用户","password":"Secret123","status":"archived"}`},
		{name: "status null", body: `{"username":"user","displayName":"用户","password":"Secret123","status":null}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementSystemAccountOptionServiceStub{}
			handler := newManagementSystemAccountCreateHandler(service, managementOperationLogOptions{})
			req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/system-accounts", strings.NewReader(tt.body))
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_super", Role: "super_admin"}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
			}
			if tt.wantMsg != "" {
				var body map[string]string
				if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if body["message"] != tt.wantMsg {
					t.Fatalf("message = %q, want %q", body["message"], tt.wantMsg)
				}
			}
			if service.createCalled {
				t.Fatal("service should not be called for invalid body")
			}
		})
	}
}
