package main

// w1: chain_pricing.go 计费目录适配层测试。fixture catalog 种子行补价后，
// 同步/异步两个计费面（chainUsagePricingCatalog / chainCostEstimator）全
// 方法走真实 pricing 引擎。

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// newW1PricedFixture 在 fixture 上把 gpt-test 目录行补齐价格并返回 catalog。
func newW1PricedFixture(t *testing.T) *chainFixture {
	t.Helper()
	fixture := newChainFixture(t)
	if _, err := fixture.db.Exec(`UPDATE provider_model_catalog SET
		input_usd_per_1m = 1.5, output_usd_per_1m = 2.0, cached_input_usd_per_1m = 0.15,
		cache_write_usd_per_1m = 0.5 WHERE model = 'gpt-test'`); err != nil {
		t.Fatalf("price catalog: %v", err)
	}
	return fixture
}

func w1PricingInput() gatewayusagePricingCostInput {
	input := gatewayusagePricingCostInput{ProviderCode: "openai", SystemAccountID: "sys_owner", Model: "gpt-test"}
	million := 1_000_000
	input.InputTokens = &million
	output := 500_000
	input.OutputTokens = &output
	return input
}

func TestW1UsagePricingCatalogEstimateCost(t *testing.T) {
	fixture := newW1PricedFixture(t)
	catalog := newChainUsagePricingCatalog(fixture.cache)
	// 空模型 / nil catalog：空串。
	if got := catalog.ResolvePricingModel("openai", fixture.systemAccount, "  "); got != "" {
		t.Fatalf("空模型 = %q", got)
	}
	var nilCatalog *chainUsagePricingCatalog
	if got := nilCatalog.ResolvePricingModel("openai", fixture.systemAccount, "gpt-test"); got != "" {
		t.Fatalf("nil catalog = %q", got)
	}
	// 命中定价模型。
	if got := catalog.ResolvePricingModel("openai", fixture.systemAccount, "gpt-test"); got != "gpt-test" {
		t.Fatalf("pricing model = %q", got)
	}
	if got := catalog.ResolvePricingModel("openai", fixture.systemAccount, "  gpt-test  "); got != "gpt-test" {
		t.Fatalf("trim 匹配 = %q", got)
	}
	// 未知模型：空串。
	if got := catalog.ResolvePricingModel("openai", fixture.systemAccount, "gpt-never"); got != "" {
		t.Fatalf("未知模型 = %q", got)
	}
	// EstimateCost：1.5×1 + 2.0×0.5 = 2.5 USD。
	cost := catalog.EstimateCost(w1PricingInput())
	if cost == nil || *cost < 2.499 || *cost > 2.501 {
		t.Fatalf("cost = %v，want ~2.5", cost)
	}
	// 无成本维度：nil。
	empty := w1PricingInput()
	empty.InputTokens = nil
	empty.OutputTokens = nil
	if got := catalog.EstimateCost(empty); got != nil {
		t.Fatalf("无维度 = %v", got)
	}
	// 未知模型：nil。
	unknown := w1PricingInput()
	unknown.Model = "gpt-never"
	if got := catalog.EstimateCost(unknown); got != nil {
		t.Fatalf("未知模型 = %v", got)
	}
	// nil catalog / 空 model 安全。
	if nilCatalog.EstimateCost(w1PricingInput()) != nil {
		t.Fatal("nil catalog 必须 nil")
	}
}

func TestW1UsagePricingCatalogCacheCosts(t *testing.T) {
	fixture := newW1PricedFixture(t)
	catalog := newChainUsagePricingCatalog(fixture.cache)
	// 无 cache token：两个方法都 nil。
	base := w1PricingInput()
	base.InputTokens, base.OutputTokens = nil, nil
	if got := catalog.EstimateCacheReadCost(base); got != nil {
		t.Fatalf("无 read token = %v", got)
	}
	if got := catalog.EstimateCacheWriteCost(base); got != nil {
		t.Fatalf("无 write token = %v", got)
	}
	// CacheRead：0.15 × 1M/1M = 0.15。
	readTokens := 1_000_000
	read := base
	read.CacheReadTokens = &readTokens
	got := catalog.EstimateCacheReadCost(read)
	if got == nil || *got < 0.149 || *got > 0.151 {
		t.Fatalf("cache read = %v，want ~0.15", got)
	}
	// CacheWrite：0.5 × 1M/1M = 0.5。
	writeTokens := 1_000_000
	write := base
	write.CacheWriteTokens = &writeTokens
	got = catalog.EstimateCacheWriteCost(write)
	if got == nil || *got < 0.499 || *got > 0.501 {
		t.Fatalf("cache write = %v，want ~0.5", got)
	}
	// 零 token + 有费率：0。
	zero := 0
	writeZero := base
	writeZero.CacheWriteTokens = &zero
	got = catalog.EstimateCacheWriteCost(writeZero)
	if got == nil || *got != 0 {
		t.Fatalf("零 write = %v", got)
	}
}

func TestW1CostEstimatorAsync(t *testing.T) {
	fixture := newW1PricedFixture(t)
	estimator := newChainCostEstimator(fixture.cache)
	// nil estimator / nil cache / 空模型：false。
	var nilEstimator *chainCostEstimator
	if _, ok := nilEstimator.EstimateCatalogCostUSD(context.Background(), gatewayquotaCatalogCostInputFixture()); ok {
		t.Fatal("nil estimator 必须 false")
	}
	if _, ok := newChainCostEstimator(nil).EstimateCatalogCostUSD(context.Background(), gatewayquotaCatalogCostInputFixture()); ok {
		t.Fatal("nil cache 必须 false")
	}
	empty := gatewayquotaCatalogCostInputFixture()
	empty.Model = "  "
	if _, ok := estimator.EstimateCatalogCostUSD(context.Background(), empty); ok {
		t.Fatal("空模型必须 false")
	}
	// 命中：1.5×1 + 2.0×0.5 = 2.5。
	cost, ok := estimator.EstimateCatalogCostUSD(context.Background(), gatewayquotaCatalogCostInputFixture())
	if !ok || cost < 2.499 || cost > 2.501 {
		t.Fatalf("cost = %v ok=%v，want ~2.5/true", cost, ok)
	}
	// 未知模型：false。
	missing := gatewayquotaCatalogCostInputFixture()
	missing.Model = "gpt-never"
	if _, ok := estimator.EstimateCatalogCostUSD(context.Background(), missing); ok {
		t.Fatal("未知模型必须 false")
	}
}

func gatewayquotaCatalogCostInputFixture() gatewayquota.CatalogCostInput {
	return gatewayquota.CatalogCostInput{
		ProviderCode: "openai", SystemAccountID: "sys_owner", Model: "gpt-test",
		InputTokens: 1_000_000, OutputTokens: 500_000,
	}
}

func TestW1PricingConversionHelpers(t *testing.T) {
	// chainHasAnyCostDimension。
	none := gatewayusagePricingCostInput{}
	if chainHasAnyCostDimension(none) {
		t.Fatal("零值必须无维度")
	}
	token := 5
	every := gatewayusagePricingCostInput{
		InputTokens: &token, OutputTokens: &token, CacheReadTokens: &token,
		CacheWriteTokens: &token, CacheWrite1hTokens: &token, InputImageTokens: &token,
		OutputImageTokens: &token, InputAudioTokens: &token, OutputAudioTokens: &token,
		OutputImageCount: &token,
	}
	if !chainHasAnyCostDimension(every) {
		t.Fatal("全维度必须命中")
	}
	// chainIntToFloat。
	if chainIntToFloat(nil) != nil {
		t.Fatal("nil 必须 nil")
	}
	if got := chainIntToFloat(&token); got == nil || *got != 5 {
		t.Fatalf("int→float = %v", got)
	}
	// chainRoundCost：10 位小数舍入。
	if got := chainRoundCost(0.1234567890123); got > 0.1234567891 || got < 0.1234567889 {
		t.Fatalf("round = %v", got)
	}
	if got := chainRoundCost(2.0); got != 2.0 {
		t.Fatalf("整值 = %v", got)
	}
	// chainCatalogPricing：nil 与字段映射。
	if chainCatalogPricing(nil) != nil {
		t.Fatal("nil item 必须 nil")
	}
	release := "2026-01-01"
	item := &gatewayruntimecache.ProviderModelCatalogItem{
		ProviderCode: "openai", Model: "m", Mode: nil, ReleaseDate: &release,
	}
	pricing := chainCatalogPricing(item)
	if pricing.ProviderCode != "openai" || pricing.Model != "m" {
		t.Fatalf("pricing = %+v", pricing)
	}
	// chainServiceTierPrices：空 / 非法 / 正常。
	if chainServiceTierPrices(nil) != nil {
		t.Fatal("nil tier 必须 nil")
	}
	if chainServiceTierPrices(json.RawMessage("bad")) != nil {
		t.Fatal("非法 JSON 必须 nil")
	}
	if chainServiceTierPrices(json.RawMessage(`{}`)) != nil {
		t.Fatal("空对象必须 nil")
	}
	if chainServiceTierPrices(json.RawMessage(`{"priority":{"inputUsdPer1M":1}}`)) == nil {
		t.Fatal("正常 tier 必须解析")
	}
	// findChainCatalogItem：trim 相等。
	items := []gatewayruntimecache.ProviderModelCatalogItem{{Model: "gpt-test"}}
	if findChainCatalogItem(items, " gpt-test ") == nil {
		t.Fatal("trim 匹配失败")
	}
	if findChainCatalogItem(items, "other") != nil {
		t.Fatal("未知模型不得命中")
	}
}
