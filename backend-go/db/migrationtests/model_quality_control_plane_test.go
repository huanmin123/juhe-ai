package migrationtests

import (
	"os"
	"strings"
	"testing"
)

func TestModelQualityControlPlaneMigrationPreservesNodePostgresCoexistence(t *testing.T) {
	const migrationName = "000083_w7_model_quality_control_plane.sql"
	source, err := os.ReadFile(migrationPath(migrationName))
	if err != nil {
		t.Fatalf("read %s: %v", migrationName, err)
	}
	up, down, found := strings.Cut(string(source), "-- +goose Down")
	if !found {
		t.Fatal("migration is missing goose Down marker")
	}

	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS juhe_business.model_quality_policies",
		"CREATE TABLE IF NOT EXISTS juhe_business.model_quality_schedules",
		"CREATE TABLE IF NOT EXISTS juhe_business.account_quality_enforcements",
		"CREATE TABLE IF NOT EXISTS juhe_stats.account_quality_health_hourly",
		"revision integer NOT NULL DEFAULT 1 CHECK (revision >= 1)",
		"manual_enforcement_enabled integer NOT NULL DEFAULT 1",
		"CHECK (manual_enforcement_enabled IN (0, 1))",
		"created_at text NOT NULL",
		"next_run_at text NOT NULL",
		"stat_hour text NOT NULL",
		"lease_token text",
		"recovery_lease_token text",
		"ADD COLUMN IF NOT EXISTS lease_token text",
		"ADD COLUMN IF NOT EXISTS recovery_lease_token text",
		"FOREIGN KEY (account_id, system_account_id)",
		"REFERENCES juhe_business.accounts(id, system_account_id) ON DELETE CASCADE",
		"CHECK (penalty_threshold BETWEEN 40 AND 100)",
		"CHECK (interval_minutes BETWEEN 10 AND 10080)",
		"CHECK (recovery_interval_minutes BETWEEN 10 AND 10080)",
		"CHECK (generation >= 1)",
		"CHECK (account_config_revision >= 1)",
		"PRIMARY KEY (account_id, stat_hour)",
		"CREATE INDEX IF NOT EXISTS idx_model_quality_schedules_due",
		"(enabled, next_run_at, id)",
		"CREATE INDEX IF NOT EXISTS idx_account_quality_enforcements_recovery",
		"CREATE INDEX IF NOT EXISTS idx_account_quality_health_hourly_scope",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("migration Up section missing %q", required)
		}
	}

	for _, forbidden := range []string{
		"timestamptz",
		"boolean NOT NULL DEFAULT",
		"DEFAULT now()",
		"jsonb",
		"json_valid(",
		"CREATE TABLE IF NOT EXISTS model_quality_",
	} {
		if strings.Contains(up, forbidden) {
			t.Fatalf("migration must not copy the Node SQLite schema fragment %q", forbidden)
		}
	}

	for _, forbidden := range []string{
		"(lease_owner IS NULL AND lease_token IS NULL AND lease_until IS NULL)",
		"btrim(lease_owner) <> '' AND btrim(lease_token) <> '' AND lease_until IS NOT NULL",
		"(recovery_lease_owner IS NULL AND recovery_lease_token IS NULL AND recovery_lease_until IS NULL)",
		"btrim(recovery_lease_owner) <> '' AND btrim(recovery_lease_token) <> '' AND recovery_lease_until IS NOT NULL",
	} {
		if strings.Contains(up, forbidden) {
			t.Fatalf("migration must not add a token lease CHECK while Node coexistence writes legacy leases: %q", forbidden)
		}
	}
	for _, required := range []string{
		"Node can initialize a fresh database before Go",
		"neither fresh nor existing tables",
		"non-empty token in every claim/complete CAS",
		"later Node-removal migration",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("migration must document the coexistence token-CAS contract %q", required)
		}
	}

	if !strings.Contains(down, "forward-only shared-schema migration") ||
		!strings.Contains(down, "SELECT 1;") || strings.Contains(down, "DROP TABLE") {
		t.Fatal("migration Down section must be an executable non-destructive shared-schema safety fence")
	}
}
