package circuitcontrolplane

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
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
		`CREATE TABLE accounts (id TEXT PRIMARY KEY, dispatch_revision INTEGER NOT NULL DEFAULT 1, circuit_projection_revision INTEGER NOT NULL DEFAULT 0, deleted_at TEXT)`,
		`CREATE TABLE account_circuit_incidents (
 circuit_scope_key TEXT PRIMARY KEY, account_id TEXT NOT NULL, account_runtime_key TEXT NOT NULL, scope_kind TEXT NOT NULL,
 key_fingerprint TEXT, protocol_code TEXT, request_lane TEXT, model_family TEXT, client_model TEXT, capability_hash TEXT,
 credential_source_account_id TEXT, client_endpoint_family TEXT, final_upstream_model TEXT, upstream_endpoint_mode TEXT, incident_id TEXT NOT NULL,
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
	if _, err := db.Exec(`CREATE UNIQUE INDEX idx_account_circuit_incidents_key_model_capability ON account_circuit_incidents(scope_kind, capability_hash) WHERE scope_kind = 'key_model' AND capability_hash IS NOT NULL`); err != nil {
		t.Fatal(err)
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

func TestListDispatchRevisionsIsOrderedAndFenced(t *testing.T) {
	s, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if _, err := db.Exec(`INSERT INTO accounts(id,dispatch_revision,circuit_projection_revision) VALUES ('a2',3,0),('a3',2,0)`); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListDispatchRevisions(context.Background(), "", 2)
	if err != nil || len(page.Items) != 2 || page.Items[0].AccountID != "a1" || page.Items[1].AccountID != "a2" || page.NextAfterAccountID != "a2" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	next, err := s.ListDispatchRevisions(context.Background(), page.NextAfterAccountID, 2)
	if err != nil || len(next.Items) != 1 || next.Items[0].AccountID != "a3" {
		t.Fatalf("next=%+v err=%v", next, err)
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

// TestIncidentCASAccountNotFoundTerminal 对齐归档热修
// compareAndSetAccountCircuitIncidentInClient：账户行缺失或 deleted_at 非空时，
// 迟到的运行态观察必须得到 account_not_found 终态（缺失行 currentDispatchRevision
// 为 0，已删行返回行内当前 revision），且终态优先于 stale_dispatch_revision，
// 不写 incident/outbox。
func TestIncidentCASAccountNotFoundTerminal(t *testing.T) {
	s, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if _, err := db.Exec(`INSERT INTO accounts(id,dispatch_revision,circuit_projection_revision,deleted_at) VALUES ('a-deleted',3,0,'2020-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	mutation := func(accountID string, dispatchRevision int64) IncidentMutation {
		incidentID := "inc-" + accountID
		return IncidentMutation{Incident: Incident{
			CircuitScopeKey: "scope-" + accountID, AccountID: accountID, AccountRuntimeKey: accountID,
			ScopeKind: "account", IncidentID: &incidentID, State: "OPEN", Generation: 1,
			DispatchRevision: dispatchRevision, TransitionID: "tr-" + accountID,
			ConfirmationFailuresRequired: 1, ChildIncidentIDs: []string{}, ConfirmationFailureEvidenceKeys: []string{},
			CreatedAtMS: 100, UpdatedAtMS: 100,
		}}
	}

	missing, err := s.CompareAndSetIncident(ctx, mutation("missing-account", 1))
	if err != nil || missing.Status != "account_not_found" || missing.CurrentDispatchRevision != 0 || missing.Incident != nil {
		t.Fatalf("missing account = %+v err=%v", missing, err)
	}
	deleted, err := s.CompareAndSetIncident(ctx, mutation("a-deleted", 3))
	if err != nil || deleted.Status != "account_not_found" || deleted.CurrentDispatchRevision != 3 || deleted.Incident != nil {
		t.Fatalf("deleted account = %+v err=%v", deleted, err)
	}
	// 终态优先：revision 不一致时仍必须是 account_not_found，而非 stale。
	deletedStale, err := s.CompareAndSetIncident(ctx, mutation("a-deleted", 99))
	if err != nil || deletedStale.Status != "account_not_found" || deletedStale.CurrentDispatchRevision != 3 {
		t.Fatalf("deleted account stale fence = %+v err=%v", deletedStale, err)
	}
	var incidents, outbox int
	if err := db.QueryRow(`SELECT COUNT(*) FROM account_circuit_incidents`).Scan(&incidents); err != nil || incidents != 0 {
		t.Fatalf("terminal CAS must not write incidents: %d err=%v", incidents, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM account_circuit_outbox`).Scan(&outbox); err != nil || outbox != 0 {
		t.Fatalf("terminal CAS must not write outbox: %d err=%v", outbox, err)
	}
}

func TestKeyModelIncidentRoundTripsAndRequiresCompleteIdentity(t *testing.T) {
	s, _ := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	incidentID := "key-model-incident"
	fingerprint := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	clientModel, capabilityHash := "gpt-4.1", "capability-v1"
	credentialSourceAccountID, clientEndpointFamily := "source-account", "chat-completions"
	finalUpstreamModel, upstreamEndpointMode := "gpt-4.1-2025-04-14", "responses"
	incident := Incident{
		CircuitScopeKey: "key-model-scope", AccountID: "a1", AccountRuntimeKey: "a1", ScopeKind: "key_model",
		KeyFingerprint: &fingerprint, ClientModel: &clientModel, CapabilityHash: &capabilityHash,
		CredentialSourceAccountID: &credentialSourceAccountID, ClientEndpointFamily: &clientEndpointFamily,
		FinalUpstreamModel: &finalUpstreamModel, UpstreamEndpointMode: &upstreamEndpointMode,
		IncidentID: &incidentID, State: "OPEN", FailureScope: "key_model", DispatchRevision: 1,
		TransitionID: "key-model-transition", ConfirmationFailuresRequired: 1, CreatedAtMS: 100, UpdatedAtMS: 100,
	}
	result, err := s.CompareAndSetIncident(context.Background(), IncidentMutation{Incident: incident})
	if err != nil || result.Status != "applied" || result.Incident == nil || result.Incident.CapabilityHash == nil || *result.Incident.CapabilityHash != capabilityHash || result.Incident.FailureScope != "key_model" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	loaded, found, err := s.GetIncident(context.Background(), incident.CircuitScopeKey)
	if err != nil || !found || loaded.ClientModel == nil || *loaded.ClientModel != clientModel || loaded.UpstreamEndpointMode == nil || *loaded.UpstreamEndpointMode != upstreamEndpointMode {
		t.Fatalf("loaded=%+v found=%v err=%v", loaded, found, err)
	}
	duplicate := incident
	duplicate.CircuitScopeKey = "duplicate-key-model-scope"
	duplicateIncidentID, duplicateTransitionID := "duplicate-key-model-incident", "duplicate-key-model-transition"
	duplicate.IncidentID, duplicate.TransitionID = &duplicateIncidentID, duplicateTransitionID
	if _, err := s.CompareAndSetIncident(context.Background(), IncidentMutation{Incident: duplicate}); err == nil {
		t.Fatal("key_model incident accepted a duplicate capability hash")
	}
	partial := incident
	partial.CircuitScopeKey = "partial-key-model-scope"
	partial.UpstreamEndpointMode = nil
	if err := validateIncident(&partial); err == nil {
		t.Fatal("key_model incident accepted a partial identity")
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
	if err := store.CheckContract(context.Background()); err != nil {
		t.Fatalf("complete circuit contract failed: %v", err)
	}
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

func TestCheckContractRequiresKeyModelCapabilityIndex(t *testing.T) {
	store, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if _, err := db.Exec(`DROP INDEX idx_account_circuit_incidents_key_model_capability`); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckContract(context.Background()); err == nil {
		t.Fatal("CheckContract accepted a missing key_model capability index")
	}
}

func TestCheckContractRejectsMalformedSameNameCapabilityIndex(t *testing.T) {
	tests := []struct{ name, ddl string }{
		{"wrong columns", `CREATE UNIQUE INDEX idx_account_circuit_incidents_key_model_capability ON account_circuit_incidents(capability_hash,scope_kind) WHERE scope_kind='key_model' AND capability_hash IS NOT NULL`},
		{"not unique", `CREATE INDEX idx_account_circuit_incidents_key_model_capability ON account_circuit_incidents(scope_kind,capability_hash) WHERE scope_kind='key_model' AND capability_hash IS NOT NULL`},
		{"missing predicate", `CREATE UNIQUE INDEX idx_account_circuit_incidents_key_model_capability ON account_circuit_incidents(scope_kind,capability_hash)`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
			if _, err := db.Exec(`DROP INDEX idx_account_circuit_incidents_key_model_capability`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(tc.ddl); err != nil {
				t.Fatal(err)
			}
			if err := store.CheckContract(context.Background()); err == nil {
				t.Fatal("CheckContract accepted malformed same-name capability index")
			}
		})
	}
}

func TestSharedSQLiteSchemaCoversCircuitContract(t *testing.T) {
	for table, required := range map[string][]string{
		"accounts":                  {"id", "dispatch_revision", "circuit_projection_revision"},
		"account_circuit_incidents": strings.Split(incidentColumns, ","),
		"account_circuit_outbox":    strings.Split(outboxColumns, ","),
	} {
		spec, ok := contracts.BusinessSQLiteSchema[table]
		if !ok {
			t.Fatalf("shared schema is missing %s", table)
		}
		actual := make(map[string]bool, len(spec.Columns))
		for _, column := range spec.Columns {
			actual[column] = true
		}
		for _, column := range required {
			if !actual[column] {
				t.Fatalf("shared schema is missing %s.%s", table, column)
			}
		}
	}
	incidentSpec := contracts.BusinessSQLiteSchema["account_circuit_incidents"]
	if !containsString(incidentSpec.Indexes, "idx_account_circuit_incidents_key_model_capability") {
		t.Fatal("shared schema is missing the key_model capability index")
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
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
	if _, err := store.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: strings.Repeat("a", 257), AccountRuntimeKey: "a1", TransitionID: "too-long-account", NowMS: 0}); err == nil {
		t.Fatal("oversized account id was accepted")
	}
	if _, err := store.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "runtime/key 1", TransitionID: "valid-runtime-key", NowMS: 0}); err != nil {
		t.Fatalf("Node-compatible runtime key was rejected: %v", err)
	}
	if _, err := store.ListByRuntimeKeys(ctx, []string{"a1", "  "}, false, 0); err == nil {
		t.Fatal("blank runtime key was silently dropped")
	}
	if _, err := store.ReleaseOutboxForReplay(ctx, "missing", "token", "upstream timeout", 0, 0); err == nil {
		t.Fatal("unbounded error class was accepted")
	}
	if ok, err := store.ReleaseOutboxForReplay(ctx, "missing", "token", " upstream_timeout ", 0, 0); err != nil || ok {
		t.Fatalf("Node-compatible trimmed error class failed: ok=%v err=%v", ok, err)
	}
	if _, err := store.ClaimOutbox(ctx, "worker", -1, 1, 1); err == nil {
		t.Fatal("negative claim time was accepted")
	}
	if _, err := store.ClaimOutbox(ctx, strings.Repeat("w", 129), 0, 1, 1); err == nil {
		t.Fatal("oversized claim owner was accepted")
	}
	const largeOperationalBatch = 50_000
	if _, err := store.ClaimOutbox(ctx, "worker", 0, 1, largeOperationalBatch); err != nil {
		t.Fatalf("large claim batch was rejected: %v", err)
	}
	if _, err := store.ListForRebuild(ctx, 0, 0, "", largeOperationalBatch); err != nil {
		t.Fatalf("large rebuild batch was rejected: %v", err)
	}
	if _, err := store.ListProjectionGaps(ctx, "", 0, "", largeOperationalBatch); err != nil {
		t.Fatalf("large projection-gap batch was rejected: %v", err)
	}
	if _, err := store.Cleanup(ctx, 0, 0, largeOperationalBatch); err != nil {
		t.Fatalf("large cleanup batch was rejected: %v", err)
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
		{name: "invalid failure scope", edit: func(v *Incident) { v.FailureScope = "upstream" }},
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

func TestKeyIncidentAcceptsNodeCompatibleFingerprint(t *testing.T) {
	id := "key-incident"
	fingerprint := "not-a-fingerprint"
	incident := Incident{CircuitScopeKey: "key-scope", AccountID: "account", AccountRuntimeKey: "runtime", ScopeKind: "key", KeyFingerprint: &fingerprint, IncidentID: &id, State: "OPEN", DispatchRevision: 1, TransitionID: "transition", ConfirmationFailuresRequired: 1}
	if err := validateIncident(&incident); err != nil {
		t.Fatalf("Node-compatible non-SHA256 fingerprint was rejected: %v", err)
	}
	tooLong := strings.Repeat("x", 257)
	incident.KeyFingerprint = &tooLong
	if err := validateIncident(&incident); err == nil {
		t.Fatal("oversized key fingerprint was accepted")
	}
}

func TestIncidentTextInputsAreTrimmedLikeNode(t *testing.T) {
	id := " incident-id "
	fingerprint := " key/fingerprint "
	incident := Incident{
		CircuitScopeKey: " scope ", AccountID: " account ", AccountRuntimeKey: " runtime/key ", ScopeKind: "key",
		KeyFingerprint: &fingerprint, IncidentID: &id, State: "OPEN", DispatchRevision: 1,
		TransitionID: " transition ", ConfirmationFailuresRequired: 1,
	}
	if err := validateIncident(&incident); err != nil {
		t.Fatalf("trimmed Node-compatible incident was rejected: %v", err)
	}
	if incident.CircuitScopeKey != "scope" || incident.AccountID != "account" || incident.AccountRuntimeKey != "runtime/key" || incident.TransitionID != "transition" || *incident.KeyFingerprint != "key/fingerprint" || *incident.IncidentID != "incident-id" {
		t.Fatalf("incident text was not normalized: %+v", incident)
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
