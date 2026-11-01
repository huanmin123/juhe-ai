package gatewayopenai

// DD2-A6 钉住测试：pendingSseEventBuffer 的 SSE 边界词表补齐 \n\r\n（数据行
// LF 结尾 + 空行 CRLF 结尾，WHATWG 合法形态；与 bridge_stream 的
// IndexBridgeSseBoundary 词表取齐），并逐字节钉住 \r\n\r\n（4 字节完整边界）
// 不被 \n\r\n 部分命中截断的优先序、endsWithBoundary 对 \n\r\n 结尾的识别
// （drainEnsuringBoundary 不得重复补 \n\n）。

import (
	"bytes"
	"testing"
)

func TestDDA6BufferShiftEventLfCrLfBoundary(t *testing.T) {
	buffer := newPendingSseEventBuffer()
	buffer.push([]byte("data: {\"a\":1}\n\r\n"))
	buffer.push([]byte("data: {\"b\":2}\n\r\n"))
	first := buffer.shiftEvent()
	if first == nil || string(first) != "data: {\"a\":1}\n\r\n" {
		t.Fatalf("first event = %q, want \\n\\r\\n 结尾的完整事件", string(first))
	}
	second := buffer.shiftEvent()
	if second == nil || string(second) != "data: {\"b\":2}\n\r\n" {
		t.Fatalf("second event = %q, want \\n\\r\\n 结尾的完整事件", string(second))
	}
	if buffer.shiftEvent() != nil {
		t.Fatal("事件多于期望")
	}
}

func TestDDA6BufferShiftEventBoundaryPriority(t *testing.T) {
	cases := []struct {
		name  string
		chunk string
		want  []string
	}{
		{"LF LF 回归", "a\n\nb\n\n", []string{"a\n\n", "b\n\n"}},
		{"CR CR 回归", "a\r\rb\r\r", []string{"a\r\r", "b\r\r"}},
		{"CRLF CRLF 完整 4 字节边界不被截断", "a\r\n\r\nb\r\n\r\n", []string{"a\r\n\r\n", "b\r\n\r\n"}},
		{"LF + CRLF 新词表", "a\n\r\nb\n\r\n", []string{"a\n\r\n", "b\n\r\n"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buffer := newPendingSseEventBuffer()
			buffer.push([]byte(tc.chunk))
			for wantIndex, want := range tc.want {
				got := buffer.shiftEvent()
				if got == nil {
					t.Fatalf("第 %d 个事件缺失", wantIndex+1)
				}
				if string(got) != want {
					t.Fatalf("第 %d 个事件 = %q, want %q", wantIndex+1, string(got), want)
				}
			}
			if buffer.shiftEvent() != nil {
				t.Fatal("事件多于期望")
			}
		})
	}
}

// TestDDA6BufferShiftEventAcrossChunks 钉住跨 chunk 的 \n\r\n 边界：LF 在上
// 一 chunk 尾、\r\n 在下一 chunk 头时同样识别（cross-chunk 词表）。
func TestDDA6BufferShiftEventAcrossChunks(t *testing.T) {
	buffer := newPendingSseEventBuffer()
	buffer.push([]byte("data: {\"a\":1}\n"))
	buffer.push([]byte("\r\ndata: {\"b\":2}\n\n"))
	first := buffer.shiftEvent()
	if first == nil || !bytes.Equal(first, []byte("data: {\"a\":1}\n\r\n")) {
		t.Fatalf("first event = %q, want 跨 chunk \\n\\r\\n 完整事件", string(first))
	}
	if second := buffer.shiftEvent(); second == nil || string(second) != "data: {\"b\":2}\n\n" {
		t.Fatalf("second event = %q", string(second))
	}
}

// TestDDA6BufferDrainEnsuringBoundaryRecognizesLfCrLf 钉住 endsWithBoundary：
// 以 \n\r\n 结尾的缓冲已是完整边界，drainEnsuringBoundary 不得再补 \n\n。
func TestDDA6BufferDrainEnsuringBoundaryRecognizesLfCrLf(t *testing.T) {
	buffer := newPendingSseEventBuffer()
	buffer.push([]byte("data: {\"a\":1}\n\r\n"))
	drained := buffer.drainEnsuringBoundary()
	if drained == nil || bytes.HasSuffix(drained, []byte("\n\n\n")) || bytes.HasSuffix(drained, []byte("\r\n\n")) {
		t.Fatalf("drainEnsuringBoundary = %q, want 原样返回不补边界", string(drained))
	}
	// 无边界结尾仍补 \n\n（既有语义回归）。
	incomplete := newPendingSseEventBuffer()
	incomplete.push([]byte("data: {\"a\":1}\n"))
	drained = incomplete.drainEnsuringBoundary()
	if drained == nil || !bytes.HasSuffix(drained, []byte("\n\n")) {
		t.Fatalf("无边界结尾的 drainEnsuringBoundary = %q, want 补 \\n\\n", string(drained))
	}
}
