package migrationtests

import (
	"os"
	"strings"
	"testing"
)

func TestAccountHealthFailureWindowMigrationMatchesNodeStateMachine(t *testing.T) {
	source, err := os.ReadFile(migrationPath("000042_w1b_account_health_failure_window.sql"))
	if err != nil {
		t.Fatalf("read account health failure window migration: %v", err)
	}
	up, down, found := strings.Cut(string(source), "-- +goose Down")
	if !found {
		t.Fatal("migration is missing goose Down marker")
	}
	for _, required := range []string{
		"ALTER TABLE juhe_business.accounts",
		"ADD COLUMN IF NOT EXISTS health_check_failure_started_at timestamptz",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("migration Up section missing %q", required)
		}
	}
	if !strings.Contains(down, "-- no-op:") || strings.Contains(down, "DROP COLUMN") {
		t.Fatal("migration Down section must remain a non-destructive no-op")
	}
}
