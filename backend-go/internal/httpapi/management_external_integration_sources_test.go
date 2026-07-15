package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementexternalintegrationsources"
	"juhe-ai/backend-go/internal/modules/publicapi"
)

func TestManagementExternalIntegrationSourceListHandlerReturnsDataForAdminRoles(t *testing.T) {
	wantResult := managementexternalintegrationsources.ListResult{
		Items:          []managementexternalintegrationsources.Source{},
		Page:           1,
		PageSize:       10,
		PageUpperBound: 0,
		HasMore:        false,
	}
	for _, role := range []string{"admin", "super_admin"} {
		t.Run(role, func(t *testing.T) {
			service := &managementExternalIntegrationSourceListServiceStub{result: wantResult}
			handler := newManagementExternalIntegrationSourceListHandler(service)
			req := httptest.NewRequest(
				http.MethodGet,
				"/__aisys__/api/external-integration-sources?page=1.0&pageSize=1e1&keyword=%20MiXeD%20&status=active&ignored=value",
				nil,
			)
			req = withManagementExternalIntegrationSourceAuth(req, role)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
				t.Fatalf("Content-Type = %q", got)
			}
			if len(service.calls) != 1 {
				t.Fatalf("service calls = %d, want 1", len(service.calls))
			}
			wantInput := managementexternalintegrationsources.ListInput{
				Page:             1,
				PageSize:         10,
				PageSizeProvided: true,
				Keyword:          "MiXeD",
				Status:           "active",
			}
			if got := service.calls[0]; !reflect.DeepEqual(got, wantInput) {
				t.Fatalf("service input = %#v, want %#v", got, wantInput)
			}
			var body struct {
				Data managementexternalintegrationsources.ListResult `json:"data"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if !reflect.DeepEqual(body.Data, wantResult) {
				t.Fatalf("data = %#v, want %#v", body.Data, wantResult)
			}
		})
	}
}

func TestManagementExternalIntegrationSourceListHandlerPreservesDefaultsAndSingleQSArrays(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		wantInput managementexternalintegrationsources.ListInput
	}{
		{
			name:   "defaults and unknown fields",
			target: "/__aisys__/api/external-integration-sources?unknown=value",
		},
		{
			name:   "single qs arrays",
			target: "/__aisys__/api/external-integration-sources?page%5B%5D=2&pageSize%5B%5D=5",
			wantInput: managementexternalintegrationsources.ListInput{
				Page:             2,
				PageSize:         5,
				PageSizeProvided: true,
			},
		},
		{
			name:   "single indexed and nested qs arrays",
			target: "/__aisys__/api/external-integration-sources?page%5B0%5D=3&pageSize%5B%5D%5B0%5D=7",
			wantInput: managementexternalintegrationsources.ListInput{
				Page:             3,
				PageSize:         7,
				PageSizeProvided: true,
			},
		},
		{
			name:   "qs maximum depth array",
			target: "/__aisys__/api/external-integration-sources?page%5B%5D%5B%5D%5B%5D%5B%5D%5B%5D=4",
			wantInput: managementexternalintegrationsources.ListInput{
				Page: 4,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementExternalIntegrationSourceListServiceStub{
				result: managementexternalintegrationsources.ListResult{Items: []managementexternalintegrationsources.Source{}},
			}
			handler := newManagementExternalIntegrationSourceListHandler(service)
			req := withManagementExternalIntegrationSourceAuth(httptest.NewRequest(http.MethodGet, test.target, nil), "admin")
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
			}
			if len(service.calls) != 1 || !reflect.DeepEqual(service.calls[0], test.wantInput) {
				t.Fatalf("service calls = %#v, want %#v", service.calls, test.wantInput)
			}
		})
	}
}

func TestManagementExternalIntegrationSourceListHandlerRejectsInvalidZodQueriesInFieldOrder(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		wantMessage string
	}{
		{name: "page empty", query: "page=", wantMessage: "Number must be greater than or equal to 1"},
		{name: "page NaN", query: "page=NaN", wantMessage: "Expected number, received nan"},
		{name: "page non-number", query: "page=nope", wantMessage: "Expected number, received nan"},
		{name: "page non-integer", query: "page=1.5", wantMessage: "Expected integer, received float"},
		{name: "page lower bound", query: "page=0", wantMessage: "Number must be greater than or equal to 1"},
		{name: "page repeated", query: "page=1&page=2", wantMessage: "Expected number, received nan"},
		{name: "page direct and bracket array", query: "page=1&page%5B%5D=2", wantMessage: "Expected number, received nan"},
		{name: "page object bracket", query: "page%5Bfoo%5D=2", wantMessage: "Expected number, received nan"},
		{name: "page nested object bracket", query: "page%5B0%5D%5Bfoo%5D=2", wantMessage: "Expected number, received nan"},
		{name: "page index outside qs array limit", query: "page%5B20%5D=2", wantMessage: "Expected number, received nan"},
		{name: "page beyond qs maximum depth", query: "page%5B%5D%5B%5D%5B%5D%5B%5D%5B%5D%5B%5D=2", wantMessage: "Expected number, received nan"},
		{name: "page size empty", query: "pageSize=", wantMessage: "Number must be greater than or equal to 1"},
		{name: "page size NaN", query: "pageSize=NaN", wantMessage: "Expected number, received nan"},
		{name: "page size non-integer", query: "pageSize=1.5", wantMessage: "Expected integer, received float"},
		{name: "page size lower bound", query: "pageSize=0", wantMessage: "Number must be greater than or equal to 1"},
		{name: "page size upper bound", query: "pageSize=101", wantMessage: "Number must be less than or equal to 100"},
		{name: "page size repeated", query: "pageSize=10&pageSize=20", wantMessage: "Expected number, received nan"},
		{name: "keyword repeated", query: "keyword=first&keyword=second", wantMessage: "Expected string, received array"},
		{name: "keyword bracket array", query: "keyword%5B%5D=first", wantMessage: "Expected string, received array"},
		{name: "keyword indexed bracket array", query: "keyword%5B0%5D=first", wantMessage: "Expected string, received array"},
		{name: "keyword object bracket", query: "keyword%5Bfoo%5D=first", wantMessage: "Expected string, received object"},
		{name: "keyword direct and bracket array", query: "keyword=first&keyword%5B%5D=second", wantMessage: "Expected string, received array"},
		{name: "keyword direct and object bracket", query: "keyword=first&keyword%5Bfoo%5D=second", wantMessage: "Expected string, received array"},
		{name: "status repeated", query: "status=active&status=disabled", wantMessage: "Expected 'all' | 'active' | 'disabled', received array"},
		{name: "status bracket array", query: "status%5B%5D=active", wantMessage: "Expected 'all' | 'active' | 'disabled', received array"},
		{name: "status indexed bracket array", query: "status%5B0%5D=active", wantMessage: "Expected 'all' | 'active' | 'disabled', received array"},
		{name: "status object bracket", query: "status%5Bfoo%5D=active", wantMessage: "Expected 'all' | 'active' | 'disabled', received object"},
		{name: "status direct and bracket array", query: "status=active&status%5B%5D=disabled", wantMessage: "Expected 'all' | 'active' | 'disabled', received array"},
		{name: "status is not trimmed", query: "status=%20active%20", wantMessage: "Invalid enum value. Expected 'all' | 'active' | 'disabled', received ' active '"},
		{
			name:        "page is first issue",
			query:       "page=0&pageSize=101&keyword=first&keyword=second&status=bad",
			wantMessage: "Number must be greater than or equal to 1",
		},
		{
			name:        "page size precedes keyword and status",
			query:       "pageSize=101&keyword=first&keyword=second&status=bad",
			wantMessage: "Number must be less than or equal to 100",
		},
		{
			name:        "keyword precedes status",
			query:       "keyword=first&keyword=second&status=bad",
			wantMessage: "Expected string, received array",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementExternalIntegrationSourceListServiceStub{}
			handler := newManagementExternalIntegrationSourceListHandler(service)
			req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/external-integration-sources?"+test.query, nil)
			req = withManagementExternalIntegrationSourceAuth(req, "admin")
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
			}
			assertManagementExternalIntegrationSourceMessage(t, rec, test.wantMessage)
			if len(service.calls) != 0 {
				t.Fatalf("service calls = %d, want 0", len(service.calls))
			}
		})
	}
}

func TestManagementExternalIntegrationSourceListHandlerAuthAndServiceFailures(t *testing.T) {
	tests := []struct {
		name        string
		role        *string
		service     managementExternalIntegrationSourceListService
		wantStatus  int
		wantMessage string
	}{
		{name: "missing auth context", service: &managementExternalIntegrationSourceListServiceStub{}, wantStatus: http.StatusInternalServerError, wantMessage: "服务器内部错误"},
		{name: "ordinary user", role: managementExternalIntegrationSourceRolePointer("user"), service: &managementExternalIntegrationSourceListServiceStub{}, wantStatus: http.StatusForbidden, wantMessage: "需要管理员权限"},
		{name: "nil service", role: managementExternalIntegrationSourceRolePointer("admin"), wantStatus: http.StatusInternalServerError, wantMessage: "服务器内部错误"},
		{name: "store error", role: managementExternalIntegrationSourceRolePointer("admin"), service: &managementExternalIntegrationSourceListServiceStub{err: errors.New("postgres unavailable")}, wantStatus: http.StatusInternalServerError, wantMessage: "服务器内部错误"},
		{name: "stored JSON error", role: managementExternalIntegrationSourceRolePointer("admin"), service: &managementExternalIntegrationSourceListServiceStub{err: errors.New("decode scopes JSON")}, wantStatus: http.StatusInternalServerError, wantMessage: "服务器内部错误"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := newManagementExternalIntegrationSourceListHandler(test.service)
			req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/external-integration-sources", nil)
			if test.role != nil {
				req = withManagementExternalIntegrationSourceAuth(req, *test.role)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, test.wantStatus, rec.Body.String())
			}
			assertManagementExternalIntegrationSourceMessage(t, rec, test.wantMessage)
		})
	}
}

func TestManagementExternalIntegrationSourceScopesHandlerReturnsCatalog(t *testing.T) {
	handler := newManagementExternalIntegrationSourceScopesHandler()
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/external-integration-sources/scopes", nil)
	req = withManagementExternalIntegrationSourceAuth(req, "admin")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	var body struct {
		Data []publicapi.ScopeOption `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got, want := len(body.Data), 16; got != want {
		t.Fatalf("scope options = %d, want %d", got, want)
	}
	if want := publicapi.ScopeOptions(); !reflect.DeepEqual(body.Data, want) {
		t.Fatalf("scope options = %+v, want %+v", body.Data, want)
	}
}

func TestManagementExternalIntegrationSourceScopesHandlerRequiresAdmin(t *testing.T) {
	handler := newManagementExternalIntegrationSourceScopesHandler()
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/external-integration-sources/scopes", nil)
	req = withManagementExternalIntegrationSourceAuth(req, "user")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["message"] != "需要管理员权限" {
		t.Fatalf("message = %q, want 需要管理员权限", body["message"])
	}
}

func TestManagementExternalIntegrationSourceScopesHandlerRejectsMissingAuthContext(t *testing.T) {
	handler := newManagementExternalIntegrationSourceScopesHandler()
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/external-integration-sources/scopes", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["message"] != "服务器内部错误" {
		t.Fatalf("message = %q, want 服务器内部错误", body["message"])
	}
}

func TestManagementExternalIntegrationSourceAPIDocsHandlerReturnsCatalog(t *testing.T) {
	handler := newManagementExternalIntegrationSourceAPIDocsHandler()
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/external-integration-sources/api-docs", nil)
	req = withManagementExternalIntegrationSourceAuth(req, "admin")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	var body struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	var gotCatalog any
	if err := json.Unmarshal(body.Data, &gotCatalog); err != nil {
		t.Fatalf("decode response catalog: %v", err)
	}
	var wantCatalog any
	if err := json.Unmarshal(publicapi.APIDocsCatalog(), &wantCatalog); err != nil {
		t.Fatalf("decode expected catalog: %v", err)
	}
	if !reflect.DeepEqual(gotCatalog, wantCatalog) {
		t.Fatalf("api docs catalog = %#v, want %#v", gotCatalog, wantCatalog)
	}
}

func TestManagementExternalIntegrationSourceAPIDocsHandlerRequiresAdmin(t *testing.T) {
	handler := newManagementExternalIntegrationSourceAPIDocsHandler()
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/external-integration-sources/api-docs", nil)
	req = withManagementExternalIntegrationSourceAuth(req, "user")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["message"] != "需要管理员权限" {
		t.Fatalf("message = %q, want 需要管理员权限", body["message"])
	}
}

func TestManagementExternalIntegrationSourceAPIDocsHandlerRejectsMissingAuthContext(t *testing.T) {
	handler := newManagementExternalIntegrationSourceAPIDocsHandler()
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/external-integration-sources/api-docs", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["message"] != "服务器内部错误" {
		t.Fatalf("message = %q, want 服务器内部错误", body["message"])
	}
}

type managementExternalIntegrationSourceListServiceStub struct {
	result managementexternalintegrationsources.ListResult
	err    error
	calls  []managementexternalintegrationsources.ListInput
}

func (s *managementExternalIntegrationSourceListServiceStub) List(
	_ context.Context,
	input managementexternalintegrationsources.ListInput,
) (managementexternalintegrationsources.ListResult, error) {
	s.calls = append(s.calls, input)
	return s.result, s.err
}

func assertManagementExternalIntegrationSourceMessage(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	want string,
) {
	t.Helper()
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["message"] != want {
		t.Fatalf("message = %q, want %q", body["message"], want)
	}
}

func managementExternalIntegrationSourceRolePointer(role string) *string {
	return &role
}

func withManagementExternalIntegrationSourceAuth(req *http.Request, role string) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_actor",
		Role:            role,
	}))
}
