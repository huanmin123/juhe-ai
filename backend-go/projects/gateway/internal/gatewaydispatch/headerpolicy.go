package gatewaydispatch

import (
	"net/http"
	"strings"
)

// Header policy, migrated from upstream/header-policy.ts. A header can be
// request-dynamic or sensitive without being a stable model conversation
// identifier; session resolvers remain client-specific.

// CodexResponsesScopedHeaderNames mirrors codexResponsesScopedHeaderNames.
var CodexResponsesScopedHeaderNames = []string{
	"openai-beta",
	"originator",
	"session-id",
	"thread-id",
	"version",
	"x-client-request-id",
	"x-oai-attestation",
	"x-openai-internal-codex-responses-lite",
	"x-openai-memgen-request",
	"x-openai-subagent",
	"x-responsesapi-include-timing-metrics",
}

var codexResponsesScopedHeaderNameSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(CodexResponsesScopedHeaderNames))
	for _, name := range CodexResponsesScopedHeaderNames {
		set[name] = struct{}{}
	}
	return set
}()

var codexResponsesScopedHeaderPrefixes = []string{"x-codex-"}

// IsCodexResponsesScopedHeaderName mirrors isCodexResponsesScopedHeaderName.
func IsCodexResponsesScopedHeaderName(name string) bool {
	normalized := normalizeHeaderName(name)
	if _, ok := codexResponsesScopedHeaderNameSet[normalized]; ok {
		return true
	}
	for _, prefix := range codexResponsesScopedHeaderPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

// StripCodexResponsesScopedHeaders mirrors stripCodexResponsesScopedHeaders.
func StripCodexResponsesScopedHeaders(headers http.Header) {
	stripHeaders(headers, IsCodexResponsesScopedHeaderName)
}

// AnthropicMessagesScopedHeaderNames mirrors anthropicMessagesScopedHeaderNames.
var AnthropicMessagesScopedHeaderNames = []string{
	"anthropic-beta",
	"anthropic-dangerous-direct-browser-access",
	"anthropic-version",
	"x-api-key",
	"x-app",
	"x-claude-code-agent-id",
	"x-claude-code-session-id",
}

var anthropicMessagesScopedHeaderNameSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(AnthropicMessagesScopedHeaderNames))
	for _, name := range AnthropicMessagesScopedHeaderNames {
		set[name] = struct{}{}
	}
	return set
}()

var anthropicMessagesScopedHeaderPrefixes = []string{"anthropic-", "x-claude-code-", "x-stainless-"}

// IsAnthropicMessagesScopedHeaderName mirrors
// isAnthropicMessagesScopedHeaderName.
func IsAnthropicMessagesScopedHeaderName(name string) bool {
	normalized := normalizeHeaderName(name)
	if _, ok := anthropicMessagesScopedHeaderNameSet[normalized]; ok {
		return true
	}
	for _, prefix := range anthropicMessagesScopedHeaderPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

// StripAnthropicMessagesScopedHeaders mirrors
// stripAnthropicMessagesScopedHeaders.
func StripAnthropicMessagesScopedHeaders(headers http.Header) {
	stripHeaders(headers, IsAnthropicMessagesScopedHeaderName)
}

// GeminiGenerateContentScopedHeaderNames mirrors the Node list.
var GeminiGenerateContentScopedHeaderNames = []string{
	"x-gemini-api-privileged-user-id",
	"x-goog-api-client",
	"x-goog-api-key",
	"x-vertex-ai-llm-request-type",
	"x-vertex-ai-llm-shared-request-type",
}

// OfficialOAuthClientHeaderProfile mirrors the profile union.
type OfficialOAuthClientHeaderProfile string

const (
	OAuthHeaderProfileOpenAICodex       OfficialOAuthClientHeaderProfile = "openai_codex"
	OAuthHeaderProfileAnthropicClaude   OfficialOAuthClientHeaderProfile = "anthropic_claude_code"
	OAuthHeaderProfileGeminiCLI         OfficialOAuthClientHeaderProfile = "gemini_cli"
	OAuthHeaderProfileXAIGrok           OfficialOAuthClientHeaderProfile = "xai_grok"
)

// IncomingHeaders mirrors the Node IncomingHeaderMap.
type IncomingHeaders = http.Header

var commonOfficialOAuthHeaderNames = map[string]struct{}{
	"accept":          {},
	"accept-language": {},
	"content-type":    {},
	"idempotency-key": {},
	"user-agent":      {},
}

var openAIOAuthCodexHeaderNames = func() map[string]struct{} {
	set := make(map[string]struct{}, len(CodexResponsesScopedHeaderNames)+1)
	for _, name := range CodexResponsesScopedHeaderNames {
		set[name] = struct{}{}
	}
	set["x-codex-turn-state"] = struct{}{}
	return set
}()

var anthropicOAuthClaudeCodeHeaderNames = map[string]struct{}{
	"anthropic-beta":                          {},
	"anthropic-dangerous-direct-browser-access": {},
	"anthropic-version":                       {},
	"x-app":                                   {},
	"x-claude-code-agent-id":                  {},
	"x-claude-code-session-id":                {},
}

var geminiOAuthCliHeaderNames = map[string]struct{}{
	"api-revision":                     {},
	"x-gemini-api-privileged-user-id":  {},
	"x-goog-api-client":                {},
	"x-vertex-ai-llm-request-type":     {},
	"x-vertex-ai-llm-shared-request-type": {},
}

var xaiOAuthGrokHeaderNames = map[string]struct{}{
	"x-grok-client-version": {},
	"x-xai-token-auth":      {},
}

// CopyOfficialOAuthClientRequestHeaders mirrors
// copyOfficialOAuthClientRequestHeaders: official subscription/OAuth adapters
// use a positive client-header policy; API-key adapters keep the generic safe
// passthrough policy.
func CopyOfficialOAuthClientRequestHeaders(inputHeaders IncomingHeaders, profile OfficialOAuthClientHeaderProfile) http.Header {
	output := http.Header{}
	if inputHeaders == nil {
		return output
	}
	for name, values := range inputHeaders {
		if len(values) == 0 || !isAllowedOfficialOAuthClientHeader(name, profile) {
			continue
		}
		output.Set(name, strings.Join(values, ", "))
	}
	return output
}

func isAllowedOfficialOAuthClientHeader(name string, profile OfficialOAuthClientHeaderProfile) bool {
	normalized := normalizeHeaderName(name)
	if _, ok := commonOfficialOAuthHeaderNames[normalized]; ok {
		return true
	}
	switch profile {
	case OAuthHeaderProfileOpenAICodex:
		if _, ok := openAIOAuthCodexHeaderNames[normalized]; ok {
			return true
		}
		return strings.HasPrefix(normalized, "x-codex-")
	case OAuthHeaderProfileAnthropicClaude:
		if _, ok := anthropicOAuthClaudeCodeHeaderNames[normalized]; ok {
			return true
		}
		return strings.HasPrefix(normalized, "x-claude-code-") || strings.HasPrefix(normalized, "x-stainless-")
	case OAuthHeaderProfileGeminiCLI:
		_, ok := geminiOAuthCliHeaderNames[normalized]
		return ok
	case OAuthHeaderProfileXAIGrok:
		_, ok := xaiOAuthGrokHeaderNames[normalized]
		return ok
	}
	return false
}

var geminiGenerateContentScopedHeaderNameSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(GeminiGenerateContentScopedHeaderNames))
	for _, name := range GeminiGenerateContentScopedHeaderNames {
		set[name] = struct{}{}
	}
	return set
}()

var geminiGenerateContentScopedHeaderPrefixes = []string{"x-gemini-", "x-goog-", "x-vertex-ai-"}

// IsGeminiGenerateContentScopedHeaderName mirrors the Node predicate.
func IsGeminiGenerateContentScopedHeaderName(name string) bool {
	normalized := normalizeHeaderName(name)
	if _, ok := geminiGenerateContentScopedHeaderNameSet[normalized]; ok {
		return true
	}
	for _, prefix := range geminiGenerateContentScopedHeaderPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

// StripGeminiGenerateContentScopedHeaders mirrors the Node helper.
func StripGeminiGenerateContentScopedHeaders(headers http.Header) {
	stripHeaders(headers, IsGeminiGenerateContentScopedHeaderName)
}

func stripHeaders(headers http.Header, shouldStrip func(string) bool) {
	for name := range headers {
		if shouldStrip(name) {
			headers.Del(name)
		}
	}
}

func normalizeHeaderName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// HeadersToObject mirrors upstream/headers.ts headersToObject.
func HeadersToObject(headers http.Header) map[string]string {
	output := make(map[string]string, len(headers))
	for name, values := range headers {
		if len(values) == 0 {
			continue
		}
		output[name] = strings.Join(values, ", ")
	}
	return output
}
