package pricing

// DeepSeek pricing snapshot, ported from
// backend/src/modules/model-pricing/deepseek-model-pricing.data.ts (curated
// 2026-07-23). DeepSeek documents Responses for deepseek-v4-flash only; V4
// Pro stays listed for product-level pre-compatibility.
var deepSeekModelPricingData = []rawModel{
	{
		Model: "deepseek-v4-flash", Mode: "chat", CatalogOrder: intp(10), ReleaseDate: "2026-04-24",
		InputCostPerToken:         perToken(0.14),
		CacheReadInputTokenCost:   perToken(0.0028),
		OutputCostPerToken:        perToken(0.28),
		ContextWindowTokens:       intp(1_000_000),
		MaxOutputTokens:           intp(384_000),
		SupportsPromptCaching:     true,
		SupportedAPIProtocols:     []string{"chat_completions", "responses", "messages"},
		InputModalities:           []string{"text"},
		OutputModalities:          []string{"text"},
		SupportedTools:            []string{"function_calling"},
		SupportedReasoningEfforts: []string{"high", "max"},
		DefaultReasoningEffort:    "high",
	},
	{
		Model: "deepseek-v4-pro", Mode: "chat", CatalogOrder: intp(20), ReleaseDate: "2026-04-24",
		InputCostPerToken:         perToken(0.435),
		CacheReadInputTokenCost:   perToken(0.003625),
		OutputCostPerToken:        perToken(0.87),
		ContextWindowTokens:       intp(1_000_000),
		MaxOutputTokens:           intp(384_000),
		SupportsPromptCaching:     true,
		SupportedAPIProtocols:     []string{"chat_completions", "responses", "messages", "completions"},
		InputModalities:           []string{"text"},
		OutputModalities:          []string{"text"},
		SupportedTools:            []string{"function_calling"},
		SupportedReasoningEfforts: []string{"high", "max"},
		DefaultReasoningEffort:    "high",
	},
}
