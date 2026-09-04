package chat

import (
	"regexp"
	"strings"
)

// Public generation error taxonomy mirrors chat-generation-error.ts: the
// route layer surfaces every non-typed server fault as internal (or phase)
// generation failures with a sanitized diagnostic detail appended to the
// verbatim Chinese public message.

// PublicChatGenerationErrorCode mirrors PublicChatGenerationErrorCode.
type PublicChatGenerationErrorCode string

const (
	GenErrUpstreamHTTP          PublicChatGenerationErrorCode = "upstream_http_error"
	GenErrUpstreamStream        PublicChatGenerationErrorCode = "upstream_stream_failed"
	GenErrImageFailed           PublicChatGenerationErrorCode = "image_generation_failed"
	GenErrImageNotEnabled       PublicChatGenerationErrorCode = "image_generation_not_enabled"
	GenErrImagePermissionDenied PublicChatGenerationErrorCode = "image_generation_permission_denied"
	GenErrImageRateLimited      PublicChatGenerationErrorCode = "image_generation_rate_limited"
	GenErrImageRequestRejected  PublicChatGenerationErrorCode = "image_generation_request_rejected"
	GenErrStreamInterrupted     PublicChatGenerationErrorCode = "stream_interrupted"
	GenErrInternal              PublicChatGenerationErrorCode = "internal_generation_failed"
)

const maxPublicDiagnosticMessageLength = 1200

var publicChatGenerationMessages = map[PublicChatGenerationErrorCode]string{
	GenErrUpstreamHTTP:          "模型服务请求失败，请稍后重试",
	GenErrUpstreamStream:        "模型响应中断，请重新发送",
	GenErrImageFailed:           "图片生成失败，请重新发送",
	GenErrImageNotEnabled:       "图片生成失败：可用上游分组未开通图片生成功能",
	GenErrImagePermissionDenied: "图片生成失败：上游拒绝了图片生成权限",
	GenErrImageRateLimited:      "图片生成失败：上游请求过于频繁，请稍后重试",
	GenErrImageRequestRejected:  "图片生成失败：上游拒绝了本次图片参数或内容",
	GenErrStreamInterrupted:     "生成连接已中断，请重新发送",
	GenErrInternal:              "生成任务异常结束，请重新发送",
}

// PublicChatGenerationError mirrors PublicChatGenerationError.
type PublicChatGenerationError struct {
	Code    PublicChatGenerationErrorCode `json:"code"`
	Message string                        `json:"message"`
}

// ChatGenerationErrorMessage mirrors chatGenerationErrorMessage.
func ChatGenerationErrorMessage(code PublicChatGenerationErrorCode) string {
	return publicChatGenerationMessages[code]
}

var chatNetworkErrorCodePatterns = regexp.MustCompile(`^(?i)(ECONNABORTED|ECONNREFUSED|ECONNRESET|EHOSTUNREACH|ENETUNREACH|EPIPE|ETIMEDOUT|UND_ERR_CONNECT_TIMEOUT|UND_ERR_SOCKET)$`)

// ClassifyChatGenerationErrorByCode mirrors the rawCode branch of
// classifyChatGenerationError for stored error codes.
func ClassifyChatGenerationErrorByCode(rawCode string) PublicChatGenerationError {
	code := PublicChatGenerationErrorCode(strings.TrimSpace(rawCode))
	if _, ok := publicChatGenerationMessages[code]; !ok {
		return ClassifyUnknownChatGenerationError(nil)
	}
	return PublicChatGenerationError{Code: code, Message: publicChatGenerationMessages[code]}
}

// ClassifyUnknownChatGenerationError mirrors classifyChatGenerationError for
// Go errors: network-style sentinel messages map to upstream_stream_failed,
// everything else falls back to internal_generation_failed; the sanitized
// diagnostic detail rides along in both cases.
func ClassifyUnknownChatGenerationError(err error) PublicChatGenerationError {
	raw := ""
	if err != nil {
		raw = strings.TrimSpace(err.Error())
	}
	if chatNetworkErrorCodePatterns.MatchString(raw) {
		return PublicChatGenerationError{Code: GenErrUpstreamStream, Message: publicChatGenerationMessages[GenErrUpstreamStream]}
	}
	fallback := publicChatGenerationMessages[GenErrInternal]
	detail := sanitizeChatDiagnosticMessage(raw)
	if detail == "" || detail == fallback {
		return PublicChatGenerationError{Code: GenErrInternal, Message: fallback}
	}
	return PublicChatGenerationError{Code: GenErrInternal, Message: fallback + "；详情：" + detail}
}

var (
	chatDiagControlChars   = regexp.MustCompile(`[\x{0000}-\x{0008}\x{000b}\x{000c}\x{000e}-\x{001f}\x{007f}]`)
	chatDiagBearer         = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	chatDiagBasic          = regexp.MustCompile(`(?i)\bBasic\s+[A-Za-z0-9+/=]+`)
	chatDiagSkKey          = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}\b`)
	chatDiagJWT            = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
	chatDiagAssignedSecret = regexp.MustCompile(`(?i)(["']?(?:authorization|api[_-]?key|access[_-]?token|refresh[_-]?token|cookie|set-cookie|client[_-]?secret)["']?\s*[:=]\s*["']?)([^\s,;"']+)`)
	chatDiagQuerySecret    = regexp.MustCompile(`(?i)([?&](?:api[_-]?key|access[_-]?token|refresh[_-]?token|token|key|secret)=)[^&#\s]+`)
	chatDiagUserInURL      = regexp.MustCompile(`(?i)(https?://)[^\s/@]+:[^\s/@]+@`)
	chatDiagAnyURL         = regexp.MustCompile(`(?i)https?://[^\s]+`)
	chatDiagWindowsPath    = regexp.MustCompile(`\b[A-Za-z]:\\(?:[^\s<>:"|?*]+\\)*[^\s<>:"|?*]*`)
	chatDiagWhitespace     = regexp.MustCompile(`\s+`)
)

// sanitizeChatDiagnosticMessage mirrors sanitizeChatDiagnosticMessage with the
// same replacement order and redaction placeholders.
func sanitizeChatDiagnosticMessage(value string) string {
	if value == "" {
		return ""
	}
	sanitized := chatDiagControlChars.ReplaceAllString(value, " ")
	sanitized = chatDiagBearer.ReplaceAllString(sanitized, "Bearer [REDACTED]")
	sanitized = chatDiagBasic.ReplaceAllString(sanitized, "Basic [REDACTED]")
	sanitized = chatDiagSkKey.ReplaceAllString(sanitized, "sk-[REDACTED]")
	sanitized = chatDiagJWT.ReplaceAllString(sanitized, "[JWT REDACTED]")
	sanitized = chatDiagAssignedSecret.ReplaceAllString(sanitized, "$1[REDACTED]")
	sanitized = chatDiagQuerySecret.ReplaceAllString(sanitized, "$1[REDACTED]")
	sanitized = chatDiagUserInURL.ReplaceAllString(sanitized, "$1[REDACTED]@")
	sanitized = chatDiagAnyURL.ReplaceAllString(sanitized, "[upstream-url]")
	sanitized = chatDiagWindowsPath.ReplaceAllString(sanitized, "[server-path]")
	sanitized = chatDiagWhitespace.ReplaceAllString(sanitized, " ")
	sanitized = strings.TrimSpace(sanitized)
	if sanitized == "" {
		return ""
	}
	runes := []rune(sanitized)
	if len(runes) <= maxPublicDiagnosticMessageLength {
		return sanitized
	}
	return string(runes[:maxPublicDiagnosticMessageLength-1]) + "…"
}
