package gatewayresponse

// DD-RC-C1 finalize 侧钉住：BridgeUsage 供给的 ok 值决定估算兜底是否被否决。
// 修复前的回归形态：桥接流上游未发 usage 时包装层把 nil 桥状态转成零值快照
// （ok=true）→ finalize 判 bridgedUsage 跳过 UsageFallback → 计费 0。契约
// （HandleUpstreamResponseInput.BridgeUsage 注释）本来就规定「返回 false =
// 本次 attempt 非桥接 / 上游未发 usage → 保持既有 inspector/估算兜底路径」；
// 本组测试钉住 finalize 消费端对该契约的履行：
//   - ok=false（上游未发 usage 的修复后供给形态）→ UsageFallback 被咨询，其
//     估算值成为最终结算 usage（非 0 计费）；
//   - ok=true（上游确实发了 usage 的快照，含全 0）→ 快照权威，fallback 不被
//     咨询（既有 DD-C6 语义保持）。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// ddRC1NoUsageFinishChunk 是不带 usage 的 chat finish 分片。
const ddRC1NoUsageFinishChunk = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"

func ddRC1StreamInput() HandleUpstreamResponseInput {
	input, _ := newInputFixture(NewSliceUpstreamBody(
		[]byte(chatDeltaChunk),
		[]byte(ddRC1NoUsageFinishChunk),
		[]byte(chatDoneChunk),
	), 200, nil)
	return input
}

// TestDDRC1BridgeUsageMissingKeepsUsageFallback 钉住：BridgeUsage 返回
// ok=false（上游未发 usage 的供给形态）时 UsageFallback 被咨询，估算值成为
// 最终 usage——不得被零值快照否决成计费 0。
func TestDDRC1BridgeUsageMissingKeepsUsageFallback(t *testing.T) {
	input := ddRC1StreamInput()
	fallbackCalls := 0
	estimatedOutput := 42
	input.Deps = &FinalizationDeps{
		UsageRecords:   &mockUsageRecords{},
		AccountEffects: &mockAccountEffects{},
		NowMs:          func() int64 { return 1000 },
		UsageFallback: func(_ ResponseDriverPort, _ gatewayproto.ParsedUsage, _ UsageFallbackInput) (gatewayproto.ParsedUsage, bool, *int, *int) {
			fallbackCalls++
			return gatewayproto.ParsedUsage{OutputTokens: &estimatedOutput}, true, nil, &estimatedOutput
		},
	}
	input.BridgeUsage = func() (gatewayproto.ParsedUsage, bool) {
		// 修复后包装层对 nil 桥状态（上游未发 usage）的供给形态：不写快照。
		return gatewayproto.EmptyUsage(), false
	}

	result, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fallbackCalls != 1 {
		t.Fatalf("UsageFallback 调用次数 = %d, want 1（ok=false 必须回落估算兜底）", fallbackCalls)
	}
	if result.Usage.OutputTokens == nil || *result.Usage.OutputTokens != 42 {
		t.Fatalf("usage = %+v, want 估算 output 42（非 0 计费）", result.Usage)
	}
}

// TestDDRC1BridgeUsageSnapshotSuppressesFallback 对照钉住：BridgeUsage 返回
// ok=true（上游确实发了 usage 的快照）时快照权威，UsageFallback 不被咨询
// （既有 DD-C6 语义保持，TestDDC6 两方向的 finalize 侧消费契约）。
func TestDDRC1BridgeUsageSnapshotSuppressesFallback(t *testing.T) {
	input := ddRC1StreamInput()
	fallbackCalls := 0
	input.Deps = &FinalizationDeps{
		UsageRecords:   &mockUsageRecords{},
		AccountEffects: &mockAccountEffects{},
		NowMs:          func() int64 { return 1000 },
		UsageFallback: func(_ ResponseDriverPort, _ gatewayproto.ParsedUsage, _ UsageFallbackInput) (gatewayproto.ParsedUsage, bool, *int, *int) {
			fallbackCalls++
			return gatewayproto.EmptyUsage(), false, nil, nil
		},
	}
	snapshotInput, snapshotOutput := 100, 50
	input.BridgeUsage = func() (gatewayproto.ParsedUsage, bool) {
		return gatewayproto.ParsedUsage{InputTokens: &snapshotInput, OutputTokens: &snapshotOutput}, true
	}

	result, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fallbackCalls != 0 {
		t.Fatalf("UsageFallback 调用次数 = %d, want 0（快照权威，估算兜底不得参与）", fallbackCalls)
	}
	if result.Usage.InputTokens == nil || *result.Usage.InputTokens != 100 ||
		result.Usage.OutputTokens == nil || *result.Usage.OutputTokens != 50 {
		t.Fatalf("usage = %+v, want 快照 100/50", result.Usage)
	}
}
