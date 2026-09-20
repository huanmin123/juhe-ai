package pricing

// DeepSeek pricing snapshot, ported from
// backend/src/modules/model-pricing/deepseek-model-pricing.data.ts (curated
// 2026-07-23). Since 2026-09-10 DeepSeek bills all models with official
// peak/off-peak time-of-day pricing (peak: weekdays UTC 01:00-04:00 and
// 06:00-10:00; weekends and Chinese public holidays are off-peak all day at
// half the peak rate). This snapshot is maintained at the user-confirmed
// peak-rate basis until time-of-day billing support lands; the fixed prices
// below therefore only match peak-window usage. Retired IDs
// (deepseek-v4-flash, retired 2026-09-10) are no longer kept here; the
// shutdown marker lives on the provider_model_catalog seed row only.
var deepSeekModelPricingData = []rawModel{
	{
		// deepseek-flash (official version DeepSeek-V4.1-Flash) at peak
		// rates per the confirmed 2026-09-20 peak-pricing decision; the
		// low/high/max effort set follows the V4 changelog entry, the
		// official default is undocumented and stays unset.
		Model: "deepseek-flash", Mode: "chat", CatalogOrder: intp(0), ReleaseDate: "2026-09-10",
		InputCostPerToken:         perToken(0.3),
		CacheReadInputTokenCost:   perToken(0.006),
		OutputCostPerToken:        perToken(1.2),
		ContextWindowTokens:       intp(1_000_000),
		MaxOutputTokens:           intp(384_000),
		SupportsPromptCaching:     true,
		SupportedAPIProtocols:     []string{"chat_completions", "responses", "messages"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedTools:            []string{"function_calling"},
		SupportedReasoningEfforts: []string{"low", "high", "max"},
	},
	{
		// User-confirmed 2026-09-20 compatibility alias: many upstream
		// providers expose this versioned ID for the same DeepSeek-V4.1-Flash
		// model, so the catalog carries it alongside the official
		// deepseek-flash ID at identical peak rates and capabilities.
		Model: "deepseek-v4.1-flash", Mode: "chat", CatalogOrder: intp(1), ReleaseDate: "2026-09-10",
		InputCostPerToken:         perToken(0.3),
		CacheReadInputTokenCost:   perToken(0.006),
		OutputCostPerToken:        perToken(1.2),
		ContextWindowTokens:       intp(1_000_000),
		MaxOutputTokens:           intp(384_000),
		SupportsPromptCaching:     true,
		SupportedAPIProtocols:     []string{"chat_completions", "responses", "messages"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedTools:            []string{"function_calling"},
		SupportedReasoningEfforts: []string{"low", "high", "max"},
	},
	{
		// Officially kept on sale after a retracted deprecation (changelog
		// 2026-09-10); priced at the peak-rate basis like the rest of the
		// snapshot until time-of-day billing support lands.
		Model: "deepseek-v4-pro", Mode: "chat", CatalogOrder: intp(20), ReleaseDate: "2026-04-24",
		InputCostPerToken:         perToken(1.32),
		CacheReadInputTokenCost:   perToken(0.044),
		OutputCostPerToken:        perToken(3.96),
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
