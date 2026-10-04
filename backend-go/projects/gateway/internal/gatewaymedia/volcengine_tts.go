// volcengine_tts.go 是 M6 同步音频的火山豆包 TTS（openspeech /api/v3/tts）
// 出站 adapter（音频设计 §5：媒体 IR → 每厂商一个 adapter，注册表组织；本
// 文件挂 speechAdapters 的 "volcengine" 条目）。报文依据契约 §9.2（置信度
// B：官方格式多源交叉）：POST /api/v3/tts（host 恒 openspeech.bytedance.com
// 语音服务域，与账户 ark base_url 无关——URL 构造特例由链上层
// chainVolcengineSpeechUpstreamURL 承载，本 adapter 只产出路径），请求
// {user:{uid},req_params:{text,speaker,audio_params:{format,sample_rate,
// speed_ratio}},reqid,operation:"query"}；公共参数映射 input→req_params.text、
// voice→speaker、response_format→audio_params.format（pcm/mp3/ogg_opus/wav，
// 零转码）、speed→speed_ratio（厂商区间 0.2–3.0，公共 0.25–4.0 超区间按
// 契约 §2.4 规则 2 本地 400）；reqid 网关生成（uuid）；user.uid 是账户
// speech_appid（链上经 AccountVendorRefs 注入的固定值，非请求面参数——
// 官方语义为调用方标识）。请求面无 model 字段（模型由语音应用/资源决定，
// 官方可查证口径见 docs/functions/火山方舟账号接入.md）——统一面的 model
// 仅作必填占位，值不透传上游。响应 {reqid,code,message,data}：code==3000
// 成功、data 为 **base64 编码**音频字符串（本 adapter 必须 base64→bytes
// 解码后透传，不得把 base64 字符串当音频下发）；code!=3000 即失败上抛。
// 计费：按字符、上游无字符回报（§2.8）→ 链上按请求字符自算 TtsInputChars
//（本包 SpeechInputCharsFromBody 既有口径，无 extra_info 形态回报可优先）。
package gatewaymedia

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// VolcengineSpeechSpeedMin/Max 是火山 TTS 的 speed 厂商区间（契约 §9.2：
// 约 0.2–3.0；公共区间 0.25–4.0 超出部分按 §2.4 规则 2 拒绝，不裁剪
// 不猜测）。
const (
	VolcengineSpeechSpeedMin = 0.2
	VolcengineSpeechSpeedMax = 3.0
)

// volcengineSpeechSampleRate 是 §9.2 请求示例的采样率（24000，与官方
// audio_params.sample_rate 示例一致；响应为编码音频流，网关零转码不改写）。
const volcengineSpeechSampleRate = 24000

// volcengineSpeechOutputFormats 是火山 TTS 支持的对外 response_format 清单
//（契约 §9.2：pcm/mp3/ogg_opus/wav，零转码；opus/aac 等 OpenAI 词表值不在
// 火山请求面，400 拒绝沿 gemini 仅 pcm / minimax 词表先例）。
var volcengineSpeechOutputFormats = []string{"mp3", "pcm", "ogg_opus", "wav"}

// volcengineSpeechContentTypeOf 按 response_format 推导下游 content-type
//（火山响应对象不回显 mime，契约 §9.2；rate 按请求面采样率 24000 声明 pcm
// 形态）。空 format 是 OpenAI 默认 mp3 语义。
func volcengineSpeechContentTypeOf(format string) string {
	switch strings.TrimSpace(format) {
	case "wav":
		return "audio/wav"
	case "pcm":
		return "audio/L16;rate=24000"
	case "ogg_opus":
		return "audio/ogg"
	default: // mp3（含空缺省）
		return "audio/mpeg"
	}
}

// volcengineSpeechAdapter 实现火山豆包 TTS（openspeech /api/v3/tts）出站
// 转换。
type volcengineSpeechAdapter struct{}

func (volcengineSpeechAdapter) Provider() string { return "volcengine" }

// BuildRequest 构造 §9.2 报文：POST /api/v3/tts（路径相对语音服务域根，
// URL 由链上层 chainVolcengineSpeechUpstreamURL 拼接固定 host）。能力边界
// 裁决（契约 §2.4 规则 2，本地 400 不换账户）：
//   - response_format 词表外（如 opus/aac/flac）→ 拒绝（零转码）；
//   - speed 超厂商区间 0.2–3.0 → 拒绝（公共区间 0.25–4.0 与厂商区间的
//     差集不得静默裁剪）。
func (a volcengineSpeechAdapter) BuildRequest(ir SpeechRequest) (string, []byte, error) {
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
		return "", nil, fmt.Errorf("该模型仅支持 response_format=mp3/pcm/ogg_opus/wav，不支持 %s，网关不做服务端格式转换", format)
	}
	if ir.Speed != nil && (*ir.Speed < VolcengineSpeechSpeedMin || *ir.Speed > VolcengineSpeechSpeedMax) {
		return "", nil, fmt.Errorf("speed=%v 超出该厂商支持区间 [0.2, 3.0]", *ir.Speed)
	}
	// user.uid 固定值：账户 speech_appid（官方语义为调用方标识；网关注入
	// 的账户级引用，非请求面参数。链上凭据分派已先行校验，此处空值防御
	// 上抛不猜测）。
	uid := strings.TrimSpace(ir.AccountVendorRefs["speech_appid"])
	if uid == "" {
		return "", nil, fmt.Errorf("火山语音账户缺少 speech_appid 凭据，不能构造 TTS 请求")
	}
	audioParams := map[string]any{
		"format":      format,
		"sample_rate": volcengineSpeechSampleRate,
	}
	if ir.Speed != nil {
		audioParams["speed_ratio"] = *ir.Speed
	}
	body := map[string]any{
		"user": map[string]any{
			"uid": uid,
		},
		"req_params": map[string]any{
			"text":    ir.Input,
			"speaker": ir.Voice,
			"audio_params": audioParams,
		},
		"reqid":     newVolcengineSpeechReqID(),
		"operation": "query",
	}
	// provider_options 命中 volcengine 的子对象 deep-merge（契约 §2.1 L3，
	// 如 {"volcengine":{"req_params":{"emotion":"happy"}}} 覆盖/补齐厂商个例
	// 参数——厂商值覆盖同名键，嵌套对象递归合并）。
	body = MergeProviderOptions(body, a.Provider(), ir.ProviderOptions)
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", nil, fmt.Errorf("编码火山 TTS 请求体失败: %w", err)
	}
	return "/api/v3/tts", encoded, nil
}

// volcengineSpeechResponseBody 是 §9.2 响应的最小消费面：code==3000 成功、
// data 为 base64 音频、message 错误信息。
type volcengineSpeechResponseBody struct {
	Reqid   string `json:"reqid"`
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data"`
}

// TransformResponse 解码 data 的 base64 载荷（契约 §9.2：base64→bytes，
// 不得把 base64 字符串当音频下发）。contentType 按请求 response_format
// 推导（火山响应不回显 mime，volcengineSpeechContentTypeOf；request 缺省
// 按 mp3 语义）。code != 3000 → 上游错误上抛（受理语义区分：同步 TTS 无
// 任务受理面，code!=3000 即整单失败，不猜测音频可用性）。
func (a volcengineSpeechAdapter) TransformResponse(upstreamBody []byte, request SpeechRequest) ([]byte, string, error) {
	var decoded volcengineSpeechResponseBody
	if err := json.Unmarshal(upstreamBody, &decoded); err != nil {
		return nil, "", fmt.Errorf("火山 TTS 响应不是有效 JSON: %w", err)
	}
	if decoded.Code != 3000 {
		return nil, "", fmt.Errorf("火山 TTS 上游错误（code=%d）: %s", decoded.Code, decoded.Message)
	}
	if decoded.Data == "" {
		return nil, "", fmt.Errorf("火山 TTS 响应缺少 data 音频载荷")
	}
	audio, err := base64.StdEncoding.DecodeString(decoded.Data)
	if err != nil {
		return nil, "", fmt.Errorf("火山 TTS data base64 解码失败: %w", err)
	}
	if len(audio) == 0 {
		return nil, "", fmt.Errorf("火山 TTS data 解码后为空载荷")
	}
	return audio, volcengineSpeechContentTypeOf(request.ResponseFormat), nil
}

// newVolcengineSpeechReqID 生成 reqid（uuid v4 形状，网关生成——契约 §9.2；
// 官方要求每次请求唯一）。crypto/rand 失败不可恢复，响亮失败不静默降级
//（沿 mockupstream 火山任务 id 生成先例）。
func newVolcengineSpeechReqID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(fmt.Sprintf("gatewaymedia: volcengine tts reqid generation failed: %v", err))
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}
