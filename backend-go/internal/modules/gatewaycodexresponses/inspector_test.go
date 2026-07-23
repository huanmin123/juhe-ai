package gatewaycodexresponses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
	"juhe-ai/backend-go/internal/protocols/codexresponses"
)

func TestInspectorShadowModeRelaysRepairableStreamAndReportsContract(t *testing.T) {
	inspector := newInspector(t, codexresponses.ModeShadow)
	stream := codexStream("fc_wrong")
	sink := &bytes.Buffer{}
	result, err := relay(t, inspector, stream, sink)
	if err != nil {
		t.Fatalf("Relay() error = %v", err)
	}
	if sink.String() != stream || result.State != gatewaystreamrelay.StateCompleted || !result.SemanticCommitted {
		t.Fatalf("relay result/sink = %#v/%q", result, sink.String())
	}
	guard, ok := result.Inspection.ResponseSnapshot.(GuardSnapshot)
	if !ok || guard.Outcome != codexresponses.OutcomeLateViolation || len(guard.Diagnostics) == 0 {
		t.Fatalf("guard snapshot = %#v, %v", result.Inspection.ResponseSnapshot, ok)
	}
}

func TestInspectorStrictModeBlocksBeforeFirstByteAndAllowsRetry(t *testing.T) {
	inspector := newInspector(t, codexresponses.ModeStrictIntercept)
	sink := &bytes.Buffer{}
	result, err := relay(t, inspector, codexStream("fc_wrong"), sink)
	if !errors.Is(err, gatewaystreamrelay.ErrProtocolTerminalFailed) {
		t.Fatalf("Relay() error = %v", err)
	}
	if sink.Len() != 0 || result.State != gatewaystreamrelay.StateFailedBeforeFirstByte || !result.RetryAllowed {
		t.Fatalf("strict relay = %#v, sink=%q", result, sink.String())
	}
	if result.Inspection.ErrorCode != "codex_responses_protocol_intercepted" || !result.Inspection.Failed {
		t.Fatalf("strict inspection = %#v", result.Inspection)
	}
}

func TestInspectorStrictModeAllowsCleanLifecycleAndMapsUsage(t *testing.T) {
	inspector := newInspector(t, codexresponses.ModeStrictIntercept)
	stream := codexStream("ctc_stream_0")
	sink := &bytes.Buffer{}
	result, err := relay(t, inspector, stream, sink)
	if err != nil {
		t.Fatalf("Relay() error = %v", err)
	}
	if result.Inspection.Usage.InputTokens == nil || *result.Inspection.Usage.InputTokens != 7 {
		t.Fatalf("usage = %#v", result.Inspection.Usage)
	}
	guard := result.Inspection.ResponseSnapshot.(GuardSnapshot)
	if guard.Outcome != codexresponses.OutcomeClean || guard.Stream.IdentityCount != 1 {
		t.Fatalf("guard = %#v", guard)
	}
}

func TestInspectorReportsParserCoverageGapWithoutStrictInterception(t *testing.T) {
	inspector := newInspector(t, codexresponses.ModeStrictIntercept)
	if err := inspector.Observe([]byte("data: {not-json}\n\n")); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	snapshot := inspector.Snapshot()
	guard := snapshot.ResponseSnapshot.(GuardSnapshot)
	if snapshot.Failed || guard.Outcome != codexresponses.OutcomeObservedUnknown || len(guard.Diagnostics) != 1 || guard.Diagnostics[0].Code != "protocol_guard_coverage_degraded" {
		t.Fatalf("coverage snapshot = %#v", snapshot)
	}
}

func TestInspectorMarksLateViolationAfterDownstreamCommit(t *testing.T) {
	inspector := newInspector(t, codexresponses.ModeShadow)
	clean := streamEventItem("added", 0, map[string]any{
		"id": "ctc_stream_0", "type": "custom_tool_call", "call_id": "call_stream_0", "name": "read_file", "input": "{}",
	})
	if err := inspector.Observe([]byte("data: " + jsonString(clean) + "\n\n")); err != nil {
		t.Fatalf("clean Observe() error = %v", err)
	}
	inspector.ObserveCommit(true, true, 32)
	changed := map[string]any{"type": "response.custom_tool_call_input.delta", "output_index": 0, "item_id": "ctc_changed", "call_id": "call_stream_0", "delta": "x"}
	if err := inspector.Observe([]byte("data: " + jsonString(changed) + "\n\n")); err != nil {
		t.Fatalf("changed Observe() error = %v", err)
	}
	guard := inspector.Snapshot().ResponseSnapshot.(GuardSnapshot)
	if guard.Outcome != codexresponses.OutcomeLateViolation {
		t.Fatalf("late guard = %#v", guard)
	}
}

func TestInspectorRejectsModesThatRequireByteRewriting(t *testing.T) {
	_, err := NewInspector(Options{Mode: codexresponses.ModeSafeRepair, Provenance: codexresponses.ProvenanceRawUpstream})
	if !errors.Is(err, ErrUnsupportedMode) {
		t.Fatalf("NewInspector() error = %v", err)
	}
	_, err = NewInspector(Options{Mode: codexresponses.ModeShadow, Provenance: codexresponses.ProvenanceRequestHistory})
	if !errors.Is(err, ErrInvalidProvenance) {
		t.Fatalf("invalid provenance error = %v", err)
	}
}

func newInspector(t *testing.T, mode codexresponses.Mode) *Inspector {
	t.Helper()
	inspector, err := NewInspector(Options{Mode: mode, Provenance: codexresponses.ProvenanceRawUpstream})
	if err != nil {
		t.Fatalf("NewInspector() error = %v", err)
	}
	return inspector
}

func relay(t *testing.T, inspector *Inspector, stream string, sink *bytes.Buffer) (gatewaystreamrelay.Result, error) {
	t.Helper()
	reader := strings.NewReader(stream)
	return gatewaystreamrelay.Relay(context.Background(), gatewaystreamrelay.SourceFunc(func(_ context.Context, p []byte) (int, error) {
		return reader.Read(p)
	}), gatewaystreamrelay.SinkFunc(func(_ context.Context, p []byte) (int, error) {
		return sink.Write(p)
	}), gatewaystreamrelay.Options{
		Inspector: inspector,
		Limits:    gatewaystreamrelay.Limits{MaxBytes: 1 << 20, BufferBytes: 4096, IdleTimeout: time.Second, TotalTimeout: time.Second},
	})
}

func codexStream(itemID string) string {
	item := `{"id":"` + itemID + `","type":"custom_tool_call","call_id":"call_stream_0","name":"read_file","input":"{}"}`
	return strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":` + item + `}`,
		`data: {"type":"response.custom_tool_call_input.delta","output_index":0,"item_id":"` + itemID + `","call_id":"call_stream_0","delta":"x"}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":` + item + `}`,
		`data: {"type":"response.completed","response":{"id":"resp_stream","output":[` + item + `],"usage":{"input_tokens":7,"output_tokens":2}}}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"
}

func jsonString(value map[string]any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func streamEventItem(stage string, outputIndex int, item map[string]any) map[string]any {
	return map[string]any{"type": "response.output_item." + stage, "output_index": outputIndex, "item": item}
}
