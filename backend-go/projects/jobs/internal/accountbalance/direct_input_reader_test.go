package accountbalance

import (
	"strings"
	"testing"
)

func TestJ2CandidateQueriesPreserveDueRecoveryAndFirstProbeShapes(t *testing.T) {
	due := j2CandidateSQL(candidateReadDue)
	if !strings.Contains(due, "balance_query_next_refresh_at IS NOT NULL") || !strings.Contains(due, "::timestamptz <= $1") {
		t.Fatal("periodic query must select only due scheduled candidates")
	}
	if !strings.Contains(due, "LIMIT $2") || strings.Contains(due, "$3") {
		t.Fatalf("due query parameter shape changed: %s", due)
	}
	recovery := j2CandidateSQL(candidateReadRecovery)
	if !strings.Contains(recovery, "balance_query_next_refresh_at IS NULL") || !strings.Contains(recovery, "ORDER BY a.id ASC") {
		t.Fatal("recovery query must select enabled candidates whose schedule is missing")
	}
	if !strings.Contains(recovery, "LIMIT $1") || strings.Contains(recovery, "$2") {
		t.Fatalf("recovery query must bind only its limit: %s", recovery)
	}
	firstProbe := j2CandidateSQL(candidateReadFirstProbe)
	if !strings.Contains(firstProbe, "balance_query_enabled = 0") || !strings.Contains(firstProbe, "balance_query_config_json = '{}'") {
		t.Fatal("first probe query must retain the empty-config intent fence")
	}
	if !strings.Contains(firstProbe, "::timestamptz <= $1") || !strings.Contains(firstProbe, "LIMIT $2") {
		t.Fatalf("first probe query parameter shape changed: %s", firstProbe)
	}
}

func TestJ2CandidateByIDSQLKeepsAccountPredicateBeforeOrderBy(t *testing.T) {
	query := j2CandidateByIDSQL(candidateReadDue)
	if !strings.Contains(query, "AND a.id=$1 ORDER BY") {
		t.Fatalf("account predicate must be part of WHERE: %s", query)
	}
	if strings.Contains(query, "$2") {
		t.Fatalf("due account query must use only the account ID placeholder: %s", query)
	}
	if strings.Contains(query, "ORDER BY a.balance_query_next_refresh_at ASC,a.id ASC AND") {
		t.Fatalf("account predicate was appended after ORDER BY: %s", query)
	}
}

func TestJ2CandidateByIDSQLUsesDisjointKindParameters(t *testing.T) {
	tests := []struct {
		kind        candidateReadKind
		mustHave    []string
		mustNotHave []string
	}{
		{
			kind:        candidateReadDue,
			mustHave:    []string{"AND a.id=$1 ORDER BY", "LIMIT 1"},
			mustNotHave: []string{"$2", "::timestamptz <="},
		},
		{
			kind:        candidateReadFirstProbe,
			mustHave:    []string{"AND a.id=$1 ORDER BY", "::timestamptz <= $2", "LIMIT 1"},
			mustNotHave: []string{"::timestamptz <= $1", "LIMIT $2"},
		},
		{
			kind:        candidateReadRecovery,
			mustHave:    []string{"AND a.id=$1 ORDER BY", "balance_query_next_refresh_at IS NULL", "LIMIT 1"},
			mustNotHave: []string{"$2", "LIMIT $1"},
		},
	}
	for _, test := range tests {
		query := j2CandidateByIDSQL(test.kind)
		for _, fragment := range test.mustHave {
			if !strings.Contains(query, fragment) {
				t.Fatalf("kind %d query must contain %q: %s", test.kind, fragment, query)
			}
		}
		for _, fragment := range test.mustNotHave {
			if strings.Contains(query, fragment) {
				t.Fatalf("kind %d query must not contain %q: %s", test.kind, fragment, query)
			}
		}
	}
}

func TestJ2RecoveryQueryUsesOnlyLimitPlaceholder(t *testing.T) {
	query := j2CandidateSQL(candidateReadRecovery)
	if strings.Contains(query, "$1::timestamptz IS NOT NULL") {
		t.Fatalf("recovery query must not use a dummy time placeholder: %s", query)
	}
	if !strings.Contains(query, "balance_query_next_refresh_at IS NULL") || !strings.Contains(query, "LIMIT $1") {
		t.Fatalf("recovery query must bind only its limit: %s", query)
	}
}

func TestJ2DirectInputSupportsNodeProxyTypes(t *testing.T) {
	reader := &PostgresDirectInputReader{secret: "j2-proxy-test-secret"}
	for _, kind := range []string{"http", "https", "socks5", "socks5h"} {
		proxy, err := reader.makeProxy(kind, "127.0.0.1", 1080, "", "")
		if err != nil {
			t.Fatalf("proxy kind %q must remain compatible: %v", kind, err)
		}
		if kind == "socks5" {
			plain, err := DecryptV1Envelope(reader.secret, proxy.Ciphertext)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(plain), `{"url":"socks5h://`) {
				t.Fatalf("legacy socks5 profile must use Node-compatible remote DNS: %s", plain)
			}
		}
	}
}
