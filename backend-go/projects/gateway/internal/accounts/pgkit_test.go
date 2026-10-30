package accounts

// pgkit_test.go 是 accounts 测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z SQLite 与内存模式彻底退役，accounts 测试迁移切片）：
// pgtestkit 每测试独立库 + 生产 store（juhe_business/juhe_stats schema 限定
// + $n 占位符，与生产 PG 装配同一方言）。
//
// 三组分类（D9/D17）：
//   - 组A（被测对象有 PG 臂且断言可逐字节保持）：HTTP/store 级测试走本文件
//     newAccountsPGDatabase 夹具，测试体断言逐字节保持；夹具 seed 与测试体
//     直连 db 的手写 SQL 经 fixtureBind 对齐生产 PG 臂（$n；裸表名由连接级
//     search_path=juhe_business,juhe_stats 解析，生产 store 的 PG 臂本就
//     s.table 全限定）。夹具 DDL 由原共享夹具 schemaStatements 逐表对齐 PG
//     类型（bigint/integer/text + 生产 CHECK），FK 按 groups/statreads 夹具
//     先例放宽（store 删除级联为显式 DELETE，seed 行不满足生产外键链）；
//     行为性约束逐字保留（idx_accounts_owner_name_unique / idx_account_tags_owner_name_unique
//     重名 409 映射依赖约束名、accounts.capability_domain 与 lock/circuit
//     outbox 的生产 CHECK）。fresh-DDL 契约测试（b1_fresh_ddl_test.go /
//     bug0335_entry_mapping_fk_write_order_test.go）另走 bootstrap.EnsurePostgres
//     生产建表路径，外键语义真实生效。
//   - 组B（SQLite-only 生产臂为被测对象：statsdb_attach_test.go 双文件 stats
//     拆分回归、delete_authorization_sqlite_test.go 删除臂逐 grant 运行态同步域、
//     StatsUsageSource sqlite VALUES join、sqlite 表名/占位符渲染锁）与 w14l
//     sqlite 故障注入连接器族，已随生产 sqlite 臂删除批次同批退役（D17）。
//   - 内存 fake（w14bBoomConnector、fakeTestEffects / fakeUsageSource /
//     recordingSink / recordingInvalidator）不依赖真库，保持原状。
//
// 连接串由 pgtestkit 自 .local dev env 解析，内容不进入任何文件、日志或断言。

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// accountsPGFixtureSchema 是 accounts/authsys/businessauth/authz 面测试的 PG
// schema。列集与原共享夹具 schemaStatements 同一子集（maintenance 生产业务
// schema 的本包读写面），类型对齐生产 PG DDL（pg_schema_business_tables.go /
// pg_schema_stats.go：revision/generation/ms 列 bigint、其余 integer/text），
// account_usage_snapshots 按生产 stats 库形态落 juhe_stats。FK 放宽与约束
// 保留理由见文件头注释。
const accountsPGFixtureSchema = `
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
	CREATE TABLE IF NOT EXISTS juhe_business.providers (
      id text PRIMARY KEY,
      code text NOT NULL UNIQUE,
      name text NOT NULL,
      description text,
      parent_code text,
      enabled integer NOT NULL DEFAULT 1,
      default_supported_models_json text NOT NULL DEFAULT '[]',
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.provider_protocol_profiles (
      id text PRIMARY KEY,
      provider_code text NOT NULL,
      name text NOT NULL,
      description text,
      enabled integer NOT NULL DEFAULT 1,
      protocol_code text NOT NULL,
      protocol_version text NOT NULL,
      base_url text NOT NULL,
      default_health_check_model text NOT NULL,
      account_types_json text NOT NULL,
      capabilities_json text NOT NULL,
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.vendors (
      id text PRIMARY KEY,
      code text NOT NULL UNIQUE,
      name text NOT NULL,
      description text,
      enabled integer NOT NULL DEFAULT 1,
      default_supported_models_json text NOT NULL DEFAULT '[]',
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.proxy_profiles (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      name text NOT NULL,
      description text,
      type text NOT NULL,
      host text NOT NULL,
      port integer NOT NULL,
      username text,
      password_encrypted text,
      enabled boolean NOT NULL DEFAULT true,
      test_status text NOT NULL DEFAULT 'unknown',
      latency_ms integer,
      outbound_ip text,
      outbound_region text,
      last_test_message text,
      last_tested_at timestamptz,
      created_at timestamptz NOT NULL,
      updated_at timestamptz NOT NULL
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
	CREATE TABLE IF NOT EXISTS juhe_business.accounts (
      id text PRIMARY KEY,
      config_revision integer NOT NULL DEFAULT 1,
      dispatch_revision bigint NOT NULL DEFAULT 1 CHECK (dispatch_revision >= 1),
      circuit_projection_revision bigint NOT NULL DEFAULT 0 CHECK (circuit_projection_revision >= 0),
      system_account_id text NOT NULL,
      vendor_code text,
      connection_driver_code text,
      oauth_type text,
      protocol_code text NOT NULL,
      protocol_version text NOT NULL,
      name text NOT NULL,
      type text NOT NULL,
      status text NOT NULL DEFAULT 'pending_test',
      credentials_encrypted text NOT NULL,
      credential_fingerprint text,
      credential_mask text NOT NULL DEFAULT '',
      oauth_access_token_expires_at text,
      oauth_refresh_token_present integer NOT NULL DEFAULT 0,
      proxy_profile_id text,
      concurrency_limit integer NOT NULL DEFAULT 5000,
      priority integer NOT NULL DEFAULT 0,
      super_priority_enabled integer NOT NULL DEFAULT 0,
      fallback_enabled integer NOT NULL DEFAULT 0,
      schedulable integer NOT NULL DEFAULT 1,
      availability_schedule_json text,
      availability_schedule_next_check_at text,
      notes text,
      account_expires_at text,
      last_used_at text,
      cooldown_until text,
      last_error_code text,
      last_error_message text,
      last_error_trace_id text,
      health_check_model text NOT NULL DEFAULT '',
      health_check_endpoint_mode text NOT NULL DEFAULT 'chat_json',
      health_check_failure_count integer NOT NULL DEFAULT 0,
      health_check_failure_started_at text,
      cooldown_retest_failure_count integer NOT NULL DEFAULT 0,
      cooldown_retest_observation_started_at text,
      cooldown_retest_generation text,
      cooldown_retest_last_at text,
      cooldown_retest_last_status_code integer,
      background_probe_enabled integer NOT NULL DEFAULT 1 CHECK (background_probe_enabled IN (0, 1)),
      temporary_unavailable_continuous_probe_enabled integer NOT NULL DEFAULT 1 CHECK (temporary_unavailable_continuous_probe_enabled IN (0, 1)),
      expedited_recovery_enabled integer NOT NULL DEFAULT 0 CHECK (expedited_recovery_enabled IN (0, 1)),
      capability_domain text NOT NULL DEFAULT 'chat' CHECK (capability_domain IN ('chat','video','audio','image')),
      last_health_check_at text,
      last_health_success_at text,
      health_check_connection_id text,
      last_health_check_status_code integer,
      last_health_check_error_code text,
      last_health_check_error_message text,
      last_health_check_trace_id text,
      stream_failure_count integer NOT NULL DEFAULT 0,
      stream_failure_window_started_at text,
      next_health_check_at text,
      balance_query_enabled integer NOT NULL DEFAULT 0,
      balance_query_next_refresh_at text,
      balance_query_config_json text NOT NULL DEFAULT '{}',
      authorization_instance_source_account_id text,
      authorization_instance_authorization_id text,
      authorization_instance_owner_system_account_id text,
      deleted_at text,
      deleted_by text,
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_owner_name_unique
      ON juhe_business.accounts (system_account_id, name) WHERE deleted_at IS NULL;
	CREATE TABLE IF NOT EXISTS juhe_business.group_accounts (
      system_account_id text NOT NULL,
      group_id text NOT NULL,
      account_id text NOT NULL,
      account_authorization_id text,
      local_priority integer NOT NULL DEFAULT 0,
      local_super_priority_enabled integer NOT NULL DEFAULT 0,
      local_fallback_enabled integer NOT NULL DEFAULT 0,
      enabled integer NOT NULL DEFAULT 1,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      PRIMARY KEY (group_id, account_id)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.models (
      id text PRIMARY KEY,
      vendor_code text,
      public_model text NOT NULL,
      upstream_model text NOT NULL,
      scope text NOT NULL DEFAULT 'builtin',
      system_account_id text,
      status text NOT NULL DEFAULT 'active',
      supported_endpoint_modes_json text,
      release_date text,
      shutdown_date text,
      catalog_visible boolean NOT NULL DEFAULT true,
      catalog_order integer NOT NULL DEFAULT 0,
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.account_supported_models (
      account_id text NOT NULL,
      model_id text NOT NULL,
      created_at text NOT NULL,
      PRIMARY KEY (account_id, model_id)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.account_connections (
      id text PRIMARY KEY,
      account_id text NOT NULL,
      connection_preset_code text NOT NULL,
      protocol_surface_code text,
      base_url text NOT NULL,
      enabled integer NOT NULL DEFAULT 1,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      UNIQUE (account_id, connection_preset_code),
      UNIQUE (account_id, id)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.account_endpoint_modes (
      connection_id text NOT NULL,
      endpoint_mode text NOT NULL,
      PRIMARY KEY (connection_id, endpoint_mode)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.account_entry_mappings (
      id text PRIMARY KEY,
      account_id text NOT NULL,
      source_model text NOT NULL,
      source_endpoint_family text NOT NULL,
      target_connection_id text NOT NULL,
      target_model_id text NOT NULL,
      target_endpoint_family text NOT NULL,
      enabled integer NOT NULL DEFAULT 1,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      UNIQUE (account_id, source_model, source_endpoint_family)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.account_tags (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      name text NOT NULL,
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE UNIQUE INDEX IF NOT EXISTS idx_account_tags_owner_name_unique
      ON juhe_business.account_tags (system_account_id, name);
	CREATE TABLE IF NOT EXISTS juhe_business.account_tag_bindings (
      account_id text NOT NULL,
      tag_id text NOT NULL,
      system_account_id text NOT NULL,
      created_at text NOT NULL,
      PRIMARY KEY (account_id, tag_id)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.account_name_search_terms (
      account_id text NOT NULL,
      system_account_id text NOT NULL,
      term text NOT NULL,
      created_at text NOT NULL,
      PRIMARY KEY (account_id, term)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.account_name_search_documents (
      account_id text PRIMARY KEY,
      system_account_id text NOT NULL,
      normalized_name text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.account_api_key_runtime_states (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      account_id text NOT NULL,
      key_fingerprint text NOT NULL,
      key_index integer NOT NULL DEFAULT 0,
      status text NOT NULL DEFAULT 'active',
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.account_lock_states (
      account_id text PRIMARY KEY,
      enabled integer NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
      lock_state text NOT NULL DEFAULT 'UNLOCKED' CHECK (lock_state IN ('UNLOCKED', 'LOCKED_IDLE', 'ENGAGED', 'DEAD_CONFIRMED')),
      lock_death_timeout_seconds integer NOT NULL DEFAULT 300 CHECK (lock_death_timeout_seconds BETWEEN 30 AND 3600),
      lock_retry_interval_seconds integer NOT NULL DEFAULT 5 CHECK (lock_retry_interval_seconds BETWEEN 5 AND 30),
      incident_id text,
      generation bigint NOT NULL DEFAULT 0 CHECK (generation >= 0),
      incident_started_at text,
      deadline_at text,
      original_status text,
      provenance text,
      next_retry_at_ms bigint,
      lease_id text,
      lease_until_ms bigint,
      updated_at text NOT NULL
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
      PRIMARY KEY (system_account_id, scope_type, scope_id)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.account_health_jobs_input_versions (
      account_id text PRIMARY KEY,
      current_version integer NOT NULL CHECK (current_version >= 1),
      reserved_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.account_health_jobs_input_outbox (
      event_id text PRIMARY KEY,
      account_id text NOT NULL,
      input_version integer NOT NULL CHECK (input_version >= 1),
      event_kind text NOT NULL CHECK (event_kind IN ('snapshot', 'tombstone')),
      reason text NOT NULL,
      config_revision integer NOT NULL CHECK (config_revision >= 1),
      dispatch_revision bigint NOT NULL CHECK (dispatch_revision >= 1),
      status text NOT NULL CHECK (status IN ('pending', 'leased', 'published', 'failed', 'superseded')),
      claim_token text,
      claimed_until text,
      attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
      available_at text NOT NULL,
      last_error text,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      UNIQUE (account_id, input_version)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.account_quality_enforcements (
      account_id text PRIMARY KEY,
      system_account_id text NOT NULL,
      enforcement_id text NOT NULL,
      generation bigint NOT NULL DEFAULT 0,
      state text NOT NULL CHECK (state IN ('active', 'cleared')),
      updated_at text NOT NULL,
      recovery_due_at text,
      recovery_lease_owner text,
      recovery_lease_until text,
      cleared_at text
    );
	CREATE TABLE IF NOT EXISTS juhe_business.group_account_stats_dirty (
      group_id text PRIMARY KEY,
      reason text,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_stats.account_usage_snapshots (
      system_account_id text NOT NULL,
      account_id text NOT NULL,
      kind text NOT NULL CHECK (kind IN ('openai_codex', 'relay_balance', 'xai_grok', 'anthropic_claude')),
      source text,
      snapshot_json text NOT NULL,
      refresh_status text,
      last_attempt_at text,
      last_success_at text,
      next_refresh_after text,
      last_error_message text,
      updated_at text NOT NULL,
      created_at text NOT NULL,
      PRIMARY KEY (system_account_id, account_id, kind)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.account_circuit_outbox (
      event_id text PRIMARY KEY,
      projection_key text NOT NULL,
      dedupe_key text NOT NULL,
      event_type text NOT NULL CHECK (event_type IN ('dispatch_revision_changed', 'incident_changed')),
      account_id text NOT NULL,
      account_runtime_key text NOT NULL,
      circuit_scope_key text,
      incident_id text,
      transition_id text NOT NULL,
      dispatch_revision bigint NOT NULL CHECK (dispatch_revision >= 1),
      generation bigint,
      ledger_revision bigint,
      status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'dispatched')),
      available_at_ms bigint NOT NULL,
      claim_token text,
      claimed_by text,
      claim_until_ms bigint,
      attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
      last_error_class text,
      acknowledged_at_ms bigint,
      created_at_ms bigint NOT NULL,
      updated_at_ms bigint NOT NULL,
      UNIQUE (projection_key, dedupe_key),
      CHECK (length(event_id) BETWEEN 1 AND 256),
      CHECK (length(projection_key) BETWEEN 1 AND 128),
      CHECK (length(dedupe_key) BETWEEN 1 AND 256),
      CHECK (length(account_runtime_key) BETWEEN 1 AND 1024),
      CHECK (length(transition_id) BETWEEN 1 AND 256)
    );
`

// newAccountsPGDatabase 创建 pgtestkit 隔离 PG 测试库、注入 accounts 夹具
// schema，并返回带 juhe_business,juhe_stats search_path 的连接池（夹具/测试
// 手写 SQL 的裸表名由 search_path 解析；生产 store 的 PG 臂本就全限定名）。
// pgtestkit 自带连接池先行关闭，测试结束统一由 pgtestkit 删库。
func newAccountsPGDatabase(t *testing.T) *sql.DB {
	t.Helper()
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, accountsPGFixtureSchema)
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

// newFreshPGDatabase 创建 pgtestkit 隔离 PG 测试库并应用 bootstrap.EnsurePostgres
// 生产全量建表（fresh-DDL 契约测试专用：b1 连接写入原语/复合外键/草稿快照链
// 与 bug0335 入口映射写序回归直接落在生产 DDL 上，外键语义真实生效），返回
// 带 juhe_business search_path 的连接池（种子/校验手写 SQL 的裸表名由
// search_path 解析）。
func newFreshPGDatabase(t *testing.T) *sql.DB {
	t.Helper()
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := bootstrap.EnsurePostgres(ctx, db)
		return err
	})
	_ = testDB.DB.Close()
	pool, err := sql.Open("pgx", testDB.DSN+"&search_path=juhe_business")
	if err != nil {
		t.Fatalf("pgtestkit: 打开 fresh DDL 连接池失败: %v", err)
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
