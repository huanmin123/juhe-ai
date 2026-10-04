package pricing

// Qwen pricing snapshot, M3 media provider addition (媒体设计 §9/契约 §10.1/§10.2；
// curated 2026-10-04，接入核对于阿里云百炼 legacy 万相 API 参考与定价检索)。
// 2 行媒体行（wan2.2-t2v-plus 视频 + paraformer-v2 长转写，M3f），计价落法
// （不编造）：
//   - 官方计费口径为人民币且无 USD 秒价：百炼万相按次/时长计费（wan2.2 系
//     按 5 秒视频条次计价），paraformer 长转写按时长计费（官方定价页均为
//     人民币口径），无可查证官方精确 USD 秒价；本引擎计价面仅 USD 且无官方
//     汇率折算链——自选汇率折算即编造 → 不落 VideoOutputUsdPerSecond /
//     AudioInputUsdPerSecond。
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
//   - 目录无 USD 秒价 → 秒计量照落但成本恒 0（计量照落成本不虚计，同
//     minimax TTS 字符价先例）；官方 USD 秒价可查证后再补
//     VideoOutputUsdPerSecond / AudioInputUsdPerSecond（契约计费行回填
//     同批）。
var qwenModelPricingData = []rawModel{
	{
		// 视频：mode=video、协议 video（统一 /v1/videos 面经万相 adapter 改写，
		// 契约 §10.1）；输入 text+image（文生/图生视频，input.img_url 承接公共
		// input_reference，url/base64 双形态直传）；M3 第五批收录
		// wan2.2-t2v-plus（文生视频主档，480P/1080P、5 秒、30fps、MP4）。
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
		Model:                 "paraformer-v2",
		Mode:                  "audio",
		InputModalities:       []string{"audio"},
		OutputModalities:      []string{"text"},
		SupportedAPIProtocols: []string{"audio_transcription"},
	},
}
