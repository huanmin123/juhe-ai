package gatewayopenai

// w9f 覆盖收尾：pendingSseEventBuffer 全生命周期与 SSE 判定纯函数。

import (
	"bytes"
	"testing"
)

func TestW9FPendingSseEventBufferLifecycle(t *testing.T) {
	buffer := newPendingSseEventBuffer()
	if buffer.length() != 0 || buffer.shiftEvent() != nil {
		t.Fatal("empty buffer")
	}
	if drained := buffer.drain(); drained != nil {
		t.Fatalf("empty drain = %v", drained)
	}
	if drained := buffer.drainEnsuringBoundary(); drained != nil {
		t.Fatalf("empty boundary drain = %v", drained)
	}
	buffer.push(nil)
	if buffer.length() != 0 {
		t.Fatal("nil push is a no-op")
	}
	// 跨 chunk 的 LF 边界。
	buffer.push([]byte("event: a\ndata: {\"x\":1}"))
	buffer.push([]byte("\n\nevent: b\ndata: {}\n\n"))
	if buffer.length() == 0 {
		t.Fatal("size after push")
	}
	first := buffer.shiftEvent()
	if first == nil || !bytes.HasSuffix(first, []byte("\n\n")) {
		t.Fatalf("first event = %q", first)
	}
	second := buffer.shiftEvent()
	if second == nil || !bytes.Contains(second, []byte("event: b")) {
		t.Fatalf("second event = %q", second)
	}
	if buffer.shiftEvent() != nil {
		t.Fatal("no third event")
	}
	// CRLF 边界。
	crlf := newPendingSseEventBuffer()
	crlf.push([]byte("data: one\r\n\r\ndata: two\r\n\r\n"))
	if event := crlf.shiftEvent(); event == nil || !bytes.HasSuffix(event, []byte("\r\n\r\n")) {
		t.Fatalf("crlf event = %q", event)
	}
	// CR-only 边界。
	cr := newPendingSseEventBuffer()
	cr.push([]byte("data: x\r\rdata: y\r\r"))
	if event := cr.shiftEvent(); event == nil || !bytes.HasSuffix(event, []byte("\r\r")) {
		t.Fatalf("cr event = %q", event)
	}
	// drain 与 drainEnsuringBoundary。
	buffer.push([]byte("data: trailing-no-boundary"))
	if drained := buffer.drain(); !bytes.Contains(drained, []byte("trailing")) {
		t.Fatalf("drain = %q", drained)
	}
	loose := newPendingSseEventBuffer()
	loose.push([]byte("data: partial"))
	if drained := loose.drainEnsuringBoundary(); !bytes.HasSuffix(drained, []byte("\n\n")) {
		t.Fatalf("boundary drain = %q", drained)
	}
	complete := newPendingSseEventBuffer()
	complete.push([]byte("data: done\n\n"))
	if drained := complete.drainEnsuringBoundary(); !bytes.Equal(drained, []byte("data: done\n\n")) {
		t.Fatalf("complete drain = %q", drained)
	}
	// 多 chunk 消费（consumePrefix 跨片段拼接路径）。
	multi := newPendingSseEventBuffer()
	for _, piece := range []string{"ev", "ent: ", "x\ndata: 1", "\n\nOK"} {
		multi.push([]byte(piece))
	}
	event := multi.shiftEvent()
	if event == nil || !bytes.Equal(event, []byte("event: x\ndata: 1\n\n")) {
		t.Fatalf("multi event = %q", event)
	}
	if rest := multi.drain(); !bytes.Equal(rest, []byte("OK")) {
		t.Fatalf("multi rest = %q", rest)
	}
}

func TestW9FIsDeferrableLeadingChatCompletionNoopEvent(t *testing.T) {
	if isDeferrableLeadingChatCompletionNoopEvent(ParsedStreamEvent{}) {
		t.Fatal("nil data")
	}
	nonChunk := ParsedStreamEvent{Data: map[string]any{"object": "response"}}
	if isDeferrableLeadingChatCompletionNoopEvent(nonChunk) {
		t.Fatal("non chunk object")
	}
	withError := ParsedStreamEvent{Data: map[string]any{"object": "chat.completion.chunk", "error": "x"}}
	if isDeferrableLeadingChatCompletionNoopEvent(withError) {
		t.Fatal("error event")
	}
	withUsage := ParsedStreamEvent{Data: map[string]any{"object": "chat.completion.chunk", "usage": map[string]any{}}}
	if isDeferrableLeadingChatCompletionNoopEvent(withUsage) {
		t.Fatal("usage event")
	}
	noChoices := ParsedStreamEvent{Data: map[string]any{"object": "chat.completion.chunk"}}
	if isDeferrableLeadingChatCompletionNoopEvent(noChoices) {
		t.Fatal("no choices")
	}
	badChoice := ParsedStreamEvent{Data: map[string]any{"object": "chat.completion.chunk", "choices": []any{"row"}}}
	if isDeferrableLeadingChatCompletionNoopEvent(badChoice) {
		t.Fatal("bad choice row")
	}
	noop := ParsedStreamEvent{Data: map[string]any{
		"object":  "chat.completion.chunk",
		"choices": []any{map[string]any{"delta": map[string]any{"role": "assistant", "content": nil}}},
	}}
	if !isDeferrableLeadingChatCompletionNoopEvent(noop) {
		t.Fatal("noop event must be deferrable")
	}
}

func TestW9FIsNoopChatCompletionChoice(t *testing.T) {
	if isNoopChatCompletionChoice(nil) {
		t.Fatal("nil choice")
	}
	if isNoopChatCompletionChoice(map[string]any{"finish_reason": "stop"}) {
		t.Fatal("finish reason")
	}
	if isNoopChatCompletionChoice(map[string]any{"text": "hello"}) {
		t.Fatal("text content")
	}
	if isNoopChatCompletionChoice(map[string]any{"message": map[string]any{}}) {
		t.Fatal("message")
	}
	if isNoopChatCompletionChoice(map[string]any{}) {
		t.Fatal("missing delta")
	}
	if isNoopChatCompletionChoice(map[string]any{"delta": map[string]any{"role": 5}}) {
		t.Fatal("non-string role")
	}
	if isNoopChatCompletionChoice(map[string]any{"delta": map[string]any{"content": "text"}}) {
		t.Fatal("real content")
	}
	if isNoopChatCompletionChoice(map[string]any{"delta": map[string]any{"tool_calls": []any{}}}) {
		t.Fatal("unknown delta key")
	}
	if !isNoopChatCompletionChoice(map[string]any{"delta": map[string]any{"content": ""}}) {
		t.Fatal("empty content is noop")
	}
}

func TestW9FVisibleOutputOnlyChoice(t *testing.T) {
	if isVisibleOutputOnlyChatCompletionChoice(nil) {
		t.Fatal("nil")
	}
	if isVisibleOutputOnlyChatCompletionChoice(map[string]any{"finish_reason": "stop"}) {
		t.Fatal("finish")
	}
	if isVisibleOutputOnlyChatCompletionChoice(map[string]any{"error": "x"}) {
		t.Fatal("error")
	}
	if isVisibleOutputOnlyChatCompletionChoice(map[string]any{"message": map[string]any{}}) {
		t.Fatal("message")
	}
	if !isVisibleOutputOnlyChatCompletionChoice(map[string]any{"text": "visible"}) {
		t.Fatal("text is visible output")
	}
	if isVisibleOutputOnlyChatCompletionChoice(map[string]any{"text": 5}) {
		t.Fatal("non-string text")
	}
	if isVisibleOutputOnlyChatCompletionChoice(map[string]any{}) {
		t.Fatal("empty choice")
	}
	if isVisibleOutputOnlyChatCompletionChoice(map[string]any{"delta": map[string]any{"content": 5}}) {
		t.Fatal("non-string content")
	}
	if isVisibleOutputOnlyChatCompletionChoice(map[string]any{"delta": map[string]any{"refusal": 5}}) {
		t.Fatal("non-string refusal")
	}
	if isVisibleOutputOnlyChatCompletionChoice(map[string]any{"delta": map[string]any{"role": 5}}) {
		t.Fatal("non-string role")
	}
	if isVisibleOutputOnlyChatCompletionChoice(map[string]any{"delta": map[string]any{"content": "hi", "tool": 1}}) {
		t.Fatal("unknown delta key")
	}
	if !isVisibleOutputOnlyChatCompletionChoice(map[string]any{"delta": map[string]any{"role": "assistant", "content": "hi"}}) {
		t.Fatal("content visible")
	}
	if !isVisibleOutputOnlyChatCompletionChoice(map[string]any{"delta": map[string]any{"refusal": "no"}}) {
		t.Fatal("refusal visible")
	}
	if isVisibleOutputOnlyChatCompletionChoice(map[string]any{"delta": map[string]any{"role": "assistant"}}) {
		t.Fatal("role only is not visible output")
	}
}

func TestW9FIsSafeVisibleOutputOnlyRoot(t *testing.T) {
	if isSafeVisibleOutputOnlyRoot(map[string]any{"error": "x"}) {
		t.Fatal("error key")
	}
	for _, key := range []string{"response", "usage", "status", "finish_reason", "code", "message"} {
		if isSafeVisibleOutputOnlyRoot(map[string]any{key: "x"}) {
			t.Fatalf("%s key must be unsafe", key)
		}
	}
	if !isSafeVisibleOutputOnlyRoot(map[string]any{"choices": []any{}}) {
		t.Fatal("choices-only root is safe")
	}
}

func TestW9FCommonResponsesTextDeltaBuffer(t *testing.T) {
	prefix := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\""
	if !isCommonResponsesTextDeltaSseBuffer([]byte(prefix + "hello" + "\"}\n\n")) {
		t.Fatal("lf delta buffer")
	}
	if !isCommonResponsesTextDeltaSseBuffer([]byte(prefix + "hello" + "\"}\r\n\r\n")) {
		t.Fatal("crlf delta buffer")
	}
	if !isCommonResponsesTextDeltaSseBuffer([]byte(prefix + "hello" + "\"}\r\r")) {
		t.Fatal("cr delta buffer")
	}
	if isCommonResponsesTextDeltaSseBuffer([]byte(prefix + "he\"llo" + "\"}\n\n")) {
		t.Fatal("embedded quote must fail")
	}
	if isCommonResponsesTextDeltaSseBuffer([]byte(prefix + "he\\llo" + "\"}\n\n")) {
		t.Fatal("embedded backslash must fail")
	}
	if isCommonResponsesTextDeltaSseBuffer([]byte(prefix + "he\nllo" + "\"}\n\n")) {
		t.Fatal("control char must fail")
	}
	if isCommonResponsesTextDeltaSseBuffer([]byte("event: other\ndata: x\n\n")) {
		t.Fatal("wrong prefix")
	}
	if isCommonResponsesTextDeltaSseBuffer([]byte(prefix + "hello")) {
		t.Fatal("no suffix")
	}
}

func TestW9FFailureEventForDecision(t *testing.T) {
	if failureEventForDecision(InspectionDecision{Action: DecisionDiscardEvent}, true) != nil {
		t.Fatal("discard yields nil")
	}
	if failureEventForDecision(InspectionDecision{Action: DecisionDryRun}, false) != nil {
		t.Fatal("dry run yields nil")
	}
	event := failureEventForDecision(InspectionDecision{Action: DecisionIntercept, ErrorCode: "custom_code", Message: "msg"}, false)
	if event == nil || !bytes.Contains(event, []byte("custom_code")) || !bytes.HasPrefix(event, []byte("event: response.failed")) {
		t.Fatalf("intercept event = %q", event)
	}
	fallback := failureEventForDecision(InspectionDecision{Action: DecisionIntercept}, true)
	if !bytes.Contains(fallback, []byte("upstream_stream_interrupted")) {
		t.Fatalf("fallback event = %q", fallback)
	}
	if got := jsonString("a\"b"); got != "\"a\\\"b\"" {
		t.Fatalf("jsonString = %q", got)
	}
}

func TestW9FBufferPassthroughHelpers(t *testing.T) {
	// canPassThroughCommonResponsesTextDeltaBuffer：常见 delta 走快速通道。
	buffer := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{})
	common := []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
	if !buffer.canPassThroughCommonResponsesTextDeltaBuffer(common) {
		t.Fatal("common delta must pass through")
	}
	if buffer.canPassThroughCommonResponsesTextDeltaBuffer([]byte("data: {}\n\n")) {
		t.Fatal("non-delta must not pass")
	}
}
