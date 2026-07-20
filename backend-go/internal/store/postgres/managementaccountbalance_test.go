package postgres

import (
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
