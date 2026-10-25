package systemteams

// pgkit_test.go 是 systemteams 测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z SQLite 与内存模式彻底退役，测试尾巴批次）：
// pgtestkit 每测试独立库 + 生产 PG 形态（NewStore / authz.NewStore，前者已
// 去 postgres 参数，juhe_business schema 限定 + $n 占位符，与 gateway 组合根
// 恒 PG 的装配同一方言）。
//
// 分组处置（D9/D17）：组A（被测对象有 PG 臂且断言逐字节保持）——HTTP/store
// 级测试走本文件夹具，断言逐字节保持；夹具 schema 列集与原 sqlite 夹具同一
// 子集，类型对齐 maintenance 生产 DDL（pg_schema_business_tables.go），FK 按
// groups 夹具先例放宽。行为性约束逐字保留：system_teams.name UNIQUE（重名
// →「团队名称已存在」映射同时匹配 UNIQUE constraint failed / duplicate key
// 文本，PG 臂依赖 duplicate key 路径）、resource_authorization_grants 的
// grantee_type CHECK、quota 绑定主键。
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

// systemTeamsPGFixtureSchema 是 systemteams/authz 面测试的 PG schema。
const systemTeamsPGFixtureSchema = `
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
	CREATE TABLE IF NOT EXISTS juhe_business.system_teams (
      id text PRIMARY KEY,
      name text NOT NULL UNIQUE,
      description text,
      status text NOT NULL DEFAULT 'active',
      created_by text NOT NULL,
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.system_team_members (
      id text PRIMARY KEY,
      team_id text NOT NULL,
      system_account_id text NOT NULL,
      member_role text NOT NULL DEFAULT 'member',
      status text NOT NULL DEFAULT 'active',
      joined_at text NOT NULL,
      removed_at text,
      created_by text NOT NULL DEFAULT '',
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.groups (
      id text PRIMARY KEY,
      name text NOT NULL,
      system_account_id text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.resource_authorizations (
      id text PRIMARY KEY,
      resource_type text NOT NULL,
      resource_id text NOT NULL,
      resource_owner_system_account_id text NOT NULL,
      grantee_system_account_id text NOT NULL,
      scope text NOT NULL DEFAULT 'use',
      status text NOT NULL DEFAULT 'active',
      effective_source_type text,
      effective_source_team_id text,
      activated_at text,
      last_source_changed_at text,
      remark text,
      expires_at text,
      limits_json text,
      created_by text NOT NULL,
      created_at text NOT NULL,
      revoked_by text,
      revoked_at text,
      revoked_reason text,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.resource_authorization_sources (
      id text PRIMARY KEY,
      authorization_id text NOT NULL,
      source_type text NOT NULL,
      source_team_id text,
      status text NOT NULL DEFAULT 'active',
      activated_at text,
      ended_at text,
      ended_reason text,
      created_by text NOT NULL,
      created_at text NOT NULL,
      revoked_by text,
      revoked_at text,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.resource_authorization_grants (
      id text PRIMARY KEY,
      resource_type text NOT NULL,
      resource_id text NOT NULL,
      resource_owner_system_account_id text NOT NULL,
      grantee_type text NOT NULL,
      grantee_system_account_id text,
      grantee_team_id text,
      scope text NOT NULL DEFAULT 'use',
      status text NOT NULL DEFAULT 'active',
      remark text,
      expires_at text,
      limits_json text,
      created_by text NOT NULL,
      created_at text NOT NULL,
      revoked_by text,
      revoked_at text,
      updated_at text NOT NULL,
      CHECK (
        (grantee_type = 'system_account' AND grantee_system_account_id IS NOT NULL AND grantee_team_id IS NULL)
        OR
        (grantee_type = 'team' AND grantee_team_id IS NOT NULL AND grantee_system_account_id IS NULL)
      )
    );
	CREATE TABLE IF NOT EXISTS juhe_business.accounts (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      deleted_at text,
      authorization_instance_authorization_id text,
      authorization_instance_source_account_id text
    );
	CREATE TABLE IF NOT EXISTS juhe_business.request_quota_hourly_window_scope_bindings (
      system_account_id text NOT NULL,
      scope_type text NOT NULL,
      scope_id text NOT NULL,
      source_type text NOT NULL,
      source_id text NOT NULL,
      window_hours integer NOT NULL,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      PRIMARY KEY (system_account_id, scope_type, scope_id)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.group_account_stats_dirty (
      group_id text PRIMARY KEY,
      reason text NOT NULL,
      updated_at text NOT NULL
    )
`

// newSystemTeamsPGDatabase 创建 pgtestkit 隔离 PG 测试库、注入夹具 schema，
// 并返回带 juhe_business search_path 的连接池（夹具/测试手写 SQL 的裸表名由
// search_path 解析；生产 store 的 PG 臂本就 s.table 全限定）。
func newSystemTeamsPGDatabase(t *testing.T) *sql.DB {
	t.Helper()
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, systemTeamsPGFixtureSchema)
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
