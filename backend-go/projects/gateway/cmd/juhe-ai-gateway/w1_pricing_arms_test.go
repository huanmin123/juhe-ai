package main

// chain_pricing.go 纯定价计算臂的补充单元测试（w1g 前缀，TestW1G 入口）。
// 通过 stub gatewayruntimecache.ReadModels 直接注入 ProviderModelCatalogItem，
// 不依赖数据库；覆盖目录行转换、service tier JSON 反序列化、标量 helper 与
// chainUsagePricingCatalog / chainCostEstimator 的有价/无价/零价/缓存维度分支。

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pricing"
)

// w1gStubCatalogModels 是 ReadModels 的最小 stub：只实现目录读取，
// 其余方法返回零值，缓存语义仍走真实 gatewayruntimecache.Service。
type w1gStubCatalogModels struct {
	mu    sync.Mutex
	items []gatewayruntimecache.ProviderModelCatalogItem
}

func (s *w1gStubCatalogModels) ListProviderModelCatalog(_ context.Context, input gatewayruntimecache.ModelCatalogListOptions) ([]gatewayruntimecache.ProviderModelCatalogItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []gatewayruntimecache.ProviderModelCatalogItem{}
	for _, item := range s.items {
		if input.ProviderCode != "" && item.ProviderCode != input.ProviderCode {
			continue
		}
		if input.SystemAccountID != "" && item.SystemAccountID != nil && *item.SystemAccountID != input.SystemAccountID {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *w1gStubCatalogModels) ReadGatewaySettings(context.Context) (gatewayruntimecache.GatewaySettings, error) {
	return gatewayruntimecache.GatewaySettings{}, nil
}

func (s *w1gStubCatalogModels) ReadGatewayRuntime(context.Context, string) (gatewayruntimecache.GatewayRuntime, error) {
	return gatewayruntimecache.GatewayRuntime{}, nil
}

func (s *w1gStubCatalogModels) ResolveGroupUsageAccessMetadata(context.Context, string, string) (*gatewayruntimecache.GroupUsageAccessMetadata, error) {
	return nil, nil
}

func (s *w1gStubCatalogModels) ListOpenAIAccountsForGroupResult(context.Context, string, string, gatewayruntimecache.OpenAIAccountsForGroupOptions) (gatewayruntimecache.OpenAIAccountsForGroupResult, error) {
	return gatewayruntimecache.OpenAIAccountsForGroupResult{}, nil
}

func (s *w1gStubCatalogModels) ListActiveResponseInspectionPolicies(context.Context, string, string) ([]gatewayruntimecache.ResponseInspectionPolicySummary, error) {
	return nil, nil
}

func (s *w1gStubCatalogModels) LoadAccountCurrentConcurrencyByID(context.Context, []string) (map[string]int, error) {
	return map[string]int{}, nil
}

// w1gNewPricingCache 组装挂了 stub 目录的 runtime cache 服务。
func w1gNewPricingCache(t *testing.T, items ...gatewayruntimecache.ProviderModelCatalogItem) *gatewayruntimecache.Service {
	t.Helper()
	service, err := gatewayruntimecache.New(&w1gStubCatalogModels{items: items}, gatewayruntimecache.Options{})
	if err != nil {
		t.Fatalf("组装 runtime cache: %v", err)
	}
	return service
}

func w1gFloatPtr(value float64) *float64 { return &value }

func w1gIntPtr(value int) *int { return &value }

func w1gInt64Ptr(value int64) *int64 { return &value }

// w1gCatalogItem 构造一个默认有价（input 2.5 / output 10）的目录行。
func w1gCatalogItem(model string, mutate func(*gatewayruntimecache.ProviderModelCatalogItem)) gatewayruntimecache.ProviderModelCatalogItem {
	item := gatewayruntimecache.ProviderModelCatalogItem{
		Scope: "builtin", Status: "active", ProviderCode: "openai", Model: model, Source: "builtin",
		InputUsdPer1M: w1gFloatPtr(2.5), OutputUsdPer1M: w1gFloatPtr(10.0),
	}
	if mutate != nil {
		mutate(&item)
	}
	return item
}

func TestW1GChainServiceTierPrices(t *testing.T) {
	cases := []struct {
		name    string
		raw     json.RawMessage
		wantLen int
	}{
		{"empty raw", nil, 0},
		{"invalid json", json.RawMessage("{bad"), 0},
		{"json null", json.RawMessage("null"), 0},
		{"empty object", json.RawMessage("{}"), 0},
	}
	for _, testCase := range cases {
		if got := chainServiceTierPrices(testCase.raw); len(got) != testCase.wantLen {
			t.Fatalf("%s: chainServiceTierPrices len = %d, want %d", testCase.name, len(got), testCase.wantLen)
		}
	}
	decoded := chainServiceTierPrices(json.RawMessage(`{"priority":{"inputUsdPer1M":4,"outputUsdPer1M":16}}`))
	if len(decoded) != 1 {
		t.Fatalf("decoded len = %d, want 1", len(decoded))
	}
	tier, ok := decoded["priority"]
	if !ok || tier.InputUsdPer1M == nil || *tier.InputUsdPer1M != 4 || tier.OutputUsdPer1M == nil || *tier.OutputUsdPer1M != 16 {
		t.Fatalf("decoded priority = %#v", tier)
	}
}

func TestW1GChainCatalogPricingConversion(t *testing.T) {
	if chainCatalogPricing(nil) != nil {
		t.Fatal("nil item 必须返回 nil")
	}
	inclusive := true
	mode := "chat"
	item := &gatewayruntimecache.ProviderModelCatalogItem{
		ProviderCode: "openai", Model: "gpt-w1g", Mode: &mode,
		InputUsdPer1M:                           w1gFloatPtr(2.5),
		OutputUsdPer1M:                          w1gFloatPtr(10),
		CachedInputUsdPer1M:                     w1gFloatPtr(0.25),
		CacheWriteUsdPer1M:                      w1gFloatPtr(1.25),
		CacheWrite1hUsdPer1M:                    w1gFloatPtr(2),
		CacheStorageUsdPer1MPerHour:             w1gFloatPtr(0.1),
		ImageInputUsdPer1M:                      w1gFloatPtr(3),
		ImageOutputUsdPer1M:                     w1gFloatPtr(12),
		AudioInputUsdPer1M:                      w1gFloatPtr(6),
		AudioOutputUsdPer1M:                     w1gFloatPtr(24),
		OutputUsdPerImage:                       w1gFloatPtr(0.02),
		CachedImageInputUsdPer1M:                w1gFloatPtr(1.5),
		ServiceTierPrices:                       json.RawMessage(`{"priority":{"inputUsdPer1M":4}}`),
		SupportedServiceTiers:                   []string{"priority", "default"},
		LongContextInputTokenThreshold:          w1gInt64Ptr(100000),
		LongContextInputTokenThresholdInclusive: &inclusive,
		LongContextInputCostMultiplier:          w1gFloatPtr(2),
		LongContextOutputCostMultiplier:         w1gFloatPtr(1.5),
	}
	converted := chainCatalogPricing(item)
	if converted.ProviderCode != "openai" || converted.Model != "gpt-w1g" || converted.Mode != "chat" {
		t.Fatalf("identity fields = %#v", converted)
	}
	if converted.InputUsdPer1M == nil || *converted.InputUsdPer1M != 2.5 || converted.OutputUsdPer1M == nil || *converted.OutputUsdPer1M != 10 {
		t.Fatalf("base price = %#v", converted.PriceSet)
	}
	if converted.CachedInputUsdPer1M == nil || *converted.CachedInputUsdPer1M != 0.25 {
		t.Fatalf("cached input = %#v", converted.CachedInputUsdPer1M)
	}
	if converted.CacheWriteUsdPer1M == nil || *converted.CacheWriteUsdPer1M != 1.25 || converted.CacheWrite1hUsdPer1M == nil || *converted.CacheWrite1hUsdPer1M != 2 {
		t.Fatalf("cache write prices = %#v", converted.PriceSet)
	}
	if converted.ImageInputUsdPer1M == nil || converted.AudioOutputUsdPer1M == nil || converted.OutputUsdPerImage == nil {
		t.Fatalf("image/audio prices = %#v", converted.PriceSet)
	}
	if converted.CachedImageInputUsdPer1M == nil || *converted.CachedImageInputUsdPer1M != 1.5 {
		t.Fatalf("cached image input = %#v", converted.CachedImageInputUsdPer1M)
	}
	if len(converted.ServiceTierPrices) != 1 || converted.ServiceTierPrices["priority"].InputUsdPer1M == nil || *converted.ServiceTierPrices["priority"].InputUsdPer1M != 4 {
		t.Fatalf("service tier prices = %#v", converted.ServiceTierPrices)
	}
	if len(converted.SupportedServiceTiers) != 2 {
		t.Fatalf("supported tiers = %#v", converted.SupportedServiceTiers)
	}
	converted.SupportedServiceTiers[0] = "mutated"
	if item.SupportedServiceTiers[0] != "priority" {
		t.Fatalf("目录行 tiers 被就地改写: %#v", item.SupportedServiceTiers)
	}
	if converted.LongContextInputTokenThreshold == nil || *converted.LongContextInputTokenThreshold != 100000 {
		t.Fatalf("long context threshold = %#v", converted.LongContextInputTokenThreshold)
	}
	if !converted.LongContextInputTokenThresholdInclusive {
		t.Fatal("inclusive 必须为 true")
	}
	if converted.LongContextInputCostMultiplier == nil || *converted.LongContextInputCostMultiplier != 2 {
		t.Fatalf("input multiplier = %#v", converted.LongContextInputCostMultiplier)
	}

	minimal := chainCatalogPricing(&gatewayruntimecache.ProviderModelCatalogItem{ProviderCode: "p", Model: "m"})
	if minimal.Mode != "" || minimal.LongContextInputTokenThreshold != nil || minimal.LongContextInputTokenThresholdInclusive {
		t.Fatalf("缺省字段 = %#v", minimal)
	}
	if len(minimal.SupportedServiceTiers) != 0 {
		t.Fatalf("缺省 tiers = %#v", minimal.SupportedServiceTiers)
	}
}

func TestW1GChainPricingScalarHelpers(t *testing.T) {
	t.Run("chainIntToFloat", func(t *testing.T) {
		if chainIntToFloat(nil) != nil {
			t.Fatal("nil 必须返回 nil")
		}
		value := chainIntToFloat(w1gIntPtr(7))
		if value == nil || *value != 7 {
			t.Fatalf("chainIntToFloat = %#v", value)
		}
	})

	t.Run("chainInt64ToInt", func(t *testing.T) {
		if chainInt64ToInt(nil) != nil {
			t.Fatal("nil 必须返回 nil")
		}
		if got := chainInt64ToInt(w1gInt64Ptr(300)); got == nil || *got != 300 {
			t.Fatalf("chainInt64ToInt = %#v", got)
		}
	})

	t.Run("chainDerefString", func(t *testing.T) {
		if chainDerefString(nil) != "" {
			t.Fatal("nil 必须返回空串")
		}
		if got := chainDerefString(w1gStrPtr("x")); got != "x" {
			t.Fatalf("chainDerefString = %q", got)
		}
	})

	t.Run("chainTokensFloat", func(t *testing.T) {
		value := chainTokensFloat(3.5)
		if value == nil || *value != 3.5 {
			t.Fatalf("chainTokensFloat = %#v", value)
		}
	})

	t.Run("chainRoundCost", func(t *testing.T) {
		if got := chainRoundCost(1.0 / 3.0); got != 0.3333333333 {
			t.Fatalf("chainRoundCost = %v, want 0.3333333333", got)
		}
		if got := chainRoundCost(2.0); got != 2.0 {
			t.Fatalf("chainRoundCost 整数 = %v", got)
		}
	})

	t.Run("findChainCatalogItem", func(t *testing.T) {
		items := []gatewayruntimecache.ProviderModelCatalogItem{
			w1gCatalogItem(" padded ", nil),
			w1gCatalogItem("other", nil),
		}
		found := findChainCatalogItem(items, "padded")
		if found == nil || found.Model != " padded " {
			t.Fatalf("findChainCatalogItem = %#v", found)
		}
		if findChainCatalogItem(items, "missing") != nil {
			t.Fatal("未命中必须返回 nil")
		}
		if findChainCatalogItem(nil, "missing") != nil {
			t.Fatal("空目录必须返回 nil")
		}
	})
}

func TestW1GChainHasAnyCostDimension(t *testing.T) {
	empty := gatewayusage.PricingCostInput{}
	if chainHasAnyCostDimension(empty) {
		t.Fatal("全空输入必须返回 false")
	}
	cases := map[string]func(*gatewayusage.PricingCostInput){
		"input":        func(in *gatewayusage.PricingCostInput) { in.InputTokens = w1gIntPtr(1) },
		"output":       func(in *gatewayusage.PricingCostInput) { in.OutputTokens = w1gIntPtr(1) },
		"cacheRead":    func(in *gatewayusage.PricingCostInput) { in.CacheReadTokens = w1gIntPtr(1) },
		"cacheWrite":   func(in *gatewayusage.PricingCostInput) { in.CacheWriteTokens = w1gIntPtr(1) },
		"cacheWrite1h": func(in *gatewayusage.PricingCostInput) { in.CacheWrite1hTokens = w1gIntPtr(1) },
		"inputImage":   func(in *gatewayusage.PricingCostInput) { in.InputImageTokens = w1gIntPtr(1) },
		"outputImage":  func(in *gatewayusage.PricingCostInput) { in.OutputImageTokens = w1gIntPtr(1) },
		"inputAudio":   func(in *gatewayusage.PricingCostInput) { in.InputAudioTokens = w1gIntPtr(1) },
		"outputAudio":  func(in *gatewayusage.PricingCostInput) { in.OutputAudioTokens = w1gIntPtr(1) },
		"outputImageN": func(in *gatewayusage.PricingCostInput) { in.OutputImageCount = w1gIntPtr(1) },
	}
	for name, fill := range cases {
		input := gatewayusage.PricingCostInput{}
		fill(&input)
		if !chainHasAnyCostDimension(input) {
			t.Fatalf("%s 维度必须被识别", name)
		}
	}
}

func TestW1GChainPricingCostInputConversion(t *testing.T) {
	source := gatewayusage.PricingCostInput{
		ProviderCode: "openai", SystemAccountID: "sys-1", Model: "gpt-w1g", ServiceTier: "priority",
		InputTokens: w1gIntPtr(1), OutputTokens: w1gIntPtr(2), CacheReadTokens: w1gIntPtr(3),
		CacheWriteTokens: w1gIntPtr(4), CacheWrite1hTokens: w1gIntPtr(5), ThinkingTokens: w1gIntPtr(6),
		InputImageTokens: w1gIntPtr(7), OutputImageTokens: w1gIntPtr(8), InputAudioTokens: w1gIntPtr(9),
		OutputAudioTokens: w1gIntPtr(10), OutputImageCount: w1gIntPtr(11),
	}
	converted := chainPricingCostInput(&pricing.Pricing{ProviderCode: "openai", Model: "gpt-w1g"}, source)
	if converted.ServiceTier != "priority" {
		t.Fatalf("service tier = %q", converted.ServiceTier)
	}
	values := []*float64{
		converted.InputTokens, converted.OutputTokens, converted.CacheReadTokens,
		converted.CacheWriteTokens, converted.CacheWrite1hTokens, converted.ThinkingTokens,
		converted.InputImageTokens, converted.OutputImageTokens, converted.InputAudioTokens,
		converted.OutputAudioTokens, converted.OutputImageCount,
	}
	for index, value := range values {
		if value == nil {
			t.Fatalf("第 %d 个 token 维度必须转换", index)
		}
	}
	if converted.InputTokens == nil || *converted.InputTokens != 1 || converted.OutputImageCount == nil || *converted.OutputImageCount != 11 {
		t.Fatalf("转换值错误: %#v", converted)
	}

	nilInput := chainPricingCostInput(&pricing.Pricing{}, gatewayusage.PricingCostInput{})
	if nilInput.InputTokens != nil || nilInput.OutputTokens != nil {
		t.Fatalf("nil 维度必须保持 nil: %#v", nilInput)
	}
}

func TestW1GChainUsagePricingCatalogNilArms(t *testing.T) {
	var nilCatalog *chainUsagePricingCatalog
	if got := nilCatalog.ResolvePricingModel("openai", "sys", "m"); got != "" {
		t.Fatalf("nil receiver ResolvePricingModel = %q", got)
	}
	if got := nilCatalog.EstimateCost(gatewayusage.PricingCostInput{Model: "m"}); got != nil {
		t.Fatalf("nil receiver EstimateCost = %#v", got)
	}
	if got := nilCatalog.EstimateCacheReadCost(gatewayusage.PricingCostInput{Model: "m", CacheReadTokens: w1gIntPtr(1)}); got != nil {
		t.Fatalf("nil receiver EstimateCacheReadCost = %#v", got)
	}
	if got := nilCatalog.EstimateCacheWriteCost(gatewayusage.PricingCostInput{Model: "m", CacheWriteTokens: w1gIntPtr(1)}); got != nil {
		t.Fatalf("nil receiver EstimateCacheWriteCost = %#v", got)
	}

	noCache := &chainUsagePricingCatalog{}
	if got := noCache.ResolvePricingModel("openai", "sys", "m"); got != "" {
		t.Fatalf("nil cache ResolvePricingModel = %q", got)
	}
	if got := noCache.EstimateCost(gatewayusage.PricingCostInput{Model: "m", InputTokens: w1gIntPtr(1)}); got != nil {
		t.Fatalf("nil cache EstimateCost = %#v", got)
	}
	if got := noCache.EstimateCacheWriteCost(gatewayusage.PricingCostInput{Model: "m", CacheWriteTokens: w1gIntPtr(1)}); got != nil {
		t.Fatalf("nil cache EstimateCacheWriteCost = %#v", got)
	}

	cache := w1gNewPricingCache(t, w1gCatalogItem("gpt-w1g", nil))
	catalog := newChainUsagePricingCatalog(cache)
	if got := catalog.ResolvePricingModel("openai", "sys-1", "   "); got != "" {
		t.Fatalf("空白模型 = %q, want 空", got)
	}
	if got := catalog.ResolvePricingModel("openai", "sys-1", "missing"); got != "" {
		t.Fatalf("未知模型 = %q, want 空", got)
	}
	if got := catalog.ResolvePricingModel("openai", "sys-1", " gpt-w1g "); got != "gpt-w1g" {
		t.Fatalf("trim 命中 = %q, want gpt-w1g", got)
	}
	if got := catalog.EstimateCost(gatewayusage.PricingCostInput{ProviderCode: "openai", Model: "gpt-w1g", ServiceTier: "default"}); got != nil {
		t.Fatalf("无成本维度必须返回 nil, got %#v", got)
	}
	if got := catalog.EstimateCacheReadCost(gatewayusage.PricingCostInput{ProviderCode: "openai", Model: "gpt-w1g"}); got != nil {
		t.Fatalf("无 cacheReadTokens 必须返回 nil, got %#v", got)
	}
	if got := catalog.EstimateCacheWriteCost(gatewayusage.PricingCostInput{ProviderCode: "openai", Model: "gpt-w1g"}); got != nil {
		t.Fatalf("无 cacheWriteTokens 必须返回 nil, got %#v", got)
	}
}

func TestW1GChainUsagePricingCatalogEstimateArms(t *testing.T) {
	systemAccount := "sys-w1g"
	cache := w1gNewPricingCache(t,
		w1gCatalogItem("gpt-w1g", nil),
		w1gCatalogItem("free-w1g", func(item *gatewayruntimecache.ProviderModelCatalogItem) {
			item.InputUsdPer1M = w1gFloatPtr(0)
			item.OutputUsdPer1M = w1gFloatPtr(0)
		}),
		w1gCatalogItem("unpriced-w1g", func(item *gatewayruntimecache.ProviderModelCatalogItem) {
			item.InputUsdPer1M = nil
			item.OutputUsdPer1M = nil
		}),
		w1gCatalogItem("cache-w1g", func(item *gatewayruntimecache.ProviderModelCatalogItem) {
			item.InputUsdPer1M = w1gFloatPtr(2)
			item.CachedInputUsdPer1M = w1gFloatPtr(0.5)
			item.CacheWriteUsdPer1M = w1gFloatPtr(1.5)
			item.CacheWrite1hUsdPer1M = w1gFloatPtr(3)
		}),
	)
	catalog := newChainUsagePricingCatalog(cache)

	t.Run("estimate cost standard rates", func(t *testing.T) {
		cost := catalog.EstimateCost(gatewayusage.PricingCostInput{
			ProviderCode: "openai", SystemAccountID: systemAccount, Model: "gpt-w1g",
			InputTokens: w1gIntPtr(10000), OutputTokens: w1gIntPtr(10000),
		})
		if cost == nil || absFloat64(*cost-0.125) > 1e-9 {
			t.Fatalf("EstimateCost = %#v, want 0.125", cost)
		}
	})

	t.Run("explicit zero price is a real free price", func(t *testing.T) {
		cost := catalog.EstimateCost(gatewayusage.PricingCostInput{
			ProviderCode: "openai", SystemAccountID: systemAccount, Model: "free-w1g",
			InputTokens: w1gIntPtr(5000), OutputTokens: w1gIntPtr(5000),
		})
		if cost == nil || *cost != 0 {
			t.Fatalf("零价 EstimateCost = %#v, want &0", cost)
		}
	})

	t.Run("unpriced row yields no estimate", func(t *testing.T) {
		cost := catalog.EstimateCost(gatewayusage.PricingCostInput{
			ProviderCode: "openai", SystemAccountID: systemAccount, Model: "unpriced-w1g",
			InputTokens: w1gIntPtr(5000), OutputTokens: w1gIntPtr(5000),
		})
		if cost != nil {
			t.Fatalf("无价 EstimateCost = %#v, want nil", cost)
		}
	})

	t.Run("unknown model yields no estimate", func(t *testing.T) {
		cost := catalog.EstimateCost(gatewayusage.PricingCostInput{
			ProviderCode: "openai", SystemAccountID: systemAccount, Model: "missing-w1g",
			InputTokens: w1gIntPtr(1),
		})
		if cost != nil {
			t.Fatalf("未知模型 EstimateCost = %#v, want nil", cost)
		}
	})

	t.Run("cache read cost", func(t *testing.T) {
		cost := catalog.EstimateCacheReadCost(gatewayusage.PricingCostInput{
			ProviderCode: "openai", SystemAccountID: systemAccount, Model: "cache-w1g",
			CacheReadTokens: w1gIntPtr(20000),
		})
		if cost == nil || absFloat64(*cost-0.01) > 1e-9 {
			t.Fatalf("EstimateCacheReadCost = %#v, want 0.01", cost)
		}
	})

	t.Run("cache read without rate yields nil", func(t *testing.T) {
		cost := catalog.EstimateCacheReadCost(gatewayusage.PricingCostInput{
			ProviderCode: "openai", SystemAccountID: systemAccount, Model: "unpriced-w1g",
			CacheReadTokens: w1gIntPtr(20000),
		})
		if cost != nil {
			t.Fatalf("缓存价缺失 EstimateCacheReadCost = %#v, want nil", cost)
		}
	})

	t.Run("cache write combines standard and 1h", func(t *testing.T) {
		// 引擎把 CacheWriteTokens 视为含 1h 部分的总量：1h 10000 全部按
		// 1h 价（3/1M）计，标准段 0 token，总成本 0.03。
		cost := catalog.EstimateCacheWriteCost(gatewayusage.PricingCostInput{
			ProviderCode: "openai", SystemAccountID: systemAccount, Model: "cache-w1g",
			CacheWriteTokens: w1gIntPtr(10000), CacheWrite1hTokens: w1gIntPtr(10000),
		})
		if cost == nil || absFloat64(*cost-0.03) > 1e-9 {
			t.Fatalf("EstimateCacheWriteCost = %#v, want 0.03", cost)
		}
	})

	t.Run("cache write zero tokens with rate yields zero", func(t *testing.T) {
		cost := catalog.EstimateCacheWriteCost(gatewayusage.PricingCostInput{
			ProviderCode: "openai", SystemAccountID: systemAccount, Model: "cache-w1g",
			CacheWriteTokens: w1gIntPtr(0),
		})
		if cost == nil || *cost != 0 {
			t.Fatalf("零 token EstimateCacheWriteCost = %#v, want &0", cost)
		}
	})

	t.Run("cache write without rate yields nil", func(t *testing.T) {
		cost := catalog.EstimateCacheWriteCost(gatewayusage.PricingCostInput{
			ProviderCode: "openai", SystemAccountID: systemAccount, Model: "unpriced-w1g",
			CacheWriteTokens: w1gIntPtr(10000),
		})
		if cost != nil {
			t.Fatalf("无写入价 EstimateCacheWriteCost = %#v, want nil", cost)
		}
	})
}

func TestW1GChainCostEstimatorArms(t *testing.T) {
	var nilEstimator *chainCostEstimator
	if _, ok := nilEstimator.EstimateCatalogCostUSD(context.Background(), gatewayquotaCatalogInput("m")); ok {
		t.Fatal("nil receiver 必须返回未定义估计")
	}
	if _, ok := (&chainCostEstimator{}).EstimateCatalogCostUSD(context.Background(), gatewayquotaCatalogInput("m")); ok {
		t.Fatal("nil cache 必须返回未定义估计")
	}

	cache := w1gNewPricingCache(t,
		w1gCatalogItem("gpt-w1g", nil),
		w1gCatalogItem("unpriced-w1g", func(item *gatewayruntimecache.ProviderModelCatalogItem) {
			item.InputUsdPer1M = nil
			item.OutputUsdPer1M = nil
		}),
	)
	estimator := newChainCostEstimator(cache)
	ctx := context.Background()

	if _, ok := estimator.EstimateCatalogCostUSD(ctx, gatewayquota.CatalogCostInput{ProviderCode: "openai", Model: "   "}); ok {
		t.Fatal("空白模型必须返回未定义估计")
	}
	if _, ok := estimator.EstimateCatalogCostUSD(ctx, gatewayquota.CatalogCostInput{ProviderCode: "openai", Model: "missing"}); ok {
		t.Fatal("未知模型必须返回未定义估计")
	}
	if _, ok := estimator.EstimateCatalogCostUSD(ctx, gatewayquota.CatalogCostInput{ProviderCode: "openai", Model: "unpriced-w1g", InputTokens: 1000, OutputTokens: 1000}); ok {
		t.Fatal("无价目录行必须返回未定义估计")
	}
	if resolved := estimator.resolveCatalogPricing(ctx, "openai", "sys-w1g", "missing"); resolved != nil {
		t.Fatalf("未知模型 resolveCatalogPricing = %#v, want nil", resolved)
	}
	resolved := estimator.resolveCatalogPricing(ctx, "openai", "sys-w1g", " gpt-w1g ")
	if resolved == nil || resolved.Model != "gpt-w1g" || resolved.ProviderCode != "openai" {
		t.Fatalf("resolveCatalogPricing = %#v", resolved)
	}
	cost, ok := estimator.EstimateCatalogCostUSD(ctx, gatewayquota.CatalogCostInput{
		ProviderCode: "openai", SystemAccountID: "sys-w1g", Model: "gpt-w1g",
		ServiceTier: "default", InputTokens: 20000, OutputTokens: 10000,
	})
	wantCost := 20000*2.5/1_000_000 + 10000*10.0/1_000_000
	if !ok || absFloat64(cost-wantCost) > 1e-9 {
		t.Fatalf("EstimateCatalogCostUSD = %v/%v, want %v", cost, ok, wantCost)
	}
}

func gatewayquotaCatalogInput(model string) gatewayquota.CatalogCostInput {
	return gatewayquota.CatalogCostInput{ProviderCode: "openai", Model: model, InputTokens: 1, OutputTokens: 1}
}
