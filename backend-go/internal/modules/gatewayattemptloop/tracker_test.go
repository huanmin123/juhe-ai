package gatewayattemptloop

import "testing"

func TestAttemptTrackerBlocksAliasButAllowsSameRuntimeRotation(t *testing.T) {
	t.Parallel()
	tracker := NewAttemptTracker()
	first := apiKeyCandidate("view-a", []int{0, 1})
	first.Projection.ResourceAccountID = "source"
	if !tracker.Claim(first, 0, replaySafeRequest()) || !tracker.Claim(first, 1, replaySafeRequest()) {
		t.Fatal("same runtime key rotation was blocked")
	}
	alias := apiKeyCandidate("view-b", []int{2})
	alias.Projection.ResourceAccountID = "source"
	if tracker.CanClaim(alias, 2, replaySafeRequest()) {
		t.Fatal("shared physical source alias was allowed")
	}
}

func TestAttemptTrackerDoesNotClaimBeforeRecord(t *testing.T) {
	t.Parallel()
	tracker := NewAttemptTracker()
	candidate := apiKeyCandidate("account", []int{0})
	if !tracker.CanClaim(candidate, 0, replaySafeRequest()) || !tracker.CanClaim(candidate, 0, replaySafeRequest()) {
		t.Fatal("read-only eligibility mutated tracker")
	}
	if !tracker.Claim(candidate, 0, replaySafeRequest()) || tracker.CanClaim(candidate, 0, replaySafeRequest()) {
		t.Fatal("claim did not fence duplicate")
	}
}
