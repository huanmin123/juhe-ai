// Package gateway defines the protocol-facing request vocabulary shared by the
// Go gateway. It deliberately has no HTTP, storage, or upstream I/O dependency.
package gateway

import "strings"

type ProtocolCode string

const (
	ProtocolOpenAI    ProtocolCode = "openai"
	ProtocolAnthropic ProtocolCode = "anthropic"
	ProtocolGemini    ProtocolCode = "gemini"
)

type ResponseProtocol string

const (
	ResponseProtocolOpenAIV1     ResponseProtocol = "openai_v1"
	ResponseProtocolAnthropicV1  ResponseProtocol = "anthropic_v1"
	ResponseProtocolGeminiV1Beta ResponseProtocol = "gemini_v1beta"
)

type ClientErrorProtocol string

const (
	ClientErrorOpenAI    ClientErrorProtocol = "openai"
	ClientErrorAnthropic ClientErrorProtocol = "anthropic"
	ClientErrorGemini    ClientErrorProtocol = "gemini"
)

type ClientProfile string

const (
	ClientProfileCodex            ClientProfile = "codex"
	ClientProfileGenericOpenAI    ClientProfile = "generic_openai"
	ClientProfileClaudeCode       ClientProfile = "claude_code"
	ClientProfileGenericAnthropic ClientProfile = "generic_anthropic"
	ClientProfileGeminiCLI        ClientProfile = "gemini_cli"
	ClientProfileGenericGemini    ClientProfile = "generic_gemini"
)

type ClientCompatibility string

const (
	CompatibilityCodexResponses  ClientCompatibility = "codex_responses"
	CompatibilityOpenAIStandard  ClientCompatibility = "openai_standard"
	CompatibilityClaudeCode      ClientCompatibility = "claude_code"
	CompatibilityAnthropicNative ClientCompatibility = "anthropic_native"
)

type ClientProfileSource string

const (
	ClientProfileSourceDefault           ClientProfileSource = "default"
	ClientProfileSourceExplicitHeader    ClientProfileSource = "explicit_header"
	ClientProfileSourceCodexTurnMetadata ClientProfileSource = "codex_turn_metadata"
	ClientProfileSourceClaudeSignature   ClientProfileSource = "claude_code_request_signature"
	ClientProfileSourceGeminiSignature   ClientProfileSource = "gemini_cli_request_signature"
)

type EndpointFamily string

const (
	EndpointUnknown               EndpointFamily = ""
	EndpointChatCompletions       EndpointFamily = "chat_completions"
	EndpointResponses             EndpointFamily = "responses"
	EndpointModels                EndpointFamily = "models"
	EndpointImages                EndpointFamily = "images"
	EndpointEmbeddings            EndpointFamily = "embeddings"
	EndpointAudio                 EndpointFamily = "audio"
	EndpointMessages              EndpointFamily = "messages"
	EndpointMessageTokenCounting  EndpointFamily = "message_token_counting"
	EndpointGenerateContent       EndpointFamily = "generate_content"
	EndpointStreamGenerateContent EndpointFamily = "stream_generate_content"
	EndpointCountTokens           EndpointFamily = "count_tokens"
	EndpointEmbedContent          EndpointFamily = "embed_content"
	EndpointInteractions          EndpointFamily = "interactions"
)

type GeminiInteractionAction string

const (
	GeminiInteractionNone   GeminiInteractionAction = ""
	GeminiInteractionCreate GeminiInteractionAction = "create"
	GeminiInteractionGet    GeminiInteractionAction = "get"
	GeminiInteractionDelete GeminiInteractionAction = "delete"
	GeminiInteractionCancel GeminiInteractionAction = "cancel"
)

type DownstreamProtocol string

const (
	DownstreamResponsesSSE             DownstreamProtocol = "responses_sse"
	DownstreamChatCompletionsSSE       DownstreamProtocol = "chat_completions_sse"
	DownstreamMessagesSSE              DownstreamProtocol = "messages_sse"
	DownstreamGeminiGenerateContentSSE DownstreamProtocol = "gemini_stream_generate_content_sse"
	DownstreamGeminiInteractionsSSE    DownstreamProtocol = "gemini_interactions_sse"
	DownstreamJSON                     DownstreamProtocol = "json"
	DownstreamUnknownStream            DownstreamProtocol = "unknown_stream"
)

type RequestLane string

const (
	RequestLaneText  RequestLane = "text"
	RequestLaneImage RequestLane = "image"
)

type Profile struct {
	Code    string
	Version string
}

// RequestShape is the bounded, HTTP-independent input required for routing
// classification. Body parsing remains the responsibility of the caller.
type RequestShape struct {
	Method              string
	Path                string
	Headers             map[string]string
	Stream              bool
	Model               string
	ImageGenerationHint bool
	StoreRequest        StoreRequestFact
}

func (r RequestShape) Header(name string) string {
	for key, value := range r.Headers {
		if strings.EqualFold(key, name) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type Definition struct {
	ID                   string
	Code                 ProtocolCode
	Version              string
	ResponseProtocol     ResponseProtocol
	ClientErrorProtocol  ClientErrorProtocol
	DefaultClientProfile ClientProfile
}

type ClientProfileResolution struct {
	Profile       ClientProfile
	Source        ClientProfileSource
	Compatibility ClientCompatibility
}
