package pricing

// Golden vectors derived from the archived Node sources under
// migration-backup/node/final-archive/backend/src/modules/model-pricing/:
//   - lookup: model-pricing.service.ts (findProviderModelPricing closure,
//     shutdown handling) + provider-driver.registry.ts (candidates, alias,
//     unavailable list) + the *.data.ts snapshot rows,
//   - billing: provider-billing.{shared,policies,service,types}.ts.
// Cost expectations are exact float64 comparisons: Node and Go both run
// IEEE-754 doubles through the same operation order (perMillion → toFixed(8),
// line costs → toFixed(10)), so golden values are bit-identical.

import (
	"testing"
)

func TestFindProviderModelPricingCanonicalAliasFallback(t *testing.T) {
	// No "gpt-5.6" row exists (openai-model-pricing.gpt5.data.ts: only
	// gpt-5.6-sol/terra/luna); buildModelCandidates("gpt-5.6") is empty, so
	// the second-chance canonical alias resolves gpt-5.6 -> gpt-5.6-sol.
	got := FindProviderModelPricing("openai", "gpt-5.6")
	if got == nil {
		t.Fatal("gpt-5.6 should resolve via canonical alias to gpt-5.6-sol")
	}
	if got.Model != "gpt-5.6-sol" {
		t.Fatalf("model = %q, want gpt-5.6-sol", got.Model)
	}
	if got.ProviderCode != "openai" {
		t.Fatalf("providerCode = %q, want openai (normalized caller code)", got.ProviderCode)
	}
	if got.Source != "openai-pricing-snapshot" {
		t.Fatalf("source = %q, want openai-pricing-snapshot", got.Source)
	}
	// Row: input_cost_per_token 0.000005, output 0.00003, cache_write
	// 0.00000625, cache_read 5e-7; perMillion(x) = toFixed(8).
	if got.InputUsdPer1M == nil || *got.InputUsdPer1M != 5 {
		t.Fatalf("inputUsdPer1M = %v, want 5", got.InputUsdPer1M)
	}
	if got.OutputUsdPer1M == nil || *got.OutputUsdPer1M != 30 {
		t.Fatalf("outputUsdPer1M = %v, want 30", got.OutputUsdPer1M)
	}
	if got.CacheWriteUsdPer1M == nil || *got.CacheWriteUsdPer1M != 6.25 {
		t.Fatalf("cacheWriteUsdPer1M = %v, want 6.25", got.CacheWriteUsdPer1M)
	}
	if got.CachedInputUsdPer1M == nil || *got.CachedInputUsdPer1M != 0.5 {
		t.Fatalf("cachedInputUsdPer1M = %v, want 0.5", got.CachedInputUsdPer1M)
	}
	if got.LongContextInputTokenThreshold == nil || *got.LongContextInputTokenThreshold != 272000 {
		t.Fatalf("longContextInputTokenThreshold = %v, want 272000", got.LongContextInputTokenThreshold)
	}
	if got.SupportsServiceTier != true || len(got.SupportedServiceTiers) != 2 {
		t.Fatalf("service tiers = %v, want [priority flex]", got.SupportedServiceTiers)
	}
}

func TestFindProviderModelPricingCandidateDateSuffix(t *testing.T) {
	// openai data has "gpt-4.1" but no dated row; the date-suffix candidate
	// (candidate list first branch) resolves gpt-4.1-2025-04-14.
	got := FindProviderModelPricing("openai", "gpt-4.1-2025-04-14")
	if got == nil || got.Model != "gpt-4.1" {
		t.Fatalf("model = %v, want gpt-4.1 via date-suffix candidate", got)
	}
	if got.InputUsdPer1M == nil || *got.InputUsdPer1M != 2 {
		t.Fatalf("inputUsdPer1M = %v, want 2", got.InputUsdPer1M)
	}
	if got.OutputUsdPer1M == nil || *got.OutputUsdPer1M != 8 {
		t.Fatalf("outputUsdPer1M = %v, want 8", got.OutputUsdPer1M)
	}
	if got.CachedInputUsdPer1M == nil || *got.CachedInputUsdPer1M != 0.5 {
		t.Fatalf("cachedInputUsdPer1M = %v, want 0.5", got.CachedInputUsdPer1M)
	}

	// anthropic: claude-sonnet-5 has no dated row either
	// (anthropic-model-pricing.data.ts lists claude-sonnet-5 only), the
	// YYYYMMDD suffix strip resolves the same way.
	anthropic := FindProviderModelPricing("anthropic", "claude-sonnet-5-20260630")
	if anthropic == nil || anthropic.Model != "claude-sonnet-5" {
		t.Fatalf("model = %v, want claude-sonnet-5 via date-suffix candidate", anthropic)
	}
	if anthropic.InputUsdPer1M == nil || *anthropic.InputUsdPer1M != 2 {
		t.Fatalf("inputUsdPer1M = %v, want 2", anthropic.InputUsdPer1M)
	}
	// model() factory: cache_write = in*1.25 -> 2.5, 1h = in*2 -> 4, read =
	// in*0.1 -> 0.2 (per-million).
	if anthropic.CacheWriteUsdPer1M == nil || *anthropic.CacheWriteUsdPer1M != 2.5 {
		t.Fatalf("cacheWriteUsdPer1M = %v, want 2.5", anthropic.CacheWriteUsdPer1M)
	}
	if anthropic.CacheWrite1hUsdPer1M == nil || *anthropic.CacheWrite1hUsdPer1M != 4 {
		t.Fatalf("cacheWrite1hUsdPer1M = %v, want 4", anthropic.CacheWrite1hUsdPer1M)
	}
	if anthropic.CachedInputUsdPer1M == nil || *anthropic.CachedInputUsdPer1M != 0.2 {
		t.Fatalf("cachedInputUsdPer1M = %v, want 0.2", anthropic.CachedInputUsdPer1M)
	}

	// Exact snapshot rows win without any candidate work.
	dated := FindProviderModelPricing("openai", "gpt-5.5-2026-04-23")
	if dated == nil || dated.Model != "gpt-5.5-2026-04-23" {
		t.Fatalf("model = %v, want exact gpt-5.5-2026-04-23 row", dated)
	}
	exactAnthropic := FindProviderModelPricing("anthropic", "claude-sonnet-4-5-20250929")
	if exactAnthropic == nil || exactAnthropic.Model != "claude-sonnet-4-5-20250929" {
		t.Fatalf("model = %v, want exact claude-sonnet-4-5-20250929 row", exactAnthropic)
	}
}

func TestFindProviderModelPricingVendorToken(t *testing.T) {
	// The 'gpt' vendor code rides the openai-compatible driver like Node
	// isOpenAICompatibleProviderCode; the pricing keeps the caller token.
	got := FindProviderModelPricing("gpt", "gpt-5.5")
	if got == nil || got.Model != "gpt-5.5" {
		t.Fatalf("model = %v, want gpt-5.5 via gpt vendor token", got)
	}
	if got.ProviderCode != "gpt" {
		t.Fatalf("providerCode = %q, want gpt", got.ProviderCode)
	}
}

// TestFindRawProviderModelPricingDuplicateNameShutdownGivesUpLayer pins the
// 审查 #7 semantics: each lookup layer is models.find(byName) FIRST and the
// shutdown check applies to that first same-named row only (model-pricing
// .service.ts:169-181). A shutdown first row gives the layer up even when a
// later duplicate row is live — the lookup must never rescan past the first
// same-named row.
func TestFindRawProviderModelPricingDuplicateNameShutdownGivesUpLayer(t *testing.T) {
	entry := &providerEntry{
		providerID: "openai-compatible",
		rawModels: []rawModel{
			{Model: "dup-model", ShutdownDate: "2026-01-01", InputCostPerToken: perToken(1)},
			{Model: "dup-model", InputCostPerToken: perToken(2)}, // later live duplicate must not answer
			{Model: "fallback", ShutdownDate: "2026-01-01", InputCostPerToken: perToken(3)},
			{Model: "fallback", InputCostPerToken: perToken(4)}, // same rule at the candidate layer
		},
	}

	// Exact layer: the first dup-model row is shutdown, so the layer gives
	// up; no candidate names a live row and the canonical alias is a no-op —
	// the whole lookup resolves to undefined.
	if got := findRawProviderModelPricing(entry, "dup-model", "2026-06-01"); got != nil {
		t.Fatalf("shutdown first duplicate must give the exact layer up, got %q", got.Model)
	}
	// Before the shutdown day the first row answers.
	before := findRawProviderModelPricing(entry, "dup-model", "2025-12-31")
	if before == nil || before.InputCostPerToken == nil || *before.InputCostPerToken != 1/1e6 {
		t.Fatalf("pre-shutdown first row must answer, got %+v", before)
	}

	// Candidate layer (date-suffix candidate "fallback"): its first row is
	// shutdown, the candidate gives up instead of rescanning to the live
	// duplicate.
	if got := findRawProviderModelPricing(entry, "fallback-2026-02-02", "2026-06-01"); got != nil {
		t.Fatalf("shutdown candidate duplicate must give the candidate layer up, got %q", got.Model)
	}

	// Canonical-alias layer: gpt-5.6 -> gpt-5.6-sol with a shutdown first
	// dup and a live later dup must stay undefined.
	aliasEntry := &providerEntry{
		providerID: "openai-compatible",
		rawModels: []rawModel{
			{Model: "gpt-5.6-sol", ShutdownDate: "2026-01-01"},
			{Model: "gpt-5.6-sol"},
		},
	}
	if got := findRawProviderModelPricing(aliasEntry, "gpt-5.6", "2026-06-01"); got != nil {
		t.Fatalf("shutdown canonical-alias duplicate must stay undefined, got %q", got.Model)
	}
}

func TestFindProviderModelPricingUnavailableAndUnknown(t *testing.T) {
	if got := FindProviderModelPricing("openai", "chatgpt-4o-latest"); got != nil {
		t.Fatalf("chatgpt-4o-latest is unavailable, got %v", got.Model)
	}
	if got := FindProviderModelPricing("openai", "o1-preview-2024-09-12"); got != nil {
		t.Fatalf("o1-preview* is unavailable by prefix, got %v", got.Model)
	}
	if got := FindProviderModelPricing("openai", "no-such-model"); got != nil {
		t.Fatalf("unknown model should not resolve, got %v", got.Model)
	}
	if got := FindProviderModelPricing("unknown-provider", "gpt-5.5"); got != nil {
		t.Fatalf("unknown provider should not resolve, got %v", got.Model)
	}
	if got := FindProviderModelPricing("openai", "   "); got != nil {
		t.Fatalf("blank model should not resolve, got %v", got.Model)
	}
}

func TestFindProviderModelPricingShutdownAsOf(t *testing.T) {
	// gpt-4.1-nano carries shutdown_date 2026-10-23 (gpt4 data); Node
	// hasModelShutdown filters shutdown_date <= asOfDate.
	before := FindProviderModelPricingAsOf("openai", "gpt-4.1-nano", "2026-10-22")
	if before == nil || before.Model != "gpt-4.1-nano" {
		t.Fatalf("before shutdown: model = %v, want gpt-4.1-nano", before)
	}
	// On the shutdown day the exact row is filtered, but the candidate list
	// keeps walking: "gpt-4.1-nano" starts with "gpt-4.1-", so the live
	// gpt-4.1 row answers (Node behaviour, still cheaper than resolving a
	// dead model).
	onDay := FindProviderModelPricingAsOf("openai", "gpt-4.1-nano", "2026-10-23")
	if onDay == nil || onDay.Model != "gpt-4.1" {
		t.Fatalf("on shutdown day the lookup must fall through to gpt-4.1, got %v", onDay)
	}
	after := FindProviderModelPricingAsOf("openai", "gpt-4.1-nano-2025-04-14", "2026-11-01")
	if after == nil || after.Model != "gpt-4.1" {
		t.Fatalf("shutdown row via candidates must fall through to gpt-4.1, got %v", after)
	}
}

func TestListProviderModelPricingOrderAndVendor(t *testing.T) {
	openai := ListProviderModelPricing("openai")
	if len(openai) == 0 {
		t.Fatal("openai list must not be empty")
	}
	// compareProviderModels: catalog order first; gpt-6-astra carries
	// catalog_order -1 in the gpt5 snapshot, then gpt-5.6-sol (order 0).
	if openai[0].Model != "gpt-6-astra" {
		t.Fatalf("first model = %q, want gpt-6-astra (catalog_order -1)", openai[0].Model)
	}
	if openai[1].Model != "gpt-5.6-sol" {
		t.Fatalf("second model = %q, want gpt-5.6-sol (catalog_order 0)", openai[1].Model)
	}
	for _, item := range openai {
		if item.ShutdownDate != "" && item.ShutdownDate <= currentUTCDate() {
			t.Fatalf("shutdown row %s leaked into the live list", item.Model)
		}
	}
	if len(ListProviderModelPricing("gpt")) != len(openai) {
		t.Fatal("gpt vendor token must list the same openai-compatible rows")
	}
	if got := ListProviderModelPricing("unknown-provider"); len(got) != 0 {
		t.Fatalf("unknown provider must list nothing, got %d rows", len(got))
	}
}

// billingBreakdown is a lookup + bill helper for the golden vectors below.
func billingBreakdown(t *testing.T, providerCode, model string, input CostInput) *CostBreakdown {
	t.Helper()
	pricing := FindProviderModelPricing(providerCode, model)
	if pricing == nil {
		t.Fatalf("fixture: %s/%s must resolve", providerCode, model)
	}
	return BuildCostBreakdown(pricing, input)
}

func wantFloat(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil, want %v", name, want)
	}
	if *got != want {
		t.Fatalf("%s = %v, want %v (bit-exact golden)", name, *got, want)
	}
}

func TestBuildCostBreakdownOpenAIStandardTokens(t *testing.T) {
	// gpt-5.5: input 5, output 30 per 1M. 1M in + 0.5M out = 5 + 15.
	got := billingBreakdown(t, "openai", "gpt-5.5", CostInput{
		ProviderCode: "openai",
		Model:        "gpt-5.5",
		InputTokens:  f64p(1_000_000),
		OutputTokens: f64p(500_000),
	})
	if got.Currency != "USD" || got.BillingPolicy != "openai" {
		t.Fatalf("currency/policy = %q/%q, want USD/openai", got.Currency, got.BillingPolicy)
	}
	wantFloat(t, "inputCostUsd", got.InputCostUsd, 5)
	wantFloat(t, "outputCostUsd", got.OutputCostUsd, 15)
	wantFloat(t, "accountChargeUsd", got.AccountChargeUsd, 20)
	wantFloat(t, "inputUsdPer1M", got.InputUsdPer1M, 5)
	wantFloat(t, "outputUsdPer1M", got.OutputUsdPer1M, 30)
	if got.CacheReadCostUsd != nil || got.CacheWriteCostUsd != nil || got.CacheWrite1hCostUsd != nil {
		t.Fatal("cache lines must stay undefined without cache usage")
	}
	if got.Multiplier != 1 {
		t.Fatalf("multiplier = %v, want 1", got.Multiplier)
	}
	if got.ServiceTierPricingSource != TierSourceDefault {
		t.Fatalf("serviceTierPricingSource = %q, want default", got.ServiceTierPricingSource)
	}
	if len(got.LineItems) != 2 {
		t.Fatalf("line items = %d, want input+output", len(got.LineItems))
	}
	first := got.LineItems[0]
	if first.Kind != LineInput || first.Label != "输入 Token" || first.Unit != LineUnitToken || first.UnitSize != 1_000_000 || first.Quantity != 1_000_000 {
		t.Fatalf("first line = %+v, want default-labeled input token line", first)
	}
	if first.CostUsd != 5 {
		t.Fatalf("first line cost = %v, want 5", first.CostUsd)
	}
}

func TestBuildCostBreakdownOpenAICacheSplit(t *testing.T) {
	// gpt-5.6-sol: input 5, cache_write 6.25, cache_read 0.5. OpenAI counts
	// cache-read tokens inside the input total; the 1h column is absent so
	// the 1h line falls back to the standard write rate. Token counts stay
	// below the 272000 long-context threshold so standard rates bill.
	got := billingBreakdown(t, "openai", "gpt-5.6-sol", CostInput{
		ProviderCode:       "openai",
		Model:              "gpt-5.6-sol",
		InputTokens:        f64p(250_000),
		CacheReadTokens:    f64p(100_000),
		CacheWriteTokens:   f64p(60_000),
		CacheWrite1hTokens: f64p(20_000),
	})
	wantFloat(t, "inputCostUsd", got.InputCostUsd, 0.75) // (250k - 100k) * 5/1M
	wantFloat(t, "cacheReadCostUsd", got.CacheReadCostUsd, 0.05)
	wantFloat(t, "cacheWriteCostUsd", got.CacheWriteCostUsd, 0.25)      // 40k * 6.25/1M
	wantFloat(t, "cacheWrite1hCostUsd", got.CacheWrite1hCostUsd, 0.125) // 20k * 6.25/1M
	wantFloat(t, "cacheWrite1hUsdPer1M", got.CacheWrite1hUsdPer1M, 6.25)
	wantFloat(t, "accountChargeUsd", got.AccountChargeUsd, 1.175)
	if len(got.LineItems) != 4 {
		t.Fatalf("line items = %d, want input/cache_read/cache_write/cache_write_1h", len(got.LineItems))
	}
	for _, line := range got.LineItems {
		switch line.Key {
		case "cache_read":
			if line.Label != "缓存读 Token" || line.UnitPriceUsd != 0.5 || line.Quantity != 100_000 {
				t.Fatalf("cache_read line = %+v", line)
			}
		case "cache_write":
			if line.Label != "缓存写入 Token" || line.Quantity != 40_000 {
				t.Fatalf("cache_write line = %+v", line)
			}
		case "cache_write_1h":
			if line.Label != "1h 缓存写入 Token" || line.Quantity != 20_000 {
				t.Fatalf("cache_write_1h line = %+v", line)
			}
		}
	}

	// Past the 272000 exclusive threshold the openai policy multiplies the
	// input-side rates by 2 and output by 1.5.
	long := billingBreakdown(t, "openai", "gpt-5.6-sol", CostInput{
		ProviderCode: "openai",
		Model:        "gpt-5.6-sol",
		InputTokens:  f64p(1_000_000),
		OutputTokens: f64p(100_000),
	})
	wantFloat(t, "long-context inputUsdPer1M", long.InputUsdPer1M, 10)
	wantFloat(t, "long-context outputUsdPer1M", long.OutputUsdPer1M, 45)
	wantFloat(t, "long-context accountChargeUsd", long.AccountChargeUsd, 14.5)
}

func TestBuildCostBreakdownServiceTierExactPrices(t *testing.T) {
	// gpt-5.5 priority: input 12.5, output 75, cache read 1.25 (tier table).
	priority := billingBreakdown(t, "openai", "gpt-5.5", CostInput{
		ProviderCode: "openai",
		Model:        "gpt-5.5",
		ServiceTier:  "priority",
		InputTokens:  f64p(1_000_000),
		OutputTokens: f64p(500_000),
	})
	wantFloat(t, "priority inputCostUsd", priority.InputCostUsd, 12.5)
	wantFloat(t, "priority outputCostUsd", priority.OutputCostUsd, 37.5)
	wantFloat(t, "priority accountChargeUsd", priority.AccountChargeUsd, 50)
	wantFloat(t, "priority inputUsdPer1M", priority.InputUsdPer1M, 12.5)
	if priority.ServiceTierPricingSource != TierSourceTierSpecific {
		t.Fatalf("priority source = %q, want tier_specific", priority.ServiceTierPricingSource)
	}

	// flex: input 2.5, output 15.
	flex := billingBreakdown(t, "openai", "gpt-5.5", CostInput{
		ProviderCode: "openai",
		Model:        "gpt-5.5",
		ServiceTier:  "flex",
		InputTokens:  f64p(1_000_000),
		OutputTokens: f64p(500_000),
	})
	wantFloat(t, "flex accountChargeUsd", flex.AccountChargeUsd, 10)
	if flex.ServiceTierPricingSource != TierSourceTierSpecific {
		t.Fatalf("flex source = %q, want tier_specific", flex.ServiceTierPricingSource)
	}

	// 'standard' and 'default' are no-tier markers; unknown tiers on the
	// openai policy collapse through hasAnyRate == false to undefined.
	for _, tier := range []string{"standard", "default", "batch", "mystery"} {
		got := billingBreakdown(t, "openai", "gpt-5.5", CostInput{
			ProviderCode: "openai",
			Model:        "gpt-5.5",
			ServiceTier:  tier,
			InputTokens:  f64p(1_000_000),
			OutputTokens: f64p(500_000),
		})
		if tier == "standard" || tier == "default" {
			wantFloat(t, tier+" accountChargeUsd", got.AccountChargeUsd, 20)
			continue
		}
		if got != nil {
			t.Fatalf("tier %q must collapse the breakdown, got %v", tier, got.AccountChargeUsd)
		}
	}
}

func TestBuildCostBreakdownAnthropicCacheSplit(t *testing.T) {
	// claude-sonnet-5: input 2, output 10, 5m write 2.5, 1h write 4, read
	// 0.2. Anthropic bills the full input separately from cached tokens and
	// splits cache writes into 5m/1h lines.
	got := billingBreakdown(t, "anthropic", "claude-sonnet-5", CostInput{
		ProviderCode:       "anthropic",
		Model:              "claude-sonnet-5",
		InputTokens:        f64p(1_000_000),
		CacheReadTokens:    f64p(200_000),
		CacheWriteTokens:   f64p(300_000),
		CacheWrite1hTokens: f64p(100_000),
		OutputTokens:       f64p(100_000),
	})
	wantFloat(t, "inputCostUsd", got.InputCostUsd, 2) // full 1M, no cache deduction
	wantFloat(t, "cacheReadCostUsd", got.CacheReadCostUsd, 0.04)
	wantFloat(t, "cacheWriteCostUsd", got.CacheWriteCostUsd, 0.5)     // 0.2M * 2.5/1M
	wantFloat(t, "cacheWrite1hCostUsd", got.CacheWrite1hCostUsd, 0.4) // 0.1M * 4/1M
	wantFloat(t, "outputCostUsd", got.OutputCostUsd, 1)
	wantFloat(t, "accountChargeUsd", got.AccountChargeUsd, 3.94)
	wantFloat(t, "cacheWriteUsdPer1M", got.CacheWriteUsdPer1M, 2.5)
	wantFloat(t, "cacheWrite1hUsdPer1M", got.CacheWrite1hUsdPer1M, 4)
	if got.BillingPolicy != "anthropic" {
		t.Fatalf("billingPolicy = %q, want anthropic", got.BillingPolicy)
	}
	for _, line := range got.LineItems {
		switch line.Key {
		case "cache_read":
			if line.Label != "缓存读 Token" {
				t.Fatalf("cache_read label = %q", line.Label)
			}
		case "cache_write":
			if line.Label != "5m 缓存写入 Token" {
				t.Fatalf("cache_write label = %q", line.Label)
			}
		case "cache_write_1h":
			if line.Label != "1h 缓存写入 Token" {
				t.Fatalf("cache_write_1h label = %q", line.Label)
			}
		}
	}

	// Anthropic rejects unsupported tiers up front.
	if got := billingBreakdown(t, "anthropic", "claude-sonnet-5", CostInput{
		ProviderCode: "anthropic",
		Model:        "claude-sonnet-5",
		ServiceTier:  "priority",
		InputTokens:  f64p(1_000),
	}); got != nil {
		t.Fatal("anthropic must reject the unsupported priority tier")
	}
}

func TestBuildCostBreakdownGeminiLongContext(t *testing.T) {
	// gemini-3.1-pro-preview: input 2, output 12, cached 0.2; threshold
	// 200000 exclusive; multipliers input x2 / output x1.5.
	long := billingBreakdown(t, "gemini", "gemini-3.1-pro-preview", CostInput{
		ProviderCode: "gemini",
		Model:        "gemini-3.1-pro-preview",
		InputTokens:  f64p(250_000),
		OutputTokens: f64p(100_000),
	})
	wantFloat(t, "inputUsdPer1M", long.InputUsdPer1M, 4)
	wantFloat(t, "outputUsdPer1M", long.OutputUsdPer1M, 18)
	wantFloat(t, "inputCostUsd", long.InputCostUsd, 1)
	wantFloat(t, "outputCostUsd", long.OutputCostUsd, 1.8)
	wantFloat(t, "accountChargeUsd", long.AccountChargeUsd, 2.8)
	if long.ServiceTierPricingSource != TierSourceDefault {
		t.Fatalf("source = %q, want default", long.ServiceTierPricingSource)
	}

	// Exactly at the exclusive threshold the multipliers must not apply.
	at := billingBreakdown(t, "gemini", "gemini-3.1-pro-preview", CostInput{
		ProviderCode: "gemini",
		Model:        "gemini-3.1-pro-preview",
		InputTokens:  f64p(200_000),
	})
	wantFloat(t, "at-threshold accountChargeUsd", at.AccountChargeUsd, 0.4)
	wantFloat(t, "at-threshold inputUsdPer1M", at.InputUsdPer1M, 2)

	// The flex tier table (input 1, output 6) feeds the long-context
	// multiplication and keeps the tier_specific source (Node spreads
	// {...rates} without touching the source).
	flex := billingBreakdown(t, "gemini", "gemini-3.1-pro-preview", CostInput{
		ProviderCode: "gemini",
		Model:        "gemini-3.1-pro-preview",
		ServiceTier:  "flex",
		InputTokens:  f64p(250_000),
		OutputTokens: f64p(100_000),
	})
	wantFloat(t, "flex long-context inputUsdPer1M", flex.InputUsdPer1M, 2)
	wantFloat(t, "flex long-context outputUsdPer1M", flex.OutputUsdPer1M, 9)
	wantFloat(t, "flex accountChargeUsd", flex.AccountChargeUsd, 0.5+0.9)
	if flex.ServiceTierPricingSource != TierSourceTierSpecific {
		t.Fatalf("flex source = %q, want tier_specific (preserved through long context)", flex.ServiceTierPricingSource)
	}
}

func TestBuildCostBreakdownOpenAIImagePolicy(t *testing.T) {
	// gpt-image-2 (mode image_generation): input 5, image input 8, image
	// output 30. Without explicit outputImageTokens the text output stream
	// is billed as image output.
	got := billingBreakdown(t, "openai", "gpt-image-2", CostInput{
		ProviderCode:     "openai",
		Model:            "gpt-image-2",
		InputTokens:      f64p(1_000_000),
		InputImageTokens: f64p(100_000),
		OutputTokens:     f64p(200_000),
	})
	wantFloat(t, "inputCostUsd", got.InputCostUsd, 4.5) // 1M - 0.1M image input
	wantFloat(t, "inputImageCostUsd", got.InputImageCostUsd, 0.8)
	wantFloat(t, "outputImageCostUsd", got.OutputImageCostUsd, 6)
	wantFloat(t, "accountChargeUsd", got.AccountChargeUsd, 11.3)
	if got.OutputCostUsd != nil {
		t.Fatalf("plain output line must not appear, got %v", got.OutputCostUsd)
	}
	wantFloat(t, "outputImageUsdPer1M", got.OutputImageUsdPer1M, 30)
}

func TestBuildCostBreakdownDeepSeekPolicy(t *testing.T) {
	// deepseek-v4-flash: input 0.14, cached 0.0028, output 0.28; cache read
	// is included in the input total like OpenAI/Gemini.
	got := billingBreakdown(t, "deepseek", "deepseek-v4-flash", CostInput{
		ProviderCode:    "deepseek",
		Model:           "deepseek-v4-flash",
		InputTokens:     f64p(1_000_000),
		CacheReadTokens: f64p(500_000),
		OutputTokens:    f64p(100_000),
	})
	wantFloat(t, "inputCostUsd", got.InputCostUsd, 0.07) // 0.5M * 0.14/1M
	wantFloat(t, "cacheReadCostUsd", got.CacheReadCostUsd, 0.0014)
	wantFloat(t, "outputCostUsd", got.OutputCostUsd, 0.028)
	wantFloat(t, "accountChargeUsd", got.AccountChargeUsd, 0.0994)
	if got.BillingPolicy != "deepseek" {
		t.Fatalf("billingPolicy = %q, want deepseek", got.BillingPolicy)
	}
	// deepseek/glm reject unsupported tiers up front.
	if tiered := billingBreakdown(t, "deepseek", "deepseek-v4-flash", CostInput{
		ProviderCode: "deepseek",
		Model:        "deepseek-v4-flash",
		ServiceTier:  "priority",
		InputTokens:  f64p(1_000),
	}); tiered != nil {
		t.Fatal("deepseek must reject the unsupported priority tier")
	}
}

func TestBuildCostBreakdownUnpricedAndOverrides(t *testing.T) {
	// gpt-5.5 has no cache-write price: positive write tokens are unpriced
	// usage and collapse the breakdown.
	if got := billingBreakdown(t, "openai", "gpt-5.5", CostInput{
		ProviderCode:     "openai",
		Model:            "gpt-5.5",
		InputTokens:      f64p(1_000),
		CacheWriteTokens: f64p(100_000),
	}); got != nil {
		t.Fatal("unpriced cache write must collapse the breakdown")
	}
	// Image unit counts without a per-image price collapse too.
	if got := billingBreakdown(t, "openai", "gpt-5.5", CostInput{
		ProviderCode:     "openai",
		Model:            "gpt-5.5",
		OutputImageCount: f64p(1),
	}); got != nil {
		t.Fatal("unpriced output image count must collapse the breakdown")
	}
	// The explicit costUsd override wins for accountChargeUsd while the
	// line items stay computed.
	got := billingBreakdown(t, "openai", "gpt-5.5", CostInput{
		ProviderCode: "openai",
		Model:        "gpt-5.5",
		InputTokens:  f64p(1_000_000),
		CostUsd:      f64p(7.77),
	})
	wantFloat(t, "accountChargeUsd", got.AccountChargeUsd, 7.77)
	wantFloat(t, "inputCostUsd", got.InputCostUsd, 5)
	// Negative costUsd overrides are not finite and fall back to the sum.
	got = billingBreakdown(t, "openai", "gpt-5.5", CostInput{
		ProviderCode: "openai",
		Model:        "gpt-5.5",
		InputTokens:  f64p(1_000_000),
		CostUsd:      f64p(-1),
	})
	wantFloat(t, "negative override accountChargeUsd", got.AccountChargeUsd, 5)
}

func TestBuildCostBreakdownAudioAndImageUnitLines(t *testing.T) {
	// Engine-branch coverage on a synthetic pricing row (no shipped row
	// carries audio + per-image pricing together): audio tokens split off
	// the text lines and image units bill per image.
	pricing := &Pricing{
		ProviderCode: "openai",
		Model:        "synthetic-multimodal",
		PriceSet: PriceSet{
			InputUsdPer1M:       f64p(10),
			OutputUsdPer1M:      f64p(20),
			AudioInputUsdPer1M:  f64p(2),
			AudioOutputUsdPer1M: f64p(4),
			OutputUsdPerImage:   f64p(0.07),
		},
	}
	got := BuildCostBreakdown(pricing, CostInput{
		ProviderCode:      "openai",
		Model:             "synthetic-multimodal",
		InputTokens:       f64p(1_000_000),
		InputAudioTokens:  f64p(200_000),
		OutputTokens:      f64p(300_000),
		OutputAudioTokens: f64p(100_000),
		OutputImageCount:  f64p(2),
	})
	if got == nil {
		t.Fatal("synthetic breakdown must build")
	}
	wantFloat(t, "inputCostUsd", got.InputCostUsd, 8) // 1M - 0.2M audio
	wantFloat(t, "inputAudioCostUsd", got.InputAudioCostUsd, 0.4)
	wantFloat(t, "outputCostUsd", got.OutputCostUsd, 4) // 0.3M - 0.1M audio
	wantFloat(t, "outputAudioCostUsd", got.OutputAudioCostUsd, 0.4)
	wantFloat(t, "outputImageUnitCostUsd", got.OutputImageUnitCostUsd, 0.14)
	wantFloat(t, "accountChargeUsd", got.AccountChargeUsd, 12.94)
	wantFloat(t, "outputUsdPerImage", got.OutputUsdPerImage, 0.07)
}

func TestBuildCostBreakdownLongContextInclusiveThreshold(t *testing.T) {
	// Engine-branch coverage: the shipped rows all use exclusive thresholds,
	// so an inclusive synthetic row pins the >= comparison. The openai
	// policy carries the long-context application.
	pricing := &Pricing{
		ProviderCode:                            "openai",
		Model:                                   "synthetic-inclusive",
		PriceSet:                                PriceSet{InputUsdPer1M: f64p(1), OutputUsdPer1M: f64p(2)},
		LongContextInputTokenThreshold:          intp(100),
		LongContextInputTokenThresholdInclusive: true,
		LongContextInputCostMultiplier:          f64p(2),
	}
	at := BuildCostBreakdown(pricing, CostInput{
		ProviderCode: "openai",
		Model:        "synthetic-inclusive",
		InputTokens:  f64p(100),
	})
	wantFloat(t, "inclusive at-threshold inputUsdPer1M", at.InputUsdPer1M, 2)
	wantFloat(t, "inclusive at-threshold accountChargeUsd", at.AccountChargeUsd, 0.0002)
}

func TestEstimateProviderCostUsd(t *testing.T) {
	// estimateProviderCostUsd resolves through the alias fallback: gpt-5.6
	// bills at gpt-5.6-sol prices, and 1M/0.5M tokens sit past the 272000
	// long-context threshold (input x2 = 10, output x1.5 = 45).
	got := EstimateProviderCostUsd(CostInput{
		ProviderCode: "openai",
		Model:        "gpt-5.6",
		InputTokens:  f64p(1_000_000),
		OutputTokens: f64p(500_000),
	})
	if got == nil || *got != 32.5 {
		t.Fatalf("gpt-5.6 estimate = %v, want 32.5", got)
	}
	if got := EstimateProviderCostUsd(CostInput{
		ProviderCode: "openai",
		Model:        "no-such-model",
		InputTokens:  f64p(100),
	}); got != nil {
		t.Fatalf("unknown model estimate = %v, want nil", got)
	}
	if got := EstimateProviderCostUsd(CostInput{ProviderCode: "openai", Model: "gpt-5.5"}); got != nil {
		t.Fatalf("no cost dimension estimate = %v, want nil", got)
	}
	if got := EstimateProviderCostUsd(CostInput{ProviderCode: "openai", Model: "", InputTokens: f64p(1)}); got != nil {
		t.Fatalf("blank model estimate = %v, want nil", got)
	}
}

func TestEstimateProviderCacheCosts(t *testing.T) {
	cacheRead := EstimateProviderCacheReadCostUsd(CostInput{
		ProviderCode:    "openai",
		Model:           "gpt-5.5",
		CacheReadTokens: f64p(100_000),
	})
	if cacheRead == nil || *cacheRead != 0.05 {
		t.Fatalf("cache read estimate = %v, want 0.05", cacheRead)
	}
	// Zero write tokens with a defined write rate estimate to 0.
	zeroWrite := EstimateProviderCacheWriteCostUsd(CostInput{
		ProviderCode:     "openai",
		Model:            "gpt-5.6-sol",
		CacheWriteTokens: f64p(0),
	})
	if zeroWrite == nil || *zeroWrite != 0 {
		t.Fatalf("zero cache write estimate = %v, want 0", zeroWrite)
	}
	// No write rate on gpt-5.5: the estimate stays undefined.
	if got := EstimateProviderCacheWriteCostUsd(CostInput{
		ProviderCode:     "openai",
		Model:            "gpt-5.5",
		CacheWriteTokens: f64p(100_000),
	}); got != nil {
		t.Fatalf("unpriced cache write estimate = %v, want nil", got)
	}
	// Missing cache dimensions bail before any lookup.
	if got := EstimateProviderCacheWriteCostUsd(CostInput{ProviderCode: "openai", Model: "gpt-5.6-sol"}); got != nil {
		t.Fatalf("no cache write dimension = %v, want nil", got)
	}
	if got := EstimateProviderCacheReadCostUsd(CostInput{ProviderCode: "openai", Model: "gpt-5.5"}); got != nil {
		t.Fatalf("no cache read dimension = %v, want nil", got)
	}
}

func TestBuildCostBreakdownUnknownProvider(t *testing.T) {
	if got := BuildCostBreakdown(&Pricing{ProviderCode: "no-such-provider", Model: "x"}, CostInput{
		ProviderCode: "no-such-provider",
		InputTokens:  f64p(1),
	}); got != nil {
		t.Fatal("unknown provider must have no billing policy")
	}
	if got := BuildCostBreakdown(nil, CostInput{Model: "x"}); got != nil {
		t.Fatal("nil pricing must yield nil breakdown")
	}
}
