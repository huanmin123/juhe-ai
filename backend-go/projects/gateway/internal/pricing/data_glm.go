package pricing

// GLM pricing snapshot, ported from
// backend/src/modules/model-pricing/glm-model-pricing.data.ts (curated
// 2026-08-26). Free models carry explicit zero prices, which stay real
// (non-undefined) prices in the billing semantics.
//
// A2 批模型目录补全（2026-10-05，+10 行：4 行视觉/OCR chat + 2 行 audio +
// 2 行 image + 1 行 video + 1 行 embedding）：智谱官方定价页
// docs.bigmodel.cn 仅人民币明文价（按 token/按万字符/按次/按百万 tokens）。
// 2026-10-05 A3 批用户裁决：不允许无价——本批新增行人民币价按约定汇率
// 7.0 换算落 USD（Source 元信息可溯），官方国际站 USD 明文可查证后覆盖；
// 换算值保留 4 位有效数字，行注释登记「X ÷ 7.0 = Y」可复核。既有 18 行
// z.ai USD 价不动；既有 cogtts/cogvideox-3 两行按同一裁决补换算价（调研
// §2.5：cogvideox-3 官方 1 元/次；cogtts 与 glm-tts 同族现行价 2 元/万
// 字符）。阶梯/分档价落最常用主档（行注释登记调研口径），至此 glm 全部
// 目录行带价（契约 §2.8 兜底仅余上游不回报计量维度的场景）。
var glmModelPricingData = []rawModel{
	{
		Model: "glm-5.3", Mode: "chat", CatalogOrder: intp(0), ReleaseDate: "2026-08-18",
		InputCostPerToken: perToken(1.4), CacheReadInputTokenCost: perToken(0.26), OutputCostPerToken: perToken(4.4),
		ContextWindowTokens: intp(1_000_000), MaxOutputTokens: intp(128_000),
		SupportedAPIProtocols:     []string{"chat_completions"},
		SupportedToolsByProtocol:  toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		SupportsPromptCaching:     true,
		InputModalities:           []string{"text"},
		OutputModalities:          []string{"text"},
		SupportedReasoningEfforts: []string{"low", "high", "max"},
		DefaultReasoningEffort:    "max",
	},
	{
		// glm-5.3-flash / glm-5.3-flashx: thinking.type is locked to
		// "enabled" (compulsive thinking), while reasoning_effort accepts
		// low/high/max with the official default max (bigmodel concept-param
		// enumerates both models; z.ai API reference names GLM-5.3/FLASH).
		Model: "glm-5.3-flash", Mode: "chat", CatalogOrder: intp(5), ReleaseDate: "2026-08-26",
		InputCostPerToken: perToken(0.15), CacheReadInputTokenCost: perToken(0.03), OutputCostPerToken: perToken(0.5),
		ContextWindowTokens: intp(1_000_000), MaxOutputTokens: intp(128_000),
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		SupportsPromptCaching:    true,
		InputModalities:          []string{"text", "image", "video", "file"},
		OutputModalities:         []string{"text"},

		SupportedReasoningEfforts: []string{"low", "high", "max"},
		DefaultReasoningEffort:    "max",
	},
	{
		Model: "glm-5.3-flashx", Mode: "chat", CatalogOrder: intp(6), ReleaseDate: "2026-09-18",
		InputCostPerToken: perToken(0.37), CacheReadInputTokenCost: perToken(0.075), OutputCostPerToken: perToken(1.25),
		ContextWindowTokens: intp(1_000_000), MaxOutputTokens: intp(128_000),
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		SupportsPromptCaching:    true,
		InputModalities:          []string{"text", "image", "video", "file"},
		OutputModalities:         []string{"text"},

		SupportedReasoningEfforts: []string{"low", "high", "max"},
		DefaultReasoningEffort:    "max",
	},
	{
		Model: "glm-5.2", Mode: "chat", CatalogOrder: intp(10), ReleaseDate: "2026-06-16",
		InputCostPerToken: perToken(1.4), CacheReadInputTokenCost: perToken(0.26), OutputCostPerToken: perToken(4.4),
		ContextWindowTokens: intp(1_000_000), MaxOutputTokens: intp(128_000),
		SupportedAPIProtocols:     []string{"chat_completions"},
		SupportedToolsByProtocol:  toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		SupportsPromptCaching:     true,
		InputModalities:           []string{"text"},
		OutputModalities:          []string{"text"},
		SupportedReasoningEfforts: []string{"high", "max"},
		DefaultReasoningEffort:    "max",
	},
	{
		Model: "glm-5.1", Mode: "chat", CatalogOrder: intp(20), ReleaseDate: "2026-04-07",
		InputCostPerToken: perToken(1.4), CacheReadInputTokenCost: perToken(0.26), OutputCostPerToken: perToken(4.4),
		ContextWindowTokens: intp(200_000), MaxOutputTokens: intp(128_000),
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		SupportsPromptCaching:    true,
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"text"},
	},
	{
		Model: "glm-5", Mode: "chat", CatalogOrder: intp(30), ReleaseDate: "2026-02-12",
		InputCostPerToken: perToken(1.0), CacheReadInputTokenCost: perToken(0.2), OutputCostPerToken: perToken(3.2),
		ContextWindowTokens: intp(200_000), MaxOutputTokens: intp(128_000),
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		SupportsPromptCaching:    true,
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"text"},
	},
	{
		Model: "glm-5-turbo", Mode: "chat", CatalogOrder: intp(40), ReleaseDate: "2026-03-16",
		InputCostPerToken: perToken(1.2), CacheReadInputTokenCost: perToken(0.24), OutputCostPerToken: perToken(4.0),
		ContextWindowTokens: intp(200_000), MaxOutputTokens: intp(128_000),
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		SupportsPromptCaching:    true,
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"text"},
	},
	{
		Model: "glm-4.7", Mode: "chat", CatalogOrder: intp(50), ReleaseDate: "2025-12-22",
		InputCostPerToken: perToken(0.6), CacheReadInputTokenCost: perToken(0.11), OutputCostPerToken: perToken(2.2),
		ContextWindowTokens: intp(200_000), MaxOutputTokens: intp(131_072),
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		SupportsPromptCaching:    true,
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"text"},
	},
	{
		Model: "glm-4.7-flashx", Mode: "chat", CatalogOrder: intp(60), ReleaseDate: "2025-12-22",
		InputCostPerToken: perToken(0.07), CacheReadInputTokenCost: perToken(0.01), OutputCostPerToken: perToken(0.4),
		ContextWindowTokens: intp(200_000), MaxOutputTokens: intp(131_072),
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		SupportsPromptCaching:    true,
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"text"},
	},
	{
		Model: "glm-4.7-flash", Mode: "chat", CatalogOrder: intp(70), ReleaseDate: "2025-12-22",
		InputCostPerToken: f64p(0), CacheReadInputTokenCost: f64p(0), OutputCostPerToken: f64p(0),
		ContextWindowTokens: intp(200_000), MaxOutputTokens: intp(131_072),
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		SupportsPromptCaching:    true,
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"text"},
	},
	{
		Model: "glm-4.6", Mode: "chat", CatalogOrder: intp(80), ReleaseDate: "2025-09-30",
		InputCostPerToken: perToken(0.6), CacheReadInputTokenCost: perToken(0.11), OutputCostPerToken: perToken(2.2),
		ContextWindowTokens: intp(200_000), MaxOutputTokens: intp(131_072),
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		SupportsPromptCaching:    true,
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"text"},
	},
	{
		Model: "glm-4.5", Mode: "chat", CatalogOrder: intp(90), ReleaseDate: "2025-07-28",
		InputCostPerToken: perToken(0.6), CacheReadInputTokenCost: perToken(0.11), OutputCostPerToken: perToken(2.2),
		ContextWindowTokens: intp(128_000), MaxOutputTokens: intp(98_304),
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		SupportsPromptCaching:    true,
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"text"},
	},
	{
		Model: "glm-4.5-x", Mode: "chat", CatalogOrder: intp(100), ReleaseDate: "2025-07-28",
		InputCostPerToken: perToken(2.2), CacheReadInputTokenCost: perToken(0.45), OutputCostPerToken: perToken(8.9),
		ContextWindowTokens: intp(128_000), MaxOutputTokens: intp(98_304),
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		SupportsPromptCaching:    true,
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"text"},
	},
	{
		Model: "glm-4.5-air", Mode: "chat", CatalogOrder: intp(110), ReleaseDate: "2025-07-28",
		InputCostPerToken: perToken(0.2), CacheReadInputTokenCost: perToken(0.03), OutputCostPerToken: perToken(1.1),
		ContextWindowTokens: intp(128_000), MaxOutputTokens: intp(98_304),
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		SupportsPromptCaching:    true,
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"text"},
	},
	{
		Model: "glm-4.5-airx", Mode: "chat", CatalogOrder: intp(120), ReleaseDate: "2025-07-28",
		InputCostPerToken: perToken(1.1), CacheReadInputTokenCost: perToken(0.22), OutputCostPerToken: perToken(4.5),
		ContextWindowTokens: intp(128_000), MaxOutputTokens: intp(98_304),
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		SupportsPromptCaching:    true,
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"text"},
	},
	{
		Model: "glm-4.5-flash", Mode: "chat", CatalogOrder: intp(130), ReleaseDate: "2025-07-28",
		InputCostPerToken: f64p(0), CacheReadInputTokenCost: f64p(0), OutputCostPerToken: f64p(0),
		ContextWindowTokens: intp(128_000), MaxOutputTokens: intp(98_304),
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		SupportsPromptCaching:    true,
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"text"},
	},
	// M3 视频增补（媒体设计 §10/契约 §7.1）：cogvideox-3 一行，mode=video、
	// 协议 video、输入 text+image（文生/图生视频）。计价落法（2026-10-05
	// A3 批裁决：不允许无价）：CogVideoX 官方按次计费 1 元/次（调研 §2.5，
	// docs.bigmodel.cn 定价页），网关视频计价面只有秒价维度
	//（VideoOutputCostPerSecond）且 glm 轮询响应不回报时长（§7.1
	// video_result 仅 url/cover_image_url）→ 按次价经约定汇率 7.0 换算
	//（1 ÷ 7.0 = 0.1429）等价登记进秒价槽位：秒计量恒缺、行项不产生，
	// 现行计费行为与 0 计费兜底一致（契约 §2.8）；SourcePricingNote 写明
	// 按次口径——未来若回填 glm 时长提取，须先重审该行价格口径
	//（按次 ≠ 按秒，不得直接按秒连乘）。
	rawModel{
		Model: "cogvideox-3", Mode: "video",
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"video"},
		SupportedAPIProtocols: []string{"video"},

		VideoOutputCostPerSecond: f64p(0.1429),
		SourcePricingCurrency:    "CNY",
		SourceExchangeRateToUsd:  f64p(7.0),
		SourceExchangeRateDate:   "2026-10-05",
		SourcePricingNote:        "官方人民币价 1 元/次（按次计费，docs.bigmodel.cn 定价页），按约定汇率 7.0 换算 $0.1429/次；网关视频计价面仅秒价维度，按次价等价登记（非每秒单价；glm 轮询不回报时长，行项不产生）",
	},
	// M6 语音增补（契约 §7.2 回填 B 级后实施）：cogtts 一行，mode=audio、
	// 协议 audio_speech（openai 透传分支，官方端点 /api/paas/v4/audio/speech，
	// 模型名取官方 api-reference「文本转语音」curl 示例）。计价落法（2026-
	// 10-05 A3 批裁决：不允许无价）：cogtts 与 glm-tts 同族，调研确认 glm 系
	// TTS 现行价 2 元/万字符（docs.bigmodel.cn 定价页）→ 2×10 ÷ 7.0 = 2.857
	//（USD/百万字符）落 TtsInputCostPerChar；上游响应无字符回报（openai
	// 兼容二进制音频流）→ 网关按请求 input 字符自算计量照落（§2.8）。
	// ResponseFormats 不声明：glm 是 openai 兼容透传，response_format 词表
	// 由上游裁决（本地不做零转码门禁，与 gemini/minimax 转换型 adapter 不同）。
	rawModel{
		Model: "cogtts", Mode: "audio",
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"audio"},
		SupportedAPIProtocols: []string{"audio_speech"},

		TtsInputCostPerChar:     perChar(2.857),
		SourcePricingCurrency:   "CNY",
		SourceExchangeRateToUsd: f64p(7.0),
		SourceExchangeRateDate:  "2026-10-05",
		SourcePricingNote:       "官方人民币价 2 元/万字符（与 glm-tts 同族现行价，docs.bigmodel.cn 定价页），按约定汇率 7.0 换算 $2.857/百万字符（2×10÷7.0）",
	},
	// A2 批视觉/OCR 对话（bigmodel OpenAI 兼容 → 协议仍 chat_completions；
	// BUG-0231 口径：chat 行一律声明 function_calling）。
	{
		// 视觉对话：glm-5v-turbo（text+image+video→text，200k 上下文/128k
		// 输出）；0-32K 档换算：输入 5 ÷ 7.0 = 0.7143、输出 22 ÷ 7.0 =
		// 3.143（USD/百万 tokens，4 位有效数字）；≥32K 档 7/26 元/百万
		// tokens 阶梯调研口径注释登记（不结构化）。来源：docs.bigmodel.cn
		// 定价页（2026-10-05 调研）。
		Model: "glm-5v-turbo", Mode: "chat", ReleaseDate: "2026-04-02",
		ContextWindowTokens: intp(200_000), MaxOutputTokens: intp(128_000),
		InputModalities:          []string{"text", "image", "video"},
		OutputModalities:         []string{"text"},
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),

		InputCostPerToken:       perToken(0.7143),
		OutputCostPerToken:      perToken(3.143),
		SourcePricingCurrency:   "CNY",
		SourceExchangeRateToUsd: f64p(7.0),
		SourceExchangeRateDate:  "2026-10-05",
		SourcePricingNote:       "官方人民币价 输入5/输出22 元/百万 tokens（0-32K 档；≥32K 7/26 元/百万 tokens，docs.bigmodel.cn 定价页），按约定汇率 7.0 换算 $0.7143/$3.143 每百万 tokens",
	},
	{
		// 视觉对话：glm-4.6v（text+image→text，128k 上下文/32k 输出）；换算：
		// 输入 1 ÷ 7.0 = 0.1429、输出 3 ÷ 7.0 = 0.4286、缓存读
		// 0.2 ÷ 7.0 = 0.02857（USD/百万 tokens）。来源：docs.bigmodel.cn
		// 定价页（2026-10-05 调研）。
		Model: "glm-4.6v", Mode: "chat", ReleaseDate: "2025-12-08",
		ContextWindowTokens: intp(128_000), MaxOutputTokens: intp(32_000),
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"text"},
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),

		InputCostPerToken:       perToken(0.1429),
		OutputCostPerToken:      perToken(0.4286),
		CacheReadInputTokenCost: perToken(0.02857),
		SourcePricingCurrency:   "CNY",
		SourceExchangeRateToUsd: f64p(7.0),
		SourceExchangeRateDate:  "2026-10-05",
		SourcePricingNote:       "官方人民币价 输入1/输出3/缓存读0.2 元/百万 tokens（docs.bigmodel.cn 定价页），按约定汇率 7.0 换算 $0.1429/$0.4286/$0.02857 每百万 tokens",
	},
	{
		// 视觉对话：glm-4.6v-flashx（text+image→text 快速档，128k 上下文/32k
		// 输出，官方未明文发布日期）；换算：输入 0.15 ÷ 7.0 = 0.02143、输出
		// 1.5 ÷ 7.0 = 0.2143（USD/百万 tokens）。来源：docs.bigmodel.cn
		// 定价页（2026-10-05 调研）。
		Model: "glm-4.6v-flashx", Mode: "chat",
		ContextWindowTokens: intp(128_000), MaxOutputTokens: intp(32_000),
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"text"},
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),

		InputCostPerToken:       perToken(0.02143),
		OutputCostPerToken:      perToken(0.2143),
		SourcePricingCurrency:   "CNY",
		SourceExchangeRateToUsd: f64p(7.0),
		SourceExchangeRateDate:  "2026-10-05",
		SourcePricingNote:       "官方人民币价 输入0.15/输出1.5 元/百万 tokens（docs.bigmodel.cn 定价页），按约定汇率 7.0 换算 $0.02143/$0.2143 每百万 tokens",
	},
	{
		// OCR 对话：glm-ocr（text+image→text 识别专精档，32k 上下文，官方未
		// 明文输出上限）；换算：输入 0.2 ÷ 7.0 = 0.02857、输出
		// 0.2 ÷ 7.0 = 0.02857（USD/百万 tokens）。来源：docs.bigmodel.cn
		// 定价页（2026-10-05 调研）。
		Model: "glm-ocr", Mode: "chat", ReleaseDate: "2026-02-03",
		ContextWindowTokens:      intp(32_000),
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"text"},
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),

		InputCostPerToken:       perToken(0.02857),
		OutputCostPerToken:      perToken(0.02857),
		SourcePricingCurrency:   "CNY",
		SourceExchangeRateToUsd: f64p(7.0),
		SourceExchangeRateDate:  "2026-10-05",
		SourcePricingNote:       "官方人民币价 输入0.2/输出0.2 元/百万 tokens（docs.bigmodel.cn 定价页），按约定汇率 7.0 换算 $0.02857/$0.02857 每百万 tokens",
	},
	{
		// 语音合成：mode=audio、协议 audio_speech；glm-tts（text→audio）为
		// 官方现行 TTS ID（cogtts 为历史行保留）。官方按字符人民币计费：
		// 2 元/万字符 = 20 元/百万字符，20 ÷ 7.0 = 2.857（USD/百万字符）
		// 落 TtsInputCostPerChar。来源：docs.bigmodel.cn 定价页
		//（2026-10-05 调研）。
		Model: "glm-tts", Mode: "audio", ReleaseDate: "2025-12-11",
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"audio"},
		SupportedAPIProtocols: []string{"audio_speech"},

		TtsInputCostPerChar:     perChar(2.857),
		SourcePricingCurrency:   "CNY",
		SourceExchangeRateToUsd: f64p(7.0),
		SourceExchangeRateDate:  "2026-10-05",
		SourcePricingNote:       "官方人民币价 2 元/万字符（docs.bigmodel.cn 定价页），按约定汇率 7.0 换算 $2.857/百万字符（2×10÷7.0）",
	},
	{
		// 语音识别：mode=audio、协议 audio_transcription；glm-asr-2512
		//（audio→text）。官方按 token 人民币计费（输入 16 元/百万 tokens）：
		// 16 ÷ 7.0 = 2.286（USD/百万输入 tokens）落 InputCostPerToken。
		// 来源：docs.bigmodel.cn 定价页（2026-10-05 调研）。
		Model: "glm-asr-2512", Mode: "audio", ReleaseDate: "2025-12-10",
		InputModalities:       []string{"audio"},
		OutputModalities:      []string{"text"},
		SupportedAPIProtocols: []string{"audio_transcription"},

		InputCostPerToken:       perToken(2.286),
		SourcePricingCurrency:   "CNY",
		SourceExchangeRateToUsd: f64p(7.0),
		SourceExchangeRateDate:  "2026-10-05",
		SourcePricingNote:       "官方人民币价 16 元/百万 tokens（输入，docs.bigmodel.cn 定价页），按约定汇率 7.0 换算 $2.286 每百万输入 tokens",
	},
	{
		// 图像生成：glm-image（images 协议，按次计费，每张=每次）；换算：
		// 0.1 ÷ 7.0 = 0.01429（USD/张）落 OutputCostPerImage。来源：
		// docs.bigmodel.cn 定价页（2026-10-05 调研）。
		Model: "glm-image", Mode: "image_generation", ReleaseDate: "2026-01-14",
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"image"},
		SupportedAPIProtocols: []string{"images"},

		OutputCostPerImage:      f64p(0.01429),
		SourcePricingCurrency:   "CNY",
		SourceExchangeRateToUsd: f64p(7.0),
		SourceExchangeRateDate:  "2026-10-05",
		SourcePricingNote:       "官方人民币价 0.1 元/次（按次计费，每张=每次，docs.bigmodel.cn 定价页），按约定汇率 7.0 换算 $0.01429/张",
	},
	{
		// 图像生成：cogview-4（images 协议，按次计费，text→image，每张=
		// 每次）；换算：0.06 ÷ 7.0 = 0.008571（USD/张，4 位有效数字）落
		// OutputCostPerImage。来源：docs.bigmodel.cn 定价页（2026-10-05
		// 调研）。
		Model: "cogview-4", Mode: "image_generation",
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"image"},
		SupportedAPIProtocols: []string{"images"},

		OutputCostPerImage:      f64p(0.008571),
		SourcePricingCurrency:   "CNY",
		SourceExchangeRateToUsd: f64p(7.0),
		SourceExchangeRateDate:  "2026-10-05",
		SourcePricingNote:       "官方人民币价 0.06 元/次（按次计费，每张=每次，docs.bigmodel.cn 定价页），按约定汇率 7.0 换算 $0.008571/张",
	},
	{
		// 视频生成：cogvideox-2（text+image→video，协议 video，按次计费）；
		// 官方 0.5 元/次（docs.bigmodel.cn 定价页）经约定汇率换算
		//（0.5 ÷ 7.0 = 0.07143）等价登记进秒价槽位（同 cogvideox-3 口径）：
		// glm 轮询响应不回报时长（§7.1）→ 行项不产生，现行计费行为与
		// 0 计费兜底一致（契约 §2.8）；按次口径见 SourcePricingNote。
		Model: "cogvideox-2", Mode: "video",
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"video"},
		SupportedAPIProtocols: []string{"video"},

		VideoOutputCostPerSecond: f64p(0.07143),
		SourcePricingCurrency:    "CNY",
		SourceExchangeRateToUsd:  f64p(7.0),
		SourceExchangeRateDate:   "2026-10-05",
		SourcePricingNote:        "官方人民币价 0.5 元/次（按次计费，docs.bigmodel.cn 定价页），按约定汇率 7.0 换算 $0.07143/次；网关视频计价面仅秒价维度，按次价等价登记（非每秒单价；glm 轮询不回报时长，行项不产生）",
	},
	{
		// 向量嵌入：mode=embedding、协议 embed_content（对照 gemini-embedding
		// 行写法）；embedding-3（text→向量，8k 输入上下文、2048 维，维度数
		// 无结构化字段仅注释登记）。官方人民币 0.5 元/百万 tokens：0.5 ÷ 7.0
		// = 0.07143（USD/百万 tokens）落 InputCostPerToken。来源：
		// docs.bigmodel.cn 定价页（2026-10-05 调研）。
		Model: "embedding-3", Mode: "embedding",
		MaxInputTokens:        intp(8_192),
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"text"},
		SupportedAPIProtocols: []string{"embed_content"},

		InputCostPerToken:       perToken(0.07143),
		SourcePricingCurrency:   "CNY",
		SourceExchangeRateToUsd: f64p(7.0),
		SourceExchangeRateDate:  "2026-10-05",
		SourcePricingNote:       "官方人民币价 0.5 元/百万 tokens（docs.bigmodel.cn 定价页），按约定汇率 7.0 换算 $0.07143 每百万 tokens",
	},
}
