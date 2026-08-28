package modelcheckowner

import "testing"

func TestAggregateEvidenceFailsClosedForMissingAndPartialFamilies(t *testing.T) {
	missing := AggregateEvidence([]map[string]any{{"kind": "stability", "status": "passed", "score": 10}})
	if missing.Formed || missing.TrustFormed || len(missing.Missing) != len(requiredEvidenceFamilies)-1 {
		t.Fatalf("missing=%#v", missing)
	}
	items := make([]map[string]any, 0, len(requiredEvidenceFamilies))
	for _, family := range requiredEvidenceFamilies {
		items = append(items, map[string]any{"kind": family, "status": "passed", "score": 10})
	}
	items[0]["status"] = "partial"
	partial := AggregateEvidence(items)
	if partial.Formed || partial.TrustFormed || len(partial.Partial) != 1 {
		t.Fatalf("partial=%#v", partial)
	}
	items[0]["status"] = "passed"
	formed := AggregateEvidence(items)
	if !formed.Formed || !formed.TrustFormed || formed.TrustScore != 1 {
		t.Fatalf("formed=%#v", formed)
	}
}

func TestAggregateEvidenceUnknownStatusFailsClosed(t *testing.T) {
	items := make([]map[string]any, 0, len(requiredEvidenceFamilies))
	for _, family := range requiredEvidenceFamilies {
		items = append(items, map[string]any{"kind": family, "status": "passed", "score": 10})
	}
	items[0]["status"] = ""
	aggregate := AggregateEvidence(items)
	if aggregate.Formed || aggregate.TrustFormed || len(aggregate.Partial) != 1 || aggregate.Partial[0] != requiredEvidenceFamilies[0] {
		t.Fatalf("unknown evidence status must fail closed: %+v", aggregate)
	}
}

func TestAggregateEvidenceRejectsIncompleteReceiptsAndDuplicates(t *testing.T) {
	items := make([]map[string]any, 0, len(requiredEvidenceFamilies)+1)
	for _, family := range requiredEvidenceFamilies {
		items = append(items, map[string]any{
			"kind": family, "status": "passed", "score": 10, "maxScore": 10,
			"evidence": map[string]any{},
		})
	}
	items[0]["evidence"] = map[string]any{"partial": true}
	items = append(items, map[string]any{"kind": "stability", "status": "passed", "score": 10, "maxScore": 10, "evidence": map[string]any{}})
	aggregate := AggregateEvidence(items)
	if aggregate.Formed || aggregate.TrustFormed || len(aggregate.Invalid) != 1 || aggregate.Invalid[0] != "identity_observation" {
		t.Fatalf("incomplete or duplicate receipts must fail closed: %+v", aggregate)
	}
}

func TestAggregateEvidenceTrustScoreUsesReceiptCoverage(t *testing.T) {
	items := make([]map[string]any, 0, len(requiredEvidenceFamilies))
	for _, family := range requiredEvidenceFamilies {
		items = append(items, map[string]any{"kind": family, "status": "failed", "score": 0, "maxScore": 100, "evidence": map[string]any{}})
	}
	aggregate := AggregateEvidence(items)
	if !aggregate.Formed || !aggregate.TrustFormed || aggregate.TrustScore != 1 {
		t.Fatalf("complete failed receipts remain trustworthy evidence: %+v", aggregate)
	}
}

func TestAggregateEvidenceAllowsScopedNeutralExclusions(t *testing.T) {
	items := make([]map[string]any, 0, len(requiredEvidenceFamilies))
	for _, family := range requiredEvidenceFamilies {
		item := map[string]any{"kind": family, "status": "passed", "score": 10, "maxScore": 10, "evidence": map[string]any{}}
		switch family {
		case "juice":
			item["status"] = "skipped"
			item["evidence"] = map[string]any{"excludedFromScoring": true, "notApplicable": true, "reason": "juice_scope_not_applicable"}
		case "distribution":
			item["status"] = "skipped"
			item["evidence"] = map[string]any{"excludedFromScoring": true, "reason": "trusted_comparison_not_attached"}
		}
		items = append(items, item)
	}
	aggregate := AggregateEvidence(items)
	if !aggregate.Formed || !aggregate.TrustFormed || aggregate.TrustScore != 1 || len(aggregate.Neutral) != 2 || len(aggregate.Invalid) != 0 {
		t.Fatalf("scoped neutral exclusions should be formed: %+v", aggregate)
	}
}

func TestAggregateEvidenceDoesNotTreatArbitraryExcludedFamilyAsNeutral(t *testing.T) {
	items := make([]map[string]any, 0, len(requiredEvidenceFamilies))
	for _, family := range requiredEvidenceFamilies {
		item := map[string]any{"kind": family, "status": "passed", "score": 10, "maxScore": 10, "evidence": map[string]any{}}
		if family == "token_integrity" {
			item["status"] = "skipped"
			item["evidence"] = map[string]any{"excludedFromScoring": true, "reason": "tokenizer_snapshot_not_attached"}
		}
		items = append(items, item)
	}
	aggregate := AggregateEvidence(items)
	if aggregate.Formed || aggregate.TrustFormed || len(aggregate.Invalid) != 1 || aggregate.Invalid[0] != "token_integrity" {
		t.Fatalf("arbitrary exclusions must remain fail-closed: %+v", aggregate)
	}
}
