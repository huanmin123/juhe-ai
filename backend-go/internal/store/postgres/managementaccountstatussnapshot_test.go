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

func TestManagementAccountStatusSnapshotRuntimeQueriesReuseSourceAccount(t *testing.T) {
	for _, fragment := range []string{
		"COALESCE(source.id, account.id)",
		"COALESCE(source.credentials_encrypted, account.credentials_encrypted)",
		"account.id = ANY($1::text[])",
		"source.id IS NULL OR source.deleted_at IS NULL",
	} {
		if !strings.Contains(managementAccountAPIKeyRuntimeSourcesSQL, fragment) {
			t.Fatalf("runtime source query missing %q", fragment)
		}
	}
	if strings.Contains(managementAccountAPIKeyRuntimeSourcesSQL, "ON source.id = account.authorization_instance_source_account_id\n AND source.deleted_at IS NULL") {
		t.Fatal("runtime source query falls back to instance credentials when the source account is deleted")
	}
	for _, fragment := range []string{
		"account_id = ANY($1::text[])",
		"ORDER BY account_id, key_index, key_fingerprint",
	} {
		if !strings.Contains(managementAccountAPIKeyRuntimeStatesSQL, fragment) {
			t.Fatalf("runtime state query missing %q", fragment)
		}
	}
}
