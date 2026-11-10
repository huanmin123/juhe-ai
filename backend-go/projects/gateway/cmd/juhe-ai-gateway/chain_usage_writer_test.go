package main

// T188（2026-11-09）：网关用量记录直投 PG 的字段桥测试。gatewayusage 与
// usagewriter 两套输入结构靠 JSON tag 对齐，本测试钉住关键字段（身份、
// 计费、token、状态、时间）不会因任一侧改名/改类型而静默丢值。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
	"github.com/huanminabc/juhe-ai/backend-go-platform/usagewriter"
)

func usageWriterTestInt(value int) *int           { return &value }
func usageWriterTestInt64(value int64) *int64     { return &value }
func usageWriterTestFloat(value float64) *float64 { return &value }
func usageWriterTestBool(value bool) *bool        { return &value }

func TestConvertUsageRecordInputPreservesBillingFacts(t *testing.T) {
	input := gatewayusage.UsageRecordInput{
		ID:                          "usage_1",
		SystemAccountID:             "sys_1",
		TraceID:                     "trace_1",
		TrafficSource:               "gateway",
		ClientIP:                    "203.0.113.9",
		APIKeyID:                    "key_1",
		GroupID:                     "group_1",
		RouteGroupID:                "route_group_1",
		CompactionExpected:          true,
		SecurityPolicyApplied:       true,
		PIIHits:                     map[string]int{"email": 2},
		AccountID:                   "acc_1",
		AccountOwnerSystemAccountID: "sys_owner",
		Endpoint:                    "/v1/chat/completions",
		ProviderCode:                "gpt",
		ProviderProtocolProfileID:   "profile_1",
		Model:                       "gpt-5.6-sol",
		UpstreamModel:               "gpt-5.6-sol",
		PricingModel:                "gpt-5.6-sol",
		RequestedServiceTier:        "default",
		EffectiveServiceTier:        "priority",
		BilledServiceTier:           "priority",
		PricingSnapshot:             map[string]any{"accountChargeUsd": 0.069317},
		ModelMappingApplied:         usageWriterTestBool(true),
		SourceEndpointFamily:        "chat_completions",
		UpstreamEndpointFamily:      "chat_completions",
		Stream:                      usageWriterTestBool(true),
		StatusCode:                  usageWriterTestInt(200),
		Success:                     true,
		FirstTokenMs:                usageWriterTestInt(120),
		DurationMs:                  usageWriterTestInt(880),
		InputTokens:                 usageWriterTestInt(13_196),
		OutputTokens:                usageWriterTestInt(167),
		CacheReadTokens:             usageWriterTestInt(4_000),
		CacheReadCostUsd:            usageWriterTestFloat(0.01),
		CacheWriteTokens:            usageWriterTestInt(13_193),
		CacheWrite1hTokens:          usageWriterTestInt(100),
		CacheWriteCostUsd:           usageWriterTestFloat(0.065965),
		ThinkingTokens:              usageWriterTestInt(9),
		InputImageTokens:            usageWriterTestInt(2),
		OutputImageTokens:           usageWriterTestInt(3),
		InputAudioTokens:            usageWriterTestInt(4),
		OutputAudioTokens:           usageWriterTestInt(5),
		OutputImageCount:            usageWriterTestInt(1),
		TtsInputChars:               usageWriterTestInt64(120),
		AudioInputSeconds:           usageWriterTestFloat(3.5),
		OutputVideoSeconds:          usageWriterTestFloat(8),
		UsageMissing:                true,
		CostUsd:                     usageWriterTestFloat(0.069317),
		ErrorCode:                   "",
		ErrorMessage:                "",
		CreatedAt:                   "2026-11-09T10:00:00.000Z",
	}

	converted, err := convertUsageRecordInput(input)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if converted.ID != input.ID || converted.SystemAccountID != input.SystemAccountID || converted.TraceID != input.TraceID {
		t.Fatalf("identity lost: %+v", converted)
	}
	if converted.APIKeyID != input.APIKeyID || converted.GroupID != input.GroupID || converted.RouteGroupID != input.RouteGroupID {
		t.Fatalf("routing identity lost: %+v", converted)
	}
	if !converted.CompactionExpected || !converted.SecurityPolicyApplied || converted.PIIHits["email"] != 2 {
		t.Fatalf("request-level facts lost: %+v", converted)
	}
	if converted.Model != input.Model || converted.UpstreamModel != input.UpstreamModel || converted.PricingModel != input.PricingModel {
		t.Fatalf("model facts lost: %+v", converted)
	}
	if converted.EffectiveServiceTier != input.EffectiveServiceTier || converted.BilledServiceTier != input.BilledServiceTier {
		t.Fatalf("service tier lost: %+v", converted)
	}
	if converted.InputTokens == nil || *converted.InputTokens != 13_196 ||
		converted.OutputTokens == nil || *converted.OutputTokens != 167 {
		t.Fatalf("token facts lost: %+v", converted)
	}
	if converted.CacheWriteTokens == nil || *converted.CacheWriteTokens != 13_193 ||
		converted.CacheWriteCostUsd == nil || *converted.CacheWriteCostUsd != 0.065965 {
		t.Fatalf("cache write billing lost: %+v", converted)
	}
	if converted.CostUsd == nil || *converted.CostUsd != 0.069317 {
		t.Fatalf("cost lost: %+v", converted)
	}
	if converted.StatusCode == nil || *converted.StatusCode != 200 || !converted.Success {
		t.Fatalf("status lost: %+v", converted)
	}
	if converted.CreatedAt != input.CreatedAt || converted.DurationMs == nil || *converted.DurationMs != 880 {
		t.Fatalf("time facts lost: %+v", converted)
	}
	if converted.PricingSnapshot == nil {
		t.Fatal("pricing snapshot lost")
	}
	if _, ok := converted.PricingSnapshot.(*usagewriter.CostBreakdown); !ok {
		t.Fatalf("pricing snapshot must be typed *CostBreakdown after bridge, got %T", converted.PricingSnapshot)
	}
}

func TestConvertUsageRecordInputDropsSuppressUsage(t *testing.T) {
	input := gatewayusage.UsageRecordInput{
		ID:            "usage_suppressed",
		TraceID:       "trace_suppressed",
		TrafficSource: "gateway",
		SuppressUsage: true,
		Success:       true,
	}
	converted, err := convertUsageRecordInput(input)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	// SuppressUsage 带 json:"-"，只用于进程内闸口（recorder/dispatch 在更早处
	// 丢弃）；类型别名后字段仍在内存记录上，但不会进入 writer 的落库列。
	if converted.ID != input.ID || converted.TraceID != input.TraceID {
		t.Fatalf("identity lost: %+v", converted)
	}
	if !converted.SuppressUsage {
		t.Fatal("SuppressUsage must survive the same-type bridge")
	}
}
