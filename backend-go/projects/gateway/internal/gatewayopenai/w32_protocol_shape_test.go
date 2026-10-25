package gatewayopenai

import (
	"testing"
)

// 流式提交边界受限重放与异常终局处置设计 §3.2.0 L2（v2.3 增量，2026-10-26）：
// IsProtocolInvalidChunkFrame 谓词语义锁定——顶层存在 `choices` 键且 JSON 值
// 不是数组即协议形状异常帧；`choices:[]`（数组）放行；无 `choices` 键放行；
// 非法 JSON / [DONE] 不适用（DataParseError 分类管辖）。inspector 按同一谓词
// 累加 InvalidProtocolEventCount。

func TestW32IsProtocolInvalidChunkFrameSemantics(t *testing.T) {
	cases := []struct {
		name     string
		dataText string
		invalid  bool
	}{
		{name: "生产空壳流首帧 choices:null", dataText: `{"id":"","object":"","created":0,"model":"","system_fingerprint":null,"choices":null,"usage":null}`, invalid: true},
		{name: "choices 字符串", dataText: `{"choices":"x"}`, invalid: true},
		{name: "choices 对象", dataText: `{"choices":{}}`, invalid: true},
		{name: "choices 数字", dataText: `{"choices":3}`, invalid: true},
		{name: "choices 空数组放行", dataText: `{"id":"","object":"chat.completion.chunk","choices":[]}`, invalid: false},
		{name: "正常 chunk 放行", dataText: `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"}}]}`, invalid: false},
		{name: "无 choices 键放行（responses 形态）", dataText: `{"type":"response.completed","response":{"output":[]}}`, invalid: false},
		{name: "无 choices 键放行（桥接转换后形态）", dataText: `{"error":{"code":"e"}}`, invalid: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			event := ParseStreamEventData(testCase.dataText, "", "", len(testCase.dataText))
			if event.DataParseError {
				t.Fatalf("夹具应可解析: %s", testCase.dataText)
			}
			if got := IsProtocolInvalidChunkFrame(event.Data); got != testCase.invalid {
				t.Fatalf("IsProtocolInvalidChunkFrame = %v, want %v (%s)", got, testCase.invalid, testCase.dataText)
			}
		})
	}
	// 非法 JSON / 空载荷不适用：谓词入参为 nil（DataParseError 走既有分类）。
	if IsProtocolInvalidChunkFrame(nil) {
		t.Fatal("nil 载荷不应命中谓词")
	}
}

func TestW32InspectorCountsInvalidProtocolEvents(t *testing.T) {
	inspector := NewStreamInspector()
	inspector.PushChunk([]byte("data: {\"choices\":null}\n\n"))                               // 异常 +1
	inspector.PushChunk([]byte("data: {\"choices\":[]}\n\n"))                                 // 放行
	inspector.PushChunk([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")) // 正常输出
	inspector.PushChunk([]byte("data: [DONE]\n\n"))
	inspection := inspector.Finish()
	if inspection.InvalidProtocolEventCount != 1 {
		t.Fatalf("InvalidProtocolEventCount = %d, want 1", inspection.InvalidProtocolEventCount)
	}
	if inspection.ParseErrorEventCount != 0 {
		t.Fatalf("ParseErrorEventCount = %d, want 0（形状异常帧解析成功）", inspection.ParseErrorEventCount)
	}
	if !inspection.TerminalReceived || !inspection.OutputReceived {
		t.Fatalf("终态与输出计数不应受 L2 影响: %+v", inspection)
	}
}
