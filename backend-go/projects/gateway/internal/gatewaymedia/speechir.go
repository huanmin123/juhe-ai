// SpeechIR 是 M1 同步音频请求的统一中间表示（音频设计 §5：下游 OpenAI
// 形态 → 媒体 IR → 每厂商一个出站 adapter）。本文件承载 OpenAI speech
// 请求体的最小规范字段（L1/L2 子集，契约 §2.3）：adapter 消费 IR 构造上游
// 报文，网关侧计量（TTS 输入字符数）也从 IR 同源提取。M2 的 MediaJobIR
// （异步任务统一模型）沿用本文件的注册表组织方式扩展。
package gatewaymedia

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// SpeechRequest 是 POST /v1/audio/speech 请求体的规范表示。L1：Model/Input/
// Voice；L2：Speed/ResponseFormat/Instructions/Language（契约 §2.3 值域，
// 校验裁决按厂商 adapter 承载——openai 族直连透传由上游裁决，gemini 等转换
// 型 adapter 在 BuildSpeechUpstreamRequest 内做能力裁决）。
// ProviderOptions 是 L3 扩展通道的对象形态投影（键=provider_code，契约
// §2.1；M3 minimax TTS 起消费——vol/pitch 等厂商个例参数 deep-merge 进
// t2a_v2 报文；openai 族直连与 gemini TTS 不消费该字段）。
// AccountVendorRefs 是链上注入的账户级厂商引用（非请求面参数，客户端不可
// 控制；M6 火山 TTS 起消费——speech_appid 构造 user.uid 固定值，契约 §9.2；
// openai 族直连与 gemini/minimax adapter 不读取该字段）。
type SpeechRequest struct {
	Model             string
	Input             string
	Voice             string
	Speed             *float64
	ResponseFormat    string
	Instructions      string
	Language          string
	ProviderOptions   map[string]any
	AccountVendorRefs map[string]string
}

// OpenAISpeechResponseFormats 是 OpenAI TTS 的官方 response_format 全集
// （契约 §4.1/§5.1：目录元数据 ResponseFormats 的值域来源）。
var OpenAISpeechResponseFormats = []string{"mp3", "opus", "aac", "flac", "wav", "pcm"}

// ParseSpeechRequest 从已解析的 OpenAI speech 请求 JSON 对象提取 SpeechIR。
// 校验为 L1 必填面（model/input 非空；OpenAI voice 为必填参数）；L2 值域
// （speed 区间/response_format 词表）由各 adapter 裁决，此处不猜测。
func ParseSpeechRequest(body map[string]any) (SpeechRequest, error) {
	if body == nil {
		return SpeechRequest{}, fmt.Errorf("语音合成请求体必须是 JSON 对象")
	}
	model, _ := body["model"].(string)
	input, _ := body["input"].(string)
	voice, _ := body["voice"].(string)
	ir := SpeechRequest{
		Model:          strings.TrimSpace(model),
		Input:          input,
		Voice:          strings.TrimSpace(voice),
		ResponseFormat: strings.TrimSpace(stringValueOf(body["response_format"])),
		Instructions:   stringValueOf(body["instructions"]),
		Language:       strings.TrimSpace(stringValueOf(body["language"])),
	}
	if ir.Model == "" {
		return SpeechRequest{}, fmt.Errorf("语音合成请求缺少 model")
	}
	if ir.Input == "" {
		return SpeechRequest{}, fmt.Errorf("语音合成请求缺少 input")
	}
	if ir.Voice == "" {
		return SpeechRequest{}, fmt.Errorf("语音合成请求缺少 voice")
	}
	if speed, ok := body["speed"].(float64); ok {
		speedValue := speed
		ir.Speed = &speedValue
	}
	return ir, nil
}

// InputChars 返回 TTS 输入字符数（UTF-8 rune 数，契约 §2.8 openai TTS 网关
// 自算计量的口径）。
func (ir SpeechRequest) InputChars() int64 {
	return int64(utf8.RuneCountInString(ir.Input))
}

func stringValueOf(value any) string {
	text, _ := value.(string)
	return text
}
