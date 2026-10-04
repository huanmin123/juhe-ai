package pricing

// Volcengine pricing snapshot, M3 media provider addition (媒体设计 §9/契约
// §9.1；curated 2026-10-04，接入核对于火山方舟官方文档与定价检索)。仅 1 行
// 媒体行（doubao-seedance-1-0-pro-250528，视频），计价落法（不编造）：
//   - 官方计费口径为人民币且无 USD 秒价：发布会/新闻口径为按 token 计费
//    （0.015 元/千 tokens，即 15 元/百万 tokens），产品页口径为按秒 × 分辨率
//     档（1080P 约 0.73 元/秒的新闻转述）——两者均为人民币/第三方转述口径，
//     火山官网定价页无可查证官方精确 USD 秒价（检索所得非官方页口径不作数）；
//     本引擎计价面仅 USD 且无官方汇率折算链——自选汇率折算即编造。
//   - 轮询响应 usage/duration 字段未回填契约（§9.1"字段名接入回填"、
//     §2.8 volcengine 列）：官方创建文档明示查询 API 返回 duration（"视频
//     时长与计费相关"），但字段全貌未回填——M3 保守不抽秒（不填
//     OutputVideoSeconds），终态计费走契约 §2.8 兜底（0 计费 + usage_missing
//     标记，同 glm cogvideox / minimax hailuo 先例）。
//   - 官方 USD 秒价或 usage/duration 回填口径可查证后，再补
//     VideoOutputCostPerSecond 与秒计量抽取（契约 §9.1 回填同批）。
var volcengineModelPricingData = []rawModel{
	{
		// 视频：mode=video、协议 video（统一 /v1/videos 面经 seedance adapter
		// 改写，契约 §9.1）；输入 text+image（文生/图生视频，content[].
		// image_url 承接公共 input_reference，url/base64 双形态直传）。
		Model:                "doubao-seedance-1-0-pro-250528",
		Mode:                 "video",
		InputModalities:      []string{"text", "image"},
		OutputModalities:     []string{"video"},
		SupportedAPIProtocols: []string{"video"},
	},
}
