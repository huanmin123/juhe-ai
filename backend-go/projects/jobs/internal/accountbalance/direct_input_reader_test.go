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
	recovery := j2CandidateSQL(candidateReadRecovery)
	if !strings.Contains(recovery, "balance_query_next_refresh_at IS NULL") || !strings.Contains(recovery, "ORDER BY a.id ASC") {
		t.Fatal("recovery query must select enabled candidates whose schedule is missing")
	}
	firstProbe := j2CandidateSQL(candidateReadFirstProbe)
	if !strings.Contains(firstProbe, "balance_query_enabled = 0") || !strings.Contains(firstProbe, "balance_query_config_json = '{}'") {
		t.Fatal("first probe query must retain the empty-config intent fence")
	}
}

func TestJ2CandidateByIDSQLKeepsAccountPredicateBeforeOrderBy(t *testing.T) {
	query := j2CandidateByIDSQL(candidateReadDue)
	if !strings.Contains(query, "AND a.id=$2 ORDER BY") {
		t.Fatalf("account predicate must be part of WHERE: %s", query)
	}
	if strings.Contains(query, "ORDER BY a.balance_query_next_refresh_at ASC,a.id ASC AND") {
		t.Fatalf("account predicate was appended after ORDER BY: %s", query)
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
