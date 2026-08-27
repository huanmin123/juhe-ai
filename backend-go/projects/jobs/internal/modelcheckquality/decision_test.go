package modelcheckquality

import (
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprobe"
)

func TestDecideSuppressesEveryMutationWhenEvidenceIsNotFormed(t *testing.T) {
	decision := Decide(modelcheckinput.TriggerManual, policy(t), modelcheckprobe.SummaryResult{Level: "suspicious", Score: 1, MaxScore: 100}, true, Evidence{}, time.Date(2026, 8, 27, 18, 0, 0, 0, time.UTC))
	if decision.Triggered || decision.Result != "not_triggered" || len(decision.ReasonCodes) != 1 || decision.ReasonCodes[0] != "quality_evidence_not_formed" {
		t.Fatalf("decision=%#v", decision)
	}
}

func TestNewFactKeepsImmutableDecisionIdentities(t *testing.T) {
	decision := Decide(modelcheckinput.TriggerScheduled, policy(t), modelcheckprobe.SummaryResult{Level: "likely", Score: 100, MaxScore: 100}, true, Evidence{Formed: true}, time.Date(2026, 8, 27, 18, 0, 0, 0, time.UTC))
	fact := NewFact(decision, "outcome-digest", "policy-digest", "evidence-digest")
	if fact.Version != 1 || fact.OutcomeDigest != "outcome-digest" || fact.PolicyDigest != "policy-digest" || fact.EvidenceDigest != "evidence-digest" || fact.Decision.Result != decision.Result || fact.Decision.Score != decision.Score {
		t.Fatalf("fact=%+v decision=%+v", fact, decision)
	}
}

func TestDecideMatchesNodeGateForThresholdAndHardFailure(t *testing.T) {
	policy := policy(t)
	below := Decide(modelcheckinput.TriggerScheduled, policy, modelcheckprobe.SummaryResult{Level: "suspicious", Score: 69, MaxScore: 100}, true, Evidence{Formed: true}, time.Now())
	if !below.Triggered || below.HardFailure || below.ReasonCodes[0] != "score_below_threshold" {
		t.Fatalf("below=%#v", below)
	}
	hard := Decide(modelcheckinput.TriggerScheduled, policy, modelcheckprobe.SummaryResult{Level: "likely", Score: 99, MaxScore: 100}, true, Evidence{Formed: true, MappingStatus: "undeclared_mismatch"}, time.Now())
	if !hard.Triggered || !hard.HardFailure || hard.ReasonCodes[0] != "hard_quality_conflict" {
		t.Fatalf("hard=%#v", hard)
	}
	unavailable := Decide(modelcheckinput.TriggerScheduled, policy, modelcheckprobe.SummaryResult{Level: "unavailable", Score: 0, MaxScore: 100}, true, Evidence{Formed: true, MappingStatus: "undeclared_mismatch"}, time.Now())
	if unavailable.Triggered || !unavailable.HardFailure || unavailable.ReasonCodes[0] != "quality_evidence_unavailable" {
		t.Fatalf("unavailable=%#v", unavailable)
	}
}

func policy(t *testing.T) modelcheckinput.PolicySnapshot {
	t.Helper()
	value, err := modelcheckinput.NewPolicySnapshot("1", "quick", true, 70, "fallback", 10)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
