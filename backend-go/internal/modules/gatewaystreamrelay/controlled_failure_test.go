package gatewaystreamrelay

import (
	"errors"
	"testing"
)

func TestEncodeControlledFailureEventUsesProtocolContracts(t *testing.T) {
	t.Parallel()
	allowed := DecideTerminalDisposition(TerminalDispositionInput{
		Commit:       SinkState{TransportCommitted: true, SemanticCommitted: true, DownstreamBytes: 1},
		TerminalKind: TerminalKindReadFailure,
		Capability:   CommittedFailureSignalProtocolEvent,
	})

	tests := []struct {
		name     string
		protocol ControlledFailureProtocol
		want     string
	}{
		{
			name:     "OpenAI Responses",
			protocol: ControlledFailureProtocolResponses,
			want:     "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"upstream_stream_interrupted\",\"message\":\"上游流式响应在输出后中断\"}}}\n\n",
		},
		{
			name:     "Anthropic Messages",
			protocol: ControlledFailureProtocolAnthropic,
			want:     "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"上游流式响应在输出后中断\",\"code\":\"upstream_stream_interrupted\"}}\n\n",
		},
		{
			name:     "Gemini Generate Content",
			protocol: ControlledFailureProtocolGemini,
			want:     "event: error\ndata: {\"error\":{\"message\":\"上游流式响应在输出后中断\",\"status\":\"UNAVAILABLE\",\"code\":\"upstream_stream_interrupted\"}}\n\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := EncodeControlledFailureEvent(allowed, test.protocol)
			if err != nil {
				t.Fatalf("EncodeControlledFailureEvent() error = %v", err)
			}
			if string(got) != test.want {
				t.Fatalf("EncodeControlledFailureEvent() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestEncodeControlledFailureEventRejectsUnsafePlanOrProtocol(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		plan     TerminalDisposition
		protocol ControlledFailureProtocol
		wantErr  error
	}{
		{
			name:     "retry upstream",
			plan:     TerminalDisposition{TerminalKind: TerminalKindReadFailure, RetryUpstream: true},
			protocol: ControlledFailureProtocolResponses,
			wantErr:  ErrControlledFailureEventNotPermitted,
		},
		{
			name:     "disconnect",
			plan:     TerminalDisposition{TerminalKind: TerminalKindReadFailure, EmitControlledEvent: true, Disconnect: true},
			protocol: ControlledFailureProtocolResponses,
			wantErr:  ErrControlledFailureEventNotPermitted,
		},
		{
			name:     "completed terminal",
			plan:     TerminalDisposition{TerminalKind: TerminalKindCompleted, EmitControlledEvent: true},
			protocol: ControlledFailureProtocolResponses,
			wantErr:  ErrControlledFailureEventNotPermitted,
		},
		{
			name:     "generic protocol",
			plan:     DecideTerminalDisposition(TerminalDispositionInput{Commit: SinkState{TransportCommitted: true, DownstreamBytes: 1}, TerminalKind: TerminalKindMissingTerminal, Capability: CommittedFailureSignalProtocolEvent}),
			protocol: "openai_chat_completions",
			wantErr:  ErrUnsupportedControlledFailureProtocol,
		},
		{
			name:     "manually constructed event plan",
			plan:     TerminalDisposition{TerminalKind: TerminalKindReadFailure, EmitControlledEvent: true},
			protocol: ControlledFailureProtocolResponses,
			wantErr:  ErrControlledFailureEventNotPermitted,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := EncodeControlledFailureEvent(test.plan, test.protocol)
			if len(got) != 0 || !errors.Is(err, test.wantErr) {
				t.Fatalf("EncodeControlledFailureEvent() = %q, %v; want no bytes and %v", got, err, test.wantErr)
			}
		})
	}
}
