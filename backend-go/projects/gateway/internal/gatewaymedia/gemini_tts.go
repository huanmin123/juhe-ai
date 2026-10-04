// gemini_tts.go 是 M1 同步音频的 Gemini TTS 出站 adapter（音频设计 §5：
// 媒体 IR → 每厂商一个 adapter，注册表组织，不进组合根 switch）。报文依据
// 契约 §5.1（置信度 A）：POST /v1beta/models/{model}:generateContent，
// generationConfig.responseModalities=["AUDIO"] + speechConfig.prebuiltVoice
// _config.voiceName；响应 candidates[0].content.parts[0].inlineData.data 为
// base64 裸 PCM（24kHz/16bit/mono）。response_format 裁决（契约 §5.1 定稿）：
// 仅 pcm 透传，其余 400（零转码）。
package gatewaymedia

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// SpeechAdapter 是同步音频出站 adapter 端口（M1 仅 gemini TTS；M2 MediaJobIR
// 的异步任务 adapter 沿本注册表结构扩展，不新增组合根 switch；M3 起
// minimax t2a_v2 同族接入）。
type SpeechAdapter interface {
	// Provider 返回注册键（provider_code 归一小写）。
	Provider() string
	// BuildRequest 把 SpeechIR 映射为上游请求（路径 + JSON body）。错误为
	// 请求侧 400 语义（能力边界裁决：零转码不猜测）。
	BuildRequest(ir SpeechRequest) (path string, body []byte, err error)
	// TransformResponse 把上游成功响应 JSON 转为下游音频载荷（裸字节 +
	// content-type）。request 携带归一后的 SpeechRequest 供按请求参数推导
	// content-type（M3 minimax：t2a_v2 响应不回显 mime，按 response_format
	// 推导；gemini 忽略该参数，mimeType 优先）。错误为上游协议失败语义。
	TransformResponse(upstreamBody []byte, request SpeechRequest) (audio []byte, contentType string, err error)
}

// speechAdapters 是 provider → adapter 注册表（音频设计 §5：媒体 adapter 以
// provider 注册表挂载；M3 minimax t2a_v2、M6 火山 /api/v3/tts 已接入，后续
// 供应商只新增条目）。
var speechAdapters = map[string]SpeechAdapter{
	"gemini":     geminiSpeechAdapter{},
	"minimax":    minimaxSpeechAdapter{},
	"volcengine": volcengineSpeechAdapter{},
}

// SpeechAdapterForProvider 按 provider_code 解析同步音频 adapter；nil 表示
// 该 provider 无媒体 adapter（openai 族直连透传，不进 adapter）。
func SpeechAdapterForProvider(providerCode string) SpeechAdapter {
	return speechAdapters[normalizeProviderKey(providerCode)]
}

func normalizeProviderKey(providerCode string) string {
	return strings.ToLower(strings.TrimSpace(providerCode))
}

// GeminiSpeechOutputContentType 是 gemini TTS 输出的固定 PCM 形态（契约
// §5.1：audio/L16;rate=24000，24kHz/16bit/mono）。
const GeminiSpeechOutputContentType = "audio/L16;rate=24000"

// geminiSpeechOutputFormats 是 gemini TTS 支持的对外 response_format 清单
// （目录 ResponseFormats 的静态镜像，契约 §5.1 裁决：仅 pcm）。
var geminiSpeechOutputFormats = []string{"pcm"}

// geminiSpeechAdapter 实现 Gemini TTS 出站转换。
type geminiSpeechAdapter struct{}

func (geminiSpeechAdapter) Provider() string { return "gemini" }

// geminiSpeechRequestBody 是 §5.1 请求体结构（speed 无对应字段：不支持即
// 忽略，不换算；instructions/language 同为忽略语义，M1 二进制响应无回显
// 通道）。
type geminiSpeechRequestBody struct {
	Contents         []geminiSpeechContent        `json:"contents"`
	GenerationConfig geminiSpeechGenerationConfig `json:"generationConfig"`
}

type geminiSpeechContent struct {
	Parts []geminiSpeechPart `json:"parts"`
}

type geminiSpeechPart struct {
	Text string `json:"text"`
}

type geminiSpeechGenerationConfig struct {
	ResponseModalities []string                 `json:"responseModalities"`
	SpeechConfig       geminiSpeechSpeechConfig `json:"speechConfig"`
}

type geminiSpeechSpeechConfig struct {
	PrebuiltVoiceConfig geminiSpeechPrebuiltVoice `json:"prebuiltVoiceConfig"`
}

type geminiSpeechPrebuiltVoice struct {
	VoiceName string `json:"voiceName"`
}

// BuildRequest 构造 §5.1 报文：model 进 URL 路径段（:generateContent 前），
// voice 透传公共值到 voiceName（voice 值域按目录 voices 清单校验属管理面
// 元数据承载，M1 目录未承载 voices 清单，网关侧不硬编码音色词表——上游
// 对非法音色 400，见任务报告缺口说明）。
func (a geminiSpeechAdapter) BuildRequest(ir SpeechRequest) (string, []byte, error) {
	if ir.Model == "" {
		return "", nil, fmt.Errorf("语音合成请求缺少 model")
	}
	// 契约 §5.1 裁决：gemini TTS 仅支持 pcm 输出，其余格式 400（零转码）。
	// 空 response_format 是 OpenAI 默认 mp3 语义，同样不静默降级。
	format := ir.ResponseFormat
	if format == "" {
		format = "mp3"
	}
	supported := false
	for _, candidate := range geminiSpeechOutputFormats {
		if candidate == format {
			supported = true
			break
		}
	}
	if !supported {
		return "", nil, fmt.Errorf("该模型仅支持 response_format=pcm（裸 PCM 24kHz/16bit/mono），不支持 %s，网关不做服务端格式转换", format)
	}
	if ir.Voice == "" {
		return "", nil, fmt.Errorf("语音合成请求缺少 voice")
	}
	body := geminiSpeechRequestBody{
		Contents: []geminiSpeechContent{
			{Parts: []geminiSpeechPart{{Text: ir.Input}}},
		},
		GenerationConfig: geminiSpeechGenerationConfig{
			ResponseModalities: []string{"AUDIO"},
			SpeechConfig: geminiSpeechSpeechConfig{
				PrebuiltVoiceConfig: geminiSpeechPrebuiltVoice{VoiceName: ir.Voice},
			},
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", nil, fmt.Errorf("编码 Gemini TTS 请求体失败: %w", err)
	}
	return "/v1beta/models/" + ir.Model + ":generateContent", encoded, nil
}

// geminiSpeechResponseBody 是 §5.1 响应的最小消费面（inlineData 音频 + 可选
// error）。
type geminiSpeechResponseBody struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				InlineData *struct {
					MimeType string `json:"mimeType"`
					Data     string `json:"data"`
				} `json:"inlineData"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// TransformResponse 解码 inlineData base64 PCM。contentType 优先上游
// mimeType（gemini 事实为 audio/L16;rate=24000），缺失回落固定 PCM 形态。
// request 参数不被消费（gemini mimeType 是响应内事实，契约 §5.1；M3 接口
// 扩展供 minimax 按请求 format 推导 content-type）。
func (a geminiSpeechAdapter) TransformResponse(upstreamBody []byte, _ SpeechRequest) ([]byte, string, error) {
	var decoded geminiSpeechResponseBody
	if err := json.Unmarshal(upstreamBody, &decoded); err != nil {
		return nil, "", fmt.Errorf("Gemini TTS 响应不是有效 JSON: %w", err)
	}
	if decoded.Error != nil && decoded.Error.Message != "" {
		return nil, "", fmt.Errorf("Gemini TTS 上游错误: %s", decoded.Error.Message)
	}
	for _, candidate := range decoded.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.InlineData == nil || part.InlineData.Data == "" {
				continue
			}
			audio, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
			if err != nil {
				return nil, "", fmt.Errorf("Gemini TTS inlineData base64 解码失败: %w", err)
			}
			contentType := strings.TrimSpace(part.InlineData.MimeType)
			if contentType == "" {
				contentType = GeminiSpeechOutputContentType
			}
			return audio, contentType, nil
		}
	}
	return nil, "", fmt.Errorf("Gemini TTS 响应缺少 inlineData 音频载荷")
}
