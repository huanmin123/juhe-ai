package gatewayaudit

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSanitizeHeadersRedactsCredentialsAndDropsAttestation(t *testing.T) {
	input := map[string][]string{
		"Authorization":     {"Bearer secret"},
		"X-Custom-Token":    {"one", "two"},
		"X-OAI-Attestation": {"attestation"},
		"Content-Type":      {"application/json"},
	}

	got := SanitizeHeaders(input)
	want := map[string][]string{
		"Authorization":  {RedactedValue},
		"X-Custom-Token": {RedactedValue, RedactedValue},
		"Content-Type":   {"application/json"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SanitizeHeaders() = %#v, want %#v", got, want)
	}

	input["Authorization"][0] = "changed"
	if got["Authorization"][0] != RedactedValue {
		t.Fatal("SanitizeHeaders() must return an independent snapshot")
	}
}

func TestSanitizeURLRedactsUserInfoAndSensitiveQueryValues(t *testing.T) {
	got, err := SanitizeURL("https://user:pass@example.com/v1/responses?api_key=secret&model=gpt-5&access_token=token")
	if err != nil {
		t.Fatalf("SanitizeURL() error = %v", err)
	}
	const want = "https://example.com/v1/responses?access_token=%5Bredacted%5D&api_key=%5Bredacted%5D&model=gpt-5"
	if got != want {
		t.Fatalf("SanitizeURL() = %q, want %q", got, want)
	}
}

func TestSanitizeURLRejectsOpaqueCredentialURL(t *testing.T) {
	if _, err := SanitizeURL("https:user:pass@example.com/v1?token=x"); err == nil {
		t.Fatal("SanitizeURL() accepted an opaque URL")
	}
}

func TestSanitizeURLNormalizesSchemeAndDropsFragment(t *testing.T) {
	got, err := SanitizeURL("HTTPS://example.com/v1#access_token=secret")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://example.com/v1" {
		t.Fatalf("SanitizeURL() = %q", got)
	}
}

func TestSanitizeURLRejectsOversizedInputBeforeParsing(t *testing.T) {
	value := "https://example.com/v1?token=" + strings.Repeat("x", MaxURLInputBytes)
	if _, err := SanitizeURL(value); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("SanitizeURL() error = %v", err)
	}
}

func TestSanitizeHeaderPreviewIsBounded(t *testing.T) {
	preview, truncated := SanitizeHeaderPreview(map[string][]string{
		"X-Large": {strings.Repeat("x", HeaderPreviewMaxValueBytes+1)},
	})
	if !truncated {
		t.Fatal("SanitizeHeaderPreview() did not report truncation")
	}
	if got := len(preview["X-Large"][0]); got != HeaderPreviewMaxValueBytes {
		t.Fatalf("preview value bytes = %d", got)
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

func TestCaptureAccountsForAndCopiesActualHeaderPreview(t *testing.T) {
	headers := map[string][]string{"X-Large": {strings.Repeat("x", 2048)}}
	capture, err := NewCapture(1024, MetadataDTO{SanitizedRequestHeaderPreview: headers})
	if err != nil {
		t.Fatal(err)
	}
	headers["X-Large"][0] = "changed"
	snapshot := takeSnapshot(t, capture)
	if snapshot.Status != CaptureStatusOverflow || snapshot.ResidentBytes > snapshot.MaxResidentBytes || snapshot.PeakResidentBytes <= snapshot.MaxResidentBytes {
		t.Fatalf("snapshot = %#v, want overflow", snapshot)
	}
	if snapshot.DTO.Metadata.SanitizedRequestHeaderPreview != nil {
		t.Fatalf("overflow retained header preview = %#v", snapshot.DTO.Metadata.SanitizedRequestHeaderPreview)
	}
}

func TestCaptureSnapshotIsIndependent(t *testing.T) {
	capture, err := NewCapture(4096)
	if err != nil {
		t.Fatal(err)
	}
	fragmentHeaders := map[string][]string{"Content-Type": {"application/json"}}
	if err = capture.AddFragment(FragmentDescriptorDTO{SanitizedHeaderPreview: fragmentHeaders}); err != nil {
		t.Fatal(err)
	}
	fragmentHeaders["Content-Type"][0] = "changed"
	first := takeSnapshot(t, capture)
	if got := first.DTO.Fragments[0].SanitizedHeaderPreview["Content-Type"][0]; got != "application/json" {
		t.Fatalf("snapshot header = %q", got)
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
