package accounthealth

import (
	"testing"
	"time"
)

func TestClassifyAutomaticProbeOutcome(t *testing.T) {
	t.Parallel()

	realHTTPS := "https://api.openai.com/v1/responses"
	realHTTP := "http://localhost:8080/v1/chat/completions"
	realAttempt := NewProbeUpstreamAttempt

	tests := []struct {
		name     string
		evidence ProbeEvidence
		want     ProbeOutcome
	}{
		{
			name: "completed real response succeeds",
			evidence: ProbeEvidence{
				Success:         true,
				UpstreamAttempt: realAttempt(realHTTPS),
				Transport:       FramingCompleteTransport(200),
			},
			want: ProbeOutcomeCompleteSuccess,
		},
		{
			name: "completed authentication failure is business-neutral",
			evidence: ProbeEvidence{
				UpstreamAttempt: realAttempt(realHTTPS),
				Transport:       FramingCompleteTransport(401),
			},
			want: ProbeOutcomeFramingCompleteNeutral,
		},
		{
			name: "completed rate-limit response is business-neutral",
			evidence: ProbeEvidence{
				UpstreamAttempt: realAttempt(realHTTPS),
				Transport:       FramingCompleteTransport(429),
			},
			want: ProbeOutcomeFramingCompleteNeutral,
		},
		{
			name: "completed server error response is business-neutral",
			evidence: ProbeEvidence{
				UpstreamAttempt: realAttempt(realHTTP),
				Transport:       FramingCompleteTransport(503),
			},
			want: ProbeOutcomeFramingCompleteNeutral,
		},
		{
			name: "connection failure before headers is account-attributable",
			evidence: ProbeEvidence{
				UpstreamAttempt: realAttempt(realHTTPS),
				Transport:       IncompleteTransport(ProbeTransportFailureConnection),
			},
			want: ProbeOutcomeUpstreamFailure,
		},
		{
			name: "timeout before response is account-attributable",
			evidence: ProbeEvidence{
				UpstreamAttempt: realAttempt(realHTTPS),
				Transport:       IncompleteTransport(ProbeTransportFailureTimeout),
			},
			want: ProbeOutcomeUpstreamFailure,
		},
		{
			name: "read interruption after headers is account-attributable",
			evidence: ProbeEvidence{
				UpstreamAttempt: realAttempt(realHTTPS),
				Transport: ProbeTransportEvidence{
					kind:             probeTransportIncomplete,
					transportFailure: ProbeTransportFailureRead,
					statusCode:       200,
				},
			},
			want: ProbeOutcomeUpstreamFailure,
		},
		{
			name: "transport evidence wins over contradictory success flag",
			evidence: ProbeEvidence{
				Success:         true,
				UpstreamAttempt: realAttempt(realHTTPS),
				Transport:       IncompleteTransport(ProbeTransportFailureRead),
			},
			want: ProbeOutcomeUpstreamFailure,
		},
		{
			name: "WHATWG HTTP URL with one slash remains a real attempt",
			evidence: ProbeEvidence{
				UpstreamAttempt: realAttempt("https:/api.example.test/v1/responses"),
				Transport:       IncompleteTransport(ProbeTransportFailureConnection),
			},
			want: ProbeOutcomeUpstreamFailure,
		},
		{
			name: "canceled real attempt is a task failure",
			evidence: ProbeEvidence{
				UpstreamAttempt: realAttempt(realHTTPS),
				Transport:       UnknownTransport(ProbeTaskFailureCanceled),
			},
			want: ProbeOutcomeTaskFailure,
		},
		{
			name: "cancellation wins over contradictory success flag",
			evidence: ProbeEvidence{
				Success:         true,
				UpstreamAttempt: realAttempt(realHTTPS),
				Transport:       UnknownTransport(ProbeTaskFailureCanceled),
			},
			want: ProbeOutcomeTaskFailure,
		},
		{
			name: "executor failure after constructing a real URL is a task failure",
			evidence: ProbeEvidence{
				UpstreamAttempt: realAttempt(realHTTPS),
				Transport:       UnknownTransport(ProbeTaskFailureExecutor),
			},
			want: ProbeOutcomeTaskFailure,
		},
		{
			name: "no real upstream attempt is a task failure",
			evidence: ProbeEvidence{
				Transport: IncompleteTransport(ProbeTransportFailureConnection),
			},
			want: ProbeOutcomeTaskFailure,
		},
		{
			name: "local synthetic error is a task failure",
			evidence: ProbeEvidence{
				UpstreamAttempt: realAttempt("account:capacity_limited"),
				Transport:       IncompleteTransport(ProbeTransportFailureConnection),
			},
			want: ProbeOutcomeTaskFailure,
		},
		{
			name: "malformed attempted URL is a task failure",
			evidence: ProbeEvidence{
				UpstreamAttempt: realAttempt("://not-a-url"),
				Transport:       IncompleteTransport(ProbeTransportFailureTimeout),
			},
			want: ProbeOutcomeTaskFailure,
		},
		{
			name: "success without a real attempt is a task failure",
			evidence: ProbeEvidence{
				Success:   true,
				Transport: FramingCompleteTransport(200),
			},
			want: ProbeOutcomeTaskFailure,
		},
		{
			name: "zero status cannot prove complete framing",
			evidence: ProbeEvidence{
				UpstreamAttempt: realAttempt(realHTTPS),
				Transport:       FramingCompleteTransport(0),
			},
			want: ProbeOutcomeTaskFailure,
		},
		{
			name: "non-three-digit status cannot prove complete framing",
			evidence: ProbeEvidence{
				UpstreamAttempt: realAttempt(realHTTPS),
				Transport:       FramingCompleteTransport(1000),
			},
			want: ProbeOutcomeTaskFailure,
		},
		{
			name: "framing and failure evidence cannot coexist",
			evidence: ProbeEvidence{
				UpstreamAttempt: realAttempt(realHTTPS),
				Transport: ProbeTransportEvidence{
					kind:             probeTransportFramingComplete,
					transportFailure: ProbeTransportFailureRead,
					statusCode:       200,
				},
			},
			want: ProbeOutcomeTaskFailure,
		},
		{
			name: "task failure marker cannot coexist with transport-incomplete",
			evidence: ProbeEvidence{
				UpstreamAttempt: realAttempt(realHTTPS),
				Transport: ProbeTransportEvidence{
					kind:             probeTransportIncomplete,
					transportFailure: ProbeTransportFailureConnection,
					taskFailure:      ProbeTaskFailureCanceled,
				},
			},
			want: ProbeOutcomeTaskFailure,
		},
		{
			name: "unknown transport failure kind is rejected",
			evidence: ProbeEvidence{
				UpstreamAttempt: realAttempt(realHTTPS),
				Transport:       IncompleteTransport(ProbeTransportFailureKind(255)),
			},
			want: ProbeOutcomeTaskFailure,
		},
		{
			name: "missing incomplete failure kind is rejected",
			evidence: ProbeEvidence{
				UpstreamAttempt: realAttempt(realHTTPS),
				Transport:       ProbeTransportEvidence{kind: probeTransportIncomplete},
			},
			want: ProbeOutcomeTaskFailure,
		},
		{
			name: "unknown transport remains a task failure regardless of failure label",
			evidence: ProbeEvidence{
				UpstreamAttempt: realAttempt(realHTTPS),
				Transport: ProbeTransportEvidence{
					kind:             probeTransportUnknown,
					transportFailure: ProbeTransportFailureConnection,
				},
			},
			want: ProbeOutcomeTaskFailure,
		},
		{
			name: "zero-value transport evidence is a task failure",
			evidence: ProbeEvidence{
				UpstreamAttempt: realAttempt(realHTTPS),
			},
			want: ProbeOutcomeTaskFailure,
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
