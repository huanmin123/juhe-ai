package circuitcontrolplane

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	// 组B（wkSQLiteLegacyStoreLabeled/w9eDDLDB 的 SQLite-only 生产臂覆盖）仍
	// 依赖 "sqlite" 驱动注册；组A 建库已迁 pgtestkit 真实 PG（pgkit_test.go）。
	_ "modernc.org/sqlite"
)

// testStore 打开就绪控制面存储。建库已迁 pgtestkit 真实 PG（PLAN-20261024T051925945Z
// 测试基建迁移切片），与 wkStoreLabeled 共用同一 PG fixture（pgkit_test.go）。
func testStore(t *testing.T, gate OwnerGate) (*Store, *sql.DB) {
	t.Helper()
	return wkPGStoreLabeled(t, "", gate)
}

func TestOwnerGateBlocksWrites(t *testing.T) {
	s, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true})
	_, err := s.AdvanceDispatchRevision(context.Background(), DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "t1"})
	if !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("err=%v", err)
	}
	var revision int
	if err := db.QueryRow(`SELECT dispatch_revision FROM ` + defaultBusinessSchema + `.accounts WHERE id='a1'`).Scan(&revision); err != nil || revision != 1 {
		t.Fatalf("revision=%d err=%v", revision, err)
	}
}

func TestListDispatchRevisionsIsOrderedAndFenced(t *testing.T) {
	s, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if _, err := db.Exec(`INSERT INTO ` + defaultBusinessSchema + `.accounts(id,dispatch_revision,circuit_projection_revision) VALUES ('a2',3,0),('a3',2,0)`); err != nil {
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
	if err := db.QueryRow(`SELECT projected_ledger_revision FROM ` + defaultBusinessSchema + `.account_circuit_incidents WHERE circuit_scope_key='scope-1'`).Scan(&projected); err != nil || projected != 1 {
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
	if _, err := db.Exec(`INSERT INTO ` + defaultBusinessSchema + `.accounts(id,dispatch_revision,circuit_projection_revision,deleted_at) VALUES ('a-deleted',3,0,'2020-01-01T00:00:00Z')`); err != nil {
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
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + defaultBusinessSchema + `.account_circuit_incidents`).Scan(&incidents); err != nil || incidents != 0 {
		t.Fatalf("terminal CAS must not write incidents: %d err=%v", incidents, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + defaultBusinessSchema + `.account_circuit_outbox`).Scan(&outbox); err != nil || outbox != 0 {
		t.Fatalf("terminal CAS must not write outbox: %d err=%v", outbox, err)
	}
}

// TestListAccountSubqueriesFenceDeletedAccounts 对齐归档
// listAccountCircuitIncidentsForRebuildInClient /
// listAccountCircuitIncidentsByRuntimeKeysInClient /
// listAccountCircuitProjectionGapsInClient 的 deleted_at IS NULL 账户围栏：
// 已删账户的迟到 incident 不回放、不进运行态摘要，也不进入投影缺口
// （含 retained closed 分支，围栏仍生效）。
func TestListAccountSubqueriesFenceDeletedAccounts(t *testing.T) {
	s, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if _, err := db.Exec(`INSERT INTO ` + defaultBusinessSchema + `.accounts(id,dispatch_revision,circuit_projection_revision,deleted_at) VALUES ('a-deleted',3,0,'2020-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	retained := int64(999_999)
	insertIncident := func(scope, accountID, state string, dispatch, ledger, projected, updated int64, retainedUntil *int64) {
		t.Helper()
		_, err := db.Exec(`INSERT INTO `+defaultBusinessSchema+`.account_circuit_incidents (
      circuit_scope_key, account_id, account_runtime_key, scope_kind, incident_id,
      child_incident_ids_json, state, generation, dispatch_revision, ledger_revision,
      projected_ledger_revision, transition_id, cooldown_observation_generation,
      upstream_attempt_observed, backoff_level, consecutive_failures,
      confirmation_failures_required, confirmation_failure_evidence_keys_json,
      recovering_successes, retained_until_ms, created_at_ms, updated_at_ms
    ) VALUES ($1, $2, $3, 'account', $4, '[]', $5, 1, $6, $7, $8, $9, 0, 0, 0, 0, 1, '[]', 0, $10, $11, $12)`,
			scope, accountID, accountID, "inc-"+scope, state, dispatch, ledger, projected, "tr-"+scope,
			retainedUntil, updated, updated)
		if err != nil {
			t.Fatal(err)
		}
	}
	insertIncident("scope-live", "a1", "OPEN", 1, 1, 0, 100, nil)
	insertIncident("scope-deleted", "a-deleted", "OPEN", 3, 3, 2, 101, nil)
	insertIncident("scope-deleted-closed", "a-deleted", "CLOSED", 3, 3, 3, 102, &retained)

	ctx := context.Background()
	rebuild, err := s.ListForRebuild(ctx, 1000, 0, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rebuild.Items) != 1 || rebuild.Items[0].CircuitScopeKey != "scope-live" {
		t.Fatalf("rebuild 必须只回放未删账户 incident: %+v", rebuild.Items)
	}
	byKeys, err := s.ListByRuntimeKeys(ctx, []string{"a1", "a-deleted"}, false, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(byKeys) != 1 || byKeys[0].CircuitScopeKey != "scope-live" {
		t.Fatalf("runtime-key 摘要必须只含未删账户 incident: %+v", byKeys)
	}
	// retained closed 分支：状态过滤器会放行已删账户的 CLOSED 行，围栏必须仍排除。
	byKeysRetained, err := s.ListByRuntimeKeys(ctx, []string{"a1", "a-deleted"}, true, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(byKeysRetained) != 1 || byKeysRetained[0].CircuitScopeKey != "scope-live" {
		t.Fatalf("retained closed 分支也必须排除已删账户: %+v", byKeysRetained)
	}
	gaps, err := s.ListProjectionGaps(ctx, "", 0, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps.Dispatch) != 1 || gaps.Dispatch[0].AccountID != "a1" {
		t.Fatalf("dispatch 缺口必须只含未删账户: %+v", gaps.Dispatch)
	}
	if len(gaps.Incidents) != 1 || gaps.Incidents[0].CircuitScopeKey != "scope-live" {
		t.Fatalf("incident 缺口必须只含未删账户: %+v", gaps.Incidents)
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
	if _, err := db.Exec(`DROP TABLE ` + defaultBusinessSchema + `.account_circuit_incidents`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE ` + defaultBusinessSchema + `.account_circuit_incidents (circuit_scope_key TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckContract(context.Background()); err == nil {
		t.Fatal("CheckContract accepted a relation missing circuit columns")
	}
}

func TestCheckContractRequiresKeyModelCapabilityIndex(t *testing.T) {
	store, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if _, err := db.Exec(`DROP INDEX ` + defaultBusinessSchema + `.idx_account_circuit_incidents_key_model_capability`); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckContract(context.Background()); err == nil {
		t.Fatal("CheckContract accepted a missing key_model capability index")
	}
}

func TestCheckContractRejectsMalformedSameNameCapabilityIndex(t *testing.T) {
	tests := []struct{ name, ddl string }{
		{"wrong columns", `CREATE UNIQUE INDEX idx_account_circuit_incidents_key_model_capability ON ` + defaultBusinessSchema + `.account_circuit_incidents(capability_hash,scope_kind) WHERE scope_kind='key_model' AND capability_hash IS NOT NULL`},
		{"not unique", `CREATE INDEX idx_account_circuit_incidents_key_model_capability ON ` + defaultBusinessSchema + `.account_circuit_incidents(scope_kind,capability_hash) WHERE scope_kind='key_model' AND capability_hash IS NOT NULL`},
		{"missing predicate", `CREATE UNIQUE INDEX idx_account_circuit_incidents_key_model_capability ON ` + defaultBusinessSchema + `.account_circuit_incidents(scope_kind,capability_hash)`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
			if _, err := db.Exec(`DROP INDEX ` + defaultBusinessSchema + `.idx_account_circuit_incidents_key_model_capability`); err != nil {
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
	if err := db.QueryRow(`SELECT child_incident_ids_json,confirmation_failure_evidence_keys_json FROM `+defaultBusinessSchema+`.account_circuit_incidents WHERE circuit_scope_key='scope-nil'`).Scan(&children, &evidence); err != nil {
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
	if err := db.QueryRow(`SELECT available_at_ms FROM ` + defaultBusinessSchema + `.account_circuit_outbox WHERE dedupe_key='dispatch:zero-time'`).Scan(&available); err != nil {
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
		// 2026-10-08 attempt-optional 对齐（validateIncident 注释同键）：原
		// "lease without attempt times" 用例锁定 attempt/租约耦合强制，随该
		// 校验删除而移除；正向覆盖见 TestW9ELeaseWithoutAttemptTimesApplied。
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
	if _, err := db.Exec(`INSERT INTO ` + defaultBusinessSchema + `.accounts(id,dispatch_revision,circuit_projection_revision) VALUES ('a2',1,0)`); err != nil {
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
	// 仅用于 New() 的 schema 校验（不触库），建库统一走 pgtestkit 真实 PG。
	return pgtestkit.New(t)
}
