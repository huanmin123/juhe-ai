package pricing

// Qwen pricing snapshot。M3 媒体供应商新增（媒体设计 §9/契约 §10.1/§10.2；
// curated 2026-10-04，接入核对于阿里云百炼 legacy 万相 API 参考与定价检索）；
// A2 批模型目录补全扩至 16 行（curated 2026-10-05）：既有 2 行媒体行
// （wan2.2-t2v-plus 视频 + paraformer-v2 长转写）+ 新增 8 行 chat
// （qwen3.8/3.7 系、视觉 vl、代码 coder、全模态 omni；百炼 OpenAI 兼容
// compatible-mode）+ 2 行 audio_speech（qwen3-tts-flash/cosyvoice-v3-flash）
// + 1 行 audio_transcription（qwen3-asr-flash）+ 2 行 video（wan3.0-video/
// wan2.7-t2v）+ 1 行 image（qwen-image-3.0）。计价落法（人民币换算裁决，
// 计划 §2.1/§2.6；2026-10-05 A3 批用户裁决：不允许无价）：
//   - 百炼官方仅人民币明文价（北京地域，来源 help.aliyun.com/zh/model-studio
//     /billing-for-model-studio 与 /zh/model-studio/text-generation-model）：
//     chat 全 token 阶梯计费、TTS 按字符、ASR 按秒、视频按秒 × 分辨率档、
//     图像按张 × 分辨率档——目录价格结构无分段/阶梯通道，阶梯/分档价落
//     最常用主档（行注释登记调研口径）；人民币价按约定汇率 7.0 换算落
//     USD（Source 元信息可溯），官方国际站 USD 明文可查证后覆盖。换算值
//     保留 4 位有效数字，行注释登记「X ÷ 7.0 = Y」可复核。
//   - 既有 M3 媒体行（wan2.2-t2v-plus / paraformer-v2）A3 批调研未覆盖
//     其单价，保持无价（0 计费 + usage_missing 兜底，契约 §2.8），待调研
//     回填后再按约定汇率换算落价。
//
// 2026-10-05 复核批（官方国际站 USD 明文覆盖，alibabacloud.com/help/en/
// model-studio/model-pricing 新加坡 International 口径）：15 行落官方 USD
// 明文价并删除换算三件套（SourcePricingCurrency/Rate/Date）——chat 8 行
// （3.8-max $2/$6、3.8-flash $0.15/$0.47、3.7-plus $0.4/$1.6 首段、
// 3.7-flash $0.030/$0.130 首段、vl-plus $0.2/$1.6、vl-flash $0.05/$0.4、
// coder-plus $1/$5 首段、omni-flash text $0.43+音频 $3.81/输出 text
// $1.66）、audio 3 行（tts-flash perChar(10)、cosyvoice-v3-flash
// perChar(13)、asr-flash $0.000035/秒）、video 2 行（wan3.0-video
// $0.10/秒 720p 牌价、wan2.7-t2v $0.10/秒 720p）、image 1 行
// （qwen-image-3.0 $0.03/张）、embedding 1 行（text-embedding-v4
// perToken(0.07)）。SourcePricingNote 改为国际站明文 + 国内人民币原句
// 留档。wan2.2-t2v-plus / paraformer-v2 国际站未售且调研未覆盖，保持无价。
//   - usage 计量照抽（契约 §10.1/§10.2：轮询响应 usage 是 DashScope 的 JSON
//     字符串形态，adapter 双形态兼容解析）：万相 wan2.5 及以下版本字段族
//     video_duration/video_ratio，wan2.6 字段族 duration/
//     input_video_duration/output_video_duration/SR/size/video_count
//     （2026-10-04 官方 legacy API 参考核实）——成功终态按
//     video_duration → output_video_duration → duration 顺序取第一个正值填
//     OutputVideoSeconds；paraformer 长转写取 duration 正值秒填
//     AudioInputSeconds（契约 §10.2 时长计量，usage 行计量照落）；解析失败或
//     无正值字段不报错、不填秒（该路径才落 usage_missing 标记，契约 §2.8
//     兜底）。
var qwenModelPricingData = []rawModel{
	{
		// 视频：mode=video、协议 video（统一 /v1/videos 面经万相 adapter 改写，
		// 契约 §10.1）；输入 text+image（文生/图生视频，input.img_url 承接公共
		// input_reference，url/base64 双形态直传）；M3 第五批收录
		// wan2.2-t2v-plus（文生视频主档，480P/1080P、5 秒、30fps、MP4）。
		// A3 批调研未覆盖该行单价 → 保持无价（0 计费 + usage_missing 兜底），
		// 待调研回填后再按约定汇率 7.0 换算落价。
		Model:                 "wan2.2-t2v-plus",
		Mode:                  "video",
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"video"},
		SupportedAPIProtocols: []string{"video"},
	},
	{
		// 长转写：mode=audio、协议 audio_transcription（统一 /v1/audio/jobs 面经
		// paraformer adapter 改写，契约 §10.2，M3f）；输入是公网音频 URL
		//（input_url → input.file_urls，零存储不暂存），输出转写结果 JSON
		// 文件；收录 paraformer-v2（通用长转写档，录音文件识别异步任务）。
		// A3 批调研未覆盖该行单价 → 保持无价（0 计费 + usage_missing 兜底），
		// 待调研回填后再按约定汇率 7.0 换算落价。
		Model:                 "paraformer-v2",
		Mode:                  "audio",
		InputModalities:       []string{"audio"},
		OutputModalities:      []string{"text"},
		SupportedAPIProtocols: []string{"audio_transcription"},
	},
	{
		// 对话：qwen3.8-max（旗舰档，1M 上下文，百炼 OpenAI 兼容
		// compatible-mode → chat_completions）。2026-10-05 复核批：官方国际
		// 站 USD 明文价覆盖换算值——输入 $2 / 输出 $6 每百万 tokens
		//（alibabacloud.com/help/en/model-studio/model-pricing，新加坡
		// International 口径）；国内人民币价 12/36 元原句留档。
		Model:                    "qwen3.8-max",
		Mode:                     "chat",
		ContextWindowTokens:      intp(1_000_000),
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"text"},
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		InputCostPerToken:        perToken(2),
		OutputCostPerToken:       perToken(6),
		SourcePricingNote:        "官方国际站 USD 明文价（alibabacloud.com/help/en/model-studio/model-pricing 新加坡 International：输入 $2 / 输出 $6 每百万 tokens）；国内人民币价 12/36 元原句留档（百炼计费页 help.aliyun.com/zh/model-studio/billing-for-model-studio）",
	},
	{
		// 对话：qwen3.8-flash（快速档，1M 上下文）。2026-10-05 复核批：官方
		// 国际站 USD 明文价覆盖换算值——输入 $0.15 / 输出 $0.47 每百万
		// tokens（alibabacloud.com/help/en/model-studio/model-pricing）；
		// 国内人民币价 0.8/2.7 元原句留档。
		Model:                    "qwen3.8-flash",
		Mode:                     "chat",
		ContextWindowTokens:      intp(1_000_000),
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"text"},
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		InputCostPerToken:        perToken(0.15),
		OutputCostPerToken:       perToken(0.47),
		SourcePricingNote:        "官方国际站 USD 明文价（alibabacloud.com/help/en/model-studio/model-pricing 新加坡 International：输入 $0.15 / 输出 $0.47 每百万 tokens）；国内人民币价 0.8/2.7 元原句留档（百炼计费页 help.aliyun.com/zh/model-studio/billing-for-model-studio）",
	},
	{
		// 对话：qwen3.7-plus（均衡档，1M 上下文）。2026-10-05 复核批：官方
		// 国际站 USD 明文价覆盖换算值——输入 $0.4 / 输出 $1.6 每百万 tokens
		//（首段；alibabacloud.com/help/en/model-studio/model-pricing，官方
		// 限时 20% off 折扣价口径，注释登记）；国内人民币价 2/8 元原句留档。
		Model:                    "qwen3.7-plus",
		Mode:                     "chat",
		ContextWindowTokens:      intp(1_000_000),
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"text"},
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		InputCostPerToken:        perToken(0.4),
		OutputCostPerToken:       perToken(1.6),
		SourcePricingNote:        "官方国际站 USD 明文价（alibabacloud.com/help/en/model-studio/model-pricing 新加坡 International：输入 $0.4 / 输出 $1.6 每百万 tokens 首段，限时 20% off）；国内人民币价 2/8 元原句留档（百炼计费页 help.aliyun.com/zh/model-studio/billing-for-model-studio）",
	},
	{
		// 对话：qwen3.7-flash（快速档，1M 上下文）。2026-10-05 复核批：官方
		// 国际站 USD 明文价覆盖换算值——输入 $0.030 / 输出 $0.130 每百万
		// tokens（首段；alibabacloud.com/help/en/model-studio/model-pricing）；
		// 国内人民币价 0.2/0.8 元原句留档。
		Model:                    "qwen3.7-flash",
		Mode:                     "chat",
		ContextWindowTokens:      intp(1_000_000),
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"text"},
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		InputCostPerToken:        perToken(0.030),
		OutputCostPerToken:       perToken(0.130),
		SourcePricingNote:        "官方国际站 USD 明文价（alibabacloud.com/help/en/model-studio/model-pricing 新加坡 International：输入 $0.030 / 输出 $0.130 每百万 tokens 首段）；国内人民币价 0.2/0.8 元原句留档（百炼计费页 help.aliyun.com/zh/model-studio/billing-for-model-studio）",
	},
	{
		// 对话视觉：qwen3-vl-plus（视觉理解档，text+image→text）。2026-10-05
		// 复核批：官方国际站 USD 明文价覆盖换算值——输入 $0.2 / 输出 $1.6
		// 每百万 tokens（alibabacloud.com/help/en/model-studio/
		// model-pricing）；国内人民币价 1/10 元原句留档。
		Model:                    "qwen3-vl-plus",
		Mode:                     "chat",
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"text"},
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		InputCostPerToken:        perToken(0.2),
		OutputCostPerToken:       perToken(1.6),
		SourcePricingNote:        "官方国际站 USD 明文价（alibabacloud.com/help/en/model-studio/model-pricing 新加坡 International：输入 $0.2 / 输出 $1.6 每百万 tokens）；国内人民币价 1/10 元原句留档（百炼文本生成模型列表 help.aliyun.com/zh/model-studio/text-generation-model）",
	},
	{
		// 对话视觉：qwen3-vl-flash（视觉理解快速档，text+image→text）。
		// 2026-10-05 复核批：官方国际站 USD 明文价覆盖换算值——输入 $0.05 /
		// 输出 $0.4 每百万 tokens（alibabacloud.com/help/en/model-studio/
		// model-pricing）；国内人民币价 0.15/1.5 元原句留档。
		Model:                    "qwen3-vl-flash",
		Mode:                     "chat",
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"text"},
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		InputCostPerToken:        perToken(0.05),
		OutputCostPerToken:       perToken(0.4),
		SourcePricingNote:        "官方国际站 USD 明文价（alibabacloud.com/help/en/model-studio/model-pricing 新加坡 International：输入 $0.05 / 输出 $0.4 每百万 tokens）；国内人民币价 0.15/1.5 元原句留档（百炼文本生成模型列表 help.aliyun.com/zh/model-studio/text-generation-model）",
	},
	{
		// 对话代码：qwen3-coder-plus（代码专精档）；官方输入长度 256k-1M
		// 阶梯、无单一上下文窗口值 → Capacity 不填。2026-10-05 复核批：官方
		// 国际站 USD 明文价覆盖换算值——输入 $1 / 输出 $5 每百万 tokens
		//（首段；alibabacloud.com/help/en/model-studio/model-pricing）；
		// 国内人民币价 4/16 元原句留档。
		Model:                    "qwen3-coder-plus",
		Mode:                     "chat",
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"text"},
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		InputCostPerToken:        perToken(1),
		OutputCostPerToken:       perToken(5),
		SourcePricingNote:        "官方国际站 USD 明文价（alibabacloud.com/help/en/model-studio/model-pricing 新加坡 International：输入 $1 / 输出 $5 每百万 tokens 首段）；国内人民币价 4/16 元原句留档（百炼文本生成模型列表 help.aliyun.com/zh/model-studio/text-generation-model）",
	},
	{
		// 对话全模态：qwen3-omni-flash（全模态档，text+image+audio→text）。
		// 2026-10-05 复核批：官方国际站 USD 明文价覆盖换算值——文本输入
		// $0.43、音频输入 $3.81、文本输出 $1.66 每百万 tokens
		//（alibabacloud.com/help/en/model-studio/model-pricing）；多模态
		//（image）输入与音频/多模态输出价官方同页另列（目录无对应槽位，
		// 注释登记不落字段）；国内人民币价 0.8/2.7 元原句留档。
		Model:                    "qwen3-omni-flash",
		Mode:                     "chat",
		InputModalities:          []string{"text", "image", "audio"},
		OutputModalities:         []string{"text"},
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		InputCostPerToken:        perToken(0.43),
		InputCostPerAudioToken:   perToken(3.81),
		OutputCostPerToken:       perToken(1.66),
		SourcePricingNote:        "官方国际站 USD 明文价（alibabacloud.com/help/en/model-studio/model-pricing 新加坡 International：text 输入 $0.43 / audio 输入 $3.81 / text 输出 $1.66 每百万 tokens；image 输入与音频/多模态输出价同页另列未落）；国内人民币价 0.8/2.7 元原句留档（百炼文本生成模型列表 help.aliyun.com/zh/model-studio/text-generation-model）",
	},
	{
		// 语音合成：mode=audio、协议 audio_speech；qwen3-tts-flash
		//（text→audio，cosyvoice 系现行 flash 档）。2026-10-05 复核批：官方
		// 国际站 USD 明文价覆盖换算值——$0.1/万字符（= $10/百万字符，
		// alibabacloud.com/help/en/model-studio/model-pricing）落
		// TtsInputCostPerChar；国内人民币价 0.8 元/万字符原句留档。
		Model:                 "qwen3-tts-flash",
		Mode:                  "audio",
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"audio"},
		SupportedAPIProtocols: []string{"audio_speech"},
		TtsInputCostPerChar:   perChar(10),
		SourcePricingNote:     "官方国际站 USD 明文价（alibabacloud.com/help/en/model-studio/model-pricing 新加坡 International：$0.1/万字符 = $10/百万字符）；国内人民币价 0.8 元/万字符原句留档（百炼计费页 help.aliyun.com/zh/model-studio/billing-for-model-studio）",
	},
	{
		// 语音合成：mode=audio、协议 audio_speech；cosyvoice-v3-flash
		//（text→audio，CosyVoice 3 代 flash 档）。2026-10-05 复核批：官方
		// 国际站 USD 明文价覆盖换算值——$0.13/万字符（= $13/百万字符，
		// alibabacloud.com/help/en/model-studio/model-pricing）落
		// TtsInputCostPerChar；国内人民币价 1 元/万字符原句留档。
		Model:                 "cosyvoice-v3-flash",
		Mode:                  "audio",
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"audio"},
		SupportedAPIProtocols: []string{"audio_speech"},
		TtsInputCostPerChar:   perChar(13),
		SourcePricingNote:     "官方国际站 USD 明文价（alibabacloud.com/help/en/model-studio/model-pricing 新加坡 International：$0.13/万字符 = $13/百万字符）；国内人民币价 1 元/万字符原句留档（百炼计费页 help.aliyun.com/zh/model-studio/billing-for-model-studio）",
	},
	{
		// 实时转写：mode=audio、协议 audio_transcription；qwen3-asr-flash
		//（audio→text，实时识别档）。2026-10-05 复核批：官方国际站 USD 明文
		// 价覆盖换算值——$0.000035/秒（alibabacloud.com/help/en/model-studio/
		// model-pricing）落 AudioInputCostPerSecond；国内人民币价 0.00022
		// 元/秒原句留档。
		Model:                   "qwen3-asr-flash",
		Mode:                    "audio",
		InputModalities:         []string{"audio"},
		OutputModalities:        []string{"text"},
		SupportedAPIProtocols:   []string{"audio_transcription"},
		AudioInputCostPerSecond: f64p(0.000035),
		SourcePricingNote:       "官方国际站 USD 明文价（alibabacloud.com/help/en/model-studio/model-pricing 新加坡 International：$0.000035/秒）；国内人民币价 0.00022 元/秒原句留档（百炼计费页 help.aliyun.com/zh/model-studio/billing-for-model-studio）",
	},
	{
		// 视频：wan3.0-video（万相 3.0 统一视频档，text+image→video，协议
		// video 经万相 adapter 改写，契约 §10.1）。2026-10-05 复核批：官方
		// 国际站 USD 明文价覆盖换算值——$0.10/秒（720p 牌价；
		// alibabacloud.com/help/en/model-studio/model-pricing，官方限时
		// 30% off 折扣另列，注释登记）；国内人民币 720P 0.6 元/秒原句留档。
		Model:                    "wan3.0-video",
		Mode:                     "video",
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"video"},
		SupportedAPIProtocols:    []string{"video"},
		VideoOutputCostPerSecond: f64p(0.1),
		SourcePricingNote:        "官方国际站 USD 明文价（alibabacloud.com/help/en/model-studio/model-pricing 新加坡 International：720p $0.10/秒 牌价，限时 30% off 另列）；国内人民币价 720P 0.6 元/秒原句留档（百炼计费页 help.aliyun.com/zh/model-studio/billing-for-model-studio）",
	},
	{
		// 视频：wan2.7-t2v（万相 2.7 文生视频档，text→video，协议 video）。
		// 2026-10-05 复核批：官方国际站 USD 明文价覆盖换算值——$0.10/秒
		//（720p；alibabacloud.com/help/en/model-studio/model-pricing）；
		// 国内人民币 720P 0.6 元/秒原句留档。
		Model:                    "wan2.7-t2v",
		Mode:                     "video",
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"video"},
		SupportedAPIProtocols:    []string{"video"},
		VideoOutputCostPerSecond: f64p(0.10),
		SourcePricingNote:        "官方国际站 USD 明文价（alibabacloud.com/help/en/model-studio/model-pricing 新加坡 International：720p $0.10/秒）；国内人民币价 720P 0.6 元/秒原句留档（百炼计费页 help.aliyun.com/zh/model-studio/billing-for-model-studio）",
	},
	{
		// 图像：qwen-image-3.0（文生图像档，images 协议）。2026-10-05 复核
		// 批：官方国际站 USD 明文价覆盖换算值——$0.03/张
		//（alibabacloud.com/help/en/model-studio/model-pricing）落
		// OutputCostPerImage；国内人民币价 0.18 元/张（1K 档）原句留档。
		Model:                 "qwen-image-3.0",
		Mode:                  "image_generation",
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"image"},
		SupportedAPIProtocols: []string{"images"},
		OutputCostPerImage:    f64p(0.03),
		SourcePricingNote:     "官方国际站 USD 明文价（alibabacloud.com/help/en/model-studio/model-pricing 新加坡 International：$0.03/张）；国内人民币价 0.18 元/张原句留档（百炼计费页 help.aliyun.com/zh/model-studio/billing-for-model-studio）",
	},
	{
		// 向量嵌入：text-embedding-v4（官方推荐档，embed_content 协议，对照
		// gemini-embedding / glm embedding-3 行写法；仅目录展示——qwen 档案
		// 无 embed_content 承接 family）。2026-10-05 复核批：官方国际站 USD
		// 明文价覆盖换算值——$0.07/百万 tokens
		//（alibabacloud.com/help/en/model-studio/model-pricing）；国内人民
		// 币价 0.5 元原句留档。
		Model:                 "text-embedding-v4",
		Mode:                  "embedding",
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"text"},
		SupportedAPIProtocols: []string{"embed_content"},
		InputCostPerToken:     perToken(0.07),
		SourcePricingNote:     "官方国际站 USD 明文价（alibabacloud.com/help/en/model-studio/model-pricing 新加坡 International：$0.07/百万 tokens）；国内人民币价 0.5 元/百万 tokens 原句留档（百炼计费页 help.aliyun.com/zh/model-studio/billing-for-model-studio）",
	},
}
