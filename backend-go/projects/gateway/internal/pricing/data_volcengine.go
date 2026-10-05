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
var volcengineModelPricingData = mustLoadProviderCatalogModels("volcengine")
