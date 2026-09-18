// 波次 w14j：控制面剩余错误臂与方言无关分支收口。复用 w13g5 注入
// fixture（SQLite + 可回放失败注入），不触真实 PostgreSQL；PG 专属语句
// 差异由 w14j_circuit_pg_script_test.go 的脚本化方言 fixture 覆盖。
package circuitstore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// TestW14JIncidentMapParseErrorArms 覆盖 mapIncidentRow 的两个解析错误臂：
// child_incident_ids_json 非法 JSON（L134-136）与 evidence keys 非 SHA256
// （L138-140）。经 GetByScopeKey 触发扫描路径。
func TestW14JIncidentMapParseErrorArms(t *testing.T) {
	_, control, db, spec := w13g5CircuitFixture(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO account_circuit_incidents (
		circuit_scope_key, account_id, account_runtime_key, scope_kind,
		incident_id, child_incident_ids_json, state, generation, dispatch_revision,
		ledger_revision, transition_id, confirmation_failures_required,
		confirmation_failure_evidence_keys_json, created_at_ms, updated_at_ms
	) VALUES ('w14j-bad-child', 'a', 'rk', 'account', 'i1', 'not-json', 'OPEN', 1, 1, 1, 'tr', 1, '[]', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := control.GetByScopeKey(ctx, "w14j-bad-child"); err == nil {
		t.Fatal("child ids 非法 JSON 必须报错")
	}
	// evidence 含非 SHA256 项 → parseEvidenceKeys 错误臂。
	if _, err := db.Exec(`INSERT INTO account_circuit_incidents (
		circuit_scope_key, account_id, account_runtime_key, scope_kind,
		incident_id, child_incident_ids_json, state, generation, dispatch_revision,
		ledger_revision, transition_id, confirmation_failures_required,
		confirmation_failure_evidence_keys_json, created_at_ms, updated_at_ms
	) VALUES ('w14j-bad-evidence', 'a', 'rk', 'account', 'i2', '[]', 'OPEN', 1, 1, 1, 'tr', 1, '["zz"]', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := control.GetByScopeKey(ctx, "w14j-bad-evidence"); err == nil {
		t.Fatal("evidence 非 SHA256 必须报错")
	}
	// 超长 child id（>256）走 parseBoundedIDArray 长度臂。
	longChild := `["` + strings.Repeat("x", 257) + `"]`
	if _, err := db.Exec(`INSERT INTO account_circuit_incidents (
		circuit_scope_key, account_id, account_runtime_key, scope_kind,
		incident_id, child_incident_ids_json, state, generation, dispatch_revision,
		ledger_revision, transition_id, confirmation_failures_required,
		confirmation_failure_evidence_keys_json, created_at_ms, updated_at_ms
	) VALUES ('w14j-long-child', 'a', 'rk', 'account', 'i3', ?, 'OPEN', 1, 1, 1, 'tr', 1, '[]', 1, 1)`, longChild); err != nil {
		t.Fatal(err)
	}
	if _, err := control.GetByScopeKey(ctx, "w14j-long-child"); err == nil {
		t.Fatal("child id 超长必须报错")
	}
	// GetByScopeKey 注入查询错误（非 ErrNoRows 臂）。
	spec.arm("FROM account_circuit_incidents circuit_incident")
	if _, err := control.GetByScopeKey(ctx, "w14j-any"); err == nil {
		t.Fatal("注入查询错误必须传播")
	}
	spec.disarm()
}

// TestW14JListForRebuildErrorArms 覆盖 rebuild 分页的查询错误与行扫描错误臂。
func TestW14JListForRebuildErrorArms(t *testing.T) {
	_, control, db, spec := w13g5CircuitFixture(t)
	ctx := context.Background()
	query := opsjobs.RebuildPageQuery{NowMS: 100, Limit: 10}
	spec.arm("FROM account_circuit_incidents circuit_incident")
	if _, err := control.ListForRebuild(ctx, query); err == nil {
		t.Fatal("注入查询错误必须传播")
	}
	spec.disarm()
	// 坏 child JSON 行触发 scanIncident 错误臂（行须通过 accounts 的
	// dispatch revision 关联子查询过滤）。
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, dispatch_revision) VALUES ('a', 'sa', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_circuit_incidents (
		circuit_scope_key, account_id, account_runtime_key, scope_kind,
		incident_id, child_incident_ids_json, state, generation, dispatch_revision,
		ledger_revision, transition_id, created_at_ms, updated_at_ms
	) VALUES ('w14j-rb-bad', 'a', 'rk', 'account', 'i', 'oops', 'OPEN', 1, 1, 1, 'tr', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := control.ListForRebuild(ctx, query); err == nil {
		t.Fatal("行扫描错误必须传播")
	}
	// rows.Err 臂与 ErrNoRows 不同：注入分页 SQL 的 Query 错误已被上例覆盖，
	// 这里补 ListByRuntimeKeys 的查询错误传播。
	spec.arm("ORDER BY account_runtime_key ASC, updated_at_ms ASC")
	if _, err := control.ListByRuntimeKeys(ctx, []string{"rk"}, false, 100); err == nil {
		t.Fatal("ListByRuntimeKeys 注入错误必须传播")
	}
	spec.disarm()
}

// TestW14JListByRuntimeKeysValidationArms 覆盖运行态键校验分支。
func TestW14JListByRuntimeKeysValidationArms(t *testing.T) {
	_, control, _, _ := w13g5CircuitFixture(t)
	ctx := context.Background()
	if _, err := control.ListByRuntimeKeys(ctx, []string{"  "}, false, 1); err == nil {
		t.Fatal("空白键必须报错")
	}
	if _, err := control.ListByRuntimeKeys(ctx, []string{strings.Repeat("k", 1025)}, false, 1); err == nil {
		t.Fatal("超长键必须报错")
	}
	if records, err := control.ListByRuntimeKeys(ctx, []string{}, false, 1); err != nil || records == nil || len(records) != 0 {
		t.Fatalf("空键集必须返回空切片: %v %v", records, err)
	}
	many := make([]string, 101)
	for i := range many {
		many[i] = fmt.Sprintf("rk-%d", i)
	}
	if _, err := control.ListByRuntimeKeys(ctx, many, false, 1); err == nil {
		t.Fatal(">100 键必须报错")
	}
	// 重复键去重后仍可查询（去重分支）。
	if _, err := control.ListByRuntimeKeys(ctx, []string{"rk", "rk", " rk "}, false, 1); err != nil {
		t.Fatalf("重复键去重后必须可查询: %v", err)
	}
}


// TestW14JClaimSQLiteValidationArms 覆盖 Claim 参数校验臂。
func TestW14JClaimSQLiteValidationArms(t *testing.T) {
	_, control, _, _ := w13g5CircuitFixture(t)
	ctx := context.Background()
	if _, err := control.Claim(ctx, "", 1, 1000, 1); err == nil {
		t.Fatal("空 owner 必须报错")
	}
	if _, err := control.Claim(ctx, strings.Repeat("o", 129), 1, 1000, 1); err == nil {
		t.Fatal("超长 owner 必须报错")
	}
	if _, err := control.Claim(ctx, "w14j-owner", -1, 1000, 1); err == nil {
		t.Fatal("负 nowMs 必须报错")
	}
	if _, err := control.Claim(ctx, "w14j-owner", 1, 0, 1); err == nil {
		t.Fatal("零 lease 必须报错")
	}
	if _, err := control.Claim(ctx, "w14j-owner", 1, 60*60_000+1, 1); err == nil {
		t.Fatal("超限 lease 必须报错")
	}
	if _, err := control.Claim(ctx, "w14j-owner", 1, 1000, 0); err == nil {
		t.Fatal("零 limit 必须报错")
	}
	if _, err := control.Claim(ctx, "w14j-owner", 1, 1000, 501); err == nil {
		t.Fatal("超限 limit 必须报错")
	}
}

// TestW14JAckSQLiteArms 覆盖 Ack 的方言无关分支：ErrNoRows、projection
// 键不匹配、dispatched 幂等、claim token 不匹配、两类 revision 回写与
// UPDATE 注入错误。
func TestW14JAckSQLiteArms(t *testing.T) {
	list, control, db, spec := w13g5CircuitFixture(t)
	ctx := context.Background()
	base := opsjobs.OutboxEvent{
		EventID:           "w14j-evt-ack",
		EventType:         "dispatch_revision_changed",
		AccountRuntimeKey: "rk",
		TransitionID:      "tr",
		DispatchRevision:  7,
		ProjectionKey:     "w14j-pk",
		ClaimToken:        "token-w14j",
	}
	if _, err := db.Exec(`INSERT INTO account_circuit_outbox (
		event_id, projection_key, dedupe_key, event_type, account_id, account_runtime_key,
		transition_id, dispatch_revision, status, claim_token, available_at_ms, created_at_ms, updated_at_ms
	) VALUES ('w14j-evt-ack', 'w14j-pk', 'd', 'dispatch_revision_changed', 'w14j-acc', 'rk', 'tr', 7, 'processing', 'token-w14j', 0, 0, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, dispatch_revision) VALUES ('w14j-acc', 'w14j-sa', 9)`); err != nil {
		t.Fatal(err)
	}
	// ErrNoRows。
	missing := base
	missing.EventID = "w14j-evt-missing"
	if committed, err := control.Ack(ctx, missing, 100); err != nil || committed {
		t.Fatalf("缺失事件必须 (false,nil): %v %v", committed, err)
	}
	// projection key 不匹配。
	wrongProjection := base
	wrongProjection.ProjectionKey = "other-pk"
	if committed, err := control.Ack(ctx, wrongProjection, 100); err != nil || committed {
		t.Fatalf("projection 键不匹配必须 (false,nil): %v %v", committed, err)
	}
	// claim token 不匹配。
	wrongToken := base
	wrongToken.ClaimToken = "other-token"
	if committed, err := control.Ack(ctx, wrongToken, 100); err != nil || committed {
		t.Fatalf("token 不匹配必须 (false,nil): %v %v", committed, err)
	}
	// dispatched 幂等。
	if _, err := db.Exec(`UPDATE account_circuit_outbox SET status='dispatched' WHERE event_id='w14j-evt-ack'`); err != nil {
		t.Fatal(err)
	}
	if committed, err := control.Ack(ctx, base, 100); err != nil || !committed {
		t.Fatalf("dispatched 幂等必须 (true,nil): %v %v", committed, err)
	}
	// processing + token 匹配 → dispatch_revision_changed 回写 accounts。
	if _, err := db.Exec(`UPDATE account_circuit_outbox SET status='processing', claim_token='token-w14j' WHERE event_id='w14j-evt-ack'`); err != nil {
		t.Fatal(err)
	}
	if committed, err := control.Ack(ctx, base, 200); err != nil || !committed {
		t.Fatalf("Ack 主路径必须成功: %v %v", committed, err)
	}
	var projected int64
	if err := db.QueryRow(`SELECT circuit_projection_revision FROM accounts WHERE id='w14j-acc'`).Scan(&projected); err != nil {
		t.Fatal(err)
	}
	if projected != 7 {
		t.Fatalf("dispatch revision 回写期待 7: %d", projected)
	}
	// incident_changed 回写分支。
	if _, err := db.Exec(`INSERT INTO account_circuit_incidents (
		circuit_scope_key, account_id, account_runtime_key, scope_kind,
		incident_id, child_incident_ids_json, state, generation, dispatch_revision,
		ledger_revision, transition_id, created_at_ms, updated_at_ms
	) VALUES ('w14j-scope', 'a', 'rk', 'account', 'w14j-inc', '[]', 'OPEN', 1, 1, 5, 'tr', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_circuit_outbox (
		event_id, projection_key, dedupe_key, event_type, account_id, account_runtime_key,
		circuit_scope_key, incident_id, transition_id, dispatch_revision, ledger_revision,
		status, claim_token, available_at_ms, created_at_ms, updated_at_ms
	) VALUES ('w14j-evt-inc', 'w14j-pk', 'd2', 'incident_changed', 'w14j-acc', 'rk', 'w14j-scope', 'w14j-inc', 'tr', 1, 5, 'processing', 'token-w14j', 0, 0, 0)`); err != nil {
		t.Fatal(err)
	}
	incidentEvent := base
	incidentEvent.EventID = "w14j-evt-inc"
	incidentEvent.EventType = "incident_changed"
	if committed, err := control.Ack(ctx, incidentEvent, 300); err != nil || !committed {
		t.Fatalf("incident_changed Ack 必须成功: %v %v", committed, err)
	}
	var ledgerProjected int64
	if err := db.QueryRow(`SELECT projected_ledger_revision FROM account_circuit_incidents WHERE circuit_scope_key='w14j-scope'`).Scan(&ledgerProjected); err != nil {
		t.Fatal(err)
	}
	if ledgerProjected != 5 {
		t.Fatalf("incident 回写期待 5: %d", ledgerProjected)
	}
	// Ack 主 UPDATE 注入错误。
	if _, err := db.Exec(`UPDATE account_circuit_outbox SET status='processing', claim_token='token-w14j' WHERE event_id='w14j-evt-inc'`); err != nil {
		t.Fatal(err)
	}
	spec.arm("SET status = 'dispatched'")
	if _, err := control.Ack(ctx, incidentEvent, 400); err == nil {
		t.Fatal("Ack UPDATE 注入必须传播")
	}
	spec.disarm()
	_ = list
}

// TestW14JReleaseForReplayArms 覆盖释放重放的校验臂、未命中错误与注入错误。
func TestW14JReleaseForReplayArms(t *testing.T) {
	_, control, db, spec := w13g5CircuitFixture(t)
	ctx := context.Background()
	event := opsjobs.OutboxEvent{EventID: "w14j-evt-rel", ProjectionKey: "w14j-pk", ClaimToken: "token"}
	if err := control.ReleaseForReplay(ctx, event, "", 1, 1); err == nil {
		t.Fatal("空 errorClass 必须报错")
	}
	if err := control.ReleaseForReplay(ctx, event, strings.Repeat("e", 65), 1, 1); err == nil {
		t.Fatal("超长 errorClass 必须报错")
	}
	if err := control.ReleaseForReplay(ctx, event, "boom", -1, 1); err == nil {
		t.Fatal("负 nowMs 必须报错")
	}
	if err := control.ReleaseForReplay(ctx, event, "boom", 1, 24*60*60_000+1); err == nil {
		t.Fatal("超限 retryDelay 必须报错")
	}
	badEvent := event
	badEvent.EventID = ""
	if err := control.ReleaseForReplay(ctx, badEvent, "boom", 1, 1); err == nil {
		t.Fatal("空 eventId 必须报错")
	}
	noToken := event
	noToken.ClaimToken = ""
	if err := control.ReleaseForReplay(ctx, noToken, "boom", 1, 1); err == nil {
		t.Fatal("空 claimToken 必须报错")
	}
	// 无匹配行 → changed != 1。
	if err := control.ReleaseForReplay(ctx, event, "boom", 1, 1); err == nil {
		t.Fatal("未命中 claim 必须报错")
	}
	// 命中路径。
	if _, err := db.Exec(`INSERT INTO account_circuit_outbox (
		event_id, projection_key, dedupe_key, event_type, account_id, account_runtime_key,
		transition_id, dispatch_revision, status, claim_token, available_at_ms, created_at_ms, updated_at_ms
	) VALUES ('w14j-evt-rel', 'w14j-pk', 'd', 'incident_changed', 'a', 'rk', 'tr', 1, 'processing', 'token', 0, 0, 0)`); err != nil {
		t.Fatal(err)
	}
	spec.arm("SET status = 'pending'")
	if err := control.ReleaseForReplay(ctx, event, "boom", 1, 1); err == nil {
		t.Fatal("Release 注入必须传播")
	}
	spec.disarm()
	if err := control.ReleaseForReplay(ctx, event, "boom", 1000, 500); err != nil {
		t.Fatalf("Release 命中必须成功: %v", err)
	}
}

// TestW14JReconcileCursorArms 覆盖游标装载/保存错误臂（正常往返已由
// TestReconcileCursorRoundTrip 覆盖）。
func TestW14JReconcileCursorArms(t *testing.T) {
	_, control, _, spec := w13g5CircuitFixture(t)
	ctx := context.Background()
	if err := control.EnsureCursorSchema(ctx); err != nil {
		t.Fatal(err)
	}
	cursorStore, err := NewReconcileCursorStore(ControlPlaneConfig{DB: control.db, Postgres: false, Now: func() time.Time { return time.UnixMilli(5000) }})
	if err != nil {
		t.Fatal(err)
	}
	// 空表 Load → nil。
	loaded, err := cursorStore.Load(ctx)
	if err != nil || loaded != nil {
		t.Fatalf("空游标必须 (nil,nil): %v %v", loaded, err)
	}
	// Load 注入非 ErrNoRows 错误。
	spec.arm("WHERE cursor_name = ?")
	if _, err := cursorStore.Load(ctx); err == nil {
		t.Fatal("Load 注入必须传播")
	}
	spec.disarm()
	// Save 注入。
	spec.arm("INSERT INTO account_circuit_reconcile_cursors")
	if err := cursorStore.Save(ctx, opsjobs.IncidentCursor{UpdatedAtMS: 1, CircuitScopeKey: "s"}); err == nil {
		t.Fatal("Save 注入必须传播")
	}
	spec.disarm()
	if err := cursorStore.Save(ctx, opsjobs.IncidentCursor{UpdatedAtMS: 9, CircuitScopeKey: "w14j-scope"}); err != nil {
		t.Fatalf("Save 必须成功: %v", err)
	}
	loaded, err = cursorStore.Load(ctx)
	if err != nil || loaded == nil || loaded.UpdatedAtMS != 9 || loaded.CircuitScopeKey != "w14j-scope" {
		t.Fatalf("游标往返必须一致: %+v %v", loaded, err)
	}
}

// TestW14JClaimQueryErrorArm 注入候选查询错误，覆盖 Claim 错误传播臂。
func TestW14JClaimQueryErrorArm(t *testing.T) {
	_, control, _, spec := w13g5CircuitFixture(t)
	spec.arm("ORDER BY available_at_ms ASC, created_at_ms ASC, event_id ASC")
	if _, err := control.Claim(context.Background(), "w14j-owner", 1, 1000, 1); err == nil {
		t.Fatal("Claim 候选查询注入必须传播")
	}
	spec.disarm()
	if _, err := control.Claim(context.Background(), "w14j-owner", 1, 1000, 1); err != nil {
		t.Fatalf("解除注入后必须成功: %v", err)
	}
}

// TestW14JMapOutboxEventScopeToken 分支补齐：scope/claim token 的空值映射
// 由 Claim 主路径之外的 mapOutboxEvent 直接验证。
func TestW14JMapOutboxEventScopeToken(t *testing.T) {
	event := mapOutboxEvent(outboxClaimRow{
		eventID: "e", projectionKey: "p", eventType: "incident_changed",
		accountID: "a", accountRuntimeKey: "rk", transitionID: "tr", dispatchRevision: 3,
	})
	if event.CircuitScopeKey != "" || event.ClaimToken != "" {
		t.Fatalf("空 scope/token 不得映射: %+v", event)
	}
}

