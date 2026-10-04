package pricing

// GLM pricing snapshot, ported from
// backend/src/modules/model-pricing/glm-model-pricing.data.ts (curated
// 2026-08-26). Free models carry explicit zero prices, which stay real
// (non-undefined) prices in the billing semantics.
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
		SupportsPromptCaching:     true,
		InputModalities:           []string{"text"},
		OutputModalities:          []string{"text"},
	},
	// M3 视频增补（媒体设计 §10/契约 §7.1）：cogvideox-3 一行，mode=video、
	// 协议 video、输入 text+image（文生/图生视频）。计价落法（不编造）：
	// CogVideoX 按次计费（2026-09 第三方聚合口径称 ¥1/次，官方定价页
	// bigmodel.cn 为 SPA 无法直接核实精确单价），网关计费面只有秒价维度
	//（VideoOutputCostPerSecond），且 glm 轮询响应不回报时长（§7.1
	// video_result 仅 url/cover_image_url）——本行不落秒价，终态计费走契约
	// §2.8 兜底（0 计费 + usage_missing 标记）；官方秒价/时长口径可查证后
	// 再补 VideoOutputCostPerSecond。
	rawModel{
		Model: "cogvideox-3", Mode: "video",
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"video"},
		SupportedAPIProtocols: []string{"video"},
	},
	// M6 语音增补（契约 §7.2 回填 B 级后实施）：cogtts 一行，mode=audio、
	// 协议 audio_speech（openai 透传分支，官方端点 /api/paas/v4/audio/speech，
	// 模型名取官方 api-reference「文本转语音」curl 示例）。计价落法（不
	// 编造）：官方按字符计费、定价页无可查证 USD 字符价——不落
	// TtsInputCostPerChar；上游响应无字符回报（openai 兼容二进制音频流）→
	// 网关按请求 input 字符自算计量照落（§2.8），成本不虚计（0 计费）。
	// ResponseFormats 不声明：glm 是 openai 兼容透传，response_format 词表
	// 由上游裁决（本地不做零转码门禁，与 gemini/minimax 转换型 adapter 不同）。
	rawModel{
		Model: "cogtts", Mode: "audio",
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"audio"},
		SupportedAPIProtocols: []string{"audio_speech"},
	},
}
