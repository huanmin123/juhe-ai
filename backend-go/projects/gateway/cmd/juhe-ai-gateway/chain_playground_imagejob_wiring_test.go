package main

// 演练场图像工作台 cmd 装配冒烟测试（设计 §3.4 v1.3）：图像任务扣费端口
// （runtime cache 目录行 × pricing.BuildCostBreakdown 本地计算；无价/false →
// cost_usd=null 语义）。v1.3 起目录 imageEstimate 计价投影已随「预计扣费」
// 展示移除而退役（无独立投影测试）。runtime cache 用 w1hReadModels 测试双
// 构造，不触网。

import (
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-platform/timeclock"
)

func ptrFloat64(value float64) *float64 { return &value }

func imageMediaPricingJSON() []byte {
	return []byte(`{"version":1,"unit":"image","baseUsd":0.05,"baseTier":"1k","tierMultipliers":{"1k":1,"2k":1.6,"4k":3}}`)
}

// newPlaygroundCostCache 构造带 gpt-image 定价行的 runtime cache（测试双装载）。
func newPlaygroundCostCache(t *testing.T, items []gatewayruntimecache.ProviderModelCatalogItem) *gatewayruntimecache.Service {
	t.Helper()
	service, err := gatewayruntimecache.New(&w1hReadModels{catalog: items}, gatewayruntimecache.Options{
		Clock: timeclock.ClockFunc(time.Now),
	})
	if err != nil {
		t.Fatalf("组装 runtime cache: %v", err)
	}
	t.Cleanup(service.Close)
	return service
}

// TestPlaygroundImageCostAdapter 扣费端口：统一模板按档位单价 × 张数（1024x1024
// → 1k 档）；按张槽位行计张价；目录 miss/无价/nil cache → false（cost_usd=null
// 语义）；usage 按张计价模型不虚构 token 费。
func TestPlaygroundImageCostAdapter(t *testing.T) {
	if _, ok := (playgroundImageCostAdapter{}).ImageCost("gpt", "gpt-image-2", "1024x1024", 1, 0, 0); ok {
		t.Fatal("nil cache 必须返回 false")
	}
	adapter := playgroundImageCostAdapter{cache: newPlaygroundCostCache(t, []gatewayruntimecache.ProviderModelCatalogItem{
		{Model: "gpt-image-2", ProviderCode: "gpt", HasMediaPricing: true, MediaPricing: imageMediaPricingJSON()},
		{Model: "grok-imagine-image", ProviderCode: "xai", OutputUsdPerImage: ptrFloat64(0.02)},
	})}
	cost, ok := adapter.ImageCost("gpt", "gpt-image-2", "1024x1024", 1, 12, 4096)
	if !ok || cost < 0.0499 || cost > 0.0501 {
		t.Fatalf("模板 1k 档计价 = %v ok=%v，期望 0.05", cost, ok)
	}
	cost, ok = adapter.ImageCost("gpt", "gpt-image-2", "2048x2048", 1, 0, 0)
	if !ok || cost < 0.0799 || cost > 0.0801 {
		t.Fatalf("模板 2k 档计价 = %v ok=%v，期望 0.08", cost, ok)
	}
	cost, ok = adapter.ImageCost("xai", "grok-imagine-image", "1024x1024", 1, 0, 0)
	if !ok || cost < 0.0199 || cost > 0.0201 {
		t.Fatalf("按张槽位计价 = %v ok=%v，期望 0.02", cost, ok)
	}
	if _, ok = adapter.ImageCost("xai", "unknown-model", "1024x1024", 1, 0, 0); ok {
		t.Fatal("目录 miss 必须返回 false")
	}
	if _, ok = adapter.ImageCost("", "gpt-image-2", "1024x1024", 1, 0, 0); ok {
		t.Fatal("provider 空必须返回 false")
	}
	// 无价行（Unpriced）：目录装载默认过滤无价行 → miss → false。
	unpricedAdapter := playgroundImageCostAdapter{cache: newPlaygroundCostCache(t, []gatewayruntimecache.ProviderModelCatalogItem{
		{Model: "gpt-image-2", ProviderCode: "gpt"},
	})}
	if _, ok = unpricedAdapter.ImageCost("gpt", "gpt-image-2", "1024x1024", 1, 0, 0); ok {
		t.Fatal("无价行必须返回 false（cost_usd=null）")
	}
}
