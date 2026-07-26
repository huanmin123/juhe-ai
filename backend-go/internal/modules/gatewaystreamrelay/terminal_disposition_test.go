package gatewaystreamrelay

import "testing"

func TestDecideTerminalDisposition(t *testing.T) {
	t.Parallel()
	committed := SinkState{TransportCommitted: true, SemanticCommitted: true, DownstreamBytes: 12}
	transportOnly := SinkState{TransportCommitted: true}
	for _, test := range []struct {
		name                 string
		input                TerminalDispositionInput
		want                 TerminalDisposition
		wantControlledPermit bool
	}{
		{name: "precommit protocol retry", input: TerminalDispositionInput{TerminalKind: TerminalKindUpstreamProtocolFailure}, want: TerminalDisposition{TerminalKind: TerminalKindUpstreamProtocolFailure, RetryUpstream: true}},
		{name: "precommit missing terminal retry", input: TerminalDispositionInput{TerminalKind: TerminalKindMissingTerminal}, want: TerminalDisposition{TerminalKind: TerminalKindMissingTerminal, RetryUpstream: true}},
		{name: "transport only remains conservative", input: TerminalDispositionInput{Commit: transportOnly, TerminalKind: TerminalKindReadFailure, Capability: CommittedFailureSignalProtocolEvent}, want: TerminalDisposition{TerminalKind: TerminalKindReadFailure, EmitControlledEvent: true}, wantControlledPermit: true},
		{name: "committed precise client event", input: TerminalDispositionInput{Commit: committed, TerminalKind: TerminalKindReadFailure, Capability: CommittedFailureSignalProtocolEvent}, want: TerminalDisposition{TerminalKind: TerminalKindReadFailure, EmitControlledEvent: true}, wantControlledPermit: true},
		{name: "committed generic disconnect", input: TerminalDispositionInput{Commit: committed, TerminalKind: TerminalKindReadFailure, Capability: CommittedFailureSignalDisconnect}, want: TerminalDisposition{TerminalKind: TerminalKindReadFailure, Disconnect: true}},
		{name: "success terminal suppresses second event", input: TerminalDispositionInput{Commit: committed, TerminalKind: TerminalKindUpstreamProtocolFailure, Capability: CommittedFailureSignalProtocolEvent, SuccessTerminalSent: true}, want: TerminalDisposition{TerminalKind: TerminalKindUpstreamProtocolFailure, Disconnect: true}},
		{name: "client cancellation never signals", input: TerminalDispositionInput{Commit: committed, TerminalKind: TerminalKindClientCanceled, Capability: CommittedFailureSignalProtocolEvent}, want: TerminalDisposition{TerminalKind: TerminalKindClientCanceled, Disconnect: true}},
		{name: "gateway local never signals", input: TerminalDispositionInput{Commit: committed, TerminalKind: TerminalKindGatewayLocal, Capability: CommittedFailureSignalProtocolEvent}, want: TerminalDisposition{TerminalKind: TerminalKindGatewayLocal, Disconnect: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := DecideTerminalDisposition(test.input)
			if got.TerminalKind != test.want.TerminalKind || got.RetryUpstream != test.want.RetryUpstream || got.EmitControlledEvent != test.want.EmitControlledEvent || got.Disconnect != test.want.Disconnect {
				t.Fatalf("DecideTerminalDisposition(%+v) = %+v, want %+v", test.input, got, test.want)
			}
			if (got.controlledFailurePermit != nil) != test.wantControlledPermit {
				t.Fatalf("controlled failure permit = %v, want %v", got.controlledFailurePermit != nil, test.wantControlledPermit)
			}
		})
	}
}
