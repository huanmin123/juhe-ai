package migrationtests

import (
	"os"
	"strings"
	"testing"
)

func TestUnifiedProviderModelPricingMigrationUsesJSONAndDropsLegacyFields(t *testing.T) {
	bootstrapSource, err := os.ReadFile(migrationPath("000046_w2_unify_provider_model_pricing.sql"))
	if err != nil {
		t.Fatal(err)
	}
	bootstrapSQL := string(bootstrapSource)
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS juhe_business.provider_model_catalog",
		"service_tier_prices_json", "jsonb_strip_nulls", "'priority'", "'flex'",
	} {
		if !strings.Contains(bootstrapSQL, required) {
			t.Fatalf("bootstrap migration missing %q", required)
		}
	}

	materializeSource, err := os.ReadFile(migrationPath("000048_w2_materialize_provider_model_pricing_aliases.sql"))
	if err != nil {
		t.Fatal(err)
	}
	materializeSQL := string(materializeSource)
	for _, required := range []string{
		"UPDATE juhe_business.provider_model_catalog AS alias",
		"WITH candidates AS", "SELECT DISTINCT ON (alias_id)",
		"RAISE EXCEPTION 'provider_model_catalog contains unresolved pricing_model aliases'",
		"RAISE EXCEPTION 'custom_provider_models contains unresolved pricing_model aliases'",
		"-- +goose StatementBegin", "-- +goose StatementEnd",
		"DROP COLUMN IF EXISTS pricing_model",
	} {
		if !strings.Contains(materializeSQL, required) {
			t.Fatalf("materialization migration missing %q", required)
		}
	}
	for _, legacy := range []string{"priority_input_usd_per_1m", "flex_input_usd_per_1m"} {
		if !strings.Contains(materializeSQL, "DROP COLUMN IF EXISTS "+legacy) {
			t.Fatalf("migration must drop %s", legacy)
		}
	}
}

func TestUnifiedProviderModelCatalogSeedMatchesCurrentSchema(t *testing.T) {
	source, err := os.ReadFile(migrationPath("000047_w2_sync_provider_model_catalog_unified_pricing.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sql := string(source)
	for _, required := range []string{"service_tier_prices_json", "ON CONFLICT (provider_code, model) DO UPDATE SET"} {
		if !strings.Contains(sql, required) {
			t.Fatalf("catalog seed missing %q", required)
		}
	}
	for _, forbidden := range []string{"pricing_model", "priority_input_usd_per_1m", "input_usd_per_1m = EXCLUDED.input_usd_per_1m"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("catalog seed must not contain %q", forbidden)
		}
	}
}
