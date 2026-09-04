package pricing

// Gemini pricing snapshot, ported from
// backend/src/modules/model-pricing/gemini-model-pricing.data.ts (curated
// 2026-08-26). The Node textModel/embeddingModel factories are preserved:
// per-million inputs convert through the same runtime /1e6 division and the
// fixed max_input/max_output defaults stay identical.
type geminiTierPrices struct {
	inputUsdPer1M               float64
	outputUsdPer1M              float64
	cachedInputUsdPer1M         *float64
	cacheStorageUsdPer1MPerHour float64
	audioInputUsdPer1M          *float64
}

type geminiModelInput struct {
	model                           string
	catalogOrder                    int
	releaseDate                     string
	shutdownDate                    string
	inputUsdPer1M                   float64
	outputUsdPer1M                  float64
	cachedInputUsdPer1M             *float64
	cacheStorageUsdPer1MPerHour     float64
	audioInputUsdPer1M              *float64
	imageInputUsdPer1M              *float64
	maxInputTokens                  *int
	flex                            *geminiTierPrices
	priority                        *geminiTierPrices
	longContextInputTokenThreshold  *int
	longContextInputCostMultiplier  *float64
	longContextOutputCostMultiplier *float64
	supportedAPIProtocols           []string
	inputModalities                 []string
	outputModalities                []string
	supportedTools                  []string
	supportedReasoningEfforts       []string
	defaultReasoningEffort          string
}

func usdPerTokenPtr(usdPer1M *float64) *float64 {
	if usdPer1M == nil {
		return nil
	}
	return perToken(*usdPer1M)
}

func geminiTextModel(in geminiModelInput) rawModel {
	out := rawModel{
		Model: in.model, Mode: "chat", CatalogOrder: &in.catalogOrder, ReleaseDate: in.releaseDate,
		InputCostPerToken:                         perToken(in.inputUsdPer1M),
		OutputCostPerToken:                        perToken(in.outputUsdPer1M),
		CacheReadInputTokenCost:                   usdPerTokenPtr(in.cachedInputUsdPer1M),
		CacheStorageInputTokenCostPerHour:         perToken(in.cacheStorageUsdPer1MPerHour),
		InputCostPerAudioToken:                    usdPerTokenPtr(in.audioInputUsdPer1M),
		InputCostPerTokenFlex:                     usdPerTokenPtr(geminiTierField(in.flex, (*geminiTierPrices).inputPtr)),
		OutputCostPerTokenFlex:                    usdPerTokenPtr(geminiTierField(in.flex, (*geminiTierPrices).outputPtr)),
		CacheReadInputTokenCostFlex:               usdPerTokenPtr(geminiTierField(in.flex, (*geminiTierPrices).cachedPtr)),
		CacheStorageInputTokenCostPerHourFlex:     usdPerTokenPtr(geminiTierField(in.flex, (*geminiTierPrices).storagePtr)),
		InputCostPerAudioTokenFlex:                usdPerTokenPtr(geminiTierField(in.flex, (*geminiTierPrices).audioPtr)),
		InputCostPerTokenPriority:                 usdPerTokenPtr(geminiTierField(in.priority, (*geminiTierPrices).inputPtr)),
		OutputCostPerTokenPriority:                usdPerTokenPtr(geminiTierField(in.priority, (*geminiTierPrices).outputPtr)),
		CacheReadInputTokenCostPriority:           usdPerTokenPtr(geminiTierField(in.priority, (*geminiTierPrices).cachedPtr)),
		CacheStorageInputTokenCostPerHourPriority: usdPerTokenPtr(geminiTierField(in.priority, (*geminiTierPrices).storagePtr)),
		InputCostPerAudioTokenPriority:            usdPerTokenPtr(geminiTierField(in.priority, (*geminiTierPrices).audioPtr)),
		LongContextInputTokenThreshold:            in.longContextInputTokenThreshold,
		LongContextInputCostMultiplier:            in.longContextInputCostMultiplier,
		LongContextOutputCostMultiplier:           in.longContextOutputCostMultiplier,
		MaxInputTokens:                            intp(1_048_576),
		MaxOutputTokens:                           intp(65_536),
		ShutdownDate:                              in.shutdownDate,
		SupportedAPIProtocols:                     in.supportedAPIProtocols,
		InputModalities:                           in.inputModalities,
		OutputModalities:                          in.outputModalities,
		SupportedTools:                            in.supportedTools,
		SupportedReasoningEfforts:                 in.supportedReasoningEfforts,
		DefaultReasoningEffort:                    in.defaultReasoningEffort,
		SupportsPromptCaching:                     true,
	}
	visible := true
	out.CatalogVisible = &visible
	if in.flex != nil || in.priority != nil {
		out.SupportedServiceTiers = []string{"priority", "flex"}
	}
	return out
}

func geminiTierField(tier *geminiTierPrices, get func(*geminiTierPrices) *float64) *float64 {
	if tier == nil {
		return nil
	}
	return get(tier)
}

func (t *geminiTierPrices) inputPtr() *float64   { return &t.inputUsdPer1M }
func (t *geminiTierPrices) outputPtr() *float64  { return &t.outputUsdPer1M }
func (t *geminiTierPrices) cachedPtr() *float64  { return t.cachedInputUsdPer1M }
func (t *geminiTierPrices) storagePtr() *float64 { return &t.cacheStorageUsdPer1MPerHour }
func (t *geminiTierPrices) audioPtr() *float64   { return t.audioInputUsdPer1M }

func geminiEmbeddingModel(in geminiModelInput) rawModel {
	out := rawModel{
		Model: in.model, Mode: "embedding", CatalogOrder: &in.catalogOrder, ReleaseDate: in.releaseDate,
		InputCostPerToken:         perToken(in.inputUsdPer1M),
		InputCostPerImageToken:    usdPerTokenPtr(in.imageInputUsdPer1M),
		InputCostPerAudioToken:    usdPerTokenPtr(in.audioInputUsdPer1M),
		MaxInputTokens:            in.maxInputTokens,
		ShutdownDate:              in.shutdownDate,
		SupportedAPIProtocols:     in.supportedAPIProtocols,
		InputModalities:           in.inputModalities,
		OutputModalities:          in.outputModalities,
		SupportedTools:            in.supportedTools,
		SupportedReasoningEfforts: in.supportedReasoningEfforts,
		DefaultReasoningEffort:    in.defaultReasoningEffort,
	}
	visible := true
	out.CatalogVisible = &visible
	return out
}

// geminiProtocols / geminiInteractions mirror the shared protocol lists.
var (
	geminiEmbeddingProtocols = []string{"embed_content"}
)

// geminiModelPricingData — curated from the official Gemini docs.
var geminiModelPricingData = []rawModel{
	geminiTextModel(geminiModelInput{
		model: "gemini-3.7-flash", catalogOrder: 0, releaseDate: "2026-08-13",
		inputUsdPer1M: 0.75, outputUsdPer1M: 3.75, cachedInputUsdPer1M: f64p(0.075), cacheStorageUsdPer1MPerHour: 0.5,
		flex:                      &geminiTierPrices{inputUsdPer1M: 0.375, outputUsdPer1M: 1.875, cachedInputUsdPer1M: f64p(0.0375), cacheStorageUsdPer1MPerHour: 0.5},
		priority:                  &geminiTierPrices{inputUsdPer1M: 1.35, outputUsdPer1M: 6.75, cachedInputUsdPer1M: f64p(0.135), cacheStorageUsdPer1MPerHour: 0.5},
		supportedAPIProtocols:     []string{"chat_completions", "generate_content", "stream_generate_content", "count_tokens", "interactions"},
		inputModalities:           []string{"text", "image"},
		outputModalities:          []string{"text"},
		supportedTools:            []string{"code_execution", "file_search", "function_calling", "google_maps_grounding", "google_search_grounding", "structured_outputs", "url_context", "computer_use"},
		supportedReasoningEfforts: []string{"low", "medium", "high"},
		defaultReasoningEffort:    "high",
	}),
	geminiTextModel(geminiModelInput{
		model: "gemini-3.6-flash", catalogOrder: 1, releaseDate: "2026-07-21",
		inputUsdPer1M: 1.5, outputUsdPer1M: 7.5, cachedInputUsdPer1M: f64p(0.15), cacheStorageUsdPer1MPerHour: 1,
		flex:                      &geminiTierPrices{inputUsdPer1M: 0.75, outputUsdPer1M: 3.75, cachedInputUsdPer1M: f64p(0.075), cacheStorageUsdPer1MPerHour: 1},
		priority:                  &geminiTierPrices{inputUsdPer1M: 2.7, outputUsdPer1M: 13.5, cachedInputUsdPer1M: f64p(0.27), cacheStorageUsdPer1MPerHour: 1.8},
		supportedAPIProtocols:     []string{"chat_completions", "generate_content", "stream_generate_content", "count_tokens", "interactions"},
		inputModalities:           []string{"text", "image", "video", "audio", "file"},
		outputModalities:          []string{"text"},
		supportedTools:            []string{"code_execution", "file_search", "function_calling", "google_maps_grounding", "google_search_grounding", "structured_outputs", "url_context", "computer_use"},
		supportedReasoningEfforts: []string{"minimal", "low", "medium", "high"},
		defaultReasoningEffort:    "medium",
	}),
	geminiTextModel(geminiModelInput{
		model: "gemini-3.5-flash-lite", catalogOrder: 5, releaseDate: "2026-07-21",
		inputUsdPer1M: 0.3, outputUsdPer1M: 2.5, cachedInputUsdPer1M: f64p(0.03), cacheStorageUsdPer1MPerHour: 1,
		flex:                      &geminiTierPrices{inputUsdPer1M: 0.15, outputUsdPer1M: 1.25, cachedInputUsdPer1M: f64p(0.02), cacheStorageUsdPer1MPerHour: 1},
		priority:                  &geminiTierPrices{inputUsdPer1M: 0.54, outputUsdPer1M: 4.5, cachedInputUsdPer1M: f64p(0.05), cacheStorageUsdPer1MPerHour: 1.8},
		supportedAPIProtocols:     []string{"chat_completions", "generate_content", "stream_generate_content", "count_tokens", "interactions"},
		inputModalities:           []string{"text", "image", "video", "audio", "file"},
		outputModalities:          []string{"text"},
		supportedTools:            []string{"code_execution", "file_search", "function_calling", "google_maps_grounding", "google_search_grounding", "structured_outputs", "url_context"},
		supportedReasoningEfforts: []string{"minimal", "low", "medium", "high"},
		defaultReasoningEffort:    "minimal",
	}),
	geminiTextModel(geminiModelInput{
		model: "gemini-3.5-flash", catalogOrder: 10, releaseDate: "2026-05-19",
		inputUsdPer1M: 1.5, outputUsdPer1M: 9, cachedInputUsdPer1M: f64p(0.15), cacheStorageUsdPer1MPerHour: 1,
		flex:                      &geminiTierPrices{inputUsdPer1M: 0.75, outputUsdPer1M: 4.5, cachedInputUsdPer1M: f64p(0.08), cacheStorageUsdPer1MPerHour: 1},
		priority:                  &geminiTierPrices{inputUsdPer1M: 2.7, outputUsdPer1M: 16.2, cachedInputUsdPer1M: f64p(0.27), cacheStorageUsdPer1MPerHour: 1.8},
		supportedAPIProtocols:     []string{"chat_completions", "generate_content", "stream_generate_content", "count_tokens", "interactions"},
		inputModalities:           []string{"text", "image", "video", "audio", "file"},
		outputModalities:          []string{"text"},
		supportedTools:            []string{"code_execution", "file_search", "function_calling", "google_maps_grounding", "google_search_grounding", "structured_outputs", "url_context", "computer_use"},
		supportedReasoningEfforts: []string{"minimal", "low", "medium", "high"},
		defaultReasoningEffort:    "medium",
	}),
	geminiTextModel(geminiModelInput{
		model: "gemini-3.1-pro-preview", catalogOrder: 20, releaseDate: "2026-02-19",
		inputUsdPer1M: 2, outputUsdPer1M: 12, cachedInputUsdPer1M: f64p(0.2), cacheStorageUsdPer1MPerHour: 4.5,
		flex:                            &geminiTierPrices{inputUsdPer1M: 1, outputUsdPer1M: 6, cachedInputUsdPer1M: f64p(0.2), cacheStorageUsdPer1MPerHour: 4.5},
		priority:                        &geminiTierPrices{inputUsdPer1M: 3.6, outputUsdPer1M: 21.6, cachedInputUsdPer1M: f64p(0.36), cacheStorageUsdPer1MPerHour: 8.1},
		longContextInputTokenThreshold:  intp(200_000),
		longContextInputCostMultiplier:  f64p(2),
		longContextOutputCostMultiplier: f64p(1.5),
		supportedAPIProtocols:           []string{"chat_completions", "generate_content", "stream_generate_content", "count_tokens", "interactions"},
		inputModalities:                 []string{"text", "image", "video", "audio", "file"},
		outputModalities:                []string{"text"},
		supportedTools:                  []string{"code_execution", "file_search", "function_calling", "google_maps_grounding", "google_search_grounding", "structured_outputs", "url_context"},
		supportedReasoningEfforts:       []string{"low", "medium", "high"},
		defaultReasoningEffort:          "high",
	}),
	geminiTextModel(geminiModelInput{
		model: "gemini-3.1-pro-preview-customtools", catalogOrder: 30, releaseDate: "2026-02-19",
		inputUsdPer1M: 2, outputUsdPer1M: 12, cachedInputUsdPer1M: f64p(0.2), cacheStorageUsdPer1MPerHour: 4.5,
		flex:                            &geminiTierPrices{inputUsdPer1M: 1, outputUsdPer1M: 6, cachedInputUsdPer1M: f64p(0.2), cacheStorageUsdPer1MPerHour: 4.5},
		priority:                        &geminiTierPrices{inputUsdPer1M: 3.6, outputUsdPer1M: 21.6, cachedInputUsdPer1M: f64p(0.36), cacheStorageUsdPer1MPerHour: 8.1},
		longContextInputTokenThreshold:  intp(200_000),
		longContextInputCostMultiplier:  f64p(2),
		longContextOutputCostMultiplier: f64p(1.5),
		supportedAPIProtocols:           []string{"chat_completions", "generate_content", "stream_generate_content", "count_tokens"},
		inputModalities:                 []string{"text", "image", "video", "audio", "file"},
		outputModalities:                []string{"text"},
		supportedTools:                  []string{"code_execution", "file_search", "function_calling", "google_maps_grounding", "google_search_grounding", "structured_outputs", "url_context"},
		supportedReasoningEfforts:       []string{"low", "medium", "high"},
		defaultReasoningEffort:          "high",
	}),
	geminiTextModel(geminiModelInput{
		model: "gemini-3-flash-preview", catalogOrder: 40, releaseDate: "2025-12-17",
		inputUsdPer1M: 0.5, outputUsdPer1M: 3, cachedInputUsdPer1M: f64p(0.05), cacheStorageUsdPer1MPerHour: 1, audioInputUsdPer1M: f64p(1),
		flex:                      &geminiTierPrices{inputUsdPer1M: 0.25, outputUsdPer1M: 1.5, cachedInputUsdPer1M: f64p(0.05), audioInputUsdPer1M: f64p(0.5), cacheStorageUsdPer1MPerHour: 1},
		priority:                  &geminiTierPrices{inputUsdPer1M: 0.9, outputUsdPer1M: 5.4, cachedInputUsdPer1M: f64p(0.09), audioInputUsdPer1M: f64p(1.8), cacheStorageUsdPer1MPerHour: 1.8},
		supportedAPIProtocols:     []string{"chat_completions", "generate_content", "stream_generate_content", "count_tokens", "interactions"},
		inputModalities:           []string{"text", "image", "video", "audio", "file"},
		outputModalities:          []string{"text"},
		supportedTools:            []string{"code_execution", "file_search", "function_calling", "google_maps_grounding", "google_search_grounding", "structured_outputs", "url_context", "computer_use"},
		supportedReasoningEfforts: []string{"minimal", "low", "medium", "high"},
		defaultReasoningEffort:    "high",
	}),
	geminiTextModel(geminiModelInput{
		model: "gemini-3.1-flash-lite", catalogOrder: 50, releaseDate: "2026-05-07", shutdownDate: "2027-05-07",
		inputUsdPer1M: 0.25, outputUsdPer1M: 1.5, cachedInputUsdPer1M: f64p(0.025), cacheStorageUsdPer1MPerHour: 1, audioInputUsdPer1M: f64p(0.5),
		flex:                      &geminiTierPrices{inputUsdPer1M: 0.125, outputUsdPer1M: 0.75, cachedInputUsdPer1M: f64p(0.0125), audioInputUsdPer1M: f64p(0.25), cacheStorageUsdPer1MPerHour: 0.5},
		priority:                  &geminiTierPrices{inputUsdPer1M: 0.45, outputUsdPer1M: 2.7, cachedInputUsdPer1M: f64p(0.045), audioInputUsdPer1M: f64p(0.9), cacheStorageUsdPer1MPerHour: 1.8},
		supportedAPIProtocols:     []string{"chat_completions", "generate_content", "stream_generate_content", "count_tokens", "interactions"},
		inputModalities:           []string{"text", "image", "video", "audio", "file"},
		outputModalities:          []string{"text"},
		supportedTools:            []string{"code_execution", "file_search", "function_calling", "google_maps_grounding", "google_search_grounding", "structured_outputs", "url_context"},
		supportedReasoningEfforts: []string{"minimal", "low", "medium", "high"},
	}),
	geminiTextModel(geminiModelInput{
		model: "gemini-2.5-pro", catalogOrder: 60, releaseDate: "2025-06-17", shutdownDate: "2026-10-16",
		inputUsdPer1M: 1.25, outputUsdPer1M: 10, cachedInputUsdPer1M: f64p(0.125), cacheStorageUsdPer1MPerHour: 4.5,
		flex:                            &geminiTierPrices{inputUsdPer1M: 0.625, outputUsdPer1M: 5, cachedInputUsdPer1M: f64p(0.125), cacheStorageUsdPer1MPerHour: 4.5},
		priority:                        &geminiTierPrices{inputUsdPer1M: 2.25, outputUsdPer1M: 18, cachedInputUsdPer1M: f64p(0.225), cacheStorageUsdPer1MPerHour: 8.1},
		longContextInputTokenThreshold:  intp(200_000),
		longContextInputCostMultiplier:  f64p(2),
		longContextOutputCostMultiplier: f64p(1.5),
		supportedAPIProtocols:           []string{"chat_completions", "generate_content", "stream_generate_content", "count_tokens", "interactions"},
		inputModalities:                 []string{"text", "image", "video", "audio", "file"},
		outputModalities:                []string{"text"},
		supportedTools:                  []string{"code_execution", "file_search", "function_calling", "google_maps_grounding", "google_search_grounding", "structured_outputs", "url_context"},
		supportedReasoningEfforts:       []string{"low", "medium", "high"},
	}),
	geminiTextModel(geminiModelInput{
		model: "gemini-2.5-flash", catalogOrder: 70, releaseDate: "2025-06-17", shutdownDate: "2026-10-16",
		inputUsdPer1M: 0.3, outputUsdPer1M: 2.5, cachedInputUsdPer1M: f64p(0.03), cacheStorageUsdPer1MPerHour: 1, audioInputUsdPer1M: f64p(1),
		flex:                      &geminiTierPrices{inputUsdPer1M: 0.15, outputUsdPer1M: 1.25, cachedInputUsdPer1M: f64p(0.03), audioInputUsdPer1M: f64p(0.5), cacheStorageUsdPer1MPerHour: 1},
		priority:                  &geminiTierPrices{inputUsdPer1M: 0.54, outputUsdPer1M: 4.5, cachedInputUsdPer1M: f64p(0.054), audioInputUsdPer1M: f64p(1.8), cacheStorageUsdPer1MPerHour: 1.8},
		supportedAPIProtocols:     []string{"chat_completions", "generate_content", "stream_generate_content", "count_tokens", "interactions"},
		inputModalities:           []string{"text", "image", "video", "audio"},
		outputModalities:          []string{"text"},
		supportedTools:            []string{"code_execution", "file_search", "function_calling", "google_maps_grounding", "google_search_grounding", "structured_outputs", "url_context"},
		supportedReasoningEfforts: []string{"low", "medium", "high"},
	}),
	geminiTextModel(geminiModelInput{
		model: "gemini-2.5-flash-lite", catalogOrder: 80, releaseDate: "2025-07-22", shutdownDate: "2026-10-16",
		inputUsdPer1M: 0.1, outputUsdPer1M: 0.4, cachedInputUsdPer1M: f64p(0.01), cacheStorageUsdPer1MPerHour: 1, audioInputUsdPer1M: f64p(0.3),
		flex:                      &geminiTierPrices{inputUsdPer1M: 0.05, outputUsdPer1M: 0.2, cachedInputUsdPer1M: f64p(0.01), audioInputUsdPer1M: f64p(0.15), cacheStorageUsdPer1MPerHour: 1},
		priority:                  &geminiTierPrices{inputUsdPer1M: 0.18, outputUsdPer1M: 0.72, cachedInputUsdPer1M: f64p(0.018), audioInputUsdPer1M: f64p(0.54), cacheStorageUsdPer1MPerHour: 1.8},
		supportedAPIProtocols:     []string{"chat_completions", "generate_content", "stream_generate_content", "count_tokens", "interactions"},
		inputModalities:           []string{"text", "image", "video", "audio", "file"},
		outputModalities:          []string{"text"},
		supportedTools:            []string{"code_execution", "file_search", "function_calling", "google_maps_grounding", "google_search_grounding", "structured_outputs", "url_context"},
		supportedReasoningEfforts: []string{"low", "medium", "high"},
	}),
	geminiEmbeddingModel(geminiModelInput{
		model: "gemini-embedding-2", catalogOrder: 100, releaseDate: "2026-04-22",
		inputUsdPer1M:         0.2,
		imageInputUsdPer1M:    f64p(0.45),
		audioInputUsdPer1M:    f64p(6.5),
		maxInputTokens:        intp(8_192),
		supportedAPIProtocols: geminiEmbeddingProtocols,
		inputModalities:       []string{"text", "image", "video", "audio", "file"},
		outputModalities:      []string{"text"},
	}),
}
