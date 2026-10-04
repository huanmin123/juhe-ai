// minimax_tts.go 是 M3 同步音频的 MiniMax TTS（t2a_v2）出站 adapter
// （音频设计 §5：媒体 IR → 每厂商一个 adapter，注册表组织；本文件挂
// speechAdapters 的 "minimax" 条目）。报文依据契约 §8.2（置信度 B：官方
// 文档多源确认）：POST /v1/t2a_v2（Bearer API Key），请求
// {model,text,stream:false,voice_setting:{voice_id,speed,vol,pitch},
// audio_setting:{sample_rate,bitrate,format,channel}}；公共参数映射
// input→text、voice→voice_setting.voice_id、speed（厂商区间 0.5–2.0，
// 公共 0.25–4.0 超区间按契约 §2.4 规则 2 本地 400）、response_format→
// audio_setting.format（mp3/pcm/flac/wav，零转码）；厂商个例（vol/pitch/
// 情感）经 provider_options.minimax deep-merge。响应 data.audio 是
// **hex 编码**音频字符串——本 adapter 必须 hex→bytes 解码后透传，不得把
// hex 字符串当音频下发（契约 §8.2 加粗约束）；计量面 extra_info 的
// usage_characters 是 TTS 字符计量的上游回报优先来源（§2.8）。
package gatewaymedia

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// MinimaxSpeechSpeedMin/Max 是 minimax t2a_v2 的 speed 厂商区间（契约
// §8.2：0.5–2.0；公共区间 0.25–4.0 超出部分按 §2.4 规则 2 拒绝，不裁剪
// 不猜测）。
const (
	MinimaxSpeechSpeedMin = 0.5
	MinimaxSpeechSpeedMax = 2.0
)

// minimaxSpeechOutputFormats 是 minimax t2a_v2 支持的对外 response_format
// 清单（契约 §8.2：mp3/pcm/flac/wav，零转码；opus/aac 等 OpenAI 词表值
// 不在 minimax 请求面，400 拒绝沿 gemini 仅 pcm 先例）。
var minimaxSpeechOutputFormats = []string{"mp3", "pcm", "flac", "wav"}

// minimaxSpeechContentTypeOf 按 response_format 推导下游 content-type
//（minimax 响应对象不回显 mime 信息，契约 §8.2；rate 按请求面默认采样率
// 32000 声明 pcm 形态）。空 format 是 OpenAI 默认 mp3 语义。
func minimaxSpeechContentTypeOf(format string) string {
	switch strings.TrimSpace(format) {
	case "wav":
		return "audio/wav"
	case "pcm":
		return "audio/L16;rate=32000"
	case "flac":
		return "audio/flac"
	default: // mp3（含空缺省）
		return "audio/mpeg"
	}
}

// minimaxSpeechAdapter 实现 MiniMax TTS（t2a_v2）出站转换。
type minimaxSpeechAdapter struct{}

func (minimaxSpeechAdapter) Provider() string { return "minimax" }

// BuildRequest 构造 §8.2 报文：POST /v1/t2a_v2（路径相对账户 base_url，
// URL 由链上层经 gatewayopenai.BuildUpstreamURL 归一为 /v1 前缀形态）。
// 能力边界裁决（契约 §2.4 规则 2，本地 400 不换账户）：
//   - response_format 词表外（如 opus/aac）→ 拒绝（零转码）；
//   - speed 超厂商区间 0.5–2.0 → 拒绝（公共区间 0.25–4.0 与厂商区间的
//     差集不得静默裁剪）。
func (a minimaxSpeechAdapter) BuildRequest(ir SpeechRequest) (string, []byte, error) {
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
	for _, candidate := range minimaxSpeechOutputFormats {
		if candidate == format {
			supported = true
			break
		}
	}
	if !supported {
		return "", nil, fmt.Errorf("该模型仅支持 response_format=mp3/pcm/flac/wav，不支持 %s，网关不做服务端格式转换", format)
	}
	if ir.Speed != nil && (*ir.Speed < MinimaxSpeechSpeedMin || *ir.Speed > MinimaxSpeechSpeedMax) {
		return "", nil, fmt.Errorf("speed=%v 超出该厂商支持区间 [0.5, 2.0]", *ir.Speed)
	}
	body := map[string]any{
		"model":  ir.Model,
		"text":   ir.Input,
		"stream": false,
		"voice_setting": map[string]any{
			"voice_id": ir.Voice,
		},
		"audio_setting": map[string]any{
			"sample_rate": 32000,
			"bitrate":     128000,
			"format":      format,
			"channel":     1,
		},
	}
	if ir.Speed != nil {
		body["voice_setting"].(map[string]any)["speed"] = *ir.Speed
	}
	// provider_options 命中 minimax 的子对象 deep-merge（契约 §2.1 L3，
	// 如 {"minimax":{"voice_setting":{"vol":2,"pitch":2}}} 覆盖/补齐厂商
	// 个例参数）。
	body = MergeProviderOptions(body, a.Provider(), ir.ProviderOptions)
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", nil, fmt.Errorf("编码 minimax TTS 请求体失败: %w", err)
	}
	return "/v1/t2a_v2", encoded, nil
}

// minimaxSpeechResponseBody 是 §8.2 响应的最小消费面：data.audio（hex
// 编码音频）、base_resp（错误信封，status_code!=0 即失败）、extra_info
//（usage_characters 是字符计量上游回报，由链上计量分支消费）。
type minimaxSpeechResponseBody struct {
	Data struct {
		Audio  string `json:"audio"`
		Status int    `json:"status"`
	} `json:"data"`
	BaseResp   *minimaxBaseResp `json:"base_resp"`
	ExtraInfo *struct {
		AudioLength      float64 `json:"audio_length"`
		UsageCharacters  float64 `json:"usage_characters"`
		WordSize         float64 `json:"word_size"`
		AudioSampleRate  float64 `json:"audio_sampling_rate"`
	} `json:"extra_info"`
}

// TransformResponse 解码 data.audio 的 hex 载荷（契约 §8.2：hex→bytes，
// 不得把 hex 字符串当音频下发）。contentType 按请求 response_format 推导
//（minimax 响应不回显 mime，minimaxSpeechContentTypeOf；request 缺省按
// mp3 语义）。base_resp.status_code != 0 → 上游错误上抛（不猜测音频可
// 用性）。
func (a minimaxSpeechAdapter) TransformResponse(upstreamBody []byte, request SpeechRequest) ([]byte, string, error) {
	var decoded minimaxSpeechResponseBody
	if err := json.Unmarshal(upstreamBody, &decoded); err != nil {
		return nil, "", fmt.Errorf("minimax TTS 响应不是有效 JSON: %w", err)
	}
	if decoded.BaseResp != nil && decoded.BaseResp.StatusCode != 0 {
		return nil, "", fmt.Errorf("minimax TTS 上游错误（base_resp.status_code=%d）: %s",
			decoded.BaseResp.StatusCode, decoded.BaseResp.StatusMsg)
	}
	if decoded.Data.Audio == "" {
		return nil, "", fmt.Errorf("minimax TTS 响应缺少 data.audio 音频载荷")
	}
	audio, err := hex.DecodeString(decoded.Data.Audio)
	if err != nil {
		return nil, "", fmt.Errorf("minimax TTS data.audio hex 解码失败: %w", err)
	}
	if len(audio) == 0 {
		return nil, "", fmt.Errorf("minimax TTS data.audio 解码后为空载荷")
	}
	return audio, minimaxSpeechContentTypeOf(request.ResponseFormat), nil
}

// SpeechReportedCharsFromJSON 从已解析的上游响应 JSON 根对象提取 minimax
// extra_info.usage_characters（契约 §8.2/§2.8：TTS 字符计量的上游回报
// 优先来源）。缺失或非正值返回 0（调用方回落请求字符数自算，不猜测）。
// 非 minimax 形态的根对象（无 extra_info.usage_characters）恒返回 0，
// gemini TTS 计量路径不受影响。
func SpeechReportedCharsFromJSON(root any) int64 {
	object, ok := root.(map[string]any)
	if !ok {
		return 0
	}
	extra, ok := object["extra_info"].(map[string]any)
	if !ok {
		return 0
	}
	characters, ok := extra["usage_characters"].(float64)
	if !ok || characters <= 0 {
		return 0
	}
	return int64(characters)
}
