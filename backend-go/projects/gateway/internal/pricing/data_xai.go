package pricing

// xAI pricing snapshot, ported from
// backend/src/modules/model-pricing/xai-model-pricing.data.ts (curated
// 2026-08-26). Models with a 200k threshold charge the higher rate for all
// request tokens once the prompt reaches the threshold.
type xaiTextModelMetadata struct {
	catalogVisible            *bool
	releaseDate               string
	supportedAPIProtocols     []string
	supportedReasoningEfforts []string
	defaultReasoningEffort    string
}

func xaiTextModel(model string, contextWindowTokens int, inputUsdPer1M, cachedInputUsdPer1M, outputUsdPer1M float64, metadata xaiTextModelMetadata) rawModel {
	out := rawModel{
		Model: model, Mode: "chat",
		CatalogVisible:                          metadata.catalogVisible,
		ReleaseDate:                             metadata.releaseDate,
		ContextWindowTokens:                     &contextWindowTokens,
		InputCostPerToken:                       perToken(inputUsdPer1M),
		CacheReadInputTokenCost:                 perToken(cachedInputUsdPer1M),
		OutputCostPerToken:                      perToken(outputUsdPer1M),
		InputCostPerTokenPriority:               perToken(inputUsdPer1M * 2),
		CacheReadInputTokenCostPriority:         perToken(cachedInputUsdPer1M * 2),
		OutputCostPerTokenPriority:              perToken(outputUsdPer1M * 2),
		LongContextInputTokenThreshold:          intp(200_000),
		LongContextInputTokenThresholdInclusive: true,
		LongContextInputCostMultiplier:          f64p(2),
		LongContextOutputCostMultiplier:         f64p(2),
		SupportsPromptCaching:                   true,
		SupportedServiceTiers:                   []string{"priority"},
		SupportedAPIProtocols:                   metadata.supportedAPIProtocols,
		InputModalities:                         []string{"text", "image"},
		OutputModalities:                        []string{"text"},
		SupportedTools:                          []string{"function_calling"},
		SupportedReasoningEfforts:               metadata.supportedReasoningEfforts,
		DefaultReasoningEffort:                  metadata.defaultReasoningEffort,
	}
	if out.SupportedAPIProtocols == nil {
		out.SupportedAPIProtocols = []string{"chat_completions", "responses"}
	}
	return out
}

// xAIModelPricingData — curated from the official xAI docs.
var xAIModelPricingData = []rawModel{
	xaiTextModel("grok-4.6", 500_000, 2, 0.5, 6, xaiTextModelMetadata{
		releaseDate:               "2026-08-12",
		supportedReasoningEfforts: []string{"low", "medium", "high", "xhigh"},
		defaultReasoningEffort:    "high",
	}),
	xaiTextModel("grok-4.5", 500_000, 2, 0.3, 6, xaiTextModelMetadata{
		releaseDate:               "2026-07-08",
		supportedReasoningEfforts: []string{"low", "medium", "high", "xhigh"},
		defaultReasoningEffort:    "high",
	}),
	xaiTextModel("grok-4.20-0309-reasoning", 1_000_000, 1.25, 0.2, 2.5, xaiTextModelMetadata{
		releaseDate: "2026-03-10",
	}),
	xaiTextModel("grok-4.20-0309-non-reasoning", 1_000_000, 1.25, 0.2, 2.5, xaiTextModelMetadata{
		releaseDate: "2026-03-10",
	}),
	xaiTextModel("grok-build-0.1", 256_000, 1, 0.2, 2, xaiTextModelMetadata{
		releaseDate: "2026-05-19",
	}),
	xaiTextModel("grok-4.20-multi-agent-0309", 1_000_000, 1.25, 0.2, 2.5, xaiTextModelMetadata{
		releaseDate:           "2026-03-10",
		supportedAPIProtocols: []string{"responses"},
	}),
	{
		Model: "grok-imagine-image-2.0", Mode: "image",
		ReleaseDate:           "2026-08-07",
		OutputCostPerImage:    f64p(0.04),
		SupportedAPIProtocols: []string{"images"},
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"image"},
	},
	{
		Model: "grok-imagine-image", Mode: "image",
		ReleaseDate:           "2026-03-02",
		OutputCostPerImage:    f64p(0.02),
		SupportedAPIProtocols: []string{"images"},
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"image"},
	},
	{
		Model: "grok-imagine-image-quality", Mode: "image",
		ReleaseDate:           "2026-04-03",
		OutputCostPerImage:    f64p(0.05),
		SupportedAPIProtocols: []string{"images"},
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"image"},
	},
}
