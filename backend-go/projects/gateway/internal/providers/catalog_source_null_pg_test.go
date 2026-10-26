package providers

import (
	"context"
	"testing"
)

// W31 回归（真实 PG）：models.source 在生产 DDL 可空（`source text` 无 NOT
// NULL），builtin 读投影曾以裸列扫入非空 string 字段——source IS NULL 的内置
// 目录行使 ListProviderModelsForRequest 恒报 converting NULL to string is
// unsupported（接口 500）。修复后 COALESCE(source,'')：可读且 API 字段语义
// 不变（NULL 读作空串，非 "manual-override" 值继续走静态定价来源解析）。

func TestW31BuiltInCatalogRowWithNullSourceStaysReadable(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	// gpt-4o-mini（cat-2）无 custom 竞争行，合并后仍以 builtin 行出现在目录。
	env.exec(t, `UPDATE models SET source = NULL WHERE id IN ('cat-1', 'cat-2')`)

	items, err := env.providersDeps.Store.ListProviderModelsForRequest(context.Background(), "gpt", "", false, false)
	if err != nil {
		t.Fatalf("null-source builtin rows must stay readable: %v", err)
	}
	found := false
	for _, item := range items {
		if item.Model == "gpt-4o-mini" {
			found = true
			if item.Scope != "built_in" {
				t.Fatalf("gpt-4o-mini scope=%q, want built_in", item.Scope)
			}
			if item.Source != "" {
				t.Fatalf("null source must project as empty string, got %q", item.Source)
			}
		}
	}
	if !found {
		t.Fatalf("gpt-4o-mini missing from catalog: %d items", len(items))
	}
}

// 同一投影的第二个消费方（内置补丁态全行读）在 source IS NULL 下同样可读。
func TestW31BuiltInPatchStateWithNullSourceStaysReadable(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.exec(t, `UPDATE models SET source = NULL WHERE id = 'cat-2'`)

	item, err := env.providersDeps.Store.findBuiltInModelPatchState(context.Background(), "cat-2")
	if err != nil {
		t.Fatalf("null-source patch state must stay readable: %v", err)
	}
	if item == nil {
		t.Fatal("cat-2 must resolve")
	}
	if item.Source != "" {
		t.Fatalf("null source must project as empty string, got %q", item.Source)
	}
}
