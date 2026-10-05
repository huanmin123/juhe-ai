package pricing

// xAI pricing snapshot, ported from
// backend/src/modules/model-pricing/xai-model-pricing.data.ts (curated
// 2026-08-26). Models with a 200k threshold charge the higher rate for all
// request tokens once the prompt reaches the threshold.
//
// 2026-10-05 全厂商补全批（依据 docs/plans/计划-20261005T110000000Z-模型
// 目录全厂商补全与场景化展示.md §3.1/§3.2/§2）：新增 2 行（grok-voice-
// transcribe-2.0 转写、grok-voice-agent 实时语音对话）。
//
// 2026-10-05 复核批（docs.x.ai pricing/release-notes 页面原文逐字回正）：
// 上一轮补全批引用的"官方数值"部分有误——grok-4.6/4.5 改价 $3.00/$0.75/
// $15.00 + 上下文 256k、grok-4.7 长上下文档倍率 1.1x 均为误引，本轮全部
// 回正（官方明文：<200k $2.00/$0.50|0.30/$6.00，≥200k $4.00/$1.00|0.60/
// $12.00，上下文 500k；1.1x 是 us.api.x.ai 区域端点全 token 乘数，仅
// grok-4.7/4.6，非长上下文档）；grok-voice-transcribe-2.0 转正为 REST
// $0.10/hr 直除。教训：数值必须以页面原文为准，不得转引。
// （本批"grok-imagine-video-1.5 统一 $0.080/sec 无分档"的结论已由 2026-10-05
// 核对批再次回正为三档明文，见下方行注释。）
//
// 2026-10-05 核对批：grok-imagine-video-1.5 秒价回正 $0.14/s（官方三档
// 480p $0.08 / 720p $0.14 / 1080p $0.25 per sec，主档取 720p，沿 sora/
// Hailuo「非最低档主档」先例）；grok-imagine-image 系三行 Mode 统一词表
// "image"→"image_generation"（展示投影双轨兼容）。
//
// M3 回填池媒体增补（契约 §6.1，curated 2026-10-04）：grok-imagine-video-1.5
// 视频行落官方秒价（见下方行注释）。
type xaiTextModelMetadata struct {
	catalogVisible            *bool
	releaseDate               string
	shutdownDate              string
	supportedAPIProtocols     []string
	supportedReasoningEfforts []string
	defaultReasoningEffort    string
	// responsesWebSearch：该模型在 responses 协议下支持 hosted web_search
	//（xAI Responses API 联网搜索；按实测能力声明的近期 grok-4.x 文本模型开启）。
	responsesWebSearch bool
	// longContext 倍率：官方 >200k 长上下文档对基础价的倍率（缺省沿用
	// 2x/2x 旧口径；官方 pricing 页对 4.x 全系 ≥200k 档均为基础价 2x）。
	// 注意：1.1x 是 US 区域端点（us.api.x.ai）对全 token 的乘数（仅
	// grok-4.7/4.6，含长上下文档），不是长上下文档档位倍率。
	longContextInputCostMultiplier  *float64
	longContextOutputCostMultiplier *float64
}

func xaiTextModel(model string, contextWindowTokens int, inputUsdPer1M, cachedInputUsdPer1M, outputUsdPer1M float64, metadata xaiTextModelMetadata) rawModel {
	out := rawModel{
		Model: model, Mode: "chat",
		CatalogVisible:                          metadata.catalogVisible,
		ReleaseDate:                             metadata.releaseDate,
		ShutdownDate:                            metadata.shutdownDate,
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
		SupportedReasoningEfforts:               metadata.supportedReasoningEfforts,
		DefaultReasoningEffort:                  metadata.defaultReasoningEffort,
	}
	if metadata.longContextInputCostMultiplier != nil {
		out.LongContextInputCostMultiplier = metadata.longContextInputCostMultiplier
	}
	if metadata.longContextOutputCostMultiplier != nil {
		out.LongContextOutputCostMultiplier = metadata.longContextOutputCostMultiplier
	}
	if out.SupportedAPIProtocols == nil {
		out.SupportedAPIProtocols = []string{"chat_completions", "responses"}
	}
	// 纯 chat 供应商：function_calling 在该行全部会话协议（默认
	// chat_completions + responses，multi-agent 行仅 responses）下可用；
	// responsesWebSearch 的行在 responses 协议追加 hosted web_search
	//（chat_completions 恒仅 function_calling，toolsByProtocol 收敛）。
	tools := []string{"function_calling"}
	if metadata.responsesWebSearch {
		tools = append(tools, "web_search")
	}
	out.SupportedToolsByProtocol = toolsByProtocol(out.SupportedAPIProtocols, tools)
	return out
}

// xAIModelPricingData — curated from the official xAI docs.
var xAIModelPricingData = []rawModel{
	// grok-4.7: the official model page lists no release date; 2026-09-21 is
	// the x.ai news announcement date, kept as a secondary-source anchor
	// (same rule as grok-4.3). Batch API is officially not supported.
	// 2026-10-05 复核批回正：长上下文档倍率 1.1x→2x（官方 pricing 两行
	// "< 200k $2.00/$0.50/$6.00、≥ 200k $4.00/$1.00/$12.00"，release-notes
	// "Pricing is $2 / $0.50 / $6 ... and $4 / $1 / $12 above" = 基础价
	// 2x；1.1x 是 us.api.x.ai 区域端点全 token 乘数，非长上下文档档位，
	// 上轮误引）。
	xaiTextModel("grok-4.7", 500_000, 2, 0.5, 6, xaiTextModelMetadata{
		releaseDate:               "2026-09-21",
		supportedReasoningEfforts: []string{"low", "medium", "high", "xhigh"},
		defaultReasoningEffort:    "high",
		responsesWebSearch:        true,
	}),
	// 2026-10-05 复核批回正：上一轮误改为 $3.00/$0.75/$15.00 + 256k，官方
	// pricing 明文 "< 200k $2.00 / $0.50 / $6.00、≥ 200k $4.00 / $1.00 /
	// $12.00 | Context 500k"，release-notes "500k context window ... Pricing
	// is $2 / $0.50 / $6 ... $4 / $1 / $12 above"，全部回正。priority 档
	// 沿用工厂 2x 口径（官方 Priority Processing 2x premium）。
	xaiTextModel("grok-4.6", 500_000, 2, 0.5, 6, xaiTextModelMetadata{
		releaseDate:               "2026-08-12",
		supportedReasoningEfforts: []string{"low", "medium", "high", "xhigh"},
		defaultReasoningEffort:    "high",
		responsesWebSearch:        true,
	}),
	// 2026-10-05 复核批回正：上一轮误改为 $3.00/$0.75/$15.00 + 256k，官方
	// pricing 明文 "< 200k $2.00 / $0.30 / $6.00、≥ 200k $4.00 / $0.60 /
	// $12.00 | Context 500k"（模型页 "Context window: 500,000 tokens"），
	// 全部回正。
	xaiTextModel("grok-4.5", 500_000, 2, 0.3, 6, xaiTextModelMetadata{
		releaseDate:               "2026-07-08",
		supportedReasoningEfforts: []string{"low", "medium", "high", "xhigh"},
		defaultReasoningEffort:    "high",
		responsesWebSearch:        true,
	}),
	// grok-4.3: the official model page lists no release date; 2026-05-01 is
	// an approximate anchor from the first press coverage. Effort set is the
	// one both the model page and the reasoning docs agree on.
	// 2026-10-05 复核批确认：现值 $1.25/$0.20/$2.50 + 上下文 1M + 长上下文
	// 默认 2x（官方 pricing "< 200k $1.25/$0.20/$2.50、≥ 200k $2.50/$0.40/
	// $5.00 | Context 1M"）无需回正，现售中。
	xaiTextModel("grok-4.3", 1_000_000, 1.25, 0.2, 2.5, xaiTextModelMetadata{
		releaseDate:               "2026-05-01",
		supportedReasoningEfforts: []string{"none", "low", "medium", "high"},
		defaultReasoningEffort:    "low",
	}),
	// grok-4.20-0309 系与 grok-build-0.1：官方 may-15-retirement 明文
	// 2026-05-15 退役（ShutdownDate 只落官方明文日期，裁决 §2.3）。
	xaiTextModel("grok-4.20-0309-reasoning", 1_000_000, 1.25, 0.2, 2.5, xaiTextModelMetadata{
		releaseDate:  "2026-03-10",
		shutdownDate: "2026-05-15",
	}),
	xaiTextModel("grok-4.20-0309-non-reasoning", 1_000_000, 1.25, 0.2, 2.5, xaiTextModelMetadata{
		releaseDate:  "2026-03-10",
		shutdownDate: "2026-05-15",
	}),
	// releaseDate 官方退役页未给（旧快照 2026-05-19 无法回溯为官方值），
	// 按只落官方明文裁决置空。
	xaiTextModel("grok-build-0.1", 256_000, 1, 0.2, 2, xaiTextModelMetadata{
		shutdownDate: "2026-05-15",
	}),
	// multi-agent 的 reasoning effort 官方语义是协作 agent 数量
	// （low/medium=4，high/xhigh=16），不是推理深度；官方未标默认档。
	xaiTextModel("grok-4.20-multi-agent-0309", 1_000_000, 1.25, 0.2, 2.5, xaiTextModelMetadata{
		releaseDate:               "2026-03-10",
		shutdownDate:              "2026-05-15",
		supportedReasoningEfforts: []string{"low", "medium", "high", "xhigh"},
		supportedAPIProtocols:     []string{"responses"},
	}),
	{
		Model: "grok-imagine-image-2.0", Mode: "image_generation",
		ReleaseDate:           "2026-08-07",
		OutputCostPerImage:    f64p(0.04),
		SupportedAPIProtocols: []string{"images"},
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"image"},
	},
	{
		Model: "grok-imagine-image", Mode: "image_generation",
		ReleaseDate:           "2026-03-02",
		OutputCostPerImage:    f64p(0.02),
		SupportedAPIProtocols: []string{"images"},
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"image"},
	},
	{
		// 2026-10-05 复核批注释补齐：release-notes 明文 2026-11-02 退役，
		// "60 天通知期 2026-09-02 起；到期后 slug 重定向 grok-imagine-image-2.0
		// quality=low，slug 不失效，价格更低"（Requests to the slug will be
		// served by grok-imagine-image-2.0 with quality set to low）。
		Model: "grok-imagine-image-quality", Mode: "image_generation",
		ReleaseDate:           "2026-04-03",
		ShutdownDate:          "2026-11-02",
		OutputCostPerImage:    f64p(0.05),
		SupportedAPIProtocols: []string{"images"},
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"image"},
	},
	// 视频（M3 回填池，契约 §6.1）：Grok Imagine Video 1.5 文/图生视频
	//（image 首帧承接公共 input_reference，url/base64 双形态直传），1–15 秒、
	// 七档宽高比、480p/720p/1080p、音轨默认开启；统一 /v1/videos 面经 xai
	// adapter 改写。ReleaseDate 2026-07-31 官方发布日。
	// 2026-10-05 核对批回正：官方 Imagine Pricing 明文三档每秒价——480p
	// $0.08 / 720p $0.14 / 1080p $0.25 per sec，目录单槽位沿 sora/Hailuo
	// 「非最低档主档」先例落 720p $0.14/s，480p/1080p 分档注释登记；上一轮
	// 复核批「官方统一 $0.080/sec 无分档」系漏读分档表，作废。
	{
		Model:                    "grok-imagine-video-1.5",
		Mode:                     "video",
		ReleaseDate:              "2026-07-31",
		SupportedAPIProtocols:    []string{"video"},
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"video"},
		VideoOutputCostPerSecond: f64p(0.14),
	},
	// 2026-10-05 全厂商补全批新增（官方 docs.x.ai pricing/voice 页 USD
	// 明文，计划 §3.1）：
	{
		// 2026-10-05 复核批回正：官方 Voice Pricing 明文 "Speech to Text |
		// $0.10 / hr (REST), $0.20 / hr (Streaming)"，网关 audio_transcription
		// 走 REST 批量面 → 按小时明文价直除 0.1/3600（运行时除法，裁决
		// §2.4），上一轮误按 Streaming $0.20/hr 换算。ReleaseDate 2026-09-18
		// 官方 release-notes 发布日。
		Model: "grok-voice-transcribe-2.0", Mode: "audio", ReleaseDate: "2026-09-18",
		InputModalities:         []string{"audio"},
		OutputModalities:        []string{"text"},
		SupportedAPIProtocols:   []string{"audio_transcription"},
		AudioInputCostPerSecond: f64p(0.1 / 3600),
	},
	{
		// 实时语音对话（voice agent）：官方 $0.05/分钟明文 → 每秒
		// usdPerMinuteToPerSecond(0.05)（直除换算，不取整）。mode/协议对照
		// gpt-realtime 现行行（mode=audio + realtime 会话面，text+audio
		// 双向模态）。官方 -latest 别名当前指向本行（别名不做目录行，
		// 由注释登记）。
		Model: "grok-voice-agent", Mode: "audio",
		InputModalities:         []string{"text", "audio"},
		OutputModalities:        []string{"text", "audio"},
		SupportedAPIProtocols:   []string{"realtime"},
		AudioInputCostPerSecond: usdPerMinuteToPerSecond(0.05),
	},
}
