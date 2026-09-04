package gatewayresponse

import (
	"net/http"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayanthropic"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// ResponseDriverPort 是 finalization 消费的协议能力视图（G02-G04 装配面）。
// 每个协议一个适配器；本包不修改协议包。
type ResponseDriverPort interface {
	// ResponseProtocol 返回 responseProtocol（'openai_v1' 等）。
	ResponseProtocol() string
	// ClientErrorProtocol 返回 clientErrorProtocol（'openai' | 'anthropic' | 'gemini'）。
	ClientErrorProtocol() string
	// DefaultClientProfile 返回 defaultClientProfile。
	DefaultClientProfile() string
	// EndpointFamilyForPath 按 originalPathAndQuery 解析 endpoint family。
	EndpointFamilyForPath(pathAndQuery string) gatewayproto.ResponseEndpointFamily
	// ExtractJSONSemanticFrames 对齐 extractGatewayProtocolJsonSemanticFramesForRequest。
	ExtractJSONSemanticFrames(value any, pathAndQuery string) []gatewayproto.SemanticFrame
	// Usage 提取对齐 parseGatewayProtocolUsageFromJson*。
	ExtractUsageFromJSONValue(value any) gatewayproto.ParsedUsage
	ExtractUsageFromJSONTextFragment(text string, skipFullDocumentParse bool) gatewayproto.ParsedUsage
	ExtractUsageFromJSONBuffer(body []byte) gatewayproto.ParsedUsage
	// 错误负载对齐 parseGatewayProtocolErrorPayload*。
	ParseErrorPayload(bodyText string, header http.Header) gatewayproto.ErrorPayload
	ParseErrorPayloadFromJSONValue(value any) gatewayproto.ErrorPayload
	// NewStreamInspector 提供流检查器。
	NewStreamInspector() gatewayproto.StreamInspector
	// StreamDriver 返回流管道视图。
	StreamDriver() StreamDriver
}

// ---- openai 适配 ----

// OpenAIResponseDriver 适配 gatewayopenai（G02）。
type OpenAIResponseDriver struct{}

// NewOpenAIResponseDriver 构造。
func NewOpenAIResponseDriver() *OpenAIResponseDriver { return &OpenAIResponseDriver{} }

func (d *OpenAIResponseDriver) ResponseProtocol() string     { return "openai_v1" }
func (d *OpenAIResponseDriver) ClientErrorProtocol() string  { return "openai" }
func (d *OpenAIResponseDriver) DefaultClientProfile() string { return "generic" }

// EndpointFamilyForPath 对齐 openAIResponseEndpointFamilyFromRequest。
func (d *OpenAIResponseDriver) EndpointFamilyForPath(pathAndQuery string) gatewayproto.ResponseEndpointFamily {
	return openAIResponseEndpointFamily(pathAndQuery)
}

func (d *OpenAIResponseDriver) ExtractJSONSemanticFrames(value any, pathAndQuery string) []gatewayproto.SemanticFrame {
	return gatewayopenai.ExtractJSONSemanticFrames(value, d.EndpointFamilyForPath(pathAndQuery))
}

func (d *OpenAIResponseDriver) ExtractUsageFromJSONValue(value any) gatewayproto.ParsedUsage {
	return gatewayopenai.ParseUsageFromJSONValue(value)
}

func (d *OpenAIResponseDriver) ExtractUsageFromJSONTextFragment(text string, skipFullDocumentParse bool) gatewayproto.ParsedUsage {
	return gatewayopenai.ParseUsageFromJSONTextFragment(text)
}

func (d *OpenAIResponseDriver) ExtractUsageFromJSONBuffer(body []byte) gatewayproto.ParsedUsage {
	return gatewayopenai.ParseUsageFromJSONBuffer(body)
}

func (d *OpenAIResponseDriver) ParseErrorPayload(bodyText string, header http.Header) gatewayproto.ErrorPayload {
	return gatewayopenai.ParseErrorPayload(bodyText, header)
}

func (d *OpenAIResponseDriver) ParseErrorPayloadFromJSONValue(value any) gatewayproto.ErrorPayload {
	return gatewayopenai.ParseErrorPayloadFromJSONValue(value)
}

func (d *OpenAIResponseDriver) NewStreamInspector() gatewayproto.StreamInspector {
	return gatewayopenai.NewStreamInspector()
}

func (d *OpenAIResponseDriver) StreamDriver() StreamDriver { return DefaultOpenAIStreamDriver() }

// openAIResponseEndpointFamily 对齐 gatewayopenai 的 containment 映射。
func openAIResponseEndpointFamily(pathAndQuery string) gatewayproto.ResponseEndpointFamily {
	path := lowerASCII(pathAndQuery)
	switch {
	case contains(path, "/chat/completions"):
		return gatewayproto.EndpointFamilyChatCompletions
	case contains(path, "/responses"):
		return gatewayproto.EndpointFamilyResponses
	default:
		return gatewayproto.EndpointFamilyUnknown
	}
}

// ---- anthropic 适配 ----

// AnthropicResponseDriver 适配 gatewayanthropic（G03）。
type AnthropicResponseDriver struct{}

// NewAnthropicResponseDriver 构造。
func NewAnthropicResponseDriver() *AnthropicResponseDriver { return &AnthropicResponseDriver{} }

func (d *AnthropicResponseDriver) ResponseProtocol() string     { return "anthropic_v1" }
func (d *AnthropicResponseDriver) ClientErrorProtocol() string  { return gatewayanthropic.ClientErrorProtocol }
func (d *AnthropicResponseDriver) DefaultClientProfile() string { return gatewayanthropic.DefaultClientProfile }

func (d *AnthropicResponseDriver) EndpointFamilyForPath(pathAndQuery string) gatewayproto.ResponseEndpointFamily {
	return gatewayproto.ResponseEndpointFamily(gatewayanthropic.ResponseEndpointFamilyFromPath(pathAndQuery))
}

func (d *AnthropicResponseDriver) ExtractJSONSemanticFrames(value any, pathAndQuery string) []gatewayproto.SemanticFrame {
	return convertAnthropicFrames(gatewayanthropic.ExtractJSONSemanticFrames(value, gatewayanthropic.ResponseEndpointFamilyFromPath(pathAndQuery)))
}

func (d *AnthropicResponseDriver) ExtractUsageFromJSONValue(value any) gatewayproto.ParsedUsage {
	return anthropicUsageValueToProto(gatewayanthropic.ParseUsageFromJSONValue(value))
}

func (d *AnthropicResponseDriver) ExtractUsageFromJSONTextFragment(text string, skipFullDocumentParse bool) gatewayproto.ParsedUsage {
	return anthropicUsageValueToProto(gatewayanthropic.ParseUsageFromJSONTextFragment(text))
}

func (d *AnthropicResponseDriver) ExtractUsageFromJSONBuffer(body []byte) gatewayproto.ParsedUsage {
	return anthropicUsageValueToProto(gatewayanthropic.ParseUsageFromJSONBuffer(body))
}

func (d *AnthropicResponseDriver) ParseErrorPayload(bodyText string, header http.Header) gatewayproto.ErrorPayload {
	return anthropicErrorPayloadToProto(gatewayanthropic.ParseErrorPayload(bodyText, header))
}

func (d *AnthropicResponseDriver) ParseErrorPayloadFromJSONValue(value any) gatewayproto.ErrorPayload {
	return anthropicErrorPayloadToProto(gatewayanthropic.ParseErrorPayloadFromJSONValue(value))
}

func (d *AnthropicResponseDriver) NewStreamInspector() gatewayproto.StreamInspector {
	return anthropicInspectorAdapter{inner: gatewayanthropic.NewStreamInspector()}
}

func (d *AnthropicResponseDriver) StreamDriver() StreamDriver { return anthropicStreamDriver{} }

// ---- gemini 适配 ----

// GeminiResponseDriver 适配 gatewaygemini（G04）。
type GeminiResponseDriver struct{}

// NewGeminiResponseDriver 构造。
func NewGeminiResponseDriver() *GeminiResponseDriver { return &GeminiResponseDriver{} }

func (d *GeminiResponseDriver) ResponseProtocol() string     { return "gemini_v1beta" }
func (d *GeminiResponseDriver) ClientErrorProtocol() string  { return "gemini" }
func (d *GeminiResponseDriver) DefaultClientProfile() string { return "generic_gemini" }

func (d *GeminiResponseDriver) EndpointFamilyForPath(pathAndQuery string) gatewayproto.ResponseEndpointFamily {
	return gatewayproto.ResponseEndpointFamily(gatewaygemini.ResponseEndpointFamilyFromPath(pathAndQuery))
}

func (d *GeminiResponseDriver) ExtractJSONSemanticFrames(value any, pathAndQuery string) []gatewayproto.SemanticFrame {
	return convertGeminiFrames(gatewaygemini.ExtractJSONSemanticFrames(value, gatewaygemini.ResponseEndpointFamilyFromPath(pathAndQuery)))
}

func (d *GeminiResponseDriver) ExtractUsageFromJSONValue(value any) gatewayproto.ParsedUsage {
	return geminiUsageValueToProto(gatewaygemini.ParseUsageFromJSONValue(value))
}

func (d *GeminiResponseDriver) ExtractUsageFromJSONTextFragment(text string, skipFullDocumentParse bool) gatewayproto.ParsedUsage {
	return geminiUsageValueToProto(gatewaygemini.ParseUsageFromJSONTextFragment(text, skipFullDocumentParse))
}

func (d *GeminiResponseDriver) ExtractUsageFromJSONBuffer(body []byte) gatewayproto.ParsedUsage {
	return geminiUsageValueToProto(gatewaygemini.ParseUsageFromJSONBuffer(body))
}

func (d *GeminiResponseDriver) ParseErrorPayload(bodyText string, header http.Header) gatewayproto.ErrorPayload {
	return parseGeminiErrorPayload(bodyText, header)
}

func (d *GeminiResponseDriver) ParseErrorPayloadFromJSONValue(value any) gatewayproto.ErrorPayload {
	return parseGeminiErrorPayloadFromValue(value)
}

func (d *GeminiResponseDriver) NewStreamInspector() gatewayproto.StreamInspector {
	return geminiInspectorAdapter{inner: gatewaygemini.NewStreamInspector()}
}

func (d *GeminiResponseDriver) StreamDriver() StreamDriver { return geminiStreamDriver{} }

// ---- inspector 适配（剥掉包内返回值以满足窄接口） ----

type anthropicInspectorAdapter struct {
	inner *gatewayanthropic.StreamInspector
}

func (a anthropicInspectorAdapter) PushChunk(chunk []byte) { a.inner.PushChunk(chunk) }
func (a anthropicInspectorAdapter) PushText(text string)   { a.inner.PushText(text) }
func (a anthropicInspectorAdapter) Finish() gatewayproto.StreamInspection {
	return convertAnthropicInspection(a.inner.Finish())
}
func (a anthropicInspectorAdapter) Snapshot() gatewayproto.StreamInspection {
	return convertAnthropicInspection(a.inner.Snapshot())
}
func (a anthropicInspectorAdapter) DrainEventSummariesCanEndStream() bool {
	return a.inner.DrainEventSummariesCanEndStream()
}

type geminiInspectorAdapter struct {
	inner *gatewaygemini.StreamInspector
}

func (g geminiInspectorAdapter) PushChunk(chunk []byte) { g.inner.PushChunk(chunk) }
func (g geminiInspectorAdapter) PushText(text string)   { g.inner.PushText(text) }
func (g geminiInspectorAdapter) Finish() gatewayproto.StreamInspection {
	return convertGeminiInspection(g.inner.Finish())
}
func (g geminiInspectorAdapter) Snapshot() gatewayproto.StreamInspection {
	return convertGeminiInspection(g.inner.Snapshot())
}
func (g geminiInspectorAdapter) DrainEventSummariesCanEndStream() bool {
	return g.inner.DrainEventSummariesCanEndStream()
}

// ---- stream driver 适配 ----

type anthropicStreamDriver struct{}

func (anthropicStreamDriver) ClientErrorProtocol() string { return gatewayanthropic.ClientErrorProtocol }
func (anthropicStreamDriver) NewStreamInspector() gatewayproto.StreamInspector {
	return anthropicInspectorAdapter{inner: gatewayanthropic.NewStreamInspector()}
}
func (anthropicStreamDriver) ResponseInspectionEndpointFamily(family gatewayproto.ResponseEndpointFamily) gatewayproto.ResponseEndpointFamily {
	return gatewayproto.ResponseEndpointFamily(gatewayanthropic.AnthropicV1Driver.ResponseInspectionEndpointFamily(string(family)))
}
func (anthropicStreamDriver) SSEResponseInspectionFailureEvent() string {
	return gatewayanthropic.AnthropicV1Driver.SSEResponseInspectionFailureEvent()
}
func (anthropicStreamDriver) DrainForKeepAliveAfterTerminal() bool {
	return gatewayanthropic.AnthropicV1Driver.DrainForKeepAliveAfterTerminal()
}

type geminiStreamDriver struct{}

func (geminiStreamDriver) ClientErrorProtocol() string { return "gemini" }
func (geminiStreamDriver) NewStreamInspector() gatewayproto.StreamInspector {
	return geminiInspectorAdapter{inner: gatewaygemini.NewStreamInspector()}
}

// ResponseInspectionEndpointFamily：G04 未冻结该映射；识别的 Gemini family
// 原样保留，未知回退 generate_content（对齐 anthropic 的 messages 回退策略）。
func (geminiStreamDriver) ResponseInspectionEndpointFamily(family gatewayproto.ResponseEndpointFamily) gatewayproto.ResponseEndpointFamily {
	switch family {
	case gatewayproto.EndpointFamilyGenerateContent,
		gatewayproto.EndpointFamilyStreamGenerateContent,
		gatewayproto.EndpointFamilyCountTokens,
		gatewayproto.EndpointFamilyEmbedContent,
		gatewayproto.EndpointFamilyInteractions,
		gatewayproto.EndpointFamilyModels:
		return family
	default:
		return gatewayproto.EndpointFamilyGenerateContent
	}
}

// SSEResponseInspectionFailureEvent：G04 未冻结；保守取 'none'（检查拦截不
// 构建协议失败事件，失败经补发终态事件承载）。
func (geminiStreamDriver) SSEResponseInspectionFailureEvent() string { return "none" }

func (geminiStreamDriver) DrainForKeepAliveAfterTerminal() bool { return false }

// ResponseDriverForProtocol 按响应协议返回驱动视图。
func ResponseDriverForProtocol(protocol string) ResponseDriverPort {
	switch protocol {
	case "anthropic_v1":
		return NewAnthropicResponseDriver()
	case "gemini_v1beta":
		return NewGeminiResponseDriver()
	default:
		return NewOpenAIResponseDriver()
	}
}

// gatewayErrorProtocolOf 把驱动错误协议映射为 G05 的协议枚举。
func gatewayErrorProtocolOf(protocol string) gatewaypreauth.GatewayErrorProtocol {
	return gatewaypreauth.GatewayErrorProtocol(protocol)
}
