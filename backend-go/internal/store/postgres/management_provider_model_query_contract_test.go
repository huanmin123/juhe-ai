package postgres

import (
	"os"
	"regexp"
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
	if !regexp.MustCompile(`(?s)-- name: UpdateManagementBuiltInProviderModelPrices :one.*RETURNING\s+id`).MatchString(sql) {
		t.Fatal("built-in provider model price update must return the committed snapshot")
	}
}
