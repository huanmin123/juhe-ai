package gatewayopenai

import (
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// MarkDownstreamWrite：仅在 buffer 参与巡检时记录下游已写。
func TestWABufferMarkDownstreamWrite(t *testing.T) {
	engaged := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{
		ClientRetryEnabled: true,
	})
	if !engaged.Engaged() {
		t.Fatal("重试开启应参与巡检")
	}
	engaged.MarkDownstreamWrite()
	// 已写下游后，可延迟的 noop 事件不再延迟（直接透传）。
	noop := []byte("data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
	result := engaged.PushChunk(noop)
	if len(result.Chunks) != 1 || string(result.Chunks[0]) != string(noop) {
		t.Fatalf("已写下游后 noop 不再延迟: %+v", result.Chunks)
	}

	disengaged := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{})
	disengaged.MarkDownstreamWrite() // 不参与巡检时无副作用（不 panic）
	if disengaged.Engaged() {
		t.Fatal("无策略无重试不应参与巡检")
	}
}

// FlushPendingOnEOF：EOF 时冲刷未完结事件并保证 SSE 事件边界。
func TestWABufferFlushPendingOnEOF(t *testing.T) {
	t.Run("不参与巡检", func(t *testing.T) {
		buffer := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{})
		if result := buffer.FlushPendingOnEOF(); len(result.Chunks) != 0 || result.PendingEvent {
			t.Fatalf("不参与巡检 EOF = %+v", result)
		}
	})
	t.Run("无边界 pending 补边界后巡检透传", func(t *testing.T) {
		var policyCalls int
		buffer := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{
			Policies:                            []InspectionPolicy{countingPolicy(&policyCalls)},
			RequiresVisibleOutputTextInspection: []bool{false},
		})
		// 纯文本 delta 快路径不匹配（非 common 前缀），走完整解析。
		buffer.PushChunk([]byte("data: {\"error\":{\"code\":\"pending\"}}"))
		result := buffer.FlushPendingOnEOF()
		if !result.ParserSkipped && len(result.Chunks) != 1 {
			t.Fatalf("EOF 冲刷应输出补边界事件: %+v", result)
		}
		if policyCalls != 1 {
			t.Fatalf("EOF 事件应进策略: %d", policyCalls)
		}
		if !strings.Contains(string(result.Chunks[0]), "pending") {
			t.Fatalf("透传内容 = %q", result.Chunks[0])
		}
	})
	t.Run("skip 后 EOF 只冲刷延迟 chunk", func(t *testing.T) {
		buffer := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{
			ClientRetryEnabled: true,
		})
		buffer.PushChunk([]byte(strings.Repeat("x", maxBufferedSseEventBytes+1)))
		result := buffer.FlushPendingOnEOF()
		if !result.ParserSkipped {
			t.Fatalf("超限后应 ParserSkipped: %+v", result)
		}
	})
}

// 巡检缓冲与 chunk 边界无关：跨 chunk 的 SSE 边界（LF/CRLF/CR）均可切事件。
func TestWABufferCrossChunkBoundaries(t *testing.T) {
	cases := []struct {
		name          string
		first, second string
	}{
		{"LF 边界跨 chunk", "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n", "\n"},
		{"CRLF 边界跨 chunk", "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\r\n", "\r\n"},
		{"CR 边界跨 chunk", "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\r", "\r"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			buffer := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{
				Policies: []InspectionPolicy{func(event ParsedStreamEvent, _ []gatewayproto.SemanticFrame) *InspectionDecision {
					calls++
					return nil
				}},
			})
			first := buffer.PushChunk([]byte(tc.first))
			if len(first.Chunks) != 0 {
				t.Fatalf("事件未完结不应输出: %+v", first.Chunks)
			}
			second := buffer.PushChunk([]byte(tc.second))
			if calls != 1 {
				t.Fatalf("跨 chunk 边界应切出 1 个事件: %d", calls)
			}
			if len(second.Chunks) != 1 || !strings.Contains(string(second.Chunks[0]), "delta") {
				t.Fatalf("事件应透传: %+v", second.Chunks)
			}
		})
	}
}

// 可延迟的行首 noop 事件：在首个实质事件前延迟，随后按序冲刷。
func TestWABufferDeferrableLeadingNoop(t *testing.T) {
	calls := 0
	buffer := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{
		Policies: []InspectionPolicy{func(event ParsedStreamEvent, _ []gatewayproto.SemanticFrame) *InspectionDecision {
			calls++
			return nil
		}},
	})
	noop := []byte("data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
	noop2 := []byte("data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":null}}]}\n\n")
	real := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"实\"}}]}\n\n")
	if result := buffer.PushChunk(noop); len(result.Chunks) != 0 {
		t.Fatalf("noop 应延迟: %+v", result.Chunks)
	}
	if result := buffer.PushChunk(noop2); len(result.Chunks) != 0 {
		t.Fatalf("noop2 应延迟: %+v", result.Chunks)
	}
	result := buffer.PushChunk(real)
	if len(result.Chunks) != 3 {
		t.Fatalf("实质事件应按序冲刷两个延迟 chunk: %d", len(result.Chunks))
	}
	if !strings.Contains(string(result.Chunks[0]), "role") || !strings.Contains(string(result.Chunks[2]), "实") {
		t.Fatalf("冲刷顺序错误: %q", result.Chunks)
	}
	if calls != 1 {
		t.Fatalf("仅实质事件进策略: %d", calls)
	}
	// 非 noop 事件形态不延迟：带 finish_reason / text / error / usage。
	nonNoop := [][]byte{
		[]byte("data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"),
		[]byte("data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"text\":\"t\"}]}\n\n"),
		[]byte("data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"message\":{}}]}\n\n"),
		[]byte("data: {\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{}}\n\n"),
		[]byte("data: {\"object\":\"other\",\"choices\":[]}\n\n"),
	}
	for index, chunk := range nonNoop {
		result := buffer.PushChunk(chunk)
		if len(result.Chunks) != 1 {
			t.Fatalf("非 noop 用例 %d 应立即透传: %+v", index, result.Chunks)
		}
	}
}

// 拦截语义：拦截时丢弃已延迟 noop，未写下游时只输出失败事件。
func TestWABufferInterceptClearsDeferred(t *testing.T) {
	buffer := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{
		Policies: []InspectionPolicy{func(event ParsedStreamEvent, _ []gatewayproto.SemanticFrame) *InspectionDecision {
			if event.Data != nil {
				if _, has := event.Data["error"]; has {
					return &InspectionDecision{Action: DecisionIntercept, ErrorCode: "blocked", Message: "拦截"}
				}
			}
			return nil
		}},
	})
	buffer.PushChunk([]byte("data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"role\":\"a\"}}]}\n\n"))
	result := buffer.PushChunk([]byte("data: {\"error\":{\"code\":\"x\"}}\n\n"))
	if result.Intercepted == nil || result.Intercepted.ErrorCode != "blocked" {
		t.Fatalf("拦截结果 = %+v", result.Intercepted)
	}
	joined := string(joinChunks(result.Chunks))
	if strings.Contains(joined, "role") {
		t.Fatalf("延迟 noop 应被丢弃: %q", joined)
	}
	if !strings.Contains(joined, "response.failed") || !strings.Contains(joined, "blocked") {
		t.Fatalf("应注入失败事件: %q", joined)
	}
}

// discard_event：事件静默丢弃，buffer 保持工作。
func TestWABufferDiscardEvent(t *testing.T) {
	buffer := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{
		Policies: []InspectionPolicy{func(event ParsedStreamEvent, _ []gatewayproto.SemanticFrame) *InspectionDecision {
			if event.Data != nil {
				if _, has := event.Data["error"]; has {
					return &InspectionDecision{Action: DecisionDiscardEvent}
				}
			}
			return nil
		}},
	})
	dropped := buffer.PushChunk([]byte("data: {\"error\":{\"code\":\"x\"}}\n\n"))
	if dropped.Intercepted != nil || len(dropped.Chunks) != 0 {
		t.Fatalf("discard 事件 = %+v", dropped)
	}
	kept := buffer.PushChunk([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"k\"}}]}\n\n"))
	if len(kept.Chunks) != 1 {
		t.Fatalf("后续事件应透传: %+v", kept.Chunks)
	}
}

// dry_run：决策生效但不注入失败事件。
func TestWABufferDryRun(t *testing.T) {
	buffer := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{
		ClientRetryEnabled: true,
		Policies: []InspectionPolicy{func(event ParsedStreamEvent, _ []gatewayproto.SemanticFrame) *InspectionDecision {
			return &InspectionDecision{Action: DecisionDryRun, ErrorCode: "dry", Message: "dry"}
		}},
	})
	result := buffer.PushChunk([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"))
	if result.Intercepted == nil || result.Intercepted.Action != DecisionDryRun {
		t.Fatalf("dry run 结果 = %+v", result.Intercepted)
	}
	if len(result.Chunks) != 0 {
		t.Fatalf("dry run 不应输出事件: %+v", result.Chunks)
	}
}

// failureEventForDecision：dry/discard 不生成事件；intercept 生成标准
// response.failed 事件；缺省 code 兜底。
func TestWAFailureEventForDecision(t *testing.T) {
	if failureEventForDecision(InspectionDecision{Action: DecisionDiscardEvent}, false) != nil {
		t.Fatal("discard 不生成事件")
	}
	if failureEventForDecision(InspectionDecision{Action: DecisionDryRun}, false) != nil {
		t.Fatal("dry run 不生成事件")
	}
	event := failureEventForDecision(InspectionDecision{Action: DecisionIntercept, ErrorCode: "e1", Message: "m1"}, false)
	if !strings.Contains(string(event), `"code":"e1"`) || !strings.Contains(string(event), "m1") || !strings.Contains(string(event), "event: response.failed") {
		t.Fatalf("失败事件 = %q", event)
	}
	defaultEvent := failureEventForDecision(InspectionDecision{Action: DecisionIntercept}, false)
	if !strings.Contains(string(defaultEvent), "upstream_stream_interrupted") {
		t.Fatalf("缺省 code = %q", defaultEvent)
	}
}

// 纯文本 delta 快路径：common 前缀 + 简单转义判定 + 三种行尾风格。
func TestWABufferCommonResponsesTextDeltaFastPath(t *testing.T) {
	calls := 0
	buffer := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{
		Policies:                            []InspectionPolicy{countingPolicy(&calls)},
		RequiresVisibleOutputTextInspection: []bool{false},
	})
	build := func(deltaBody, suffix string) []byte {
		return []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"" + deltaBody + "\"}" + suffix)
	}
	cases := []struct {
		name string
		body string
	}{
		{"中文文本", "你好世界"},
		{"空格与控制符内不含引号", "plain text"},
	}
	for _, tc := range cases {
		for _, suffix := range []string{"\n\n", "\r\n\r\n", "\r\r"} {
			result := buffer.PushChunk(build(tc.body, suffix))
			if calls != 0 {
				t.Fatalf("快路径不应进策略（%s/%q）: %d", tc.name, suffix, calls)
			}
			if len(result.Chunks) != 1 {
				t.Fatalf("快路径应透传（%s/%q）: %+v", tc.name, suffix, result.Chunks)
			}
		}
	}
	// delta 含引号 → 非快路径，进策略。
	result := buffer.PushChunk(build("a\"b", "\n\n"))
	if calls != 1 {
		t.Fatalf("含引号 delta 应进策略: %d", calls)
	}
	if len(result.Chunks) != 1 {
		t.Fatalf("含引号 delta 透传: %+v", result.Chunks)
	}
	// 非法 JSON 负载进 DataParseError 分支后仍透传。
	broken := buffer.PushChunk([]byte("data: {broken}\n\n"))
	if len(broken.Chunks) != 1 {
		t.Fatalf("解析失败事件应透传: %+v", broken.Chunks)
	}
}

// 快路径关闭时，chat 纯文本选择事件走 uninspectable 直通；带元数据键的事件
// 不直通（回归 isSafeVisibleOutputOnlyRoot / isVisibleOutputOnlyChatCompletionChoice）。
func TestWABufferUninspectableVisibleOutputVariants(t *testing.T) {
	calls := 0
	newBuffer := func() *ResponseInspectionBuffer {
		calls = 0
		return NewResponseInspectionBuffer(ResponseInspectionBufferOptions{
			Policies:                            []InspectionPolicy{countingPolicy(&calls)},
			RequiresVisibleOutputTextInspection: []bool{false},
		})
	}
	t.Run("event:message + choices 纯文本直通", func(t *testing.T) {
		buffer := newBuffer()
		result := buffer.PushChunk([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		if calls != 0 {
			t.Fatalf("纯文本 chat 事件应直通: %d", calls)
		}
		if len(result.Chunks) != 1 {
			t.Fatalf("直通输出: %+v", result.Chunks)
		}
	})
	t.Run("finish_reason 存在则不直通", func(t *testing.T) {
		buffer := newBuffer()
		buffer.PushChunk([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n"))
		if calls != 1 {
			t.Fatalf("带 finish_reason 应进策略: %d", calls)
		}
	})
	t.Run("refusal 计为可见输出", func(t *testing.T) {
		buffer := newBuffer()
		result := buffer.PushChunk([]byte("data: {\"choices\":[{\"delta\":{\"refusal\":\"拒绝\"}}]}\n\n"))
		if calls != 0 || len(result.Chunks) != 1 {
			t.Fatalf("refusal 应直通: %d/%+v", calls, result.Chunks)
		}
	})
	t.Run("message 类型非字符串不直通", func(t *testing.T) {
		buffer := newBuffer()
		buffer.PushChunk([]byte("data: {\"choices\":[{\"delta\":{\"content\":null}}]}\n\n"))
		if calls != 1 {
			t.Fatalf("delta.content=null 应进策略: %d", calls)
		}
	})
}

// isNoopChatCompletionChoice 边界（经 isDeferrableLeadingChatCompletionNoopEvent 间接验证）。
func TestWAIsDeferrableNoopEdgeCases(t *testing.T) {
	parse := func(data string) ParsedStreamEvent {
		return ParseSseEventText("data: " + data + "\n\n")
	}
	cases := []struct {
		name string
		data string
		want bool
	}{
		{"role-only delta", `{"object":"chat.completion.chunk","choices":[{"delta":{"role":"assistant"}}]}`, true},
		{"空 content delta", `{"object":"chat.completion.chunk","choices":[{"delta":{"role":"a","content":""}}]}`, true},
		{"delta 非对象", `{"object":"chat.completion.chunk","choices":[{"delta":"x"}]}`, false},
		{"无 choices", `{"object":"chat.completion.chunk","choices":[]}`, false},
		{"无 object 字段", `{"choices":[{"delta":{"role":"a"}}]}`, false},
		{"data 解析失败", `{broken`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := parse(tc.data)
			if got := isDeferrableLeadingChatCompletionNoopEvent(event); got != tc.want {
				t.Fatalf("isDeferrable = %v，期望 %v", got, tc.want)
			}
		})
	}
}

// pendingSseEventBuffer：跨 chunk 部分消费与 CR/CRLF 边界（consumePrefix 细分路径）。
func TestWAPendingBufferConsumePrefix(t *testing.T) {
	buffer := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{ClientRetryEnabled: true})
	// 一个事件被拆成多个小块（首块小于边界位置）。
	chunk1 := "data: {\"a\":"
	chunk2 := "1}\n"
	chunk3 := "\n"
	if result := buffer.PushChunk([]byte(chunk1)); len(result.Chunks) != 0 {
		t.Fatalf("未完结不输出: %+v", result.Chunks)
	}
	if result := buffer.PushChunk([]byte(chunk2)); len(result.Chunks) != 0 {
		t.Fatalf("边界未到不输出: %+v", result.Chunks)
	}
	result := buffer.PushChunk([]byte(chunk3))
	if len(result.Chunks) != 1 || string(result.Chunks[0]) != chunk1+chunk2+chunk3 {
		t.Fatalf("跨 chunk 事件应完整透传: %+v", result.Chunks)
	}
	// 尾部无边界：EOF 补 \n\n。
	buffer.PushChunk([]byte("data: {\"b\":2}"))
	result = buffer.FlushPendingOnEOF()
	if len(result.Chunks) != 1 || !strings.HasSuffix(string(result.Chunks[0]), "}\n\n") {
		t.Fatalf("EOF 补边界 = %+v", result.Chunks)
	}
}

// extractStreamEventTypeFromJSONPrefix / hasImageStreamPayloadHint 经 oversized
// 路径验证：单行 data 超过 256KB 行限、类型可从前缀识别时按 oversized 图片
// 事件分类而非整流跳过。
func TestWAOpenAIOversizedImageEvent(t *testing.T) {
	inspector := NewStreamInspector()
	pad := strings.Repeat("a", 300*1024)
	inspector.PushText("data: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"" + pad + "\"}\n\n")
	snapshot := inspector.Finish()
	if snapshot.Skipped {
		t.Fatalf("带类型的 oversized 事件不应整流跳过: %+v", snapshot)
	}
	if !snapshot.ImageOutputReceived || !snapshot.OutputReceived {
		t.Fatalf("图片 oversized 分类 = %+v", snapshot)
	}
	if snapshot.EventCount != 1 {
		t.Fatalf("事件数 = %d", snapshot.EventCount)
	}
	summary := inspector.DrainEventSummaries()
	if len(summary) != 1 || !summary[0].ParseError || !summary[0].Output {
		t.Fatalf("图片 oversized summary = %+v", summary)
	}
}

func joinChunks(chunks [][]byte) []byte {
	var out []byte
	for _, chunk := range chunks {
		out = append(out, chunk...)
	}
	return out
}
