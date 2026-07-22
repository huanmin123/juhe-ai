package gatewayaudit

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeAttemptPreservesOriginalURLWithExplicitBound(t *testing.T) {
	const original = "https://user:pass@example.com/v1/responses?api_key=secret&model=gpt-5#fragment"
	got, err := normalizeAttempt(AttemptInput{UpstreamURL: original})
	if err != nil {
		t.Fatalf("normalizeAttempt() error = %v", err)
	}
	if got.UpstreamURL != original || got.UpstreamURLTruncated {
		t.Fatalf("normalized URL = %q truncated=%v", got.UpstreamURL, got.UpstreamURLTruncated)
	}

	longURL := "https://example.com/?value=" + strings.Repeat("x", maxURLBytes)
	got, err = normalizeAttempt(AttemptInput{UpstreamURL: longURL})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.UpstreamURL) != maxURLBytes || !got.UpstreamURLTruncated {
		t.Fatalf("bounded URL bytes = %d truncated=%v", len(got.UpstreamURL), got.UpstreamURLTruncated)
	}
}

func TestBoundUTF8UsesByteLimitWithoutProducingInvalidText(t *testing.T) {
	got, truncated := BoundUTF8("你好abc", 7)
	if got != "你好a" || !truncated {
		t.Fatalf("BoundUTF8() = %q, %v, want %q, true", got, truncated, "你好a")
	}
	if len(got) > 7 {
		t.Fatalf("BoundUTF8() bytes = %d, want <= 7", len(got))
	}
}

func TestResolveTerminalEnforcesStreamTerminalAndRetrySemantics(t *testing.T) {
	streamFailure := ResolveTerminal(TerminalInput{
		RequestedOutcome: OutcomeSuccess,
		Success:          true,
		Stream:           true,
		TerminalRequired: true,
		TerminalReceived: false,
	})
	if streamFailure.Outcome != OutcomeStreamFailed || streamFailure.Success {
		t.Fatalf("missing terminal result = %#v", streamFailure)
	}
	if streamFailure.ErrorCode != ErrorCodeMissingStreamTerminal {
		t.Fatalf("missing terminal error code = %q", streamFailure.ErrorCode)
	}
	if streamFailure.ErrorCode != "upstream_stream_interrupted" {
		t.Fatalf("missing terminal grouping code = %q", streamFailure.ErrorCode)
	}
	genericStream := ResolveTerminal(TerminalInput{
		RequestedOutcome: OutcomeSuccess,
		Success:          true,
		Stream:           true,
		TerminalRequired: false,
		TerminalReceived: false,
	})
	if genericStream.Outcome != OutcomeSuccess || !genericStream.Success {
		t.Fatalf("generic stream result = %#v", genericStream)
	}

	retried := ResolveTerminal(TerminalInput{
		RequestedOutcome: OutcomeSuccess,
		Success:          true,
		HadFailedAttempt: true,
	})
	if retried.Outcome != OutcomeSuccessAfterRetry || !retried.Success {
		t.Fatalf("retry result = %#v", retried)
	}

	aborted := ResolveTerminal(TerminalInput{
		RequestedOutcome: OutcomeUpstreamFailed,
		Success:          false,
		ClientAborted:    true,
	})
	if aborted.Outcome != OutcomeClientAborted || aborted.ErrorPhase != "client" {
		t.Fatalf("aborted result = %#v", aborted)
	}

	contradictoryAbort := ResolveTerminal(TerminalInput{
		RequestedOutcome: OutcomeSuccess,
		Success:          true,
		ClientAborted:    true,
	})
	if contradictoryAbort.Outcome != OutcomeClientAborted || contradictoryAbort.Success {
		t.Fatalf("contradictory aborted result = %#v", contradictoryAbort)
	}
}

func TestCaptureHandoffAlwaysContainsConsistentTerminal(t *testing.T) {
	capture, err := NewCapture(MinResidentBytes)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := takeSnapshot(t, capture)
	if snapshot.DTO.Terminal.Outcome != OutcomeGatewayFailed || snapshot.DTO.Terminal.Success || snapshot.DTO.Terminal.ErrorCode != ErrorCodeInconsistentTerminal {
		t.Fatalf("fallback terminal = %#v", snapshot.DTO.Terminal)
	}
}

func TestCaptureBudgetOverflowIsStickyAndProducesNoPayload(t *testing.T) {
	capture, err := NewCapture(1536)
	if err != nil {
		t.Fatalf("NewCapture() error = %v", err)
	}
	if err = capture.AddFragment(FragmentDescriptorDTO{PartType: "a", CaptureStatus: "complete"}); err != nil {
		t.Fatalf("first Add() error = %v", err)
	}
	if err = capture.AddFragment(FragmentDescriptorDTO{PartType: "b", CaptureStatus: "complete"}); !errors.Is(err, ErrCaptureOverflow) {
		t.Fatalf("overflow Add() error = %v", err)
	}
	if err = capture.AddFragment(FragmentDescriptorDTO{PartType: "late"}); !errors.Is(err, ErrCaptureOverflow) {
		t.Fatalf("late Add() error = %v", err)
	}

	snapshot := takeSnapshot(t, capture)
	if snapshot.Status != CaptureStatusOverflow || snapshot.ResidentBytes > snapshot.MaxResidentBytes || snapshot.PeakResidentBytes <= snapshot.MaxResidentBytes || snapshot.MaxResidentBytes != 1536 {
		t.Fatalf("overflow snapshot = %#v", snapshot)
	}
	if len(snapshot.DTO.Fragments) != 0 {
		t.Fatalf("overflow snapshot retained fragments = %#v", snapshot.DTO.Fragments)
	}
}

func TestCaptureRejectsInvalidBudget(t *testing.T) {
	if _, err := NewCapture(MinResidentBytes - 1); err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("NewCapture() error = %v", err)
	}
}

func TestCaptureClampsConfiguredBudgetToHardLimit(t *testing.T) {
	capture, err := NewCapture(MaxResidentBytes + 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := takeSnapshot(t, capture).MaxResidentBytes; got != MaxResidentBytes {
		t.Fatalf("max resident bytes = %d, want %d", got, MaxResidentBytes)
	}
}

func TestCaptureChargesResidentDTOBytes(t *testing.T) {
	capture, err := NewCapture(MinResidentBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err = capture.AddFragment(FragmentDescriptorDTO{PartType: "12345"}); !errors.Is(err, ErrCaptureOverflow) {
		t.Fatalf("Add() error = %v, want overflow", err)
	}
}

func TestCaptureChargesEmptyItemOverhead(t *testing.T) {
	capture, err := NewCapture(1536)
	if err != nil {
		t.Fatal(err)
	}
	if err = capture.AddFragment(FragmentDescriptorDTO{}); err != nil {
		t.Fatalf("first Add() error = %v", err)
	}
	if err = capture.AddFragment(FragmentDescriptorDTO{}); !errors.Is(err, ErrCaptureOverflow) {
		t.Fatalf("second Add() error = %v, want overflow", err)
	}
}

func TestCaptureSnapshotIsIndependent(t *testing.T) {
	capture, err := NewCapture(4096)
	if err != nil {
		t.Fatal(err)
	}
	status := 201
	if err = capture.AddAttempt(AttemptInput{UpstreamStatusCode: &status}); err != nil {
		t.Fatal(err)
	}
	status = 500
	first := takeSnapshot(t, capture)
	if got := first.DTO.Attempts[0].UpstreamStatusCode; got == nil || *got != 201 {
		t.Fatalf("snapshot status = %v", got)
	}
	if _, err = capture.TakeSnapshot(); !errors.Is(err, ErrCaptureFinalized) {
		t.Fatalf("second TakeSnapshot() error = %v", err)
	}
	if err = capture.AddFragment(FragmentDescriptorDTO{}); !errors.Is(err, ErrCaptureFinalized) {
		t.Fatalf("AddFragment() after handoff error = %v", err)
	}
}

func TestCaptureBuilderResolvesTerminalAndStripsQueryFromPath(t *testing.T) {
	capture, err := NewCapture(4096, MetadataDTO{Path: "/v1/responses?token=secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err = capture.SetTerminal(TerminalInput{
		Success:          true,
		Stream:           true,
		TerminalRequired: true,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := takeSnapshot(t, capture)
	if snapshot.DTO.Metadata.Path != "/v1/responses" {
		t.Fatalf("path = %q", snapshot.DTO.Metadata.Path)
	}
	if snapshot.DTO.Terminal.Outcome != OutcomeStreamFailed || snapshot.DTO.Terminal.Success {
		t.Fatalf("terminal = %#v", snapshot.DTO.Terminal)
	}
}

func takeSnapshot(t *testing.T, capture *Capture) Snapshot {
	t.Helper()
	snapshot, err := capture.TakeSnapshot()
	if err != nil {
		t.Fatalf("TakeSnapshot() error = %v", err)
	}
	return snapshot
}

func TestResolveTerminalNormalizesUnknownOutcome(t *testing.T) {
	got := ResolveTerminal(TerminalInput{RequestedOutcome: Outcome("unknown")})
	if got.Outcome != OutcomeGatewayFailed || got.ErrorCode != ErrorCodeInconsistentTerminal {
		t.Fatalf("ResolveTerminal() = %#v", got)
	}
}
