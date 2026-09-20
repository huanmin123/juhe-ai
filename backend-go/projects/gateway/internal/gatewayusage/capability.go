package gatewayusage

import (
	"regexp"
	"strings"
)

// UsageServiceTier mirrors the Node UsageServiceTier type: a capability
// token string ('default', 'flex', 'priority', ...).
type UsageServiceTier = string

// UsageReasoningEffort mirrors the Node UsageReasoningEffort type.
type UsageReasoningEffort = string

// 2026-09-20 死代码清理：UsageServiceTierFacts / ResolveUsageServiceTiers /
// ResolveUsageServiceTiersInput 已删除——全仓零生产调用，仅专属表驱动测试
// TestResolveUsageServiceTiers（usage_test.go）引用，测试一并删除。

// usageCapabilityTokenPattern mirrors /^[a-z0-9][a-z0-9._-]{0,63}$/i.
var usageCapabilityTokenPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// NormalizeUsageCapabilityToken mirrors normalizeUsageCapabilityToken: the
// value must be a string with no surrounding whitespace and match the
// capability token shape; anything else is undefined (empty string here).
func NormalizeUsageCapabilityToken(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	// `value !== value.trim()` rejects values with surrounding whitespace.
	if text != strings.TrimSpace(text) {
		return ""
	}
	if !usageCapabilityTokenPattern.MatchString(text) {
		return ""
	}
	return text
}

// normalizeOptionalUsageServiceTier mirrors normalizeOptionalUsageServiceTier.
func normalizeOptionalUsageServiceTier(value any) UsageServiceTier {
	return NormalizeUsageCapabilityToken(value)
}

// NormalizeUsageServiceTier mirrors normalizeUsageServiceTier: undefined
// input falls back to 'default'.
func NormalizeUsageServiceTier(value any) UsageServiceTier {
	if normalized := normalizeOptionalUsageServiceTier(value); normalized != "" {
		return normalized
	}
	return "default"
}

// NormalizeUsageReasoningEffort mirrors normalizeUsageReasoningEffort.
func NormalizeUsageReasoningEffort(value any) UsageReasoningEffort {
	return NormalizeUsageCapabilityToken(value)
}
