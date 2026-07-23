package postgres

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestGatewayCandidateHydrationSQLUsesBoundedBulkReads(t *testing.T) {
	for _, fragment := range []string{
		"SELECT unnest($1::text[])",
		"FROM juhe_business.account_supported_models AS models",
		"FROM juhe_business.account_model_mappings AS mappings",
		"mappings.enabled = true",
		"ORDER BY mappings.source_model, mappings.source_endpoint_family, mappings.upstream_model",
		"FROM juhe_business.proxy_profiles",
		"WHERE id = ANY($1::text[])",
		"FROM juhe_stats.account_quality_scores",
		"last_sample_at >= $2::text",
	} {
		joined := gatewayCandidateAccountFactsSQL + gatewayCandidateProxyFactsSQL + gatewayCandidateFreshQualitySQL
		if !strings.Contains(joined, fragment) {
			t.Fatalf("hydration SQL missing %q", fragment)
		}
	}
	for _, sql := range []string{gatewayCandidateAccountFactsSQL, gatewayCandidateProxyFactsSQL, gatewayCandidateFreshQualitySQL} {
		if strings.Contains(strings.ToUpper(sql), " OFFSET ") || strings.Contains(strings.ToUpper(sql), "SELECT *") {
			t.Fatalf("hydration SQL must remain bounded and explicit: %s", sql)
		}
	}
}

func TestGatewayCandidateQualityOnlyDegradesForMissingSchema(t *testing.T) {
	for _, code := range []string{"42P01", "42703"} {
		if !gatewayCandidateQualityUnavailable(&pgconn.PgError{Code: code}) {
			t.Fatalf("code %s should degrade", code)
		}
	}
	if gatewayCandidateQualityUnavailable(&pgconn.PgError{Code: "08006"}) {
		t.Fatal("connection failure must not degrade")
	}
}

func TestNormalizedGatewayHydrationIDsDeduplicatesAndCaps(t *testing.T) {
	values := []string{" a ", "", "a", "b", "c"}
	got := normalizedGatewayHydrationIDs(values, 2)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("normalized ids = %#v", got)
	}
}
