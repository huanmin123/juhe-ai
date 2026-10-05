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
		// compatible-mode → chat_completions）；全 token 阶梯人民币计费，落
		// 0-128K 主档换算：输入 12 ÷ 7.0 = 1.714、输出 36 ÷ 7.0 = 5.143
		//（USD/百万 tokens，4 位有效数字），全阶梯见百炼计费页。来源：
		// 百炼计费页 help.aliyun.com/zh/model-studio/billing-for-model-studio
		//（2026-10-05 调研）。
		Model:                    "qwen3.8-max",
		Mode:                     "chat",
		ContextWindowTokens:      intp(1_000_000),
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"text"},
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		InputCostPerToken:        perToken(1.714),
		OutputCostPerToken:       perToken(5.143),
		SourcePricingCurrency:    "CNY",
		SourceExchangeRateToUsd:  f64p(7.0),
		SourceExchangeRateDate:   "2026-10-05",
		SourcePricingNote:        "官方人民币价 输入12/输出36 元/百万 tokens（0-128K 主档，百炼计费页 help.aliyun.com/zh/model-studio/billing-for-model-studio），按约定汇率 7.0 换算 $1.714/$5.143 每百万 tokens",
	},
	{
		// 对话：qwen3.8-flash（快速档，1M 上下文）；全 token 阶梯人民币计费，
		// 落 0-128K 主档换算：输入 0.8 ÷ 7.0 = 0.1143、输出 2.7 ÷ 7.0 =
		// 0.3857（USD/百万 tokens），全阶梯见百炼计费页。来源：百炼计费页
		// help.aliyun.com/zh/model-studio/billing-for-model-studio
		//（2026-10-05 调研）。
		Model:                    "qwen3.8-flash",
		Mode:                     "chat",
		ContextWindowTokens:      intp(1_000_000),
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"text"},
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		InputCostPerToken:        perToken(0.1143),
		OutputCostPerToken:       perToken(0.3857),
		SourcePricingCurrency:    "CNY",
		SourceExchangeRateToUsd:  f64p(7.0),
		SourceExchangeRateDate:   "2026-10-05",
		SourcePricingNote:        "官方人民币价 输入0.8/输出2.7 元/百万 tokens（0-128K 主档，百炼计费页 help.aliyun.com/zh/model-studio/billing-for-model-studio），按约定汇率 7.0 换算 $0.1143/$0.3857 每百万 tokens",
	},
	{
		// 对话：qwen3.7-plus（均衡档，1M 上下文）；全 token 阶梯人民币计费，
		// 落 0-128K 主档换算：输入 2 ÷ 7.0 = 0.2857、输出 8 ÷ 7.0 = 1.143
		//（USD/百万 tokens），全阶梯见百炼计费页。来源：百炼计费页
		// help.aliyun.com/zh/model-studio/billing-for-model-studio
		//（2026-10-05 调研）。
		Model:                    "qwen3.7-plus",
		Mode:                     "chat",
		ContextWindowTokens:      intp(1_000_000),
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"text"},
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		InputCostPerToken:        perToken(0.2857),
		OutputCostPerToken:       perToken(1.143),
		SourcePricingCurrency:    "CNY",
		SourceExchangeRateToUsd:  f64p(7.0),
		SourceExchangeRateDate:   "2026-10-05",
		SourcePricingNote:        "官方人民币价 输入2/输出8 元/百万 tokens（0-128K 主档，百炼计费页 help.aliyun.com/zh/model-studio/billing-for-model-studio），按约定汇率 7.0 换算 $0.2857/$1.143 每百万 tokens",
	},
	{
		// 对话：qwen3.7-flash（快速档，1M 上下文）；全 token 阶梯人民币计费，
		// 落 0-32K 主档换算：输入 0.2 ÷ 7.0 = 0.02857、输出 0.8 ÷ 7.0 =
		// 0.1143（USD/百万 tokens），全阶梯见百炼计费页。来源：百炼计费页
		// help.aliyun.com/zh/model-studio/billing-for-model-studio
		//（2026-10-05 调研）。
		Model:                    "qwen3.7-flash",
		Mode:                     "chat",
		ContextWindowTokens:      intp(1_000_000),
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"text"},
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		InputCostPerToken:        perToken(0.02857),
		OutputCostPerToken:       perToken(0.1143),
		SourcePricingCurrency:    "CNY",
		SourceExchangeRateToUsd:  f64p(7.0),
		SourceExchangeRateDate:   "2026-10-05",
		SourcePricingNote:        "官方人民币价 输入0.2/输出0.8 元/百万 tokens（0-32K 主档，百炼计费页 help.aliyun.com/zh/model-studio/billing-for-model-studio），按约定汇率 7.0 换算 $0.02857/$0.1143 每百万 tokens",
	},
	{
		// 对话视觉：qwen3-vl-plus（视觉理解档，text+image→text）；全 token
		// 阶梯人民币计费，落 0-128K 主档换算：输入 1 ÷ 7.0 = 0.1429、输出
		// 10 ÷ 7.0 = 1.429（USD/百万 tokens），全阶梯见百炼文本生成模型列表。
		// 来源：help.aliyun.com/zh/model-studio/text-generation-model
		//（2026-10-05 调研）。
		Model:                    "qwen3-vl-plus",
		Mode:                     "chat",
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"text"},
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		InputCostPerToken:        perToken(0.1429),
		OutputCostPerToken:       perToken(1.429),
		SourcePricingCurrency:    "CNY",
		SourceExchangeRateToUsd:  f64p(7.0),
		SourceExchangeRateDate:   "2026-10-05",
		SourcePricingNote:        "官方人民币价 输入1/输出10 元/百万 tokens（0-128K 主档，百炼文本生成模型列表 help.aliyun.com/zh/model-studio/text-generation-model），按约定汇率 7.0 换算 $0.1429/$1.429 每百万 tokens",
	},
	{
		// 对话视觉：qwen3-vl-flash（视觉理解快速档，text+image→text）；全
		// token 阶梯人民币计费，落 0-128K 主档换算：输入 0.15 ÷ 7.0 =
		// 0.02143、输出 1.5 ÷ 7.0 = 0.2143（USD/百万 tokens），全阶梯见百炼
		// 文本生成模型列表。来源：
		// help.aliyun.com/zh/model-studio/text-generation-model
		//（2026-10-05 调研）。
		Model:                    "qwen3-vl-flash",
		Mode:                     "chat",
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"text"},
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		InputCostPerToken:        perToken(0.02143),
		OutputCostPerToken:       perToken(0.2143),
		SourcePricingCurrency:    "CNY",
		SourceExchangeRateToUsd:  f64p(7.0),
		SourceExchangeRateDate:   "2026-10-05",
		SourcePricingNote:        "官方人民币价 输入0.15/输出1.5 元/百万 tokens（0-128K 主档，百炼文本生成模型列表 help.aliyun.com/zh/model-studio/text-generation-model），按约定汇率 7.0 换算 $0.02143/$0.2143 每百万 tokens",
	},
	{
		// 对话代码：qwen3-coder-plus（代码专精档）；官方输入长度 256k-1M
		// 阶梯、无单一上下文窗口值 → Capacity 不填；全 token 阶梯人民币
		// 计费，落 0-256K 主档换算：输入 4 ÷ 7.0 = 0.5714、输出 16 ÷ 7.0 =
		// 2.286（USD/百万 tokens），全阶梯见百炼文本生成模型列表。来源：
		// help.aliyun.com/zh/model-studio/text-generation-model
		//（2026-10-05 调研）。
		Model:                    "qwen3-coder-plus",
		Mode:                     "chat",
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"text"},
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		InputCostPerToken:        perToken(0.5714),
		OutputCostPerToken:       perToken(2.286),
		SourcePricingCurrency:    "CNY",
		SourceExchangeRateToUsd:  f64p(7.0),
		SourceExchangeRateDate:   "2026-10-05",
		SourcePricingNote:        "官方人民币价 输入4/输出16 元/百万 tokens（0-256K 主档，百炼文本生成模型列表 help.aliyun.com/zh/model-studio/text-generation-model），按约定汇率 7.0 换算 $0.5714/$2.286 每百万 tokens",
	},
	{
		// 对话全模态：qwen3-omni-flash（全模态档，text+image+audio→text）；
		// 全 token 阶梯人民币计费，落 0-128K 主档换算：输入 0.8 ÷ 7.0 =
		// 0.1143、输出 2.7 ÷ 7.0 = 0.3857（USD/百万 tokens），全阶梯见百炼
		// 文本生成模型列表。来源：
		// help.aliyun.com/zh/model-studio/text-generation-model
		//（2026-10-05 调研）。
		Model:                    "qwen3-omni-flash",
		Mode:                     "chat",
		InputModalities:          []string{"text", "image", "audio"},
		OutputModalities:         []string{"text"},
		SupportedAPIProtocols:    []string{"chat_completions"},
		SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		InputCostPerToken:        perToken(0.1143),
		OutputCostPerToken:       perToken(0.3857),
		SourcePricingCurrency:    "CNY",
		SourceExchangeRateToUsd:  f64p(7.0),
		SourceExchangeRateDate:   "2026-10-05",
		SourcePricingNote:        "官方人民币价 输入0.8/输出2.7 元/百万 tokens（0-128K 主档，百炼文本生成模型列表 help.aliyun.com/zh/model-studio/text-generation-model），按约定汇率 7.0 换算 $0.1143/$0.3857 每百万 tokens",
	},
	{
		// 语音合成：mode=audio、协议 audio_speech；qwen3-tts-flash
		//（text→audio，cosyvoice 系现行 flash 档）。官方按字符人民币计费：
		// 0.8 元/万字符 = 8 元/百万字符，8 ÷ 7.0 = 1.143（USD/百万字符）
		// 落 TtsInputCostPerChar。来源：百炼计费页（2026-10-05 调研）。
		Model:                   "qwen3-tts-flash",
		Mode:                    "audio",
		InputModalities:         []string{"text"},
		OutputModalities:        []string{"audio"},
		SupportedAPIProtocols:   []string{"audio_speech"},
		TtsInputCostPerChar:     perChar(1.143),
		SourcePricingCurrency:   "CNY",
		SourceExchangeRateToUsd: f64p(7.0),
		SourceExchangeRateDate:  "2026-10-05",
		SourcePricingNote:       "官方人民币价 0.8 元/万字符（百炼计费页 help.aliyun.com/zh/model-studio/billing-for-model-studio），按约定汇率 7.0 换算 $1.143/百万字符（0.8×10÷7.0）",
	},
	{
		// 语音合成：mode=audio、协议 audio_speech；cosyvoice-v3-flash
		//（text→audio，CosyVoice 3 代 flash 档）。官方按字符人民币计费：
		// 1 元/万字符 = 10 元/百万字符，10 ÷ 7.0 = 1.429（USD/百万字符）
		// 落 TtsInputCostPerChar。来源：百炼计费页（2026-10-05 调研）。
		Model:                   "cosyvoice-v3-flash",
		Mode:                    "audio",
		InputModalities:         []string{"text"},
		OutputModalities:        []string{"audio"},
		SupportedAPIProtocols:   []string{"audio_speech"},
		TtsInputCostPerChar:     perChar(1.429),
		SourcePricingCurrency:   "CNY",
		SourceExchangeRateToUsd: f64p(7.0),
		SourceExchangeRateDate:  "2026-10-05",
		SourcePricingNote:       "官方人民币价 1 元/万字符（百炼计费页 help.aliyun.com/zh/model-studio/billing-for-model-studio），按约定汇率 7.0 换算 $1.429/百万字符（1×10÷7.0）",
	},
	{
		// 实时转写：mode=audio、协议 audio_transcription；qwen3-asr-flash
		//（audio→text，实时识别档）。官方按秒人民币计费：0.00022 ÷ 7.0 =
		// 0.00003143（USD/秒，4 位有效数字）落 AudioInputCostPerSecond。
		// 来源：百炼计费页（2026-10-05 调研）。
		Model:                   "qwen3-asr-flash",
		Mode:                    "audio",
		InputModalities:         []string{"audio"},
		OutputModalities:        []string{"text"},
		SupportedAPIProtocols:   []string{"audio_transcription"},
		AudioInputCostPerSecond: f64p(0.00003143),
		SourcePricingCurrency:   "CNY",
		SourceExchangeRateToUsd: f64p(7.0),
		SourceExchangeRateDate:  "2026-10-05",
		SourcePricingNote:       "官方人民币价 0.00022 元/秒（百炼计费页 help.aliyun.com/zh/model-studio/billing-for-model-studio），按约定汇率 7.0 换算 $0.00003143/秒",
	},
	{
		// 视频：wan3.0-video（万相 3.0 统一视频档，text+image→video，协议
		// video 经万相 adapter 改写，契约 §10.1）；官方按秒 × 分辨率档人民
		// 币计价（480P/720P/1080P 0.3/0.6/1.2 元/秒）→ 落 720P 主档：
		// 0.6 ÷ 7.0 = 0.08571（USD/秒，4 位有效数字），分档注释登记不
		// 结构化。来源：百炼计费页（2026-10-05 调研）。
		Model:                    "wan3.0-video",
		Mode:                     "video",
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"video"},
		SupportedAPIProtocols:    []string{"video"},
		VideoOutputCostPerSecond: f64p(0.08571),
		SourcePricingCurrency:    "CNY",
		SourceExchangeRateToUsd:  f64p(7.0),
		SourceExchangeRateDate:   "2026-10-05",
		SourcePricingNote:        "官方人民币价 720P 0.6 元/秒（主档；480P 0.3/1080P 1.2 元/秒分档登记，百炼计费页 help.aliyun.com/zh/model-studio/billing-for-model-studio），按约定汇率 7.0 换算 $0.08571/秒",
	},
	{
		// 视频：wan2.7-t2v（万相 2.7 文生视频档，text→video，协议 video）；
		// 官方按秒 × 分辨率档人民币计价（720P 0.6 / 1080P 1 元/秒）→ 落
		// 720P 主档：0.6 ÷ 7.0 = 0.08571（USD/秒，4 位有效数字），分档注释
		// 登记不结构化。来源：百炼计费页（2026-10-05 调研）。
		Model:                    "wan2.7-t2v",
		Mode:                     "video",
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"video"},
		SupportedAPIProtocols:    []string{"video"},
		VideoOutputCostPerSecond: f64p(0.08571),
		SourcePricingCurrency:    "CNY",
		SourceExchangeRateToUsd:  f64p(7.0),
		SourceExchangeRateDate:   "2026-10-05",
		SourcePricingNote:        "官方人民币价 720P 0.6 元/秒（主档；1080P 1 元/秒分档登记，百炼计费页 help.aliyun.com/zh/model-studio/billing-for-model-studio），按约定汇率 7.0 换算 $0.08571/秒",
	},
	{
		// 图像：qwen-image-3.0（文生图像档，images 协议）；官方按张 × 分辨率
		// 档人民币计价（1K 档 0.18 元/张）→ 0.18 ÷ 7.0 = 0.02571（USD/张，
		// 4 位有效数字）落 OutputCostPerImage，其余分辨率档调研未登记。
		// 来源：百炼计费页（2026-10-05 调研）。
		Model:                   "qwen-image-3.0",
		Mode:                    "image_generation",
		InputModalities:         []string{"text"},
		OutputModalities:        []string{"image"},
		SupportedAPIProtocols:   []string{"images"},
		OutputCostPerImage:      f64p(0.02571),
		SourcePricingCurrency:   "CNY",
		SourceExchangeRateToUsd: f64p(7.0),
		SourceExchangeRateDate:  "2026-10-05",
		SourcePricingNote:       "官方人民币价 0.18 元/张（1K 档，百炼计费页 help.aliyun.com/zh/model-studio/billing-for-model-studio），按约定汇率 7.0 换算 $0.02571/张",
	},
	{
		// 向量嵌入：text-embedding-v4（官方推荐档，embed_content 协议，对照
		// gemini-embedding / glm embedding-3 行写法；仅目录展示——qwen 档案
		// 无 embed_content 承接 family）。官方人民币 0.5 元/百万 tokens：
		// 0.5 ÷ 7.0 = 0.07143（USD/百万 tokens）。来源：百炼计费页
		// help.aliyun.com/zh/model-studio/billing-for-model-studio（2026-10-05）。
		Model:                   "text-embedding-v4",
		Mode:                    "embedding",
		InputModalities:         []string{"text"},
		OutputModalities:        []string{"text"},
		SupportedAPIProtocols:   []string{"embed_content"},
		InputCostPerToken:       perToken(0.07143),
		SourcePricingCurrency:   "CNY",
		SourceExchangeRateToUsd: f64p(7.0),
		SourceExchangeRateDate:  "2026-10-05",
		SourcePricingNote:       "官方人民币价 0.5 元/百万 tokens（百炼计费页），按约定汇率 7.0 换算 $0.07143 每百万 tokens",
	},
}
