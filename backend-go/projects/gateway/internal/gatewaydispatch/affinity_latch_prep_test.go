package gatewaydispatch

import (
	"context"
	"testing"
)

type claimOwnerAffinity struct {
	fakeAffinity
	owner      string
	orderCalls int
}

func (f *claimOwnerAffinity) ClaimAsync(ctx context.Context, sessionAffinityKey, proposedAccountID string, scope AffinityScope) (string, bool) {
	return f.owner, f.owner != ""
}

func (f *claimOwnerAffinity) OrderAsync(ctx context.Context, accounts []AccountCandidate, sessionAffinityKey string, options AffinityOrderingOptions) ([]AccountCandidate, error) {
	f.orderCalls++
	return accounts, nil
}

func TestPrepareDispatchAccountsDoesNotRePromoteBusyAffinityOwner(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	affinity := &claimOwnerAffinity{owner: "a-1"}
	engine.Affinity = affinity
	// a-1 is at its hard concurrency limit; a-2 is free. Capacity ordering puts
	// a-2 first; the claim returns a-1 but must not re-promote the busy owner.
	engine.Concurrency = &mapBackedConcurrencyStore{current: map[string]int{"a-1": 4}}
	input := dispatchPreparationInput(t, testAccounts("a-1", "a-2"))
	input.SessionAffinityKey = "aff-latch-prep"

	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if result.SessionAffinityOwnerAccountID != "a-1" {
		t.Fatalf("owner = %q, want a-1", result.SessionAffinityOwnerAccountID)
	}
	if !result.SessionAffinityOwnerBusy {
		t.Fatal("hard-busy owner must be reported to the final settlement")
	}
	if len(result.Accounts) != 2 || result.Accounts[0].ID != "a-2" {
		t.Fatalf("accounts = %v, want a-2 first", accountIDs(result.Accounts))
	}
	// Only the initial OrderAsync at preparation entry; the claim must not add
	// a second reorder for the busy owner.
	if affinity.orderCalls != 1 {
		t.Fatalf("order calls = %d, want 1 (initial only)", affinity.orderCalls)
	}
}
