package gatewayresponse

import (
	"regexp"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// 路径模式（non-stream-json-inspection.ts / finalization.ts）。

var (
	audioPathPattern = regexp.MustCompile(`/audio/(?:transcriptions|translations)$`)
	// managementPrefixPattern 对齐 /\/(?:batches|fine_tuning|vector_stores)(?:\/|$)/。
	managementPrefixPattern = regexp.MustCompile(`/(?:batches|fine_tuning|vector_stores)(?:/|$)`)
	// filesRootPattern 对齐 /^(?:\/v1)?\/files(?:\/|$)/。
	filesRootPattern = regexp.MustCompile(`^(?:/v1)?/files(?:/|$)`)
	// v1PrefixPattern 对齐 /^\/v1(?=\/|$)/ 的 replace 语义。
	v1PrefixPattern = regexp.MustCompile(`^/v1(/|$)`)
	// v1betaPrefixPattern 对齐 /^\/v1beta(?=\/|$)/ 的 replace 语义。
	v1betaPrefixPattern = regexp.MustCompile(`^/v1beta(/|$)`)
)

// normalizeV1PrefixPath 对齐 `requestPath.replace(/^\/v1(?=\/|$)/, '') || '/'`。
// Go 不支持 lookahead：仅当前缀后是结尾或 '/' 时剥离，并保留 '/'。
func normalizeV1PrefixPath(requestPath string) string {
	if match := v1PrefixPattern.FindStringSubmatchIndex(requestPath); match != nil {
		return "/" + requestPath[match[1]:]
	}
	if requestPath == "" {
		return "/"
	}
	return requestPath
}

// normalizeV1BetaPrefixPath 对齐 `replace(/^\/v1beta(?=\/|$)/, '') || '/'`。
func normalizeV1BetaPrefixPath(requestPath string) string {
	if match := v1betaPrefixPattern.FindStringSubmatchIndex(requestPath); match != nil {
		return "/" + requestPath[match[1]:]
	}
	if requestPath == "" {
		return "/"
	}
	return requestPath
}

// IsOpenAIStreamContentType 转发 gatewaypreauth 的同名判定。
func IsOpenAIStreamContentType(contentType string) bool {
	return gatewaypreauth.IsOpenAIStreamContentType(contentType)
}

// LowercasedRequestPath 对齐 `(originalUrl || path).split('?', 1)[0]
// .toLowerCase()`。
func LowercasedRequestPath(originalPathAndQuery string) string {
	path, _, _ := strings.Cut(originalPathAndQuery, "?")
	return strings.ToLower(path)
}

func lowerASCII(value string) string { return strings.ToLower(value) }

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
