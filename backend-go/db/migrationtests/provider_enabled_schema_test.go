package migrationtests

import (
	"os"
	"strings"
	"testing"
)

func TestProviderEnabledColumnsRemainPostgresBooleans(t *testing.T) {
	for _, contract := range []struct {
		migration string
		table     string
	}{
		{"000004_w1b_public_groups.sql", "juhe_business.providers"},
		{"000005_w1b_public_accounts.sql", "juhe_business.provider_protocol_profiles"},
		{"000008_w2_management_provider_options.sql", "juhe_business.protocol_endpoint_families"},
		{"000008_w2_management_provider_options.sql", "juhe_business.provider_protocol_profile_families"},
	} {
		source, err := os.ReadFile(migrationPath(contract.migration))
		if err != nil {
			t.Fatalf("read %s: %v", contract.migration, err)
		}
		up, _, found := strings.Cut(string(source), "-- +goose Down")
		if !found {
			t.Fatalf("%s is missing goose Down marker", contract.migration)
		}
		start := strings.Index(up, "CREATE TABLE IF NOT EXISTS "+contract.table)
		if start < 0 {
			t.Fatalf("%s is missing %s", contract.migration, contract.table)
		}
		tableSQL := up[start:]
		if end := strings.Index(tableSQL, ");"); end >= 0 {
			tableSQL = tableSQL[:end]
		}
		if !strings.Contains(tableSQL, "enabled boolean NOT NULL DEFAULT true") {
			t.Fatalf("%s.enabled must remain PostgreSQL boolean", contract.table)
		}
	}
}
