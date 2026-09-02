package modelcheckowner

import "testing"

func TestBuildTrustReportFailsClosedAndFlagsAnomaly(t *testing.T) {
	report := BuildTrustReport(EvidenceAggregate{Formed: false, Missing: []string{"token_integrity"}}, []map[string]any{{"kind": "identity_observation", "status": "failed"}, {"kind": "juice", "hardAnomaly": true}})
	if report.EvidenceFormed || report.IdentityStatus != "suspected_downgrade" || !report.HardAnomaly {
		t.Fatalf("report=%#v", report)
	}
}

func TestBuildTrustReportReadsNestedEvaluatorEvidence(t *testing.T) {
	aggregate := EvidenceAggregate{Formed: true, TrustFormed: true, TrustScore: 1}
	report := BuildTrustReport(aggregate, []map[string]any{
		{"kind": "juice", "status": "failed", "evidence": map[string]any{"hardAnomaly": true}},
		{"kind": "cross_model", "status": "passed", "evidence": map[string]any{"modelMismatch": true}},
		{"kind": "token_integrity", "status": "warning", "evidence": map[string]any{"reasonCodes": []any{"proportional_padding"}}},
	})
	if !report.TrustFormed || !report.HardAnomaly {
		t.Fatalf("nested anomalies must preserve formed trust and hard anomaly: %+v", report)
	}
	for _, reason := range []string{"gpt56_juice_mixed_or_replaced", "cross_model_mismatch", "token_integrity_anomaly"} {
		found := false
		for _, current := range report.ReasonCodes {
			if current == reason {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing reason %q in %+v", reason, report.ReasonCodes)
		}
	}
}

func TestBuildTrustReportUsesProtocolAndProbeLevelCoverage(t *testing.T) {
	items := []map[string]any{
		{"kind": "protocol_basic", "status": "passed", "evidence": map[string]any{"success": true, "responseModel": "gpt-5.6"}},
		{"kind": "structured_output", "status": "failed", "evidence": map[string]any{"success": true, "responseModel": "gpt-5.6"}},
		{"kind": "behavior_probe", "status": "warning", "evidence": map[string]any{"requestFailureCount": 2, "scoringProbeCount": 6}},
	}
	report := BuildTrustReport(EvidenceAggregate{TrustScore: 1}, items)
	if report.ProtocolStatus != "failed" || report.IdentityStatus != "consistent" || report.EvidenceCoverage != 80 {
		t.Fatalf("report=%+v, want failed protocol, consistent identity, 80%% coverage", report)
	}
}

func TestBuildTrustReportMarksUnavailableModelEvidence(t *testing.T) {
	report := BuildTrustReport(EvidenceAggregate{}, []map[string]any{
		{"kind": "protocol_basic", "status": "failed", "evidence": map[string]any{"success": false, "responseModel": "gpt-5.6"}},
	})
	if report.EvidenceCoverage != 0 || report.ProtocolStatus != "insufficient_evidence" {
		t.Fatalf("report=%+v, failed response must not form model/protocol evidence", report)
	}
	for _, reason := range []string{"model_response_evidence_unavailable"} {
		found := false
		for _, current := range report.ReasonCodes {
			if current == reason {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing reason %q in %+v", reason, report.ReasonCodes)
		}
	}
}

func TestBuildTrustReportAddsProtocolFailureReason(t *testing.T) {
	report := BuildTrustReport(EvidenceAggregate{}, []map[string]any{
		{"kind": "protocol_basic", "status": "failed", "evidence": map[string]any{"success": true, "responseModel": "gpt-5.6"}},
	})
	if report.ProtocolStatus != "failed" {
		t.Fatalf("protocol status=%q", report.ProtocolStatus)
	}
	for _, current := range report.ReasonCodes {
		if current == "protocol_check_failed" {
			return
		}
	}
	t.Fatalf("missing protocol_check_failed in %+v", report.ReasonCodes)
}

func TestBuildTrustReportMarksWarningAndMismatchIdentity(t *testing.T) {
	items := []map[string]any{
		{"kind": "protocol_basic", "status": "passed", "evidence": map[string]any{"success": true, "responseModel": "gpt-5.6"}},
		{"kind": "responses_stream", "status": "warning", "evidence": map[string]any{"success": true, "responseModel": "gpt-5.6"}},
		{"kind": "cross_model", "status": "failed", "evidence": map[string]any{"success": true, "responseModel": "other-model", "modelMismatch": true}},
	}
	report := BuildTrustReport(EvidenceAggregate{}, items)
	if report.ProtocolStatus != "warning" || report.IdentityStatus != "suspected_downgrade" || !report.HardAnomaly {
		t.Fatalf("report=%+v, want warning protocol and downgrade identity", report)
	}
}

func TestBuildTrustReportDoesNotPromoteIdentityBehaviorFailureToHardDowngrade(t *testing.T) {
	report := BuildTrustReport(EvidenceAggregate{}, []map[string]any{
		{"kind": "responses_basic", "status": "passed", "evidence": map[string]any{"success": true, "responseModel": "gpt-5.6"}},
		{"kind": "identity_observation", "status": "failed", "evidence": map[string]any{"success": true, "requestFailureCount": 0, "scoringProbeCount": 7}},
	})
	if report.IdentityStatus != "consistent" || report.HardAnomaly {
		t.Fatalf("report=%+v, identity behavior quality failure must remain score evidence", report)
	}
}

func TestBuildTrustReportCoverageIncludesTrustedComparisonRequestFailures(t *testing.T) {
	report := BuildTrustReport(EvidenceAggregate{}, []map[string]any{
		{"kind": "protocol_basic", "status": "passed", "evidence": map[string]any{"success": true, "responseModel": "gpt-5.6"}},
		{"kind": "trusted_comparison.protocol_basic", "status": "skipped", "evidence": map[string]any{"requestFailure": true}},
	})
	if report.EvidenceCoverage != 50 {
		t.Fatalf("coverage=%d, want target and trusted comparison probes counted like the Node completeness summary", report.EvidenceCoverage)
	}
}
