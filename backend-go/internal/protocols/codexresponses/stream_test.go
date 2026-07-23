package codexresponses

import (
	"strings"
	"testing"
)

const streamResponseID = "resp_contract_sse"

func TestStreamStateTracksLifecycleAndCompletedEnvelope(t *testing.T) {
	state := NewStreamState(ProvenanceRawUpstream, false, nil)
	for _, step := range []struct {
		name  string
		event map[string]any
	}{
		{name: "added", event: streamItemEvent("added", 0, customToolItem(nil))},
		{name: "delta", event: map[string]any{"type": "response.custom_tool_call_input.delta", "output_index": 0, "item_id": "ctc_stream_0", "call_id": "call_stream_0", "delta": "x"}},
		{name: "done", event: streamItemEvent("done", 0, customToolItem(map[string]any{"input": `{"path":"README.md"}`}))},
	} {
		result := state.Consume(StreamInput{ResponseResourceID: streamResponseID, Event: step.event}, true)
		if result.Outcome != OutcomeClean {
			t.Fatalf("%s result = %#v", step.name, result)
		}
	}
	completed := state.Consume(StreamInput{ResponseResourceID: streamResponseID, Event: completedEvent([]any{customToolItem(map[string]any{"input": `{"path":"README.md"}`})})}, true)
	if completed.Outcome != OutcomeClean {
		t.Fatalf("completed result = %#v", completed)
	}
	identity, ok := state.IdentityFor(streamResponseID, 0)
	if !ok || identity.ItemID != "ctc_stream_0" || identity.ItemType != "custom_tool_call" || identity.Stage != StreamStageDone {
		t.Fatalf("identity = %#v, %v", identity, ok)
	}
}

func TestStreamStateBlocksIdentityDriftAndDuplicateIDs(t *testing.T) {
	state := streamStateWithAdded(t)
	changed := state.Consume(StreamInput{ResponseResourceID: streamResponseID, Event: map[string]any{
		"type": "response.custom_tool_call_input.delta", "output_index": 0, "item_id": "ctc_changed", "call_id": "call_stream_0",
	}}, true)
	if changed.Outcome != OutcomeBlocked || !issueCodes(changed.Issues)["event_item_id_inconsistent"] {
		t.Fatalf("changed identity = %#v", changed)
	}

	duplicateState := streamStateWithAdded(t)
	duplicate := duplicateState.Consume(StreamInput{ResponseResourceID: streamResponseID, Event: streamItemEvent("added", 1, customToolItem(nil))}, true)
	if duplicate.Outcome != OutcomeBlocked || !issueCodes(duplicate.Issues)["duplicate_item_identity"] {
		t.Fatalf("duplicate identity = %#v", duplicate)
	}
}

func TestStreamStateClassifiesUnknownAndRepairsR0Identity(t *testing.T) {
	unknownState := NewStreamState(ProvenanceRawUpstream, false, nil)
	unknown := unknownState.Consume(StreamInput{ResponseResourceID: streamResponseID, Event: streamItemEvent("added", 4, map[string]any{
		"id": "future_4", "type": "future_item", "payload": "opaque",
	})}, true)
	if unknown.Outcome != OutcomeObservedUnknown || !issueCodes(unknown.Issues)["unknown_item_type"] {
		t.Fatalf("unknown item = %#v", unknown)
	}

	repairState := NewStreamState(ProvenanceGatewayBridge, true, func(prefix, _ string, _, _ int) string {
		return prefix + "_client_stable"
	})
	added := repairState.Consume(StreamInput{ResponseResourceID: streamResponseID, Event: streamItemEvent("added", 0, customToolItem(map[string]any{"id": "fc_wrong"}))}, true)
	if added.Outcome != OutcomeRepairable || len(added.Repairs) != 1 || added.Repairs[0].ClientItemID != "ctc_client_stable" || added.Repairs[0].Field != "item.id" {
		t.Fatalf("added repair = %#v", added)
	}
	delta := repairState.Consume(StreamInput{ResponseResourceID: streamResponseID, Event: map[string]any{
		"type": "response.custom_tool_call_input.delta", "output_index": 0, "item_id": "fc_wrong", "call_id": "call_stream_0",
	}}, true)
	if len(delta.Repairs) != 1 || delta.Repairs[0].ClientItemID != "ctc_client_stable" || delta.Repairs[0].Field != "item_id" {
		t.Fatalf("delta repair = %#v", delta)
	}
}

func TestStreamStateCompletedChecksResourceAndFinalIdentity(t *testing.T) {
	state := streamStateWithAdded(t)
	mismatch := state.Consume(StreamInput{ResponseResourceID: streamResponseID, Event: completedEvent([]any{customToolItem(map[string]any{"id": "ctc_changed"})})}, true)
	if mismatch.Outcome != OutcomeBlocked || !issueCodes(mismatch.Issues)["event_item_id_inconsistent"] {
		t.Fatalf("completed mismatch = %#v", mismatch)
	}
	after := state.Consume(StreamInput{ResponseResourceID: streamResponseID, Event: streamItemEvent("done", 0, customToolItem(nil))}, true)
	if after.Outcome != OutcomeBlocked || !issueCodes(after.Issues)["event_after_response_completed"] {
		t.Fatalf("event after completion = %#v", after)
	}

	resourceState := NewStreamState(ProvenanceRawUpstream, false, nil)
	resource := resourceState.Consume(StreamInput{ResponseResourceID: streamResponseID, Event: map[string]any{
		"type": "response.completed", "response": map[string]any{"id": "resp_other", "output": []any{}},
	}}, true)
	if resource.Outcome != OutcomeBlocked || !issueCodes(resource.Issues)["response_resource_id_inconsistent"] {
		t.Fatalf("resource mismatch = %#v", resource)
	}
}

func TestStreamStateBoundsDiagnosticsAndRetainedIdentities(t *testing.T) {
	state := NewStreamState(ProvenanceRawUpstream, false, nil)
	for index := 0; index < StreamDiagnosticLimit+5; index++ {
		state.Consume(StreamInput{Event: map[string]any{"type": "response.output_item.added"}}, true)
	}
	longID := "ctc_" + strings.Repeat("x", 16*1024)
	longResource := "resp_" + strings.Repeat("r", 16*1024)
	result := state.Consume(StreamInput{ResponseResourceID: longResource, Event: streamItemEvent("added", 0, customToolItem(map[string]any{"id": longID}))}, true)
	if result.Outcome != OutcomeClean {
		t.Fatalf("long identity = %#v", result)
	}
	snapshot := state.Snapshot()
	if len(snapshot.Diagnostics) != StreamDiagnosticLimit || snapshot.OmittedIssueCount != 5 {
		t.Fatalf("snapshot diagnostics = %#v", snapshot)
	}
	identity, ok := state.IdentityFor(longResource, 0)
	if !ok || !strings.HasPrefix(identity.ItemID, "sha256:") || strings.Contains(identity.ItemID, longID) {
		t.Fatalf("retained identity = %#v, %v", identity, ok)
	}
	comment := state.Consume(StreamInput{Comment: true}, true)
	if comment.Outcome != OutcomeClean || comment.EventCategory != "sse_comment" || !state.CanTransparentRetry(false) || state.CanTransparentRetry(true) {
		t.Fatalf("comment/retry = %#v", comment)
	}
}

func streamStateWithAdded(t *testing.T) *StreamState {
	t.Helper()
	state := NewStreamState(ProvenanceRawUpstream, false, nil)
	result := state.Consume(StreamInput{ResponseResourceID: streamResponseID, Event: streamItemEvent("added", 0, customToolItem(nil))}, true)
	if result.Outcome != OutcomeClean {
		t.Fatalf("seed added = %#v", result)
	}
	return state
}

func streamItemEvent(stage string, outputIndex int, item map[string]any) map[string]any {
	return map[string]any{"type": "response.output_item." + stage, "output_index": outputIndex, "item": item}
}

func completedEvent(output []any) map[string]any {
	return map[string]any{"type": "response.completed", "response": map[string]any{"id": streamResponseID, "output": output}}
}

func customToolItem(overrides map[string]any) map[string]any {
	item := map[string]any{"id": "ctc_stream_0", "type": "custom_tool_call", "call_id": "call_stream_0", "name": "read_file", "input": `{}`}
	for key, value := range overrides {
		item[key] = value
	}
	return item
}
