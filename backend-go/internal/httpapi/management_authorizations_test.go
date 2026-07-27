package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/config"
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementauthorizations"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementAuthorizationCreateHandlerCreatesAndWritesOperationLog(t *testing.T) {
	createdAt := time.Date(2026, 7, 9, 8, 30, 0, 0, time.UTC)
	queueStub := &operationLogQueueStub{}
	service := &managementAuthorizationCreateServiceStub{
		result: managementauthorizations.Summary{
			ID:                             "rauthgrant_main",
			ResourceType:                   "account",
			ResourceID:                     "acct_main",
			ResourceName:                   "主账号",
			ResourceOwnerSystemAccountID:   "sys_owner",
			ResourceOwnerSystemAccountName: "资源归属人",
			GranteeType:                    "system_account",
			GranteeSystemAccountID:         "sys_grantee",
			GranteeSystemAccountName:       "被授权人",
			GranteeUsername:                "grantee",
			Scope:                          "use",
			Status:                         "active",
			Limits: port.ManagementRequestQuotaLimits{
				Daily: &port.ManagementRequestQuotaLimit{Enabled: true, Limit: 10},
			},
			AuthorizationSources: []port.ManagementResourceAuthorizationSourceSummary{},
			Usage:                port.ManagementAccountUsageSummary{},
			CreatedBy:            "sys_admin",
			CreatedAt:            createdAt,
			UpdatedAt:            createdAt,
		},
	}
	handler := newManagementAuthorizationCreateHandler(
		service,
		managementAuthorizationScopeAdmin,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			Now:      func() time.Time { return createdAt },
			NewLogID: func() string { return "oplog_authorization_create" },
		}),
	)
	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/authorizations?systemAccountId=sys_owner", strings.NewReader(`{
		"resourceType":"account",
		"resourceId":"acct_main",
		"granteeType":"system_account",
		"granteeId":"sys_grantee",
		"targetGroupId":"grp_target",
		"remark":"项目授权",
		"expiresAt":"2026-07-10T00:00:00.000Z",
		"limits":{"daily":{"enabled":true,"limit":10}}
	}`))
	req.RemoteAddr = "127.0.0.1:12345"
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_authorization_create"))
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_admin",
		Username:        "admin",
		DisplayName:     "管理员",
		Role:            "admin",
		SessionID:       "sess_admin",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
	if !service.called ||
		service.input.ResourceOwnerSystemAccountID != "sys_owner" ||
		service.input.ResourceType != "account" ||
		service.input.ResourceID != "acct_main" ||
		service.input.GranteeType != "system_account" ||
		service.input.GranteeID != "sys_grantee" ||
		service.input.TargetGroupID != "grp_target" ||
		service.input.Remark != "项目授权" ||
		!service.input.HasRemark ||
		service.input.ExpiresAt != "2026-07-10T00:00:00.000Z" ||
		!service.input.HasExpiresAt ||
		!service.input.HasLimits ||
		service.input.ActorSystemAccountID != "sys_admin" {
		t.Fatalf("service input = %+v", service.input)
	}
	if queueStub.calls != 1 {
		t.Fatalf("operation log queue calls = %d, want 1", queueStub.calls)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("DecodeWriteTaskPayload() error = %v", err)
	}
	if queueStub.taskType != operationlogjob.TaskTypeWrite ||
		queueStub.options.Queue != operationlogjob.QueueName ||
		logInput.ID != "oplog_authorization_create" ||
		logInput.TraceID != "req_authorization_create" ||
		logInput.ActorSystemAccountID != "sys_admin" ||
		logInput.OperationScopeSystemAccountID != "sys_owner" ||
		logInput.Mode != "admin" ||
		logInput.Module != "authorizations" ||
		logInput.Action != "create" ||
		logInput.OperationKey != "authorizations.create" ||
		logInput.ResourceType != "authorization" ||
		logInput.ResourceID != "rauthgrant_main" ||
		logInput.ResourceName != "主账号" ||
		logInput.Summary != "创建资源授权：主账号 -> 被授权人" ||
		logInput.Method != http.MethodPost ||
		logInput.Path != "/__aisys__/api/authorizations" ||
		logInput.ClientIP != "127.0.0.1" ||
		!logInput.CreatedAt.Equal(createdAt) {
		t.Fatalf("operation log input = %+v", logInput)
	}
	if logInput.StatusCode == nil || *logInput.StatusCode != http.StatusCreated {
		t.Fatalf("status code = %+v, want 201", logInput.StatusCode)
	}
	if len(logInput.Targets) != 2 ||
		logInput.Targets[0].TargetID != "acct_main" ||
		logInput.Targets[1].TargetID != "sys_grantee" {
		t.Fatalf("targets = %+v", logInput.Targets)
	}
	if len(logInput.Viewers) != 2 ||
		logInput.Viewers[0].SystemAccountID != "sys_owner" ||
		logInput.Viewers[1].SystemAccountID != "sys_grantee" {
		t.Fatalf("viewers = %+v", logInput.Viewers)
	}
}

func TestManagementMyAuthorizationCreateHandlerUsesSelfScope(t *testing.T) {
	service := &managementAuthorizationCreateServiceStub{
		result: managementauthorizations.Summary{
			ID:                           "rauthgrant_team",
			ResourceType:                 "group",
			ResourceID:                   "grp_owner",
			ResourceName:                 "归属分组",
			ResourceOwnerSystemAccountID: "sys_owner",
			GranteeType:                  "team",
			GranteeTeamID:                "team_ops",
			GranteeTeamName:              "运维团队",
			Scope:                        "use",
			Status:                       "active",
			AuthorizationSources:         []port.ManagementResourceAuthorizationSourceSummary{},
			Usage:                        port.ManagementAccountUsageSummary{},
			CreatedBy:                    "sys_owner",
			CreatedAt:                    time.Date(2026, 7, 9, 8, 30, 0, 0, time.UTC),
			UpdatedAt:                    time.Date(2026, 7, 9, 8, 30, 0, 0, time.UTC),
		},
	}
	handler := newManagementAuthorizationCreateHandler(service, managementAuthorizationScopeSelf)
	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/my-authorizations", strings.NewReader(`{
		"resourceType":"group",
		"resourceId":"grp_owner",
		"granteeType":"team",
		"granteeId":"team_ops"
	}`))
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_owner",
		Username:        "owner",
		Role:            "user",
		SessionID:       "sess_owner",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
	if !service.called || service.input.ResourceOwnerSystemAccountID != "sys_owner" || service.input.ActorSystemAccountID != "sys_owner" {
		t.Fatalf("service input = %+v", service.input)
	}
}

func TestManagementAuthorizationCreateHandlerRejectsInvalidScopeOrPayload(t *testing.T) {
	tests := []struct {
		name   string
		target string
		body   string
		want   string
	}{
		{
			name:   "admin missing owner",
			target: "/__aisys__/api/authorizations",
			body:   `{"resourceType":"group","resourceId":"grp_owner","granteeType":"team","granteeId":"team_ops"}`,
			want:   "管理员新增授权时必须指定授权人",
		},
		{
			name:   "null limits",
			target: "/__aisys__/api/authorizations?systemAccountId=sys_owner",
			body:   `{"resourceType":"group","resourceId":"grp_owner","granteeType":"team","granteeId":"team_ops","limits":null}`,
			want:   "授权参数不合法",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementAuthorizationCreateServiceStub{}
			handler := newManagementAuthorizationCreateHandler(service, managementAuthorizationScopeAdmin)
			req := httptest.NewRequest(http.MethodPost, tt.target, strings.NewReader(tt.body))
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
				SystemAccountID: "sys_admin",
				Username:        "admin",
				Role:            "admin",
				SessionID:       "sess_admin",
			}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
			}
			if service.called {
				t.Fatal("service was called for invalid request")
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body["message"] != tt.want {
				t.Fatalf("message = %q, want %q", body["message"], tt.want)
			}
		})
	}
}

func TestManagementAuthorizationCreateHandlerRedactsInvalidServiceErrors(t *testing.T) {
	service := &managementAuthorizationCreateServiceStub{err: managementauthorizations.ErrAuthorizationCreateInvalid}
	handler := newManagementAuthorizationCreateHandler(service, managementAuthorizationScopeSelf)
	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/my-authorizations", strings.NewReader(`{
		"resourceType":"group",
		"resourceId":"grp_owner",
		"granteeType":"team",
		"granteeId":"team_ops"
	}`))
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_owner",
		Username:        "owner",
		Role:            "user",
		SessionID:       "sess_owner",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["message"] != "授权参数不合法" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementAuthorizationUpdateHandlerUpdatesAndWritesOperationLog(t *testing.T) {
	updatedAt := time.Date(2026, 7, 9, 10, 30, 0, 0, time.UTC)
	queueStub := &operationLogQueueStub{}
	service := &managementAuthorizationCreateServiceStub{
		updateFound: true,
		updateResult: managementauthorizations.Summary{
			ID:                             "rauthgrant_main",
			ResourceType:                   "account",
			ResourceID:                     "acct_main",
			ResourceName:                   "主账号",
			ResourceOwnerSystemAccountID:   "sys_owner",
			ResourceOwnerSystemAccountName: "资源归属人",
			GranteeType:                    "system_account",
			GranteeSystemAccountID:         "sys_grantee",
			GranteeSystemAccountName:       "被授权人",
			Scope:                          "use",
			Status:                         "paused",
			Limits: port.ManagementRequestQuotaLimits{
				Daily: &port.ManagementRequestQuotaLimit{Enabled: true, Limit: 10},
			},
			AuthorizationSources: []port.ManagementResourceAuthorizationSourceSummary{},
			Usage:                port.ManagementAccountUsageSummary{},
			CreatedBy:            "sys_owner",
			CreatedAt:            updatedAt,
			UpdatedAt:            updatedAt,
		},
	}
	handler := newManagementAuthorizationUpdateHandler(
		service,
		managementAuthorizationScopeAdmin,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			Now:      func() time.Time { return updatedAt },
			NewLogID: func() string { return "oplog_authorization_update" },
		}),
	)
	req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/authorizations/rauthgrant_main?systemAccountId=sys_owner", strings.NewReader(`{
		"status":"paused",
		"expiresAt":null,
		"limits":{"daily":{"enabled":true,"limit":10}}
	}`))
	req.RemoteAddr = "127.0.0.1:22345"
	req = managementAuthorizationRequestWithURLParam(req, "id", "rauthgrant_main")
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_authorization_update"))
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_admin",
		Username:        "admin",
		DisplayName:     "管理员",
		Role:            "admin",
		SessionID:       "sess_admin",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if !service.updateCalled ||
		service.updateInput.AuthorizationID != "rauthgrant_main" ||
		service.updateInput.ActorSystemAccountID != "sys_admin" ||
		service.updateInput.ActorRole != "admin" ||
		service.updateInput.ScopedSystemAccountID != "sys_owner" ||
		!service.updateInput.HasStatus ||
		service.updateInput.Status != "paused" ||
		!service.updateInput.HasExpiresAt ||
		service.updateInput.ExpiresAt != nil ||
		!service.updateInput.HasLimits ||
		service.updateInput.LimitsIsNull ||
		service.updateInput.Limits == nil {
		t.Fatalf("update input = %+v", service.updateInput)
	}
	var body struct {
		Data managementauthorizations.Summary `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.ID != "rauthgrant_main" || body.Data.Status != "paused" {
		t.Fatalf("response data = %+v", body.Data)
	}
	if queueStub.calls != 1 {
		t.Fatalf("operation log queue calls = %d, want 1", queueStub.calls)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("DecodeWriteTaskPayload() error = %v", err)
	}
	if logInput.ID != "oplog_authorization_update" ||
		logInput.TraceID != "req_authorization_update" ||
		logInput.ActorSystemAccountID != "sys_admin" ||
		logInput.OperationScopeSystemAccountID != "sys_owner" ||
		logInput.Mode != "admin" ||
		logInput.Module != "authorizations" ||
		logInput.Action != "update" ||
		logInput.OperationKey != "authorizations.update" ||
		logInput.ResourceType != "authorization" ||
		logInput.ResourceID != "rauthgrant_main" ||
		logInput.ResourceName != "主账号" ||
		logInput.Summary != "更新资源授权：主账号 -> 被授权人" ||
		logInput.Method != http.MethodPatch ||
		logInput.Path != "/__aisys__/api/authorizations/rauthgrant_main" ||
		logInput.ClientIP != "127.0.0.1" ||
		!logInput.CreatedAt.Equal(updatedAt) {
		t.Fatalf("operation log input = %+v", logInput)
	}
	if logInput.StatusCode == nil || *logInput.StatusCode != http.StatusOK {
		t.Fatalf("status code = %+v, want 200", logInput.StatusCode)
	}
	if len(logInput.Changes) != 3 ||
		logInput.Changes[0].Field != "status" ||
		logInput.Changes[1].Field != "expiresAt" ||
		logInput.Changes[2].Field != "limits" {
		t.Fatalf("changes = %+v", logInput.Changes)
	}
}

func TestManagementMyAuthorizationUpdateHandlerUsesSelfScope(t *testing.T) {
	service := &managementAuthorizationCreateServiceStub{
		updateFound: true,
		updateResult: managementauthorizations.Summary{
			ID:                           "rauthgrant_main",
			ResourceType:                 "group",
			ResourceID:                   "grp_owner",
			ResourceOwnerSystemAccountID: "sys_owner",
			GranteeType:                  "team",
			GranteeTeamID:                "team_ops",
			Scope:                        "use",
			Status:                       "active",
			AuthorizationSources:         []port.ManagementResourceAuthorizationSourceSummary{},
			Usage:                        port.ManagementAccountUsageSummary{},
			CreatedAt:                    time.Date(2026, 7, 9, 10, 30, 0, 0, time.UTC),
			UpdatedAt:                    time.Date(2026, 7, 9, 10, 30, 0, 0, time.UTC),
		},
	}
	handler := newManagementAuthorizationUpdateHandler(service, managementAuthorizationScopeSelf)
	req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/my-authorizations/rauthgrant_main?systemAccountId=sys_other", strings.NewReader(`{"limits":null}`))
	req = managementAuthorizationRequestWithURLParam(req, "id", "rauthgrant_main")
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_owner",
		Username:        "owner",
		Role:            "user",
		SessionID:       "sess_owner",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if !service.updateCalled ||
		service.updateInput.AuthorizationID != "rauthgrant_main" ||
		service.updateInput.ActorSystemAccountID != "sys_owner" ||
		service.updateInput.ActorRole != "user" ||
		service.updateInput.ScopedSystemAccountID != "sys_owner" ||
		!service.updateInput.HasLimits ||
		!service.updateInput.LimitsIsNull {
		t.Fatalf("update input = %+v", service.updateInput)
	}
}

func TestManagementAuthorizationUpdateHandlerRejectsInvalidOrMissingRecord(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		body     string
		found    bool
		wantCode int
		wantMsg  string
	}{
		{name: "empty id", id: " ", body: `{"status":"paused"}`, found: true, wantCode: http.StatusBadRequest, wantMsg: "授权记录 ID 不合法"},
		{name: "empty payload", id: "rauthgrant_main", body: `{}`, found: true, wantCode: http.StatusBadRequest, wantMsg: "修改授权参数不合法"},
		{name: "unknown field", id: "rauthgrant_main", body: `{"status":"paused","remark":"x"}`, found: true, wantCode: http.StatusBadRequest, wantMsg: "修改授权参数不合法"},
		{name: "missing", id: "rauthgrant_missing", body: `{"status":"paused"}`, found: false, wantCode: http.StatusNotFound, wantMsg: "授权记录不存在"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementAuthorizationCreateServiceStub{updateFound: tt.found}
			handler := newManagementAuthorizationUpdateHandler(service, managementAuthorizationScopeAdmin)
			req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/authorizations/"+url.PathEscape(tt.id), strings.NewReader(tt.body))
			req = managementAuthorizationRequestWithURLParam(req, "id", tt.id)
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
				SystemAccountID: "sys_admin",
				Username:        "admin",
				Role:            "admin",
				SessionID:       "sess_admin",
			}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantCode, rec.Body.String())
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body["message"] != tt.wantMsg {
				t.Fatalf("message = %q, want %q", body["message"], tt.wantMsg)
			}
		})
	}
}

func TestManagementAuthorizationExpireUpdateHandlerUpdatesAndWritesOperationLog(t *testing.T) {
	updatedAt := time.Date(2026, 7, 9, 13, 30, 0, 0, time.UTC)
	expiresAt := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	queueStub := &operationLogQueueStub{}
	service := &managementAuthorizationCreateServiceStub{
		updateFound: true,
		updateResult: managementauthorizations.Summary{
			ID:                             "rauthgrant_main",
			ResourceType:                   "account",
			ResourceID:                     "acct_main",
			ResourceName:                   "主账号",
			ResourceOwnerSystemAccountID:   "sys_owner",
			ResourceOwnerSystemAccountName: "资源归属人",
			GranteeType:                    "system_account",
			GranteeSystemAccountID:         "sys_grantee",
			GranteeSystemAccountName:       "被授权人",
			Scope:                          "use",
			Status:                         "paused",
			ExpiresAt:                      &expiresAt,
			Limits: port.ManagementRequestQuotaLimits{
				Hourly: &port.ManagementRequestHourlyQuotaLimit{Enabled: true, Hours: 3, Limit: 1},
			},
			AuthorizationSources: []port.ManagementResourceAuthorizationSourceSummary{},
			Usage:                port.ManagementAccountUsageSummary{},
			CreatedBy:            "sys_owner",
			CreatedAt:            updatedAt,
			UpdatedAt:            updatedAt,
		},
	}
	handler := newManagementAuthorizationExpireUpdateHandler(
		service,
		managementAuthorizationScopeAdmin,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			Now:      func() time.Time { return updatedAt },
			NewLogID: func() string { return "oplog_authorization_update_expire" },
		}),
	)
	req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/authorizations/rauthgrant_main/expire?systemAccountId=sys_owner", strings.NewReader(`{
		"expiresAt":"2027-01-02T03:04:05.000Z",
		"limits":{"hourly":{"enabled":true,"hours":3,"limit":1}}
	}`))
	req.RemoteAddr = "127.0.0.1:42345"
	req = managementAuthorizationRequestWithURLParam(req, "id", "rauthgrant_main")
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_authorization_update_expire"))
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_admin",
		Username:        "admin",
		DisplayName:     "管理员",
		Role:            "admin",
		SessionID:       "sess_admin",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if !service.updateCalled ||
		service.updateInput.AuthorizationID != "rauthgrant_main" ||
		service.updateInput.ActorSystemAccountID != "sys_admin" ||
		service.updateInput.ActorRole != "admin" ||
		service.updateInput.ScopedSystemAccountID != "sys_owner" ||
		service.updateInput.HasStatus ||
		!service.updateInput.HasExpiresAt ||
		service.updateInput.ExpiresAt == nil ||
		*service.updateInput.ExpiresAt != "2027-01-02T03:04:05.000Z" ||
		!service.updateInput.HasLimits ||
		service.updateInput.LimitsIsNull ||
		service.updateInput.Limits == nil {
		t.Fatalf("update input = %+v", service.updateInput)
	}
	if queueStub.calls != 1 {
		t.Fatalf("operation log queue calls = %d, want 1", queueStub.calls)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("DecodeWriteTaskPayload() error = %v", err)
	}
	if logInput.ID != "oplog_authorization_update_expire" ||
		logInput.TraceID != "req_authorization_update_expire" ||
		logInput.ActorSystemAccountID != "sys_admin" ||
		logInput.OperationScopeSystemAccountID != "sys_owner" ||
		logInput.Mode != "admin" ||
		logInput.Module != "authorizations" ||
		logInput.Action != "update_expire" ||
		logInput.OperationKey != "authorizations.update_expire" ||
		logInput.ResourceType != "authorization" ||
		logInput.ResourceID != "rauthgrant_main" ||
		logInput.ResourceName != "主账号" ||
		logInput.Summary != "更新授权有效期：主账号 -> 被授权人" ||
		logInput.Method != http.MethodPatch ||
		logInput.Path != "/__aisys__/api/authorizations/rauthgrant_main/expire" ||
		logInput.ClientIP != "127.0.0.1" ||
		!logInput.CreatedAt.Equal(updatedAt) {
		t.Fatalf("operation log input = %+v", logInput)
	}
	if logInput.StatusCode == nil || *logInput.StatusCode != http.StatusOK {
		t.Fatalf("status code = %+v, want 200", logInput.StatusCode)
	}
	if len(logInput.Changes) != 3 ||
		logInput.Changes[0].Field != "status" ||
		logInput.Changes[1].Field != "expiresAt" ||
		logInput.Changes[2].Field != "limits" {
		t.Fatalf("changes = %+v", logInput.Changes)
	}
}

func TestManagementMyAuthorizationExpireUpdateHandlerUsesSelfScope(t *testing.T) {
	service := &managementAuthorizationCreateServiceStub{
		updateFound: true,
		updateResult: managementauthorizations.Summary{
			ID:                           "rauthgrant_main",
			ResourceType:                 "group",
			ResourceID:                   "grp_owner",
			ResourceOwnerSystemAccountID: "sys_owner",
			GranteeType:                  "team",
			GranteeTeamID:                "team_ops",
			Scope:                        "use",
			Status:                       "active",
			AuthorizationSources:         []port.ManagementResourceAuthorizationSourceSummary{},
			Usage:                        port.ManagementAccountUsageSummary{},
			CreatedAt:                    time.Date(2026, 7, 9, 13, 30, 0, 0, time.UTC),
			UpdatedAt:                    time.Date(2026, 7, 9, 13, 30, 0, 0, time.UTC),
		},
	}
	handler := newManagementAuthorizationExpireUpdateHandler(service, managementAuthorizationScopeSelf)
	req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/my-authorizations/rauthgrant_main/expire?systemAccountId=sys_other", strings.NewReader(`{"expiresAt":null,"limits":null}`))
	req = managementAuthorizationRequestWithURLParam(req, "id", "rauthgrant_main")
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_owner",
		Username:        "owner",
		Role:            "user",
		SessionID:       "sess_owner",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if !service.updateCalled ||
		service.updateInput.AuthorizationID != "rauthgrant_main" ||
		service.updateInput.ActorSystemAccountID != "sys_owner" ||
		service.updateInput.ActorRole != "user" ||
		service.updateInput.ScopedSystemAccountID != "sys_owner" ||
		service.updateInput.HasStatus ||
		!service.updateInput.HasExpiresAt ||
		service.updateInput.ExpiresAt != nil ||
		!service.updateInput.HasLimits ||
		!service.updateInput.LimitsIsNull {
		t.Fatalf("update input = %+v", service.updateInput)
	}
}

func TestManagementAuthorizationExpireUpdateHandlerRejectsInvalidOrMissingRecord(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		body     string
		found    bool
		wantCode int
		wantMsg  string
	}{
		{name: "empty id", id: " ", body: `{"expiresAt":null}`, found: true, wantCode: http.StatusBadRequest, wantMsg: "授权记录 ID 不合法"},
		{name: "missing expiresAt", id: "rauthgrant_main", body: `{"limits":null}`, found: true, wantCode: http.StatusBadRequest, wantMsg: "修改授权参数不合法"},
		{name: "unknown field", id: "rauthgrant_main", body: `{"expiresAt":null,"status":"paused"}`, found: true, wantCode: http.StatusBadRequest, wantMsg: "修改授权参数不合法"},
		{name: "invalid limits", id: "rauthgrant_main", body: `{"expiresAt":null,"limits":[]}`, found: true, wantCode: http.StatusBadRequest, wantMsg: "修改授权参数不合法"},
		{name: "missing", id: "rauthgrant_missing", body: `{"expiresAt":null}`, found: false, wantCode: http.StatusNotFound, wantMsg: "授权记录不存在"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementAuthorizationCreateServiceStub{updateFound: tt.found}
			handler := newManagementAuthorizationExpireUpdateHandler(service, managementAuthorizationScopeAdmin)
			req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/authorizations/"+url.PathEscape(tt.id)+"/expire", strings.NewReader(tt.body))
			req = managementAuthorizationRequestWithURLParam(req, "id", tt.id)
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
				SystemAccountID: "sys_admin",
				Username:        "admin",
				Role:            "admin",
				SessionID:       "sess_admin",
			}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantCode, rec.Body.String())
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body["message"] != tt.wantMsg {
				t.Fatalf("message = %q, want %q", body["message"], tt.wantMsg)
			}
		})
	}
}

func TestManagementAuthorizationReturnHandlerReturnsAndWritesOperationLog(t *testing.T) {
	createdAt := time.Date(2026, 7, 9, 9, 30, 0, 0, time.UTC)
	queueStub := &operationLogQueueStub{}
	service := &managementAuthorizationCreateServiceStub{
		returnFound: true,
		returnResult: managementauthorizations.Summary{
			ID:                             "rauthgrant_main",
			ResourceType:                   "account",
			ResourceID:                     "acct_main",
			ResourceName:                   "主账号",
			ResourceOwnerSystemAccountID:   "sys_owner",
			ResourceOwnerSystemAccountName: "资源归属人",
			GranteeType:                    "system_account",
			GranteeSystemAccountID:         "sys_grantee",
			GranteeSystemAccountName:       "被授权人",
			Scope:                          "use",
			Status:                         "returned",
			AuthorizationSources:           []port.ManagementResourceAuthorizationSourceSummary{},
			Usage:                          port.ManagementAccountUsageSummary{},
			CreatedBy:                      "sys_owner",
			CreatedAt:                      createdAt,
			UpdatedAt:                      createdAt,
		},
	}
	handler := newManagementAuthorizationReturnHandler(
		service,
		managementAuthorizationScopeAdmin,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			Now:      func() time.Time { return createdAt },
			NewLogID: func() string { return "oplog_authorization_return" },
		}),
	)
	req := httptest.NewRequest(http.MethodDelete, "/__aisys__/api/authorizations/rauthgrant_main/return?systemAccountId=sys_grantee", nil)
	req.RemoteAddr = "127.0.0.1:23456"
	req = managementAuthorizationRequestWithURLParam(req, "id", "rauthgrant_main")
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_authorization_return"))
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_admin",
		Username:        "admin",
		DisplayName:     "管理员",
		Role:            "admin",
		SessionID:       "sess_admin",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rec.Code, rec.Body.String())
	}
	if !service.returnCalled ||
		service.returnInput.AuthorizationID != "rauthgrant_main" ||
		service.returnInput.GranteeSystemAccountID != "sys_grantee" ||
		service.returnInput.ActorSystemAccountID != "sys_admin" {
		t.Fatalf("return input = %+v", service.returnInput)
	}
	if queueStub.calls != 1 {
		t.Fatalf("operation log queue calls = %d, want 1", queueStub.calls)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("DecodeWriteTaskPayload() error = %v", err)
	}
	if logInput.ID != "oplog_authorization_return" ||
		logInput.TraceID != "req_authorization_return" ||
		logInput.ActorSystemAccountID != "sys_admin" ||
		logInput.OperationScopeSystemAccountID != "sys_grantee" ||
		logInput.Mode != "admin" ||
		logInput.Module != "authorizations" ||
		logInput.Action != "return" ||
		logInput.OperationKey != "authorizations.return" ||
		logInput.ResourceType != "authorization" ||
		logInput.ResourceID != "rauthgrant_main" ||
		logInput.ResourceName != "acct_main" ||
		logInput.Summary != "归还授权使用权：acct_main" ||
		logInput.Method != http.MethodDelete ||
		logInput.Path != "/__aisys__/api/authorizations/rauthgrant_main/return" ||
		logInput.ClientIP != "127.0.0.1" ||
		!logInput.CreatedAt.Equal(createdAt) {
		t.Fatalf("operation log input = %+v", logInput)
	}
	if logInput.StatusCode == nil || *logInput.StatusCode != http.StatusNoContent {
		t.Fatalf("status code = %+v, want 204", logInput.StatusCode)
	}
	if len(logInput.Changes) != 1 ||
		logInput.Changes[0].Field != "returned" ||
		logInput.Changes[0].After != true {
		t.Fatalf("changes = %+v", logInput.Changes)
	}
	if len(logInput.Viewers) != 2 ||
		logInput.Viewers[0].SystemAccountID != "sys_owner" ||
		logInput.Viewers[1].SystemAccountID != "sys_grantee" {
		t.Fatalf("viewers = %+v", logInput.Viewers)
	}
}

func TestManagementMyAuthorizationReturnHandlerUsesSelfScope(t *testing.T) {
	service := &managementAuthorizationCreateServiceStub{
		returnFound: true,
		returnResult: managementauthorizations.Summary{
			ID:                           "rauthgrant_main",
			ResourceType:                 "group",
			ResourceID:                   "grp_owner",
			ResourceOwnerSystemAccountID: "sys_owner",
			GranteeType:                  "system_account",
			GranteeSystemAccountID:       "sys_grantee",
			Scope:                        "use",
			Status:                       "returned",
			AuthorizationSources:         []port.ManagementResourceAuthorizationSourceSummary{},
			Usage:                        port.ManagementAccountUsageSummary{},
			CreatedBy:                    "sys_owner",
			CreatedAt:                    time.Date(2026, 7, 9, 9, 30, 0, 0, time.UTC),
			UpdatedAt:                    time.Date(2026, 7, 9, 9, 30, 0, 0, time.UTC),
		},
	}
	handler := newManagementAuthorizationReturnHandler(service, managementAuthorizationScopeSelf)
	req := httptest.NewRequest(http.MethodDelete, "/__aisys__/api/my-authorizations/rauthgrant_main/return?systemAccountId=sys_other", nil)
	req = managementAuthorizationRequestWithURLParam(req, "id", "rauthgrant_main")
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_grantee",
		Username:        "grantee",
		Role:            "user",
		SessionID:       "sess_grantee",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rec.Code, rec.Body.String())
	}
	if !service.returnCalled ||
		service.returnInput.AuthorizationID != "rauthgrant_main" ||
		service.returnInput.GranteeSystemAccountID != "sys_grantee" ||
		service.returnInput.ActorSystemAccountID != "sys_grantee" {
		t.Fatalf("return input = %+v", service.returnInput)
	}
}

func TestManagementResourceAuthorizationReturnHandlerReturnsAndWritesOperationLog(t *testing.T) {
	createdAt := time.Date(2026, 7, 9, 10, 30, 0, 0, time.UTC)
	queueStub := &operationLogQueueStub{}
	service := &managementAuthorizationCreateServiceStub{
		resourceReturnFound: true,
		resourceReturnResult: managementauthorizations.Summary{
			ID:                             "rauthgrant_main",
			ResourceType:                   "account",
			ResourceID:                     "acct_owner",
			ResourceName:                   "主账号",
			ResourceOwnerSystemAccountID:   "sys_owner",
			ResourceOwnerSystemAccountName: "资源归属人",
			GranteeType:                    "system_account",
			GranteeSystemAccountID:         "sys_grantee",
			GranteeSystemAccountName:       "被授权人",
			Scope:                          "use",
			Status:                         "returned",
			AuthorizationSources:           []port.ManagementResourceAuthorizationSourceSummary{},
			Usage:                          port.ManagementAccountUsageSummary{},
			CreatedBy:                      "sys_owner",
			CreatedAt:                      createdAt,
			UpdatedAt:                      createdAt,
		},
	}
	handler := newManagementResourceAuthorizationReturnHandler(
		service,
		managementAuthorizationScopeAdmin,
		"account",
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			Now:      func() time.Time { return createdAt },
			NewLogID: func() string { return "oplog_account_authorization_return" },
		}),
	)
	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/accounts/acct_authorized/return-authorization?systemAccountId=sys_grantee", nil)
	req.RemoteAddr = "127.0.0.1:23456"
	req = managementAuthorizationRequestWithURLParam(req, "id", "acct_authorized")
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_account_authorization_return"))
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_admin",
		Username:        "admin",
		DisplayName:     "管理员",
		Role:            "admin",
		SessionID:       "sess_admin",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rec.Code, rec.Body.String())
	}
	if !service.resourceReturnCalled ||
		service.resourceReturnInput.ResourceType != "account" ||
		service.resourceReturnInput.ResourceID != "acct_authorized" ||
		service.resourceReturnInput.GranteeSystemAccountID != "sys_grantee" ||
		service.resourceReturnInput.ActorSystemAccountID != "sys_admin" {
		t.Fatalf("resource return input = %+v", service.resourceReturnInput)
	}
	if queueStub.calls != 1 {
		t.Fatalf("operation log queue calls = %d, want 1", queueStub.calls)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("DecodeWriteTaskPayload() error = %v", err)
	}
	if logInput.ID != "oplog_account_authorization_return" ||
		logInput.TraceID != "req_account_authorization_return" ||
		logInput.ActorSystemAccountID != "sys_admin" ||
		logInput.OperationScopeSystemAccountID != "sys_grantee" ||
		logInput.Mode != "admin" ||
		logInput.OperationKey != "accounts.return_authorization" ||
		logInput.ResourceType != "authorization" ||
		logInput.ResourceID != "rauthgrant_main" ||
		logInput.ResourceName != "主账号" ||
		logInput.Summary != "归还授权账户：主账号" ||
		logInput.Method != http.MethodPost ||
		logInput.Path != "/__aisys__/api/accounts/acct_authorized/return-authorization" ||
		logInput.ClientIP != "127.0.0.1" ||
		!logInput.CreatedAt.Equal(createdAt) {
		t.Fatalf("operation log input = %+v", logInput)
	}
	if logInput.StatusCode == nil || *logInput.StatusCode != http.StatusNoContent {
		t.Fatalf("status code = %+v, want 204", logInput.StatusCode)
	}
	if len(logInput.Changes) != 1 ||
		logInput.Changes[0].Field != "returned" ||
		logInput.Changes[0].Label != "归还授权账户" ||
		logInput.Changes[0].After != true {
		t.Fatalf("changes = %+v", logInput.Changes)
	}
}

func TestManagementMyResourceAuthorizationReturnHandlerUsesSelfScope(t *testing.T) {
	service := &managementAuthorizationCreateServiceStub{
		resourceReturnFound: true,
		resourceReturnResult: managementauthorizations.Summary{
			ID:                           "rauthgrant_group",
			ResourceType:                 "group",
			ResourceID:                   "grp_owner",
			ResourceOwnerSystemAccountID: "sys_owner",
			GranteeType:                  "system_account",
			GranteeSystemAccountID:       "sys_grantee",
			Scope:                        "use",
			Status:                       "returned",
			AuthorizationSources:         []port.ManagementResourceAuthorizationSourceSummary{},
			Usage:                        port.ManagementAccountUsageSummary{},
			CreatedBy:                    "sys_owner",
			CreatedAt:                    time.Date(2026, 7, 9, 10, 30, 0, 0, time.UTC),
			UpdatedAt:                    time.Date(2026, 7, 9, 10, 30, 0, 0, time.UTC),
		},
	}
	handler := newManagementResourceAuthorizationReturnHandler(service, managementAuthorizationScopeSelf, "group")
	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/my-groups/grp_authorized/return-authorization?systemAccountId=sys_other", nil)
	req = managementAuthorizationRequestWithURLParam(req, "id", "grp_authorized")
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_grantee",
		Username:        "grantee",
		Role:            "user",
		SessionID:       "sess_grantee",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rec.Code, rec.Body.String())
	}
	if !service.resourceReturnCalled ||
		service.resourceReturnInput.ResourceType != "group" ||
		service.resourceReturnInput.ResourceID != "grp_authorized" ||
		service.resourceReturnInput.GranteeSystemAccountID != "sys_grantee" ||
		service.resourceReturnInput.ActorSystemAccountID != "sys_grantee" {
		t.Fatalf("resource return input = %+v", service.resourceReturnInput)
	}
}

func TestManagementResourceAuthorizationReturnHandlerErrors(t *testing.T) {
	for _, tt := range []struct {
		name         string
		resourceType string
		scope        managementAuthorizationScope
		auth         managementauth.Context
		query        string
		urlParam     string
		found        bool
		wantCode     int
		wantMsg      string
	}{
		{
			name:         "account not found",
			resourceType: "account",
			scope:        managementAuthorizationScopeAdmin,
			auth:         managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"},
			query:        "?systemAccountId=sys_grantee",
			urlParam:     "acct_missing",
			wantCode:     http.StatusNotFound,
			wantMsg:      "授权账户不存在或不可归还",
		},
		{
			name:         "group invalid query",
			resourceType: "group",
			scope:        managementAuthorizationScopeAdmin,
			auth:         managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"},
			query:        "?systemAccountId=",
			urlParam:     "grp_authorized",
			wantCode:     http.StatusBadRequest,
			wantMsg:      "查询参数不合法",
		},
		{
			name:         "admin route requires admin",
			resourceType: "account",
			scope:        managementAuthorizationScopeAdmin,
			auth:         managementauth.Context{SystemAccountID: "sys_user", Role: "user"},
			urlParam:     "acct_authorized",
			wantCode:     http.StatusForbidden,
			wantMsg:      "需要管理员权限",
		},
		{
			name:         "empty resource id",
			resourceType: "group",
			scope:        managementAuthorizationScopeSelf,
			auth:         managementauth.Context{SystemAccountID: "sys_grantee", Role: "user"},
			wantCode:     http.StatusBadRequest,
			wantMsg:      "授权分组 ID 不合法",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementAuthorizationCreateServiceStub{resourceReturnFound: tt.found}
			handler := newManagementResourceAuthorizationReturnHandler(service, tt.scope, tt.resourceType)
			req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/resources/"+tt.urlParam+"/return-authorization"+tt.query, nil)
			if tt.urlParam != "" {
				req = managementAuthorizationRequestWithURLParam(req, "id", tt.urlParam)
			}
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, tt.auth))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantCode, rec.Body.String())
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body["message"] != tt.wantMsg {
				t.Fatalf("message = %q, want %q", body["message"], tt.wantMsg)
			}
		})
	}
}

func TestManagementAuthorizationRevokeHandlerRevokesAndWritesOperationLog(t *testing.T) {
	createdAt := time.Date(2026, 7, 9, 12, 30, 0, 0, time.UTC)
	queueStub := &operationLogQueueStub{}
	service := &managementAuthorizationCreateServiceStub{
		revokeFound: true,
		revokeResult: managementauthorizations.Summary{
			ID:                             "rauthgrant_main",
			ResourceType:                   "account",
			ResourceID:                     "acct_main",
			ResourceName:                   "主账号",
			ResourceOwnerSystemAccountID:   "sys_owner",
			ResourceOwnerSystemAccountName: "资源归属人",
			GranteeType:                    "system_account",
			GranteeSystemAccountID:         "sys_grantee",
			GranteeSystemAccountName:       "被授权人",
			Scope:                          "use",
			Status:                         "revoked",
			AuthorizationSources:           []port.ManagementResourceAuthorizationSourceSummary{},
			Usage:                          port.ManagementAccountUsageSummary{},
			CreatedBy:                      "sys_owner",
			CreatedAt:                      createdAt,
			UpdatedAt:                      createdAt,
		},
	}
	handler := newManagementAuthorizationRevokeHandler(
		service,
		managementAuthorizationScopeAdmin,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			Now:      func() time.Time { return createdAt },
			NewLogID: func() string { return "oplog_authorization_revoke" },
		}),
	)
	req := httptest.NewRequest(http.MethodDelete, "/__aisys__/api/authorizations/rauthgrant_main?systemAccountId=sys_owner", nil)
	req.RemoteAddr = "127.0.0.1:34567"
	req = managementAuthorizationRequestWithURLParam(req, "id", "rauthgrant_main")
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_authorization_revoke"))
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_admin",
		Username:        "admin",
		DisplayName:     "管理员",
		Role:            "admin",
		SessionID:       "sess_admin",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if !service.revokeCalled ||
		service.revokeInput.AuthorizationID != "rauthgrant_main" ||
		service.revokeInput.ActorSystemAccountID != "sys_admin" ||
		service.revokeInput.ActorRole != "admin" ||
		service.revokeInput.ScopedSystemAccountID != "sys_owner" {
		t.Fatalf("revoke input = %+v", service.revokeInput)
	}
	var body struct {
		Data managementauthorizations.Summary `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.ID != "rauthgrant_main" || body.Data.Status != "revoked" {
		t.Fatalf("response data = %+v", body.Data)
	}
	if queueStub.calls != 1 {
		t.Fatalf("operation log queue calls = %d, want 1", queueStub.calls)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("DecodeWriteTaskPayload() error = %v", err)
	}
	if logInput.ID != "oplog_authorization_revoke" ||
		logInput.TraceID != "req_authorization_revoke" ||
		logInput.ActorSystemAccountID != "sys_admin" ||
		logInput.OperationScopeSystemAccountID != "sys_owner" ||
		logInput.Mode != "admin" ||
		logInput.Module != "authorizations" ||
		logInput.Action != "revoke" ||
		logInput.OperationKey != "authorizations.revoke" ||
		logInput.ResourceType != "authorization" ||
		logInput.ResourceID != "rauthgrant_main" ||
		logInput.ResourceName != "主账号" ||
		logInput.Summary != "回收资源授权：主账号 -> 被授权人" ||
		logInput.Method != http.MethodDelete ||
		logInput.Path != "/__aisys__/api/authorizations/rauthgrant_main" ||
		logInput.ClientIP != "127.0.0.1" ||
		!logInput.CreatedAt.Equal(createdAt) {
		t.Fatalf("operation log input = %+v", logInput)
	}
	if logInput.StatusCode == nil || *logInput.StatusCode != http.StatusOK {
		t.Fatalf("status code = %+v, want 200", logInput.StatusCode)
	}
	if len(logInput.Changes) != 1 ||
		logInput.Changes[0].Field != "revoked" ||
		logInput.Changes[0].After != true {
		t.Fatalf("changes = %+v", logInput.Changes)
	}
}

func TestManagementMyAuthorizationRevokeHandlerUsesSelfScope(t *testing.T) {
	service := &managementAuthorizationCreateServiceStub{
		revokeFound: true,
		revokeResult: managementauthorizations.Summary{
			ID:                           "rauthgrant_main",
			ResourceType:                 "group",
			ResourceID:                   "grp_owner",
			ResourceOwnerSystemAccountID: "sys_owner",
			GranteeType:                  "team",
			GranteeTeamID:                "team_ops",
			Scope:                        "use",
			Status:                       "revoked",
			AuthorizationSources:         []port.ManagementResourceAuthorizationSourceSummary{},
			Usage:                        port.ManagementAccountUsageSummary{},
			CreatedAt:                    time.Date(2026, 7, 9, 12, 30, 0, 0, time.UTC),
			UpdatedAt:                    time.Date(2026, 7, 9, 12, 30, 0, 0, time.UTC),
		},
	}
	handler := newManagementAuthorizationRevokeHandler(service, managementAuthorizationScopeSelf)
	req := httptest.NewRequest(http.MethodDelete, "/__aisys__/api/my-authorizations/rauthgrant_main?systemAccountId=sys_other", nil)
	req = managementAuthorizationRequestWithURLParam(req, "id", "rauthgrant_main")
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_owner",
		Username:        "owner",
		Role:            "user",
		SessionID:       "sess_owner",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if !service.revokeCalled ||
		service.revokeInput.AuthorizationID != "rauthgrant_main" ||
		service.revokeInput.ActorSystemAccountID != "sys_owner" ||
		service.revokeInput.ActorRole != "user" ||
		service.revokeInput.ScopedSystemAccountID != "sys_owner" {
		t.Fatalf("revoke input = %+v", service.revokeInput)
	}
}

func TestManagementAuthorizationRevokeHandlerRejectsInvalidOrMissingRecord(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		found    bool
		wantCode int
		wantMsg  string
	}{
		{name: "empty id", id: " ", found: true, wantCode: http.StatusBadRequest, wantMsg: "授权记录 ID 不合法"},
		{name: "missing", id: "rauthgrant_missing", found: false, wantCode: http.StatusNotFound, wantMsg: "授权记录不存在"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementAuthorizationCreateServiceStub{revokeFound: tt.found}
			handler := newManagementAuthorizationRevokeHandler(service, managementAuthorizationScopeAdmin)
			req := httptest.NewRequest(http.MethodDelete, "/__aisys__/api/authorizations/"+url.PathEscape(tt.id), nil)
			req = managementAuthorizationRequestWithURLParam(req, "id", tt.id)
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
				SystemAccountID: "sys_admin",
				Username:        "admin",
				Role:            "admin",
				SessionID:       "sess_admin",
			}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantCode, rec.Body.String())
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body["message"] != tt.wantMsg {
				t.Fatalf("message = %q, want %q", body["message"], tt.wantMsg)
			}
		})
	}
}

func TestManagementAuthorizationListHandlerParsesAdminQueryAndResponds(t *testing.T) {
	createdAt := time.Date(2026, 7, 9, 10, 30, 0, 0, time.UTC)
	service := &managementAuthorizationCreateServiceStub{
		listResult: managementauthorizations.ListResult{
			Items: []managementauthorizations.ListItem{{
				ID:                           "rauthgrant_main",
				ResourceType:                 "account",
				ResourceID:                   "acct_main",
				ResourceOwnerSystemAccountID: "sys_owner",
				GranteeType:                  "system_account",
				GranteeSystemAccountID:       "sys_grantee",
				Status:                       "active",
				CreatedAt:                    createdAt,
				Permissions: managementauthorizations.Permissions{
					CanEdit:      true,
					CanAuthorize: true,
				},
				SourceSummary: managementauthorizations.SourceSummary{
					ActiveSourceCount: 1,
					HasManual:         true,
					TeamSources:       []managementauthorizations.TeamSourceItem{},
				},
			}},
			Total:    2,
			HasMore:  true,
			Page:     2,
			PageSize: 1,
		},
	}
	handler := newManagementAuthorizationListHandler(service, managementAuthorizationScopeAdmin)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/authorizations?systemAccountId=all&keyword=%20%E4%B8%BB%20&resourceType=account&resourceId=acct_main&resourceOwnerSystemAccountId=sys_owner&granteeSystemAccountId=sys_grantee&teamId=team_ops&status=active&direction=inbound&sourceType=team&startDate=2026-07-01&endDate=2026-07-09&page=2&pageSize=1", nil)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_admin",
		Username:        "admin",
		Role:            "admin",
		SessionID:       "sess_admin",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if !service.listCalled ||
		service.listInput.ActorSystemAccountID != "sys_admin" ||
		service.listInput.ActorRole != "admin" ||
		service.listInput.ScopedSystemAccountID != "" ||
		service.listInput.ResourceType != "account" ||
		service.listInput.ResourceID != "acct_main" ||
		service.listInput.ResourceOwnerSystemAccountID != "sys_owner" ||
		service.listInput.GranteeSystemAccountID != "sys_grantee" ||
		service.listInput.TeamID != "team_ops" ||
		service.listInput.Status != "active" ||
		service.listInput.Direction != "" ||
		service.listInput.SourceType != "team" ||
		service.listInput.Keyword != "主" ||
		service.listInput.Page != 2 ||
		service.listInput.PageSize != 1 {
		t.Fatalf("list input = %+v", service.listInput)
	}
	var body struct {
		Data managementauthorizations.ListResult `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Data.Items) != 1 ||
		body.Data.Items[0].ID != "rauthgrant_main" ||
		body.Data.Items[0].SourceSummary.ActiveSourceCount != 1 ||
		body.Data.Items[0].SourceSummary.TeamSources == nil {
		t.Fatalf("response data = %+v", body.Data)
	}
}

func TestManagementMyAuthorizationListHandlerUsesSelfScopeAndDirection(t *testing.T) {
	service := &managementAuthorizationCreateServiceStub{
		listResult: managementauthorizations.ListResult{Items: []managementauthorizations.ListItem{}, Page: 1, PageSize: 50},
	}
	handler := newManagementAuthorizationListHandler(service, managementAuthorizationScopeSelf)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-authorizations?systemAccountId=&direction=inbound&page=1&pageSize=20", nil)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_grantee",
		Username:        "grantee",
		Role:            "user",
		SessionID:       "sess_grantee",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if !service.listCalled ||
		service.listInput.ActorSystemAccountID != "sys_grantee" ||
		service.listInput.ScopedSystemAccountID != "sys_grantee" ||
		service.listInput.Direction != "inbound" ||
		service.listInput.PageSize != 20 {
		t.Fatalf("list input = %+v", service.listInput)
	}
}

func TestManagementAuthorizationUsageOverviewHandlersParseQueryAndScope(t *testing.T) {
	service := &managementAuthorizationCreateServiceStub{
		teamUsageResult: managementauthorizations.TeamUsageOverview{
			Range:    port.ManagementAccountUsageStatsRange{StartDate: "2026-07-01", EndDate: "2026-07-09", Days: 9, MaxDays: 31},
			Rows:     []port.ManagementAuthorizationTeamUsageRow{{ID: "team_ops:account:acct_main", TeamID: "team_ops", Usage: port.ManagementAuthorizationUsageRowSummary{RequestCount: 2}}},
			Total:    1,
			Page:     2,
			PageSize: 1,
		},
		userUsageResult: managementauthorizations.UserUsageOverview{
			Range:    port.ManagementAccountUsageStatsRange{StartDate: "2026-07-01", EndDate: "2026-07-09", Days: 9, MaxDays: 31},
			Rows:     []port.ManagementAuthorizationUserUsageRow{{ID: "sys_grantee:group:grp_main", UserName: "Grantee"}},
			Total:    1,
			Page:     1,
			PageSize: 20,
		},
	}
	teamHandler := newManagementAuthorizationTeamUsageOverviewHandler(service, managementAuthorizationScopeAdmin)
	teamReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/authorizations/usage/team-details?systemAccountId=sys_owner&resourceType=account&resourceId=acct_main&teamId=team_ops&startDate=2026-07-01&endDate=2026-07-09&page=2&pageSize=1", nil)
	teamReq = teamReq.WithContext(context.WithValue(teamReq.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_admin",
		Username:        "admin",
		Role:            "admin",
		SessionID:       "sess_admin",
	}))
	teamRec := httptest.NewRecorder()

	teamHandler.ServeHTTP(teamRec, teamReq)

	if teamRec.Code != http.StatusOK {
		t.Fatalf("team status = %d, want 200; body = %s", teamRec.Code, teamRec.Body.String())
	}
	if !service.teamUsageCalled ||
		service.teamUsageInput.ActorSystemAccountID != "sys_admin" ||
		service.teamUsageInput.ActorRole != "admin" ||
		service.teamUsageInput.ScopedSystemAccountID != "sys_owner" ||
		service.teamUsageInput.ResourceType != "account" ||
		service.teamUsageInput.ResourceID != "acct_main" ||
		service.teamUsageInput.TeamID != "team_ops" ||
		service.teamUsageInput.StartDate != "2026-07-01" ||
		service.teamUsageInput.EndDate != "2026-07-09" ||
		service.teamUsageInput.Page != 2 ||
		service.teamUsageInput.PageSize != 1 {
		t.Fatalf("team usage input = %+v", service.teamUsageInput)
	}
	var teamBody struct {
		Data managementauthorizations.TeamUsageOverview `json:"data"`
	}
	if err := json.NewDecoder(teamRec.Body).Decode(&teamBody); err != nil {
		t.Fatalf("decode team usage response: %v", err)
	}
	if teamBody.Data.Rows[0].ID != "team_ops:account:acct_main" {
		t.Fatalf("team usage response = %+v", teamBody.Data)
	}

	userHandler := newManagementAuthorizationUserUsageOverviewHandler(service, managementAuthorizationScopeSelf)
	userReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-authorizations/usage/user-details?systemAccountId=&systemAccountId=sys_owner&resourceType=group&granteeSystemAccountId=sys_grantee", nil)
	userReq = userReq.WithContext(context.WithValue(userReq.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_grantee",
		Username:        "grantee",
		Role:            "user",
		SessionID:       "sess_grantee",
	}))
	userRec := httptest.NewRecorder()

	userHandler.ServeHTTP(userRec, userReq)

	if userRec.Code != http.StatusOK {
		t.Fatalf("user status = %d, want 200; body = %s", userRec.Code, userRec.Body.String())
	}
	if !service.userUsageCalled ||
		service.userUsageInput.ActorSystemAccountID != "sys_grantee" ||
		service.userUsageInput.ActorRole != "user" ||
		service.userUsageInput.ScopedSystemAccountID != "sys_grantee" ||
		service.userUsageInput.ResourceType != "group" ||
		service.userUsageInput.GranteeSystemAccountID != "sys_grantee" {
		t.Fatalf("user usage input = %+v", service.userUsageInput)
	}
}

func TestManagementAuthorizationUsageSummaryHandlersParseQueryAndScope(t *testing.T) {
	service := &managementAuthorizationCreateServiceStub{
		teamUsageSummaryResult: managementauthorizations.UsageSummary{
			Range:   port.ManagementAccountUsageStatsRange{StartDate: "2026-07-01", EndDate: "2026-07-09", Days: 9, MaxDays: 31},
			Summary: port.ManagementAuthorizationUsageAggregateSummary{RequestCount: 3, InputTokens: 4, CacheWriteTokens: 5, TotalTokens: 6, TotalCost: 0.7},
		},
		userUsageSummaryResult: managementauthorizations.UsageSummary{
			Range:   port.ManagementAccountUsageStatsRange{StartDate: "2026-07-01", EndDate: "2026-07-09", Days: 9, MaxDays: 31},
			Summary: port.ManagementAuthorizationUsageAggregateSummary{RequestCount: 8},
		},
	}
	teamHandler := newManagementAuthorizationUsageSummaryHandler(service, managementAuthorizationScopeAdmin, false)
	teamReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/authorizations/usage/team-summary?systemAccountId=sys_owner&resourceType=account&resourceId=acct_main&teamId=team_ops&startDate=2026-07-01&endDate=2026-07-09", nil)
	teamReq = teamReq.WithContext(context.WithValue(teamReq.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_admin",
		Username:        "admin",
		Role:            "admin",
		SessionID:       "sess_admin",
	}))
	teamRec := httptest.NewRecorder()

	teamHandler.ServeHTTP(teamRec, teamReq)

	if teamRec.Code != http.StatusOK {
		t.Fatalf("team summary status = %d, want 200; body = %s", teamRec.Code, teamRec.Body.String())
	}
	if !service.teamUsageSummaryCalled ||
		service.teamUsageSummaryInput.ActorSystemAccountID != "sys_admin" ||
		service.teamUsageSummaryInput.ScopedSystemAccountID != "sys_owner" ||
		service.teamUsageSummaryInput.ResourceType != "account" ||
		service.teamUsageSummaryInput.ResourceID != "acct_main" ||
		service.teamUsageSummaryInput.TeamID != "team_ops" ||
		service.teamUsageSummaryInput.StartDate != "2026-07-01" ||
		service.teamUsageSummaryInput.EndDate != "2026-07-09" {
		t.Fatalf("team summary input = %+v", service.teamUsageSummaryInput)
	}
	var teamBody struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(teamRec.Body).Decode(&teamBody); err != nil {
		t.Fatalf("decode team summary response: %v", err)
	}
	if len(teamBody.Data) != 2 || teamBody.Data["range"] == nil || teamBody.Data["summary"] == nil {
		t.Fatalf("team summary response keys = %+v", teamBody.Data)
	}

	userHandler := newManagementAuthorizationUsageSummaryHandler(service, managementAuthorizationScopeSelf, true)
	userReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-authorizations/usage/user-summary?systemAccountId=&systemAccountId=sys_owner&resourceType=group&granteeSystemAccountId=sys_grantee", nil)
	userReq = userReq.WithContext(context.WithValue(userReq.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_grantee",
		Username:        "grantee",
		Role:            "user",
		SessionID:       "sess_grantee",
	}))
	userRec := httptest.NewRecorder()

	userHandler.ServeHTTP(userRec, userReq)

	if userRec.Code != http.StatusOK {
		t.Fatalf("user summary status = %d, want 200; body = %s", userRec.Code, userRec.Body.String())
	}
	if !service.userUsageSummaryCalled ||
		service.userUsageSummaryInput.ActorSystemAccountID != "sys_grantee" ||
		service.userUsageSummaryInput.ScopedSystemAccountID != "sys_grantee" ||
		service.userUsageSummaryInput.ResourceType != "group" ||
		service.userUsageSummaryInput.GranteeSystemAccountID != "sys_grantee" {
		t.Fatalf("user summary input = %+v", service.userUsageSummaryInput)
	}
}

func TestManagementAuthorizationUsageHandlersRejectContractDrift(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		userSummary bool
		paginated   bool
	}{
		{name: "team rows reject user filter", path: "/usage/team-details?granteeSystemAccountId=sys_user", paginated: true},
		{name: "team summary reject user filter", path: "/usage/team-summary?granteeSystemAccountId=sys_user"},
		{name: "team summary reject pagination", path: "/usage/team-summary?page=1"},
		{name: "user summary reject unknown", path: "/usage/user-summary?includeSummary=true", userSummary: true},
		{name: "summary requires resource type", path: "/usage/user-summary?resourceId=acct_main", userSummary: true},
		{name: "rows requires resource type", path: "/usage/user-details?resourceId=acct_main", userSummary: true, paginated: true},
		{name: "reject duplicate scalar", path: "/usage/user-summary?teamId=one&teamId=two", userSummary: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementAuthorizationCreateServiceStub{}
			var handler http.Handler
			if test.paginated {
				if test.userSummary {
					handler = newManagementAuthorizationUserUsageOverviewHandler(service, managementAuthorizationScopeAdmin)
				} else {
					handler = newManagementAuthorizationTeamUsageOverviewHandler(service, managementAuthorizationScopeAdmin)
				}
			} else {
				handler = newManagementAuthorizationUsageSummaryHandler(service, managementAuthorizationScopeAdmin, test.userSummary)
			}
			req := httptest.NewRequest(http.MethodGet, test.path, nil)
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
				SystemAccountID: "sys_admin",
				Role:            "admin",
			}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
			}
			if service.teamUsageCalled || service.userUsageCalled || service.teamUsageSummaryCalled || service.userUsageSummaryCalled {
				t.Fatal("service should not be called for invalid query")
			}
		})
	}
}

func TestManagementAuthorizationUsageSummaryHandlerRequiresAdminAuthentication(t *testing.T) {
	service := &managementAuthorizationCreateServiceStub{}
	handler := newManagementAuthorizationUsageSummaryHandler(service, managementAuthorizationScopeAdmin, false)
	tests := []struct {
		name    string
		context *managementauth.Context
		status  int
	}{
		{name: "anonymous", status: http.StatusUnauthorized},
		{name: "ordinary user", context: &managementauth.Context{SystemAccountID: "sys_user", Role: "user"}, status: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/usage/team-summary", nil)
			if test.context != nil {
				req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, *test.context))
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != test.status {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, test.status, rec.Body.String())
			}
		})
	}
	if service.teamUsageSummaryCalled {
		t.Fatal("service should not be called without admin authentication")
	}
}

func TestManagementAuthorizationUsageOverviewHandlerRejectsInvalidQuery(t *testing.T) {
	service := &managementAuthorizationCreateServiceStub{}
	handler := newManagementAuthorizationTeamUsageOverviewHandler(service, managementAuthorizationScopeAdmin)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/authorizations/usage/team-details?resourceType=invalid", nil)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_admin",
		Username:        "admin",
		Role:            "admin",
		SessionID:       "sess_admin",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	if service.teamUsageCalled {
		t.Fatal("service should not be called for invalid query")
	}
}

func TestManagementAuthorizationUsageHandlerParsesQueryAndScope(t *testing.T) {
	createdAt := time.Date(2026, 7, 9, 11, 30, 0, 0, time.UTC)
	service := &managementAuthorizationCreateServiceStub{
		usageFound: true,
		usageResult: managementauthorizations.UsageDetail{
			Detail: managementauthorizations.Detail{
				Summary: managementauthorizations.Summary{
					ID:                           "rauthgrant_main",
					ResourceType:                 "account",
					ResourceID:                   "acct_main",
					ResourceOwnerSystemAccountID: "sys_owner",
					GranteeType:                  "system_account",
					GranteeSystemAccountID:       "sys_grantee",
					Scope:                        "use",
					Status:                       "active",
					AuthorizationSources:         []port.ManagementResourceAuthorizationSourceSummary{},
					Usage:                        port.ManagementAccountUsageSummary{RequestCount: 8},
					CreatedAt:                    createdAt,
					UpdatedAt:                    createdAt,
				},
				Permissions: managementauthorizations.Permissions{CanEdit: true, CanAuthorize: true},
			},
			UsageBySystemAccount: []port.ManagementResourceAuthorizationUsageDetail{{
				SystemAccountID:               "sys_grantee",
				ManagementAccountUsageSummary: port.ManagementAccountUsageSummary{RequestCount: 8},
				RangeUsage:                    port.ManagementAccountUsageSummary{RequestCount: 8},
			}},
			UsageBySystemAccountTotal:    1,
			UsageBySystemAccountPage:     2,
			UsageBySystemAccountPageSize: 5,
			UsageRange:                   port.ManagementAccountUsageStatsRange{StartDate: "2026-07-01", EndDate: "2026-07-09", Days: 9, MaxDays: 31},
		},
	}
	handler := newManagementAuthorizationUsageHandler(service, managementAuthorizationScopeAdmin)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/authorizations/rauthgrant_main/usage?systemAccountId=sys_owner&startDate=2026-07-01&endDate=2026-07-09&page=2&pageSize=5", nil)
	req = managementAuthorizationRequestWithURLParam(req, "id", "rauthgrant_main")
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_admin",
		Username:        "admin",
		Role:            "admin",
		SessionID:       "sess_admin",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if !service.usageCalled ||
		service.usageInput.AuthorizationID != "rauthgrant_main" ||
		service.usageInput.ActorSystemAccountID != "sys_admin" ||
		service.usageInput.ActorRole != "admin" ||
		service.usageInput.ScopedSystemAccountID != "sys_owner" ||
		service.usageInput.StartDate != "2026-07-01" ||
		service.usageInput.EndDate != "2026-07-09" ||
		service.usageInput.Page != 2 ||
		service.usageInput.PageSize != 5 {
		t.Fatalf("usage input = %+v", service.usageInput)
	}
	var body struct {
		Data managementauthorizations.UsageDetail `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode usage response: %v", err)
	}
	if body.Data.ID != "rauthgrant_main" || body.Data.UsageBySystemAccount[0].RequestCount != 8 || body.Data.UsageRange.Days != 9 {
		t.Fatalf("usage response = %+v", body.Data)
	}

	selfHandler := newManagementAuthorizationUsageHandler(service, managementAuthorizationScopeSelf)
	selfReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-authorizations/rauthgrant_main/usage?systemAccountId=sys_owner", nil)
	selfReq = managementAuthorizationRequestWithURLParam(selfReq, "id", "rauthgrant_main")
	selfReq = selfReq.WithContext(context.WithValue(selfReq.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_grantee",
		Username:        "grantee",
		Role:            "user",
		SessionID:       "sess_grantee",
	}))
	selfRec := httptest.NewRecorder()

	selfHandler.ServeHTTP(selfRec, selfReq)

	if selfRec.Code != http.StatusOK {
		t.Fatalf("self status = %d, want 200; body = %s", selfRec.Code, selfRec.Body.String())
	}
	if service.usageInput.ActorSystemAccountID != "sys_grantee" ||
		service.usageInput.ActorRole != "user" ||
		service.usageInput.ScopedSystemAccountID != "sys_grantee" {
		t.Fatalf("self usage input = %+v", service.usageInput)
	}
}

func TestManagementAuthorizationUsageHandlerRejectsInvalidQueryOrMissingRecord(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		id       string
		found    bool
		wantCode int
		wantMsg  string
	}{
		{name: "invalid query", target: "/__aisys__/api/authorizations/rauthgrant_main/usage?page=0", id: "rauthgrant_main", found: true, wantCode: http.StatusBadRequest, wantMsg: "查询参数不合法"},
		{name: "empty id", target: "/__aisys__/api/authorizations/%20/usage", id: " ", found: true, wantCode: http.StatusBadRequest, wantMsg: "授权记录 ID 不合法"},
		{name: "missing", target: "/__aisys__/api/authorizations/rauthgrant_missing/usage", id: "rauthgrant_missing", found: false, wantCode: http.StatusNotFound, wantMsg: "授权记录不存在"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementAuthorizationCreateServiceStub{usageFound: tt.found}
			handler := newManagementAuthorizationUsageHandler(service, managementAuthorizationScopeAdmin)
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			req = managementAuthorizationRequestWithURLParam(req, "id", tt.id)
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
				SystemAccountID: "sys_admin",
				Username:        "admin",
				Role:            "admin",
				SessionID:       "sess_admin",
			}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantCode, rec.Body.String())
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body["message"] != tt.wantMsg {
				t.Fatalf("message = %q, want %q", body["message"], tt.wantMsg)
			}
		})
	}
}

func TestManagementAuthorizationListHandlerRejectsInvalidQuery(t *testing.T) {
	tests := []string{
		"/__aisys__/api/authorizations?systemAccountId=",
		"/__aisys__/api/authorizations?status=deleted",
		"/__aisys__/api/authorizations?startDate=2026-99-99",
		"/__aisys__/api/authorizations?page=0",
	}
	for _, target := range tests {
		t.Run(target, func(t *testing.T) {
			service := &managementAuthorizationCreateServiceStub{}
			handler := newManagementAuthorizationListHandler(service, managementAuthorizationScopeAdmin)
			req := httptest.NewRequest(http.MethodGet, target, nil)
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
				SystemAccountID: "sys_admin",
				Username:        "admin",
				Role:            "admin",
				SessionID:       "sess_admin",
			}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
			}
			if service.listCalled {
				t.Fatal("service was called for invalid query")
			}
		})
	}
}

func TestManagementAuthorizationDetailHandlerParsesScopeAndResponds(t *testing.T) {
	createdAt := time.Date(2026, 7, 9, 11, 30, 0, 0, time.UTC)
	service := &managementAuthorizationCreateServiceStub{
		getFound: true,
		getResult: managementauthorizations.Detail{
			Summary: managementauthorizations.Summary{
				ID:                           "rauthgrant_main",
				ResourceType:                 "account",
				ResourceID:                   "acct_main",
				ResourceOwnerSystemAccountID: "sys_owner",
				GranteeType:                  "system_account",
				GranteeSystemAccountID:       "sys_grantee",
				Scope:                        "use",
				Status:                       "active",
				AuthorizationSources:         []port.ManagementResourceAuthorizationSourceSummary{},
				Usage:                        port.ManagementAccountUsageSummary{},
				CreatedBy:                    "sys_owner",
				CreatedAt:                    createdAt,
				UpdatedAt:                    createdAt,
			},
			Permissions: managementauthorizations.Permissions{CanEdit: true, CanAuthorize: true},
		},
	}
	handler := newManagementAuthorizationDetailHandler(service, managementAuthorizationScopeAdmin)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/authorizations/rauthgrant_main?systemAccountId=sys_owner", nil)
	req = managementAuthorizationRequestWithURLParam(req, "id", "rauthgrant_main")
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_admin",
		Username:        "admin",
		Role:            "admin",
		SessionID:       "sess_admin",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if !service.getCalled ||
		service.getInput.AuthorizationID != "rauthgrant_main" ||
		service.getInput.ActorSystemAccountID != "sys_admin" ||
		service.getInput.ActorRole != "admin" ||
		service.getInput.ScopedSystemAccountID != "sys_owner" {
		t.Fatalf("get input = %+v", service.getInput)
	}
	var body struct {
		Data managementauthorizations.Detail `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.ID != "rauthgrant_main" || !body.Data.Permissions.CanEdit {
		t.Fatalf("response data = %+v", body.Data)
	}
}

func TestManagementMyAuthorizationDetailHandlerUsesSelfScope(t *testing.T) {
	service := &managementAuthorizationCreateServiceStub{
		getFound: true,
		getResult: managementauthorizations.Detail{
			Summary: managementauthorizations.Summary{
				ID:                           "rauthgrant_main",
				ResourceType:                 "group",
				ResourceID:                   "grp_owner",
				ResourceOwnerSystemAccountID: "sys_owner",
				GranteeType:                  "system_account",
				GranteeSystemAccountID:       "sys_grantee",
				Scope:                        "use",
				Status:                       "active",
				AuthorizationSources:         []port.ManagementResourceAuthorizationSourceSummary{},
				Usage:                        port.ManagementAccountUsageSummary{},
				CreatedAt:                    time.Date(2026, 7, 9, 11, 30, 0, 0, time.UTC),
				UpdatedAt:                    time.Date(2026, 7, 9, 11, 30, 0, 0, time.UTC),
			},
		},
	}
	handler := newManagementAuthorizationDetailHandler(service, managementAuthorizationScopeSelf)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-authorizations/rauthgrant_main?systemAccountId=sys_other", nil)
	req = managementAuthorizationRequestWithURLParam(req, "id", "rauthgrant_main")
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_grantee",
		Username:        "grantee",
		Role:            "user",
		SessionID:       "sess_grantee",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if !service.getCalled ||
		service.getInput.ActorSystemAccountID != "sys_grantee" ||
		service.getInput.ActorRole != "user" ||
		service.getInput.ScopedSystemAccountID != "sys_grantee" {
		t.Fatalf("get input = %+v", service.getInput)
	}
}

func TestManagementAuthorizationDetailHandlerRejectsInvalidOrMissingRecord(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		found    bool
		wantCode int
		wantMsg  string
	}{
		{name: "empty id", id: " ", found: true, wantCode: http.StatusBadRequest, wantMsg: "授权记录 ID 不合法"},
		{name: "missing", id: "rauthgrant_missing", found: false, wantCode: http.StatusNotFound, wantMsg: "授权记录不存在"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementAuthorizationCreateServiceStub{getFound: tt.found}
			handler := newManagementAuthorizationDetailHandler(service, managementAuthorizationScopeAdmin)
			req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/authorizations/"+url.PathEscape(tt.id), nil)
			req = managementAuthorizationRequestWithURLParam(req, "id", tt.id)
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
				SystemAccountID: "sys_admin",
				Username:        "admin",
				Role:            "admin",
				SessionID:       "sess_admin",
			}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantCode, rec.Body.String())
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body["message"] != tt.wantMsg {
				t.Fatalf("message = %q, want %q", body["message"], tt.wantMsg)
			}
		})
	}
}

func TestRouterRegistersW4ManagementAuthorizationListDetailCreateUpdateExpireReturnAndRevoke(t *testing.T) {
	service := &managementAuthorizationCreateServiceStub{
		result: managementauthorizations.Summary{
			ID:                           "rauthgrant_main",
			ResourceType:                 "group",
			ResourceID:                   "grp_owner",
			ResourceName:                 "归属分组",
			ResourceOwnerSystemAccountID: "sys_owner",
			GranteeType:                  "team",
			GranteeTeamID:                "team_ops",
			GranteeTeamName:              "运维团队",
			Scope:                        "use",
			Status:                       "active",
			AuthorizationSources:         []port.ManagementResourceAuthorizationSourceSummary{},
			Usage:                        port.ManagementAccountUsageSummary{},
			CreatedBy:                    "sys_admin",
			CreatedAt:                    time.Date(2026, 7, 9, 8, 30, 0, 0, time.UTC),
			UpdatedAt:                    time.Date(2026, 7, 9, 8, 30, 0, 0, time.UTC),
		},
		updateFound: true,
		updateResult: managementauthorizations.Summary{
			ID:                           "rauthgrant_main",
			ResourceType:                 "account",
			ResourceID:                   "acct_main",
			ResourceOwnerSystemAccountID: "sys_owner",
			GranteeType:                  "system_account",
			GranteeSystemAccountID:       "sys_grantee",
			Scope:                        "use",
			Status:                       "paused",
			AuthorizationSources:         []port.ManagementResourceAuthorizationSourceSummary{},
			Usage:                        port.ManagementAccountUsageSummary{},
			CreatedBy:                    "sys_owner",
			CreatedAt:                    time.Date(2026, 7, 9, 10, 30, 0, 0, time.UTC),
			UpdatedAt:                    time.Date(2026, 7, 9, 10, 30, 0, 0, time.UTC),
		},
		returnFound: true,
		returnResult: managementauthorizations.Summary{
			ID:                           "rauthgrant_main",
			ResourceType:                 "account",
			ResourceID:                   "acct_main",
			ResourceOwnerSystemAccountID: "sys_owner",
			GranteeType:                  "system_account",
			GranteeSystemAccountID:       "sys_grantee",
			Scope:                        "use",
			Status:                       "returned",
			AuthorizationSources:         []port.ManagementResourceAuthorizationSourceSummary{},
			Usage:                        port.ManagementAccountUsageSummary{},
			CreatedBy:                    "sys_owner",
			CreatedAt:                    time.Date(2026, 7, 9, 9, 30, 0, 0, time.UTC),
			UpdatedAt:                    time.Date(2026, 7, 9, 9, 30, 0, 0, time.UTC),
		},
		resourceReturnFound: true,
		resourceReturnResult: managementauthorizations.Summary{
			ID:                           "rauthgrant_resource",
			ResourceType:                 "account",
			ResourceID:                   "acct_owner",
			ResourceOwnerSystemAccountID: "sys_owner",
			GranteeType:                  "system_account",
			GranteeSystemAccountID:       "sys_grantee",
			Scope:                        "use",
			Status:                       "returned",
			AuthorizationSources:         []port.ManagementResourceAuthorizationSourceSummary{},
			Usage:                        port.ManagementAccountUsageSummary{},
			CreatedBy:                    "sys_owner",
			CreatedAt:                    time.Date(2026, 7, 9, 10, 30, 0, 0, time.UTC),
			UpdatedAt:                    time.Date(2026, 7, 9, 10, 30, 0, 0, time.UTC),
		},
		revokeFound: true,
		revokeResult: managementauthorizations.Summary{
			ID:                           "rauthgrant_main",
			ResourceType:                 "account",
			ResourceID:                   "acct_main",
			ResourceOwnerSystemAccountID: "sys_owner",
			GranteeType:                  "system_account",
			GranteeSystemAccountID:       "sys_grantee",
			Scope:                        "use",
			Status:                       "revoked",
			AuthorizationSources:         []port.ManagementResourceAuthorizationSourceSummary{},
			Usage:                        port.ManagementAccountUsageSummary{},
			CreatedBy:                    "sys_owner",
			CreatedAt:                    time.Date(2026, 7, 9, 12, 30, 0, 0, time.UTC),
			UpdatedAt:                    time.Date(2026, 7, 9, 12, 30, 0, 0, time.UTC),
		},
		listResult: managementauthorizations.ListResult{
			Items:    []managementauthorizations.ListItem{},
			Total:    0,
			Page:     1,
			PageSize: 50,
		},
		teamUsageResult: managementauthorizations.TeamUsageOverview{
			Range:    port.ManagementAccountUsageStatsRange{StartDate: "2026-07-09", EndDate: "2026-07-09", Days: 1, MaxDays: 31},
			Rows:     []port.ManagementAuthorizationTeamUsageRow{},
			Page:     1,
			PageSize: 20,
		},
		userUsageResult: managementauthorizations.UserUsageOverview{
			Range:    port.ManagementAccountUsageStatsRange{StartDate: "2026-07-09", EndDate: "2026-07-09", Days: 1, MaxDays: 31},
			Rows:     []port.ManagementAuthorizationUserUsageRow{},
			Page:     1,
			PageSize: 20,
		},
		teamUsageSummaryResult: managementauthorizations.UsageSummary{
			Range:   port.ManagementAccountUsageStatsRange{StartDate: "2026-07-09", EndDate: "2026-07-09", Days: 1, MaxDays: 31},
			Summary: port.ManagementAuthorizationUsageAggregateSummary{},
		},
		userUsageSummaryResult: managementauthorizations.UsageSummary{
			Range:   port.ManagementAccountUsageStatsRange{StartDate: "2026-07-09", EndDate: "2026-07-09", Days: 1, MaxDays: 31},
			Summary: port.ManagementAuthorizationUsageAggregateSummary{},
		},
		usageFound: true,
		usageResult: managementauthorizations.UsageDetail{
			Detail: managementauthorizations.Detail{
				Summary: managementauthorizations.Summary{
					ID:                           "rauthgrant_main",
					ResourceType:                 "account",
					ResourceID:                   "acct_main",
					ResourceOwnerSystemAccountID: "sys_owner",
					GranteeType:                  "system_account",
					GranteeSystemAccountID:       "sys_grantee",
					Scope:                        "use",
					Status:                       "active",
					AuthorizationSources:         []port.ManagementResourceAuthorizationSourceSummary{},
					Usage:                        port.ManagementAccountUsageSummary{},
					CreatedAt:                    time.Date(2026, 7, 9, 11, 30, 0, 0, time.UTC),
					UpdatedAt:                    time.Date(2026, 7, 9, 11, 30, 0, 0, time.UTC),
				},
			},
			UsageBySystemAccount:         []port.ManagementResourceAuthorizationUsageDetail{},
			UsageBySystemAccountPage:     1,
			UsageBySystemAccountPageSize: 200,
			UsageRange:                   port.ManagementAccountUsageStatsRange{StartDate: "2026-07-09", EndDate: "2026-07-09", Days: 1, MaxDays: 31},
		},
		getFound: true,
		getResult: managementauthorizations.Detail{
			Summary: managementauthorizations.Summary{
				ID:                           "rauthgrant_main",
				ResourceType:                 "group",
				ResourceID:                   "grp_owner",
				ResourceOwnerSystemAccountID: "sys_owner",
				GranteeType:                  "team",
				GranteeTeamID:                "team_ops",
				Scope:                        "use",
				Status:                       "active",
				AuthorizationSources:         []port.ManagementResourceAuthorizationSourceSummary{},
				Usage:                        port.ManagementAccountUsageSummary{},
				CreatedAt:                    time.Date(2026, 7, 9, 11, 30, 0, 0, time.UTC),
				UpdatedAt:                    time.Date(2026, 7, 9, 11, 30, 0, 0, time.UTC),
			},
		},
	}
	readAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_read"},
	}
	touchAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_touch"},
	}
	router := NewRouter(RouterOptions{
		Config:                               config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                               slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAuthorizationListHandler:   newManagementAuthorizationListHandler(service, managementAuthorizationScopeAdmin),
		ManagementMyAuthorizationListHandler: newManagementAuthorizationListHandler(service, managementAuthorizationScopeSelf),
		ManagementAuthorizationTeamUsageOverviewHandler:   newManagementAuthorizationTeamUsageOverviewHandler(service, managementAuthorizationScopeAdmin),
		ManagementMyAuthorizationTeamUsageOverviewHandler: newManagementAuthorizationTeamUsageOverviewHandler(service, managementAuthorizationScopeSelf),
		ManagementAuthorizationTeamUsageSummaryHandler:    newManagementAuthorizationUsageSummaryHandler(service, managementAuthorizationScopeAdmin, false),
		ManagementMyAuthorizationTeamUsageSummaryHandler:  newManagementAuthorizationUsageSummaryHandler(service, managementAuthorizationScopeSelf, false),
		ManagementAuthorizationUserUsageOverviewHandler:   newManagementAuthorizationUserUsageOverviewHandler(service, managementAuthorizationScopeAdmin),
		ManagementMyAuthorizationUserUsageOverviewHandler: newManagementAuthorizationUserUsageOverviewHandler(service, managementAuthorizationScopeSelf),
		ManagementAuthorizationUserUsageSummaryHandler:    newManagementAuthorizationUsageSummaryHandler(service, managementAuthorizationScopeAdmin, true),
		ManagementMyAuthorizationUserUsageSummaryHandler:  newManagementAuthorizationUsageSummaryHandler(service, managementAuthorizationScopeSelf, true),
		ManagementAuthorizationUsageHandler:               newManagementAuthorizationUsageHandler(service, managementAuthorizationScopeAdmin),
		ManagementMyAuthorizationUsageHandler:             newManagementAuthorizationUsageHandler(service, managementAuthorizationScopeSelf),
		ManagementAuthorizationDetailHandler:              newManagementAuthorizationDetailHandler(service, managementAuthorizationScopeAdmin),
		ManagementMyAuthorizationDetailHandler:            newManagementAuthorizationDetailHandler(service, managementAuthorizationScopeSelf),
		ManagementAuthorizationCreateHandler:              newManagementAuthorizationCreateHandler(service, managementAuthorizationScopeAdmin),
		ManagementMyAuthorizationCreateHandler:            newManagementAuthorizationCreateHandler(service, managementAuthorizationScopeSelf),
		ManagementAuthorizationUpdateHandler:              newManagementAuthorizationUpdateHandler(service, managementAuthorizationScopeAdmin),
		ManagementMyAuthorizationUpdateHandler:            newManagementAuthorizationUpdateHandler(service, managementAuthorizationScopeSelf),
		ManagementAuthorizationExpireUpdateHandler:        newManagementAuthorizationExpireUpdateHandler(service, managementAuthorizationScopeAdmin),
		ManagementMyAuthorizationExpireUpdateHandler:      newManagementAuthorizationExpireUpdateHandler(service, managementAuthorizationScopeSelf),
		ManagementAuthorizationReturnHandler:              newManagementAuthorizationReturnHandler(service, managementAuthorizationScopeAdmin),
		ManagementMyAuthorizationReturnHandler:            newManagementAuthorizationReturnHandler(service, managementAuthorizationScopeSelf),
		ManagementAccountAuthorizationReturnHandler:       newManagementResourceAuthorizationReturnHandler(service, managementAuthorizationScopeAdmin, "account"),
		ManagementMyAccountAuthorizationReturnHandler:     newManagementResourceAuthorizationReturnHandler(service, managementAuthorizationScopeSelf, "account"),
		ManagementGroupAuthorizationReturnHandler:         newManagementResourceAuthorizationReturnHandler(service, managementAuthorizationScopeAdmin, "group"),
		ManagementMyGroupAuthorizationReturnHandler:       newManagementResourceAuthorizationReturnHandler(service, managementAuthorizationScopeSelf, "group"),
		ManagementAuthorizationRevokeHandler:              newManagementAuthorizationRevokeHandler(service, managementAuthorizationScopeAdmin),
		ManagementMyAuthorizationRevokeHandler:            newManagementAuthorizationRevokeHandler(service, managementAuthorizationScopeSelf),
		ManagementAPIAuthMiddleware:                       NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware:                  NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
	})

	for _, item := range []struct {
		method string
		path   string
		body   string
		status int
	}{
		{
			method: http.MethodPost,
			path:   "/__aisys__/api/authorizations?systemAccountId=sys_owner",
			body:   `{"resourceType":"group","resourceId":"grp_owner","granteeType":"team","granteeId":"team_ops"}`,
			status: http.StatusCreated,
		},
		{
			method: http.MethodPost,
			path:   "/__aisys__/api/my-authorizations",
			body:   `{"resourceType":"group","resourceId":"grp_owner","granteeType":"team","granteeId":"team_ops"}`,
			status: http.StatusCreated,
		},
		{
			method: http.MethodPatch,
			path:   "/__aisys__/api/authorizations/rauthgrant_main?systemAccountId=sys_owner",
			body:   `{"status":"paused"}`,
			status: http.StatusOK,
		},
		{
			method: http.MethodPatch,
			path:   "/__aisys__/api/my-authorizations/rauthgrant_main",
			body:   `{"status":"paused"}`,
			status: http.StatusOK,
		},
		{
			method: http.MethodPatch,
			path:   "/__aisys__/api/authorizations/rauthgrant_main/expire?systemAccountId=sys_owner",
			body:   `{"expiresAt":null}`,
			status: http.StatusOK,
		},
		{
			method: http.MethodPatch,
			path:   "/__aisys__/api/my-authorizations/rauthgrant_main/expire",
			body:   `{"expiresAt":null}`,
			status: http.StatusOK,
		},
		{
			method: http.MethodDelete,
			path:   "/__aisys__/api/authorizations/rauthgrant_main/return?systemAccountId=sys_grantee",
			status: http.StatusNoContent,
		},
		{
			method: http.MethodDelete,
			path:   "/__aisys__/api/my-authorizations/rauthgrant_main/return",
			status: http.StatusNoContent,
		},
		{
			method: http.MethodPost,
			path:   "/__aisys__/api/accounts/acct_authorized/return-authorization?systemAccountId=sys_grantee",
			status: http.StatusNoContent,
		},
		{
			method: http.MethodPost,
			path:   "/__aisys__/api/my-accounts/acct_authorized/return-authorization",
			status: http.StatusNoContent,
		},
		{
			method: http.MethodPost,
			path:   "/__aisys__/api/groups/grp_authorized/return-authorization?systemAccountId=sys_grantee",
			status: http.StatusNoContent,
		},
		{
			method: http.MethodPost,
			path:   "/__aisys__/api/my-groups/grp_authorized/return-authorization",
			status: http.StatusNoContent,
		},
		{
			method: http.MethodDelete,
			path:   "/__aisys__/api/authorizations/rauthgrant_main?systemAccountId=sys_owner",
			status: http.StatusOK,
		},
		{
			method: http.MethodDelete,
			path:   "/__aisys__/api/my-authorizations/rauthgrant_main",
			status: http.StatusOK,
		},
	} {
		req := httptest.NewRequest(item.method, item.path, strings.NewReader(item.body))
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != item.status {
			t.Fatalf("%s %s status = %d, want %d; body = %s", item.method, item.path, rec.Code, item.status, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", item.path, got)
		}
	}
	if touchAuthenticator.touchCookieHeader == "" {
		t.Fatal("authorization create routes did not use touch middleware")
	}
	if readAuthenticator.cookieHeader != "" {
		t.Fatalf("authorization create routes used read middleware cookie = %q", readAuthenticator.cookieHeader)
	}
	readAuthenticator.cookieHeader = ""
	touchAuthenticator.touchCookieHeader = ""
	for _, path := range []string{
		"/__aisys__/api/authorizations?systemAccountId=all",
		"/__aisys__/api/authorizations/usage/team-details?systemAccountId=all",
		"/__aisys__/api/authorizations/usage/team-summary?systemAccountId=all",
		"/__aisys__/api/authorizations/usage/user-details?systemAccountId=all",
		"/__aisys__/api/authorizations/usage/user-summary?systemAccountId=all",
		"/__aisys__/api/authorizations/rauthgrant_main/usage?systemAccountId=all",
		"/__aisys__/api/authorizations/rauthgrant_main?systemAccountId=all",
		"/__aisys__/api/my-authorizations",
		"/__aisys__/api/my-authorizations/usage/team-details",
		"/__aisys__/api/my-authorizations/usage/team-summary",
		"/__aisys__/api/my-authorizations/usage/user-details",
		"/__aisys__/api/my-authorizations/usage/user-summary",
		"/__aisys__/api/my-authorizations/rauthgrant_main/usage",
		"/__aisys__/api/my-authorizations/rauthgrant_main",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200; body = %s", path, rec.Code, rec.Body.String())
		}
	}
	if readAuthenticator.cookieHeader == "" || touchAuthenticator.touchCookieHeader != "" {
		t.Fatalf("authorization list routes auth headers read=%q touch=%q", readAuthenticator.cookieHeader, touchAuthenticator.touchCookieHeader)
	}
}

type managementAuthorizationCreateServiceStub struct {
	called                 bool
	input                  managementauthorizations.CreateInput
	result                 managementauthorizations.Summary
	err                    error
	getCalled              bool
	getInput               managementauthorizations.GetInput
	getResult              managementauthorizations.Detail
	getFound               bool
	getErr                 error
	updateCalled           bool
	updateInput            managementauthorizations.UpdateInput
	updateResult           managementauthorizations.Summary
	updateFound            bool
	updateErr              error
	returnCalled           bool
	returnInput            managementauthorizations.ReturnInput
	returnResult           managementauthorizations.Summary
	returnFound            bool
	returnErr              error
	resourceReturnCalled   bool
	resourceReturnInput    managementauthorizations.ResourceReturnInput
	resourceReturnResult   managementauthorizations.Summary
	resourceReturnFound    bool
	resourceReturnErr      error
	revokeCalled           bool
	revokeInput            managementauthorizations.RevokeInput
	revokeResult           managementauthorizations.Summary
	revokeFound            bool
	revokeErr              error
	listCalled             bool
	listInput              managementauthorizations.ListInput
	listResult             managementauthorizations.ListResult
	listErr                error
	teamUsageCalled        bool
	teamUsageInput         managementauthorizations.UsageOverviewInput
	teamUsageResult        managementauthorizations.TeamUsageOverview
	teamUsageErr           error
	userUsageCalled        bool
	userUsageInput         managementauthorizations.UsageOverviewInput
	userUsageResult        managementauthorizations.UserUsageOverview
	userUsageErr           error
	teamUsageSummaryCalled bool
	teamUsageSummaryInput  managementauthorizations.UsageSummaryInput
	teamUsageSummaryResult managementauthorizations.UsageSummary
	teamUsageSummaryErr    error
	userUsageSummaryCalled bool
	userUsageSummaryInput  managementauthorizations.UsageSummaryInput
	userUsageSummaryResult managementauthorizations.UsageSummary
	userUsageSummaryErr    error
	usageCalled            bool
	usageInput             managementauthorizations.UsageDetailInput
	usageResult            managementauthorizations.UsageDetail
	usageFound             bool
	usageErr               error
}

func (s *managementAuthorizationCreateServiceStub) Create(_ *http.Request, input managementauthorizations.CreateInput) (managementauthorizations.Summary, error) {
	s.called = true
	s.input = input
	if s.err != nil {
		return managementauthorizations.Summary{}, s.err
	}
	return s.result, nil
}

func (s *managementAuthorizationCreateServiceStub) List(_ *http.Request, input managementauthorizations.ListInput) (managementauthorizations.ListResult, error) {
	s.listCalled = true
	s.listInput = input
	if s.listErr != nil {
		return managementauthorizations.ListResult{}, s.listErr
	}
	return s.listResult, nil
}

func (s *managementAuthorizationCreateServiceStub) TeamUsageOverview(_ *http.Request, input managementauthorizations.UsageOverviewInput) (managementauthorizations.TeamUsageOverview, error) {
	s.teamUsageCalled = true
	s.teamUsageInput = input
	if s.teamUsageErr != nil {
		return managementauthorizations.TeamUsageOverview{}, s.teamUsageErr
	}
	return s.teamUsageResult, nil
}

func (s *managementAuthorizationCreateServiceStub) UserUsageOverview(_ *http.Request, input managementauthorizations.UsageOverviewInput) (managementauthorizations.UserUsageOverview, error) {
	s.userUsageCalled = true
	s.userUsageInput = input
	if s.userUsageErr != nil {
		return managementauthorizations.UserUsageOverview{}, s.userUsageErr
	}
	return s.userUsageResult, nil
}

func (s *managementAuthorizationCreateServiceStub) TeamUsageSummary(_ *http.Request, input managementauthorizations.UsageSummaryInput) (managementauthorizations.UsageSummary, error) {
	s.teamUsageSummaryCalled = true
	s.teamUsageSummaryInput = input
	if s.teamUsageSummaryErr != nil {
		return managementauthorizations.UsageSummary{}, s.teamUsageSummaryErr
	}
	return s.teamUsageSummaryResult, nil
}

func (s *managementAuthorizationCreateServiceStub) UserUsageSummary(_ *http.Request, input managementauthorizations.UsageSummaryInput) (managementauthorizations.UsageSummary, error) {
	s.userUsageSummaryCalled = true
	s.userUsageSummaryInput = input
	if s.userUsageSummaryErr != nil {
		return managementauthorizations.UsageSummary{}, s.userUsageSummaryErr
	}
	return s.userUsageSummaryResult, nil
}

func (s *managementAuthorizationCreateServiceStub) UsageDetail(_ *http.Request, input managementauthorizations.UsageDetailInput) (managementauthorizations.UsageDetail, bool, error) {
	s.usageCalled = true
	s.usageInput = input
	if s.usageErr != nil {
		return managementauthorizations.UsageDetail{}, false, s.usageErr
	}
	return s.usageResult, s.usageFound, nil
}

func (s *managementAuthorizationCreateServiceStub) Get(_ *http.Request, input managementauthorizations.GetInput) (managementauthorizations.Detail, bool, error) {
	s.getCalled = true
	s.getInput = input
	if s.getErr != nil {
		return managementauthorizations.Detail{}, false, s.getErr
	}
	return s.getResult, s.getFound, nil
}

func (s *managementAuthorizationCreateServiceStub) Update(_ *http.Request, input managementauthorizations.UpdateInput) (managementauthorizations.Summary, bool, error) {
	s.updateCalled = true
	s.updateInput = input
	if s.updateErr != nil {
		return managementauthorizations.Summary{}, false, s.updateErr
	}
	return s.updateResult, s.updateFound, nil
}

func (s *managementAuthorizationCreateServiceStub) Return(_ *http.Request, input managementauthorizations.ReturnInput) (managementauthorizations.Summary, bool, error) {
	s.returnCalled = true
	s.returnInput = input
	if s.returnErr != nil {
		return managementauthorizations.Summary{}, false, s.returnErr
	}
	return s.returnResult, s.returnFound, nil
}

func (s *managementAuthorizationCreateServiceStub) ReturnByResource(_ *http.Request, input managementauthorizations.ResourceReturnInput) (managementauthorizations.Summary, bool, error) {
	s.resourceReturnCalled = true
	s.resourceReturnInput = input
	if s.resourceReturnErr != nil {
		return managementauthorizations.Summary{}, false, s.resourceReturnErr
	}
	return s.resourceReturnResult, s.resourceReturnFound, nil
}

func (s *managementAuthorizationCreateServiceStub) Revoke(_ *http.Request, input managementauthorizations.RevokeInput) (managementauthorizations.Summary, bool, error) {
	s.revokeCalled = true
	s.revokeInput = input
	if s.revokeErr != nil {
		return managementauthorizations.Summary{}, false, s.revokeErr
	}
	return s.revokeResult, s.revokeFound, nil
}

func managementAuthorizationRequestWithURLParam(req *http.Request, key string, value string) *http.Request {
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add(key, value)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

var _ managementAuthorizationCreateService = (*managementAuthorizationCreateServiceStub)(nil)
var _ managementAuthorizationGetService = (*managementAuthorizationCreateServiceStub)(nil)
var _ managementAuthorizationListService = (*managementAuthorizationCreateServiceStub)(nil)
var _ managementAuthorizationRevokeService = (*managementAuthorizationCreateServiceStub)(nil)
var _ managementAuthorizationResourceReturnService = (*managementAuthorizationCreateServiceStub)(nil)
var _ managementAuthorizationReturnService = (*managementAuthorizationCreateServiceStub)(nil)
var _ managementAuthorizationUpdateService = (*managementAuthorizationCreateServiceStub)(nil)
