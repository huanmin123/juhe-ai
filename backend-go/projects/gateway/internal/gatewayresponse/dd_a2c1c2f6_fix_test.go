package gatewayresponse

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// 深挖复审修复批（DD-C1 / DD-C2 / DD-F6）钉住测试：
//   - DD-C1：SSE 分帧 CRLF 空行边界（\r\n\r\n）识别，LF 流逐字节回归；
//   - DD-C2：拦截器 pending 无边界字节流的持有上限与整段放行；
//   - DD-F6：多值响应头转发不再被 Set 覆盖。

// ---- DD-C1：shiftSseEvent 边界词表 ----

func TestDDC1ShiftSseEventBoundaryForms(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		events    []string
		remaining string
	}{
		{"LF 既有语义", "data: a\n\ntail", []string{"data: a\n\n"}, "tail"},
		{"CR 既有语义", "data: a\r\rtail", []string{"data: a\r\r"}, "tail"},
		{"CRLF 空行（修复目标）", "data: a\r\n\r\ntail", []string{"data: a\r\n\r\n"}, "tail"},
		{"CRLF 多事件", "data: 1\r\n\r\ndata: 2\r\n\r\n", []string{"data: 1\r\n\r\n", "data: 2\r\n\r\n"}, ""},
		{"事件行尾 CRLF 不触发（无空行）", "data: a\r\ndata: b\r\n", nil, "data: a\r\ndata: b\r\n"},
		{"跨 chunk 不完整边界等待", "data: a\r\n\r", nil, "data: a\r\n\r"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pending := []byte(tc.input)
			var got [][]byte
			for {
				event := shiftSseEvent(&pending)
				if event == nil {
					break
				}
				got = append(got, event)
			}
			if len(got) != len(tc.events) {
				t.Fatalf("events = %q, want %q", got, tc.events)
			}
			for index := range got {
				if string(got[index]) != tc.events[index] {
					t.Fatalf("event[%d] = %q, want %q", index, got[index], tc.events[index])
				}
			}
			if string(pending) != tc.remaining {
				t.Fatalf("remaining = %q, want %q", pending, tc.remaining)
			}
		})
	}
}

// TestDDC1InterceptorCRLFStream 钉住全 CRLF SSE 流经拦截器逐事件透传（修复前
// 整流粘连到 EOF，chunks=0）；LF 流事件结构一致，两种行尾的透传字节均逐字节
// 等于输入（不重写）。
func TestDDC1InterceptorCRLFStream(t *testing.T) {
	build := func(lineEnding string) string {
		return "data: {\"a\":1}" + lineEnding + lineEnding +
			"data: {\"b\":2}" + lineEnding + lineEnding +
			"data: [DONE]" + lineEnding + lineEnding
	}
	lf := NewOpenAIStreamInterceptor(OpenAIStreamInterceptorOptions{})
	lfResult := lf.PushChunk([]byte(build("\n")))
	lfResult = mergeInterceptorResults(lfResult, lf.FlushPendingOnEOF())

	crlf := NewOpenAIStreamInterceptor(OpenAIStreamInterceptorOptions{})
	crlfResult := crlf.PushChunk([]byte(build("\r\n")))
	crlfResult = mergeInterceptorResults(crlfResult, crlf.FlushPendingOnEOF())

	if len(crlfResult.Chunks) != len(lfResult.Chunks) {
		t.Fatalf("CRLF chunks = %d, LF chunks = %d", len(crlfResult.Chunks), len(lfResult.Chunks))
	}
	if crlfResult.PendingEvent {
		t.Fatal("CRLF 流不应留下未分帧 pending")
	}
	// 逐字节回归：两种行尾的透传字节拼接都与各自输入完全一致（不重写行尾）。
	if joined := strings.Join(chunkStrings(crlfResult.Chunks), ""); joined != build("\r\n") {
		t.Fatalf("CRLF 透传字节被改写: %q", joined)
	}
	if joined := strings.Join(chunkStrings(lfResult.Chunks), ""); joined != build("\n") {
		t.Fatalf("LF 透传字节被改写: %q", joined)
	}
	// 事件边界粒度：CRLF 流的每个 chunk 恰是一个完整事件（含边界）。
	if len(crlfResult.Chunks) > 0 && !strings.HasSuffix(chunkStrings(crlfResult.Chunks)[0], "\r\n\r\n") {
		t.Fatalf("CRLF 首事件未以空行边界结束: %q", crlfResult.Chunks[0])
	}
}

func chunkStrings(chunks [][]byte) []string {
	out := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		out = append(out, string(chunk))
	}
	return out
}

// ---- DD-C2：pending 持有上限 ----

// TestDDC2PendingOverflowRelease 钉住无边界字节流越过上限时整段放行、内存
// 恢复有界、计数可见；后续正常事件照常分帧。
func TestDDC2PendingOverflowRelease(t *testing.T) {
	interceptor := NewOpenAIStreamInterceptor(OpenAIStreamInterceptorOptions{})
	// 无边界垃圾字节（无任何空行）超过 256KB 上限。
	unbounded := strings.Repeat("x", openAIStreamInterceptorMaxPendingBytes+128)
	result := interceptor.PushChunk([]byte(unbounded))
	if len(result.Chunks) != 1 || len(result.Chunks[0]) != len(unbounded) {
		t.Fatalf("越限未整段放行: chunks=%d", len(result.Chunks))
	}
	if interceptor.PendingOverflowCount() != 1 {
		t.Fatalf("越限计数 = %d, want 1", interceptor.PendingOverflowCount())
	}
	if len(interceptor.pending) != 0 {
		t.Fatalf("放行后 pending 未清空: %d", len(interceptor.pending))
	}
	// 后续正常事件照常分帧：透传事件字节不变。
	follow := interceptor.PushChunk([]byte("data: {\"ok\":true}\n\n"))
	if len(follow.Chunks) != 1 || string(follow.Chunks[0]) != "data: {\"ok\":true}\n\n" {
		t.Fatalf("放行后正常事件透传异常: %q", follow.Chunks)
	}
	// 有界内的无边界字节不放行（pending 正常持有，等 EOF flush）。
	small := NewOpenAIStreamInterceptor(OpenAIStreamInterceptorOptions{})
	smallResult := small.PushChunk([]byte("no-boundary-bytes"))
	if len(smallResult.Chunks) != 0 || small.PendingOverflowCount() != 0 {
		t.Fatalf("有界内不应触发越限放行: chunks=%d count=%d", len(smallResult.Chunks), small.PendingOverflowCount())
	}
	flushed := small.FlushPendingOnEOF()
	if len(flushed.Chunks) != 1 || string(flushed.Chunks[0]) != "no-boundary-bytes" {
		t.Fatalf("EOF flush 语义变化: %q", flushed.Chunks)
	}
}

// ---- DD-F6：多值响应头 ----

func TestDDF6MultiValueResponseHeaderForwarded(t *testing.T) {
	// 经真实 http.Header 断言：多值头两值都保留，单值头行为不变。
	header := http.Header{}
	header["X-Multi"] = []string{"first", "second"}
	header["X-Single"] = []string{"only"}
	header["Content-Type"] = []string{"application/json"}
	header["X-Lower-Multi"] = []string{"a", "b"}
	upstream := &GatewayUpstreamResponse{Status: 200, Header: header}
	recorder := httptest.NewRecorder()
	downstream := StreamDownstream{Res: gatewaypreauth.NewTrackingWriter(recorder)}
	prepareUpstreamResponseForDownstream(downstream, upstream, false)

	downstreamHeader := recorder.Header()
	multi := downstreamHeader.Values("X-Multi")
	if len(multi) != 2 || multi[0] != "first" || multi[1] != "second" {
		t.Fatalf("多值头 X-Multi = %q, want [first second]", multi)
	}
	lower := downstreamHeader.Values("X-Lower-Multi")
	if len(lower) != 2 || lower[0] != "a" || lower[1] != "b" {
		t.Fatalf("多值头 X-Lower-Multi = %q, want [a b]", lower)
	}
	if single := downstreamHeader.Values("X-Single"); len(single) != 1 || single[0] != "only" {
		t.Fatalf("单值头 X-Single = %q, want [only]", single)
	}
	if got := downstreamHeader.Values("Content-Type"); len(got) != 1 || got[0] != "application/json" {
		t.Fatalf("单值头 Content-Type = %q, want [application/json]", got)
	}
}
