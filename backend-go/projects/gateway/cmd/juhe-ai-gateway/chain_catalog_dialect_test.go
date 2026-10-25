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

	// PostgreSQL 唯一存储形态（PLAN-20261024T051925945Z）：原 sqlite 臂的
	// COLLATE NOCASE 排序断言随方言退役删除；pg 臂 lower(public_model) 排序
	// 断言保留（fixture 上真实执行 end to end）。
	pgSource, err := newChainCatalogSource(fixture.db)
	if err != nil {
		t.Fatalf("create pg source: %v", err)
	}
	pgQuery, _ := pgSource.customCatalogQuery(options, "2026-09-04", []string{"openai"})
	if strings.Contains(pgQuery, "COLLATE") {
		t.Fatalf("pg query still carries COLLATE (no such collation on PostgreSQL): %s", pgQuery)
	}
	if !strings.Contains(pgQuery, "lower(public_model)") {
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
		if _, err := fixture.db.Exec(fixtureBind(`INSERT INTO models (
				id, vendor_code, public_model, upstream_model, scope, system_account_id, status, catalog_visible,
				supported_endpoint_modes_json, input_usd_per_1m, created_at, updated_at
			) VALUES (?, 'openai', ?, ?, 'personal', ?, 'active', TRUE, '["chat_json","chat_sse"]', 1.0, ?, ?)`),
			"mdl_cpm_"+string(rune('a'+index)), model, model, fixture.systemAccount, now, now); err != nil {
			t.Fatalf("seed custom row %s: %v", model, err)
		}
	}
	source, err := newChainCatalogSource(fixture.db)
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
