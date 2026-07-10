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
	"time"

	"juhe-ai/backend-go/internal/config"
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementgroups"
)

func TestManagementGroupCreateHandlerCreatesTargetedGroupAndOperationLog(t *testing.T) {
	createdAt := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	queueStub := &operationLogQueueStub{}
	service := &managementGroupCreateServiceStub{
		result: managementgroups.CreateResult{
			ID:              "grp_created",
			SystemAccountID: "sys_user",
			Name:            "高并发分组",
			ProviderCode:    "openai",
			Enabled:         true,
			GroupType:       "high_concurrency",
			AccountIDs:      []string{},
		},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
			DisplayName:     "管理员",
			Role:            "admin",
			SessionID:       "sess_admin",
		},
	})(newManagementGroupCreateHandler(
		service,
		managementGroupScopeAdmin,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			Now:      func() time.Time { return createdAt },
			NewLogID: func() string { return "oplog_group_create" },
		}),
	))

	req := httptest.NewRequest(
		http.MethodPost,
		"/__aisys__/api/groups?systemAccountId=sys_user",
		strings.NewReader(`{
			"name":" 高并发分组 ",
			"providerCode":" openai ",
			"description":" 说明 ",
			"enabled":true,
			"groupType":"high_concurrency",
			"schedulingPolicy":{
				"defaultSoftConcurrency":25,
				"maxQueueWaitMs":90000,
				"clientIpConcurrencyLimit":8,
				"clientIpConcurrencyOverflowMode":"queue",
				"imageLaneMaxConcurrency":3
			}
		}`),
	)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_group_create"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
	if !service.called ||
		service.input.SystemAccountID != "sys_user" ||
		!service.input.IncludeSystemAccountFields ||
		service.input.Name != "高并发分组" ||
		service.input.ProviderCode != "openai" ||
		service.input.Description == nil ||
		*service.input.Description != "说明" ||
		service.input.SchedulingPolicy == nil ||
		service.input.SchedulingPolicy.DefaultSoftConcurrency == nil ||
		*service.input.SchedulingPolicy.DefaultSoftConcurrency != 25 {
		t.Fatalf("create input = %+v called=%v", service.input, service.called)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("decode operation log: %v", err)
	}
	if logInput.ID != "oplog_group_create" ||
		logInput.TraceID != "req_group_create" ||
		logInput.ActorRole != "admin" ||
		logInput.OperationScopeSystemAccountID != "sys_user" ||
		logInput.Mode != "admin" ||
		logInput.OperationKey != "groups.create" ||
		logInput.ResourceType != "group" ||
		logInput.ResourceID != "grp_created" ||
		logInput.ResourceName != "高并发分组" ||
		logInput.VisibilityScope != "targeted" ||
		logInput.Path != "/__aisys__/api/groups/" ||
		len(logInput.Changes) != 4 ||
		len(logInput.Viewers) != 1 ||
		logInput.Viewers[0].SystemAccountID != "sys_user" ||
		logInput.Viewers[0].VisibilityReason != "resource_owner" ||
		!logInput.CreatedAt.Equal(createdAt) {
		t.Fatalf("operation log = %+v", logInput)
	}
}

func TestManagementMyGroupCreateHandlerForcesSelfScope(t *testing.T) {
	queueStub := &operationLogQueueStub{}
	service := &managementGroupCreateServiceStub{
		result: managementgroups.CreateResult{
			ID:           "grp_self",
			Name:         "个人分组",
			ProviderCode: "openai",
			Enabled:      true,
			GroupType:    "personal",
			AccountIDs:   []string{},
		},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
			Role:            "admin",
			SessionID:       "sess_admin",
		},
	})(newManagementGroupCreateHandler(
		service,
		managementGroupScopeSelf,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			NewLogID: func() string { return "oplog_my_group_create" },
		}),
	))
	req := httptest.NewRequest(
		http.MethodPost,
		"/__aisys__/api/my-groups?systemAccountId=sys_admin",
		strings.NewReader(`{"name":"个人分组","providerCode":"openai"}`),
	)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
	if !service.called ||
		service.input.SystemAccountID != "sys_admin" ||
		service.input.IncludeSystemAccountFields {
		t.Fatalf("create input = %+v called=%v", service.input, service.called)
	}
	if strings.Contains(rec.Body.String(), "systemAccountId") {
		t.Fatalf("self response leaked system account field: %s", rec.Body.String())
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("decode self operation log: %v", err)
	}
	if logInput.ActorSystemAccountID != "sys_admin" ||
		logInput.ActorRole != "user" ||
		logInput.OperationScopeSystemAccountID != "sys_admin" ||
		logInput.Mode != "self" ||
		logInput.Path != "/__aisys__/api/my-groups/" {
		t.Fatalf("self operation log = %+v", logInput)
	}
}

func TestManagementGroupCreateHandlerRejectsOrdinaryUserOnAdminRoute(t *testing.T) {
	service := &managementGroupCreateServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementGroupCreateHandler(service, managementGroupScopeAdmin, managementOperationLogOptions{}))
	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/groups", strings.NewReader(`{"name":"分组","providerCode":"openai"}`))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	if service.called {
		t.Fatal("service should not be called")
	}
}

func TestManagementGroupCreateHandlerRejectsInvalidBodies(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantStatus  int
		wantMessage string
	}{
		{name: "array", body: `[]`, wantStatus: http.StatusBadRequest},
		{name: "missing fields", body: `{}`, wantStatus: http.StatusBadRequest},
		{name: "blank name", body: `{"name":" ","providerCode":"openai"}`, wantStatus: http.StatusBadRequest},
		{name: "unknown field", body: `{"name":"分组","providerCode":"openai","providerProtocolProfileId":"profile"}`, wantStatus: http.StatusBadRequest},
		{name: "null description", body: `{"name":"分组","providerCode":"openai","description":null}`, wantStatus: http.StatusBadRequest},
		{name: "group type is not trimmed", body: `{"name":"分组","providerCode":"openai","groupType":" high_concurrency "}`, wantStatus: http.StatusBadRequest},
		{name: "null policy", body: `{"name":"分组","providerCode":"openai","schedulingPolicy":null}`, wantStatus: http.StatusBadRequest},
		{name: "internal policy field", body: `{"name":"分组","providerCode":"openai","schedulingPolicy":{"mode":"balanced_fast"}}`, wantStatus: http.StatusBadRequest},
		{name: "fractional policy", body: `{"name":"分组","providerCode":"openai","schedulingPolicy":{"defaultSoftConcurrency":1.5}}`, wantStatus: http.StatusBadRequest},
		{name: "policy below minimum", body: `{"name":"分组","providerCode":"openai","schedulingPolicy":{"maxQueueWaitMs":0}}`, wantStatus: http.StatusBadRequest},
		{name: "invalid overflow", body: `{"name":"分组","providerCode":"openai","schedulingPolicy":{"clientIpConcurrencyOverflowMode":"drop"}}`, wantStatus: http.StatusBadRequest},
		{name: "trailing json", body: `{"name":"分组","providerCode":"openai"} {}`, wantStatus: http.StatusBadRequest, wantMessage: "请求体无效"},
		{
			name:       "too large",
			body:       `{"name":"` + strings.Repeat("x", managementGroupCreateMaxBodyBytes) + `","providerCode":"openai"}`,
			wantStatus: http.StatusRequestEntityTooLarge,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementGroupCreateServiceStub{}
			handler := newManagementGroupCreateHandler(service, managementGroupScopeSelf, managementOperationLogOptions{})
			req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/my-groups", strings.NewReader(tt.body))
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
				SystemAccountID: "sys_user",
				Role:            "user",
			}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if service.called {
				t.Fatal("service should not be called")
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if tt.wantStatus == http.StatusRequestEntityTooLarge {
				if body["message"] != "请求体过大" {
					t.Fatalf("message = %q", body["message"])
				}
			} else {
				wantMessage := tt.wantMessage
				if wantMessage == "" {
					wantMessage = "分组参数无效"
				}
				if body["message"] != wantMessage {
					t.Fatalf("message = %q, want %q", body["message"], wantMessage)
				}
			}
		})
	}
}

func TestManagementGroupCreateHandlerMapsErrorsWithoutLeakingInternals(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantText   string
	}{
		{name: "system account missing", err: managementgroups.ErrSystemAccountNotFound, wantStatus: http.StatusBadRequest, wantText: "目标系统账户不存在"},
		{name: "provider missing", err: &managementgroups.ProviderNotFoundError{Code: "openai"}, wantStatus: http.StatusBadRequest, wantText: "不支持的供应商：openai"},
		{name: "provider disabled", err: &managementgroups.ProviderDisabledError{Code: "openai"}, wantStatus: http.StatusBadRequest, wantText: "供应商已停用：openai"},
		{name: "duplicate", err: &managementgroups.NameExistsError{Name: "分组"}, wantStatus: http.StatusConflict, wantText: "同一供应商下分组名称已存在：分组"},
		{name: "validation", err: &managementgroups.ValidationError{Message: "internal detail"}, wantStatus: http.StatusBadRequest, wantText: "分组参数无效"},
		{name: "internal", err: errors.New("postgres password leaked"), wantStatus: http.StatusInternalServerError, wantText: "服务器内部错误"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementGroupCreateServiceStub{err: tt.err}
			handler := newManagementGroupCreateHandler(service, managementGroupScopeSelf, managementOperationLogOptions{})
			req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/my-groups", strings.NewReader(`{"name":"分组","providerCode":"openai"}`))
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
				SystemAccountID: "sys_user",
				Role:            "user",
			}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body["message"] != tt.wantText {
				t.Fatalf("message = %q, want %q", body["message"], tt.wantText)
			}
			if strings.Contains(rec.Body.String(), "password") {
				t.Fatalf("response leaked internal error: %s", rec.Body.String())
			}
		})
	}
}

func TestManagementGroupOptionsHandlerRequiresAdminAndParsesQuery(t *testing.T) {
	service := &managementGroupOptionServiceStub{
		options: []managementgroups.Option{
			{
				ID:                     "group_default",
				SystemAccountID:        "sys_user",
				SystemAccountName:      "用户",
				OwnerSystemAccountID:   "sys_user",
				OwnerSystemAccountName: "用户",
				Name:                   "默认分组",
				ProviderCode:           "openai",
				Enabled:                true,
				IsDefault:              true,
				GroupType:              "personal",
				AccessType:             "owner",
			},
		},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementGroupOptionsHandler(service, managementGroupScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/groups/options?systemAccountId=sys_user&ids=group_a,group_b&ids=group_a&keyword=%20%E9%BB%98%E8%AE%A4%20&providerCode=%20openai%20&limit=500&manageableOnly=yes&preferDefault=1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.input.SystemAccountID != "sys_user" ||
		!service.input.IncludeSystemAccountFields ||
		service.input.Keyword != "默认" ||
		service.input.ProviderCode != "openai" ||
		service.input.Limit != 500 ||
		!service.input.ManageableOnly ||
		!service.input.PreferDefault {
		t.Fatalf("service input = %+v", service.input)
	}
	if len(service.input.IDs) != 2 || service.input.IDs[0] != "group_a" || service.input.IDs[1] != "group_b" {
		t.Fatalf("ids = %#v", service.input.IDs)
	}
	var body struct {
		Data []managementgroups.Option `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].SystemAccountName != "用户" || body.Data[0].AccessType != "owner" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementGroupOptionsHandlerRejectsOrdinaryUser(t *testing.T) {
	service := &managementGroupOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementGroupOptionsHandler(service, managementGroupScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/groups/options?systemAccountId=sys_admin", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if service.called {
		t.Fatal("service should not be called for ordinary user on management route")
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["message"] != "需要管理员权限" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementMyGroupOptionsHandlerForcesSelfScope(t *testing.T) {
	service := &managementGroupOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementGroupOptionsHandler(service, managementGroupScopeSelf))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-groups/options?systemAccountId=sys_admin&manageableOnly=bad&preferDefault=false", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.input.SystemAccountID != "sys_user" ||
		service.input.IncludeSystemAccountFields ||
		service.input.ManageableOnly ||
		service.input.PreferDefault {
		t.Fatalf("service input = %+v", service.input)
	}
}

func TestManagementGroupOptionsHandlerRedactsStoreErrors(t *testing.T) {
	handler := newManagementGroupOptionsHandler(&managementGroupOptionServiceStub{err: errors.New("postgres password leaked")}, managementGroupScopeSelf)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-groups/options", nil)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_user", Role: "user"}))
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

func TestManagementGroupAccountOptionsHandlerRequiresAdminAndParsesQuery(t *testing.T) {
	service := &managementGroupOptionServiceStub{
		accountOptions: []managementgroups.AccountOption{
			{
				Option: managementgroups.Option{
					ID:                     "group_default",
					SystemAccountID:        "sys_user",
					SystemAccountName:      "用户",
					OwnerSystemAccountID:   "sys_user",
					OwnerSystemAccountName: "用户",
					Name:                   "默认分组",
					ProviderCode:           "openai",
					Enabled:                true,
					IsDefault:              true,
					GroupType:              "personal",
					AccessType:             "owner",
				},
				AccountIDs: []string{"acct_a", "acct_b"},
			},
		},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementGroupAccountOptionsHandler(service, managementGroupScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/groups/account-options?systemAccountId=sys_user&ids=group_a,group_b&ids=group_a&keyword=%20%E9%BB%98%E8%AE%A4%20&providerCode=%20openai%20&limit=500&manageableOnly=yes&preferDefault=1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.accountInput.SystemAccountID != "sys_user" ||
		!service.accountInput.IncludeSystemAccountFields ||
		service.accountInput.Keyword != "默认" ||
		service.accountInput.ProviderCode != "openai" ||
		service.accountInput.Limit != 500 ||
		!service.accountInput.ManageableOnly ||
		!service.accountInput.PreferDefault {
		t.Fatalf("service account input = %+v", service.accountInput)
	}
	if len(service.accountInput.IDs) != 2 || service.accountInput.IDs[0] != "group_a" || service.accountInput.IDs[1] != "group_b" {
		t.Fatalf("ids = %#v", service.accountInput.IDs)
	}
	var body struct {
		Data []managementgroups.AccountOption `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 1 ||
		body.Data[0].SystemAccountName != "用户" ||
		body.Data[0].AccessType != "owner" ||
		len(body.Data[0].AccountIDs) != 2 {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementGroupAccountOptionsHandlerRejectsOrdinaryUser(t *testing.T) {
	service := &managementGroupOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementGroupAccountOptionsHandler(service, managementGroupScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/groups/account-options?systemAccountId=sys_admin", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if service.accountCalled {
		t.Fatal("service should not be called for ordinary user on management route")
	}
}

func TestManagementMyGroupAccountOptionsHandlerForcesSelfScope(t *testing.T) {
	service := &managementGroupOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementGroupAccountOptionsHandler(service, managementGroupScopeSelf))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-groups/account-options?systemAccountId=sys_admin&manageableOnly=bad&preferDefault=false", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.accountInput.SystemAccountID != "sys_user" ||
		service.accountInput.IncludeSystemAccountFields ||
		service.accountInput.ManageableOnly ||
		service.accountInput.PreferDefault {
		t.Fatalf("service account input = %+v", service.accountInput)
	}
}

func TestManagementGroupAccountOptionsHandlerRedactsStoreErrors(t *testing.T) {
	handler := newManagementGroupAccountOptionsHandler(&managementGroupOptionServiceStub{err: errors.New("postgres password leaked")}, managementGroupScopeSelf)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-groups/account-options", nil)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_user", Role: "user"}))
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

func TestRouterRegistersW2ManagementGroupOptions(t *testing.T) {
	service := &managementGroupOptionServiceStub{
		options:        []managementgroups.Option{{ID: "group_default", OwnerSystemAccountID: "sys_admin", Name: "默认分组", ProviderCode: "openai", Enabled: true, IsDefault: true, GroupType: "personal", AccessType: "owner"}},
		accountOptions: []managementgroups.AccountOption{{Option: managementgroups.Option{ID: "group_default", OwnerSystemAccountID: "sys_admin", Name: "默认分组", ProviderCode: "openai", Enabled: true, IsDefault: true, GroupType: "personal", AccessType: "owner"}, AccountIDs: []string{"acct_main"}}},
	}
	router := NewRouter(RouterOptions{
		Config:                                 config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                                 slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementGroupOptionsHandler:          newManagementGroupOptionsHandler(service, managementGroupScopeAdmin),
		ManagementMyGroupOptionsHandler:        newManagementGroupOptionsHandler(service, managementGroupScopeSelf),
		ManagementGroupAccountOptionsHandler:   newManagementGroupAccountOptionsHandler(service, managementGroupScopeAdmin),
		ManagementMyGroupAccountOptionsHandler: newManagementGroupAccountOptionsHandler(service, managementGroupScopeSelf),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	for _, path := range []string{"/__aisys__/api/groups/options", "/__aisys__/api/my-groups/options", "/__aisys__/api/groups/account-options", "/__aisys__/api/my-groups/account-options"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", path, got)
		}
	}
}

func TestRouterRegistersW5ManagementGroupCreateRoutes(t *testing.T) {
	authenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusCreated, map[string]string{"id": "grp_created"})
	})
	router := NewRouter(RouterOptions{
		Config:                           config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                           slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementGroupCreateHandler:     handler,
		ManagementMyGroupCreateHandler:   handler,
		ManagementAPIAuthMiddleware:      NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(authenticator),
	})

	for index, path := range []string{"/__aisys__/api/groups", "/__aisys__/api/my-groups"} {
		body := `{"name":"新分组 ` + string(rune('A'+index)) + `","providerCode":"openai"}`
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusCreated {
			t.Fatalf("%s status = %d, want 201; body = %s", path, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", path, got)
		}
	}
}

func TestRouterW5ManagementGroupCreateUsesRawNodeScopeForDuplicateGuard(t *testing.T) {
	authenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	}
	createCalls := 0
	router := NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger: slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementGroupCreateHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			createCalls++
			writeData(w, http.StatusCreated, map[string]string{"id": "grp_created"})
		}),
		ManagementAPIAuthMiddleware:      NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(authenticator),
	})

	first := httptest.NewRequest(http.MethodPost, "/__aisys__/api/groups", strings.NewReader(`{"name":" 新分组 ","providerCode":" openai "}`))
	first.Header.Set("Cookie", "juhe_ai_session=session-token")
	first.Header.Set("Content-Type", "application/json")
	firstRec := httptest.NewRecorder()
	router.ServeHTTP(firstRec, first)
	if firstRec.Code != http.StatusCreated {
		t.Fatalf("first status = %d, want 201; body = %s", firstRec.Code, firstRec.Body.String())
	}

	second := httptest.NewRequest(http.MethodPost, "/__aisys__/api/groups?systemAccountId=all", strings.NewReader(`{"name":"新分组","providerCode":"openai"}`))
	second.Header.Set("Cookie", "juhe_ai_session=session-token")
	second.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	router.ServeHTTP(secondRec, second)
	if secondRec.Code != http.StatusCreated {
		t.Fatalf("second status = %d, want 201 for distinct raw scope; body = %s", secondRec.Code, secondRec.Body.String())
	}
	duplicate := httptest.NewRequest(http.MethodPost, "/__aisys__/api/groups?systemAccountId=all", strings.NewReader(`{"name":"新分组","providerCode":"openai"}`))
	duplicate.Header.Set("Cookie", "juhe_ai_session=session-token")
	duplicate.Header.Set("Content-Type", "application/json")
	duplicateRec := httptest.NewRecorder()
	router.ServeHTTP(duplicateRec, duplicate)
	if duplicateRec.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, want 409; body = %s", duplicateRec.Code, duplicateRec.Body.String())
	}
	if createCalls != 2 {
		t.Fatalf("create calls = %d, want 2", createCalls)
	}
}

func TestRouterW5ManagementGroupCreateEnforcesBodyLimitBeforeHandler(t *testing.T) {
	authenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	}
	createCalls := 0
	router := NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger: slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementGroupCreateHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			createCalls++
			writeData(w, http.StatusCreated, map[string]string{"id": "grp_created"})
		}),
		ManagementAPIAuthMiddleware:      NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(authenticator),
	})
	body := `{"name":"` + strings.Repeat("x", 256<<10) + `","providerCode":"openai"}`
	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/groups", strings.NewReader(body))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body = %s", rec.Code, rec.Body.String())
	}
	if createCalls != 0 {
		t.Fatalf("create calls = %d, want 0", createCalls)
	}
}

func TestRouterW5ManagementGroupCreateUsesSystemJSONBodyErrorContract(t *testing.T) {
	authenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	}
	createCalls := 0
	router := NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger: slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementGroupCreateHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			createCalls++
			writeData(w, http.StatusCreated, map[string]string{"id": "grp_created"})
		}),
		ManagementAPIAuthMiddleware:      NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(authenticator),
	})
	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/groups", strings.NewReader(`{"name":`))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	if createCalls != 0 {
		t.Fatalf("create calls = %d, want 0", createCalls)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["message"] != "请求体无效" {
		t.Fatalf("message = %q", body["message"])
	}
}

func TestRouterW5ManagementGroupCreateMatchesExpressJSONBoundary(t *testing.T) {
	tests := []struct {
		name              string
		contentType       string
		body              string
		wantStatus        int
		wantMessage       string
		wantAuthTouchCall bool
	}{
		{
			name:        "scalar json is rejected before auth",
			contentType: "application/json",
			body:        `"group"`,
			wantStatus:  http.StatusBadRequest,
			wantMessage: "请求体无效",
		},
		{
			name:              "empty json body becomes empty object",
			contentType:       "application/json",
			body:              "",
			wantStatus:        http.StatusBadRequest,
			wantMessage:       "分组参数无效",
			wantAuthTouchCall: true,
		},
		{
			name:              "non json content type is not parsed",
			contentType:       "text/plain",
			body:              `{"name":"分组","providerCode":"openai"}`,
			wantStatus:        http.StatusBadRequest,
			wantMessage:       "分组参数无效",
			wantAuthTouchCall: true,
		},
		{
			name:        "unsupported json charset",
			contentType: "application/json; charset=iso-8859-1",
			body:        `{"name":"分组","providerCode":"openai"}`,
			wantStatus:  http.StatusUnsupportedMediaType,
			wantMessage: "请求体无效",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authenticator := &managementAPIAuthenticatorStub{
				context: managementauth.Context{
					SystemAccountID: "sys_admin",
					Username:        "admin",
					Role:            "admin",
					SessionID:       "sess_admin",
				},
			}
			service := &managementGroupCreateServiceStub{}
			router := NewRouter(RouterOptions{
				Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
				Logger: slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
				ManagementGroupCreateHandler: newManagementGroupCreateHandler(
					service,
					managementGroupScopeAdmin,
					managementOperationLogOptions{},
				),
				ManagementAPIAuthMiddleware:      NewManagementAPIAuthMiddleware(authenticator),
				ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(authenticator),
			})
			req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/groups", strings.NewReader(tt.body))
			req.Header.Set("Cookie", "juhe_ai_session=session-token")
			req.Header.Set("Content-Type", tt.contentType)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body["message"] != tt.wantMessage {
				t.Fatalf("message = %q, want %q", body["message"], tt.wantMessage)
			}
			authTouched := authenticator.touchCookieHeader != ""
			if authTouched != tt.wantAuthTouchCall {
				t.Fatalf("auth touch called = %v, want %v", authTouched, tt.wantAuthTouchCall)
			}
			if service.called {
				t.Fatal("service should not be called")
			}
		})
	}
}

func TestRouterW5ManagementGroupCreateChecksAdminBeforeMutationGuard(t *testing.T) {
	authenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_user",
			Username:        "user",
			Role:            "user",
			SessionID:       "sess_user",
		},
	}
	handlerCalls := 0
	router := NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger: slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementGroupCreateHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			handlerCalls++
			writeData(w, http.StatusCreated, map[string]string{"id": "grp_created"})
		}),
		ManagementAPIAuthMiddleware:      NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(authenticator),
	})

	for attempt := 0; attempt < 2; attempt++ {
		req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/groups", strings.NewReader(`{"name":"分组","providerCode":"openai"}`))
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d status = %d, want 403; body = %s", attempt+1, rec.Code, rec.Body.String())
		}
	}
	if handlerCalls != 0 {
		t.Fatalf("handler calls = %d, want 0", handlerCalls)
	}
}

func TestRouterDoesNotRegisterW5ManagementGroupCreateWhenDisabled(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusCreated, map[string]string{"id": "grp_created"})
	})
	router := NewRouter(RouterOptions{
		Config:                         config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                         slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementGroupCreateHandler:   handler,
		ManagementMyGroupCreateHandler: handler,
	})

	for _, path := range []string{"/__aisys__/api/groups", "/__aisys__/api/my-groups"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"name":"分组","providerCode":"openai"}`))
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", path, rec.Code)
		}
	}
}

func TestRouterDoesNotRegisterW2ManagementGroupOptionsWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                               config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                               slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementGroupOptionsHandler:        newManagementGroupOptionsHandler(&managementGroupOptionServiceStub{}, managementGroupScopeAdmin),
		ManagementGroupAccountOptionsHandler: newManagementGroupAccountOptionsHandler(&managementGroupOptionServiceStub{}, managementGroupScopeAdmin),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	for _, path := range []string{"/__aisys__/api/groups/options", "/__aisys__/api/groups/account-options"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", path, rec.Code)
		}
	}
}

type managementGroupOptionServiceStub struct {
	called         bool
	accountCalled  bool
	input          managementgroups.OptionListInput
	accountInput   managementgroups.OptionListInput
	options        []managementgroups.Option
	accountOptions []managementgroups.AccountOption
	err            error
}

type managementGroupCreateServiceStub struct {
	called bool
	input  managementgroups.CreateInput
	result managementgroups.CreateResult
	err    error
}

func (s *managementGroupCreateServiceStub) Create(_ *http.Request, input managementgroups.CreateInput) (managementgroups.CreateResult, error) {
	s.called = true
	s.input = input
	return s.result, s.err
}

func (s *managementGroupOptionServiceStub) Options(_ *http.Request, input managementgroups.OptionListInput) ([]managementgroups.Option, error) {
	s.called = true
	s.input = input
	return s.options, s.err
}

func (s *managementGroupOptionServiceStub) AccountOptions(_ *http.Request, input managementgroups.OptionListInput) ([]managementgroups.AccountOption, error) {
	s.accountCalled = true
	s.accountInput = input
	return s.accountOptions, s.err
}
