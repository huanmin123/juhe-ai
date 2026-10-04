package pricing

// xAI pricing snapshot, ported from
// backend/src/modules/model-pricing/xai-model-pricing.data.ts (curated
// 2026-08-26). Models with a 200k threshold charge the higher rate for all
// request tokens once the prompt reaches the threshold.
//
// M3 回填池媒体增补（契约 §6.1，curated 2026-10-04）：grok-imagine-video-1.5
// 视频行，计价落法（不编造）：
//   - 官方 USD 秒价未核实（契约 §6.1 能力页经代理抓取核实报文，定价子页
//     未抓取）→ 不落 VideoOutputCostPerSecond；官方秒价可查证后再补
//    （qwen/minimax/volcengine 同先例）。
//   - 终态计费走契约 §2.8 兜底：轮询 done 的 video.duration 是时长计量
//     基源，adapter 照抽 OutputVideoSeconds；目录无价 → 0 计费 +
//     usage_missing 标记，计量照落成本不虚计。
type xaiTextModelMetadata struct {
	catalogVisible            *bool
	releaseDate               string
	supportedAPIProtocols     []string
	supportedReasoningEfforts []string
	defaultReasoningEffort    string
	// responsesWebSearch：该模型在 responses 协议下支持 hosted web_search
	//（xAI Responses API 联网搜索；按实测能力声明的近期 grok-4.x 文本模型开启）。
	responsesWebSearch bool
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
		SupportedReasoningEfforts:               metadata.supportedReasoningEfforts,
		DefaultReasoningEffort:                  metadata.defaultReasoningEffort,
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
	xaiTextModel("grok-4.7", 500_000, 2, 0.5, 6, xaiTextModelMetadata{
		releaseDate:               "2026-09-21",
		supportedReasoningEfforts: []string{"low", "medium", "high", "xhigh"},
		defaultReasoningEffort:    "high",
		responsesWebSearch:        true,
	}),
	xaiTextModel("grok-4.6", 500_000, 2, 0.5, 6, xaiTextModelMetadata{
		releaseDate:               "2026-08-12",
		supportedReasoningEfforts: []string{"low", "medium", "high", "xhigh"},
		defaultReasoningEffort:    "high",
		responsesWebSearch:        true,
	}),
	xaiTextModel("grok-4.5", 500_000, 2, 0.3, 6, xaiTextModelMetadata{
		releaseDate:               "2026-07-08",
		supportedReasoningEfforts: []string{"low", "medium", "high", "xhigh"},
		defaultReasoningEffort:    "high",
		responsesWebSearch:        true,
	}),
	// grok-4.3: the official model page lists no release date; 2026-05-01 is
	// an approximate anchor from the first press coverage. Effort set is the
	// one both the model page and the reasoning docs agree on.
	xaiTextModel("grok-4.3", 1_000_000, 1.25, 0.2, 2.5, xaiTextModelMetadata{
		releaseDate:               "2026-05-01",
		supportedReasoningEfforts: []string{"none", "low", "medium", "high"},
		defaultReasoningEffort:    "low",
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
	// multi-agent 的 reasoning effort 官方语义是协作 agent 数量
	// （low/medium=4，high/xhigh=16），不是推理深度；官方未标默认档。
	xaiTextModel("grok-4.20-multi-agent-0309", 1_000_000, 1.25, 0.2, 2.5, xaiTextModelMetadata{
		releaseDate:               "2026-03-10",
		supportedReasoningEfforts: []string{"low", "medium", "high", "xhigh"},
		supportedAPIProtocols:     []string{"responses"},
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
		ShutdownDate:          "2026-11-02",
		OutputCostPerImage:    f64p(0.05),
		SupportedAPIProtocols: []string{"images"},
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"image"},
	},
	// 视频（M3 回填池，契约 §6.1）：Grok Imagine Video 1.5 文/图生视频
	// （image 首帧承接公共 input_reference，url/base64 双形态直传），1–15 秒、
	// 七档宽高比、480p/720p/1080p、音轨默认开启；统一 /v1/videos 面经 xai
	// adapter 改写。不落秒价（官方 USD 秒价未核实，见文件头计价落法）。
	{
		Model:                 "grok-imagine-video-1.5",
		Mode:                  "video",
		SupportedAPIProtocols: []string{"video"},
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"video"},
	},
}
