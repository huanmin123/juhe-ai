package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/publicapi"
)

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

func withManagementExternalIntegrationSourceAuth(req *http.Request, role string) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_actor",
		Role:            role,
	}))
}
