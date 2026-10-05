package pricing

// GLM pricing snapshot, ported from
// backend/src/modules/model-pricing/glm-model-pricing.data.ts (curated
// 2026-08-26). Free models carry explicit zero prices, which stay real
// (non-undefined) prices in the billing semantics.
//
// A2 批模型目录补全（2026-10-05，+10 行：4 行视觉/OCR chat + 2 行 audio +
// 2 行 image + 1 行 video + 1 行 embedding）：智谱官方定价页
// docs.bigmodel.cn 仅人民币明文价（按 token/按万字符/按次/按百万 tokens）。
// 2026-10-05 A3 批用户裁决：不允许无价——本批新增行人民币价按约定汇率
// 7.0 换算落 USD（Source 元信息可溯），官方国际站 USD 明文可查证后覆盖；
// 换算值保留 4 位有效数字，行注释登记「X ÷ 7.0 = Y」可复核。既有 18 行
// z.ai USD 价不动；既有 cogtts/cogvideox-3 两行按同一裁决补换算价（调研
// §2.5：cogvideox-3 官方 1 元/次；cogtts 与 glm-tts 同族现行价 2 元/万
// 字符）。阶梯/分档价落最常用主档（行注释登记调研口径），至此 glm 全部
// 目录行带价（契约 §2.8 兜底仅余上游不回报计量维度的场景）。
//
// 2026-10-05 复核批（官方国际站 z.ai 定价页 docs.z.ai/guides/overview/
// pricing USD 明文覆盖）：6 行落官方 USD 明文价并删除换算三件套——
// glm-4.6v $0.3/缓存读 $0.05/$0.9、glm-4.6v-flashx $0.04/$0.004/$0.4、
// glm-ocr $0.03/$0.03、glm-asr-2512 perToken(0.03)（$0.03/MTok）、
// glm-image $0.015/张、cogview-4 $0.01/张。撤价 2 行——cogvideox-3 与
// cogvideox-2 按次价（z.ai $0.2/video、国内 1 元/0.5 元/次）进不了每秒
// 槽位语义，撤除 VideoOutputCostPerSecond 回无价（0 计费兜底 +
// usage_missing，契约 §2.8），按次口径改注释登记。保留换算 3 行——
// glm-5v-turbo / glm-tts / embedding-3 z.ai 无价，维持约定汇率换算
// （行注释注明）；cogtts 同 glm-tts 口径不变。
//
// 2026-10-05 按次计费批（用户裁决：无法返回 usage 的上游按官网计费规则、
// 按输入计）：pricing 层新增 VideoOutputCostPerCall 按次槽位（与
// OutputCostPerImage 同构、与秒价槽位互斥），上一段「撤价回无价」口径自此
// 被取代——cogvideox-3 落 z.ai 官方 USD 明文 $0.2/video、cogvideox-2 落
// 国内 0.5 元/次 ÷ 7.0 = $0.0714 换算，两行恢复有价（按次，一次任务 =
// 一次调用，契约 §2.8 按次计费）；glm 轮询响应不回报时长，按次行不依赖
// 秒计量。
var glmModelPricingData = mustLoadProviderCatalogModels("glm")
