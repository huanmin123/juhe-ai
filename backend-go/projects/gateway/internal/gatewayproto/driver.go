package gatewayproto

import (
	"net/http"
)

// EndpointMode mirrors AccountSupportedEndpointMode for the openai protocol
// family. Anthropic/Gemini modes arrive with their own driver slices.
type EndpointMode string

const (
	EndpointModeChatJSON      EndpointMode = "chat_json"
	EndpointModeChatSSE       EndpointMode = "chat_sse"
	EndpointModeResponsesJSON EndpointMode = "responses_json"
	EndpointModeResponsesSSE  EndpointMode = "responses_sse"
)

// RequestLane mirrors OpenAIGatewayRequestLane.
type RequestLane string

const (
	LaneText  RequestLane = "text"
	LaneImage RequestLane = "image"
)

// RequestShape identifies how a client addressed the gateway.
type RequestShape struct {
	Method string
	// Path is the request path without query string.
	Path string
	// OriginalPathAndQuery keeps the raw client path (query included).
	OriginalPathAndQuery string
	Stream               bool
}

// ProtocolProfile mirrors ProviderProtocolProfileDefinition.
type ProtocolProfile struct {
	ID           string
	ProviderCode string
	ProtocolCode string
	// ProtocolVersion examples: "v1" (openai), "v1beta" (gemini).
	ProtocolVersion string
}

// ResolvedModelMapping mirrors ResolvedOpenAIModelMapping: the account model
// mapping that a request transformation must apply.
type ResolvedModelMapping struct {
	SourceModel            string
	SourceEndpointFamily   string
	UpstreamModel          string
	UpstreamEndpointFamily string
	RuntimeSource          string
	RuntimeRouteRuleID     string
}

// BuildUpstreamRequestInput carries everything the driver needs to produce
// the upstream request.
type BuildUpstreamRequestInput struct {
	Method              string
	ClientPathAndQuery  string
	Body                []byte
	Header              http.Header
	UpstreamBaseURL     string
	Profile             ProtocolProfile
	ModelMapping        *ResolvedModelMapping
	ParsedBody          any
	ParsedBodyAvailable bool
}

// BuildUpstreamRequestResult is the exact upstream request the dispatcher
// must send.
type BuildUpstreamRequestResult struct {
	Method        string
	URL           string
	PathAndQuery  string
	Header        http.Header
	Body          []byte
	Stream        bool
	EndpointMode  EndpointMode
	Lane          RequestLane
	UpstreamModel string
}

// BuildUpstreamRequestError code values (mirrors the Node mapping errors).
const (
	// ErrCodeModelMappingRequestInvalid maps to
	// account_model_mapping_request_invalid: model mapping requires the
	// client body to be a valid JSON object.
	ErrCodeModelMappingRequestInvalid = "account_model_mapping_request_invalid"
	// ErrCodeUnsupportedModelMappingConversion is returned when a resolved
	// mapping targets an upstream family this driver slice cannot convert.
	ErrCodeUnsupportedModelMappingConversion = "account_model_mapping_conversion_unsupported"
)

// BuildUpstreamError is a request-side driver error carrying the gateway
// error code the dispatcher must surface.
type BuildUpstreamError struct {
	Code    string
	Message string
}

func (e *BuildUpstreamError) Error() string { return e.Message }

// InspectResponseInput is the buffered (non-stream) response under
// inspection.
type InspectResponseInput struct {
	StatusCode   int
	Header       http.Header
	Body         []byte
	RequestShape RequestShape
}

// ResponseInspection is the driver verdict over a buffered upstream
// response: protocol completion evidence, semantic success, failure
// attribution and extracted usage.
type ResponseInspection struct {
	EndpointFamily   ResponseEndpointFamily
	ProtocolComplete bool
	SemanticSuccess  bool
	OutputReceived   bool
	Failed           bool
	FinishReason     string
	Status           string
	ErrorCode        string
	ErrorMessage     string
	Usage            ParsedUsage
}

// AttemptEvidence converts the inspection into the frozen outcome evidence.
func (r ResponseInspection) AttemptEvidence(statusCode int) AttemptEvidence {
	return AttemptEvidence{
		StatusCode:      statusCode,
		SemanticSuccess: r.SemanticSuccess,
	}
}

// ProtocolDriver mirrors GatewayProtocolDriver. The five core methods named
// by the G01 slice contract are MatchPath, BuildUpstreamRequest,
// NewStreamInspector (InspectStream), InspectResponse and ExtractUsage*.
type ProtocolDriver interface {
	// Identity.
	ID() string
	ProtocolCode() string
	ProtocolVersion() string
	ResponseProtocol() string
	ClientErrorProtocol() string
	DefaultClientProfile() string

	// Selection surface.
	SupportsProfile(profile ProtocolProfile) bool
	MatchPath(shape RequestShape) bool
	EndpointModeForRequestShape(shape RequestShape) (EndpointMode, bool)

	// Request transform: client request -> upstream request.
	BuildUpstreamRequest(input BuildUpstreamRequestInput) (*BuildUpstreamRequestResult, error)

	// InspectStream: a fresh incremental stream inspector.
	NewStreamInspector() StreamInspector

	// InspectResponse: buffered non-stream response semantics.
	InspectResponse(input InspectResponseInput) ResponseInspection

	// ExtractUsage: usage extraction contract.
	ExtractUsageFromJSONBuffer(body []byte) ParsedUsage
	ExtractUsageFromJSONValue(value any) ParsedUsage
	ExtractUsageFromJSONTextFragment(text string) ParsedUsage

	// ParseErrorPayload: upstream error body normalization.
	ParseErrorPayload(bodyText string, header http.Header) ErrorPayload
}
