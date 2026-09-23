package pricing

// Anthropic pricing snapshot, ported from
// backend/src/modules/model-pricing/anthropic-model-pricing.data.ts. The
// Node `model()` factory derives cache prices from the per-million input
// price; the arithmetic order (a*1.25/1e6, a*2/1e6, a*0.1/1e6) is preserved
// so the float64 values match the Node rows bit for bit.
func anthropicModel(model string, catalogOrder int, releaseDate string, inPer1M, outPer1M float64, contextWindow, maxInput, maxOutput int, efforts []string, defaultEffort string) rawModel {
	return rawModel{
		Model:        model,
		CatalogOrder: &catalogOrder,
		ReleaseDate:  releaseDate,

		InputCostPerToken:                   perToken(inPer1M),
		OutputCostPerToken:                  perToken(outPer1M),
		CacheCreationInputTokenCost:         f64p(inPer1M * 1.25 / 1_000_000),
		CacheCreationInputTokenCostAbove1hr: f64p(inPer1M * 2 / 1_000_000),
		CacheReadInputTokenCost:             f64p(inPer1M * 0.1 / 1_000_000),

		ContextWindowTokens: &contextWindow,
		MaxInputTokens:      &maxInput,
		MaxOutputTokens:     &maxOutput,

		SupportedAPIProtocols: []string{"messages", "message_token_counting"},
		SupportsPromptCaching: true,
		SupportedServiceTiers: []string{},
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"text"},
		SupportedTools:        []string{"function_calling", "code_execution"},

		SupportedReasoningEfforts: efforts,
		DefaultReasoningEffort:    defaultEffort,
	}
}

// anthropicModelCacheRead — variant for the rows whose official cache-read
// multiplier deviates from the standard 0.1x (0.025x on Claude Fable 5.1,
// 0.05x on Claude Opus 5.5; official pricing page, checked 2026-09-23).
// Cache writes keep the shared 1.25x/2x derivation.
func anthropicModelCacheRead(cacheReadMultiplier float64, model string, catalogOrder int, releaseDate string, inPer1M, outPer1M float64, contextWindow, maxInput, maxOutput int, efforts []string, defaultEffort string) rawModel {
	row := anthropicModel(model, catalogOrder, releaseDate, inPer1M, outPer1M, contextWindow, maxInput, maxOutput, efforts, defaultEffort)
	row.CacheReadInputTokenCost = f64p(inPer1M * cacheReadMultiplier / 1_000_000)
	return row
}

// anthropicModelPricingData — curated from Anthropic's official docs.
// Official deprecations (checked 2026-09-20) show the current dated rows as
// Active with tentative floors only ("not sooner than"), no confirmed
// shutdown dates, so no row carries ShutdownDate here.
var anthropicModelPricingData = []rawModel{
	// Claude Sonnet 5: the officially scheduled 2026-09-01 increase to
	// $3/$15 was cancelled; the launch intro price $2/$10 (through
	// 2026-08-31) is now the standard price (official pricing page,
	// checked 2026-09-23).
	anthropicModelCacheRead(0.05, "claude-opus-5-5", 2, "2026-09-22", 4, 20, 1_000_000, 1_000_000, 128_000,
		[]string{"low", "medium", "high", "xhigh", "max"}, "medium"),
	anthropicModel("claude-opus-5", 5, "2026-07-24", 5, 25, 1_000_000, 1_000_000, 128_000,
		[]string{"low", "medium", "high", "xhigh", "max"}, "high"),
	anthropicModelCacheRead(0.025, "claude-fable-5-1", 10, "2026-09-01", 10, 50, 1_000_000, 1_000_000, 128_000,
		[]string{"low", "medium", "high", "xhigh", "max"}, "high"),
	anthropicModel("claude-fable-5", 20, "2026-06-09", 10, 50, 1_000_000, 1_000_000, 128_000,
		[]string{"low", "medium", "high", "xhigh", "max"}, "high"),
	anthropicModel("claude-sonnet-5", 25, "2026-06-30", 2, 10, 1_000_000, 1_000_000, 128_000,
		[]string{"low", "medium", "high", "xhigh", "max"}, "high"),
	anthropicModel("claude-opus-4-8", 40, "2026-05-28", 5, 25, 1_000_000, 1_000_000, 128_000,
		[]string{"low", "medium", "high", "xhigh", "max"}, "high"),
	anthropicModel("claude-opus-4-7", 50, "2026-04-16", 5, 25, 1_000_000, 1_000_000, 128_000,
		[]string{"low", "medium", "high", "xhigh", "max"}, "high"),
	anthropicModel("claude-opus-4-6", 60, "2026-02-05", 5, 25, 1_000_000, 1_000_000, 128_000,
		[]string{"low", "medium", "high", "max"}, "high"),
	anthropicModel("claude-opus-4-5", 80, "2025-11-24", 5, 25, 200_000, 200_000, 64_000,
		[]string{"low", "medium", "high"}, "high"),
	anthropicModel("claude-opus-4-5-20251101", 90, "2025-11-01", 5, 25, 200_000, 200_000, 64_000,
		[]string{"low", "medium", "high"}, "high"),
	anthropicModel("claude-sonnet-4-6", 120, "2026-02-17", 3, 15, 1_000_000, 1_000_000, 64_000,
		[]string{"low", "medium", "high", "max"}, "high"),
	anthropicModel("claude-sonnet-4-5", 140, "2025-09-29", 3, 15, 200_000, 200_000, 64_000,
		[]string{}, ""),
	anthropicModel("claude-sonnet-4-5-20250929", 150, "2025-09-29", 3, 15, 200_000, 200_000, 64_000,
		[]string{}, ""),
	anthropicModel("claude-haiku-4-5", 160, "2025-10-15", 1, 5, 200_000, 200_000, 64_000,
		[]string{}, ""),
	anthropicModel("claude-haiku-4-5-20251001", 170, "2025-10-01", 1, 5, 200_000, 200_000, 64_000,
		[]string{}, ""),
}
