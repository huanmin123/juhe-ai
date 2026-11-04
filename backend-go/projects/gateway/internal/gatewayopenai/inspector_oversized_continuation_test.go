package gatewayopenai

import (
	"strings"
	"testing"
)

// BUG-0354：未知类型超大 data 行只丢弃当前事件的完整解析，后续 [DONE]
// 必须继续解析；无 type 时按 message 记 summary，输出提示仍可置位。
func TestOversizedUnknownDataLineKeepsParsingFollowingEvents(t *testing.T) {
	inspector := NewStreamInspector()
	pad := strings.Repeat("x", 300*1024)
	inspector.PushText("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + pad + "\"}}]}\n\n")
	inspector.PushText("data: [DONE]\n\n")

	snapshot := inspector.Finish()
	if snapshot.Skipped {
		t.Fatalf("超大 data 行不应整流跳过: %+v", snapshot)
	}
	if !snapshot.TerminalReceived {
		t.Fatalf("后续 [DONE] 应被识别为终止: %+v", snapshot)
	}
	if snapshot.EventTypeCounts["message"] != 1 {
		t.Fatalf("超大未知事件应按 message 计数: %+v", snapshot.EventTypeCounts)
	}
	if !snapshot.OutputReceived || snapshot.OutputEventCount != 1 {
		t.Fatalf("超大未知事件应通过输出键提示置位: %v/%d", snapshot.OutputReceived, snapshot.OutputEventCount)
	}

	summaries := inspector.DrainEventSummaries()
	if len(summaries) != 2 {
		t.Fatalf("summary 数 = %d，期望 2", len(summaries))
	}
	if !summaries[0].ParseError || !summaries[0].Output || summaries[0].Terminal || summaries[0].Failed {
		t.Fatalf("超大未知事件 summary = %+v", summaries[0])
	}
	if !summaries[1].Terminal || summaries[1].Output || summaries[1].Failed {
		t.Fatalf("[DONE] summary 不应继承前一事件提示 = %+v", summaries[1])
	}
}

// BUG-0354：超大顶层 {"error":{...}} 数据行必须置 FailedReceived，
// 即使后续 [DONE] 也不能把错误吞成成功。
func TestOversizedTopLevelErrorDataLineNotSwallowed(t *testing.T) {
	inspector := NewStreamInspector()
	pad := strings.Repeat("x", 300*1024)
	inspector.PushText("data: {\"error\":{\"message\":\"" + pad + "\"}}\n\n")
	inspector.PushText("data: [DONE]\n\n")

	snapshot := inspector.Finish()
	if snapshot.Skipped {
		t.Fatalf("超大 error data 行不应整流跳过: %+v", snapshot)
	}
	if !snapshot.FailedReceived {
		t.Fatalf("超大顶层 error 帧必须置 FailedReceived: %+v", snapshot)
	}
	if !snapshot.TerminalReceived {
		t.Fatalf("失败事件必须同时终止: %+v", snapshot)
	}
	if snapshot.EventTypeCounts["message"] != 1 || snapshot.EventTypeCounts["[DONE]"] != 1 {
		t.Fatalf("事件计数 = %+v", snapshot.EventTypeCounts)
	}

	summaries := inspector.DrainEventSummaries()
	if len(summaries) != 2 {
		t.Fatalf("summary 数 = %d，期望 2", len(summaries))
	}
	if !summaries[0].ParseError || !summaries[0].Failed || !summaries[0].Terminal {
		t.Fatalf("超大 error summary = %+v", summaries[0])
	}
	if summaries[1].Failed || summaries[1].Output || !summaries[1].Terminal {
		t.Fatalf("[DONE] summary 不应继承失败提示 = %+v", summaries[1])
	}
}

// 输出提示只认精确 JSON 键："output_tokens" 不能命中 "output"。
func TestOversizedOutputHintIgnoresOutputTokensKey(t *testing.T) {
	inspector := NewStreamInspector()
	pad := strings.Repeat("x", 300*1024)
	inspector.PushText("data: {\"usage\":{\"output_tokens\":123},\"padding\":\"" + pad + "\"}\n\n")

	snapshot := inspector.Finish()
	if snapshot.Skipped {
		t.Fatalf("超大 data 行不应整流跳过: %+v", snapshot)
	}
	if snapshot.OutputReceived || snapshot.OutputEventCount != 0 {
		t.Fatalf("output_tokens 不应触发输出提示: %v/%d", snapshot.OutputReceived, snapshot.OutputEventCount)
	}
}

// 输出键只出现在尾窗时同样命中（首 4096 字节之外）。
func TestOversizedOutputHintUsesTailWindow(t *testing.T) {
	inspector := NewStreamInspector()
	pad := strings.Repeat("x", 300*1024)
	inspector.PushText("data: {\"padding\":\"" + pad + "\",\"content\":\"tail\"}\n\n")

	snapshot := inspector.Finish()
	if snapshot.Skipped {
		t.Fatalf("超大 data 行不应整流跳过: %+v", snapshot)
	}
	if !snapshot.OutputReceived || snapshot.OutputEventCount != 1 {
		t.Fatalf("尾窗 output 键应置位输出提示: %v/%d", snapshot.OutputReceived, snapshot.OutputEventCount)
	}
}

// BUG-0354 + BUG-0353：error/output 提示属于单个事件，普通事件收尾后必须
// 清理，后续无提示的超大事件不得继承上一条事件的失败/输出结论。
func TestOversizedHintStateDoesNotLeakAcrossEvents(t *testing.T) {
	inspector := NewStreamInspector()
	inspector.PushText("data: {\"error\":{\"code\":\"boom\"},\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	inspector.PushText("data: " + strings.Repeat("z", 300*1024) + "\n\n")

	inspector.Finish()
	summaries := inspector.DrainEventSummaries()
	if len(summaries) != 2 {
		t.Fatalf("summary 数 = %d，期望 2", len(summaries))
	}
	if !summaries[0].Failed || !summaries[0].Output {
		t.Fatalf("首事件应失败且有输出: %+v", summaries[0])
	}
	if summaries[1].Failed || summaries[1].Output || summaries[1].Terminal {
		t.Fatalf("后续超大事件不得继承上一条事件的提示: %+v", summaries[1])
	}
}
