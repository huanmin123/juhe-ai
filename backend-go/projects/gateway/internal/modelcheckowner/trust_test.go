package modelcheckowner

import "testing"

func TestBuildTrustReportFailsClosedAndFlagsAnomaly(t *testing.T) {
	report := BuildTrustReport(EvidenceAggregate{Formed: false, Missing: []string{"token_integrity"}}, []map[string]any{{"kind": "identity_observation", "status": "failed"}, {"kind": "juice", "hardAnomaly": true}})
	if report.EvidenceFormed || report.IdentityStatus != "suspected_downgrade" || !report.HardAnomaly {
		t.Fatalf("report=%#v", report)
	}
}
