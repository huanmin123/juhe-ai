package main

// G20 phase-3 in-flight quota cost estimator: gatewayquota.CostEstimator over
// the model catalog pricing rows (Node model-catalog.service.ts
// estimateCatalogCostUsdAsync -> provider-billing buildCostBreakdown ->
// accountChargeUsd).
//
// The estimate input the inflight quota produces carries only
// inputTokens/outputTokens plus the service tier (estimateGatewayRequestCostUsd),
// so this adapter ports the estimate-path slice of the token billing engine:
// catalog row resolution (findCatalogItem over the runtime cache catalog),
// the service-tier rate resolution, the long-context multipliers and the
// per-provider policy differences (openai/gemini/xai apply the long-context
// multipliers, anthropic/deepseek/glm keep standard rates; anthropic and
// deepseek/glm reject unsupported tiers up front). The full billing engine
// (cache/image/audio line items) remains the provider-billing slice.

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// chainCostEstimator implements gatewayquota.CostEstimator over the runtime
// cache model catalog (listProviderModelCatalog: built-in + account rows).
type chainCostEstimator struct {
	cache *gatewayruntimecache.Service
}

func newChainCostEstimator(cache *gatewayruntimecache.Service) *chainCostEstimator {
	return &chainCostEstimator{cache: cache}
}

// EstimateCatalogCostUSD mirrors estimateCatalogCostUsdAsync: ok=false
// mirrors the undefined estimate (no inflight reservation).
func (e *chainCostEstimator) EstimateCatalogCostUSD(ctx context.Context, input gatewayquota.CatalogCostInput) (float64, bool) {
	if e == nil || e.cache == nil || strings.TrimSpace(input.Model) == "" {
		return 0, false
	}
	catalog, err := e.cache.ListCachedProviderModelCatalogAsync(ctx, gatewayruntimecache.ModelCatalogListOptions{
		ProviderCode:    input.ProviderCode,
		SystemAccountID: input.SystemAccountID,
	})
	if err != nil {
		return 0, false
	}
	pricing := findChainCatalogItem(catalog, input.Model)
	if pricing == nil {
		return 0, false
	}
	rates, ok := chainEstimateRates(pricing, input.ServiceTier, input.InputTokens, input.OutputTokens)
	if !ok {
		return 0, false
	}
	return chainEstimateAccountCharge(rates, input.InputTokens, input.OutputTokens), true
}

// findChainCatalogItem mirrors findCatalogItem (trim equality).
func findChainCatalogItem(items []gatewayruntimecache.ProviderModelCatalogItem, model string) *gatewayruntimecache.ProviderModelCatalogItem {
	normalized := strings.TrimSpace(model)
	for index := range items {
		if strings.TrimSpace(items[index].Model) == normalized {
			return &items[index]
		}
	}
	return nil
}

// chainEstimateRates mirrors serviceTierRates + applyLongContextRates for the
// input/output token estimate input. ok=false mirrors the undefined
// breakdown (unknown tier or unpriced usage).
func chainEstimateRates(pricing *gatewayruntimecache.ProviderModelCatalogItem, serviceTier string, inputTokens, outputTokens int64) (*chainEstimateRateSet, bool) {
	provider := chainNormalizeProviderToken(pricing.ProviderCode)
	policy := chainBillingPolicyFor(provider)
	if policy == nil {
		return nil, false
	}
	rates := &chainEstimateRateSet{
		inputPer1M:  pricing.InputUsdPer1M,
		outputPer1M: pricing.OutputUsdPer1M,
	}
	tier := strings.TrimSpace(serviceTier)
	if tier != "" && tier != "default" && tier != "standard" {
		// serviceTierRates: a tier absent from supportedServiceTiers or the
		// tier price table collapses the breakdown to unknown (undefined
		// estimate).
		supported := false
		for _, supportedTier := range pricing.SupportedServiceTiers {
			if supportedTier == tier {
				supported = true
				break
			}
		}
		var tierPrices map[string]struct {
			InputUsdPer1M  *float64 `json:"inputUsdPer1M"`
			OutputUsdPer1M *float64 `json:"outputUsdPer1M"`
		}
		if len(pricing.ServiceTierPrices) > 0 {
			if err := json.Unmarshal(pricing.ServiceTierPrices, &tierPrices); err != nil {
				return nil, false
			}
		}
		tierPrice, hasTierPrice := tierPrices[tier]
		if !supported || !hasTierPrice {
			return nil, false
		}
		rates.inputPer1M = tierPrice.InputUsdPer1M
		rates.outputPer1M = tierPrice.OutputUsdPer1M
	}
	if policy.longContext && pricing.LongContextInputTokenThreshold != nil {
		threshold := *pricing.LongContextInputTokenThreshold
		inclusive := pricing.LongContextInputTokenThresholdInclusive != nil && *pricing.LongContextInputTokenThresholdInclusive
		applies := inputTokens >= threshold
		if !inclusive {
			applies = inputTokens > threshold
		}
		if threshold > 0 && applies {
			inputMultiplier := chainValidMultiplier(pricing.LongContextInputCostMultiplier)
			outputMultiplier := chainValidMultiplier(pricing.LongContextOutputCostMultiplier)
			rates.inputPer1M = chainMultiplyRate(rates.inputPer1M, inputMultiplier)
			rates.outputPer1M = chainMultiplyRate(rates.outputPer1M, outputMultiplier)
		}
	}
	// hasUnpricedUsage: positive token counts require a rate.
	if (inputTokens > 0 && rates.inputPer1M == nil) || (outputTokens > 0 && rates.outputPer1M == nil) {
		return nil, false
	}
	return rates, true
}

// chainEstimateAccountCharge mirrors legacyBreakdownFromLines accountChargeUsd
// for the input/output lines.
func chainEstimateAccountCharge(rates *chainEstimateRateSet, inputTokens, outputTokens int64) float64 {
	total := 0.0
	if rates.inputPer1M != nil && inputTokens > 0 {
		total += roundChainCost(float64(inputTokens) / 1_000_000 * *rates.inputPer1M)
	}
	if rates.outputPer1M != nil && outputTokens > 0 {
		total += roundChainCost(float64(outputTokens) / 1_000_000 * *rates.outputPer1M)
	}
	return roundChainCost(total)
}

// chainEstimateRateSet carries the two estimate rates.
type chainEstimateRateSet struct {
	inputPer1M  *float64
	outputPer1M *float64
}

// chainBillingPolicy mirrors the per-provider buildCostBreakdown differences
// the estimate path observes.
type chainBillingPolicy struct {
	longContext bool
}

// chainBillingPolicyFor mirrors providerBillingPolicyForProvider.
func chainBillingPolicyFor(providerCode string) *chainBillingPolicy {
	switch chainNormalizeProviderToken(providerCode) {
	case "openai", "gpt":
		return &chainBillingPolicy{longContext: true}
	case "gemini", "xai":
		return &chainBillingPolicy{longContext: true}
	case "anthropic", "deepseek", "glm":
		// anthropic keeps standard tier rates without long-context
		// multipliers; deepseek/glm use standardRates entirely.
		return &chainBillingPolicy{longContext: false}
	default:
		return nil
	}
}

func chainValidMultiplier(value *float64) float64 {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || *value <= 0 {
		return 1
	}
	return *value
}

func chainMultiplyRate(value *float64, multiplier float64) *float64 {
	if value == nil {
		return nil
	}
	out := *value * multiplier
	return &out
}

// roundChainCost mirrors roundCost: Number(value.toFixed(10)).
func roundChainCost(value float64) float64 {
	out, err := strconv.ParseFloat(strconv.FormatFloat(value, 'f', 10, 64), 64)
	if err != nil {
		return value
	}
	return out
}
