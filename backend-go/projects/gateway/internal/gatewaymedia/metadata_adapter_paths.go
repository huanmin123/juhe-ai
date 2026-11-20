// metadata_adapter_paths.go 集中声明各 adapter 出站报文的规范路径（点号
// 表示嵌套）。metadata 只允许新增路径：这些路径即使本次请求未写入基础报文
// （例如未传 size 时的 parameters.size），也不得由扩展通道补写——否则上游
// 实际工作量会与创建时计费快照脱钩（设计《对外协议面对齐 new-api 设计》
// §5.1）。顶层字段仍由 ExtractMetadata 的 reserved 集合先行拦截。
package gatewaymedia

var (
	// glm 视频创建报文：{model,prompt,negative_prompt?,image_url?,size?,
	// duration?,with_audio?,fps?}（契约 §7.1）。
	glmVideoMetadataReservedPaths = []string{
		"model", "prompt", "negative_prompt", "image_url",
		"size", "duration", "with_audio", "fps",
	}

	// minimax 视频创建报文：{model,prompt,duration?,first_frame_image?}
	// （契约 §8.1）。
	minimaxVideoMetadataReservedPaths = []string{
		"model", "prompt", "duration", "first_frame_image",
	}

	// qwen 万相视频创建报文：{model,input,parameters?}；input 内的
	// prompt/img_url/negative_prompt 与 parameters 的 size/duration/seed
	// 都是规范映射字段，parameters.prompt_extend 等新增键仍允许（契约 §10.1）。
	qwenVideoMetadataReservedPaths = []string{
		"model", "input.prompt", "input.negative_prompt", "input.img_url",
		"parameters.size", "parameters.duration", "parameters.seed",
	}

	// veo 创建报文：{instances,parameters}；instances 整体由 adapter 构造，
	// parameters 内 aspectRatio/resolution/negativePrompt/durationSeconds 为
	// 规范映射字段，personGeneration 等新增键仍允许（契约 §5.2）。
	veoVideoMetadataReservedPaths = []string{
		"instances",
		"parameters.aspectRatio", "parameters.resolution",
		"parameters.negativePrompt", "parameters.durationSeconds",
	}

	// volcengine 视频创建报文：{model,content,resolution?,ratio?,duration?,
	// seed?}（契约 §9.1）。
	volcengineVideoMetadataReservedPaths = []string{
		"model", "content", "resolution", "ratio", "duration", "seed",
	}

	// xai 视频创建报文：{model,prompt,duration?,aspect_ratio?,resolution?,
	// image?,generate_audio?}（契约 §6.1）。
	xaiVideoMetadataReservedPaths = []string{
		"model", "prompt", "duration", "aspect_ratio", "resolution",
		"image", "generate_audio",
	}

	// openai 视频创建报文：{model,n,prompt?,seconds?,size?,input_reference?}
	// （契约 §4.3）。
	openAIVideoMetadataReservedPaths = []string{
		"model", "n", "prompt", "seconds", "size", "input_reference",
	}

	// qwen 长转写创建报文：{model,input,parameters?}；input.file_urls 与
	// parameters.language_hints 为规范映射字段（契约 §10.2）。
	qwenASRMetadataReservedPaths = []string{
		"model", "input.file_urls", "parameters.language_hints",
	}

	// volcengine 长转写创建报文：{user:{uid},audio:{url,format},
	// request:{model_name}}；audio_format 是显式消费键，合并前已剔除
	// （契约 §9.2）。
	volcengineASRMetadataReservedPaths = []string{
		"user.uid", "audio.url", "audio.format", "request.model_name",
	}

	// minimax TTS 创建报文：{model,text,stream,voice_setting,audio_setting}；
	// voice_setting.vol/pitch 等厂商个例新增键仍允许（契约 §8.2）。
	minimaxTTSMetadataReservedPaths = []string{
		"model", "text", "stream",
		"voice_setting.voice_id", "voice_setting.speed",
		"audio_setting.sample_rate", "audio_setting.bitrate",
		"audio_setting.format", "audio_setting.channel",
	}

	// gemini TTS 创建报文：{contents,generationConfig}；responseModalities
	// 与整棵 speechConfig 是规范映射字段，generationConfig.temperature 等
	// 新增键仍允许（契约 §5.1）。
	geminiTTSMetadataReservedPaths = []string{
		"contents",
		"generationConfig.responseModalities",
		"generationConfig.speechConfig",
	}

	// volcengine TTS 创建报文：{model,text_prompt,speaker,audio_config}；
	// audio_config.loudness_rate 等新增键仍允许（契约 §9.2）。
	volcengineTTSMetadataReservedPaths = []string{
		"model", "text_prompt", "speaker",
		"audio_config.format", "audio_config.speech_rate",
	}

	// volcengine 流式 TTS 的 metadata 相对 req_params 层合并：text/speaker/
	// audio_params 及其格式与语速映射为规范路径，enable_emotion 等新增键
	// 仍允许。
	volcengineTTSStreamMetadataReservedPaths = []string{
		"text", "speaker",
		"audio_params.format", "audio_params.speech_rate",
	}
)
