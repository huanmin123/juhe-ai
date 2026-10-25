package ipstats

// pgkit_test.go 是 ipstats 测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z SQLite 与内存模式彻底退役，测试尾巴批次）：
// pgtestkit 每测试独立库 + 生产形态（NewStore(db, ...)，client_ip_*/
// stats_job_state 落 juhe_stats、system_settings 落 juhe_business，与生产
// 装配同一 schema 限定 + $n 占位符）。
//
// 分组处置（D9/D17）：组A（被测对象有 PG 臂且断言逐字节保持）——HTTP/store
// 级测试走本文件夹具，断言逐字节保持；夹具 schema 列集与原 sqlite 夹具同一
// 子集，类型照抄 maintenance 生产 DDL（pg_schema_stats.go /
// pg_schema_business_tables.go）；FK 按 groups 夹具先例放宽。行为性约束逐字
// 保留（idx_client_ip_policies_active_unique 部分唯一索引）。
//
// 连接串由 pgtestkit 自 .local dev env 解析，内容不进入任何文件、日志或断言。

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// ipstatsPGFixtureSchema 是 ipstats 面测试的 PG schema（juhe_stats + 管理面
// system 表）。
const ipstatsPGFixtureSchema = `
	CREATE SCHEMA IF NOT EXISTS juhe_business;
	CREATE SCHEMA IF NOT EXISTS juhe_stats;
	CREATE TABLE IF NOT EXISTS juhe_business.system_accounts (
      id text PRIMARY KEY,
      username text NOT NULL UNIQUE,
      display_name text NOT NULL,
      description text,
      role text NOT NULL DEFAULT 'user',
      status text NOT NULL DEFAULT 'active',
      password_hash text NOT NULL,
      must_change_password integer NOT NULL DEFAULT 0,
      image_generation_enabled integer NOT NULL DEFAULT 0,
      ai_account_limit integer CHECK (ai_account_limit BETWEEN 0 AND 1000000),
      expedited_account_limit integer CHECK (expedited_account_limit BETWEEN 0 AND 100),
      request_limits_json text,
      last_login_at text,
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.system_sessions (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      token_hash text NOT NULL UNIQUE,
      expires_at text NOT NULL,
      created_at text NOT NULL,
      last_seen_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.system_settings (
      system_account_id text NOT NULL,
      key text NOT NULL,
      value_json text NOT NULL,
      updated_at text NOT NULL,
      PRIMARY KEY (system_account_id, key)
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
	CREATE TABLE IF NOT EXISTS juhe_stats.client_ip_usage_range_windows (
      ip_hash text NOT NULL,
      start_date text NOT NULL,
      end_date text NOT NULL,
      request_count bigint NOT NULL DEFAULT 0,
      success_count bigint NOT NULL DEFAULT 0,
      error_count bigint NOT NULL DEFAULT 0,
      input_tokens bigint NOT NULL DEFAULT 0,
      output_tokens bigint NOT NULL DEFAULT 0,
      cache_read_tokens bigint NOT NULL DEFAULT 0,
      cache_read_cost_usd double precision NOT NULL DEFAULT 0,
      cache_write_tokens bigint NOT NULL DEFAULT 0,
      cache_write_1h_tokens bigint NOT NULL DEFAULT 0,
      cache_write_cost_usd double precision NOT NULL DEFAULT 0,
      thinking_tokens bigint NOT NULL DEFAULT 0,
      input_image_tokens bigint NOT NULL DEFAULT 0,
      output_image_tokens bigint NOT NULL DEFAULT 0,
      total_cost_usd double precision NOT NULL DEFAULT 0,
      duration_ms_sum bigint NOT NULL DEFAULT 0,
      duration_ms_count bigint NOT NULL DEFAULT 0,
      duration_ms_max bigint NOT NULL DEFAULT 0,
      average_duration_ms double precision,
      first_token_ms_sum bigint NOT NULL DEFAULT 0,
      first_token_ms_count bigint NOT NULL DEFAULT 0,
      average_first_token_ms double precision,
      active_days integer NOT NULL DEFAULT 0,
      last_used_at text,
      last_error_at text,
      updated_at text NOT NULL,
      PRIMARY KEY (ip_hash, start_date, end_date)
    );
	CREATE TABLE IF NOT EXISTS juhe_stats.client_ip_range_window_dirty_ips (
      ip_hash text PRIMARY KEY,
      generation bigint NOT NULL DEFAULT 1,
      first_dirty_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_stats.client_ip_account_usage_range_windows (
      ip_hash text NOT NULL,
      account_id text NOT NULL,
      start_date text NOT NULL,
      end_date text NOT NULL,
      request_count bigint NOT NULL DEFAULT 0,
      success_count bigint NOT NULL DEFAULT 0,
      error_count bigint NOT NULL DEFAULT 0,
      input_tokens bigint NOT NULL DEFAULT 0,
      output_tokens bigint NOT NULL DEFAULT 0,
      cache_read_tokens bigint NOT NULL DEFAULT 0,
      cache_read_cost_usd double precision NOT NULL DEFAULT 0,
      cache_write_tokens bigint NOT NULL DEFAULT 0,
      cache_write_1h_tokens bigint NOT NULL DEFAULT 0,
      cache_write_cost_usd double precision NOT NULL DEFAULT 0,
      thinking_tokens bigint NOT NULL DEFAULT 0,
      input_image_tokens bigint NOT NULL DEFAULT 0,
      output_image_tokens bigint NOT NULL DEFAULT 0,
      total_cost_usd double precision NOT NULL DEFAULT 0,
      duration_ms_sum bigint NOT NULL DEFAULT 0,
      duration_ms_count bigint NOT NULL DEFAULT 0,
      duration_ms_max bigint NOT NULL DEFAULT 0,
      average_duration_ms double precision,
      first_token_ms_sum bigint NOT NULL DEFAULT 0,
      first_token_ms_count bigint NOT NULL DEFAULT 0,
      average_first_token_ms double precision,
      active_days integer NOT NULL DEFAULT 0,
      last_used_at text,
      last_error_at text,
      updated_at text NOT NULL,
      PRIMARY KEY (ip_hash, account_id, start_date, end_date)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.accounts (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      name text NOT NULL,
      vendor_code text NOT NULL DEFAULT 'gpt',
      account_expires_at text
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
	CREATE TABLE IF NOT EXISTS juhe_stats.client_ip_policies (
      id text PRIMARY KEY,
      ip_hash text NOT NULL,
      policy_type text NOT NULL,
      status text NOT NULL,
      reason text,
      expires_at text,
      created_by_system_account_id text NOT NULL,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      disabled_at text,
      disabled_by_system_account_id text,
      disabled_reason text
    );
	CREATE UNIQUE INDEX IF NOT EXISTS idx_client_ip_policies_active_unique
      ON juhe_stats.client_ip_policies (ip_hash) WHERE status = 'active'
`

// newIPStatsPGDatabase 创建 pgtestkit 隔离 PG 测试库、注入夹具 schema，并返回
// 带 juhe_business,juhe_stats search_path 的连接池（夹具/测试手写 SQL 的裸表
// 名由 search_path 解析；生产 store 的 PG 臂本就 s.table 全限定）。
func newIPStatsPGDatabase(t *testing.T) *sql.DB {
	t.Helper()
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, ipstatsPGFixtureSchema)
		return err
	})
	_ = testDB.DB.Close()
	pool, err := sql.Open("pgx", testDB.DSN+"&search_path=juhe_business,juhe_stats")
	if err != nil {
		t.Fatalf("pgtestkit: 打开夹具连接池失败: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

// fixtureBind 把夹具/测试手写 SQL 的 ? 占位符按序改写为 $n（与生产 Store.bind
// 同一惯例；夹具 SQL 的字符串字面量不含 ?，计数不受字面量干扰）。
func fixtureBind(query string) string {
	if !strings.Contains(query, "?") {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + strconv.Itoa(index))
			index++
			continue
		}
		out.WriteByte(query[i])
	}
	return out.String()
}
