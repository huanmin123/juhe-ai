package schedulingpreferences

// pgkit_test.go 是 schedulingpreferences 测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z SQLite 与内存模式彻底退役，测试尾巴批次）：
// pgtestkit 每测试独立库 + 生产 PG 臂（NewStore(db, true, ...)，juhe_business
// schema 限定 + $n 占位符，与 gateway 组合根恒 PG 的装配同一方言）。
//
// 分组处置（D9/D17）：组A（被测对象有 PG 臂且断言逐字节保持）——HTTP/store
// 级测试走本文件夹具，断言逐字节保持；夹具 schema 与 maintenance 生产 DDL
// 对齐（pg_schema_business_tables.go 的 scheduling_preferences 全列与行为性
// CHECK、三个部分唯一索引），FK 按 groups 夹具先例放宽。
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

// schedulingPreferencesPGFixtureSchema 是 schedulingpreferences 面测试的 PG
// schema。生产部分唯一索引同名创建（重名 409 映射依赖约束名）。
const schedulingPreferencesPGFixtureSchema = `
	CREATE SCHEMA IF NOT EXISTS juhe_business;
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
	CREATE TABLE IF NOT EXISTS juhe_business.scheduling_preferences (
      id text PRIMARY KEY,
      system_account_id text,
      name text NOT NULL,
      chat_mode text NOT NULL CHECK (chat_mode IN ('cost_first', 'speed_first')),
      chat_speed_first_config_json text,
      built_in integer NOT NULL DEFAULT 0 CHECK (built_in IN (0, 1)),
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE UNIQUE INDEX IF NOT EXISTS idx_scheduling_preferences_owner_name_unique
      ON juhe_business.scheduling_preferences (system_account_id, name) WHERE built_in = 0;
	CREATE UNIQUE INDEX IF NOT EXISTS idx_scheduling_preferences_owner_name_unique_lower
      ON juhe_business.scheduling_preferences (system_account_id, lower(name)) WHERE built_in = 0;
	CREATE UNIQUE INDEX IF NOT EXISTS idx_scheduling_preferences_builtin_id_unique
      ON juhe_business.scheduling_preferences (id) WHERE built_in = 1;
	CREATE INDEX IF NOT EXISTS idx_scheduling_preferences_owner
      ON juhe_business.scheduling_preferences (system_account_id);
	CREATE TABLE IF NOT EXISTS juhe_business.api_keys (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      route_strategy_id text NOT NULL,
      name text NOT NULL,
      status text NOT NULL DEFAULT 'active',
      scheduling_preference_id text,
      created_at text NOT NULL,
      updated_at text NOT NULL
    )
`

// newSchedulingPreferencesPGDatabase 创建 pgtestkit 隔离 PG 测试库、注入夹具
// schema，并返回带 juhe_business search_path 的连接池（夹具/测试手写 SQL 的
// 裸表名由 search_path 解析；生产 store 的 PG 臂本就 s.table 全限定）。
func newSchedulingPreferencesPGDatabase(t *testing.T) *sql.DB {
	t.Helper()
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, schedulingPreferencesPGFixtureSchema)
		return err
	})
	_ = testDB.DB.Close()
	pool, err := sql.Open("pgx", testDB.DSN+"&search_path=juhe_business")
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
