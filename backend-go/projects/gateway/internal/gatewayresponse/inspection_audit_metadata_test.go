package gatewayresponse

import "testing"

// D-88 (BUG-0175 wave 4 W4-E): inspectionAuditMetadata carries the full
// 32-field responseInspectionAuditMetadata contract (audit/metadata.ts),
// not the previous 16-field subset.

func TestInspectionAuditMetadataFullFieldContract(t *testing.T) {
	decision := &ResponseInspectionDecision{
		Reason:                  "configured_response_policy",
		Action:                  "replace_with_failure",
		Transport:               "sse",
		TriggerPhase:            "after_downstream_write",
		EndpointFamily:          "openai_responses",
		FrameType:               "response.completed",
		UpstreamEventType:       "response.completed",
		UpstreamErrorCode:       "rate_limit_exceeded",
		UpstreamErrorType:       "invalid_request_error",
		UpstreamErrorMessage:    "quota",
		FinishReason:            "stop",
		ClientProfile:           "generic_openai",
		CodexCompactionExpected: true,
		RewriteErrorCode:        "rewrite_failed",
		RewriteMessage:          "rewrite message",
		DownstreamWritten:       true,
		PolicyID:                "policy-1",
		PolicyName:              "quota guard",
		PolicySource:            "system_default",
		PolicyScopeType:         "global",
		PolicyProtocolCode:      "openai",
		PolicyProviderCode:      "gpt",
		ExecutionMode:           "enforce",
		DataHandling:            "buffered",
		RetryEnabled:            true,
		AccountSwitch:           "switch_next",
		AccountState:            "temporary_unavailable",
		MatchedField:            "error.code",
		MatchedValue:            "rate_limit_exceeded",
		MatchedSnippet:          "rate_limit",
	}
	metadata := inspectionAuditMetadata(decision)
	want := []string{
		// 32 keys of responseInspectionAuditMetadata, in contract order.
		"responsePolicyMatched", "responseInspectionIntercepted", "fallbackReason",
		"inspectionAction", "transport", "endpointFamily", "frameType",
		"triggerPhase", "upstreamEventType", "upstreamErrorCode", "upstreamErrorType",
		"upstreamErrorMessage", "finishReason", "clientProfile", "codexCompactionExpected",
		"rewriteErrorCode", "rewriteMessage", "downstreamWritten", "policyId",
		"policyName", "policySource", "policyScopeType", "policyProtocolCode",
		"policyProviderCode", "executionMode", "dataHandling", "retryEnabled",
		"accountSwitch", "accountState", "matchedField", "matchedValue", "matchedSnippet",
	}
	if len(metadata) != len(want) {
		t.Fatalf("metadata fields = %d, want %d: %v", len(metadata), len(want), metadata)
	}
	for _, key := range want {
		if _, ok := metadata[key]; !ok {
			t.Errorf("metadata missing key %q", key)
		}
	}
	if metadata["fallbackReason"] != decision.Reason || metadata["inspectionAction"] != decision.Action {
		t.Errorf("fallbackReason/inspectionAction must mirror reason/action: %v", metadata)
	}
	if metadata["responseInspectionIntercepted"] != true || metadata["responsePolicyMatched"] != true {
		t.Errorf("boolean contract drift: %v", metadata)
	}
}

func TestInspectionAuditMetadataEmptyStringsOmitted(t *testing.T) {
	// Empty strings follow the Node JSON.stringify undefined-drop semantics;
	// the boolean fields always render.
	metadata := inspectionAuditMetadata(&ResponseInspectionDecision{
		Action:            "dry_run",
		Reason:            "before_downstream_write_response_failure",
		DownstreamWritten: false,
	})
	if metadata["responseInspectionIntercepted"] != false {
		t.Errorf("dry_run must not count as intercepted: %v", metadata["responseInspectionIntercepted"])
	}
	for _, key := range []string{"fallbackReason", "inspectionAction"} {
		if _, ok := metadata[key]; !ok {
			t.Errorf("populated key %q must stay present", key)
		}
	}
	for _, key := range []string{"policyId", "executionMode", "dataHandling", "accountState", "matchedField"} {
		if _, ok := metadata[key]; ok {
			t.Errorf("empty key %q must be omitted", key)
		}
	}
	if _, ok := metadata["downstreamWritten"]; !ok {
		t.Error("boolean downstreamWritten must always render")
	}
}
