package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/config"
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementexternalintegrationsources"
)

func TestManagementExternalIntegrationSourceDeleteHandlerChecksBoundary(t *testing.T) {
	tests := []struct {
		name        string
		auth        *managementauth.Context
		nilService  bool
		wantStatus  int
		wantMessage string
	}{
		{name: "missing auth context", wantStatus: http.StatusInternalServerError, wantMessage: "服务器内部错误"},
		{
			name:        "blank system account",
			auth:        &managementauth.Context{SystemAccountID: "  ", Role: "admin"},
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "服务器内部错误",
		},
		{
			name:        "ordinary user",
			auth:        &managementauth.Context{SystemAccountID: "sys_user", Role: "user"},
			wantStatus:  http.StatusForbidden,
			wantMessage: "需要管理员权限",
		},
		{
			name:        "nil service",
			auth:        &managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"},
			nilService:  true,
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "服务器内部错误",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementExternalIntegrationSourceDeleteServiceStub{}
			var handler http.Handler
			if test.nilService {
				handler = newManagementExternalIntegrationSourceDeleteHandler(nil, managementOperationLogOptions{})
			} else {
				handler = newManagementExternalIntegrationSourceDeleteHandler(service, managementOperationLogOptions{})
			}
			req := managementExternalIntegrationSourceDeleteRequest("source_1")
			if test.auth != nil {
				req = requestWithManagementExternalIntegrationSourceAuthContext(req, *test.auth)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != test.wantStatus ||
				!strings.Contains(rec.Body.String(), test.wantMessage) ||
				service.calls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.calls, rec.Body.String())
			}
		})
	}
}

func TestManagementExternalIntegrationSourceDeleteHandlerMapsErrors(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantMessage string
	}{
		{
			name:        "invalid",
			err:         fmtDeleteTestError(managementexternalintegrationsources.ErrDeleteInvalid),
			wantStatus:  http.StatusBadRequest,
			wantMessage: "来源系统不存在",
		},
		{
			name:        "not found",
			err:         managementexternalintegrationsources.ErrNotFound,
			wantStatus:  http.StatusNotFound,
			wantMessage: "来源系统不存在",
		},
		{
			name:        "built in",
			err:         managementexternalintegrationsources.ErrBuiltInDeleteRestricted,
			wantStatus:  http.StatusBadRequest,
			wantMessage: "内置测试 Token 不支持删除",
		},
		{
			name:        "internal",
			err:         errors.New("database password leaked"),
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "服务器内部错误",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementExternalIntegrationSourceDeleteServiceStub{err: test.err}
			handler := newManagementExternalIntegrationSourceDeleteHandler(service, managementOperationLogOptions{})
			req := managementExternalIntegrationSourceDeleteRequest("source_1")
			req = requestWithManagementExternalIntegrationSourceAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_admin",
				Role:            "admin",
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != test.wantStatus || !strings.Contains(rec.Body.String(), test.wantMessage) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if service.calls != 1 || service.input.SourceID != "source_1" {
				t.Fatalf("service calls=%d input=%+v", service.calls, service.input)
			}
		})
	}
}

func TestManagementExternalIntegrationSourceDeleteHandlerWritesExactOperationLog(t *testing.T) {
	now := time.Date(2026, time.July, 16, 9, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	queueStub := &operationLogQueueStub{}
	service := &managementExternalIntegrationSourceDeleteServiceStub{
		result: managementexternalintegrationsources.DeleteResult{
			SourceID:   "source_1",
			SourceName: "合作方系统",
			TokenCount: 3,
		},
	}
	handler := newManagementExternalIntegrationSourceDeleteHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			Now:      func() time.Time { return now },
			NewLogID: func() string { return "oplog_source_delete" },
		}),
	)
	req := managementExternalIntegrationSourceDeleteRequest(" raw/source-id ")
	req.RemoteAddr = "203.0.113.10:43120"
	req.Header.Set("User-Agent", "delete-client/1.0")
	req = requestWithManagementExternalIntegrationSourceAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_admin",
		Username:        "admin",
		DisplayName:     "管理员",
		Role:            "super_admin",
	})
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_source_delete"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || queueStub.calls != 1 {
		t.Fatalf("status=%d body=%q queue calls=%d", rec.Code, rec.Body.String(), queueStub.calls)
	}
	if service.calls != 1 || service.input.SourceID != " raw/source-id " {
		t.Fatalf("service calls=%d input=%+v", service.calls, service.input)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("decode operation log: %v", err)
	}
	if logInput.ID != "oplog_source_delete" ||
		logInput.TraceID != "req_source_delete" ||
		logInput.ActorSystemAccountID != "sys_admin" ||
		logInput.ActorUsername != "admin" ||
		logInput.ActorDisplayName != "管理员" ||
		logInput.ActorRole != "super_admin" ||
		logInput.Mode != "self" ||
		logInput.Module != "external_integration_sources" ||
		logInput.Action != "delete" ||
		logInput.OperationKey != "external_integration_sources.delete" ||
		logInput.ResourceType != "external_integration_source" ||
		logInput.ResourceID != "source_1" ||
		logInput.ResourceName != "合作方系统" ||
		logInput.Summary != "删除外部来源系统：合作方系统" ||
		logInput.DetailLevel != "full" ||
		logInput.VisibilityScope != "admin_only" ||
		logInput.OperationScopeSystemAccountID != "" ||
		len(logInput.Viewers) != 0 ||
		logInput.Method != http.MethodDelete ||
		logInput.Path != "/__aisys__/api/external-integration-sources/source_1" ||
		logInput.StatusCode == nil || *logInput.StatusCode != http.StatusNoContent ||
		logInput.ClientIP != "203.0.113.10" ||
		logInput.UserAgent != "delete-client/1.0" ||
		!logInput.CreatedAt.Equal(now.UTC()) {
		t.Fatalf("operation log=%+v", logInput)
	}
	if len(logInput.Changes) != 2 ||
		logInput.Changes[0].Field != "deleted" ||
		logInput.Changes[0].Label != "删除状态" ||
		logInput.Changes[0].Before != false ||
		logInput.Changes[0].After != true ||
		logInput.Changes[1].Field != "tokenCount" ||
		logInput.Changes[1].Label != "关联 Token 数量" ||
		logInput.Changes[1].Before != float64(3) ||
		logInput.Changes[1].After != float64(0) {
		t.Fatalf("operation log changes=%#v", logInput.Changes)
	}
}

func TestManagementExternalIntegrationSourceDeleteHandlerIgnoresOperationLogFailure(t *testing.T) {
	queueStub := &operationLogQueueStub{err: errors.New("queue unavailable")}
	service := &managementExternalIntegrationSourceDeleteServiceStub{
		result: managementexternalintegrationsources.DeleteResult{
			SourceID:   "source_1",
			SourceName: "合作方系统",
			TokenCount: 1,
		},
	}
	handler := newManagementExternalIntegrationSourceDeleteHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{Client: queueStub}),
	)
	req := managementExternalIntegrationSourceDeleteRequest("source_1")
	req = requestWithManagementExternalIntegrationSourceAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_admin",
		Role:            "admin",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || queueStub.calls != 1 {
		t.Fatalf("status=%d body=%q queue calls=%d", rec.Code, rec.Body.String(), queueStub.calls)
	}
}

func managementExternalIntegrationSourceDeleteRequest(sourceID string) *http.Request {
	req := httptest.NewRequest(
		http.MethodDelete,
		"/__aisys__/api/external-integration-sources/source_1",
		nil,
	)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", sourceID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

func fmtDeleteTestError(err error) error {
	return errors.Join(errors.New("wrapped"), err)
}

type managementExternalIntegrationSourceDeleteServiceStub struct {
	input  managementexternalintegrationsources.DeleteInput
	result managementexternalintegrationsources.DeleteResult
	err    error
	calls  int
}

func (s *managementExternalIntegrationSourceDeleteServiceStub) Delete(
	_ context.Context,
	input managementexternalintegrationsources.DeleteInput,
) (managementexternalintegrationsources.DeleteResult, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}
