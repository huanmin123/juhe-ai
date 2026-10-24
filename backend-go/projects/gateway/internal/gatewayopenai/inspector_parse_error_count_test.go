package gatewayopenai

import (
	"strings"
	"testing"
)

// 流式提交边界受限重放与异常终局处置设计 §3.2.0/§3.2.1：DataParseError 事件
// 计数（StreamInspection.ParseErrorEventCount）是 EOF 异常终局判定与字节级
// 隔离观测的判别键。oversized 事件走自有 summary，不进本计数。

func TestInspectorCountsDataParseErrorEvents(t *testing.T) {
	inspector := NewStreamInspector()
	inspector.PushText("data: {\"type\":\"response.output_text.delta\",\"delta\":\"你好\"}\n\n")
	// W22 形态：半截 chunk + 错误帧粘连成单个非法 JSON data 载荷。
	inspector.PushText("data: {\"choices\":[{\"delta\":{\"content\":\"上级\"}]} data: {\"error\":{\"code\":\"boom\"}}\n\n")
	inspector.PushText("data: [DONE]\n\n")
	inspection := inspector.Finish()

	if inspection.ParseErrorEventCount != 1 {
		t.Fatalf("ParseErrorEventCount = %d, want 1", inspection.ParseErrorEventCount)
	}
	if !inspection.TerminalReceived {
		t.Fatal("终止事件应照常识别")
	}
	if !inspection.OutputReceived {
		t.Fatal("首个合法 delta 的可见输出应照常识别")
	}
}

func TestInspectorParseErrorCountStaysZeroForCleanStreams(t *testing.T) {
	inspector := NewStreamInspector()
	inspector.PushText("data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
	inspector.PushText(": keep-alive\n\n")
	inspector.PushText("data: [DONE]\n\n")
	if inspection := inspector.Finish(); inspection.ParseErrorEventCount != 0 {
		t.Fatalf("ParseErrorEventCount = %d, want 0", inspection.ParseErrorEventCount)
	}
}

func TestInspectorOversizedEventsDoNotCountAsParseErrors(t *testing.T) {
	inspector := NewStreamInspector()
	// 合法 JSON 的超大 delta（base64 图像流形态）：oversized 汇总 ParseError
	// 标记存在，但不属于 DataParseError，不得进 ParseErrorEventCount。
	big := strings.Repeat("a", 600*1024)
	inspector.PushText("data: {\"type\":\"response.output_text.delta\",\"delta\":\"" + big + "\"}\n\n")
	inspection := inspector.Finish()
	if inspection.ParseErrorEventCount != 0 {
		t.Fatalf("oversized 事件不应计入 ParseErrorEventCount: %d", inspection.ParseErrorEventCount)
	}
	if !inspection.Skipped && inspection.EventCount == 0 {
		t.Fatal("oversized 事件应仍有 summary 证据")
	}
}

func TestInspectorParseErrorCountAccumulatesAcrossEvents(t *testing.T) {
	inspector := NewStreamInspector()
	inspector.PushText("data: not-json-1\n\n")
	inspector.PushText("data: not-json-2\n\n")
	inspection := inspector.Finish()
	if inspection.ParseErrorEventCount != 2 {
		t.Fatalf("ParseErrorEventCount = %d, want 2", inspection.ParseErrorEventCount)
	}
}
