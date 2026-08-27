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
