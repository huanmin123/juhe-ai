package pricing

// Gemini pricing snapshot, ported from
// backend/src/modules/model-pricing/gemini-model-pricing.data.ts (curated
// 2026-08-26). The Node textModel/embeddingModel factories are preserved:
// per-million inputs convert through the same runtime /1e6 division and the
// fixed max_input/max_output defaults stay identical.
//
// 2026-10-05 全厂商补全批（依据 docs/plans/计划-20261005T110000000Z-模型
// 目录全厂商补全与场景化展示.md §3.1/§3.2/§2，来源 = 计划 §6.1：
// ai.google.dev/gemini-api/docs 的 models / pricing / deprecations 页）：
// 新增 6 行（veo-3.1-generate-preview / veo-3.1-lite-generate-preview 视频、
// gemini-3.8-live 与 gemini-omni-1.1-flash 实时音频对话、
// gemini-3.8-flash-tts / gemini-3.8-flash-lite-tts TTS）；修正 2 行
// （veo-3.0 两行补 ShutdownDate 2026-06-30，deprecations 页明文，继任者
// veo-3.1）。gemini-3.6-flash 现价转写级不符（§2.2 低置信）不落，登记
// 计划 §6.3 待复核。分辨率分档（veo 720p/1080p/4k）沿 M2 sora 先例单值
// 落主档、分档注释登记，不结构化。
//
// 2026-10-05 复核批（官方 pricing/deprecations 页原文核对）：修正——
// 2.5 系三行补 cache write 明文价（pro $0.125 / flash $0.03 / flash-lite
// $0.01，≤200k 主档）、veo-3.1 两行补 ShutdownDate 2026-10-22、
// 3.6-flash 注释登记 2027-01-01 起官方明文价、embedding-2 注释登记 video
// input $12.00；新增 4 行（gemini-3.1-flash-image / gemini-3-pro-image
// 图像、gemini-3.5-transcribe / gemini-3.5-transcribe-live 转写）。
// 3.6-flash 现价 $0.75/$3.75 复核确认正确；3.8-live-extended-thinking
// 官方无单独加价不落；3.5-flash >200k 档官方无此档确认未落。
//
// 2026-10-05 核对批（两路独立复核代理对官方定价页原文交叉回正）：
// gemini-3.8-live $0.75/$4.50 + audio $3/$12 + image input $1.00（原误引
// $3.75/$1.5/$6）；3.8 TTS 两行回正（flash-tts $0.50/$9.00、lite-tts
// $0.50/$6.00，含 2027-01-01 起翻倍明文注释）；图像两行补 text 输出通道
// （3.1-flash-image input 回正 $0.50 + output $3.00；3-pro-image output
// $12.00）；3.5-flash/3.5-flash-lite priority 缓存存储回正 $1.0/hr、
// 3.5-flash flex 缓存读回正 $0.075（batch $0.08 注释登记）；veo-3.0 两行
// ReleaseDate/ShutdownDate 按 deprecations preview 条目回正
// （2025-07-31 / 2025-11-12，2026-06-30 属 GA -001 版）；TTS 四行文件序
// 108/109/110/111 升序（纯布局移动）。gemini-omni-1.1-flash 官方
// GA/Preview 两表未区分，存疑不动（登记计划待复核清单）。
type geminiTierPrices struct {
	inputUsdPer1M               float64
	outputUsdPer1M              float64
	cachedInputUsdPer1M         *float64
	cacheStorageUsdPer1MPerHour float64
	audioInputUsdPer1M          *float64
}

type geminiModelInput struct {
	model               string
	catalogOrder        int
	releaseDate         string
	shutdownDate        string
	inputUsdPer1M       float64
	outputUsdPer1M      float64
	cachedInputUsdPer1M *float64
	// cacheWriteUsdPer1M：官方 context cache 明文写入价（2026-10-05 复核批
	// 新增通道；2.5 系三行落 ≤200k 主档，>200k 档按行注释登记不结构化）。
	cacheWriteUsdPer1M          *float64
	cacheStorageUsdPer1MPerHour float64
	audioInputUsdPer1M          *float64
	// audioOutputUsdPer1M：实时音频对话行（Live/omni）的音频输出 token 价
	// （2026-10-05 全厂商补全批新增通道；TTS 行沿用 outputUsdPer1M 语义）。
	audioOutputUsdPer1M             *float64
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
	// 未声明缓存存储价的行不落 0：PriceSet 语义里非 nil 0 是"真实免费"价
	//（glm-4.7-flash 先例），Live/omni 等无缓存明文的行不编造（存量行均
	// 显式非 0，行为不变）。SupportsPromptCaching 同理以缓存读价存在为准。
	var cacheStoragePerHour *float64
	if in.cacheStorageUsdPer1MPerHour != 0 {
		cacheStoragePerHour = perToken(in.cacheStorageUsdPer1MPerHour)
	}
	out := rawModel{
		Model: in.model, Mode: "chat", CatalogOrder: &in.catalogOrder, ReleaseDate: in.releaseDate,
		InputCostPerToken:                         perToken(in.inputUsdPer1M),
		OutputCostPerToken:                        perToken(in.outputUsdPer1M),
		CacheReadInputTokenCost:                   usdPerTokenPtr(in.cachedInputUsdPer1M),
		CacheCreationInputTokenCost:               usdPerTokenPtr(in.cacheWriteUsdPer1M),
		CacheStorageInputTokenCostPerHour:         cacheStoragePerHour,
		InputCostPerAudioToken:                    usdPerTokenPtr(in.audioInputUsdPer1M),
		OutputCostPerAudioToken:                   usdPerTokenPtr(in.audioOutputUsdPer1M),
		InputCostPerImageToken:                    usdPerTokenPtr(in.imageInputUsdPer1M),
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
		// 「协议 × 工具」矩阵：google_search_grounding / code_execution 等
		// 原生（hosted）工具归 generate_content / stream_generate_content /
		// interactions 原生协议；OpenAI 兼容的 chat_completions 只携带
		// function_calling；count_tokens 是计数协议，不携带工具键。
		SupportedToolsByProtocol:  toolsByProtocol(in.supportedAPIProtocols, in.supportedTools),
		SupportedReasoningEfforts: in.supportedReasoningEfforts,
		DefaultReasoningEffort:    in.defaultReasoningEffort,
		// SupportsPromptCaching 由下方以缓存读价存在为准（见函数头注释）。
	}
	visible := true
	out.CatalogVisible = &visible
	out.SupportsPromptCaching = in.cachedInputUsdPer1M != nil
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

// geminiTTSModel 是 M1 同步音频的 TTS 工厂（mode=audio、协议 audio_speech）：
// 官方按 token 计价——text input 单价 + audio output 单价，计费沿既有
// usageMetadata token 链路（音频设计 §9/契约 §5.1）。输出恒为裸 PCM
// （24kHz/16bit/mono），ResponseFormats 固定 ["pcm"]（契约 §5.1 裁决：
// 其余格式 400，零转码）。
func geminiTTSModel(in geminiModelInput) rawModel {
	out := rawModel{
		Model: in.model, Mode: "audio", CatalogOrder: &in.catalogOrder, ReleaseDate: in.releaseDate,
		InputCostPerToken:       perToken(in.inputUsdPer1M),
		OutputCostPerAudioToken: perToken(in.outputUsdPer1M),
		SupportedAPIProtocols:   in.supportedAPIProtocols,
		InputModalities:         in.inputModalities,
		OutputModalities:        in.outputModalities,
		ResponseFormats:         []string{"pcm"},
	}
	visible := true
	out.CatalogVisible = &visible
	return out
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
		SupportedToolsByProtocol:  toolsByProtocol(in.supportedAPIProtocols, in.supportedTools),
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
		// 2026-10-05 新增（官方定价页明文）：omni 全模态实时对话，text
		// $0.50/$3.00、audio in $1.00 / audio out $5.00、image in $0.50
		// per 1M tokens。mode=chat；协议 generate_content +
		// stream_generate_content（Live/omni 会话面不经 OpenAI 兼容
		// chat_completions/count_tokens/interactions），工具清单对照现役
		// chat 行。官方无缓存/档位明文，缓存字段与档位不落。
		model: "gemini-omni-1.1-flash", catalogOrder: -3,
		inputUsdPer1M: 0.5, outputUsdPer1M: 3,
		audioInputUsdPer1M: f64p(1), audioOutputUsdPer1M: f64p(5),
		imageInputUsdPer1M:    f64p(0.5),
		supportedAPIProtocols: []string{"generate_content", "stream_generate_content"},
		inputModalities:       []string{"text", "image", "audio"},
		outputModalities:      []string{"text", "audio"},
		supportedTools:        []string{"code_execution", "file_search", "function_calling", "google_maps_grounding", "google_search_grounding", "structured_outputs", "url_context", "computer_use"},
	}),
	geminiTextModel(geminiModelInput{
		// 2026-10-05 核对批回正（两路独立复核官方定价页原句）：Input
		// $0.75 (text) / $3.00 or $0.005/min (audio) / $1.00 or $0.002/min
		// (image+video)；Output $4.50 (text) / $12.00 or $0.018/min (audio)
		// per 1M tokens。image+video 输入 $1.00 落 image 通道（分钟价
		// $0.002/min 目录无音频/图像分钟通道，注释登记）；协议/工具对照
		// gemini-omni-1.1-flash 同款（Live 会话面）。
		model: "gemini-3.8-live", catalogOrder: -2,
		inputUsdPer1M: 0.75, outputUsdPer1M: 4.5,
		audioInputUsdPer1M: f64p(3), audioOutputUsdPer1M: f64p(12),
		imageInputUsdPer1M:    f64p(1),
		supportedAPIProtocols: []string{"generate_content", "stream_generate_content"},
		inputModalities:       []string{"text", "image", "audio"},
		outputModalities:      []string{"text", "audio"},
		supportedTools:        []string{"code_execution", "file_search", "function_calling", "google_maps_grounding", "google_search_grounding", "structured_outputs", "url_context", "computer_use"},
	}),
	geminiTextModel(geminiModelInput{
		// Promotional prices through 2026-12-31; everything doubles on
		// 2027-01-01 (input/output $1.50/$7.50, cache read $0.15, storage
		// $1.00/hr, priority $2.70/$13.50, flex $0.75/$3.75). Official
		// pricing page, checked 2026-09-23; re-check by 2027-01-01.
		model: "gemini-3.8-flash", catalogOrder: -1, releaseDate: "2026-09-02",
		inputUsdPer1M: 0.75, outputUsdPer1M: 3.75, cachedInputUsdPer1M: f64p(0.075), cacheStorageUsdPer1MPerHour: 0.5,
		flex:                      &geminiTierPrices{inputUsdPer1M: 0.375, outputUsdPer1M: 1.875, cachedInputUsdPer1M: f64p(0.0375), cacheStorageUsdPer1MPerHour: 0.5},
		priority:                  &geminiTierPrices{inputUsdPer1M: 1.35, outputUsdPer1M: 6.75, cachedInputUsdPer1M: f64p(0.135), cacheStorageUsdPer1MPerHour: 0.5},
		supportedAPIProtocols:     []string{"chat_completions", "generate_content", "stream_generate_content", "count_tokens", "interactions"},
		inputModalities:           []string{"text", "image", "video", "audio", "file"},
		outputModalities:          []string{"text"},
		supportedTools:            []string{"code_execution", "file_search", "function_calling", "google_maps_grounding", "google_search_grounding", "structured_outputs", "url_context", "computer_use"},
		supportedReasoningEfforts: []string{"low", "medium", "high"},
		defaultReasoningEffort:    "medium",
	}),
	geminiTextModel(geminiModelInput{
		// Promotional prices through 2026-12-31, doubling on 2027-01-01 like
		// gemini-3.8-flash above.
		model: "gemini-3.7-flash", catalogOrder: 0, releaseDate: "2026-08-13",
		inputUsdPer1M: 0.75, outputUsdPer1M: 3.75, cachedInputUsdPer1M: f64p(0.075), cacheStorageUsdPer1MPerHour: 0.5,
		flex:                      &geminiTierPrices{inputUsdPer1M: 0.375, outputUsdPer1M: 1.875, cachedInputUsdPer1M: f64p(0.0375), cacheStorageUsdPer1MPerHour: 0.5},
		priority:                  &geminiTierPrices{inputUsdPer1M: 1.35, outputUsdPer1M: 6.75, cachedInputUsdPer1M: f64p(0.135), cacheStorageUsdPer1MPerHour: 0.5},
		supportedAPIProtocols:     []string{"chat_completions", "generate_content", "stream_generate_content", "count_tokens", "interactions"},
		inputModalities:           []string{"text", "image"},
		outputModalities:          []string{"text"},
		supportedTools:            []string{"code_execution", "file_search", "function_calling", "google_maps_grounding", "google_search_grounding", "structured_outputs", "url_context", "computer_use"},
		supportedReasoningEfforts: []string{"low", "medium", "high"},
		defaultReasoningEffort:    "medium",
	}),
	geminiTextModel(geminiModelInput{
		// Promotional prices through 2026-12-31, mirroring gemini-3.7-flash
		// (doubles on 2027-01-01). 2026-10-05 复核批：$0.75/$3.75 现值复核
		// 确认正确；官方定价页明文 2027-01-01 起 input $1.50 / output
		// $7.50 / cache $0.15，Priority 档 $1.35/$6.75（届时需按明文改价
		// 并复核 storage/flex 档）。
		model: "gemini-3.6-flash", catalogOrder: 1, releaseDate: "2026-07-21",
		inputUsdPer1M: 0.75, outputUsdPer1M: 3.75, cachedInputUsdPer1M: f64p(0.075), cacheStorageUsdPer1MPerHour: 0.5,
		flex:                      &geminiTierPrices{inputUsdPer1M: 0.375, outputUsdPer1M: 1.875, cachedInputUsdPer1M: f64p(0.0375), cacheStorageUsdPer1MPerHour: 0.5},
		priority:                  &geminiTierPrices{inputUsdPer1M: 1.35, outputUsdPer1M: 6.75, cachedInputUsdPer1M: f64p(0.135), cacheStorageUsdPer1MPerHour: 0.5},
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
		flex: &geminiTierPrices{inputUsdPer1M: 0.15, outputUsdPer1M: 1.25, cachedInputUsdPer1M: f64p(0.02), cacheStorageUsdPer1MPerHour: 1},
		// 2026-10-05 核对批回正：priority 档缓存存储 $1.0/hr（补全批误引
		// $1.8，官方定价页 priority 段明文 $1.00）。
		priority:                  &geminiTierPrices{inputUsdPer1M: 0.54, outputUsdPer1M: 4.5, cachedInputUsdPer1M: f64p(0.05), cacheStorageUsdPer1MPerHour: 1},
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
		// 2026-10-05 核对批回正：flex 档缓存读 $0.075（补全批误引 $0.08，
		// 官方定价页 flex 段明文 $0.075；Batch 档 $0.08 为独立明文价，目录
		// 无 batch 档通道，注释登记）；priority 档缓存存储 $1.0/hr（误引
		// $1.8 同上）。
		flex:                      &geminiTierPrices{inputUsdPer1M: 0.75, outputUsdPer1M: 4.5, cachedInputUsdPer1M: f64p(0.075), cacheStorageUsdPer1MPerHour: 1},
		priority:                  &geminiTierPrices{inputUsdPer1M: 2.7, outputUsdPer1M: 16.2, cachedInputUsdPer1M: f64p(0.27), cacheStorageUsdPer1MPerHour: 1},
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
		defaultReasoningEffort:    "minimal",
	}),
	geminiTextModel(geminiModelInput{
		// 2026-10-05 复核批补 cache write：官方定价页明文 context cache 写入
		// $0.125（≤200k 主档；>200k 档 $0.25，分档不结构化只注释登记）。
		model: "gemini-2.5-pro", catalogOrder: 60, releaseDate: "2025-06-17",
		inputUsdPer1M: 1.25, outputUsdPer1M: 10, cachedInputUsdPer1M: f64p(0.125), cacheWriteUsdPer1M: f64p(0.125), cacheStorageUsdPer1MPerHour: 4.5,
		flex:                            &geminiTierPrices{inputUsdPer1M: 0.625, outputUsdPer1M: 5, cachedInputUsdPer1M: f64p(0.125), cacheStorageUsdPer1MPerHour: 4.5},
		priority:                        &geminiTierPrices{inputUsdPer1M: 2.25, outputUsdPer1M: 18, cachedInputUsdPer1M: f64p(0.225), cacheStorageUsdPer1MPerHour: 8.1},
		longContextInputTokenThreshold:  intp(200_000),
		longContextInputCostMultiplier:  f64p(2),
		longContextOutputCostMultiplier: f64p(1.5),
		supportedAPIProtocols:           []string{"chat_completions", "generate_content", "stream_generate_content", "count_tokens", "interactions"},
		inputModalities:                 []string{"text", "image", "video", "audio", "file"},
		outputModalities:                []string{"text"},
		supportedTools:                  []string{"code_execution", "file_search", "function_calling", "google_maps_grounding", "google_search_grounding", "structured_outputs", "url_context"},
		// 2.5 系官方不支持 thinkingLevel（仅 thinkingBudget，128-32768 且不可关闭），
		// 不记录 effort 档位。
	}),
	geminiTextModel(geminiModelInput{
		// 2026-10-05 复核批补 cache write：官方定价页明文 context cache 写入
		// $0.03（audio 档 $0.10，无 audio cache write 字段只注释登记）。
		model: "gemini-2.5-flash", catalogOrder: 70, releaseDate: "2025-06-17",
		inputUsdPer1M: 0.3, outputUsdPer1M: 2.5, cachedInputUsdPer1M: f64p(0.03), cacheWriteUsdPer1M: f64p(0.03), cacheStorageUsdPer1MPerHour: 1, audioInputUsdPer1M: f64p(1),
		flex:                  &geminiTierPrices{inputUsdPer1M: 0.15, outputUsdPer1M: 1.25, cachedInputUsdPer1M: f64p(0.03), audioInputUsdPer1M: f64p(0.5), cacheStorageUsdPer1MPerHour: 1},
		priority:              &geminiTierPrices{inputUsdPer1M: 0.54, outputUsdPer1M: 4.5, cachedInputUsdPer1M: f64p(0.054), audioInputUsdPer1M: f64p(1.8), cacheStorageUsdPer1MPerHour: 1.8},
		supportedAPIProtocols: []string{"chat_completions", "generate_content", "stream_generate_content", "count_tokens", "interactions"},
		inputModalities:       []string{"text", "image", "video", "audio"},
		outputModalities:      []string{"text"},
		supportedTools:        []string{"code_execution", "file_search", "function_calling", "google_maps_grounding", "google_search_grounding", "structured_outputs", "url_context"},
		// 2.5 系官方不支持 thinkingLevel（仅 thinkingBudget，0-24576），不记录 effort 档位。
	}),
	geminiTextModel(geminiModelInput{
		// 2026-10-05 复核批补 cache write：官方定价页明文 context cache 写入
		// $0.01（audio 档 $0.03，无 audio cache write 字段只注释登记）。
		model: "gemini-2.5-flash-lite", catalogOrder: 80, releaseDate: "2025-07-22",
		inputUsdPer1M: 0.1, outputUsdPer1M: 0.4, cachedInputUsdPer1M: f64p(0.01), cacheWriteUsdPer1M: f64p(0.01), cacheStorageUsdPer1MPerHour: 1, audioInputUsdPer1M: f64p(0.3),
		flex:                  &geminiTierPrices{inputUsdPer1M: 0.05, outputUsdPer1M: 0.2, cachedInputUsdPer1M: f64p(0.01), audioInputUsdPer1M: f64p(0.15), cacheStorageUsdPer1MPerHour: 1},
		priority:              &geminiTierPrices{inputUsdPer1M: 0.18, outputUsdPer1M: 0.72, cachedInputUsdPer1M: f64p(0.018), audioInputUsdPer1M: f64p(0.54), cacheStorageUsdPer1MPerHour: 1.8},
		supportedAPIProtocols: []string{"chat_completions", "generate_content", "stream_generate_content", "count_tokens", "interactions"},
		inputModalities:       []string{"text", "image", "video", "audio", "file"},
		outputModalities:      []string{"text"},
		supportedTools:        []string{"code_execution", "file_search", "function_calling", "google_maps_grounding", "google_search_grounding", "structured_outputs", "url_context"},
		// 2.5 系官方不支持 thinkingLevel（仅 thinkingBudget，512-24576 且 =0 关闭），
		// 不记录 effort 档位。
	}),
	geminiEmbeddingModel(geminiModelInput{
		// 2026-10-05 复核批注释补齐：官方定价页明文 video input $12.00/1M
		// tokens（目录无 video input 价格字段，只注释登记不落字段）。
		model: "gemini-embedding-2", catalogOrder: 100, releaseDate: "2026-04-22",
		inputUsdPer1M:         0.2,
		imageInputUsdPer1M:    f64p(0.45),
		audioInputUsdPer1M:    f64p(6.5),
		maxInputTokens:        intp(8_192),
		supportedAPIProtocols: geminiEmbeddingProtocols,
		inputModalities:       []string{"text", "image", "video", "audio", "file"},
		outputModalities:      []string{"text"},
	}),
	// M1 同步音频 TTS（音频设计 §9）：价格取自 Gemini 官方定价页
	// ai.google.dev/gemini-api/docs/pricing（核实于 2026-10-04）——
	// flash-preview-tts text input $0.50 / audio output $10.00 per 1M tokens、
	// pro-preview-tts $1.00 / $20.00。发布 2025-05-20（Google I/O 2025）。
	geminiTTSModel(geminiModelInput{
		model: "gemini-2.5-flash-preview-tts", catalogOrder: 110, releaseDate: "2025-05-20",
		inputUsdPer1M:         0.5,
		outputUsdPer1M:        10,
		supportedAPIProtocols: []string{"audio_speech"},
		inputModalities:       []string{"text"},
		outputModalities:      []string{"audio"},
	}),
	geminiTTSModel(geminiModelInput{
		model: "gemini-2.5-pro-preview-tts", catalogOrder: 111, releaseDate: "2025-05-20",
		inputUsdPer1M:         1,
		outputUsdPer1M:        20,
		supportedAPIProtocols: []string{"audio_speech"},
		inputModalities:       []string{"text"},
		outputModalities:      []string{"audio"},
	}),
	// 2026-10-05 全厂商补全批新增（官方定价页明文）：gemini-3.8 TTS 两行，
	// geminiTTSModel 工厂（audio_speech + ResponseFormats ["pcm"]，对照
	// gemini-2.5 tts 行字段）。官方无独立发布日明文，ReleaseDate 留空。
	// 2026-10-05 核对批回正：官方定价页明文 3.8-flash-tts $0.50 / $9.00
	//（2027-01-01 起 $1.00/$18.00）、3.8-flash-lite-tts $0.50 / $6.00
	//（2027 起 $1.00/$12.00）per 1M tokens——补全批误引 $0.75/$6 与
	// $0.30/$3，本轮以页面原文回正。文件序按 catalogOrder 108/109/110/111
	// 升序排布（纯布局移动，2.5 两行值不变）。
	geminiTTSModel(geminiModelInput{
		model: "gemini-3.8-flash-tts", catalogOrder: 108,
		inputUsdPer1M:         0.5,
		outputUsdPer1M:        9,
		supportedAPIProtocols: []string{"audio_speech"},
		inputModalities:       []string{"text"},
		outputModalities:      []string{"audio"},
	}),
	geminiTTSModel(geminiModelInput{
		model: "gemini-3.8-flash-lite-tts", catalogOrder: 109,
		inputUsdPer1M:         0.5,
		outputUsdPer1M:        6,
		supportedAPIProtocols: []string{"audio_speech"},
		inputModalities:       []string{"text"},
		outputModalities:      []string{"audio"},
	}),
	// M3 视频增补（媒体设计 §10/契约 §5.2）：Veo 3 两行，mode=video、协议
	// video。价格取 Gemini 官方定价页 ai.google.dev/gemini-api/docs/pricing
	// Veo 段（核实于 2026-10-04）——veo-3.0-generate-preview 720p $0.40/秒、
	// veo-3.0-fast-generate-preview 720p $0.15/秒（1080p 分档价机制本任务
	// 不建，沿 M2 sora 先例：单值按 720p 基准档落价，1080p 分档随 M3+ 计价
	// 完善扩展）。Veo operation 响应不回报输出秒数且 seconds 属 ignored
	// 参数（veo adapter Capabilities），网关当前无计量基源——终态计费走
	// 契约 §2.8 兜底（0 计费 + usage_missing），目录秒价留待 Veo 回报时长
	// 字段（或固定档裁决）后生效。发布 2025-07-31（官方 deprecations 页
	// preview 版条目）。
	// 2026-10-05 核对批回正：官方 deprecations 页区分 preview 与 GA 两表
	//——preview 版（本目录两行 ID）Retired November 12, 2025，GA -001 版
	// 才是 2026-06-30 终止支持；补全批把 GA 日期误挂到 preview 行，本轮
	// ReleaseDate/ShutdownDate 按官方 preview 条目回正（preview ID 不改名，
	// GA 日期区分登记于此）。
	rawModel{
		Model: "veo-3.0-generate-preview", Mode: "video", ReleaseDate: "2025-07-31", ShutdownDate: "2025-11-12",
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"video"},
		SupportedAPIProtocols:    []string{"video"},
		VideoOutputCostPerSecond: f64p(0.40),
	},
	rawModel{
		Model: "veo-3.0-fast-generate-preview", Mode: "video", ReleaseDate: "2025-07-31", ShutdownDate: "2025-11-12",
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"video"},
		SupportedAPIProtocols:    []string{"video"},
		VideoOutputCostPerSecond: f64p(0.15),
	},
	// 2026-10-05 全厂商补全批新增（官方定价页 Veo 段明文）：veo-3.1 系在售，
	// mode=video、协议 video，秒价落主档；分辨率分档注释登记（机制沿 M2
	// 先例不结构化）。官方无独立发布日明文，ReleaseDate 留空。
	// 2026-10-05 复核批：官方 deprecations 页明文两行 2026-10-22 终止支持，
	// ShutdownDate 落官方明文日期（裁决 §2.3）。
	rawModel{
		// veo-3.1：720p/1080p 均 $0.40/秒（主档 $0.40）；4k 档 $0.60/秒
		// 分档注释登记，不结构化。
		Model: "veo-3.1-generate-preview", Mode: "video", ShutdownDate: "2026-10-22",
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"video"},
		SupportedAPIProtocols:    []string{"video"},
		VideoOutputCostPerSecond: f64p(0.40),
	},
	rawModel{
		// veo-3.1-lite：$0.05/秒（主档）；1080p 档 $0.08/秒分档注释登记，
		// 不结构化。
		Model: "veo-3.1-lite-generate-preview", Mode: "video", ShutdownDate: "2026-10-22",
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"video"},
		SupportedAPIProtocols:    []string{"video"},
		VideoOutputCostPerSecond: f64p(0.05),
	},
	// 2026-10-05 复核批新增 4 行（官方定价页明文，四路复核批 §2.5）：图像
	// 2 行 + 转写 2 行。CatalogOrder 沿现役惯例（embedding 100、TTS 108+
	// 之后的专用档位段）；官方无发布日明文，ReleaseDate 置空（nil）。
	rawModel{
		// Nano Banana 2 图像生成：mode=image_generation（用途分类），走
		// generate_content 出图（协议对照现役 gemini 行 generate_content +
		// stream_generate_content 双协议写法）。2026-10-05 核对批回正（官方
		// 定价页原句）：Input $0.50 (text/image)——text 与 image 输入同价，
		// 共用模态无关 token 通道；Output $3.00 (text and thinking) +
		// $60.00 (images)——文本/thinking 输出落 OutputCostPerToken、image
		// token 输出落 image 通道（补全批误引 text input $3.00 且漏 text
		// 输出通道）。images $60.00/1M image tokens 等价 $0.045/0.5K 张、
		// $0.067/1K、$0.101/2K、$0.151/4K（注释登记，不建每张价）。
		Model: "gemini-3.1-flash-image", Mode: "image_generation", CatalogOrder: intp(101),
		InputModalities:         []string{"text", "image"},
		OutputModalities:        []string{"text", "image"},
		SupportedAPIProtocols:   []string{"generate_content", "stream_generate_content"},
		InputCostPerToken:       perToken(0.5),
		OutputCostPerToken:      perToken(3),
		OutputCostPerImageToken: perToken(60),
	},
	rawModel{
		// Nano Banana Pro 图像生成：2026-10-05 核对批补 text 输出通道（官方
		// 原句 Output $12.00 (text and thinking) + $120.00 (images)——补全批
		// 只落 image 输出通道）。text input $2.00/1M（image input 亦
		// $2.00/1M，注释登记）；images $120.00/1M image tokens——等价
		// $0.134/1K-2K 张、$0.24/4K（注释登记）。
		Model: "gemini-3-pro-image", Mode: "image_generation", CatalogOrder: intp(102),
		InputModalities:         []string{"text", "image"},
		OutputModalities:        []string{"text", "image"},
		SupportedAPIProtocols:   []string{"generate_content", "stream_generate_content"},
		InputCostPerToken:       perToken(2),
		OutputCostPerToken:      perToken(12),
		OutputCostPerImageToken: perToken(120),
	},
	rawModel{
		// 同步音频转写：mode=audio、协议 audio_transcription（对照
		// grok-voice-transcribe-2.0 行字段）。audio input $2.00/1M tokens +
		// $0.003/min 明文直除每秒（usdPerMinuteToPerSecond）、text output
		// $12.00/1M tokens。
		Model: "gemini-3.5-transcribe", Mode: "audio", CatalogOrder: intp(112),
		InputModalities:         []string{"audio"},
		OutputModalities:        []string{"text"},
		SupportedAPIProtocols:   []string{"audio_transcription"},
		InputCostPerAudioToken:  perToken(2),
		AudioInputCostPerSecond: usdPerMinuteToPerSecond(0.003),
		OutputCostPerToken:      perToken(12),
	},
	rawModel{
		// 实时转写会话面：mode=audio、协议 realtime。audio input $3.50/1M
		// tokens + $0.005/min 明文直除每秒；output $21.00/1M tokens（文本）。
		Model: "gemini-3.5-transcribe-live", Mode: "audio", CatalogOrder: intp(113),
		InputModalities:         []string{"audio"},
		OutputModalities:        []string{"text"},
		SupportedAPIProtocols:   []string{"realtime"},
		InputCostPerAudioToken:  perToken(3.5),
		AudioInputCostPerSecond: usdPerMinuteToPerSecond(0.005),
		OutputCostPerToken:      perToken(21),
	},
}
