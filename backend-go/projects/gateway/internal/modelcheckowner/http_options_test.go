package modelcheckowner

import (
	"net/http/httptest"
	"testing"
)

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
