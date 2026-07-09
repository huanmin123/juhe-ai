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
	"juhe-ai/backend-go/internal/modules/managementsystemteams"
)

func TestManagementSystemTeamsHandlerListsAdminScope(t *testing.T) {
	service := &managementSystemTeamServiceStub{
		listResult: managementsystemteams.ListResult{
			Items: []managementsystemteams.Summary{{
				ID:                "team_ops",
				Name:              "运维团队",
				Status:            "active",
				MemberCount:       2,
				ActiveMemberCount: 2,
				CreatedBy:         "sys_admin",
				CreatedAt:         "2026-07-09T10:00:00Z",
				UpdatedAt:         "2026-07-09T11:00:00Z",
			}},
			Total:    3,
			HasMore:  true,
			Page:     2,
			PageSize: 1,
		},
	}
	handler := newManagementSystemTeamsHandler(service, managementSystemTeamScopeAdmin)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/system-teams?systemAccountId=sys_user&keyword=%E8%BF%90%E7%BB%B4&page=2&pageSize=1", nil)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_admin",
		Role:            "admin",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	if !service.listCalled ||
		service.listInput.SystemAccountID != "sys_user" ||
		service.listInput.Keyword != "运维" ||
		service.listInput.Page != 2 ||
		service.listInput.PageSize != 1 {
		t.Fatalf("list input = %+v", service.listInput)
	}
	var body struct {
		Data managementsystemteams.ListResult `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Data.Items) != 1 || body.Data.Items[0].ID != "team_ops" || body.Data.Total != 3 || !body.Data.HasMore {
		t.Fatalf("response = %+v", body.Data)
	}
}

func TestManagementSystemTeamsHandlerMyDetailForcesSelfScope(t *testing.T) {
	service := &managementSystemTeamServiceStub{
		detailFound: true,
		detailResult: managementsystemteams.Detail{
			Summary: managementsystemteams.Summary{
				ID:        "team_ops",
				Name:      "运维团队",
				Status:    "active",
				CreatedBy: "sys_admin",
			},
			Members: []managementsystemteams.MemberSummary{{
				ID:              "teammem_1",
				TeamID:          "team_ops",
				SystemAccountID: "sys_user",
				MemberRole:      "member",
				Status:          "active",
			}},
		},
	}
	handler := newManagementSystemTeamsHandler(service, managementSystemTeamScopeSelf)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-teams/team_ops?systemAccountId=sys_other", nil)
	req = withSystemTeamRouteParam(req, "team_ops")
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_user",
		Role:            "user",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	if !service.detailCalled || service.detailTeamID != "team_ops" || service.detailSystemAccountID != "sys_user" {
		t.Fatalf("detail args team=%q systemAccount=%q", service.detailTeamID, service.detailSystemAccountID)
	}
}

func TestManagementSystemTeamsHandlerRejectsOrdinaryUserForAdminScope(t *testing.T) {
	service := &managementSystemTeamServiceStub{}
	handler := newManagementSystemTeamsHandler(service, managementSystemTeamScopeAdmin)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/system-teams", nil)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_user",
		Role:            "user",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if service.listCalled || service.detailCalled {
		t.Fatal("service should not be called for ordinary user")
	}
}

func TestManagementSystemTeamsHandlerMapsDetailNotFound(t *testing.T) {
	service := &managementSystemTeamServiceStub{}
	handler := newManagementSystemTeamsHandler(service, managementSystemTeamScopeAdmin)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/system-teams/team_missing", nil)
	req = withSystemTeamRouteParam(req, "team_missing")
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_admin",
		Role:            "admin",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

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

func TestRouterRegistersW4ManagementSystemTeamsReadWithoutTouch(t *testing.T) {
	service := &managementSystemTeamServiceStub{
		listResult: managementsystemteams.ListResult{Items: []managementsystemteams.Summary{{ID: "team_ops", Name: "运维团队", Status: "active"}}},
	}
	readAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_read"},
	}
	router := NewRouter(RouterOptions{
		Config:                       config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                       slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementSystemTeamsHandler: newManagementSystemTeamsHandler(service, managementSystemTeamScopeAdmin),
		ManagementAPIAuthMiddleware:  NewManagementAPIAuthMiddleware(readAuthenticator),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/system-teams", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if readAuthenticator.cookieHeader == "" || readAuthenticator.touchCookieHeader != "" {
		t.Fatalf("auth headers read=%q touch=%q", readAuthenticator.cookieHeader, readAuthenticator.touchCookieHeader)
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
	called                bool
	input                 managementsystemteams.CreateInput
	result                managementsystemteams.Summary
	err                   error
	listCalled            bool
	listInput             managementsystemteams.ListInput
	listResult            managementsystemteams.ListResult
	listErr               error
	detailCalled          bool
	detailTeamID          string
	detailSystemAccountID string
	detailResult          managementsystemteams.Detail
	detailFound           bool
	detailErr             error
}

func (s *managementSystemTeamServiceStub) List(_ context.Context, input managementsystemteams.ListInput) (managementsystemteams.ListResult, error) {
	s.listCalled = true
	s.listInput = input
	return s.listResult, s.listErr
}

func (s *managementSystemTeamServiceStub) Detail(_ context.Context, teamID string, systemAccountID string) (managementsystemteams.Detail, bool, error) {
	s.detailCalled = true
	s.detailTeamID = teamID
	s.detailSystemAccountID = systemAccountID
	return s.detailResult, s.detailFound, s.detailErr
}

func (s *managementSystemTeamServiceStub) Create(_ context.Context, input managementsystemteams.CreateInput) (managementsystemteams.Summary, error) {
	s.called = true
	s.input = input
	return s.result, s.err
}

func withSystemTeamRouteParam(req *http.Request, teamID string) *http.Request {
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", teamID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}
