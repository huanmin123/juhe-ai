package migrations

import (
	"os"
	"strings"
	"testing"
)

func TestClientIPAccountUsageRangeWindowsMigrationMatchesNodeWorkerContract(t *testing.T) {
	source, err := os.ReadFile("000041_w6_management_client_ip_stats_detail.sql")
	if err != nil {
		t.Fatalf("read client IP account range migration: %v", err)
	}
	up, down, found := strings.Cut(string(source), "-- +goose Down")
	if !found {
		t.Fatal("migration is missing goose Down marker")
	}
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS juhe_stats.client_ip_account_usage_range_windows",
		"PRIMARY KEY (ip_hash, account_id, start_date, end_date)",
		"CREATE INDEX IF NOT EXISTS idx_client_ip_account_range_requests",
		"request_count DESC",
		"account_id",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("migration Up section missing %q", required)
		}
	}
	if strings.Contains(up, "REFERENCES ") || strings.Contains(up, "FOREIGN KEY") {
		t.Fatal("shared stats window must not depend on business-table foreign keys")
	}
	if !strings.Contains(down, "-- no-op:") || strings.Contains(down, "DROP TABLE") {
		t.Fatal("migration Down section must remain a non-destructive no-op")
	}
}
