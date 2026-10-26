package accounthealth

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// w9h_pg_test.go 运行 J1 jobs-owned PG 分支：Store 的 postgres 臂、outcome
// 投影 PG 臂与 PG direct input 读链。建库走 pgtestkit 每测试独立 fresh 库
// （原 shared.env 门禁共享覆盖库装配已退役）：juhe_jobs schema 由 prepare
// 预置（J1 EnsureSchema 契约是只校验属主不建 schema），业务/统计面表按
// maintenance 权威 PG schema 源逐表提取执行（生产形状零漂移），PG 不可达
// 由 pgtestkit 显式 Fatal 而非 t.Skip；隔离库随测试结束删除，不再需要
// 共享库行级清理纪律与库形自适应探测。连接串永不进入日志或断言消息。

const w9hFixtureNow = "2026-09-06T12:00:00.000Z"

// w9hBusinessTables 是 fresh 库需要预置的 juhe_business 权威表，按 FK 依赖
// 序排列（fixtures 以 sys_admin/gpt/deepseek 写行，相关外键全部落在集合内）。
var w9hBusinessTables = []string{
	"system_accounts", "system_teams", "vendors", "system_settings", "proxy_profiles",
	"resource_authorizations", "resource_authorization_grants", "account_connections",
	"account_endpoint_modes", "accounts", "groups", "group_accounts",
	"group_account_stats_dirty",
	"account_health_jobs_input_versions", "account_health_projection_cursors",
	"account_health_projection_receipts", "account_circuit_outbox",
}

// w9hStatsTables 是 J1 direct input 契约要求的 juhe_stats 只读面表。
var w9hStatsTables = []string{
	"usage_stats_totals", "usage_stats_daily", "usage_stats_weekly",
	"usage_stats_monthly", "usage_quota_hourly_windows",
}

// w9hExtractTableDDL 从 maintenance 权威 schema 源提取指定表的 CREATE TABLE
// 语句（DDL 在 Go 源里是裸串字面量，不含反引号，首个反引号即语句终点；
// w14j statsagg seed 同款先例）。任一表提取失败显式 Fatal。
func w9hExtractTableDDL(t *testing.T, source string, tables []string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("w9h: 无法读取权威 schema 源 %s: %v", source, err)
	}
	pattern := `(?s)CREATE TABLE IF NOT EXISTS (` + strings.Join(tables, "|") + `) \(.*?` + "`" + `,`
	matches := regexp.MustCompile(pattern).FindAllStringSubmatch(string(raw), -1)
	statements := map[string]string{}
	for _, match := range matches {
		// 剥掉捕获尾部的裸串终止符 "`,"，只留 SQL 本体（终终止于建表右括号）。
		statements[match[1]] = strings.TrimSuffix(strings.ReplaceAll(match[0], "\r\n", "\n"), "`,")
	}
	missing := []string{}
	for _, table := range tables {
		if statements[table] == "" {
			missing = append(missing, table)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("w9h: 权威 schema 源 %s 缺少表 DDL: %s", source, strings.Join(missing, ", "))
	}
	return statements
}

// w9hOpenKitDB 打开 pgtestkit 隔离 PG 测试库：预置三个 schema 命名空间、
// 按权威 DDL 建业务/统计面表并种入 sys_admin 属主与 gpt/deepseek 厂商行
// （fixtures 外键目标）。返回裸连接池（fixtures/projection/reader 用）与
// 库连接串（store 构造注入）。测试结束自动关池并删库。
func w9hOpenKitDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	business := w9hExtractTableDDL(t, "../../../../projects/maintenance/internal/schema/pg_schema_business_tables.go", w9hBusinessTables)
	stats := w9hExtractTableDDL(t, "../../../../projects/maintenance/internal/schema/pg_schema_stats.go", w9hStatsTables)
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		for _, schema := range []string{"juhe_jobs", "juhe_business", "juhe_stats"} {
			if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+schema); err != nil {
				return err
			}
		}
		for _, table := range w9hBusinessTables {
			if _, err := db.ExecContext(ctx, `SET search_path = juhe_business; `+business[table]); err != nil {
				return fmt.Errorf("预置 juhe_business.%s: %w", table, err)
			}
		}
		for _, table := range w9hStatsTables {
			if _, err := db.ExecContext(ctx, `SET search_path = juhe_stats; `+stats[table]); err != nil {
				return fmt.Errorf("预置 juhe_stats.%s: %w", table, err)
			}
		}
		// fixtures 以 'sys_admin' 写 settings/账户行（settings FK system_accounts），
		// gpt/deepseek 账户行外键 vendors(code)：幂等补齐属主与厂商行。
		if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
			VALUES ('sys_admin', 'sys_admin', 'sys_admin', 'admin', 'active', 'w9h-fixture-no-login', $1, $1)
			ON CONFLICT (id) DO NOTHING`, w9hFixtureNow); err != nil {
			return fmt.Errorf("system_accounts 种子失败: %w", err)
		}
		for _, vendor := range []string{"gpt", "deepseek"} {
			if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.vendors (id, code, name, enabled, created_at, updated_at)
				VALUES ($1, $1, $1, 1, $2, $2) ON CONFLICT (code) DO NOTHING`, vendor, w9hFixtureNow); err != nil {
				return fmt.Errorf("vendors 种子失败: %w", err)
			}
		}
		// J1 direct schedule 的 canonical settings 是共享覆盖库形态下的既有
		// 前置（w12d/w16f 各测试 seed 同值；w13g8 缺设置臂在事务内显式清空），
		// fresh 库按同值预置，保持各臂断言语义不变。
		for key, value := range map[string]string{
			"accountHealthCheckIntervalHours":      "24",
			"accountHealthCheckJitterMinutes":      "10",
			"accountHealthCheckFailureThreshold":   "3",
			"defaultTemporaryUnschedulableMinutes": "60",
			"cooldownAccountRetestMaxBackoffHours": "24",
			"usageStatsTimezone":                   `"Asia/Shanghai"`,
		} {
			if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
				VALUES ('sys_admin', $1, $2, $3) ON CONFLICT (system_account_id, key) DO NOTHING`, key, value, w9hFixtureNow); err != nil {
				return fmt.Errorf("canonical settings 种子失败: %w", err)
			}
		}
		return nil
	})
	return testDB.DB, testDB.DSN
}

func w9hMustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("fixture exec 失败: %v (query: %.120s)", err, query)
	}
}

const w9hScheduleJSON = `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"00:00","end":"23:59"}]}`

// w9hSeedBusinessAccount inserts one business account fixture plus its input
// version row. Extra columns keep the NOT NULL contract satisfied.
// w9hPresetBaseURL 按连接 preset 返回断言期望的连接 base_url（gpt OAuth 走
// Codex 后端；供应商解耦后 base_url 是连接行事实，凭据 JSON 不再承载）。
func w9hPresetBaseURL(preset, accountType string) string {
	switch preset {
	case "gpt_openai_v1":
		if accountType == "oauth" {
			return "https://chatgpt.com/backend-api/codex"
		}
		return "https://api.openai.com/v1"
	case "deepseek_openai_v1":
		return "https://api.deepseek.com"
	default:
		return "https://upstream.example.com/v1"
	}
}

// w9hSeedBusinessAccount 写入供应商解耦后形态的账户：身份列
// vendor_code/connection_driver_code + 健康检查连接行（connection_preset_code/
// base_url/account_endpoint_modes，profile 参数剥掉 profile_ 前缀即 preset）。
// 授权实例行（sourceID 非空）不写 vendor/连接事实，资源事实归来源账户。
func w9hSeedBusinessAccount(t *testing.T, db *sql.DB, id, provider, profile, accountType, status, endpointMode string, sourceID, authorizationID sql.NullString) {
	t.Helper()
	isInstance := sourceID.Valid && sourceID.String != ""
	if isInstance {
		w9hMustExec(t, db, `INSERT INTO juhe_business.accounts (
			id, system_account_id, connection_driver_code, protocol_code, protocol_version,
			name, type, status, credentials_encrypted, credential_fingerprint, credential_mask,
			schedulable, concurrency_limit, priority, super_priority_enabled, fallback_enabled,
			health_check_model, health_check_endpoint_mode,
			authorization_instance_source_account_id, authorization_instance_authorization_id,
			created_at, updated_at, config_revision, dispatch_revision
		) VALUES (
			$1, 'sys_admin', $2, 'openai', 'v1',
			$3, $4, $5, $6, 'w9h-fp', 'w9h-mask',
			1, 0, 0, 0, 0,
			'w9h-model', $7,
			$8, $9,
			$10, $11, 1, 1
		)`, id, provider, "w9h-"+id, accountType, status, w9hCredentialsEnvelope(id), endpointMode, sourceID, authorizationID, w9hFixtureNow, w9hFixtureNow)
	} else {
		w9hMustExec(t, db, `INSERT INTO juhe_business.accounts (
			id, system_account_id, vendor_code, connection_driver_code, oauth_type, protocol_code, protocol_version,
			name, type, status, credentials_encrypted, credential_fingerprint, credential_mask,
			schedulable, concurrency_limit, priority, super_priority_enabled, fallback_enabled,
			health_check_model, health_check_endpoint_mode,
			authorization_instance_source_account_id, authorization_instance_authorization_id,
			created_at, updated_at, config_revision, dispatch_revision
		) VALUES (
			$1, 'sys_admin', $2, $2, $3, 'openai', 'v1',
			$4, $5, $6, $7, 'w9h-fp', 'w9h-mask',
			1, 0, 0, 0, 0,
			'w9h-model', $8,
			$9, $10,
			$11, $12, 1, 1
		)`, id, provider, w9hOAuthType(accountType), "w9h-"+id, accountType, status, w9hCredentialsEnvelope(id), endpointMode, sourceID, authorizationID, w9hFixtureNow, w9hFixtureNow)
		preset := strings.TrimPrefix(profile, "profile_")
		connectionID := "w9h-conn-" + id
		w9hMustExec(t, db, `INSERT INTO juhe_business.account_connections (id, account_id, connection_preset_code, protocol_surface_code, base_url, enabled, created_at, updated_at)
			VALUES ($1, $2, $3, 'openai_v1', $4, 1, $5, $6) ON CONFLICT (id) DO NOTHING`,
			connectionID, id, preset, w9hPresetBaseURL(preset, accountType), w9hFixtureNow, w9hFixtureNow)
		w9hMustExec(t, db, `INSERT INTO juhe_business.account_endpoint_modes (connection_id, endpoint_mode) VALUES ($1, $2)
			ON CONFLICT (connection_id, endpoint_mode) DO NOTHING`, connectionID, endpointMode)
		w9hMustExec(t, db, `UPDATE juhe_business.accounts SET health_check_connection_id = $1 WHERE id = $2`, connectionID, id)
	}
	w9hMustExec(t, db, `INSERT INTO juhe_business.account_health_jobs_input_versions (account_id, current_version, reserved_at) VALUES ($1, 1, $2)
		ON CONFLICT (account_id) DO NOTHING`, id, w9hFixtureNow)
	w9hMustExec(t, db, `INSERT INTO juhe_business.groups (id, system_account_id, name, enabled, is_default, created_at, updated_at)
		VALUES ('w9h-group', 'sys_admin', 'w9h-group', 1, 0, $1, $2)
		ON CONFLICT (id) DO NOTHING`, w9hFixtureNow, w9hFixtureNow)
	w9hMustExec(t, db, `INSERT INTO juhe_business.group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at)
		VALUES ('sys_admin', 'w9h-group', $1, 1, $2, $3)`, id, w9hFixtureNow, w9hFixtureNow)
}

// w9hOAuthType 返回 fixture 的 accounts.oauth_type 列值（google_oauth 账户
// 用 ai_studio 占位，避免触碰 Code Assist 分流臂）。
func w9hOAuthType(accountType string) any {
	if accountType == "google_oauth" {
		return "ai_studio"
	}
	return nil
}

func w9hCredentialsEnvelope(id string) string {
	envelope, err := exactkeyprobe.EncryptV1Envelope("w9h-credential-secret", []byte(`{"api_key":"sk-w9h-`+id+`"}`))
	if err != nil {
		panic(err)
	}
	return envelope
}

func TestW9HPgStoreLifecycleAndSuppression(t *testing.T) {
	_, dsn := w9hOpenKitDB(t)
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: dsn})
	if err != nil {
		t.Fatalf("open pg store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	// Lease lifecycle: acquire, renew, release and re-acquire.
	lease, acquired, err := store.AcquireOwnerLease(ctx, "w9h-lease-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire: %v acquired=%t", err, acquired)
	}
	if renewed, err := store.RenewOwnerLease(ctx, lease, time.Minute); err != nil || !renewed {
		t.Fatalf("renew: renewed=%t err=%v", renewed, err)
	}
	if err := store.ReleaseOwnerLease(ctx, lease); err != nil {
		t.Fatalf("release: %v", err)
	}
	lease, acquired, err = store.AcquireOwnerLease(ctx, "w9h-lease-owner-2", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("re-acquire: %v acquired=%t", err, acquired)
	}

	// Outcome append: direct-input invalid writes a suppression row.
	observed := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	inserted, err := store.AppendOutcome(ctx, lease, Outcome{
		OutcomeID: "w9h-outcome-1", RequestID: "w9h-request-1", AccountID: "w9h-acc-x",
		Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: observed, InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1,
		ErrorCode: "direct_input_invalid", ErrorMessage: "坏凭据", NextDueAt: ptrTime(observed.Add(10 * time.Minute)),
	})
	if err != nil || !inserted {
		t.Fatalf("append outcome: inserted=%t err=%v", inserted, err)
	}
	suppressions, err := store.LoadDirectInputSuppressions(ctx, observed)
	if err != nil {
		t.Fatalf("load suppressions: %v", err)
	}
	found := false
	for _, suppression := range suppressions {
		if suppression.AccountID == "w9h-acc-x" && suppression.InputVersion == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("suppression missing: %+v", suppressions)
	}
	// Duplicate request id: no insert; the matching payload refreshes the
	// suppression next-due timestamp.
	inserted, err = store.AppendOutcome(ctx, lease, Outcome{
		OutcomeID: "w9h-outcome-1-dup", RequestID: "w9h-request-1", AccountID: "w9h-acc-x",
		Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: observed, InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1,
		ErrorCode: "direct_input_invalid", NextDueAt: ptrTime(observed.Add(20 * time.Minute)),
	})
	if err != nil || inserted {
		t.Fatalf("duplicate append: inserted=%t err=%v", inserted, err)
	}
	// Current state + request dedupe + key cursors.
	if state, found, err := store.LoadCurrentState(ctx, "w9h-acc-x"); err != nil || !found || state.AccountID != "w9h-acc-x" {
		t.Fatalf("load current state: found=%t state=%#v err=%v", found, state, err)
	}
	if has, err := store.HasRequest(ctx, "w9h-request-1"); err != nil || !has {
		t.Fatalf("has request: %t %v", has, err)
	}
	if has, err := store.HasRequest(ctx, "w9h-request-absent"); err != nil || has {
		t.Fatalf("absent request: %t %v", has, err)
	}
	if err := store.SaveKeyCursor(ctx, lease, "w9h-acc-x", "balance", "fp-1", 3); err != nil {
		t.Fatalf("save key cursor: %v", err)
	}
	if index, found, err := store.LoadKeyCursor(ctx, "w9h-acc-x", "balance", "fp-1"); err != nil || !found || index != 3 {
		t.Fatalf("load key cursor: index=%d found=%t err=%v", index, found, err)
	}
	if err := store.ReleaseOwnerLease(ctx, lease); err != nil {
		t.Fatalf("release final lease: %v", err)
	}
}

func TestW9HPgOutcomeProjectionEndToEnd(t *testing.T) {
	db, dsn := w9hOpenKitDB(t)

	// Projection fixtures: a pending_test account with an enabled schedule so
	// the activation transition restores dispatch and writes the circuit outbox.
	w9hSeedBusinessAccount(t, db, "w9h-proj-acc", "deepseek", "profile_deepseek_openai_v1", "api_key", "pending_test", "chat_json", sql.NullString{}, sql.NullString{})
	w9hMustExec(t, db, `UPDATE juhe_business.accounts SET availability_schedule_json = $1 WHERE id = 'w9h-proj-acc'`, w9hScheduleJSON)

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
	now := func() time.Time { return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC) }
	projector, err := NewOutcomeProjector(store, OutcomeProjectorConfig{
		Business: business, CredentialSecret: "w9h-credential-secret",
		PollInterval: time.Second, BatchSize: 10, ConsumerKey: "w9h-consumer", Now: now,
	})
	if err != nil {
		t.Fatalf("new projector: %v", err)
	}

	base := Outcome{
		AccountID: "w9h-proj-acc", Outcome: exactkeyprobe.OutcomeUpstreamFailed,
		ObservedAt: now(), InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1,
	}
	lease := mustLease(t, store)
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(context.Background(), lease) })
	// 1) applied health_failure with projection.
	if _, err := store.AppendOutcome(ctx, lease, Outcome{
		OutcomeID: "w9h-outcome-apply", RequestID: "w9h-request-apply", AccountID: base.AccountID,
		Outcome: base.Outcome, ObservedAt: base.ObservedAt, InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1,
		NextDueAt: ptrTime(now().Add(5 * time.Minute)), FailureCount: 2,
		Projection: &Projection{
			TargetAccountID: base.AccountID, TransitionKind: "health_failure", InputVersion: 1,
			ConfigRevision: 1, DispatchRevision: 1, ExpectedAccountStatus: "pending_test",
		},
	}); err != nil {
		t.Fatalf("append applied outcome: %v", err)
	}
	// 2) stale outcome (config revision ahead of the account fence).
	if _, err := store.AppendOutcome(ctx, lease, Outcome{
		OutcomeID: "w9h-outcome-stale", RequestID: "w9h-request-stale", AccountID: base.AccountID,
		Outcome: base.Outcome, ObservedAt: base.ObservedAt.Add(time.Second), InputVersion: 1, ConfigRevision: 99, DispatchRevision: 1,
		NextDueAt: ptrTime(now().Add(5 * time.Minute)),
		Projection: &Projection{
			TargetAccountID: base.AccountID, TransitionKind: "health_failure", InputVersion: 1,
			ConfigRevision: 99, DispatchRevision: 1, ExpectedAccountStatus: "pending_test",
		},
	}); err != nil {
		t.Fatalf("append stale outcome: %v", err)
	}

	result, err := projector.DrainOnce(ctx)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if result.Processed != 2 {
		t.Fatalf("drain processed=%d", result.Processed)
	}
	assertW9HDisposition := func(outcomeID, disposition, reason string) {
		t.Helper()
		var gotDisposition sql.NullString
		var gotReason sql.NullString
		if err := db.QueryRow(`SELECT disposition, reason FROM juhe_business.account_health_projection_receipts WHERE outcome_id = $1`, outcomeID).Scan(&gotDisposition, &gotReason); err != nil {
			t.Fatalf("receipt %s: %v", outcomeID, err)
		}
		if gotDisposition.String != disposition || gotReason.String != reason {
			t.Fatalf("receipt %s: disposition=%q reason=%q", outcomeID, gotDisposition.String, gotReason.String)
		}
	}
	assertW9HDisposition("w9h-outcome-apply", "applied", "")
	assertW9HDisposition("w9h-outcome-stale", "stale", "config_revision_stale")
	// 3) activation_success on a new input epoch restores dispatch through the
	// family root.
	w9hMustExec(t, db, `UPDATE juhe_business.account_health_jobs_input_versions SET current_version = 2 WHERE account_id = 'w9h-proj-acc'`)
	if _, err := store.AppendOutcome(ctx, lease, Outcome{
		OutcomeID: "w9h-outcome-activate", RequestID: "w9h-request-activate", AccountID: base.AccountID,
		Outcome: exactkeyprobe.OutcomeSuccess, ObservedAt: base.ObservedAt.Add(2 * time.Second), InputVersion: 2, ConfigRevision: 1, DispatchRevision: 1,
		NextDueAt: ptrTime(now().Add(30 * time.Minute)),
		Projection: &Projection{
			TargetAccountID: base.AccountID, TransitionKind: "activation_success", InputVersion: 2,
			ConfigRevision: 1, DispatchRevision: 1, ExpectedAccountStatus: "pending_test",
		},
	}); err != nil {
		t.Fatalf("append activation outcome: %v", err)
	}
	result, err = projector.DrainOnce(ctx)
	if err != nil {
		t.Fatalf("activation drain: %v", err)
	}
	if result.Processed != 1 {
		t.Fatalf("activation drain processed=%d", result.Processed)
	}
	assertW9HDisposition("w9h-outcome-activate", "applied", "")
	var failureCount int
	var accountStatus string
	if err := db.QueryRow(`SELECT health_check_failure_count, status FROM juhe_business.accounts WHERE id = 'w9h-proj-acc'`).Scan(&failureCount, &accountStatus); err != nil {
		t.Fatal(err)
	}
	if failureCount != 0 || accountStatus != "active" {
		t.Fatalf("activation must reset failure count and activate: count=%d status=%s", failureCount, accountStatus)
	}
	var outboxRows int
	if err := db.QueryRow(`SELECT count(*) FROM juhe_business.account_circuit_outbox WHERE account_id = 'w9h-proj-acc'`).Scan(&outboxRows); err != nil {
		t.Fatal(err)
	}
	if outboxRows != 1 {
		t.Fatalf("dispatch outbox rows=%d", outboxRows)
	}
	// Receipts make a second drain a no-op.
	result, err = projector.DrainOnce(ctx)
	if err != nil {
		t.Fatalf("second drain: %v", err)
	}
	if result.Processed != 0 {
		t.Fatalf("second drain processed=%+v", result)
	}
}

func mustLease(t *testing.T, store *Store) OwnerLease {
	t.Helper()
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), "w9h-proj-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("lease: %v acquired=%t", err, acquired)
	}
	return lease
}

func TestW9HPgDirectInputReaderChain(t *testing.T) {
	db, dsn := w9hOpenKitDB(t)

	// Settings needed by loadDirectSchedule (idempotent canonical values).
	for key, value := range map[string]string{
		"accountHealthCheckIntervalHours":      "24",
		"accountHealthCheckJitterMinutes":      "10",
		"accountHealthCheckFailureThreshold":   "3",
		"defaultTemporaryUnschedulableMinutes": "60",
		"cooldownAccountRetestMaxBackoffHours": "24",
		"usageStatsTimezone":                   "\"Asia/Shanghai\"",
	} {
		w9hMustExec(t, db, `INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
			VALUES ('sys_admin', $1, $2, $3) ON CONFLICT (system_account_id, key) DO NOTHING`, key, value, w9hFixtureNow)
	}

	// (a) simple due api_key account with a proxy.
	w9hSeedBusinessAccount(t, db, "w9h-din-1", "deepseek", "profile_deepseek_openai_v1", "api_key", "active", "chat_json", sql.NullString{}, sql.NullString{})
	w9hMustExec(t, db, `INSERT INTO juhe_business.proxy_profiles (id, system_account_id, name, type, host, port, enabled, created_at, updated_at)
		VALUES ('w9h-proxy-1', 'sys_admin', 'w9h proxy', 'http', '127.0.0.1', 8080, true, $1, $2)`, w9hFixtureNow, w9hFixtureNow)
	w9hMustExec(t, db, `UPDATE juhe_business.accounts SET proxy_profile_id = 'w9h-proxy-1' WHERE id = 'w9h-din-1'`)

	// (b) authorization instance: source account + active authorization grant.
	w9hSeedBusinessAccount(t, db, "w9h-din-src", "gpt", "profile_gpt_openai_v1", "oauth", "active", "responses_json", sql.NullString{}, sql.NullString{})
	w9hMustExec(t, db, `UPDATE juhe_business.accounts SET credentials_encrypted = $1 WHERE id = 'w9h-din-src'`,
		w9hOAuthEnvelope(`{"access_token":"at-w9h","expires_at":"2027-01-01T00:00:00.000Z","base_url":"https://chatgpt.com/backend-api/codex"}`))
	w9hMustExec(t, db, `INSERT INTO juhe_business.resource_authorizations (
		id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status,
		effective_source_type, activated_at, limits_json, created_by, created_at, updated_at
	) VALUES ('w9h-auth-1', 'account', 'w9h-din-src', 'sys_admin', 'sys_admin', 'health_check', 'active',
		'direct', $1, '{"daily":{"enabled":true,"limit":100}}', 'w9h', $2, $3)`, w9hFixtureNow, w9hFixtureNow, w9hFixtureNow)
	w9hSeedBusinessAccount(t, db, "w9h-din-2", "gpt", "profile_gpt_openai_v1", "oauth", "active", "responses_json", sql.NullString{String: "w9h-din-src", Valid: true}, sql.NullString{String: "w9h-auth-1", Valid: true})

	w9hMustExec(t, db, `UPDATE juhe_business.group_accounts SET account_authorization_id = 'w9h-auth-1' WHERE account_id = 'w9h-din-2'`)

	// (c) malformed candidate: undecryptable credentials isolate as a failure.
	w9hSeedBusinessAccount(t, db, "w9h-din-bad", "deepseek", "profile_deepseek_openai_v1", "api_key", "active", "chat_json", sql.NullString{}, sql.NullString{})
	w9hMustExec(t, db, `UPDATE juhe_business.accounts SET credentials_encrypted = 'not-an-envelope' WHERE id = 'w9h-din-bad'`)

	reader, err := NewPostgresDirectInputReader(db, "w9h-credential-secret", time.Hour, func() time.Time {
		return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}
	if err := reader.CheckContract(context.Background()); err != nil {
		t.Fatalf("check contract: %v", err)
	}
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: dsn})
	if err != nil {
		t.Fatalf("open pg store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	reader.SetSuppressionProvider(func(ctx context.Context, now time.Time) ([]DirectInputSuppression, error) {
		return store.LoadDirectInputSuppressions(ctx, now)
	})

	result, err := reader.LoadDueWithFailures(context.Background(), 10)
	if err != nil {
		t.Fatalf("load due: %v", err)
	}
	byAccount := map[string]bool{}
	for _, input := range result.Inputs {
		byAccount[input.AccountID] = true
	}
	if !byAccount["w9h-din-1"] || !byAccount["w9h-din-2"] {
		t.Fatalf("inputs=%v failures=%+v", byAccount, result.Failures)
	}
	for _, input := range result.Inputs {
		if input.AccountID == "w9h-din-1" {
			if input.BaseURL != "https://api.deepseek.com" {
				t.Fatalf("deepseek base url=%q", input.BaseURL)
			}
			if input.Proxy == nil {
				t.Fatal("proxy envelope missing")
			}
			if len(input.APIKeys) != 1 || input.APIKeys[0].Fingerprint == "" {
				t.Fatalf("api keys=%+v", input.APIKeys)
			}
			if input.TLSPolicyVersion != "j1-direct-upstream-v1" || input.ExpiresAt.IsZero() || input.Schedule.FailureThreshold != 3 {
				t.Fatalf("input envelope=%+v", input)
			}
			// The probe protocol for deepseek chat mode is the OpenAI wire
			// protocol.
			if input.Type != "api_key" || input.Provider != "openai" {
				t.Fatalf("probe protocol=%q type=%q", input.Provider, input.Type)
			}
		}
		if input.AccountID == "w9h-din-2" {
			if !input.Eligibility.AuthorizationEligible {
				t.Fatalf("authorization eligibility=%+v", input.Eligibility)
			}
			if input.Eligibility.SourceConfigRevision == nil || *input.Eligibility.SourceConfigRevision != 1 {
				t.Fatalf("source revision=%v", input.Eligibility.SourceConfigRevision)
			}
			if input.OAuthAccess == nil {
				t.Fatal("oauth access envelope missing")
			}
			if input.BaseURL != "https://chatgpt.com/backend-api/codex" {
				t.Fatalf("codex base url=%q", input.BaseURL)
			}
			if input.OAuthExpiresAt == nil || !input.OAuthExpiresAt.After(time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)) {
				t.Fatalf("oauth expires=%v", input.OAuthExpiresAt)
			}
		}
	}
	var badIsolated bool
	for _, failure := range result.Failures {
		if failure.AccountID == "w9h-din-bad" {
			badIsolated = true
		}
	}
	if !badIsolated {
		t.Fatalf("malformed candidate not isolated: %+v", result.Failures)
	}

	// Explicit account load skips the schedule predicate but keeps the guards.
	inputs, err := reader.LoadAccount(context.Background(), "w9h-din-1")
	if err != nil || len(inputs) != 1 {
		t.Fatalf("load account: %d %v", len(inputs), err)
	}
	// Empty account id fails fast.
	if _, err := reader.LoadAccount(context.Background(), "  "); err == nil {
		t.Fatal("blank account id must fail")
	}
}

func w9hOAuthEnvelope(payload string) string {
	envelope, err := exactkeyprobe.EncryptV1Envelope("w9h-credential-secret", []byte(payload))
	if err != nil {
		panic(err)
	}
	return envelope
}
