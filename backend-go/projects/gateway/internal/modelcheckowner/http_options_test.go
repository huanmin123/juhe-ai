package modelcheckowner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type accountOptionsScopeSpy struct {
	query AccountOptionsQuery
}

func (s *accountOptionsScopeSpy) ListAccountOptions(_ context.Context, query AccountOptionsQuery) ([]AccountOption, error) {
	s.query = query
	return []AccountOption{{ID: "acct-1", Name: "Account"}}, nil
}

func (s *accountOptionsScopeSpy) ModelCheckOptions() ModelCheckOptions { return ModelCheckOptions{} }

func TestParseAccountOptionsQueryRejectsAmbiguousSelectedIDs(t *testing.T) {
	request := httptest.NewRequest("GET", "/account-options?purpose=run&selectedIds=a&selectedIds%5B%5D=b", nil)
	if _, err := parseAccountOptionsQuery(request); err == nil {
		t.Fatal("both selectedIds encodings must be rejected")
	}
}

func TestParseAccountOptionsQuerySupportsBracketEncoding(t *testing.T) {
	request := httptest.NewRequest("GET", "/account-options?purpose=history&selectedIds%5B%5D=a&selectedIds%5B%5D=b", nil)
	query, err := parseAccountOptionsQuery(request)
	if err != nil {
		t.Fatal(err)
	}
	if query.Purpose != "history" || len(query.SelectedID) != 2 || query.Limit != 50 {
		t.Fatalf("query=%+v", query)
	}
}

func TestHTTPAccountOptionsUsesAuthenticatedSystemAccountScope(t *testing.T) {
	handler := newTestHTTPHandler()
	spy := &accountOptionsScopeSpy{}
	handler.AccountOptions = spy
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/account-options?purpose=run", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if spy.query.SystemAccountID != "sys-1" || spy.query.Purpose != "run" {
		t.Fatalf("scope query=%+v", spy.query)
	}
}

func TestHTTPAccountOptionsForwardsAdministratorGlobalScope(t *testing.T) {
	handler := newTestHTTPHandler()
	handler.AllowCrossAccount = true
	spy := &accountOptionsScopeSpy{}
	handler.AccountOptions = spy
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/account-options?purpose=run&systemAccountId=all", nil))
	if response.Code != http.StatusOK || !spy.query.AllSystemAccounts || spy.query.SystemAccountID != "" {
		t.Fatalf("status=%d scope query=%+v body=%s", response.Code, spy.query, response.Body.String())
	}
}

func TestHTTPAccountOptionsRejectsMissingAuthenticatedScope(t *testing.T) {
	handler := newTestHTTPHandler()
	handler.AccountOptions = &accountOptionsScopeSpy{}
	handler.Authorize = func(context.Context, *http.Request) (string, error) { return "", nil }
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/account-options?purpose=run", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
