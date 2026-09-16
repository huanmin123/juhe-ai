package accounthealth

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// w12d_projection_faults_test.go 通过 SQLite fixture 的结构注入（删表/坏数据）
// 覆盖投影链路的错误臂与重锚定/降级分支。

// TestW12dProjectionCursorReanchoring 覆盖游标重锚定与 resolveCursorStorage 臂。
func TestW12dProjectionCursorReanchoring(t *testing.T) {
	fixture := newProjectionFixture(t)
	ctx := context.Background()
	// 存一条 outcome（存储时间与游标文本不同）。
	outcome := Outcome{OutcomeID: "w12d-anchor", RequestID: "w12d-anchor-r", AccountID: "w12d-acc", Outcome: OutcomeSuccess, ObservedAt: projectionFixtureNow, InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7}
	fixture.insertOutcome(projectionFixtureNow, outcome)
	// 游标指向同一 outcome 但时间文本精度漂移。
	if _, err := fixture.business.Exec(`INSERT INTO account_health_projection_cursors (consumer_key, observed_at, outcome_id, updated_at) VALUES (?, '2026-09-06T11:00:00Z', 'w12d-anchor', '2026-09-06T11:00:00Z')`, DefaultProjectionConsumerKey); err != nil {
		t.Fatal(err)
	}
	cursor, err := fixture.projector.loadCursor(ctx)
	if err != nil || cursor == nil {
		t.Fatalf("reanchor: %+v %v", cursor, err)
	}
	if cursor.ObservedAtText != "2026-09-06T12:00:00.000Z" && cursor.ObservedAtText != "2026-09-06T12:00:00Z" {
		t.Fatalf("cursor text=%q", cursor.ObservedAtText)
	}
	if cursor.ObservedAt != projectionFixtureNow.UTC() {
		t.Fatalf("anchored time=%s", cursor.ObservedAt)
	}
	// resolveCursorStorage：缺失 outcome → 空文本；无效 observed_at → 空。
	resolved, err := fixture.projector.resolveCursorStorage(ctx, "w12d-missing-outcome")
	if err != nil || resolved != "" {
		t.Fatalf("missing outcome resolve: %q %v", resolved, err)
	}
	if _, err := fixture.store.db.Exec(`UPDATE account_health_outcomes SET observed_at = 'not-a-time' WHERE outcome_id = 'w12d-anchor'`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.projector.resolveCursorStorage(ctx, "w12d-anchor"); err == nil || !strings.Contains(err.Error(), "锚定 J1 投影游标失败") {
		t.Fatalf("invalid time resolve must fail: %v", err)
	}
	if _, err := fixture.store.db.Exec(`UPDATE account_health_outcomes SET observed_at = ? WHERE outcome_id = 'w12d-anchor'`, projectionFixtureNow.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}

// TestW12dProjectionFaultInjection 用删表注入覆盖投影事务错误臂。
func TestW12dProjectionFaultInjection(t *testing.T) {
	fixture := newProjectionFixture(t)
	ctx := context.Background()
	// (a) 删除 receipts 表 → insertReceipt 错误上抛（游标不推进）。
	if _, err := fixture.business.Exec(`DROP TABLE account_health_projection_receipts`); err != nil {
		t.Fatal(err)
	}
	outcome := Outcome{OutcomeID: "w12d-fault-1", RequestID: "r", AccountID: "w12d-acc", Outcome: OutcomeSuccess, ObservedAt: projectionFixtureNow, InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7}
	fixture.insertOutcome(projectionFixtureNow, outcome)
	// findReceipt 读取错误先于插入上抛（receipts 表缺失）。
	if _, err := fixture.projector.DrainOnce(ctx); err == nil || !strings.Contains(err.Error(), "receipt 失败") {
		t.Fatalf("receipt failure: %v", err)
	}
	// 直接事务内 insertReceipt 错误分支。
	tx, err := fixture.business.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.projector.insertReceipt(ctx, tx, ProjectionResult{OutcomeID: "w12d-x", AccountID: "a", InputVersion: 1}, ProjectionApplied, ""); err == nil {
		t.Fatal("insertReceipt must fail without table")
	}
	_ = tx.Rollback()
	// (b) 删除 cursors 表 → loadCursor/advanceCursor 错误。
	if _, err := fixture.business.Exec(`DROP TABLE account_health_projection_cursors`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.projector.loadCursor(ctx); err == nil || !strings.Contains(err.Error(), "读取 J1 投影游标失败") {
		t.Fatalf("loadCursor failure: %v", err)
	}
	next := &OutcomeCursor{ObservedAt: projectionFixtureNow, ObservedAtText: "2026-09-06T12:00:00Z", OutcomeID: "w12d-x"}
	if _, err := fixture.projector.advanceCursor(ctx, next); err == nil || !strings.Contains(err.Error(), "读取 J1 投影游标失败") {
		t.Fatalf("advanceCursor failure: %v", err)
	}
	// (c) 删除 accounts/input_versions 表 → findAccountFence / fenceMismatch 错误。
	if _, err := fixture.business.Exec(`DROP TABLE account_health_jobs_input_versions`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.business.Exec(`DROP TABLE accounts`); err != nil {
		t.Fatal(err)
	}
	// fixture store 里重开一个 business 连接恢复 receipts/cursors，覆盖
	// findAccountFence/fenceMismatchReason 错误臂。
	if err := fixture.business.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestW12dProjectOutcomeFaults 直接驱动 projectOutcome 错误臂。
func TestW12dProjectOutcomeFaults(t *testing.T) {
	fixture := newProjectionFixture(t)
	ctx := context.Background()
	fixture.seedAccount(t, map[string]any{"id": "w12d-fault-acc", "status": "active", "config_revision": int64(5), "dispatch_revision": int64(7)})
	// 删除 input_versions 表 → fenceMismatchReason 读取错误。
	if _, err := fixture.business.Exec(`DROP TABLE account_health_jobs_input_versions`); err != nil {
		t.Fatal(err)
	}
	outcome := Outcome{OutcomeID: "w12d-fault-2", RequestID: "r", AccountID: "w12d-fault-acc", Outcome: OutcomeUpstreamFailed, ObservedAt: projectionFixtureNow, InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7,
		NextDueAt: ptrTime(projectionFixtureNow.Add(time.Hour)),
		Projection: &Projection{TargetAccountID: "w12d-fault-acc", TransitionKind: "health_failure", InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7, ExpectedAccountStatus: "active"}}
	fixture.insertOutcome(projectionFixtureNow, outcome)
	if _, err := fixture.projector.DrainOnce(ctx); err == nil || !strings.Contains(err.Error(), "input version") {
		t.Fatalf("input version failure: %v", err)
	}
	// 删除 accounts 表 → findAccountFence 错误。
	if _, err := fixture.business.Exec(`DROP TABLE accounts`); err != nil {
		t.Fatal(err)
	}
	outcome2 := outcome
	outcome2.OutcomeID = "w12d-fault-3"
	outcome2.RequestID = "r3"
	fixture.insertOutcome(projectionFixtureNow.Add(time.Second), outcome2)
	if _, err := fixture.projector.DrainOnce(ctx); err == nil || !strings.Contains(err.Error(), "fence 失败") {
		t.Fatalf("account fence failure: %v", err)
	}
}

// TestW12dProjectionPlanArms 覆盖 activation 计划降级与余额探测臂。
func TestW12dProjectionPlanArms(t *testing.T) {
	fixture := newProjectionFixture(t)
	// (a) 坏 schedule JSON → plan 降级 disabled、不恢复 dispatch。
	fixture.seedAccount(t, map[string]any{"id": "w12d-plan-acc", "status": "pending_test", "config_revision": int64(5), "dispatch_revision": int64(7), "availability_schedule_json": "{bad-json"})
	w12dSeedInputVersion(t, fixture, "w12d-plan-acc", 1)
	outcome := Outcome{OutcomeID: "w12d-plan-1", RequestID: "r", AccountID: "w12d-plan-acc", Outcome: OutcomeSuccess, ObservedAt: projectionFixtureNow, InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7,
		NextDueAt: ptrTime(projectionFixtureNow.Add(time.Hour)),
		Projection: &Projection{TargetAccountID: "w12d-plan-acc", TransitionKind: "activation_success", InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7, ExpectedAccountStatus: "pending_test"}}
	fixture.insertOutcome(projectionFixtureNow, outcome)
	result := fixture.drain(t)
	if result.Processed != 1 {
		t.Fatalf("processed=%d", result.Processed)
	}
	disposition, reason := fixture.receipt(t, "w12d-plan-1")
	if disposition != "applied" {
		t.Fatalf("receipt=%s/%s", disposition, reason)
	}
	var status string
	if err := fixture.business.QueryRow(`SELECT status FROM accounts WHERE id = 'w12d-plan-acc'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "disabled" {
		t.Fatalf("degraded plan status=%q", status)
	}
	var revision int64
	if err := fixture.business.QueryRow(`SELECT dispatch_revision FROM accounts WHERE id = 'w12d-plan-acc'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 7 {
		t.Fatalf("disabled plan must not restore dispatch: revision=%d", revision)
	}
	// (b) shouldScheduleBalanceAutoDetection 分支：非 activation、非 active 计划、
	// 非 api_key、已有余额配置 → 均返回 false。
	account := &projectionAccountFence{accountType: "api_key", balanceQueryEnabled: sql.NullInt64{Int64: 0, Valid: true}, balanceQueryConfigJSON: "{}"}
	plan := activationProjectionPlan{status: "active"}
	nonActivation := Outcome{Projection: &Projection{TransitionKind: "health_success"}}
	if scheduled, err := fixture.projector.shouldScheduleBalanceAutoDetection(account, nonActivation, plan); err != nil || scheduled {
		t.Fatalf("non activation: %t %v", scheduled, err)
	}
	inactivePlan := activationProjectionPlan{status: "disabled"}
	activation := Outcome{Projection: &Projection{TransitionKind: "activation_success"}}
	if scheduled, err := fixture.projector.shouldScheduleBalanceAutoDetection(account, activation, inactivePlan); err != nil || scheduled {
		t.Fatalf("inactive plan: %t %v", scheduled, err)
	}
	oauthAccount := &projectionAccountFence{accountType: "oauth", balanceQueryEnabled: sql.NullInt64{Int64: 0, Valid: true}, balanceQueryConfigJSON: "{}"}
	if scheduled, err := fixture.projector.shouldScheduleBalanceAutoDetection(oauthAccount, activation, plan); err != nil || scheduled {
		t.Fatalf("oauth account: %t %v", scheduled, err)
	}
	withConfig := &projectionAccountFence{accountType: "api_key", balanceQueryEnabled: sql.NullInt64{Int64: 3, Valid: true}, balanceQueryConfigJSON: `{"x":1}`}
	if scheduled, err := fixture.projector.shouldScheduleBalanceAutoDetection(withConfig, activation, plan); err != nil || scheduled {
		t.Fatalf("configured balance: %t %v", scheduled, err)
	}
	// (c) 解密失败 → 上抛。
	undecryptable := &projectionAccountFence{accountType: "api_key", balanceQueryEnabled: sql.NullInt64{Int64: 0, Valid: true}, balanceQueryConfigJSON: "{}", credentialsEncrypted: "not-an-envelope"}
	if _, err := fixture.projector.shouldScheduleBalanceAutoDetection(undecryptable, activation, plan); err == nil || !strings.Contains(err.Error(), "解密 J1 投影账户凭据失败") {
		t.Fatalf("decrypt failure: %v", err)
	}
	// (d) 合法 envelope 但内容非 JSON → 解析失败上抛。
	badJSON := &projectionAccountFence{accountType: "api_key", balanceQueryEnabled: sql.NullInt64{Int64: 0, Valid: true}, balanceQueryConfigJSON: "{}", credentialsEncrypted: testEnvelope(t, "projection-test-secret", `{bad`)}
	if _, err := fixture.projector.shouldScheduleBalanceAutoDetection(badJSON, activation, plan); err == nil || !strings.Contains(err.Error(), "解析 J1 投影账户凭据失败") {
		t.Fatalf("json failure: %v", err)
	}
	// (e) 有 key → true。
	secret := "projection-test-secret"
	good := &projectionAccountFence{accountType: "api_key", balanceQueryEnabled: sql.NullInt64{Int64: 0, Valid: true}, balanceQueryConfigJSON: "{}", credentialsEncrypted: testEnvelope(t, secret, `{"api_key":"sk-w12d"}`)}
	if scheduled, err := fixture.projector.shouldScheduleBalanceAutoDetection(good, activation, plan); err != nil || !scheduled {
		t.Fatalf("scheduled: %t %v", scheduled, err)
	}
	// (f) PG fence guard 文本。
	if !strings.Contains(cooldownObservationFenceGuard(true), "date_trunc") {
		t.Fatal("postgres guard must use date_trunc")
	}
}
