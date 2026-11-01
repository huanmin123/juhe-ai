package gatewayresponse

// 深挖复审 R2 验证后回归修复批（DD2-A5 / DD2-A6）钉住测试：
//   - DD2-A5：拦截器 pending 越限放行不再整段绕过策略检查——越限时先对
//     pending 中已完整事件走清点循环（分类/拦截），仅放行剩余无边界尾段
//     （未完成事件本就不可解析，透传语义不变，内存上界不变）；
//   - DD2-A6：SSE 事件边界词表补齐 \n\r\n（数据行 LF 结尾 + 空行 CRLF 结尾，
//     WHATWG 合法形态；与 bridge_stream.IndexBridgeSseBoundary 词表取齐），
//     并逐字节钉住 \r\n\r\n 不被 \n\r\n 部分命中截断的优先序。

import (
	"bytes"
	"testing"
)

// ---- DD2-A5：越限放行先清点完整事件 ----

// TestDDA5OverflowChunkWithCompleteInterceptableEventStillIntercepted 钉住：
// 越限 chunk 内夹带的完整应拦事件先被策略拦截（修复前越限分支先于清点循环
// return，完整事件原样透传绕过响应检查策略）。
func TestDDA5OverflowChunkWithCompleteInterceptableEventStillIntercepted(t *testing.T) {
	interceptor := NewOpenAIStreamInterceptor(OpenAIStreamInterceptorOptions{
		Policies: []RuntimeResponseInspectionPolicy{interceptorMountPolicy},
		Context:  &ResponseInspectionRuntimeContext{ClientProfile: "codex"},
	})
	tail := bytes.Repeat([]byte("x"), openAIStreamInterceptorMaxPendingBytes+64)
	chunk := append(append([]byte(nil), contextWindowErrorChunk...), tail...)
	if len(chunk) <= openAIStreamInterceptorMaxPendingBytes {
		t.Fatalf("测试前置：chunk 必须越过持有上限 len=%d", len(chunk))
	}
	result := interceptor.PushChunk(chunk)
	if result.Intercepted == nil {
		t.Fatalf("越限 chunk 内的完整应拦事件必须被策略拦截，result = %+v", result)
	}
	if len(result.Chunks) != 0 {
		t.Fatalf("拦截时不得有下游字节写出，chunks = %d", len(result.Chunks))
	}
}

// TestDDA5OverflowBoundlessTailStillPassedThrough 钉住既有越限语义保持：无任
// 何边界的尾段照常整段放行（未完成事件不可解析，透传语义与内存上界不变）。
func TestDDA5OverflowBoundlessTailStillPassedThrough(t *testing.T) {
	interceptor := NewOpenAIStreamInterceptor(OpenAIStreamInterceptorOptions{
		Policies: []RuntimeResponseInspectionPolicy{interceptorMountPolicy},
		Context:  &ResponseInspectionRuntimeContext{ClientProfile: "codex"},
	})
	tail := bytes.Repeat([]byte("x"), openAIStreamInterceptorMaxPendingBytes+64)
	result := interceptor.PushChunk(tail)
	if result.Intercepted != nil {
		t.Fatalf("无边界尾段不得触发策略决策: %+v", result.Intercepted)
	}
	if len(result.Chunks) != 1 || !bytes.Equal(result.Chunks[0], tail) {
		t.Fatalf("无边界尾段必须原样放行，chunks = %d", len(result.Chunks))
	}
	if interceptor.PendingOverflowCount() != 1 {
		t.Fatalf("PendingOverflowCount = %d, want 1", interceptor.PendingOverflowCount())
	}
}

// TestDDA5OverflowPassthroughKeepsCompleteEventsBeforeTail 钉住混合形态：完
// 整非命中事件先透传，无边界尾段随后放行，输出字节与输入逐字节一致。
func TestDDA5OverflowPassthroughKeepsCompleteEventsBeforeTail(t *testing.T) {
	interceptor := NewOpenAIStreamInterceptor(OpenAIStreamInterceptorOptions{
		Policies: []RuntimeResponseInspectionPolicy{interceptorMountPolicy},
		Context:  &ResponseInspectionRuntimeContext{ClientProfile: "codex"},
	})
	normal := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	tail := bytes.Repeat([]byte("x"), openAIStreamInterceptorMaxPendingBytes+64)
	chunk := append(append([]byte(nil), normal...), tail...)
	result := interceptor.PushChunk(chunk)
	if result.Intercepted != nil {
		t.Fatalf("非命中完整事件不得拦截: %+v", result.Intercepted)
	}
	if len(result.Chunks) != 2 {
		t.Fatalf("chunks = %d, want 2（完整事件 + 无边界尾段）", len(result.Chunks))
	}
	if !bytes.Equal(result.Chunks[0], normal) || !bytes.Equal(result.Chunks[1], tail) {
		t.Fatal("透传字节必须与输入逐字节一致")
	}
}

// ---- DD2-A6：\n\r\n 边界词表 + 优先序逐字节回归 ----

func TestDDA6ShiftSseEventBoundaries(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string // 依次 shift 出的事件；未列出 = 无更多事件
	}{
		{"LF LF 回归", "a\n\nb\n\n", []string{"a\n\n", "b\n\n"}},
		{"CR CR 回归", "a\r\rb\r\r", []string{"a\r\r", "b\r\r"}},
		{"CRLF CRLF 回归（4 字节完整边界）", "a\r\n\r\nb\r\n\r\n", []string{"a\r\n\r\n", "b\r\n\r\n"}},
		{"LF + CRLF（新词表）", "a\n\r\nb\n\r\n", []string{"a\n\r\n", "b\n\r\n"}},
		{"CR CR 先于后续 LF CRLF", "a\r\r\n\r\n", []string{"a\r\r", "\n\r\n"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pending := []byte(tc.input)
			for wantIndex, want := range tc.want {
				got := shiftSseEvent(&pending)
				if got == nil {
					t.Fatalf("第 %d 个事件缺失，pending 残留 = %q", wantIndex+1, string(pending))
				}
				if string(got) != want {
					t.Fatalf("第 %d 个事件 = %q, want %q", wantIndex+1, string(got), want)
				}
			}
			if got := shiftSseEvent(&pending); got != nil {
				t.Fatalf("事件多于期望: %q", string(got))
			}
		})
	}
}

func TestDDA6ShiftSseEventIncompleteTailHeld(t *testing.T) {
	for _, input := range []string{"a\n", "a\n\r", "a\r\n", "a\r", ""} {
		pending := []byte(input)
		if got := shiftSseEvent(&pending); got != nil {
			t.Fatalf("input %q 不构成完整事件，got %q", input, string(got))
		}
	}
}

func TestDDA6SseEventEndBoundaries(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantEnd  int
		wantDone bool
	}{
		{"LF LF 回归", "a\n\nb", 3, true},
		{"CR CR 回归", "a\r\rb", 3, true},
		{"CRLF CRLF 回归（完整 4 字节）", "a\r\n\r\nb", 5, true},
		{"LF + CRLF（新词表）", "a\n\r\nb", 4, true},
		{"不完整 LF CR", "a\n\r", 3, false},
		{"无边界", "ab", 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			end, complete := sseEventEnd([]byte(tc.input), 0)
			if end != tc.wantEnd || complete != tc.wantDone {
				t.Fatalf("sseEventEnd(%q) = (%d, %v), want (%d, %v)", tc.input, end, complete, tc.wantEnd, tc.wantDone)
			}
		})
	}
}

// TestDDA6SseEventEndCrLfNotTruncatedByLfCrLf 逐字节钉住优先序："data: x" 7
// 字节后接 \r\n\r\n，边界终点是第 11 字节（4 字节完整边界），不得被 \n\r\n
// 部分命中截成第 10 字节。
func TestDDA6SseEventEndCrLfNotTruncatedByLfCrLf(t *testing.T) {
	end, complete := sseEventEnd([]byte("data: x\r\n\r\nrest"), 0)
	if end != 11 || !complete {
		t.Fatalf("sseEventEnd = (%d, %v), want (11, true)——\\r\\n\\r\\n 必须整体命中", end, complete)
	}
}

// TestDDA6UsageOnlyFrameWithLfCrLfBoundaryIsStripped 端到端钉 sseEventEnd 的
// 消费面：usage-only 帧以 \n\r\n 结尾时同样被剥除（修复前滞留为未完帧）。
func TestDDA6UsageOnlyFrameWithLfCrLfBoundaryIsStripped(t *testing.T) {
	chunk := "data: {\"choices\":[],\"usage\":{\"total_tokens\":5}}\n\r\n"
	filtered, dropped := filterGatewayInjectedUsageOnlySseFrames([]byte(chunk))
	if !dropped {
		t.Fatal("\\n\\r\\n 结尾的 usage-only 帧必须被识别并剥除")
	}
	if len(filtered) != 0 {
		t.Fatalf("filtered = %q, want 空", string(filtered))
	}
	// 普通数据帧（同边界形态）不受影响（dropped=false 时调用方沿用原始字节）。
	normal := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\r\n"
	if _, dropped := filterGatewayInjectedUsageOnlySseFrames([]byte(normal)); dropped {
		t.Fatal("普通帧必须保留，不得剥除")
	}
}
