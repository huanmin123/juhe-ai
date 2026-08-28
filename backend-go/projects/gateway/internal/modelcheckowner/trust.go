package modelcheckowner

import "strings"

type TrustReport struct {
	IdentityStatus string   `json:"identityStatus"`
	EvidenceFormed bool     `json:"evidenceFormed"`
	TrustFormed    bool     `json:"trustFormed"`
	TrustScore     float64  `json:"trustScore"`
	HardAnomaly    bool     `json:"hardAnomaly"`
	ReasonCodes    []string `json:"reasonCodes"`
}

// BuildTrustReport derives a credential-free trust summary from evaluation
// receipts. It never infers missing evidence as success and has no mutation
// side effects.
func BuildTrustReport(aggregate EvidenceAggregate, items []map[string]any) TrustReport {
	report := TrustReport{IdentityStatus: "unknown", EvidenceFormed: aggregate.Formed, TrustFormed: aggregate.TrustFormed, TrustScore: aggregate.TrustScore, ReasonCodes: append([]string(nil), aggregate.Missing...)}
	for _, family := range aggregate.Partial {
		report.ReasonCodes = appendReason(report.ReasonCodes, family+"_partial")
	}
	for _, family := range aggregate.Invalid {
		report.ReasonCodes = appendReason(report.ReasonCodes, family+"_evidence_insufficient")
	}
	for _, family := range aggregate.Neutral {
		report.ReasonCodes = appendReason(report.ReasonCodes, family+"_not_applicable")
	}
	for _, item := range items {
		kind, _ := item["kind"].(string)
		status, _ := item["status"].(string)
		evidence, _ := item["evidence"].(map[string]any)
		if status == "failed" && kind == "identity_observation" {
			report.IdentityStatus = "suspected_downgrade"
			report.HardAnomaly = true
			report.ReasonCodes = appendReason(report.ReasonCodes, "identity_probe_failed")
		}
		if kind == "juice" {
			anomaly, _ := item["hardAnomaly"].(bool)
			if !anomaly {
				anomaly, _ = evidence["hardAnomaly"].(bool)
			}
			if anomaly {
				report.HardAnomaly = true
				// Keep the historical reason code for API/report compatibility;
				// the more precise code is additive.
				report.ReasonCodes = appendReason(report.ReasonCodes, "gpt56_juice_anomaly")
				report.ReasonCodes = appendReason(report.ReasonCodes, "gpt56_juice_mixed_or_replaced")
			}
		}
		if strings.Contains(kind, "cross_model") && (status == "failed" || evidenceBool(evidence, "modelMismatch")) {
			report.ReasonCodes = appendReason(report.ReasonCodes, "cross_model_mismatch")
		}
		if kind == "token_integrity" && (status == "failed" || evidenceString(evidence, "reasonCodes", "proportional_padding")) {
			report.ReasonCodes = appendReason(report.ReasonCodes, "token_integrity_anomaly")
		}
	}
	if report.IdentityStatus == "unknown" && aggregate.Formed {
		report.IdentityStatus = "verified"
	}
	return report
}

func evidenceBool(evidence map[string]any, key string) bool {
	value, _ := evidence[key].(bool)
	return value
}

func evidenceString(evidence map[string]any, key, expected string) bool {
	values, ok := evidence[key].([]string)
	if ok {
		for _, value := range values {
			if value == expected {
				return true
			}
		}
	}
	anyValues, ok := evidence[key].([]any)
	if ok {
		for _, value := range anyValues {
			if text, _ := value.(string); text == expected {
				return true
			}
		}
	}
	return false
}

func appendReason(reasons []string, reason string) []string {
	for _, current := range reasons {
		if current == reason {
			return reasons
		}
	}
	return append(reasons, reason)
}
