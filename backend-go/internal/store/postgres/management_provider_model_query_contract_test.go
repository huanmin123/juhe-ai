package postgres

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestManagementProviderModelQueryReturnsBuiltInIDAndUsesPresenceAwarePricePatch(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_provider_models.sql")
	if err != nil {
		t.Fatalf("read query source: %v", err)
	}
	sql := string(source)
	if regexp.MustCompile(`(?s)-- name: ListManagementProviderModelCatalog :many\s+SELECT\s+''::text AS id`).MatchString(sql) {
		t.Fatal("built-in provider model query must return the persisted id")
	}
	if !regexp.MustCompile(`input_usd_per_1m\s*=\s*CASE WHEN sqlc\.arg\(input_usd_per_1m_present\)`).MatchString(sql) {
		t.Fatal("built-in provider model price update must distinguish omitted from explicit null")
	}
	updateStart := strings.Index(sql, "-- name: UpdateManagementBuiltInProviderModelPrices :one")
	if updateStart < 0 {
		t.Fatal("built-in provider model update query start missing")
	}
	updateEnd := strings.Index(sql[updateStart:], "-- name: FindManagementCustomProviderModel :one")
	if updateEnd < 0 {
		t.Fatal("built-in provider model update query end missing")
	}
	updateSQL := sql[updateStart : updateStart+updateEnd]
	if !regexp.MustCompile(`(?s)WITH\s+locked\s+AS\s+MATERIALIZED\s*\(\s*SELECT\s+catalog\.\*\s+FROM\s+juhe_business\.provider_model_catalog\s+AS\s+catalog.*?FOR\s+UPDATE\s*\),\s*updated\s+AS\s*\(\s*UPDATE\s+juhe_business\.provider_model_catalog\s+AS\s+target`).MatchString(updateSQL) {
		t.Fatal("built-in provider model update must lock the old row in the same statement")
	}
	if !regexp.MustCompile(`(?s)FROM\s+locked\s+WHERE\s+target\.id\s*=\s*locked\.id\s+AND\s+target\.provider_code\s*=\s*locked\.provider_code\s+RETURNING\s+target\.\*`).MatchString(updateSQL) {
		t.Fatal("built-in provider model update must update the locked row and return its committed snapshot")
	}
	for _, column := range []string{"status", "mode", "input_usd_per_1m", "audio_output_usd_per_1m"} {
		if !regexp.MustCompile(`ELSE\s+target\.` + column + `\s+END`).MatchString(updateSQL) {
			t.Fatalf("built-in provider model update must qualify the old %s value", column)
		}
	}
	configurationColumns := []string{
		"id", "provider_code", "status", "mode", "supported_api_protocols_json", "supported_service_tiers_json",
		"supported_reasoning_efforts_json", "default_reasoning_effort", "release_date", "shutdown_date",
		"context_window_tokens", "max_input_tokens", "max_output_tokens", "input_usd_per_1m", "output_usd_per_1m",
		"cached_input_usd_per_1m", "cache_write_usd_per_1m", "cache_write_1h_usd_per_1m", "service_tier_prices_json",
		"image_input_usd_per_1m", "image_output_usd_per_1m", "audio_input_usd_per_1m", "audio_output_usd_per_1m",
		"output_usd_per_image", "updated_at",
	}
	for _, column := range configurationColumns {
		for _, snapshot := range []string{"before", "after"} {
			alias := snapshot + "_" + column
			if !regexp.MustCompile(`(?m)\s+` + snapshotSource(snapshot) + `\.` + column + `\s+AS\s+` + alias + `\s*,?`).MatchString(updateSQL) {
				t.Fatalf("built-in provider model update must return %s", alias)
			}
		}
	}
	if !regexp.MustCompile(`(?s)FROM\s+locked\s+INNER\s+JOIN\s+updated\s+ON\s+true\s*;`).MatchString(updateSQL) {
		t.Fatal("built-in provider model update must pair the locked before row with the updated after row")
	}
}

func snapshotSource(snapshot string) string {
	if snapshot == "before" {
		return "locked"
	}
	return "updated"
}
