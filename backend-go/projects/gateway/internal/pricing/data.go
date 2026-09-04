package pricing

import "strings"

// rawModel mirrors provider-driver.types RawModelPricing — the LiteLLM/
// model-price-repo style snapshot rows the Node *.data.ts files carry. Token
// prices are USD per token; per-million values are derived by the same
// runtime arithmetic the Node factories use so the float64 bits match.
type rawModel struct {
	Model        string
	Mode         string
	CatalogOrder *int
	ReleaseDate  string
	ShutdownDate string

	InputCostPerToken          *float64
	InputCostPerTokenPriority  *float64
	InputCostPerTokenFlex      *float64
	InputCostPerTokenBatch     *float64
	OutputCostPerToken         *float64
	OutputCostPerTokenPriority *float64
	OutputCostPerTokenFlex     *float64
	OutputCostPerTokenBatch    *float64

	CacheCreationInputTokenCost                 *float64
	CacheCreationInputTokenCostPriority         *float64
	CacheCreationInputTokenCostFlex             *float64
	CacheCreationInputTokenCostAbove1hr         *float64
	CacheCreationInputTokenCostAbove1hrPriority *float64
	CacheCreationInputTokenCostAbove1hrFlex     *float64
	CacheStorageInputTokenCostPerHour           *float64
	CacheStorageInputTokenCostPerHourPriority   *float64
	CacheStorageInputTokenCostPerHourFlex       *float64
	CacheReadInputTokenCost                     *float64
	CacheReadInputTokenCostPriority             *float64
	CacheReadInputTokenCostFlex                 *float64
	CacheReadInputImageTokenCost                *float64

	InputCostPerImageToken         *float64
	OutputCostPerImage             *float64
	OutputCostPerImageToken        *float64
	InputCostPerAudioToken         *float64
	InputCostPerAudioTokenPriority *float64
	InputCostPerAudioTokenFlex     *float64
	OutputCostPerAudioToken        *float64

	ContextWindowTokens *int
	MaxInputTokens      *int
	MaxOutputTokens     *int
	MaxTokens           *int

	LongContextInputTokenThreshold          *int
	LongContextInputTokenThresholdInclusive bool
	LongContextInputCostMultiplier          *float64
	LongContextOutputCostMultiplier         *float64

	SupportedAPIProtocols []string
	InputModalities       []string
	OutputModalities      []string
	SupportedTools        []string

	SupportsPromptCaching     bool
	SupportedServiceTiers     []string
	SupportedReasoningEfforts []string
	DefaultReasoningEffort    string

	CodexSupportedReasoningLevels []string
	CodexDefaultReasoningLevel    string
	CodexMultiAgentVersion        string

	CatalogVisible *bool

	SourcePricingCurrency   string
	SourceExchangeRateToUsd *float64
	SourceExchangeRateDate  string
	SourcePricingNote       string
}

// perToken divides a USD-per-1M literal at runtime float64 precision, matching
// the Node `<usd> / 1_000_000` data rows bit for bit.
func perToken(usdPer1M float64) *float64 {
	out := usdPer1M / 1_000_000
	return &out
}

// providerEntry mirrors one ModelPricingProviderDriver registration.
type providerEntry struct {
	providerID    string
	pricingSource string
	billingPolicy string
	supports      func(providerCode string) bool
	rawModels     []rawModel
}

// providerCatalog mirrors modelPricingProviderDrivers (registry order kept:
// openai, deepseek, glm, anthropic, gemini, xai).
var providerCatalog = []*providerEntry{
	{
		providerID:    "openai-compatible",
		pricingSource: "openai-pricing-snapshot",
		billingPolicy: "openai",
		supports:      isOpenAICompatibleProviderCode,
		rawModels:     openAIModelPricingData,
	},
	{
		providerID:    "deepseek",
		pricingSource: "deepseek-pricing-snapshot",
		billingPolicy: "deepseek",
		supports:      func(code string) bool { return normalizeProviderToken(code) == "deepseek" },
		rawModels:     deepSeekModelPricingData,
	},
	{
		providerID:    "glm",
		pricingSource: "glm-pricing-snapshot",
		billingPolicy: "glm",
		supports:      func(code string) bool { return normalizeProviderToken(code) == "glm" },
		rawModels:     glmModelPricingData,
	},
	{
		providerID:    "anthropic",
		pricingSource: "anthropic-pricing-snapshot",
		billingPolicy: "anthropic",
		supports:      func(code string) bool { return normalizeProviderToken(code) == "anthropic" },
		rawModels:     anthropicModelPricingData,
	},
	{
		providerID:    "gemini",
		pricingSource: "gemini-pricing-snapshot",
		billingPolicy: "gemini",
		supports:      func(code string) bool { return normalizeProviderToken(code) == "gemini" },
		rawModels:     geminiModelPricingData,
	},
	{
		providerID:    "xai",
		pricingSource: "xai-official-pricing-2026-07-18",
		billingPolicy: "xai",
		supports:      func(code string) bool { return normalizeProviderToken(code) == "xai" },
		rawModels:     xAIModelPricingData,
	},
}

// providerEntryFor mirrors modelPricingProviderDriverForProvider: the first
// driver whose supportsProvider matches the normalized provider token.
func providerEntryFor(providerCode string) *providerEntry {
	normalized := normalizeProviderToken(providerCode)
	if normalized == "" {
		return nil
	}
	for _, entry := range providerCatalog {
		if entry.supports(normalized) {
			return entry
		}
	}
	return nil
}

// normalizeProviderToken mirrors domain/provider-protocol normalizeProviderToken.
func normalizeProviderToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
