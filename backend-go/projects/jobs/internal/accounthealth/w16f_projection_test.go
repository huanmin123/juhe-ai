package accounthealth

// w16f_projection_test.go 波次 w16f 第三批：OutcomeProjector 未覆盖臂：
//   - Run 的 ctx 取消臂与 DrainOnce 的批量页钳制臂；
//   - listProjectedOutcomes 的查询失败臂（w13g5 注入 store 作为 jobs 侧）；
//   - 游标损坏臂（时间戳格式非法：loadCursor/advanceCursor 归一化失败）与
//     全 NULL 游标行的 existing=nil + INSERT 冲突臂；
//   - dispatch 家族推进：授权实例回溯家族根臂与家族根缺失臂。
// 全部基于 SQLite 临时 fixture，不触网。

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"database/sql"

	_ "modernc.org/sqlite"
)

// TestW16fProjectorRunCancellationArm 覆盖 Run 的 ctx 取消臂。
func TestW16fProjectorRunCancellationArm(t *testing.T) {
	fixture := newProjectionFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := fixture.projector.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的 Run 必须返回 ctx 错误: %v", err)
	}
}

// TestW16fDrainBatchPageClampArm 覆盖 DrainOnce 的页大小钳制臂。
func TestW16fDrainBatchPageClampArm(t *testing.T) {
	fixture := newProjectionFixture(t)
	projector, err := NewOutcomeProjector(fixture.store, OutcomeProjectorConfig{
		Business:         mustW16fBusiness(t, fixture.business),
		CredentialSecret: "projection-test-secret",
		PollInterval:     DefaultProjectionPollInterval,
		BatchSize:        150,
		Logger:           slog.Default(),
		Now:              func() time.Time { return projectionFixtureNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := projector.DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("空 store drain 必须成功: %v", err)
	}
	if result.Processed != 0 {
		t.Fatalf("空 store drain processed=%d", result.Processed)
	}
}

// TestW16fListProjectedOutcomesQueryErrorArm 覆盖 outcome 页读取的查询失败臂。
func TestW16fListProjectedOutcomesQueryErrorArm(t *testing.T) {
	fixture := newProjectionFixture(t)
	jobsStore, spec := w13g5HealthInjectStore(t)
	if err := jobsStore.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	projector, err := NewOutcomeProjector(jobsStore, OutcomeProjectorConfig{
		Business:         mustW16fBusiness(t, fixture.business),
		CredentialSecret: "projection-test-secret",
		PollInterval:     DefaultProjectionPollInterval,
		BatchSize:        DefaultProjectionBatchSize,
		Logger:           slog.Default(),
		Now:              func() time.Time { return projectionFixtureNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	spec.armOnce("SELECT payload, observed_at FROM account_health_outcomes")
	defer spec.disarm()
	if _, err := projector.DrainOnce(context.Background()); err == nil {
		t.Fatal("outcome 页读取失败必须传播")
	}
}

// TestW16fCursorCorruptionArms 覆盖游标时间戳非法的归一化失败臂与全 NULL
// 游标行的 INSERT 冲突臂。
func TestW16fCursorCorruptionArms(t *testing.T) {
	fixture := newProjectionFixture(t)
	ctx := context.Background()
	projector := fixture.projector
	// observed_at 非法时间戳 → loadCursor 归一化失败。
	if _, err := fixture.business.Exec(`INSERT INTO account_health_projection_cursors (consumer_key, observed_at, outcome_id, updated_at) VALUES (?, 'w16f-not-a-time', 'w16f-outcome', '2026-09-18T00:00:00Z')`, DefaultProjectionConsumerKey); err != nil {
		t.Fatal(err)
	}
	if _, err := projector.loadCursor(ctx); err == nil || !strings.Contains(err.Error(), "observedAt") {
		t.Fatalf("非法游标时间戳必须报错: %v", err)
	}
	// advanceCursor 同样在归一化处失败。
	next := &OutcomeCursor{ObservedAtText: projectionFixtureNow.Format(time.RFC3339Nano), ObservedAt: projectionFixtureNow, OutcomeID: "w16f-next"}
	if _, err := projector.advanceCursor(ctx, next); err == nil {
		t.Fatal("advanceCursor 非法游标必须报错")
	}
	// 全 NULL 游标行 → existing=nil → INSERT 主键冲突。
	if _, err := fixture.business.Exec(`UPDATE account_health_projection_cursors SET observed_at = NULL, outcome_id = NULL WHERE consumer_key = ?`, DefaultProjectionConsumerKey); err != nil {
		t.Fatal(err)
	}
	if _, err := projector.advanceCursor(ctx, next); err == nil {
		t.Fatal("全 NULL 游标行的重插必须冲突报错")
	}
}

// TestW16fDispatchFamilyArms 覆盖 dispatch 家族推进的实例回溯与家族根缺失臂。
func TestW16fDispatchFamilyArms(t *testing.T) {
	fixture := newProjectionFixture(t)
	ctx := context.Background()
	// 家族根 + 授权实例（source 指向根）+ 孤儿实例（source 缺失）。
	for _, row := range []struct {
		id      string
		source  any
		revision int64
	}{{id: "w16f-root", source: nil, revision: 7}, {id: "w16f-instance", source: "w16f-root", revision: 7}, {id: "w16f-orphan", source: "w16f-missing-root", revision: 7}} {
		if _, err := fixture.business.Exec(`INSERT INTO accounts (id, status, config_revision, dispatch_revision, authorization_instance_source_account_id) VALUES (?, 'active', 5, ?, ?)`, row.id, row.revision, row.source); err != nil {
			t.Fatal(err)
		}
	}
	projector := fixture.projector
	// 实例恢复 → 回溯家族根并推进整个家族。
	tx, err := fixture.business.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := projector.advanceDispatchRevisionFamily(ctx, tx, "w16f-instance", "w16f-transition-1"); err != nil {
		t.Fatalf("家族推进必须成功: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var rootRevision, instanceRevision int64
	if err := fixture.business.QueryRow(`SELECT dispatch_revision FROM accounts WHERE id = 'w16f-root'`).Scan(&rootRevision); err != nil {
		t.Fatal(err)
	}
	if err := fixture.business.QueryRow(`SELECT dispatch_revision FROM accounts WHERE id = 'w16f-instance'`).Scan(&instanceRevision); err != nil {
		t.Fatal(err)
	}
	if rootRevision != 8 || instanceRevision != 8 {
		t.Fatalf("家族根与实例必须同时推进: root=%d instance=%d", rootRevision, instanceRevision)
	}
	var outboxRows int
	if err := fixture.business.QueryRow(`SELECT count(*) FROM account_circuit_outbox WHERE account_id IN ('w16f-root', 'w16f-instance')`).Scan(&outboxRows); err != nil {
		t.Fatal(err)
	}
	if outboxRows != 2 {
		t.Fatalf("outbox 行数=%d", outboxRows)
	}
	// 家族根缺失 → 错误臂。
	tx2, err := fixture.business.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx2.Rollback() }()
	if err := projector.advanceDispatchRevisionFamily(ctx, tx2, "w16f-orphan", "w16f-transition-2"); err == nil || !strings.Contains(err.Error(), "AI 账户不存在") {
		t.Fatalf("家族根缺失必须报错: %v", err)
	}
}

// mustW16fBusiness 把 fixture 业务库包装成投影业务句柄。
func mustW16fBusiness(t *testing.T, business *sql.DB) *ProjectionBusinessDB {
	t.Helper()
	handle, err := NewProjectionBusinessDB(business, false)
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

// w16fBusinessFixture 提供独立于 projectionFixture 的最小业务库（注入场景用）。
func w16fBusinessFixture(t *testing.T) *sql.DB {
	t.Helper()
	business, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "w16f-business.sqlite3"))+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = business.Close() })
	if _, err := business.Exec(projectionBusinessSchema); err != nil {
		t.Fatalf("初始化 w16f 业务库失败: %v", err)
	}
	return business
}

// TestW16fProjectorRunTimerResetArm 覆盖 Run 的定时器触发后 Reset 臂。
func TestW16fProjectorRunTimerResetArm(t *testing.T) {
	fixture := newProjectionFixture(t)
	projector, err := NewOutcomeProjector(fixture.store, OutcomeProjectorConfig{
		Business:         mustW16fBusiness(t, fixture.business),
		CredentialSecret: "projection-test-secret",
		PollInterval:     minProjectionPollInterval,
		BatchSize:        DefaultProjectionBatchSize,
		Logger:           slog.Default(),
		Now:              func() time.Time { return projectionFixtureNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(600 * time.Millisecond)
		cancel()
	}()
	if err := projector.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run 必须以取消退出: %v", err)
	}
}

// TestW16fProjectionMissingAccountArm 覆盖账户缺失的 stale receipt 臂
// （findAccountFence ErrNoRows）。
func TestW16fProjectionMissingAccountArm(t *testing.T) {
	fixture := newProjectionFixture(t)
	outcome := Outcome{
		OutcomeID: "w16f-missing-outcome", RequestID: "w16f-missing-request", AccountID: "w16f-ghost",
		Outcome: OutcomeSuccess, ObservedAt: projectionFixtureNow,
		InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7,
		NextDueAt: ptrTime(projectionFixtureNow.Add(time.Hour)),
		Projection: &Projection{
			TargetAccountID: "w16f-ghost", TransitionKind: "health_success",
			InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7,
			ExpectedAccountStatus: "active",
		},
	}
	fixture.insertOutcome(projectionFixtureNow, outcome)
	result := fixture.drain(t)
	if result.Processed != 1 {
		t.Fatalf("processed=%d", result.Processed)
	}
	var disposition, reason string
	if err := fixture.business.QueryRow(`SELECT disposition, reason FROM account_health_projection_receipts WHERE outcome_id = 'w16f-missing-outcome'`).Scan(&disposition, &reason); err != nil {
		t.Fatal(err)
	}
	if disposition != "stale" || reason != "account_missing_or_deleted" {
		t.Fatalf("缺失账户必须落 stale receipt: %s %s", disposition, reason)
	}
}

// TestW16fProjectionActivationErrorArm 覆盖 activation_error 投影（允许缺
// NextDueAt → timeTextOrNil nil 分支）。
func TestW16fProjectionActivationErrorArm(t *testing.T) {
	fixture := newProjectionFixture(t)
	fixture.seedAccount(t, map[string]any{"id": "acct-1", "status": "pending_test"})
	outcome := Outcome{
		OutcomeID: "w16f-activation-error", RequestID: "w16f-activation-error-request", AccountID: "acct-1",
		Outcome: OutcomeNeutral, ObservedAt: projectionFixtureNow,
		InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7,
		Projection: &Projection{
			TargetAccountID: "acct-1", TransitionKind: "activation_error",
			InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7,
			ExpectedAccountStatus: "pending_test",
		},
	}
	fixture.insertOutcome(projectionFixtureNow, outcome)
	result := fixture.drain(t)
	if result.Processed != 1 {
		t.Fatalf("processed=%d", result.Processed)
	}
	var status string
	if err := fixture.business.QueryRow(`SELECT status FROM accounts WHERE id = 'acct-1'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "error" {
		t.Fatalf("activation_error 必须置 error: %s", status)
	}
}
