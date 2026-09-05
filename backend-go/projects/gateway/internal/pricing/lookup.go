package pricing

// Provider model pricing lookup closure, ported from
// backend/src/modules/model-pricing/model-pricing.service.ts
// (listProviderModelPricing / getProviderModelPricing /
// findProviderModelPricing). This is the built-in snapshot lookup the
// catalog assembly path uses; runtime catalog consumption
// (model-catalog.service.ts findCatalogItem) is a plain trim-equality
// match over merged rows and never applies candidates/alias fallback.

import (
	"sort"
	"strings"
)

// ListProviderModelPricing mirrors listProviderModelPricing: every
// non-shutdown snapshot row of the provider as of today (UTC), sorted by
// compareProviderModels (catalog order, release date desc, model asc).
// providerCode is normalized like normalizeProviderToken; unknown
// providers yield an empty list.
func ListProviderModelPricing(providerCode string) []*Pricing {
	return ListProviderModelPricingAsOf(providerCode, currentUTCDate())
}

// ListProviderModelPricingAsOf mirrors listProviderModelPricingAsOf with an
// explicit shutdown cutoff (ISO date, compared as a string).
func ListProviderModelPricingAsOf(providerCode string, asOfDate string) []*Pricing {
	normalized := normalizeProviderToken(providerCode)
	if normalized == "" {
		return nil
	}
	entry := providerEntryFor(normalized)
	if entry == nil || len(entry.rawModels) == 0 {
		return nil
	}
	out := make([]*Pricing, 0, len(entry.rawModels))
	for index := range entry.rawModels {
		item := &entry.rawModels[index]
		if hasModelShutdown(item, asOfDate) {
			continue
		}
		out = append(out, toProviderModelPricing(item, entry, normalized))
	}
	sort.SliceStable(out, func(i, j int) bool { return compareProviderModels(out[i], out[j]) < 0 })
	return out
}

// FindProviderModelPricing mirrors getProviderModelPricing + the
// findProviderModelPricing lookup closure: exact normalized name first,
// then the per-provider candidate list (each candidate canonicalized via
// the OpenAI alias rule), then the canonical alias of the requested model
// itself. Shutdown rows are skipped; unavailable OpenAI models never
// resolve. Returns nil for unknown providers/models.
func FindProviderModelPricing(providerCode string, model string) *Pricing {
	return FindProviderModelPricingAsOf(providerCode, model, currentUTCDate())
}

// FindProviderModelPricingAsOf is FindProviderModelPricing with an explicit
// shutdown cutoff.
func FindProviderModelPricingAsOf(providerCode string, model string, asOfDate string) *Pricing {
	normalized := normalizeProviderToken(providerCode)
	if normalized == "" || model == "" {
		return nil
	}
	entry := providerEntryFor(normalized)
	if entry == nil || len(entry.rawModels) == 0 {
		return nil
	}
	raw := findRawProviderModelPricing(entry, normalizeWhitespace(model), asOfDate)
	if raw == nil {
		return nil
	}
	return toProviderModelPricing(raw, entry, normalized)
}

// findRawProviderModelPricing mirrors findProviderModelPricing
// (model-pricing.service.ts:158-185) over the driver's raw rows.
func findRawProviderModelPricing(entry *providerEntry, normalized string, asOfDate string) *rawModel {
	if normalized == "" {
		return nil
	}
	if entry.isUnavailableModel(normalized) {
		return nil
	}

	for index := range entry.rawModels {
		item := &entry.rawModels[index]
		if normalizeWhitespace(item.Model) == normalized && !hasModelShutdown(item, asOfDate) {
			return item
		}
	}

	for _, candidate := range entry.buildModelCandidates(normalized) {
		normalizedCandidate := canonicalOpenAIModelAlias(candidate)
		for index := range entry.rawModels {
			item := &entry.rawModels[index]
			if normalizeWhitespace(item.Model) == normalizedCandidate && !hasModelShutdown(item, asOfDate) {
				return item
			}
		}
	}

	if canonicalAlias := canonicalOpenAIModelAlias(normalized); canonicalAlias != normalized {
		for index := range entry.rawModels {
			item := &entry.rawModels[index]
			if normalizeWhitespace(item.Model) == canonicalAlias && !hasModelShutdown(item, asOfDate) {
				return item
			}
		}
	}

	return nil
}

// normalizeWhitespace mirrors normalizeModel: the Node lookup only trims.
func normalizeWhitespace(value string) string {
	return strings.TrimSpace(value)
}
