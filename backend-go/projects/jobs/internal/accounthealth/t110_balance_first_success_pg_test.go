package accounthealth

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
)

// TestW9HPgFirstHealthSuccessSeedsBalanceDetection 是 T110 的 PG 端到端回归
// （每测试独立 fresh 库，经 pgtestkit，不触碰共享库）：
//  1. 新建即 active 的物理单 Key 账户在期间编辑后 config_revision 为 2，
//     activation 首探（ConfigRevision=1）投影判 stale，不写探测意图；
//  2. 首次周期 health_success（ConfigRevision=2）仍写一次
//     balance_query_next_refresh_at=ObservedAt 探测意图（修复前永久丢失）；
//  3. 账户已有成功记录后的二次 health_success 不重写该列。
func TestW9HPgFirstHealthSuccessSeedsBalanceDetection(t *testing.T) {
	db, dsn := w9hOpenKitDB(t)
	const accountID = "t110-acc"
	w9hSeedBusinessAccount(t, db, accountID, "deepseek", "profile_deepseek_openai_v1", "api_key", "active", "chat_json", sql.NullString{}, sql.NullString{})
	// 创建后编辑把业务账户配置版本推到 2；余额查询保持关闭与空配置，
	// last_health_success_at 保持 NULL（从未成功健康检查）。
	w9hMustExec(t, db, `UPDATE juhe_business.accounts
		SET config_revision = 2, balance_query_enabled = 0, balance_query_config_json = '{}'
		WHERE id = $1`, accountID)

	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: dsn})
	if err != nil {
		t.Fatalf("open pg store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	business, err := NewProjectionBusinessDB(db)
	if err != nil {
		t.Fatalf("projection business db: %v", err)
	}
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	projector, err := NewOutcomeProjector(store, OutcomeProjectorConfig{
		Business: business, CredentialSecret: "w9h-credential-secret",
		PollInterval: time.Second, BatchSize: 10, ConsumerKey: "t110-consumer",
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new projector: %v", err)
	}
	lease := mustLease(t, store)
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(context.Background(), lease) })

	healthValues := func(observed time.Time) map[string]any {
		text := observed.UTC().Format(time.RFC3339Nano)
		return map[string]any{
			"last_health_check_at":          text,
			"last_health_success_at":        text,
			"last_health_check_status_code": 200,
		}
	}
	receipt := func(t *testing.T, outcomeID string) (string, string) {
		t.Helper()
		var disposition, reason sql.NullString
		if err := db.QueryRow(`SELECT disposition, reason FROM juhe_business.account_health_projection_receipts WHERE outcome_id = $1`, outcomeID).Scan(&disposition, &reason); err != nil {
			t.Fatalf("receipt %s: %v", outcomeID, err)
		}
		return disposition.String, reason.String
	}
	balanceIntent := func(t *testing.T) sql.NullTime {
		t.Helper()
		var value sql.NullTime
		if err := db.QueryRow(`SELECT balance_query_next_refresh_at FROM juhe_business.accounts WHERE id = $1`, accountID).Scan(&value); err != nil {
			t.Fatalf("read balance intent: %v", err)
		}
		return value
	}

	// 1) activation 首探结果在 config_revision 已升到 2 后投影，判 stale。
	activationObserved := now
	if inserted, err := store.AppendOutcome(ctx, lease, Outcome{
		OutcomeID: "t110-outcome-activation", RequestID: "t110-request-activation", AccountID: accountID,
		Outcome: exactkeyprobe.OutcomeSuccess, ObservedAt: activationObserved, StatusCode: 200,
		InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1, AccountStatus: "active",
		NextDueAt: ptrTime(now.Add(30 * time.Minute)),
		Projection: &Projection{
			TargetAccountID: accountID, TransitionKind: "activation_success", InputVersion: 1,
			ConfigRevision: 1, DispatchRevision: 1, ExpectedAccountStatus: "active",
			Values: healthValues(activationObserved),
		},
	}); err != nil || !inserted {
		t.Fatalf("append activation outcome: inserted=%t err=%v", inserted, err)
	}
	if result, err := projector.DrainOnce(ctx); err != nil {
		t.Fatalf("activation drain: %v", err)
	} else if result.Processed != 1 {
		t.Fatalf("activation drain processed=%d", result.Processed)
	}
	if disposition, reason := receipt(t, "t110-outcome-activation"); disposition != "stale" || reason != "config_revision_stale" {
		t.Fatalf("activation receipt: disposition=%q reason=%q", disposition, reason)
	}
	if intent := balanceIntent(t); intent.Valid {
		t.Fatalf("stale activation must not seed balance intent, got %s", intent.Time)
	}

	// 2) 首次周期 health_success 必须写一次探测意图（T110 修复点）。
	healthObserved := now.Add(time.Hour).Add(123 * time.Millisecond)
	if inserted, err := store.AppendOutcome(ctx, lease, Outcome{
		OutcomeID: "t110-outcome-health-1", RequestID: "t110-request-health-1", AccountID: accountID,
		Outcome: exactkeyprobe.OutcomeSuccess, ObservedAt: healthObserved, StatusCode: 200,
		InputVersion: 1, ConfigRevision: 2, DispatchRevision: 1, AccountStatus: "active",
		NextDueAt: ptrTime(healthObserved.Add(24 * time.Hour)),
		Projection: &Projection{
			TargetAccountID: accountID, TransitionKind: "health_success", InputVersion: 1,
			ConfigRevision: 2, DispatchRevision: 1, ExpectedAccountStatus: "active",
			Values: healthValues(healthObserved),
		},
	}); err != nil || !inserted {
		t.Fatalf("append health success: inserted=%t err=%v", inserted, err)
	}
	if result, err := projector.DrainOnce(ctx); err != nil {
		t.Fatalf("health drain: %v", err)
	} else if result.Processed != 1 {
		t.Fatalf("health drain processed=%d", result.Processed)
	}
	if disposition, reason := receipt(t, "t110-outcome-health-1"); disposition != "applied" || reason != "" {
		t.Fatalf("health receipt: disposition=%q reason=%q", disposition, reason)
	}
	if intent := balanceIntent(t); !intent.Valid || !intent.Time.Equal(healthObserved) {
		t.Fatalf("balance intent=%+v want %s", intent, healthObserved)
	}

	// 3) 账户已有成功记录后的二次成功不得重写探测意图。
	sentinel := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	w9hMustExec(t, db, `UPDATE juhe_business.accounts SET balance_query_next_refresh_at = $1 WHERE id = $2`, sentinel, accountID)
	secondObserved := healthObserved.Add(time.Second)
	if inserted, err := store.AppendOutcome(ctx, lease, Outcome{
		OutcomeID: "t110-outcome-health-2", RequestID: "t110-request-health-2", AccountID: accountID,
		Outcome: exactkeyprobe.OutcomeSuccess, ObservedAt: secondObserved, StatusCode: 200,
		InputVersion: 1, ConfigRevision: 2, DispatchRevision: 1, AccountStatus: "active",
		NextDueAt: ptrTime(secondObserved.Add(24 * time.Hour)),
		Projection: &Projection{
			TargetAccountID: accountID, TransitionKind: "health_success", InputVersion: 1,
			ConfigRevision: 2, DispatchRevision: 1, ExpectedAccountStatus: "active",
			Values: healthValues(secondObserved),
		},
	}); err != nil || !inserted {
		t.Fatalf("append second health success: inserted=%t err=%v", inserted, err)
	}
	if result, err := projector.DrainOnce(ctx); err != nil {
		t.Fatalf("second health drain: %v", err)
	} else if result.Processed != 1 {
		t.Fatalf("second health drain processed=%d", result.Processed)
	}
	if disposition, reason := receipt(t, "t110-outcome-health-2"); disposition != "applied" || reason != "" {
		t.Fatalf("second health receipt: disposition=%q reason=%q", disposition, reason)
	}
	if intent := balanceIntent(t); !intent.Valid || !intent.Time.Equal(sentinel) {
		t.Fatalf("second success must not rewrite balance intent, got %+v", intent)
	}
}

// TestW9HPgAuthorizationInstanceFirstSuccessDoesNotSeedBalance 覆盖 B1：授权
// 实例的 credentials_encrypted 生产为占位 "{}"（不是 V1 envelope），
// health_success 首次成功路径必须在解密前按「非授权实例」跳过；否则
// DecryptV1Envelope 报 envelope 格式错误，DrainOnce 在投影处中断、receipt
// 不写、游标不前进，毒化全部账户投影。
func TestW9HPgAuthorizationInstanceFirstSuccessDoesNotSeedBalance(t *testing.T) {
	db, dsn := w9hOpenKitDB(t)
	const sourceID = "t110-src"
	const instanceID = "t110-inst"
	const authorizationID = "t110-auth"
	w9hSeedBusinessAccount(t, db, sourceID, "gpt", "profile_gpt_openai_v1", "api_key", "active", "chat_json", sql.NullString{}, sql.NullString{})
	w9hMustExec(t, db, `INSERT INTO juhe_business.resource_authorizations (
		id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status,
		effective_source_type, activated_at, limits_json, created_by, created_at, updated_at
	) VALUES ($1, 'account', $2, 'sys_admin', 'sys_admin', 'health_check', 'active',
		'direct', $3, '{"daily":{"enabled":true,"limit":100}}', 'w9h', $4, $5)`,
		authorizationID, sourceID, w9hFixtureNow, w9hFixtureNow, w9hFixtureNow)
	w9hSeedBusinessAccount(t, db, instanceID, "gpt", "profile_gpt_openai_v1", "api_key", "active", "chat_json",
		sql.NullString{String: sourceID, Valid: true}, sql.NullString{String: authorizationID, Valid: true})
	// 生产形态：实例凭据为占位 "{}"；从未成功健康检查，余额查询关闭且空配置。
	w9hMustExec(t, db, `UPDATE juhe_business.accounts
		SET credentials_encrypted = '{}', status = 'active', balance_query_enabled = 0,
		    balance_query_config_json = '{}', last_health_success_at = NULL,
		    config_revision = 1, dispatch_revision = 1
		WHERE id = $1`, instanceID)

	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: dsn})
	if err != nil {
		t.Fatalf("open pg store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	business, err := NewProjectionBusinessDB(db)
	if err != nil {
		t.Fatalf("projection business db: %v", err)
	}
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	projector, err := NewOutcomeProjector(store, OutcomeProjectorConfig{
		Business: business, CredentialSecret: "w9h-credential-secret",
		PollInterval: time.Second, BatchSize: 10, ConsumerKey: "t110-instance-consumer",
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new projector: %v", err)
	}
	lease := mustLease(t, store)
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(context.Background(), lease) })

	observed := now.Add(2 * time.Hour)
	observedText := observed.UTC().Format(time.RFC3339Nano)
	if inserted, err := store.AppendOutcome(ctx, lease, Outcome{
		OutcomeID: "t110-inst-outcome-health", RequestID: "t110-inst-request-health", AccountID: instanceID,
		Outcome: exactkeyprobe.OutcomeSuccess, ObservedAt: observed, StatusCode: 200,
		InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1, AccountStatus: "active",
		NextDueAt: ptrTime(observed.Add(24 * time.Hour)),
		Projection: &Projection{
			TargetAccountID: instanceID, TransitionKind: "health_success", InputVersion: 1,
			ConfigRevision: 1, DispatchRevision: 1, ExpectedAccountStatus: "active",
			SourceRevision: ptrInt64(1),
			Values: map[string]any{
				"last_health_check_at":          observedText,
				"last_health_success_at":        observedText,
				"last_health_check_status_code": 200,
			},
		},
	}); err != nil || !inserted {
		t.Fatalf("append instance health success: inserted=%t err=%v", inserted, err)
	}
	result, err := projector.DrainOnce(ctx)
	if err != nil {
		t.Fatalf("instance health drain must not fail on placeholder credentials: %v", err)
	}
	if result.Processed != 1 {
		t.Fatalf("instance health drain processed=%d", result.Processed)
	}
	var disposition, reason sql.NullString
	if err := db.QueryRow(`SELECT disposition, reason FROM juhe_business.account_health_projection_receipts WHERE outcome_id = $1`, "t110-inst-outcome-health").Scan(&disposition, &reason); err != nil {
		t.Fatalf("instance receipt: %v", err)
	}
	if disposition.String != "applied" || reason.String != "" {
		t.Fatalf("instance receipt: disposition=%q reason=%q", disposition.String, reason.String)
	}
	var balanceIntent sql.NullTime
	if err := db.QueryRow(`SELECT balance_query_next_refresh_at FROM juhe_business.accounts WHERE id = $1`, instanceID).Scan(&balanceIntent); err != nil {
		t.Fatalf("read instance balance intent: %v", err)
	}
	if balanceIntent.Valid {
		t.Fatalf("authorization instance must not seed balance intent, got %s", balanceIntent.Time)
	}
	// 投影仍须正常落地健康事实（跳过余额意图不改变 health_success 其它写入）。
	var lastSuccess sql.NullTime
	if err := db.QueryRow(`SELECT last_health_success_at FROM juhe_business.accounts WHERE id = $1`, instanceID).Scan(&lastSuccess); err != nil {
		t.Fatalf("read instance last success: %v", err)
	}
	if !lastSuccess.Valid || !lastSuccess.Time.Equal(observed) {
		t.Fatalf("instance last_health_success_at=%+v want %s", lastSuccess, observed)
	}
}
