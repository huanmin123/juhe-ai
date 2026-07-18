package migrationtests

import (
	"os"
	"strings"
	"testing"
)

func TestAPIKeyRecordCleanupTargetsMigrationMatchesNodeWorkerContract(t *testing.T) {
	source, err := os.ReadFile(migrationPath("000037_w5_api_key_record_cleanup_targets.sql"))
	if err != nil {
		t.Fatalf("read API Key cleanup target migration: %v", err)
	}
	sql := string(source)
	up, down, found := strings.Cut(sql, "-- +goose Down")
	if !found {
		t.Fatal("migration is missing goose Down marker")
	}
	for _, required := range []string{
		"CREATE TABLE juhe_dataset.api_key_record_cleanup_targets",
		"api_key_id text PRIMARY KEY",
		"system_account_id text NOT NULL",
		"created_at timestamptz NOT NULL",
		"updated_at timestamptz NOT NULL",
		"attempt_count integer NOT NULL DEFAULT 0",
		"CHECK (attempt_count >= 0)",
		"last_attempt_at timestamptz",
		"last_blocked_reason text",
		"last_error_message text",
		"CREATE INDEX idx_api_key_record_cleanup_targets_attempt",
		"COALESCE(last_attempt_at, created_at)",
		"created_at",
		"api_key_id",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("migration Up section missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"REFERENCES ",
		"FOREIGN KEY",
		"CREATE TABLE IF NOT EXISTS",
	} {
		if strings.Contains(up, forbidden) {
			t.Fatalf("migration Up section should not contain %q", forbidden)
		}
	}
	normalizedUp := strings.Join(strings.Fields(up), " ")
	if !strings.Contains(
		normalizedUp,
		"COALESCE(last_attempt_at, created_at), created_at, api_key_id",
	) {
		t.Fatal("cleanup target attempt index columns must keep worker ordering")
	}
	if !strings.Contains(down, "-- no-op:") || strings.Contains(down, "DROP TABLE") {
		t.Fatal("migration Down section must remain a non-destructive no-op")
	}
}
