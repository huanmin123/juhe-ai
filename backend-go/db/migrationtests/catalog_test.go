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

func TestGPTCodexAutoReviewDefaultMigrationIsProviderScopedAndIdempotent(t *testing.T) {
	const migrationName = "000071_w2_gpt_codex_auto_review_default.sql"
	source, err := os.ReadFile(migrationPath(migrationName))
	if err != nil {
		t.Fatalf("read %s: %v", migrationName, err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")

	for _, want := range []string{
		"WHERE code = 'gpt'",
		`'["codex-auto-review"]'::jsonb`,
		"NOT (default_supported_models_json::jsonb @>",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("%s missing %q", migrationName, want)
		}
	}
	if strings.Contains(sql, "WHERE code = 'openai'") {
		t.Fatalf("%s must not change generic OpenAI-compatible defaults", migrationName)
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
		Name:    "000086_w7_model_quality_configuration_snapshots.sql",
	}
	if gotLatest := catalog.Entries[len(catalog.Entries)-1]; gotLatest != wantLatest {
		t.Fatalf("latest migration = %+v, want %+v", gotLatest, wantLatest)
	}
}

func TestModelQualityConfigurationSnapshotMigrationBackfillsBeforeConstraints(t *testing.T) {
	const migrationName = "000086_w7_model_quality_configuration_snapshots.sql"
	source, err := os.ReadFile(migrationPath(migrationName))
	if err != nil {
		t.Fatalf("read %s: %v", migrationName, err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")

	for _, want := range []string{
		"ADD COLUMN IF NOT EXISTS profile text",
		"ADD COLUMN IF NOT EXISTS penalty_threshold integer",
		"ADD COLUMN IF NOT EXISTS penalty_action text",
		"ADD COLUMN IF NOT EXISTS recovery_interval_minutes integer",
		"ADD COLUMN IF NOT EXISTS config_source text",
		"ADD COLUMN IF NOT EXISTS config_source_id text",
		"ADD COLUMN IF NOT EXISTS recovery_model text",
		"LEFT JOIN juhe_business.model_quality_policies AS policy",
		"juhe_business.migration_000086_try_jsonb(run.policy_snapshot_json)",
		"FROM juhe_dataset.model_check_runs AS run",
		"source.policy_snapshot ->> 'scheduleId'",
		"source.policy_snapshot ->> 'threshold'",
		"source.policy_snapshot ->> 'recoveryIntervalMinutes'",
		"source.resolved_config_source_id IS NOT NULL",
		"ALTER COLUMN config_source SET NOT NULL",
		"CHECK (config_source IN ('manual', 'schedule'))",
		"account_quality_enforcements_config_source_id_check",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("%s missing %q", migrationName, want)
		}
	}

	scheduleBackfill := strings.Index(sql, "UPDATE juhe_business.model_quality_schedules AS schedule")
	scheduleNotNull := strings.Index(sql, "ALTER COLUMN profile SET NOT NULL")
	enforcementBackfill := strings.Index(sql, "UPDATE juhe_business.account_quality_enforcements AS enforcement")
	enforcementNotNull := strings.Index(sql, "ALTER COLUMN config_source SET NOT NULL")
	if scheduleBackfill < 0 || scheduleNotNull <= scheduleBackfill {
		t.Fatalf("%s must backfill schedules before enforcing NOT NULL", migrationName)
	}
	if enforcementBackfill < 0 || enforcementNotNull <= enforcementBackfill {
		t.Fatalf("%s must backfill enforcements before enforcing NOT NULL", migrationName)
	}
}

func TestGatewayRouteDispatchGenerationMigrationFencesAllPreflightInputs(t *testing.T) {
	const migrationName = "000081_w10_gateway_route_dispatch_generation.sql"
	source, err := os.ReadFile(migrationPath(migrationName))
	if err != nil {
		t.Fatalf("read %s: %v", migrationName, err)
	}
	sql := strings.ToLower(string(source))
	for _, want := range []string{
		"create table if not exists juhe_business.gateway_route_dispatch_generations",
		"generation bigint not null check (generation > 0)",
		"create or replace function juhe_business.bump_gateway_route_dispatch_generation()",
		"juhe_business.route_strategies",
		"juhe_business.route_strategy_groups",
		"juhe_business.groups",
		"juhe_business.resource_authorizations",
		"juhe_business.group_authorization_settings",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("%s missing %q", migrationName, want)
		}
	}
}
