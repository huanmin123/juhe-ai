package pricing

// OpenAI pricing snapshot, ported from backend/src/modules/model-pricing/
// openai-model-pricing.{data,gpt4,gpt5,image,reasoning}.data.ts. Token prices
// are USD per token literals identical to the Node snapshot rows.
//
// 2026-10-05 全厂商补全批（依据 docs/plans/计划-20261005T110000000Z-模型
// 目录全厂商补全与场景化展示.md §3.1/§3.2/§2，来源 = 计划 §6.1：
// platform.openai.com/docs/pricing.md + deprecations.md + docs/models）：
//   - 新增 11 行：gpt-realtime-2.1 / gpt-realtime-2 / gpt-realtime-2.1-mini
//     / gpt-realtime-1.5 / gpt-audio-1.5 / gpt-live-1 / gpt-transcribe /
//     gpt-live-transcribe / gpt-realtime-whisper / gpt-realtime-translate /
//     gpt-5.6-cyber（价格全部 pricing.md 明文；新增行 ReleaseDate 无官方
//     明文的一律 nil）。
//   - 修正 22 行：gpt-image-2.5-sunburst/flare 图价 $8/$30（原 $10/$40，
//     2 行）；sora-2/sora-2-pro（2026-09-24）、gpt-5.3-codex/gpt-5.1/
//     gpt-5.4-nano（2027-04-01）、whisper-1/gpt-4o-transcribe/
//     gpt-4o-mini-transcribe（2027-02-26）、tts-1/tts-1-hd/gpt-4o-mini-tts
//     （2027-01-06）、gpt-realtime（2027-01-20）补 ShutdownDate
//     （deprecations.md 明文，只落官方明文日期，裁决 §2.3，12 行）；
//     gpt-5.5/5.5-pro/5.4/5.4-pro 落长上下文档（>272k：in 2x / out 1.5x，
//     含各自 -YYYY-MM-DD 快照行，同一模型同价卡，8 行）；gpt-6-astra
//     Ultrafast 档注释登记（价格结构无 ultrafast 通道，不结构化，
//     裁决 §2.6 同型）。
//   - chat-latest、gpt-rosalind-research 官方在售但不收录（计划 §3.3）。
//
// openAIModelPricingData = openAIGPT4 + openAIGPT5 + openAIImage +
// openAIReasoning + openAIAudio + openAIVideo (order preserved).
var openAIModelPricingData = func() []rawModel {
	out := make([]rawModel, 0, len(openAIGPT4ModelPricingData)+len(openAIGPT5ModelPricingData)+len(openAIImageModelPricingData)+len(openAIReasoningModelPricingData)+len(openAIAudioModelPricingData)+len(openAIVideoModelPricingData))
	out = append(out, openAIGPT4ModelPricingData...)
	out = append(out, openAIGPT5ModelPricingData...)
	out = append(out, openAIImageModelPricingData...)
	out = append(out, openAIReasoningModelPricingData...)
	out = append(out, openAIAudioModelPricingData...)
	out = append(out, openAIVideoModelPricingData...)
	return out
}()

// openAIGPT4ModelPricingData — curated 2026-07-13.
var openAIGPT4ModelPricingData = []rawModel{
	{
		Model: "gpt-4.1", Mode: "chat", ReleaseDate: "2025-04-14",
		MaxTokens: intp(32768), ContextWindowTokens: intp(1047576), MaxOutputTokens: intp(32768),
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"text"},
		SupportedAPIProtocols: []string{"chat_completions", "responses"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions", "responses"},
			[]string{"function_calling", "web_search", "file_search", "image_generation", "code_interpreter", "mcp"}),
		InputCostPerToken: f64p(0.000002), InputCostPerTokenPriority: f64p(0.0000035),
		OutputCostPerToken: f64p(0.000008), OutputCostPerTokenPriority: f64p(0.000014),
		CacheReadInputTokenCost: f64p(5e-7), CacheReadInputTokenCostPriority: f64p(8.75e-7),
		SupportsPromptCaching: true,
		SupportedServiceTiers: []string{"priority"},
	},
	{
		Model: "gpt-4.1-mini", Mode: "chat", ReleaseDate: "2025-04-14",
		MaxTokens: intp(32768), ContextWindowTokens: intp(1047576), MaxOutputTokens: intp(32768),
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"text"},
		SupportedAPIProtocols: []string{"chat_completions", "responses"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions", "responses"},
			[]string{"function_calling", "web_search", "file_search", "code_interpreter", "mcp"}),
		InputCostPerToken: f64p(4e-7), InputCostPerTokenPriority: f64p(7e-7),
		OutputCostPerToken: f64p(0.0000016), OutputCostPerTokenPriority: f64p(0.0000028),
		CacheReadInputTokenCost: f64p(1e-7), CacheReadInputTokenCostPriority: f64p(1.75e-7),
		SupportsPromptCaching: true,
		SupportedServiceTiers: []string{"priority"},
	},
	{
		Model: "gpt-4.1-nano", Mode: "chat", ReleaseDate: "2025-04-14",
		MaxTokens: intp(32768), ContextWindowTokens: intp(1047576), MaxOutputTokens: intp(32768),
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"text"},
		SupportedAPIProtocols: []string{"chat_completions", "responses"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions", "responses"},
			[]string{"function_calling", "file_search", "image_generation", "code_interpreter", "mcp"}),
		InputCostPerToken: f64p(1e-7), InputCostPerTokenPriority: f64p(2e-7),
		OutputCostPerToken: f64p(4e-7), OutputCostPerTokenPriority: f64p(8e-7),
		CacheReadInputTokenCost: f64p(2.5e-8), CacheReadInputTokenCostPriority: f64p(5e-8),
		ShutdownDate:          "2026-10-23",
		SupportsPromptCaching: true,
		SupportedServiceTiers: []string{"priority"},
	},
	{
		Model: "gpt-4o", Mode: "chat", ReleaseDate: "2024-05-13",
		MaxTokens: intp(16384), ContextWindowTokens: intp(128000), MaxOutputTokens: intp(16384),
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"text"},
		SupportedAPIProtocols: []string{"chat_completions", "responses"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions", "responses"},
			[]string{"function_calling", "web_search", "file_search", "image_generation", "code_interpreter", "mcp"}),
		InputCostPerToken: f64p(0.0000025), InputCostPerTokenPriority: f64p(0.00000425),
		OutputCostPerToken: f64p(0.00001), OutputCostPerTokenPriority: f64p(0.000017),
		CacheReadInputTokenCost: f64p(0.00000125), CacheReadInputTokenCostPriority: f64p(0.000002125),
		SupportsPromptCaching: true,
		SupportedServiceTiers: []string{"priority"},
	},
	{
		Model: "gpt-4o-mini", Mode: "chat", ReleaseDate: "2024-07-18",
		MaxTokens: intp(16384), ContextWindowTokens: intp(128000), MaxOutputTokens: intp(16384),
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"text"},
		SupportedAPIProtocols: []string{"chat_completions", "responses"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions", "responses"},
			[]string{"function_calling", "web_search", "file_search", "image_generation", "code_interpreter", "mcp"}),
		InputCostPerToken: f64p(1.5e-7), InputCostPerTokenPriority: f64p(2.5e-7),
		OutputCostPerToken: f64p(6e-7), OutputCostPerTokenPriority: f64p(0.000001),
		CacheReadInputTokenCost: f64p(7.5e-8), CacheReadInputTokenCostPriority: f64p(1.25e-7),
		SupportsPromptCaching: true,
		SupportedServiceTiers: []string{"priority"},
	},
	{
		Model: "gpt-4o-2024-05-13", Mode: "chat", ReleaseDate: "2024-05-13",
		MaxTokens: intp(4096), ContextWindowTokens: intp(128000), MaxOutputTokens: intp(4096),
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"text"},
		SupportedAPIProtocols: []string{"chat_completions", "responses"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions", "responses"},
			[]string{"function_calling", "web_search", "file_search", "image_generation", "code_interpreter", "mcp"}),
		InputCostPerToken:       f64p(0.000005),
		CacheReadInputTokenCost: f64p(0.0000025),
		OutputCostPerToken:      f64p(0.000015),
		SupportsPromptCaching:   true,
		ShutdownDate:            "2026-10-23",
	},
	{
		Model: "gpt-4-turbo", Mode: "chat", ReleaseDate: "2024-04-09",
		MaxTokens: intp(4096), ContextWindowTokens: intp(128000), MaxOutputTokens: intp(4096),
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"text"},
		SupportedAPIProtocols: []string{"chat_completions", "responses"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions", "responses"},
			[]string{"function_calling"}),
		InputCostPerToken:  f64p(0.00001),
		OutputCostPerToken: f64p(0.00003),
		ShutdownDate:       "2026-10-23",
	},
	{
		Model: "gpt-4-turbo-2024-04-09", Mode: "chat", ReleaseDate: "2024-04-09",
		MaxTokens: intp(4096), ContextWindowTokens: intp(128000), MaxOutputTokens: intp(4096),
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"text"},
		SupportedAPIProtocols: []string{"chat_completions", "responses"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions", "responses"},
			[]string{"function_calling"}),
		InputCostPerToken:  f64p(0.00001),
		OutputCostPerToken: f64p(0.00003),
		ShutdownDate:       "2026-10-23",
	},
	{
		Model: "gpt-4-1106-preview", Mode: "chat", ReleaseDate: "2023-11-06",
		MaxTokens: intp(4096), ContextWindowTokens: intp(128000), MaxOutputTokens: intp(4096),
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"text"},
		SupportedAPIProtocols: []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"},
			[]string{"function_calling"}),
		InputCostPerToken:  f64p(0.00001),
		OutputCostPerToken: f64p(0.00003),
		ShutdownDate:       "2026-10-23",
	},
	{
		Model: "gpt-4", Mode: "chat", ReleaseDate: "2023-06-13",
		MaxTokens: intp(8192), ContextWindowTokens: intp(8192), MaxOutputTokens: intp(4096),
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"text"},
		SupportedAPIProtocols: []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"},
			[]string{"function_calling"}),
		InputCostPerToken:  f64p(0.00003),
		OutputCostPerToken: f64p(0.00006),
		ShutdownDate:       "2026-10-23",
	},
	{
		Model: "gpt-4-0613", Mode: "chat", ReleaseDate: "2023-06-13",
		MaxTokens: intp(8192), ContextWindowTokens: intp(8192), MaxOutputTokens: intp(4096),
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"text"},
		SupportedAPIProtocols: []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"},
			[]string{"function_calling"}),
		InputCostPerToken:  f64p(0.00003),
		OutputCostPerToken: f64p(0.00006),
		ShutdownDate:       "2026-10-23",
	},
	{
		Model: "gpt-3.5-turbo", Mode: "chat", ReleaseDate: "2023-06-13",
		MaxTokens: intp(4096), ContextWindowTokens: intp(16385), MaxOutputTokens: intp(4096),
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"text"},
		SupportedAPIProtocols: []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"},
			[]string{"function_calling"}),
		InputCostPerToken:  f64p(5e-7),
		OutputCostPerToken: f64p(0.0000015),
		ShutdownDate:       "2026-10-23",
	},
	{
		Model: "gpt-3.5-turbo-0125", Mode: "chat", ReleaseDate: "2024-01-25",
		MaxTokens: intp(4096), ContextWindowTokens: intp(16385), MaxOutputTokens: intp(4096),
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"text"},
		SupportedAPIProtocols: []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"},
			[]string{"function_calling"}),
		InputCostPerToken:  f64p(5e-7),
		OutputCostPerToken: f64p(0.0000015),
		ShutdownDate:       "2026-10-23",
	},
	{
		Model: "gpt-3.5-turbo-1106", Mode: "chat", ReleaseDate: "2023-11-06",
		MaxTokens: intp(4096), ContextWindowTokens: intp(16385), MaxOutputTokens: intp(4096),
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"text"},
		SupportedAPIProtocols: []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"},
			[]string{"function_calling"}),
		InputCostPerToken:  f64p(0.000001),
		OutputCostPerToken: f64p(0.000002),
		ShutdownDate:       "2026-09-28",
	},
}

// gpt5ToolsGpt5Dot6 mirrors the shared hosted tool list of the GPT-5.6 family
// rows; the *ByProtocol matrices split it across the row protocols (responses
// carries the hosted set, chat_completions only function_calling).
var gpt5ToolsGpt5Dot6 = []string{"function_calling", "web_search", "file_search", "image_generation", "code_interpreter", "hosted_shell", "apply_patch", "skills", "computer_use", "mcp", "tool_search"}

var gpt5ToolsGpt5Dot6ByProtocol = toolsByProtocol([]string{"chat_completions", "responses"}, gpt5ToolsGpt5Dot6)

// gpt5ToolsGpt61Sol mirrors the GPT-6.1 Sol hosted tool list (same set as
// the GPT-5.6 family), but the 2026-09-30 model page restricts tool calling
// to the Responses API: Chat Completions is supported without tool calling,
// so the matrix carries no chat_completions key (consumers treat a missing
// protocol key as an empty tool set).
var gpt5ToolsGpt61SolByProtocol = toolsByProtocol([]string{"responses"}, gpt5ToolsGpt5Dot6)

// gpt5ToolsGpt55 mirrors the GPT-5.5 family hosted tool list.
var gpt5ToolsGpt55 = []string{"function_calling", "web_search", "file_search", "tool_search", "image_generation", "code_interpreter", "hosted_shell", "apply_patch", "skills", "computer_use", "mcp"}

var gpt5ToolsGpt55ByProtocol = toolsByProtocol([]string{"chat_completions", "responses"}, gpt5ToolsGpt55)

// gpt5ToolsGpt54Mini mirrors the GPT-5.4/mini family tool list.
var gpt5ToolsGpt54Mini = gpt5ToolsGpt55

var gpt5ToolsGpt54MiniByProtocol = gpt5ToolsGpt55ByProtocol

// gpt5ToolsGpt54Nano mirrors the GPT-5.4-nano tool list.
var gpt5ToolsGpt54Nano = []string{"function_calling", "web_search", "file_search", "image_generation", "code_interpreter", "hosted_shell", "apply_patch", "skills", "mcp"}

var gpt5ToolsGpt54NanoByProtocol = toolsByProtocol([]string{"chat_completions", "responses"}, gpt5ToolsGpt54Nano)

// gpt5ToolsProCodex543 mirrors the GPT-5 pro / codex tool lists that share
// the same content (responses-only rows).
var gpt5ToolsProCodex543 = []string{"function_calling", "web_search", "file_search", "tool_search", "image_generation", "apply_patch", "computer_use", "mcp"}

var gpt5ToolsProCodex543ByProtocol = toolsByProtocol([]string{"responses"}, gpt5ToolsProCodex543)

// openAIGPT5ModelPricingData — GPT-6/GPT-5 family, curated with the
// 2026-07-30 pricing changelog.
var openAIGPT5ModelPricingData = []rawModel{
	{
		Model: "gpt-6.1-sol", Mode: "chat", ReleaseDate: "2026-09-30",
		ContextWindowTokens: intp(1050000), MaxInputTokens: intp(922000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(0.000002), InputCostPerTokenPriority: f64p(0.000004), InputCostPerTokenFlex: f64p(0.000001),
		OutputCostPerToken: f64p(0.00001), OutputCostPerTokenPriority: f64p(0.00002), OutputCostPerTokenFlex: f64p(0.000005),
		CacheCreationInputTokenCost: f64p(0.0000025), CacheCreationInputTokenCostPriority: f64p(0.000005), CacheCreationInputTokenCostFlex: f64p(0.00000125),
		CacheReadInputTokenCost: f64p(1e-7), CacheReadInputTokenCostPriority: f64p(2e-7), CacheReadInputTokenCostFlex: f64p(5e-8),
		LongContextInputTokenThreshold: intp(272000),
		LongContextInputCostMultiplier: f64p(2), LongContextOutputCostMultiplier: f64p(1.5),
		SupportsPromptCaching:     true,
		SupportedServiceTiers:     []string{"priority", "flex"},
		SupportedReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"},
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  gpt5ToolsGpt61SolByProtocol,
	},
	{
		Model: "gpt-6-sol", Mode: "chat", ReleaseDate: "2026-09-22",
		ContextWindowTokens: intp(1050000), MaxInputTokens: intp(922000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(0.000002), InputCostPerTokenPriority: f64p(0.000004), InputCostPerTokenFlex: f64p(0.000001),
		OutputCostPerToken: f64p(0.00001), OutputCostPerTokenPriority: f64p(0.00002), OutputCostPerTokenFlex: f64p(0.000005),
		CacheCreationInputTokenCost: f64p(0.0000025), CacheCreationInputTokenCostPriority: f64p(0.000005), CacheCreationInputTokenCostFlex: f64p(0.00000125),
		CacheReadInputTokenCost: f64p(2e-7), CacheReadInputTokenCostPriority: f64p(4e-7), CacheReadInputTokenCostFlex: f64p(1e-7),
		LongContextInputTokenThreshold: intp(272000),
		LongContextInputCostMultiplier: f64p(2), LongContextOutputCostMultiplier: f64p(1.5),
		SupportsPromptCaching:     true,
		SupportedServiceTiers:     []string{"priority", "flex"},
		SupportedReasoningEfforts: []string{"none", "low", "medium", "high", "xhigh", "max"},
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  gpt5ToolsGpt5Dot6ByProtocol,
	},
	{
		Model: "gpt-6-luna", Mode: "chat", ReleaseDate: "2026-09-22",
		ContextWindowTokens: intp(1050000), MaxInputTokens: intp(922000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(1e-7), InputCostPerTokenPriority: f64p(2e-7), InputCostPerTokenFlex: f64p(5e-8),
		OutputCostPerToken: f64p(5e-7), OutputCostPerTokenPriority: f64p(0.000001), OutputCostPerTokenFlex: f64p(2.5e-7),
		CacheCreationInputTokenCost: f64p(1.25e-7), CacheCreationInputTokenCostPriority: f64p(2.5e-7), CacheCreationInputTokenCostFlex: f64p(6.25e-8),
		CacheReadInputTokenCost: f64p(1e-8), CacheReadInputTokenCostPriority: f64p(2e-8), CacheReadInputTokenCostFlex: f64p(5e-9),
		LongContextInputTokenThreshold: intp(272000),
		LongContextInputCostMultiplier: f64p(2), LongContextOutputCostMultiplier: f64p(1.5),
		SupportsPromptCaching:     true,
		SupportedServiceTiers:     []string{"priority", "flex"},
		SupportedReasoningEfforts: []string{"none", "low", "medium", "high", "xhigh", "max"},
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  gpt5ToolsGpt5Dot6ByProtocol,
	},
	{
		// 2026-10-05 官方定价页档位变更登记：2026-07-30 起 priority 档改名
		// Fast，并新增 Ultrafast 档（in $60 / cached $6 / cache write $75 /
		// out $300 per 1M tokens；长上下文 >272k：$120/$12/$150/$450 =
		// in 2x / out 1.5x）。目录价格结构的档位通道只有 priority/flex/batch
		//（rawServiceTierPrices 固定键），Ultrafast 档无法结构化，按分段
		// 不结构化同型裁决（计划 §2.6）注释登记，待 rawModel 扩档后落价。
		Model: "gpt-6-astra", Mode: "chat", CatalogOrder: intp(-1), ReleaseDate: "2026-09-03",
		ContextWindowTokens: intp(1050000), MaxInputTokens: intp(922000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(0.00001), InputCostPerTokenPriority: f64p(0.00002), InputCostPerTokenFlex: f64p(0.000005),
		OutputCostPerToken: f64p(0.00005), OutputCostPerTokenPriority: f64p(0.0001), OutputCostPerTokenFlex: f64p(0.000025),
		CacheCreationInputTokenCost: f64p(0.0000125), CacheCreationInputTokenCostPriority: f64p(0.000025), CacheCreationInputTokenCostFlex: f64p(0.00000625),
		CacheReadInputTokenCost: f64p(0.000001), CacheReadInputTokenCostPriority: f64p(0.000002), CacheReadInputTokenCostFlex: f64p(0.0000005),
		LongContextInputTokenThreshold: intp(272000),
		LongContextInputCostMultiplier: f64p(2), LongContextOutputCostMultiplier: f64p(1.5),
		SupportsPromptCaching:     true,
		SupportedServiceTiers:     []string{"priority", "flex"},
		SupportedReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"},
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  gpt5ToolsGpt5Dot6ByProtocol,
	},
	{
		// gpt-5.6-sol official promo prices (from 2026-08-21, re-check at
		// least by 2026-11-21): $4/$0.4/$20 with priority 2x, flex 0.5x and
		// cache write 1.25x ratios preserved.
		Model: "gpt-5.6-sol", Mode: "chat", CatalogOrder: intp(0), ReleaseDate: "2026-06-26",
		ContextWindowTokens: intp(1050000), MaxInputTokens: intp(922000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(0.000004), InputCostPerTokenPriority: f64p(0.000008), InputCostPerTokenFlex: f64p(0.000002),
		OutputCostPerToken: f64p(0.00002), OutputCostPerTokenPriority: f64p(0.00004), OutputCostPerTokenFlex: f64p(0.00001),
		CacheCreationInputTokenCost: f64p(0.000005), CacheCreationInputTokenCostPriority: f64p(0.00001), CacheCreationInputTokenCostFlex: f64p(0.0000025),
		CacheReadInputTokenCost: f64p(4e-7), CacheReadInputTokenCostPriority: f64p(8e-7), CacheReadInputTokenCostFlex: f64p(2e-7),
		LongContextInputTokenThreshold: intp(272000),
		LongContextInputCostMultiplier: f64p(2), LongContextOutputCostMultiplier: f64p(1.5),
		SupportsPromptCaching:         true,
		SupportedServiceTiers:         []string{"priority", "flex"},
		SupportedReasoningEfforts:     []string{"none", "low", "medium", "high", "xhigh", "max"},
		CodexSupportedReasoningLevels: []string{"low", "medium", "high", "xhigh", "max", "ultra"},
		CodexDefaultReasoningLevel:    "low",
		CodexMultiAgentVersion:        "v2",
		SupportedAPIProtocols:         []string{"chat_completions", "responses"},
		InputModalities:               []string{"text", "image"},
		OutputModalities:              []string{"text"},
		SupportedToolsByProtocol:      gpt5ToolsGpt5Dot6ByProtocol,
	},
	{
		Model: "gpt-5.6-terra", Mode: "chat", CatalogOrder: intp(1), ReleaseDate: "2026-06-26",
		ContextWindowTokens: intp(1050000), MaxInputTokens: intp(922000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(0.000002), InputCostPerTokenPriority: f64p(0.000004), InputCostPerTokenFlex: f64p(0.000001),
		OutputCostPerToken: f64p(0.000012), OutputCostPerTokenPriority: f64p(0.000024), OutputCostPerTokenFlex: f64p(0.000006),
		CacheCreationInputTokenCost: f64p(0.0000025), CacheCreationInputTokenCostPriority: f64p(0.000005), CacheCreationInputTokenCostFlex: f64p(0.00000125),
		CacheReadInputTokenCost: f64p(2e-7), CacheReadInputTokenCostPriority: f64p(4e-7), CacheReadInputTokenCostFlex: f64p(1e-7),
		LongContextInputTokenThreshold: intp(272000),
		LongContextInputCostMultiplier: f64p(2), LongContextOutputCostMultiplier: f64p(1.5),
		SupportsPromptCaching:         true,
		SupportedServiceTiers:         []string{"priority", "flex"},
		SupportedReasoningEfforts:     []string{"none", "low", "medium", "high", "xhigh", "max"},
		CodexSupportedReasoningLevels: []string{"low", "medium", "high", "xhigh", "max", "ultra"},
		CodexDefaultReasoningLevel:    "medium",
		CodexMultiAgentVersion:        "v2",
		SupportedAPIProtocols:         []string{"chat_completions", "responses"},
		InputModalities:               []string{"text", "image"},
		OutputModalities:              []string{"text"},
		SupportedToolsByProtocol:      gpt5ToolsGpt5Dot6ByProtocol,
	},
	{
		Model: "gpt-5.6-luna", Mode: "chat", CatalogOrder: intp(2), ReleaseDate: "2026-06-26",
		ContextWindowTokens: intp(1050000), MaxInputTokens: intp(922000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(2e-7), InputCostPerTokenPriority: f64p(4e-7), InputCostPerTokenFlex: f64p(1e-7),
		OutputCostPerToken: f64p(0.0000012), OutputCostPerTokenPriority: f64p(0.0000024), OutputCostPerTokenFlex: f64p(6e-7),
		CacheCreationInputTokenCost: f64p(2.5e-7), CacheCreationInputTokenCostPriority: f64p(5e-7), CacheCreationInputTokenCostFlex: f64p(1.25e-7),
		CacheReadInputTokenCost: f64p(2e-8), CacheReadInputTokenCostPriority: f64p(4e-8), CacheReadInputTokenCostFlex: f64p(1e-8),
		LongContextInputTokenThreshold: intp(272000),
		LongContextInputCostMultiplier: f64p(2), LongContextOutputCostMultiplier: f64p(1.5),
		SupportsPromptCaching:         true,
		SupportedServiceTiers:         []string{"priority", "flex"},
		SupportedReasoningEfforts:     []string{"none", "low", "medium", "high", "xhigh", "max"},
		CodexSupportedReasoningLevels: []string{"low", "medium", "high", "xhigh", "max"},
		CodexDefaultReasoningLevel:    "medium",
		SupportedAPIProtocols:         []string{"chat_completions", "responses"},
		InputModalities:               []string{"text", "image"},
		OutputModalities:              []string{"text"},
		SupportedToolsByProtocol:      gpt5ToolsGpt5Dot6ByProtocol,
	},
	{
		// 2026-10-05 新增（官方 pricing.md 明文）：gpt-5.6-cyber，in $12.50 /
		// cached $1.25 / out $75.00、cache write $15.625 per 1M tokens。
		// 工具集/协议对照 gpt-5.6 家族共享清单；官方未给发布日与上下文
		// 窗口明文，ReleaseDate/ContextWindowTokens 留空（不编造）。
		Model: "gpt-5.6-cyber", Mode: "chat",
		InputCostPerToken:           perToken(12.5),
		OutputCostPerToken:          perToken(75),
		CacheReadInputTokenCost:     perToken(1.25),
		CacheCreationInputTokenCost: perToken(15.625),
		SupportsPromptCaching:       true,
		SupportedAPIProtocols:       []string{"chat_completions", "responses"},
		InputModalities:             []string{"text", "image"},
		OutputModalities:            []string{"text"},
		SupportedToolsByProtocol:    gpt5ToolsGpt5Dot6ByProtocol,
	},
	{
		// 2026-10-05 落长上下文档（官方定价页明文）：>272k 输入触发 in 2x /
		// out 1.5x（同 gpt-5.6/6.x 家族既有口径；含下方 -2026-04-23 快照行，
		// 同一模型同价卡）。
		Model: "gpt-5.5", Mode: "chat", ReleaseDate: "2026-04-23",
		MaxTokens: intp(128000), ContextWindowTokens: intp(1050000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(0.000005), InputCostPerTokenPriority: f64p(0.0000125), InputCostPerTokenFlex: f64p(0.0000025),
		OutputCostPerToken: f64p(0.00003), OutputCostPerTokenPriority: f64p(0.000075), OutputCostPerTokenFlex: f64p(0.000015),
		CacheReadInputTokenCost: f64p(5e-7), CacheReadInputTokenCostPriority: f64p(0.00000125), CacheReadInputTokenCostFlex: f64p(2.5e-7),
		LongContextInputTokenThreshold: intp(272000),
		LongContextInputCostMultiplier: f64p(2), LongContextOutputCostMultiplier: f64p(1.5),
		SupportsPromptCaching:     true,
		SupportedReasoningEfforts: []string{"none", "low", "medium", "high", "xhigh"},
		SupportedServiceTiers:     []string{"priority", "flex"},
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  gpt5ToolsGpt55ByProtocol,
	},
	{
		// 2026-10-05 长上下文档随 gpt-5.5 基行同步（同一模型同价卡）。
		Model: "gpt-5.5-2026-04-23", Mode: "chat", ReleaseDate: "2026-04-23",
		MaxTokens: intp(128000), ContextWindowTokens: intp(1050000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(0.000005), InputCostPerTokenPriority: f64p(0.0000125), InputCostPerTokenFlex: f64p(0.0000025),
		OutputCostPerToken: f64p(0.00003), OutputCostPerTokenPriority: f64p(0.000075), OutputCostPerTokenFlex: f64p(0.000015),
		CacheReadInputTokenCost: f64p(5e-7), CacheReadInputTokenCostPriority: f64p(0.00000125), CacheReadInputTokenCostFlex: f64p(2.5e-7),
		LongContextInputTokenThreshold: intp(272000),
		LongContextInputCostMultiplier: f64p(2), LongContextOutputCostMultiplier: f64p(1.5),
		SupportsPromptCaching:     true,
		SupportedReasoningEfforts: []string{"none", "low", "medium", "high", "xhigh"},
		SupportedServiceTiers:     []string{"priority", "flex"},
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  gpt5ToolsGpt55ByProtocol,
	},
	{
		// 2026-10-05 落长上下文档（>272k：in 2x / out 1.5x，随 gpt-5.5 家族）。
		Model: "gpt-5.5-pro", Mode: "responses", ReleaseDate: "2026-04-23",
		MaxTokens: intp(128000), ContextWindowTokens: intp(1050000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(0.00003), InputCostPerTokenFlex: f64p(0.000015),
		OutputCostPerToken: f64p(0.00018), OutputCostPerTokenFlex: f64p(0.00009),
		LongContextInputTokenThreshold: intp(272000),
		LongContextInputCostMultiplier: f64p(2), LongContextOutputCostMultiplier: f64p(1.5),
		SupportsPromptCaching:     true,
		SupportedReasoningEfforts: []string{"medium", "high", "xhigh"},
		SupportedServiceTiers:     []string{"flex"},
		SupportedAPIProtocols:     []string{"responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  toolsByProtocol([]string{"responses"}, []string{"function_calling", "web_search", "file_search", "image_generation", "code_interpreter", "hosted_shell", "mcp"}),
	},
	{
		// 2026-10-05 长上下文档随 gpt-5.5-pro 基行同步（同一模型同价卡）。
		Model: "gpt-5.5-pro-2026-04-23", Mode: "responses", ReleaseDate: "2026-04-23",
		MaxTokens: intp(128000), ContextWindowTokens: intp(1050000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(0.00003), InputCostPerTokenFlex: f64p(0.000015),
		OutputCostPerToken: f64p(0.00018), OutputCostPerTokenFlex: f64p(0.00009),
		LongContextInputTokenThreshold: intp(272000),
		LongContextInputCostMultiplier: f64p(2), LongContextOutputCostMultiplier: f64p(1.5),
		SupportsPromptCaching:     true,
		SupportedReasoningEfforts: []string{"medium", "high", "xhigh"},
		SupportedServiceTiers:     []string{"flex"},
		SupportedAPIProtocols:     []string{"responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  toolsByProtocol([]string{"responses"}, []string{"function_calling", "web_search", "file_search", "image_generation", "code_interpreter", "hosted_shell", "mcp"}),
	},
	{
		// 2026-10-05 落长上下文档（>272k：in 2x / out 1.5x，官方定价页明文；
		// 含下方 -2026-03-05 快照行，同一模型同价卡）。
		Model: "gpt-5.4", Mode: "chat", ReleaseDate: "2026-03-05",
		MaxTokens: intp(128000), ContextWindowTokens: intp(1050000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(0.0000025), InputCostPerTokenPriority: f64p(0.000005), InputCostPerTokenFlex: f64p(0.00000125),
		OutputCostPerToken: f64p(0.000015), OutputCostPerTokenPriority: f64p(0.00003), OutputCostPerTokenFlex: f64p(0.0000075),
		CacheReadInputTokenCost: f64p(2.5e-7), CacheReadInputTokenCostPriority: f64p(5e-7), CacheReadInputTokenCostFlex: f64p(1.3e-7),
		LongContextInputTokenThreshold: intp(272000),
		LongContextInputCostMultiplier: f64p(2), LongContextOutputCostMultiplier: f64p(1.5),
		SupportsPromptCaching:     true,
		SupportedReasoningEfforts: []string{"none", "low", "medium", "high", "xhigh"},
		SupportedServiceTiers:     []string{"priority", "flex"},
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  gpt5ToolsGpt54MiniByProtocol,
	},
	{
		// 2026-10-05 长上下文档随 gpt-5.4 基行同步（同一模型同价卡）。
		Model: "gpt-5.4-2026-03-05", Mode: "chat", ReleaseDate: "2026-03-05",
		MaxTokens: intp(128000), ContextWindowTokens: intp(1050000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(0.0000025), InputCostPerTokenPriority: f64p(0.000005), InputCostPerTokenFlex: f64p(0.00000125),
		OutputCostPerToken: f64p(0.000015), OutputCostPerTokenPriority: f64p(0.00003), OutputCostPerTokenFlex: f64p(0.0000075),
		CacheReadInputTokenCost: f64p(2.5e-7), CacheReadInputTokenCostPriority: f64p(5e-7), CacheReadInputTokenCostFlex: f64p(1.3e-7),
		LongContextInputTokenThreshold: intp(272000),
		LongContextInputCostMultiplier: f64p(2), LongContextOutputCostMultiplier: f64p(1.5),
		SupportsPromptCaching:     true,
		SupportedReasoningEfforts: []string{"none", "low", "medium", "high", "xhigh"},
		SupportedServiceTiers:     []string{"priority", "flex"},
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  gpt5ToolsGpt54MiniByProtocol,
	},
	{
		Model: "gpt-5.4-mini", Mode: "chat", ReleaseDate: "2026-03-17",
		MaxTokens: intp(128000), ContextWindowTokens: intp(400000), MaxInputTokens: intp(272000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(7.5e-7), InputCostPerTokenPriority: f64p(0.0000015), InputCostPerTokenFlex: f64p(3.75e-7),
		OutputCostPerToken: f64p(0.0000045), OutputCostPerTokenPriority: f64p(0.000009), OutputCostPerTokenFlex: f64p(0.00000225),
		CacheReadInputTokenCost: f64p(7.5e-8), CacheReadInputTokenCostPriority: f64p(1.5e-7), CacheReadInputTokenCostFlex: f64p(3.75e-8),
		SupportsPromptCaching:     true,
		SupportedReasoningEfforts: []string{"none", "low", "medium", "high", "xhigh"},
		SupportedServiceTiers:     []string{"priority", "flex"},
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  gpt5ToolsGpt54MiniByProtocol,
	},
	{
		Model: "gpt-5.4-mini-2026-03-17", Mode: "chat", ReleaseDate: "2026-03-17",
		MaxTokens: intp(128000), ContextWindowTokens: intp(400000), MaxInputTokens: intp(272000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(7.5e-7), InputCostPerTokenPriority: f64p(0.0000015), InputCostPerTokenFlex: f64p(3.75e-7),
		OutputCostPerToken: f64p(0.0000045), OutputCostPerTokenPriority: f64p(0.000009), OutputCostPerTokenFlex: f64p(0.00000225),
		CacheReadInputTokenCost: f64p(7.5e-8), CacheReadInputTokenCostPriority: f64p(1.5e-7), CacheReadInputTokenCostFlex: f64p(3.75e-8),
		SupportsPromptCaching:     true,
		SupportedReasoningEfforts: []string{"none", "low", "medium", "high", "xhigh"},
		SupportedServiceTiers:     []string{"priority", "flex"},
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  gpt5ToolsGpt54MiniByProtocol,
	},
	{
		// 2026-10-05 补 ShutdownDate：官方 deprecations.md 明文 2027-04-01
		// 关停（快照行 gpt-5.4-nano-2026-03-17 未在清单单列，不落日期）。
		Model: "gpt-5.4-nano", Mode: "chat", ReleaseDate: "2026-03-17",
		ShutdownDate: "2027-04-01",
		MaxTokens:    intp(128000), ContextWindowTokens: intp(400000), MaxInputTokens: intp(272000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(2e-7), InputCostPerTokenFlex: f64p(1e-7),
		OutputCostPerToken: f64p(0.00000125), OutputCostPerTokenFlex: f64p(6.25e-7),
		CacheReadInputTokenCost: f64p(2e-8), CacheReadInputTokenCostFlex: f64p(1e-8),
		SupportsPromptCaching:     true,
		SupportedReasoningEfforts: []string{"none", "low", "medium", "high", "xhigh"},
		SupportedServiceTiers:     []string{"flex"},
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  gpt5ToolsGpt54NanoByProtocol,
	},
	{
		Model: "gpt-5.4-nano-2026-03-17", Mode: "chat", ReleaseDate: "2026-03-17",
		MaxTokens: intp(128000), ContextWindowTokens: intp(400000), MaxInputTokens: intp(272000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(2e-7), InputCostPerTokenFlex: f64p(1e-7),
		OutputCostPerToken: f64p(0.00000125), OutputCostPerTokenFlex: f64p(6.25e-7),
		CacheReadInputTokenCost: f64p(2e-8), CacheReadInputTokenCostFlex: f64p(1e-8),
		SupportsPromptCaching:     true,
		SupportedReasoningEfforts: []string{"none", "low", "medium", "high", "xhigh"},
		SupportedServiceTiers:     []string{"flex"},
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  gpt5ToolsGpt54NanoByProtocol,
	},
	{
		// 2026-10-05 落长上下文档（>272k：in 2x / out 1.5x，随 gpt-5.4 家族）。
		Model: "gpt-5.4-pro", Mode: "responses", ReleaseDate: "2026-03-05",
		MaxTokens: intp(128000), ContextWindowTokens: intp(1050000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(0.00003), InputCostPerTokenFlex: f64p(0.000015),
		OutputCostPerToken: f64p(0.00018), OutputCostPerTokenFlex: f64p(0.00009),
		LongContextInputTokenThreshold: intp(272000),
		LongContextInputCostMultiplier: f64p(2), LongContextOutputCostMultiplier: f64p(1.5),
		SupportsPromptCaching:     true,
		SupportedReasoningEfforts: []string{"medium", "high", "xhigh"},
		SupportedServiceTiers:     []string{"flex"},
		SupportedAPIProtocols:     []string{"responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  gpt5ToolsProCodex543ByProtocol,
	},
	{
		// 2026-10-05 长上下文档随 gpt-5.4-pro 基行同步（同一模型同价卡）。
		Model: "gpt-5.4-pro-2026-03-05", Mode: "responses", ReleaseDate: "2026-03-05",
		MaxTokens: intp(128000), ContextWindowTokens: intp(1050000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(0.00003), InputCostPerTokenFlex: f64p(0.000015),
		OutputCostPerToken: f64p(0.00018), OutputCostPerTokenFlex: f64p(0.00009),
		LongContextInputTokenThreshold: intp(272000),
		LongContextInputCostMultiplier: f64p(2), LongContextOutputCostMultiplier: f64p(1.5),
		SupportsPromptCaching:     true,
		SupportedReasoningEfforts: []string{"medium", "high", "xhigh"},
		SupportedServiceTiers:     []string{"flex"},
		SupportedAPIProtocols:     []string{"responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  gpt5ToolsProCodex543ByProtocol,
	},
	{
		// 2026-10-05 补 ShutdownDate：官方 deprecations.md 明文 2027-04-01
		// 关停（裁决 §2.3 只落官方明文日期）。
		Model: "gpt-5.3-codex", Mode: "responses",
		ShutdownDate: "2027-04-01",
		MaxTokens:    intp(128000), ContextWindowTokens: intp(400000), MaxInputTokens: intp(272000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(0.00000175), InputCostPerTokenPriority: f64p(0.0000035),
		OutputCostPerToken: f64p(0.000014), OutputCostPerTokenPriority: f64p(0.000028),
		CacheReadInputTokenCost: f64p(1.75e-7), CacheReadInputTokenCostPriority: f64p(3.5e-7),
		SupportsPromptCaching:     true,
		SupportedServiceTiers:     []string{"priority"},
		SupportedAPIProtocols:     []string{"responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  toolsByProtocol([]string{"responses"}, []string{"function_calling", "web_search", "hosted_shell", "skills"}),
		SupportedReasoningEfforts: []string{"low", "medium", "high", "xhigh"},
	},
	{
		Model: "gpt-5.2", Mode: "chat", ReleaseDate: "2025-12-11",
		MaxTokens: intp(128000), MaxInputTokens: intp(400000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(0.00000175), InputCostPerTokenPriority: f64p(0.0000035),
		OutputCostPerToken: f64p(0.000014), OutputCostPerTokenPriority: f64p(0.000028),
		CacheReadInputTokenCost: f64p(1.75e-7), CacheReadInputTokenCostPriority: f64p(3.5e-7),
		SupportsPromptCaching:     true,
		SupportedReasoningEfforts: []string{"none", "low", "medium", "high", "xhigh"},
		SupportedServiceTiers:     []string{"priority"},
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions", "responses"},
			[]string{"function_calling", "web_search", "file_search", "image_generation", "code_interpreter", "hosted_shell", "apply_patch", "skills", "mcp"}),
	},
	{
		Model: "gpt-5.2-2025-12-11", Mode: "chat", ReleaseDate: "2025-12-11",
		MaxTokens: intp(128000), MaxInputTokens: intp(400000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(0.00000175), InputCostPerTokenPriority: f64p(0.0000035),
		OutputCostPerToken: f64p(0.000014), OutputCostPerTokenPriority: f64p(0.000028),
		CacheReadInputTokenCost: f64p(1.75e-7), CacheReadInputTokenCostPriority: f64p(3.5e-7),
		SupportsPromptCaching:     true,
		SupportedReasoningEfforts: []string{"none", "low", "medium", "high", "xhigh"},
		SupportedServiceTiers:     []string{"priority"},
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions", "responses"},
			[]string{"function_calling", "web_search", "file_search", "image_generation", "code_interpreter", "hosted_shell", "apply_patch", "skills", "mcp"}),
	},
	{
		Model: "gpt-5.2-pro", Mode: "responses", ReleaseDate: "2025-12-11",
		MaxTokens: intp(128000), MaxInputTokens: intp(400000), MaxOutputTokens: intp(128000),
		InputCostPerToken:         f64p(0.000021),
		OutputCostPerToken:        f64p(0.000168),
		SupportsPromptCaching:     true,
		SupportedReasoningEfforts: []string{"medium", "high", "xhigh"},
		SupportedServiceTiers:     []string{"priority"},
		SupportedAPIProtocols:     []string{"responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  toolsByProtocol([]string{"responses"}, []string{"function_calling", "file_search", "image_generation", "mcp", "web_search"}),
	},
	{
		Model: "gpt-5.2-pro-2025-12-11", Mode: "responses", ReleaseDate: "2025-12-11",
		MaxTokens: intp(128000), MaxInputTokens: intp(400000), MaxOutputTokens: intp(128000),
		InputCostPerToken:         f64p(0.000021),
		OutputCostPerToken:        f64p(0.000168),
		SupportsPromptCaching:     true,
		SupportedReasoningEfforts: []string{"medium", "high", "xhigh"},
		SupportedServiceTiers:     []string{"priority"},
		SupportedAPIProtocols:     []string{"responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  toolsByProtocol([]string{"responses"}, []string{"function_calling", "file_search", "image_generation", "mcp", "web_search"}),
	},
	{
		// 2026-10-05 补 ShutdownDate：官方 deprecations.md 明文 2027-04-01
		// 关停（快照行 gpt-5.1-2025-11-13 未在 deprecations 清单单列，
		// 不落日期）。
		Model: "gpt-5.1", Mode: "chat", ReleaseDate: "2025-11-13",
		ShutdownDate: "2027-04-01",
		MaxTokens:    intp(128000), MaxInputTokens: intp(400000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(0.00000125), InputCostPerTokenPriority: f64p(0.0000025),
		OutputCostPerToken: f64p(0.00001), OutputCostPerTokenPriority: f64p(0.00002),
		CacheReadInputTokenCost: f64p(1.25e-7), CacheReadInputTokenCostPriority: f64p(2.5e-7),
		SupportsPromptCaching:     true,
		SupportedReasoningEfforts: []string{"none", "low", "medium", "high"},
		SupportedServiceTiers:     []string{"priority"},
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions", "responses"},
			[]string{"function_calling", "web_search", "file_search", "image_generation", "code_interpreter", "apply_patch", "mcp"}),
	},
	{
		Model: "gpt-5.1-2025-11-13", Mode: "chat", ReleaseDate: "2025-11-13",
		MaxTokens: intp(128000), MaxInputTokens: intp(400000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(0.00000125), InputCostPerTokenPriority: f64p(0.0000025),
		OutputCostPerToken: f64p(0.00001), OutputCostPerTokenPriority: f64p(0.00002),
		CacheReadInputTokenCost: f64p(1.25e-7), CacheReadInputTokenCostPriority: f64p(2.5e-7),
		SupportsPromptCaching:     true,
		SupportedReasoningEfforts: []string{"none", "low", "medium", "high"},
		SupportedServiceTiers:     []string{"priority"},
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions", "responses"},
			[]string{"function_calling", "web_search", "file_search", "image_generation", "code_interpreter", "apply_patch", "mcp"}),
	},
	{
		Model: "gpt-5", Mode: "chat", ReleaseDate: "2025-08-07",
		MaxTokens: intp(128000), ContextWindowTokens: intp(400000), MaxInputTokens: intp(272000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(0.00000125), InputCostPerTokenPriority: f64p(0.0000025),
		OutputCostPerToken: f64p(0.00001), OutputCostPerTokenPriority: f64p(0.00002),
		CacheReadInputTokenCost: f64p(1.25e-7), CacheReadInputTokenCostPriority: f64p(2.5e-7),
		SupportsPromptCaching:     true,
		SupportedReasoningEfforts: []string{"minimal", "low", "medium", "high"},
		SupportedServiceTiers:     []string{"priority", "flex"},
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  toolsByProtocol([]string{"chat_completions", "responses"}, []string{"function_calling", "web_search", "file_search", "image_generation", "code_interpreter", "mcp"}),
	},
	{
		Model: "gpt-5-2025-08-07", Mode: "chat", ReleaseDate: "2025-08-07",
		MaxTokens: intp(128000), ContextWindowTokens: intp(400000), MaxInputTokens: intp(272000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(0.00000125), InputCostPerTokenPriority: f64p(0.0000025),
		OutputCostPerToken: f64p(0.00001), OutputCostPerTokenPriority: f64p(0.00002),
		CacheReadInputTokenCost: f64p(1.25e-7), CacheReadInputTokenCostPriority: f64p(2.5e-7),
		SupportsPromptCaching:     true,
		SupportedReasoningEfforts: []string{"minimal", "low", "medium", "high"},
		SupportedServiceTiers:     []string{"priority", "flex"},
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  toolsByProtocol([]string{"chat_completions", "responses"}, []string{"function_calling", "web_search", "file_search", "image_generation", "code_interpreter", "mcp"}),
	},
	{
		Model: "gpt-5-mini", Mode: "chat", ReleaseDate: "2025-08-07",
		MaxTokens: intp(128000), ContextWindowTokens: intp(400000), MaxInputTokens: intp(272000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(2.5e-7), InputCostPerTokenPriority: f64p(4.5e-7),
		OutputCostPerToken: f64p(0.000002), OutputCostPerTokenPriority: f64p(0.0000036),
		CacheReadInputTokenCost: f64p(2.5e-8), CacheReadInputTokenCostPriority: f64p(4.5e-8),
		SupportsPromptCaching:     true,
		SupportedServiceTiers:     []string{"priority", "flex"},
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  toolsByProtocol([]string{"chat_completions", "responses"}, []string{"function_calling", "web_search", "file_search", "code_interpreter", "mcp"}),
		SupportedReasoningEfforts: []string{"minimal", "low", "medium", "high"},
	},
	{
		Model: "gpt-5-mini-2025-08-07", Mode: "chat", ReleaseDate: "2025-08-07",
		MaxTokens: intp(128000), ContextWindowTokens: intp(400000), MaxInputTokens: intp(272000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(2.5e-7), InputCostPerTokenPriority: f64p(4.5e-7),
		OutputCostPerToken: f64p(0.000002), OutputCostPerTokenPriority: f64p(0.0000036),
		CacheReadInputTokenCost: f64p(2.5e-8), CacheReadInputTokenCostPriority: f64p(4.5e-8),
		SupportsPromptCaching:     true,
		SupportedServiceTiers:     []string{"priority", "flex"},
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  toolsByProtocol([]string{"chat_completions", "responses"}, []string{"function_calling", "web_search", "file_search", "code_interpreter", "mcp"}),
		SupportedReasoningEfforts: []string{"minimal", "low", "medium", "high"},
	},
	{
		Model: "gpt-5-nano", Mode: "chat", ReleaseDate: "2025-08-07",
		MaxTokens: intp(128000), ContextWindowTokens: intp(400000), MaxInputTokens: intp(272000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(5e-8), InputCostPerTokenPriority: f64p(2e-7),
		OutputCostPerToken: f64p(4e-7), OutputCostPerTokenPriority: f64p(8e-7),
		CacheReadInputTokenCost: f64p(5e-9), CacheReadInputTokenCostPriority: f64p(5e-8),
		SupportsPromptCaching:     true,
		SupportedServiceTiers:     []string{"priority", "flex"},
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  toolsByProtocol([]string{"chat_completions", "responses"}, []string{"function_calling", "web_search", "file_search", "image_generation", "code_interpreter", "mcp"}),
		SupportedReasoningEfforts: []string{"minimal", "low", "medium", "high"},
	},
	{
		Model: "gpt-5-nano-2025-08-07", Mode: "chat", ReleaseDate: "2025-08-07",
		MaxTokens: intp(128000), ContextWindowTokens: intp(400000), MaxInputTokens: intp(272000), MaxOutputTokens: intp(128000),
		InputCostPerToken: f64p(5e-8), InputCostPerTokenPriority: f64p(2e-7),
		OutputCostPerToken: f64p(4e-7), OutputCostPerTokenPriority: f64p(8e-7),
		CacheReadInputTokenCost: f64p(5e-9), CacheReadInputTokenCostPriority: f64p(5e-8),
		SupportsPromptCaching:     true,
		SupportedServiceTiers:     []string{"priority", "flex"},
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  toolsByProtocol([]string{"chat_completions", "responses"}, []string{"function_calling", "web_search", "file_search", "image_generation", "code_interpreter", "mcp"}),
		SupportedReasoningEfforts: []string{"minimal", "low", "medium", "high"},
	},
	{
		Model: "gpt-5-pro", Mode: "responses", ReleaseDate: "2025-10-06",
		MaxTokens: intp(128000), ContextWindowTokens: intp(400000), MaxOutputTokens: intp(272000),
		InputCostPerToken:         f64p(0.000015),
		OutputCostPerToken:        f64p(0.00012),
		SupportsPromptCaching:     true,
		SupportedServiceTiers:     []string{"priority"},
		SupportedAPIProtocols:     []string{"responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  toolsByProtocol([]string{"responses"}, []string{"function_calling", "file_search", "image_generation", "mcp", "web_search"}),
		SupportedReasoningEfforts: []string{"high"},
	},
	{
		Model: "gpt-5-pro-2025-10-06", Mode: "responses", ReleaseDate: "2025-10-06",
		MaxTokens: intp(128000), ContextWindowTokens: intp(400000), MaxOutputTokens: intp(272000),
		InputCostPerToken:         f64p(0.000015),
		OutputCostPerToken:        f64p(0.00012),
		SupportsPromptCaching:     true,
		SupportedServiceTiers:     []string{"priority"},
		SupportedAPIProtocols:     []string{"responses"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedToolsByProtocol:  toolsByProtocol([]string{"responses"}, []string{"function_calling", "file_search", "image_generation", "mcp", "web_search"}),
		SupportedReasoningEfforts: []string{"high"},
	},
}

// openAIImageModelPricingData — image generation models, curated 2026-07-24.
var openAIImageModelPricingData = []rawModel{
	{
		// 2026-10-05 改价（官方 pricing.md 明文）：image in $8 / cached image
		// $2 / image out $30 per 1M image tokens（原 $10/$2.5/$40）。
		Model: "gpt-image-2.5-sunburst", Mode: "image_generation", ReleaseDate: "2026-09-08",
		ContextWindowTokens:          intp(202_752),
		MaxOutputTokens:              intp(65_536),
		InputModalities:              []string{"text", "image"},
		OutputModalities:             []string{"image"},
		SupportedAPIProtocols:        []string{"images", "responses"},
		InputCostPerToken:            f64p(0.000005),
		CacheReadInputTokenCost:      f64p(0.00000125),
		InputCostPerImageToken:       f64p(0.000008),
		CacheReadInputImageTokenCost: f64p(0.000002),
		OutputCostPerImageToken:      f64p(0.00003),
		SupportsPromptCaching:        true,
	},
	{
		// 2026-10-05 改价（官方 pricing.md 明文）：image in $8 / cached image
		// $2 / image out $30 per 1M image tokens（原 $10/$2.5/$40）。
		Model: "gpt-image-2.5-flare", Mode: "image_generation", ReleaseDate: "2026-09-08",
		ContextWindowTokens:          intp(202_752),
		MaxOutputTokens:              intp(65_536),
		InputModalities:              []string{"text", "image"},
		OutputModalities:             []string{"image"},
		SupportedAPIProtocols:        []string{"images", "responses"},
		InputCostPerToken:            f64p(0.000005),
		CacheReadInputTokenCost:      f64p(0.00000125),
		InputCostPerImageToken:       f64p(0.000008),
		CacheReadInputImageTokenCost: f64p(0.000002),
		OutputCostPerImageToken:      f64p(0.00003),
		SupportsPromptCaching:        true,
	},
	{
		Model: "gpt-image-2", Mode: "image_generation", ReleaseDate: "2026-04-21",
		InputModalities:              []string{"text", "image"},
		OutputModalities:             []string{"image"},
		SupportedAPIProtocols:        []string{"images"},
		InputCostPerToken:            f64p(0.000005),
		CacheReadInputTokenCost:      f64p(0.00000125),
		InputCostPerImageToken:       f64p(0.000008),
		CacheReadInputImageTokenCost: f64p(0.000002),
		OutputCostPerImageToken:      f64p(0.00003),
		SupportsPromptCaching:        true,
	},
	{
		Model: "gpt-image-2-2026-04-21", Mode: "image_generation", ReleaseDate: "2026-04-21",
		InputModalities:              []string{"text", "image"},
		OutputModalities:             []string{"image"},
		SupportedAPIProtocols:        []string{"images"},
		InputCostPerToken:            f64p(0.000005),
		CacheReadInputTokenCost:      f64p(0.00000125),
		InputCostPerImageToken:       f64p(0.000008),
		CacheReadInputImageTokenCost: f64p(0.000002),
		OutputCostPerImageToken:      f64p(0.00003),
		SupportsPromptCaching:        true,
	},
	{
		Model: "gpt-image-1.5", Mode: "image_generation",
		InputModalities:              []string{"text", "image"},
		OutputModalities:             []string{"image", "text"},
		SupportedAPIProtocols:        []string{"images"},
		InputCostPerToken:            f64p(0.000005),
		OutputCostPerToken:           f64p(0.00001),
		CacheReadInputTokenCost:      f64p(0.00000125),
		InputCostPerImageToken:       f64p(0.000008),
		CacheReadInputImageTokenCost: f64p(0.000002),
		OutputCostPerImageToken:      f64p(0.000032),
		SupportsPromptCaching:        true,
		ShutdownDate:                 "2026-12-01",
	},
	{
		Model: "gpt-image-1-mini", Mode: "image_generation",
		InputModalities:              []string{"text", "image"},
		OutputModalities:             []string{"image", "text"},
		SupportedAPIProtocols:        []string{"images"},
		InputCostPerToken:            f64p(0.000002),
		CacheReadInputTokenCost:      f64p(2e-7),
		InputCostPerImageToken:       f64p(0.0000025),
		CacheReadInputImageTokenCost: f64p(2.5e-7),
		OutputCostPerImageToken:      f64p(0.000008),
		SupportsPromptCaching:        true,
		ShutdownDate:                 "2026-12-01",
	},
	{
		Model: "gpt-image-1", Mode: "image_generation", ReleaseDate: "2025-04-23",
		InputModalities:              []string{"text", "image"},
		OutputModalities:             []string{"image"},
		SupportedAPIProtocols:        []string{"images", "responses"},
		InputCostPerToken:            f64p(0.000005),
		CacheReadInputTokenCost:      f64p(0.00000125),
		InputCostPerImageToken:       f64p(0.00001),
		CacheReadInputImageTokenCost: f64p(0.0000025),
		OutputCostPerImageToken:      f64p(0.00004),
		SupportsPromptCaching:        true,
		ShutdownDate:                 "2026-10-23",
	},
}

// openAIReasoningModelPricingData — o-series models, curated 2026-07-13.
var openAIReasoningModelPricingData = []rawModel{
	{
		Model: "o1", Mode: "chat", ReleaseDate: "2024-12-05",
		MaxTokens: intp(100000), ContextWindowTokens: intp(200000), MaxOutputTokens: intp(100000),
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		SupportedToolsByProtocol:  toolsByProtocol([]string{"chat_completions", "responses"}, []string{"function_calling", "file_search", "mcp"}),
		InputCostPerToken:         f64p(0.000015),
		OutputCostPerToken:        f64p(0.00006),
		CacheReadInputTokenCost:   f64p(0.0000075),
		ShutdownDate:              "2026-10-23",
		SupportsPromptCaching:     true,
		SupportedReasoningEfforts: []string{"low", "medium", "high"},
	},
	{
		Model: "o1-pro", Mode: "responses",
		MaxTokens: intp(100000), ContextWindowTokens: intp(200000), MaxOutputTokens: intp(100000),
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"text"},
		SupportedAPIProtocols:    []string{"responses"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"responses"}, []string{"function_calling", "file_search", "mcp"}),
		InputCostPerToken:        f64p(0.00015),
		OutputCostPerToken:       f64p(0.0006),
		ShutdownDate:             "2026-10-23",
		SupportsPromptCaching:    true,
	},
	{
		Model: "o3", Mode: "chat", ReleaseDate: "2025-04-16",
		MaxTokens: intp(100000), ContextWindowTokens: intp(200000), MaxOutputTokens: intp(100000),
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"text"},
		SupportedAPIProtocols:    []string{"chat_completions", "responses"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions", "responses"}, []string{"function_calling", "file_search", "image_generation", "code_interpreter", "mcp", "web_search"}),
		InputCostPerToken:        f64p(0.000002), InputCostPerTokenPriority: f64p(0.0000035),
		OutputCostPerToken: f64p(0.000008), OutputCostPerTokenPriority: f64p(0.000014),
		CacheReadInputTokenCost: f64p(5e-7), CacheReadInputTokenCostPriority: f64p(8.75e-7),
		SupportsPromptCaching:     true,
		SupportedReasoningEfforts: []string{"low", "medium", "high"},
		SupportedServiceTiers:     []string{"priority", "flex"},
	},
	{
		Model: "o3-pro", Mode: "responses",
		MaxTokens: intp(100000), ContextWindowTokens: intp(200000), MaxOutputTokens: intp(100000),
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"text"},
		SupportedAPIProtocols:    []string{"responses"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"responses"}, []string{"function_calling", "file_search", "image_generation", "mcp", "web_search"}),
		InputCostPerToken:        f64p(0.00002),
		OutputCostPerToken:       f64p(0.00008),
		SupportsPromptCaching:    true,
	},
	{
		Model: "o3-mini", Mode: "chat", ReleaseDate: "2025-01-31",
		MaxTokens: intp(100000), ContextWindowTokens: intp(200000), MaxOutputTokens: intp(100000),
		InputModalities:           []string{"text"},
		OutputModalities:          []string{"text"},
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		SupportedToolsByProtocol:  toolsByProtocol([]string{"chat_completions", "responses"}, []string{"function_calling", "file_search", "code_interpreter", "mcp", "image_generation"}),
		InputCostPerToken:         f64p(0.0000011),
		OutputCostPerToken:        f64p(0.0000044),
		CacheReadInputTokenCost:   f64p(5.5e-7),
		ShutdownDate:              "2026-10-23",
		SupportsPromptCaching:     true,
		SupportedReasoningEfforts: []string{"low", "medium", "high"},
	},
	{
		Model: "o4-mini", Mode: "chat", ReleaseDate: "2025-04-16",
		MaxTokens: intp(100000), ContextWindowTokens: intp(200000), MaxOutputTokens: intp(100000),
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"text"},
		SupportedAPIProtocols:    []string{"chat_completions", "responses"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions", "responses"}, []string{"function_calling", "file_search", "code_interpreter", "mcp", "web_search"}),
		InputCostPerToken:        f64p(0.0000011), InputCostPerTokenPriority: f64p(0.000002),
		OutputCostPerToken: f64p(0.0000044), OutputCostPerTokenPriority: f64p(0.000008),
		CacheReadInputTokenCost: f64p(2.75e-7), CacheReadInputTokenCostPriority: f64p(5e-7),
		ShutdownDate:              "2026-10-23",
		SupportsPromptCaching:     true,
		SupportedReasoningEfforts: []string{"low", "medium", "high"},
		SupportedServiceTiers:     []string{"priority", "flex"},
	},
}

// openAIAudioModelPricingData — M1 同步音频模型（mode=audio），价格取自
// OpenAI 官方定价页 platform.openai.com/docs/pricing（核实于 2026-10-04）：
//   - TTS：tts-1 $15.00/1M chars、tts-1-hd $30.00/1M chars（按字符计费）；
//     gpt-4o-mini-tts 官方按 token 计价（text input $0.60/1M tokens +
//     audio output $12.00/1M tokens），无官方字符价，TtsInputCostPerChar 留空。
//   - STT：whisper-1 $0.006/min（折算每秒）；gpt-4o-transcribe
//     $2.50/1M text in + $10.00/1M text out + $6.00/1M audio in
//     （官方折算约 $0.006/min）；gpt-4o-mini-transcribe $1.25 / $5.00 /
//     $3.00（约 $0.003/min）。
//   - 发布日期：gpt-4o-mini-tts / gpt-4o-transcribe / gpt-4o-mini-transcribe
//     2025-03-20（官方发布日）；tts-1 / tts-1-hd 2023-11-06（TTS API GA）；
//     whisper-1 2023-03-01（Whisper API 发布）。
//
// 任务书列出的 gpt-4o-tts 不在官方定价页与官方 audio API 文档模型清单中，
// 无官方价可查，按「不得编造」不落行（报告已注明）。
// ResponseFormats 为 OpenAI TTS 官方全集（音频设计 §4.1 / 契约 §5.1 裁决）。
//
// 2026-10-05 全厂商补全批：新增 10 行（gpt-realtime-2.1 / gpt-realtime-2 /
// gpt-realtime-2.1-mini / gpt-realtime-1.5 / gpt-audio-1.5 / gpt-live-1 /
// gpt-transcribe / gpt-live-transcribe / gpt-realtime-whisper /
// gpt-realtime-translate，价格全部 pricing.md 明文；realtime 系 audio
// cached 价 $0.40/$0.30 无独立字段列，沿 gpt-realtime 首行裁决落注释）；
// 修正 7 行 ShutdownDate（deprecations.md 明文）：gpt-4o-mini-tts/tts-1/
// tts-1-hd（2027-01-06）、gpt-4o-transcribe/gpt-4o-mini-transcribe/
// whisper-1（2027-02-26）、gpt-realtime（2027-01-20）。
var openAIAudioModelPricingData = []rawModel{
	{
		// 2026-10-05 补 ShutdownDate：官方 deprecations.md 明文 2027-01-06。
		Model: "gpt-4o-mini-tts", Mode: "audio", ReleaseDate: "2025-03-20",
		ShutdownDate:            "2027-01-06",
		InputModalities:         []string{"text"},
		OutputModalities:        []string{"audio"},
		SupportedAPIProtocols:   []string{"audio_speech"},
		ResponseFormats:         []string{"mp3", "opus", "aac", "flac", "wav", "pcm"},
		InputCostPerToken:       perToken(0.6),
		OutputCostPerAudioToken: perToken(12),
	},
	{
		// 2026-10-05 补 ShutdownDate：官方 deprecations.md 明文 2027-02-26。
		Model: "gpt-4o-transcribe", Mode: "audio", ReleaseDate: "2025-03-20",
		ShutdownDate:            "2027-02-26",
		InputModalities:         []string{"audio"},
		OutputModalities:        []string{"text"},
		SupportedAPIProtocols:   []string{"audio_transcription"},
		InputCostPerToken:       perToken(2.5),
		OutputCostPerToken:      perToken(10),
		InputCostPerAudioToken:  perToken(6),
		AudioInputCostPerSecond: usdPerMinuteToPerSecond(0.006),
	},
	{
		// 2026-10-05 补 ShutdownDate：官方 deprecations.md 明文 2027-02-26。
		Model: "gpt-4o-mini-transcribe", Mode: "audio", ReleaseDate: "2025-03-20",
		ShutdownDate:            "2027-02-26",
		InputModalities:         []string{"audio"},
		OutputModalities:        []string{"text"},
		SupportedAPIProtocols:   []string{"audio_transcription"},
		InputCostPerToken:       perToken(1.25),
		OutputCostPerToken:      perToken(5),
		InputCostPerAudioToken:  perToken(3),
		AudioInputCostPerSecond: usdPerMinuteToPerSecond(0.003),
	},
	{
		// 2026-10-05 补 ShutdownDate：官方 deprecations.md 明文 2027-01-06。
		Model: "tts-1", Mode: "audio", ReleaseDate: "2023-11-06",
		ShutdownDate:          "2027-01-06",
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"audio"},
		SupportedAPIProtocols: []string{"audio_speech"},
		ResponseFormats:       []string{"mp3", "opus", "aac", "flac", "wav", "pcm"},
		TtsInputCostPerChar:   perChar(15),
	},
	{
		// 2026-10-05 补 ShutdownDate：官方 deprecations.md 明文 2027-01-06。
		Model: "tts-1-hd", Mode: "audio", ReleaseDate: "2023-11-06",
		ShutdownDate:          "2027-01-06",
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"audio"},
		SupportedAPIProtocols: []string{"audio_speech"},
		ResponseFormats:       []string{"mp3", "opus", "aac", "flac", "wav", "pcm"},
		TtsInputCostPerChar:   perChar(30),
	},
	{
		// 2026-10-05 补 ShutdownDate：官方 deprecations.md 明文 2027-02-26。
		Model: "whisper-1", Mode: "audio", ReleaseDate: "2023-03-01",
		ShutdownDate:            "2027-02-26",
		InputModalities:         []string{"audio"},
		OutputModalities:        []string{"text"},
		SupportedAPIProtocols:   []string{"audio_transcription"},
		AudioInputCostPerSecond: usdPerMinuteToPerSecond(0.006),
	},
	{
		// M5b realtime（Realtime 设计 §5/契约 §4.4）：gpt-realtime 系首个
		// 目录行，audio token 计价。价格核实于 2026-10-04：官方定价页
		//（developers.openai.com/pricing，反爬 403，以检索快照核对该页
		// "Realtime and audio generation models" 表 audio $32.00 输入 /
		// $64.00 输出 per 1M tokens；多源一致 [openrouter / glamdringresearch
		// / apidog / trembit]）。快照三连中 cached audio $0.40/1M 属缓存
		// 维度，price 字段面无 audio-cache 独立列，不落（M5b2 计费面接线
		// 时再裁决归属）；text token 价多源分歧（$2.50～$5 输入 / $10～$20
		// 输出），按「查不到精确不编造」不落。发布日期 2025-08-28（Realtime
		// API GA 同日，Techmeme 转载 OpenAI 公告）。usage 从事件流累计、
		// 连接关闭终态落库（M5b2 交付），空会话 0 计费 + usage_missing。
		// 2026-10-05 补 ShutdownDate：官方 deprecations.md 明文 2027-01-20。
		Model: "gpt-realtime", Mode: "audio", ReleaseDate: "2025-08-28",
		ShutdownDate:            "2027-01-20",
		InputModalities:         []string{"text", "audio"},
		OutputModalities:        []string{"text", "audio"},
		SupportedAPIProtocols:   []string{"realtime"},
		InputCostPerAudioToken:  perToken(32),
		OutputCostPerAudioToken: perToken(64),
	},
	// 2026-10-05 全厂商补全批新增（价格全部 platform.openai.com/docs/
	// pricing.md 明文；发布日无官方明文一律 nil）：
	//   - realtime 系：audio in/out + text in/out token 价明文齐全（本批
	//     起 text 价有明文即落，旧 gpt-realtime 行"多源分歧不落"口径不再
	//     适用于新行）；缓存读价仅当 text/audio cached 官方同值时落模态
	//     无关通道（2.1/-2 均 $0.40），不同值（mini $0.06/$0.30）不落、注释登记。
	//   - 按分钟明文价（gpt-live-1 $0.05/min、gpt-transcribe $0.0045/min、
	//     gpt-live-transcribe / gpt-realtime-whisper $0.017/min、
	//     gpt-realtime-translate $0.034/min）按裁决 §2.4 直除换算 per-second。
	{
		// audio $32 in / $0.40 cached / $64 out；text $4/$24 per 1M。
		// 官方 text cached 与 audio cached 同为 $0.40，模态无关
		// CacheReadInputTokenCost 通道可安全落值。
		Model: "gpt-realtime-2.1", Mode: "audio",
		InputModalities:         []string{"text", "audio"},
		OutputModalities:        []string{"text", "audio"},
		SupportedAPIProtocols:   []string{"realtime"},
		CacheReadInputTokenCost: perToken(0.40),
		InputCostPerToken:       perToken(4),
		OutputCostPerToken:      perToken(24),
		InputCostPerAudioToken:  perToken(32),
		OutputCostPerAudioToken: perToken(64),
	},
	{
		// audio $32 in / $0.40 cached / $64 out；text $4/$24 per 1M。
		// 官方 text cached 与 audio cached 同为 $0.40，同上落值。
		Model: "gpt-realtime-2", Mode: "audio",
		InputModalities:         []string{"text", "audio"},
		OutputModalities:        []string{"text", "audio"},
		SupportedAPIProtocols:   []string{"realtime"},
		CacheReadInputTokenCost: perToken(0.40),
		InputCostPerToken:       perToken(4),
		OutputCostPerToken:      perToken(24),
		InputCostPerAudioToken:  perToken(32),
		OutputCostPerAudioToken: perToken(64),
	},
	{
		// mini：audio $10 in / $0.30 cached / $20 out；text $0.60/$2.40
		//（cached $0.06）。text/audio cached 不同值（$0.06 vs $0.30），模态
		// 无关通道落任一值都会误计另一模态 → 不落，注释登记官方明文。
		Model: "gpt-realtime-2.1-mini", Mode: "audio",
		InputModalities:         []string{"text", "audio"},
		OutputModalities:        []string{"text", "audio"},
		SupportedAPIProtocols:   []string{"realtime"},
		InputCostPerToken:       perToken(0.6),
		OutputCostPerToken:      perToken(2.4),
		InputCostPerAudioToken:  perToken(10),
		OutputCostPerAudioToken: perToken(20),
	},
	{
		// audio $32 in / $0.40 cached / $64 out；text $4/$16 per 1M。
		Model: "gpt-realtime-1.5", Mode: "audio",
		InputModalities:         []string{"text", "audio"},
		OutputModalities:        []string{"text", "audio"},
		SupportedAPIProtocols:   []string{"realtime"},
		InputCostPerToken:       perToken(4),
		OutputCostPerToken:      perToken(16),
		InputCostPerAudioToken:  perToken(32),
		OutputCostPerAudioToken: perToken(64),
	},
	{
		// gpt-audio-1.5：audio $32/$64、text $2.5/$10 per 1M。mode=audio
		//（对照 gpt-realtime 现行行 mode 选择）；服务面为 chat_completions
		// / responses 的音频生成（非 realtime 会话面）。
		Model: "gpt-audio-1.5", Mode: "audio",
		InputModalities:         []string{"text", "audio"},
		OutputModalities:        []string{"text", "audio"},
		SupportedAPIProtocols:   []string{"chat_completions", "responses"},
		InputCostPerToken:       perToken(2.5),
		OutputCostPerToken:      perToken(10),
		InputCostPerAudioToken:  perToken(32),
		OutputCostPerAudioToken: perToken(64),
	},
	{
		// gpt-live-1：官方 $0.05/min 明文 → 每秒 usdPerMinuteToPerSecond
		//（直除换算，官方按秒计费不取整）。
		Model: "gpt-live-1", Mode: "audio",
		InputModalities:         []string{"text", "audio"},
		OutputModalities:        []string{"text", "audio"},
		SupportedAPIProtocols:   []string{"realtime"},
		AudioInputCostPerSecond: usdPerMinuteToPerSecond(0.05),
	},
	{
		// gpt-transcribe：官方 $0.0045/min 明文 → 每秒直除换算。
		Model: "gpt-transcribe", Mode: "audio",
		InputModalities:         []string{"audio"},
		OutputModalities:        []string{"text"},
		SupportedAPIProtocols:   []string{"audio_transcription"},
		AudioInputCostPerSecond: usdPerMinuteToPerSecond(0.0045),
	},
	{
		// gpt-live-transcribe：官方 $0.017/min 明文 → 每秒直除换算；
		// Live 会话内的实时转写（realtime 面）。
		Model: "gpt-live-transcribe", Mode: "audio",
		InputModalities:         []string{"audio"},
		OutputModalities:        []string{"text"},
		SupportedAPIProtocols:   []string{"realtime"},
		AudioInputCostPerSecond: usdPerMinuteToPerSecond(0.017),
	},
	{
		// gpt-realtime-whisper：官方 $0.017/min 明文 → 每秒直除换算；
		// realtime 会话内的 whisper 转写档。
		Model: "gpt-realtime-whisper", Mode: "audio",
		InputModalities:         []string{"audio"},
		OutputModalities:        []string{"text"},
		SupportedAPIProtocols:   []string{"realtime"},
		AudioInputCostPerSecond: usdPerMinuteToPerSecond(0.017),
	},
	{
		// gpt-realtime-translate：官方 $0.034/min 明文 → 每秒直除换算；
		// realtime 会话内的实时翻译档。
		Model: "gpt-realtime-translate", Mode: "audio",
		InputModalities:         []string{"audio"},
		OutputModalities:        []string{"text"},
		SupportedAPIProtocols:   []string{"realtime"},
		AudioInputCostPerSecond: usdPerMinuteToPerSecond(0.034),
	},
}

// openAIVideoModelPricingData — M2 视频模型（mode=video），价格取自 OpenAI
// 官方公布价（Sora 2 API，OpenAI DevDay 2025-10-06 发布会公布，
// platform.openai.com/docs/pricing 为权威页；2026-10-04 检索多源复核一致
// [itechguides.com / wavespeed.ai 等转引]，官方页反爬 403 未直接抓取）：
//   - sora-2：720p $0.10/秒（batch $0.05/秒，batch 档不落价——网关无 batch
//     语义，不猜测）；
//   - sora-2-pro：720p $0.30/秒（1080p $0.50/秒）。
//
// 分辨率分档价机制本任务不建（媒体设计 §10"可按分辨率分档"留待扩展）：
// 单值按 720p 基准档落价，1080p/480p 分档随 M3 计价完善（目录价格字段沿
// PriceSet 单值模式扩展）。
// sora-2/image-to-video（图生视频模型）因模型名含斜杠、与目录行
// provider_model_catalog 的 model 标识冲突，本任务不收录；待图生视频接入
// 任务再裁决命名（别名或独立行）。
// 发布日期：sora-2 / sora-2-pro 2025-10-06（DevDay 2025 发布日，与
// gpt-5-pro 同日）。
// 2026-10-05 修正：官方 deprecations.md 明文两行 2026-09-24 关停，
// ShutdownDate 落官方明文日期（裁决 §2.3）；快照行保留供历史计费口径
// 与关停前回溯（FindProviderModelPricingAsOf）。
var openAIVideoModelPricingData = []rawModel{
	{
		Model: "sora-2", Mode: "video", ReleaseDate: "2025-10-06", ShutdownDate: "2026-09-24",
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"video"},
		SupportedAPIProtocols:    []string{"video"},
		VideoOutputCostPerSecond: f64p(0.10),
	},
	{
		Model: "sora-2-pro", Mode: "video", ReleaseDate: "2025-10-06", ShutdownDate: "2026-09-24",
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"video"},
		SupportedAPIProtocols:    []string{"video"},
		VideoOutputCostPerSecond: f64p(0.30),
	},
}
