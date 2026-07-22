package gateway

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
)

var geminiCLIUserAgentPattern = regexp.MustCompile(`(?i)(\bGeminiCLI(?:[-/]|$)|proxy_client=geminicli\b)`)

func ResolveClientProfile(protocol ProtocolCode, request RequestShape) (ClientProfileResolution, bool) {
	switch protocol {
	case ProtocolOpenAI:
		return resolveOpenAIClientProfile(request), true
	case ProtocolAnthropic:
		return resolveAnthropicClientProfile(request), true
	case ProtocolGemini:
		return resolveGeminiClientProfile(request), true
	default:
		return ClientProfileResolution{}, false
	}
}

func resolveOpenAIClientProfile(request RequestShape) ClientProfileResolution {
	downstream := ResolveDownstreamProtocol(ProtocolOpenAI, request)
	if hasCodexTurnID(request.Header("x-codex-turn-metadata")) &&
		(downstream == DownstreamResponsesSSE || isCodexCompactRequest(request)) {
		return ClientProfileResolution{Profile: ClientProfileCodex, Source: ClientProfileSourceCodexTurnMetadata, Compatibility: CompatibilityCodexResponses}
	}
	return ClientProfileResolution{Profile: ClientProfileGenericOpenAI, Source: ClientProfileSourceDefault, Compatibility: CompatibilityOpenAIStandard}
}

func resolveAnthropicClientProfile(request RequestShape) ClientProfileResolution {
	method := strings.ToUpper(strings.TrimSpace(request.Method))
	family := anthropicEndpointFamily(request.Path)
	supportedShape := method == "POST" && (family == EndpointMessages || family == EndpointMessageTokenCounting)
	if supportedShape && explicitClientProfile(request) == ClientProfileClaudeCode {
		return ClientProfileResolution{Profile: ClientProfileClaudeCode, Source: ClientProfileSourceExplicitHeader, Compatibility: CompatibilityClaudeCode}
	}
	if method == "POST" && family == EndpointMessages && isClaudeCodeSignature(request) {
		return ClientProfileResolution{Profile: ClientProfileClaudeCode, Source: ClientProfileSourceClaudeSignature, Compatibility: CompatibilityClaudeCode}
	}
	return ClientProfileResolution{Profile: ClientProfileGenericAnthropic, Source: ClientProfileSourceDefault, Compatibility: CompatibilityAnthropicNative}
}

func resolveGeminiClientProfile(request RequestShape) ClientProfileResolution {
	method := strings.ToUpper(strings.TrimSpace(request.Method))
	family := geminiEndpointFamily(request.Path)
	supportedShape := (method == "POST" && (family == EndpointGenerateContent || family == EndpointStreamGenerateContent ||
		family == EndpointCountTokens || family == EndpointEmbedContent)) ||
		(family == EndpointInteractions && GeminiInteractionActionForRequest(method, request.Path) != GeminiInteractionNone)
	if supportedShape && explicitClientProfile(request) == ClientProfileGeminiCLI {
		return ClientProfileResolution{Profile: ClientProfileGeminiCLI, Source: ClientProfileSourceExplicitHeader, Compatibility: CompatibilityOpenAIStandard}
	}
	if method == "POST" && (family == EndpointGenerateContent || family == EndpointStreamGenerateContent) && isGeminiCLISignature(request) {
		return ClientProfileResolution{Profile: ClientProfileGeminiCLI, Source: ClientProfileSourceGeminiSignature, Compatibility: CompatibilityOpenAIStandard}
	}
	return ClientProfileResolution{Profile: ClientProfileGenericGemini, Source: ClientProfileSourceDefault, Compatibility: CompatibilityOpenAIStandard}
}

func explicitClientProfile(request RequestShape) ClientProfile {
	normalized := normalizeExplicitProfile(request.Header("x-juhe-client-profile"))
	switch normalized {
	case string(ClientProfileClaudeCode):
		return ClientProfileClaudeCode
	case string(ClientProfileGeminiCLI):
		return ClientProfileGeminiCLI
	default:
		return ""
	}
}

func normalizeExplicitProfile(value string) string {
	var result strings.Builder
	separator := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if r == '-' || unicode.IsSpace(r) {
			if !separator {
				result.WriteByte('_')
			}
			separator = true
			continue
		}
		separator = false
		result.WriteRune(r)
	}
	return result.String()
}

func hasCodexTurnID(value string) bool {
	if value == "" {
		return false
	}
	var metadata struct {
		TurnID string `json:"turn_id"`
	}
	return json.Unmarshal([]byte(value), &metadata) == nil && strings.TrimSpace(metadata.TurnID) != ""
}

func isCodexCompactRequest(request RequestShape) bool {
	return strings.EqualFold(strings.TrimSpace(request.Method), "POST") && normalizedPath(request.Path, "/v1") == "/responses/compact"
}

func isClaudeCodeSignature(request RequestShape) bool {
	signals := 0
	userAgent := strings.ToLower(request.Header("user-agent"))
	if strings.HasPrefix(userAgent, "claude-cli/") || strings.Contains(userAgent, " claude-cli/") {
		signals++
	}
	for _, item := range strings.Split(strings.ToLower(request.Header("anthropic-beta")), ",") {
		if strings.HasPrefix(strings.TrimSpace(item), "claude-code-") {
			signals++
			break
		}
	}
	if request.Header("x-claude-code-session-id") != "" || request.Header("x-claude-code-agent-id") != "" {
		signals++
	}
	if queryValues(request.Path).Get("beta") == "true" {
		signals++
	}
	return signals >= 2
}

func isGeminiCLISignature(request RequestShape) bool {
	if !geminiCLIUserAgentPattern.MatchString(request.Header("user-agent")) {
		return false
	}
	return request.Header("x-goog-api-key") != "" || request.Header("x-api-key") != "" ||
		request.Header("authorization") != "" || strings.TrimSpace(queryValues(request.Path).Get("key")) != ""
}
