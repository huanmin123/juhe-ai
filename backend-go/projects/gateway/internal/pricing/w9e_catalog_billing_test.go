package pricing

// w9e 覆盖率战役：补 catalog.go / billing.go / lookup.go / pricing.go 的
// 候选构建、协议推断、tier 元数据与估算入口的未覆盖分支。
// 纯函数级测试，不依赖外部资源。

import (
	"math"
	"testing"
)

func fptr(v float64) *float64 { return &v }

func iptr(v int) *int { return &v }

func TestW9EBuildProviderModelCandidates(t *testing.T) {
	// glm: date strip + base match
	glm := buildGlmModelCandidates("glm-4.7-20260101")
	if len(glm) == 0 || glm[0] != "glm-4.7" {
		t.Fatalf("glm candidates = %v, want first glm-4.7", glm)
	}
	glmBase := buildGlmModelCandidates("glm-4.7-flash")
	found := false
	for _, c := range glmBase {
		if c == "glm-4.7-flash" {
			found = true
		}
	}
	if !found {
		t.Fatalf("glm-4.7-flash candidates %v missing base", glmBase)
	}
	if got := buildGlmModelCandidates("unknown-model"); len(got) != 0 {
		t.Fatalf("unknown glm candidates = %v, want empty", got)
	}

	// anthropic: date strip + base match
	ant := buildAnthropicModelCandidates("claude-opus-4-6-20260101")
	if len(ant) == 0 || ant[0] != "claude-opus-4-6" {
		t.Fatalf("anthropic candidates = %v, want first claude-opus-4-6", ant)
	}
	antBase := buildAnthropicModelCandidates("claude-haiku-4-5")
	found = false
	for _, c := range antBase {
		if c == "claude-haiku-4-5" {
			found = true
		}
	}
	if !found {
		t.Fatalf("claude-haiku-4-5 candidates %v missing base", antBase)
	}

	// deepseek: date strip only, nil without a date suffix
	if got := buildDeepSeekModelCandidates("deepseek-chat-20260101"); len(got) == 0 || got[0] != "deepseek-chat" {
		t.Fatalf("deepseek candidates = %v, want [deepseek-chat]", got)
	}
	if got := buildDeepSeekModelCandidates("deepseek-chat"); got != nil {
		t.Fatalf("deepseek without date = %v, want nil", got)
	}

	// gemini: models/ prefix strip + base match (model or prefix-stripped)
	gem := buildGeminiModelCandidates("models/gemini-2.5-pro")
	found = false
	for _, c := range gem {
		if c == "gemini-2.5-pro" {
			found = true
		}
	}
	if !found {
		t.Fatalf("models/gemini-2.5-pro candidates %v missing gemini-2.5-pro", gem)
	}
	gemBare := buildGeminiModelCandidates("gemini-2.5-pro")
	found = false
	for _, c := range gemBare {
		if c == "gemini-2.5-pro" {
			found = true
		}
	}
	if !found {
		t.Fatalf("gemini-2.5-pro candidates %v missing base", gemBare)
	}

	// xai: only the stripped date form; nil when there is no date suffix
	if got := buildXAIModelCandidates("grok-4-2026-01-01"); len(got) != 1 || got[0] != "grok-4" {
		t.Fatalf("xai candidates = %v, want [grok-4]", got)
	}
	if got := buildXAIModelCandidates("grok-4"); got != nil {
		t.Fatalf("xai without date = %v, want nil", got)
	}

	// dispatch via providerEntry
	openaiEntry := providerEntryFor("openai")
	if openaiEntry == nil {
		t.Fatal("openai entry missing")
	}
	if got := openaiEntry.buildModelCandidates("gpt-4.1-2025-04-14"); len(got) == 0 {
		t.Fatal("openai buildModelCandidates should produce a date-stripped candidate")
	}
	xaiEntry := providerEntryFor("xai")
	if got := xaiEntry.buildModelCandidates("grok-4"); got != nil {
		t.Fatalf("xai dispatch without date = %v, want nil", got)
	}
}

func TestW9EInferOpenAIModelApiProtocols(t *testing.T) {
	cases := []struct {
		name string
		item *rawModel
		want []string
	}{
		{"explicit", &rawModel{SupportedAPIProtocols: []string{"responses"}}, []string{"responses"}},
		{"image_generation mode", &rawModel{Mode: "image_generation"}, []string{"images"}},
		{"gpt-image literal", &rawModel{Model: "gpt-image-1"}, []string{"images"}},
		{"dall-e", &rawModel{Model: "dall-e-3"}, []string{"images"}},
		{"completion mode", &rawModel{Mode: "completion"}, []string{"completions"}},
		{"codex", &rawModel{Model: "gpt-5.3-codex"}, []string{"responses"}},
		{"pro suffix", &rawModel{Model: "gpt-5.5-pro"}, []string{"responses"}},
		{"responses mode", &rawModel{Mode: "responses"}, []string{"responses"}},
		{"chat mode", &rawModel{Mode: "chat"}, []string{"chat_completions", "responses"}},
		{"gpt prefix", &rawModel{Model: "gpt-5.5"}, []string{"chat_completions", "responses"}},
		{"o prefix", &rawModel{Model: "o4-mini"}, []string{"chat_completions", "responses"}},
		{"fallback", &rawModel{Model: "whisper-1"}, []string{}},
		{"blank fallback", &rawModel{Model: "  ", Mode: " "}, []string{}},
	}
	for _, tc := range cases {
		got := inferOpenAIModelApiProtocols(tc.item)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: protocols = %v, want %v", tc.name, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s: protocols = %v, want %v", tc.name, got, tc.want)
			}
		}
	}
}

func TestW9EInferAPIProtocolsPerProvider(t *testing.T) {
	if got := providerEntryFor("openai").inferAPIProtocols(&rawModel{Mode: "chat"}); len(got) != 2 {
		t.Fatalf("openai chat protocols = %v", got)
	}
	for _, code := range []string{"anthropic", "deepseek", "glm", "gemini", "xai"} {
		entry := providerEntryFor(code)
		if entry == nil {
			t.Fatalf("%s entry missing", code)
		}
		withExplicit := entry.inferAPIProtocols(&rawModel{SupportedAPIProtocols: []string{"p1"}})
		if len(withExplicit) != 1 || withExplicit[0] != "p1" {
			t.Fatalf("%s explicit protocols = %v", code, withExplicit)
		}
		def := entry.inferAPIProtocols(&rawModel{})
		if code == "anthropic" || code == "xai" {
			if len(def) != 0 {
				t.Fatalf("%s default protocols should be empty, got %v", code, def)
			}
			continue
		}
		if len(def) == 0 {
			t.Fatalf("%s default protocols empty", code)
		}
	}
	if got := providerEntryFor("anthropic").inferAPIProtocols(&rawModel{}); len(got) != 0 {
		t.Fatalf("anthropic default = %v, want empty", got)
	}
	if got := providerEntryFor("xai").inferAPIProtocols(&rawModel{}); len(got) != 0 {
		t.Fatalf("xai default = %v, want empty", got)
	}
	if got := providerEntryFor("deepseek").inferAPIProtocols(&rawModel{}); len(got) != 1 || got[0] != "chat_completions" {
		t.Fatalf("deepseek default = %v", got)
	}
	if got := providerEntryFor("glm").inferAPIProtocols(&rawModel{}); len(got) != 1 || got[0] != "chat_completions" {
		t.Fatalf("glm default = %v", got)
	}
	if got := providerEntryFor("gemini").inferAPIProtocols(&rawModel{}); len(got) != 3 || got[0] != "generate_content" {
		t.Fatalf("gemini default = %v", got)
	}
}

func TestW9EReleaseDateHelpers(t *testing.T) {
	if got := suffixOrRawReleaseDate(&rawModel{ReleaseDate: "2026-01-01"}); got != "2026-01-01" {
		t.Fatalf("explicit release date = %q", got)
	}
	if got := suffixOrRawReleaseDate(&rawModel{Model: "grok-4-2026-01-01"}); got != "2026-01-01" {
		t.Fatalf("suffix release date = %q", got)
	}
	if got := suffixOrRawReleaseDate(&rawModel{Model: "grok-4"}); got != "" {
		t.Fatalf("no release date = %q, want empty", got)
	}
	if got := openAIModelReleaseDate(&rawModel{Model: "gpt-4.1-2025-04-14"}); got != "2025-04-14" {
		t.Fatalf("openai suffix release date = %q", got)
	}
	if got := openAIModelReleaseDate(&rawModel{Model: "gpt-3.5-turbo"}); got == "" {
		t.Fatal("known-map release date missing for gpt-3.5-turbo")
	}
}

func TestW9ENormalizePriceAndFixedNumber(t *testing.T) {
	if got := normalizePrice(nil); got != nil {
		t.Fatal("normalizePrice(nil) should stay nil")
	}
	nan := math.NaN()
	if got := normalizePrice(&nan); got != nil {
		t.Fatal("NaN should normalize to nil")
	}
	inf := math.Inf(1)
	if got := normalizePrice(&inf); got != nil {
		t.Fatal("+Inf should normalize to nil")
	}
	if got := normalizePrice(fptr(1.5)); got == nil || *got != 1.5 {
		t.Fatalf("normalizePrice(1.5) = %v", got)
	}
	if got := fixedNumber(1.005, 2); got != 1.0 {
		t.Fatalf("fixedNumber rounding = %v, want 1", got)
	}
	if got := fixedNumber(math.NaN(), 8); !math.IsNaN(got) {
		t.Fatalf("fixedNumber NaN passthrough = %v", got)
	}
}

func TestW9ESumOptionalCosts(t *testing.T) {
	if got := sumOptionalCosts(); got != nil {
		t.Fatalf("sumOptionalCosts() = %v, want nil", got)
	}
	if got := sumOptionalCosts(nil, nil); got != nil {
		t.Fatalf("all-nil = %v, want nil", got)
	}
	got := sumOptionalCosts(fptr(1.0000000001), fptr(2))
	// roundCost keeps 10 decimals, so 1.0000000001 + 2 stays 3.0000000001.
	if got == nil || math.Abs(*got-3.0000000001) > 1e-12 {
		t.Fatalf("sum = %v, want 3.0000000001", got)
	}
	if got := sumOptionalCosts(fptr(4e-12), fptr(6e-12)); got == nil || *got != 0 {
		t.Fatalf("rounded-to-zero sum = %v, want 0", got)
	}
}

func TestW9EProviderEntryForAndBillingPolicy(t *testing.T) {
	if got := providerEntryFor(""); got != nil {
		t.Fatal("empty provider should have no entry")
	}
	if got := providerEntryFor("  "); got != nil {
		t.Fatal("blank provider should have no entry")
	}
	if got := providerEntryFor("not-a-provider"); got != nil {
		t.Fatal("unknown provider should have no entry")
	}
	if got := providerEntryFor("  OPENAI  "); got == nil {
		t.Fatal("normalized openai should resolve")
	}
	if got := billingPolicyForProvider("nope"); got != nil {
		t.Fatal("unknown billing policy should be nil")
	}
	for _, code := range []string{"openai", "anthropic", "deepseek", "glm", "gemini", "xai"} {
		if got := billingPolicyForProvider(code); got == nil {
			t.Fatalf("billing policy for %s missing", code)
		}
	}
}

func TestW9EListProviderModelPricingAsOf(t *testing.T) {
	if got := ListProviderModelPricingAsOf("", "2026-01-01"); got != nil {
		t.Fatal("empty provider list should be nil")
	}
	if got := ListProviderModelPricingAsOf("unknown", "2026-01-01"); got != nil {
		t.Fatal("unknown provider list should be nil")
	}
	all := ListProviderModelPricingAsOf("openai", "2026-09-16")
	if len(all) == 0 {
		t.Fatal("openai list should not be empty")
	}
	// compareProviderModels 排序可稳定复放：相邻行按 catalog order 升序。
	for i := 1; i < len(all); i++ {
		if compareProviderModels(all[i-1], all[i]) > 0 {
			t.Fatalf("openai list not sorted at index %d", i)
		}
	}
	// far-future cutoff excludes shutdown rows
	future := ListProviderModelPricingAsOf("openai", "9999-12-31")
	if len(future) >= len(all) {
		t.Fatalf("far-future cutoff should exclude shutdown rows: %d vs %d", len(future), len(all))
	}
}

func TestW9EFindProviderModelPricingAsOfGuards(t *testing.T) {
	if got := FindProviderModelPricingAsOf("", "gpt-5.5", "2026-09-16"); got != nil {
		t.Fatal("empty provider should not resolve")
	}
	if got := FindProviderModelPricingAsOf("openai", "", "2026-09-16"); got != nil {
		t.Fatal("empty model should not resolve")
	}
	if got := FindProviderModelPricingAsOf("unknown", "m", "2026-09-16"); got != nil {
		t.Fatal("unknown provider should not resolve")
	}
	if got := FindProviderModelPricingAsOf("openai", "   ", "2026-09-16"); got != nil {
		t.Fatal("blank model should not resolve")
	}
	// unavailable OpenAI model never resolves
	if got := FindProviderModelPricingAsOf("openai", "gpt-4-32k", "2026-09-16"); got != nil {
		t.Fatalf("unavailable openai model should not resolve, got %v", got.Model)
	}
	// whitespace in model is normalized before lookup
	if got := FindProviderModelPricingAsOf("openai", " gpt-5.5 ", "2026-09-16"); got == nil {
		t.Fatal("whitespace-padded model should resolve")
	}
}

func TestW9EGeminiTierField(t *testing.T) {
	if got := geminiTierField(nil, (*geminiTierPrices).inputPtr); got != nil {
		t.Fatal("nil tier should return nil")
	}
	tier := &geminiTierPrices{inputUsdPer1M: 1.25}
	if got := geminiTierField(tier, (*geminiTierPrices).inputPtr); got == nil || *got != 1.25 {
		t.Fatalf("tier input = %v, want 1.25", got)
	}
}

func TestW9ECompareProviderModelsOrdering(t *testing.T) {
	left := &Pricing{Model: "a", CatalogOrder: iptr(1), ReleaseDate: "2026-01-01"}
	right := &Pricing{Model: "b", CatalogOrder: iptr(2), ReleaseDate: "2025-01-01"}
	if compareProviderModels(left, right) != -1 {
		t.Fatal("lower catalog order should sort first")
	}
	if compareProviderModels(right, left) != 1 {
		t.Fatal("comparison should be antisymmetric")
	}
	// same catalog order -> release date descending
	sameA := &Pricing{Model: "a", CatalogOrder: iptr(3), ReleaseDate: "2026-01-01"}
	sameB := &Pricing{Model: "b", CatalogOrder: iptr(3), ReleaseDate: "2025-06-01"}
	if compareProviderModels(sameA, sameB) != -1 {
		t.Fatal("newer release date should sort first")
	}
	// release date present beats missing
	noDate := &Pricing{Model: "b", CatalogOrder: iptr(3)}
	if compareProviderModels(sameA, noDate) != -1 {
		t.Fatal("dated row should sort before undated row")
	}
	// both missing release date -> model name tiebreak
	undatedA := &Pricing{Model: "a", CatalogOrder: iptr(3)}
	undatedB := &Pricing{Model: "b", CatalogOrder: iptr(3)}
	if compareProviderModels(undatedA, undatedB) != -1 {
		t.Fatal("model name tiebreak should be ascending")
	}
	// fully equal -> 0
	if compareProviderModels(undatedA, &Pricing{Model: "a", CatalogOrder: iptr(3)}) != 0 {
		t.Fatal("equal rows should compare 0")
	}
}

func TestW9EEstimateProviderCacheCosts(t *testing.T) {
	// empty model guard
	if got := EstimateProviderCacheWriteCostUsd(CostInput{Model: ""}); got != nil {
		t.Fatal("empty model cache write estimate should be nil")
	}
	if got := EstimateProviderCacheReadCostUsd(CostInput{Model: "gpt-5.5"}); got != nil {
		t.Fatal("nil cache read tokens should yield nil")
	}
	if got := EstimateProviderCacheWriteCostUsd(CostInput{Model: "gpt-5.5"}); got != nil {
		t.Fatal("nil cache write tokens should yield nil")
	}
	if got := EstimateProviderCacheReadCostUsd(CostInput{Model: "", CacheReadTokens: fptr(1)}); got != nil {
		t.Fatal("empty model cache read estimate should be nil")
	}
	// unknown model: no pricing -> nil
	if got := EstimateProviderCacheWriteCostUsd(CostInput{Model: "no-such-model", CacheWriteTokens: fptr(10)}); got != nil {
		t.Fatal("unknown model cache write estimate should be nil")
	}
	if got := EstimateProviderCacheReadCostUsd(CostInput{Model: "no-such-model", CacheReadTokens: fptr(10)}); got != nil {
		t.Fatal("unknown model cache read estimate should be nil")
	}
	// real model: gpt-5.6-sol has cache write/read prices (golden row)
	write := EstimateProviderCacheWriteCostUsd(CostInput{ProviderCode: "openai", Model: "gpt-5.6-sol", CacheWriteTokens: fptr(1_000_000)})
	if write == nil || *write != 6.25 {
		t.Fatalf("gpt-5.6-sol cache write cost = %v, want 6.25", write)
	}
	read := EstimateProviderCacheReadCostUsd(CostInput{ProviderCode: "openai", Model: "gpt-5.5", CacheReadTokens: fptr(1_000_000)})
	if read == nil || *read != 0.5 {
		t.Fatalf("gpt-5.5 cache read cost = %v, want 0.5", read)
	}
	// 1h write only
	write1h := EstimateProviderCacheWriteCostUsd(CostInput{ProviderCode: "openai", Model: "gpt-5.6-sol", CacheWrite1hTokens: fptr(0)})
	if write1h == nil || *write1h != 0 {
		t.Fatalf("zero-token 1h write = %v, want 0", write1h)
	}
	// model without cache prices -> nil even with tokens
	noCache := EstimateProviderCacheReadCostUsd(CostInput{ProviderCode: "openai", Model: "whisper-1", CacheReadTokens: fptr(10)})
	if noCache != nil {
		t.Fatalf("whisper-1 cache read = %v, want nil", noCache)
	}
	noCacheWrite := EstimateProviderCacheWriteCostUsd(CostInput{ProviderCode: "openai", Model: "gpt-5.5", CacheWriteTokens: fptr(10)})
	if noCacheWrite != nil {
		t.Fatalf("gpt-5.5 cache write = %v, want nil (no cache write price)", noCacheWrite)
	}
}

func TestW9EHasUnsupportedServiceTier(t *testing.T) {
	pricing := FindProviderModelPricing("openai", "gpt-5.5")
	if pricing == nil {
		t.Fatal("gpt-5.5 pricing missing")
	}
	if hasUnsupportedServiceTier(pricing, CostInput{ServiceTier: ""}) {
		t.Fatal("empty tier should never be unsupported")
	}
	if hasUnsupportedServiceTier(pricing, CostInput{ServiceTier: "priority"}) {
		t.Fatal("supported tier should not be flagged")
	}
	if !hasUnsupportedServiceTier(pricing, CostInput{ServiceTier: "batch"}) {
		t.Fatal("unsupported tier should be flagged")
	}
	if hasUnsupportedServiceTier(pricing, CostInput{ServiceTier: " priority "}) {
		t.Fatal("normalized tier should match")
	}
}

func TestW9ETierPricingMetadata(t *testing.T) {
	full := PriceSet{InputUsdPer1M: fptr(1), OutputUsdPer1M: fptr(2)}
	tierFull := PriceSet{InputUsdPer1M: fptr(0.5), OutputUsdPer1M: fptr(1)}
	src, _ := tierPricingMetadata(full, tierFull)
	if src != TierSourceTierSpecific {
		t.Fatalf("full tier source = %v, want tier-specific", src)
	}
	partialTier := PriceSet{InputUsdPer1M: fptr(0.5)}
	src, _ = tierPricingMetadata(full, partialTier)
	if src != TierSourceMixed {
		t.Fatalf("partial tier source = %v, want mixed", src)
	}
	src, _ = tierPricingMetadata(full, PriceSet{})
	if src != TierSourceUnknown {
		t.Fatalf("empty tier source = %v, want unknown", src)
	}
}

func TestW9EBillingPolicyForUnknownProvider(t *testing.T) {
	if got := billingPolicyForProvider(""); got != nil {
		t.Fatal("empty provider should have no billing policy")
	}
}

func TestW9EServiceTierRatesBranches(t *testing.T) {
	pricing := FindProviderModelPricing("openai", "gpt-5.5")
	if pricing == nil {
		t.Fatal("gpt-5.5 pricing missing")
	}
	// empty tier -> standard rates
	standard := serviceTierRates(pricing, CostInput{})
	if standard.serviceTierPricingSource != TierSourceDefault {
		t.Fatalf("empty tier source = %v, want default", standard.serviceTierPricingSource)
	}
	// unsupported tier -> unknown
	unknown := serviceTierRates(pricing, CostInput{ServiceTier: "nope"})
	if unknown.serviceTierPricingSource != TierSourceUnknown {
		t.Fatalf("unsupported tier source = %v, want unknown", unknown.serviceTierPricingSource)
	}
	// supported tier -> tier rates
	tiered := serviceTierRates(pricing, CostInput{ServiceTier: "priority"})
	if tiered.serviceTierPricingSource == TierSourceUnknown {
		t.Fatal("supported priority tier should resolve")
	}
	if tiered.InputUsdPer1M == nil {
		t.Fatal("priority tier should own input rate")
	}
}
