// transcriptionir.go 是同步语音转写（对外 POST /v1/audio/transcriptions）
// 的出站 adapter 端口。与 SpeechAdapter（TTS 二进制音频）分开建模：转写
// 的上游响应是 JSON/文本，下游也期待 JSON/文本，不能复用音频字节转换接口。
// 首个实现是火山 openspeech 大模型录音文件识别极速版
// （volcengine_asr_sync.go）；后续厂商只新增注册表条目，不进组合根 switch。
package gatewaymedia

import "net/http"

// TranscriptionRequest 是同步语音转写请求的规范输入。Audio 为已缓冲的
// multipart file part 原始字节（零拷贝切片，调用方保证生命周期）；网关
// 零存储，不在磁盘暂存上传音频。
type TranscriptionRequest struct {
	Model          string
	Audio          []byte
	AudioFormat    string
	Filename       string
	ResponseFormat string
	Language       string
	Prompt         string
	Temperature    *float64
}

// TranscriptionAdapter 是同步语音转写出站 adapter 端口。
type TranscriptionAdapter interface {
	// Provider 返回注册键（provider_code 归一小写）。
	Provider() string
	// BuildRequest 把规范输入映射为上游请求（路径 + JSON body + 厂商私有
	// 头）。错误为请求侧 400 语义（能力边界裁决：不支持的音频格式/
	// response_format 显式拒绝，不猜测）。
	BuildRequest(ir TranscriptionRequest) (path string, body []byte, extraHeaders map[string]string, err error)
	// TransformResponse 把上游成功响应转换为 OpenAI 兼容的转写响应
	// （json/text/verbose_json/srt/vtt）；statusCode/header 供头驱动厂商
	// 校验业务状态。错误为上游协议失败语义。
	TransformResponse(statusCode int, header http.Header, upstreamBody []byte, ir TranscriptionRequest) (body []byte, contentType string, err error)
}

// transcriptionAdapters 是 provider → adapter 注册表。
var transcriptionAdapters = map[string]TranscriptionAdapter{
	"volcengine": volcengineTranscriptionAdapter{},
}

// TranscriptionAdapterForProvider 按 provider_code 解析同步转写 adapter；
// nil 表示该 provider 无同步转写 adapter（openai 族直连透传，不进 adapter）。
func TranscriptionAdapterForProvider(providerCode string) TranscriptionAdapter {
	return transcriptionAdapters[normalizeProviderKey(providerCode)]
}
