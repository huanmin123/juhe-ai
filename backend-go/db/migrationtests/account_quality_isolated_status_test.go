package migrationtests

import (
	"os"
	"strings"
	"testing"
)

func TestAccountQualityIsolatedStatusMigrationAllowsNewStatusAndRestoresLegacyConstraint(t *testing.T) {
	const migrationName = "000082_w7_account_quality_isolated_status.sql"
	source, err := os.ReadFile(migrationPath(migrationName))
	if err != nil {
		t.Fatalf("read %s: %v", migrationName, err)
	}
	up, down, found := strings.Cut(string(source), "-- +goose Down")
	if !found {
		t.Fatal("migration is missing goose Down marker")
	}

	for _, required := range []string{
		"ALTER TABLE juhe_business.accounts",
		"DROP CONSTRAINT IF EXISTS accounts_status_check",
		"ADD CONSTRAINT accounts_status_check CHECK",
		"'active'",
		"'pending_test'",
		"'disabled'",
		"'error'",
		"'rate_limited'",
		"'temporary_unavailable'",
		"'quality_isolated'",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("migration Up section missing %q", required)
		}
	}

	upConstraint := up[strings.LastIndex(up, "ADD CONSTRAINT accounts_status_check"):]
	// A PostgreSQL CHECK constraint applies identically to INSERT and UPDATE.
	// Keeping the new value in the sole accounts status constraint therefore
	// verifies both admission paths without requiring a live database fixture.
	if !strings.Contains(upConstraint, "'quality_isolated'") {
		t.Fatal("quality_isolated must be permitted by the accounts status CHECK for INSERT and UPDATE")
	}

	for _, required := range []string{
		"UPDATE juhe_business.accounts",
		"SET status = 'disabled'",
		"WHERE status = 'quality_isolated'",
		"DROP CONSTRAINT IF EXISTS accounts_status_check",
		"ADD CONSTRAINT accounts_status_check CHECK",
		"'temporary_unavailable'",
	} {
		if !strings.Contains(down, required) {
			t.Fatalf("migration Down section missing %q", required)
		}
	}
	downConstraint := down[strings.LastIndex(down, "ADD CONSTRAINT accounts_status_check"):]
	if strings.Contains(downConstraint, "'quality_isolated'") {
		t.Fatal("migration Down section must not retain quality_isolated in the legacy status CHECK")
	}
}
