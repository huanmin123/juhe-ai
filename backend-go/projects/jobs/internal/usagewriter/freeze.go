package usagewriter

import (
	"math"
	"strconv"
	"strings"
)

// pricing freeze（计价快照冻结时点）语义移植，对照：
//   - backend/src/storage/usage-records.repository.ts
//     freezeUsageRecordPricingFactsAsync / enrichSingleUsageRecordPricingAsync
//     / usageRecordPricingSnapshotForWrite / hasUsageRecordPricingSnapshotFact
//   - gateway/internal/pricing（C03，只读参考）：定价算法留在 pricing 包，
//     本包通过 CatalogPricing port 调用；冻结语义是"记录在写入/入队时点
//     定价一次，冻结的 pricingSnapshot 之后不再按未来目录重新解释"。
//
// 解冻面（读侧）按 Node withCostBreakdown 契约：优先用冻结快照，缺失时才
// 走 fallback（multiplier=1、serviceTierPricingSource='unknown'）。

// CostBreakdown mirrors ProviderCostBreakdown
// (model-pricing/provider-billing.types.ts)，字段与
// gateway/internal/pricing.CostBreakdown 对齐；nil 指针表示 Node undefined。
// JSON tag 与 Node 序列化一致，落库为 cost_breakdown_snapshot_json。
type CostBreakdown struct {
	Currency       string         `json:"currency,omitempty"`
	BillingPolicy  string         `json:"billingPolicy,omitempty"`
	LineItems      []CostLineItem `json:"lineItems,omitempty"`
	InputCostUsd   *float64       `json:"inputCostUsd,omitempty"`
	OutputCostUsd  *float64       `json:"outputCostUsd,omitempty"`
	InputUsdPer1M  *float64       `json:"inputUsdPer1M,omitempty"`
	OutputUsdPer1M *float64       `json:"outputUsdPer1M,omitempty"`

	CacheReadCostUsd   *float64 `json:"cacheReadCostUsd,omitempty"`
	CacheReadUsdPer1M  *float64 `json:"cacheReadUsdPer1M,omitempty"`
	CacheWriteCostUsd  *float64 `json:"cacheWriteCostUsd,omitempty"`
	CacheWriteUsdPer1M *float64 `json:"cacheWriteUsdPer1M,omitempty"`

	CacheWrite1hCostUsd  *float64 `json:"cacheWrite1hCostUsd,omitempty"`
	CacheWrite1hUsdPer1M *float64 `json:"cacheWrite1hUsdPer1M,omitempty"`
	ThinkingTokens       *float64 `json:"thinkingTokens,omitempty"`

	InputImageCostUsd      *float64 `json:"inputImageCostUsd,omitempty"`
	OutputImageCostUsd     *float64 `json:"outputImageCostUsd,omitempty"`
	InputImageUsdPer1M     *float64 `json:"inputImageUsdPer1M,omitempty"`
	OutputImageUsdPer1M    *float64 `json:"outputImageUsdPer1M,omitempty"`
	InputAudioCostUsd      *float64 `json:"inputAudioCostUsd,omitempty"`
	OutputAudioCostUsd     *float64 `json:"outputAudioCostUsd,omitempty"`
	InputAudioUsdPer1M     *float64 `json:"inputAudioUsdPer1M,omitempty"`
	OutputAudioUsdPer1M    *float64 `json:"outputAudioUsdPer1M,omitempty"`
	OutputImageUnitCostUsd *float64 `json:"outputImageUnitCostUsd,omitempty"`
	OutputUsdPerImage      *float64 `json:"outputUsdPerImage,omitempty"`

	AccountChargeUsd *float64 `json:"accountChargeUsd,omitempty"`
	Multiplier       float64  `json:"multiplier"`
	// ServiceTierPricingSource mirrors the ProviderCostBreakdown discriminant:
	// default | tier_specific | multiplier | mixed | unknown.
	ServiceTierPricingSource string   `json:"serviceTierPricingSource"`
	ServiceTierMultiplier    *float64 `json:"serviceTierMultiplier,omitempty"`
}

// CostLineItem mirrors ProviderCostLineItem.
type CostLineItem struct {
	Key          string  `json:"key"`
	Kind         string  `json:"kind"`
	Label        string  `json:"label"`
	Quantity     float64 `json:"quantity"`
	Unit         string  `json:"unit"`
	UnitSize     float64 `json:"unitSize"`
	UnitPriceUsd float64 `json:"unitPriceUsd"`
	CostUsd      float64 `json:"costUsd"`
}

// ServiceTierPricingSource 取值（provider-billing.types.ts）。
const (
	TierSourceDefault      = "default"
	TierSourceTierSpecific = "tier_specific"
	TierSourceMultiplier   = "multiplier"
	TierSourceMixed        = "mixed"
	TierSourceUnknown      = "unknown"
)

// CatalogPricing ports the pricing enrichment the writer needs from the
// C03 pricing slice (gateway/internal/pricing; separate Go module, adapted
// at the composition root). It mirrors the consumed surface of
// enrichSingleUsageRecordPricingAsync:
//   - ResolvePricingModel mirrors resolveUsageRecordPricingModel over the
//     (providerCode, systemAccountId) catalog;
//   - BuildBreakdown mirrors buildCatalogCostBreakdownFromPricing for the
//     resolved cost model.
type CatalogPricing interface {
	// ResolvePricingModel resolves the catalog pricing model for the actual
	// (upstream ?? requested) model; empty string = unresolved.
	ResolvePricingModel(ctx Ctx, providerCode string, systemAccountID string, upstreamModel string, requestedModel string) string
	// BuildBreakdown mirrors buildCatalogCostBreakdownFromPricing; nil = the
	// Node undefined snapshot (model not found in catalog).
	BuildBreakdown(ctx Ctx, providerCode string, systemAccountID string, model string, serviceTier string, input UsageRecordInput) *CostBreakdown
}

// HasCostDimension mirrors hasUsageRecordCostDimension.
func HasCostDimension(input UsageRecordInput) bool {
	return input.InputTokens != nil ||
		input.OutputTokens != nil ||
		input.CacheReadTokens != nil ||
		input.CacheWriteTokens != nil ||
		input.CacheWrite1hTokens != nil ||
		input.InputImageTokens != nil ||
		input.OutputImageTokens != nil ||
		input.InputAudioTokens != nil ||
		input.OutputAudioTokens != nil ||
		input.OutputImageCount != nil
}

// HasPricingSnapshotFact mirrors hasUsageRecordPricingSnapshotFact.
func HasPricingSnapshotFact(input UsageRecordInput) bool {
	return HasCostDimension(input) ||
		finiteUsageNumber(input.CacheReadCostUsd) != nil ||
		finiteUsageNumber(input.CacheWriteCostUsd) != nil ||
		finiteUsageNumber(input.CostUsd) != nil
}

// finiteUsageNumber mirrors finiteUsageNumber.
func finiteUsageNumber(value *float64) *float64 {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) {
		return nil
	}
	return value
}

// EnrichUsageRecordPricing mirrors enrichSingleUsageRecordPricingAsync over
// the injected CatalogPricing port (the per-call catalog cache of the Node
// implementation belongs to the adapter). Missing pricingSnapshot facts,
// provider code, or a cost model leave the input unchanged; a failed catalog
// lookup keeps the original record (Node logs
// "使用记录写入前补算成本失败，保留原始用量记录" and returns input).
func EnrichUsageRecordPricing(ctx Ctx, input UsageRecordInput, catalog CatalogPricing) UsageRecordInput {
	if input.PricingSnapshot != nil {
		return input
	}
	if !HasPricingSnapshotFact(input) {
		return input
	}
	if input.ProviderCode == "" || catalog == nil {
		return input
	}
	providerCode := input.ProviderCode
	catalogSystemAccountID := firstNonEmpty(input.AccountOwnerSystemAccountID, input.SystemAccountID)
	upstreamModel := normalizeUsageRecordPricingModel(input.UpstreamModel)
	requestedModel := normalizeUsageRecordPricingModel(input.Model)
	existingPricingModel := normalizeUsageRecordPricingModel(input.PricingModel)

	pricingModel := existingPricingModel
	if pricingModel == "" {
		pricingModel = catalog.ResolvePricingModel(ctx, providerCode, catalogSystemAccountID, upstreamModel, requestedModel)
	}
	costModel := firstNonEmpty(pricingModel, upstreamModel, requestedModel)
	if costModel == "" {
		if pricingModel != "" && pricingModel != input.PricingModel {
			input.PricingModel = pricingModel
		}
		return input
	}

	enriched := input
	if pricingModel != "" && pricingModel != input.PricingModel {
		enriched.PricingModel = pricingModel
	}
	if !HasCostDimension(enriched) {
		return enriched
	}
	pricingSnapshot := catalog.BuildBreakdown(ctx, providerCode, catalogSystemAccountID, costModel, enriched.BilledServiceTier, enriched)
	if pricingSnapshot == nil {
		return enriched
	}
	if enriched.CacheReadCostUsd == nil && enriched.CacheReadTokens != nil {
		enriched.CacheReadCostUsd = pricingSnapshot.CacheReadCostUsd
	}
	if enriched.CacheWriteCostUsd == nil && (enriched.CacheWriteTokens != nil || enriched.CacheWrite1hTokens != nil) {
		enriched.CacheWriteCostUsd = sumOptionalCosts(pricingSnapshot.CacheWriteCostUsd, pricingSnapshot.CacheWrite1hCostUsd)
		if enriched.CacheWriteCostUsd == nil && (pricingSnapshot.CacheWriteUsdPer1M != nil || pricingSnapshot.CacheWrite1hUsdPer1M != nil) {
			zero := 0.0
			enriched.CacheWriteCostUsd = &zero
		}
	}
	if enriched.CostUsd == nil {
		enriched.CostUsd = pricingSnapshot.AccountChargeUsd
	}
	if enriched.PricingSnapshot == nil {
		enriched.PricingSnapshot = pricingSnapshot
	}
	return enriched
}

// FreezeUsageRecordPricingFacts mirrors freezeUsageRecordPricingFactsAsync:
// the freeze-time (入队时点) pricing snapshot. An already frozen snapshot is
// kept untouched (frozen facts are never re-interpreted against a future
// catalog); records without pricing facts pass through; otherwise the
// snapshot is enriched/enrolled once at this instant.
func FreezeUsageRecordPricingFacts(ctx Ctx, input UsageRecordInput, catalog CatalogPricing, catalogFallbackEnabled bool) UsageRecordInput {
	if !HasPricingSnapshotFact(input) {
		return input
	}
	enriched := EnrichUsageRecordPricing(ctx, input, catalog)
	if enriched.PricingSnapshot != nil {
		return enriched
	}
	pricingSnapshot := PricingSnapshotForWrite(ctx, enriched, catalogFallbackEnabled, catalog)
	if pricingSnapshot == nil {
		return enriched
	}
	enriched.PricingSnapshot = pricingSnapshot
	return enriched
}

// PricingSnapshotForWrite mirrors usageRecordPricingSnapshotForWrite: an
// existing snapshot wins; records without pricing facts produce none; the
// catalog snapshot is only attempted when catalogLookupEnabled is true
// (mirrors the Node `runtimeConfig.databaseDriver !== 'postgres'` guard),
// the provider code is present and a cost dimension exists; otherwise the
// deterministic fallback applies.
func PricingSnapshotForWrite(ctx Ctx, input UsageRecordInput, catalogLookupEnabled bool, catalog CatalogPricing) *CostBreakdown {
	if input.PricingSnapshot != nil {
		if snapshot, ok := input.PricingSnapshot.(*CostBreakdown); ok {
			return snapshot
		}
		return nil
	}
	if !HasPricingSnapshotFact(input) {
		return nil
	}
	if catalogLookupEnabled && input.ProviderCode != "" && HasCostDimension(input) {
		model := firstNonEmpty(
			normalizeUsageRecordPricingModel(input.PricingModel),
			normalizeUsageRecordPricingModel(input.UpstreamModel),
			normalizeUsageRecordPricingModel(input.Model),
		)
		if model != "" && catalog != nil {
			snapshot := catalog.BuildBreakdown(
				ctx,
				input.ProviderCode,
				firstNonEmpty(input.AccountOwnerSystemAccountID, input.SystemAccountID),
				model,
				serviceTierForWrite(input),
				input,
			)
			if snapshot != nil {
				return snapshot
			}
		}
	}
	return FallbackPricingSnapshot(input)
}

// serviceTierForWrite mirrors the Node service tier fallback chain
// billed ?? reported ?? effective ?? requested.
func serviceTierForWrite(input UsageRecordInput) string {
	return firstNonEmpty(input.BilledServiceTier, input.ReportedServiceTier, input.EffectiveServiceTier, input.RequestedServiceTier)
}

// FallbackPricingSnapshot mirrors the Node deterministic fallback:
// { cacheReadCostUsd, cacheWriteCostUsd, thinkingTokens, accountChargeUsd,
// multiplier: 1, serviceTierPricingSource: 'unknown' }.
func FallbackPricingSnapshot(input UsageRecordInput) *CostBreakdown {
	var thinkingTokens *float64
	if input.ThinkingTokens != nil {
		value := float64(*input.ThinkingTokens)
		thinkingTokens = &value
	}
	return &CostBreakdown{
		CacheReadCostUsd:         finiteUsageNumber(input.CacheReadCostUsd),
		CacheWriteCostUsd:        finiteUsageNumber(input.CacheWriteCostUsd),
		ThinkingTokens:           thinkingTokens,
		AccountChargeUsd:         finiteUsageNumber(input.CostUsd),
		Multiplier:               1,
		ServiceTierPricingSource: TierSourceUnknown,
	}
}

// normalizeUsageRecordPricingModel mirrors normalizeUsageRecordPricingModel.
func normalizeUsageRecordPricingModel(value string) string {
	normalized := strings.TrimSpace(value)
	return normalized
}

// sumOptionalCosts mirrors sumOptionalCosts: sum defined parts rounded to 10
// decimals; no defined part stays undefined.
func sumOptionalCosts(values ...*float64) *float64 {
	any := false
	total := 0.0
	for _, value := range values {
		if value == nil {
			continue
		}
		any = true
		total += *value
	}
	if !any {
		return nil
	}
	rounded := roundCost(total)
	return &rounded
}

// roundCost mirrors Number(value.toFixed(10)).
func roundCost(value float64) float64 {
	return fixedNumber(value, 10)
}

// fixedNumber mirrors Number(value.toFixed(digits)).
func fixedNumber(value float64, digits int) float64 {
	converted, err := strconv.ParseFloat(strconv.FormatFloat(value, 'f', digits, 64), 64)
	if err != nil {
		return value
	}
	return converted
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
