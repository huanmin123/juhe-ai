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

	"github.com/go-chi/chi/v5"

	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementexternalintegrationsources"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementExternalIntegrationSourceTokenCreateHandlerSuccessAndOperationLog(t *testing.T) {
	result := managementExternalIntegrationSourceTokenCreateResultFixture()
	service := &managementExternalIntegrationSourceTokenCreateServiceStub{result: result}
	queueStub := &operationLogQueueStub{}
	handler := newManagementExternalIntegrationSourceTokenCreateHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Client:   queueStub,
			NewLogID: func() string { return "oplog_external_source_token_create" },
			Now:      func() time.Time { return time.Date(2026, 7, 16, 6, 7, 8, 0, time.UTC) },
		}),
	)
	body := `{"name":" 生产 Token ","status":"disabled","scopes":["juhe_ai_public:group_list:read"],"expiresAt":"2026-08-01T00:00:00.000Z"}`
	req := managementExternalIntegrationSourceTokenCreateRequest(" source_1 ", body)
	req = requestWithManagementExternalIntegrationSourceAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_admin",
		Username:        "admin",
		DisplayName:     "管理员",
		Role:            "admin",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated || service.calls != 1 || queueStub.calls != 1 {
		t.Fatalf("status=%d service=%d logs=%d body=%s", rec.Code, service.calls, queueStub.calls, rec.Body.String())
	}
	if rec.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("Pragma = %q", rec.Header().Get("Pragma"))
	}
	if service.input.SourceID != " source_1 " || service.input.Name != " 生产 Token " ||
		service.input.Status != "disabled" || service.input.ExpiresAt != "2026-08-01T00:00:00.000Z" {
		t.Fatalf("service input = %#v", service.input)
	}
	if scopes, ok := service.input.Scopes.([]any); !ok || len(scopes) != 1 || scopes[0] != "juhe_ai_public:group_list:read" {
		t.Fatalf("service scopes = %#v", service.input.Scopes)
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
	if logInput.ID != "oplog_external_source_token_create" ||
		logInput.Module != "external_integration_sources" || logInput.Action != "create_token" ||
		logInput.OperationKey != "external_integration_sources.create_token" ||
		logInput.ResourceType != "external_integration_source" || logInput.ResourceID != "source_1" ||
		logInput.ResourceName != "合作方" || logInput.Summary != "生成外部来源系统 Token：合作方" ||
		logInput.DetailLevel != "full" || logInput.VisibilityScope != "admin_only" || logInput.Mode != "self" ||
		logInput.StatusCode == nil || *logInput.StatusCode != http.StatusCreated || len(logInput.Changes) != 3 {
		t.Fatalf("operation log = %#v", logInput)
	}
	wantChanges := []port.OperationLogChange{
		{Field: "tokenName", Label: "Token 名称", Before: nil, After: "生产 Token"},
		{Field: "tokenPreview", Label: "Token 标识", Before: nil, After: "juis_pre...suffix88"},
		{Field: "expiresAt", Label: "到期时间", Before: nil, After: "2026-08-01T00:00:00.000Z"},
	}
	if !reflect.DeepEqual(logInput.Changes, wantChanges) {
		t.Fatalf("operation log changes = %#v", logInput.Changes)
	}
	encodedLog := string(queueStub.payload)
	for _, secret := range []string{
		"juis_plaintext_token_secret",
		"tokenHash",
		"ciphertext",
		"juhe_ai_public:group_list:read",
		"notes",
	} {
		if strings.Contains(encodedLog, secret) {
			t.Fatalf("operation log leaked %q: %s", secret, encodedLog)
		}
	}
}

func TestManagementExternalIntegrationSourceTokenCreateHandlerStrictPayloadAndDefaults(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantStatus  int
		wantMessage string
		wantCalls   int
	}{
		{name: "defaults", body: `{"name":"Token"}`, wantStatus: http.StatusCreated, wantCalls: 1},
		{name: "missing name reaches service", body: `{}`, wantStatus: http.StatusCreated, wantCalls: 1},
		{name: "expires null", body: `{"name":"Token","expiresAt":null}`, wantStatus: http.StatusCreated, wantCalls: 1},
		{name: "invalid json", body: `{`, wantStatus: http.StatusBadRequest, wantMessage: "请求体无效"},
		{name: "trailing json", body: `{} {}`, wantStatus: http.StatusBadRequest, wantMessage: "请求体无效"},
		{name: "array root", body: `[]`, wantStatus: http.StatusBadRequest, wantMessage: "Token 参数无效"},
		{name: "unknown field", body: `{"name":"Token","notes":"private"}`, wantStatus: http.StatusBadRequest, wantMessage: "Token 参数无效"},
		{name: "name null", body: `{"name":null}`, wantStatus: http.StatusBadRequest, wantMessage: "Token 参数无效"},
		{name: "status null", body: `{"name":"Token","status":null}`, wantStatus: http.StatusBadRequest, wantMessage: "Token 参数无效"},
		{name: "scopes null", body: `{"name":"Token","scopes":null}`, wantStatus: http.StatusBadRequest, wantMessage: "Token 参数无效"},
		{name: "scopes object", body: `{"name":"Token","scopes":{}}`, wantStatus: http.StatusBadRequest, wantMessage: "Token 参数无效"},
		{name: "expires number", body: `{"name":"Token","expiresAt":1}`, wantStatus: http.StatusBadRequest, wantMessage: "Token 参数无效"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementExternalIntegrationSourceTokenCreateServiceStub{result: managementExternalIntegrationSourceTokenCreateResultFixture()}
			handler := newManagementExternalIntegrationSourceTokenCreateHandler(service, managementOperationLogOptions{})
			req := managementExternalIntegrationSourceTokenCreateRequest("source_1", test.body)
			req = requestWithManagementExternalIntegrationSourceAuthContext(req, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != test.wantStatus || service.calls != test.wantCalls {
				t.Fatalf("status=%d want=%d calls=%d want=%d body=%s", rec.Code, test.wantStatus, service.calls, test.wantCalls, rec.Body.String())
			}
			if test.wantMessage != "" && !strings.Contains(rec.Body.String(), test.wantMessage) {
				t.Fatalf("body=%s want message=%q", rec.Body.String(), test.wantMessage)
			}
			if test.name == "defaults" {
				if service.input.SourceID != "source_1" || service.input.Name != "Token" || service.input.Status != "" ||
					service.input.Scopes != nil || service.input.ExpiresAt != nil {
					t.Fatalf("default input = %#v", service.input)
				}
			}
		})
	}
}

func TestManagementExternalIntegrationSourceTokenCreateHandlerChecksBoundaryBeforeBody(t *testing.T) {
	tests := []struct {
		name        string
		auth        *managementauth.Context
		nilService  bool
		wantStatus  int
		wantMessage string
	}{
		{name: "missing auth", wantStatus: http.StatusInternalServerError, wantMessage: "服务器内部错误"},
		{name: "blank account", auth: &managementauth.Context{SystemAccountID: " ", Role: "admin"}, wantStatus: http.StatusInternalServerError, wantMessage: "服务器内部错误"},
		{name: "ordinary user", auth: &managementauth.Context{SystemAccountID: "sys_user", Role: "user"}, wantStatus: http.StatusForbidden, wantMessage: "需要管理员权限"},
		{name: "nil service", auth: &managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}, nilService: true, wantStatus: http.StatusInternalServerError, wantMessage: "服务器内部错误"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementExternalIntegrationSourceTokenCreateServiceStub{}
			var handler http.Handler = newManagementExternalIntegrationSourceTokenCreateHandler(service, managementOperationLogOptions{})
			if test.nilService {
				handler = newManagementExternalIntegrationSourceTokenCreateHandler(nil, managementOperationLogOptions{})
			}
			req := managementExternalIntegrationSourceTokenCreateRequest("source_1", `{`)
			if test.auth != nil {
				req = requestWithManagementExternalIntegrationSourceAuthContext(req, *test.auth)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != test.wantStatus || service.calls != 0 || !strings.Contains(rec.Body.String(), test.wantMessage) {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.calls, rec.Body.String())
			}
		})
	}
}

func TestManagementExternalIntegrationSourceTokenCreateHandlerMapsErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantText   string
		forbidText string
	}{
		{name: "validation", err: managementExternalIntegrationSourceTokenCreateValidationError(), wantStatus: http.StatusBadRequest, wantText: "来源系统名称不能为空"},
		{name: "not found", err: managementexternalintegrationsources.ErrNotFound, wantStatus: http.StatusBadRequest, wantText: "来源系统不存在"},
		{name: "built in restricted", err: managementexternalintegrationsources.ErrBuiltInTokenCreateRestricted, wantStatus: http.StatusBadRequest, wantText: "内置测试 Token 不支持新增 Token"},
		{name: "token exists", err: managementexternalintegrationsources.ErrTokenExists, wantStatus: http.StatusBadRequest, wantText: "来源系统 token 已存在，请重新生成"},
		{name: "internal", err: errors.New("database password leaked"), wantStatus: http.StatusInternalServerError, wantText: "服务器内部错误", forbidText: "database password leaked"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementExternalIntegrationSourceTokenCreateServiceStub{err: test.err}
			handler := newManagementExternalIntegrationSourceTokenCreateHandler(service, managementOperationLogOptions{})
			req := managementExternalIntegrationSourceTokenCreateRequest("source_1", `{"name":"Token"}`)
			req = requestWithManagementExternalIntegrationSourceAuthContext(req, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != test.wantStatus || service.calls != 1 || !strings.Contains(rec.Body.String(), test.wantText) {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.calls, rec.Body.String())
			}
			if test.forbidText != "" && strings.Contains(rec.Body.String(), test.forbidText) {
				t.Fatalf("internal error leaked: %s", rec.Body.String())
			}
		})
	}
}

func TestManagementExternalIntegrationSourceTokenCreateHandlerLogFailureDoesNotOverrideCreated(t *testing.T) {
	queueStub := &operationLogQueueStub{err: errors.New("queue unavailable")}
	service := &managementExternalIntegrationSourceTokenCreateServiceStub{result: managementExternalIntegrationSourceTokenCreateResultFixture()}
	handler := newManagementExternalIntegrationSourceTokenCreateHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{Client: queueStub}),
	)
	req := managementExternalIntegrationSourceTokenCreateRequest("source_1", `{"name":"Token"}`)
	req = requestWithManagementExternalIntegrationSourceAuthContext(req, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated || queueStub.calls != 1 {
		t.Fatalf("status=%d logs=%d body=%s", rec.Code, queueStub.calls, rec.Body.String())
	}
}

func managementExternalIntegrationSourceTokenCreateRequest(sourceID string, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/external-integration-sources/source/tokens", strings.NewReader(body))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", sourceID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

func managementExternalIntegrationSourceTokenCreateResultFixture() managementexternalintegrationsources.TokenCreateResult {
	expiresAt := "2026-08-01T00:00:00.000Z"
	return managementexternalintegrationsources.TokenCreateResult{
		Source: managementexternalintegrationsources.Detail{Source: managementexternalintegrationsources.Source{
			ID:   "source_1",
			Name: "合作方",
		}},
		Token: managementexternalintegrationsources.CreatedToken{
			ID:          "token_1",
			Name:        "生产 Token",
			Token:       "juis_plaintext_token_secret",
			TokenPrefix: "juis_pre",
			TokenSuffix: "suffix88",
			Scopes:      []string{"juhe_ai_public:group_list:read"},
			ExpiresAt:   &expiresAt,
		},
	}
}

func managementExternalIntegrationSourceTokenCreateValidationError() error {
	service := managementexternalintegrationsources.NewTokenCreateService(managementExternalIntegrationSourceTokenCreatePortStub{}, "test-secret")
	_, err := service.Create(context.Background(), managementexternalintegrationsources.TokenCreateInput{SourceID: "source_1"})
	return err
}

type managementExternalIntegrationSourceTokenCreateServiceStub struct {
	input  managementexternalintegrationsources.TokenCreateInput
	result managementexternalintegrationsources.TokenCreateResult
	err    error
	calls  int
}

func (s *managementExternalIntegrationSourceTokenCreateServiceStub) Create(
	_ context.Context,
	input managementexternalintegrationsources.TokenCreateInput,
) (managementexternalintegrationsources.TokenCreateResult, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

type managementExternalIntegrationSourceTokenCreatePortStub struct{}

func (managementExternalIntegrationSourceTokenCreatePortStub) CreateManagementExternalIntegrationSourceToken(
	context.Context,
	port.ManagementExternalIntegrationSourceTokenCreateInput,
) (port.ManagementExternalIntegrationSourceTokenCreateResult, error) {
	panic("must not be called for invalid input")
}

var _ managementExternalIntegrationSourceTokenCreateService = (*managementexternalintegrationsources.TokenCreateService)(nil)
