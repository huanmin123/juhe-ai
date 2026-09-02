package modelcheckowner

import (
	"math"
	"strings"
)

type TrustReport struct {
	IdentityStatus       string   `json:"identityStatus"`
	MappingStatus        string   `json:"mappingStatus"`
	UsageIntegrityStatus string   `json:"usageIntegrityStatus"`
	ProtocolStatus       string   `json:"protocolStatus"`
	EvidenceStatus       string   `json:"evidenceStatus"`
	RequestedModel       string   `json:"requestedModel,omitempty"`
	MappedUpstreamModel  string   `json:"mappedUpstreamModel,omitempty"`
	ObservedModel        string   `json:"observedModel,omitempty"`
	MappingApplied       bool     `json:"mappingApplied"`
	ProbeSetVersion      string   `json:"probeSetVersion,omitempty"`
	EvidenceCoverage     int      `json:"evidenceCoverage"`
	EvidenceFormed       bool     `json:"evidenceFormed"`
	TrustFormed          bool     `json:"trustFormed"`
	TrustScore           float64  `json:"trustScore"`
	HardAnomaly          bool     `json:"hardAnomaly"`
	ReasonCodes          []string `json:"reasonCodes"`
}

// BuildTrustReport derives a credential-free trust summary from evaluation
// receipts. It never infers missing evidence as success and has no mutation
// side effects. Status values use the Node/frontend vocabulary so the
// management UI renders every label.
func BuildTrustReport(aggregate EvidenceAggregate, items []map[string]any) TrustReport {
	report := TrustReport{
		IdentityStatus:       "insufficient_evidence",
		UsageIntegrityStatus: "insufficient_evidence",
		ProtocolStatus:       "insufficient_evidence",
		EvidenceStatus:       "insufficient",
		EvidenceFormed:       aggregate.Formed,
		TrustFormed:          aggregate.TrustFormed,
		TrustScore:           aggregate.TrustScore,
		EvidenceCoverage:     evidenceCompleteness(items),
		ReasonCodes:          append([]string(nil), aggregate.Missing...),
	}
	for _, family := range aggregate.Partial {
		report.ReasonCodes = appendReason(report.ReasonCodes, family+"_partial")
	}
	for _, family := range aggregate.Invalid {
		report.ReasonCodes = appendReason(report.ReasonCodes, family+"_evidence_insufficient")
	}
	for _, family := range aggregate.Neutral {
		report.ReasonCodes = appendReason(report.ReasonCodes, family+"_not_applicable")
	}
	hasModelResponseEvidence := false
	protocolCount, successfulProtocolCount := 0, 0
	protocolWarning, protocolFailed := false, false
	for _, item := range items {
		kind, _ := item["kind"].(string)
		if isTrustedComparisonEvidence(kind) {
			continue
		}
		status, _ := item["status"].(string)
		evidence, _ := item["evidence"].(map[string]any)
		if evidenceBool(evidence, "success") {
			if responseModel := strings.TrimSpace(evidenceStringValue(evidence, "responseModel")); responseModel != "" {
				hasModelResponseEvidence = true
				if report.ObservedModel == "" {
					report.ObservedModel = responseModel
				}
				if evidenceBool(evidence, "modelMismatch") {
					report.IdentityStatus = "suspected_downgrade"
					report.HardAnomaly = true
				}
			}
		}
		if isProtocolEvidenceKind(kind) {
			protocolCount++
			if evidenceBool(evidence, "success") {
				successfulProtocolCount++
				switch status {
				case "failed":
					protocolFailed = true
				case "warning", "skipped":
					protocolWarning = true
				}
			}
		}
		if kind == "juice" {
			anomaly, _ := item["hardAnomaly"].(bool)
			if !anomaly {
				anomaly, _ = evidence["hardAnomaly"].(bool)
			}
			if anomaly {
				report.HardAnomaly = true
				report.IdentityStatus = "suspected_downgrade"
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
	if !hasModelResponseEvidence || successfulProtocolCount == 0 {
		report.ProtocolStatus = "insufficient_evidence"
	} else if protocolFailed {
		report.ProtocolStatus = "failed"
	} else if protocolCount != successfulProtocolCount || protocolWarning {
		report.ProtocolStatus = "warning"
	} else {
		report.ProtocolStatus = "consistent"
	}
	if report.IdentityStatus == "insufficient_evidence" && hasModelResponseEvidence {
		report.IdentityStatus = "consistent"
	}
	if !hasModelResponseEvidence {
		report.EvidenceCoverage = 0
		report.ReasonCodes = appendReason(report.ReasonCodes, "model_response_evidence_unavailable")
	}
	if protocolFailed {
		report.ReasonCodes = appendReason(report.ReasonCodes, "protocol_check_failed")
	}
	return report
}

func isTrustedComparisonEvidence(kind string) bool {
	return strings.HasPrefix(strings.TrimSpace(kind), "trusted_comparison.")
}

func isProtocolEvidenceKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "responses_basic", "protocol_basic", "responses_stream", "protocol_stream", "structured_output", "tool_calling", "usage_shape":
		return true
	default:
		return false
	}
}

// evidenceCompleteness mirrors the Node probe-level completeness calculation.
// AggregateEvidence.TrustScore deliberately measures family receipts and must
// not be reused as a percentage of individual probes that actually replied.
func evidenceCompleteness(items []map[string]any) int {
	evidenceProbeCount, scoredEvidenceProbeCount := 0, 0
	for _, item := range items {
		evidence, _ := item["evidence"].(map[string]any)
		requestFailures, hasRequestFailures := evidenceNumber(evidence, "requestFailureCount")
		scoringProbes, hasScoringProbes := evidenceNumber(evidence, "scoringProbeCount")
		if hasRequestFailures || hasScoringProbes {
			requestFailures = maxZero(requestFailures)
			scoringProbes = maxZero(scoringProbes)
			evidenceProbeCount += requestFailures + scoringProbes
			scoredEvidenceProbeCount += scoringProbes
			continue
		}
		if evidenceBool(evidence, "requestFailure") {
			evidenceProbeCount++
			continue
		}
		if evidenceBool(evidence, "excludedFromScoring") {
			continue
		}
		status, _ := item["status"].(string)
		if status != "skipped" {
			evidenceProbeCount++
			scoredEvidenceProbeCount++
		}
	}
	if evidenceProbeCount == 0 {
		return 0
	}
	return int(math.Round(float64(scoredEvidenceProbeCount) * 100 / float64(evidenceProbeCount)))
}

func evidenceNumber(evidence map[string]any, key string) (int, bool) {
	if evidence == nil {
		return 0, false
	}
	value, ok := numberValue(evidence[key])
	if !ok {
		return 0, false
	}
	return int(math.Trunc(value)), true
}

func maxZero(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func evidenceStringValue(evidence map[string]any, key string) string {
	if evidence == nil {
		return ""
	}
	value, _ := evidence[key].(string)
	return value
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
