package gatewaybody

import "regexp"

// Usage capability-token normalization, mirroring the consumed surface of
// usage/reasoning-effort.ts and usage/service-tier.ts:
//
//	normalizeUsageReasoningEffort       = normalizeUsageCapabilityToken
//	normalizeOptionalUsageServiceTier   = normalizeUsageCapabilityToken
//	normalizeUsageServiceTier(value)    = normalizeUsageCapabilityToken ?? 'default'
//
// A token must equal its own trim() and match /^[a-z0-9][a-z0-9._-]{0,63}$/i.
var usageCapabilityTokenPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// NormalizeUsageReasoningEffort mirrors normalizeUsageReasoningEffort for a
// decoded string token.
func NormalizeUsageReasoningEffort(value string) (string, bool) {
	return normalizeUsageCapabilityToken(value)
}

// NormalizeUsageReasoningEffortValue mirrors normalizeUsageReasoningEffort
// for a decoded JSON value (non-strings are rejected).
func NormalizeUsageReasoningEffortValue(value any) (string, bool) {
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	return normalizeUsageCapabilityToken(text)
}

// NormalizeOptionalUsageServiceTier mirrors normalizeOptionalUsageServiceTier.
func NormalizeOptionalUsageServiceTier(value string) (string, bool) {
	return normalizeUsageCapabilityToken(value)
}

// NormalizeUsageServiceTierValue mirrors normalizeUsageServiceTier: values
// outside the token grammar normalize to "default".
func NormalizeUsageServiceTierValue(value any) string {
	if text, ok := value.(string); ok {
		if normalized, ok := normalizeUsageCapabilityToken(text); ok {
			return normalized
		}
	}
	return "default"
}

func normalizeUsageCapabilityToken(value string) (string, bool) {
	if value != trimJSSpace(value) {
		return "", false
	}
	if !usageCapabilityTokenPattern.MatchString(value) {
		return "", false
	}
	return value, true
}
