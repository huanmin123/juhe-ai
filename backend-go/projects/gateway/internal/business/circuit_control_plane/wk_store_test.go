package circuitcontrolplane

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// wkReadyStore 以测试名唯一 DSN 构造就绪存储（共享缓存内存库按名隔离）。
func wkReadyStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	return wkStoreNamed(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
}

func wkStoreNamed(t *testing.T, gate OwnerGate) (*Store, *sql.DB) {
	t.Helper()
	return wkStoreLabeled(t, "", gate)
}

func wkStoreLabeled(t *testing.T, label string, gate OwnerGate) (*Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+strings.ReplaceAll(t.Name(), "/", "-")+label+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range wkDDLs {
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

var wkDDLs = []string{
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

func wkBaseIncident(scope, accountID string, dispatchRevision int64) IncidentMutation {
	incidentID := "inc-" + scope
	return IncidentMutation{Incident: Incident{
		CircuitScopeKey: scope, AccountID: accountID, AccountRuntimeKey: accountID, ScopeKind: "account",
		IncidentID: &incidentID, State: "OPEN", Generation: 1, DispatchRevision: dispatchRevision,
		TransitionID: "tr-" + scope, ConfirmationFailuresRequired: 1,
		ChildIncidentIDs: []string{}, ConfirmationFailureEvidenceKeys: []string{},
		CreatedAtMS: 100, UpdatedAtMS: 100,
	}}
}

func wkOutboxEvent(scope, incidentID string, dispatchRevision, generation, ledger int64) Outbox {
	return Outbox{
		EventID: "evt-" + scope, ProjectionKey: ProjectionKey, EventType: "incident_changed",
		AccountID: "a1", AccountRuntimeKey: "a1", CircuitScopeKey: strPtr2(scope), IncidentID: strPtr2(incidentID),
		TransitionID: "tr-" + scope, DispatchRevision: dispatchRevision,
		Generation: int64Ptr2(generation), LedgerRevision: int64Ptr2(ledger),
	}
}

func strPtr2(v string) *string { return &v }
func int64Ptr2(v int64) *int64 { return &v }

func TestPostgresTextRewriting(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	s.mode = Postgres
	s.schema = "tenant_1"
	if got := s.table("accounts"); got != `"tenant_1".accounts` {
		t.Fatalf("table=%q", got)
	}
	if got := s.bind("SELECT * FROM accounts WHERE id=? AND status=?"); got != "SELECT * FROM accounts WHERE id=$1 AND status=$2" {
		t.Fatalf("bind=%q", got)
	}
	if got := s.forUpdate(); got != " FOR UPDATE" {
		t.Fatalf("forUpdate=%q", got)
	}
	if got := s.forUpdateSkipLocked(); got != " FOR UPDATE SKIP LOCKED" {
		t.Fatalf("forUpdateSkipLocked=%q", got)
	}
	// 非法 schema 拒绝。
	if _, err := New(db, Postgres, "bad schema", OwnerGate{}); !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("schema err=%v", err)
	}
}

func TestLoadIncidentForProjectionFences(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()
	// 身份缺失。
	if _, err := s.LoadIncidentForProjection(ctx, Outbox{AccountID: ""}); err == nil {
		t.Fatal("缺 account id 必须失败")
	}
	if _, err := s.LoadIncidentForProjection(ctx, Outbox{AccountID: "a1"}); err == nil {
		t.Fatal("缺 scope 必须失败")
	}
	if _, err := s.LoadIncidentForProjection(ctx, Outbox{AccountID: "a1", CircuitScopeKey: strPtr2("scope")}); err == nil {
		t.Fatal("缺 incident id 必须失败")
	}
	// 账户缺失 → missing。
	missing := wkOutboxEvent("scope-x", "inc-x", 1, 1, 1)
	missing.AccountID = "ghost"
	loaded, err := s.LoadIncidentForProjection(ctx, missing)
	if err != nil || loaded.Status != "missing" {
		t.Fatalf("missing=%+v err=%v", loaded, err)
	}
	// 常规 fixture：a1 在 revision 1 上创建 incident。
	if _, err := s.CompareAndSetIncident(ctx, wkBaseIncident("scope-1", "a1", 1)); err != nil {
		t.Fatal(err)
	}
	// revision 不一致 → stale。
	stale := wkOutboxEvent("scope-1", "inc-scope-1", 99, 1, 1)
	loaded, err = s.LoadIncidentForProjection(ctx, stale)
	if err != nil || loaded.Status != "stale" || loaded.CurrentDispatchRevision != 1 {
		t.Fatalf("stale=%+v err=%v", loaded, err)
	}
	// incident id 不一致 → missing。
	mismatched := wkOutboxEvent("scope-1", "inc-other", 1, 1, 1)
	loaded, err = s.LoadIncidentForProjection(ctx, mismatched)
	if err != nil || loaded.Status != "missing" {
		t.Fatalf("id mismatch=%+v err=%v", loaded, err)
	}
	// generation 不一致 → missing。
	genMismatch := wkOutboxEvent("scope-1", "inc-scope-1", 1, 9, 1)
	loaded, err = s.LoadIncidentForProjection(ctx, genMismatch)
	if err != nil || loaded.Status != "missing" {
		t.Fatalf("generation mismatch=%+v err=%v", loaded, err)
	}
	// ledger 超前 → missing。
	ledgerAhead := wkOutboxEvent("scope-1", "inc-scope-1", 1, 1, 5)
	loaded, err = s.LoadIncidentForProjection(ctx, ledgerAhead)
	if err != nil || loaded.Status != "missing" {
		t.Fatalf("ledger ahead=%+v err=%v", loaded, err)
	}
	// 全部一致 → current。
	current := wkOutboxEvent("scope-1", "inc-scope-1", 1, 1, 1)
	loaded, err = s.LoadIncidentForProjection(ctx, current)
	if err != nil || loaded.Status != "current" || loaded.Incident.IncidentID == nil || loaded.CurrentDispatchRevision != 1 {
		t.Fatalf("current=%+v err=%v", loaded, err)
	}
	// gate 未就绪。
	blocked, _ := wkStoreLabeled(t, "-blocked", OwnerGate{})
	if _, err := blocked.LoadIncidentForProjection(ctx, current); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("gate err=%v", err)
	}
	_ = db
}

func TestGetIncidentValidation(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()
	if _, _, err := s.GetIncident(ctx, "  "); err == nil {
		t.Fatal("空 scope 必须失败")
	}
	if _, _, err := s.GetIncident(ctx, strings.Repeat("x", 2049)); err == nil {
		t.Fatal("超长 scope 必须失败")
	}
	if _, found, err := s.GetIncident(ctx, "no-such"); err != nil || found {
		t.Fatalf("缺失 incident: found=%v err=%v", found, err)
	}
	blocked, _ := wkStoreLabeled(t, "-blocked", OwnerGate{})
	if _, _, err := blocked.GetIncident(ctx, "scope"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("gate err=%v", err)
	}
}

func TestCompareAndSetIncidentFences(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()
	// 负期望 ledger。
	negative := wkBaseIncident("scope-neg", "a1", 1)
	negative.ExpectedLedgerRevision = int64Ptr2(-1)
	if _, err := s.CompareAndSetIncident(ctx, negative); err == nil {
		t.Fatal("负期望 ledger 必须失败")
	}
	// 账户 revision 不一致 → stale_dispatch_revision。
	if result, err := s.CompareAndSetIncident(ctx, wkBaseIncident("scope-stale-rev", "a1", 42)); err != nil || result.Status != "stale_dispatch_revision" || result.CurrentDispatchRevision != 1 {
		t.Fatalf("stale dispatch=%+v err=%v", result, err)
	}
	// 首次创建（ExpectedLedgerRevision nil）。
	created, err := s.CompareAndSetIncident(ctx, wkBaseIncident("scope-cas", "a1", 1))
	if err != nil || created.Status != "applied" || created.Incident.LedgerRevision != 1 {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	// 已存在但期望 nil（新 transition）→ cas_conflict。
	existingNilExpected := wkBaseIncident("scope-cas", "a1", 1)
	existingNilExpected.TransitionID = "tr-cas-nil"
	if result, err := s.CompareAndSetIncident(ctx, existingNilExpected); err != nil || result.Status != "cas_conflict" || result.Incident == nil {
		t.Fatalf("conflict=%+v err=%v", result, err)
	}
	// 期望错误值 → cas_conflict。
	wrongExpected := wkBaseIncident("scope-cas", "a1", 1)
	wrongExpected.ExpectedLedgerRevision = int64Ptr2(42)
	wrongExpected.TransitionID = "tr-cas-2"
	if result, err := s.CompareAndSetIncident(ctx, wrongExpected); err != nil || result.Status != "cas_conflict" {
		t.Fatalf("wrong expected=%+v err=%v", result, err)
	}
	// 幂等重放：同 transition id → idempotent。
	replay := wkBaseIncident("scope-cas", "a1", 1)
	if result, err := s.CompareAndSetIncident(ctx, replay); err != nil || result.Status != "idempotent" || result.Incident == nil || result.Incident.LedgerRevision != 1 {
		t.Fatalf("replay=%+v err=%v", result, err)
	}
	// 身份重放冲突：TransitionID 决定 dedupe 键；同 dedupe 键但不同 scope 必须拒绝。
	conflictingTransition := wkBaseIncident("scope-cas-2", "a1", 1)
	conflictingTransition.TransitionID = "tr-scope-cas"
	if _, err := s.CompareAndSetIncident(ctx, conflictingTransition); !errors.Is(err, ErrIdentityReplay) {
		t.Fatalf("identity replay err=%v", err)
	}
	// 正常递增：期望 ledger=1 → applied ledger=2。
	advance := wkBaseIncident("scope-cas", "a1", 1)
	advance.ExpectedLedgerRevision = int64Ptr2(1)
	advance.State = "RECOVERING"
	advance.Generation = 2
	advance.TransitionID = "tr-cas-3"
	advanced, err := s.CompareAndSetIncident(ctx, advance)
	if err != nil || advanced.Status != "applied" || advanced.Incident.LedgerRevision != 2 {
		t.Fatalf("advanced=%+v err=%v", advanced, err)
	}
	// 期望落后 → cas_conflict（ledger 已是 2）。
	behind := wkBaseIncident("scope-cas", "a1", 1)
	behind.ExpectedLedgerRevision = int64Ptr2(1)
	behind.TransitionID = "tr-cas-4"
	if result, err := s.CompareAndSetIncident(ctx, behind); err != nil || result.Status != "cas_conflict" {
		t.Fatalf("behind expected=%+v err=%v", result, err)
	}
}

func TestAcknowledgeOutboxBranches(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "t-ack", NowMS: 100}); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimOutbox(ctx, "worker", 100, 60_000, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	event := claimed[0]
	// 错误投影键 → false。
	if ok, err := s.AcknowledgeOutbox(ctx, event.EventID, "wrong-key", *event.ClaimToken, 110); err != nil || ok {
		t.Fatalf("wrong key: ok=%v err=%v", ok, err)
	}
	// 非法 event id。
	if _, err := s.AcknowledgeOutbox(ctx, "  ", ProjectionKey, *event.ClaimToken, 110); err == nil {
		t.Fatal("空 event id 必须失败")
	}
	// 非法 claim token。
	if _, err := s.AcknowledgeOutbox(ctx, event.EventID, ProjectionKey, " ", 110); err == nil {
		t.Fatal("空 claim token 必须失败")
	}
	// 负时间。
	if _, err := s.AcknowledgeOutbox(ctx, event.EventID, ProjectionKey, *event.ClaimToken, -1); err == nil {
		t.Fatal("负 ack 时间必须失败")
	}
	// 正确提交。
	if ok, err := s.AcknowledgeOutbox(ctx, event.EventID, ProjectionKey, *event.ClaimToken, 110); err != nil || !ok {
		t.Fatalf("ack: ok=%v err=%v", ok, err)
	}
	// 重复 ack（已 dispatched）→ true 幂等。
	if ok, err := s.AcknowledgeOutbox(ctx, event.EventID, ProjectionKey, *event.ClaimToken, 111); err != nil || !ok {
		t.Fatalf("重复 ack: ok=%v err=%v", ok, err)
	}
	// 不存在的 event → false。
	if ok, err := s.AcknowledgeOutbox(ctx, "ghost", ProjectionKey, "token", 120); err != nil || ok {
		t.Fatalf("ghost ack: ok=%v err=%v", ok, err)
	}
	// gate 未就绪。
	blocked, _ := wkStoreLabeled(t, "-blocked", OwnerGate{})
	if _, err := blocked.AcknowledgeOutbox(ctx, "e", ProjectionKey, "t", 0); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("gate err=%v", err)
	}
}

func TestAcknowledgeOutboxIncidentProjection(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := s.CompareAndSetIncident(ctx, wkBaseIncident("scope-ack", "a1", 1)); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimOutbox(ctx, "worker", 100, 60_000, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	if ok, err := s.AcknowledgeOutbox(ctx, claimed[0].EventID, ProjectionKey, *claimed[0].ClaimToken, 150); err != nil || !ok {
		t.Fatalf("ack: ok=%v err=%v", ok, err)
	}
	var projected int64
	if err := db.QueryRow(`SELECT projected_ledger_revision FROM account_circuit_incidents WHERE circuit_scope_key='scope-ack'`).Scan(&projected); err != nil || projected != 1 {
		t.Fatalf("projected=%d err=%v", projected, err)
	}
}

func TestListForRebuildCursorPagination(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := s.CompareAndSetIncident(ctx, wkBaseIncident("scope-r1", "a1", 1)); err != nil {
		t.Fatal(err)
	}
	// 第二条 incident（更新时间相同 → scope key 决定顺序）。
	second := wkBaseIncident("scope-r2", "a1", 1)
	second.UpdatedAtMS = 150
	if _, err := s.CompareAndSetIncident(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompareAndSetIncident(ctx, wkBaseIncident("scope-r3", "a1", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE account_circuit_incidents SET updated_at_ms=150 WHERE circuit_scope_key='scope-r3'`); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListForRebuild(ctx, 10_000, 0, "", 2)
	if err != nil || len(page.Items) != 2 || page.NextCursor == nil {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	next, err := s.ListForRebuild(ctx, 10_000, page.NextCursor.UpdatedAtMS, page.NextCursor.CircuitScopeKey, 10)
	if err != nil || len(next.Items) == 0 {
		t.Fatalf("next=%+v err=%v", next, err)
	}
	// 越界参数。
	if _, err := s.ListForRebuild(ctx, -1, 0, "", 10); err == nil {
		t.Fatal("负 now 必须失败")
	}
	if _, err := s.ListForRebuild(ctx, 0, -1, "", 10); err == nil {
		t.Fatal("负 afterUpdated 必须失败")
	}
	if _, err := s.ListForRebuild(ctx, 0, 0, strings.Repeat("x", 2049), 10); err == nil {
		t.Fatal("超长 afterScope 必须失败")
	}
}

func TestListByRuntimeKeysBoundsAndDedup(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := s.CompareAndSetIncident(ctx, wkBaseIncident("scope-k1", "a1", 1)); err != nil {
		t.Fatal(err)
	}
	// 去重与 trimming。
	if _, err := s.ListByRuntimeKeys(ctx, []string{" a1 ", "a1"}, false, 10_000); err != nil {
		t.Fatal(err)
	}
	// 空白键必须失败关闭。
	if _, err := s.ListByRuntimeKeys(ctx, []string{"  "}, false, 10_000); err == nil {
		t.Fatal("空白 runtime key 必须失败")
	}
	// 超过 100 个键。
	many := make([]string, 101)
	for i := range many {
		many[i] = "k" + strconvItoa(i)
	}
	if _, err := s.ListByRuntimeKeys(ctx, many, false, 10_000); err == nil {
		t.Fatal("超过 100 键必须失败")
	}
	// retained closed 分支需要 now。
	if _, err := s.ListByRuntimeKeys(ctx, []string{"a1"}, true, -1); err == nil {
		t.Fatal("负 now 必须失败")
	}
}

func strconvItoa(v int) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}

func TestListProjectionGapsValidation(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := s.ListProjectionGaps(ctx, strings.Repeat("x", 257), 0, "", 10); err == nil {
		t.Fatal("超长 afterAccount 必须失败")
	}
	if _, err := s.ListProjectionGaps(ctx, "", -1, "", 10); err == nil {
		t.Fatal("负 afterUpdated 必须失败")
	}
	if _, err := s.ListProjectionGaps(ctx, "", 0, "", 0); err == nil {
		t.Fatal("limit 0 必须失败")
	}
	gaps, err := s.ListProjectionGaps(ctx, "", 0, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	// 新账户 revision=1、投影=0 → 天然存在 dispatch 缺口。
	if len(gaps.Dispatch) != 1 || gaps.Dispatch[0].AccountID != "a1" || gaps.Incidents != nil {
		t.Fatalf("gaps=%+v err=%v", gaps, err)
	}
}

func TestCleanupRemovesAckedAndClosed(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "t-clean", NowMS: 100}); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimOutbox(ctx, "worker", 100, 60_000, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatal(err)
	}
	if ok, err := s.AcknowledgeOutbox(ctx, claimed[0].EventID, ProjectionKey, *claimed[0].ClaimToken, 200); err != nil || !ok {
		t.Fatal(err)
	}
	// ack 时间早于清理水位之前 → 0。
	result, err := s.Cleanup(ctx, 1000, 100, 10)
	if err != nil || result.DeletedOutbox != 0 {
		t.Fatalf("early cleanup=%+v err=%v", result, err)
	}
	// 到期清理 outbox。
	result, err = s.Cleanup(ctx, 1000, 200, 10)
	if err != nil || result.DeletedOutbox != 1 {
		t.Fatalf("cleanup=%+v err=%v", result, err)
	}
	// closed 且已投影且无 pending outbox 的 incident 会被清理。
	closed := wkBaseIncident("scope-clean", "a1", 2)
	closed.State = "CLOSED"
	retained := int64(5000)
	closed.RetainedUntilMS = &retained
	closed.UpdatedAtMS = 500
	if _, err := s.CompareAndSetIncident(ctx, closed); err != nil {
		t.Fatal(err)
	}
	claimed2, err := s.ClaimOutbox(ctx, "worker", 500, 60_000, 10)
	if err != nil || len(claimed2) == 0 {
		t.Fatalf("claim2=%+v err=%v", claimed2, err)
	}
	if ok, err := s.AcknowledgeOutbox(ctx, claimed2[0].EventID, ProjectionKey, *claimed2[0].ClaimToken, 600); err != nil || !ok {
		t.Fatal(err)
	}
	result, err = s.Cleanup(ctx, 5000, 5000, 10)
	if err != nil || result.DeletedIncidents != 1 {
		t.Fatalf("incident cleanup=%+v err=%v", result, err)
	}
	// gate 未就绪。
	blocked, _ := wkStoreLabeled(t, "-blocked", OwnerGate{})
	if _, err := blocked.Cleanup(ctx, 0, 0, 1); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("gate err=%v", err)
	}
}

func TestScanIncidentRejectsCorruptRelations(t *testing.T) {
	valid := func(children, evidence string) *fakeScanner {
		columns := make([]any, 0, 44)
		// 按 incidentColumns 顺序填充 44 列。
		values := []any{
			"scope", "a1", "a1", "account",
			(*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil),
			(*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil),
			(*string)(nil), (*string)(nil),
			strPtr2("inc-1"), (*string)(nil), children, (*string)(nil), "OPEN", (*string)(nil),
			int64(1), int64(1), int64(1), int64(0), "tr", int64(0),
			(*int64)(nil), (*int64)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*int64)(nil),
			(*int64)(nil), (*int64)(nil), 0, int64(0), int64(0), int64(1), evidence, int64(0),
			(*string)(nil), (*int64)(nil), int64(100), int64(100),
		}
		columns = append(columns, values...)
		return &fakeScanner{values: columns}
	}
	if _, err := scanIncident(valid("null", "[]")); err == nil {
		t.Fatal("null children 必须失败")
	}
	if _, err := scanIncident(valid("{bad", "[]")); err == nil {
		t.Fatal("坏 children JSON 必须失败")
	}
	if _, err := scanIncident(valid(`["c1","c1"]`, "[]")); err == nil {
		t.Fatal("重复 children 必须失败")
	}
	if _, err := scanIncident(valid("[]", "null")); err == nil {
		t.Fatal("null evidence 必须失败")
	}
	if _, err := scanIncident(valid("[]", `["not-hash"]`)); err == nil {
		t.Fatal("非 SHA256 evidence 必须失败")
	}
	if _, err := scanIncident(valid("[]", "[]")); err != nil {
		t.Fatalf("合法 incident 不应失败: %v", err)
	}
}

type fakeScanner struct {
	values []any
}

func (f *fakeScanner) Scan(dest ...any) error {
	if len(dest) != len(f.values) {
		return errors.New("column count mismatch")
	}
	for i := range dest {
		switch target := dest[i].(type) {
		case *string:
			if v, ok := f.values[i].(*string); ok {
				if v == nil {
					target = nil
				} else {
					*target = *v
				}
			} else if v, ok := f.values[i].(string); ok {
				*target = v
			} else {
				return errors.New("type mismatch for string")
			}
		case *sql.NullString:
			switch v := f.values[i].(type) {
			case *string:
				target.Valid = v != nil
				if v != nil {
					target.String = *v
				}
			case string:
				target.Valid = true
				target.String = v
			default:
				target.Valid = false
			}
		case *int64:
			if v, ok := f.values[i].(int64); ok {
				*target = v
			} else {
				*target = int64(f.values[i].(int))
			}
		case *int:
			if v, ok := f.values[i].(int); ok {
				*target = v
			} else {
				*target = int(f.values[i].(int64))
			}
		case **string:
			*target = f.values[i].(*string)
		case **int64:
			*target = f.values[i].(*int64)
		default:
			return errors.New("unsupported scan target")
		}
	}
	return nil
}

func TestAdvanceAndClaimValidationExtras(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()
	// replay 身份冲突：同 transition id 不同账户身份。
	if _, err := db.Exec(`INSERT INTO accounts(id,dispatch_revision,circuit_projection_revision) VALUES ('a2',5,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "t-replay", NowMS: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a2", AccountRuntimeKey: "a2", TransitionID: "t-replay", NowMS: 101}); !errors.Is(err, ErrIdentityReplay) {
		t.Fatalf("replay err=%v", err)
	}
	// 不存在的账户。
	if _, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "ghost", AccountRuntimeKey: "ghost", TransitionID: "t-ghost", NowMS: 100}); err == nil {
		t.Fatal("缺失账户必须失败")
	}
	// lease 边界。
	if _, err := s.ClaimOutbox(ctx, "worker", 0, 0, 1); err == nil {
		t.Fatal("lease 0 必须失败")
	}
	if _, err := s.ClaimOutbox(ctx, "worker", 0, maxClaimLeaseMS+1, 1); err == nil {
		t.Fatal("lease 超限必须失败")
	}
	if _, err := s.ReleaseOutboxForReplay(ctx, "e", "t", "bad class!", 0, 0); err == nil {
		t.Fatal("非法 error class 必须失败")
	}
	if _, err := s.ReleaseOutboxForReplay(ctx, "e", "t", "timeout", 0, maxRetryDelayMS+1); err == nil {
		t.Fatal("delay 超限必须失败")
	}
	if _, err := s.ReleaseOutboxForReplay(ctx, "e", "t", "timeout", maxInt64, maxRetryDelayMS); err == nil {
		t.Fatal("时间溢出必须失败")
	}
}

func TestCheckContractGateBlocked(t *testing.T) {
	blocked, _ := wkStoreLabeled(t, "-gateblocked", OwnerGate{})
	if err := blocked.CheckContract(context.Background()); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("gate err=%v", err)
	}
}

func TestCheckContractPostgresModeOnSQLiteFails(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	// Postgres 模式索引校验查询在 SQLite 上必然失败（缺 pg_catalog）。
	s.mode = Postgres
	if err := s.CheckContract(context.Background()); err == nil {
		t.Fatal("Postgres 索引校验在 SQLite 上必须失败")
	}
}

func TestAdvanceAndListValidationExtras(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "  ", NowMS: 1}); err == nil {
		t.Fatal("空 transition 必须失败")
	}
	if _, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "  ", TransitionID: "t", NowMS: 1}); err == nil {
		t.Fatal("空 runtime key 必须失败")
	}
	if _, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: strings.Repeat("t", 257), NowMS: 1}); err == nil {
		t.Fatal("超长 transition 必须失败")
	}
	if _, err := s.ListDispatchRevisions(ctx, strings.Repeat("x", 257), 1); err == nil {
		t.Fatal("超长 afterAccount 必须失败")
	}
	if _, err := s.ListDispatchRevisions(ctx, "", maxBatchLimit+1); err == nil {
		t.Fatal("limit 超限必须失败")
	}
	blocked, _ := wkStoreLabeled(t, "-listblocked", OwnerGate{})
	if _, err := blocked.ListDispatchRevisions(ctx, "", 1); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("gate err=%v", err)
	}
}

func TestAcknowledgeOutboxWrongToken(t *testing.T) {
	s, _ := wkReadyStore(t)
	defer s.db.Close()
	ctx := context.Background()
	if _, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "t-wrong", NowMS: 100}); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimOutbox(ctx, "worker", 100, 60_000, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatal(err)
	}
	if ok, err := s.AcknowledgeOutbox(ctx, claimed[0].EventID, ProjectionKey, "wrong-token", 110); err != nil || ok {
		t.Fatalf("wrong token ack: ok=%v err=%v", ok, err)
	}
}

func TestValidateIncidentExtendedBranches(t *testing.T) {
	base := func() Incident {
		id := "inc"
		return Incident{CircuitScopeKey: "scope", AccountID: "account", AccountRuntimeKey: "runtime", ScopeKind: "account", IncidentID: &id, State: "OPEN", DispatchRevision: 1, TransitionID: "transition", ConfirmationFailuresRequired: 1, UpdatedAtMS: 100, ChildIncidentIDs: []string{}, ConfirmationFailureEvidenceKeys: []string{}}
	}
	expectFail := func(name string, edit func(*Incident)) {
		t.Helper()
		incident := base()
		edit(&incident)
		err := validateIncident(&incident)
		if timesErr := validateIncidentTimes(&incident, incident.UpdatedAtMS); err == nil {
			err = timesErr
		}
		if err == nil {
			t.Fatalf("%s 必须失败", name)
		}
	}
	lease := "lease"
	expectFail("invalid lease purpose", func(v *Incident) {
		purpose := "mystery"
		v.LeaseID, v.LeasePurpose, v.LeaseUntilMS = &lease, &purpose, int64Ptr2(500)
	})
	expectFail("invalid failure class", func(v *Incident) {
		class := "mystery"
		v.LastFailureClass = &class
	})
	expectFail("attempt timestamps without lease", func(v *Incident) {
		started := int64(100)
		v.AttemptStartedAtMS = &started
	})
	expectFail("active lease without attempt times", func(v *Incident) {
		purpose, until := "confirmation", int64(500)
		v.LeaseID, v.LeasePurpose, v.LeaseUntilMS = &lease, &purpose, &until
	})
	expectFail("attempt start after hard deadline", func(v *Incident) {
		purpose, until := "confirmation", int64(500)
		started, deadline := int64(900), int64(400)
		v.LeaseID, v.LeasePurpose, v.LeaseUntilMS = &lease, &purpose, &until
		v.AttemptStartedAtMS, v.AttemptHardDeadlineMS = &started, &deadline
	})
	expectFail("negative open until", func(v *Incident) {
		negative := int64(-5)
		v.OpenUntilMS = &negative
	})
	expectFail("created after updated", func(v *Incident) {
		v.CreatedAtMS = 200
	})
	expectFail("too many children", func(v *Incident) {
		v.ChildIncidentIDs = make([]string, 65)
		for i := range v.ChildIncidentIDs {
			v.ChildIncidentIDs[i] = "child" + strconvItoa(i)
		}
	})
	expectFail("evidence beyond bound", func(v *Incident) {
		v.ConfirmationFailureEvidenceKeys = make([]string, 3)
		for i := range v.ConfirmationFailureEvidenceKeys {
			v.ConfirmationFailureEvidenceKeys[i] = strings.Repeat("a", 64)
		}
	})
	expectFail("numeric invalid", func(v *Incident) {
		v.Generation = -1
	})
	expectFail("consecutive beyond required", func(v *Incident) {
		v.ConsecutiveFailures = 5
	})
	expectFail("closed without retention", func(v *Incident) {
		v.State = "CLOSED"
	})
	expectFail("blank child id", func(v *Incident) {
		v.ChildIncidentIDs = []string{"   "}
	})
	expectFail("invalid optional text", func(v *Incident) {
		blank := " "
		v.ProtocolCode = &blank
		v.ScopeKind = "protocol_model"
		v.ProtocolCode = strPtr2("openai")
		v.RequestLane = strPtr2("text")
		v.ModelFamily = strPtr2("family")
		v.KeyFingerprint = &blank
	})
	// 合法：evidence 大小写归一 + child 归一。
	normalized := base()
	normalized.ConfirmationFailureEvidenceKeys = []string{"  " + strings.Repeat("A", 64) + "  "}
	normalized.ConfirmationFailuresRequired = 1
	if err := validateIncident(&normalized); err != nil {
		t.Fatalf("归一化 incident 被拒: %v", err)
	}
	if got := normalized.ConfirmationFailureEvidenceKeys[0]; got != strings.Repeat("a", 64) {
		t.Fatalf("evidence 未小写归一: %q", got)
	}
}

func TestSmallHelpers(t *testing.T) {
	if value(nil) != "" || value(strPtr2("x")) != "x" {
		t.Fatal("value 不符")
	}
	var nilIncident *Incident
	if optionalIncident(Incident{}, false) != nilIncident && optionalIncident(Incident{}, false) != nil {
		t.Fatal("optionalIncident(false) 必须为 nil")
	}
	if optionalIncident(Incident{}, true) == nil {
		t.Fatal("optionalIncident(true) 不应为 nil")
	}
	if boolInt(true) != 1 || boolInt(false) != 0 {
		t.Fatal("boolInt 不符")
	}
	if nullableText("") != nil || nullableText("x") == "" {
		t.Fatal("nullableText 不符")
	}
	if _, err := normalizeErrorClass("   "); err == nil {
		t.Fatal("空白 error class 必须失败")
	}
	if _, err := normalizeErrorClass(strings.Repeat("x", 65)); err == nil {
		t.Fatal("超长 error class 必须失败")
	}
	if normalized, err := normalizeErrorClass("  timeout "); err != nil || normalized != "timeout" {
		t.Fatalf("normalize=%q err=%v", normalized, err)
	}
}

func TestReadIncidentsRejectsCorruptRows(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()
	// 直接注入 children JSON 为空的 incident 行，读取路径必须失败关闭。
	if _, err := db.Exec(`INSERT INTO account_circuit_incidents (
      circuit_scope_key, account_id, account_runtime_key, scope_kind, incident_id,
      child_incident_ids_json, state, generation, dispatch_revision, ledger_revision,
      projected_ledger_revision, transition_id, cooldown_observation_generation,
      upstream_attempt_observed, backoff_level, consecutive_failures,
      confirmation_failures_required, confirmation_failure_evidence_keys_json,
      recovering_successes, created_at_ms, updated_at_ms
    ) VALUES ('scope-corrupt', 'a1', 'a1', 'account', 'inc-corrupt', '', 'OPEN', 1, 1, 1, 0, 'tr', 0, 0, 0, 0, 1, '[]', 0, 100, 100)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListForRebuild(ctx, 10_000, 0, "", 10); err == nil {
		t.Fatal("损坏行必须使读取失败")
	}
	if _, err := s.ListProjectionGaps(ctx, "", 0, "", 10); err == nil {
		t.Fatal("损坏行走缺口查询也必须失败")
	}
}
