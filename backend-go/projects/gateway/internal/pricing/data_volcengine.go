package pricing

// Volcengine pricing snapshot。M3 媒体供应商新增（媒体设计 §9/契约 §9.1，
// curated 2026-10-04，接入核对于火山方舟官方文档与定价检索）；A2 批模型
// 目录补全扩至 14 行（curated 2026-10-05）：6 行 chat（doubao-seed 系，
// OpenAI 兼容 /api/v3）+ 4 行 video（seedance 系，含既有
// doubao-seedance-1-0-pro-250528）+ 3 行 image（seedream 系）；2026-10-05
// 占位行批再 +1 行 doubao-tts 语音合成占位行（非官方模型 ID，见行注释）
// → 现 15 行。计价落法
// （人民币换算裁决，计划 §2.1/§2.6；2026-10-05 A3 批用户裁决：不允许无价）：
//   - 火山官方计费口径为人民币且无官方 USD 价（模型列表页
//     docs.volcengine.com/docs/82379/1330310、价格页
//     docs.volcengine.com/docs/82379/1544106，均 2026-09-28 更新）：chat 系
//     按输入长度分段计费、视频按 token/按秒 × 分辨率档、图像按张——目录
//     价格结构无分段通道，分段/阶梯价落最常用主档（行注释登记调研口径
//     全档）；人民币价按约定汇率 7.0 换算落 USD（Source 元信息可溯），
//     官方国际站 USD 明文可查证后覆盖。换算值保留 4 位有效数字，行注释
//     登记「X ÷ 7.0 = Y」可复核。
//   - 缓存存储 0.017 元/百万 tokens/小时（价格页口径）：目录无该列，不落
//     CacheStorageInputTokenCostPerHour（注释登记）。
//   - seedance 系官方按输出 token 计费 × 分辨率档，网关视频计价面只有秒价
//     维度（VideoOutputCostPerSecond）→ 按官方「每秒参考价」（720p 16:9
//     5 秒示例推导口径）换算落 USD/秒，按 token 原价在行注释登记。
//   - doubao-seedance-1-0-pro-250528 为 M3 批既有行：A3 批调研未覆盖其
//     每秒参考价，保持无价（0 计费 + usage_missing 兜底，契约 §2.8），
//     待调研回填。
//   - seedance 系轮询响应 usage/duration 字段未回填契约（§9.1"字段名接入
//     回填"、§2.8 volcengine 列）：官方创建文档明示查询 API 返回 duration
//     （"视频时长与计费相关"），但字段全貌未回填——保守不抽秒（不填
//     OutputVideoSeconds），秒价落值在回填前不产生计费行项。
//   - 豆包语音服务（TTS/ASR 应用面）无 model_id（音色经 req_params.speaker
//     配置、模型由语音应用/资源决定）——原「不落目录行」裁决已被 2026-10-05
//     占位行批取代：落 doubao-tts 占位目录行承载统一面路由与计费匹配
//     （占位名显式标注非官方 ID，见行注释）；M6 TTS adapter 配置事实见
//     docs/functions/火山方舟账号接入.md TTS 节。
//   - doubao-seedance-1-0-pro 官方下线公告为"即将下线"但无明文日期 →
//     不落 ShutdownDate（裁决 §2.3），行注释登记。
//
// 2026-10-05 复核批（官方国际站 BytePlus ModelArk 定价页
// docs.byteplus.com/docs/ModelArk/1099320 USD 明文覆盖）：7 行落官方 USD
// 明文价并删除换算三件套（SourcePricingCurrency/Rate/Date）——chat 3 行
// （2-1-turbo $0.5/$2.5/$0.1、2-0-lite $0.25/$2.00/$0.05+音频 $3.75、
// 2-0-mini $0.10/$0.40/$0.02+音频 $1.50）、video 4 行（seedance-2-5
// $0.231/秒、2-0 $0.15/秒、fast $0.12/秒、mini $0.08/秒，官方每秒示例价
// 720p 16:9 5s 口径）；SourcePricingNote 改为国际站明文 + 国内人民币原句
// 留档。doubao-seed-evolving / 2-1-pro-260915 / 2-1-lite-260915 国际站
// 未售，保留约定汇率换算（行注释注明）。
var volcengineModelPricingData = []rawModel{
	{
		// 语音合成占位目录行（2026-10-05 用户裁决：多能力/无 ID 能力也要标注，
		// 否则调度与计费无从匹配）。`doubao-tts` 是占位名、**非官方模型 ID**——
		// 豆包语音是独立产品线（appid + cluster + voice_type 音色调用，方舟
		// 模型列表/价格页无 TTS 模型在售），统一面 model 字段被
		// volcengine_tts adapter 忽略（M6 契约 §9.2）。凭据 speech_appid/
		// speech_token 双值缺失即无该能力。计费：官方按字符（语音合成 2.0 =
		// 3 元/万字符，豆包语音产品计费 PDF），3 元/万字符 = 30 元/百万字符，
		// 30 ÷ 7.0 = 4.2857（USD/百万字符）落 TtsInputCostPerChar；上游无
		// 字符回报 → 网关按请求字符自算计量照落。
		Model:                   "doubao-tts",
		Mode:                    "audio",
		InputModalities:         []string{"text"},
		OutputModalities:        []string{"audio"},
		SupportedAPIProtocols:   []string{"audio_speech"},
		ResponseFormats:         []string{"mp3"},
		TtsInputCostPerChar:     perChar(4.2857),
		SourcePricingCurrency:   "CNY",
		SourceExchangeRateToUsd: f64p(7.0),
		SourceExchangeRateDate:  "2026-10-05",
		SourcePricingNote:       "占位目录行（非官方模型 ID）；官方人民币价 语音合成模型2.0 = 3 元/万字符（豆包语音产品计费 PDF volcengine.com/docs/6561/14275），按约定汇率 7.0 换算 $4.2857/百万字符",
	},

	{
		// 视频：mode=video、协议 video（统一 /v1/videos 面经 seedance adapter
		// 改写，契约 §9.1）；输入 text+image（文生/图生视频，content[].
		// image_url 承接公共 input_reference，url/base64 双形态直传）。
		// 官方下线公告列为"即将下线"但无明文日期（不落 ShutdownDate，裁决
		// §2.3）；按 token（0.015 元/千 tokens 新闻转述口径）/按秒 × 分辨率档
		// 人民币计费（价格页 /docs/82379/1544106）。A3 批调研未覆盖该行的
		// 官方每秒参考价 → 保持无价（0 计费 + usage_missing 兜底），待调研
		// 回填后再按约定汇率 7.0 换算落价。
		Model:                 "doubao-seedance-1-0-pro-250528",
		Mode:                  "video",
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"video"},
		SupportedAPIProtocols: []string{"video"},
	},
	{
		// 对话：doubao-seed-evolving（持续演进旗舰档，1M 上下文/256k 输出，
		// OpenAI 兼容 /api/v3）；thinking 型（efforts low/medium/high/minimal，
		// 官方参数页明文默认 high）。输入长度分段人民币计费（主档换算）：
		// 输入 6.00 ÷ 7.0 = 0.8571、输出 30.00 ÷ 7.0 = 4.286、缓存读
		// 1.20 ÷ 7.0 = 0.1714（USD/百万 tokens，4 位有效数字）。来源：模型
		// 列表页 docs.volcengine.com/docs/82379/1330310（2026-09-28 更新）、
		// 价格页 /docs/82379/1544106。2026-10-05 复核批：国际站未售，保留
		// 约定汇率换算。
		Model:                     "doubao-seed-evolving",
		Mode:                      "chat",
		ContextWindowTokens:       intp(1_000_000),
		MaxOutputTokens:           intp(256_000),
		InputModalities:           []string{"text"},
		OutputModalities:          []string{"text"},
		SupportedAPIProtocols:     []string{"chat_completions"},
		SupportedToolsByProtocol:  toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		SupportedReasoningEfforts: []string{"low", "medium", "high", "minimal"},
		DefaultReasoningEffort:    "high",
		InputCostPerToken:         perToken(0.8571),
		OutputCostPerToken:        perToken(4.286),
		CacheReadInputTokenCost:   perToken(0.1714),
		SourcePricingCurrency:     "CNY",
		SourceExchangeRateToUsd:   f64p(7.0),
		SourceExchangeRateDate:    "2026-10-05",
		SourcePricingNote:         "官方人民币价 输入6/输出30/缓存读1.2 元/百万 tokens（火山方舟价格页 docs.volcengine.com/docs/82379/1544106），按约定汇率 7.0 换算 $0.8571/$4.286/$0.1714 每百万 tokens",
	},
	{
		// 对话：doubao-seed-2-1-pro-260915（2.1 代 pro 档，1M 上下文/256k
		// 输出）；thinking 型（efforts low/medium/high/minimal，官方参数页
		// 明文默认 high）。输入长度分段人民币计费（主档换算）：输入
		// 6.00 ÷ 7.0 = 0.8571、输出 30.00 ÷ 7.0 = 4.286、缓存读
		// 1.20 ÷ 7.0 = 0.1714（USD/百万 tokens）。来源：模型列表页
		// docs.volcengine.com/docs/82379/1330310（2026-09-28 更新）、价格页
		// /docs/82379/1544106。2026-10-05 复核批：国际站未售，保留约定汇率
		// 换算。
		Model:                     "doubao-seed-2-1-pro-260915",
		Mode:                      "chat",
		ReleaseDate:               "2026-09-15",
		ContextWindowTokens:       intp(1_000_000),
		MaxOutputTokens:           intp(256_000),
		InputModalities:           []string{"text"},
		OutputModalities:          []string{"text"},
		SupportedAPIProtocols:     []string{"chat_completions"},
		SupportedToolsByProtocol:  toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		SupportedReasoningEfforts: []string{"low", "medium", "high", "minimal"},
		DefaultReasoningEffort:    "high",
		InputCostPerToken:         perToken(0.8571),
		OutputCostPerToken:        perToken(4.286),
		CacheReadInputTokenCost:   perToken(0.1714),
		SourcePricingCurrency:     "CNY",
		SourceExchangeRateToUsd:   f64p(7.0),
		SourceExchangeRateDate:    "2026-10-05",
		SourcePricingNote:         "官方人民币价 输入6/输出30/缓存读1.2 元/百万 tokens（火山方舟价格页 docs.volcengine.com/docs/82379/1544106），按约定汇率 7.0 换算 $0.8571/$4.286/$0.1714 每百万 tokens",
	},
	{
		// 对话：doubao-seed-2-1-lite-260915（2.1 代 lite 档，1M 上下文/256k
		// 输出，官方支持音频输入 → InputModalities 加 audio）；thinking 型
		//（efforts low/medium/high/minimal，官方参数页明文默认 high）。输入
		// 长度分段人民币计费（主档换算）：输入 0.80 ÷ 7.0 = 0.1143、输出
		// 2.70 ÷ 7.0 = 0.3857、缓存读 0.16 ÷ 7.0 = 0.02286、音频输入
		// 12.00 ÷ 7.0 = 1.714（USD/百万 tokens）。来源：模型列表页
		// docs.volcengine.com/docs/82379/1330310（2026-09-28 更新）、价格页
		// /docs/82379/1544106。2026-10-05 复核批：国际站未售，保留约定汇率
		// 换算。
		Model:                     "doubao-seed-2-1-lite-260915",
		Mode:                      "chat",
		ReleaseDate:               "2026-09-15",
		ContextWindowTokens:       intp(1_000_000),
		MaxOutputTokens:           intp(256_000),
		InputModalities:           []string{"text", "audio"},
		OutputModalities:          []string{"text"},
		SupportedAPIProtocols:     []string{"chat_completions"},
		SupportedToolsByProtocol:  toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		SupportedReasoningEfforts: []string{"low", "medium", "high", "minimal"},
		DefaultReasoningEffort:    "high",
		InputCostPerToken:         perToken(0.1143),
		OutputCostPerToken:        perToken(0.3857),
		CacheReadInputTokenCost:   perToken(0.02286),
		InputCostPerAudioToken:    perToken(1.714),
		SourcePricingCurrency:     "CNY",
		SourceExchangeRateToUsd:   f64p(7.0),
		SourceExchangeRateDate:    "2026-10-05",
		SourcePricingNote:         "官方人民币价 输入0.8/输出2.7/缓存读0.16/音频输入12 元/百万 tokens（火山方舟价格页 docs.volcengine.com/docs/82379/1544106），按约定汇率 7.0 换算 $0.1143/$0.3857/$0.02286/$1.714 每百万 tokens",
	},
	{
		// 对话：doubao-seed-2-1-turbo-260628（2.1 代 turbo 档，官方列表页
		// 上下文 256k 口径）；thinking 型（efforts low/medium/high/minimal，
		// 官方参数页明文默认 high）。2026-10-05 复核批：官方国际站 USD 明文价
		// 覆盖换算值——输入 $0.5 / 输出 $2.5 / 缓存读 $0.1 每百万 tokens
		//（BytePlus ModelArk 定价页 docs.byteplus.com/docs/ModelArk/1099320）；
		// Flex/Batch 档 $0.25/$1.25、缓存存储 $0.0083（目录无对应列，注释
		// 登记）；国内人民币价 3/15/0.6 元原句留档。
		Model:                     "doubao-seed-2-1-turbo-260628",
		Mode:                      "chat",
		ReleaseDate:               "2026-06-28",
		ContextWindowTokens:       intp(256_000),
		InputModalities:           []string{"text"},
		OutputModalities:          []string{"text"},
		SupportedAPIProtocols:     []string{"chat_completions"},
		SupportedToolsByProtocol:  toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		SupportedReasoningEfforts: []string{"low", "medium", "high", "minimal"},
		DefaultReasoningEffort:    "high",
		InputCostPerToken:         perToken(0.5),
		OutputCostPerToken:        perToken(2.5),
		CacheReadInputTokenCost:   perToken(0.1),
		SourcePricingNote:         "官方国际站 USD 明文价（BytePlus ModelArk docs.byteplus.com/docs/ModelArk/1099320：输入 $0.5 / 输出 $2.5 / 缓存读 $0.1，Flex/Batch $0.25/$1.25、缓存存储 $0.0083）；国内人民币价 3/15/0.6 元/百万 tokens 原句留档（火山方舟价格页 docs.volcengine.com/docs/82379/1544106）",
	},
	{
		// 对话：doubao-seed-2-0-lite-260428（2.0 代 lite 档，256k 上下文/128k
		// 输出）；thinking 型（efforts low/medium/high；2.0 系官方无明文默认
		// effort → 不落 DefaultReasoningEffort）。2026-10-05 复核批：官方国际
		// 站 USD 明文价覆盖换算值——输入首段 $0.25 / 输出首段 $2.00 / 缓存读
		// $0.05 / 音频输入 $3.75 每百万 tokens（第二段 $0.50/$4.00 注释登记；
		// BytePlus ModelArk docs.byteplus.com/docs/ModelArk/1099320）；国内
		// 人民币价原句留档。
		Model:                     "doubao-seed-2-0-lite-260428",
		Mode:                      "chat",
		ReleaseDate:               "2026-04-28",
		ContextWindowTokens:       intp(256_000),
		MaxOutputTokens:           intp(128_000),
		InputModalities:           []string{"text", "audio"},
		OutputModalities:          []string{"text"},
		SupportedAPIProtocols:     []string{"chat_completions"},
		SupportedToolsByProtocol:  toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		SupportedReasoningEfforts: []string{"low", "medium", "high"},
		InputCostPerToken:         perToken(0.25),
		OutputCostPerToken:        perToken(2),
		CacheReadInputTokenCost:   perToken(0.05),
		InputCostPerAudioToken:    perToken(3.75),
		SourcePricingNote:         "官方国际站 USD 明文价（BytePlus ModelArk docs.byteplus.com/docs/ModelArk/1099320：输入 $0.25 首段（第二段 $0.50）/ 输出 $2.00 首段（第二段 $4.00）/ 缓存读 $0.05 / 音频输入 $3.75 每百万 tokens）；国内人民币价 0.6（三段 0.6/0.9/1.8）/3.6/0.12/音频 12 元原句留档（火山方舟价格页 docs.volcengine.com/docs/82379/1544106）",
	},
	{
		// 对话：doubao-seed-2-0-mini-260428（2.0 代 mini 档，256k 上下文/
		// 128k 输出）；thinking 型（efforts low/medium/high；2.0 系官方无明文
		// 默认 effort → 不落 DefaultReasoningEffort）。2026-10-05 复核批：官方
		// 国际站 USD 明文价覆盖换算值——输入 $0.10 / 输出 $0.40 / 缓存读
		// $0.02 / 音频输入 $1.50 每百万 tokens（BytePlus ModelArk
		// docs.byteplus.com/docs/ModelArk/1099320）；国内人民币分段价原句留档。
		Model:                     "doubao-seed-2-0-mini-260428",
		Mode:                      "chat",
		ReleaseDate:               "2026-04-28",
		ContextWindowTokens:       intp(256_000),
		MaxOutputTokens:           intp(128_000),
		InputModalities:           []string{"text", "audio"},
		OutputModalities:          []string{"text"},
		SupportedAPIProtocols:     []string{"chat_completions"},
		SupportedToolsByProtocol:  toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"}),
		SupportedReasoningEfforts: []string{"low", "medium", "high"},
		InputCostPerToken:         perToken(0.1),
		OutputCostPerToken:        perToken(0.4),
		CacheReadInputTokenCost:   perToken(0.02),
		InputCostPerAudioToken:    perToken(1.5),
		SourcePricingNote:         "官方国际站 USD 明文价（BytePlus ModelArk docs.byteplus.com/docs/ModelArk/1099320：输入 $0.10 / 输出 $0.40 / 缓存读 $0.02 / 音频输入 $1.50 每百万 tokens）；国内人民币价 0.2（三段 0.2/0.4/0.8）/2.0（三段 2.0/4.0/8.0）元原句留档（火山方舟价格页 docs.volcengine.com/docs/82379/1544106）",
	},
	{
		// 视频：doubao-seedance-2-5-260628（2.5 代旗舰，text+image→video，
		// 协议 video 经 seedance adapter 改写，契约 §9.1）。2026-10-05 复核批：
		// 官方国际站 USD 明文价覆盖换算值——$0.231/秒（官方每秒示例价，720p
		// 16:9 5 秒口径；BytePlus ModelArk docs.byteplus.com/docs/ModelArk/
		// 1099320）；官方按输出 token 牌价 $10.70/百万 tokens（限时折扣另计）
		// 注释登记，目录只有秒价维度仍落秒价。国内人民币换算值 0.2157 作废，
		// 人民币原句留档。
		Model:                    "doubao-seedance-2-5-260628",
		Mode:                     "video",
		ReleaseDate:              "2026-06-28",
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"video"},
		SupportedAPIProtocols:    []string{"video"},
		VideoOutputCostPerSecond: f64p(0.231),
		SourcePricingNote:        "官方国际站 USD 明文价（BytePlus ModelArk docs.byteplus.com/docs/ModelArk/1099320：每秒示例价 $0.231/秒，720p 16:9 5 秒口径；按输出 token 牌价 $10.70/百万 tokens，限时折扣另计）；国内人民币每秒参考价 1.51 元/秒原句留档（火山方舟价格页 docs.volcengine.com/docs/82379/1544106）",
	},
	{
		// 视频：doubao-seedance-2-0-260128（2.0 代标准档，text+image→video，
		// 协议 video）。2026-10-05 复核批：官方国际站 USD 明文价覆盖换算值——
		// $0.15/秒（官方每秒示例价，720p 16:9 5 秒口径；BytePlus ModelArk
		// docs.byteplus.com/docs/ModelArk/1099320）；按输出 token 牌价 $7.0/
		// 百万 tokens（限时折扣另计）注释登记。国内人民币换算值 0.71 作废，
		// 人民币原句留档。
		Model:                    "doubao-seedance-2-0-260128",
		Mode:                     "video",
		ReleaseDate:              "2026-01-28",
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"video"},
		SupportedAPIProtocols:    []string{"video"},
		VideoOutputCostPerSecond: f64p(0.15),
		SourcePricingNote:        "官方国际站 USD 明文价（BytePlus ModelArk docs.byteplus.com/docs/ModelArk/1099320：每秒示例价 $0.15/秒，720p 16:9 5 秒口径；按输出 token 牌价 $7.0/百万 tokens，限时折扣另计）；国内人民币每秒参考价 4.97 元/秒原句留档（火山方舟价格页 docs.volcengine.com/docs/82379/1544106）",
	},
	{
		// 视频：doubao-seedance-2-0-fast-260128（2.0 代快速档，text+image→
		// video，协议 video）。2026-10-05 复核批：官方国际站 USD 明文价覆盖
		// 换算值——$0.12/秒（官方每秒示例价，720p 16:9 5 秒口径；BytePlus
		// ModelArk docs.byteplus.com/docs/ModelArk/1099320）；按输出 token
		// 牌价 $5.6/百万 tokens（限时折扣另计）注释登记。国内人民币换算值
		// 0.5714 作废，人民币原句留档。
		Model:                    "doubao-seedance-2-0-fast-260128",
		Mode:                     "video",
		ReleaseDate:              "2026-01-28",
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"video"},
		SupportedAPIProtocols:    []string{"video"},
		VideoOutputCostPerSecond: f64p(0.12),
		SourcePricingNote:        "官方国际站 USD 明文价（BytePlus ModelArk docs.byteplus.com/docs/ModelArk/1099320：每秒示例价 $0.12/秒，720p 16:9 5 秒口径；按输出 token 牌价 $5.6/百万 tokens，限时折扣另计）；国内人民币每秒参考价 4.00 元/秒原句留档（火山方舟价格页 docs.volcengine.com/docs/82379/1544106）",
	},
	{
		// 视频：doubao-seedance-2-0-mini-260615（2.0 代 mini 档，text+image→
		// video，协议 video）。2026-10-05 复核批：官方国际站 USD 明文价覆盖
		// 换算值——$0.08/秒（官方每秒示例价，720p 16:9 5 秒口径；BytePlus
		// ModelArk docs.byteplus.com/docs/ModelArk/1099320）；按输出 token
		// 牌价 $3.5/百万 tokens（限时折扣另计）注释登记。国内人民币换算值
		// 0.3543 作废，人民币原句留档。
		Model:                    "doubao-seedance-2-0-mini-260615",
		Mode:                     "video",
		ReleaseDate:              "2026-06-15",
		InputModalities:          []string{"text", "image"},
		OutputModalities:         []string{"video"},
		SupportedAPIProtocols:    []string{"video"},
		VideoOutputCostPerSecond: f64p(0.08),
		SourcePricingNote:        "官方国际站 USD 明文价（BytePlus ModelArk docs.byteplus.com/docs/ModelArk/1099320：每秒示例价 $0.08/秒，720p 16:9 5 秒口径；按输出 token 牌价 $3.5/百万 tokens，限时折扣另计）；国内人民币每秒参考价 2.48 元/秒原句留档（火山方舟价格页 docs.volcengine.com/docs/82379/1544106）",
	},
	{
		// 图像：doubao-seedream-5-0-pro-260628（5.0 代 pro 档，文生/图生
		// 图像，images 协议对照 openai gpt-image 行结构）；官方按张人民币
		// 计费（≤261 万像素档 0.30 元/张主档换算：0.30 ÷ 7.0 = 0.04286
		// USD/张；>261 万像素档 0.60 元/张及图层价调研口径注释登记，不
		// 结构化）。来源：模型列表页 docs.volcengine.com/docs/82379/1330310
		//（2026-09-28 更新）、价格页 /docs/82379/1544106。
		Model:                   "doubao-seedream-5-0-pro-260628",
		Mode:                    "image_generation",
		ReleaseDate:             "2026-06-28",
		InputModalities:         []string{"text", "image"},
		OutputModalities:        []string{"image"},
		SupportedAPIProtocols:   []string{"images"},
		OutputCostPerImage:      f64p(0.04286),
		SourcePricingCurrency:   "CNY",
		SourceExchangeRateToUsd: f64p(7.0),
		SourceExchangeRateDate:  "2026-10-05",
		SourcePricingNote:       "官方人民币价 0.30 元/张（≤261 万像素档；>261 万像素 0.60 元/张及图层价另计，火山方舟价格页 docs.volcengine.com/docs/82379/1544106），按约定汇率 7.0 换算 $0.04286/张",
	},
	{
		// 图像：doubao-seedream-5-0-flash-260915（5.0 代 flash 档，images
		// 协议）；官方按张人民币计费（0.12 元/张换算：0.12 ÷ 7.0 = 0.01714
		// USD/张）。来源：模型列表页 docs.volcengine.com/docs/82379/1330310
		//（2026-09-28 更新）、价格页 /docs/82379/1544106。
		Model:                   "doubao-seedream-5-0-flash-260915",
		Mode:                    "image_generation",
		ReleaseDate:             "2026-09-15",
		InputModalities:         []string{"text", "image"},
		OutputModalities:        []string{"image"},
		SupportedAPIProtocols:   []string{"images"},
		OutputCostPerImage:      f64p(0.01714),
		SourcePricingCurrency:   "CNY",
		SourceExchangeRateToUsd: f64p(7.0),
		SourceExchangeRateDate:  "2026-10-05",
		SourcePricingNote:       "官方人民币价 0.12 元/张（火山方舟价格页 docs.volcengine.com/docs/82379/1544106），按约定汇率 7.0 换算 $0.01714/张",
	},
	{
		// 图像：doubao-seedream-5-0-260128（5.0 代标准档，images 协议）；官方
		// 按张人民币计费（0.22 元/张换算：0.22 ÷ 7.0 = 0.03143 USD/张）。
		// 来源：模型列表页 docs.volcengine.com/docs/82379/1330310
		//（2026-09-28 更新）、价格页 /docs/82379/1544106。
		Model:                   "doubao-seedream-5-0-260128",
		Mode:                    "image_generation",
		ReleaseDate:             "2026-01-28",
		InputModalities:         []string{"text", "image"},
		OutputModalities:        []string{"image"},
		SupportedAPIProtocols:   []string{"images"},
		OutputCostPerImage:      f64p(0.03143),
		SourcePricingCurrency:   "CNY",
		SourceExchangeRateToUsd: f64p(7.0),
		SourceExchangeRateDate:  "2026-10-05",
		SourcePricingNote:       "官方人民币价 0.22 元/张（火山方舟价格页 docs.volcengine.com/docs/82379/1544106），按约定汇率 7.0 换算 $0.03143/张",
	},
	// M6 TTS（契约 §9.2 已实施，openspeech /api/v3/tts adapter）目录承载
	// 见文件头 doubao-tts 占位行（2026-10-05 占位行批落行：官方无 model 字
	// 段——音色经 req_params.speaker 配置、模型由语音应用/资源决定，统一面
	// model 仅必填占位、由 adapter 忽略；计费按字符、上游无字符回报 → 网关
	// 按请求 input 自算计量照落。字符价取豆包语音产品计费 PDF 明文换算，
	// 见占位行 Source 元信息）。配置事实见 docs/functions/火山方舟账号接入.md
	// TTS 节。
}
