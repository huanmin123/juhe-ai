package postgres

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const providerModelCatalogSnapshotMigration = "../../../db/migrations/000035_w2_sync_provider_model_catalog_gpt_tiers.sql"

func TestProviderModelCatalogSnapshotMigrationCountsAndRepresentativeModels(t *testing.T) {
	source, err := os.ReadFile(providerModelCatalogSnapshotMigration)
	if err != nil {
		t.Fatalf("read provider model catalog snapshot migration: %v", err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")

	const header = "-- Provider row counts total/visible: gpt=81/81, anthropic=42/24, gemini=10/10, deepseek=6/6, glm=18/17"
	if !strings.Contains(sql, header) {
		t.Fatalf("provider model catalog snapshot migration missing header %q", header)
	}

	rowPattern := regexp.MustCompile(`(?m)^    'provider_model_[^']+', '(gpt|anthropic|gemini|deepseek|glm)', '([^']+)'`)
	matches := rowPattern.FindAllStringSubmatch(sql, -1)
	if len(matches) != 157 {
		t.Fatalf("provider model catalog snapshot row count = %d, want 157", len(matches))
	}

	wantCounts := map[string]int{
		"gpt":       81,
		"anthropic": 42,
		"gemini":    10,
		"deepseek":  6,
		"glm":       18,
	}
	actualCounts := make(map[string]int, len(wantCounts))
	models := make(map[string]map[string]bool, len(wantCounts))
	for _, match := range matches {
		providerCode := match[1]
		model := match[2]
		actualCounts[providerCode]++
		if models[providerCode] == nil {
			models[providerCode] = make(map[string]bool)
		}
		models[providerCode][model] = true
	}
	for providerCode, wantCount := range wantCounts {
		if actualCounts[providerCode] != wantCount {
			t.Fatalf("%s snapshot row count = %d, want %d", providerCode, actualCounts[providerCode], wantCount)
		}
	}

	representativeModels := map[string]string{
		"gpt":       "gpt-5.6-sol",
		"anthropic": "claude-opus-4-6",
		"gemini":    "gemini-3.5-flash",
		"deepseek":  "deepseek-v4-flash",
		"glm":       "glm-5.2",
	}
	for providerCode, model := range representativeModels {
		if !models[providerCode][model] {
			t.Fatalf("%s representative model %q missing from provider model catalog snapshot", providerCode, model)
		}
	}

	gptTierPattern := regexp.MustCompile(`(?m)^    'provider_model_[^']+', 'gpt', '([^']+)'[^\n]*\n    '\[[^']*\]', '(\[[^']*\])',`)
	gptTierMatches := gptTierPattern.FindAllStringSubmatch(sql, -1)
	gptServiceTiers := make(map[string]string, len(gptTierMatches))
	for _, match := range gptTierMatches {
		gptServiceTiers[match[1]] = match[2]
	}
	flexModels := []string{
		"gpt-5.6-sol",
		"gpt-5.6-terra",
		"gpt-5.6-luna",
		"gpt-5.5",
		"gpt-5.4",
		"gpt-5",
		"gpt-5-mini",
		"gpt-5-nano",
		"o3",
		"o4-mini",
	}
	for _, model := range flexModels {
		const wantTiers = `["priority","flex"]`
		if actualTiers := gptServiceTiers[model]; actualTiers != wantTiers {
			t.Fatalf("gpt model %q supported_service_tiers_json = %q, want %q", model, actualTiers, wantTiers)
		}
	}
}

func TestProviderModelCatalogSnapshotMigrationDisablesBeforeUpsert(t *testing.T) {
	source, err := os.ReadFile(providerModelCatalogSnapshotMigration)
	if err != nil {
		t.Fatalf("read provider model catalog snapshot migration: %v", err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")

	const disableSQL = `UPDATE juhe_business.provider_model_catalog
SET status = 'disabled',
    catalog_visible = false,
    updated_at = now()
WHERE provider_code IN ('gpt', 'anthropic', 'gemini', 'deepseek', 'glm');`
	disableIndex := strings.Index(sql, disableSQL)
	insertIndex := strings.Index(sql, "INSERT INTO juhe_business.provider_model_catalog")
	upsertIndex := strings.Index(sql, "ON CONFLICT (provider_code, model) DO UPDATE SET")
	if disableIndex < 0 {
		t.Fatal("provider model catalog snapshot migration missing disable-before-upsert statement")
	}
	if insertIndex < 0 || upsertIndex < 0 {
		t.Fatal("provider model catalog snapshot migration missing insert or upsert statement")
	}
	if !(disableIndex < insertIndex && insertIndex < upsertIndex) {
		t.Fatalf("provider model catalog snapshot statement order = disable:%d insert:%d upsert:%d", disableIndex, insertIndex, upsertIndex)
	}
}

func TestProviderModelCatalogSnapshotMigrationUpdatesAllCatalogFieldsOnConflict(t *testing.T) {
	source, err := os.ReadFile(providerModelCatalogSnapshotMigration)
	if err != nil {
		t.Fatalf("read provider model catalog snapshot migration: %v", err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")

	const marker = "ON CONFLICT (provider_code, model) DO UPDATE SET"
	conflictIndex := strings.Index(sql, marker)
	if conflictIndex < 0 {
		t.Fatalf("provider model catalog snapshot migration missing %q", marker)
	}
	conflictSQL := sql[conflictIndex:]
	if downIndex := strings.Index(conflictSQL, "-- +goose Down"); downIndex >= 0 {
		conflictSQL = conflictSQL[:downIndex]
	}

	wantAssignments := map[string]string{
		"status":                                "'active'",
		"mode":                                  "EXCLUDED.mode",
		"catalog_order":                         "EXCLUDED.catalog_order",
		"release_date":                          "EXCLUDED.release_date",
		"shutdown_date":                         "EXCLUDED.shutdown_date",
		"supported_api_protocols_json":          "EXCLUDED.supported_api_protocols_json",
		"supported_service_tiers_json":          "EXCLUDED.supported_service_tiers_json",
		"supported_reasoning_efforts_json":      "EXCLUDED.supported_reasoning_efforts_json",
		"default_reasoning_effort":              "EXCLUDED.default_reasoning_effort",
		"codex_supported_reasoning_levels_json": "EXCLUDED.codex_supported_reasoning_levels_json",
		"codex_default_reasoning_level":         "EXCLUDED.codex_default_reasoning_level",
		"codex_multi_agent_version":             "EXCLUDED.codex_multi_agent_version",
		"pricing_model":                         "EXCLUDED.pricing_model",
		"context_window_tokens":                 "EXCLUDED.context_window_tokens",
		"input_usd_per_1m":                      "EXCLUDED.input_usd_per_1m",
		"output_usd_per_1m":                     "EXCLUDED.output_usd_per_1m",
		"cached_input_usd_per_1m":               "EXCLUDED.cached_input_usd_per_1m",
		"cache_write_usd_per_1m":                "EXCLUDED.cache_write_usd_per_1m",
		"cache_write_1h_usd_per_1m":             "EXCLUDED.cache_write_1h_usd_per_1m",
		"image_input_usd_per_1m":                "EXCLUDED.image_input_usd_per_1m",
		"image_output_usd_per_1m":               "EXCLUDED.image_output_usd_per_1m",
		"audio_input_usd_per_1m":                "EXCLUDED.audio_input_usd_per_1m",
		"audio_output_usd_per_1m":               "EXCLUDED.audio_output_usd_per_1m",
		"output_usd_per_image":                  "EXCLUDED.output_usd_per_image",
		"max_input_tokens":                      "EXCLUDED.max_input_tokens",
		"max_output_tokens":                     "EXCLUDED.max_output_tokens",
		"max_tokens":                            "EXCLUDED.max_tokens",
		"supports_prompt_caching":               "EXCLUDED.supports_prompt_caching",
		"catalog_visible":                       "EXCLUDED.catalog_visible",
		"source":                                "EXCLUDED.source",
		"updated_at":                            "EXCLUDED.updated_at",
	}

	assignmentPattern := regexp.MustCompile(`(?m)^  ([a-z0-9_]+) = ([^,;\n]+)[,;]$`)
	matches := assignmentPattern.FindAllStringSubmatch(conflictSQL, -1)
	actualAssignments := make(map[string]string, len(matches))
	for _, match := range matches {
		actualAssignments[match[1]] = match[2]
	}
	if len(actualAssignments) != len(wantAssignments) {
		t.Fatalf("provider model catalog conflict assignment count = %d, want %d: %v", len(actualAssignments), len(wantAssignments), actualAssignments)
	}
	for field, wantValue := range wantAssignments {
		if actualAssignments[field] != wantValue {
			t.Fatalf("provider model catalog conflict assignment %s = %q, want %q", field, actualAssignments[field], wantValue)
		}
	}
}
