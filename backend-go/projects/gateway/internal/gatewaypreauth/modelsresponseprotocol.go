package gatewaypreauth

import (
	"regexp"
	"strings"
)

// Port of request/models-response-protocol.ts: which protocol serves the
// GET /models request when the authenticated models fast path applies.

// ResponseProtocolCode mirrors the response protocol union values used here.
type ResponseProtocolCode string

const (
	ResponseProtocolOpenAI     ResponseProtocolCode = "openai_v1"
	ResponseProtocolAnthropicV ResponseProtocolCode = "anthropic_v1"
	ResponseProtocolGeminiV    ResponseProtocolCode = "gemini_v1beta"
)

// GatewayClientProfileHeader mirrors gatewayClientProfileHeader.
const GatewayClientProfileHeader = "x-juhe-client-profile"

// ResolveGatewayModelsResponseProtocol mirrors resolveGatewayModelsResponseProtocol.
func ResolveGatewayModelsResponseProtocol(req *GatewayRequest) (ResponseProtocolCode, bool) {
	if IsGeminiModelsRequest(req) && isExplicitGeminiModelsClient(req) {
		return ResponseProtocolGeminiV, true
	}
	if IsAnthropicModelsRequest(req) && isExplicitAnthropicModelsClient(req) {
		return ResponseProtocolAnthropicV, true
	}
	if IsOpenAIModelsRequest(req) || IsAnthropicModelsRequest(req) || IsGeminiModelsRequest(req) {
		return ResponseProtocolOpenAI, true
	}
	return "", false
}

// isExplicitGeminiModelsClient mirrors the Node helper field by field.
func isExplicitGeminiModelsClient(req *GatewayRequest) bool {
	path, query := splitPathAndQuery(req.PathAndQuery())
	if strings.ToLower(path) == "/v1beta/models" {
		return true
	}
	profile := normalizedHeaderToken(req.Header(GatewayClientProfileHeader))
	if profile == "gemini" || profile == "generic_gemini" || profile == "gemini_cli" {
		return true
	}
	if lowerHeaderToken(req.Header("x-goog-api-key")) != "" {
		return true
	}
	if key, ok := queryParamFirstValue(query, "key"); ok && strings.TrimSpace(key) != "" {
		return true
	}
	userAgent := lowerHeaderToken(req.Header("user-agent"))
	return userAgent != "" && (geminiCLIUserAgentPattern.MatchString(userAgent) || geminiCLIProxyPattern.MatchString(userAgent))
}

var (
	geminiCLIUserAgentPattern = regexp.MustCompile(`(?i)\bgeminicli(?:[-/]|$)`)
	geminiCLIProxyPattern     = regexp.MustCompile(`(?i)\bproxy_client=geminicli\b`)
)

// isExplicitAnthropicModelsClient mirrors the Node helper.
func isExplicitAnthropicModelsClient(req *GatewayRequest) bool {
	profile := normalizedHeaderToken(req.Header(GatewayClientProfileHeader))
	if profile == "anthropic" || profile == "generic_anthropic" || profile == "claude_code" {
		return true
	}
	if normalizedHeaderToken(req.Header("anthropic-version")) != "" ||
		normalizedHeaderToken(req.Header("anthropic-beta")) != "" ||
		normalizedHeaderToken(req.Header("x-claude-code-session-id")) != "" ||
		normalizedHeaderToken(req.Header("x-claude-code-agent-id")) != "" {
		return true
	}
	return claudeCodeUserAgent(req)
}

// claudeCodeUserAgent mirrors claudeCodeUserAgent.
func claudeCodeUserAgent(req *GatewayRequest) bool {
	userAgent := lowerHeaderToken(req.Header("user-agent"))
	return userAgent != "" && (strings.HasPrefix(userAgent, "claude-cli/") || strings.Contains(userAgent, " claude-cli/"))
}

// normalizedHeaderToken mirrors normalizedHeaderToken: trim, lowercase and
// collapse runs of '-' and whitespace into '_'.
func normalizedHeaderToken(value string) string {
	lower := lowerHeaderToken(value)
	if lower == "" {
		return ""
	}
	return collapseSeparators(lower)
}

// lowerHeaderToken mirrors lowerHeaderToken: trimmed lowercase or ”.
func lowerHeaderToken(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	return strings.ToLower(trimmed)
}

// collapseSeparators mirrors replace(/[-\s]+/g, '_').
func collapseSeparators(value string) string {
	var builder strings.Builder
	previousSeparator := false
	for _, r := range value {
		if r == '-' || r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f' {
			if !previousSeparator {
				builder.WriteByte('_')
			}
			previousSeparator = true
			continue
		}
		previousSeparator = false
		builder.WriteRune(r)
	}
	return builder.String()
}
