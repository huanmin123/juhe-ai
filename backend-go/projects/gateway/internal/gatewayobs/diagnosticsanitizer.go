package gatewayobs

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// 诊断脱敏，逐字节对齐
// backend/src/modules/gateway/diagnostics/diagnostic-sanitizer.ts。
//
// JS 正则里的反向引用/负向先行（引号赋值模式）RE2 不支持，改为等价的手写
// 扫描器；其余模式用 RE2 并显式拼入 ECMAScript \s 字符类，保证与 JS 语义
// 一致。

const diagnosticRedacted = "[redacted]"

const diagnosticMaxRecursiveDepth = 8
const diagnosticMaxObjectKeys = 200
const diagnosticMaxArrayItems = 100

// jsSpaceSet 是 ECMAScript \s 的等价字符集内容（RE2 语法，不含方括号）；
// jsSpaceClass 是可直接独立使用的字符类。
const jsSpaceSet = `\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}`
const jsSpaceClass = "[" + jsSpaceSet + "]"

// sensitiveAssignmentKeyPattern 原文（有序备选，保留顺序语义）。
const sensitiveAssignmentKeyPattern = "access[_-]?token|api[_-]?key|apikey|authorization|client[_-]?secret|code[_-]?verifier|cookie|credential(?:s)?|id[_-]?token|key|password|proxy[_-]?authorization|refresh[_-]?token|secret|session(?:id)?|set[_-]?cookie|token"

var (
	// quotedSensitiveAssignmentPattern 的 key 段整段匹配（模式里 key 被
	// 同种引号包裹，段必须恰为一个备选）。
	sensitiveKeySegmentPattern = regexp.MustCompile(`^(?:` + sensitiveAssignmentKeyPattern + `)$`)
	// (?i) 显式大写：等价 JS /i。
	urlUserinfoPattern = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)([^/` + jsSpaceSet + `?#@]+)@`)
	bearerPattern      = regexp.MustCompile(`(?i)\bBearer(` + jsSpaceClass + `+)[A-Za-z0-9._~+/=-]{8,}`)
	skTokenPattern     = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}`)
	juisTokenPattern   = regexp.MustCompile(`\bjuis_[A-Za-z0-9_-]{8,}`)
	// bareSensitiveAssignmentPattern：\b + 有序备选 + 分隔符 + 值。
	bareSensitiveAssignmentPattern = regexp.MustCompile(`(?i)\b(` + sensitiveAssignmentKeyPattern + `)(` + jsSpaceClass + `*(?:=|:)` + jsSpaceClass + `*)([^` + jsSpaceSet + `&;,)}\]]+)`)
)

var sensitiveFieldNames = map[string]struct{}{
	"accesstoken":        {},
	"apikey":             {},
	"authorization":      {},
	"clientsecret":       {},
	"codeverifier":       {},
	"cookie":             {},
	"credential":         {},
	"credentials":        {},
	"idtoken":            {},
	"key":                {},
	"password":           {},
	"proxyauthorization": {},
	"refreshtoken":       {},
	"secret":             {},
	"session":            {},
	"sessionid":          {},
	"setcookie":          {},
	"token":              {},
}

// SanitizeDiagnosticPayload mirrors sanitizeDiagnosticPayload. Objects decode
// 为 map[string]interface{} / []interface{}（JSON 形状）；其他类型原样返回。
func SanitizeDiagnosticPayload(value interface{}) interface{} {
	return sanitizeDiagnosticValue(value, "", false, 0)
}

func sanitizeDiagnosticValue(value interface{}, fieldName string, hasFieldName bool, depth int) interface{} {
	if hasFieldName && isSensitiveDiagnosticFieldName(fieldName) {
		return diagnosticRedacted
	}
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return sanitizeSensitiveString(typed)
	case []interface{}:
		if depth >= diagnosticMaxRecursiveDepth {
			return "[truncated]"
		}
		limit := len(typed)
		truncatedMarker := ""
		if len(typed) > diagnosticMaxArrayItems {
			limit = diagnosticMaxArrayItems
			truncatedMarker = "[truncated:" + strconv.Itoa(len(typed)-diagnosticMaxArrayItems) + "]"
		}
		output := make([]interface{}, 0, limit+1)
		for _, item := range typed[:limit] {
			output = append(output, sanitizeDiagnosticValue(item, "", false, depth+1))
		}
		if truncatedMarker != "" {
			output = append(output, truncatedMarker)
		}
		return output
	case map[string]interface{}:
		if depth >= diagnosticMaxRecursiveDepth {
			return "[truncated]"
		}
		output := make(map[string]interface{}, len(typed))
		count := 0
		truncated := false
		for key, item := range typed {
			if count >= diagnosticMaxObjectKeys {
				truncated = true
				break
			}
			output[key] = sanitizeDiagnosticValue(item, key, true, depth+1)
			count += 1
		}
		if truncated {
			output["__truncated__"] = true
		}
		return output
	default:
		return value
	}
}

// sanitizeSensitiveString mirrors sanitizeSensitiveString：六段替换按 Node
// 源码顺序依次应用。
func sanitizeSensitiveString(value string) string {
	// 1. URL userinfo: /\b([a-z][a-z0-9+.-]*:\/\/)([^/\s?#@]+)@/gi -> $1[redacted]@
	value = urlUserinfoPattern.ReplaceAllString(value, "${1}"+diagnosticRedacted+"@")
	// 2. /\bBearer\s+[A-Za-z0-9._~+/=-]{8,}/gi -> `Bearer [redacted]`
	value = bearerPattern.ReplaceAllString(value, "Bearer "+diagnosticRedacted)
	// 3. /\bsk-[A-Za-z0-9_-]{8,}/g -> sk-[redacted]
	value = skTokenPattern.ReplaceAllString(value, "sk-"+diagnosticRedacted)
	// 4. /\bjuis_[A-Za-z0-9_-]{8,}/g -> juis_[redacted]
	value = juisTokenPattern.ReplaceAllString(value, "juis_"+diagnosticRedacted)
	// 5. 引号赋值模式（手写扫描器，见 redactQuotedSensitiveAssignments）。
	value = redactQuotedSensitiveAssignments(value)
	// 6. 裸赋值模式 -> $1$2[redacted]
	value = bareSensitiveAssignmentPattern.ReplaceAllString(value, "${1}${2}"+diagnosticRedacted)
	return value
}

func isSensitiveDiagnosticFieldName(name string) bool {
	if name == "" {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(name))
	var builder strings.Builder
	for _, r := range normalized {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
		}
	}
	normalized = builder.String()
	_, sensitive := sensitiveFieldNames[normalized]
	return sensitive
}

// ---------------------------------------------------------------------------
// 引号赋值模式（RE2 无反向引用，手写扫描器）
// ---------------------------------------------------------------------------

// redactQuotedSensitiveAssignments implements quotedSensitiveAssignmentPattern
// (gi): (["'])(KEY)\1(\s*:\s*)(["'])(?:\\.|(?!\4)[^\\])*\4 → key 段与分隔符
// 原样保留，值替换为 [redacted]。
func redactQuotedSensitiveAssignments(value string) string {
	var out strings.Builder
	out.Grow(len(value))
	i := 0
	for i < len(value) {
		if c := value[i]; c == '\'' || c == '"' {
			if replacement, end, ok := matchQuotedSensitiveAssignment(value, i); ok {
				out.WriteString(replacement)
				i = end
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(value[i:])
		out.WriteString(value[i : i+size])
		i += size
	}
	return out.String()
}

// matchQuotedSensitiveAssignment tries the quoted pattern at s[start] where
// s[start] is a single or double quote; on success it returns the replacement
// text and the index just past the match.
func matchQuotedSensitiveAssignment(s string, start int) (string, int, bool) {
	keyQuote := s[start]
	closeIndex := strings.IndexByte(s[start+1:], keyQuote)
	if closeIndex < 0 {
		return "", 0, false
	}
	key := s[start+1 : start+1+closeIndex]
	if !sensitiveKeySegmentPattern.MatchString(key) {
		return "", 0, false
	}
	pos := start + 1 + closeIndex + 1
	separatorStart := pos
	pos = skipJSWhitespace(s, pos)
	if pos >= len(s) || s[pos] != ':' {
		return "", 0, false
	}
	pos += 1
	pos = skipJSWhitespace(s, pos)
	separator := s[separatorStart:pos]
	if pos >= len(s) {
		return "", 0, false
	}
	valueQuote := s[pos]
	if valueQuote != '\'' && valueQuote != '"' {
		return "", 0, false
	}
	t := pos + 1
	for {
		if t >= len(s) {
			return "", 0, false
		}
		b := s[t]
		if b == '\\' {
			// \\. 不能跨越行终止符。
			if t+1 >= len(s) || isJSLineTerminatorAt(s, t+1) {
				return "", 0, false
			}
			_, size := utf8.DecodeRuneInString(s[t+1:])
			t += 1 + size
			continue
		}
		if b == valueQuote {
			replacement := string(keyQuote) + key + string(keyQuote) + separator + string(valueQuote) + diagnosticRedacted + string(valueQuote)
			return replacement, t + 1, true
		}
		// (?!\4)[^\\]：除反斜杠与值引号外的任意字符（含行终止符）。
		_, size := utf8.DecodeRuneInString(s[t:])
		t += size
	}
}

func skipJSWhitespace(s string, index int) int {
	for index < len(s) {
		r, size := utf8.DecodeRuneInString(s[index:])
		if !isJSWhitespaceRune(r) {
			break
		}
		index += size
	}
	return index
}

func isJSWhitespaceRune(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x00A0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

func isJSLineTerminatorAt(s string, index int) bool {
	switch s[index] {
	case '\n', '\r':
		return true
	case 0xe2: // U+2028/U+2029 的首字节
		return index+2 < len(s) && s[index+1] == 0x80 && (s[index+2] == 0xa8 || s[index+2] == 0xa9)
	}
	return false
}
