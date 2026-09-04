package gatewaypreauth

import (
	"errors"
	"net/http"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayanthropic"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
)

// Port of the consumed surface of protocols/registry.ts: the client error
// protocol resolution and the native request predicate. The openai driver
// owns the default /v1 path space; the anthropic and gemini drivers own
// their native request shapes.

// ProtocolCodeOpenAI mirrors OPENAI_PROTOCOL_CODE.
const ProtocolCodeOpenAI = gatewayopenai.ProtocolCode

// ProtocolCodeGemini mirrors GEMINI_PROTOCOL_CODE.
const ProtocolCodeGemini = gatewaygemini.ProtocolCode

// errGatewayProtocolDriverMissing mirrors the thrown
// '未配置网关协议驱动：missing_profile' when no driver matches the request.
var errGatewayProtocolDriverMissing = errors.New("未配置网关协议驱动：missing_profile")

// IsOpenAIProtocolRequestPath mirrors isOpenAIProtocolRequestPath(endpoint)
// via the gatewayopenai path classifier.
func IsOpenAIProtocolRequestPath(originalPathAndQuery string) bool {
	return gatewayopenai.IsProtocolRequestPath(originalPathAndQuery)
}

// gatewayProtocolDriverForRequest mirrors gatewayProtocolDriverForRequest:
// the openai driver wins the openai path space; otherwise the first driver
// whose native predicate matches. ok=false mirrors the undefined result.
func gatewayProtocolDriverForRequest(req *GatewayRequest) (GatewayErrorProtocol, bool) {
	endpoint := req.PathAndQuery()
	if IsOpenAIProtocolRequestPath(endpoint) {
		return GatewayErrorProtocolOpenAI, true
	}
	if req.HTTP != nil && gatewayanthropic.IsNativeRequest(req.HTTP) {
		return GatewayErrorProtocolAnthropic, true
	}
	if req.HTTP != nil && gatewaygemini.IsNativeRequest(req.HTTP) {
		return GatewayErrorProtocolGemini, true
	}
	return "", false
}

// GatewayProtocolClientErrorProtocolForRequest mirrors
// gatewayProtocolClientErrorProtocolForRequest(req) without a profile: the
// request-matched driver's client error protocol; unknown requests mirror
// the Node throw.
func GatewayProtocolClientErrorProtocolForRequest(req *GatewayRequest) (GatewayErrorProtocol, error) {
	protocol, ok := gatewayProtocolDriverForRequest(req)
	if !ok {
		return "", errGatewayProtocolDriverMissing
	}
	return protocol, nil
}

// IsGatewayProtocolNativeRequest mirrors isGatewayProtocolNativeRequest: on
// the openai path space only the openai protocol code matches; otherwise the
// anthropic / gemini native predicates decide.
func IsGatewayProtocolNativeRequest(req *GatewayRequest, protocolCode string) bool {
	if IsOpenAIProtocolRequestPath(req.PathAndQuery()) {
		return protocolCode == ProtocolCodeOpenAI
	}
	if protocolCode == gatewayanthropic.ProtocolCode && req.HTTP != nil {
		return gatewayanthropic.IsNativeRequest(req.HTTP)
	}
	if protocolCode == gatewaygemini.ProtocolCode && req.HTTP != nil {
		return gatewaygemini.IsNativeRequest(req.HTTP)
	}
	return false
}

// IsOpenAIModelsRequest mirrors isOpenAIModelsRequest.
func IsOpenAIModelsRequest(req *GatewayRequest) bool {
	return gatewayopenai.IsModelsRequest(req.MethodUpper(), req.PathAndQuery())
}

// IsAnthropicModelsRequest mirrors isAnthropicModelsRequest.
func IsAnthropicModelsRequest(req *GatewayRequest) bool {
	return req.HTTP != nil && gatewayanthropic.IsModelsRequest(req.HTTP)
}

// IsGeminiModelsRequest mirrors isGeminiModelsRequest.
func IsGeminiModelsRequest(req *GatewayRequest) bool {
	return req.HTTP != nil && gatewaygemini.IsModelsRequest(req.HTTP)
}

// IsGatewayModelsRequest mirrors the pre-auth private isGatewayModelsRequest.
func IsGatewayModelsRequest(req *GatewayRequest) bool {
	return IsOpenAIModelsRequest(req) || IsAnthropicModelsRequest(req) || IsGeminiModelsRequest(req)
}

// rawHTTPRequest returns the underlying *http.Request (nil-safe).
func rawHTTPRequest(req *GatewayRequest) *http.Request {
	if req == nil {
		return nil
	}
	return req.HTTP
}
