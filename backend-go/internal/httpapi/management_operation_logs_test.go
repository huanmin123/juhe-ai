package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementoperationlogs"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementOperationLogsHandlerRequiresAdminAndParsesQuery(t *testing.T) {
	service := &managementOperationLogServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementOperationLogsHandler(service, managementOperationLogScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/operation-logs?page=2&pageSize=20&summaryKeyword=%E6%A0%87%E7%AD%BE&module=accounts&action=update_tags&resourceType=account&resourceId=acct_main&traceId=req_&actorSystemAccountId=sys_admin&affectedSystemAccountId=sys_user&operationScopeSystemAccountId=sys_user&startAt=2026-07-08T10:00:00Z&endAt=2026-07-07T10:00:00Z", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	input := service.listInput
	if input.Page != 2 ||
		input.PageSize != 20 ||
		input.SummaryKeyword != "标签" ||
		input.Module != "accounts" ||
		input.Action != "update_tags" ||
		input.ResourceType != "account" ||
		input.ResourceID != "acct_main" ||
		input.TraceID != "req_" ||
		input.ActorSystemAccountID != "sys_admin" ||
		input.AffectedSystemAccountID != "sys_user" ||
		input.OperationScopeSystemAccountID != "sys_user" {
		t.Fatalf("input = %+v", input)
	}
	if input.StartAt.IsZero() || input.EndAt.IsZero() || !input.StartAt.Before(input.EndAt) {
		t.Fatalf("date range = %s - %s", input.StartAt, input.EndAt)
	}
}

func TestManagementOperationLogsHandlerRejectsOrdinaryUser(t *testing.T) {
	service := &managementOperationLogServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementOperationLogsHandler(service, managementOperationLogScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/operation-logs", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if service.listCalled {
		t.Fatal("service should not be called for ordinary user on admin operation logs route")
	}
}

func TestManagementMyOperationLogsHandlerForcesViewerScope(t *testing.T) {
	service := &managementOperationLogServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementOperationLogsHandler(service, managementOperationLogScopeSelf))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-operation-logs?actorSystemAccountId=sys_admin&affectedSystemAccountId=sys_admin&operationScopeSystemAccountId=sys_admin", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	input := service.listInput
	if input.ViewerSystemAccountID != "sys_user" ||
		input.ActorSystemAccountID != "" ||
		input.AffectedSystemAccountID != "" ||
		input.OperationScopeSystemAccountID != "" {
		t.Fatalf("input = %+v", input)
	}
}

func TestManagementOperationLogsDetailNotFoundAndRedactsErrors(t *testing.T) {
	tests := []struct {
		name       string
		found      bool
		err        error
		wantStatus int
		wantMsg    string
	}{
		{name: "not found", wantStatus: http.StatusNotFound, wantMsg: "操作日志不存在"},
		{name: "store error", err: errors.New("postgres password leaked"), wantStatus: http.StatusInternalServerError, wantMsg: "服务器内部错误"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementOperationLogServiceStub{detailFound: tt.found, detailErr: tt.err}
			handler := newManagementOperationLogsHandler(service, managementOperationLogScopeSelf)
			req := managementOperationLogDetailRequest("/__aisys__/api/my-operation-logs/oplog_1", "oplog_1")
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_user", Role: "user"}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantStatus, rec.Body.String())
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

func TestRouterRegistersW2ManagementOperationLogs(t *testing.T) {
	service := &managementOperationLogServiceStub{
		detailFound: true,
		detail: managementoperationlogs.Detail{
			Summary: managementoperationlogs.Summary{
				ID:                   "oplog_1",
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "admin",
				Mode:                 "admin",
				Module:               "accounts",
				Action:               "update_tags",
				OperationKey:         "accounts.update_tags",
				ResourceType:         "account",
				Summary:              "更新账户标签：主账号",
				DetailLevel:          "full",
				VisibilityScope:      "targeted",
				CreatedAt:            time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
			},
			Changes:  []port.OperationLogChange{},
			Metadata: map[string]any{},
			Targets:  []managementoperationlogs.TargetSummary{},
			Viewers:  []managementoperationlogs.ViewerSummary{},
		},
	}
	router := NewRouter(RouterOptions{
		Config:                           config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                           slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementOperationLogsHandler:   newManagementOperationLogsHandler(service, managementOperationLogScopeAdmin),
		ManagementMyOperationLogsHandler: newManagementOperationLogsHandler(service, managementOperationLogScopeSelf),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	for _, path := range []string{"/__aisys__/api/operation-logs", "/__aisys__/api/operation-logs/oplog_1", "/__aisys__/api/my-operation-logs", "/__aisys__/api/my-operation-logs/oplog_1"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200; body = %s", path, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", path, got)
		}
	}
}

func TestRouterDoesNotRegisterW2ManagementOperationLogsWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                         config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                         slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementOperationLogsHandler: newManagementOperationLogsHandler(&managementOperationLogServiceStub{}, managementOperationLogScopeAdmin),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/operation-logs", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", rec.Code)
	}
}

func managementOperationLogDetailRequest(target string, id string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

type managementOperationLogServiceStub struct {
	listCalled  bool
	listInput   managementoperationlogs.ListInput
	listResult  managementoperationlogs.ListResult
	listErr     error
	detailInput managementoperationlogs.DetailInput
	detail      managementoperationlogs.Detail
	detailFound bool
	detailErr   error
}

func (s *managementOperationLogServiceStub) List(_ *http.Request, input managementoperationlogs.ListInput) (managementoperationlogs.ListResult, error) {
	s.listCalled = true
	s.listInput = input
	return s.listResult, s.listErr
}

func (s *managementOperationLogServiceStub) Detail(_ *http.Request, input managementoperationlogs.DetailInput) (managementoperationlogs.Detail, bool, error) {
	s.detailInput = input
	return s.detail, s.detailFound, s.detailErr
}
