package openaicompat

import "strings"

// bridgeEstimateTokenCountFromText is a local copy of the archived Node
// estimateTokenCountFromText (gateway/protocols/openai-v1/stream-events.ts;
// Go already ports it as gatewayopenai.EstimateTokenCountFromText). A local
// copy avoids the gatewayopenai -> openaicompat import cycle in the bridge
// packages. CJK chars count one token each, ASCII four per token, other runes
// two per token; empty text estimates 0.
func bridgeEstimateTokenCountFromText(text string) int {
	if strings.TrimSpace(text) == "" {
		return 0
	}
	asciiLikeChars := 0
	cjkChars := 0
	otherChars := 0
	for _, char := range text {
		code := int(char)
		switch {
		case bridgeIsCjkCodePoint(code):
			cjkChars++
		case code <= 0x7f:
			asciiLikeChars++
		default:
			otherChars++
		}
	}
	total := bridgeCeilDiv(asciiLikeChars, 4) + cjkChars + bridgeCeilDiv(otherChars, 2)
	if total < 1 {
		return 1
	}
	return total
}

func bridgeCeilDiv(value, divisor int) int {
	if divisor <= 0 {
		return 0
	}
	return (value + divisor - 1) / divisor
}

func bridgeIsCjkCodePoint(code int) bool {
	return (code >= 0x3400 && code <= 0x9fff) ||
		(code >= 0xf900 && code <= 0xfaff) ||
		(code >= 0x20000 && code <= 0x2ebef) ||
		(code >= 0x2f800 && code <= 0x2fa1f)
}
