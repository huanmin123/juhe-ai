package accountbalancesnapshotcleanup

import (
	"os"
	"strings"
	"testing"
)

func TestPostgresCleanupSQLKeepsNewerSnapshots(t *testing.T) {
	data, err := os.ReadFile("../../store/postgres/queries/w3_account_balance_snapshot_cleanup.sql")
	if err != nil {
		t.Fatalf("read cleanup SQL: %v", err)
	}
	sql := string(data)
	for _, want := range []string{
		"DELETE FROM juhe_stats.account_usage_snapshots",
		"system_account_id = sqlc.arg(system_account_id)::text",
		"account_id = sqlc.arg(account_id)::text",
		"kind = 'relay_balance'",
		"updated_at <= sqlc.arg(updated_before)::timestamptz",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("cleanup SQL missing %q", want)
		}
	}
}
