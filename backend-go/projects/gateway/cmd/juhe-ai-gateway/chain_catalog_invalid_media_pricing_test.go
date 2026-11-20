package main

// 运行期非法 media_pricing_json 的目录可达性回归（AC-016）：该行对绑定/展示
// 仍按无价过滤，但同步/异步计价查询必须能读到 MediaPricingInvalid 标记，
// 否则会目录 miss 后回退静态 legacy 价——这正是 2026-11-20 独立复审发现的
// P0 缺口。本用例走真实 PG 目录源 + runtime cache，不复用直接构造 item 的
// 单测替身。

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestChainCatalogInvalidMediaPricingReachableForRuntimePricing(t *testing.T) {
	fixture := newChainFixture(t)
	// cogvideox-3 在静态 legacy 层有按次价；用它验证非法行绝不能回填静态价。
	if _, err := fixture.db.Exec(fixtureBind(`INSERT INTO models (
			id, vendor_code, public_model, upstream_model, scope, status, catalog_visible,
			supported_endpoint_modes_json, media_pricing_json, created_at, updated_at)
		VALUES ('mdl_invalid_media_pricing', 'glm', 'cogvideox-3', 'cogvideox-3', 'global', 'active', TRUE,
			'["chat_json","video_create","video_get","video_content","video_cancel"]',
			'{"version":1}', '2026-11-20T00:00:00.000Z', '2026-11-20T00:00:00.000Z')`)); err != nil {
		t.Fatalf("seed invalid media pricing row: %v", err)
	}
	// 结构合法但 mode/kind 不匹配（video 目录行配 unit=image）同样按无价处理。
	if _, err := fixture.db.Exec(fixtureBind(`INSERT INTO models (
			id, vendor_code, public_model, upstream_model, scope, status, catalog_visible,
			supported_endpoint_modes_json, media_pricing_json, created_at, updated_at)
		VALUES ('mdl_mode_mismatch_media_pricing', 'glm', 'cogvideox-3-mode-mismatch', 'cogvideox-3-mode-mismatch', 'global', 'active', TRUE,
			'["chat_json","video_create","video_get","video_content","video_cancel"]',
			'{"version":1,"unit":"image","baseUsd":0.04,"baseTier":"1k","tierMultipliers":{"1k":1,"2k":1.6,"4k":3}}',
			'2026-11-20T00:00:00.000Z', '2026-11-20T00:00:00.000Z')`)); err != nil {
		t.Fatalf("seed mode mismatch media pricing row: %v", err)
	}
	// Gemini 原生 generate_content 图像行：endpoint modes 投影为 chat，但
	// media_pricing_json 是 image 模板；读侧不得据此判成非法价（2026-11-20
	// 第二轮复审 P0）。
	if _, err := fixture.db.Exec(fixtureBind(`INSERT INTO models (
			id, vendor_code, public_model, upstream_model, scope, status, catalog_visible,
			supported_endpoint_modes_json, media_pricing_json, created_at, updated_at)
		VALUES ('mdl_generate_content_image', 'gemini', 'gemini-3.1-flash-image', 'gemini-3.1-flash-image', 'global', 'active', TRUE,
			'["generate_content_json","generate_content_sse"]',
			'{"version":1,"unit":"image","baseUsd":0.0672,"baseTier":"1k","tierMultipliers":{"1k":1,"2k":1.6,"4k":3}}',
			'2026-11-20T00:00:00.000Z', '2026-11-20T00:00:00.000Z')`)); err != nil {
		t.Fatalf("seed generate_content image row: %v", err)
	}

	source, err := newChainCatalogSource(fixture.db)
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	// 默认目录（绑定/展示）仍按无价过滤：非法行不可见。
	defaultItems, err := source.ListProviderModelCatalog(context.Background(),
		gatewayruntimecache.ModelCatalogListOptions{ProviderCode: "glm"})
	if err != nil {
		t.Fatalf("list default catalog: %v", err)
	}
	if item := findChainCatalogItem(defaultItems, "cogvideox-3"); item != nil {
		t.Fatalf("非法 media_pricing_json 行不得进入 priced/绑定目录: %#v", item)
	}

	if item := findChainCatalogItem(defaultItems, "cogvideox-3-mode-mismatch"); item != nil {
		t.Fatalf("mode/kind 不匹配的 media_pricing_json 行不得进入 priced/绑定目录: %#v", item)
	}

	// chat 投影的 generate_content 图像行必须保持有价、可绑定（不得因
	// mode 推导为 chat 而误判 invalid）。
	geminiItems, err := source.ListProviderModelCatalog(context.Background(),
		gatewayruntimecache.ModelCatalogListOptions{ProviderCode: "gemini"})
	if err != nil {
		t.Fatalf("list gemini default catalog: %v", err)
	}
	geminiImage := findChainCatalogItem(geminiItems, "gemini-3.1-flash-image")
	if geminiImage == nil || !geminiImage.HasMediaPricing || geminiImage.MediaPricingInvalid {
		t.Fatalf("generate_content 图像行必须保持有价: %#v", geminiImage)
	}
	geminiPricing := chainCatalogPricing(geminiImage)
	if geminiPricing == nil || geminiPricing.MediaPricing == nil || geminiPricing.MediaPricingInvalid {
		t.Fatalf("generate_content 图像行计价行必须带有效模板: %#v", geminiPricing)
	}

	// 运行期计价查询打开 IncludeInvalidMediaPricing：行必须可见且带 invalid 标记。
	runtimeItems, err := source.ListProviderModelCatalog(context.Background(),
		gatewayruntimecache.ModelCatalogListOptions{ProviderCode: "glm", IncludeInvalidMediaPricing: true})
	if err != nil {
		t.Fatalf("list runtime catalog: %v", err)
	}
	item := findChainCatalogItem(runtimeItems, "cogvideox-3")
	if item == nil {
		t.Fatalf("运行期计价目录必须能看到非法 media_pricing_json 行: %#v", runtimeItems)
	}
	if mismatch := findChainCatalogItem(runtimeItems, "cogvideox-3-mode-mismatch"); mismatch == nil ||
		!mismatch.MediaPricingInvalid || mismatch.HasMediaPricing {
		t.Fatalf("mode/kind 不匹配行必须在运行期标记 invalid: %#v", mismatch)
	}
	if !item.MediaPricingInvalid || item.HasMediaPricing || len(item.MediaPricing) != 0 {
		t.Fatalf("非法行标记错误: invalid=%v has=%v json=%s",
			item.MediaPricingInvalid, item.HasMediaPricing, string(item.MediaPricing))
	}
	if chainHasDirectCatalogPrice(*item) {
		t.Fatal("非法行不得被判定为可计价/可绑定")
	}
	// 非法行不得从静态 legacy 层补齐媒体价（cogvideox-3 静态层有按次价）。
	pricingRow := chainCatalogPricing(item)
	if pricingRow == nil || !pricingRow.MediaPricingInvalid || pricingRow.MediaPricing != nil {
		t.Fatalf("chainCatalogPricing 必须保留 invalid 且不产出模板: %#v", pricingRow)
	}
	if pricingRow.PriceSet.VideoOutputUsdPerCall != nil {
		t.Fatalf("非法行不得回填静态 legacy 按次价: %v", *pricingRow.PriceSet.VideoOutputUsdPerCall)
	}

	// 同步/异步共用的 runtime-cache 查询（findItem）必须带出 invalid 标记。
	catalog := &chainUsagePricingCatalog{cache: fixture.cache, memo: newChainPricingCatalogMemo()}
	if !catalog.MediaPricingInvalid("glm", "", "cogvideox-3") {
		t.Fatal("MediaPricingInvalid 运行时查询不可达（P0 回归）")
	}
	if catalog.HasMediaPricing("glm", "", "cogvideox-3") {
		t.Fatal("非法行不得报告有效媒体模板")
	}
}
