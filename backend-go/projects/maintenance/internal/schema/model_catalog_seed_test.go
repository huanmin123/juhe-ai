// Tests for the generated Node pricing snapshot (model_catalog_data.go) and
// the shared seed helpers. The row count pins the 2026-09-04 Node dump; when
// the Node pricing data changes, regenerate with dump_model_catalog.mts and
// update the pinned count in one change.

package schema

import (
	"encoding/json"
	"strings"
	"testing"
)

// nodeModelCatalogCompare mirrors compareProviderModels in Node
// model-pricing.service.ts (catalog order asc when both defined, then release
// date desc with nils last, then model asc).
func nodeModelCatalogCompare(left, right modelCatalogSeedRow) int {
	if left.CatalogOrder != nil && right.CatalogOrder != nil && *left.CatalogOrder != *right.CatalogOrder {
		if *left.CatalogOrder < *right.CatalogOrder {
			return -1
		}
		return 1
	}
	switch {
	case left.ReleaseDate != nil && right.ReleaseDate != nil && *left.ReleaseDate != *right.ReleaseDate:
		if *left.ReleaseDate > *right.ReleaseDate {
			return -1
		}
		return 1
	case left.ReleaseDate != nil && right.ReleaseDate == nil:
		return -1
	case left.ReleaseDate == nil && right.ReleaseDate != nil:
		return 1
	}
	return strings.Compare(left.Model, right.Model)
}

func TestModelCatalogSeedRowsPinnedCount(t *testing.T) {
	// 106 rows for the 2026-09-04 dump (gpt 56, xai 9, deepseek 2,
	// anthropic 13, gemini 12, glm 14). Regenerate + update together when the
	// Node pricing data changes.
	if len(modelCatalogSeedRows) != 106 {
		t.Fatalf("model catalog seed rows = %d, want 106 (stale snapshot? regenerate with dump_model_catalog.mts)", len(modelCatalogSeedRows))
	}
	perProvider := map[string]int{}
	for _, row := range modelCatalogSeedRows {
		perProvider[row.ProviderCode]++
	}
	want := map[string]int{"gpt": 56, "xai": 9, "deepseek": 2, "anthropic": 13, "gemini": 12, "glm": 14}
	if len(perProvider) != len(want) {
		t.Fatalf("provider set = %v, want %v", perProvider, want)
	}
	for provider, count := range want {
		if perProvider[provider] != count {
			t.Fatalf("provider %s rows = %d, want %d", provider, perProvider[provider], count)
		}
	}
}

func TestModelCatalogSeedRowsIdentityAndOrder(t *testing.T) {
	seenModels := map[string]bool{}
	seenIDs := map[string]bool{}
	for _, row := range modelCatalogSeedRows {
		key := row.ProviderCode + "\x00" + row.Model
		if seenModels[key] {
			t.Fatalf("duplicate (provider, model) %q", key)
		}
		seenModels[key] = true
		wantID := providerModelCatalogID(row.ProviderCode, row.Model)
		if row.ID != wantID {
			t.Fatalf("row id %q does not match providerModelCatalogId %q", row.ID, wantID)
		}
		if seenIDs[row.ID] {
			t.Fatalf("duplicate id %q", row.ID)
		}
		seenIDs[row.ID] = true
		if row.Source == "" {
			t.Fatalf("row %q has empty source", row.ID)
		}
		if !jsonValidString(row.SupportedAPIProtocolsJSON) {
			t.Fatalf("row %q has invalid supported_api_protocols_json", row.ID)
		}
		if !jsonValidString(row.ServiceTierPricesJSON) {
			t.Fatalf("row %q has invalid service_tier_prices_json", row.ID)
		}
	}
	// Sortedness holds within each provider segment (Node sorts
	// listProviderModelPricing per provider); segments follow the
	// DEFAULT_PROVIDER_SEEDS order (gpt, xai, deepseek, anthropic, gemini,
	// glm) instead of a global comparator order.
	segmentStart := 0
	for i := 1; i <= len(modelCatalogSeedRows); i++ {
		if i == len(modelCatalogSeedRows) || modelCatalogSeedRows[i].ProviderCode != modelCatalogSeedRows[segmentStart].ProviderCode {
			for j := segmentStart + 1; j < i; j++ {
				if nodeModelCatalogCompare(modelCatalogSeedRows[j-1], modelCatalogSeedRows[j]) > 0 {
					t.Fatalf("rows for provider %s not in Node listProviderModelPricing order at index %d: %s > %s", modelCatalogSeedRows[segmentStart].ProviderCode, j, modelCatalogSeedRows[j-1].ID, modelCatalogSeedRows[j].ID)
				}
			}
			segmentStart = i
		}
	}
}

func TestProviderModelCatalogID(t *testing.T) {
	cases := []struct {
		provider, model, want string
	}{
		// Golden values produced by Node providerModelCatalogId.
		{"gpt", "gpt-5.6-sol", "provider_model_gpt_gpt_5_6_sol_69ec47b65152"},
		{"gpt", "gpt-6-astra", "provider_model_gpt_gpt_6_astra_14ec3d68eae1"},
	}
	for _, testCase := range cases {
		if got := providerModelCatalogID(testCase.provider, testCase.model); got != testCase.want {
			t.Fatalf("providerModelCatalogId(%q, %q) = %q, want %q", testCase.provider, testCase.model, got, testCase.want)
		}
	}
}

func TestActiveModelCatalogSeedRowsShutdownFilter(t *testing.T) {
	var shutdownDate *string
	for i := range modelCatalogSeedRows {
		if modelCatalogSeedRows[i].ShutdownDate != nil {
			shutdownDate = modelCatalogSeedRows[i].ShutdownDate
			break
		}
	}
	if shutdownDate == nil {
		t.Skip("no shutdown dates in snapshot")
	}
	all := len(modelCatalogSeedRows)
	if got := len(activeModelCatalogSeedRows("2000-01-01")); got != all {
		t.Fatalf("rows active at 2000-01-01 = %d, want %d", got, all)
	}
	excluded := 0
	for _, row := range modelCatalogSeedRows {
		if row.ShutdownDate != nil && *row.ShutdownDate <= *shutdownDate {
			excluded++
		}
	}
	if excluded == 0 {
		t.Fatalf("shutdown date %s should exclude at least one row", *shutdownDate)
	}
	if got := len(activeModelCatalogSeedRows(*shutdownDate)); got != all-excluded {
		t.Fatalf("rows active at %s = %d, want %d", *shutdownDate, got, all-excluded)
	}
}

func jsonValidString(value string) bool {
	return json.Valid([]byte(value))
}
