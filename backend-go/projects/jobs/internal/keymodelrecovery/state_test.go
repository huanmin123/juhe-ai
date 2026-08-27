package keymodelrecovery

import (
	"testing"
	"time"
)

func key() CapabilityKey {
	return CapabilityKey{CredentialSourceAccountID: "source-a", KeyFingerprint: "fingerprint-a", ClientModel: "B", ClientEndpointFamily: "chat", FinalUpstreamModel: "b-upstream", UpstreamEndpointMode: "chat_json", DispatchRevision: 9}
}

func TestStateLifecycleAndRecoveryGap(t *testing.T) {
	now := time.Unix(1_000, 0).UTC()
	state, err := NewOpen(key(), now)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(5 * time.Second); !state.RetryAt.Equal(want) {
		t.Fatalf("first open retry=%s want %s", state.RetryAt, want)
	}
	if got := []time.Duration{Backoff(1), Backoff(2), Backoff(3), Backoff(4)}; got[0] != 5*time.Second || got[1] != 15*time.Second || got[2] != time.Minute || got[3] != 5*time.Minute {
		t.Fatalf("backoff=%v", got)
	}
	now = state.RetryAt
	state, _ = Acquire(state, 1, 9, "lease-1", now)
	state, _ = Settle(state, RecoveryResult{Generation: 1, DispatchRevision: 9, LeaseID: "lease-1", Outcome: CompleteSuccess, ObservedAt: now})
	if state.Phase != Recovering || state.RecoverySuccessCount != 1 {
		t.Fatalf("first success=%#v", state)
	}
	first := state.LastRecoverySuccessAt
	// Queueing does not mutate success evidence.
	now = now.Add(90 * time.Second)
	if state.RecoverySuccessCount != 1 || !state.LastRecoverySuccessAt.Equal(first) {
		t.Fatal("queue delay reset recovery evidence")
	}
	state, _ = Acquire(state, 1, 9, "lease-2", now)
	state, _ = Settle(state, RecoveryResult{Generation: 1, DispatchRevision: 9, LeaseID: "lease-2", Outcome: CompleteSuccess, ObservedAt: now})
	if state.RecoverySuccessCount != 2 {
		t.Fatalf("within max gap count=%d", state.RecoverySuccessCount)
	}
	now = now.Add(RecoverySuccessMaxGap + time.Nanosecond)
	state, _ = Acquire(state, 1, 9, "lease-3", now)
	state, _ = Settle(state, RecoveryResult{Generation: 1, DispatchRevision: 9, LeaseID: "lease-3", Outcome: CompleteSuccess, ObservedAt: now})
	if state.Phase != Recovering || state.RecoverySuccessCount != 1 {
		t.Fatalf("long gap must restart sequence: %#v", state)
	}
	if _, status := Settle(state, RecoveryResult{Generation: 0, DispatchRevision: 9, LeaseID: "old", Outcome: UpstreamNotComplete, ObservedAt: now}); status != Stale {
		t.Fatalf("late result status=%s", status)
	}
}

func TestFailureUnknownAndContinuationPriority(t *testing.T) {
	now := time.Unix(2_000, 0).UTC()
	state, _ := NewOpen(key(), now)
	now = state.RetryAt
	state, _ = Acquire(state, 1, 9, "lease", now)
	state, _ = Settle(state, RecoveryResult{Generation: 1, DispatchRevision: 9, LeaseID: "lease", Outcome: UpstreamNotComplete, ObservedAt: now})
	if state.Phase != Open || state.RetryAt.Sub(now) != 15*time.Second {
		t.Fatalf("second backoff=%#v", state)
	}
	state, _ = Acquire(state, 1, 9, "wrong-time", now)
	if state.Phase == HalfOpen {
		t.Fatal("not-due state acquired lease")
	}
	continuation := state
	continuation.Phase, continuation.RetryAt, continuation.RecoverySuccessCount = Recovering, now, 1
	open := state
	open.RetryAt = now
	ordered := PrioritizeDue([]Due{{State: open, SourceID: "s"}, {State: continuation, SourceID: "s"}}, now)
	if len(ordered) != 2 || ordered[0].State.Phase != Recovering {
		t.Fatalf("continuation starved: %#v", ordered)
	}
	// At 24 running probes, a due continuation consumes the reserved capacity;
	// an OPEN probe cannot take it before the continuation starts.
	running := make([]Running, 24)
	for i := range running {
		running[i] = Running{SourceID: "other"}
	}
	selected := SelectDue([]Due{{State: open, SourceID: "new"}, {State: continuation, SourceID: "continuation"}}, running, now)
	if len(selected) != 1 || selected[0].State.Phase != Recovering {
		t.Fatalf("continuation reserve violated: %#v", selected)
	}
	// Unknown after a valid lease preserves the recovery sequence.
	continuation.Lease = &Lease{ID: "unknown", Until: now.Add(ProbeLease)}
	continuation.Phase = HalfOpen
	preserved, status := Settle(continuation, RecoveryResult{Generation: continuation.Generation, DispatchRevision: continuation.DispatchRevision, LeaseID: "unknown", Outcome: Unknown, ObservedAt: now})
	if status != Applied || preserved.Phase != Recovering || preserved.RecoverySuccessCount != 1 {
		t.Fatalf("unknown changed recovery evidence: %#v", preserved)
	}
}

func TestCapabilityIsolation(t *testing.T) {
	a, err := Hash(key())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Hash(CapabilityKey{CredentialSourceAccountID: "source-a", KeyFingerprint: "fingerprint-b", ClientModel: "B", ClientEndpointFamily: "chat", FinalUpstreamModel: "b-upstream", UpstreamEndpointMode: "chat_json", DispatchRevision: 9})
	c, _ := Hash(CapabilityKey{CredentialSourceAccountID: "source-a", KeyFingerprint: "fingerprint-a", ClientModel: "C", ClientEndpointFamily: "chat", FinalUpstreamModel: "c-upstream", UpstreamEndpointMode: "chat_json", DispatchRevision: 9})
	if a == b || a == c {
		t.Fatalf("capability keys collided: %s %s %s", a, b, c)
	}
}
