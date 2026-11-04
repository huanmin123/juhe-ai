package gatewayopenai

import (
	"strings"
	"testing"
)

// BUG-0353：普通事件结束后必须清理 oversized 临时状态，否则后续超大
// response.completed 会复用整条流第一条普通事件的类型，终止识别丢失。
func TestOversizedTerminalAfterNormalEventKeepsEventType(t *testing.T) {
	inspector := NewStreamInspector()
	inspector.PushText("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n")

	pad := strings.Repeat("x", 300*1024)
	inspector.PushText("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"content\":[{\"type\":\"output_text\",\"text\":\"" + pad + "\"}]}],\"usage\":{\"input_tokens\":11,\"output_tokens\":2}}}\n\n")

	snapshot := inspector.Finish()
	if snapshot.Skipped {
		t.Fatalf("可识别的超大终止事件不应整流失明: %+v", snapshot)
	}
	if !snapshot.TerminalReceived {
		t.Fatalf("超大 response.completed 应终止: %+v", snapshot)
	}
	if snapshot.EventTypeCounts["response.completed"] != 1 {
		t.Fatalf("response.completed 计数 = %d，期望 1", snapshot.EventTypeCounts["response.completed"])
	}
	if snapshot.EventTypeCounts["response.created"] != 1 {
		t.Fatalf("response.created 计数 = %d，期望 1（不得复用为 2）", snapshot.EventTypeCounts["response.created"])
	}
	if snapshot.LastEventType != "response.completed" {
		t.Fatalf("LastEventType = %q，期望 response.completed", snapshot.LastEventType)
	}
	waOAssertToken(t, snapshot.Usage.InputTokens, 11, "input（超大终止事件尾部）")
	waOAssertToken(t, snapshot.Usage.OutputTokens, 2, "output（超大终止事件尾部）")
}

// 无 event: 名的数据帧同样走 data 内 type 前缀识别；上一条普通事件的
// 类型不得泄漏到超大终止事件。
func TestOversizedTerminalWithoutEventNameKeepsDataType(t *testing.T) {
	inspector := NewStreamInspector()
	inspector.PushText("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")

	pad := strings.Repeat("y", 300*1024)
	inspector.PushText("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"content\":[{\"type\":\"output_text\",\"text\":\"" + pad + "\"}]}]}}\n\n")

	snapshot := inspector.Finish()
	if snapshot.Skipped {
		t.Fatalf("可识别的超大终止事件不应整流失明: %+v", snapshot)
	}
	if !snapshot.TerminalReceived {
		t.Fatalf("无 event 名的超大 response.completed 应终止: %+v", snapshot)
	}
	if snapshot.EventTypeCounts["response.completed"] != 1 {
		t.Fatalf("response.completed 计数 = %d，期望 1", snapshot.EventTypeCounts["response.completed"])
	}
	if snapshot.EventTypeCounts["response.output_text.delta"] != 1 {
		t.Fatalf("delta 计数 = %d，期望 1（不得复用为 2）", snapshot.EventTypeCounts["response.output_text.delta"])
	}
	if snapshot.LastEventType != "response.completed" {
		t.Fatalf("LastEventType = %q，期望 response.completed", snapshot.LastEventType)
	}
}
