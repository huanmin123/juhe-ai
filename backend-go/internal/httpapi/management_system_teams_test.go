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

	"juhe-ai/backend-go/internal/config"
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementsystemteams"
)

func TestManagementSystemTeamCreateHandlerRequiresAdminAndWritesOperationLog(t *testing.T) {
	queueStub := &operationLogQueueStub{}
	service := &managementSystemTeamServiceStub{
		result: managementsystemteams.Summary{
			ID:                "team_ops",
			Name:              "运维团队",
			Description:       "负责稳定性",
			Status:            "active",
			MemberCount:       0,
			ActiveMemberCount: 0,
			CreatedBy:         "sys_admin",
			CreatedAt:         "2026-07-09T10:00:00Z",
			UpdatedAt:         "2026-07-09T10:00:00Z",
		},
	}
	handler := newManagementSystemTeamCreateHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			NewLogID: func() string { return "oplog_create_team" },
		}),
	)
	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/system-teams?systemAccountId=sys_owner", strings.NewReader(`{"name":"运维团队","description":"负责稳定性"}`))
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
		t.Fatalf("status = %d, want 201, body = %s", rec.Code, rec.Body.String())
	}
	if !service.called ||
		service.input.Name != "运维团队" ||
		service.input.Description == nil ||
		*service.input.Description != "负责稳定性" ||
		service.input.CreatedBy != "sys_admin" {
		t.Fatalf("service input = %+v", service.input)
	}
	var body struct {
		Data managementsystemteams.Summary `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.ID != "team_ops" || body.Data.Name != "运维团队" || body.Data.CreatedBy != "sys_admin" {
		t.Fatalf("response = %+v", body.Data)
	}
	if queueStub.calls != 1 || queueStub.taskType != operationlogjob.TaskTypeWrite {
		t.Fatalf("operation log queue calls = %d taskType = %q", queueStub.calls, queueStub.taskType)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("decode operation log payload: %v", err)
	}
	if logInput.OperationKey != "system_teams.create" ||
		logInput.Module != "system_teams" ||
		logInput.Action != "create" ||
		logInput.ResourceType != "system_team" ||
		logInput.ResourceID != "team_ops" ||
		logInput.ActorSystemAccountID != "sys_admin" ||
		logInput.OperationScopeSystemAccountID != "sys_admin" ||
		logInput.StatusCode == nil ||
		*logInput.StatusCode != http.StatusCreated {
		t.Fatalf("operation log input = %+v", logInput)
	}
	if len(logInput.Changes) != 3 ||
		logInput.Changes[0].Field != "name" ||
		logInput.Changes[1].Field != "description" ||
		logInput.Changes[2].Field != "status" {
		t.Fatalf("operation log changes = %+v", logInput.Changes)
	}
	if len(logInput.Viewers) != 1 ||
		logInput.Viewers[0].SystemAccountID != "sys_admin" ||
		logInput.Viewers[0].VisibilityReason != "team_creator" {
		t.Fatalf("operation log viewers = %+v", logInput.Viewers)
	}
}

func TestManagementSystemTeamCreateHandlerRejectsOrdinaryUser(t *testing.T) {
	service := &managementSystemTeamServiceStub{}
	handler := newManagementSystemTeamCreateHandler(service, managementOperationLogOptions{})
	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/system-teams", strings.NewReader(`{"name":"团队"}`))
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_user",
		Role:            "user",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if service.called {
		t.Fatal("service should not be called for ordinary user")
	}
}

func TestManagementSystemTeamCreateHandlerValidatesBody(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		body     string
		wantCode int
		wantMsg  string
	}{
		{name: "syntax error", target: "/__aisys__/api/system-teams", body: `{"name":`, wantCode: http.StatusBadRequest, wantMsg: "请求体无效"},
		{name: "trailing json", target: "/__aisys__/api/system-teams", body: `{"name":"团队"} true`, wantCode: http.StatusBadRequest, wantMsg: "请求体无效"},
		{name: "missing name", target: "/__aisys__/api/system-teams", body: `{}`, wantCode: http.StatusBadRequest, wantMsg: "团队参数不合法"},
		{name: "blank name", target: "/__aisys__/api/system-teams", body: `{"name":"   "}`, wantCode: http.StatusBadRequest, wantMsg: "团队参数不合法"},
		{name: "bad description", target: "/__aisys__/api/system-teams", body: `{"name":"团队","description":1}`, wantCode: http.StatusBadRequest, wantMsg: "团队参数不合法"},
		{name: "bad status", target: "/__aisys__/api/system-teams", body: `{"name":"团队","status":"archived"}`, wantCode: http.StatusBadRequest, wantMsg: "团队参数不合法"},
		{name: "unknown field", target: "/__aisys__/api/system-teams", body: `{"name":"团队","extra":true}`, wantCode: http.StatusBadRequest, wantMsg: "团队参数不合法"},
		{name: "blank scope query", target: "/__aisys__/api/system-teams?systemAccountId=", body: `{"name":"团队"}`, wantCode: http.StatusBadRequest, wantMsg: "查询参数不合法"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementSystemTeamServiceStub{}
			handler := newManagementSystemTeamCreateHandler(service, managementOperationLogOptions{})
			req := httptest.NewRequest(http.MethodPost, tt.target, strings.NewReader(tt.body))
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
				SystemAccountID: "sys_admin",
				Role:            "admin",
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
			if service.called {
				t.Fatal("service should not be called for invalid request")
			}
		})
	}
}

func TestManagementSystemTeamCreateHandlerMapsServiceErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantMsg  string
	}{
		{name: "invalid", err: managementsystemteams.ErrSystemTeamCreateInvalid, wantCode: http.StatusBadRequest, wantMsg: "团队参数不合法"},
		{name: "duplicate name", err: managementsystemteams.ErrSystemTeamNameExists, wantCode: http.StatusConflict, wantMsg: "团队名称已存在"},
		{name: "store error", err: errors.New("postgres password leaked"), wantCode: http.StatusInternalServerError, wantMsg: "创建团队失败"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementSystemTeamServiceStub{err: tt.err}
			handler := newManagementSystemTeamCreateHandler(service, managementOperationLogOptions{})
			req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/system-teams", strings.NewReader(`{"name":"团队"}`))
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
				SystemAccountID: "sys_admin",
				Role:            "admin",
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

func TestRouterRegistersW4ManagementSystemTeamCreate(t *testing.T) {
	service := &managementSystemTeamServiceStub{
		result: managementsystemteams.Summary{ID: "team_ops", Name: "运维团队", Status: "active", CreatedBy: "sys_admin"},
	}
	readAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_read"},
	}
	touchAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_touch"},
	}
	router := NewRouter(RouterOptions{
		Config:                            config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                            slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementSystemTeamCreateHandler: newManagementSystemTeamCreateHandler(service, managementOperationLogOptions{}),
		ManagementAPIAuthMiddleware:       NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware:  NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
	})

	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/system-teams", strings.NewReader(`{"name":"运维团队"}`))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if readAuthenticator.cookieHeader != "" || touchAuthenticator.touchCookieHeader == "" {
		t.Fatalf("auth headers read=%q touch=%q", readAuthenticator.cookieHeader, touchAuthenticator.touchCookieHeader)
	}
}

func TestRouterDoesNotRegisterW4ManagementSystemTeamCreateWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                            config.Config{Host: "127.0.0.1", Port: 3000},
		ManagementSystemTeamCreateHandler: newManagementSystemTeamCreateHandler(&managementSystemTeamServiceStub{}, managementOperationLogOptions{}),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
		ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/system-teams", strings.NewReader(`{"name":"运维团队"}`))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", rec.Code)
	}
}

type managementSystemTeamServiceStub struct {
	called bool
	input  managementsystemteams.CreateInput
	result managementsystemteams.Summary
	err    error
}

func (s *managementSystemTeamServiceStub) Create(_ context.Context, input managementsystemteams.CreateInput) (managementsystemteams.Summary, error) {
	s.called = true
	s.input = input
	return s.result, s.err
}
