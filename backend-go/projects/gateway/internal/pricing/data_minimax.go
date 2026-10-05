package pricing

// MiniMax pricing snapshot, M3 media provider addition (媒体设计 §9/契约
// §8.2/§8.1；curated 2026-10-04，接入核对于 platform.minimax.cn 按量计费
// 页)。
//
// 2026-10-05 全厂商补全批（依据 docs/plans/计划-20261005T110000000Z-模型
// 目录全厂商补全与场景化展示.md §3.1/§3.2/§2，来源 = 计划 §6.1：
// platform.minimax.io 国际站 pricing-paygo / pricing overview / models-intro，
// USD 明文）：新增 11 行（MiniMax-M3 / -M2.7 / -M2.7-highspeed chat 三行、
// speech-2.8-hd / speech-2.8-turbo / speech-2.6-hd / speech-2.6-turbo TTS
// 四行、asr-1.0 转写、MiniMax-H3 / -H3-Max 视频两行、image-01 图像）；
// 修正 2 行（speech-02-turbo 补国际站 USD 字符价、MiniMax-Hailuo-2.3 登记
// 国际站按条价口径但不折秒）。只落官方明文 USD 价（裁决 §2.1），国内站
// 人民币价不折算落库；分段/阶梯档不结构化、注释登记（裁决 §2.6）。
// CatalogOrder 自本批起显式编号：存量两行 0/1，新增行排续 2 起。
//
// 计价落法（不编造）：
//   - MiniMax-Hailuo-2.3（视频）：官方按次/档位计费（分辨率 × 时长），
//     国际站定价页明文为按条价（768P-6s $0.28/条）；按条 → 每秒是推导，
//     禁止折算（裁决 §2.4）——不落 VideoOutputCostPerSecond；且轮询响应
//     不回报时长（契约 §8.1 query 响应仅 status/file_id/file_download_url）
//     ——终态计费走契约 §2.8 兜底（0 计费 + usage_missing 标记），同 glm
//     cogvideox 先例。
var minimaxModelPricingData = []rawModel{
	{
		// TTS：mode=audio、协议 audio_speech（统一 /v1/audio/speech 面）。
		// ResponseFormats 为 §8.2 词表（mp3/pcm/flac/wav，零转码）。
		// 2026-10-05 补价：国际站 platform.minimax.io 明文 $60/1M chars
		// （TtsInputCostPerChar=perChar(60)）；国内站 platform.minimax.cn
		// 口径为 ¥2.00/万字符（人民币明文，不折算落库，裁决 §2.1）。
		Model:                 "speech-02-turbo",
		Mode:                  "audio",
		CatalogOrder:          intp(0),
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"audio"},
		SupportedAPIProtocols: []string{"audio_speech"},
		ResponseFormats:       []string{"mp3", "pcm", "flac", "wav"},
		TtsInputCostPerChar:   perChar(60),
	},
	{
		// 视频：mode=video、协议 video；输入 text+image（文生/图生视频，
		// first_frame_image 承接公共 input_reference）。
		// 2026-10-05 口径登记：国际站定价页按条明文（768P-6s $0.28/条），
		// 按条 → 每秒是推导、禁止折秒（裁决 §2.4），保持无价（见文件头
		// 计价落法；0 计费 + usage_missing 兜底）。
		Model:                 "MiniMax-Hailuo-2.3",
		Mode:                  "video",
		CatalogOrder:          intp(1),
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"video"},
		SupportedAPIProtocols: []string{"video"},
	},
	{
		// 对话主模型（platform.minimax.io 国际站 USD 明文，2026-10-05）：
		// ≤512k 档 in $0.30 / out $1.20 per 1M tokens、缓存读 $0.06。
		// 上下文 1M；官方单次输出上限 512k，128k 为目录建议值（MaxOutput
		// 无"官方上限"独立通道，注释登记 512k 上限）。
		// 分段/档位不结构化（裁决 §2.6）：>512k 档 in $0.60 / out $2.40 /
		// 缓存读 $0.12（恰为 ≤512k 档 2 倍）、priority 档 1.5 倍，注释登记
		// 官方明文；目录价落 ≤512k 主档。
		Model: "MiniMax-M3", Mode: "chat", CatalogOrder: intp(2), ReleaseDate: "2026-06-01",
		ContextWindowTokens: intp(1_000_000), MaxOutputTokens: intp(128_000),
		// 官方明文输入模态：文本+图像+视频（platform.minimax.io models-intro）。
		InputModalities:       []string{"text", "image", "video"},
		OutputModalities:      []string{"text"},
		SupportedAPIProtocols: []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"},
			[]string{"function_calling"}),
		InputCostPerToken:       perToken(0.30),
		OutputCostPerToken:      perToken(1.20),
		CacheReadInputTokenCost: perToken(0.06),
		SupportsPromptCaching:   true,
	},
	{
		// 对话模型（国际站 USD 明文）：204.8k 上下文，in $0.30 / out $1.20
		// per 1M tokens，缓存读 $0.06、缓存写 $0.375。
		Model: "MiniMax-M2.7", Mode: "chat", CatalogOrder: intp(3), ReleaseDate: "2026-03-18",
		ContextWindowTokens:   intp(204_800),
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"text"},
		SupportedAPIProtocols: []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"},
			[]string{"function_calling"}),
		InputCostPerToken:           perToken(0.3),
		OutputCostPerToken:          perToken(1.2),
		CacheReadInputTokenCost:     perToken(0.06),
		CacheCreationInputTokenCost: perToken(0.375),
		SupportsPromptCaching:       true,
	},
	{
		// 高速档对话模型（国际站 USD 明文）：in $0.60 / out $2.40 per 1M
		// tokens；上下文与 M2.7 同族 204.8k。官方未列独立缓存价，不落。
		Model: "MiniMax-M2.7-highspeed", Mode: "chat", CatalogOrder: intp(4),
		ContextWindowTokens:   intp(204_800),
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"text"},
		SupportedAPIProtocols: []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"},
			[]string{"function_calling"}),
		InputCostPerToken:  perToken(0.6),
		OutputCostPerToken: perToken(2.4),
	},
	{
		// TTS（国际站 USD 明文）：speech-2.8-hd $100/1M chars，audio_speech；
		// ResponseFormats 沿 speech-02-turbo 零转码词表 §8.2（官方全集 7 值，pcmu 系/opus 不在零转码清单）。
		Model: "speech-2.8-hd", Mode: "audio", CatalogOrder: intp(5), ReleaseDate: "2026-01-23",
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"audio"},
		SupportedAPIProtocols: []string{"audio_speech"},
		ResponseFormats:       []string{"mp3", "pcm", "flac", "wav"},
		TtsInputCostPerChar:   perChar(100),
	},
	{
		// TTS（国际站 USD 明文）：speech-2.8-turbo $60/1M chars。
		Model: "speech-2.8-turbo", Mode: "audio", CatalogOrder: intp(6), ReleaseDate: "2026-01-23",
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"audio"},
		SupportedAPIProtocols: []string{"audio_speech"},
		ResponseFormats:       []string{"mp3", "pcm", "flac", "wav"},
		TtsInputCostPerChar:   perChar(60),
	},
	{
		// TTS（国际站 USD 明文）：speech-2.6-hd $100/1M chars。
		Model: "speech-2.6-hd", Mode: "audio", CatalogOrder: intp(7), ReleaseDate: "2025-10-29",
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"audio"},
		SupportedAPIProtocols: []string{"audio_speech"},
		ResponseFormats:       []string{"mp3", "pcm", "flac", "wav"},
		TtsInputCostPerChar:   perChar(100),
	},
	{
		// TTS（国际站 USD 明文）：speech-2.6-turbo $60/1M chars。
		Model: "speech-2.6-turbo", Mode: "audio", CatalogOrder: intp(8), ReleaseDate: "2025-10-29",
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"audio"},
		SupportedAPIProtocols: []string{"audio_speech"},
		ResponseFormats:       []string{"mp3", "pcm", "flac", "wav"},
		TtsInputCostPerChar:   perChar(60),
	},
	{
		// ASR（国际站 USD 明文）：$0.38/小时 → 每秒 0.38/3600（运行时除法，
		// 官方小时明文价直除换算 per-second，裁决 §2.4；分钟回转无损校验：
		// ×3600 = $0.38/h）。
		Model: "asr-1.0", Mode: "audio", CatalogOrder: intp(9),
		InputModalities:         []string{"audio"},
		OutputModalities:        []string{"text"},
		SupportedAPIProtocols:   []string{"audio_transcription"},
		AudioInputCostPerSecond: f64p(0.38 / 3600),
	},
	{
		// 视频（国际站 USD 明文）：MiniMax-H3 文/图生视频，768P $0.08/秒
		// （主档）；2K 档 $0.13/秒为分辨率分档价，目录无分辨率分档通道，
		// 沿 sora/veo 先例单值落主档、分档注释登记（M2 计价落法）。
		// 输入素材（图生视频参考图）官方另有计费口径，本轮未收录其 USD
		// 明文，不落（usage 兜底同 §2.8）。
		Model: "MiniMax-H3", Mode: "video", CatalogOrder: intp(10), ReleaseDate: "2026-07-31",
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"video"},
		SupportedAPIProtocols:    []string{"video"},
		VideoOutputCostPerSecond: f64p(0.08),
	},
	{
		// 视频（国际站 USD 明文）：MiniMax-H3-Max 480P $0.05/秒（主档）；
		// 768P 档 $0.08/秒分档注释登记，不结构化。官方未给独立发布日，
		// ReleaseDate 留空（不编造）。
		Model: "MiniMax-H3-Max", Mode: "video", CatalogOrder: intp(11),
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"video"},
		SupportedAPIProtocols:    []string{"video"},
		VideoOutputCostPerSecond: f64p(0.05),
	},
	{
		// 图像生成（国际站 USD 明文）：image-01 $0.0035/张，按张计价
		// OutputCostPerImage（对照 openai gpt-image 行结构，无按张外的
		// token 明文，不落）。
		Model: "image-01", Mode: "image_generation", CatalogOrder: intp(12), ReleaseDate: "2025-02-15",
		SupportedAPIProtocols: []string{"images"},
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"image"},
		OutputCostPerImage:    f64p(0.0035),
	},
}
