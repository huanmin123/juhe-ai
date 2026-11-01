package gatewayresponse

// 深挖复审 R2 验证后回归修复批（DD2-B1 / DD2-B2）钉住测试：桥接 usage 快照
// 的消费以完成流为条件（pipeResult.Completed）。截断流（clean EOF 但缺协议
// 终止事件 → missing_terminal 失败结算，pipeResult.Completed=false）不消费
// 快照，回落既有估算兜底——修复前 finalize 无条件消费快照，中途 usage 的
// 截断流按半程 {N,0} 计费成 0 输出（chat 桥半程 usage 与 gemini 桥任意事件
// 带 usage 键的形态是同一消费面）。完成流快照保持权威（对照：
// TestDDRC1BridgeUsageSnapshotSuppressesFallback，fixture 为 delta+finish+
// [DONE] 完成流）。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// TestDD2B1TruncatedStreamSkipsBridgeSnapshotFallsBackToEstimate 钉住：只有
// 内容 delta、无 finish/[DONE] 的截断流（missing_terminal → Completed=false）
// 携带半程 usage 快照（ok=true）时，UsageFallback 仍被咨询且估算值成为失败
// 记账（RecordCompletedUpstreamAttempt，Success=false）的计费 usage——不得
// 按快照 {100,50} 或 {N,0} 计费。失败终态 return 不携带 result.Usage，计费
// 面在 UsageRecords 记账入参。
func TestDD2B1TruncatedStreamSkipsBridgeSnapshotFallsBackToEstimate(t *testing.T) {
	input, _ := newInputFixture(NewSliceUpstreamBody([]byte(chatDeltaChunk)), 200, nil)
	usageRecords := &mockUsageRecords{}
	fallbackCalls := 0
	estimatedOutput := 42
	input.Deps = &FinalizationDeps{
		UsageRecords:   usageRecords,
		AccountEffects: &mockAccountEffects{},
		NowMs:          func() int64 { return 1000 },
		UsageFallback: func(_ ResponseDriverPort, _ gatewayproto.ParsedUsage, facts UsageFallbackInput) (gatewayproto.ParsedUsage, bool, *int, *int) {
			fallbackCalls++
			if facts.Completed {
				t.Fatal("截断流的 fallback facts.Completed 必须 = false")
			}
			return gatewayproto.ParsedUsage{OutputTokens: &estimatedOutput}, true, nil, &estimatedOutput
		},
	}
	snapshotInput, snapshotOutput := 100, 50
	input.BridgeUsage = func() (gatewayproto.ParsedUsage, bool) {
		// 桥泵在 pump err==nil（clean EOF）时已写入的半程 usage 快照。
		return gatewayproto.ParsedUsage{InputTokens: &snapshotInput, OutputTokens: &snapshotOutput}, true
	}

	result, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fallbackCalls != 1 {
		t.Fatalf("UsageFallback 调用次数 = %d, want 1（截断流必须回落估算兜底）", fallbackCalls)
	}
	if len(usageRecords.completed) != 1 {
		t.Fatalf("失败记账条数 = %d, want 1", len(usageRecords.completed))
	}
	recorded := usageRecords.completed[0]
	if recorded.Success {
		t.Fatal("截断流记账必须 Success=false")
	}
	if recorded.Usage.OutputTokens == nil || *recorded.Usage.OutputTokens != 42 {
		t.Fatalf("记账 usage = %+v, want 估算 output 42（截断流按可见输出计费，而非快照 50/0）", recorded.Usage)
	}
	if recorded.Usage.InputTokens != nil && *recorded.Usage.InputTokens == 100 {
		t.Fatalf("记账 usage.InputTokens = %d, 截断流不得消费半程快照输入值", *recorded.Usage.InputTokens)
	}
	_ = result
}
