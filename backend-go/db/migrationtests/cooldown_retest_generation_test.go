package migrationtests

import (
	"os"
	"strings"
	"testing"
)

func TestCooldownRetestGenerationMigrationAddsNullableValidatedStateWithoutBackfill(t *testing.T) {
	const migrationName = "000090_w7_cooldown_retest_generation.sql"
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
		"ADD COLUMN IF NOT EXISTS cooldown_retest_generation text",
		"DROP CONSTRAINT IF EXISTS accounts_cooldown_retest_generation_check",
		"ADD CONSTRAINT accounts_cooldown_retest_generation_check",
		"cooldown_retest_generation IS NULL",
		"btrim(cooldown_retest_generation) <> ''",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("migration Up section missing %q", required)
		}
	}

	upSQL := strings.ToLower(stripMigrationSQLLineComments(up))
	for _, forbidden := range []string{
		"cooldown_retest_generation text not null",
		"cooldown_retest_generation text default",
		"update juhe_business.accounts",
	} {
		if strings.Contains(upSQL, forbidden) {
			t.Fatalf("migration must preserve NULL legacy state without invented generations, found %q", forbidden)
		}
	}

	downSQL := strings.ToLower(stripMigrationSQLLineComments(down))
	if !strings.Contains(downSQL, "select 1;") {
		t.Fatal("migration Down section must be an executable no-op")
	}
	for _, destructive := range []string{"drop column", "drop constraint", "delete from", "update "} {
		if strings.Contains(downSQL, destructive) {
			t.Fatalf("migration Down section must preserve shared cooldown state, found %q", destructive)
		}
	}
}
