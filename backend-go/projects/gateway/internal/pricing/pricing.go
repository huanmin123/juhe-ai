// Package pricing owns the C03 vertical slice: the static model pricing
// catalog and cost calculator ported from
// backend/src/modules/model-pricing/ (model-pricing.service.ts,
// provider-billing.shared.ts, provider-billing.policies.ts,
// provider-billing.service.ts and the *.data.ts snapshots). It covers:
//
//   - the six provider pricing snapshots (openai/gpt, anthropic, deepseek,
//     glm, gemini, xai) as immutable Go data with per-million conversions
//     bit-compatible with the Node snapshots,
//   - the provider billing policies (per-provider token cost breakdowns with
//     cache split, service-tier exact prices and long-context multipliers),
//   - the GPT Priority/Flex tier linkage: tiers always bill the exact
//     per-model tier prices from the catalog, never a generic multiplier
//     (docs/functions/GPT请求服务等级与思考级别覆盖设计.md); the tier
//     switches are read from the settings snapshot provider, defaulting to
//     enabled, which is the current Node contract path,
//   - the pricing freeze helper for the J-F usage writer
//     (freezeUsageRecordPricingFactsAsync): a usage record is priced once at
//     request time and the frozen snapshot is never re-interpreted against a
//     future catalog.
//
// The module exposes no HTTP routes on the Node side; the model catalog
// management surface (model-catalog.service.ts custom catalog + codex models)
// belongs to the providers slice. Catalog display rendering
// (buildProviderCatalogDisplay) is presentation-only and stays deferred.
package pricing

import (
	"math"
	"strconv"
)

// PriceSet mirrors provider-billing.types ProviderBillingPriceSet. nil means
// the Node undefined price; a non-nil 0 is a real free price (glm-4.7-flash).
type PriceSet struct {
	InputUsdPer1M               *float64
	OutputUsdPer1M              *float64
	CachedInputUsdPer1M         *float64
	CacheWriteUsdPer1M          *float64
	CacheWrite1hUsdPer1M        *float64
	CacheStorageUsdPer1MPerHour *float64
	ImageInputUsdPer1M          *float64
	ImageOutputUsdPer1M         *float64
	AudioInputUsdPer1M          *float64
	AudioOutputUsdPer1M         *float64
	OutputUsdPerImage           *float64
}

// ServiceTierPrices mirrors Record<string, ModelPriceSet> (priority/flex/batch).
type ServiceTierPrices map[string]PriceSet

// Pricing mirrors the ProviderModelPricing / ProviderBillingPricing merge the
// Node billing path consumes (model-pricing.service.ts
// toProviderModelPricing).
type Pricing struct {
	ProviderCode string
	Model        string
	Mode         string
	CatalogOrder *int
	ReleaseDate  string
	ShutdownDate string
	PriceSet

	CachedImageInputUsdPer1M *float64
	ServiceTierPrices        ServiceTierPrices

	SupportedAPIProtocols     []string
	InputModalities           []string
	OutputModalities          []string
	SupportedTools            []string
	SupportedServiceTiers     []string
	SupportedReasoningEfforts []string
	DefaultReasoningEffort    string

	CodexSupportedReasoningLevels []string
	CodexDefaultReasoningLevel    string
	CodexMultiAgentVersion        string

	ContextWindowTokens *int
	MaxInputTokens      *int
	MaxOutputTokens     *int
	MaxTokens           *int

	LongContextInputTokenThreshold          *int
	LongContextInputTokenThresholdInclusive bool
	LongContextInputCostMultiplier          *float64
	LongContextOutputCostMultiplier         *float64

	SupportsPromptCaching bool
	SupportsServiceTier   bool
	CatalogVisible        bool

	SourcePricingCurrency   string
	SourceExchangeRateToUsd *float64
	SourceExchangeRateDate  string
	SourcePricingNote       string
	Source                  string
}

// CostInput mirrors ProviderBillingCostInput plus the costUsd override the
// CostBreakdownInput extension carries. nil = Node undefined.
type CostInput struct {
	ProviderCode string
	Model        string
	ServiceTier  string

	InputTokens        *float64
	OutputTokens       *float64
	CacheReadTokens    *float64
	CacheWriteTokens   *float64
	CacheWrite1hTokens *float64
	ThinkingTokens     *float64
	InputImageTokens   *float64
	OutputImageTokens  *float64
	InputAudioTokens   *float64
	OutputAudioTokens  *float64
	OutputImageCount   *float64
	CostUsd            *float64
}

// CostLineKind mirrors ProviderCostLineKind.
type CostLineKind string

const (
	LineInput        CostLineKind = "input"
	LineOutput       CostLineKind = "output"
	LineCacheRead    CostLineKind = "cache_read"
	LineCacheWrite   CostLineKind = "cache_write"
	LineCacheWrite1h CostLineKind = "cache_write_1h"
	LineImageInput   CostLineKind = "image_input"
	LineImageOutput  CostLineKind = "image_output"
	LineAudioInput   CostLineKind = "audio_input"
	LineAudioOutput  CostLineKind = "audio_output"
	LineImageOutUnit CostLineKind = "image_output_unit"
	LineOther        CostLineKind = "other"
	LineUnitToken                 = "token"
	LineUnitImage                 = "image"
	tokenUnitSize                 = 1_000_000.0
)

// CostLineItem mirrors ProviderCostLineItem.
type CostLineItem struct {
	Key          string
	Kind         CostLineKind
	Label        string
	Quantity     float64
	Unit         string
	UnitSize     float64
	UnitPriceUsd float64
	CostUsd      float64
}

// ServiceTierPricingSource mirrors the ProviderCostBreakdown discriminant.
type ServiceTierPricingSource string

const (
	TierSourceDefault      ServiceTierPricingSource = "default"
	TierSourceTierSpecific ServiceTierPricingSource = "tier_specific"
	TierSourceMultiplier   ServiceTierPricingSource = "multiplier"
	TierSourceMixed        ServiceTierPricingSource = "mixed"
	TierSourceUnknown      ServiceTierPricingSource = "unknown"
)

// CostBreakdown mirrors ProviderCostBreakdown. Missing optional cost/rate
// fields stay nil (Node undefined).
type CostBreakdown struct {
	Currency      string // always "USD" when produced by BuildCostBreakdown
	BillingPolicy string // policy id when produced by BuildCostBreakdown
	LineItems     []CostLineItem

	InputCostUsd        *float64
	OutputCostUsd       *float64
	InputUsdPer1M       *float64
	OutputUsdPer1M      *float64
	CacheReadCostUsd    *float64
	CacheReadUsdPer1M   *float64
	CacheWriteCostUsd   *float64
	CacheWriteUsdPer1M  *float64
	CacheWrite1hCostUsd *float64

	CacheWrite1hUsdPer1M *float64
	ThinkingTokens       *float64

	InputImageCostUsd      *float64
	OutputImageCostUsd     *float64
	InputImageUsdPer1M     *float64
	OutputImageUsdPer1M    *float64
	InputAudioCostUsd      *float64
	OutputAudioCostUsd     *float64
	InputAudioUsdPer1M     *float64
	OutputAudioUsdPer1M    *float64
	OutputImageUnitCostUsd *float64
	OutputUsdPerImage      *float64

	AccountChargeUsd *float64
	Multiplier       float64 // always 1; Node never produces another value

	ServiceTierPricingSource ServiceTierPricingSource
	ServiceTierMultiplier    *float64 // never set: no generic tier multiplier exists
}

// f64p allocates an optional float64 (test/data helper).
func f64p(v float64) *float64 { return &v }

// intp allocates an optional int (test/data helper).
func intp(v int) *int { return &v }

// nonNegative mirrors provider-billing.shared nonNegative: undefined/NaN/Inf
// collapse to 0, negatives clamp to 0.
func nonNegative(v *float64) float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return 0
	}
	return math.Max(*v, 0)
}

// finite mirrors provider-billing.shared finite: undefined/NaN/Inf/negative
// collapse to undefined.
func finite(v *float64) *float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0 {
		return nil
	}
	return v
}

// validMultiplier mirrors provider-billing.shared validMultiplier.
func validMultiplier(v *float64) float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) || *v <= 0 {
		return 1
	}
	return *v
}

// multiplyRate mirrors multiply: undefined stays undefined.
func multiplyRate(v *float64, multiplier float64) *float64 {
	if v == nil {
		return nil
	}
	out := *v * multiplier
	return &out
}

// roundCost mirrors roundCost: Number(value.toFixed(10)).
func roundCost(v float64) float64 {
	return fixedNumber(v, 10)
}

// fixedNumber mirrors Number(value.toFixed(digits)).
func fixedNumber(v float64, digits int) float64 {
	out, err := strconv.ParseFloat(strconv.FormatFloat(v, 'f', digits, 64), 64)
	if err != nil {
		return v
	}
	return out
}

// perMillion mirrors model-pricing.service perMillion:
// Number((price * 1_000_000).toFixed(8)), undefined-safe.
func perMillion(v *float64) *float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return nil
	}
	out := fixedNumber(*v*1_000_000, 8)
	return &out
}

// sumOptionalCosts mirrors sumOptionalCosts: sum defined parts and round;
// no defined part stays undefined.
func sumOptionalCosts(parts ...*float64) *float64 {
	total := 0.0
	any := false
	for _, part := range parts {
		if part == nil {
			continue
		}
		any = true
		total += *part
	}
	if !any {
		return nil
	}
	out := roundCost(total)
	return &out
}

// hasAnyRate mirrors hasAnyRate: at least one finite rate must exist.
func hasAnyRate(set PriceSet) bool {
	for _, v := range []*float64{
		set.InputUsdPer1M, set.OutputUsdPer1M, set.CachedInputUsdPer1M,
		set.CacheWriteUsdPer1M, set.CacheWrite1hUsdPer1M,
		set.CacheStorageUsdPer1MPerHour, set.ImageInputUsdPer1M,
		set.ImageOutputUsdPer1M, set.AudioInputUsdPer1M,
		set.AudioOutputUsdPer1M, set.OutputUsdPerImage,
	} {
		if v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) {
			return true
		}
	}
	return false
}

// hasAnyCostDimension mirrors hasAnyCostDimension.
func hasAnyCostDimension(input CostInput) bool {
	return input.InputTokens != nil || input.OutputTokens != nil ||
		input.CacheReadTokens != nil || input.CacheWriteTokens != nil ||
		input.CacheWrite1hTokens != nil || input.InputImageTokens != nil ||
		input.OutputImageTokens != nil || input.InputAudioTokens != nil ||
		input.OutputAudioTokens != nil || input.OutputImageCount != nil
}
