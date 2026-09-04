package gatewayaccounteffects

import (
	"strings"
	"time"
)

// trimSpace mirrors String.prototype.trim (Unicode whitespace).
func trimSpace(value string) string {
	return strings.TrimFunc(value, isUnicodeSpace)
}

func isUnicodeSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x85, 0xA0, 0x1680, 0x2028, 0x2029, 0x205F, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// timeParseRFC3339 parses the strict RFC3339 instant shape validated by
// rfc3339InstantPattern (offset mandatory).
func timeParseRFC3339(text string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, text)
}

// canonicalRFC3339 mirrors Date.prototype.toISOString(): UTC with exactly
// three fractional digits.
func canonicalRFC3339(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}
