package main

// T188 性能回归锚点：gatewayusage.UsageRecordInput 与 usagewriter 输入已是
// 同一类型别名，直投桥常规路径必须零分配（此前 JSON 桥约 13.5µs/73 allocs）。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

func benchUsageRecord() gatewayusage.UsageRecordInput {
	i := 123
	f := 0.0123
	b := true
	return gatewayusage.UsageRecordInput{
		ID: "usage_20261109_s01_1700000000000_abcdef", SystemAccountID: "sys_1", TraceID: "trace_1",
		TrafficSource: "gateway", ClientIP: "203.0.113.9", APIKeyID: "key_1", GroupID: "g1", RouteGroupID: "rg1",
		CompactionExpected: true, SecurityPolicyApplied: true, PIIHits: map[string]int{"EMAIL": 1},
		AccountID: "acc_1", AccountOwnerSystemAccountID: "sys_owner", GroupOwnerSystemAccountID: "sys_owner",
		AccountAccessType: "owner", GroupAccessType: "owner", AccountAuthorizationID: "auth_1",
		AccountAuthorizationSourceType: "manual", GroupAuthorizationID: "gauth_1", GroupAuthorizationSourceType: "manual",
		Endpoint: "/v1/responses", ProviderCode: "gpt", ProviderProtocolProfileID: "profile_1", UsageSemantic: "openai",
		Model: "gpt-5.6-sol", UpstreamModel: "gpt-5.6-sol", UpstreamResponseModel: "gpt-5.6-sol", PricingModel: "gpt-5.6-sol",
		RequestedServiceTier: "default", EffectiveServiceTier: "priority", ReportedServiceTier: "priority", BilledServiceTier: "priority",
		RequestedReasoningEffort: "high", EffectiveReasoningEffort: "high", ModelMappingApplied: &b, ModelMappingSource: "vendor",
		SourceEndpointFamily: "responses", UpstreamEndpointFamily: "responses", Stream: &b, StatusCode: &i, Success: true,
		FirstTokenMs: &i, DurationMs: &i, InputTokens: &i, OutputTokens: &i, CacheReadTokens: &i, CacheReadCostUsd: &f,
		CacheWriteTokens: &i, CacheWrite1hTokens: &i, CacheWriteCostUsd: &f, ThinkingTokens: &i, InputImageTokens: &i,
		OutputImageTokens: &i, InputAudioTokens: &i, OutputAudioTokens: &i, OutputImageCount: &i, TtsInputChars: func() *int64 { v := int64(10); return &v }(),
		AudioInputSeconds: &f, OutputVideoSeconds: &f, CostUsd: &f, CreatedAt: "2026-11-09T10:00:00.000Z",
	}
}

func BenchmarkConvertUsageRecordInput(b *testing.B) {
	in := benchUsageRecord()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := convertUsageRecordInput(in); err != nil {
			b.Fatal(err)
		}
	}
}
