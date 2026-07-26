package gatewaystreamrelay

// CommittedFailureSignal declares the only safe client action after a stream
// has crossed its downstream commit fence. The response adapter owns actual
// event encoding and connection lifecycle; Relay only exposes the typed plan.
type CommittedFailureSignal string

const (
	CommittedFailureSignalDisconnect    CommittedFailureSignal = "disconnect"
	CommittedFailureSignalProtocolEvent CommittedFailureSignal = "protocol_error_event"
)

// TerminalKind is intentionally typed rather than inferred from status/error
// text. Only upstream protocol, missing-terminal, and interrupted-stream
// failures are eligible for an upstream retry or controlled client event.
type TerminalKind string

const (
	TerminalKindCompleted               TerminalKind = "completed"
	TerminalKindUpstreamProtocolFailure TerminalKind = "upstream_protocol_failure"
	TerminalKindMissingTerminal         TerminalKind = "missing_terminal"
	TerminalKindReadFailure             TerminalKind = "read_failure"
	TerminalKindClientCanceled          TerminalKind = "client_canceled"
	TerminalKindGatewayLocal            TerminalKind = "gateway_local"
)

type TerminalDispositionInput struct {
	Commit              SinkState
	TerminalKind        TerminalKind
	Capability          CommittedFailureSignal
	SuccessTerminalSent bool
}

type TerminalDisposition struct {
	TerminalKind        TerminalKind
	RetryUpstream       bool
	EmitControlledEvent bool
	Disconnect          bool

	// controlledEventAuthorized is deliberately unexported. A response adapter
	// may observe the public plan, but only DecideTerminalDisposition can mint
	// the proof required to encode a second terminal event.
	controlledEventAuthorized bool
}

// DecideTerminalDisposition yields one owner-independent outcome. Go keeps a
// stricter precommit retry fence than Node: once headers are sent, a future
// listener cannot safely substitute an HTTP response, even with zero body
// bytes. That difference is deliberate and documented in W10.
func DecideTerminalDisposition(input TerminalDispositionInput) TerminalDisposition {
	result := TerminalDisposition{TerminalKind: input.TerminalKind}
	if input.TerminalKind == TerminalKindCompleted {
		return result
	}
	retryableUpstream := input.TerminalKind == TerminalKindUpstreamProtocolFailure || input.TerminalKind == TerminalKindMissingTerminal || input.TerminalKind == TerminalKindReadFailure
	preCommit := !input.Commit.TransportCommitted && !input.Commit.SemanticCommitted && input.Commit.DownstreamBytes == 0
	if retryableUpstream && preCommit {
		result.RetryUpstream = true
		return result
	}
	if input.SuccessTerminalSent {
		result.Disconnect = true
		return result
	}
	if retryableUpstream && input.Capability == CommittedFailureSignalProtocolEvent {
		result.EmitControlledEvent = true
		result.controlledEventAuthorized = true
		return result
	}
	result.Disconnect = true
	return result
}
