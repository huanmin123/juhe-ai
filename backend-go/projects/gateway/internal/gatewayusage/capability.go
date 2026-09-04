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

// UsageServiceTierFacts mirrors UsageServiceTierFacts (service-tier.ts).
type UsageServiceTierFacts struct {
	RequestedServiceTier  UsageServiceTier
	EffectiveServiceTier  UsageServiceTier
	ReportedServiceTier   UsageServiceTier // optional; empty = undefined
	BilledServiceTier     UsageServiceTier
	HasReportedTier       bool
}

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

// ResolveUsageServiceTiers mirrors resolveUsageServiceTiers: effective
// defaults to requested, billed defaults to reported then effective.
func ResolveUsageServiceTiers(input ResolveUsageServiceTiersInput) UsageServiceTierFacts {
	requested := input.RequestedServiceTier
	if requested == "" {
		requested = "default"
	}
	effective := input.EffectiveServiceTier
	if effective == "" {
		effective = requested
	}
	reported := input.ReportedServiceTier
	billed := reported
	if billed == "" {
		billed = effective
	}
	return UsageServiceTierFacts{
		RequestedServiceTier: requested,
		EffectiveServiceTier: effective,
		ReportedServiceTier:  reported,
		HasReportedTier:      reported != "",
		BilledServiceTier:    billed,
	}
}

// ResolveUsageServiceTiersInput mirrors the resolveUsageServiceTiers input
// object; empty strings mean undefined.
type ResolveUsageServiceTiersInput struct {
	RequestedServiceTier UsageServiceTier
	EffectiveServiceTier UsageServiceTier
	ReportedServiceTier  UsageServiceTier
}
