package gatewayusage

import (
	"regexp"
	"strings"
)

// Diagnostic payload sanitizer mirroring
// backend/src/modules/gateway/diagnostics/diagnostic-sanitizer.ts.

const diagnosticRedacted = "[redacted]"

const (
	diagnosticMaxRecursiveDepth = 8
	diagnosticMaxObjectKeys     = 200
	diagnosticMaxArrayItems     = 100
)

// sensitiveAssignmentKeyAlternatives documents the source alternation (kept
// for review parity with diagnostic-sanitizer.ts); the executable
// expansion is sensitiveAssignmentLiterals.
var sensitiveAssignmentKeyAlternatives = []string{
	"access[_-]?token",
	"api[_-]?key",
	"apikey",
	"authorization",
	"client[_-]?secret",
	"code[_-]?verifier",
	"cookie",
	"credential(?:s)?",
	"id[_-]?token",
	"key",
	"password",
	"proxy[_-]?authorization",
	"refresh[_-]?token",
	"secret",
	"session(?:id)?",
	"set[_-]?cookie",
	"token",
}

var diagnosticURLCredentialsPattern = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)([^/\s?#@]+)@`)
var diagnosticBearerPattern = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{8,}`)
var diagnosticSkPattern = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}`)
var diagnosticJuisPattern = regexp.MustCompile(`\bjuis_[A-Za-z0-9_-]{8,}`)
var diagnosticBareSensitivePattern = regexp.MustCompile(`(?i)\b(access[_-]?token|api[_-]?key|apikey|authorization|client[_-]?secret|code[_-]?verifier|cookie|credentials?|id[_-]?token|key|password|proxy[_-]?authorization|refresh[_-]?token|secret|session(?:id)?|set[_-]?cookie|token)(\s*(?:=|:)\s*)([^\s&;,)}\]]+)`)

var diagnosticSensitiveFieldNames = map[string]bool{
	"accesstoken":        true,
	"apikey":             true,
	"authorization":      true,
	"clientsecret":       true,
	"codeverifier":       true,
	"cookie":             true,
	"credential":         true,
	"credentials":        true,
	"idtoken":            true,
	"key":                true,
	"password":           true,
	"proxyauthorization": true,
	"refreshtoken":       true,
	"secret":             true,
	"session":            true,
	"sessionid":          true,
	"setcookie":          true,
	"token":              true,
}

// SanitizeDiagnosticString mirrors sanitizeDiagnosticPayload for the string
// inputs this slice feeds it (diagnostic messages): URL credentials, bearer
// tokens, sk-/juis- style keys and sensitive key=value / key="value"
// assignments are redacted.
func SanitizeDiagnosticString(value string) string {
	result := diagnosticURLCredentialsPattern.ReplaceAllString(value, `$1`+diagnosticRedacted+`@`)
	result = diagnosticBearerPattern.ReplaceAllString(result, "Bearer "+diagnosticRedacted)
	result = diagnosticSkPattern.ReplaceAllString(result, "sk-[redacted]")
	result = diagnosticJuisPattern.ReplaceAllString(result, "juis_[redacted]")
	result = replaceQuotedSensitiveAssignments(result)
	result = diagnosticBareSensitivePattern.ReplaceAllStringFunc(result, func(match string) string {
		groups := diagnosticBareSensitivePattern.FindStringSubmatch(match)
		if groups == nil {
			return match
		}
		return groups[1] + groups[2] + diagnosticRedacted
	})
	return result
}

// replaceQuotedSensitiveAssignments ports quotedSensitiveAssignmentPattern.
// The JS regex uses backreferences ((["'])…\1 and a \4 value-quote guard),
// which Go's RE2 engine does not support, so the match is walked manually
// with identical leftmost-first semantics.
func replaceQuotedSensitiveAssignments(value string) string {
	var out strings.Builder
	i := 0
	for i < len(value) {
		ch := value[i]
		if ch != '\'' && ch != '"' {
			out.WriteByte(ch)
			i++
			continue
		}
		if match, end := matchQuotedSensitiveAssignment(value, i); match != "" {
			out.WriteString(match)
			i = end
			continue
		}
		out.WriteByte(ch)
		i++
	}
	return out.String()
}

// matchQuotedSensitiveAssignment attempts one match starting at the quote
// index `start`. On success it returns the replacement text and the index
// just past the match.
func matchQuotedSensitiveAssignment(value string, start int) (string, int) {
	keyQuote := value[start]
	key, keyEnd := matchSensitiveKey(value, start+1)
	if key == "" {
		return "", 0
	}
	if keyEnd >= len(value) || value[keyEnd] != keyQuote {
		return "", 0
	}
	separatorEnd, separatorOK := matchAssignmentSeparator(value, keyEnd+1)
	if !separatorOK {
		return "", 0
	}
	if separatorEnd >= len(value) || (value[separatorEnd] != '\'' && value[separatorEnd] != '"') {
		return "", 0
	}
	valueQuote := value[separatorEnd]
	valueEnd, ok := scanQuotedValueEnd(value, separatorEnd+1, valueQuote)
	if !ok {
		return "", 0
	}
	var replacement strings.Builder
	replacement.WriteByte(keyQuote)
	replacement.WriteString(value[start+1 : keyEnd])
	replacement.WriteByte(keyQuote)
	replacement.WriteString(value[keyEnd+1 : separatorEnd])
	replacement.WriteByte(valueQuote)
	replacement.WriteString(diagnosticRedacted)
	replacement.WriteByte(valueQuote)
	return replacement.String(), valueEnd + 1
}

// sensitiveAssignmentLiterals expands each alternation into concrete literal
// candidates, preserving JS preference order (greedy optionals first). The
// outer list order is the source alternation order because JS alternation is
// leftmost-first, not longest-match.
var sensitiveAssignmentLiterals = expandSensitiveAssignmentLiterals()

func expandSensitiveAssignmentLiterals() [][]string {
	expand := func(parts ...[]string) []string {
		results := []string{""}
		for _, part := range parts {
			var next []string
			for _, prefix := range results {
				for _, option := range part {
					next = append(next, prefix+option)
				}
			}
			results = next
		}
		return results
	}
	optionalSeparator := []string{"_", "-", ""}
	optional := func(text string) []string { return []string{text, ""} }
	return [][]string{
		expand([]string{"access"}, optionalSeparator, []string{"token"}),
		expand([]string{"api"}, optionalSeparator, []string{"key"}),
		expand([]string{"apikey"}),
		expand([]string{"authorization"}),
		expand([]string{"client"}, optionalSeparator, []string{"secret"}),
		expand([]string{"code"}, optionalSeparator, []string{"verifier"}),
		expand([]string{"cookie"}),
		expand([]string{"credential"}, optional("s")),
		expand([]string{"id"}, optionalSeparator, []string{"token"}),
		expand([]string{"key"}),
		expand([]string{"password"}),
		expand([]string{"proxy"}, optionalSeparator, []string{"authorization"}),
		expand([]string{"refresh"}, optionalSeparator, []string{"token"}),
		expand([]string{"secret"}),
		expand([]string{"session"}, optional("id")),
		expand([]string{"set"}, optionalSeparator, []string{"cookie"}),
		expand([]string{"token"}),
	}
}

// matchSensitiveKey tries the alternation in source order at exactly
// position `start` (case-insensitive) and returns the matched key text and
// end index.
func matchSensitiveKey(value string, start int) (string, int) {
	rest := strings.ToLower(value[start:])
	for _, candidates := range sensitiveAssignmentLiterals {
		for _, candidate := range candidates {
			if strings.HasPrefix(rest, candidate) {
				return value[start : start+len(candidate)], start + len(candidate)
			}
		}
	}
	return "", 0
}

// matchAssignmentSeparator mirrors (\s*(?:=|:)\s*): whitespace, one '=' or
// ':' around whitespace. Returns the end index, or start when no separator
// matches.
func matchAssignmentSeparator(value string, start int) (int, bool) {
	i := start
	for i < len(value) && isASCIIWhitespace(value[i]) {
		i++
	}
	if i >= len(value) || (value[i] != '=' && value[i] != ':') {
		return start, false
	}
	i++
	for i < len(value) && isASCIIWhitespace(value[i]) {
		i++
	}
	return i, true
}

// scanQuotedValueEnd mirrors (?:\\.|(?!\4)[^\\])*: escaped pairs are
// consumed, the unescaped value quote terminates, a lone backslash before
// anything else fails the match.
func scanQuotedValueEnd(value string, start int, valueQuote byte) (int, bool) {
	i := start
	for i < len(value) {
		ch := value[i]
		if ch == '\\' {
			if i+1 >= len(value) {
				return 0, false
			}
			i += 2
			continue
		}
		if ch == valueQuote {
			return i, true
		}
		i++
	}
	return 0, false
}

func isASCIIWhitespace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '\v' || ch == '\f'
}

// SanitizeDiagnosticValue mirrors the generic sanitizeDiagnosticPayload walk
// over arbitrary JSON-like values: sensitive field names redact the whole
// value, strings are sanitized, arrays cap at 100 items, objects cap at 200
// keys and the walk stops at depth 8.
func SanitizeDiagnosticValue(value any) any {
	return sanitizeDiagnosticValue(value, "", 0)
}

func sanitizeDiagnosticValue(value any, fieldName string, depth int) any {
	if isSensitiveDiagnosticFieldName(fieldName) {
		return diagnosticRedacted
	}
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return SanitizeDiagnosticString(typed)
	case bool, int, int64, float64, uint64:
		return value
	case []any:
		if depth >= diagnosticMaxRecursiveDepth {
			return "[truncated]"
		}
		limit := len(typed)
		truncated := false
		if limit > diagnosticMaxArrayItems {
			limit = diagnosticMaxArrayItems
			truncated = true
		}
		output := make([]any, 0, limit+1)
		for _, item := range typed[:limit] {
			output = append(output, sanitizeDiagnosticValue(item, "", depth+1))
		}
		if truncated {
			output = append(output, "[truncated:"+itoa(len(typed))+"]")
		}
		return output
	case map[string]any:
		if depth >= diagnosticMaxRecursiveDepth {
			return "[truncated]"
		}
		output := make(map[string]any, len(typed))
		count := 0
		for key, item := range typed {
			if count >= diagnosticMaxObjectKeys {
				output["__truncated__"] = true
				break
			}
			output[key] = sanitizeDiagnosticValue(item, key, depth+1)
			count++
		}
		return output
	default:
		return value
	}
}

func isSensitiveDiagnosticFieldName(name string) bool {
	if name == "" {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(name))
	var kept strings.Builder
	for _, r := range normalized {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			kept.WriteRune(r)
		}
	}
	return diagnosticSensitiveFieldNames[kept.String()]
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	pos := len(digits)
	negative := value < 0
	v := value
	for v != 0 {
		d := v % 10
		if d < 0 {
			d = -d
		}
		pos--
		digits[pos] = byte('0' + d)
		v /= 10
	}
	if negative {
		pos--
		digits[pos] = '-'
	}
	return string(digits[pos:])
}
