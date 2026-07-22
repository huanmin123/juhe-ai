package migrationtests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/migrationcatalog"
)

func migrationPath(name string) string {
	return filepath.Join("..", "migrations", name)
}

func TestDeepSeekProviderOptionsMigrationMatchesCurrentContract(t *testing.T) {
	const migrationName = "000055_w2_sync_deepseek_provider_options.sql"
	source, err := os.ReadFile(migrationPath(migrationName))
	if err != nil {
		t.Fatalf("read %s: %v", migrationName, err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")

	for _, want := range []string{
		`'["deepseek-v4-flash","deepseek-v4-pro"]'`,
		`WHERE code = 'deepseek'`,
		`'profile_deepseek_openai_v1'`,
		`'https://api.deepseek.com'`,
		`'["chat","passthrough"]'`,
		`'profile_deepseek_anthropic_v1'`,
		`'https://api.deepseek.com/anthropic'`,
		`'["messages","models","passthrough"]'`,
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("%s missing %q", migrationName, want)
		}
	}
	for _, legacy := range []string{"deepseek-ai-v4-flash", "deepseek-ai-v4-pro"} {
		if strings.Contains(sql, legacy) {
			t.Fatalf("%s retains removed model %q", migrationName, legacy)
		}
	}

}

func TestProviderAuthProtocolCatchUpMigrationUpgradesVersion59Databases(t *testing.T) {
	const migrationName = "000060_w2_provider_auth_protocol_schema_20260718.sql"
	source, err := os.ReadFile(migrationPath(migrationName))
	if err != nil {
		t.Fatalf("read %s: %v", migrationName, err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")

	for _, want := range []string{
		"ADD COLUMN IF NOT EXISTS long_context_input_token_threshold_inclusive boolean NOT NULL DEFAULT false",
		"DROP CONSTRAINT IF EXISTS accounts_type_check",
		"ADD CONSTRAINT accounts_type_check CHECK (type IN ('api_key', 'oauth', 'google_oauth'))",
		"DROP CONSTRAINT IF EXISTS accounts_health_check_endpoint_mode_check",
		"'interactions_json', 'interactions_sse'",
		"'xai', 'xai', 'xAI / Grok', 'openai'",
		"'profile_xai_openai_v1'",
		"'profile_gemini_native_v1beta'",
		"'[\"api_key\",\"google_oauth\"]'",
		"'gemini_v1beta_interactions'",
		"('profile_gemini_native_v1beta', 'interactions'",
		"'grp_default_xai_sys_admin'",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("%s missing %q", migrationName, want)
		}
	}

	providerPosition := strings.Index(sql, "'xai', 'xai', 'xAI / Grok', 'openai'")
	profilePosition := strings.Index(sql, "'profile_xai_openai_v1'")
	groupPosition := strings.Index(sql, "'grp_default_xai_sys_admin'")
	if providerPosition < 0 || profilePosition <= providerPosition || groupPosition <= providerPosition {
		t.Fatalf("%s must seed xAI provider before its profile and default group", migrationName)
	}
}

func TestMigrationCatalogContainsOnlyUniqueContiguousVersionedSQLFiles(t *testing.T) {
	catalog, err := migrationcatalog.Inspect(os.DirFS(migrationPath(".")))
	if err != nil {
		t.Fatalf("inspect migration catalog: %v", err)
	}
	if len(catalog.Entries) == 0 {
		t.Fatal("migration catalog must not be empty")
	}
	for index, entry := range catalog.Entries {
		wantVersion := int64(index + 1)
		if entry.Version != wantVersion {
			t.Fatalf("migration catalog entry %d has version %d, want contiguous version %d", index, entry.Version, wantVersion)
		}
	}

	wantLatest := migrationcatalog.Entry{
		Version: migrationcatalog.CurrentSchemaVersion,
		Name:    "000071_w7_drop_page_data_dirty_domains.sql",
	}
	if gotLatest := catalog.Entries[len(catalog.Entries)-1]; gotLatest != wantLatest {
		t.Fatalf("latest migration = %+v, want %+v", gotLatest, wantLatest)
	}
}
