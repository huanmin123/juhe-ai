// jobs cmd 组合根共享 PG kit 夹具：pgtestkit 每测试独立 fresh 库装配组合根
// 测试所需的完整契约面（原 w1cover 共享覆盖库 + w14j 自愈 seed + w16j jobs
// 预置的装配已退役——fresh 库由本夹具一次性建齐，PG 不可达显式 Fatal）：
//   - juhe_business/juhe_stats 表按 maintenance 权威 PG schema 源逐表提取执行
//     （生产形状零漂移，列型为 canonical integer/boolean/text 组合）；
//   - jobs 自有面（juhe_jobs schema、J3a proxy_latency_*、scheduling_preferences
//     内置行、capability domain 阶段记录、canonical settings）沿用
//     w14jPGSeedStatements 加法幂等清单（权威同源，"already exists" 容忍）；
//   - sys_admin 属主与 gpt/deepseek 厂商行为 fixtures 外键目标。
//
// 连接串绝不写入日志与断言；隔离库随测试结束删除，无需行级清理纪律。
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// w14jSharedEnvValue 从 shared.env 读取指定键值（Redis state URL 等非 PG
// 事实）；找不到返回空串。值绝不打印。
func w14jSharedEnvValue(t *testing.T, key string) string {
	t.Helper()
	pgtestkit.SkipUnlessExternalAuthorized(t)
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	candidates := []string{
		filepath.Join("..", "..", "..", "..", "..", ".local", "project-resources", "dev", "env", "shared.env"),
		filepath.FromSlash("F:/sub2api-lite/.local/project-resources/dev/env/shared.env"),
	}
	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if value, ok := strings.CutPrefix(line, key+"="); ok {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

// wgCmdBusinessTables 是 fresh 库需要预置的 juhe_business 权威表，按 FK 依赖
// 序排列（组合根装配链与各 cmd 测试夹具的外键全部落在集合内）。
var wgCmdBusinessTables = []string{
	"system_accounts", "system_teams", "vendors", "global_settings", "system_settings",
	"route_strategies", "scheduling_preferences", "proxy_profiles", "proxy_pools", "proxy_pool_members", "account_tags",
	"resource_authorizations", "resource_authorization_grants", "account_connections",
	"account_endpoint_modes", "accounts", "account_proxy_leases", "groups", "group_accounts",
	"group_account_stats_dirty",
	"account_list_availability_projection_dependency_health",
	"account_list_availability_dirty",
	"account_list_availability_projections",
	"account_list_availability_projection_index",
	"account_list_availability_projection_tags",
	"account_list_availability_runtime_overlays",
	"account_list_availability_projection_viewer_health",
	"account_health_jobs_input_versions", "account_health_jobs_input_outbox",
	"account_health_projection_cursors", "account_health_projection_receipts",
	"account_circuit_outbox",
	"proxy_latency_projection_receipts", "proxy_latency_projection_cursors",
	"media_jobs", "resource_authorization_sources", "system_sessions",
	"request_quota_hourly_window_scope_bindings",
	"api_keys",
}

// wgCmdStatsTables 是 statsverify CheckSchema 的 18 表契约与组合根 stats 家族
// 消费的 juhe_stats 权威表。
var wgCmdStatsTables = []string{
	"stats_job_state", "group_account_stats", "account_usage_snapshots",
	"usage_stats_totals", "usage_stats_daily", "usage_stats_hourly",
	"usage_stats_weekly", "usage_stats_monthly", "usage_quota_hourly_windows",
	"usage_quota_hourly_window_dirty_scopes", "usage_overview_dirty_scopes",
	"usage_rank_snapshots", "usage_record_cleanup_deductions",
	"authorization_team_usage_summary_daily", "authorization_user_usage_summary_daily",
	"ai_performance_summary_dirty_system_accounts",
	"account_quality_minute_stats", "account_quality_dirty_accounts",
	"account_quality_scores", "account_health_hourly",
	"client_ip_registry", "client_ip_stats_daily", "client_ip_account_stats_daily",
	"client_ip_usage_range_windows", "client_ip_account_usage_range_windows",
	"client_ip_range_window_dirty_ips", "client_ip_account_range_window_dirty_ips",
}

// wgCmdExtractTableDDL 从 maintenance 权威 schema 源提取指定表的 CREATE TABLE
// 语句（DDL 在 Go 源里是裸串字面量，不含反引号，首个反引号即语句终点；
// accounthealth w9h 同款先例）。任一表提取失败显式 Fatal。
func wgCmdExtractTableDDL(t *testing.T, source string, tables []string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("wg cmd kit: 无法读取权威 schema 源 %s: %v", source, err)
	}
	pattern := `(?s)CREATE TABLE IF NOT EXISTS (` + strings.Join(tables, "|") + `) \(.*?` + "`" + `,`
	matches := regexp.MustCompile(pattern).FindAllStringSubmatch(string(raw), -1)
	statements := map[string]string{}
	for _, match := range matches {
		// 剥掉捕获尾部的裸串终止符 "`,"，只留 SQL 本体。
		statements[match[1]] = strings.TrimSuffix(strings.ReplaceAll(match[0], "\r\n", "\n"), "`,")
	}
	missing := []string{}
	for _, table := range tables {
		if statements[table] == "" {
			missing = append(missing, table)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("wg cmd kit: 权威 schema 源 %s 缺少表 DDL: %s", source, strings.Join(missing, ", "))
	}
	return statements
}

// wgCmdKitSeedUpgradeLedger 在夹具库上写升级账本（T89）：组合根子进程用例的
// schemagate 启动门禁要求 schema_state.schema_revision == 二进制
// contracts.SchemaRevision 且 schema_migrations 有不超过它的行，否则进程在
// 启动即被「数据库尚未升级到本版本」拦截，永远进不到被测分支。
//
// revision 一律取 contracts.SchemaRevision（不硬编码版本号）：二进制升级后
// 夹具无需再改。账本 DDL 权威在 maintenance/internal/schema/upgrade_ledger.go，
// 但 maintenance 是独立 module 且该包为 internal，jobs 不可导入，故此处按同一
// 形状建表（仅测试库使用）。
func wgCmdKitSeedUpgradeLedger(ctx context.Context, db *sql.DB) error {
	revision := contracts.SchemaRevision
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS juhe_business.schema_state (
  id              smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  schema_revision bigint NOT NULL,
  schema_digest   text NOT NULL,
  app_version     text NOT NULL,
  converged_at    text NOT NULL
)`,
		`CREATE TABLE IF NOT EXISTS juhe_business.schema_migrations (
  revision     bigint PRIMARY KEY,
  step_id      text NOT NULL UNIQUE,
  title        text NOT NULL,
  target       text NOT NULL CHECK (target IN ('primary','j3a','j3b','account_balance')),
  destructive  boolean NOT NULL DEFAULT false,
  status       text NOT NULL CHECK (status IN ('running','applied')),
  checksum     text NOT NULL,
  impl_version integer NOT NULL DEFAULT 1,
  adopted      boolean NOT NULL DEFAULT false,
  started_at   text,
  applied_at   text,
  duration_ms  integer,
  app_version  text,
  detail_json  text
)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("夹具升级账本建表失败: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.schema_migrations
		(revision, step_id, title, target, destructive, status, checksum, impl_version, adopted, applied_at, app_version)
		VALUES ($1, 'w16j-ledger-fixture', '夹具账本行（等价已被本二进制收敛）', 'primary', false, 'applied', 'fixture', 1, false, '2026-10-12T08:00:00Z', 'test')
		ON CONFLICT (revision) DO NOTHING`, revision); err != nil {
		return fmt.Errorf("夹具写升级账本行失败: %w", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.schema_state
		(id, schema_revision, schema_digest, app_version, converged_at)
		VALUES (1, $1, 'fixture', 'test', '2026-10-12T08:00:00Z')
		ON CONFLICT (id) DO UPDATE SET schema_revision = EXCLUDED.schema_revision`,
		revision); err != nil {
		return fmt.Errorf("夹具写升级状态行失败: %w", err)
	}
	return nil
}

// wgCmdKitDB 打开 pgtestkit 隔离 PG 测试库并按上述契约面预置；返回裸连接池
// （夹具/进程内装配用）与库连接串（子进程 env/ store 构造注入）。测试结束
// 自动关池并删库。
func wgCmdKitDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	business := wgCmdExtractTableDDL(t, "../../../../projects/maintenance/internal/schema/pg_schema_business_tables.go", wgCmdBusinessTables)
	stats := wgCmdExtractTableDDL(t, "../../../../projects/maintenance/internal/schema/pg_schema_stats.go", wgCmdStatsTables)
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		for _, schema := range []string{"juhe_jobs", "juhe_business", "juhe_stats", "juhe_usage", "juhe_dataset"} {
			if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+schema); err != nil {
				return err
			}
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS juhe_usage.usage_records (
			id text NOT NULL, created_at text NOT NULL, PRIMARY KEY (created_at, id))`); err != nil {
			return err
		}
		// account_proxy_leases 依赖独立 sequence（权威 DDL 中该 sequence 是
		// 单独 schema 条目，不在表提取面内）。
		if _, err := db.ExecContext(ctx, `CREATE SEQUENCE IF NOT EXISTS juhe_business.account_proxy_lease_generation_seq`); err != nil {
			return fmt.Errorf("预置 account_proxy_lease_generation_seq: %w", err)
		}
		for _, table := range wgCmdBusinessTables {
			if _, err := db.ExecContext(ctx, `SET search_path = juhe_business; `+business[table]); err != nil {
				return fmt.Errorf("预置 juhe_business.%s: %w", table, err)
			}
		}
		for _, table := range wgCmdStatsTables {
			if _, err := db.ExecContext(ctx, `SET search_path = juhe_stats; `+stats[table]); err != nil {
				return fmt.Errorf("预置 juhe_stats.%s: %w", table, err)
			}
		}
		// jobs 自有面与组合根启动契约：w14jPGSeedStatements 加法幂等清单
		// （"already exists" 在生产形状表上是预期结果，其余失败即夹具缺陷）。
		for _, statement := range w14jPGSeedStatements() {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "multiple primary keys") {
					continue
				}
				return fmt.Errorf("jobs 面种子失败: %w（语句前缀 %.80s）", err, statement)
			}
		}
		// 升级启动门禁（schemagate）夹具契约：账本两表 + 收敛状态行，revision
		// 由 contracts.SchemaRevision 派生。
		if err := wgCmdKitSeedUpgradeLedger(ctx, db); err != nil {
			return err
		}
		// fixtures 外键目标：sys_admin 属主、gpt/deepseek 厂商、J1 direct
		// schedule canonical settings（w12d/w9h/w14q 各测试 seed 同值）。
		if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.system_accounts
			(id, username, display_name, role, status, password_hash, created_at, updated_at)
			VALUES ('sys_admin', 'sys_admin', 'sys_admin', 'admin', 'active', 'wg-cmd-kit-seed-not-a-login', '2026-09-06T12:00:00.000Z', '2026-09-06T12:00:00.000Z')
			ON CONFLICT (id) DO NOTHING`); err != nil {
			return fmt.Errorf("system_accounts 种子失败: %w", err)
		}
		for _, vendor := range []string{"gpt", "deepseek"} {
			if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.vendors (id, code, name, enabled, created_at, updated_at)
				VALUES ($1, $1, $1, 1, '2026-09-06T12:00:00.000Z', '2026-09-06T12:00:00.000Z')
				ON CONFLICT (code) DO NOTHING`, vendor); err != nil {
				return fmt.Errorf("vendors 种子失败: %w", err)
			}
		}
		for key, value := range map[string]string{
			"accountHealthCheckIntervalHours":      "24",
			"accountHealthCheckJitterMinutes":      "10",
			"accountHealthCheckFailureThreshold":   "3",
			"defaultTemporaryUnschedulableMinutes": "60",
			"cooldownAccountRetestMaxBackoffHours": "24",
			"usageStatsTimezone":                   `"Asia/Shanghai"`,
		} {
			if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
				VALUES ('sys_admin', $1, $2, '2026-09-06T12:00:00.000Z') ON CONFLICT (system_account_id, key) DO NOTHING`, key, value); err != nil {
				return fmt.Errorf("canonical settings 种子失败: %w", err)
			}
		}
		return nil
	})
	// J2 余额交接表是组合根 account-balance-stats-projection 的启动前置；
	// 生产 EnsureSchema 是幂等权威 DDL，测试夹具直接复用，避免在测试侧
	// 复制表结构（w14j j2-service 另有显式 bootstrap 断言）。
	bootstrap, err := accountbalance.OpenStore(accountbalance.StoreConfig{
		Mode:        accountbalance.StorePostgres,
		PostgresURL: testDB.DSN,
	})
	if err != nil {
		t.Fatalf("打开 J2 balance store 做夹具 bootstrap: %v", err)
	}
	if err := bootstrap.EnsureSchema(context.Background()); err != nil {
		_ = bootstrap.Close()
		t.Fatalf("J2 balance 夹具 bootstrap: %v", err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("关闭 J2 balance 夹具 store: %v", err)
	}
	return testDB.DB, testDB.DSN
}

// w16jContractlessInputURL 返回同一 PG 服务器上无业务契约面的输入库连接串
// （标准 postgres 维护库）。J1/J3a 的 CheckContract 用 schema 全限定关系名
// 做零行只读校验，search_path 指向空 schema 不会使其失败；契约失败臂改在
// postgres 库上注入"可达但缺契约"：契约预检在首个全限定关系上失败，装配收敛
// 为退出码 1。只读零写、不创建任何对象；postgres 库意外包含业务契约时报错，
// 防止契约误过挂起 run()。
func w16jContractlessInputURL(t *testing.T, pgURL string) string {
	t.Helper()
	base := pgURL
	if index := strings.Index(base, "?"); index >= 0 {
		base = base[:index]
	}
	index := strings.LastIndex(base, "/")
	if index < 0 {
		t.Fatal("w16j: kit 库连接串无法解析库名")
	}
	inputURL := base[:index+1] + "postgres"
	db, err := sql.Open("pgx", inputURL)
	if err != nil {
		t.Fatalf("w16j: 打开契约缺失输入库失败: %v", err)
	}
	defer func() { _ = db.Close() }()
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer pingCancel()
	if err := db.PingContext(pingCtx); err != nil {
		t.Fatalf("w16j: 契约缺失输入库不可达: %v", err)
	}
	var one int
	if err := db.QueryRow(`SELECT 1 FROM juhe_business.accounts LIMIT 0`).Scan(&one); err == nil {
		t.Fatal("w16j: postgres 维护库意外包含业务契约面，无法注入契约失败臂")
	}
	return inputURL
}
