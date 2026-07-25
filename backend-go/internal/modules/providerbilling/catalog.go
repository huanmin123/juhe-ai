package providerbilling

import (
	"math"
	"strconv"
	"strings"
)

type CatalogDisplayFormat string

const (
	FormatUSDPer1MTokens    CatalogDisplayFormat = "usd_per_1m_tokens"
	FormatUSDPerImage       CatalogDisplayFormat = "usd_per_image"
	FormatUSDPer1MTokenHour CatalogDisplayFormat = "usd_per_1m_token_hour"
	FormatTokens            CatalogDisplayFormat = "tokens"
	FormatMultiplier        CatalogDisplayFormat = "multiplier"
	FormatText              CatalogDisplayFormat = "text"
)

type CatalogDisplayItem struct {
	Key    string               `json:"key"`
	Label  string               `json:"label"`
	Format CatalogDisplayFormat `json:"format"`
	Value  any                  `json:"value"`
}

type CatalogDisplaySection struct {
	Key   string               `json:"key"`
	Label string               `json:"label"`
	Items []CatalogDisplayItem `json:"items"`
}

// PriceSet contains only catalog-facing rates. Pointers distinguish a real zero
// price from a rate the provider has not published.
type PriceSet struct {
	InputUSDPer1M               *float64
	OutputUSDPer1M              *float64
	CachedInputUSDPer1M         *float64
	CacheWriteUSDPer1M          *float64
	CacheWrite1hUSDPer1M        *float64
	CacheStorageUSDPer1MPerHour *float64
	ImageInputUSDPer1M          *float64
	ImageOutputUSDPer1M         *float64
	AudioInputUSDPer1M          *float64
	AudioOutputUSDPer1M         *float64
	OutputUSDPerImage           *float64
}

type CatalogFacts struct {
	PriceSet
	Mode                               string
	ServiceTierPrices                  map[string]PriceSet
	SupportedServiceTiers              []string
	SupportedReasoningEfforts          []string
	ContextWindowTokens                *int64
	MaxInputTokens                     *int64
	MaxOutputTokens                    *int64
	LongContextInputTokenThreshold     *int64
	LongContextInputThresholdInclusive bool
	LongContextInputCostMultiplier     *float64
	LongContextOutputCostMultiplier    *float64
	SourcePricingCurrency              string
	SourceExchangeRateToUSD            *float64
	SourceExchangeRateDate             string
	SourcePricingNote                  string
}

// BuildCatalogDisplay returns the provider-specific display contract. It does
// not calculate charges and intentionally returns no display for unknown owners.
func BuildCatalogDisplay(providerCode string, facts CatalogFacts) []CatalogDisplaySection {
	provider := strings.ToLower(strings.TrimSpace(providerCode))
	var sections []CatalogDisplaySection
	switch provider {
	case "openai", "gpt":
		sections = append(sections,
			priceSection("token_pricing", "Token 计费",
				price("input", "输入", facts.InputUSDPer1M, FormatUSDPer1MTokens),
				price("output", "输出", facts.OutputUSDPer1M, FormatUSDPer1MTokens),
				price("cache_read", "缓存命中", facts.CachedInputUSDPer1M, FormatUSDPer1MTokens)),
			mediaSection(facts.PriceSet), imageUnitSection(facts.PriceSet))
		sections = appendCommon(sections, facts, true, true)
	case "anthropic":
		sections = append(sections, priceSection("token_pricing", "Token 计费",
			price("input", "输入", facts.InputUSDPer1M, FormatUSDPer1MTokens),
			price("output", "输出", facts.OutputUSDPer1M, FormatUSDPer1MTokens),
			price("cache_read", "缓存读取", facts.CachedInputUSDPer1M, FormatUSDPer1MTokens),
			price("cache_write_5m", "5m 缓存写入", facts.CacheWriteUSDPer1M, FormatUSDPer1MTokens),
			price("cache_write_1h", "1h 缓存写入", facts.CacheWrite1hUSDPer1M, FormatUSDPer1MTokens)))
		sections = appendCommon(sections, facts, false, true)
	case "gemini":
		sections = append(sections,
			priceSection("token_pricing", "Token 计费",
				price("input", "输入", facts.InputUSDPer1M, FormatUSDPer1MTokens),
				price("output", "输出", facts.OutputUSDPer1M, FormatUSDPer1MTokens),
				price("cache_read", "缓存命中", facts.CachedInputUSDPer1M, FormatUSDPer1MTokens),
				price("cache_storage", "缓存存储", facts.CacheStorageUSDPer1MPerHour, FormatUSDPer1MTokenHour)),
			mediaSection(facts.PriceSet))
		sections = appendCommon(sections, facts, true, true)
	case "xai":
		title := "Token 计费"
		if strings.EqualFold(strings.TrimSpace(facts.Mode), "image") {
			title = "图像输入"
		}
		sections = append(sections,
			priceSection("token_pricing", title,
				price("input", "输入", facts.InputUSDPer1M, FormatUSDPer1MTokens),
				price("output", "输出", facts.OutputUSDPer1M, FormatUSDPer1MTokens),
				price("cache_read", "缓存命中", facts.CachedInputUSDPer1M, FormatUSDPer1MTokens)),
			imageUnitSection(facts.PriceSet))
		sections = appendCommon(sections, facts, true, true)
	case "deepseek":
		sections = append(sections, priceSection("token_pricing", "Token 计费",
			price("cache_miss", "Cache miss", facts.InputUSDPer1M, FormatUSDPer1MTokens),
			price("cache_hit", "Cache hit", facts.CachedInputUSDPer1M, FormatUSDPer1MTokens),
			price("output", "输出", facts.OutputUSDPer1M, FormatUSDPer1MTokens)))
		sections = append(sections, reasoningSection(facts), capacitySection(facts), sourceConversionSection(facts))
	case "glm":
		if validPrice(facts.InputUSDPer1M) && validPrice(facts.OutputUSDPer1M) && *facts.InputUSDPer1M == *facts.OutputUSDPer1M && facts.CachedInputUSDPer1M == nil {
			sections = append(sections, priceSection("token_pricing", "Token 计费", price("token", "Token", facts.InputUSDPer1M, FormatUSDPer1MTokens)))
		} else {
			sections = append(sections, priceSection("token_pricing", "Token 计费",
				price("input", "输入", facts.InputUSDPer1M, FormatUSDPer1MTokens),
				price("cache_hit", "缓存命中", facts.CachedInputUSDPer1M, FormatUSDPer1MTokens),
				price("output", "输出", facts.OutputUSDPer1M, FormatUSDPer1MTokens)))
		}
		sections = append(sections, reasoningSection(facts), capacitySection(facts))
	default:
		return nil
	}
	return compact(sections)
}

func appendCommon(sections []CatalogDisplaySection, facts CatalogFacts, longContext, conversion bool) []CatalogDisplaySection {
	sections = append(sections, serviceTierSections(facts)...)
	if longContext {
		sections = append(sections, longContextSection(facts))
	}
	sections = append(sections, reasoningSection(facts), capacitySection(facts))
	if conversion {
		sections = append(sections, sourceConversionSection(facts))
	}
	return sections
}

type itemCandidate struct {
	item CatalogDisplayItem
	ok   bool
}

func price(key, label string, value *float64, format CatalogDisplayFormat) itemCandidate {
	if !validPrice(value) {
		return itemCandidate{}
	}
	return itemCandidate{item: CatalogDisplayItem{Key: key, Label: label, Format: format, Value: *value}, ok: true}
}

func textItem(key, label string, value any, format CatalogDisplayFormat) itemCandidate {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return itemCandidate{}
		}
		value = strings.TrimSpace(typed)
	case *int64:
		if typed == nil || *typed < 0 {
			return itemCandidate{}
		}
		value = *typed
	case *float64:
		if !validPrice(typed) {
			return itemCandidate{}
		}
		value = *typed
	default:
		return itemCandidate{}
	}
	return itemCandidate{item: CatalogDisplayItem{Key: key, Label: label, Format: format, Value: value}, ok: true}
}

func priceSection(key, label string, candidates ...itemCandidate) CatalogDisplaySection {
	return section(key, label, candidates...)
}

func section(key, label string, candidates ...itemCandidate) CatalogDisplaySection {
	items := make([]CatalogDisplayItem, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ok {
			items = append(items, candidate.item)
		}
	}
	return CatalogDisplaySection{Key: key, Label: label, Items: items}
}

func mediaSection(prices PriceSet) CatalogDisplaySection {
	return priceSection("multimodal_pricing", "多模态计费",
		price("image_input", "图片输入", prices.ImageInputUSDPer1M, FormatUSDPer1MTokens),
		price("image_output", "图片输出", prices.ImageOutputUSDPer1M, FormatUSDPer1MTokens),
		price("audio_input", "音频输入", prices.AudioInputUSDPer1M, FormatUSDPer1MTokens),
		price("audio_output", "音频输出", prices.AudioOutputUSDPer1M, FormatUSDPer1MTokens))
}

func imageUnitSection(prices PriceSet) CatalogDisplaySection {
	return priceSection("image_generation", "图片生成", price("output_image", "每张", prices.OutputUSDPerImage, FormatUSDPerImage))
}

func serviceTierSections(facts CatalogFacts) []CatalogDisplaySection {
	sections := make([]CatalogDisplaySection, 0, len(facts.SupportedServiceTiers))
	seen := make(map[string]struct{}, len(facts.SupportedServiceTiers))
	for _, rawTier := range facts.SupportedServiceTiers {
		tier := strings.TrimSpace(rawTier)
		if tier == "" {
			continue
		}
		if _, exists := seen[tier]; exists {
			continue
		}
		seen[tier] = struct{}{}
		rates, ok := facts.ServiceTierPrices[tier]
		if !ok {
			continue
		}
		candidate := priceSection("tier_"+tier, tierLabel(tier),
			price("input", "输入", rates.InputUSDPer1M, FormatUSDPer1MTokens),
			price("output", "输出", rates.OutputUSDPer1M, FormatUSDPer1MTokens),
			price("cache_read", "缓存命中", rates.CachedInputUSDPer1M, FormatUSDPer1MTokens),
			price("cache_write", "缓存写入", rates.CacheWriteUSDPer1M, FormatUSDPer1MTokens),
			price("cache_write_1h", "1h 缓存写入", rates.CacheWrite1hUSDPer1M, FormatUSDPer1MTokens),
			price("cache_storage", "缓存存储", rates.CacheStorageUSDPer1MPerHour, FormatUSDPer1MTokenHour),
			price("audio_input", "音频输入", rates.AudioInputUSDPer1M, FormatUSDPer1MTokens))
		if len(candidate.Items) > 0 {
			sections = append(sections, candidate)
		}
	}
	return sections
}

func reasoningSection(facts CatalogFacts) CatalogDisplaySection {
	efforts := compactStrings(facts.SupportedReasoningEfforts)
	if len(efforts) == 0 {
		return CatalogDisplaySection{}
	}
	return section("reasoning", "思考能力", textItem("levels", "级别", strings.Join(efforts, " / "), FormatText))
}

func capacitySection(facts CatalogFacts) CatalogDisplaySection {
	return section("capacity", "容量",
		textItem("context", "上下文", facts.ContextWindowTokens, FormatTokens),
		textItem("max_input", "最大输入", facts.MaxInputTokens, FormatTokens),
		textItem("max_output", "最大输出", facts.MaxOutputTokens, FormatTokens))
}

func longContextSection(facts CatalogFacts) CatalogDisplaySection {
	if facts.LongContextInputTokenThreshold == nil || *facts.LongContextInputTokenThreshold < 0 {
		return CatalogDisplaySection{}
	}
	thresholdLabel := "触发阈值（不含）"
	if facts.LongContextInputThresholdInclusive {
		thresholdLabel = "触发阈值（含）"
	}
	return section("long_context", "长上下文计费",
		textItem("threshold", thresholdLabel, facts.LongContextInputTokenThreshold, FormatTokens),
		textItem("input_multiplier", "输入倍率", facts.LongContextInputCostMultiplier, FormatMultiplier),
		textItem("output_multiplier", "输出倍率", facts.LongContextOutputCostMultiplier, FormatMultiplier))
}

func sourceConversionSection(facts CatalogFacts) CatalogDisplaySection {
	currency := strings.TrimSpace(facts.SourcePricingCurrency)
	if currency == "" || strings.EqualFold(currency, "USD") {
		return CatalogDisplaySection{}
	}
	var exchange any
	if validPrice(facts.SourceExchangeRateToUSD) {
		exchange = "$" + trimFloat(*facts.SourceExchangeRateToUSD)
	}
	return section("currency_conversion", "美元换算",
		textItem("source_currency", "官方币种", currency, FormatText),
		textItem("exchange_rate", "1 "+currency, exchange, FormatText),
		textItem("exchange_rate_date", "汇率日期", facts.SourceExchangeRateDate, FormatText),
		textItem("source_note", "官方源价", facts.SourcePricingNote, FormatText))
}

func compact(sections []CatalogDisplaySection) []CatalogDisplaySection {
	result := make([]CatalogDisplaySection, 0, len(sections))
	for _, value := range sections {
		if value.Key != "" && len(value.Items) > 0 {
			result = append(result, value)
		}
	}
	return result
}

func compactStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func tierLabel(tier string) string {
	switch tier {
	case "priority":
		return "Priority"
	case "flex":
		return "Flex"
	case "batch":
		return "Batch API"
	default:
		return tier
	}
}

func validPrice(value *float64) bool {
	return value != nil && !math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= 0
}

func trimFloat(value float64) string {
	rounded := math.Round(value*1e8) / 1e8
	return strconv.FormatFloat(rounded, 'f', -1, 64)
}
