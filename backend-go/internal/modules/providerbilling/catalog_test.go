package providerbilling

import (
	"math"
	"reflect"
	"testing"
)

func TestBuildCatalogDisplayProviderPolicies(t *testing.T) {
	price := func(value float64) *float64 { return &value }
	tokens := func(value int64) *int64 { return &value }
	facts := CatalogFacts{
		PriceSet: PriceSet{
			InputUSDPer1M: price(1), OutputUSDPer1M: price(2), CachedInputUSDPer1M: price(.2),
			CacheWriteUSDPer1M: price(1.25), CacheWrite1hUSDPer1M: price(2), CacheStorageUSDPer1MPerHour: price(.03),
			ImageInputUSDPer1M: price(3), ImageOutputUSDPer1M: price(4), AudioInputUSDPer1M: price(5), AudioOutputUSDPer1M: price(6), OutputUSDPerImage: price(.04),
		},
		SupportedServiceTiers: []string{"priority"},
		ServiceTierPrices: map[string]PriceSet{"priority": {
			InputUSDPer1M: price(2), CacheStorageUSDPer1MPerHour: price(.06), AudioInputUSDPer1M: price(8),
		}},
		SupportedReasoningEfforts:          []string{"low", "high"},
		ContextWindowTokens:                tokens(128000),
		LongContextInputTokenThreshold:     tokens(100000),
		LongContextInputThresholdInclusive: true,
		LongContextInputCostMultiplier:     price(2),
		SourcePricingCurrency:              "CNY",
		SourceExchangeRateToUSD:            price(.138),
	}

	tests := []struct {
		provider string
		wantKeys []string
	}{
		{"gpt", []string{"token_pricing", "multimodal_pricing", "image_generation", "tier_priority", "long_context", "reasoning", "capacity", "currency_conversion"}},
		{"anthropic", []string{"token_pricing", "tier_priority", "reasoning", "capacity", "currency_conversion"}},
		{"gemini", []string{"token_pricing", "multimodal_pricing", "tier_priority", "long_context", "reasoning", "capacity", "currency_conversion"}},
		{"xai", []string{"token_pricing", "image_generation", "tier_priority", "long_context", "reasoning", "capacity", "currency_conversion"}},
		{"deepseek", []string{"token_pricing", "reasoning", "capacity", "currency_conversion"}},
		{"glm", []string{"token_pricing", "reasoning", "capacity"}},
	}
	for _, test := range tests {
		t.Run(test.provider, func(t *testing.T) {
			got := BuildCatalogDisplay(test.provider, facts)
			keys := make([]string, 0, len(got))
			for _, section := range got {
				keys = append(keys, section.Key)
				if len(section.Items) == 0 {
					t.Fatalf("empty section escaped compaction: %+v", section)
				}
			}
			if !reflect.DeepEqual(keys, test.wantKeys) {
				t.Fatalf("keys = %v, want %v", keys, test.wantKeys)
			}
		})
	}
}

func TestBuildCatalogDisplayTierIncludesCacheStorageAndOmitsAbsentRates(t *testing.T) {
	price := func(value float64) *float64 { return &value }
	got := BuildCatalogDisplay(" OPENAI ", CatalogFacts{
		SupportedServiceTiers: []string{" priority ", "priority", "missing", "empty"},
		ServiceTierPrices: map[string]PriceSet{
			"priority": {InputUSDPer1M: price(0), CacheStorageUSDPer1MPerHour: price(.125)},
			"empty":    {},
		},
	})
	if len(got) != 1 || got[0].Key != "tier_priority" {
		t.Fatalf("sections = %+v, want one normalized priority tier", got)
	}
	want := []CatalogDisplayItem{
		{Key: "input", Label: "输入", Format: FormatUSDPer1MTokens, Value: float64(0)},
		{Key: "cache_storage", Label: "缓存存储", Format: FormatUSDPer1MTokenHour, Value: .125},
	}
	if !reflect.DeepEqual(got[0].Items, want) {
		t.Fatalf("items = %+v, want %+v", got[0].Items, want)
	}
}

func TestBuildCatalogDisplayGLMSinglePriceAndNoData(t *testing.T) {
	price := func(value float64) *float64 { return &value }
	shared := price(.8)
	got := BuildCatalogDisplay("glm", CatalogFacts{PriceSet: PriceSet{InputUSDPer1M: shared, OutputUSDPer1M: price(.8)}})
	if len(got) != 1 || len(got[0].Items) != 1 || got[0].Items[0].Key != "token" {
		t.Fatalf("single-price GLM display = %+v", got)
	}
	if got := BuildCatalogDisplay("gpt", CatalogFacts{}); len(got) != 0 {
		t.Fatalf("empty facts = %+v, want no display", got)
	}
	if got := BuildCatalogDisplay("custom", CatalogFacts{PriceSet: PriceSet{InputUSDPer1M: price(1)}}); got != nil {
		t.Fatalf("unknown provider = %+v, want nil", got)
	}
}

func TestBuildCatalogDisplayRejectsInvalidFacts(t *testing.T) {
	negative := -1.0
	nan := math.NaN()
	inf := math.Inf(1)
	negativeTokens := int64(-1)
	got := BuildCatalogDisplay("gemini", CatalogFacts{
		PriceSet:            PriceSet{InputUSDPer1M: &negative, OutputUSDPer1M: &nan, CacheStorageUSDPer1MPerHour: &inf},
		ContextWindowTokens: &negativeTokens,
	})
	if len(got) != 0 {
		t.Fatalf("invalid facts = %+v, want no display", got)
	}
}

func TestBuildCatalogDisplayXAIImageTitleAndCurrencyText(t *testing.T) {
	price := func(value float64) *float64 { return &value }
	got := BuildCatalogDisplay("xai", CatalogFacts{
		Mode: " image ", PriceSet: PriceSet{InputUSDPer1M: price(3)},
		SourcePricingCurrency: " EUR ", SourceExchangeRateToUSD: price(1.123456789),
	})
	if len(got) != 2 || got[0].Label != "图像输入" || got[1].Key != "currency_conversion" {
		t.Fatalf("display = %+v", got)
	}
	if got[1].Items[1].Value != "$1.12345679" {
		t.Fatalf("exchange value = %#v", got[1].Items[1].Value)
	}
}
