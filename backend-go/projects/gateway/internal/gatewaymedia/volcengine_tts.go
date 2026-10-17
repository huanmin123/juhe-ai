// volcengine_tts.go 是火山豆包音频生成的同步 TTS（openspeech
// /api/v3/tts/create）出站 adapter（音频设计 §5：媒体 IR → 每厂商一个
// adapter，注册表组织；本文件挂 speechAdapters 的 "volcengine" 条目）。报文
// 依据契约 §9.2（置信度 A：官方文档 + 2026-10-17 真实上游实测）：M6 依据的
// 旧端点 POST /api/v3/tts 已 404 下线，现役面为 POST /api/v3/tts/create
// （host 恒 openspeech.bytedance.com 语音服务域，与账户 ark base_url 无关
// ——URL 构造特例由链上层 chainVolcengineSpeechUpstreamURL 承载，本
// adapter 只产出路径），鉴权 X-Api-Key 单头（链上凭据分派承载），请求
// {model,text_prompt,speaker,audio_config:{format,speech_rate}}；公共参数
// 映射 input→text_prompt、voice→speaker、response_format→audio_config.
// format（实测词表 mp3/wav/ogg_opus/opus，pcm 实测上游挂起不采纳，零转
// 码）、speed→audio_config.speech_rate（倍率形态；上游对越界值静默接受，
// 网关维持声明区间 0.2–3.0 预校验，契约 §2.4 规则 2 本地 400）。model 真
// 实透传（现役 seed-audio-1.0）；统一面历史占位名 doubao-tts 由本 adapter
// 归一映射为 seed-audio-1.0，兼容既有账户的占位目录行。响应 200
// {audio,duration,original_duration,url}：audio 为 **base64 编码**音频字符
// 串（本 adapter 必须 base64→bytes 解码后透传，不得把 base64 字符串当音频
// 下发）；成功响应无 code 字段，audio 缺失/解码失败即错误上抛（HTTP 非
// 200 的平铺错误信封由链上层按状态码统一处理，同步面无任务受理语义）。
// 计费：按字符、上游无字符回报（§2.8）→ 链上按请求字符自算
// TtsInputChars（本包 SpeechInputCharsFromBody 既有口径）。
package gatewaymedia

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// VolcengineSpeechSpeedMin/Max 是火山音频生成的 speed 声明区间（契约 §9.2：
// 0.2–3.0；3.0 实测接受、上游对越界值静默接受不拒绝——网关维持声明区间
// 预校验，公共区间 0.25–4.0 超出部分按 §2.4 规则 2 拒绝，不裁剪不猜测）。
const (
	VolcengineSpeechSpeedMin = 0.2
	VolcengineSpeechSpeedMax = 3.0
)

// 现役模型与占位名归一（契约 §9.2）：统一面 model 真实透传；doubao-tts 是
// 旧版"无 model 字段"时期的目录占位行（火山方舟账号接入.md 模型目录节），
// 新版请求面有 model 字段，占位名出站前归一映射为现役模型，既有账户的
// supportedModels 引用与目录行保持可用。
const (
	VolcengineTTSCurrentModel     = "seed-audio-1.0"
	VolcengineTTSPlaceholderModel = "doubao-tts"
)

// volcengineSpeechOutputFormats 是火山音频生成支持的对外 response_format
// 清单（契约 §9.2 实测词表：mp3/wav/ogg_opus/opus，零转码；opus 上游返回
// OggS 容器——2026-10-17 实测注记：上游对 opus 偶发返回 WAV(RIFF) 容器字
// 节，同请求重试即恢复 OggS，上游内部编码产出不稳定，网关零转码不纠正，
// 下游按字节头识别容器、勿按注释"纠正"content-type；pcm 实测上游挂起超时
// 不采纳；flac/aac 等 OpenAI 词表值不在火山请求面，400 拒绝沿 gemini 仅
// pcm / minimax 词表先例）。
var volcengineSpeechOutputFormats = []string{"mp3", "wav", "ogg_opus", "opus"}

// volcengineSpeechContentTypeOf 按 response_format 推导下游 content-type
// （火山响应不回显 mime，契约 §9.2；ogg_opus/opus 上游均为 OggS 容器）。
// 空 format 是 OpenAI 默认 mp3 语义。
func volcengineSpeechContentTypeOf(format string) string {
	switch strings.TrimSpace(format) {
	case "wav":
		return "audio/wav"
	case "ogg_opus", "opus":
		return "audio/ogg"
	default: // mp3（含空缺省）
		return "audio/mpeg"
	}
}

// volcengineSpeechAdapter 实现火山豆包音频生成（openspeech
// /api/v3/tts/create）出站转换。
type volcengineSpeechAdapter struct{}

func (volcengineSpeechAdapter) Provider() string { return "volcengine" }

// BuildRequest 构造 §9.2 报文：POST /api/v3/tts/create（路径相对语音服务域
// 根，URL 由链上层 chainVolcengineSpeechUpstreamURL 拼接固定 host）。能力
// 边界裁决（契约 §2.4 规则 2，本地 400 不换账户）：
//   - response_format 词表外（如 pcm/flac/aac）→ 拒绝（零转码）；
//   - speed 超声明区间 0.2–3.0 → 拒绝（公共区间 0.25–4.0 与声明区间的
//     差集不得静默裁剪）。
func (a volcengineSpeechAdapter) BuildRequest(ir SpeechRequest) (string, []byte, error) {
	if IsVolcengineTTSStreamModel(ir.Model) {
		// 2.0 流式面分支（火山流式语音桥接设计 V1a）：词表/请求体/路径均
		// 与 1.0 同步面不同构，先行分派（在 1.0 词表校验之前）。
		return buildVolcengineTTSStreamRequest(ir)
	}
	if ir.Model == "" {
		return "", nil, fmt.Errorf("语音合成请求缺少 model")
	}
	if ir.Input == "" {
		return "", nil, fmt.Errorf("语音合成请求缺少 input")
	}
	if ir.Voice == "" {
		return "", nil, fmt.Errorf("语音合成请求缺少 voice")
	}
	format := strings.TrimSpace(ir.ResponseFormat)
	if format == "" {
		format = "mp3" // OpenAI 默认语义（契约 §4.1）
	}
	supported := false
	for _, candidate := range volcengineSpeechOutputFormats {
		if candidate == format {
			supported = true
			break
		}
	}
	if !supported {
		return "", nil, fmt.Errorf("该模型仅支持 response_format=mp3/wav/ogg_opus/opus，不支持 %s，网关不做服务端格式转换", format)
	}
	if ir.Speed != nil && (*ir.Speed < VolcengineSpeechSpeedMin || *ir.Speed > VolcengineSpeechSpeedMax) {
		return "", nil, fmt.Errorf("speed=%v 超出该厂商支持区间 [0.2, 3.0]", *ir.Speed)
	}
	model := strings.TrimSpace(ir.Model)
	if model == VolcengineTTSPlaceholderModel {
		model = VolcengineTTSCurrentModel
	}
	audioConfig := map[string]any{
		"format": format,
	}
	if ir.Speed != nil {
		audioConfig["speech_rate"] = *ir.Speed
	}
	body := map[string]any{
		"model":        model,
		"text_prompt":  ir.Input,
		"speaker":      ir.Voice,
		"audio_config": audioConfig,
	}
	// provider_options 命中 volcengine 的子对象 deep-merge（契约 §2.1 L3，
	// 如 {"volcengine":{"audio_config":{"loudness_rate":10}}} 覆盖/补齐厂商
	// 个例参数——厂商值覆盖同名键，嵌套对象递归合并）。
	body = MergeProviderOptions(body, a.Provider(), ir.ProviderOptions)
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", nil, fmt.Errorf("编码火山 TTS 请求体失败: %w", err)
	}
	return "/api/v3/tts/create", encoded, nil
}

// volcengineSpeechResponseBody 是 §9.2 响应的最小消费面：audio 为 base64
// 音频（成功响应无 code 字段；code/message 仅出现在 HTTP 非 200 的平铺错误
// 信封，由链上层按状态码统一处理）。
type volcengineSpeechResponseBody struct {
	Audio    string  `json:"audio"`
	Duration float64 `json:"duration"`
	URL      string  `json:"url"`
}

// TransformResponse 解码 audio 的 base64 载荷（契约 §9.2：base64→bytes，
// 不得把 base64 字符串当音频下发）。contentType 按请求 response_format
// 推导（火山响应不回显 mime，volcengineSpeechContentTypeOf；request 缺省
// 按 mp3 语义）。audio 缺失/解码失败 → 上游错误上抛（同步面无任务受理语
// 义，不猜测音频可用性）。
func (a volcengineSpeechAdapter) TransformResponse(upstreamBody []byte, request SpeechRequest) ([]byte, string, error) {
	if IsVolcengineTTSStreamModel(request.Model) {
		// 2.0 流式面分支：NDJSON 分块拼接（同步面的单 JSON base64 解析
		// 不适用于流式载荷）。
		return transformVolcengineTTSStreamResponse(upstreamBody, request)
	}
	var decoded volcengineSpeechResponseBody
	if err := json.Unmarshal(upstreamBody, &decoded); err != nil {
		return nil, "", fmt.Errorf("火山 TTS 响应不是有效 JSON: %w", err)
	}
	if decoded.Audio == "" {
		return nil, "", fmt.Errorf("火山 TTS 响应缺少 audio 音频载荷")
	}
	audio, err := base64.StdEncoding.DecodeString(decoded.Audio)
	if err != nil {
		return nil, "", fmt.Errorf("火山 TTS audio base64 解码失败: %w", err)
	}
	if len(audio) == 0 {
		return nil, "", fmt.Errorf("火山 TTS audio 解码后为空载荷")
	}
	return audio, volcengineSpeechContentTypeOf(request.ResponseFormat), nil
}

// SpeechReportedDurationSecondsFromJSON 从 volcengine TTS 上游成功响应
// JSON 根对象提取生成原始时长秒数（顶层 duration 字段；官方计费维度即
// 该原始时长——豆包语音计费文档明文"按模型生成的原始时长计费，倍速不
// 影响计费时长"，精确至秒，2026-10-17 维度修正批）。缺失或非正值返回
// ok=false（调用方 0 计量 + usage_missing，不猜测）。非 volcengine 形态
// 根对象恒 false，其它 provider 计量路径不受影响。
func SpeechReportedDurationSecondsFromJSON(root any) (float64, bool) {
	object, ok := root.(map[string]any)
	if !ok {
		return 0, false
	}
	duration, ok := object["duration"].(float64)
	if !ok || duration <= 0 {
		return 0, false
	}
	return duration, true
}
