package pricing

// MiniMax pricing snapshot, M3 media provider addition (媒体设计 §9/契约
// §8.2/§8.1；curated 2026-10-04，接入核对于 platform.minimax.cn 按量计费
// 页)。两行均为媒体行，计价落法（不编造）：
//   - speech-02-turbo（TTS）：官方按量口径 ¥2.00/万字符（platform.minimax.cn
//     docs/guides/pricing-paygo「同步语音合成 T2A speech-2.6-turbo /
//     speech-02-turbo 2.00」），官方无美元价；本引擎计价面仅 USD 且无官方
//     汇率折算链——不落 TtsInputCostPerChar（自选汇率折算即编造）。TTS
//     字符计量照常落 usage（extra_info.usage_characters 优先，契约 §2.8），
//     目录无价 → 成本不虚计（0）；官方美元口径可查证后再补字符价。
//   - MiniMax-Hailuo-2.3（视频）：官方按次/档位计费（分辨率 × 时长），
//     platform.minimax.io 定价页为登录态 SPA，无可查证官方精确 USD 秒价
//    （检索所得均为第三方聚合转售口径，不作数）；且轮询响应不回报时长
//    （契约 §8.1 query 响应仅 status/file_id/file_download_url）——不落
//     VideoOutputCostPerSecond，终态计费走契约 §2.8 兜底（0 计费 +
//     usage_missing 标记），同 glm cogvideox 先例。
var minimaxModelPricingData = []rawModel{
	{
		// TTS：mode=audio、协议 audio_speech（统一 /v1/audio/speech 面）。
		// ResponseFormats 为 §8.2 词表（mp3/pcm/flac/wav，零转码）。
		Model:                "speech-02-turbo",
		Mode:                 "audio",
		InputModalities:      []string{"text"},
		OutputModalities:     []string{"audio"},
		SupportedAPIProtocols: []string{"audio_speech"},
		ResponseFormats:      []string{"mp3", "pcm", "flac", "wav"},
	},
	{
		// 视频：mode=video、协议 video；输入 text+image（文生/图生视频，
		// first_frame_image 承接公共 input_reference）。
		Model:                "MiniMax-Hailuo-2.3",
		Mode:                 "video",
		InputModalities:      []string{"text", "image"},
		OutputModalities:     []string{"video"},
		SupportedAPIProtocols: []string{"video"},
	},
}
