package modelcheckowner

import "strings"

// EvidenceAggregate is the only input shape accepted by the future quality
// projector. Missing or partial probe families never become a quality fact.
type EvidenceAggregate struct {
	Formed      bool
	Score       int
	MaxScore    int
	Families    []string
	Missing     []string
	Partial     []string
	TrustScore  float64
	TrustFormed bool
	// Invalid contains families whose receipt is present but explicitly marks
	// the evidence as partial/insufficient. It is kept separate from Missing
	// so callers can distinguish absent probes from an attempted probe that
	// did not yield a complete observation.
	Invalid []string
	Neutral []string
}

var requiredEvidenceFamilies = []string{"identity_observation", "token_integrity", "stability", "distribution", "cross_model", "juice", "usage_shape", "behavior_probe", "long_context"}

// AggregateEvidence validates family coverage and computes a bounded score.
// It intentionally does not infer missing families from the run score.
func AggregateEvidence(items []map[string]any) EvidenceAggregate {
	byKind := make(map[string]map[string]any, len(items))
	duplicates := make(map[string]bool)
	for _, item := range items {
		kind, _ := item["kind"].(string)
		kind = canonicalEvidenceFamily(kind)
		if strings.TrimSpace(kind) != "" {
			if _, exists := byKind[kind]; exists {
				duplicates[kind] = true
			}
			byKind[kind] = item
		}
	}
	result := EvidenceAggregate{}
	formedCount := 0
	for _, family := range requiredEvidenceFamilies {
		item, ok := byKind[family]
		if !ok {
			result.Missing = append(result.Missing, family)
			continue
		}
		result.Families = append(result.Families, family)
		status, _ := item["status"].(string)
		if status == "skipped" && neutralExcludedFamily(family, item["evidence"]) {
			result.Neutral = append(result.Neutral, family)
			continue
		}
		if status != "passed" && status != "failed" && status != "warning" {
			result.Partial = append(result.Partial, family)
		}
		if evidenceIncomplete(item["evidence"]) {
			result.Invalid = append(result.Invalid, family)
		}
		maxScore := 10
		if value, ok := numberValue(item["maxScore"]); ok && value > 0 {
			maxScore = int(value)
		}
		result.MaxScore += maxScore
		score := 0
		if value, ok := numberValue(item["score"]); ok && value > 0 {
			score = int(value)
		}
		if score > maxScore {
			score = maxScore
		}
		result.Score += score
		if status == "passed" || status == "failed" || status == "warning" {
			formedCount++
		}
	}
	result.Formed = len(result.Missing) == 0 && len(result.Partial) == 0 && len(result.Invalid) == 0 && len(duplicates) == 0
	// TrustScore is a receipt-completeness measure, not a quality score. A
	// complete failed probe is still trustworthy evidence of failure; using
	// score here would conflate quality with whether the probe actually ran.
	applicable := len(requiredEvidenceFamilies) - len(result.Neutral)
	if applicable > 0 {
		result.TrustScore = float64(formedCount) / float64(applicable)
	}
	if result.Formed {
		// Trust is formed only from complete receipts. A score is never used as
		// a substitute for receipt completeness; the trust projector applies
		// additional identity/anomaly gates below.
		result.TrustFormed = true
	}
	return result
}

// canonicalEvidenceFamily keeps the owner aggregate compatible with the
// gateway's scoped trusted-comparison names. The probe layer uses explicit
// names to distinguish comparison evidence from the target's own families;
// aggregation still consumes the stable family contract.
func canonicalEvidenceFamily(kind string) string {
	switch strings.TrimSpace(kind) {
	case "comparison":
		return "cross_model"
	case "distribution_similarity":
		return "distribution"
	default:
		return strings.TrimSpace(kind)
	}
}

// neutralExcludedFamily recognizes only scope exclusions emitted by the
// probe suite itself. Arbitrary excluded/insufficient evidence remains an
// invalid receipt so callers cannot widen the formed gate by setting flags.
func neutralExcludedFamily(family string, value any) bool {
	record, ok := value.(map[string]any)
	if !ok || record == nil || record["excludedFromScoring"] != true {
		return false
	}
	reason, _ := record["reason"].(string)
	switch family {
	case "juice":
		return record["notApplicable"] == true && reason == "juice_scope_not_applicable"
	case "distribution":
		return reason == "trusted_comparison_not_attached"
	default:
		return false
	}
}

func evidenceIncomplete(value any) bool {
	record, ok := value.(map[string]any)
	if !ok || record == nil {
		return false
	}
	for _, key := range []string{"partial", "evidenceInsufficient", "excludedFromScoring", "requestFailure"} {
		if flag, ok := record[key].(bool); ok && flag {
			return true
		}
	}
	if completed, ok := numberValue(record["completedProbeCount"]); ok {
		if required, requiredOK := numberValue(record["requiredProbeCount"]); requiredOK && completed < required {
			return true
		}
	}
	return false
}

func numberValue(value any) (float64, bool) {
	switch number := value.(type) {
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case float64:
		return number, true
	default:
		return 0, false
	}
}
