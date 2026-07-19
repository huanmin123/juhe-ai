package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementexternalintegrationsources"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementExternalIntegrationSourceBuiltInResetHandlerSuccessAndOperationLog(t *testing.T) {
	result := managementExternalIntegrationSourceTokenCreateResultFixture()
	service := &managementExternalIntegrationSourceBuiltInResetServiceStub{result: result}
	queueStub := &operationLogQueueStub{}
	handler := newManagementExternalIntegrationSourceBuiltInResetHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Client:   queueStub,
			NewLogID: func() string { return "oplog_external_source_builtin_reset" },
			Now:      func() time.Time { return time.Date(2026, 7, 19, 1, 2, 3, 0, time.UTC) },
		}),
	)
	req := httptest.NewRequest(http.MethodPost, managementExternalIntegrationSourceBuiltInResetPath, nil)
	req = requestWithManagementExternalIntegrationSourceAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_admin",
		Username:        "admin",
		DisplayName:     "管理员",
		Role:            "admin",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || service.calls != 1 || queueStub.calls != 1 {
		t.Fatalf("status=%d service=%d logs=%d body=%s", rec.Code, service.calls, queueStub.calls, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("Cache-Control=%q Pragma=%q", rec.Header().Get("Cache-Control"), rec.Header().Get("Pragma"))
	}
	var response struct {
		Data managementexternalintegrationsources.TokenCreateResult `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil || !reflect.DeepEqual(response.Data, result) {
		t.Fatalf("response=%#v err=%v", response, err)
	}

	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("decode operation log: %v", err)
	}
	if logInput.ID != "oplog_external_source_builtin_reset" ||
		logInput.Module != "external_integration_sources" || logInput.Action != "reset_builtin_test_token" ||
		logInput.OperationKey != "external_integration_sources.reset_builtin_test_token" ||
		logInput.ResourceType != "external_integration_source" || logInput.ResourceID != "source_1" ||
		logInput.ResourceName != "合作方" || logInput.Summary != "重置内置测试 Token" ||
		logInput.DetailLevel != "full" || logInput.VisibilityScope != "admin_only" || logInput.Mode != "self" ||
		logInput.StatusCode == nil || *logInput.StatusCode != http.StatusOK {
		t.Fatalf("operation log = %#v", logInput)
	}
	wantChanges := []port.OperationLogChange{{
		Field: "tokenPreview", Label: "Token 标识", Before: nil, After: "juis_pre...suffix88",
	}}
	if !reflect.DeepEqual(logInput.Changes, wantChanges) {
		t.Fatalf("operation log changes = %#v", logInput.Changes)
	}
	if strings.Contains(string(queueStub.payload), "juis_plaintext_token_secret") {
		t.Fatalf("operation log leaked token: %s", queueStub.payload)
	}
}

func TestManagementExternalIntegrationSourceBuiltInResetHandlerChecksBoundaryAndMapsErrors(t *testing.T) {
	tests := []struct {
		name        string
		auth        *managementauth.Context
		nilService  bool
		serviceErr  error
		wantStatus  int
		wantMessage string
		forbidText  string
		wantCalls   int
	}{
		{name: "missing auth", wantStatus: http.StatusInternalServerError, wantMessage: "服务器内部错误"},
		{name: "ordinary user", auth: &managementauth.Context{SystemAccountID: "sys_user", Role: "user"}, wantStatus: http.StatusForbidden, wantMessage: "需要管理员权限"},
		{name: "nil service", auth: &managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}, nilService: true, wantStatus: http.StatusInternalServerError, wantMessage: "服务器内部错误"},
		{name: "not found", auth: &managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}, serviceErr: managementexternalintegrationsources.ErrBuiltInResetNotFound, wantStatus: http.StatusBadRequest, wantMessage: "内置测试 Token 不存在", wantCalls: 1},
		{name: "internal", auth: &managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}, serviceErr: errors.New("database secret leaked"), wantStatus: http.StatusInternalServerError, wantMessage: "服务器内部错误", forbidText: "database secret leaked", wantCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementExternalIntegrationSourceBuiltInResetServiceStub{err: test.serviceErr}
			var handler http.Handler = newManagementExternalIntegrationSourceBuiltInResetHandler(service, managementOperationLogOptions{})
			if test.nilService {
				handler = newManagementExternalIntegrationSourceBuiltInResetHandler(nil, managementOperationLogOptions{})
			}
			req := httptest.NewRequest(http.MethodPost, managementExternalIntegrationSourceBuiltInResetPath, nil)
			if test.auth != nil {
				req = requestWithManagementExternalIntegrationSourceAuthContext(req, *test.auth)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != test.wantStatus || service.calls != test.wantCalls || !strings.Contains(rec.Body.String(), test.wantMessage) {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.calls, rec.Body.String())
			}
			if test.forbidText != "" && strings.Contains(rec.Body.String(), test.forbidText) {
				t.Fatalf("internal error leaked: %s", rec.Body.String())
			}
		})
	}
}

func TestManagementExternalIntegrationSourceBuiltInResetHandlerLogFailureDoesNotOverrideResponse(t *testing.T) {
	service := &managementExternalIntegrationSourceBuiltInResetServiceStub{result: managementExternalIntegrationSourceTokenCreateResultFixture()}
	queueStub := &operationLogQueueStub{err: errors.New("queue unavailable")}
	handler := newManagementExternalIntegrationSourceBuiltInResetHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{Client: queueStub}),
	)
	req := httptest.NewRequest(http.MethodPost, managementExternalIntegrationSourceBuiltInResetPath, nil)
	req = requestWithManagementExternalIntegrationSourceAuthContext(req, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || queueStub.calls != 1 {
		t.Fatalf("status=%d logs=%d body=%s", rec.Code, queueStub.calls, rec.Body.String())
	}
}

type managementExternalIntegrationSourceBuiltInResetServiceStub struct {
	result managementexternalintegrationsources.TokenCreateResult
	err    error
	calls  int
}

func (s *managementExternalIntegrationSourceBuiltInResetServiceStub) Reset(context.Context) (managementexternalintegrationsources.TokenCreateResult, error) {
	s.calls++
	return s.result, s.err
}
