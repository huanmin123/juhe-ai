// Package gatewayopenai is the G02 slice of the W4-W5 gateway chain: the
// complete OpenAI v1 protocol driver.
//
// It mirrors backend/src/modules/gateway/protocols/openai-v1:
//
//   - request transformation (model mapping, upstream URL, request lane),
//   - SSE stream parsing and the incremental stream inspector,
//   - buffered (non-stream) response semantics and the SSE inspection
//     buffer,
//   - usage extraction and the stream usage fallback,
//   - upstream error payload normalization.
//
// All behavior is verified against shared/platform/mockupstream scenarios
// with zero real network egress.
package gatewayopenai

import (
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// Protocol identity constants (mirrors domain/provider-protocol.ts).
const (
	ProtocolCode    = "openai"
	ProtocolVersion = "v1"

	// ResponseProtocol mirrors the driver responseProtocol value.
	ResponseProtocol = gatewayproto.ResponseProtocolOpenAI
	// ClientErrorProtocol mirrors clientErrorProtocol.
	ClientErrorProtocol = "openai"
	// DefaultClientProfile mirrors defaultClientProfile.
	DefaultClientProfile = "generic_openai"

	// GeminiOpenAIChatProfileID guards anthropic->chat mapping resolution
	// (GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID).
	GeminiOpenAIChatProfileID = "profile_gemini_openai_chat_v1beta"

	// Endpoint family tokens.
	FamilyChatCompletions       = "chat_completions"
	FamilyResponses             = "responses"
	FamilyAnthropicMessages     = "messages"
	FamilyGeminiGenerateContent = "generate_content"
	FamilyGeminiStreamGenerate  = "stream_generate_content"

	// Model mapping runtime sources.
	RuntimeSourceExplicitHybridRoute = "explicit_hybrid_route"
)
