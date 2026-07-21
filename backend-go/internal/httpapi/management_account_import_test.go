package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/modules/managementaccountimport"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

type importHTTPStub struct{ systemAccountID string }

func (s *importHTTPStub) Preview(_ context.Context, _ []byte, _ managementaccountimport.OptionsInput) (managementaccountimport.Result, error) {
	return managementaccountimport.Result{CanImport: true, Mode: "preview"}, nil
}
func (s *importHTTPStub) Confirm(_ context.Context, _ []byte, _ managementaccountimport.OptionsInput, owner string) (managementaccountimport.Result, error) {
	s.systemAccountID = owner
	return managementaccountimport.Result{CanImport: true, Imported: 1, Mode: "import"}, nil
}

func TestManagementAccountImportHandlerUsesSelfScope(t *testing.T) {
	service := &importHTTPStub{}
	handler := newManagementAccountImportHandler(service, managementAccountImportScopeSelf, true)
	req := httptest.NewRequest(http.MethodPost, "/my-accounts/import/confirm?systemAccountId=other", strings.NewReader(`{"data":{"type":"juhe-ai-account-import","version":1,"accounts":[]}}`))
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "owner-1", Role: "user"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || service.systemAccountID != "owner-1" {
		t.Fatalf("status=%d owner=%q body=%s", rec.Code, service.systemAccountID, rec.Body.String())
	}
}

func TestManagementAccountImportHandlerRequiresAdminForManagementScope(t *testing.T) {
	handler := newManagementAccountImportHandler(&importHTTPStub{}, managementAccountImportScopeAdmin, false)
	req := httptest.NewRequest(http.MethodPost, "/accounts/import/preview", strings.NewReader(`{"data":{}}`))
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "user", Role: "user"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
