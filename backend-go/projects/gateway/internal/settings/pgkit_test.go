package settings

// pgkit_test.go 是 settings 测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z SQLite 与内存模式彻底退役，测试尾巴批次）：
// pgtestkit 每测试独立库 + 生产 PG 形态（NewStore(db, ...)，juhe_business
// schema 限定 + $n 占位符，与 gateway 组合根 settings.NewStore(db, ...)
// （恒 PG）同一装配形态）。
//
// 分组处置（D9/D17）：
//   - 组A（被测对象有 PG 臂且断言逐字节保持）：HTTP/store 级测试走本文件夹具，
//     断言逐字节保持；夹具 seed/校验手写 SQL 经 fixtureSQL 对齐生产 PG 臂
//     （juhe_business 限定 + $n）。schema 列集与原 sqlite 夹具同一子集，类型
//     对齐 maintenance 生产 DDL（pg_schema_business_tables.go）；FK 按 groups
//     夹具先例放宽（system_settings 的 system_account_id 外键不建，seed 行
//     'sys_admin' 不满足生产外键链）。
//   - SQLite 专属臂（时区在线守卫的统计探针路径、六库分拆回归、
//     usageStatsDataExists 探针臂）：生产 PG 模式在触 SQL 前即拒绝在线时区
//     变更、从不探针统计库，无 PG 对应物，按 D9 删除（见 settings_test.go
//     退役登记）。
//
// 连接串由 pgtestkit 自 .local dev env 解析，内容不进入任何文件、日志或断言。

import (
	"context"
	"database/sql"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// settingsPGFixtureSchema 是 settings 面测试的 PG schema。列集与原 sqlite
// 夹具同一子集，类型照抄 maintenance 生产 DDL（pg_schema_business_tables.go）；
// FK 放宽与约束保留理由见文件头注释。
const settingsPGFixtureSchema = `
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
	CREATE TABLE IF NOT EXISTS juhe_business.system_settings (
      system_account_id text NOT NULL,
      key text NOT NULL,
      value_json text NOT NULL,
      updated_at text NOT NULL,
      PRIMARY KEY (system_account_id, key)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.global_settings (
      key text PRIMARY KEY,
      value_json text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.accounts (
      id text PRIMARY KEY,
      config_revision integer NOT NULL DEFAULT 1,
      dispatch_revision bigint NOT NULL DEFAULT 1 CHECK (dispatch_revision >= 1),
      vendor_code text,
      connection_driver_code text,
      type text NOT NULL,
      deleted_at text
    );
	CREATE TABLE IF NOT EXISTS juhe_business.account_health_jobs_input_versions (
      account_id text PRIMARY KEY,
      current_version integer NOT NULL CHECK (current_version >= 1),
      reserved_at text NOT NULL
    );
`

// fixtureTablePattern 是夹具手写 seed/校验 SQL 的裸表名改写（与生产 PG 臂
// s.table 的 juhe_business. 限定同形式）。同前缀族按长名优先排列。
var fixtureTablePattern = regexp.MustCompile(`\b(FROM|INTO|UPDATE|JOIN)\s+(account_health_jobs_input_versions|system_settings|global_settings|system_sessions|system_accounts|usage_stats_totals|accounts)\b`)

// fixtureBind 把夹具手写 SQL 的 ? 占位符按序改写为 $n（与生产 Store.bind 同一
// 惯例；夹具 SQL 的字符串字面量不含 ?，计数不受字面量干扰）。
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

// fixtureSQL 把夹具手写 SQL 对齐生产 PG 臂方言（juhe_business 限定 + $n）。
func fixtureSQL(query string) string {
	return fixtureBind(fixtureTablePattern.ReplaceAllString(query, "$1 juhe_business.$2"))
}

// newSettingsPGDatabase 创建 pgtestkit 隔离 PG 测试库、注入 settings 夹具
// schema，并返回带 juhe_business search_path 的连接池（夹具/测试手写 SQL 的
// 裸表名由 search_path 解析；生产 store 的 PG 臂本就 s.table 全限定）。
// pgtestkit 自带连接池先行关闭，测试结束统一由 pgtestkit 删库。
func newSettingsPGDatabase(t *testing.T) *sql.DB {
	t.Helper()
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, settingsPGFixtureSchema)
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
