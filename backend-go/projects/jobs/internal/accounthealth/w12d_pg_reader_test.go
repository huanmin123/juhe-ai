package accounthealth

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"
)

// w12d_pg_reader_test.go 通过门禁化开发 PG（w1cover 覆盖库）补齐 w12d 波次
// PG 分支：direct input 配额/抑制/错误隔离臂与 store 冷却状态 CAS 臂。
// 数据全部使用 w12d- 前缀 ID，测试结束清理；数据库不可达时 t.Skip。
// 连接串永不进入日志或断言消息。

const w12dFixtureNowText = "2026-09-06T12:00:00Z"

func w12dPgDB(t *testing.T) *sql.DB {
	t.Helper()
	db := w9hPgDB(t)
	t.Cleanup(func() { w12dCleanupRows(t, db) })
	return db
}

func w12dCleanupRows(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`DELETE FROM juhe_jobs.account_health_outcomes WHERE outcome_id LIKE 'w12d-%' OR account_id LIKE 'w12d-%' OR request_id LIKE 'w12d-%'`,
		`DELETE FROM juhe_jobs.account_health_current_state WHERE account_id LIKE 'w12d-%'`,
		`DELETE FROM juhe_jobs.account_health_direct_input_suppressions WHERE account_id LIKE 'w12d-%'`,
		`DELETE FROM juhe_jobs.account_health_key_cursors WHERE account_id LIKE 'w12d-%'`,
		`DELETE FROM juhe_jobs.account_health_owner_leases WHERE owner_id LIKE 'w12d-%'`,
		`DELETE FROM juhe_business.account_health_projection_receipts WHERE outcome_id LIKE 'w12d-%'`,
		`DELETE FROM juhe_business.account_health_projection_cursors WHERE consumer_key LIKE 'w12d-%'`,
		`DELETE FROM juhe_business.account_health_jobs_input_versions WHERE account_id LIKE 'w12d-%'`,
		`DELETE FROM juhe_business.account_circuit_outbox WHERE account_id LIKE 'w12d-%'`,
		`DELETE FROM juhe_business.group_accounts WHERE account_id LIKE 'w12d-%' OR group_id LIKE 'w12d-%'`,
		`DELETE FROM juhe_business.groups WHERE id LIKE 'w12d-%'`,
		`DELETE FROM juhe_business.resource_authorization_grants WHERE id LIKE 'w12d-%' OR resource_id LIKE 'w12d-%'`,
		`DELETE FROM juhe_business.resource_authorizations WHERE id LIKE 'w12d-%'`,
		`DELETE FROM juhe_business.proxy_profiles WHERE id LIKE 'w12d-%'`,
		`DELETE FROM juhe_business.accounts WHERE id LIKE 'w12d-%'`,
		`DELETE FROM juhe_stats.usage_stats_totals WHERE system_account_id LIKE 'w12d-%' OR scope_id LIKE 'w12d-%'`,
		`DELETE FROM juhe_stats.usage_stats_daily WHERE system_account_id LIKE 'w12d-%' OR scope_id LIKE 'w12d-%'`,
		`DELETE FROM juhe_stats.usage_stats_weekly WHERE system_account_id LIKE 'w12d-%' OR scope_id LIKE 'w12d-%'`,
		`DELETE FROM juhe_stats.usage_stats_monthly WHERE system_account_id LIKE 'w12d-%' OR scope_id LIKE 'w12d-%'`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Logf("w12d cleanup: %v", err)
		}
	}
}

// w12dSeedSettings 保证 direct schedule settings 幂等存在（canonical 值）。
func w12dSeedSettings(t *testing.T, db *sql.DB) {
	t.Helper()
	for key, value := range map[string]string{
		"accountHealthCheckIntervalHours":      "24",
		"accountHealthCheckJitterMinutes":      "10",
		"accountHealthCheckFailureThreshold":   "3",
		"defaultTemporaryUnschedulableMinutes": "60",
		"cooldownAccountRetestMaxBackoffHours": "24",
		"usageStatsTimezone":                   "\"Asia/Shanghai\"",
	} {
		if _, err := db.Exec(`INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
			VALUES ('sys_admin', $1, $2, $3) ON CONFLICT (system_account_id, key) DO NOTHING`, key, value, w12dFixtureNowText); err != nil {
			t.Fatal(err)
		}
	}
}

// w12dSeedAPIKeyAccount 插入一个可被 direct input 读到的 api_key 候选。
func w12dSeedAPIKeyAccount(t *testing.T, db *sql.DB, secret, id, provider, profile string) {
	t.Helper()
	envelope, err := EncryptV1Envelope(secret, []byte(`{"api_key":"sk-w12d-`+id+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.accounts (
		id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
		name, type, status, credentials_encrypted, credential_fingerprint, credential_mask,
		schedulable, concurrency_limit, priority, super_priority_enabled, fallback_enabled,
		client_compatibility, health_check_model, health_check_endpoint_mode,
		created_at, updated_at, config_revision, dispatch_revision
	) VALUES (
		$1, 'sys_admin', $2, $3, 'openai', 'v1',
		$4, 'api_key', 'active', $5, 'w12d-fp', 'w12d-mask',
		1, 0, 0, 0, 0,
		'', 'w12d-model', 'chat_json',
		$6, $6, 5, 7
	)`, id, provider, profile, "w12d-"+id, envelope, w12dFixtureNowText); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.account_health_jobs_input_versions (account_id, current_version, reserved_at) VALUES ($1, 1, $2)
		ON CONFLICT (account_id) DO NOTHING`, id, w12dFixtureNowText); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.groups (id, system_account_id, name, provider_code, enabled, is_default, created_at, updated_at)
		VALUES ('w12d-group', 'sys_admin', 'w12d-group', 'gpt', 1, 0, $1, $1)
		ON CONFLICT (id) DO NOTHING`, w12dFixtureNowText); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at)
		VALUES ('sys_admin', 'w12d-group', $1, 1, $2, $2) ON CONFLICT (group_id, account_id) DO NOTHING`, id, w12dFixtureNowText); err != nil {
		t.Fatal(err)
	}
}

func TestW12dReaderLimitAndValidationArms(t *testing.T) {
	db := w12dPgDB(t)
	w12dSeedSettings(t, db)
	reader, err := NewPostgresDirectInputReader(db, "w12d-secret", time.Hour, func() time.Time {
		return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}
	// limit 越界臂。
	if _, err := reader.LoadDueWithFailures(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "limit 必须在 1..") {
		t.Fatalf("limit=0: %v", err)
	}
	if _, err := reader.LoadDueWithFailures(context.Background(), maxJ1Capacity+1); err == nil {
		t.Fatal("limit>maxJ1Capacity 必须报错")
	}
	// LoadDue 存在隔离 failure 时必须报错。
	reader.SetSuppressionProvider(func(ctx context.Context, now time.Time) ([]DirectInputSuppression, error) {
		return nil, nil
	})
	// LoadDue 存在隔离 failure 时必须报错：以真实坏凭据候选制造 failure。
	w12dSeedAPIKeyAccount(t, db, "w12d-secret", "w12d-limit-bad", "deepseek", "profile_deepseek_openai_v1")
	if _, err := db.Exec(`UPDATE juhe_business.accounts SET credentials_encrypted = 'not-an-envelope' WHERE id = 'w12d-limit-bad'`); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.LoadDue(context.Background(), 10); err == nil || !strings.Contains(err.Error(), "LoadDueWithFailures") {
		t.Fatalf("LoadDue with failures: %v", err)
	}
	// suppression provider 失败臂。
	reader.SetSuppressionProvider(func(ctx context.Context, now time.Time) ([]DirectInputSuppression, error) {
		return nil, context.DeadlineExceeded
	})
	if _, err := reader.LoadDueWithFailures(context.Background(), 10); err == nil || !strings.Contains(err.Error(), "抑制快照失败") {
		t.Fatalf("suppression provider failure: %v", err)
	}
	reader.SetSuppressionProvider(nil)
	// LoadAccount 空 ID。
	if _, err := reader.LoadAccount(context.Background(), "   "); err == nil {
		t.Fatal("blank account id 必须报错")
	}
}

// TestW12dReaderQuotaArms 覆盖授权配额：无 team grant 直接通过；limits_json
// 超限跳过候选；坏 limits_json 报错。
func TestW12dReaderQuotaArms(t *testing.T) {
	db := w12dPgDB(t)
	w12dSeedSettings(t, db)
	secret := "w12d-quota-secret"
	// (a) 授权 + 无 limits → 候选通过 quota（grant ErrNoRows 臂）。
	envelope, err := EncryptV1Envelope(secret, []byte(`{"api_key":"sk-w12d-quota-a"}`))
	if err != nil {
		t.Fatal(err)
	}
	// 授权场景需要源账户：quota-a 是其授权实例，resource_id 指向源账户。
	if _, err := db.Exec(`INSERT INTO juhe_business.accounts (
		id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
		name, type, status, credentials_encrypted, credential_fingerprint, credential_mask,
		schedulable, concurrency_limit, priority, super_priority_enabled, fallback_enabled,
		client_compatibility, health_check_model, health_check_endpoint_mode,
		created_at, updated_at, config_revision, dispatch_revision
	) VALUES (
		'w12d-quota-src', 'sys_admin', 'gpt', 'profile_gpt_openai_v1', 'openai', 'v1',
		'w12d-quota-src', 'api_key', 'active', $1, 'w12d-fp-src', 'w12d-mask',
		1, 0, 0, 0, 0,
		'', 'w12d-model', 'chat_json',
		$2, $2, 5, 7
	)`, envelope, w12dFixtureNowText); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.accounts (
		id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
		name, type, status, credentials_encrypted, credential_fingerprint, credential_mask,
		schedulable, concurrency_limit, priority, super_priority_enabled, fallback_enabled,
		client_compatibility, health_check_model, health_check_endpoint_mode,
		authorization_instance_source_account_id, authorization_instance_authorization_id,
		created_at, updated_at, config_revision, dispatch_revision
	) VALUES (
		'w12d-quota-a', 'sys_admin', 'gpt', 'profile_gpt_openai_v1', 'openai', 'v1',
		'w12d-quota-a', 'api_key', 'active', $1, 'w12d-fp', 'w12d-mask',
		1, 0, 0, 0, 0,
		'', 'w12d-model', 'chat_json',
		'w12d-quota-src', 'w12d-auth-1',
		$2, $2, 5, 7
	)`, envelope, w12dFixtureNowText); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.account_health_jobs_input_versions (account_id, current_version, reserved_at) VALUES ('w12d-quota-a', 1, $1)
		ON CONFLICT (account_id) DO NOTHING`, w12dFixtureNowText); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.groups (id, system_account_id, name, provider_code, enabled, is_default, created_at, updated_at)
		VALUES ('w12d-group', 'sys_admin', 'w12d-group', 'gpt', 1, 0, $1, $1) ON CONFLICT (id) DO NOTHING`, w12dFixtureNowText); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.group_accounts (system_account_id, group_id, account_id, account_authorization_id, enabled, created_at, updated_at)
		VALUES ('sys_admin', 'w12d-group', 'w12d-quota-a', 'w12d-auth-1', 1, $1, $1) ON CONFLICT (group_id, account_id) DO NOTHING`, w12dFixtureNowText); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.resource_authorizations (
		id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status,
		effective_source_type, activated_at, limits_json, created_by, created_at, updated_at
	) VALUES ('w12d-auth-1', 'account', 'w12d-quota-src', 'sys_admin', 'sys_admin', 'health_check', 'active',
		'direct', $1, NULL, 'w12d', $1, $1)`, w12dFixtureNowText); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.account_health_jobs_input_versions (account_id, current_version, reserved_at) VALUES ('w12d-quota-src', 1, $1)
		ON CONFLICT (account_id) DO NOTHING`, w12dFixtureNowText); err != nil {
		t.Fatal(err)
	}
	// 配额花费行：非零但无 limits → 无限制。
	if _, err := db.Exec(`INSERT INTO juhe_stats.usage_stats_totals (system_account_id, scope_type, scope_id, total_cost_usd, updated_at)
		VALUES ('sys_admin', 'account_authorization', 'w12d-auth-1', 12.5, $1)`, w12dFixtureNowText); err != nil {
		t.Fatal(err)
	}
	reader, err := NewPostgresDirectInputReader(db, secret, time.Hour, func() time.Time {
		return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.LoadDueWithFailures(context.Background(), 10)
	if err != nil {
		t.Fatalf("quota load: %v", err)
	}
	found := false
	for _, input := range result.Inputs {
		if input.AccountID == "w12d-quota-a" {
			found = true
			if !input.Eligibility.AuthorizationEligible {
				t.Fatal("authorization must be quota eligible")
			}
		}
	}
	if !found {
		t.Fatalf("quota candidate missing: inputs=%+v failures=%+v", result.Inputs, result.Failures)
	}
}

// TestW12dReaderSuppressionAndMalformedArms 覆盖抑制 CTE 与坏候选隔离。
func TestW12dReaderSuppressionAndMalformedArms(t *testing.T) {
	db := w12dPgDB(t)
	w12dSeedSettings(t, db)
	secret := "w12d-suppress-secret"
	w12dSeedAPIKeyAccount(t, db, secret, "w12d-supp-1", "deepseek", "profile_deepseek_openai_v1")
	// (b) 坏凭据候选（envelope 非法）→ scan 构造失败被隔离。
	w12dSeedAPIKeyAccount(t, db, secret, "w12d-supp-2", "deepseek", "profile_deepseek_openai_v1")
	if _, err := db.Exec(`UPDATE juhe_business.accounts SET credentials_encrypted = 'not-an-envelope' WHERE id = 'w12d-supp-2'`); err != nil {
		t.Fatal(err)
	}
	reader, err := NewPostgresDirectInputReader(db, secret, time.Hour, func() time.Time {
		return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	// (c) 抑制快照把 w12d-supp-1 挡在窗口内 → 只剩隔离 failure。
	suppression := []DirectInputSuppression{{AccountID: "w12d-supp-1", InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7, NextDueAt: time.Date(2026, 9, 6, 13, 0, 0, 0, time.UTC)}}
	reader.SetSuppressionProvider(func(ctx context.Context, now time.Time) ([]DirectInputSuppression, error) {
		return suppression, nil
	})
	result, err := reader.LoadDueWithFailures(context.Background(), 10)
	if err != nil {
		t.Fatalf("suppressed load: %v", err)
	}
	for _, input := range result.Inputs {
		if input.AccountID == "w12d-supp-1" {
			t.Fatal("suppressed candidate must be filtered")
		}
	}
	badIsolated := false
	for _, failure := range result.Failures {
		if failure.AccountID == "w12d-supp-2" {
			badIsolated = true
		}
	}
	if !badIsolated {
		t.Fatalf("malformed candidate must be isolated: %+v", result.Failures)
	}
}

// TestW12dStoreCooldownStateArms 覆盖 store 的冷却 current state CAS PG 臂。
func TestW12dStoreCooldownStateArms(t *testing.T) {
	db := w12dPgDB(t)
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: w9hPgDSN(t)})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := store.AcquireOwnerLease(ctx, "w12d-cooldown-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("lease: %v %t", err, acquired)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(ctx, lease) })
	observed := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	fence := &CooldownFence{ObservationStartedAt: observed, Generation: "gen-w12d-cool"}
	// cooldown 转换（ExpectedCooldownFence 存在）→ updateCooldownCurrentStateTx：
	// 无基线行 → epoch upsert 臂。
	if _, err := store.AppendOutcome(ctx, lease, Outcome{
		OutcomeID: "w12d-cool-1", RequestID: "w12d-cool-r1", AccountID: "w12d-cool-acc",
		Outcome: OutcomeNeutral, ObservedAt: observed, InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7,
		AccountStatus: "temporary_unavailable",
		NextDueAt:     ptrTime(observed.Add(30 * time.Minute)),
		Projection: &Projection{
			TargetAccountID: "w12d-cool-acc", TransitionKind: "cooldown_defer", InputVersion: 1,
			ConfigRevision: 5, DispatchRevision: 7, ExpectedAccountStatus: "temporary_unavailable",
			ExpectedCooldownFence: fence, CooldownFence: fence,
		},
	}); err != nil {
		t.Fatalf("cooldown defer append: %v", err)
	}
	state, found, err := store.LoadCurrentState(ctx, "w12d-cool-acc")
	if err != nil || !found || state.AccountStatus != "temporary_unavailable" || state.CooldownFence == nil {
		t.Fatalf("cooldown state: found=%t state=%#v err=%v", found, state, err)
	}
	// 同 epoch 冷却 CAS 命中（fence 一致）→ update 臂。
	if _, err := store.AppendOutcome(ctx, lease, Outcome{
		OutcomeID: "w12d-cool-2", RequestID: "w12d-cool-r2", AccountID: "w12d-cool-acc",
		Outcome: OutcomeNeutral, ObservedAt: observed.Add(time.Minute), InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7,
		NextDueAt: ptrTime(observed.Add(40 * time.Minute)),
		Projection: &Projection{
			TargetAccountID: "w12d-cool-acc", TransitionKind: "cooldown_defer", InputVersion: 1,
			ConfigRevision: 5, DispatchRevision: 7, ExpectedAccountStatus: "temporary_unavailable",
			ExpectedCooldownFence: fence, CooldownFence: fence,
		},
	}); err != nil {
		t.Fatalf("cooldown re-append: %v", err)
	}
	// 直接 input invalid 抑制基线：先写 baseline（error_code=direct_input_invalid、无 fence），
	// 再以冷却转换 rehydrate（updateCooldownCurrentStateTx 的空基线分支）。
	if _, err := store.AppendOutcome(ctx, lease, Outcome{
		OutcomeID: "w12d-cool-3", RequestID: "w12d-cool-r3", AccountID: "w12d-cool-baseline",
		Outcome: OutcomeTaskFailed, ObservedAt: observed, InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7,
		ErrorCode: "direct_input_invalid", ErrorMessage: "隔离",
	}); err != nil {
		t.Fatalf("invalid baseline: %v", err)
	}
	if _, err := store.AppendOutcome(ctx, lease, Outcome{
		OutcomeID: "w12d-cool-4", RequestID: "w12d-cool-r4", AccountID: "w12d-cool-baseline",
		Outcome: OutcomeNeutral, ObservedAt: observed.Add(time.Minute), InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7,
		NextDueAt: ptrTime(observed.Add(50 * time.Minute)),
		Projection: &Projection{
			TargetAccountID: "w12d-cool-baseline", TransitionKind: "cooldown_defer", InputVersion: 1,
			ConfigRevision: 5, DispatchRevision: 7, ExpectedAccountStatus: "rate_limited",
			ExpectedCooldownFence: &CooldownFence{ObservationStartedAt: observed, Generation: "gen-w12d-baseline"},
		},
	}); err != nil {
		t.Fatalf("rehydrate append: %v", err)
	}
	// task failure retry：先落普通 health 基线，再追加带 NextDueAt 的 TaskFailed。
	if _, err := store.AppendOutcome(ctx, lease, Outcome{
		OutcomeID: "w12d-cool-5", RequestID: "w12d-cool-r5", AccountID: "w12d-cool-retry",
		Outcome: OutcomeSuccess, ObservedAt: observed, InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7,
		StatusCode: 200, NextDueAt: ptrTime(observed.Add(time.Hour)),
	}); err != nil {
		t.Fatalf("success baseline: %v", err)
	}
	if _, err := store.AppendOutcome(ctx, lease, Outcome{
		OutcomeID: "w12d-cool-6", RequestID: "w12d-cool-r6", AccountID: "w12d-cool-retry",
		Outcome: OutcomeTaskFailed, ObservedAt: observed.Add(time.Minute), InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7,
		ErrorCode: "probe_timeout", ErrorMessage: "超时", NextDueAt: ptrTime(observed.Add(2 * time.Hour)),
	}); err != nil {
		t.Fatalf("task failure retry: %v", err)
	}
	retryState, found, err := store.LoadCurrentState(ctx, "w12d-cool-retry")
	if err != nil || !found || retryState.Outcome != OutcomeTaskFailed || retryState.NextDueAt == nil {
		t.Fatalf("retry state: found=%t state=%#v err=%v", found, retryState, err)
	}
	// 输出 fence 一致的投影 outcome → writeOutcomePayloadTx PG 臂。
	var payloadText string
	if err := db.QueryRow(`SELECT payload::text FROM juhe_jobs.account_health_outcomes WHERE outcome_id = 'w12d-cool-2'`).Scan(&payloadText); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payloadText, "cooldown_defer") {
		t.Fatalf("projection payload must persist: %.120s", payloadText)
	}
	_ = os.Getenv
}
