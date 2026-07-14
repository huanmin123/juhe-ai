package migrations_test

import (
	"os"
	"strings"
	"testing"
)

func TestUnifiedProviderModelPricingMigrationUsesJSONAndDropsLegacyFields(t *testing.T) {
	source, err := os.ReadFile("000046_w2_unify_provider_model_pricing.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(source)
	for _, required := range []string{"service_tier_prices_json", "jsonb_strip_nulls", "'priority'", "'flex'", "DROP COLUMN pricing_model"} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
	for _, legacy := range []string{"priority_input_usd_per_1m", "flex_input_usd_per_1m"} {
		if !strings.Contains(sql, "DROP COLUMN "+legacy) {
			t.Fatalf("migration must drop %s", legacy)
		}
	}
}
