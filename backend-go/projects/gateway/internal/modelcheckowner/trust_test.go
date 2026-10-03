package modelcheckowner

import "testing"

func TestBuildTrustReportFailsClosedAndFlagsAnomaly(t *testing.T) {
	report := BuildTrustReport(EvidenceAggregate{Formed: false, Missing: []string{"token_integrity"}}, []map[string]any{
		{"kind": "behavior_probe", "status": "failed"},
		{"kind": "protocol_basic", "status": "passed", "evidence": map[string]any{"success": true, "responseModel": "other-model", "modelMismatch": true}},
	})
	if report.EvidenceFormed || report.IdentityStatus != "suspected_downgrade" || !report.HardAnomaly {
		t.Fatalf("report=%#v", report)
	}
}

func TestBuildTrustReportReadsNestedEvaluatorEvidence(t *testing.T) {
	aggregate := EvidenceAggregate{Formed: true, TrustFormed: true, TrustScore: 1}
	report := BuildTrustReport(aggregate, []map[string]any{
		{"kind": "protocol_basic", "status": "passed", "evidence": map[string]any{"success": true, "responseModel": "other-model", "modelMismatch": true}},
		{"kind": "cross_model", "status": "passed", "evidence": map[string]any{"modelMismatch": true}},
		{"kind": "comparison", "status": "failed", "evidence": map[string]any{"modelMismatch": true}},
		{"kind": "token_integrity", "status": "warning", "evidence": map[string]any{"reasonCodes": []any{"proportional_padding"}}},
	})
	if !report.TrustFormed || !report.HardAnomaly {
		t.Fatalf("nested anomalies must preserve formed trust and hard anomaly: %+v", report)
	}
	// cross_model 门同时命中原始 cross_model 族证据与可信对比链 comparison 项
	// （后者经 canonicalEvidenceFamily 映射回 cross_model 族）。
	for _, reason := range []string{"cross_model_mismatch", "token_integrity_anomaly"} {
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

func TestBuildTrustReportDoesNotPromoteBehaviorFailureToHardDowngrade(t *testing.T) {
	report := BuildTrustReport(EvidenceAggregate{}, []map[string]any{
		{"kind": "responses_basic", "status": "passed", "evidence": map[string]any{"success": true, "responseModel": "gpt-5.6"}},
		{"kind": "behavior_probe", "status": "failed", "evidence": map[string]any{"success": true, "requestFailureCount": 0, "scoringProbeCount": 7}},
	})
	if report.IdentityStatus != "consistent" || report.HardAnomaly {
		t.Fatalf("report=%+v, behavior quality failure must remain score evidence", report)
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

func TestBuildTrustReportPromotesUsageIntegrityByDecisionTable(t *testing.T) {
	tokenItem := func(status string, reasonCodes ...string) map[string]any {
		codes := make([]any, 0, len(reasonCodes))
		for _, reason := range reasonCodes {
			codes = append(codes, reason)
		}
		return map[string]any{"kind": "token_integrity", "status": status, "evidence": map[string]any{"reasonCodes": codes}}
	}
	for _, tc := range []struct {
		name  string
		items []map[string]any
		want  string
	}{
		{name: "no token item keeps default", items: []map[string]any{{"kind": "protocol_basic", "status": "passed", "evidence": map[string]any{"success": true, "responseModel": "gpt-5.6"}}}, want: "insufficient_evidence"},
		{name: "passed projects consistent", items: []map[string]any{tokenItem("passed")}, want: "consistent"},
		{name: "failed projects suspected padding", items: []map[string]any{tokenItem("failed", "proportional_padding")}, want: "suspected_padding"},
		{name: "proportional padding reason escalates non-failed status", items: []map[string]any{{"kind": "token_integrity", "status": "warning", "evidence": map[string]any{"reasonCodes": []string{"proportional_padding"}}}}, want: "suspected_padding"},
		{name: "warning with slope warning projects warning", items: []map[string]any{tokenItem("warning", "slope_warning")}, want: "warning"},
		{name: "warning with bucket rounding projects warning", items: []map[string]any{tokenItem("warning", "bucket_rounding")}, want: "warning"},
		{name: "warning without decisive reason keeps default", items: []map[string]any{tokenItem("warning")}, want: "insufficient_evidence"},
		{name: "skipped with missing reported usage projects unsupported", items: []map[string]any{tokenItem("skipped", "reported_usage_missing")}, want: "unsupported"},
		{name: "skipped with incompatible reported usage projects unsupported", items: []map[string]any{tokenItem("skipped", "reported_usage_incompatible")}, want: "unsupported"},
		{name: "skipped tokenizer snapshot missing keeps default", items: []map[string]any{{"kind": "token_integrity", "status": "skipped", "evidence": map[string]any{"reason": "tokenizer_snapshot_not_attached"}}}, want: "insufficient_evidence"},
		{name: "unknown status keeps default", items: []map[string]any{tokenItem("mystery")}, want: "insufficient_evidence"},
		{name: "decisive items escalate to the strongest fact", items: []map[string]any{tokenItem("passed"), tokenItem("skipped", "reported_usage_missing")}, want: "unsupported"},
		{name: "trusted comparison token item never promotes", items: []map[string]any{{"kind": "trusted_comparison.token_integrity", "status": "failed", "evidence": map[string]any{"reasonCodes": []any{"proportional_padding"}}}}, want: "insufficient_evidence"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := BuildTrustReport(EvidenceAggregate{}, tc.items)
			if report.UsageIntegrityStatus != tc.want {
				t.Fatalf("usage integrity status=%q, want %q (report=%+v)", report.UsageIntegrityStatus, tc.want, report)
			}
		})
	}
}
