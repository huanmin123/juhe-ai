package modelcheckowner

import "strings"

type TrustReport struct {
	IdentityStatus string   `json:"identityStatus"`
	EvidenceFormed bool     `json:"evidenceFormed"`
	TrustScore     float64  `json:"trustScore"`
	HardAnomaly    bool     `json:"hardAnomaly"`
	ReasonCodes    []string `json:"reasonCodes"`
}

// BuildTrustReport derives a credential-free trust summary from evaluation
// receipts. It never infers missing evidence as success and has no mutation
// side effects.
func BuildTrustReport(aggregate EvidenceAggregate, items []map[string]any) TrustReport {
	report := TrustReport{IdentityStatus: "unknown", EvidenceFormed: aggregate.Formed, TrustScore: aggregate.TrustScore, ReasonCodes: append([]string(nil), aggregate.Missing...)}
	for _, item := range items {
		kind, _ := item["kind"].(string)
		status, _ := item["status"].(string)
		if status == "failed" && kind == "identity_observation" {
			report.IdentityStatus = "suspected_downgrade"
			report.HardAnomaly = true
			report.ReasonCodes = appendReason(report.ReasonCodes, "identity_probe_failed")
		}
		if kind == "juice" {
			if anomaly, ok := item["hardAnomaly"].(bool); ok && anomaly {
				report.HardAnomaly = true
				report.ReasonCodes = appendReason(report.ReasonCodes, "gpt56_juice_anomaly")
			}
		}
		if strings.Contains(kind, "cross_model") && status == "failed" {
			report.ReasonCodes = appendReason(report.ReasonCodes, "cross_model_mismatch")
		}
	}
	if report.IdentityStatus == "unknown" && aggregate.Formed {
		report.IdentityStatus = "verified"
	}
	return report
}

func appendReason(reasons []string, reason string) []string {
	for _, current := range reasons {
		if current == reason {
			return reasons
		}
	}
	return append(reasons, reason)
}
