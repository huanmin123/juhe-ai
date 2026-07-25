package providerbilling

import (
	"math"
	"reflect"
	"strings"
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
		CachedImageInputUSDPer1M: price(.75),
		SupportedServiceTiers:    []string{"priority"},
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
		{"gpt", []string{"token_pricing", "image_generation", "tier_priority", "long_context", "reasoning", "capacity", "currency_conversion"}},
		{"anthropic", []string{"token_pricing", "tier_priority", "reasoning", "capacity", "currency_conversion"}},
		{"gemini", []string{"token_pricing", "tier_priority", "reasoning", "capacity", "currency_conversion"}},
		{"xai", []string{"token_pricing", "image_generation", "tier_priority", "reasoning", "capacity", "currency_conversion"}},
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

func TestBuildCatalogDisplayOpenAIMergesTokenModalities(t *testing.T) {
	price := func(value float64) *float64 { return &value }
	got := BuildCatalogDisplay("gpt", CatalogFacts{
		PriceSet: PriceSet{
			InputUSDPer1M:       price(5),
			CachedInputUSDPer1M: price(1.25),
			CacheWriteUSDPer1M:  price(6.25),
			OutputUSDPer1M:      price(30),
			ImageInputUSDPer1M:  price(8),
			ImageOutputUSDPer1M: price(30),
		},
		CachedImageInputUSDPer1M: price(2),
	})
	want := []CatalogDisplayItem{
		{Key: "input", Label: "文本输入", Format: FormatUSDPer1MTokens, Value: float64(5)},
		{Key: "cache_read", Label: "文本缓存读", Format: FormatUSDPer1MTokens, Value: 1.25},
		{Key: "cache_write", Label: "文本缓存写入", Format: FormatUSDPer1MTokens, Value: 6.25},
		{Key: "output", Label: "文本输出", Format: FormatUSDPer1MTokens, Value: float64(30)},
		{Key: "image_input", Label: "图片输入", Format: FormatUSDPer1MTokens, Value: float64(8)},
		{Key: "image_cache_read", Label: "图片缓存读", Format: FormatUSDPer1MTokens, Value: float64(2)},
		{Key: "image_output", Label: "图片输出", Format: FormatUSDPer1MTokens, Value: float64(30)},
	}
	if len(got) != 1 || got[0].Key != "token_pricing" || !reflect.DeepEqual(got[0].Items, want) {
		t.Fatalf("OpenAI display = %+v, want merged token pricing %+v", got, want)
	}
}

func TestBuildCatalogDisplayGeminiAndXAILongContextPricesAreDirectRows(t *testing.T) {
	price := func(value float64) *float64 { return &value }
	tokens := func(value int64) *int64 { return &value }
	tests := []struct {
		name     string
		provider string
		facts    CatalogFacts
		want     []CatalogDisplayItem
	}{
		{
			name:     "gemini",
			provider: "gemini",
			facts: CatalogFacts{
				PriceSet: PriceSet{
					InputUSDPer1M: price(1.25), OutputUSDPer1M: price(10), CachedInputUSDPer1M: price(.125),
					CacheStorageUSDPer1MPerHour: price(4.5),
				},
				LongContextInputTokenThreshold: tokens(200000),
				LongContextInputCostMultiplier: price(2), LongContextOutputCostMultiplier: price(1.5),
			},
			want: []CatalogDisplayItem{
				{Key: "input", Label: "输入", Format: FormatUSDPer1MTokens, Value: 1.25},
				{Key: "output", Label: "输出", Format: FormatUSDPer1MTokens, Value: float64(10)},
				{Key: "cache_read", Label: "缓存读", Format: FormatUSDPer1MTokens, Value: .125},
				{Key: "cache_storage", Label: "缓存存储", Format: FormatUSDPer1MTokenHour, Value: 4.5},
				{Key: "long_context_input", Label: "长上下文（> 200K）输入", Format: FormatUSDPer1MTokens, Value: 2.5},
				{Key: "long_context_cache_read", Label: "长上下文（> 200K）缓存读", Format: FormatUSDPer1MTokens, Value: .25},
				{Key: "long_context_output", Label: "长上下文（> 200K）输出", Format: FormatUSDPer1MTokens, Value: float64(15)},
			},
		},
		{
			name:     "xai",
			provider: "xai",
			facts: CatalogFacts{
				PriceSet:                       PriceSet{InputUSDPer1M: price(2), CachedInputUSDPer1M: price(.3), OutputUSDPer1M: price(6)},
				LongContextInputTokenThreshold: tokens(200000), LongContextInputThresholdInclusive: true,
				LongContextInputCostMultiplier: price(2), LongContextOutputCostMultiplier: price(2),
			},
			want: []CatalogDisplayItem{
				{Key: "input", Label: "输入", Format: FormatUSDPer1MTokens, Value: float64(2)},
				{Key: "cache_read", Label: "缓存读", Format: FormatUSDPer1MTokens, Value: .3},
				{Key: "output", Label: "输出", Format: FormatUSDPer1MTokens, Value: float64(6)},
				{Key: "long_context_input", Label: "长上下文（>= 200K）输入", Format: FormatUSDPer1MTokens, Value: float64(4)},
				{Key: "long_context_cache_read", Label: "长上下文（>= 200K）缓存读", Format: FormatUSDPer1MTokens, Value: .6},
				{Key: "long_context_output", Label: "长上下文（>= 200K）输出", Format: FormatUSDPer1MTokens, Value: float64(12)},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := BuildCatalogDisplay(test.provider, test.facts)
			if len(got) != 1 || got[0].Key != "token_pricing" || !reflect.DeepEqual(got[0].Items, test.want) {
				t.Fatalf("display = %+v, want token pricing %+v", got, test.want)
			}
		})
	}
}

func TestBuildCatalogDisplayNormalizesLabelsReasoningAndTierOrder(t *testing.T) {
	price := func(value float64) *float64 { return &value }
	got := BuildCatalogDisplay("openai", CatalogFacts{
		SupportedServiceTiers: []string{"priority"},
		ServiceTierPrices: map[string]PriceSet{"priority": {
			InputUSDPer1M: price(1), CachedInputUSDPer1M: price(.1), CacheWriteUSDPer1M: price(1.25),
			CacheWrite1hUSDPer1M: price(2), CacheStorageUSDPer1MPerHour: price(.03), OutputUSDPer1M: price(6), AudioInputUSDPer1M: price(4),
		}},
		SupportedReasoningEfforts: []string{"none", " low ", "none", "high", "low"},
	})
	if len(got) != 2 {
		t.Fatalf("sections = %+v, want tier and reasoning", got)
	}
	wantTier := []CatalogDisplayItem{
		{Key: "input", Label: "输入", Format: FormatUSDPer1MTokens, Value: float64(1)},
		{Key: "cache_read", Label: "缓存读", Format: FormatUSDPer1MTokens, Value: .1},
		{Key: "cache_write", Label: "缓存写入", Format: FormatUSDPer1MTokens, Value: 1.25},
		{Key: "cache_write_1h", Label: "1h 缓存写入", Format: FormatUSDPer1MTokens, Value: float64(2)},
		{Key: "cache_storage", Label: "缓存存储", Format: FormatUSDPer1MTokenHour, Value: .03},
		{Key: "output", Label: "输出", Format: FormatUSDPer1MTokens, Value: float64(6)},
		{Key: "audio_input", Label: "音频输入", Format: FormatUSDPer1MTokens, Value: float64(4)},
	}
	if got[0].Key != "tier_priority" || !reflect.DeepEqual(got[0].Items, wantTier) {
		t.Fatalf("tier = %+v, want %+v", got[0], wantTier)
	}
	if got[1].Key != "reasoning" || got[1].Items[0].Value != "low / high" {
		t.Fatalf("reasoning = %+v, want none removed and values deduplicated", got[1])
	}

	deepSeek := BuildCatalogDisplay("deepseek", CatalogFacts{PriceSet: PriceSet{InputUSDPer1M: price(1), CachedInputUSDPer1M: price(.1)}})
	if gotLabels := []string{deepSeek[0].Items[0].Label, deepSeek[0].Items[1].Label}; !reflect.DeepEqual(gotLabels, []string{"输入", "缓存读"}) {
		t.Fatalf("DeepSeek labels = %v", gotLabels)
	}
	glm := BuildCatalogDisplay("glm", CatalogFacts{PriceSet: PriceSet{InputUSDPer1M: price(1), CachedInputUSDPer1M: price(.1)}})
	if glm[0].Items[1].Label != "缓存读" {
		t.Fatalf("GLM cache label = %q", glm[0].Items[1].Label)
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
	one := 1.0
	negativeTokens := int64(-1)
	got := BuildCatalogDisplay("gemini", CatalogFacts{
		PriceSet:            PriceSet{InputUSDPer1M: &negative, OutputUSDPer1M: &nan, CacheStorageUSDPer1MPerHour: &inf},
		ContextWindowTokens: &negativeTokens,
	})
	if len(got) != 0 {
		t.Fatalf("invalid facts = %+v, want no display", got)
	}
	for _, invalid := range []*float64{&negative, &nan, &inf} {
		got := BuildCatalogDisplay("openai", CatalogFacts{
			PriceSet:                 PriceSet{InputUSDPer1M: &one},
			CachedImageInputUSDPer1M: invalid,
		})
		if len(got) != 1 || len(got[0].Items) != 1 || got[0].Items[0].Label != "输入" {
			t.Fatalf("invalid cached image price escaped fail-closed: %+v", got)
		}
	}
}

func TestBuildCatalogDisplayFallsBackForInvalidLongContextMultipliers(t *testing.T) {
	price := func(value float64) *float64 { return &value }
	tokens := int64(200000)
	invalid := []float64{-1, 0, math.NaN(), math.Inf(1)}
	for _, multiplier := range invalid {
		got := BuildCatalogDisplay("xai", CatalogFacts{
			PriceSet:                       PriceSet{InputUSDPer1M: price(2), OutputUSDPer1M: price(6)},
			LongContextInputTokenThreshold: &tokens,
			LongContextInputCostMultiplier: &multiplier, LongContextOutputCostMultiplier: &multiplier,
		})
		if len(got) != 1 || len(got[0].Items) != 4 {
			t.Fatalf("multiplier %v did not retain base-price fallback rows: %+v", multiplier, got)
		}
		if got[0].Items[2].Key != "long_context_input" || got[0].Items[2].Value != float64(2) ||
			got[0].Items[3].Key != "long_context_output" || got[0].Items[3].Value != float64(6) {
			t.Fatalf("multiplier %v fallback = %+v, want base input/output prices", multiplier, got[0].Items[2:])
		}
	}
}

func TestBuildCatalogDisplayOmitsMissingOrNegativeLongContextThreshold(t *testing.T) {
	price := func(value float64) *float64 { return &value }
	negativeThreshold := int64(-1)
	for _, threshold := range []*int64{nil, &negativeThreshold} {
		got := BuildCatalogDisplay("gemini", CatalogFacts{
			PriceSet:                       PriceSet{InputUSDPer1M: price(2)},
			LongContextInputTokenThreshold: threshold,
			LongContextInputCostMultiplier: price(2),
		})
		if len(got) != 1 || len(got[0].Items) != 1 || strings.HasPrefix(got[0].Items[0].Key, "long_context_") {
			t.Fatalf("threshold %v produced long-context rows: %+v", threshold, got)
		}
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
