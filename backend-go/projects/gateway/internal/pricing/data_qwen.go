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
//   - 既有 M3 媒体行（wan2.2-t2v-plus / paraformer-v2）已于 2026-10-05
//     用户裁决补价（wan2.2-t2v-plus 1080P 0.70 元/秒、paraformer-v2
//     0.00008 元/秒，按约定汇率 7.0 换算落 USD，Source 元信息可溯）；
//     原「保持无价（0 计费 + usage_missing 兜底）」口径作废。
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
// 留档。wan2.2-t2v-plus 国际站未售，按国内人民币明文价以约定汇率 7.0
// 换算落价（2026-10-05 用户裁决补价，行内 Source 元信息可溯）；
// paraformer-v2 于 2026-10-05 取证批改落国际站 USD 明文 $0.000012/秒
//（Model Studio 计费页 China Beijing tab，行内 Source 元信息可溯）。
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
var qwenModelPricingData = mustLoadProviderCatalogModels("qwen")

