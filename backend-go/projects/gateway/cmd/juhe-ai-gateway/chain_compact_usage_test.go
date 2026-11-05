package main

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestChainCompactUsageRecorderWritesTokensAndCost(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	catalog := &fakeChainUsagePricingCatalog{cost: floatPtrOf(0.03), cacheReadCost: floatPtrOf(0.01)}
	rec := chainCompactUsageRecorder{recorder: recorder, pricing: catalog, syncPricingAllowed: true}
	rec.Record(gatewaycodex.CompactPreflightInput{
		SystemAccountID: "sys_owner",
		UsageContext: gatewaypreauth.GatewayFailureUsageContext{
			TraceID: "trace_compact", SystemAccountID: "sys_owner", APIKeyID: "key_1", GroupID: "grp_1",
			ClientIP: "127.0.0.1", Endpoint: "/v1/responses/compact", EffectiveServiceTier: "default",
		},
	}, gatewayruntimecache.OpenAIAccountSecret{
		ID: "acc_compact", VendorCode: "gpt", ProviderProtocolProfileID: "profile_gpt",
		AccountOwnerSystemAccountID: "sys_owner", AccountAccessType: "owner",
	}, "gpt-5", gatewayproto.ParsedUsage{InputTokens: intPtrOf(11), OutputTokens: intPtrOf(7)})
	if len(recorder.records) != 1 {
		t.Fatalf("records=%d", len(recorder.records))
	}
	record := recorder.records[0]
	if record.TraceID != "trace_compact" || record.AccountID != "acc_compact" || record.Model != "gpt-5" {
		t.Fatalf("record identity=%+v", record)
	}
	if record.InputTokens == nil || *record.InputTokens != 11 || record.OutputTokens == nil || *record.OutputTokens != 7 {
		t.Fatalf("record tokens=%+v", record)
	}
	if record.CostUsd == nil || *record.CostUsd != 0.03 || record.CacheReadCostUsd == nil || *record.CacheReadCostUsd != 0.01 {
		t.Fatalf("record costs=%+v", record)
	}
	if !record.CompactionExpected || record.Stream == nil || *record.Stream || record.StatusCode == nil || *record.StatusCode != 200 {
		t.Fatalf("record flags=%+v", record)
	}
}
