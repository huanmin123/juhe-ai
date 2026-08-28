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
