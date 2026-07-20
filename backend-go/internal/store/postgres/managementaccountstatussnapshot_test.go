package postgres

import (
	"strings"
	"testing"
)

func TestManagementAccountStatusSnapshotSQLKeepsScopeAndInputOrder(t *testing.T) {
	for _, fragment := range []string{
		"a.id = ANY($1::text[])",
		"$2 = '' OR a.system_account_id = $2",
		"ORDER BY array_position($1::text[], a.id)",
		"juhe_stats.usage_stats_daily",
		"ga.account_authorization_id",
		"authorization_unavailable",
	} {
		if !strings.Contains(managementAccountStatusSnapshotSQL, fragment) {
			t.Fatalf("query missing %q", fragment)
		}
	}
	for _, forbidden := range []string{"usage_records", "credentials_encrypted", "credential_mask"} {
		if strings.Contains(managementAccountStatusSnapshotSQL, forbidden) {
			t.Fatalf("query contains forbidden source %q", forbidden)
		}
	}
}
