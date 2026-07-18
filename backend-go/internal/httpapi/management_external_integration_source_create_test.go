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

func TestManagementExternalIntegrationSourceCreateHandlerSuccessAndOperationLog(t *testing.T) {
	result := managementExternalIntegrationSourceCreateResultFixture()
	service := &managementExternalIntegrationSourceCreateServiceStub{result: result}
	queueStub := &operationLogQueueStub{}
	handler := newManagementExternalIntegrationSourceCreateHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Client:   queueStub,
			NewLogID: func() string { return "oplog_external_source_create" },
			Now:      func() time.Time { return time.Date(2026, 7, 16, 5, 6, 7, 0, time.UTC) },
		}),
	)
	body := `{"name":" 新来源 ","status":"disabled","scopes":["juhe_ai_public:group_list:read"],"rateLimits":[{"windowSeconds":60,"maxRequests":10}],"expiresAt":"2026-08-01T00:00:00.000Z","notes":"private note"}`
	req := managementExternalIntegrationSourceCreateRequest(body)
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
	input := service.input
	if input.Name != " 新来源 " || input.Status != "disabled" || input.ExpiresAt != "2026-08-01T00:00:00.000Z" ||
		input.Notes != "private note" {
		t.Fatalf("service input = %#v", input)
	}
	if _, ok := input.Scopes.([]any); !ok {
		t.Fatalf("scopes type = %T", input.Scopes)
	}
	if _, ok := input.RateLimits.([]any); !ok {
		t.Fatalf("rate limits type = %T", input.RateLimits)
	}
	var response struct {
		Data managementexternalintegrationsources.CreateResult `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil || !reflect.DeepEqual(response.Data, result) {
		t.Fatalf("response=%#v err=%v", response, err)
	}

	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("decode operation log: %v", err)
	}
	if logInput.ID != "oplog_external_source_create" || logInput.Module != "external_integration_sources" ||
		logInput.Action != "create" || logInput.OperationKey != "external_integration_sources.create" ||
		logInput.ResourceType != "external_integration_source" || logInput.ResourceID != "source_1" ||
		logInput.ResourceName != "新来源" || logInput.DetailLevel != "full" ||
		logInput.VisibilityScope != "admin_only" || logInput.Mode != "self" ||
		logInput.StatusCode == nil || *logInput.StatusCode != http.StatusCreated || len(logInput.Changes) != 4 {
		t.Fatalf("operation log = %#v", logInput)
	}
	wantFields := []string{"name", "status", "expiresAt", "rateLimits"}
	for index, field := range wantFields {
		if logInput.Changes[index].Field != field {
			t.Fatalf("operation log changes = %#v", logInput.Changes)
		}
	}
	encodedLog := string(queueStub.payload)
	for _, secret := range []string{"juis_plaintext_secret", "private note", "tokenHash", "ciphertext"} {
		if strings.Contains(encodedLog, secret) {
			t.Fatalf("operation log leaked %q: %s", secret, encodedLog)
		}
	}
}

func TestManagementExternalIntegrationSourceCreateHandlerStrictPayload(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCalls  int
	}{
		{name: "valid defaults", body: `{"name":"source"}`, wantStatus: http.StatusCreated, wantCalls: 1},
		{name: "unknown field", body: `{"name":"source","unknown":true}`, wantStatus: http.StatusBadRequest},
		{name: "trailing JSON", body: `{"name":"source"} {}`, wantStatus: http.StatusBadRequest},
		{name: "array root", body: `[]`, wantStatus: http.StatusBadRequest},
		{name: "invalid JSON", body: `{`, wantStatus: http.StatusBadRequest},
		{name: "name null", body: `{"name":null}`, wantStatus: http.StatusBadRequest},
		{name: "status null", body: `{"name":"source","status":null}`, wantStatus: http.StatusBadRequest},
		{name: "scopes null", body: `{"name":"source","scopes":null}`, wantStatus: http.StatusBadRequest},
		{name: "rate limits null", body: `{"name":"source","rateLimits":null}`, wantStatus: http.StatusBadRequest},
		{name: "expires number", body: `{"name":"source","expiresAt":1}`, wantStatus: http.StatusBadRequest},
		{name: "notes number", body: `{"name":"source","notes":1}`, wantStatus: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementExternalIntegrationSourceCreateServiceStub{result: managementExternalIntegrationSourceCreateResultFixture()}
			handler := newManagementExternalIntegrationSourceCreateHandler(service, managementOperationLogOptions{})
			req := managementExternalIntegrationSourceCreateRequest(test.body)
			req = requestWithManagementExternalIntegrationSourceAuthContext(req, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != test.wantStatus || service.calls != test.wantCalls {
				t.Fatalf("status=%d want=%d calls=%d want=%d body=%s", rec.Code, test.wantStatus, service.calls, test.wantCalls, rec.Body.String())
			}
		})
	}
}

func TestManagementExternalIntegrationSourceCreateHandlerChecksBoundaryBeforeBody(t *testing.T) {
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
			service := &managementExternalIntegrationSourceCreateServiceStub{}
			var handler http.Handler = newManagementExternalIntegrationSourceCreateHandler(service, managementOperationLogOptions{})
			if test.nilService {
				handler = newManagementExternalIntegrationSourceCreateHandler(nil, managementOperationLogOptions{})
			}
			req := managementExternalIntegrationSourceCreateRequest(`{`)
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

func TestManagementExternalIntegrationSourceCreateHandlerMapsErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantText   string
	}{
		{name: "validation", err: managementExternalIntegrationSourceCreateValidationError(), wantStatus: http.StatusBadRequest, wantText: "来源系统名称不能为空"},
		{name: "name exists", err: managementexternalintegrationsources.ErrNameExists, wantStatus: http.StatusBadRequest, wantText: "来源系统名称已存在"},
		{name: "token exists", err: managementexternalintegrationsources.ErrTokenExists, wantStatus: http.StatusBadRequest, wantText: "来源系统 token 已存在，请重新生成"},
		{name: "internal", err: errors.New("database password leaked"), wantStatus: http.StatusInternalServerError, wantText: "服务器内部错误"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementExternalIntegrationSourceCreateServiceStub{err: test.err}
			handler := newManagementExternalIntegrationSourceCreateHandler(service, managementOperationLogOptions{})
			req := managementExternalIntegrationSourceCreateRequest(`{"name":"source"}`)
			req = requestWithManagementExternalIntegrationSourceAuthContext(req, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != test.wantStatus || service.calls != 1 || !strings.Contains(rec.Body.String(), test.wantText) {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.calls, rec.Body.String())
			}
		})
	}
}

func TestManagementExternalIntegrationSourceCreateHandlerLogFailureDoesNotOverrideCreated(t *testing.T) {
	queueStub := &operationLogQueueStub{err: errors.New("queue unavailable")}
	service := &managementExternalIntegrationSourceCreateServiceStub{result: managementExternalIntegrationSourceCreateResultFixture()}
	handler := newManagementExternalIntegrationSourceCreateHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{Client: queueStub}),
	)
	req := managementExternalIntegrationSourceCreateRequest(`{"name":"source"}`)
	req = requestWithManagementExternalIntegrationSourceAuthContext(req, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated || queueStub.calls != 1 {
		t.Fatalf("status=%d logs=%d body=%s", rec.Code, queueStub.calls, rec.Body.String())
	}
}

func managementExternalIntegrationSourceCreateRequest(body string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/__aisys__/api/external-integration-sources", strings.NewReader(body))
}

func managementExternalIntegrationSourceCreateResultFixture() managementexternalintegrationsources.CreateResult {
	expiresAt := "2026-08-01T00:00:00.000Z"
	notes := "private note"
	return managementexternalintegrationsources.CreateResult{
		Source: managementexternalintegrationsources.Source{
			ID:         "source_1",
			Name:       "新来源",
			Status:     "disabled",
			RateLimits: []managementexternalintegrationsources.RateLimitRule{{WindowSeconds: 60, MaxRequests: 10}},
			ExpiresAt:  &expiresAt,
			Notes:      &notes,
		},
		Token: managementexternalintegrationsources.CreatedToken{
			ID:    "token_1",
			Name:  "新来源 生产 Token",
			Token: "juis_plaintext_secret",
		},
	}
}

func managementExternalIntegrationSourceCreateValidationError() error {
	service := managementexternalintegrationsources.NewCreateService(managementExternalIntegrationSourceCreatePortStub{}, "test-secret")
	_, err := service.Create(context.Background(), managementexternalintegrationsources.CreateInput{Name: ""})
	return err
}

type managementExternalIntegrationSourceCreateServiceStub struct {
	input  managementexternalintegrationsources.CreateInput
	result managementexternalintegrationsources.CreateResult
	err    error
	calls  int
}

func (s *managementExternalIntegrationSourceCreateServiceStub) Create(
	_ context.Context,
	input managementexternalintegrationsources.CreateInput,
) (managementexternalintegrationsources.CreateResult, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

type managementExternalIntegrationSourceCreatePortStub struct{}

func (managementExternalIntegrationSourceCreatePortStub) CreateManagementExternalIntegrationSource(
	context.Context,
	port.ManagementExternalIntegrationSourceCreateInput,
) (port.ManagementExternalIntegrationSourceCreateResult, error) {
	panic("must not be called for invalid input")
}
