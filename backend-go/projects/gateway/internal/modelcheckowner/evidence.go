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
}

var requiredEvidenceFamilies = []string{"identity_observation", "token_integrity", "stability", "distribution", "cross_model", "juice", "usage_shape", "behavior_probe", "long_context"}

// AggregateEvidence validates family coverage and computes a bounded score.
// It intentionally does not infer missing families from the run score.
func AggregateEvidence(items []map[string]any) EvidenceAggregate {
	byKind := make(map[string]map[string]any, len(items))
	for _, item := range items {
		kind, _ := item["kind"].(string)
		if strings.TrimSpace(kind) != "" {
			byKind[kind] = item
		}
	}
	result := EvidenceAggregate{MaxScore: len(requiredEvidenceFamilies) * 10}
	for _, family := range requiredEvidenceFamilies {
		item, ok := byKind[family]
		if !ok {
			result.Missing = append(result.Missing, family)
			continue
		}
		result.Families = append(result.Families, family)
		status, _ := item["status"].(string)
		if status != "passed" && status != "failed" && status != "warning" {
			result.Partial = append(result.Partial, family)
		}
		if score, ok := item["score"].(int); ok && score > 0 {
			result.Score += score
		} else if score, ok := item["score"].(float64); ok && score > 0 {
			result.Score += int(score)
		}
	}
	result.Formed = len(result.Missing) == 0 && len(result.Partial) == 0
	if result.Formed && result.MaxScore > 0 {
		result.TrustScore = float64(result.Score) / float64(result.MaxScore)
		result.TrustFormed = true
	}
	return result
}
