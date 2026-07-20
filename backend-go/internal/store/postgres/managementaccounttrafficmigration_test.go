package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestManagementAccountTrafficMigrationQueriesKeepScopeLocksAndMutationGuards(t *testing.T) {
	raw, err := os.ReadFile("queries/w3_management_account_traffic_migration.sql")
	if err != nil {
		t.Fatalf("read query: %v", err)
	}
	sql := string(raw)
	for _, fragment := range []string{
		"FOR UPDATE OF accounts",
		"resource_authorizations.grantee_system_account_id = accounts.system_account_id",
		"group_accounts.account_authorization_id IS NOT DISTINCT FROM accounts.authorization_instance_authorization_id",
		"sqlc.arg(can_access_all)::boolean OR accounts.system_account_id = sqlc.arg(effective_system_account_id)::text",
		"cooldown_retest_observation_started_at",
		"INSERT INTO juhe_business.group_account_stats_dirty",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("query missing %q", fragment)
		}
	}
}
