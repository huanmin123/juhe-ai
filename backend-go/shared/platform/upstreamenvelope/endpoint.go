// Package upstreamenvelope 是系统探针的上游 HTTP 请求信封唯一构造源。
// 契约权威：docs/architecture/上游请求信封统一设计.md（§3 契约、§4.1–4.4、
// §4.2 变体矩阵）。账户测试、模型检测、健康/恢复探针（F2–F5）对相同的已解析
// 目标、协议、endpointMode 与 body 编码必须经本包 Build 生成最终
// *http.Request；调用方不得另行拼接路径、标准头、认证头或协议 envelope 字段
// （§3.1 共享构造规则）。本包只负责请求构造（method/URL/header/body 编码），
// 不持有凭据，不承担重试、评分、结算、超时、账户选择或网络发送职责（§4.1）。
//
// M1 交付范围：endpoint catalog、认证策略、预算策略、统一入口与契约测试；
// F2–F5 探针包的请求构造迁移在 M2–M4，Codex / Gemini Code Assist 的包装
// 钩子在 M5 接入（§6 迁移步骤）。
//
// catalog 条目数据取证自三个探针包现状：accountprobe 的 buildTestRequest /
// buildUpstreamURL / executeAttempt，exactkeyprobe 的 buildProbeRequest /
// joinBaseURL，modelcheckprobe 的 BuildBasic / buildURL。
package upstreamenvelope

import (
	"fmt"
	"net/http"
)

// Protocol 是上游协议族（语义对齐 accountprobe 的 DiagnosticProtocol 分层）。
type Protocol string

const (
	ProtocolOpenAI    Protocol = "openai"
	ProtocolAnthropic Protocol = "anthropic"
	ProtocolGemini    Protocol = "gemini"
)

// EndpointMode 与三个探针包现役的 endpointMode 命名一致
// （accountprobe.EndpointMode、exactkeyprobe Input.EndpointMode、
// modelcheckprofile 的 EndpointModeForProtocol 产物）。
type EndpointMode string

const (
	ModeChatJSON               EndpointMode = "chat_json"
	ModeChatSSE                EndpointMode = "chat_sse"
	ModeResponsesJSON          EndpointMode = "responses_json"
	ModeResponsesSSE           EndpointMode = "responses_sse"
	ModeMessagesJSON           EndpointMode = "messages_json"
	ModeMessagesSSE            EndpointMode = "messages_sse"
	ModeGenerateContentJSON    EndpointMode = "generate_content_json"
	ModeGenerateContentSSE     EndpointMode = "generate_content_sse"
	ModeInteractionsJSON       EndpointMode = "interactions_json"
	ModeInteractionsSSE        EndpointMode = "interactions_sse"
	ModeImagesJSON             EndpointMode = "images_json"
	ModeAudioSpeech            EndpointMode = "audio_speech"
	ModeAudioTranscriptionJSON EndpointMode = "audio_transcription_json"
)

// BodyEncoding 是请求体的线格式。SSE 端点的请求体仍是 JSON，流式语义由
// 调用方 body 内的 stream 字段承载（§3.2 探针内容独立）；信封只负责
// Accept 形态与编码分类。
type BodyEncoding string

const (
	EncodingJSON      BodyEncoding = "json"
	EncodingSSE       BodyEncoding = "sse"
	EncodingMultipart BodyEncoding = "multipart"
)

// BudgetField 是 catalog 条目登记的预算字段位置（§4.3：预算字段名由协议
// 与端点决定，不由调用方随意选择）。
type BudgetField string

const (
	// BudgetFieldNone：端点契约没有输出预算字段（images/audio/interactions）。
	// 预算策略必须为 BudgetNone，不得伪造字段兜底（§4.3）。
	BudgetFieldNone BudgetField = "none"
	// BudgetFieldMaxTokens：Chat Completions 与 Anthropic Messages
	// （Anthropic 将 max_tokens 作为协议必填字段，见 Endpoint.BudgetRequired）。
	BudgetFieldMaxTokens BudgetField = "max_tokens"
	// BudgetFieldMaxOutputTokens：OpenAI Responses。
	BudgetFieldMaxOutputTokens BudgetField = "max_output_tokens"
	// BudgetFieldGeminiGenerationConfig：Gemini generationConfig.maxOutputTokens。
	BudgetFieldGeminiGenerationConfig BudgetField = "generationConfig.maxOutputTokens"
)

// Endpoint 是 endpoint catalog 的一条登记（§4.2：实现前必须从当前
// accountprobe / exactkeyprobe / modelcheckprobe 调用点形成完整 catalog）。
type Endpoint struct {
	Protocol Protocol
	Mode     EndpointMode
	Method   string
	// PathTemplate 是相对请求路径（可含 query）。{model} 占位符仅 gemini
	// generateContent 使用，替换值为 geminiModelPath（含 "models/" 前缀，
	// 见 envelope.go）。
	PathTemplate string
	Encoding     BodyEncoding
	BudgetField  BudgetField
	// BudgetRequired 标记预算字段是协议必填（Anthropic max_tokens）：
	// BudgetNone 策略对这类端点必须报错，不得静默生成缺字段的非法请求。
	BudgetRequired bool
	// Auth 是该端点的默认认证策略。oauth / google_oauth / GLM anthropic
	// 等账户形态变体由调用方按账户解析改选（§1：认证字段随适配器变化，
	// 凭据选择仍属于账户/路由解析）。
	Auth AuthStrategy
	// Headers 是端点固定特殊头，Build 时无条件写入。
	Headers map[string]string
}

// catalog 是端点登记表（§8.1：新增系统探针端点前先登记本表、契约测试与
// Mock，不得在调用方另建最终 HTTP 请求构造器）。
var catalog = map[EndpointMode]Endpoint{
	// OpenAI Chat Completions（accountprobe buildOpenAIChatCompletionsPayload、
	// exactkeyprobe chat_json/chat_sse、modelcheckprobe ProtocolOpenAIChat）。
	ModeChatJSON: {
		Protocol:     ProtocolOpenAI,
		Mode:         ModeChatJSON,
		Method:       http.MethodPost,
		PathTemplate: "/v1/chat/completions",
		Encoding:     EncodingJSON,
		BudgetField:  BudgetFieldMaxTokens,
		Auth:         AuthBearer,
	},
	ModeChatSSE: {
		Protocol:     ProtocolOpenAI,
		Mode:         ModeChatSSE,
		Method:       http.MethodPost,
		PathTemplate: "/v1/chat/completions",
		Encoding:     EncodingSSE,
		BudgetField:  BudgetFieldMaxTokens,
		Auth:         AuthBearer,
	},
	// OpenAI Responses（accountprobe buildOpenAIResponsesPayload、
	// exactkeyprobe responses_*、modelcheckprobe ProtocolOpenAIResponses）。
	ModeResponsesJSON: {
		Protocol:     ProtocolOpenAI,
		Mode:         ModeResponsesJSON,
		Method:       http.MethodPost,
		PathTemplate: "/v1/responses",
		Encoding:     EncodingJSON,
		BudgetField:  BudgetFieldMaxOutputTokens,
		Auth:         AuthBearer,
	},
	ModeResponsesSSE: {
		Protocol:     ProtocolOpenAI,
		Mode:         ModeResponsesSSE,
		Method:       http.MethodPost,
		PathTemplate: "/v1/responses",
		Encoding:     EncodingSSE,
		BudgetField:  BudgetFieldMaxOutputTokens,
		Auth:         AuthBearer,
	},
	// Anthropic Messages（BUG-0201 后的 1024 轻量探针契约，无 thinking/effort；
	// anthropic-version 与 exactkeyprobe/accountprobe 现状一致）。
	ModeMessagesJSON: {
		Protocol:       ProtocolAnthropic,
		Mode:           ModeMessagesJSON,
		Method:         http.MethodPost,
		PathTemplate:   "/v1/messages",
		Encoding:       EncodingJSON,
		BudgetField:    BudgetFieldMaxTokens,
		BudgetRequired: true,
		Auth:           AuthAnthropicAPIKey,
		Headers:        map[string]string{"anthropic-version": "2023-06-01"},
	},
	ModeMessagesSSE: {
		Protocol:       ProtocolAnthropic,
		Mode:           ModeMessagesSSE,
		Method:         http.MethodPost,
		PathTemplate:   "/v1/messages",
		Encoding:       EncodingSSE,
		BudgetField:    BudgetFieldMaxTokens,
		BudgetRequired: true,
		Auth:           AuthAnthropicAPIKey,
		Headers:        map[string]string{"anthropic-version": "2023-06-01"},
	},
	// Gemini GenerateContent（路径模板的 {model} 由 geminiModelPath 替换；
	// SSE 形态是 :streamGenerateContent?alt=sse）。
	ModeGenerateContentJSON: {
		Protocol:     ProtocolGemini,
		Mode:         ModeGenerateContentJSON,
		Method:       http.MethodPost,
		PathTemplate: "/v1beta/{model}:generateContent",
		Encoding:     EncodingJSON,
		BudgetField:  BudgetFieldGeminiGenerationConfig,
		Auth:         AuthGeminiAPIKey,
	},
	ModeGenerateContentSSE: {
		Protocol:     ProtocolGemini,
		Mode:         ModeGenerateContentSSE,
		Method:       http.MethodPost,
		PathTemplate: "/v1beta/{model}:streamGenerateContent?alt=sse",
		Encoding:     EncodingSSE,
		BudgetField:  BudgetFieldGeminiGenerationConfig,
		Auth:         AuthGeminiAPIKey,
	},
	// Gemini Interactions（§1：独立 endpoint/body 语义，只有契约支持时才
	// 应用输出预算——当前探针 body 不携带预算字段，登记为 none）。
	// api-revision 头取证自 exactkeyprobe；accountprobe 现状未发送，M2/M4
	// 迁移时以 catalog 口径收敛。
	ModeInteractionsJSON: {
		Protocol:     ProtocolGemini,
		Mode:         ModeInteractionsJSON,
		Method:       http.MethodPost,
		PathTemplate: "/v1beta/interactions",
		Encoding:     EncodingJSON,
		BudgetField:  BudgetFieldNone,
		Auth:         AuthGeminiAPIKey,
		Headers:      map[string]string{"api-revision": "2026-05-20"},
	},
	ModeInteractionsSSE: {
		Protocol:     ProtocolGemini,
		Mode:         ModeInteractionsSSE,
		Method:       http.MethodPost,
		PathTemplate: "/v1beta/interactions",
		Encoding:     EncodingSSE,
		BudgetField:  BudgetFieldNone,
		Auth:         AuthGeminiAPIKey,
		Headers:      map[string]string{"api-revision": "2026-05-20"},
	},
	// 图片生成（§1：不是图片输入；按端点自身请求字段构造，不套用文本输出
	// 预算）。
	ModeImagesJSON: {
		Protocol:     ProtocolOpenAI,
		Mode:         ModeImagesJSON,
		Method:       http.MethodPost,
		PathTemplate: "/v1/images/generations",
		Encoding:     EncodingJSON,
		BudgetField:  BudgetFieldNone,
		Auth:         AuthBearer,
	},
	// 音频生成与转录（§1：转录为 multipart/form-data；gemini 协议的
	// audio_speech 探针形态经协议内 generateContent 承载，属于调用方模式
	// 解析，本条目是 OpenAI 形态端点）。
	ModeAudioSpeech: {
		Protocol:     ProtocolOpenAI,
		Mode:         ModeAudioSpeech,
		Method:       http.MethodPost,
		PathTemplate: "/v1/audio/speech",
		Encoding:     EncodingJSON,
		BudgetField:  BudgetFieldNone,
		Auth:         AuthBearer,
	},
	ModeAudioTranscriptionJSON: {
		Protocol:     ProtocolOpenAI,
		Mode:         ModeAudioTranscriptionJSON,
		Method:       http.MethodPost,
		PathTemplate: "/v1/audio/transcriptions",
		Encoding:     EncodingMultipart,
		BudgetField:  BudgetFieldNone,
		Auth:         AuthBearer,
	},
}

// catalogOrder 固定 Entries 的返回顺序（契约测试的覆盖基准）。
var catalogOrder = []EndpointMode{
	ModeChatJSON,
	ModeChatSSE,
	ModeResponsesJSON,
	ModeResponsesSSE,
	ModeMessagesJSON,
	ModeMessagesSSE,
	ModeGenerateContentJSON,
	ModeGenerateContentSSE,
	ModeInteractionsJSON,
	ModeInteractionsSSE,
	ModeImagesJSON,
	ModeAudioSpeech,
	ModeAudioTranscriptionJSON,
}

// Lookup 返回 endpointMode 对应的登记条目。未登记的 endpointMode 必须返回
// 明确错误，禁止静默兜底（§4.2：对不支持组合断言明确失败）。
func Lookup(mode EndpointMode) (Endpoint, error) {
	entry, ok := catalog[mode]
	if !ok {
		return Endpoint{}, fmt.Errorf("upstreamenvelope: endpointMode %q 未在 endpoint catalog 登记（新增系统探针端点前先登记 catalog，设计文档 §8.1）", mode)
	}
	if entry.Headers != nil {
		// 返回副本，避免调用方改动共享登记表。
		entry.Headers = cloneStrings(entry.Headers)
	}
	return entry, nil
}

// Entries 按固定顺序返回全部登记条目（契约测试的覆盖基准）。
func Entries() []Endpoint {
	out := make([]Endpoint, 0, len(catalogOrder))
	for _, mode := range catalogOrder {
		entry, err := Lookup(mode)
		if err != nil {
			// catalogOrder 与 catalog 同文件维护，缺项属编程错误。
			panic(fmt.Sprintf("upstreamenvelope: catalogOrder 条目 %q 缺少登记: %v", mode, err))
		}
		out = append(out, entry)
	}
	return out
}

func cloneStrings(value map[string]string) map[string]string {
	out := make(map[string]string, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}
