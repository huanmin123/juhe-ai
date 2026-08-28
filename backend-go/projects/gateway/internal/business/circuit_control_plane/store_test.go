package circuitcontrolplane

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "modernc.org/sqlite"
)

func testStore(t *testing.T, gate OwnerGate) (*Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:circuit-control-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ddls := []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY, dispatch_revision INTEGER NOT NULL DEFAULT 1, circuit_projection_revision INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE account_circuit_incidents (
 circuit_scope_key TEXT PRIMARY KEY, account_id TEXT NOT NULL, account_runtime_key TEXT NOT NULL, scope_kind TEXT NOT NULL,
 key_fingerprint TEXT, protocol_code TEXT, request_lane TEXT, model_family TEXT, incident_id TEXT NOT NULL,
 parent_incident_id TEXT, child_incident_ids_json TEXT NOT NULL, caused_by_terminal_outcome_id TEXT, state TEXT NOT NULL,
 failure_scope TEXT, generation INTEGER NOT NULL, dispatch_revision INTEGER NOT NULL, ledger_revision INTEGER NOT NULL,
 projected_ledger_revision INTEGER NOT NULL, transition_id TEXT NOT NULL, cooldown_observation_generation INTEGER NOT NULL,
 open_until_ms INTEGER, next_transition_at_ms INTEGER, lease_id TEXT, lease_purpose TEXT, lease_owner_run_id TEXT,
 lease_until_ms INTEGER, attempt_started_at_ms INTEGER, attempt_hard_deadline_ms INTEGER, upstream_attempt_observed INTEGER NOT NULL,
 backoff_level INTEGER NOT NULL, consecutive_failures INTEGER NOT NULL, confirmation_failures_required INTEGER NOT NULL,
 confirmation_failure_evidence_keys_json TEXT NOT NULL, recovering_successes INTEGER NOT NULL, last_failure_class TEXT,
 retained_until_ms INTEGER, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL)`,
		`CREATE TABLE account_circuit_outbox (
 event_id TEXT PRIMARY KEY, projection_key TEXT NOT NULL, dedupe_key TEXT NOT NULL UNIQUE, event_type TEXT NOT NULL,
 account_id TEXT NOT NULL, account_runtime_key TEXT NOT NULL, circuit_scope_key TEXT, incident_id TEXT, transition_id TEXT NOT NULL,
 dispatch_revision INTEGER NOT NULL, generation INTEGER, ledger_revision INTEGER, status TEXT NOT NULL, available_at_ms INTEGER NOT NULL,
 claim_token TEXT, claimed_by TEXT, claim_until_ms INTEGER, attempt_count INTEGER NOT NULL, last_error_class TEXT,
 acknowledged_at_ms INTEGER, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL)`,
	}
	for _, ddl := range ddls {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO accounts(id,dispatch_revision,circuit_projection_revision) VALUES ('a1',1,0)`); err != nil {
		t.Fatal(err)
	}
	s, err := New(db, SQLite, "", gate)
	if err != nil {
		t.Fatal(err)
	}
	return s, db
}

func TestOwnerGateBlocksWrites(t *testing.T) {
	s, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true})
	_, err := s.AdvanceDispatchRevision(context.Background(), DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "t1"})
	if !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("err=%v", err)
	}
	var revision int
	if err := db.QueryRow(`SELECT dispatch_revision FROM accounts WHERE id='a1'`).Scan(&revision); err != nil || revision != 1 {
		t.Fatalf("revision=%d err=%v", revision, err)
	}
}

func TestAdvanceReplayClaimAckAndRelease(t *testing.T) {
	s, _ := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	first, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "t1", NowMS: 100})
	if err != nil || first.Status != "applied" || first.DispatchRevision != 2 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	replay, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "t1", NowMS: 101})
	if err != nil || replay.Status != "idempotent" || replay.DispatchRevision != 2 {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	claimed, err := s.ClaimOutbox(ctx, "worker-1", 100, 20, 10)
	if err != nil || len(claimed) != 1 || claimed[0].Status != "processing" {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	ok, err := s.AcknowledgeOutbox(ctx, claimed[0].EventID, ProjectionKey, *claimed[0].ClaimToken, 110)
	if err != nil || !ok {
		t.Fatalf("ack=%v err=%v", ok, err)
	}
	claimedAgain, err := s.ClaimOutbox(ctx, "worker-2", 110, 20, 10)
	if err != nil || len(claimedAgain) != 0 {
		t.Fatalf("claimed again=%+v err=%v", claimedAgain, err)
	}
	if _, err = s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "t2", NowMS: 120}); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ClaimOutbox(ctx, "worker-3", 120, 20, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if ok, err := s.ReleaseOutboxForReplay(ctx, pending[0].EventID, "wrong-token", "upstream_timeout", 121, 5); err != nil || ok {
		t.Fatalf("stale release ok=%v err=%v", ok, err)
	}
	if ok, err := s.ReleaseOutboxForReplay(ctx, pending[0].EventID, *pending[0].ClaimToken, "upstream_timeout", 121, 5); err != nil || !ok {
		t.Fatalf("release ok=%v err=%v", ok, err)
	}
	if early, err := s.ClaimOutbox(ctx, "worker-4", 125, 20, 10); err != nil || len(early) != 0 {
		t.Fatalf("early replay=%+v err=%v", early, err)
	}
	if replayed, err := s.ClaimOutbox(ctx, "worker-4", 126, 20, 10); err != nil || len(replayed) != 1 || replayed[0].AttemptCount != 2 {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
}

func TestIncidentCASAndProjection(t *testing.T) {
	s, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	incidentID := "inc-1"
	in := IncidentMutation{Incident: Incident{CircuitScopeKey: "scope-1", AccountID: "a1", AccountRuntimeKey: "a1", ScopeKind: "account", IncidentID: &incidentID, State: "OPEN", Generation: 1, DispatchRevision: 1, TransitionID: "tr-1", ConfirmationFailuresRequired: 1, ChildIncidentIDs: []string{}, ConfirmationFailureEvidenceKeys: []string{}, CreatedAtMS: 100, UpdatedAtMS: 100}}
	result, err := s.CompareAndSetIncident(ctx, in)
	if err != nil || result.Status != "applied" || result.Incident == nil || result.Incident.LedgerRevision != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	stale := in
	stale.TransitionID = "tr-stale"
	stale.ExpectedLedgerRevision = ptr(int64(0))
	result, err = s.CompareAndSetIncident(ctx, stale)
	if err != nil || result.Status != "cas_conflict" {
		t.Fatalf("stale=%+v err=%v", result, err)
	}
	claimed, err := s.ClaimOutbox(ctx, "worker", 100, 20, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	if ok, err := s.AcknowledgeOutbox(ctx, claimed[0].EventID, ProjectionKey, *claimed[0].ClaimToken, 110); err != nil || !ok {
		t.Fatalf("ack=%v err=%v", ok, err)
	}
	var projected int
	if err := db.QueryRow(`SELECT projected_ledger_revision FROM account_circuit_incidents WHERE circuit_scope_key='scope-1'`).Scan(&projected); err != nil || projected != 1 {
		t.Fatalf("projected=%d err=%v", projected, err)
	}
}

func TestPostgresSchemaIsValidatedDefaultedAndQuoted(t *testing.T) {
	db := openTestDB(t)
	for _, schema := range []string{"bad.schema", "tenant;drop", "tenant space", "1tenant", `tenant"x`} {
		if _, err := New(db, Postgres, schema, OwnerGate{}); !errors.Is(err, ErrInvalidSchema) {
			t.Fatalf("schema %q err=%v", schema, err)
		}
	}
	store, err := New(db, Postgres, "", OwnerGate{})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.table("accounts"); got != `"juhe_business".accounts` {
		t.Fatalf("default schema qualification=%q", got)
	}
	store, err = New(db, Postgres, "tenant_1", OwnerGate{})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.table("accounts"); got != `"tenant_1".accounts` {
		t.Fatalf("qualified schema=%q", got)
	}
}

func TestCheckContractChecksCircuitColumns(t *testing.T) {
	store, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if _, err := db.Exec(`DROP TABLE account_circuit_incidents`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE account_circuit_incidents (circuit_scope_key TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckContract(context.Background()); err == nil {
		t.Fatal("CheckContract accepted a relation missing circuit columns")
	}
}

func TestNilIncidentSlicesPersistAsEmptyArrays(t *testing.T) {
	store, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	id := "nil-slices"
	result, err := store.CompareAndSetIncident(context.Background(), IncidentMutation{Incident: Incident{
		CircuitScopeKey: "scope-nil", AccountID: "a1", AccountRuntimeKey: "a1", ScopeKind: "account",
		IncidentID: &id, State: "OPEN", Generation: 0, DispatchRevision: 1, TransitionID: "nil-slices-transition",
		ConfirmationFailuresRequired: 1, UpdatedAtMS: 0,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Incident == nil || result.Incident.ChildIncidentIDs == nil || result.Incident.ConfirmationFailureEvidenceKeys == nil {
		t.Fatalf("result slices were not normalized: %+v", result.Incident)
	}
	var children, evidence string
	if err := db.QueryRow(`SELECT child_incident_ids_json,confirmation_failure_evidence_keys_json FROM account_circuit_incidents WHERE circuit_scope_key='scope-nil'`).Scan(&children, &evidence); err != nil {
		t.Fatal(err)
	}
	if children != `[]` || evidence != `[]` {
		t.Fatalf("persisted children=%q evidence=%q", children, evidence)
	}
	loaded, found, err := store.GetIncident(context.Background(), "scope-nil")
	if err != nil || !found || loaded.ChildIncidentIDs == nil || loaded.ConfirmationFailureEvidenceKeys == nil {
		t.Fatalf("loaded=%+v found=%v err=%v", loaded, found, err)
	}
}

func TestCircuitInputBoundsAndZeroTime(t *testing.T) {
	store, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	if _, err := store.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "zero-time", NowMS: 0}); err != nil {
		t.Fatal(err)
	}
	var available int64
	if err := db.QueryRow(`SELECT available_at_ms FROM account_circuit_outbox WHERE dedupe_key='dispatch:zero-time'`).Scan(&available); err != nil {
		t.Fatal(err)
	}
	if available != 0 {
		t.Fatalf("zero time was replaced: %d", available)
	}
	if _, err := store.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "negative-time", NowMS: -1}); err == nil {
		t.Fatal("negative dispatch time was accepted")
	}
	if _, err := store.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "runtime key", TransitionID: "invalid-runtime-key", NowMS: 0}); err == nil {
		t.Fatal("unbounded runtime key was accepted")
	}
	if _, err := store.ListByRuntimeKeys(ctx, []string{"a1", "  "}, false, 0); err == nil {
		t.Fatal("blank runtime key was silently dropped")
	}
	if _, err := store.ReleaseOutboxForReplay(ctx, "missing", "token", "upstream timeout", 0, 0); err == nil {
		t.Fatal("unbounded error class was accepted")
	}
	if _, err := store.ReleaseOutboxForReplay(ctx, "missing", "token", " upstream_timeout", 0, 0); err == nil {
		t.Fatal("non-canonical error class was accepted")
	}
	if _, err := store.ClaimOutbox(ctx, "worker", -1, 1, 1); err == nil {
		t.Fatal("negative claim time was accepted")
	}
}

func TestIncidentStateScopeLeaseAndRetentionBounds(t *testing.T) {
	base := func() Incident {
		id := "incident"
		return Incident{CircuitScopeKey: "scope", AccountID: "account", AccountRuntimeKey: "runtime", ScopeKind: "account", IncidentID: &id, State: "OPEN", DispatchRevision: 1, TransitionID: "transition", ConfirmationFailuresRequired: 1}
	}
	tests := []struct {
		name string
		edit func(*Incident)
	}{
		{name: "state", edit: func(v *Incident) { v.State = "INVALID" }},
		{name: "key scope shape", edit: func(v *Incident) { v.ScopeKind = "key" }},
		{name: "protocol scope shape", edit: func(v *Incident) { v.ScopeKind = "protocol_model" }},
		{name: "partial lease", edit: func(v *Incident) { lease := "lease"; v.LeaseID = &lease }},
		{name: "lease without attempt times", edit: func(v *Incident) {
			lease, purpose, owner := "lease", "confirmation", "run"
			until := int64(100)
			v.LeaseID, v.LeasePurpose, v.LeaseOwnerRunID, v.LeaseUntilMS = &lease, &purpose, &owner, &until
		}},
		{name: "invalid failure scope", edit: func(v *Incident) { v.FailureScope = "account" }},
		{name: "negative created time", edit: func(v *Incident) { v.CreatedAtMS = -1 }},
		{name: "negative recovery successes", edit: func(v *Incident) { v.RecoveringSuccesses = -1 }},
		{name: "duplicate evidence", edit: func(v *Incident) {
			hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			v.ConfirmationFailureEvidenceKeys = []string{hash, hash}
		}},
		{name: "retention before now", edit: func(v *Incident) { v.State = "CLOSED"; retained := int64(99); v.RetainedUntilMS = &retained }},
		{name: "non closed retention", edit: func(v *Incident) { retained := int64(100); v.RetainedUntilMS = &retained }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			incident := base()
			incident.UpdatedAtMS = 100
			test.edit(&incident)
			validationErr := validateIncident(&incident)
			if err := validateIncidentTimes(&incident, incident.UpdatedAtMS); err != nil {
				validationErr = err
			}
			if validationErr == nil {
				t.Fatal("invalid incident was accepted")
			}
		})
	}
	validClosed := base()
	validClosed.State = "CLOSED"
	validClosed.UpdatedAtMS = 100
	retained := int64(100)
	validClosed.RetainedUntilMS = &retained
	if err := validateIncident(&validClosed); err != nil {
		t.Fatalf("equal retention boundary rejected: %v", err)
	}
	if err := validateIncidentTimes(&validClosed, 100); err != nil {
		t.Fatalf("equal retention time rejected: %v", err)
	}
}

func TestKeyIncidentRequiresSHA256Fingerprint(t *testing.T) {
	id := "key-incident"
	fingerprint := "not-a-fingerprint"
	incident := Incident{CircuitScopeKey: "key-scope", AccountID: "account", AccountRuntimeKey: "runtime", ScopeKind: "key", KeyFingerprint: &fingerprint, IncidentID: &id, State: "OPEN", DispatchRevision: 1, TransitionID: "transition", ConfirmationFailuresRequired: 1}
	if err := validateIncident(&incident); err == nil {
		t.Fatal("key incident accepted a non-SHA256 fingerprint")
	}
}

func TestIncidentScopeCannotChangeAccount(t *testing.T) {
	store, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if _, err := db.Exec(`INSERT INTO accounts(id,dispatch_revision,circuit_projection_revision) VALUES ('a2',1,0)`); err != nil {
		t.Fatal(err)
	}
	firstID, secondID := "first", "second"
	first := IncidentMutation{Incident: Incident{CircuitScopeKey: "shared-scope", AccountID: "a1", AccountRuntimeKey: "a1", ScopeKind: "account", IncidentID: &firstID, State: "OPEN", DispatchRevision: 1, TransitionID: "first-transition", ConfirmationFailuresRequired: 1}}
	if _, err := store.CompareAndSetIncident(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.AccountID, second.AccountRuntimeKey, second.IncidentID, second.TransitionID = "a2", "a2", &secondID, "second-transition"
	if result, err := store.CompareAndSetIncident(context.Background(), second); err == nil && result.Status == "applied" {
		t.Fatal("incident scope was reassigned to another account")
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:circuit-control-schema-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}
