package gatewayproto

// ResponseEndpointFamily mirrors the Node ResponseEndpointFamily union
// (protocols/openai-v1/response-semantics.ts). The family drives which
// response shapes a driver inspects.
type ResponseEndpointFamily string

const (
	EndpointFamilyUnknown               ResponseEndpointFamily = "unknown"
	EndpointFamilyChatCompletions       ResponseEndpointFamily = "chat_completions"
	EndpointFamilyResponses             ResponseEndpointFamily = "responses"
	EndpointFamilyMessages              ResponseEndpointFamily = "messages"
	EndpointFamilyModels                ResponseEndpointFamily = "models"
	EndpointFamilyMessageTokenCounting  ResponseEndpointFamily = "message_token_counting"
	EndpointFamilyGenerateContent       ResponseEndpointFamily = "generate_content"
	EndpointFamilyStreamGenerateContent ResponseEndpointFamily = "stream_generate_content"
	EndpointFamilyCountTokens           ResponseEndpointFamily = "count_tokens"
	EndpointFamilyEmbedContent          ResponseEndpointFamily = "embed_content"
	EndpointFamilyInteractions          ResponseEndpointFamily = "interactions"
)

// ResponseTransport mirrors OpenAIResponseTransport.
type ResponseTransport string

const (
	TransportJSON ResponseTransport = "json"
	TransportSSE  ResponseTransport = "sse"
)

// Response protocol codes carried on semantic frames.
const (
	ResponseProtocolOpenAI    = "openai_v1"
	ResponseProtocolAnthropic = "anthropic_v1"
	ResponseProtocolGemini    = "gemini_v1beta"
)

// Semantic frame types (ResponseSemanticFrameType).
const (
	FrameTypeOutputTextDelta string = "output_text_delta"
	FrameTypeOutputTextDone  string = "output_text_done"
	FrameTypeError           string = "error"
	FrameTypeCompleted       string = "completed"
	FrameTypeUsage           string = "usage"
	FrameTypeRawJSONPath     string = "raw_json_path"
)

// SemanticFrame mirrors ResponseSemanticFrame. Index fields default to 0,
// matching how the Node driver only sets them where meaningful; raw payloads
// keep the exact upstream JSON subtree the frame was derived from.
type SemanticFrame struct {
	FrameType      string
	Protocol       string
	EndpointFamily ResponseEndpointFamily
	Transport      ResponseTransport
	Text           string
	ErrorCode      string
	ErrorType      string
	ErrorMessage   string
	FinishReason   string
	Status         string
	Usage          ParsedUsage
	RawJSON        any
	RawJSONPaths   []string
	RawText        string
	EventType      string
	ChoiceIndex    int
	OutputIndex    int
	ContentIndex   int
	VisibleOutput  bool
}

// ErrorPayload mirrors GatewayProtocolErrorPayload: the normalized
// code/type/message triple extracted from an upstream error body.
type ErrorPayload struct {
	Code    string
	Type    string
	Message string
}

// HasEvidence reports whether anything was extracted.
func (p ErrorPayload) HasEvidence() bool {
	return p.Code != "" || p.Type != "" || p.Message != ""
}
