package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/modules/managementaccountexport"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

func TestManagementAccountExportHandlerUsesSelfScopeAndStreamsDataEnvelope(t *testing.T) {
	service := &accountExportServiceStub{result: managementaccountexport.Result{
		Document: managementaccountexport.Document{Type: managementaccountexport.ProtocolType, Version: 1, Accounts: []managementaccountexport.Account{{Ref: "account-1"}}},
		Summary:  managementaccountexport.Summary{Accounts: 1},
	}}
	handler := newManagementAccountExportHandler(service, managementAccountExportScopeSelf)
	req := httptest.NewRequest(http.MethodPost, "/my-accounts/export?systemAccountId=other", strings.NewReader(`{"accountIds":["account-1"]}`))
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "owner-1", Role: "user"}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || service.input.SystemAccountID != "owner-1" {
		t.Fatalf("status=%d input=%+v body=%s", rec.Code, service.input, rec.Body.String())
	}
	var body struct {
		Data managementaccountexport.Result `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Data.Document.Type != managementaccountexport.ProtocolType {
		t.Fatalf("body=%s err=%v", rec.Body.String(), err)
	}
}

func TestManagementAccountExportHandlerRejectsInvalidAndNonAdminRequests(t *testing.T) {
	service := &accountExportServiceStub{}
	handler := newManagementAccountExportHandler(service, managementAccountExportScopeAdmin)
	for _, test := range []struct {
		name string
		role string
		body string
		want int
	}{
		{name: "admin required", role: "user", body: `{"accountIds":["account-1"]}`, want: http.StatusForbidden},
		{name: "ambiguous", role: "admin", body: `{"accountIds":["account-1"],"filters":{}}`, want: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/accounts/export", strings.NewReader(test.body))
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "actor", Role: test.role}))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, test.want, rec.Body.String())
			}
		})
	}
}

type accountExportServiceStub struct {
	input  managementaccountexport.Input
	result managementaccountexport.Result
	err    error
}

func (s *accountExportServiceStub) Write(_ *http.Request, writer io.Writer, input managementaccountexport.Input) (managementaccountexport.Summary, error) {
	s.input = input
	if s.err != nil {
		return managementaccountexport.Summary{}, s.err
	}
	if err := json.NewEncoder(writer).Encode(map[string]any{"data": s.result}); err != nil {
		return managementaccountexport.Summary{}, err
	}
	return s.result.Summary, nil
}
