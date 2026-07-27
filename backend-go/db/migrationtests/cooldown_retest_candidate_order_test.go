package migrationtests

import (
	"os"
	"strings"
	"testing"
)

func TestCooldownRetestCandidateOrderMigrationMatchesGlobalKeysetScan(t *testing.T) {
	const migrationName = "000091_w7_cooldown_retest_candidate_order.sql"
	source, err := os.ReadFile(migrationPath(migrationName))
	if err != nil {
		t.Fatalf("read %s: %v", migrationName, err)
	}
	up, down, found := strings.Cut(string(source), "-- +goose Down")
	if !found {
		t.Fatal("migration is missing goose Down marker")
	}

	for _, required := range []string{
		"DROP CONSTRAINT IF EXISTS accounts_cooldown_retest_generation_check",
		"ADD CONSTRAINT accounts_cooldown_retest_generation_check",
		"CHR(160)",
		"CHR(65279)",
		"cooldown_retest_generation = btrim(cooldown_retest_generation",
		"CREATE INDEX IF NOT EXISTS idx_accounts_cooldown_retest_candidate_order",
		"ON juhe_business.accounts (\n    cooldown_until ASC,\n    priority ASC,\n    created_at ASC,\n    id ASC,\n    health_check_endpoint_mode\n  )",
		"WHERE deleted_at IS NULL",
		"AND cooldown_until IS NOT NULL",
		"AND schedulable = true",
		"AND type IN ('api_key', 'oauth', 'google_oauth')",
		"AND status IN ('temporary_unavailable', 'rate_limited')",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("migration Up section missing %q", required)
		}
	}
	if strings.Contains(up, "CHR(133)") {
		t.Fatal("migration must preserve U+0085 NEL; ECMAScript trim does not classify it as whitespace")
	}

	downSQL := strings.ToLower(stripMigrationSQLLineComments(down))
	if !strings.Contains(downSQL, "select 1;") {
		t.Fatal("migration Down section must be an executable no-op")
	}
	for _, destructive := range []string{"drop index", "delete from", "update "} {
		if strings.Contains(downSQL, destructive) {
			t.Fatalf("migration Down section must preserve the shared candidate index, found %q", destructive)
		}
	}
}
