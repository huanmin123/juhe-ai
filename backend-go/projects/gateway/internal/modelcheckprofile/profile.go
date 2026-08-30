// Package modelcheckprofile freezes the Gateway J3b protocol/model catalog.
// It is pure: no database, network, Node or mutable registry dependency.
package modelcheckprofile

import "strings"

type Protocol string

const (
	ProtocolOpenAIResponses Protocol = "openai_responses"
	ProtocolOpenAIChat      Protocol = "openai_chat"
	ProtocolAnthropic       Protocol = "anthropic_messages"
	ProtocolGeminiNative    Protocol = "gemini_native"
)

type EndpointFamily string

const (
	EndpointResponses       EndpointFamily = "responses"
	EndpointChatCompletions EndpointFamily = "chat_completions"
	EndpointMessages        EndpointFamily = "messages"
	EndpointGenerateContent EndpointFamily = "generate_content"
	EndpointStreamGenerate  EndpointFamily = "stream_generate_content"
)

// Endpoint modes are the immutable request shapes selected by the Business
// account health-check configuration. Keep these values separate from
// Protocol: one protocol can have a JSON and a streaming request shape.
const (
	EndpointModeResponsesJSON       = "responses_json"
	EndpointModeResponsesSSE        = "responses_sse"
	EndpointModeChatJSON            = "chat_json"
	EndpointModeChatSSE             = "chat_sse"
	EndpointModeMessagesJSON        = "messages_json"
	EndpointModeMessagesSSE         = "messages_sse"
	EndpointModeGenerateContentJSON = "generate_content_json"
	EndpointModeGenerateContentSSE  = "generate_content_sse"
)

type ProtocolProfile struct {
	ID                         string
	Protocol                   Protocol
	ProtocolLabel              string
	ProviderCode               string
	ProviderProtocolProfileIDs []string
	Models                     []string
	DefaultModel               string
}

const (
	DefaultModel            = "gpt-5.6-sol"
	DefaultProfile          = "quick"
	ProbeSetVersion         = "multi-provider-model-check-v4-gpt56-preview"
	QuickProbeSetVersion    = "multi-provider-model-check-quick-v2-light-suite"
	DistributionSampleCount = 5
)

var catalog = []ProtocolProfile{
	{ID: "openai_responses_strong", Protocol: ProtocolOpenAIResponses, ProtocolLabel: "OpenAI Responses", ProviderCode: "gpt", ProviderProtocolProfileIDs: []string{"profile_gpt_openai_v1"}, Models: []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4"}, DefaultModel: "gpt-5.6-sol"},
	{ID: "openai_responses_strong", Protocol: ProtocolOpenAIResponses, ProtocolLabel: "OpenAI Responses", ProviderCode: "openai", ProviderProtocolProfileIDs: []string{"profile_openai_openai_v1"}, Models: []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4"}, DefaultModel: "gpt-5.6-sol"},
	{ID: "openai_chat_strong", Protocol: ProtocolOpenAIChat, ProtocolLabel: "OpenAI Chat Completions", ProviderCode: "deepseek", ProviderProtocolProfileIDs: []string{"profile_deepseek_openai_v1"}, Models: []string{"deepseek-v4-flash", "deepseek-v4-pro"}, DefaultModel: "deepseek-v4-flash"},
	{ID: "anthropic_messages_strong", Protocol: ProtocolAnthropic, ProtocolLabel: "Anthropic Messages", ProviderCode: "deepseek", ProviderProtocolProfileIDs: []string{"profile_deepseek_anthropic_v1"}, Models: []string{"deepseek-v4-flash", "deepseek-v4-pro"}, DefaultModel: "deepseek-v4-flash"},
	{ID: "openai_chat_strong", Protocol: ProtocolOpenAIChat, ProtocolLabel: "OpenAI Chat Completions", ProviderCode: "glm", ProviderProtocolProfileIDs: []string{"profile_glm_general_openai_v1", "profile_glm_coding_openai_v1"}, Models: []string{"glm-5.2", "glm-5.1"}, DefaultModel: "glm-5.2"},
	{ID: "anthropic_messages_strong", Protocol: ProtocolAnthropic, ProtocolLabel: "Anthropic Messages", ProviderCode: "glm", ProviderProtocolProfileIDs: []string{"profile_glm_coding_anthropic_v1"}, Models: []string{"glm-5.2", "glm-5.1"}, DefaultModel: "glm-5.2"},
	{ID: "anthropic_messages_strong", Protocol: ProtocolAnthropic, ProtocolLabel: "Anthropic Messages", ProviderCode: "anthropic", ProviderProtocolProfileIDs: []string{"profile_anthropic_anthropic_v1"}, Models: []string{"claude-opus-5", "claude-opus-4-8"}, DefaultModel: "claude-opus-5"},
	{ID: "gemini_native_strong", Protocol: ProtocolGeminiNative, ProtocolLabel: "Gemini native v1beta", ProviderCode: "gemini", ProviderProtocolProfileIDs: []string{"profile_gemini_native_v1beta"}, Models: []string{"gemini-3.5-flash", "gemini-3.1-pro-preview"}, DefaultModel: "gemini-3.5-flash"},
	{ID: "openai_chat_strong", Protocol: ProtocolOpenAIChat, ProtocolLabel: "OpenAI Chat Completions", ProviderCode: "gemini", ProviderProtocolProfileIDs: []string{"profile_gemini_openai_chat_v1beta"}, Models: []string{"gemini-3.5-flash", "gemini-3.1-pro-preview"}, DefaultModel: "gemini-3.5-flash"},
}

var pairedModels = map[string]string{
	"gpt-5.6-sol": "gpt-5.6-terra", "gpt-5.6-terra": "gpt-5.6-sol", "gpt-5.6-luna": "gpt-5.6-terra", "gpt-5.5": "gpt-5.4", "gpt-5.4": "gpt-5.5",
	"claude-opus-5": "claude-opus-4-8", "claude-opus-4-8": "claude-opus-5", "glm-5.2": "glm-5.1", "glm-5.1": "glm-5.2",
	"deepseek-v4-flash": "deepseek-v4-pro", "deepseek-v4-pro": "deepseek-v4-flash", "gemini-3.5-flash": "gemini-3.1-pro-preview", "gemini-3.1-pro-preview": "gemini-3.5-flash",
}

func Profiles() []ProtocolProfile {
	result := make([]ProtocolProfile, len(catalog))
	for i, p := range catalog {
		result[i] = clone(p)
	}
	return result
}
func NormalizeToken(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
func Find(providerCode, profileID string) (ProtocolProfile, bool) {
	providerCode, profileID = NormalizeToken(providerCode), NormalizeToken(profileID)
	for _, p := range catalog {
		if NormalizeToken(p.ProviderCode) != providerCode {
			continue
		}
		for _, id := range p.ProviderProtocolProfileIDs {
			if NormalizeToken(id) == profileID {
				return clone(p), true
			}
		}
	}
	return ProtocolProfile{}, false
}
func FindForModel(providerCode, profileID, model string) (ProtocolProfile, bool) {
	p, ok := Find(providerCode, profileID)
	if !ok {
		return ProtocolProfile{}, false
	}
	for _, candidate := range p.Models {
		if candidate == model {
			return p, true
		}
	}
	return ProtocolProfile{}, false
}
func SupportedModels() []string {
	seen := map[string]bool{}
	result := make([]string, 0, 16)
	for _, p := range catalog {
		for _, model := range p.Models {
			if !seen[model] {
				seen[model] = true
				result = append(result, model)
			}
		}
	}
	return result
}
func PairedModel(profile ProtocolProfile, model string) string {
	if preferred, ok := pairedModels[model]; ok {
		for _, candidate := range profile.Models {
			if candidate == preferred {
				return preferred
			}
		}
	}
	for _, candidate := range profile.Models {
		if candidate != model {
			return candidate
		}
	}
	return ""
}
func SourceEndpointFamilies(profile ProtocolProfile) []EndpointFamily {
	switch profile.Protocol {
	case ProtocolOpenAIResponses:
		return []EndpointFamily{EndpointResponses}
	case ProtocolOpenAIChat:
		return []EndpointFamily{EndpointChatCompletions}
	case ProtocolAnthropic:
		return []EndpointFamily{EndpointMessages}
	case ProtocolGeminiNative:
		return []EndpointFamily{EndpointGenerateContent, EndpointStreamGenerate}
	default:
		return nil
	}
}

// EndpointModeForProtocol provides the deterministic default used only when
// an older caller has not yet supplied the Business health-check mode.
func EndpointModeForProtocol(protocol Protocol, stream bool) string {
	switch protocol {
	case ProtocolOpenAIResponses:
		if stream {
			return EndpointModeResponsesSSE
		}
		return EndpointModeResponsesJSON
	case ProtocolOpenAIChat:
		if stream {
			return EndpointModeChatSSE
		}
		return EndpointModeChatJSON
	case ProtocolAnthropic:
		if stream {
			return EndpointModeMessagesSSE
		}
		return EndpointModeMessagesJSON
	case ProtocolGeminiNative:
		if stream {
			return EndpointModeGenerateContentSSE
		}
		return EndpointModeGenerateContentJSON
	default:
		return ""
	}
}

// ProtocolForEndpointMode rejects modes that the Gateway J3b probe does not
// implement. In particular, images/interactions are not silently rewritten
// into a text probe.
func ProtocolForEndpointMode(mode string) (Protocol, bool) {
	switch strings.TrimSpace(mode) {
	case EndpointModeResponsesJSON, EndpointModeResponsesSSE:
		return ProtocolOpenAIResponses, true
	case EndpointModeChatJSON, EndpointModeChatSSE:
		return ProtocolOpenAIChat, true
	case EndpointModeMessagesJSON, EndpointModeMessagesSSE:
		return ProtocolAnthropic, true
	case EndpointModeGenerateContentJSON, EndpointModeGenerateContentSSE:
		return ProtocolGeminiNative, true
	default:
		return "", false
	}
}

func EndpointModeIsStreaming(mode string) bool {
	switch strings.TrimSpace(mode) {
	case EndpointModeResponsesSSE, EndpointModeChatSSE, EndpointModeMessagesSSE, EndpointModeGenerateContentSSE:
		return true
	default:
		return false
	}
}

func EndpointModeMatchesProtocol(protocol Protocol, mode string) bool {
	resolved, ok := ProtocolForEndpointMode(mode)
	return ok && resolved == protocol
}
func clone(profile ProtocolProfile) ProtocolProfile {
	profile.ProviderProtocolProfileIDs = append([]string(nil), profile.ProviderProtocolProfileIDs...)
	profile.Models = append([]string(nil), profile.Models...)
	return profile
}
