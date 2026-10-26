package apikeys

// pgkit_test.go 是 apikeys 测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z SQLite 与内存模式彻底退役，apikeys 测试迁移切片）：
// pgtestkit 每测试独立库 + 生产 store 的 PG 臂（NewStore postgres=true，
// juhe_business/juhe_dataset schema 限定 + $n 占位符，与生产 PG 装配同一方言）。
//
// 三组分类（D9/D17）：
//   - 组A（被测对象有 PG 臂且断言可逐字节保持）：HTTP/store 级测试走本文件
//     newAPIKeysPGDatabase 夹具，测试体断言逐字节保持；夹具 seed 手写 SQL 与
//     测试体直连 db 的手写 SQL 经 fixtureBind 对齐生产 PG 臂（$n；裸表名由
//     连接级 search_path=juhe_business,juhe_dataset 解析，生产 store 的 PG 臂
//     本就 s.table 全限定）。schema 与 maintenance 生产 DDL 对齐
//     （pg_schema_business_tables.go / pg_schema_dataset.go），按 groups 夹具
//     先例放宽：不建 FOREIGN KEY（store 删除级联为显式 DELETE，seed 行不满足
//     生产外键链）；行为性约束逐字保留（idx_api_keys_owner_name_unique 重名
//     409 映射依赖该约束名、purpose/chat_mode/window_hours 生产 CHECK）。
//     api_key_record_cleanup_targets 按生产 datasetTable 形态落 juhe_dataset。
//   - 组B（SQLite-only 生产臂为被测对象）：bug0161_test.go 的 SQLite 删除臂
//     cleanup 目标放置/未接 dataset 句柄契约、usage_test.go 与
//     w11a_apikeys_extra_test.go / w11a_apikeys_store_direct_test.go 的
//     StatsUsageSource sqlite 臂与读进程降级段、quota_hourly_dirty_test.go 的
//     SQLite 模式不打脏臂——按 D17 不提前迁移也不提前删除，走本文件
//     newSQLiteTestEnv 或各文件自带的内存 SQLite 夹具，随生产 sqlite 臂删除
//     批次同批退役。
//   - scripted 假驱动/内存 fake（recordingSubmitter / fakeUsageSource /
//     CacheInvalidator 包装桩）不依赖真库，保持原状。
//
// 连接串由 pgtestkit 自 .local dev env 解析，内容不进入任何文件、日志或断言。

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// apiKeysPGFixtureSchema 是 apikeys/authsys/businessauth 面测试的 PG schema。
// 列集与类型照抄 maintenance 生产 DDL（pg_schema_business_tables.go /
// pg_schema_dataset.go），FK 按夹具先例放宽（见文件头注释）；生产索引
// idx_api_keys_owner_name_unique 必须同名创建——duplicateAPIKeyNameError 的
// PG 臂依赖报错文本中的约束名映射 409。
const apiKeysPGFixtureSchema = `
	CREATE SCHEMA IF NOT EXISTS juhe_business;
	CREATE SCHEMA IF NOT EXISTS juhe_dataset;
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
	CREATE TABLE IF NOT EXISTS juhe_business.groups (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      name text NOT NULL,
      description text,
      enabled integer NOT NULL DEFAULT 1,
      is_default integer NOT NULL DEFAULT 0,
      group_type text NOT NULL DEFAULT 'personal',
      group_kind text NOT NULL DEFAULT 'standard' CHECK (group_kind IN ('standard','super')),
      scheduling_policy_json text,
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.route_strategies (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      name text NOT NULL,
      description text,
      mode text NOT NULL DEFAULT 'normal',
      status text NOT NULL DEFAULT 'active',
      is_default integer NOT NULL DEFAULT 0,
      config_json text,
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.route_strategy_groups (
      id text PRIMARY KEY,
      route_strategy_id text NOT NULL,
      system_account_id text NOT NULL,
      group_id text NOT NULL,
      priority integer NOT NULL DEFAULT 1,
      weight integer NOT NULL DEFAULT 1,
      status text NOT NULL DEFAULT 'active',
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.api_keys (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      route_strategy_id text NOT NULL,
      name text NOT NULL,
      description text,
      key_hash text NOT NULL UNIQUE,
      key_prefix text NOT NULL,
      key_suffix text NOT NULL,
      key_secret_encrypted text NOT NULL,
      status text NOT NULL DEFAULT 'active',
      is_default integer NOT NULL DEFAULT 0,
      purpose text NOT NULL DEFAULT 'general' CHECK (purpose IN ('general', 'chat', 'studio')),
      expires_at text,
      quota_limits_json text,
      availability_schedule_json text,
      availability_schedule_next_check_at text,
      scheduling_preference_id text,
      last_used_at text,
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_owner_name_unique
      ON juhe_business.api_keys (system_account_id, name);
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
	CREATE TABLE IF NOT EXISTS juhe_business.request_quota_hourly_window_scope_bindings (
      system_account_id text NOT NULL,
      scope_type text NOT NULL,
      scope_id text NOT NULL,
      source_type text NOT NULL,
      source_id text NOT NULL,
      window_hours integer NOT NULL,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      CHECK (window_hours BETWEEN 1 AND 720),
      CHECK (scope_type IN ('api_key', 'account_authorization', 'group_authorization', 'account_authorization_team', 'group_authorization_team')),
      CHECK (source_type IN ('api_key', 'resource_authorization_grant')),
      PRIMARY KEY (system_account_id, scope_type, scope_id),
      UNIQUE (source_type, source_id, system_account_id, scope_type, scope_id)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.system_settings (
      system_account_id text NOT NULL,
      key text NOT NULL,
      value_json text NOT NULL,
      updated_at text NOT NULL,
      PRIMARY KEY (system_account_id, key)
    );
	CREATE TABLE IF NOT EXISTS juhe_dataset.api_key_record_cleanup_targets (
      api_key_id text PRIMARY KEY,
      system_account_id text NOT NULL,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      attempt_count integer NOT NULL DEFAULT 0,
      last_attempt_at text,
      last_blocked_reason text,
      last_error_message text
    );
`

// newAPIKeysPGDatabase 创建 pgtestkit 隔离 PG 测试库、注入 apikeys 夹具 schema，
// 并返回带 juhe_business,juhe_dataset search_path 的连接池（夹具/测试手写
// SQL 的裸表名由 search_path 解析；生产 store 的 PG 臂本就全限定名）。
// pgtestkit 自带连接池先行关闭，测试结束统一由 pgtestkit 删库。
func newAPIKeysPGDatabase(t *testing.T) *sql.DB {
	t.Helper()
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, apiKeysPGFixtureSchema)
		return err
	})
	_ = testDB.DB.Close()
	pool, err := sql.Open("pgx", testDB.DSN+"&search_path=juhe_business,juhe_dataset")
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
			out.WriteString("$" + itoa(index))
			index++
			continue
		}
		out.WriteByte(query[i])
	}
	return out.String()
}

// sqliteFixtureSchema 是组B SQLite-only 生产臂测试的业务 schema（原共享夹具
// DDL 原样保留，随生产 sqlite 臂删除批次同批退役）。
var sqliteFixtureSchema = []string{
	`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER, expedited_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS groups (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT, enabled INTEGER NOT NULL DEFAULT 1, is_default INTEGER NOT NULL DEFAULT 0, group_type TEXT NOT NULL DEFAULT 'personal', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS route_strategies (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, mode TEXT NOT NULL DEFAULT 'normal', status TEXT NOT NULL DEFAULT 'active', is_default INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS route_strategy_groups (id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS api_keys (
		id TEXT PRIMARY KEY,
		system_account_id TEXT NOT NULL,
		route_strategy_id TEXT NOT NULL,
		name TEXT NOT NULL,
		description TEXT,
		key_hash TEXT NOT NULL UNIQUE,
		key_prefix TEXT NOT NULL,
		key_suffix TEXT NOT NULL,
		key_secret_encrypted TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active',
		is_default INTEGER NOT NULL DEFAULT 0,
		purpose TEXT NOT NULL DEFAULT 'general',
		expires_at TEXT,
		quota_limits_json TEXT,
		availability_schedule_json TEXT,
		availability_schedule_next_check_at TEXT,
		scheduling_preference_id TEXT,
		last_used_at TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	// 调度偏好独立实体 P1（api_keys.scheduling_preference_id 引用目标）。
	`CREATE TABLE IF NOT EXISTS scheduling_preferences (
		id TEXT PRIMARY KEY,
		system_account_id TEXT,
		name TEXT NOT NULL,
		chat_mode TEXT NOT NULL,
		chat_speed_first_config_json TEXT,
		built_in INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_owner_name_unique ON api_keys(system_account_id, name)`,
	`CREATE TABLE IF NOT EXISTS request_quota_hourly_window_scope_bindings (
		system_account_id TEXT NOT NULL,
		scope_type TEXT NOT NULL,
		scope_id TEXT NOT NULL,
		source_type TEXT NOT NULL,
		source_id TEXT NOT NULL,
		window_hours INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (system_account_id, scope_type, scope_id)
	)`,
	`CREATE TABLE IF NOT EXISTS api_key_record_cleanup_targets (
		api_key_id TEXT PRIMARY KEY,
		system_account_id TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		attempt_count INTEGER NOT NULL DEFAULT 0,
		last_attempt_at TEXT,
		last_blocked_reason TEXT,
		last_error_message TEXT
	)`,
}
