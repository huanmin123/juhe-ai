package main

// 压缩标记透传（用量记录压缩标记设计 §压缩请求识别）：preflight 冻结的请求
// 级压缩期望（GatewayFailureUsageContext.CompactionExpected，单点判定
// gatewaycodex.CodexCompactionExpectedForRequest）必须经 chainFinalizationUsage
// 构造点注入，完成与失败两条记账原样落 UsageRecordInput.CompactionExpected；
// 普通请求零值 false。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

func TestChainFinalizationUsageCompactionExpected(t *testing.T) {
	statusCode := 502
	// 压缩请求：构造点表达式与 chain_v1.go 同源（context.UsageContext 字段）。
	compactionUsageContext := gatewaypreauth.GatewayFailureUsageContext{
		TraceID: "trace-compaction-main",
	}
	compactionUsageContext.CompactionExpected = true
	recorder := gatewayusage.NewMemoryUsageRecorder(nil, nil)
	usage := chainFinalizationUsage{recorder: recorder, compactionExpected: compactionUsageContext.CompactionExpected}

	completed := accountingCompletedInput()
	completed.UsageContext = compactionUsageContext
	usage.RecordCompletedUpstreamAttempt(completed)
	usage.RecordFailedUpstreamAttempt(gatewayresponse.FailedAttemptInput{
		UsageContext: compactionUsageContext,
		StatusCode:   &statusCode,
		ErrorMessage: "上游 502",
	})

	records := recorder.Records()
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
	if !records[0].CompactionExpected {
		t.Errorf("完成记录 CompactionExpected = false, want true")
	}
	if !records[1].CompactionExpected {
		t.Errorf("失败记录 CompactionExpected = false, want true")
	}

	// 普通请求：UsageContext 未冻结压缩期望，两条记账保持 false。
	ordinaryContext := gatewaypreauth.GatewayFailureUsageContext{TraceID: "trace-compaction-ordinary"}
	ordinaryRecorder := gatewayusage.NewMemoryUsageRecorder(nil, nil)
	ordinary := chainFinalizationUsage{recorder: ordinaryRecorder, compactionExpected: ordinaryContext.CompactionExpected}
	ordinaryCompleted := accountingCompletedInput()
	ordinaryCompleted.UsageContext = ordinaryContext
	ordinary.RecordCompletedUpstreamAttempt(ordinaryCompleted)
	ordinary.RecordFailedUpstreamAttempt(gatewayresponse.FailedAttemptInput{
		UsageContext: ordinaryContext,
		StatusCode:   &statusCode,
		ErrorMessage: "上游 502",
	})
	ordinaryRecords := ordinaryRecorder.Records()
	if len(ordinaryRecords) != 2 {
		t.Fatalf("ordinary records = %d, want 2", len(ordinaryRecords))
	}
	for index, record := range ordinaryRecords {
		if record.CompactionExpected {
			t.Errorf("普通请求 records[%d].CompactionExpected = true, want false", index)
		}
	}
}
