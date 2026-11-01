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
	// lastServiceTier 记录最近一次 BuildBreakdown 入参档位（C3-H3：冻结
	// 计价档位必须与网关 cost_usd 的回退链一致）。
	lastServiceTier string
}

func (m *mockCatalog) ResolvePricingModel(ctx Ctx, providerCode string, systemAccountID string, upstreamModel string, requestedModel string) string {
	m.resolveCalls++
	return m.resolveModel
}

func (m *mockCatalog) BuildBreakdown(ctx Ctx, providerCode string, systemAccountID string, model string, serviceTier string, input UsageRecordInput) *CostBreakdown {
	m.breakdownCalls++
	m.lastServiceTier = serviceTier
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
	result := FreezeUsageRecordPricingFacts(context.Background(), input, catalog)
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

func TestFreezePassthroughWithoutFacts(t *testing.T) {
	catalog := &mockCatalog{resolveModel: "m"}
	input := UsageRecordInput{TraceID: "t", TrafficSource: TrafficSourceGateway}
	result := FreezeUsageRecordPricingFacts(context.Background(), input, catalog)
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
	result := FreezeUsageRecordPricingFacts(context.Background(), input, catalog)
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
	// 写路径不做 catalog 快照（PG 形态恒假守卫），直接落 deterministic fallback。
	catalog := &mockCatalog{resolveModel: "gpt-5", breakdown: &CostBreakdown{Multiplier: 1, ServiceTierPricingSource: TierSourceDefault}}
	input := UsageRecordInput{
		TraceID: "t", TrafficSource: TrafficSourceGateway, ProviderCode: "gpt",
		Model: "gpt-5", InputTokens: intPtr(4), CostUsd: floatPtr(0.2),
	}
	snapshot := PricingSnapshotForWrite(context.Background(), input, catalog)
	if snapshot == nil || snapshot.ServiceTierPricingSource != TierSourceUnknown {
		t.Fatalf("write-path snapshot = %+v", snapshot)
	}
	if catalog.breakdownCalls != 0 {
		t.Fatalf("catalog consulted on write path: %d", catalog.breakdownCalls)
	}
}

// TestC3H3FreezeServiceTierFallbackChain 钉住 C3-H3：冻结快照的计价档位必须
// 与网关 cost_usd 同源（网关不写 billed_service_tier，实际口径是 reported ??
// effective ?? requested ?? standard）。修复前传 enriched.BilledServiceTier，
// 网关链路恒为空 → 快照恒按 standard 计价，与 cost_usd 的分档价分叉。
func TestC3H3FreezeServiceTierFallbackChain(t *testing.T) {
	cases := []struct {
		name            string
		billed          string
		reported        string
		effective       string
		requested       string
		wantServiceTier string
	}{
		{name: "requested 非空", requested: "priority", wantServiceTier: "priority"},
		{name: "effective 非空", effective: "flex", requested: "priority", wantServiceTier: "flex"},
		{name: "reported 非空", reported: "priority", effective: "flex", requested: "priority", wantServiceTier: "priority"},
		{name: "billed 优先", billed: "batch", reported: "priority", effective: "flex", requested: "priority", wantServiceTier: "batch"},
		{name: "全部为空回落 standard", wantServiceTier: ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			catalog := &mockCatalog{
				resolveModel: "gpt-5",
				breakdown:    &CostBreakdown{Multiplier: 1, ServiceTierPricingSource: TierSourceTierSpecific},
			}
			input := UsageRecordInput{
				TraceID: "t", TrafficSource: TrafficSourceGateway, ProviderCode: "gpt",
				Model: "gpt-5", InputTokens: intPtr(100), CostUsd: floatPtr(0.2),
				BilledServiceTier:    testCase.billed,
				ReportedServiceTier:  testCase.reported,
				EffectiveServiceTier: testCase.effective,
				RequestedServiceTier: testCase.requested,
			}
			EnrichUsageRecordPricing(context.Background(), input, catalog)
			if catalog.breakdownCalls != 1 {
				t.Fatalf("breakdown calls = %d, want 1", catalog.breakdownCalls)
			}
			if catalog.lastServiceTier != testCase.wantServiceTier {
				t.Fatalf("服务档位 = %q, want %q（回退链 billed ?? reported ?? effective ?? requested；空值由目录适配器归一为 standard）",
					catalog.lastServiceTier, testCase.wantServiceTier)
			}
		})
	}
}

// TestC3H3FreezeSnapshotUsesTierPricing：同一条记录在快照生成路径上使用
// reported 档位（网关实际写法），确保冻结快照的档位单价与 cost_usd 一致。
func TestC3H3FreezeSnapshotUsesTierPricing(t *testing.T) {
	catalog := &mockCatalog{
		resolveModel: "gpt-5",
		breakdown: &CostBreakdown{
			AccountChargeUsd:         floatPtr(0.42),
			Multiplier:               1,
			ServiceTierPricingSource: TierSourceTierSpecific,
		},
	}
	input := UsageRecordInput{
		TraceID: "t", TrafficSource: TrafficSourceGateway, ProviderCode: "gpt",
		Model: "gpt-5", InputTokens: intPtr(100),
		ReportedServiceTier: "priority",
	}
	result := FreezeUsageRecordPricingFacts(context.Background(), input, catalog)
	snapshot, ok := result.PricingSnapshot.(*CostBreakdown)
	if !ok || snapshot.AccountChargeUsd == nil || *snapshot.AccountChargeUsd != 0.42 {
		t.Fatalf("frozen snapshot = %+v", result.PricingSnapshot)
	}
	if catalog.lastServiceTier != "priority" {
		t.Fatalf("冻结计价档位 = %q, want priority", catalog.lastServiceTier)
	}
}
