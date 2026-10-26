package statsverify

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// w10c_pg_gate_test.go 覆盖 statsverify 的 PostgreSQL 专有分支（分组统计刷新
// 与 client-ip 聚合批处理）。建库走 pgtestkit 每测试独立 fresh 库（原
// JUHE_AI_W10CSV_PG_URL 显式 opt-in 的 w1cover scratch 库装配已退役）：
// EnsureSchema 契约是只校验不建表（外部 bootstrap 责任），prepare 按
// maintenance 生产 PG schema 最小同构形状预置 postgresRequiredTables 全集；
// PG 不可达由 pgtestkit 显式 Fatal 而非 t.Skip。连接串永不进入日志或断言。

// w10cSvFixtureDDL 是 fresh 库预置 DDL。被测路径实际消费的表
// （system_settings / groups / group_accounts / accounts /
// resource_authorizations / group_account_stats_dirty / group_account_stats /
// stats_job_state / usage_records）按生产列集同构；其余
// postgresRequiredTables 成员在本测试只做存在性校验（EnsureSchema），按
// 存在性最小形状建（缺列会被真实 SQL 大声失败，不静默）。
const w10cSvFixtureDDL = `
CREATE SCHEMA IF NOT EXISTS juhe_usage;
CREATE SCHEMA IF NOT EXISTS juhe_stats;
CREATE SCHEMA IF NOT EXISTS juhe_business;
CREATE TABLE IF NOT EXISTS juhe_usage.usage_records (
  id text NOT NULL,
  traffic_source text,
  client_ip text,
  account_id text,
  success integer,
  first_token_ms integer,
  duration_ms integer,
  input_tokens bigint,
  output_tokens bigint,
  cache_read_tokens bigint,
  cache_read_cost_usd double precision,
  cache_write_tokens bigint,
  cache_write_1h_tokens bigint,
  cache_write_cost_usd double precision,
  thinking_tokens bigint,
  input_image_tokens integer,
  output_image_tokens integer,
  cost_usd double precision,
  created_at text NOT NULL,
  PRIMARY KEY (created_at, id)
);
CREATE TABLE IF NOT EXISTS juhe_stats.stats_job_state (
  scope_type text NOT NULL,
  scope_id text NOT NULL DEFAULT '',
  job_name text NOT NULL,
  cursor_created_at text,
  cursor_id text,
  last_success_at text,
  last_error_message text,
  lag_seconds integer,
  updated_at text NOT NULL,
  PRIMARY KEY (scope_type, scope_id, job_name)
);
CREATE TABLE IF NOT EXISTS juhe_stats.client_ip_registry (
  ip_hash text PRIMARY KEY,
  bucket_no integer NOT NULL,
  aggregate_ip_key text NOT NULL,
  client_ip text NOT NULL,
  ip_version integer NOT NULL,
  first_seen_at text NOT NULL,
  last_seen_at text NOT NULL,
  created_at text NOT NULL,
  updated_at text NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_stats.client_ip_stats_daily (
  ip_hash text NOT NULL,
  stat_date text NOT NULL,
  updated_at text NOT NULL,
  PRIMARY KEY (ip_hash, stat_date)
);
CREATE TABLE IF NOT EXISTS juhe_stats.client_ip_account_stats_daily (
  ip_hash text NOT NULL,
  account_id text NOT NULL,
  stat_date text NOT NULL,
  updated_at text NOT NULL,
  PRIMARY KEY (ip_hash, account_id, stat_date)
);
CREATE TABLE IF NOT EXISTS juhe_stats.client_ip_usage_range_windows (
  ip_hash text NOT NULL,
  start_date text NOT NULL,
  end_date text NOT NULL,
  updated_at text NOT NULL,
  PRIMARY KEY (ip_hash, start_date, end_date)
);
CREATE TABLE IF NOT EXISTS juhe_stats.client_ip_account_usage_range_windows (
  ip_hash text NOT NULL,
  account_id text NOT NULL,
  start_date text NOT NULL,
  end_date text NOT NULL,
  updated_at text NOT NULL,
  PRIMARY KEY (ip_hash, account_id, start_date, end_date)
);
CREATE TABLE IF NOT EXISTS juhe_stats.client_ip_range_window_dirty_ips (
  ip_hash text PRIMARY KEY,
  generation bigint NOT NULL DEFAULT 1,
  first_dirty_at text NOT NULL,
  updated_at text NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_stats.client_ip_account_range_window_dirty_ips (
  ip_hash text PRIMARY KEY,
  generation bigint NOT NULL DEFAULT 1,
  first_dirty_at text NOT NULL,
  updated_at text NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_stats.group_account_stats (
  system_account_id text NOT NULL,
  group_id text NOT NULL,
  total integer NOT NULL DEFAULT 0,
  available integer NOT NULL DEFAULT 0,
  active integer NOT NULL DEFAULT 0,
  disabled integer NOT NULL DEFAULT 0,
  error integer NOT NULL DEFAULT 0,
  rate_limited integer NOT NULL DEFAULT 0,
  current_concurrency integer NOT NULL DEFAULT 0,
  concurrency_limit integer NOT NULL DEFAULT 0,
  updated_at text NOT NULL,
  PRIMARY KEY (system_account_id, group_id)
);
CREATE TABLE IF NOT EXISTS juhe_stats.usage_stats_daily (
  system_account_id text NOT NULL,
  scope_type text NOT NULL,
  scope_id text NOT NULL DEFAULT '',
  stat_date text NOT NULL,
  updated_at text NOT NULL,
  PRIMARY KEY (system_account_id, scope_type, scope_id, stat_date)
);
CREATE TABLE IF NOT EXISTS juhe_stats.usage_stats_hourly (
  system_account_id text NOT NULL,
  scope_type text NOT NULL,
  scope_id text NOT NULL DEFAULT '',
  stat_hour text NOT NULL,
  updated_at text NOT NULL,
  PRIMARY KEY (system_account_id, scope_type, scope_id, stat_hour)
);
CREATE TABLE IF NOT EXISTS juhe_business.system_settings (
  system_account_id text NOT NULL,
  key text NOT NULL,
  value_json text NOT NULL,
  updated_at text NOT NULL,
  PRIMARY KEY (system_account_id, key)
);
CREATE TABLE IF NOT EXISTS juhe_business.groups (
  id text PRIMARY KEY,
  system_account_id text NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_business.group_accounts (
  group_id text NOT NULL,
  account_id text NOT NULL,
  account_authorization_id text,
  enabled integer NOT NULL DEFAULT 1,
  PRIMARY KEY (group_id, account_id)
);
CREATE TABLE IF NOT EXISTS juhe_business.accounts (
  id text PRIMARY KEY,
  system_account_id text,
  status text,
  schedulable integer,
  cooldown_until text,
  concurrency_limit integer,
  deleted_at text
);
CREATE TABLE IF NOT EXISTS juhe_business.resource_authorizations (
  id text PRIMARY KEY,
  status text,
  expires_at text
);
CREATE TABLE IF NOT EXISTS juhe_business.group_account_stats_dirty (
  group_id text PRIMARY KEY,
  reason text,
  updated_at text NOT NULL
);
`

// w10cSvOpenKitStore 打开 pgtestkit 隔离 PG 测试库（预置 w10cSvFixtureDDL）
// 并经生产构造装配 ModePostgres store；测试结束自动关闭并删库。
func w10cSvOpenKitStore(t *testing.T) (*Store, string) {
	t.Helper()
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, w10cSvFixtureDDL)
		return err
	})
	store, err := OpenStore(StoreConfig{
		Mode:                 StorePostgres,
		PostgresURL:          testDB.DSN,
		PostgresMaxOpenConns: 4,
		PostgresMaxIdleConns: 2,
	})
	if err != nil {
		t.Fatalf("隔离 PG 测试库 OpenStore 失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, testDB.Name
}

func TestW10CPGStoreGroupStatsAndClientIP(t *testing.T) {
	ctx := context.Background()
	store, fixtureDatabase := w10cSvOpenKitStore(t)
	if store.mode != StorePostgres {
		t.Fatalf("模式应为 postgres")
	}
	var currentDatabase string
	if err := store.db.QueryRowContext(ctx, "SELECT current_database()").Scan(&currentDatabase); err != nil {
		t.Fatalf("PG 读取 current_database 失败: %v", err)
	}
	if currentDatabase != fixtureDatabase {
		t.Fatalf("PG current_database=%q，要求为隔离夹具库 %q", currentDatabase, fixtureDatabase)
	}

	// checkPostgresSchema：确认 fresh 库具备外部预置表（prepare 已按生产形状建齐）。
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("PG EnsureSchema 失败: %v", err)
	}

	// 时区行（client-ip 聚合空批次需要 LoadUsageStatsLocation）。
	mustExec(t, ctx, store.db, `INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
		VALUES ('sys_admin', 'usageStatsTimezone', $1, $2) ON CONFLICT (system_account_id, key) DO UPDATE SET value_json = EXCLUDED.value_json`,
		mustJSONString("UTC"), NowIso(time.Now()))

	// MarkAllGroupAccountStatsDirty Postgres 臂。
	if err := store.MarkAllGroupAccountStatsDirty(ctx, "w10cpgsv-initial", time.Now()); err != nil {
		t.Fatalf("PG MarkAllGroupAccountStatsDirty 失败: %v", err)
	}

	// RefreshDirtyGroupAccountStats Postgres 包装（refreshDirtyGroupAccountStatsPostgres
	// 0% → 全臂；空 __all__ 分批消费路径，清理脏行）。
	if _, err := store.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: time.Now()}); err != nil {
		t.Fatalf("PG RefreshDirtyGroupAccountStats 失败: %v", err)
	}
	// 消费后 __all__ 脏行应已被删除。
	var dirtyCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_business.group_account_stats_dirty WHERE reason LIKE 'w10cpgsv%'`).Scan(&dirtyCount); err != nil {
		t.Fatal(err)
	}
	if dirtyCount != 0 {
		t.Fatalf("PG 脏行应被消费删除, count=%d", dirtyCount)
	}

	// AggregateClientIPStatsBatch Postgres 路径（beginWriteTx PG + client-ip
	// 聚合 PG 批处理）。fresh 库 usage_records 为空：只验证空批次游标推进与
	// PG 批处理无错。
	if _, err := store.AggregateClientIPStatsBatch(ctx, 10, time.Now()); err != nil {
		t.Fatalf("PG AggregateClientIPStatsBatch 失败: %v", err)
	}
}
