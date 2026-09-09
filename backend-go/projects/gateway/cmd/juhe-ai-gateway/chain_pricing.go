package main

// G20 phase-3 in-flight quota cost estimator + the synchronous usage pricing
// catalog: the composition adapters over the shared internal/pricing billing
// engine (buildProviderBillingCostBreakdown port).
//
// Both adapters resolve the model pricing row through the runtime cache model
// catalog (listProviderModelCatalog: built-in + account/custom rows,
// findCatalogItem trim-equality match) and delegate every billing rule to
// pricing.BuildCostBreakdown: service-tier exact rates, the per-provider
// policies (openai/gemini/xai long-context multipliers, anthropic/deepseek/glm
// tier rejection + standard rates), cache/image/audio line items and the
// accountChargeUsd aggregation. There is deliberately no second estimate
// implementation here — the earlier chain-local rate/multiplier copy was
// retired in favour of the engine (adjudication: threshold/edge divergences
// resolved in the engine's favour).

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pricing"
)

// gatewayusagePricingCostInput aliases the frozen usage pricing input so the
// adapter bodies read against the same shape gatewayusage freezes.
type gatewayusagePricingCostInput = gatewayusage.PricingCostInput

// chainCatalogLookupTimeout bounds one synchronous catalog lookup (the usage
// service calls this adapter inside the finalization pipeline).
const chainCatalogLookupTimeout = 10 * time.Second

// chainCostEstimator implements gatewayquota.CostEstimator over the runtime
// cache model catalog (Node estimateCatalogCostUsdAsync →
// buildProviderBillingCostBreakdown → accountChargeUsd).
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
	catalogPricing := e.resolveCatalogPricing(ctx, input.ProviderCode, input.SystemAccountID, input.Model)
	if catalogPricing == nil {
		return 0, false
	}
	breakdown := pricing.BuildCostBreakdown(catalogPricing, pricing.CostInput{
		ProviderCode: catalogPricing.ProviderCode,
		Model:        catalogPricing.Model,
		ServiceTier:  input.ServiceTier,
		InputTokens:  chainTokensFloat(float64(input.InputTokens)),
		OutputTokens: chainTokensFloat(float64(input.OutputTokens)),
	})
	if breakdown == nil || breakdown.AccountChargeUsd == nil {
		return 0, false
	}
	return *breakdown.AccountChargeUsd, true
}

// resolveCatalogPricing mirrors resolveCatalogPricingAsync: catalog row lookup
// plus the conversion onto the pricing.Pricing shape the engine consumes.
func (e *chainCostEstimator) resolveCatalogPricing(ctx context.Context, providerCode, systemAccountID, model string) *pricing.Pricing {
	catalog, err := e.cache.ListCachedProviderModelCatalogAsync(ctx, gatewayruntimecache.ModelCatalogListOptions{
		ProviderCode:    providerCode,
		SystemAccountID: systemAccountID,
	})
	if err != nil {
		return nil
	}
	item := findChainCatalogItem(catalog, model)
	if item == nil {
		return nil
	}
	return chainCatalogPricing(item)
}

// chainUsagePricingCatalog implements gatewayusage.PricingCatalog over the
// same runtime-cache catalog + pricing engine (Node model-catalog.service.ts
// estimateCatalogCostUsd / estimateCatalogCacheReadCostUsd /
// estimateCatalogCacheWriteCostUsd / resolveCatalogPricingModel; the service
// gates the synchronous surface on cacheDriver !== 'redis').
type chainUsagePricingCatalog struct {
	cache *gatewayruntimecache.Service
}

func newChainUsagePricingCatalog(cache *gatewayruntimecache.Service) *chainUsagePricingCatalog {
	return &chainUsagePricingCatalog{cache: cache}
}

// ResolvePricingModel mirrors resolveCatalogPricingModel: the matched catalog
// row's model id; empty mirrors the Node undefined.
func (c *chainUsagePricingCatalog) ResolvePricingModel(providerCode, systemAccountID, model string) string {
	if c == nil || c.cache == nil || strings.TrimSpace(model) == "" {
		return ""
	}
	item := c.findItem(providerCode, systemAccountID, model)
	if item == nil {
		return ""
	}
	return item.Model
}

// EstimateCost mirrors estimateCatalogCostUsd; nil = the Node undefined.
func (c *chainUsagePricingCatalog) EstimateCost(input gatewayusagePricingCostInput) *float64 {
	breakdown := c.breakdownFor(input, true)
	if breakdown == nil || breakdown.AccountChargeUsd == nil {
		return nil
	}
	cost := *breakdown.AccountChargeUsd
	return &cost
}

// EstimateCacheReadCost mirrors estimateCatalogCacheReadCostUsd.
func (c *chainUsagePricingCatalog) EstimateCacheReadCost(input gatewayusagePricingCostInput) *float64 {
	if input.CacheReadTokens == nil {
		return nil
	}
	breakdown := c.breakdownFor(input, false)
	if breakdown == nil || breakdown.CacheReadUsdPer1M == nil {
		return nil
	}
	if breakdown.CacheReadCostUsd != nil {
		cost := *breakdown.CacheReadCostUsd
		return &cost
	}
	zero := 0.0
	return &zero
}

// EstimateCacheWriteCost mirrors estimateCatalogCacheWriteCostUsd
// (cacheWriteCostFromBreakdown).
func (c *chainUsagePricingCatalog) EstimateCacheWriteCost(input gatewayusagePricingCostInput) *float64 {
	if input.CacheWriteTokens == nil && input.CacheWrite1hTokens == nil {
		return nil
	}
	breakdown := c.breakdownFor(input, false)
	if breakdown == nil {
		return nil
	}
	values := []*float64{breakdown.CacheWriteCostUsd, breakdown.CacheWrite1hCostUsd}
	total := 0.0
	any := false
	for _, value := range values {
		if value == nil {
			continue
		}
		any = true
		total += *value
	}
	if any {
		out := chainRoundCost(total)
		return &out
	}
	cacheWriteZero := input.CacheWriteTokens == nil || *input.CacheWriteTokens == 0
	cacheWrite1hZero := input.CacheWrite1hTokens == nil || *input.CacheWrite1hTokens == 0
	if cacheWriteZero && cacheWrite1hZero &&
		(breakdown.CacheWriteUsdPer1M != nil || breakdown.CacheWrite1hUsdPer1M != nil) {
		zero := 0.0
		return &zero
	}
	return nil
}

// breakdownFor resolves the catalog row and runs the billing engine.
// withCostDimensions mirrors the hasAnyCostDimension guard of
// estimateCatalogCostUsd (the cache estimators gate on their own token
// presence instead).
func (c *chainUsagePricingCatalog) breakdownFor(input gatewayusagePricingCostInput, withCostDimensions bool) *pricing.CostBreakdown {
	if c == nil || c.cache == nil || strings.TrimSpace(input.Model) == "" {
		return nil
	}
	if withCostDimensions && !chainHasAnyCostDimension(input) {
		return nil
	}
	item := c.findItem(input.ProviderCode, input.SystemAccountID, input.Model)
	if item == nil {
		return nil
	}
	catalogPricing := chainCatalogPricing(item)
	if catalogPricing == nil {
		return nil
	}
	return pricing.BuildCostBreakdown(catalogPricing, chainPricingCostInput(catalogPricing, input))
}

func (c *chainUsagePricingCatalog) findItem(providerCode, systemAccountID, model string) *gatewayruntimecache.ProviderModelCatalogItem {
	ctx, cancel := context.WithTimeout(context.Background(), chainCatalogLookupTimeout)
	defer cancel()
	catalog, err := c.cache.ListCachedProviderModelCatalogAsync(ctx, gatewayruntimecache.ModelCatalogListOptions{
		ProviderCode:    providerCode,
		SystemAccountID: systemAccountID,
	})
	if err != nil {
		return nil
	}
	return findChainCatalogItem(catalog, model)
}

// ---- catalog row → pricing.Pricing conversion ----

// chainCatalogPricing converts a runtime cache catalog row onto the
// pricing.Pricing shape the shared billing engine consumes (the Node billing
// path reads the catalog item itself as the pricing row).
func chainCatalogPricing(item *gatewayruntimecache.ProviderModelCatalogItem) *pricing.Pricing {
	if item == nil {
		return nil
	}
	return &pricing.Pricing{
		ProviderCode: item.ProviderCode,
		Model:        item.Model,
		Mode:         chainDerefString(item.Mode),
		PriceSet: pricing.PriceSet{
			InputUsdPer1M:               item.InputUsdPer1M,
			OutputUsdPer1M:              item.OutputUsdPer1M,
			CachedInputUsdPer1M:         item.CachedInputUsdPer1M,
			CacheWriteUsdPer1M:          item.CacheWriteUsdPer1M,
			CacheWrite1hUsdPer1M:        item.CacheWrite1hUsdPer1M,
			CacheStorageUsdPer1MPerHour: item.CacheStorageUsdPer1MPerHour,
			ImageInputUsdPer1M:          item.ImageInputUsdPer1M,
			ImageOutputUsdPer1M:         item.ImageOutputUsdPer1M,
			AudioInputUsdPer1M:          item.AudioInputUsdPer1M,
			AudioOutputUsdPer1M:         item.AudioOutputUsdPer1M,
			OutputUsdPerImage:           item.OutputUsdPerImage,
		},
		CachedImageInputUsdPer1M: item.CachedImageInputUsdPer1M,
		ServiceTierPrices:        chainServiceTierPrices(item.ServiceTierPrices),

		SupportedServiceTiers: append([]string(nil), item.SupportedServiceTiers...),

		LongContextInputTokenThreshold:          chainInt64ToInt(item.LongContextInputTokenThreshold),
		LongContextInputTokenThresholdInclusive: item.LongContextInputTokenThresholdInclusive != nil && *item.LongContextInputTokenThresholdInclusive,
		LongContextInputCostMultiplier:          item.LongContextInputCostMultiplier,
		LongContextOutputCostMultiplier:         item.LongContextOutputCostMultiplier,
	}
}

// chainServiceTierPrices deserializes the stored serviceTierPrices JSON
// (Record<string, ModelPriceSet>) onto the engine's typed map.
func chainServiceTierPrices(raw json.RawMessage) pricing.ServiceTierPrices {
	if len(raw) == 0 {
		return nil
	}
	var decoded map[string]pricing.PriceSet
	if json.Unmarshal(raw, &decoded) != nil || decoded == nil {
		return nil
	}
	if len(decoded) == 0 {
		return nil
	}
	return decoded
}

// chainPricingCostInput converts the frozen usage pricing input onto the
// engine CostInput (token *int → *float64).
func chainPricingCostInput(catalogPricing *pricing.Pricing, input gatewayusagePricingCostInput) pricing.CostInput {
	return pricing.CostInput{
		ProviderCode:       catalogPricing.ProviderCode,
		Model:              catalogPricing.Model,
		ServiceTier:        input.ServiceTier,
		InputTokens:        chainIntToFloat(input.InputTokens),
		OutputTokens:       chainIntToFloat(input.OutputTokens),
		CacheReadTokens:    chainIntToFloat(input.CacheReadTokens),
		CacheWriteTokens:   chainIntToFloat(input.CacheWriteTokens),
		CacheWrite1hTokens: chainIntToFloat(input.CacheWrite1hTokens),
		ThinkingTokens:     chainIntToFloat(input.ThinkingTokens),
		InputImageTokens:   chainIntToFloat(input.InputImageTokens),
		OutputImageTokens:  chainIntToFloat(input.OutputImageTokens),
		InputAudioTokens:   chainIntToFloat(input.InputAudioTokens),
		OutputAudioTokens:  chainIntToFloat(input.OutputAudioTokens),
		OutputImageCount:   chainIntToFloat(input.OutputImageCount),
	}
}

// chainHasAnyCostDimension mirrors hasAnyCostDimension.
func chainHasAnyCostDimension(input gatewayusagePricingCostInput) bool {
	return input.InputTokens != nil || input.OutputTokens != nil ||
		input.CacheReadTokens != nil || input.CacheWriteTokens != nil ||
		input.CacheWrite1hTokens != nil || input.InputImageTokens != nil ||
		input.OutputImageTokens != nil || input.InputAudioTokens != nil ||
		input.OutputAudioTokens != nil || input.OutputImageCount != nil
}

func chainIntToFloat(value *int) *float64 {
	if value == nil {
		return nil
	}
	out := float64(*value)
	return &out
}

// chainRoundCost mirrors roundCost / Number(value.toFixed(10)).
func chainRoundCost(value float64) float64 {
	out, err := strconv.ParseFloat(strconv.FormatFloat(value, 'f', 10, 64), 64)
	if err != nil {
		return value
	}
	return out
}

func chainTokensFloat(value float64) *float64 {
	return &value
}

func chainDerefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func chainInt64ToInt(value *int64) *int {
	if value == nil {
		return nil
	}
	out := int(*value)
	return &out
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
