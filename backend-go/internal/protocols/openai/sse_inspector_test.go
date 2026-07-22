package openai

import (
	"errors"
	"strings"
	"testing"
)

func TestSSEInspectorParsesChunkedChatStream(t *testing.T) {
	inspector := newTestSSEInspector(t, DefaultSSELimits())
	stream := "event: message\r\ndata: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\r\n\r\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"prompt_tokens_details\":{\"cached_tokens\":3}}}\n\n" +
		"data: [DONE]\n\n"

	for _, chunk := range splitEvery(stream, 7) {
		if _, err := inspector.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	if err := inspector.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}

	got := inspector.Snapshot()
	if !got.TerminalReceived || got.TerminalEventType != "[DONE]" {
		t.Fatalf("terminal = (%v, %q), want (true, [DONE])", got.TerminalReceived, got.TerminalEventType)
	}
	if got.FailedReceived || got.Error != nil {
		t.Fatalf("failure = (%v, %#v), want no failure", got.FailedReceived, got.Error)
	}
	if got.EventCount != 3 || got.LastEventType != "[DONE]" || got.FinishReason != "stop" {
		t.Fatalf("event summary = %#v", got)
	}
	assertUsageValue(t, "input tokens", got.Usage.InputTokens, 10)
	assertUsageValue(t, "output tokens", got.Usage.OutputTokens, 2)
	assertUsageValue(t, "cache read tokens", got.Usage.CacheReadTokens, 3)
	if got.PendingEvent || !got.Finished {
		t.Fatalf("finished state = %#v", got)
	}
}

func TestSSEInspectorMergesResponsesUsageAndExtractsTerminalError(t *testing.T) {
	inspector := newTestSSEInspector(t, DefaultSSELimits())
	stream := "event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\",\"usage\":{\"input_tokens\":4}}\n\n" +
		"event: response.failed\n" +
		"data: {\"type\":\"response.failed\",\"response\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":5,\"input_tokens_details\":{\"cached_tokens\":2},\"output_tokens_details\":{\"reasoning_tokens\":3}},\"service_tier\":\"flex\",\"error\":{\"code\":\"upstream_error\",\"message\":\"boom\"}}}\n\n"

	if _, err := inspector.Write([]byte(stream)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := inspector.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}

	got := inspector.Snapshot()
	if !got.TerminalReceived || got.TerminalEventType != "response.failed" || !got.FailedReceived {
		t.Fatalf("terminal failure = %#v", got)
	}
	if got.Error == nil || got.Error.Code != "upstream_error" || got.Error.Message != "boom" || got.Error.EventType != "response.failed" {
		t.Fatalf("error = %#v", got.Error)
	}
	assertUsageValue(t, "input tokens", got.Usage.InputTokens, 7)
	assertUsageValue(t, "output tokens", got.Usage.OutputTokens, 5)
	assertUsageValue(t, "cache read tokens", got.Usage.CacheReadTokens, 2)
	assertUsageValue(t, "thinking tokens", got.Usage.ThinkingTokens, 3)
	if got.Usage.ServiceTier == nil || *got.Usage.ServiceTier != "flex" {
		t.Fatalf("service tier = %#v, want flex", got.Usage.ServiceTier)
	}
}

func TestSSEInspectorSupportsBareCRAndPreservesFirstTerminal(t *testing.T) {
	inspector := newTestSSEInspector(t, DefaultSSELimits())
	stream := "event: response.completed\rdata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\r\rdata: [DONE]\r\r"
	for _, chunk := range splitEvery(stream, 5) {
		if _, err := inspector.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	if err := inspector.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	got := inspector.Snapshot()
	if !got.TerminalReceived || got.TerminalEventType != "response.completed" {
		t.Fatalf("terminal = (%v, %q), want first terminal response.completed", got.TerminalReceived, got.TerminalEventType)
	}
	if got.EventCount != 2 || got.LastEventType != "[DONE]" {
		t.Fatalf("events = %#v", got)
	}
}

func TestSSEInspectorNormalizesDetailedUsageVariants(t *testing.T) {
	inspector := newTestSSEInspector(t, DefaultSSELimits())
	stream := "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{" +
		"\"input_tokens\":\"9\",\"output_tokens\":0," +
		"\"input_tokens_details\":{\"cache_creation\":{\"ephemeral_5m_input_tokens\":4,\"ephemeral_1h_input_tokens\":6},\"image_tokens\":2,\"audio_tokens\":3}," +
		"\"output_tokens_details\":{\"reasoning_tokens\":5,\"image_tokens\":7,\"audio_tokens\":8}," +
		"\"output_image_count\":1}}}\n\n"
	if _, err := inspector.Write([]byte(stream)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := inspector.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	got := inspector.Snapshot().Usage
	assertUsageValue(t, "input tokens", got.InputTokens, 9)
	assertUsageValue(t, "output tokens", got.OutputTokens, 0)
	assertUsageValue(t, "cache write tokens", got.CacheWriteTokens, 10)
	assertUsageValue(t, "cache write 1h tokens", got.CacheWrite1hTokens, 6)
	assertUsageValue(t, "thinking tokens", got.ThinkingTokens, 5)
	assertUsageValue(t, "input image tokens", got.InputImageTokens, 2)
	assertUsageValue(t, "output image tokens", got.OutputImageTokens, 7)
	assertUsageValue(t, "input audio tokens", got.InputAudioTokens, 3)
	assertUsageValue(t, "output audio tokens", got.OutputAudioTokens, 8)
	assertUsageValue(t, "output image count", got.OutputImageCount, 1)
}

func TestSSEInspectorClassifiesErrorWithoutInventingTerminal(t *testing.T) {
	inspector := newTestSSEInspector(t, DefaultSSELimits())
	stream := "event: error\ndata: {\"type\":\"error\",\"code\":\"bad_request\",\"message\":\"invalid\"}\n\n"
	if _, err := inspector.Write([]byte(stream)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := inspector.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	got := inspector.Snapshot()
	if got.TerminalReceived || !got.FailedReceived {
		t.Fatalf("classification = %#v", got)
	}
	if got.Error == nil || got.Error.Code != "bad_request" || got.Error.Message != "invalid" {
		t.Fatalf("error = %#v", got.Error)
	}
}

func TestSSEInspectorIgnoresMCPCallItemFailureAsStreamFailure(t *testing.T) {
	inspector := newTestSSEInspector(t, DefaultSSELimits())
	stream := "event: response.mcp_call.failed\ndata: {\"type\":\"response.mcp_call.failed\",\"error\":{\"code\":\"tool_error\",\"message\":\"tool failed\"}}\n\n"
	if _, err := inspector.Write([]byte(stream)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := inspector.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	got := inspector.Snapshot()
	if got.FailedReceived || got.Error != nil || got.TerminalReceived {
		t.Fatalf("classification = %#v", got)
	}
}

func TestSSEInspectorRecordsMalformedEventAndContinues(t *testing.T) {
	inspector := newTestSSEInspector(t, DefaultSSELimits())
	stream := "event: response.completed\ndata: {not-json}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}"
	if _, err := inspector.Write([]byte(stream)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := inspector.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	got := inspector.Snapshot()
	if got.MalformedEventCount != 1 || got.EventCount != 2 || !got.TerminalReceived {
		t.Fatalf("inspection = %#v", got)
	}
}

func TestSSEInspectorEnforcesLineEventAndTotalLimits(t *testing.T) {
	t.Run("line", func(t *testing.T) {
		inspector := newTestSSEInspector(t, SSELimits{MaxLineBytes: 8, MaxEventBytes: 64, MaxTotalBytes: 128})
		n, err := inspector.Write([]byte("data: 123\n"))
		if !errors.Is(err, ErrSSELineTooLarge) {
			t.Fatalf("Write() error = %v, want ErrSSELineTooLarge", err)
		}
		if n != 8 || inspector.Snapshot().TotalBytes != 8 {
			t.Fatalf("Write() = (%d, total %d), want accepted prefix 8", n, inspector.Snapshot().TotalBytes)
		}
		assertStickyLimitError(t, inspector, ErrSSELineTooLarge)
	})

	t.Run("event", func(t *testing.T) {
		inspector := newTestSSEInspector(t, SSELimits{MaxLineBytes: 64, MaxEventBytes: 20, MaxTotalBytes: 128})
		n, err := inspector.Write([]byte("data: 12345\ndata: 67890\n"))
		if !errors.Is(err, ErrSSEEventTooLarge) {
			t.Fatalf("Write() error = %v, want ErrSSEEventTooLarge", err)
		}
		if n != 20 || inspector.Snapshot().TotalBytes != 20 {
			t.Fatalf("Write() = (%d, total %d), want accepted prefix 20", n, inspector.Snapshot().TotalBytes)
		}
		assertStickyLimitError(t, inspector, ErrSSEEventTooLarge)
	})

	t.Run("total", func(t *testing.T) {
		inspector := newTestSSEInspector(t, SSELimits{MaxLineBytes: 64, MaxEventBytes: 64, MaxTotalBytes: 10})
		if _, err := inspector.Write([]byte("data: 1234")); err != nil {
			t.Fatalf("first Write() error = %v", err)
		}
		n, err := inspector.Write([]byte("x"))
		if !errors.Is(err, ErrSSEStreamTooLarge) {
			t.Fatalf("second Write() error = %v, want ErrSSEStreamTooLarge", err)
		}
		if n != 0 || inspector.Snapshot().TotalBytes != 10 {
			t.Fatalf("second Write() = (%d, total %d), want no byte accepted", n, inspector.Snapshot().TotalBytes)
		}
		assertStickyLimitError(t, inspector, ErrSSEStreamTooLarge)
	})
}

func TestNewSSEInspectorRejectsInvalidLimits(t *testing.T) {
	for _, limits := range []SSELimits{
		{},
		{MaxLineBytes: 1, MaxEventBytes: 1},
		{MaxLineBytes: -1, MaxEventBytes: 1, MaxTotalBytes: 1},
	} {
		if _, err := NewSSEInspector(limits); err == nil {
			t.Fatalf("NewSSEInspector(%#v) error = nil", limits)
		}
	}
}

func newTestSSEInspector(t *testing.T, limits SSELimits) *SSEInspector {
	t.Helper()
	inspector, err := NewSSEInspector(limits)
	if err != nil {
		t.Fatalf("NewSSEInspector() error = %v", err)
	}
	return inspector
}

func assertUsageValue(t *testing.T, label string, got *int64, want int64) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("%s = %#v, want %d", label, got, want)
	}
}

func assertStickyLimitError(t *testing.T, inspector *SSEInspector, want error) {
	t.Helper()
	if _, err := inspector.Write([]byte("data: ignored\n\n")); !errors.Is(err, want) {
		t.Fatalf("sticky Write() error = %v, want %v", err, want)
	}
	if err := inspector.Finish(); !errors.Is(err, want) {
		t.Fatalf("sticky Finish() error = %v, want %v", err, want)
	}
}

func splitEvery(value string, width int) []string {
	var chunks []string
	for len(value) > 0 {
		n := width
		if len(value) < n {
			n = len(value)
		}
		chunks = append(chunks, value[:n])
		value = value[n:]
	}
	return chunks
}

func TestSSEInspectorSnapshotDoesNotExposeMutableUsage(t *testing.T) {
	inspector := newTestSSEInspector(t, DefaultSSELimits())
	if _, err := inspector.Write([]byte("data: {\"usage\":{\"input_tokens\":9}}\n\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	first := inspector.Snapshot()
	if first.Usage.InputTokens == nil {
		t.Fatal("input tokens = nil")
	}
	*first.Usage.InputTokens = 100
	second := inspector.Snapshot()
	assertUsageValue(t, "input tokens", second.Usage.InputTokens, 9)
	if strings.TrimSpace(second.LastEventType) == "" {
		t.Fatal("last event type is empty")
	}
}
