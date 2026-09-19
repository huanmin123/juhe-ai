package accounthealth

// w16f_reader_pg_test.go 波次 w16f 第四批：PG direct input 读链未覆盖臂
// （w1cover 覆盖库，门禁复用 w9hPgDB，不可达即 t.Skip）：
//   - scanDirectCandidate 的 cooldown fence 构造臂与来源 revision 附加臂；
//   - load() 的配额校验错误传播臂、配额耗尽跳过臂、LoadAccount 失败隔离臂；
//   - authorizationQuotaEligible 的 team grant limits 解析失败臂；
//   - loadDirectSchedule 各设置项的无效值臂（回滚事务内注入，不落库）。
// 数据全部使用 w16f- 前缀 ID，t.Cleanup 清理；连接串永不进入日志或断言。

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

const w16fFixtureNow = "2026-09-18T00:00:00Z"

const w16fReaderSecret = "w16f-reader-secret"

// w16fCredentials 用读链 secret 构造凭据 envelope。
func w16fCredentials(t *testing.T, plaintext string) string {
	t.Helper()
	envelope, err := EncryptV1Envelope(w16fReaderSecret, []byte(plaintext))
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

// w16fCleanupJobsRows 清理本文件产生的全部 fixture 行。
func w16fCleanupJobsRows(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`DELETE FROM juhe_jobs.account_health_outcomes WHERE outcome_id LIKE 'w16f-%' OR account_id LIKE 'w16f-%'`,
		`DELETE FROM juhe_jobs.account_health_current_state WHERE account_id LIKE 'w16f-%'`,
		`DELETE FROM juhe_jobs.account_health_direct_input_suppressions WHERE account_id LIKE 'w16f-%'`,
		`DELETE FROM juhe_jobs.account_health_key_cursors WHERE account_id LIKE 'w16f-%'`,
		`DELETE FROM juhe_jobs.account_health_owner_leases WHERE owner_id LIKE 'w16f-%'`,
		`DELETE FROM juhe_business.account_health_projection_receipts WHERE outcome_id LIKE 'w16f-%'`,
		`DELETE FROM juhe_business.account_health_projection_cursors WHERE consumer_key LIKE 'w16f-%'`,
		`DELETE FROM juhe_business.account_circuit_outbox WHERE account_id LIKE 'w16f-%'`,
		`DELETE FROM juhe_business.group_accounts WHERE account_id LIKE 'w16f-%'`,
		`DELETE FROM juhe_business.account_health_jobs_input_versions WHERE account_id LIKE 'w16f-%'`,
		`DELETE FROM juhe_business.resource_authorization_grants WHERE id LIKE 'w16f-%'`,
		`DELETE FROM juhe_business.group_accounts WHERE account_id LIKE 'w16f-%'`,
		`DELETE FROM juhe_business.accounts WHERE id LIKE 'w16f-%'`,
		`DELETE FROM juhe_business.resource_authorizations WHERE id LIKE 'w16f-%'`,
		`DELETE FROM juhe_business.system_teams WHERE id LIKE 'w16f-%'`,
		`DELETE FROM juhe_stats.usage_stats_totals WHERE scope_id LIKE 'w16f-%'`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Logf("w16f cleanup: %v", err)
		}
	}
}

// w16fSeedSettings 幂等写入读链所需的 canonical settings。
func w16fSeedSettings(t *testing.T, db *sql.DB) {
	t.Helper()
	for key, value := range map[string]string{
		"accountHealthCheckIntervalHours":      "24",
		"accountHealthCheckJitterMinutes":      "10",
		"accountHealthCheckFailureThreshold":   "3",
		"defaultTemporaryUnschedulableMinutes": "60",
		"cooldownAccountRetestMaxBackoffHours": "24",
		"usageStatsTimezone":                   "\"Asia/Shanghai\"",
	} {
		w9hMustExec(t, db, `INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
			VALUES ('sys_admin', $1, $2, $3) ON CONFLICT (system_account_id, key) DO NOTHING`, key, value, w16fFixtureNow)
	}
}

// w16fSeedAuthorizationChain 建源账户 + 授权（带 team）+ 授权实例。
// FK 顺序：team → 授权 → 实例。
func w16fSeedAuthorizationChain(t *testing.T, db *sql.DB) {
	t.Helper()
	w9hMustExec(t, db, `INSERT INTO juhe_business.system_teams (id, name, status, created_by, created_at, updated_at)
		VALUES ('w16f-team', 'w16f-team', 'active', 'w16f', $1, $1) ON CONFLICT (id) DO NOTHING`, w16fFixtureNow)
	w9hSeedBusinessAccount(t, db, "w16f-cool-src", "deepseek", "profile_deepseek_openai_v1", "api_key", "active", "chat_json", sql.NullString{}, sql.NullString{})
	w9hMustExec(t, db, `UPDATE juhe_business.accounts SET credentials_encrypted = $1 WHERE id = 'w16f-cool-src'`,
		w16fCredentials(t, `{"api_key":"sk-w16f-src"}`))
	w9hMustExec(t, db, `INSERT INTO juhe_business.resource_authorizations (
		id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status,
		effective_source_type, effective_source_team_id, activated_at, limits_json, created_by, created_at, updated_at
	) VALUES ('w16f-auth', 'account', 'w16f-cool-src', 'sys_admin', 'sys_admin', 'health_check', 'active',
		'direct', 'w16f-team', $1, NULL, 'w16f', $1, $1)
	ON CONFLICT (id) DO UPDATE SET resource_id = EXCLUDED.resource_id, status = EXCLUDED.status,
		effective_source_team_id = EXCLUDED.effective_source_team_id, limits_json = EXCLUDED.limits_json, updated_at = EXCLUDED.updated_at`, w16fFixtureNow)
	w9hSeedBusinessAccount(t, db, "w16f-cool-inst", "deepseek", "profile_deepseek_openai_v1", "api_key", "active", "chat_json",
		sql.NullString{String: "w16f-cool-src", Valid: true}, sql.NullString{String: "w16f-auth", Valid: true})
	w9hMustExec(t, db, `UPDATE juhe_business.group_accounts SET account_authorization_id = 'w16f-auth' WHERE account_id = 'w16f-cool-inst'`)
}

// TestW16fReaderPGSettingArms 在回滚事务内注入无效 settings，驱动
// loadDirectSchedule 各设置项的无效值臂。
func TestW16fReaderPGSettingArms(t *testing.T) {
	db := w9hPgDB(t)
	ctx := context.Background()
	base := map[string]string{
		"accountHealthCheckIntervalHours":      "24",
		"accountHealthCheckJitterMinutes":      "10",
		"accountHealthCheckFailureThreshold":   "3",
		"defaultTemporaryUnschedulableMinutes": "60",
		"cooldownAccountRetestMaxBackoffHours": "24",
		"usageStatsTimezone":                   "\"Asia/Shanghai\"",
	}
	assertInvalid := func(t *testing.T, badKey, badValue string) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		for key, value := range base {
			if key == badKey {
				value = badValue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
				VALUES ('sys_admin', $1, $2, $3) ON CONFLICT (system_account_id, key) DO UPDATE SET value_json = EXCLUDED.value_json`, key, value, w16fFixtureNow); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, err := loadDirectSchedule(ctx, tx); err == nil || !strings.Contains(err.Error(), badKey+" 无效") {
			t.Fatalf("%s 无效必须报错: %v", badKey, err)
		}
	}
	assertInvalid(t, "accountHealthCheckJitterMinutes", `"w16f"`)
	assertInvalid(t, "accountHealthCheckFailureThreshold", `"w16f"`)
	assertInvalid(t, "defaultTemporaryUnschedulableMinutes", `"w16f"`)
	assertInvalid(t, "cooldownAccountRetestMaxBackoffHours", `"w16f"`)
}

// TestW16fReaderPGScanQuotaArms 覆盖读链的 cooldown fence 扫描、来源
// revision 附加、配额错误/耗尽、team grant 解析与 LoadAccount 隔离臂。
func TestW16fReaderPGScanQuotaArms(t *testing.T) {
	db := w9hPgDB(t)
	// 先清一次历史遗留（上一轮失败可能留下 FK 顺序导致未清净的行）。
	w16fCleanupJobsRows(t, db)
	t.Cleanup(func() { w16fCleanupJobsRows(t, db) })
	w16fSeedSettings(t, db)
	now := func() time.Time { return time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC) }

	// (a) 冷却账户 + 完整 cooldown fence（无来源）→ 扫描构造 fence。
	w9hSeedBusinessAccount(t, db, "w16f-cool-simple", "deepseek", "profile_deepseek_openai_v1", "api_key", "temporary_unavailable", "chat_json", sql.NullString{}, sql.NullString{})
	w9hMustExec(t, db, `UPDATE juhe_business.accounts SET
		cooldown_retest_observation_started_at = $1,
		cooldown_retest_generation = 'w16f-gen-simple'
		WHERE id = 'w16f-cool-simple'`, w16fFixtureNow)

	// (b) 授权实例 + cooldown fence → 扫描附加来源 revision。
	w16fSeedAuthorizationChain(t, db)
	w9hMustExec(t, db, `UPDATE juhe_business.accounts SET status = 'temporary_unavailable',
		cooldown_retest_observation_started_at = $1,
		cooldown_retest_generation = 'w16f-gen-inst'
		WHERE id = 'w16f-cool-inst'`, w16fFixtureNow)

	reader, err := NewPostgresDirectInputReader(db, w16fReaderSecret, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.LoadDueWithFailures(context.Background(), 10)
	if err != nil {
		t.Fatalf("冷却扫描 load 必须成功: %v", err)
	}
	simpleIsolated := false
	for _, failure := range result.Failures {
		if failure.AccountID == "w16f-cool-simple" {
			simpleIsolated = true
		}
	}
	if !simpleIsolated {
		t.Fatalf("无来源 revision 的冷却候选必须被隔离: inputs=%+v failures=%+v", result.Inputs, result.Failures)
	}
	instanceInputFound := false
	for _, input := range result.Inputs {
		if input.AccountID == "w16f-cool-inst" {
			instanceInputFound = true
			if input.Cooldown == nil || input.Cooldown.Generation != "w16f-gen-inst" {
				t.Fatalf("实例输入必须携带冷却 fence: %+v", input.Cooldown)
			}
			if input.Cooldown != nil && (input.Cooldown.SourceConfigRevision == nil || *input.Cooldown.SourceConfigRevision != 1) {
				t.Fatalf("实例 fence 必须附加来源 revision: %+v", input.Cooldown)
			}
		}
	}
	if !instanceInputFound {
		t.Fatalf("授权实例候选缺失: inputs=%+v failures=%+v", result.Inputs, result.Failures)
	}

	// (c) 授权 limits 非法 → 整读失败（authorizationQuotaEligible 错误传播）。
	w9hMustExec(t, db, `UPDATE juhe_business.resource_authorizations SET limits_json = 'w16f-not-json' WHERE id = 'w16f-auth'`)
	if _, err := reader.LoadDueWithFailures(context.Background(), 10); err == nil {
		t.Fatal("非法 limits 必须导致整读失败")
	}

	// (d) 授权总配额耗尽 → 候选静默省略（无 input 无 failure）。
	w9hMustExec(t, db, `UPDATE juhe_business.resource_authorizations SET limits_json = $1 WHERE id = 'w16f-auth'`, `{"total":{"enabled":true,"limit":10}}`)
	w9hMustExec(t, db, `INSERT INTO juhe_stats.usage_stats_totals (system_account_id, scope_type, scope_id, total_cost_usd, updated_at)
		VALUES ('sys_admin', 'account_authorization', 'w16f-auth', 100, $1)
		ON CONFLICT (system_account_id, scope_type, scope_id) DO UPDATE SET total_cost_usd = 100`, w16fFixtureNow)
	result, err = reader.LoadDueWithFailures(context.Background(), 10)
	if err != nil {
		t.Fatalf("配额耗尽读必须成功收敛: %v", err)
	}
	for _, input := range result.Inputs {
		if input.AccountID == "w16f-cool-inst" {
			t.Fatal("配额耗尽的候选必须被省略")
		}
	}
	for _, failure := range result.Failures {
		if failure.AccountID == "w16f-cool-inst" {
			t.Fatal("配额耗尽不是候选构造失败")
		}
	}
	w9hMustExec(t, db, `DELETE FROM juhe_stats.usage_stats_totals WHERE scope_id = 'w16f-auth'`)

	// (e) team grant limits 非法 → 解析失败传播。
	w9hMustExec(t, db, `UPDATE juhe_business.accounts SET status = 'active',
		cooldown_retest_observation_started_at = NULL, cooldown_retest_generation = NULL
		WHERE id = 'w16f-cool-inst'`)
	w9hMustExec(t, db, `UPDATE juhe_business.resource_authorizations SET limits_json = NULL WHERE id = 'w16f-auth'`)
	w9hMustExec(t, db, `INSERT INTO juhe_business.resource_authorization_grants (
		id, resource_type, resource_id, resource_owner_system_account_id, grantee_type, grantee_team_id,
		scope, status, limits_json, created_by, created_at, updated_at
	) VALUES ('w16f-grant', 'account', 'w16f-cool-src', 'sys_admin', 'team', 'w16f-team',
		'health_check', 'active', 'w16f-grant-not-json', 'w16f', $1, $1)
		ON CONFLICT (id) DO NOTHING`, w16fFixtureNow)
	if _, err := reader.LoadDueWithFailures(context.Background(), 10); err == nil {
		t.Fatal("team grant limits 非法必须导致整读失败")
	}

	// (f) LoadAccount 遇坏候选 → 隔离结果转错误。
	w9hSeedBusinessAccount(t, db, "w16f-bad", "deepseek", "profile_deepseek_openai_v1", "api_key", "active", "chat_json", sql.NullString{}, sql.NullString{})
	w9hMustExec(t, db, `UPDATE juhe_business.accounts SET credentials_encrypted = 'w16f-not-an-envelope' WHERE id = 'w16f-bad'`)
	if _, err := reader.LoadAccount(context.Background(), "w16f-bad"); err == nil || !strings.Contains(err.Error(), "候选构造失败") {
		t.Fatalf("LoadAccount 坏候选必须报错: %v", err)
	}
}

// TestW16fStorePGLeaseNoRowsArm 覆盖 PG 租约未到期时 acquire 的
// ErrNoRows → (false, nil) 臂。
func TestW16fStorePGLeaseNoRowsArm(t *testing.T) {
	db := w9hPgDB(t)
	t.Cleanup(func() { w16fCleanupJobsRows(t, db) })
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: w9hPgDSN(t)})
	if err != nil {
		t.Fatalf("open pg store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	lease, acquired, err := store.AcquireOwnerLease(ctx, "w16f-lease-holder", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("首次租约必须获取: %t %v", acquired, err)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(ctx, lease) })
	// 租约仍被持有 → 另一 owner 的 acquire 返回 false, nil。
	second, acquired, err := store.AcquireOwnerLease(ctx, "w16f-lease-other", time.Minute)
	if err != nil || acquired || second.OwnerID != "" {
		t.Fatalf("未到期租约必须返回 false,nil: %t %+v %v", acquired, second, err)
	}
}

// TestW16fPGDispatchFamilyChildLockArm 覆盖 PG 家族推进的授权实例子锁臂
// （advanceDispatchRevisionFamily 1177-1181）。
func TestW16fPGDispatchFamilyChildLockArm(t *testing.T) {
	db := w9hPgDB(t)
	t.Cleanup(func() { w16fCleanupJobsRows(t, db) })
	w9hSeedBusinessAccount(t, db, "w16f-pg-root", "deepseek", "profile_deepseek_openai_v1", "api_key", "active", "chat_json", sql.NullString{}, sql.NullString{})
	w9hSeedBusinessAccount(t, db, "w16f-pg-inst", "deepseek", "profile_deepseek_openai_v1", "api_key", "active", "chat_json", sql.NullString{}, sql.NullString{})
	w9hMustExec(t, db, `UPDATE juhe_business.accounts SET authorization_instance_source_account_id = 'w16f-pg-root' WHERE id = 'w16f-pg-inst'`)
	business, err := NewProjectionBusinessDB(db, true)
	if err != nil {
		t.Fatal(err)
	}
	jobsStore, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: w9hPgDSN(t)})
	if err != nil {
		t.Fatalf("open pg store: %v", err)
	}
	t.Cleanup(func() { _ = jobsStore.Close() })
	projector, err := NewOutcomeProjector(jobsStore, OutcomeProjectorConfig{
		Business: business, CredentialSecret: w16fReaderSecret,
		PollInterval: DefaultProjectionPollInterval, BatchSize: DefaultProjectionBatchSize,
		ConsumerKey: "w16f-pg-dispatch", Now: func() time.Time { return time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := projector.advanceDispatchRevisionFamily(ctx, tx, "w16f-pg-inst", "w16f-pg-transition"); err != nil {
		t.Fatalf("PG 家族推进必须成功: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var outboxRows int
	if err := db.QueryRow(`SELECT count(*) FROM juhe_business.account_circuit_outbox WHERE account_id IN ('w16f-pg-root', 'w16f-pg-inst')`).Scan(&outboxRows); err != nil {
		t.Fatal(err)
	}
	if outboxRows != 2 {
		t.Fatalf("PG outbox 行数=%d", outboxRows)
	}
}
