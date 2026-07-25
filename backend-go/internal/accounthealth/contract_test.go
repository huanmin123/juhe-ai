package accounthealth

import (
	"testing"
	"time"
)

func TestClassifyAutomaticProbeOutcome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		evidence ProbeEvidence
		want     ProbeOutcome
	}{
		{
			name:     "success wins even when no response evidence was retained",
			evidence: ProbeEvidence{Success: true},
			want:     ProbeOutcomeCompleteSuccess,
		},
		{
			name:     "completed HTTPS response is account attributable",
			evidence: ProbeEvidence{UpstreamURL: "https://api.openai.com/v1/responses", ResponseObserved: true},
			want:     ProbeOutcomeUpstreamFailure,
		},
		{
			name:     "framed diagnostic failure is neutral before response attribution",
			evidence: ProbeEvidence{UpstreamURL: "https://api.openai.com/v1/responses", ResponseObserved: true, FramingComplete: true},
			want:     ProbeOutcomeFramingCompleteNeutral,
		},
		{
			name:     "completed HTTP response is account attributable",
			evidence: ProbeEvidence{UpstreamURL: "http://localhost:8080/v1/chat/completions", ResponseObserved: true},
			want:     ProbeOutcomeUpstreamFailure,
		},
		{
			name:     "WHATWG HTTP URL with one slash remains account attributable",
			evidence: ProbeEvidence{UpstreamURL: "https:/api.example.test/v1/responses", ResponseObserved: true},
			want:     ProbeOutcomeUpstreamFailure,
		},
		{
			name:     "local synthetic URL is never account attributable",
			evidence: ProbeEvidence{UpstreamURL: "account:capacity_limited", ResponseObserved: true},
			want:     ProbeOutcomeTaskFailure,
		},
		{
			name:     "request without response header is not account attributable",
			evidence: ProbeEvidence{UpstreamURL: "https://api.openai.com/v1/responses"},
			want:     ProbeOutcomeTaskFailure,
		},
		{
			name:     "malformed URL is not account attributable",
			evidence: ProbeEvidence{UpstreamURL: "://not-a-url", ResponseObserved: true},
			want:     ProbeOutcomeTaskFailure,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyAutomaticProbeOutcome(tt.evidence); got != tt.want {
				t.Fatalf("ClassifyAutomaticProbeOutcome(%+v) = %q, want %q", tt.evidence, got, tt.want)
			}
		})
	}
}

func TestCooldownRetestEligibility(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Second)
	future := now.Add(time.Second)

	tests := []struct {
		name      string
		candidate CooldownCandidate
		want      bool
	}{
		{
			name:      "due temporary unavailable account in a group",
			candidate: CooldownCandidate{Status: StatusTemporaryUnavailable, Schedulable: true, BoundGroupID: "group-1", CooldownUntil: &past},
			want:      true,
		},
		{
			name:      "due rate limited account in a group",
			candidate: CooldownCandidate{Status: StatusRateLimited, Schedulable: true, BoundGroupID: "group-1", CooldownUntil: &past},
			want:      true,
		},
		{
			name:      "active account is not a cooldown retest candidate",
			candidate: CooldownCandidate{Status: StatusActive, Schedulable: true, BoundGroupID: "group-1", CooldownUntil: &past},
		},
		{
			name:      "unschedulable account is not a candidate",
			candidate: CooldownCandidate{Status: StatusTemporaryUnavailable, BoundGroupID: "group-1", CooldownUntil: &past},
		},
		{
			name:      "unbound account is not a candidate",
			candidate: CooldownCandidate{Status: StatusTemporaryUnavailable, Schedulable: true, CooldownUntil: &past},
		},
		{
			name:      "future cooldown is not due",
			candidate: CooldownCandidate{Status: StatusTemporaryUnavailable, Schedulable: true, BoundGroupID: "group-1", CooldownUntil: &future},
		},
		{
			name:      "expired account is not a candidate",
			candidate: CooldownCandidate{Status: StatusTemporaryUnavailable, Schedulable: true, BoundGroupID: "group-1", CooldownUntil: &past, ExpiresAt: &past},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CooldownRetestEligible(tt.candidate, now); got != tt.want {
				t.Fatalf("CooldownRetestEligible(%+v) = %v, want %v", tt.candidate, got, tt.want)
			}
		})
	}
}

func TestHealthCheckEligibility(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Second)
	future := now.Add(time.Second)

	tests := []struct {
		name      string
		candidate HealthCheckCandidate
		want      bool
	}{
		{name: "active due candidate", candidate: HealthCheckCandidate{Status: StatusActive, Schedulable: true, BoundGroupID: "group-1", NextCheckAt: &past}, want: true},
		{name: "pending test may be unschedulable", candidate: HealthCheckCandidate{Status: StatusPendingTest, BoundGroupID: "group-1"}, want: true},
		{name: "active unschedulable is skipped", candidate: HealthCheckCandidate{Status: StatusActive, BoundGroupID: "group-1"}},
		{name: "cooldown account is not a health check candidate", candidate: HealthCheckCandidate{Status: StatusRateLimited, Schedulable: true, BoundGroupID: "group-1"}},
		{name: "unbound account is skipped", candidate: HealthCheckCandidate{Status: StatusPendingTest}},
		{name: "expired account is skipped", candidate: HealthCheckCandidate{Status: StatusPendingTest, BoundGroupID: "group-1", ExpiresAt: &past}},
		{name: "future scheduled account is skipped", candidate: HealthCheckCandidate{Status: StatusPendingTest, BoundGroupID: "group-1", NextCheckAt: &future}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HealthCheckEligible(tt.candidate, now); got != tt.want {
				t.Fatalf("HealthCheckEligible(%+v) = %v, want %v", tt.candidate, got, tt.want)
			}
		})
	}
}

func TestCooldownRetestTaskCurrent(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	other := now.Add(time.Second)

	if !CooldownRetestTaskCurrent(RetestTaskVersion{ConfigRevision: 7, ObservationStartedAt: &now}, RetestTaskVersion{ConfigRevision: 7, ObservationStartedAt: &now}) {
		t.Fatal("matching config revision and observation must remain current")
	}
	if CooldownRetestTaskCurrent(RetestTaskVersion{ConfigRevision: 7, ObservationStartedAt: &now}, RetestTaskVersion{ConfigRevision: 8, ObservationStartedAt: &now}) {
		t.Fatal("config revision change must discard stale task")
	}
	if CooldownRetestTaskCurrent(RetestTaskVersion{ConfigRevision: 7, ObservationStartedAt: &now}, RetestTaskVersion{ConfigRevision: 7, ObservationStartedAt: &other}) {
		t.Fatal("observation generation change must discard stale task")
	}
	if CooldownRetestTaskCurrent(RetestTaskVersion{ConfigRevision: 7, ObservationStartedAt: &now}, RetestTaskVersion{ConfigRevision: 7}) {
		t.Fatal("cleared observation must discard stale task")
	}
}

func TestCooldownRetestActionForOutcome(t *testing.T) {
	t.Parallel()

	tests := map[ProbeOutcome]RetestAction{
		ProbeOutcomeCompleteSuccess: RetestActionRestore,
		ProbeOutcomeTaskFailure:     RetestActionDefer,
		ProbeOutcomeUpstreamFailure: RetestActionRecordFailure,
	}
	for outcome, want := range tests {
		got, ok := CooldownRetestActionFor(outcome)
		if !ok || got != want {
			t.Fatalf("CooldownRetestActionFor(%q) = (%q, %v), want (%q, true)", outcome, got, ok, want)
		}
	}
	if _, ok := CooldownRetestActionFor(ProbeOutcome("stale")); ok {
		t.Fatal("stale is an execution discard, not a persistence action")
	}
}
