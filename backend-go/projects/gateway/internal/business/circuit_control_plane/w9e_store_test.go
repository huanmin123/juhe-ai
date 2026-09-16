package circuitcontrolplane

// w9e 覆盖率战役：补 store.go 构造校验、索引契约分支、各入口的参数围栏、
// CAS/出站的身份重放与幂等分支、损坏行扫描分支。全部基于既有 SQLite
// 内存 fixture（wkStoreLabeled），不依赖外部资源。

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestW9ENewStoreValidation(t *testing.T) {
	if _, err := New(nil, SQLite, "", OwnerGate{Confirmed: true}); err == nil {
		t.Fatal("nil db 必须失败")
	}
	_, db := wkReadyStore(t)
	defer db.Close()
	if _, err := New(db, Mode("mysql"), "", OwnerGate{Confirmed: true}); err == nil {
		t.Fatal("非法 mode 必须失败")
	}
	// Postgres schema 默认值与非法标识符（不触库）。
	pg, err := New(db, Postgres, "", OwnerGate{Confirmed: true})
	if err != nil {
		t.Fatalf("postgres 空 schema 应默认 %q: %v", defaultBusinessSchema, err)
	}
	if pg.schema != defaultBusinessSchema {
		t.Fatalf("默认 schema = %q", pg.schema)
	}
	if _, err := New(db, Postgres, `bad-schema`, OwnerGate{Confirmed: true}); !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("非法 postgres schema 应返回 ErrInvalidSchema, got %v", err)
	}
	if _, err := New(db, Postgres, "  ", OwnerGate{Confirmed: true}); err != nil {
		t.Fatalf("空白 schema 应默认化: %v", err)
	}
}

func w9eDDLDB(t *testing.T, name string, indexDDL string) (*sql.DB, error) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+strings.ReplaceAll(t.Name(), "/", "-")+"-"+name+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range wkDDLs {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if indexDDL != "" {
		if _, err := db.Exec(indexDDL); err != nil {
			t.Fatal(err)
		}
	}
	return db, nil
}

func TestW9ECheckContractIndexDefinitionBranches(t *testing.T) {
	ctx := context.Background()
	gate := OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}

	// 缺索引 → missing。
	db, _ := w9eDDLDB(t, "noindex", "")
	s, err := New(db, SQLite, "", gate)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CheckContract(ctx); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("缺索引应报 missing, got %v", err)
	}

	// 唯一性不符。
	db2, _ := w9eDDLDB(t, "nonunique", `CREATE INDEX idx_account_circuit_incidents_key_model_capability ON account_circuit_incidents(scope_kind, capability_hash)`)
	s2, _ := New(db2, SQLite, "", gate)
	if err := s2.CheckContract(ctx); err == nil || !strings.Contains(err.Error(), "unique=false") {
		t.Fatalf("非唯一索引应报 unique 不符, got %v", err)
	}

	// 列不符。
	db3, _ := w9eDDLDB(t, "wrongcols", `CREATE UNIQUE INDEX idx_account_circuit_incidents_key_model_capability ON account_circuit_incidents(scope_kind) WHERE scope_kind = 'key_model' AND capability_hash IS NOT NULL`)
	s3, _ := New(db3, SQLite, "", gate)
	if err := s3.CheckContract(ctx); err == nil || !strings.Contains(err.Error(), "columns=") {
		t.Fatalf("列不符应报 columns, got %v", err)
	}

	// 缺部分谓词（required 为 partial）。
	db4, _ := w9eDDLDB(t, "nopartial", `CREATE UNIQUE INDEX idx_account_circuit_incidents_key_model_capability ON account_circuit_incidents(scope_kind, capability_hash)`)
	s4, _ := New(db4, SQLite, "", gate)
	if err := s4.CheckContract(ctx); err == nil || (!strings.Contains(err.Error(), "predicate") && !strings.Contains(err.Error(), "partial")) {
		t.Fatalf("缺谓词应报 predicate/partial, got %v", err)
	}

	// 契约表缺失（无 accounts 表）。
	db5, _ := sql.Open("sqlite", "file:"+strings.ReplaceAll(t.Name(), "/", "-")+"-notables?mode=memory&cache=shared")
	defer db5.Close()
	s5, _ := New(db5, SQLite, "", gate)
	if err := s5.CheckContract(ctx); err == nil || !strings.Contains(err.Error(), "accounts") {
		t.Fatalf("缺表应报 verify circuit contract accounts, got %v", err)
	}
}

func TestW9EPredicatesAndColumnsHelpers(t *testing.T) {
	if sameIndexColumns([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("长度不同必须不等")
	}
	if sameIndexColumns([]string{"a", "b"}, []string{"a", "c"}) {
		t.Fatal("列不同必须不等")
	}
	if !sameIndexColumns([]string{"a", "b"}, []string{"a", "b"}) {
		t.Fatal("相同列必须相等")
	}
	// 谓词归一化：引号/括号/cast/空白/and 顺序。
	if !predicatesEquivalent(
		`scope_kind = 'key_model' AND capability_hash IS NOT NULL`,
		"(SCOPE_KIND = 'key_model') AND (CAPABILITY_HASH IS NOT NULL)") {
		t.Fatal("大小写与括号差异应等价")
	}
	if !predicatesEquivalent(
		"a::text = 'x' AND b = 1",
		"b = 1 AND a = 'x'") {
		t.Fatal("cast 与 and 顺序差异应等价")
	}
	if predicatesEquivalent("a = 1", "a = 2") {
		t.Fatal("不同谓词必须不等")
	}
	if indexPredicate("CREATE INDEX x ON t(c)") != "" {
		t.Fatal("无 where 的定义谓词应为空")
	}
	if got := indexPredicate("CREATE INDEX x ON t(c) WHERE scope_kind = 'key_model'"); got != "scope_kind = 'key_model'" {
		t.Fatalf("indexPredicate = %q", got)
	}
}

func TestW9EAdvanceDispatchRevisionFences(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()

	if _, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "  ", AccountRuntimeKey: "a1", TransitionID: "t", NowMS: 1}); err == nil {
		t.Fatal("空白 account id 必须失败")
	}
	if _, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: " ", TransitionID: "t", NowMS: 1}); err == nil {
		t.Fatal("空白 runtime key 必须失败")
	}
	if _, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: " ", NowMS: 1}); err == nil {
		t.Fatal("空白 transition id 必须失败")
	}
	if _, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "t", NowMS: -1}); err == nil {
		t.Fatal("负 nowMS 必须失败")
	}
	if _, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: strings.Repeat("x", 257), AccountRuntimeKey: "a1", TransitionID: "t", NowMS: 1}); err == nil {
		t.Fatal("超长 account id 必须失败")
	}
	// 未知账户：SQL no rows 原样返回。
	if _, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "ghost", AccountRuntimeKey: "a1", TransitionID: "t-ghost", NowMS: 1}); err == nil {
		t.Fatal("未知账户必须失败")
	}
	// 同 transition id 不同身份 → ErrIdentityReplay。
	if _, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "w9e-tr", NowMS: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "w9e-tr", NowMS: 2}); err != nil {
		t.Fatalf("同身份重放应幂等成功, got %v", err)
	}
	if _, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "other", TransitionID: "w9e-tr", NowMS: 2}); !errors.Is(err, ErrIdentityReplay) {
		t.Fatalf("异身份重放应 ErrIdentityReplay, got %v", err)
	}
}

func TestW9EListDispatchRevisionsBounds(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := s.ListDispatchRevisions(ctx, "", 0); err == nil {
		t.Fatal("limit=0 必须失败")
	}
	if _, err := s.ListDispatchRevisions(ctx, "", maxBatchLimit+1); err == nil {
		t.Fatal("limit 超界必须失败")
	}
	if _, err := s.ListDispatchRevisions(ctx, strings.Repeat("x", 257), 10); err == nil {
		t.Fatal("超长 afterAccountId 必须失败")
	}
	page, err := s.ListDispatchRevisions(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].AccountID != "a1" {
		t.Fatalf("page = %+v", page)
	}
	if page.NextAfterAccountID != "" {
		t.Fatalf("不足一页时不应有游标, got %q", page.NextAfterAccountID)
	}
}

func TestW9ELoadIncidentForProjectionBranches(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()

	// 身份不完整。
	if _, err := s.LoadIncidentForProjection(ctx, Outbox{AccountID: ""}); err == nil {
		t.Fatal("缺 account id 必须失败")
	}
	if _, err := s.LoadIncidentForProjection(ctx, Outbox{AccountID: "a1"}); err == nil {
		t.Fatal("缺 scope key 必须失败")
	}
	if _, err := s.LoadIncidentForProjection(ctx, Outbox{AccountID: "a1", CircuitScopeKey: strPtr2("s")}); err == nil {
		t.Fatal("缺 incident id 必须失败")
	}
	// 账户缺失 → missing。
	load, err := s.LoadIncidentForProjection(ctx, Outbox{AccountID: "ghost", CircuitScopeKey: strPtr2("s"), IncidentID: strPtr2("i"), DispatchRevision: 1})
	if err != nil || load.Status != "missing" {
		t.Fatalf("ghost account → missing, got %+v err=%v", load, err)
	}
	// 建立真实 incident。
	mut := wkBaseIncident("w9e-scope", "a1", 1)
	res, err := s.CompareAndSetIncident(ctx, mut)
	if err != nil || res.Status != "applied" {
		t.Fatalf("seed incident failed: %+v err=%v", res, err)
	}
	event := Outbox{AccountID: "a1", CircuitScopeKey: strPtr2("w9e-scope"), IncidentID: strPtr2("inc-w9e-scope"), DispatchRevision: 1, Generation: int64Ptr2(1), LedgerRevision: int64Ptr2(1)}
	// revision 不符 → stale。
	stale, err := s.LoadIncidentForProjection(ctx, Outbox{AccountID: "a1", CircuitScopeKey: strPtr2("w9e-scope"), IncidentID: strPtr2("inc-w9e-scope"), DispatchRevision: 2})
	if err != nil || stale.Status != "stale" || stale.CurrentDispatchRevision != 1 {
		t.Fatalf("stale load = %+v err=%v", stale, err)
	}
	// generation 不符 → missing。
	genMismatch := event
	genMismatch.Generation = int64Ptr2(99)
	miss, err := s.LoadIncidentForProjection(ctx, genMismatch)
	if err != nil || miss.Status != "missing" {
		t.Fatalf("generation mismatch → missing, got %+v err=%v", miss, err)
	}
	// ledger revision 高于 incident → missing。
	ledgerMismatch := event
	ledgerMismatch.LedgerRevision = int64Ptr2(5)
	miss2, err := s.LoadIncidentForProjection(ctx, ledgerMismatch)
	if err != nil || miss2.Status != "missing" {
		t.Fatalf("ledger mismatch → missing, got %+v err=%v", miss2, err)
	}
	// 完全匹配 → current。
	current, err := s.LoadIncidentForProjection(ctx, event)
	if err != nil || current.Status != "current" || current.Incident.CircuitScopeKey == "" {
		t.Fatalf("current load = %+v err=%v", current, err)
	}
	// incident id 不符 → missing。
	wrongIncident := event
	wrongIncident.IncidentID = strPtr2("inc-other")
	miss3, err := s.LoadIncidentForProjection(ctx, wrongIncident)
	if err != nil || miss3.Status != "missing" {
		t.Fatalf("incident id mismatch → missing, got %+v err=%v", miss3, err)
	}
}

func TestW9ECompareAndSetIncidentFences(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()

	mut := wkBaseIncident("w9e-cas", "a1", 1)
	mut.ExpectedLedgerRevision = int64Ptr2(-1)
	if _, err := s.CompareAndSetIncident(ctx, mut); err == nil {
		t.Fatal("负 expected ledger revision 必须失败")
	}
	mut2 := wkBaseIncident("w9e-cas", "a1", 1)
	mut2.UpdatedAtMS = -5
	if _, err := s.CompareAndSetIncident(ctx, mut2); err == nil {
		t.Fatal("负 updatedAtMs 必须失败")
	}
	// 账户不存在 → account_not_found 终态。
	ghost := wkBaseIncident("w9e-ghost", "ghost", 1)
	res, err := s.CompareAndSetIncident(ctx, ghost)
	if err != nil || res.Status != "account_not_found" {
		t.Fatalf("ghost account = %+v err=%v", res, err)
	}
	// 账户逻辑删除 → account_not_found。
	if _, err := db.Exec(`INSERT INTO accounts(id,dispatch_revision,circuit_projection_revision,deleted_at) VALUES ('del',1,0,'2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	deleted := wkBaseIncident("w9e-del", "del", 1)
	res2, err := s.CompareAndSetIncident(ctx, deleted)
	if err != nil || res2.Status != "account_not_found" {
		t.Fatalf("deleted account = %+v err=%v", res2, err)
	}
	// dispatch revision 不符 → stale。
	stale := wkBaseIncident("w9e-stale", "a1", 99)
	res3, err := s.CompareAndSetIncident(ctx, stale)
	if err != nil || res3.Status != "stale_dispatch_revision" || res3.CurrentDispatchRevision != 1 {
		t.Fatalf("stale = %+v err=%v", res3, err)
	}
	// 首写成功。
	first := wkBaseIncident("w9e-flow", "a1", 1)
	applied, err := s.CompareAndSetIncident(ctx, first)
	if err != nil || applied.Status != "applied" || applied.Incident.LedgerRevision != 1 {
		t.Fatalf("first apply = %+v err=%v", applied, err)
	}
	// 创建撞已有 scope（expected=nil + found）→ cas_conflict。
	conflict := wkBaseIncident("w9e-flow", "a1", 1)
	conflict.Incident.TransitionID = "tr-w9e-flow-conflict"
	conflictRes, err := s.CompareAndSetIncident(ctx, conflict)
	if err != nil || conflictRes.Status != "cas_conflict" || conflictRes.Incident == nil {
		t.Fatalf("conflict = %+v err=%v", conflictRes, err)
	}
	// 正确 expected ledger 重放同 transition → idempotent。
	replay := wkBaseIncident("w9e-flow", "a1", 1)
	replay.ExpectedLedgerRevision = int64Ptr2(1)
	idem, err := s.CompareAndSetIncident(ctx, replay)
	if err != nil || idem.Status != "idempotent" || idem.Incident == nil {
		t.Fatalf("idempotent = %+v err=%v", idem, err)
	}
	// 同 transition id 不同 scope → ErrIdentityReplay。
	identity := wkBaseIncident("w9e-flow2", "a1", 1)
	identity.Incident.TransitionID = "tr-w9e-flow"
	if _, err := s.CompareAndSetIncident(ctx, identity); !errors.Is(err, ErrIdentityReplay) {
		t.Fatalf("异 scope 重放应 ErrIdentityReplay, got %v", err)
	}
	// 第二代推进（expected=1 → ledger=2）。
	second := wkBaseIncident("w9e-flow", "a1", 1)
	second.Incident.State = "RECOVERING"
	second.Incident.TransitionID = "tr-w9e-flow-2"
	second.ExpectedLedgerRevision = int64Ptr2(1)
	applied2, err := s.CompareAndSetIncident(ctx, second)
	if err != nil || applied2.Status != "applied" || applied2.Incident.LedgerRevision != 2 {
		t.Fatalf("second apply = %+v err=%v", applied2, err)
	}
	// generation 回退 → cas_conflict。
	regress := wkBaseIncident("w9e-flow", "a1", 1)
	regress.Incident.Generation = 0
	regress.Incident.TransitionID = "tr-w9e-flow-3"
	regress.ExpectedLedgerRevision = int64Ptr2(2)
	regressRes, err := s.CompareAndSetIncident(ctx, regress)
	if err != nil || regressRes.Status != "cas_conflict" {
		t.Fatalf("generation regress = %+v err=%v", regressRes, err)
	}
	// expected ledger 与现状不符 → cas_conflict。
	wrongFence := wkBaseIncident("w9e-flow", "a1", 1)
	wrongFence.Incident.TransitionID = "tr-w9e-flow-4"
	wrongFence.ExpectedLedgerRevision = int64Ptr2(42)
	wrongRes, err := s.CompareAndSetIncident(ctx, wrongFence)
	if err != nil || wrongRes.Status != "cas_conflict" {
		t.Fatalf("wrong fence = %+v err=%v", wrongRes, err)
	}
	// 事故归属另一账户。
	other := wkBaseIncident("w9e-flow", "a2", 1)
	other.Incident.TransitionID = "tr-w9e-flow-5"
	other.ExpectedLedgerRevision = int64Ptr2(2)
	if _, err := db.Exec(`INSERT INTO accounts(id,dispatch_revision,circuit_projection_revision) VALUES ('a2',1,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompareAndSetIncident(ctx, other); err == nil || !strings.Contains(err.Error(), "another account") {
		t.Fatalf("他账户 scope 应报错, got %v", err)
	}
}

func TestW9EClaimOutboxFences(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()

	if _, err := s.ClaimOutbox(ctx, " ", 1, 1000, 10); err == nil {
		t.Fatal("空白 owner 必须失败")
	}
	if _, err := s.ClaimOutbox(ctx, strings.Repeat("o", 129), 1, 1000, 10); err == nil {
		t.Fatal("超长 owner 必须失败")
	}
	if _, err := s.ClaimOutbox(ctx, "owner", 1, 0, 10); err == nil {
		t.Fatal("lease=0 必须失败")
	}
	if _, err := s.ClaimOutbox(ctx, "owner", 1, maxClaimLeaseMS+1, 10); err == nil {
		t.Fatal("lease 超界必须失败")
	}
	if _, err := s.ClaimOutbox(ctx, "owner", 1, 1000, 0); err == nil {
		t.Fatal("limit=0 必须失败")
	}
	if _, err := s.ClaimOutbox(ctx, "owner", -1, 1000, 10); err == nil {
		t.Fatal("负 nowMS 必须失败")
	}
	if _, err := s.ClaimOutbox(ctx, "owner", maxInt64, maxClaimLeaseMS, 10); err == nil {
		t.Fatal("lease 溢出必须失败")
	}
	// 正常 claim + 过期重claim。
	if _, err := db.Exec(`INSERT INTO account_circuit_outbox (event_id,projection_key,dedupe_key,event_type,account_id,account_runtime_key,transition_id,dispatch_revision,status,available_at_ms,attempt_count,created_at_ms,updated_at_ms) VALUES ('e1','circuit-control-plane','d1','incident_changed','a1','a1','t1',1,'pending',100,0,100,100)`); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimOutbox(ctx, "owner", 200, 1000, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %+v err=%v", claimed, err)
	}
	// 租约期内再次 claim 拿不到。
	again, err := s.ClaimOutbox(ctx, "owner", 300, 1000, 10)
	if err != nil || len(again) != 0 {
		t.Fatalf("租约期内不应重claim, got %+v err=%v", again, err)
	}
	// 租约过期后可重claim。
	renewed, err := s.ClaimOutbox(ctx, "owner", 1300, 1000, 10)
	if err != nil || len(renewed) != 1 || renewed[0].AttemptCount != 2 {
		t.Fatalf("过期重claim = %+v err=%v", renewed, err)
	}
}

func TestW9EAcknowledgeOutboxFences(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()

	if err := s.ackSeed(ctx, db); err != nil {
		t.Fatal(err)
	}
	// 投影键不符。
	ok, err := s.AcknowledgeOutbox(ctx, "e1", "wrong-key", "tok", 200)
	if err != nil || ok {
		t.Fatalf("投影键不符应 false, got %v err=%v", ok, err)
	}
	// 事件不存在。
	ok, err = s.AcknowledgeOutbox(ctx, "missing", ProjectionKey, "tok", 200)
	if err != nil || ok {
		t.Fatalf("missing 事件应 false, got %v err=%v", ok, err)
	}
	// token 不符。
	ok, err = s.AcknowledgeOutbox(ctx, "e1", ProjectionKey, "bad-token", 200)
	if err != nil || ok {
		t.Fatalf("token 不符应 false, got %v err=%v", ok, err)
	}
	// 正确 ack。
	ok, err = s.AcknowledgeOutbox(ctx, "e1", ProjectionKey, "tok", 200)
	if err != nil || !ok {
		t.Fatalf("ack 应成功, got %v err=%v", ok, err)
	}
	// 已 dispatched 再 ack → 幂等 true。
	ok, err = s.AcknowledgeOutbox(ctx, "e1", ProjectionKey, "tok", 300)
	if err != nil || !ok {
		t.Fatalf("重复 ack 应幂等 true, got %v err=%v", ok, err)
	}
}

func (s *Store) ackSeed(ctx context.Context, db *sql.DB) error {
	_, err := db.Exec(`INSERT INTO account_circuit_outbox (event_id,projection_key,dedupe_key,event_type,account_id,account_runtime_key,transition_id,dispatch_revision,status,available_at_ms,claim_token,claimed_by,claim_until_ms,attempt_count,created_at_ms,updated_at_ms) VALUES ('e1','` + ProjectionKey + `','d1','incident_changed','a1','a1','t1',1,'processing',100,'tok','owner',9999,1,100,100)`)
	return err
}

func TestW9EReleaseOutboxForReplayFences(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()

	if err := s.ackSeed(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReleaseOutboxForReplay(ctx, " ", "tok", "timeout_before_complete", 200, 10); err == nil {
		t.Fatal("空白 event id 必须失败")
	}
	if _, err := s.ReleaseOutboxForReplay(ctx, "e1", " ", "timeout_before_complete", 200, 10); err == nil {
		t.Fatal("空白 token 必须失败")
	}
	if _, err := s.ReleaseOutboxForReplay(ctx, "e1", "tok", "  ", 200, 10); err == nil {
		t.Fatal("空白 error class 必须失败")
	}
	if _, err := s.ReleaseOutboxForReplay(ctx, "e1", "tok", "bad class!", 200, 10); err == nil {
		t.Fatal("非法 error class 必须失败")
	}
	if _, err := s.ReleaseOutboxForReplay(ctx, "e1", "tok", strings.Repeat("x", 65), 200, 10); err == nil {
		t.Fatal("超长 error class 必须失败")
	}
	if _, err := s.ReleaseOutboxForReplay(ctx, "e1", "tok", "timeout_before_complete", 200, -1); err == nil {
		t.Fatal("负 delay 必须失败")
	}
	if _, err := s.ReleaseOutboxForReplay(ctx, "e1", "tok", "timeout_before_complete", 200, maxRetryDelayMS+1); err == nil {
		t.Fatal("delay 超界必须失败")
	}
	if _, err := s.ReleaseOutboxForReplay(ctx, "e1", "tok", "timeout_before_complete", -1, 10); err == nil {
		t.Fatal("负 nowMS 必须失败")
	}
	if _, err := s.ReleaseOutboxForReplay(ctx, "e1", "tok", "timeout_before_complete", maxInt64, maxRetryDelayMS); err == nil {
		t.Fatal("时间溢出必须失败")
	}
	// token 不符 → false。
	ok, err := s.ReleaseOutboxForReplay(ctx, "e1", "wrong", "timeout_before_complete", 200, 10)
	if err != nil || ok {
		t.Fatalf("token 不符应 false, got %v err=%v", ok, err)
	}
	// 正常释放。
	ok, err = s.ReleaseOutboxForReplay(ctx, "e1", "tok", " timeout_before_complete ", 200, 50)
	if err != nil || !ok {
		t.Fatalf("release 应成功, got %v err=%v", ok, err)
	}
	var status, errClass string
	var availableAt int64
	if err := db.QueryRow(`SELECT status,last_error_class,available_at_ms FROM account_circuit_outbox WHERE event_id='e1'`).Scan(&status, &errClass, &availableAt); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || errClass != "timeout_before_complete" || availableAt != 250 {
		t.Fatalf("released row = %s %s %d", status, errClass, availableAt)
	}
}

func TestW9EListForRebuildFences(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()

	if _, err := s.ListForRebuild(ctx, 100, 0, strings.Repeat("x", 2049), 10); err == nil {
		t.Fatal("超长 afterScope 必须失败")
	}
	if _, err := s.ListForRebuild(ctx, 100, 0, "", 0); err == nil {
		t.Fatal("limit=0 必须失败")
	}
	if _, err := s.ListForRebuild(ctx, -1, 0, "", 10); err == nil {
		t.Fatal("负 nowMS 必须失败")
	}
	if _, err := s.ListForRebuild(ctx, 100, -5, "", 10); err == nil {
		t.Fatal("负 afterUpdatedMS 必须失败")
	}
	page, err := s.ListForRebuild(ctx, 10_000, 0, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 || page.NextCursor != nil {
		t.Fatalf("空库 page = %+v", page)
	}
	// 有数据时游标为最后一行；不足一页时游标清空。
	if _, err := s.CompareAndSetIncident(ctx, wkBaseIncident("w9e-rb", "a1", 1)); err != nil {
		t.Fatal(err)
	}
	page2, err := s.ListForRebuild(ctx, 10_000, 0, "", 10)
	if err != nil || len(page2.Items) != 1 {
		t.Fatalf("page2 = %+v err=%v", page2, err)
	}
	if page2.NextCursor != nil {
		t.Fatalf("不足一页应无游标, got %+v", page2.NextCursor)
	}
	// limit=1 恰好等于结果数 → 带游标。
	page3, err := s.ListForRebuild(ctx, 10_000, 0, "", 1)
	if err != nil || len(page3.Items) != 1 || page3.NextCursor == nil {
		t.Fatalf("满页应有游标, page3=%+v err=%v", page3, err)
	}
}

func TestW9EListByRuntimeKeysFences(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()

	got, err := s.ListByRuntimeKeys(ctx, nil, false, 100)
	if err != nil || len(got) != 0 {
		t.Fatalf("空 keys = %+v err=%v", got, err)
	}
	if _, err := s.ListByRuntimeKeys(ctx, []string{" "}, false, 100); err == nil {
		t.Fatal("非法 runtime key 必须失败")
	}
	tooMany := make([]string, 101)
	for i := range tooMany {
		tooMany[i] = "k" + strings.Repeat("x", i+1)
	}
	if _, err := s.ListByRuntimeKeys(ctx, tooMany, false, 100); err == nil {
		t.Fatal(">100 keys 必须失败")
	}
	if _, err := s.ListByRuntimeKeys(ctx, []string{"a1"}, false, -1); err == nil {
		t.Fatal("负 nowMS 必须失败")
	}
	// 去重后查询。
	if _, err := s.CompareAndSetIncident(ctx, wkBaseIncident("w9e-rt", "a1", 1)); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListByRuntimeKeys(ctx, []string{"a1", " a1 "}, true, 100)
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %+v err=%v", items, err)
	}
}

func TestW9EListProjectionGapsFences(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()

	if _, err := s.ListProjectionGaps(ctx, strings.Repeat("x", 257), 0, "", 10); err == nil {
		t.Fatal("超长 afterAccountId 必须失败")
	}
	if _, err := s.ListProjectionGaps(ctx, "", 0, strings.Repeat("x", 2049), 10); err == nil {
		t.Fatal("超长 afterScope 必须失败")
	}
	if _, err := s.ListProjectionGaps(ctx, "", 0, "", 0); err == nil {
		t.Fatal("limit=0 必须失败")
	}
	if _, err := s.ListProjectionGaps(ctx, "", -1, "", 10); err == nil {
		t.Fatal("负 afterUpdatedMS 必须失败")
	}
	gaps, err := s.ListProjectionGaps(ctx, "", 0, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	// fixture 的 a1 行 projection=0 < dispatch=1，本身就是一个 dispatch 缺口。
	if len(gaps.Dispatch) != 1 || gaps.Dispatch[0].AccountID != "a1" {
		t.Fatalf("空库 dispatch gaps = %+v", gaps.Dispatch)
	}
	// 事故已写但投影未推进 → incident 缺口。
	if _, err := s.CompareAndSetIncident(ctx, wkBaseIncident("w9e-gap", "a1", 1)); err != nil {
		t.Fatal(err)
	}
	gaps2, err := s.ListProjectionGaps(ctx, "", 0, "", 10)
	if err != nil || len(gaps2.Incidents) != 1 {
		t.Fatalf("gaps2 = %+v err=%v", gaps2, err)
	}
}

func TestW9ECleanupFencesAndEffects(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()

	if _, err := s.Cleanup(ctx, 100, 100, 0); err == nil {
		t.Fatal("limit=0 必须失败")
	}
	if _, err := s.Cleanup(ctx, -1, 100, 10); err == nil {
		t.Fatal("负 nowMS 必须失败")
	}
	if _, err := s.Cleanup(ctx, 100, -1, 10); err == nil {
		t.Fatal("负 acknowledgedBeforeMS 必须失败")
	}
	if err := s.ackSeed(ctx, db); err != nil {
		t.Fatal(err)
	}
	// 已 dispatched 且 ack 时间早于阈值 → 删除。
	if _, err := db.Exec(`UPDATE account_circuit_outbox SET status='dispatched', acknowledged_at_ms=150 WHERE event_id='e1'`); err != nil {
		t.Fatal(err)
	}
	result, err := s.Cleanup(ctx, 1_000, 500, 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedOutbox != 1 || result.DeletedIncidents != 0 {
		t.Fatalf("cleanup result = %+v", result)
	}
	// 关闭且过保留期且无未派发 outbox 的事故 → 删除。
	closed := wkBaseIncident("w9e-clean", "a1", 1)
	closed.Incident.State = "CLOSED"
	closed.Incident.RetainedUntilMS = int64Ptr2(5_000)
	closed.Incident.CreatedAtMS = 100
	closed.UpdatedAtMS = 2_500
	if _, err := s.CompareAndSetIncident(ctx, closed); err != nil {
		t.Fatalf("seed closed incident: %v", err)
	}
	// projected_ledger_revision 落后 → 不删。
	result2, err := s.Cleanup(ctx, 5_000, 500, 10)
	if err != nil {
		t.Fatal(err)
	}
	if result2.DeletedIncidents != 0 {
		t.Fatalf("投影落后的关闭事故不应删除: %+v", result2)
	}
	// 推进投影后可删。
	event := Outbox{AccountID: "a1", CircuitScopeKey: strPtr2("w9e-clean"), IncidentID: strPtr2("inc-w9e-clean"), DispatchRevision: 1, LedgerRevision: int64Ptr2(1)}
	load, err := s.LoadIncidentForProjection(ctx, event)
	if err != nil || load.Status != "current" {
		t.Fatalf("load = %+v err=%v", load, err)
	}
	if err := s.projectAckForTest(ctx, event); err != nil {
		t.Fatal(err)
	}
	// CAS 落下的 pending outbox 会阻止物理删除，置为 dispatched 模拟已派发。
	if _, err := db.Exec(`UPDATE account_circuit_outbox SET status='dispatched' WHERE circuit_scope_key='w9e-clean'`); err != nil {
		t.Fatal(err)
	}
	result3, err := s.Cleanup(ctx, 5_000, 500, 10)
	if err != nil || result3.DeletedIncidents != 1 {
		t.Fatalf("cleanup3 = %+v err=%v", result3, err)
	}
}

func (s *Store) projectAckForTest(ctx context.Context, event Outbox) error {
	_, err := s.db.ExecContext(ctx, `UPDATE account_circuit_incidents SET projected_ledger_revision=ledger_revision WHERE circuit_scope_key=?`, *event.CircuitScopeKey)
	return err
}

func TestW9EScanIncidentCorruptEvidenceBranches(t *testing.T) {
	valid := func(children, evidence string) *fakeScanner {
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
		return &fakeScanner{values: values}
	}
	// children 是对象而非数组。
	if _, err := scanIncident(valid(`{"a":1}`, "[]")); err == nil {
		t.Fatal("对象 children 必须失败")
	}
	// children 超 64 项。
	many := make([]string, 65)
	for i := range many {
		many[i] = "c"
	}
	childrenJSON := `["` + strings.Join(many, `","`) + `"]`
	if _, err := scanIncident(valid(childrenJSON, "[]")); err == nil {
		t.Fatal("超 64 children 必须失败")
	}
	// children 元素为空白。
	if _, err := scanIncident(valid(`[" "]`, "[]")); err == nil {
		t.Fatal("空白 child id 必须失败")
	}
	// evidence 空字符串。
	if _, err := scanIncident(valid("[]", "")); err == nil {
		t.Fatal("空 evidence 必须失败")
	}
	// evidence 是数字而非数组。
	if _, err := scanIncident(valid("[]", "5")); err == nil {
		t.Fatal("标量 evidence 必须失败")
	}
	// evidence 元素非 SHA256。
	if _, err := scanIncident(valid("[]", `["abc"]`)); err == nil {
		t.Fatal("非 SHA256 evidence 必须失败")
	}
	// evidence 数量超出 required+1（required=1 → 上界 2）。
	three := `["` + strings.Repeat("a", 64) + `","` + strings.Repeat("b", 64) + `","` + strings.Repeat("c", 64) + `"]`
	if _, err := scanIncident(valid("[]", three)); err == nil {
		t.Fatal("超量 evidence 必须失败")
	}
}

func TestW9EValidateIncidentBranches(t *testing.T) {
	base := func() IncidentMutation { return wkBaseIncident("w9e-vi", "a1", 1) }
	ctx := context.Background()
	s, db := wkReadyStore(t)
	defer db.Close()

	// 非法 state。
	bad := base()
	bad.Incident.State = "BOGUS"
	if _, err := s.CompareAndSetIncident(ctx, bad); err == nil || !strings.Contains(err.Error(), "invalid incident state") {
		t.Fatalf("非法 state = %v", err)
	}
	// 非法 scope kind。
	bad = base()
	bad.Incident.ScopeKind = "bogus"
	if _, err := s.CompareAndSetIncident(ctx, bad); err == nil || !strings.Contains(err.Error(), "invalid scope kind") {
		t.Fatalf("非法 scope kind = %v", err)
	}
	// 非法 failure scope。
	bad = base()
	bad.Incident.FailureScope = "bogus"
	if _, err := s.CompareAndSetIncident(ctx, bad); err == nil || !strings.Contains(err.Error(), "invalid failure scope") {
		t.Fatalf("非法 failure scope = %v", err)
	}
	// 账户 scope 携带 key 字段。
	bad = base()
	bad.Incident.KeyFingerprint = strPtr2("fp")
	if _, err := s.CompareAndSetIncident(ctx, bad); err == nil || !strings.Contains(err.Error(), "account scope") {
		t.Fatalf("账户 scope 带 key 字段 = %v", err)
	}
	// key scope 缺 fingerprint。
	bad = base()
	bad.Incident.ScopeKind = "key"
	if _, err := s.CompareAndSetIncident(ctx, bad); err == nil || !strings.Contains(err.Error(), "key scope") {
		t.Fatalf("key scope 缺 fingerprint = %v", err)
	}
	// protocol_model scope 缺字段。
	bad = base()
	bad.Incident.ScopeKind = "protocol_model"
	if _, err := s.CompareAndSetIncident(ctx, bad); err == nil || !strings.Contains(err.Error(), "protocol_model scope") {
		t.Fatalf("protocol_model 缺字段 = %v", err)
	}
	// key_model scope 不完整。
	bad = base()
	bad.Incident.ScopeKind = "key_model"
	bad.Incident.KeyFingerprint = strPtr2("fp")
	if _, err := s.CompareAndSetIncident(ctx, bad); err == nil || !strings.Contains(err.Error(), "key_model scope") {
		t.Fatalf("key_model 不完整 = %v", err)
	}
	// 非法 lease purpose。
	bad = base()
	bad.Incident.LeaseID = strPtr2("lease")
	bad.Incident.LeasePurpose = strPtr2("bogus")
	bad.Incident.LeaseOwnerRunID = strPtr2("run")
	bad.Incident.LeaseUntilMS = int64Ptr2(500)
	if _, err := s.CompareAndSetIncident(ctx, bad); err == nil || !strings.Contains(err.Error(), "lease purpose") {
		t.Fatalf("非法 lease purpose = %v", err)
	}
	// lease 字段不齐。
	bad = base()
	bad.Incident.LeaseID = strPtr2("lease")
	if _, err := s.CompareAndSetIncident(ctx, bad); err == nil || !strings.Contains(err.Error(), "together") {
		t.Fatalf("lease 字段不齐 = %v", err)
	}
	// attempt 时间戳缺 lease。
	bad = base()
	bad.Incident.AttemptStartedAtMS = int64Ptr2(100)
	if _, err := s.CompareAndSetIncident(ctx, bad); err == nil || !strings.Contains(err.Error(), "active lease") {
		t.Fatalf("attempt 缺 lease = %v", err)
	}
	// 有 lease 但缺 attempt 时间戳。
	bad = base()
	bad.Incident.LeaseID = strPtr2("lease")
	bad.Incident.LeasePurpose = strPtr2("confirmation")
	bad.Incident.LeaseOwnerRunID = strPtr2("run")
	bad.Incident.LeaseUntilMS = int64Ptr2(500)
	if _, err := s.CompareAndSetIncident(ctx, bad); err == nil || !strings.Contains(err.Error(), "attempt start and hard deadline") {
		t.Fatalf("lease 缺 attempt = %v", err)
	}
	// 非法 failure class。
	bad = base()
	bad.Incident.LastFailureClass = strPtr2("bogus")
	if _, err := s.CompareAndSetIncident(ctx, bad); err == nil || !strings.Contains(err.Error(), "failure class") {
		t.Fatalf("非法 failure class = %v", err)
	}
	// 重复 children。
	bad = base()
	bad.Incident.ChildIncidentIDs = []string{"c1", "c1"}
	if _, err := s.CompareAndSetIncident(ctx, bad); err == nil || !strings.Contains(err.Error(), "unique") {
		t.Fatalf("重复 children = %v", err)
	}
	// evidence 非 SHA256。
	bad = base()
	bad.Incident.ConfirmationFailureEvidenceKeys = []string{"nothex"}
	if _, err := s.CompareAndSetIncident(ctx, bad); err == nil || !strings.Contains(err.Error(), "SHA256") {
		t.Fatalf("evidence 非 SHA256 = %v", err)
	}
	// consecutive failures 超过 required。
	bad = base()
	bad.Incident.ConsecutiveFailures = 5
	if _, err := s.CompareAndSetIncident(ctx, bad); err == nil || !strings.Contains(err.Error(), "consecutive failures") {
		t.Fatalf("consecutive 超限 = %v", err)
	}
	// 非法 generation。
	bad = base()
	bad.Incident.Generation = -1
	if _, err := s.CompareAndSetIncident(ctx, bad); err == nil || !strings.Contains(err.Error(), "numeric") {
		t.Fatalf("负 generation = %v", err)
	}
	// CLOSED 缺 retained。
	bad = base()
	bad.Incident.State = "CLOSED"
	if _, err := s.CompareAndSetIncident(ctx, bad); err == nil || !strings.Contains(err.Error(), "retained_until_ms") {
		t.Fatalf("CLOSED 缺 retained = %v", err)
	}
	// 非 CLOSED 带 retained。
	bad = base()
	bad.Incident.RetainedUntilMS = int64Ptr2(10)
	if _, err := s.CompareAndSetIncident(ctx, bad); err == nil || !strings.Contains(err.Error(), "non-closed") {
		t.Fatalf("非 CLOSED 带 retained = %v", err)
	}
	// created 晚于 updated。
	bad = base()
	bad.Incident.CreatedAtMS = 500
	bad.UpdatedAtMS = 100
	if _, err := s.CompareAndSetIncident(ctx, bad); err == nil || !strings.Contains(err.Error(), "created_at_ms") {
		t.Fatalf("created 晚于 updated = %v", err)
	}
	// retained 早于 now。
	bad = base()
	bad.Incident.State = "CLOSED"
	bad.Incident.RetainedUntilMS = int64Ptr2(50)
	bad.UpdatedAtMS = 100
	if _, err := s.CompareAndSetIncident(ctx, bad); err == nil || !strings.Contains(err.Error(), "retained_until_ms cannot precede") {
		t.Fatalf("retained 早于 now = %v", err)
	}
	// lease 时间顺序非法。
	bad = base()
	bad.Incident.LeaseID = strPtr2("lease")
	bad.Incident.LeasePurpose = strPtr2("confirmation")
	bad.Incident.LeaseOwnerRunID = strPtr2("run")
	bad.Incident.LeaseUntilMS = int64Ptr2(500)
	bad.Incident.AttemptStartedAtMS = int64Ptr2(600)
	bad.Incident.AttemptHardDeadlineMS = int64Ptr2(700)
	if _, err := s.CompareAndSetIncident(ctx, bad); err == nil || !strings.Contains(err.Error(), "attempt start <= hard deadline") {
		t.Fatalf("lease 时间顺序 = %v", err)
	}
	// 负的开放时间。
	bad = base()
	bad.Incident.OpenUntilMS = int64Ptr2(-3)
	if _, err := s.CompareAndSetIncident(ctx, bad); err == nil || !strings.Contains(err.Error(), "openUntilMs") {
		t.Fatalf("负 openUntilMs = %v", err)
	}
}

func TestW9EValidateKeyModelScopeHappyPath(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()
	mut := wkBaseIncident("w9e-km|fp|p", "a1", 1)
	mut.Incident.ScopeKind = "key_model"
	mut.Incident.KeyFingerprint = strPtr2("fp")
	mut.Incident.ClientModel = strPtr2("gpt-5.5")
	mut.Incident.CapabilityHash = strPtr2(strings.Repeat("a", 64))
	mut.Incident.CredentialSourceAccountID = strPtr2("src")
	mut.Incident.ClientEndpointFamily = strPtr2("openai")
	mut.Incident.FinalUpstreamModel = strPtr2("gpt-5.5")
	mut.Incident.UpstreamEndpointMode = strPtr2("responses")
	res, err := s.CompareAndSetIncident(ctx, mut)
	if err != nil || res.Status != "applied" {
		t.Fatalf("key_model happy path = %+v err=%v", res, err)
	}
}

func TestW9ERandomTokenShape(t *testing.T) {
	tok, err := randomToken()
	if err != nil || len(tok) != 32 {
		t.Fatalf("randomToken = %q err=%v", tok, err)
	}
}
