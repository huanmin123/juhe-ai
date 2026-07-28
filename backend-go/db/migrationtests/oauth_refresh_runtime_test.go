package migrationtests

import (
	"os"
	"strings"
	"testing"
)

func TestOAuthRefreshRuntimeMigrationMatchesNodeAuthority(t *testing.T) {
	const migrationName = "000092_w7_oauth_refresh_runtime.sql"
	source, err := os.ReadFile(migrationPath(migrationName))
	if err != nil {
		t.Fatalf("read %s: %v", migrationName, err)
	}
	up, down, found := strings.Cut(string(source), "-- +goose Down")
	if !found {
		t.Fatal("migration is missing goose Down marker")
	}

	for _, required := range []string{
		"ADD COLUMN IF NOT EXISTS oauth_access_token_expires_at text",
		"ADD COLUMN IF NOT EXISTS oauth_refresh_token_present integer NOT NULL DEFAULT 0",
		"CREATE INDEX IF NOT EXISTS idx_accounts_openai_oauth_refresh_due",
		"provider_code",
		"status",
		"CREATE INDEX IF NOT EXISTS idx_accounts_openai_oauth_refresh_pg_due",
		"provider_protocol_profile_id",
		"(oauth_access_token_expires_at IS NOT NULL)",
		"oauth_access_token_expires_at ASC",
		"updated_at ASC",
		"id ASC",
		"WHERE authorization_instance_authorization_id IS NULL",
		"AND deleted_at IS NULL",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("migration Up section missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"accounts_oauth_refresh_token_present_check",
		"timestamptz",
		"oauth_refresh_token_present boolean",
	} {
		if strings.Contains(strings.ToLower(up), forbidden) {
			t.Fatalf("migration Up section must preserve Node storage semantics, found %q", forbidden)
		}
	}

	downSQL := strings.ToLower(stripMigrationSQLLineComments(down))
	if !strings.Contains(downSQL, "select 1;") {
		t.Fatal("migration Down section must be an executable no-op")
	}
	for _, destructive := range []string{"drop index", "drop column", "delete from", "update "} {
		if strings.Contains(downSQL, destructive) {
			t.Fatalf("migration Down section must preserve shared OAuth state, found %q", destructive)
		}
	}
}
