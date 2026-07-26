package migrationtests

import (
	"os"
	"strings"
	"testing"
)

func TestUserRequestLimitsMigrationAddsReversibleJSONObjectColumn(t *testing.T) {
	const migrationName = "000084_w2_user_request_limits.sql"
	source, err := os.ReadFile(migrationPath(migrationName))
	if err != nil {
		t.Fatalf("read %s: %v", migrationName, err)
	}
	up, down, found := strings.Cut(string(source), "-- +goose Down")
	if !found {
		t.Fatal("migration is missing goose Down marker")
	}

	for _, required := range []string{
		"ALTER TABLE juhe_business.system_accounts",
		"ADD COLUMN IF NOT EXISTS request_limits_json text",
		"DROP CONSTRAINT IF EXISTS system_accounts_request_limits_json_object_check",
		"ADD CONSTRAINT system_accounts_request_limits_json_object_check",
		"request_limits_json IS NULL",
		"jsonb_typeof(request_limits_json::jsonb) = 'object'",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("migration Up section missing %q", required)
		}
	}

	for _, required := range []string{
		"DROP CONSTRAINT IF EXISTS system_accounts_request_limits_json_object_check",
		"DROP COLUMN IF EXISTS request_limits_json",
	} {
		if !strings.Contains(down, required) {
			t.Fatalf("migration Down section missing %q", required)
		}
	}
}
