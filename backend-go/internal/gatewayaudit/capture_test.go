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
}

func TestCaptureBudgetOverflowIsStickyAndProducesNoPayload(t *testing.T) {
	capture, err := NewCapture(1024)
	if err != nil {
		t.Fatalf("NewCapture() error = %v", err)
	}
	if err = capture.Add(ResidentItem{Kind: "a", ExternalBytes: 31}); err != nil {
		t.Fatalf("first Add() error = %v", err)
	}
	if err = capture.Add(ResidentItem{Kind: "b", ExternalBytes: 39}); !errors.Is(err, ErrCaptureOverflow) {
		t.Fatalf("overflow Add() error = %v", err)
	}
	if err = capture.Add(ResidentItem{Kind: "late", ExternalBytes: 1}); !errors.Is(err, ErrCaptureOverflow) {
		t.Fatalf("late Add() error = %v", err)
	}

	snapshot := capture.Snapshot()
	if snapshot.Status != CaptureStatusOverflow || snapshot.ResidentBytes != 1096 || snapshot.MaxResidentBytes != 1024 {
		t.Fatalf("overflow snapshot = %#v", snapshot)
	}
	if len(snapshot.Items) != 0 {
		t.Fatalf("overflow snapshot retained items = %#v", snapshot.Items)
	}
}

func TestCaptureRejectsInvalidBudgetAndSize(t *testing.T) {
	if _, err := NewCapture(0); err == nil || !strings.Contains(err.Error(), "positive") {
		t.Fatalf("NewCapture(0) error = %v", err)
	}
	capture, err := NewCapture(1)
	if err != nil {
		t.Fatal(err)
	}
	if err = capture.Add(ResidentItem{ExternalBytes: -1}); err == nil {
		t.Fatal("Add() accepted a negative size")
	}
}

func TestCaptureClampsConfiguredBudgetToHardLimit(t *testing.T) {
	capture, err := NewCapture(MaxResidentBytes + 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := capture.Snapshot().MaxResidentBytes; got != MaxResidentBytes {
		t.Fatalf("max resident bytes = %d, want %d", got, MaxResidentBytes)
	}
}

func TestCaptureChargesResidentDTOBytes(t *testing.T) {
	capture, err := NewCapture(ResidentItemOverheadBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err = capture.Add(ResidentItem{Kind: "12345", ExternalBytes: 0}); !errors.Is(err, ErrCaptureOverflow) {
		t.Fatalf("Add() error = %v, want overflow", err)
	}
}

func TestCaptureChargesEmptyItemOverhead(t *testing.T) {
	capture, err := NewCapture(ResidentItemOverheadBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err = capture.Add(ResidentItem{}); err != nil {
		t.Fatalf("first Add() error = %v", err)
	}
	if err = capture.Add(ResidentItem{}); !errors.Is(err, ErrCaptureOverflow) {
		t.Fatalf("second Add() error = %v, want overflow", err)
	}
}

func TestResolveTerminalNormalizesUnknownOutcome(t *testing.T) {
	got := ResolveTerminal(TerminalInput{RequestedOutcome: Outcome("unknown")})
	if got.Outcome != OutcomeGatewayFailed || got.ErrorCode != ErrorCodeInconsistentTerminal {
		t.Fatalf("ResolveTerminal() = %#v", got)
	}
}
