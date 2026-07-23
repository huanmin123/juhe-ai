package gatewaycodexresponses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
	"juhe-ai/backend-go/internal/protocols/codexresponses"
	"juhe-ai/backend-go/internal/protocols/openai"
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

func TestInspectorStrictModeBuffersFragmentedViolationBeforeCommit(t *testing.T) {
	inspector := newInspector(t, codexresponses.ModeStrictIntercept)
	frame := `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"ctc_1","type":"custom_tool_call","call_id":"call_1","input":"{}"}}` + "\n\n"
	chunks := [][]byte{[]byte(frame[:len(frame)/2]), []byte(frame[len(frame)/2:])}
	index := 0
	sink := &bytes.Buffer{}
	result, err := gatewaystreamrelay.Relay(context.Background(), gatewaystreamrelay.SourceFunc(func(_ context.Context, p []byte) (int, error) {
		if index >= len(chunks) {
			return 0, io.EOF
		}
		chunk := chunks[index]
		index++
		return copy(p, chunk), nil
	}), gatewaystreamrelay.SinkFunc(func(_ context.Context, p []byte) (int, error) {
		return sink.Write(p)
	}), gatewaystreamrelay.Options{
		Inspector: inspector,
		Limits:    gatewaystreamrelay.Limits{MaxBytes: 1 << 20, BufferBytes: 4096, IdleTimeout: time.Second, TotalTimeout: time.Second},
	})
	if !errors.Is(err, gatewaystreamrelay.ErrProtocolTerminalFailed) || sink.Len() != 0 || result.FirstByteSent || !result.RetryAllowed {
		t.Fatalf("fragmented strict sink/result/error = %q %#v %v", sink.String(), result, err)
	}
}

func TestInspectorStrictModeValidatesPendingEventAtEOFBeforeCommit(t *testing.T) {
	inspector := newInspector(t, codexresponses.ModeStrictIntercept)
	pending := `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"ctc_1","type":"custom_tool_call","call_id":"call_1","input":"{}"}}`
	sink := &bytes.Buffer{}
	result, err := relay(t, inspector, pending, sink)
	if !errors.Is(err, gatewaystreamrelay.ErrProtocolTerminalFailed) || sink.Len() != 0 || result.FirstByteSent || !result.RetryAllowed {
		t.Fatalf("pending strict sink/result/error = %q %#v %v", sink.String(), result, err)
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

func TestInspectorSafeRepairRewritesR0IdentityAcrossStreamLifecycle(t *testing.T) {
	inspector := newInspector(t, codexresponses.ModeSafeRepair)
	stream := codexStream("fc_wrong")
	sink := &bytes.Buffer{}
	result, err := relay(t, inspector, stream, sink)
	if err != nil {
		t.Fatalf("Relay() error = %v", err)
	}
	if strings.Contains(sink.String(), "fc_wrong") || strings.Count(sink.String(), "ctc_stream_repair_1") != 4 {
		t.Fatalf("rewritten stream = %q", sink.String())
	}
	guard := result.Inspection.ResponseSnapshot.(GuardSnapshot)
	if guard.Outcome != codexresponses.OutcomeRepairedSafe || len(guard.RepairRuleIDs) != 1 || guard.RepairRuleIDs[0] != "codex.r0.response.replace_stream_item_id" {
		t.Fatalf("safe repair guard = %#v", guard)
	}
}

func TestInspectorSafeRepairBuffersPartialFrame(t *testing.T) {
	inspector := newInspector(t, codexresponses.ModeSafeRepair)
	frame := ": keep-extension\r\nid: cursor-1\r\nretry: 1000\r\nevent: response.output_item.added\r\n" +
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_wrong","type":"custom_tool_call","call_id":"call_1","name":"read_file","input":"{}"}}` + "\r\n\r\n"
	first, err := inspector.Transform([]byte(frame[:len(frame)/2]))
	if err != nil || len(first) != 0 {
		t.Fatalf("first Transform() = %q, %v", first, err)
	}
	second, err := inspector.Transform([]byte(frame[len(frame)/2:]))
	if err != nil || !strings.Contains(string(second), `"id":"ctc_stream_repair_1"`) || strings.Contains(string(second), "fc_wrong") ||
		!strings.Contains(string(second), ": keep-extension\r\nid: cursor-1\r\nretry: 1000\r\nevent: response.output_item.added\r\n") {
		t.Fatalf("second Transform() = %q, %v", second, err)
	}
}

func TestInspectorSafeRepairAcceptsMultipleFramesInOneChunk(t *testing.T) {
	inspector, err := NewInspector(Options{
		Mode: codexresponses.ModeSafeRepair, Provenance: codexresponses.ProvenanceRawUpstream,
		SSELimits: openai.SSELimits{MaxLineBytes: 1024, MaxEventBytes: 128, MaxTotalBytes: 1024},
	})
	if err != nil {
		t.Fatalf("NewInspector() error = %v", err)
	}
	frame := `data: {"type":"response.created","response":{"id":"r"}}` + "\n\n"
	output, err := inspector.Transform([]byte(frame + frame))
	if err != nil || string(output) != frame+frame {
		t.Fatalf("multiple frames = %q, %v", output, err)
	}
}

func TestInspectorSafeRepairAddsBoundaryForEOFPendingRepair(t *testing.T) {
	inspector := newInspector(t, codexresponses.ModeSafeRepair)
	partial := `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_wrong","type":"custom_tool_call","call_id":"call_1","name":"read_file","input":"{}"}}`
	if output, err := inspector.Transform([]byte(partial)); err != nil || len(output) != 0 {
		t.Fatalf("partial Transform() = %q, %v", output, err)
	}
	output, err := inspector.FinishTransform()
	if err != nil || !strings.HasSuffix(string(output), "\n\n") || !strings.Contains(string(output), `"id":"ctc_stream_repair_1"`) {
		t.Fatalf("EOF repair = %q, %v", output, err)
	}
}

func TestInspectorSafeRepairRejectsRewrittenSingleLineAboveLineLimit(t *testing.T) {
	inspector, err := NewInspector(Options{
		Mode: codexresponses.ModeSafeRepair, Provenance: codexresponses.ProvenanceRawUpstream,
		SSELimits:    openai.SSELimits{MaxLineBytes: 100, MaxEventBytes: 1024, MaxTotalBytes: 4096},
		CreateItemID: func(prefix, _ string, _, _ int) string { return prefix + "_client_stable" },
	})
	if err != nil {
		t.Fatalf("NewInspector() error = %v", err)
	}
	frame := "data: {\"type\":\"response.output_item.added\",\n" +
		"data: \"output_index\":0,\"item\":{\"id\":\"fc_wrong\",\n" +
		"data: \"type\":\"custom_tool_call\",\"call_id\":\"call_1\",\"name\":\"read_file\",\"input\":\"{}\"}}\n\n"
	output, err := inspector.Transform([]byte(frame))
	if !errors.Is(err, ErrRewriteFrameTooLarge) {
		t.Fatalf("Transform() output=%q error=%v snapshot=%#v", output, err, inspector.Snapshot())
	}
}

func TestInspectorSafeRepairBlocksR2AndUnboundedGeneratedID(t *testing.T) {
	t.Run("R2", func(t *testing.T) {
		inspector := newInspector(t, codexresponses.ModeSafeRepair)
		malformed := `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_wrong","type":"custom_tool_call","call_id":"call_1","input":"{}"}}` + "\n\n"
		sink := &bytes.Buffer{}
		result, err := relay(t, inspector, malformed, sink)
		if !errors.Is(err, gatewaystreamrelay.ErrProtocolTerminalFailed) || sink.Len() != 0 || !result.RetryAllowed || result.Inspection.ErrorCode != "codex_responses_protocol_blocked" {
			t.Fatalf("R2 sink/result/error = %q %#v %v", sink.String(), result, err)
		}
	})

	t.Run("unbounded generated ID", func(t *testing.T) {
		inspector, err := NewInspector(Options{
			Mode: codexresponses.ModeSafeRepair, Provenance: codexresponses.ProvenanceRawUpstream,
			CreateItemID: func(prefix, _ string, _, _ int) string { return prefix + "_" + strings.Repeat("x", 300) },
		})
		if err != nil {
			t.Fatalf("NewInspector() error = %v", err)
		}
		sink := &bytes.Buffer{}
		result, err := relay(t, inspector, codexStream("fc_wrong"), sink)
		if !errors.Is(err, gatewaystreamrelay.ErrProtocolTerminalFailed) || sink.Len() != 0 || result.Inspection.ErrorCode != "codex_responses_protocol_blocked" {
			t.Fatalf("unbounded sink/result/error = %q %#v %v", sink.String(), result, err)
		}
	})
}

func TestInspectorSafeRepairDoesNotDropPartialDataAfterTerminal(t *testing.T) {
	inspector := newInspector(t, codexresponses.ModeSafeRepair)
	trailing := `data: {"type":"response.output_item.done","output_index":1,"item":{"id":"ctc_late","type":"custom_tool_call","call_id":"call_late","name":"read_file","input":"{}"}}`
	sink := &bytes.Buffer{}
	result, err := relay(t, inspector, codexStream("ctc_stream_0")+trailing, sink)
	if !errors.Is(err, gatewaystreamrelay.ErrProtocolTerminalFailed) || sink.Len() != 0 || result.Inspection.ErrorCode != "codex_responses_protocol_blocked" {
		t.Fatalf("trailing sink/result/error = %q %#v %v", sink.String(), result, err)
	}
}

func TestInspectorReportsParserCoverageGapWithoutStrictInterception(t *testing.T) {
	inspector := newInspector(t, codexresponses.ModeStrictIntercept)
	if _, err := inspector.Transform([]byte("data: {not-json}\n\n")); err != nil {
		t.Fatalf("Transform() error = %v", err)
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

func TestInspectorRequiresTransformSeamForSafeRepair(t *testing.T) {
	safe, err := NewInspector(Options{Mode: codexresponses.ModeSafeRepair, Provenance: codexresponses.ProvenanceRawUpstream})
	if err != nil {
		t.Fatalf("safe NewInspector() error = %v", err)
	}
	if err := safe.Observe([]byte("data: {}\n\n")); !errors.Is(err, ErrTransformRequired) {
		t.Fatalf("safe Observe() error = %v", err)
	}
	_, err = NewInspector(Options{Mode: codexresponses.ModeShadow, Provenance: codexresponses.ProvenanceRequestHistory})
	if !errors.Is(err, ErrInvalidProvenance) {
		t.Fatalf("invalid provenance error = %v", err)
	}
}

func newInspector(t *testing.T, mode codexresponses.Mode) *Inspector {
	t.Helper()
	options := Options{Mode: mode, Provenance: codexresponses.ProvenanceRawUpstream}
	if mode == codexresponses.ModeSafeRepair {
		options.CreateItemID = func(prefix, _ string, sequence, _ int) string {
			return fmt.Sprintf("%s_stream_repair_%d", prefix, sequence)
		}
	}
	inspector, err := NewInspector(options)
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
