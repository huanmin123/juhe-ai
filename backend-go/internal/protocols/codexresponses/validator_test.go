package codexresponses

import (
	"errors"
	"strings"
	"testing"
)

func TestRegistryMirrorsCurrentCodexItemFamilies(t *testing.T) {
	registry := NewRegistry()
	if registry.Revision != Revision || len(registry.Items()) != 17 {
		t.Fatalf("registry revision/items = %q/%d", registry.Revision, len(registry.Items()))
	}
	functionCall, ok := registry.Item("function_call")
	if !ok || functionCall.Prefix != "fc" || len(functionCall.EventStages) != 3 {
		t.Fatalf("function_call contract = %#v", functionCall)
	}
	if _, ok := registry.ItemByPrefix("ctc"); !ok {
		t.Fatal("custom_tool_call prefix is missing")
	}
	trigger, ok := registry.Item("compaction_trigger")
	if !ok || trigger.Prefix != "" || len(trigger.RepairableIDPaths) != 0 {
		t.Fatalf("compaction_trigger contract = %#v", trigger)
	}
	functionCall.RequiredFields[0].Name = "mutated"
	fresh, _ := registry.Item("function_call")
	if fresh.RequiredFields[0].Name == "mutated" {
		t.Fatal("registry returned mutable contract state")
	}
}

func TestValidateCleanFunctionCallAndOutput(t *testing.T) {
	result := Validate(map[string]any{
		"output": []any{
			map[string]any{"type": "function_call", "id": "fc_1", "name": "lookup", "arguments": "{}", "call_id": "call_1"},
			map[string]any{"type": "function_call_output", "id": "fco_1", "call_id": "call_1", "output": "ok"},
		},
	}, ProvenanceRawUpstream)
	if result.Outcome != OutcomeClean || len(result.Issues) != 0 || result.Revision != Revision {
		t.Fatalf("validation = %#v", result)
	}
}

func TestValidateClassifiesUnknownAndRepairableIdentity(t *testing.T) {
	unknown := Validate(map[string]any{"output": []any{map[string]any{"type": "future_item", "id": "future_1"}}}, ProvenanceGatewayBridge)
	if unknown.Outcome != OutcomeObservedUnknown || len(unknown.Issues) != 1 || unknown.Issues[0].Code != "unknown_item_type" {
		t.Fatalf("unknown validation = %#v", unknown)
	}
	repairable := Validate(map[string]any{"output": []any{map[string]any{
		"type": "function_call", "id": "ctc_wrong", "name": "lookup", "arguments": "{}", "call_id": "call_1",
	}}}, ProvenanceRawUpstream)
	if repairable.Outcome != OutcomeRepairable || repairable.Issues[0].RepairLevel != RepairR0 {
		t.Fatalf("repairable validation = %#v", repairable)
	}
}

func TestValidateBlocksDuplicateIdentityAndMissingRequiredField(t *testing.T) {
	result := Validate(map[string]any{"output": []any{
		map[string]any{"type": "function_call", "id": "fc_same", "name": "lookup", "arguments": "{}", "call_id": "call_1"},
		map[string]any{"type": "function_call", "id": "fc_same", "name": "lookup", "call_id": "call_2"},
	}}, ProvenanceRawUpstream)
	if result.Outcome != OutcomeBlocked {
		t.Fatalf("validation outcome = %q, want blocked", result.Outcome)
	}
	codes := issueCodes(result.Issues)
	if !codes["duplicate_item_identity"] || !codes["item_required_field_invalid"] {
		t.Fatalf("issue codes = %#v", codes)
	}
}

func TestValidateToolCorrelationAndExternalHistory(t *testing.T) {
	orphan := Validate(map[string]any{"output": []any{map[string]any{
		"type": "function_call_output", "id": "fco_1", "call_id": "old_call", "output": "ok",
	}}}, ProvenanceRequestHistory)
	if orphan.Outcome != OutcomeBlocked || !issueCodes(orphan.Issues)["orphan_tool_output"] {
		t.Fatalf("request history orphan = %#v", orphan)
	}
	knownHistory := Validate(map[string]any{
		"previous_response_id": "resp_previous",
		"output":               []any{map[string]any{"type": "function_call_output", "id": "fco_1", "call_id": "old_call", "output": "ok"}},
	}, ProvenanceRequestHistory)
	if knownHistory.Outcome != OutcomeClean {
		t.Fatalf("external history validation = %#v", knownHistory)
	}
}

func TestValidateRejectsAmbiguousCollectionsAndBoundsDiagnostics(t *testing.T) {
	ambiguous := Validate(map[string]any{"input": []any{}, "output": []any{}}, ProvenanceRawUpstream)
	if ambiguous.Outcome != OutcomeBlocked || ambiguous.Issues[0].Code != "response_item_collections_ambiguous" {
		t.Fatalf("ambiguous validation = %#v", ambiguous)
	}
	items := make([]string, DiagnosticLimit+7)
	for index := range items {
		items[index] = `{"type":"function_call","id":"fc_` + string(rune('a'+index)) + `","name":"lookup","arguments":"{}"}`
	}
	result, err := ValidateJSON([]byte(`{"output":[`+strings.Join(items, ",")+"]}"), ProvenanceRawUpstream)
	if err != nil {
		t.Fatalf("ValidateJSON() error = %v", err)
	}
	if len(result.Issues) != DiagnosticLimit || result.OmittedIssueCount == 0 || result.Outcome != OutcomeBlocked {
		t.Fatalf("bounded diagnostics = %#v", result)
	}
	if _, err := ValidateJSON([]byte(`[]`), ProvenanceRawUpstream); err == nil {
		t.Fatal("ValidateJSON() accepted a non-object")
	}
	if _, err := ValidateJSON([]byte(`{`), ProvenanceRawUpstream); !errors.Is(err, ErrInvalidJSON) {
		t.Fatal("invalid JSON sentinel was not preserved")
	}
	if _, err := ValidateJSON([]byte(`{} {}`), ProvenanceRawUpstream); !errors.Is(err, ErrInvalidJSON) {
		t.Fatal("trailing JSON value was accepted")
	}
}

func TestCommitStateRequiresNoTransportCommitForRetry(t *testing.T) {
	if !(CommitState{}).CanRetryUpstream() {
		t.Fatal("uncommitted attempt should be retryable")
	}
	for _, state := range []CommitState{
		{TransportCommitted: true},
		{SemanticCommitted: true},
		{DownstreamBytes: 1},
	} {
		if state.CanRetryUpstream() {
			t.Fatalf("committed state is retryable: %#v", state)
		}
	}
}

func TestOutcomeAtCommitMarksLateContractViolations(t *testing.T) {
	if got := OutcomeAtCommit(OutcomeBlocked, CommitState{SemanticCommitted: true}); got != OutcomeLateViolation {
		t.Fatalf("OutcomeAtCommit() = %q", got)
	}
	if got := OutcomeAtCommit(OutcomeRepairable, CommitState{}); got != OutcomeRepairable {
		t.Fatalf("uncommitted OutcomeAtCommit() = %q", got)
	}
}

func TestValidateJSONEnforcesSafeLocalShellTimeout(t *testing.T) {
	valid := `{"output":[{"type":"local_shell_call","id":"lsh_1","status":"completed","action":{"type":"exec","command":["go","test"],"timeout_ms":9007199254740991}}]}`
	if result, err := ValidateJSON([]byte(valid), ProvenanceRawUpstream); err != nil || result.Outcome != OutcomeClean {
		t.Fatalf("safe timeout validation = %#v, %v", result, err)
	}
	unsafe := `{"output":[{"type":"local_shell_call","id":"lsh_1","status":"completed","action":{"type":"exec","command":["go","test"],"timeout_ms":9007199254740992}}]}`
	result, err := ValidateJSON([]byte(unsafe), ProvenanceRawUpstream)
	if err != nil || result.Outcome != OutcomeBlocked || !issueCodes(result.Issues)["item_required_field_invalid"] {
		t.Fatalf("unsafe timeout validation = %#v, %v", result, err)
	}
}

func issueCodes(issues []Issue) map[string]bool {
	result := make(map[string]bool, len(issues))
	for _, issue := range issues {
		result[issue.Code] = true
	}
	return result
}
