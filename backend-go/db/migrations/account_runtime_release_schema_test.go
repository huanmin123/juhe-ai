package migrations

import (
	"os"
	"strings"
	"testing"
)

func TestAccountRuntimeReleaseMigrationsCoverExistingDatabases(t *testing.T) {
	continuousProbeSource, err := os.ReadFile("000057_w1b_account_temporary_unavailable_continuous_probe.sql")
	if err != nil {
		t.Fatalf("read continuous probe migration: %v", err)
	}
	continuousProbeUp, continuousProbeDown, found := strings.Cut(string(continuousProbeSource), "-- +goose Down")
	if !found {
		t.Fatal("continuous probe migration is missing goose Down marker")
	}
	for _, required := range []string{
		"-- +goose Up",
		"ALTER TABLE juhe_business.accounts",
		"ADD COLUMN IF NOT EXISTS temporary_unavailable_continuous_probe_enabled integer",
		"ALTER COLUMN temporary_unavailable_continuous_probe_enabled SET DEFAULT 1",
		"UPDATE juhe_business.accounts",
		"WHERE temporary_unavailable_continuous_probe_enabled IS NULL",
		"ADD CONSTRAINT accounts_temporary_unavailable_continuous_probe_enabled_check",
		"NOT VALID",
		"VALIDATE CONSTRAINT accounts_temporary_unavailable_continuous_probe_enabled_check",
		"ALTER COLUMN temporary_unavailable_continuous_probe_enabled SET NOT NULL",
		"CHECK (temporary_unavailable_continuous_probe_enabled IN (0, 1))",
	} {
		if !strings.Contains(continuousProbeUp, required) {
			t.Fatalf("continuous probe migration Up section missing %q", required)
		}
	}
	if !strings.Contains(continuousProbeDown, "-- no-op:") || strings.Contains(continuousProbeDown, "DROP COLUMN") {
		t.Fatal("continuous probe migration Down section must remain a non-destructive no-op")
	}

	dirtyDomainSource, err := os.ReadFile("000056_w7_page_data_dirty_domains.sql")
	if err != nil {
		t.Fatalf("read page dirty domain migration: %v", err)
	}
	dirtyDomainUp, dirtyDomainDown, found := strings.Cut(string(dirtyDomainSource), "-- +goose Down")
	if !found {
		t.Fatal("page dirty domain migration is missing goose Down marker")
	}
	for _, required := range []string{
		"-- +goose Up",
		"CREATE TABLE IF NOT EXISTS juhe_business.page_data_dirty_domains",
		"domain TEXT PRIMARY KEY",
		"generation BIGINT NOT NULL",
		"is_dirty BOOLEAN NOT NULL DEFAULT TRUE",
	} {
		if !strings.Contains(dirtyDomainUp, required) {
			t.Fatalf("page dirty domain migration Up section missing %q", required)
		}
	}
	if !strings.Contains(dirtyDomainDown, "-- no-op:") || strings.Contains(dirtyDomainDown, "DROP TABLE") {
		t.Fatal("page dirty domain migration Down section must remain a non-destructive no-op")
	}
}

func TestExistingGooseDoBlockMigrationsUseStatementMarkers(t *testing.T) {
	for _, name := range []string{
		"000005_w1b_public_accounts.sql",
		"000013_w2_management_account_authorization_instances.sql",
	} {
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		text := string(source)
		if strings.Count(text, "-- +goose StatementBegin") != strings.Count(text, "-- +goose StatementEnd") {
			t.Fatalf("%s has unbalanced Goose statement markers", name)
		}
		if !strings.Contains(text, "-- +goose StatementBegin\nDO $$") || !strings.Contains(text, "END $$;\n-- +goose StatementEnd") {
			t.Fatalf("%s must wrap its DO block for Goose parsing", name)
		}
	}
}
