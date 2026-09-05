package main

// Dual-dialect catalog ordering tests (chain_catalog.go K2): the
// custom-provider model ORDER BY mirrors the Node SQLite / PostgreSQL split
// (custom-provider-models.repository.ts) instead of the SQLite-only
// `model COLLATE NOCASE` that 500'd every PG catalog read.

import (
	"context"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestChainCatalogCustomModelOrderDualDialect(t *testing.T) {
	fixture := newChainFixture(t)
	options := gatewayruntimecache.ModelCatalogListOptions{ProviderCode: "openai", SystemAccountID: "sys_owner"}

	sqliteSource, err := newChainCatalogSource(fixture.db, false)
	if err != nil {
		t.Fatalf("create sqlite source: %v", err)
	}
	sqliteQuery, _ := sqliteSource.customCatalogQuery(options, "2026-09-04")
	if !strings.Contains(sqliteQuery, "model COLLATE NOCASE") {
		t.Fatalf("sqlite query lost the Node NOCASE ordering: %s", sqliteQuery)
	}
	// The rendered SQLite query must actually run end to end.
	if _, err := sqliteSource.ListProviderModelCatalog(context.Background(), options); err != nil {
		t.Fatalf("sqlite catalog read: %v", err)
	}

	pgSource, err := newChainCatalogSource(fixture.db, true)
	if err != nil {
		t.Fatalf("create pg source: %v", err)
	}
	pgQuery, _ := pgSource.customCatalogQuery(options, "2026-09-04")
	if strings.Contains(pgQuery, "COLLATE") {
		t.Fatalf("pg query still carries COLLATE (no such collation on PostgreSQL): %s", pgQuery)
	}
	if !strings.Contains(pgQuery, "lower(model)") {
		t.Fatalf("pg query missing the Node postgres ordering: %s", pgQuery)
	}
}

// TestChainCatalogCustomModelNOCASEOrdering proves the SQLite arm returns the
// case-insensitive Node order over mixed-case custom rows (alpha < beta <
// Gamma in NOCASE order while a binary order would interleave them).
func TestChainCatalogCustomModelNOCASEOrdering(t *testing.T) {
	fixture := newChainFixture(t)
	now := "2026-09-04T00:00:00.000Z"
	for index, model := range []string{"beta", "Gamma", "alpha"} {
		if _, err := fixture.db.Exec(`INSERT INTO custom_provider_models (
				id, provider_code, model, scope, system_account_id, status, catalog_visible,
				supported_api_protocols_json, supported_service_tiers_json, supported_reasoning_efforts_json,
				service_tier_prices_json, created_at, updated_at
			) VALUES (?, 'openai', ?, 'personal', ?, 'active', 1, '[]', '[]', '[]', '{}', ?, ?)`,
			"cpm_"+string(rune('a'+index)), model, fixture.systemAccount, now, now); err != nil {
			t.Fatalf("seed custom row %s: %v", model, err)
		}
	}
	source, err := newChainCatalogSource(fixture.db, false)
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	items, err := source.ListProviderModelCatalog(context.Background(), gatewayruntimecache.ModelCatalogListOptions{ProviderCode: "openai", SystemAccountID: fixture.systemAccount})
	if err != nil {
		t.Fatalf("list catalog: %v", err)
	}
	ordered := []string{}
	for _, item := range items {
		if item.Scope == "personal" {
			ordered = append(ordered, item.Model)
		}
	}
	if len(ordered) != 3 || ordered[0] != "alpha" || ordered[1] != "beta" || ordered[2] != "Gamma" {
		t.Fatalf("custom order = %v, want NOCASE alpha/beta/Gamma", ordered)
	}
}
