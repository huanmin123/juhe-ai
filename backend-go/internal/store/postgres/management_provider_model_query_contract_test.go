package postgres

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestManagementProviderModelCatalogUsesPostgresBooleanVisibility(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_provider_models.sql")
	if err != nil {
		t.Fatalf("read provider model query: %v", err)
	}
	sql := string(source)
	if !regexp.MustCompile(`(?i)catalog_visible\s*=\s*true`).MatchString(sql) {
		t.Fatal("built-in provider model catalog must filter catalog_visible with PostgreSQL boolean true")
	}
	integerVisibility := regexp.MustCompile(`(?i)catalog_visible\s*=\s*(?:'1'|1\b)`)
	for _, invalidSQL := range []string{"catalog_visible = 1\n", "catalog_visible = '1'\n"} {
		if !integerVisibility.MatchString(invalidSQL) {
			t.Fatalf("integer visibility guard does not reject %q", strings.TrimSpace(invalidSQL))
		}
	}
	if integerVisibility.MatchString(sql) {
		t.Fatal("built-in provider model catalog must not compare PostgreSQL boolean catalog_visible with integer 1")
	}
	unionParts := strings.Split(sql, "UNION ALL")
	if len(unionParts) < 2 {
		t.Fatal("provider model catalog union is missing")
	}
	customSelect := strings.Split(unionParts[1], "FROM juhe_business.custom_provider_models")[0]
	if strings.Count(customSelect, "catalog_visible") != 1 || !regexp.MustCompile(`(?s)system_account_id,\s+status,\s+mode,`).MatchString(customSelect) {
		t.Fatal("custom provider model catalog must align catalog_visible with the built-in UNION column order")
	}
}

func TestManagementProviderModelOptionsReturnCapabilityContract(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_provider_models.sql")
	if err != nil {
		t.Fatalf("read provider model query: %v", err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")
	start := strings.Index(sql, "-- name: ListManagementProviderModelOptions :many")
	end := strings.Index(sql, "-- name: ListManagementProviderModelCapabilityCandidates :many")
	if start < 0 || end <= start {
		t.Fatal("provider model options query boundaries missing")
	}
	optionsSQL := sql[start:end]
	for _, column := range []string{
		"mode", "release_date", "supported_api_protocols_json", "supported_service_tiers_json",
		"supported_reasoning_efforts_json", "default_reasoning_effort",
	} {
		if !strings.Contains(optionsSQL, column) {
			t.Fatalf("provider model options must return %s", column)
		}
	}
	if !strings.Contains(optionsSQL, "release_date DESC") {
		t.Fatal("provider model options must prioritize newest release_date")
	}
	customStart := strings.Index(optionsSQL, "custom_ranked AS")
	if customStart < 0 || strings.Contains(optionsSQL[customStart:], "custom.catalog_visible = true") {
		t.Fatal("custom provider model options must ignore legacy catalog visibility")
	}
}

func TestManagementProviderModelQueryLocksFullConfigurationBeforeFullUpdate(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_provider_models.sql")
	if err != nil {
		t.Fatalf("read query source: %v", err)
	}
	sql := string(source)
	if regexp.MustCompile(`(?s)-- name: ListManagementProviderModelCatalog :many\s+SELECT\s+''::text AS id`).MatchString(sql) {
		t.Fatal("built-in provider model query must return the persisted id")
	}
	lockStart := strings.Index(sql, "-- name: LockManagementBuiltInProviderModelConfiguration :one")
	updateStart := strings.Index(sql, "-- name: UpdateManagementBuiltInProviderModelConfiguration :one")
	updateEnd := strings.Index(sql, "-- name: FindManagementCustomProviderModel :one")
	if lockStart < 0 || updateStart <= lockStart || updateEnd <= updateStart {
		t.Fatal("built-in provider model lock/update query boundaries missing")
	}
	lockSQL := sql[lockStart:updateStart]
	updateSQL := sql[updateStart:updateEnd]
	if !strings.Contains(lockSQL, "FOR UPDATE") || strings.Contains(updateSQL, "WITH locked") || strings.Contains(updateSQL, "CASE WHEN") {
		t.Fatal("built-in provider model update must lock first and write a complete candidate")
	}
	configurationColumns := []string{
		"id", "provider_code", "status", "catalog_visible", "mode", "supported_api_protocols_json", "supported_service_tiers_json",
		"supported_reasoning_efforts_json", "default_reasoning_effort", "release_date", "shutdown_date",
		"context_window_tokens", "max_input_tokens", "max_output_tokens", "input_usd_per_1m", "output_usd_per_1m",
		"cached_input_usd_per_1m", "cache_write_usd_per_1m", "cache_write_1h_usd_per_1m", "service_tier_prices_json",
		"image_input_usd_per_1m", "image_output_usd_per_1m", "audio_input_usd_per_1m", "audio_output_usd_per_1m",
		"output_usd_per_image", "updated_at",
	}
	for _, column := range configurationColumns {
		if !regexp.MustCompile(`(?s)` + regexp.QuoteMeta(column)).MatchString(lockSQL) {
			t.Fatalf("built-in provider model lock must return %s", column)
		}
		if column != "id" && column != "provider_code" && !regexp.MustCompile(regexp.QuoteMeta(column)+`\s*=\s*sqlc\.(?:n?arg)\(`).MatchString(updateSQL) {
			t.Fatalf("built-in provider model update must fully assign %s", column)
		}
	}
	if !strings.Contains(updateSQL, "RETURNING id, provider_code, status, catalog_visible, mode") {
		t.Fatal("built-in provider model update must return the stored after snapshot")
	}
}

func TestManagementCustomProviderModelQueryLocksFullRowBeforeExactUpdate(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_provider_models.sql")
	if err != nil {
		t.Fatalf("read provider model query: %v", err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")
	lockStart := strings.Index(sql, "-- name: LockManagementCustomProviderModel :one")
	updateStart := strings.Index(sql, "-- name: UpdateManagementCustomProviderModel :one")
	upsertStart := strings.Index(sql, "-- name: UpsertManagementCustomProviderModel :one")
	if lockStart < 0 || updateStart <= lockStart || upsertStart <= updateStart {
		t.Fatalf("custom provider model lock/update query boundaries missing")
	}
	lockSQL := sql[lockStart:updateStart]
	updateSQL := sql[updateStart:upsertStart]
	if !strings.Contains(lockSQL, "WHERE id = sqlc.arg(id) AND provider_code = sqlc.arg(provider_code)") || !strings.Contains(lockSQL, "FOR UPDATE") {
		t.Fatalf("custom provider model lock must scope id+provider and use FOR UPDATE:\n%s", lockSQL)
	}
	if strings.Contains(updateSQL, "ON CONFLICT") || !strings.Contains(updateSQL, "UPDATE juhe_business.custom_provider_models") || !strings.Contains(updateSQL, "RETURNING") {
		t.Fatalf("custom provider model PATCH must use an exact UPDATE/RETURNING:\n%s", updateSQL)
	}
	for _, column := range []string{"status", "catalog_visible", "mode", "supported_api_protocols_json", "service_tier_prices_json", "pricing_notes", "capability_notes", "notes", "updated_by", "updated_at"} {
		if !regexp.MustCompile(regexp.QuoteMeta(column) + `\s*=\s*sqlc\.(?:n?arg)\(`).MatchString(updateSQL) {
			t.Fatalf("custom provider model update must fully assign %s", column)
		}
	}
}
