package circuitruntime

// w11c: runtime store validation branches, canary/escalation guards,
// revision projector and audit bound paths.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
)

func TestW11CRuntimeStoreInputValidation(t *testing.T) {
	clock := newW7BClock()
	_, rt := w7bReadyStore(t, clock, 0, time.Minute)
	ctx := context.Background()
	accountID := "w11c-acc"
	scope := w7bAccountScope(accountID)

	// Suspect validation.
	if _, err := rt.SuspectGatewayAccountCircuit(ctx, GatewayAccountCircuitSuspectInput{AccountID: "", Scope: scope, DispatchRevision: 1, TransitionID: "t", Reason: "r"}); err == nil {
		t.Fatalf("empty account id must fail")
	}
	if _, err := rt.SuspectGatewayAccountCircuit(ctx, GatewayAccountCircuitSuspectInput{AccountID: accountID, Scope: scope, DispatchRevision: 0, TransitionID: "t", Reason: "r"}); err == nil {
		t.Fatalf("zero revision must fail")
	}
	if _, err := rt.SuspectGatewayAccountCircuit(ctx, GatewayAccountCircuitSuspectInput{AccountID: accountID, Scope: scope, DispatchRevision: 1, TransitionID: " ", Reason: "r"}); err == nil {
		t.Fatalf("blank transition must fail")
	}
	// Replace revision validation.
	if _, err := rt.ReplaceGatewayAccountCircuitDispatchRevision(ctx, GatewayAccountCircuitReplaceDispatchRevisionInput{AccountID: accountID, Scope: scope, DispatchRevision: -1, TransitionID: "t"}); err == nil {
		t.Fatalf("negative revision must fail")
	}
	// Canary completion validation.
	if _, err := rt.CompleteGatewayAccountCircuitCanary(ctx, GatewayAccountCircuitCompleteCanaryInput{
		GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, "t"),
		LeaseID: "lease", Outcome: GatewayAccountCircuitCompletionOutcome("bogus"),
	}); err == nil {
		t.Fatalf("invalid outcome must fail")
	}
	if _, err := rt.CompleteGatewayAccountCircuitCanary(ctx, GatewayAccountCircuitCompleteCanaryInput{
		GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, "t"),
		LeaseID: "lease", Outcome: GatewayAccountCircuitCompletionUnknown, Reason: strings.Repeat("r", 2000),
	}); err == nil {
		t.Fatalf("oversized reason must fail")
	}
	if _, err := rt.CompleteGatewayAccountCircuitCanary(ctx, GatewayAccountCircuitCompleteCanaryInput{
		GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, "t"),
		LeaseID: "lease", Outcome: GatewayAccountCircuitCompletionUnknown, EvidenceScopeKey: strings.Repeat("k", 3000),
	}); err == nil {
		t.Fatalf("oversized evidence scope key must fail")
	}
	// Escalation validation.
	if _, err := rt.RecordGatewayAccountCircuitProtocolModelOpenEvidence(ctx, GatewayAccountCircuitProtocolModelOpenEvidenceInput{
		AccountID: accountID, Scope: w7bProtocolScope(accountID, "m"), Generation: 1, DispatchRevision: 1,
		EvidenceID: "", AccountTransitionID: "p", Reason: "r", MaxProtocolScopes: 8, Window: time.Minute,
	}); err == nil {
		t.Fatalf("empty evidence id must fail")
	}
	// Clear escalation validation.
	if _, err := rt.ClearGatewayAccountCircuitEscalationEvidence(ctx, GatewayAccountCircuitClearAccountEscalationEvidenceInput{
		AccountID: accountID, AccountRuntimeKey: accountID, DispatchRevision: 0, EvidenceID: "e",
	}); err == nil {
		t.Fatalf("zero revision must fail")
	}
	// Account revision replacement validation.
	if _, err := rt.ReplaceGatewayAccountCircuitAccountDispatchRevision(ctx, GatewayAccountCircuitReplaceAccountDispatchRevisionInput{
		AccountID: "", DispatchRevision: 2, TransitionID: "t",
	}); err == nil {
		t.Fatalf("empty account id must fail")
	}
	// List due validation.
	if _, err := rt.ListDueGatewayAccountCircuits(ctx, GatewayAccountCircuitListDueInput{Limit: 0}); err == nil {
		t.Fatalf("zero limit must fail")
	}
	// Restore validation.
	if _, err := rt.RestoreGatewayAccountCircuit(ctx, GatewayAccountCircuitRestoreInput{
		AccountID: accountID, State: GatewayAccountCircuitState{Scope: scope, Phase: GatewayAccountCircuitPhaseSuspect, DispatchRevision: 0},
	}); err == nil {
		t.Fatalf("restore without revision must fail")
	}
	// Get validation.
	if _, err := rt.GetGatewayAccountCircuit(ctx, GatewayAccountCircuitGetInput{AccountID: "", Scope: scope}); err == nil {
		t.Fatalf("get without account must fail")
	}
}

func TestW11CRuntimeCanaryLifecycleBranches(t *testing.T) {
	clock := newW7BClock()
	_, rt := w7bReadyStore(t, clock, 0, time.Minute)
	ctx := context.Background()
	accountID := "w11c-canary"
	scope := w7bOpenProtocolScope(t, rt, clock, accountID, "w11c-bucket")

	// Acquire a canary lease and complete it with a valid outcome; the
	// canary transport failure re-opens the circuit.
	lease, err := rt.AcquireGatewayAccountCircuitCanaryLease(ctx, GatewayAccountCircuitAcquireCanaryLeaseInput{
		GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 2, 1, "w11c-canary-acquire"),
		LeaseID: "w11c-canary-lease", LeaseUntil: clock.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("canary acquire: %v", err)
	}
	if lease.Status == "" {
		t.Fatalf("empty status")
	}
	completed, err := rt.CompleteGatewayAccountCircuitCanary(ctx, GatewayAccountCircuitCompleteCanaryInput{
		GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 2, 1, "w11c-canary-complete"),
		LeaseID: "w11c-canary-lease", Outcome: GatewayAccountCircuitCompletionTransportFailure, Reason: "w11c-upstream",
	})
	if err != nil {
		t.Fatalf("canary complete: %v", err)
	}
	if completed.State.Phase != GatewayAccountCircuitPhaseOpen {
		t.Fatalf("canary transport should re-open, got %s", completed.State.Phase)
	}
	// A canary with a framing outcome and evidence scope key recovers.
	recoveredLease, err := rt.AcquireGatewayAccountCircuitCanaryLease(ctx, GatewayAccountCircuitAcquireCanaryLeaseInput{
		GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, completed.State.Generation, 1, "w11c-canary-acquire-2"),
		LeaseID: "w11c-canary-lease-2", LeaseUntil: clock.Now().Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("canary acquire 2: %v", err)
	}
	recovered, err := rt.CompleteGatewayAccountCircuitCanary(ctx, GatewayAccountCircuitCompleteCanaryInput{
		GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, recoveredLease.State.Generation, 1, "w11c-canary-complete-2"),
		LeaseID: "w11c-canary-lease-2", Outcome: GatewayAccountCircuitCompletionFramingComplete, EvidenceScopeKey: "w11c-evidence-scope",
	})
	if err != nil {
		t.Fatalf("canary framing: %v", err)
	}
	if recovered.State.Phase == "" {
		t.Fatalf("empty phase")
	}
	// Lease deadline validation on the canary acquire.
	if _, err := rt.AcquireGatewayAccountCircuitCanaryLease(ctx, GatewayAccountCircuitAcquireCanaryLeaseInput{
		GatewayAccountCircuitTransitionIdentity: w7bIdentity(accountID, scope, 1, 1, "w11c-bad"),
		LeaseID: " ", LeaseUntil: clock.Now().Add(time.Minute),
	}); err == nil {
		t.Fatalf("blank lease id must fail")
	}
}

func TestW11CRuntimeEscalationAndClear(t *testing.T) {
	clock := newW7BClock()
	_, rt := w7bReadyStore(t, clock, 0, time.Minute)
	ctx := context.Background()
	accountID := "w11c-esc"
	scope := w7bOpenProtocolScope(t, rt, clock, accountID, "w11c-esc-bucket")
	openState, err := rt.GetGatewayAccountCircuit(ctx, GatewayAccountCircuitGetInput{AccountID: accountID, Scope: scope})
	if err != nil || openState.Phase != GatewayAccountCircuitPhaseOpen {
		t.Fatalf("open state = (%+v, %v)", openState, err)
	}
	// Record open evidence below the distinct-scope threshold.
	escalation, err := rt.RecordGatewayAccountCircuitProtocolModelOpenEvidence(ctx, GatewayAccountCircuitProtocolModelOpenEvidenceInput{
		AccountID: accountID, Scope: scope, Generation: openState.Generation, DispatchRevision: 1,
		EvidenceID: "w11c-ev-1", AccountTransitionID: "w11c-parent", Reason: "w11c-transport",
		ConfirmedFailureCount: 1, MaxProtocolScopes: 8, Window: time.Minute,
	})
	if err != nil {
		t.Fatalf("escalation record: %v", err)
	}
	if escalation.Status == "" || escalation.AccountState.Phase == "" {
		t.Fatalf("escalation result = %+v", escalation)
	}
	// Clearing the escalation evidence completes the Lua round trip; the
	// stored evidence revision governs whether the clear reports true.
	cleared, err := rt.ClearGatewayAccountCircuitEscalationEvidence(ctx, GatewayAccountCircuitClearAccountEscalationEvidenceInput{
		AccountID: accountID, AccountRuntimeKey: accountID, DispatchRevision: 1, EvidenceID: "w11c-clear",
	})
	if err != nil {
		t.Fatalf("clear = (%v, %v)", cleared, err)
	}
}

func TestW11CRevisionProjectorBranches(t *testing.T) {
	clock := newW7BClock()
	store, rt := w7bReadyStore(t, clock, 0, time.Minute)
	_ = rt
	ctx := context.Background()
	// Invalid retention is rejected.
	if _, err := NewAccountCircuitRevisionProjector(store.client, -time.Minute); err == nil {
		t.Fatalf("negative retention must fail")
	}
	projector, err := NewAccountCircuitRevisionProjector(store.client, time.Minute)
	if err != nil {
		t.Fatalf("projector: %v", err)
	}
	accountID := "w11c-proj"
	scope := w7bAccountScope(accountID)
	if _, err := rt.SuspectGatewayAccountCircuit(ctx, GatewayAccountCircuitSuspectInput{AccountID: accountID, Scope: scope, DispatchRevision: 1, TransitionID: "w11c-t1", Reason: "x", Now: clock.Now()}); err != nil {
		t.Fatal(err)
	}
	event := func(revision int64) GatewayAccountCircuitOutboxEvent {
		return GatewayAccountCircuitOutboxEvent{EventID: "w11c-e", ProjectionKey: GatewayAccountCircuitProjectionKey, EventType: GatewayAccountCircuitDispatchRevisionChanged, AccountID: accountID, AccountRuntimeKey: accountID, TransitionID: "w11c-tp", DispatchRevision: revision}
	}
	if _, err := projector.ProjectGatewayAccountCircuitRevision(ctx, event(0)); err == nil {
		t.Fatalf("zero revision must fail")
	}
	// An unknown account is recorded without closing anything.
	applied, err := projector.ProjectGatewayAccountCircuitRevision(ctx, event(5))
	if err != nil || applied.Status != GatewayAccountCircuitRevisionApplied || applied.ClosedStates != 1 {
		t.Fatalf("applied = %+v err=%v", applied, err)
	}
}

func TestW11CRuntimeIndexAuditBounds(t *testing.T) {
	clock := newW7BClock()
	server := miniredis.RunT(t)
	store, err := New(Config{URL: "redis://" + server.Addr(), Namespace: "w11c-audit", Retention: time.Minute}, w7bGate())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	backfiller, err := NewAccountCircuitRuntimeIndexBackfiller(store.client)
	if err != nil {
		t.Fatal(err)
	}
	reader := DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{}, nil
	})
	// A pre-populated states entry forces the audit to read and compare.
	scopeKey := mustGatewayAccountCircuitScopeKey(w7bAccountScope("w11c-audit-acc"))
	entry := map[string]any{
		"state": map[string]any{
			"scopeKey": scopeKey,
			"scope":    map[string]any{"kind": "account", "accountRuntimeKey": "w11c-audit-acc"},
			"phase":    "SUSPECT", "generation": 1, "dispatchRevision": 1, "transitionId": "w11c-t",
			"updatedAt": clock.Now().UnixMilli(),
		},
	}
	raw, _ := json.Marshal(entry)
	if err := store.client.client.HSet(context.Background(), backfiller.keys.states, scopeKey, raw).Err(); err != nil {
		t.Fatal(err)
	}
	// Audit data bound: MaxBytes is clamped by validation to >= 1024, so use
	// MaxFields = 1 to exceed the bound with one entry.
	if _, err := backfiller.WithDispatchRevisionReader(reader).BackfillGatewayAccountCircuitRuntimeIndex(context.Background(), GatewayAccountCircuitRuntimeIndexBackfillInput{
		OwnerID: "w11c-owner", MaxFields: 1,
	}); err == nil {
		t.Fatalf("expected audit data bound error")
	}
}

func TestW11CUniqueSortedRuntimeStrings(t *testing.T) {
	values := uniqueSortedRuntimeStrings([]string{"b", "a", "b", "c", "a"})
	if len(values) != 3 || values[0] != "a" || values[2] != "c" {
		t.Fatalf("unique sort = %v", values)
	}
	if !isUniqueSortedRuntimeStrings([]string{"a", "b"}) || isUniqueSortedRuntimeStrings([]string{"b", "a"}) || isUniqueSortedRuntimeStrings([]string{"a", "a"}) {
		t.Fatalf("isUniqueSortedRuntimeStrings misbehaves")
	}
}
