package usagewriter

// T188 性能回归锚点：gateway 直投后 Enqueue 在 gateway 进程热路径执行。
// 参考（i9-13900K）：无冻结 ~7µs/6.7KB，冻结 ~9.5µs；
// EstimateUsageRecordBytes ~4.8µs（反射遍历，队列字节预算用途）。

import (
	"context"
	"testing"
)

func benchInput() UsageRecordInput {
	i := 100
	f := 0.01
	return UsageRecordInput{
		ID: "usage_20261109_s01_1700000000000_abcdef", SystemAccountID: "sys_1", TraceID: "trace", TrafficSource: "gateway",
		APIKeyID: "k", GroupID: "g", AccountID: "a", Endpoint: "/v1/responses", ProviderCode: "gpt",
		Model: "gpt-5.6-sol", PricingModel: "gpt-5.6-sol", InputTokens: &i, OutputTokens: &i, CostUsd: &f,
		RequestedServiceTier: "default", Success: true, CreatedAt: "2026-11-09T10:00:00.000Z",
	}
}

func BenchmarkWriterEnqueueNoFreeze(b *testing.B) {
	w := NewWriter(Config{QueueMaxItems: 1_000_000, QueueMaxBytes: 1 << 30, FreezePricing: false, ShardCount: 16}, &mockStore{}, nil, WithCatalog(&mockCatalog{resolveModel: "gpt-5.6-sol", breakdown: &CostBreakdown{Multiplier: 1, ServiceTierPricingSource: TierSourceDefault}}))
	in := benchInput()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := w.Enqueue(ctx, in); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEstimateUsageRecordBytes(b *testing.B) {
	in := benchInput()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EstimateUsageRecordBytes(in, 64*1024*1024)
	}
}
