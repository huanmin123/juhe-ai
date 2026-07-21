package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestManagementAccountBalanceSQLUsesCurrentAccountAndSnapshotModels(t *testing.T) {
	checks := map[string][]string{
		managementAccountBalanceSnapshotSQL:  {"juhe_stats.account_usage_snapshots", "relay_balance", "system_account_id = $2"},
		managementAccountBalanceCandidateSQL: {"juhe_business.accounts", "credentials_encrypted", "type = 'api_key'"},
		managementAccountBalanceUpsertSQL:    {"juhe_stats.account_usage_snapshots", "relay_balance", "ON CONFLICT"},
	}
	for sql, wants := range checks {
		for _, want := range wants {
			if !strings.Contains(sql, want) {
				t.Fatalf("balance SQL missing %q: %s", want, sql)
			}
		}
	}
}

func TestAccountBalanceQueryMigrationDefinesRuntimeColumns(t *testing.T) {
	source, err := os.ReadFile("../../../db/migrations/000065_w3_account_balance_query.sql")
	if err != nil {
		t.Fatalf("read account balance query migration: %v", err)
	}

	sql := string(source)
	for _, want := range []string{
		"ADD COLUMN IF NOT EXISTS balance_query_enabled boolean NOT NULL DEFAULT false",
		"ADD COLUMN IF NOT EXISTS balance_query_config_json text NOT NULL DEFAULT '{}'",
		"ADD COLUMN IF NOT EXISTS balance_query_next_refresh_at timestamptz",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("account balance query migration missing %q", want)
		}
	}
}

func TestAccountBalanceRefreshCandidatesUsePostgresBooleanPredicates(t *testing.T) {
	for _, sql := range []string{accountBalanceRefreshRecoveryCandidatesSQL, accountBalanceRefreshDueCandidatesSQL} {
		if strings.Contains(sql, "schedulable = 1") || strings.Contains(sql, "balance_query_enabled = 1") {
			t.Fatalf("balance refresh SQL uses integer boolean predicate: %s", sql)
		}
		for _, want := range []string{"schedulable = true", "balance_query_enabled = true"} {
			if !strings.Contains(sql, want) {
				t.Fatalf("balance refresh SQL missing %q: %s", want, sql)
			}
		}
	}
}
