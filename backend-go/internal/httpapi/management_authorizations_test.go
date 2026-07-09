package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestRouterRegistersW4ManagementAuthorizationCreate(t *testing.T) {
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
	}
	readAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_read"},
	}
	touchAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_touch"},
	}
	router := NewRouter(RouterOptions{
		Config:                                 config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                                 slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAuthorizationCreateHandler:   newManagementAuthorizationCreateHandler(service, managementAuthorizationScopeAdmin),
		ManagementMyAuthorizationCreateHandler: newManagementAuthorizationCreateHandler(service, managementAuthorizationScopeSelf),
		ManagementAPIAuthMiddleware:            NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware:       NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
	})

	for _, item := range []struct {
		path string
		body string
	}{
		{
			path: "/__aisys__/api/authorizations?systemAccountId=sys_owner",
			body: `{"resourceType":"group","resourceId":"grp_owner","granteeType":"team","granteeId":"team_ops"}`,
		},
		{
			path: "/__aisys__/api/my-authorizations",
			body: `{"resourceType":"group","resourceId":"grp_owner","granteeType":"team","granteeId":"team_ops"}`,
		},
	} {
		req := httptest.NewRequest(http.MethodPost, item.path, strings.NewReader(item.body))
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusCreated {
			t.Fatalf("%s status = %d, want 201; body = %s", item.path, rec.Code, rec.Body.String())
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
}

type managementAuthorizationCreateServiceStub struct {
	called bool
	input  managementauthorizations.CreateInput
	result managementauthorizations.Summary
	err    error
}

func (s *managementAuthorizationCreateServiceStub) Create(_ *http.Request, input managementauthorizations.CreateInput) (managementauthorizations.Summary, error) {
	s.called = true
	s.input = input
	if s.err != nil {
		return managementauthorizations.Summary{}, s.err
	}
	return s.result, nil
}

var _ managementAuthorizationCreateService = (*managementAuthorizationCreateServiceStub)(nil)
