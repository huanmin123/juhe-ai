package circuitcontrolplane

// w11c: incident validation branches, nil-slice persistence, closed-db error
// propagation and outbox lifecycle corners.

import (
	"context"
	"strings"
	"testing"
)

func w11cGate() OwnerGate {
	return OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}
}

func w11cBaseMutation() IncidentMutation {
	incidentID := "w11c-inc"
	return IncidentMutation{Incident: Incident{
		CircuitScopeKey: "w11c-scope", AccountID: "a1", AccountRuntimeKey: "a1", ScopeKind: "account",
		IncidentID: &incidentID, State: "OPEN", Generation: 1, DispatchRevision: 1,
		TransitionID: "w11c-tr", ConfirmationFailuresRequired: 1,
		ChildIncidentIDs: []string{}, ConfirmationFailureEvidenceKeys: []string{},
		CreatedAtMS: 100, UpdatedAtMS: 100,
	}}
}

func TestW11CValidateIncidentOptionalBranches(t *testing.T) {
	// Key fingerprint bound.
	in := w11cBaseMutation()
	in.CircuitScopeKey = "w11c-scope-fp"
	in.ScopeKind = "key"
	bad := strings.Repeat("f", 300)
	in.KeyFingerprint = &bad
	if err := validateIncident(&in.Incident); err == nil && in.KeyFingerprint != nil {
		t.Fatalf("oversized key fingerprint must fail")
	}
	// Protocol code / request lane / model family bounds.
	in = w11cBaseMutation()
	in.ScopeKind = "protocol_model"
	in.CircuitScopeKey = "w11c-scope-pm"
	in.ProtocolCode = ptr(strings.Repeat("p", 100))
	if err := validateIncident(&in.Incident); err == nil {
		t.Fatalf("oversized protocol code must fail")
	}
	in.ProtocolCode = ptr("openai")
	in.RequestLane = ptr(strings.Repeat("l", 100))
	if err := validateIncident(&in.Incident); err == nil {
		t.Fatalf("oversized request lane must fail")
	}
	in.RequestLane = ptr("text")
	in.ModelFamily = ptr(strings.Repeat("m", 300))
	if err := validateIncident(&in.Incident); err == nil {
		t.Fatalf("oversized model family must fail")
	}
	in.ModelFamily = ptr("gpt")
	// Lease / parent / terminal outcome bounds.
	in.LeaseID = ptr(strings.Repeat("l", 300))
	if err := validateIncident(&in.Incident); err == nil {
		t.Fatalf("oversized lease id must fail")
	}
	in.LeaseID = ptr("lease")
	in.LeaseOwnerRunID = ptr(strings.Repeat("o", 300))
	if err := validateIncident(&in.Incident); err == nil {
		t.Fatalf("oversized lease owner must fail")
	}
	in.LeaseOwnerRunID = ptr("owner")
	in.ParentIncidentID = ptr(strings.Repeat("p", 300))
	if err := validateIncident(&in.Incident); err == nil {
		t.Fatalf("oversized parent incident must fail")
	}
	in.ParentIncidentID = nil
	in.CausedByTerminalOutcomeID = ptr(strings.Repeat("c", 300))
	if err := validateIncident(&in.Incident); err == nil {
		t.Fatalf("oversized terminal outcome must fail")
	}
	in.CausedByTerminalOutcomeID = nil
	// Key-model optional fields.
	in.ScopeKind = "key_model"
	in.CircuitScopeKey = "w11c-scope-km"
	in.ClientModel = ptr(strings.Repeat("c", 300))
	if err := validateIncident(&in.Incident); err == nil {
		t.Fatalf("oversized client model must fail")
	}
	in.ClientModel = ptr("m")
	// Runtime key validation.
	in.AccountRuntimeKey = strings.Repeat("k", 2000)
	if err := validateIncident(&in.Incident); err == nil {
		t.Fatalf("oversized runtime key must fail")
	}
}

func TestW11CIncidentNilSlicesPersist(t *testing.T) {
	s, _ := testStore(t, w11cGate())
	ctx := context.Background()
	in := w11cBaseMutation()
	in.CircuitScopeKey = "w11c-nil-slices"
	in.ChildIncidentIDs = nil
	in.ConfirmationFailureEvidenceKeys = nil
	result, err := s.CompareAndSetIncident(ctx, in)
	if err != nil || result.Status != "applied" {
		t.Fatalf("nil slices result = (%s, %v)", result.Status, err)
	}
	loaded, found, err := s.GetIncident(ctx, "w11c-nil-slices")
	if err != nil || !found {
		t.Fatalf("get = (%v, %v)", found, err)
	}
	if loaded.ChildIncidentIDs == nil || loaded.ConfirmationFailureEvidenceKeys == nil {
		t.Fatalf("nil slices must persist as empty arrays: %+v", loaded)
	}
}

func TestW11CAdvanceDispatchRevisionReplay(t *testing.T) {
	s, _ := testStore(t, w11cGate())
	ctx := context.Background()
	first, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "w11c-tr-1", NowMS: 100})
	if err != nil || first.Status != "applied" || first.DispatchRevision != 2 {
		t.Fatalf("first = (%+v, %v)", first, err)
	}
	// Replaying the same transition id is idempotent.
	replay, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "w11c-tr-1", NowMS: 200})
	if err != nil || replay.Status != "idempotent" || replay.DispatchRevision != 2 {
		t.Fatalf("replay = (%+v, %v)", replay, err)
	}
	// A fresh transition advances again.
	second, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "w11c-tr-2", NowMS: 300})
	if err != nil || second.Status != "applied" || second.DispatchRevision != 3 {
		t.Fatalf("second = (%+v, %v)", second, err)
	}
}

func TestW11CClosedDBErrorPropagation(t *testing.T) {
	s, db := testStore(t, w11cGate())
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "w11c-tr", NowMS: 1}); err == nil {
		t.Fatalf("advance on closed db must fail")
	}
	if _, err := s.ListDispatchRevisions(ctx, "", 10); err == nil {
		t.Fatalf("list revisions on closed db must fail")
	}
	if _, err := s.LoadIncidentForProjection(ctx, Outbox{EventID: "w11c-e", ProjectionKey: ProjectionKey}); err == nil {
		t.Fatalf("projection load on closed db must fail")
	}
	if _, err := s.CompareAndSetIncident(ctx, w11cBaseMutation()); err == nil {
		t.Fatalf("cas on closed db must fail")
	}
	if _, _, err := s.GetIncident(ctx, "w11c-scope"); err == nil {
		t.Fatalf("get incident on closed db must fail")
	}
	if _, err := s.ClaimOutbox(ctx, "w11c-owner", 1, 10, 5); err == nil {
		t.Fatalf("claim on closed db must fail")
	}
	if _, err := s.AcknowledgeOutbox(ctx, "w11c-e", ProjectionKey, "token", 1); err == nil {
		t.Fatalf("ack on closed db must fail")
	}
	if _, err := s.ReleaseOutboxForReplay(ctx, "w11c-e", "token", "class", 1, 1); err == nil {
		t.Fatalf("release on closed db must fail")
	}
	if _, err := s.ListForRebuild(ctx, 1, 0, "", 10); err == nil {
		t.Fatalf("rebuild list on closed db must fail")
	}
	if _, err := s.ListByRuntimeKeys(ctx, []string{"a1"}, true, 1); err == nil {
		t.Fatalf("runtime list on closed db must fail")
	}
	if _, err := s.ListProjectionGaps(ctx, "", 0, "", 10); err == nil {
		t.Fatalf("gaps on closed db must fail")
	}
	if _, err := s.Cleanup(ctx, 1, 1, 10); err == nil {
		t.Fatalf("cleanup on closed db must fail")
	}
	if err := s.CheckContract(ctx); err == nil {
		t.Fatalf("contract check on closed db must fail")
	}
}

func TestW11CCheckContractIndexBranches(t *testing.T) {
	s, db := testStore(t, w11cGate())
	ctx := context.Background()
	// A missing key-model capability index fails the contract check.
	if _, err := db.ExecContext(ctx, "DROP INDEX idx_account_circuit_incidents_key_model_capability"); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckContract(ctx); err == nil {
		t.Fatalf("missing capability index must fail")
	}
	// Recreate it as a full (non-partial) index: the predicate check fails.
	if _, err := db.ExecContext(ctx, "CREATE UNIQUE INDEX idx_account_circuit_incidents_key_model_capability ON account_circuit_incidents(scope_kind, capability_hash)"); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckContract(ctx); err == nil {
		t.Fatalf("non-partial capability index must fail")
	}
}
