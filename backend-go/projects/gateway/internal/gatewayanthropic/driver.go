package gatewayanthropic

import (
	"net/http"
)

// 默认客户端画像（对齐 defaultClientProfile 'generic_anthropic'）。
const DefaultClientProfile = "generic_anthropic"

// 客户端错误协议（对齐 clientErrorProtocol 'anthropic'）。
const ClientErrorProtocol = "anthropic"

// DriverID 对齐 Node driver 的 id 'anthropic-v1'。
const DriverID = "anthropic-v1"

// Driver 是 GatewayProtocolDriver 在本包内的等价接口（对齐 Node
// protocols/_shared/types.ts）。G05/G16 编排层可基于本接口注册协议驱动；
// 各协议包自行定义等价接口，协议之间不互相 import。
type Driver interface {
	ID() string
	ProtocolCode() string
	ProtocolVersion() string
	ResponseProtocol() string
	ClientErrorProtocol() string
	DefaultClientProfile() string

	// SupportsProfile 对齐 supportsProfile。
	SupportsProfile(protocolCode, protocolVersion string) bool
	// EndpointModeForRequestShape 对齐 endpointModeForRequestShape。
	EndpointModeForRequestShape(endpoint string, stream bool) string
	// IsNativeRequest 对齐 isNativeRequest。
	IsNativeRequest(r *http.Request) bool
	// IsModelsRequest 对齐 isModelsRequest。
	IsModelsRequest(r *http.Request) bool
	// ResponseEndpointFamilyForRequest 对齐 responseEndpointFamilyForRequest。
	ResponseEndpointFamilyForRequest(r *http.Request) EndpointFamily
	// ExtractJSONSemanticFrames 对齐 extractJsonSemanticFrames。
	ExtractJSONSemanticFrames(value any, r *http.Request) []ResponseSemanticFrame
	// CreateStreamInspector 对齐 createStreamInspector。
	CreateStreamInspector() *StreamInspector
	// ResponseInspectionEndpointFamily 对齐 responseInspectionEndpointFamily。
	ResponseInspectionEndpointFamily(endpointFamily string) EndpointFamily
	// ExtractSSESemanticFrames 对齐 extractSseSemanticFrames。
	ExtractSSESemanticFrames(event StreamEvent, endpointFamily string) []ResponseSemanticFrame
	// SSEResponseInspectionFailureEvent 对齐 sseResponseInspectionFailureEvent
	//（Anthropic 为 'none'：流内没有显式失败事件名）。
	SSEResponseInspectionFailureEvent() string
	// DrainForKeepAliveAfterTerminal 对齐 drainForKeepAliveAfterTerminal。
	DrainForKeepAliveAfterTerminal() bool

	ParseUsageFromJSONBuffer(responseBody []byte) ParsedUsage
	ParseUsageFromJSONValue(value any) ParsedUsage
	ParseUsageFromJSONTextFragment(text string) ParsedUsage
	ParseErrorPayload(text string, header http.Header) ErrorPayload
	ParseErrorPayloadFromJSONValue(value any) ErrorPayload
	// ApplyStreamUsageFallback 对齐 applyStreamUsageFallback。
	ApplyStreamUsageFallback(facts RequestFacts, usage ParsedUsage, input StreamUsageFallbackInput) StreamUsageFallbackResult
}

// AnthropicV1Driver 是 Driver 的 Anthropic /v1 实现。
var AnthropicV1Driver Driver = anthropicV1Driver{}

type anthropicV1Driver struct{}

func (anthropicV1Driver) ID() string                   { return DriverID }
func (anthropicV1Driver) ProtocolCode() string         { return ProtocolCode }
func (anthropicV1Driver) ProtocolVersion() string      { return ProtocolVersion }
func (anthropicV1Driver) ResponseProtocol() string     { return ResponseProtocol }
func (anthropicV1Driver) ClientErrorProtocol() string  { return ClientErrorProtocol }
func (anthropicV1Driver) DefaultClientProfile() string { return DefaultClientProfile }
func (anthropicV1Driver) SupportsProfile(protocolCode, protocolVersion string) bool {
	return protocolCode == ProtocolCode && protocolVersion == ProtocolVersion
}

func (anthropicV1Driver) EndpointModeForRequestShape(endpoint string, stream bool) string {
	return EndpointModeForRequestShape(endpoint, stream)
}

func (anthropicV1Driver) IsNativeRequest(r *http.Request) bool { return IsNativeRequest(r) }
func (anthropicV1Driver) IsModelsRequest(r *http.Request) bool { return IsModelsRequest(r) }

func (anthropicV1Driver) ResponseEndpointFamilyForRequest(r *http.Request) EndpointFamily {
	return ResponseEndpointFamilyFromPath(RequestPathAndQuery(r))
}

func (anthropicV1Driver) ExtractJSONSemanticFrames(value any, r *http.Request) []ResponseSemanticFrame {
	return ExtractJSONSemanticFrames(value, ResponseEndpointFamilyFromPath(RequestPathAndQuery(r)))
}

func (anthropicV1Driver) CreateStreamInspector() *StreamInspector { return NewStreamInspector() }

// ResponseInspectionEndpointFamily 对齐 anthropicEndpointFamilyOrMessages：
// 无法识别时回退 messages。
func (anthropicV1Driver) ResponseInspectionEndpointFamily(endpointFamily string) EndpointFamily {
	switch EndpointFamily(endpointFamily) {
	case EndpointFamilyMessages, EndpointFamilyModels, EndpointFamilyMessageTokenCount:
		return EndpointFamily(endpointFamily)
	default:
		return EndpointFamilyMessages
	}
}

func (anthropicV1Driver) ExtractSSESemanticFrames(event StreamEvent, endpointFamily string) []ResponseSemanticFrame {
	return ExtractSSESemanticFrames(event, anthropicV1Driver{}.ResponseInspectionEndpointFamily(endpointFamily))
}

func (anthropicV1Driver) SSEResponseInspectionFailureEvent() string { return "none" }
func (anthropicV1Driver) DrainForKeepAliveAfterTerminal() bool      { return false }

func (anthropicV1Driver) ParseUsageFromJSONBuffer(responseBody []byte) ParsedUsage {
	return ParseUsageFromJSONBuffer(responseBody)
}

func (anthropicV1Driver) ParseUsageFromJSONValue(value any) ParsedUsage {
	return ParseUsageFromJSONValue(value)
}

func (anthropicV1Driver) ParseUsageFromJSONTextFragment(text string) ParsedUsage {
	return ParseUsageFromJSONTextFragment(text)
}

func (anthropicV1Driver) ParseErrorPayload(text string, header http.Header) ErrorPayload {
	return ParseErrorPayload(text, header)
}

func (anthropicV1Driver) ParseErrorPayloadFromJSONValue(value any) ErrorPayload {
	return ParseErrorPayloadFromJSONValue(value)
}

func (anthropicV1Driver) ApplyStreamUsageFallback(facts RequestFacts, usage ParsedUsage, input StreamUsageFallbackInput) StreamUsageFallbackResult {
	return ApplyStreamUsageFallback(facts, usage, input)
}
