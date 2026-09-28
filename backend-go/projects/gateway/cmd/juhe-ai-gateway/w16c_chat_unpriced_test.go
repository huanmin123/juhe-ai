package main

// w16c（批次1-C）回归：chat 面目录读取必须以 IncludeUnpriced=true 走缓存
// 目录链（对齐 Node includeUnpriced:true），未定价模型不得从 chat 模型
// 列表/能力/默认推举消失。

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// w16cCatalogReadModels 复刻目录源的 unpriced 过滤臂（chain_catalog.go 的
// IncludeUnpriced=false 剔除无直接定价行），并记录 chat 面实际传入的开关。
type w16cCatalogReadModels struct {
	gatewayruntimecache.ReadModels
	catalog   []gatewayruntimecache.ProviderModelCatalogItem
	seenFlags []bool
}

func (m *w16cCatalogReadModels) ListProviderModelCatalog(_ context.Context, input gatewayruntimecache.ModelCatalogListOptions) ([]gatewayruntimecache.ProviderModelCatalogItem, error) {
	m.seenFlags = append(m.seenFlags, input.IncludeUnpriced)
	out := []gatewayruntimecache.ProviderModelCatalogItem{}
	for _, item := range m.catalog {
		if !input.IncludeUnpriced && item.InputUsdPer1M == nil && item.OutputUsdPer1M == nil {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func w16cFloatPtr(value float64) *float64 { return &value }

// TestW16CChatCatalogIncludesUnpriced chat 面经 runtime cache 读目录时
// IncludeUnpriced 必须为 true：有价行与无价行同时可见。
func TestW16CChatCatalogIncludesUnpriced(t *testing.T) {
	models := &w16cCatalogReadModels{catalog: []gatewayruntimecache.ProviderModelCatalogItem{
		{ProviderCode: "gpt", Model: "gpt-priced", Status: "active", InputUsdPer1M: w16cFloatPtr(1.5)},
		{ProviderCode: "gpt", Model: "gpt-free", Status: "active"},
	}}
	cache, err := gatewayruntimecache.New(models, gatewayruntimecache.Options{})
	if err != nil {
		t.Fatalf("组装缓存: %v", err)
	}
	t.Cleanup(cache.Close)

	items := (&chatModelCatalog{cache: cache}).ListProviderCatalog("gpt", "sys_w16c")
	found := map[string]bool{}
	for _, item := range items {
		found[item.Model] = true
	}
	if !found["gpt-priced"] {
		t.Fatalf("有价行必须可见 = %+v", items)
	}
	if !found["gpt-free"] {
		t.Fatalf("无价行必须对 chat 面可见（includeUnpriced 语义）= %+v", items)
	}
	if len(models.seenFlags) == 0 {
		t.Fatal("chat 面必须触发目录读取")
	}
	for _, flag := range models.seenFlags {
		if !flag {
			t.Fatalf("chat 面目录读取必须传 IncludeUnpriced=true = %v", models.seenFlags)
		}
	}
}
