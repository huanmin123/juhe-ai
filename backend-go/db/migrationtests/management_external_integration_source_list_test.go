package migrationtests

import (
	"os"
	"strings"
	"testing"
)

func TestManagementExternalIntegrationSourceListMigrationAddsReversiblePrefixIndex(t *testing.T) {
	source, err := os.ReadFile(migrationPath("000049_w2_management_external_integration_source_list.sql"))
	if err != nil {
		t.Fatalf("read management external integration source list migration: %v", err)
	}
	up, down, found := strings.Cut(string(source), "-- +goose Down")
	if !found {
		t.Fatal("migration is missing goose Down marker")
	}
	const indexName = "idx_external_integration_sources_name_lower_c_prefix"
	for _, required := range []string{
		"CREATE INDEX IF NOT EXISTS " + indexName,
		"ON juhe_business.external_integration_sources ((lower(name) COLLATE \"C\"))",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("migration Up section missing %q", required)
		}
	}
	if !strings.Contains(down, "DROP INDEX IF EXISTS juhe_business."+indexName) {
		t.Fatal("migration Down section must drop the prefix index created by Up")
	}
	if strings.Contains(down, "-- no-op:") {
		t.Fatal("migration Down section must be a symmetric index drop")
	}
}
