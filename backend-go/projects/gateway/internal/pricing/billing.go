package pricing

// Provider billing engine, ported from
// backend/src/modules/model-pricing/provider-billing.{types,shared,policies,
// registry,service}.ts:
//
//   - BuildCostBreakdown        = buildProviderBillingCostBreakdown
//   - EstimateProviderCostUsd   = estimateProviderCostUsd
//   - EstimateProviderCacheWriteCostUsd / EstimateProviderCacheReadCostUsd
//   - per-provider policies (openai/anthropic/gemini/xai/deepseek/glm)
//     with tier-specific rates (priority/flex/batch), cache split into
//     input/cache_write/cache_write_1h/cache_read, long-context
//     multipliers, image/audio line items and the costUsd override.
//
// The catalog display surface (buildCatalogDisplay /
// buildProviderCatalogDisplay) is presentation-only and stays deferred, so
// ProviderBillingPolicy.buildCatalogDisplay has no counterpart here.

import (
	"math"
	"strings"
)

// tokenBillingLabels mirrors provider-billing.shared TokenBillingLabels.
type tokenBillingLabels struct {
	input           string
	output          string
	cacheRead       string
	cacheWrite      string
	cacheWrite1h    string
	imageInput      string
	imageOutput     string
	audioInput      string
	audioOutput     string
	imageOutputUnit string
}

// defaultTokenBillingLabels mirrors defaultTokenBillingLabels.
var defaultTokenBillingLabels = tokenBillingLabels{
	input:           "输入 Token",
	output:          "输出 Token",
	cacheRead:       "缓存读 Token",
	cacheWrite:      "缓存写入 Token",
	cacheWrite1h:    "1h 缓存写入 Token",
	imageInput:      "图片输入 Token",
	imageOutput:     "图片输出 Token",
	audioInput:      "音频输入 Token",
	audioOutput:     "音频输出 Token",
	imageOutputUnit: "输出图片",
}

// tokenBillingOptions mirrors provider-billing.shared TokenBillingOptions.
type tokenBillingOptions struct {
	cacheReadIncludedInInput bool
	cacheReadFallbackToInput bool
	labels                   tokenBillingLabels
}

// resolvedRates mirrors provider-billing.types ResolvedProviderBillingRates.
type resolvedRates struct {
	PriceSet
	serviceTierPricingSource ServiceTierPricingSource
	serviceTierMultiplier    *float64
}

// billingPolicy mirrors the per-provider buildCostBreakdown differences.
// The display half of the Node ProviderBillingPolicy interface is not
// ported (presentation-only).
type billingPolicy struct {
	id                       string
	applyLongContext         bool
	rejectUnsupportedTier    bool
	cacheReadIncludedInInput bool
	cacheReadFallbackToInput bool
	labels                   tokenBillingLabels
}

// billingPolicies mirrors the six provider-billing.policies.ts policies.
var billingPolicies = []*billingPolicy{
	{
		id:                       "openai",
		applyLongContext:         true,
		cacheReadIncludedInInput: true,
		cacheReadFallbackToInput: true,
		labels:                   defaultTokenBillingLabels,
	},
	{
		id:                    "anthropic",
		rejectUnsupportedTier: true,
		labels: tokenBillingLabels{
			input:           "输入 Token",
			output:          "输出 Token",
			cacheRead:       "缓存读 Token",
			cacheWrite:      "5m 缓存写入 Token",
			cacheWrite1h:    "1h 缓存写入 Token",
			imageInput:      "图片输入 Token",
			imageOutput:     "图片输出 Token",
			audioInput:      "音频输入 Token",
			audioOutput:     "音频输出 Token",
			imageOutputUnit: "输出图片",
		},
	},
	{
		id:                       "gemini",
		applyLongContext:         true,
		cacheReadIncludedInInput: true,
		labels: tokenBillingLabels{
			input:           "输入 Token",
			output:          "输出 Token",
			cacheRead:       "缓存读 Token",
			cacheWrite:      "缓存写入 Token",
			cacheWrite1h:    "1h 缓存写入 Token",
			imageInput:      "图片输入 Token",
			imageOutput:     "图片输出 Token",
			audioInput:      "音频输入 Token",
			audioOutput:     "音频输出 Token",
			imageOutputUnit: "输出图片",
		},
	},
	{
		id:                       "xai",
		applyLongContext:         true,
		cacheReadIncludedInInput: true,
		labels:                   defaultTokenBillingLabels,
	},
	{
		id:                       "deepseek",
		rejectUnsupportedTier:    true,
		cacheReadIncludedInInput: true,
		labels: tokenBillingLabels{
			input:           "输入 Token",
			output:          "输出 Token",
			cacheRead:       "缓存读 Token",
			cacheWrite:      "缓存写入 Token",
			cacheWrite1h:    "1h 缓存写入 Token",
			imageInput:      "图片输入 Token",
			imageOutput:     "图片输出 Token",
			audioInput:      "音频输入 Token",
			audioOutput:     "音频输出 Token",
			imageOutputUnit: "输出图片",
		},
	},
	{
		id:                       "glm",
		rejectUnsupportedTier:    true,
		cacheReadIncludedInInput: true,
		labels: tokenBillingLabels{
			input:           "输入 Token",
			output:          "输出 Token",
			cacheRead:       "缓存读 Token",
			cacheWrite:      "缓存写入 Token",
			cacheWrite1h:    "1h 缓存写入 Token",
			imageInput:      "图片输入 Token",
			imageOutput:     "图片输出 Token",
			audioInput:      "音频输入 Token",
			audioOutput:     "音频输出 Token",
			imageOutputUnit: "输出图片",
		},
	},
}

// billingPolicyForProvider mirrors providerBillingPolicyForProvider
// (provider-billing.registry.ts): the driver registry resolves the
// provider token, the policy rides on the driver.
func billingPolicyForProvider(providerCode string) *billingPolicy {
	entry := providerEntryFor(providerCode)
	if entry == nil {
		return nil
	}
	for _, policy := range billingPolicies {
		if policy.id == entry.billingPolicy {
			return policy
		}
	}
	return nil
}

// BuildCostBreakdown mirrors buildProviderBillingCostBreakdown: the policy
// is selected by pricing.ProviderCode (never the input's), and the Node
// output always stamps currency 'USD' plus the policy id. nil mirrors the
// Node undefined breakdown (unknown provider, unsupported tier or unpriced
// usage).
func BuildCostBreakdown(pricing *Pricing, input CostInput) *CostBreakdown {
	if pricing == nil {
		return nil
	}
	policy := billingPolicyForProvider(pricing.ProviderCode)
	if policy == nil {
		return nil
	}
	breakdown := policy.buildCostBreakdown(pricing, input)
	if breakdown == nil {
		return nil
	}
	breakdown.Currency = "USD"
	breakdown.BillingPolicy = policy.id
	return breakdown
}

// buildCostBreakdown mirrors the per-provider policy bodies in
// provider-billing.policies.ts.
func (p *billingPolicy) buildCostBreakdown(pricing *Pricing, input CostInput) *CostBreakdown {
	if p.rejectUnsupportedTier && hasUnsupportedServiceTier(pricing, input) {
		return nil
	}
	rates := serviceTierRates(pricing, input)
	if p.applyLongContext {
		rates = applyLongContextRates(pricing, input, rates)
	}
	billable := input
	if pricing.Mode == "image_generation" &&
		rates.ImageOutputUsdPer1M != nil && input.OutputImageTokens == nil {
		// OpenAI image policy: the text output stream is billed as image
		// output tokens unless the caller separates them.
		billable.OutputImageTokens = input.OutputTokens
	}
	return buildTokenCostBreakdown(pricing, billable, rates, tokenBillingOptions{
		cacheReadIncludedInInput: p.cacheReadIncludedInInput,
		cacheReadFallbackToInput: p.cacheReadFallbackToInput,
		labels:                   p.labels,
	})
}

// EstimateProviderCostUsd mirrors estimateProviderCostUsd: resolve the
// model over the built-in snapshot and return accountChargeUsd.
func EstimateProviderCostUsd(input CostInput) *float64 {
	breakdown := estimateProviderBreakdown(input)
	if breakdown == nil || breakdown.AccountChargeUsd == nil {
		return nil
	}
	return breakdown.AccountChargeUsd
}

// estimateProviderBreakdown mirrors the staticPricingBreakdown helper: the
// model must exist in the built-in snapshot for the input's provider.
func estimateProviderBreakdown(input CostInput) *CostBreakdown {
	if input.Model == "" || !hasAnyCostDimension(input) {
		return nil
	}
	pricing := FindProviderModelPricing(input.ProviderCode, input.Model)
	if pricing == nil {
		return nil
	}
	return BuildCostBreakdown(pricing, input)
}

// EstimateProviderCacheWriteCostUsd mirrors estimateProviderCacheWriteCostUsd.
func EstimateProviderCacheWriteCostUsd(input CostInput) *float64 {
	if input.Model == "" || (input.CacheWriteTokens == nil && input.CacheWrite1hTokens == nil) {
		return nil
	}
	breakdown := estimateProviderBreakdown(input)
	if breakdown == nil {
		return nil
	}
	if total := sumOptionalCosts(breakdown.CacheWriteCostUsd, breakdown.CacheWrite1hCostUsd); total != nil {
		return total
	}
	if (input.CacheWriteTokens == nil || *input.CacheWriteTokens == 0) &&
		(input.CacheWrite1hTokens == nil || *input.CacheWrite1hTokens == 0) &&
		(breakdown.CacheWriteUsdPer1M != nil || breakdown.CacheWrite1hUsdPer1M != nil) {
		zero := 0.0
		return &zero
	}
	return nil
}

// EstimateProviderCacheReadCostUsd mirrors estimateProviderCacheReadCostUsd.
func EstimateProviderCacheReadCostUsd(input CostInput) *float64 {
	if input.Model == "" || input.CacheReadTokens == nil {
		return nil
	}
	breakdown := estimateProviderBreakdown(input)
	if breakdown == nil || breakdown.CacheReadUsdPer1M == nil {
		return nil
	}
	if breakdown.CacheReadCostUsd != nil {
		return breakdown.CacheReadCostUsd
	}
	zero := 0.0
	return &zero
}

// standardRates mirrors provider-billing.shared standardRates.
func standardRates(pricing *Pricing) resolvedRates {
	return resolvedRates{
		PriceSet:                 directRates(pricing.PriceSet),
		serviceTierPricingSource: TierSourceDefault,
	}
}

// hasUnsupportedServiceTier mirrors provider-billing.shared
// hasUnsupportedServiceTier.
func hasUnsupportedServiceTier(pricing *Pricing, input CostInput) bool {
	tier := normalizedTier(input.ServiceTier)
	if tier == "" {
		return false
	}
	for _, supported := range pricing.SupportedServiceTiers {
		if supported == tier {
			return false
		}
	}
	return true
}

// serviceTierRates mirrors provider-billing.shared serviceTierRates: the
// tier table owns the token rates (no standard-rate fallback for
// input/output/cache), while image/audio/per-image rates fall back to the
// standard modal rates.
func serviceTierRates(pricing *Pricing, input CostInput) resolvedRates {
	tier := normalizedTier(input.ServiceTier)
	if tier == "" {
		return standardRates(pricing)
	}
	supported := false
	for _, supportedTier := range pricing.SupportedServiceTiers {
		if supportedTier == tier {
			supported = true
			break
		}
	}
	if !supported {
		return resolvedRates{serviceTierPricingSource: TierSourceUnknown}
	}
	tierRates, ok := pricing.ServiceTierPrices[tier]
	if !ok {
		return resolvedRates{serviceTierPricingSource: TierSourceUnknown}
	}
	resolved := directRates(tierRates)
	if resolved.ImageInputUsdPer1M == nil {
		resolved.ImageInputUsdPer1M = finite(pricing.ImageInputUsdPer1M)
	}
	if resolved.ImageOutputUsdPer1M == nil {
		resolved.ImageOutputUsdPer1M = finite(pricing.ImageOutputUsdPer1M)
	}
	if resolved.AudioInputUsdPer1M == nil {
		resolved.AudioInputUsdPer1M = finite(pricing.AudioInputUsdPer1M)
	}
	if resolved.AudioOutputUsdPer1M == nil {
		resolved.AudioOutputUsdPer1M = finite(pricing.AudioOutputUsdPer1M)
	}
	if resolved.OutputUsdPerImage == nil {
		resolved.OutputUsdPerImage = finite(pricing.OutputUsdPerImage)
	}
	source, multiplier := tierPricingMetadata(pricing.PriceSet, tierRates)
	return resolvedRates{
		PriceSet:                 resolved,
		serviceTierPricingSource: source,
		serviceTierMultiplier:    multiplier,
	}
}

// applyLongContextRates mirrors provider-billing.shared applyLongContextRates.
func applyLongContextRates(pricing *Pricing, input CostInput, rates resolvedRates) resolvedRates {
	threshold := pricing.LongContextInputTokenThreshold
	if threshold == nil {
		return rates
	}
	inputTokens := nonNegative(input.InputTokens)
	applies := inputTokens >= float64(*threshold)
	if !pricing.LongContextInputTokenThresholdInclusive {
		applies = inputTokens > float64(*threshold)
	}
	if !applies {
		return rates
	}
	inputMultiplier := validMultiplier(pricing.LongContextInputCostMultiplier)
	outputMultiplier := validMultiplier(pricing.LongContextOutputCostMultiplier)
	// Node spreads {...rates} and only overrides the five token rates; the
	// tier pricing source and multiplier carry through unchanged.
	rates.InputUsdPer1M = multiplyRate(rates.InputUsdPer1M, inputMultiplier)
	rates.CachedInputUsdPer1M = multiplyRate(rates.CachedInputUsdPer1M, inputMultiplier)
	rates.CacheWriteUsdPer1M = multiplyRate(rates.CacheWriteUsdPer1M, inputMultiplier)
	rates.CacheWrite1hUsdPer1M = multiplyRate(rates.CacheWrite1hUsdPer1M, inputMultiplier)
	rates.OutputUsdPer1M = multiplyRate(rates.OutputUsdPer1M, outputMultiplier)
	return rates
}

// buildTokenCostBreakdown mirrors provider-billing.shared
// buildTokenCostBreakdown.
func buildTokenCostBreakdown(pricing *Pricing, input CostInput, rates resolvedRates, options tokenBillingOptions) *CostBreakdown {
	if !hasAnyRate(rates.PriceSet) {
		return nil
	}

	cacheReadTokens := nonNegative(input.CacheReadTokens)
	cacheWriteTokens := nonNegative(input.CacheWriteTokens)
	cacheWrite1hTotal := cacheWriteTokens
	if cacheWrite1hTotal == 0 {
		cacheWrite1hTotal = nonNegative(input.CacheWrite1hTokens)
	}
	cacheWrite1hTokens := math.Min(nonNegative(input.CacheWrite1hTokens), cacheWrite1hTotal)
	cacheWriteStandardTokens := math.Max(cacheWriteTokens-cacheWrite1hTokens, 0)
	inputImageTokens := 0.0
	if rates.ImageInputUsdPer1M != nil {
		inputImageTokens = nonNegative(input.InputImageTokens)
	}
	outputImageTokens := 0.0
	if rates.ImageOutputUsdPer1M != nil {
		outputImageTokens = nonNegative(input.OutputImageTokens)
	}
	inputAudioTokens := 0.0
	if rates.AudioInputUsdPer1M != nil {
		inputAudioTokens = nonNegative(input.InputAudioTokens)
	}
	outputAudioTokens := 0.0
	if rates.AudioOutputUsdPer1M != nil {
		outputAudioTokens = nonNegative(input.OutputAudioTokens)
	}
	outputImageCount := 0.0
	if rates.OutputUsdPerImage != nil {
		outputImageCount = nonNegative(input.OutputImageCount)
	}
	if nonNegative(input.OutputImageCount) > 0 && rates.OutputUsdPerImage == nil {
		return nil
	}
	cacheReadRate := rates.CachedInputUsdPer1M
	if cacheReadRate == nil && options.cacheReadFallbackToInput {
		cacheReadRate = rates.InputUsdPer1M
	}
	uncachedInputTokens := math.Max(
		nonNegative(input.InputTokens)-
			boolFloat(options.cacheReadIncludedInInput)*cacheReadTokens-
			inputImageTokens-
			inputAudioTokens,
		0)
	outputTokens := math.Max(nonNegative(input.OutputTokens)-outputImageTokens-outputAudioTokens, 0)

	if hasUnpricedUsage(hasUnpricedUsageInput{
		uncachedInputTokens: uncachedInputTokens,
		inputRate:           rates.InputUsdPer1M,
		outputTokens:        outputTokens,
		outputRate:          rates.OutputUsdPer1M,
		cacheReadTokens:     cacheReadTokens,
		cacheReadRate:       cacheReadRate,
		cacheWriteStandard:  cacheWriteStandardTokens,
		cacheWriteRate:      rates.CacheWriteUsdPer1M,
		cacheWrite1hTokens:  cacheWrite1hTokens,
		cacheWrite1hRate:    orRate(rates.CacheWrite1hUsdPer1M, rates.CacheWriteUsdPer1M),
	}) {
		return nil
	}

	var lines []CostLineItem
	lines = addTokenLine(lines, "input", LineInput, options.labels.input, uncachedInputTokens, rates.InputUsdPer1M)
	lines = addTokenLine(lines, "output", LineOutput, options.labels.output, outputTokens, rates.OutputUsdPer1M)
	lines = addTokenLine(lines, "cache_read", LineCacheRead, options.labels.cacheRead, cacheReadTokens, cacheReadRate)
	lines = addTokenLine(lines, "cache_write", LineCacheWrite, options.labels.cacheWrite, cacheWriteStandardTokens, rates.CacheWriteUsdPer1M)
	lines = addTokenLine(lines, "cache_write_1h", LineCacheWrite1h, options.labels.cacheWrite1h, cacheWrite1hTokens, orRate(rates.CacheWrite1hUsdPer1M, rates.CacheWriteUsdPer1M))
	lines = addTokenLine(lines, "image_input", LineImageInput, options.labels.imageInput, inputImageTokens, rates.ImageInputUsdPer1M)
	lines = addTokenLine(lines, "image_output", LineImageOutput, options.labels.imageOutput, outputImageTokens, rates.ImageOutputUsdPer1M)
	lines = addTokenLine(lines, "audio_input", LineAudioInput, options.labels.audioInput, inputAudioTokens, rates.AudioInputUsdPer1M)
	lines = addTokenLine(lines, "audio_output", LineAudioOutput, options.labels.audioOutput, outputAudioTokens, rates.AudioOutputUsdPer1M)
	lines = addUnitLine(lines, "image_output_unit", LineImageOutUnit, options.labels.imageOutputUnit, outputImageCount, LineUnitImage, rates.OutputUsdPerImage)

	return legacyBreakdownFromLines(lines, input, rates)
}

// orRate mirrors the `a ?? b` rate fallbacks.
func orRate(primary, fallback *float64) *float64 {
	if primary != nil {
		return primary
	}
	return fallback
}

// boolFloat renders a boolean as 0/1 for arithmetic.
func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

// legacyBreakdownFromLines mirrors provider-billing.shared
// legacyBreakdownFromLines: the legacy flat fields are read back off the
// line items, the account charge is the costUsd override or the line sum.
func legacyBreakdownFromLines(lines []CostLineItem, input CostInput, rates resolvedRates) *CostBreakdown {
	line := func(kind CostLineKind) *CostLineItem {
		for index := range lines {
			if lines[index].Kind == kind {
				return &lines[index]
			}
		}
		return nil
	}
	cost := func(kind CostLineKind) *float64 {
		if item := line(kind); item != nil {
			value := item.CostUsd
			return &value
		}
		return nil
	}
	rate := func(kind CostLineKind) *float64 {
		if item := line(kind); item != nil {
			value := item.UnitPriceUsd
			return &value
		}
		return nil
	}
	calculated := 0.0
	for index := range lines {
		calculated += lines[index].CostUsd
	}
	calculated = roundCost(calculated)

	return &CostBreakdown{
		LineItems:                lines,
		InputCostUsd:             cost(LineInput),
		OutputCostUsd:            cost(LineOutput),
		InputUsdPer1M:            orRate(rate(LineInput), rates.InputUsdPer1M),
		OutputUsdPer1M:           orRate(rate(LineOutput), rates.OutputUsdPer1M),
		CacheReadCostUsd:         cost(LineCacheRead),
		CacheReadUsdPer1M:        orRate(rate(LineCacheRead), rates.CachedInputUsdPer1M),
		CacheWriteCostUsd:        cost(LineCacheWrite),
		CacheWriteUsdPer1M:       orRate(rate(LineCacheWrite), rates.CacheWriteUsdPer1M),
		CacheWrite1hCostUsd:      cost(LineCacheWrite1h),
		CacheWrite1hUsdPer1M:     orRate(orRate(rate(LineCacheWrite1h), rates.CacheWrite1hUsdPer1M), rates.CacheWriteUsdPer1M),
		ThinkingTokens:           input.ThinkingTokens,
		InputImageCostUsd:        cost(LineImageInput),
		OutputImageCostUsd:       cost(LineImageOutput),
		InputImageUsdPer1M:       orRate(rate(LineImageInput), rates.ImageInputUsdPer1M),
		OutputImageUsdPer1M:      orRate(rate(LineImageOutput), rates.ImageOutputUsdPer1M),
		InputAudioCostUsd:        cost(LineAudioInput),
		OutputAudioCostUsd:       cost(LineAudioOutput),
		InputAudioUsdPer1M:       orRate(rate(LineAudioInput), rates.AudioInputUsdPer1M),
		OutputAudioUsdPer1M:      orRate(rate(LineAudioOutput), rates.AudioOutputUsdPer1M),
		OutputImageUnitCostUsd:   cost(LineImageOutUnit),
		OutputUsdPerImage:        orRate(rate(LineImageOutUnit), rates.OutputUsdPerImage),
		AccountChargeUsd:         orRate(finite(input.CostUsd), &calculated),
		Multiplier:               1,
		ServiceTierPricingSource: rates.serviceTierPricingSource,
		ServiceTierMultiplier:    rates.serviceTierMultiplier,
	}
}

// directRates mirrors provider-billing.shared directRates.
func directRates(set PriceSet) PriceSet {
	return PriceSet{
		InputUsdPer1M:               finite(set.InputUsdPer1M),
		OutputUsdPer1M:              finite(set.OutputUsdPer1M),
		CachedInputUsdPer1M:         finite(set.CachedInputUsdPer1M),
		CacheWriteUsdPer1M:          finite(set.CacheWriteUsdPer1M),
		CacheWrite1hUsdPer1M:        finite(set.CacheWrite1hUsdPer1M),
		CacheStorageUsdPer1MPerHour: finite(set.CacheStorageUsdPer1MPerHour),
		ImageInputUsdPer1M:          finite(set.ImageInputUsdPer1M),
		ImageOutputUsdPer1M:         finite(set.ImageOutputUsdPer1M),
		AudioInputUsdPer1M:          finite(set.AudioInputUsdPer1M),
		AudioOutputUsdPer1M:         finite(set.AudioOutputUsdPer1M),
		OutputUsdPerImage:           finite(set.OutputUsdPerImage),
	}
}

// tierPricingMetadata mirrors provider-billing.shared tierPricingMetadata:
// every token rate present in the tier table marks tier specificity; a
// standard rate missing from the tier table marks the mix.
func tierPricingMetadata(standard, tier PriceSet) (ServiceTierPricingSource, *float64) {
	pairs := [][2]*float64{
		{standard.InputUsdPer1M, tier.InputUsdPer1M},
		{standard.OutputUsdPer1M, tier.OutputUsdPer1M},
		{standard.CachedInputUsdPer1M, tier.CachedInputUsdPer1M},
		{standard.CacheWriteUsdPer1M, tier.CacheWriteUsdPer1M},
		{standard.CacheWrite1hUsdPer1M, tier.CacheWrite1hUsdPer1M},
		{standard.AudioInputUsdPer1M, tier.AudioInputUsdPer1M},
		{standard.AudioOutputUsdPer1M, tier.AudioOutputUsdPer1M},
	}
	specific := 0
	missing := 0
	for _, pair := range pairs {
		switch {
		case pair[1] != nil:
			specific++
		case pair[0] != nil:
			missing++
		}
	}
	switch {
	case specific > 0 && missing == 0:
		return TierSourceTierSpecific, nil
	case specific > 0:
		return TierSourceMixed, nil
	default:
		return TierSourceUnknown, nil
	}
}

// hasUnpricedUsageInput mirrors the named argument object of
// provider-billing.shared hasUnpricedUsage.
type hasUnpricedUsageInput struct {
	uncachedInputTokens float64
	inputRate           *float64
	outputTokens        float64
	outputRate          *float64
	cacheReadTokens     float64
	cacheReadRate       *float64
	cacheWriteStandard  float64
	cacheWriteRate      *float64
	cacheWrite1hTokens  float64
	cacheWrite1hRate    *float64
}

// hasUnpricedUsage mirrors provider-billing.shared hasUnpricedUsage: any
// positive quantity without a rate collapses the breakdown.
func hasUnpricedUsage(input hasUnpricedUsageInput) bool {
	return (input.uncachedInputTokens > 0 && input.inputRate == nil) ||
		(input.outputTokens > 0 && input.outputRate == nil) ||
		(input.cacheReadTokens > 0 && input.cacheReadRate == nil) ||
		(input.cacheWriteStandard > 0 && input.cacheWriteRate == nil) ||
		(input.cacheWrite1hTokens > 0 && input.cacheWrite1hRate == nil)
}

// addTokenLine mirrors provider-billing.shared addTokenLine.
func addTokenLine(lines []CostLineItem, key string, kind CostLineKind, label string, quantity float64, unitPriceUsd *float64) []CostLineItem {
	if quantity <= 0 || unitPriceUsd == nil {
		return lines
	}
	cost := roundCost(quantity / tokenUnitSize * *unitPriceUsd)
	return append(lines, CostLineItem{
		Key:          key,
		Kind:         kind,
		Label:        label,
		Quantity:     quantity,
		Unit:         LineUnitToken,
		UnitSize:     tokenUnitSize,
		UnitPriceUsd: *unitPriceUsd,
		CostUsd:      cost,
	})
}

// addUnitLine mirrors provider-billing.shared addUnitLine.
func addUnitLine(lines []CostLineItem, key string, kind CostLineKind, label string, quantity float64, unit string, unitPriceUsd *float64) []CostLineItem {
	if quantity <= 0 || unitPriceUsd == nil {
		return lines
	}
	cost := roundCost(quantity * *unitPriceUsd)
	return append(lines, CostLineItem{
		Key:          key,
		Kind:         kind,
		Label:        label,
		Quantity:     quantity,
		Unit:         unit,
		UnitSize:     1,
		UnitPriceUsd: *unitPriceUsd,
		CostUsd:      cost,
	})
}

// normalizedTier mirrors provider-billing.shared normalizedTier: blank,
// 'default' and 'standard' mean the standard rate set.
func normalizedTier(value string) string {
	tier := strings.TrimSpace(value)
	if tier == "" || tier == "default" || tier == "standard" {
		return ""
	}
	return tier
}
