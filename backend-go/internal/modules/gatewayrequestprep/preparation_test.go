package gatewayrequestprep

import (
	"testing"

	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
)

func TestPrepareDerivesProtocolProfileAndFailureCapability(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		input         Input
		protocol      Protocol
		downstream    DownstreamProtocol
		profile       ClientProfile
		compatibility RequestClientCompatibility
		committed     gatewaystreamrelay.CommittedFailureSignal
		codec         gatewaystreamrelay.ControlledFailureProtocol
		codecOK       bool
	}{
		{
			name:     "generic OpenAI chat stream disconnects after commit",
			input:    Input{Method: "POST", Path: "/v1/chat/completions", StreamRequested: true, FallbackProtocol: ProtocolGemini},
			protocol: ProtocolOpenAI, downstream: DownstreamChatCompletionsSSE, profile: ClientProfileGenericOpenAI,
			compatibility: RequestClientCompatibilityOpenAI, committed: gatewaystreamrelay.CommittedFailureSignalDisconnect,
		},
		{
			name:     "explicit Codex Responses stream receives Responses failure event",
			input:    Input{Method: "POST", Path: "/v1/responses", StreamRequested: true, ExplicitClientProfile: "codex"},
			protocol: ProtocolOpenAI, downstream: DownstreamResponsesSSE, profile: ClientProfileCodex,
			compatibility: RequestClientCompatibilityOpenAI, committed: gatewaystreamrelay.CommittedFailureSignalProtocolEvent,
			codec: gatewaystreamrelay.ControlledFailureProtocolResponses, codecOK: true,
		},
		{
			name:     "validated Codex turn metadata controls compact request profile",
			input:    Input{Method: "POST", Path: "/v1/responses/compact", CodexTurnMetadataValid: true},
			protocol: ProtocolOpenAI, downstream: DownstreamJSON, profile: ClientProfileCodex,
			compatibility: RequestClientCompatibilityCodex, committed: gatewaystreamrelay.CommittedFailureSignalDisconnect,
		},
		{
			name:     "Claude Code signature needs two independent signals",
			input:    Input{Method: "POST", Path: "/v1/messages", StreamRequested: true, UserAgent: "claude-cli/1.2", HasAnthropicBeta: true},
			protocol: ProtocolAnthropic, downstream: DownstreamMessagesSSE, profile: ClientProfileClaudeCode,
			compatibility: RequestClientCompatibilityClaude, committed: gatewaystreamrelay.CommittedFailureSignalProtocolEvent,
			codec: gatewaystreamrelay.ControlledFailureProtocolAnthropic, codecOK: true,
		},
		{
			name:     "generic Anthropic stream does not receive Claude event",
			input:    Input{Method: "POST", Path: "/v1/messages", StreamRequested: true, UserAgent: "claude-cli/1.2"},
			protocol: ProtocolAnthropic, downstream: DownstreamMessagesSSE, profile: ClientProfileGenericAnthropic,
			compatibility: RequestClientCompatibilityAnthropic, committed: gatewaystreamrelay.CommittedFailureSignalDisconnect,
		},
		{
			name:     "Gemini CLI signature receives Gemini event",
			input:    Input{Method: "POST", Path: "/v1beta/models/gemini-2.5:streamGenerateContent", UserAgent: "GeminiCLI/1.0", HasGeminiCredential: true},
			protocol: ProtocolGemini, downstream: DownstreamGeminiStreamGenerateContentSSE, profile: ClientProfileGeminiCLI,
			compatibility: RequestClientCompatibilityOpenAI, committed: gatewaystreamrelay.CommittedFailureSignalProtocolEvent,
			codec: gatewaystreamrelay.ControlledFailureProtocolGemini, codecOK: true,
		},
		{
			name:     "Gemini stream requires credential as part of CLI signature",
			input:    Input{Method: "POST", Path: "/v1beta/models/gemini-2.5:generateContent", GeminiAltSSE: true, UserAgent: "GeminiCLI/1.0"},
			protocol: ProtocolGemini, downstream: DownstreamGeminiStreamGenerateContentSSE, profile: ClientProfileGenericGemini,
			compatibility: RequestClientCompatibilityOpenAI, committed: gatewaystreamrelay.CommittedFailureSignalDisconnect,
		},
		{
			name:     "unknown fallback is not a protocol event capability",
			input:    Input{Method: "POST", Path: "/bridge/opaque", StreamRequested: true},
			protocol: ProtocolUnknown, downstream: DownstreamUnknownStream, profile: ClientProfileUnknown,
			compatibility: RequestClientCompatibilityOpenAI, committed: gatewaystreamrelay.CommittedFailureSignalDisconnect,
		},
		{
			name:     "unsupported native method falls back instead of changing protocol",
			input:    Input{Method: "GET", Path: "/v1/messages", StreamRequested: true, FallbackProtocol: ProtocolGemini},
			protocol: ProtocolGemini, downstream: DownstreamUnknownStream, profile: ClientProfileGenericGemini,
			compatibility: RequestClientCompatibilityOpenAI, committed: gatewaystreamrelay.CommittedFailureSignalDisconnect,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := Prepare(test.input)
			if got.Protocol() != test.protocol || got.DownstreamProtocol() != test.downstream || got.ClientProfile() != test.profile || got.RequestClientCompatibility() != test.compatibility || got.CommittedFailureSignal() != test.committed {
				t.Fatalf("Prepare() = protocol=%q downstream=%q profile=%q compatibility=%q committed=%q", got.Protocol(), got.DownstreamProtocol(), got.ClientProfile(), got.RequestClientCompatibility(), got.CommittedFailureSignal())
			}
			codec, codecOK := got.ControlledFailureProtocol()
			if codec != test.codec || codecOK != test.codecOK {
				t.Fatalf("ControlledFailureProtocol() = %q, %v; want %q, %v", codec, codecOK, test.codec, test.codecOK)
			}
		})
	}
}

func TestPrepareProtocolEventCanOnlyBeMintedForExactProfileAndProtocol(t *testing.T) {
	t.Parallel()
	prepared := Prepare(Input{Method: "POST", Path: "/v1/responses", StreamRequested: true, ExplicitClientProfile: "codex"})
	disposition := gatewaystreamrelay.DecideTerminalDisposition(gatewaystreamrelay.TerminalDispositionInput{
		Commit:       gatewaystreamrelay.SinkState{TransportCommitted: true, SemanticCommitted: true, DownstreamBytes: 1},
		TerminalKind: gatewaystreamrelay.TerminalKindReadFailure,
		Capability:   prepared.CommittedFailureSignal(),
	})
	codec, ok := prepared.ControlledFailureProtocol()
	if !ok {
		t.Fatal("prepared Codex Responses request has no controlled failure protocol")
	}
	bytes, err := gatewaystreamrelay.EncodeControlledFailureEvent(disposition, codec)
	if err != nil || string(bytes) == "" {
		t.Fatalf("controlled event = %q, %v", bytes, err)
	}

	generic := Prepare(Input{Method: "POST", Path: "/v1/chat/completions", StreamRequested: true})
	if generic.CommittedFailureSignal() != gatewaystreamrelay.CommittedFailureSignalDisconnect {
		t.Fatalf("generic committed signal = %q", generic.CommittedFailureSignal())
	}
	if _, ok := generic.ControlledFailureProtocol(); ok {
		t.Fatal("generic OpenAI request unexpectedly received an event encoder")
	}
}
