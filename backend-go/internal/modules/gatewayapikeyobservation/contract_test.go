package gatewayapikeyobservation

import (
	"testing"
	"time"
)

func TestQualifyClassifiesNodeCompatiblePersistentAndDeferredFacts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		source  TrafficSource
		outcome Outcome
		want    Eligibility
	}{
		{TrafficSourceGateway, OutcomeCompleteSuccess, EligibilityForbidden},
		{TrafficSourceGateway, OutcomeExplicitPolicyFailure, EligibilityPersistentEligible},
		{TrafficSourceAccountHealthCheck, OutcomeCompleteSuccess, EligibilityPersistentEligible},
		{TrafficSourceRuntimeRecoveryProbe, OutcomeUpstreamFailure, EligibilityPersistentEligible},
		{TrafficSourceCooldownRetest, OutcomeFramingCompleteNeutral, EligibilityDeferOnly},
		{TrafficSourceAccountHealthCheck, OutcomeProbeTaskFailure, EligibilityDeferOnly},
	}
	for _, test := range tests {
		candidate := validCandidate()
		candidate.TrafficSource, candidate.Outcome = test.source, test.outcome
		result, err := Qualify(candidate)
		if err != nil {
			t.Fatalf("Qualify(%s/%s) error = %v", test.source, test.outcome, err)
		}
		if result.Eligibility != test.want {
			t.Fatalf("Qualify(%s/%s) = %s, want %s", test.source, test.outcome, result.Eligibility, test.want)
		}
	}
}

func TestQualifyRetainsSeparateAuthorizedAndResourceIdentities(t *testing.T) {
	t.Parallel()
	candidate := validCandidate()
	candidate.ResourceAccountID = "physical-source"
	candidate.Outcome = OutcomeCompleteSuccess
	candidate.TrafficSource = TrafficSourceAccountHealthCheck
	result, err := Qualify(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if result.Candidate.LocalAccountID == result.Candidate.ResourceAccountID {
		t.Fatal("resource identity was collapsed into local authorization identity")
	}
}

func TestQualifyFailsClosedForUnsafeKeyAndFences(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*Candidate){
		"disabled":             func(c *Candidate) { c.SelectedKey.Status = "disabled" },
		"unknown status":       func(c *Candidate) { c.SelectedKey.Status = "unknown" },
		"raw key fingerprint":  func(c *Candidate) { c.SelectedKey.Fingerprint = "sk-live-secret" },
		"unknown source":       func(c *Candidate) { c.TrafficSource = TrafficSource("automatic_probe") },
		"empty fingerprint":    func(c *Candidate) { c.SelectedKey.Fingerprint = "" },
		"negative index":       func(c *Candidate) { c.SelectedKey.Index = -1 },
		"source revision":      func(c *Candidate) { c.SourceDispatchRevision = 0 },
		"wrong type":           func(c *Candidate) { c.ResourceAccountType = "oauth" },
		"oversized diagnostic": func(c *Candidate) { c.Diagnostic = string(make([]byte, 513)) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := validCandidate()
			mutate(&candidate)
			if _, err := Qualify(candidate); err == nil {
				t.Fatal("Qualify() accepted unsafe candidate")
			}
		})
	}
}

func validCandidate() Candidate {
	return Candidate{LocalAccountID: "authorized", ResourceAccountID: "source", SystemAccountID: "system", AuthorizationBindingID: "binding", ResourceAccountType: "api_key", TargetConfigRevision: 1, TargetDispatchRevision: 1, SourceConfigRevision: 1, SourceDispatchRevision: 1, SelectedKey: SelectedKey{Fingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Index: 0, Status: "active"}, TrafficSource: TrafficSourceGateway, Outcome: OutcomeCompleteSuccess, TraceID: "trace", AttemptID: "attempt", ObservedAt: time.Now().UTC()}
}
