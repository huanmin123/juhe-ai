package main

// 完成尝试 usage 行 DurationMs 口径单元验证：
//
//   - 未传 CompletedAtMs（gatewayresponse 生产构造点的实际形态）时，按兜底
//     口径取完成收尾观测时刻计算 DurationMs（finalize 时刻 ≈ 同一次 HTTP
//     完成时刻，对齐 Node httpCompletion.wait 语义），记录不得再落 NULL；
//   - 显式传 CompletedAtMs 时保持精确透传（completedAt - startedAt）。

import (
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
)

// TestChainFinalizationUsageCompletedAttemptDurationFallback：锁定 DurationMs
// 两个口径——缺省 CompletedAtMs 的兜底非空与显式 CompletedAtMs 的精确透传。
func TestChainFinalizationUsageCompletedAttemptDurationFallback(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	usage := chainFinalizationUsage{recorder: recorder}

	// (a) 不传 CompletedAtMs：兜底口径 = 完成收尾观测时刻，DurationMs 非空且
	// ≈ now - StartedAtMs（StartedAtMs 取 1s 前，容忍调度抖动取 900..5000）。
	startedAtMs := time.Now().UnixMilli() - 1_000
	usage.RecordCompletedUpstreamAttempt(gatewayresponse.CompletedAttemptInput{
		UsageContext: gatewaypreauth.GatewayFailureUsageContext{TraceID: "trace_duration_fallback"},
		Success:      true,
		StatusCode:   200,
		Stream:       true,
		StartedAtMs:  startedAtMs,
	})
	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(recorder.records))
	}
	fallback := recorder.records[0]
	if fallback.DurationMs == nil {
		t.Fatal("未传 CompletedAtMs 时 DurationMs 必须按兜底口径非空")
	}
	if *fallback.DurationMs < 900 || *fallback.DurationMs > 5_000 {
		t.Fatalf("兜底 DurationMs = %d, want 900..5000（StartedAtMs 取 1s 前）", *fallback.DurationMs)
	}

	// (b) 显式传 CompletedAtMs = StartedAtMs + 500：精确透传 500。
	recorder.records = nil
	explicitCompletedAtMs := startedAtMs + 500
	usage.RecordCompletedUpstreamAttempt(gatewayresponse.CompletedAttemptInput{
		UsageContext:  gatewaypreauth.GatewayFailureUsageContext{TraceID: "trace_duration_explicit"},
		Success:       true,
		StatusCode:    200,
		Stream:        true,
		StartedAtMs:   startedAtMs,
		CompletedAtMs: &explicitCompletedAtMs,
	})
	if len(recorder.records) != 1 {
		t.Fatalf("显式传值 records = %d, want 1", len(recorder.records))
	}
	explicit := recorder.records[0]
	if explicit.DurationMs == nil || *explicit.DurationMs != 500 {
		t.Fatalf("显式 DurationMs = %v, want 500（completedAt - startedAt）", explicit.DurationMs)
	}
}
