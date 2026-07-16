package postgres

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

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
		"id", "provider_code", "status", "mode", "supported_api_protocols_json", "supported_service_tiers_json",
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
	if !strings.Contains(updateSQL, "RETURNING id, provider_code, status, mode") {
		t.Fatal("built-in provider model update must return the stored after snapshot")
	}
}
