// volcengine_tts_stream.go 是火山豆包语音合成模型 2.0（seed-tts-2.0）的
// 单向流式合成出站转换（[火山流式语音桥接设计](docs/functions/火山流式语音
// 桥接设计.md) V1a；注册表仍挂 speechAdapters 的 "volcengine" 条目——同一
// volcengineSpeechAdapter 内按 model 分派：seed-audio-1.0/doubao-tts 走既有
// 同步面（volcengine_tts.go，行为零变化），seed-tts-2.0 走本文件流式面）。
// 报文依据（2026-10-18 实测 + 官方文档检索，帧级 B 级实施首步回填）：
// POST /api/v3/tts/unidirectional（语音服务域，与同步面同 host——URL 构造
// 特例同 chainVolcengineSpeechUpstreamURL），鉴权 X-Api-Key 单头（同一
// speech_token 实测穿过鉴权层，零新凭据），请求
// {"user":{"uid"},"req_params":{"text","speaker","audio_params":{...}}}；
// 响应为 NDJSON 流（A 级实测：音频帧 data 为 base64 字符串、句级帧带
// sentence、终止帧 code 20000000、错误形态 {"reqid","code","message"}——
// 55000000 音色未授权）。V1a 全缓冲转换（沿 minimax/gemini「JSON 回
// base64 音频」同族先例：32MB 窗口完整缓冲后拼接音频透传）；字符级流式回
// 流为 V1b（需共享管道新增转换透传模式，独立跟进）。
// 计费：官方维度为字符（语音合成 2.0 按字符 3 元/万字符，豆包语音计费页）
// → 链上沿既有 TtsInputChars 请求字符自算口径（speechmeter.go），目录行
// ttsInputCostPerChar 计价（minimax speech 行同字段先例）；响应内 duration
// 不参与计费。
package gatewaymedia

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// VolcengineTTSStreamModel 是豆包语音合成模型 2.0 的统一面模型名（官方
// X-Api-Resource-Id 即该值——模型名即资源 ID，同 volc.seedasr.auc 先例）。
const VolcengineTTSStreamModel = "seed-tts-2.0"

// volcengineTTSStreamOutputFormats 是 2.0 面的对外 response_format 词表
// （官方文档：mp3/pcm/ogg_opus/wav，2026-10-18 检索；与 1.0 同步面词表
// mp3/wav/ogg_opus/opus 不同——2.0 无 opus、pcm 为正式支持，1.0 的
// 「pcm 上游挂起不采纳」限制不适用于 2.0）。词表外本地 400 零转码。
var volcengineTTSStreamOutputFormats = []string{"mp3", "pcm", "ogg_opus", "wav"}

// volcengineTTSStreamContentTypeOf 按 response_format 推导下游 content-type
// （2.0 响应不回显 mime；pcm 无容器按裸 PCM 描述）。
func volcengineTTSStreamContentTypeOf(format string) string {
	switch strings.TrimSpace(format) {
	case "wav":
		return "audio/wav"
	case "ogg_opus":
		return "audio/ogg"
	case "pcm":
		return "audio/pcm"
	default: // mp3（含空缺省）
		return "audio/mpeg"
	}
}

// IsVolcengineTTSStreamModel 判定 model 是否流式 TTS 面（TrimSpace 后精确
// 匹配；1.0 系模型名含占位归一不进入本判定——同步面分支先行）。
func IsVolcengineTTSStreamModel(model string) bool {
	return strings.TrimSpace(model) == VolcengineTTSStreamModel
}

// volcengineTTSStreamRequest 是 unidirectional 面的请求体（B 级：req_params
// 全字段以官方文档实施首步回填；speech_rate 为速度映射位——2.0 官方字段名
// 待实弹核对，暂按 audio_params 同族命名落位并留 TODO-B 标注）。
type volcengineTTSStreamRequest struct {
	User      map[string]any `json:"user"`
	ReqParams map[string]any `json:"req_params"`
}

// buildVolcengineTTSStreamRequest 构造 2.0 流式面请求体与路径。能力边界
// 裁决（沿同步面 §2.4 规则 2 先例）：response_format 词表外 → 400；speed
// 超声明区间 [0.2, 3.0]（与 1.0 同族区间）→ 400。provider_options 命中
// volcengine 的子对象 deep-merge 到 req_params 层（厂商个例参数如
// enable_emotion，契约 §2.1 L3）。
func buildVolcengineTTSStreamRequest(ir SpeechRequest) (string, []byte, error) {
	format := strings.TrimSpace(ir.ResponseFormat)
	if format == "" {
		format = "mp3"
	}
	supported := false
	for _, candidate := range volcengineTTSStreamOutputFormats {
		if candidate == format {
			supported = true
			break
		}
	}
	if !supported {
		return "", nil, fmt.Errorf("该模型仅支持 response_format=mp3/pcm/ogg_opus/wav，不支持 %s，网关不做服务端格式转换", format)
	}
	if ir.Speed != nil && (*ir.Speed < VolcengineSpeechSpeedMin || *ir.Speed > VolcengineSpeechSpeedMax) {
		return "", nil, fmt.Errorf("speed=%v 超出该厂商支持区间 [0.2, 3.0]", *ir.Speed)
	}
	audioParams := map[string]any{
		"format": format,
	}
	if ir.Speed != nil {
		// TODO-B（实施首步回填）：2.0 速度字段名以官方文档/实弹核对；
		// speech_rate 为同族暂定命名。
		audioParams["speech_rate"] = *ir.Speed
	}
	body := volcengineTTSStreamRequest{
		User: map[string]any{"uid": volcASRUpstreamUserID},
		ReqParams: map[string]any{
			"text":         ir.Input,
			"speaker":      ir.Voice,
			"audio_params": audioParams,
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", nil, fmt.Errorf("编码火山流式 TTS 请求体失败: %w", err)
	}
	return "/api/v3/tts/unidirectional", encoded, nil
}

// volcengineTTSStreamFrame 是 unidirectional 流的行帧消费面（2026-10-18
// 真实上游 A 级实测，2.0 音色出音 71KB mp3）：音频帧 `{"code":0,"data":"<base64
// 分块>"}`——**data 是 base64 字符串非对象**；句级帧 `{"code":0,"data":null,
// "sentence":{...}}`（丢弃）；终止帧 `{"code":20000000,"message":"OK"}`；
// 错误帧 `{"reqid","code":55000000,"message"}`（音色未授权形态实测）。
type volcengineTTSStreamFrame struct {
	Code    *int64          `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// transformVolcengineTTSStreamResponse 解析 NDJSON 流式载荷：data 为 base64
// 字符串的行按序解码拼接（data 为对象/null 的行——句级帧——丢弃）；
// code==20000000 为终止帧；code 非 0 非 20000000 以 message 上抛（错误帧）；
// 无任何音频分块 → 协议违约上抛。contentType 按 2.0 词表推导。
func transformVolcengineTTSStreamResponse(upstreamBody []byte, request SpeechRequest) ([]byte, string, error) {
	var audio bytes.Buffer
	sawError := ""
	for _, line := range strings.Split(string(upstreamBody), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var frame volcengineTTSStreamFrame
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			return nil, "", fmt.Errorf("火山流式 TTS 响应含非 JSON 行: %w", err)
		}
		code := int64(0)
		if frame.Code != nil {
			code = *frame.Code
		}
		if code == 20000000 {
			break // 终止帧（A 级实测：message "OK"，data null）
		}
		if code != 0 {
			if sawError == "" {
				sawError = strings.TrimSpace(frame.Message)
				if sawError == "" {
					sawError = fmt.Sprintf("火山流式 TTS 上游错误码 %d", code)
				}
			}
			continue
		}
		// 音频帧：data 是 base64 字符串（A 级实测形态）。非字符串 data
		//（句级帧的 null/对象）按 json.Unmarshal 类型错误自然跳过。
		var chunk string
		if err := json.Unmarshal(frame.Data, &chunk); err != nil || chunk == "" {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(chunk)
		if err != nil {
			return nil, "", fmt.Errorf("火山流式 TTS 音频分块 base64 解码失败: %w", err)
		}
		audio.Write(decoded)
	}
	if audio.Len() == 0 {
		if sawError != "" {
			return nil, "", fmt.Errorf("火山流式 TTS 上游错误: %s", sawError)
		}
		return nil, "", fmt.Errorf("火山流式 TTS 响应缺少音频分块（NDJSON 无 data 音频载荷）")
	}
	return audio.Bytes(), volcengineTTSStreamContentTypeOf(request.ResponseFormat), nil
}
