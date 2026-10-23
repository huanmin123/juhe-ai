package gatewayusage

// 压缩标记透传（用量记录压缩标记设计 §压缩请求识别）：请求级上下文的
// CompactionExpected 必须从两条失败记账入口原样进到 UsageRecordInput，随
// spool JSON 透传落列 compaction_expected。普通请求零值 false。

import (
	"context"
	"testing"
)

func TestCompactionExpectedReachesUsageRecordInput(t *testing.T) {
	t.Run("failed_upstream_attempt_true", func(t *testing.T) {
		harness := newHarness(ServiceConfig{FinalizationMaxItems: 8, FinalizationMaxConcurrency: 2})
		usageCtx := usageContext()
		usageCtx.CompactionExpected = true
		if err := harness.service.RecordFailedUpstreamAttempt(context.Background(), usageCtx, testAccount(), RecordFailedUpstreamAttemptInput{
			Model:       "gpt-requested",
			UpstreamURL: "https://upstream.example.com/v1",
			StartedAtMs: 1700000000000 - 10,
		}); err != nil {
			t.Fatalf("err = %v", err)
		}
		if !harness.service.dispatch.WaitForIdle(2000) {
			t.Fatal("not idle")
		}
		record, ok := harness.recorder.LastRecord()
		if !ok {
			t.Fatal("record missing")
		}
		if !record.CompactionExpected {
			t.Fatalf("CompactionExpected = false, want true（失败入口透传）")
		}
	})

	t.Run("gateway_failure_true", func(t *testing.T) {
		harness := newHarness(ServiceConfig{FinalizationMaxItems: 8, FinalizationMaxConcurrency: 2})
		failureCtx := GatewayFailureUsageContext{GatewayUsageContext: usageContext()}
		failureCtx.CompactionExpected = true
		if err := harness.service.RecordGatewayFailure(context.Background(), failureCtx, RecordGatewayFailureInput{
			Model:       "gpt-requested",
			StatusCode:  403,
			StartedAtMs: 1700000000000 - 10,
		}); err != nil {
			t.Fatalf("err = %v", err)
		}
		if !harness.service.dispatch.WaitForIdle(2000) {
			t.Fatal("not idle")
		}
		record, ok := harness.recorder.LastRecord()
		if !ok {
			t.Fatal("record missing")
		}
		if !record.CompactionExpected {
			t.Fatalf("CompactionExpected = false, want true（网关失败入口透传）")
		}
	})

	t.Run("ordinary_request_defaults_false", func(t *testing.T) {
		harness := newHarness(ServiceConfig{FinalizationMaxItems: 8, FinalizationMaxConcurrency: 2})
		if err := harness.service.RecordFailedUpstreamAttempt(context.Background(), usageContext(), testAccount(), RecordFailedUpstreamAttemptInput{
			Model:       "gpt-requested",
			UpstreamURL: "https://upstream.example.com/v1",
			StartedAtMs: 1700000000000 - 10,
		}); err != nil {
			t.Fatalf("err = %v", err)
		}
		if !harness.service.dispatch.WaitForIdle(2000) {
			t.Fatal("not idle")
		}
		record, ok := harness.recorder.LastRecord()
		if !ok {
			t.Fatal("record missing")
		}
		if record.CompactionExpected {
			t.Fatalf("普通请求 CompactionExpected = true, want false")
		}
	})
}
