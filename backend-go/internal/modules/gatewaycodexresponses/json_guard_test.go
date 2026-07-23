package gatewaycodexresponses

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"juhe-ai/backend-go/internal/protocols/codexresponses"
)

func TestInspectJSONSafeRepairReplacesOnlyR0ItemID(t *testing.T) {
	raw := []byte(`{"id":"resp_json","object":"response","output":[{"id":"fc_wrong_custom","type":"custom_tool_call","call_id":"call_1","name":"apply_patch","input":"patch","unknown":{"keep":true}}]}`)
	result, err := InspectJSON(raw, JSONOptions{
		Mode:       codexresponses.ModeSafeRepair,
		Provenance: codexresponses.ProvenanceRawUpstream,
		CreateItemID: func(prefix, _ string, sequence, _ int) string {
			return prefix + "_generated_" + string(rune('0'+sequence))
		},
	})
	if err != nil {
		t.Fatalf("InspectJSON() error = %v", err)
	}
	if result.Outcome != codexresponses.OutcomeRepairedSafe || !result.Changed {
		t.Fatalf("result = %#v", result)
	}
	if len(result.RepairRuleIDs) != 1 || result.RepairRuleIDs[0] != "codex.r0.response.replace_item_id" {
		t.Fatalf("repair rules = %#v", result.RepairRuleIDs)
	}
	var repaired map[string]any
	if err := json.Unmarshal(result.Body, &repaired); err != nil {
		t.Fatalf("repaired JSON: %v", err)
	}
	item := repaired["output"].([]any)[0].(map[string]any)
	if item["id"] != "ctc_generated_1" || item["call_id"] != "call_1" || item["unknown"].(map[string]any)["keep"] != true {
		t.Fatalf("repaired item = %#v", item)
	}
	if bytes.Equal(result.Body, raw) {
		t.Fatal("repair did not change response")
	}
}

func TestInspectJSONStrictAndShadowDoNotRewrite(t *testing.T) {
	raw := []byte(`{"object":"response","output":[{"id":"fc_wrong_custom","type":"custom_tool_call","call_id":"call_1","name":"apply_patch","input":"patch"}]}`)
	for _, mode := range []codexresponses.Mode{codexresponses.ModeStrictIntercept, codexresponses.ModeShadow} {
		result, err := InspectJSON(raw, JSONOptions{Mode: mode, Provenance: codexresponses.ProvenanceRawUpstream})
		if err != nil {
			t.Fatalf("mode %s: InspectJSON() error = %v", mode, err)
		}
		if result.Outcome != codexresponses.OutcomeRepairable || result.Changed || !bytes.Equal(result.Body, raw) {
			t.Fatalf("mode %s result = %#v", mode, result)
		}
	}
}

func TestInspectJSONBlocksR2AndLateRepair(t *testing.T) {
	duplicate := []byte(`{"object":"response","output":[{"id":"msg_same","type":"message","role":"assistant","content":[]},{"id":"msg_same","type":"message","role":"assistant","content":[]}]}`)
	result, err := InspectJSON(duplicate, JSONOptions{Mode: codexresponses.ModeSafeRepair, Provenance: codexresponses.ProvenanceRawUpstream})
	if err != nil {
		t.Fatalf("duplicate InspectJSON() error = %v", err)
	}
	if result.Outcome != codexresponses.OutcomeBlocked || result.Changed || !result.Retryable {
		t.Fatalf("duplicate result = %#v", result)
	}
	late, err := InspectJSON([]byte(`{"object":"response","output":[{"id":"fc_wrong_custom","type":"custom_tool_call","call_id":"call_1","name":"apply_patch","input":"patch"}]}`), JSONOptions{
		Mode:       codexresponses.ModeSafeRepair,
		Provenance: codexresponses.ProvenanceRawUpstream,
		Commit:     codexresponses.CommitState{SemanticCommitted: true, DownstreamBytes: 10},
	})
	if err != nil {
		t.Fatalf("late InspectJSON() error = %v", err)
	}
	if late.Outcome != codexresponses.OutcomeLateViolation || late.Changed || late.Retryable {
		t.Fatalf("late result = %#v", late)
	}
}

func TestInspectJSONEnvelopeAndBodyBoundsFailClosed(t *testing.T) {
	result, err := InspectJSON([]byte(`{"output":[]}`), JSONOptions{Mode: codexresponses.ModeSafeRepair, Provenance: codexresponses.ProvenanceRawUpstream})
	if err != nil || result.Outcome != codexresponses.OutcomeBlocked || !result.Retryable {
		t.Fatalf("envelope result/error = %#v/%v", result, err)
	}
	_, err = InspectJSON([]byte(`{"object":"response","output":[]}`), JSONOptions{MaxBytes: 8, Provenance: codexresponses.ProvenanceRawUpstream})
	if !errors.Is(err, ErrJSONBodyTooLarge) {
		t.Fatalf("body bound error = %v", err)
	}
	_, err = InspectJSON([]byte(`{"object":"response","output":[]}`), JSONOptions{Provenance: codexresponses.ProvenanceRawUpstream})
	if err != nil {
		t.Fatalf("valid empty response error = %v", err)
	}
}

func TestInspectJSONRejectsUnsupportedRepairProvenance(t *testing.T) {
	result, err := InspectJSON([]byte(`{"object":"response","output":[{"id":"fc_wrong_custom","type":"custom_tool_call","call_id":"call_1","name":"apply_patch","input":"patch"}]}`), JSONOptions{
		Mode:       codexresponses.ModeSafeRepair,
		Provenance: codexresponses.ProvenanceRequestHistory,
	})
	if err != nil {
		t.Fatalf("InspectJSON() error = %v", err)
	}
	if result.Outcome != codexresponses.OutcomeBlocked || result.Changed || result.Retryable {
		t.Fatalf("unsupported provenance result = %#v", result)
	}
}

func TestInspectJSONBridgeAndMultipleRepairsUseUniqueTypedIDs(t *testing.T) {
	raw := []byte(`{"object":"response","output":[{"id":"bad_one","type":"message","role":"assistant","content":[]},{"id":"bad_two","type":"custom_tool_call","call_id":"call_1","name":"apply_patch","input":"patch"}]}`)
	result, err := InspectJSON(raw, JSONOptions{
		Mode:       codexresponses.ModeSafeRepair,
		Provenance: codexresponses.ProvenanceGatewayBridge,
		CreateItemID: func(prefix, _ string, sequence, _ int) string {
			return prefix + map[int]string{1: "_one", 2: "_two"}[sequence]
		},
	})
	if err != nil {
		t.Fatalf("InspectJSON() error = %v", err)
	}
	if result.Outcome != codexresponses.OutcomeRepairedBridge {
		t.Fatalf("outcome = %s", result.Outcome)
	}
	var body map[string]any
	if err := json.Unmarshal(result.Body, &body); err != nil {
		t.Fatal(err)
	}
	items := body["output"].([]any)
	if items[0].(map[string]any)["id"] != "msg_one" || items[1].(map[string]any)["id"] != "ctc_two" {
		t.Fatalf("items = %#v", items)
	}
}

func TestInspectJSONCleanCompactAndUnknownPreserveOriginalBytes(t *testing.T) {
	compact := []byte("{\n  \"output\": []\n}")
	clean, err := InspectJSON(compact, JSONOptions{
		Mode: codexresponses.ModeSafeRepair, Provenance: codexresponses.ProvenanceRawUpstream, EnvelopeKind: JSONEnvelopeCompact,
	})
	if err != nil || clean.Outcome != codexresponses.OutcomeClean || !bytes.Equal(clean.Body, compact) {
		t.Fatalf("compact result/error = %#v/%v", clean, err)
	}
	unknownRaw := []byte(`{"object":"response","output":[{"id":"future_1","type":"future_item","opaque":{"number":9007199254740991}}]}`)
	unknown, err := InspectJSON(unknownRaw, JSONOptions{Mode: codexresponses.ModeSafeRepair, Provenance: codexresponses.ProvenanceRawUpstream})
	if err != nil || unknown.Outcome != codexresponses.OutcomeObservedUnknown || !bytes.Equal(unknown.Body, unknownRaw) {
		t.Fatalf("unknown result/error = %#v/%v", unknown, err)
	}
}

func TestInspectJSONGenerationFailureIsBlockedWithoutMutation(t *testing.T) {
	raw := []byte(`{"object":"response","output":[{"id":"wrong","type":"message","role":"assistant","content":[]}]}`)
	result, err := InspectJSON(raw, JSONOptions{
		Mode: codexresponses.ModeSafeRepair, Provenance: codexresponses.ProvenanceRawUpstream,
		CreateItemID: func(_, _ string, _, _ int) string { return "wrong_prefix" },
	})
	if err != nil {
		t.Fatalf("InspectJSON() error = %v", err)
	}
	if result.Outcome != codexresponses.OutcomeBlocked || result.Changed || !bytes.Equal(result.Body, raw) || !result.Retryable {
		t.Fatalf("result = %#v", result)
	}
	if result.Issues[len(result.Issues)-1].Code != "safe_repair_failed" {
		t.Fatalf("issues = %#v", result.Issues)
	}
}

func TestInspectJSONRejectsMalformedAndInvalidConfiguration(t *testing.T) {
	_, err := InspectJSON([]byte(`{"object":"response"`), JSONOptions{Provenance: codexresponses.ProvenanceRawUpstream})
	if !errors.Is(err, ErrJSONInvalid) {
		t.Fatalf("invalid JSON error = %v", err)
	}
	_, err = InspectJSON([]byte(`{"object":"response","output":[]}`), JSONOptions{Mode: "future", Provenance: codexresponses.ProvenanceRawUpstream})
	if !errors.Is(err, ErrJSONUnsupportedMode) {
		t.Fatalf("unsupported mode error = %v", err)
	}
	_, err = InspectJSON([]byte(`{"object":"response","output":[]}`), JSONOptions{Provenance: codexresponses.ProvenanceUnknown})
	if !errors.Is(err, ErrJSONProvenance) {
		t.Fatalf("provenance error = %v", err)
	}
	_, err = InspectJSON([]byte(`{"object":"response","output":[]}`), JSONOptions{EnvelopeKind: "future", Provenance: codexresponses.ProvenanceRawUpstream})
	if !errors.Is(err, ErrJSONUnsupportedEnvelope) {
		t.Fatalf("envelope kind error = %v", err)
	}
	_, err = InspectJSON([]byte(`{"object":"response"`), JSONOptions{Provenance: codexresponses.ProvenanceRawUpstream})
	if !errors.Is(err, codexresponses.ErrInvalidJSON) {
		t.Fatalf("shared invalid JSON sentinel missing = %v", err)
	}
}
