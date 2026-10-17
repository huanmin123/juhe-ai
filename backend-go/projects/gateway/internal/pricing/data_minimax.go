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
//     国际站定价页明文为按条价（768P-6s $0.28/条）。2026-10-05 按次计费批
//     （用户裁决：无法返回 usage 的上游按官网计费规则、按输入计）：按条
//     主档落 VideoOutputCostPerCall 按次槽位（一次任务 = 一次调用，契约
//     §2.8 按次计费）；按条 → 每秒是推导，禁止折算（裁决 §2.4 维持）
//     ——不落 VideoOutputCostPerSecond；轮询响应不回报时长（契约 §8.1
//     query 响应仅 status/file_id/file_download_url），按次行不依赖秒计量。
//
// 2026-10-17 全供应商口径核对批：目录主口径由「国际站 USD 明文直落」整体
// 回正为「国内站人民币明文 ÷ 约定汇率 7.0」——账户协议档案实际连接
// api.minimax.chat 国内站（账单人民币），国际明文与国内价并不同步（实证：
// speech-2.8-hd 国内 3.50 元/万字符 ≈ $5e-05/字符 vs 国际 $1e-04/字符）。
// 13 行全部重取 platform.minimax.cn 定价概览（platform.minimaxi.com 同站，
// 直取 pricing/overview.md 渲染原文）：TTS 五行按国内 2.00/3.50 元/万字符、
// asr-1.0 按 2.50 元/小时、H3/H3-Max 按 768P 主档 0.50 元/秒（H3-Max 此前
// 误落国际 480P 档 $0.05）、Hailuo-2.3 按 2.00 元/条、image-01 按
// 0.025 元/张；M3/M2.7 值恰与国内÷7.0 等价不动、补 >512k 长上下文 ×2 分档
// 与缓存写 2.625 元/百万。分档/素材/资源包规则留各行 SourcePricingNote；
// 锚点见 catalogdata_price_anchors_test.go。
var minimaxModelPricingData = mustLoadProviderCatalogModels("minimax")
