package gatewayanthropic

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Claude Code 客户端兼容常量（对齐 client-compatibility.ts）。
const (
	GatewayClientProfileHeader = "x-juhe-client-profile"

	AnthropicClaudeCodeVersion    = "2.1.201"
	AnthropicClaudeCodeUserAgent  = "claude-cli/" + AnthropicClaudeCodeVersion + " (external, sdk-cli)"
	ClaudeCodeSessionIDHeader     = "x-claude-code-session-id"
	ClaudeCodeAgentIDHeader       = "x-claude-code-agent-id"
	AnthropicBetaHeader           = "anthropic-beta"
	ClientRequestIDHeader         = "x-client-request-id"
	RequestIDHeader               = "x-request-id"
	AnthropicClaudeCodeBetaHeader = "claude-code-20250219"
)

// AnthropicClaudeCodeBetaHeaders 对齐 anthropicClaudeCodeBetaHeaders。
var AnthropicClaudeCodeBetaHeaders = []string{
	"claude-code-20250219",
	"interleaved-thinking-2025-05-14",
	"effort-2025-11-24",
}

// ClientCompatibilityOptions 对齐 AnthropicClientCompatibilityOptions。
type ClientCompatibilityOptions struct {
	// RequestClientCompatibility 是账户/路由配置的客户端兼容能力
	//（ClientCompatibilityCapability，如 "claude_code"）。
	RequestClientCompatibility string
	// TargetPathAndQuery 是上游目标路径（用于重写后的目标仍判定为 messages）。
	TargetPathAndQuery string
	// SessionIDHolder 允许调用方在单个请求生命周期内复用随机生成的会话 ID
	//（对齐 Node 把随机 UUID 缓存在请求对象上）。可为 nil。
	SessionIDHolder *string
}

// ShouldApplyClaudeCodeMessagesCompatibility 对齐同名函数：
//   - 请求本身或目标路径是 POST /messages；
//   - 且满足显式能力、网关客户端画像头、或（原始请求为 messages 且
//     Claude Code 请求签名信号 >= 2）。
func ShouldApplyClaudeCodeMessagesCompatibility(r *http.Request, options ClientCompatibilityOptions) bool {
	originalMessagesRequest := IsMessagesPostRequest(r, "")
	targetPathAndQuery := options.TargetPathAndQuery
	targetMessagesRequest := false
	if targetPathAndQuery != "" {
		targetMessagesRequest = IsMessagesPostRequest(r, targetPathAndQuery)
	}
	if !originalMessagesRequest && !targetMessagesRequest {
		return false
	}
	if options.RequestClientCompatibility == "claude_code" {
		return true
	}
	if parseGatewayClientProfileHeader(r.Header.Get(GatewayClientProfileHeader)) == "claude_code" {
		return true
	}
	return originalMessagesRequest && isClaudeCodeAnthropicRequestSignature(r)
}

// ApplyClientCompatibilityHeaders 对齐 applyAnthropicClientCompatibilityHeaders：
// 覆写非 Claude Code 的 user-agent、合并 anthropic-beta、补全会话 ID 头。
// 返回最终采用的会话 ID（未应用兼容时为空）。
func ApplyClientCompatibilityHeaders(r *http.Request, options ClientCompatibilityOptions) string {
	if !ShouldApplyClaudeCodeMessagesCompatibility(r, options) {
		return ""
	}
	if !isClaudeCodeUserAgent(r.Header.Get("User-Agent")) {
		r.Header.Set("User-Agent", AnthropicClaudeCodeUserAgent)
	}
	r.Header.Set(AnthropicBetaHeader, mergeAnthropicBetaHeader(r.Header.Get(AnthropicBetaHeader), AnthropicClaudeCodeBetaHeaders))
	if r.Header.Get(ClaudeCodeSessionIDHeader) == "" {
		r.Header.Set(ClaudeCodeSessionIDHeader, ClaudeCodeSessionIDForRequest(r, options.SessionIDHolder))
	}
	return r.Header.Get(ClaudeCodeSessionIDHeader)
}

// ClaudeCodeSessionIDForRequest 对齐 anthropicClaudeCodeSessionIdForRequest：
// 继承既有 session/x-client-request-id/x-request-id，否则生成（并可缓存）
// 随机 UUID。
func ClaudeCodeSessionIDForRequest(r *http.Request, holder *string) string {
	existing := firstHeaderValue(r, ClaudeCodeSessionIDHeader, ClientRequestIDHeader, RequestIDHeader)
	if existing != "" {
		return existing
	}
	if holder != nil && *holder != "" {
		return *holder
	}
	generated := randomUUID()
	if holder != nil {
		*holder = generated
	}
	return generated
}

// PathAndQueryForRequest 对齐 anthropicClaudeCodePathAndQueryForRequest：
// 需要兼容时在目标路径上补 beta=true。
func PathAndQueryForRequest(r *http.Request, pathAndQuery string, options *ClientCompatibilityOptions) string {
	if pathAndQuery == "" {
		pathAndQuery = RequestPathAndQuery(r)
	}
	opts := ClientCompatibilityOptions{}
	if options != nil {
		opts = *options
	}
	if opts.TargetPathAndQuery == "" {
		opts.TargetPathAndQuery = pathAndQuery
	}
	if !ShouldApplyClaudeCodeMessagesCompatibility(r, opts) {
		return pathAndQuery
	}
	return withQueryParamIfMissing(pathAndQuery, "beta", "true")
}

// mergeAnthropicBetaHeader 对齐 mergeAnthropicBetaHeader：先保留现有值，
// 再合并必需 beta（大小写不敏感去重，逗号连接）。
func mergeAnthropicBetaHeader(current string, required []string) string {
	normalized := map[string]string{}
	var order []string
	appendItem := func(value string) {
		for _, item := range splitAnthropicBetaHeader(value) {
			key := strings.ToLower(item)
			if _, exists := normalized[key]; !exists {
				normalized[key] = item
				order = append(order, item)
			}
		}
	}
	appendItem(current)
	for _, value := range required {
		appendItem(value)
	}
	return strings.Join(order, ",")
}

func splitAnthropicBetaHeader(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	output := make([]string, 0, len(parts))
	for _, item := range parts {
		trimmed := strings.TrimSpace(item)
		if trimmed != "" {
			output = append(output, trimmed)
		}
	}
	return output
}

// isClaudeCodeUserAgent 对齐 isClaudeCodeUserAgent。
func isClaudeCodeUserAgent(value string) bool {
	if value == "" {
		return false
	}
	normalized := strings.ToLower(value)
	return strings.HasPrefix(normalized, "claude-cli/") || strings.Contains(normalized, " claude-cli/")
}

// isClaudeCodeAnthropicRequestSignature 对齐：>= 2 个信号才判定为 Claude Code。
func isClaudeCodeAnthropicRequestSignature(r *http.Request) bool {
	signals := 0
	if isClaudeCodeUserAgent(r.Header.Get("User-Agent")) {
		signals++
	}
	if hasClaudeCodeBetaHeader(r) {
		signals++
	}
	if hasClaudeCodeSessionHeader(r) {
		signals++
	}
	if hasAnthropicBetaQuery(RequestPathAndQuery(r)) {
		signals++
	}
	return signals >= 2
}

func hasClaudeCodeBetaHeader(r *http.Request) bool {
	betaHeader := strings.ToLower(r.Header.Get(AnthropicBetaHeader))
	if betaHeader == "" {
		return false
	}
	for _, item := range splitAnthropicBetaHeader(betaHeader) {
		if strings.HasPrefix(item, "claude-code-") {
			return true
		}
	}
	return false
}

func hasClaudeCodeSessionHeader(r *http.Request) bool {
	return r.Header.Get(ClaudeCodeSessionIDHeader) != "" || r.Header.Get(ClaudeCodeAgentIDHeader) != ""
}

func hasAnthropicBetaQuery(pathAndQuery string) bool {
	parts := splitPathAndQuery(pathAndQuery)
	if parts.Query == "" {
		return false
	}
	params, err := url.ParseQuery(strings.TrimPrefix(parts.Query, "?"))
	if err != nil {
		return false
	}
	return params.Get("beta") == "true"
}

// parseGatewayClientProfileHeader 对齐：小写化并把连字符/空白换成下划线。
func parseGatewayClientProfileHeader(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	normalized := strings.ToLower(trimmed)
	return replaceAllRuns(normalized, "- \t\n\v\f\r", '_')
}

// replaceAllRuns 对齐 JS replace(/[-\s]+/g, '_')。
func replaceAllRuns(value string, charset string, replacement rune) string {
	var builder strings.Builder
	prevReplaced := false
	for _, r := range value {
		if strings.ContainsRune(charset, r) {
			if !prevReplaced {
				builder.WriteRune(replacement)
				prevReplaced = true
			}
			continue
		}
		builder.WriteRune(r)
		prevReplaced = false
	}
	return builder.String()
}

func firstHeaderValue(r *http.Request, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(r.Header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

// randomUUID 生成 RFC 4122 v4 UUID（对齐 node:crypto randomUUID）。
func randomUUID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(fmt.Sprintf("生成 Claude Code 会话 UUID 失败: %v", err))
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}
