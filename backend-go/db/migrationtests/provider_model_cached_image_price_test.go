package migrationtests

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestProviderModelCachedImagePriceMigrationMatchesNodeSnapshot(t *testing.T) {
	const migrationName = "000079_w2_custom_provider_model_cache_storage_price.sql"
	source, err := os.ReadFile(filepath.Join("..", "migrations", migrationName))
	if err != nil {
		t.Fatalf("read %s: %v", migrationName, err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")

	for _, want := range []string{
		"ALTER TABLE juhe_business.custom_provider_models\n  ADD COLUMN IF NOT EXISTS cache_storage_usd_per_1m_per_hour double precision;",
		"ALTER TABLE juhe_business.provider_model_catalog\n  ADD COLUMN IF NOT EXISTS cached_image_input_usd_per_1m double precision;",
		"WHERE catalog.provider_code = 'gpt'\n  AND catalog.model = pricing.model;",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("%s missing contract fragment %q", migrationName, want)
		}
	}

	rowPattern := regexp.MustCompile(`(?m)^  \('([^']+)', ([0-9.]+)::double precision\)[,]?$`)
	matches := rowPattern.FindAllStringSubmatch(sql, -1)
	wantPrices := map[string]float64{
		"gpt-image-2":            2,
		"gpt-image-2-2026-04-21": 2,
		"gpt-image-1.5":          2,
		"gpt-image-1-mini":       0.25,
		"gpt-image-1":            2.5,
	}
	if len(matches) != len(wantPrices) {
		t.Fatalf("%s cached image price row count = %d, want %d", migrationName, len(matches), len(wantPrices))
	}

	seen := make(map[string]bool, len(matches))
	for _, match := range matches {
		model := match[1]
		if seen[model] {
			t.Fatalf("%s duplicates cached image price for %q", migrationName, model)
		}
		seen[model] = true

		got, err := strconv.ParseFloat(match[2], 64)
		if err != nil {
			t.Fatalf("parse cached image price for %q: %v", model, err)
		}
		want, ok := wantPrices[model]
		if !ok {
			t.Fatalf("%s seeds unexpected cached image model %q", migrationName, model)
		}
		if got != want {
			t.Fatalf("%s cached image price for %q = %v, want %v", migrationName, model, got, want)
		}
	}
	for model := range wantPrices {
		if !seen[model] {
			t.Fatalf("%s missing cached image price for %q", migrationName, model)
		}
	}
}
