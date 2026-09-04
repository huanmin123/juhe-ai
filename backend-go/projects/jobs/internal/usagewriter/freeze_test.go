package usagewriter

import (
	"context"
	"math"
	"testing"
)

// mockCatalog is the CatalogPricing mock: programmable model resolution and
// breakdown, counting calls so tests can assert the freeze-once semantics.
type mockCatalog struct {
	resolveModel   string
	resolveCalls   int
	breakdown      *CostBreakdown
	breakdownCalls int
	failModel      bool
}

func (m *mockCatalog) ResolvePricingModel(ctx Ctx, providerCode string, systemAccountID string, upstreamModel string, requestedModel string) string {
	m.resolveCalls++
	return m.resolveModel
}

func (m *mockCatalog) BuildBreakdown(ctx Ctx, providerCode string, systemAccountID string, model string, serviceTier string, input UsageRecordInput) *CostBreakdown {
	m.breakdownCalls++
	return m.breakdown
}

func floatPtr(v float64) *float64 { return &v }
func intPtr(v int) *int           { return &v }

func TestHasPricingSnapshotFact(t *testing.T) {
	tests := []struct {
		name  string
		input UsageRecordInput
		want  bool
	}{
		{"empty", UsageRecordInput{}, false},
		{"input tokens", UsageRecordInput{InputTokens: intPtr(1)}, true},
		{"cost only", UsageRecordInput{CostUsd: floatPtr(0.1)}, true},
		{"nan cost ignored", func() UsageRecordInput {
			nan := math.NaN()
			return UsageRecordInput{CostUsd: &nan}
		}(), false},
		{"image tokens", UsageRecordInput{OutputImageCount: intPtr(2)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasPricingSnapshotFact(tt.input); got != tt.want {
				t.Fatalf("HasPricingSnapshotFact = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFreezeKeepsFrozenSnapshotUntouched(t *testing.T) {
	// freeze 生效：已有快照原样保留，不再查询目录（"冻结后不重算"）。
	catalog := &mockCatalog{resolveModel: "gpt-x", breakdown: &CostBreakdown{Multiplier: 1, ServiceTierPricingSource: TierSourceDefault}}
	frozen := &CostBreakdown{AccountChargeUsd: floatPtr(0.5), Multiplier: 1, ServiceTierPricingSource: TierSourceUnknown}
	input := UsageRecordInput{
		TraceID: "t", TrafficSource: TrafficSourceGateway, ProviderCode: "gpt",
		Model: "gpt-old", InputTokens: intPtr(100), CostUsd: floatPtr(0.9),
		PricingSnapshot: frozen,
	}
	result := FreezeUsageRecordPricingFacts(context.Background(), input, catalog, true)
	snapshot, ok := result.PricingSnapshot.(*CostBreakdown)
	if !ok || snapshot != frozen {
		t.Fatalf("frozen snapshot replaced: %+v", result.PricingSnapshot)
	}
	if result.CostUsd == nil || *result.CostUsd != 0.9 {
		t.Fatalf("cost rewritten: %+v", result.CostUsd)
	}
	if catalog.resolveCalls != 0 || catalog.breakdownCalls != 0 {
		t.Fatalf("catalog consulted on frozen record: resolve=%d breakdown=%d", catalog.resolveCalls, catalog.breakdownCalls)
	}
}

func TestFreezeUnfrozenRecordUsesCatalogOnce(t *testing.T) {
	// 解冻面：无快照记录在 freeze 时点补算一次；冻结后再次 freeze（重复入队
	// 场景）不再触碰目录，catalog 变化不影响已冻结结果。
	catalog := &mockCatalog{
		resolveModel: "gpt-5",
		breakdown: &CostBreakdown{
			CacheReadCostUsd:         floatPtr(0.01),
			CacheWriteCostUsd:        floatPtr(0.02),
			CacheWrite1hCostUsd:      floatPtr(0.04),
			AccountChargeUsd:         floatPtr(0.10),
			Multiplier:               1,
			ServiceTierPricingSource: TierSourceDefault,
		},
	}
	input := UsageRecordInput{
		TraceID: "t", TrafficSource: TrafficSourceGateway, ProviderCode: "gpt",
		SystemAccountID: "sys1", Model: "gpt-5-alias", UpstreamModel: "gpt-5",
		InputTokens: intPtr(10), CacheReadTokens: intPtr(5), CacheWriteTokens: intPtr(2), CacheWrite1hTokens: intPtr(1),
	}
	frozenOnce := FreezeUsageRecordPricingFacts(context.Background(), input, catalog, true)
	if frozenOnce.PricingSnapshot == nil {
		t.Fatal("expected snapshot frozen at enqueue time")
	}
	if frozenOnce.PricingModel != "gpt-5" {
		t.Fatalf("pricingModel = %q", frozenOnce.PricingModel)
	}
	snapshot := frozenOnce.PricingSnapshot.(*CostBreakdown)
	if snapshot.AccountChargeUsd == nil || *snapshot.AccountChargeUsd != 0.10 {
		t.Fatalf("account charge = %+v", snapshot.AccountChargeUsd)
	}
	if frozenOnce.CostUsd == nil || *frozenOnce.CostUsd != 0.10 {
		t.Fatalf("costUsd backfilled = %+v", frozenOnce.CostUsd)
	}
	if frozenOnce.CacheReadCostUsd == nil || *frozenOnce.CacheReadCostUsd != 0.01 {
		t.Fatalf("cacheReadCostUsd backfilled = %+v", frozenOnce.CacheReadCostUsd)
	}
	if frozenOnce.CacheWriteCostUsd == nil {
		t.Fatal("cacheWriteCostUsd missing")
	}
	// cache write cost = sum(cacheWrite, cacheWrite1h) rounded to 10 decimals.
	if *frozenOnce.CacheWriteCostUsd != 0.0600000000 {
		t.Fatalf("cacheWriteCostUsd = %v, want 0.06 sum", *frozenOnce.CacheWriteCostUsd)
	}
	firstCalls := catalog.breakdownCalls
	if firstCalls == 0 {
		t.Fatal("catalog breakdown never consulted")
	}

	// Re-freezing the frozen record must not consult the catalog again
	// (frozen facts are never re-interpreted, even as the catalog "changes").
	catalog.resolveModel = "price-changed-model"
	refrozen := FreezeUsageRecordPricingFacts(context.Background(), frozenOnce, catalog, true)
	if catalog.breakdownCalls != firstCalls || catalog.resolveCalls != 1 {
		t.Fatalf("frozen record re-consulted catalog: resolve=%d breakdown=%d", catalog.resolveCalls, catalog.breakdownCalls)
	}
	if refrozen.PricingSnapshot != frozenOnce.PricingSnapshot {
		t.Fatal("frozen snapshot replaced on re-freeze")
	}
}

func TestFreezePassthroughWithoutFacts(t *testing.T) {
	catalog := &mockCatalog{resolveModel: "m"}
	input := UsageRecordInput{TraceID: "t", TrafficSource: TrafficSourceGateway}
	result := FreezeUsageRecordPricingFacts(context.Background(), input, catalog, true)
	if result.PricingSnapshot != nil {
		t.Fatalf("unexpected snapshot: %+v", result.PricingSnapshot)
	}
	if catalog.resolveCalls != 0 {
		t.Fatalf("catalog consulted without facts: %d", catalog.resolveCalls)
	}
}

func TestFallbackPricingSnapshot(t *testing.T) {
	input := UsageRecordInput{
		TraceID: "t", TrafficSource: TrafficSourceGateway,
		CacheReadCostUsd: floatPtr(0.001), CacheWriteCostUsd: floatPtr(0.002),
		ThinkingTokens: intPtr(7), CostUsd: floatPtr(0.5),
	}
	snapshot := FallbackPricingSnapshot(input)
	if snapshot.Multiplier != 1 || snapshot.ServiceTierPricingSource != TierSourceUnknown {
		t.Fatalf("fallback = %+v", snapshot)
	}
	if snapshot.AccountChargeUsd == nil || *snapshot.AccountChargeUsd != 0.5 {
		t.Fatalf("accountChargeUsd = %+v", snapshot.AccountChargeUsd)
	}
	if snapshot.ThinkingTokens == nil || *snapshot.ThinkingTokens != 7 {
		t.Fatalf("thinkingTokens = %+v", snapshot.ThinkingTokens)
	}
	// No provider catalog on the fallback path.
	if snapshot.CacheReadCostUsd == nil || *snapshot.CacheReadCostUsd != 0.001 {
		t.Fatalf("cacheReadCostUsd = %+v", snapshot.CacheReadCostUsd)
	}
}

func TestEnrichKeepsRecordOnCatalogMiss(t *testing.T) {
	// 目录无该模型：enrich 不产生任何成本（Node enrich 返回原样），随后
	// freeze 阶段落 deterministic fallback 快照（multiplier=1、unknown 来源）。
	catalog := &mockCatalog{resolveModel: "", breakdown: nil}
	input := UsageRecordInput{
		TraceID: "t", TrafficSource: TrafficSourceGateway, ProviderCode: "gpt",
		Model: "unknown-model", InputTokens: intPtr(3),
	}
	result := FreezeUsageRecordPricingFacts(context.Background(), input, catalog, true)
	if result.CostUsd != nil {
		t.Fatal("cost fabricated on catalog miss")
	}
	snapshot, ok := result.PricingSnapshot.(*CostBreakdown)
	if !ok {
		t.Fatalf("expected fallback snapshot on freeze, got %+v", result.PricingSnapshot)
	}
	if snapshot.Multiplier != 1 || snapshot.ServiceTierPricingSource != TierSourceUnknown {
		t.Fatalf("fallback snapshot = %+v", snapshot)
	}
	if snapshot.AccountChargeUsd != nil {
		t.Fatalf("fallback charge fabricated: %+v", snapshot.AccountChargeUsd)
	}

	// Enrich 单独调用（无 freeze 兜底）时保持原样。
	enriched := EnrichUsageRecordPricing(context.Background(), input, catalog)
	if enriched.PricingSnapshot != nil || enriched.CostUsd != nil {
		t.Fatalf("enrich fabricated facts: %+v", enriched)
	}
}

func TestPricingSnapshotForWriteCatalogGuard(t *testing.T) {
	// Postgres 写路径不做 catalog 快照（databaseDriver 守卫），直接落
	// deterministic fallback。
	catalog := &mockCatalog{resolveModel: "gpt-5", breakdown: &CostBreakdown{Multiplier: 1, ServiceTierPricingSource: TierSourceDefault}}
	input := UsageRecordInput{
		TraceID: "t", TrafficSource: TrafficSourceGateway, ProviderCode: "gpt",
		Model: "gpt-5", InputTokens: intPtr(4), CostUsd: floatPtr(0.2),
	}
	snapshot := PricingSnapshotForWrite(context.Background(), input, false, catalog)
	if snapshot == nil || snapshot.ServiceTierPricingSource != TierSourceUnknown {
		t.Fatalf("postgres-path snapshot = %+v", snapshot)
	}
	if catalog.breakdownCalls != 0 {
		t.Fatalf("catalog consulted on postgres path: %d", catalog.breakdownCalls)
	}
	// SQLite 路径允许 catalog 快照。
	snapshot = PricingSnapshotForWrite(context.Background(), input, true, catalog)
	if snapshot == nil || snapshot.ServiceTierPricingSource != TierSourceDefault {
		t.Fatalf("sqlite-path snapshot = %+v", snapshot)
	}
	if catalog.breakdownCalls != 1 {
		t.Fatalf("catalog breakdown calls = %d", catalog.breakdownCalls)
	}
}

func TestUsageSemanticsContract(t *testing.T) {
	// usage-semantics/types.ts 契约落地：字段 + 两个操作。
	usage := ParsedUsageTokens{InputTokens: intPtr(11), OutputTokens: intPtr(3), CacheReadTokens: intPtr(4), ThinkingTokens: intPtr(1)}
	var semantic UsageSemantic = OpenAIUsageSemantic{}
	if semantic.ID() != "openai" {
		t.Fatalf("semantic id = %q", semantic.ID())
	}
	normalized := semantic.NormalizeForStorage(usage)
	if normalized != usage {
		t.Fatalf("openai normalize changed usage: %+v → %+v", usage, normalized)
	}
	denominator := semantic.CacheReadRateDenominator(ParsedUsageCacheReadRateInput{InputTokens: usage.InputTokens, CacheReadTokens: usage.CacheReadTokens})
	if denominator != 11 {
		t.Fatalf("cacheReadRateDenominator = %d, want 11", denominator)
	}
	// nil 字段 → 0（Node undefined → 0 分母）。
	if got := semantic.CacheReadRateDenominator(ParsedUsageCacheReadRateInput{}); got != 0 {
		t.Fatalf("empty denominator = %d", got)
	}

	// Resolver：未知 id 回落默认（usageSemanticForProfile 契约）。
	resolved := SemanticForID(DefaultUsageSemanticResolver{}, "anthropic")
	if resolved.ID() != "openai" {
		t.Fatalf("fallback semantic = %q", resolved.ID())
	}
}
