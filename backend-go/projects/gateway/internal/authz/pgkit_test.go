package authz
// pgkit_test.go 是 authz 测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z SQLite 与内存模式彻底退役，测试尾巴批次）：
// pgtestkit 每测试独立库 + 生产 PG 臂（NewStore(db, ...)，juhe_business
// schema 限定 + $n 占位符，与 gateway 组合根恒 PG 的装配同一方言）。
//
// 分组处置（D9/D17）：组A（被测对象有 PG 臂且断言逐字节保持）——store/HTTP 级
// 测试走本文件夹具，断言逐字节保持；夹具 schema 列集与原 sqlite 夹具同一
// 子集，类型照抄 maintenance 生产 DDL（pg_schema_business_tables.go）与
// accounts/aipublic 夹具先例，FK 放宽。必要偏离登记：不建
// idx_accounts_owner_name_unique 部分唯一索引（原 sqlite 夹具同款裁定：多个
// seeder 在同一 owner 下插入无名 DEFAULT '' 账户，名称去重由名称阶梯写入前
// 查询自担）。
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
// authzPGFixtureSchema 是 authz 面测试的 PG schema。
const authzPGFixtureSchema = `
	CREATE SCHEMA IF NOT EXISTS juhe_business;
	CREATE TABLE IF NOT EXISTS juhe_business.system_accounts (
      id text PRIMARY KEY,
      username text UNIQUE DEFAULT '',
      display_name text DEFAULT '',
      description text,
      role text DEFAULT 'user',
      status text DEFAULT 'active',
      password_hash text DEFAULT '',
      must_change_password integer DEFAULT 0,
      image_generation_enabled integer DEFAULT 0,
      ai_account_limit integer,
      expedited_account_limit integer,
      request_limits_json text,
      last_login_at text,
      created_at text DEFAULT '',
      updated_at text DEFAULT ''
    );
	CREATE TABLE IF NOT EXISTS juhe_business.system_teams (
      id text PRIMARY KEY,
      name text DEFAULT '',
      description text,
      status text DEFAULT 'active',
      created_by text DEFAULT '',
      created_at text DEFAULT '',
      updated_at text DEFAULT ''
    );
	CREATE TABLE IF NOT EXISTS juhe_business.system_team_members (
      id text PRIMARY KEY,
      team_id text DEFAULT '',
      system_account_id text DEFAULT '',
      member_role text DEFAULT 'member',
      status text DEFAULT 'active',
      joined_at text DEFAULT '',
      removed_at text,
      created_by text DEFAULT '',
      created_at text DEFAULT '',
      updated_at text DEFAULT ''
    );
	CREATE TABLE IF NOT EXISTS juhe_business.groups (
      id text PRIMARY KEY,
      system_account_id text DEFAULT '',
      name text DEFAULT '',
      description text,
      enabled integer DEFAULT 1,
      is_default integer DEFAULT 0,
      group_type text DEFAULT 'personal',
      group_kind text DEFAULT 'standard',
      scheduling_policy_json text,
      created_at text DEFAULT '',
      updated_at text DEFAULT '',
      status text DEFAULT 'active'
    );
	CREATE TABLE IF NOT EXISTS juhe_business.resource_authorizations (
      id text PRIMARY KEY,
      resource_type text DEFAULT '',
      resource_id text DEFAULT '',
      resource_owner_system_account_id text DEFAULT '',
      grantee_system_account_id text DEFAULT '',
      scope text DEFAULT 'use',
      status text DEFAULT 'active',
      effective_source_type text,
      effective_source_team_id text,
      activated_at text,
      last_source_changed_at text,
      remark text,
      expires_at text,
      limits_json text,
      created_by text DEFAULT '',
      created_at text DEFAULT '',
      revoked_by text,
      revoked_at text,
      revoked_reason text,
      updated_at text DEFAULT ''
    );
	CREATE TABLE IF NOT EXISTS juhe_business.resource_authorization_sources (
      id text PRIMARY KEY,
      authorization_id text DEFAULT '',
      source_type text DEFAULT '',
      source_team_id text,
      status text DEFAULT 'active',
      activated_at text,
      ended_at text,
      ended_reason text,
      created_by text DEFAULT '',
      created_at text DEFAULT '',
      revoked_by text,
      revoked_at text,
      updated_at text DEFAULT ''
    );
	CREATE TABLE IF NOT EXISTS juhe_business.resource_authorization_grants (
      id text PRIMARY KEY,
      resource_type text DEFAULT '',
      resource_id text DEFAULT '',
      resource_owner_system_account_id text DEFAULT '',
      grantee_type text DEFAULT '',
      grantee_system_account_id text,
      grantee_team_id text,
      scope text DEFAULT 'use',
      status text DEFAULT 'active',
      remark text,
      expires_at text,
      limits_json text,
      created_by text DEFAULT '',
      created_at text DEFAULT '',
      revoked_by text,
      revoked_at text,
      updated_at text DEFAULT ''
    );
	CREATE TABLE IF NOT EXISTS juhe_business.accounts (
      id text PRIMARY KEY,
      config_revision integer DEFAULT 1,
      dispatch_revision bigint DEFAULT 1,
      circuit_projection_revision bigint DEFAULT 0,
      system_account_id text DEFAULT '',
      vendor_code text,
      connection_driver_code text DEFAULT '',
      oauth_type text,
      protocol_code text DEFAULT '',
      protocol_version text DEFAULT '',
      name text DEFAULT '',
      type text DEFAULT '',
      status text DEFAULT 'pending_test',
      credentials_encrypted text DEFAULT '',
      credential_fingerprint text,
      credential_mask text DEFAULT '',
      oauth_access_token_expires_at text,
      oauth_refresh_token_present integer DEFAULT 0,
      proxy_profile_id text,
      concurrency_limit integer DEFAULT 5000,
      priority integer DEFAULT 0,
      super_priority_enabled integer DEFAULT 0,
      fallback_enabled integer DEFAULT 0,
      schedulable integer DEFAULT 1,
      availability_schedule_json text,
      availability_schedule_next_check_at text,
      notes text,
      account_expires_at text,
      last_used_at text,
      cooldown_until text,
      last_error_code text,
      last_error_message text,
      last_error_trace_id text,
      cooldown_retest_failure_count integer DEFAULT 0,
      cooldown_retest_observation_started_at text,
      cooldown_retest_generation text,
      cooldown_retest_last_at text,
      cooldown_retest_last_status_code integer,
      temporary_unavailable_continuous_probe_enabled integer DEFAULT 1,
      expedited_recovery_enabled integer DEFAULT 0,
      capability_domain text DEFAULT 'chat',
      health_check_connection_id text,
      health_check_model text,
      health_check_endpoint_mode text,
      last_health_check_at text,
      next_health_check_at text,
      last_health_success_at text,
      health_check_failure_count integer DEFAULT 0,
      health_check_failure_started_at text,
      last_health_check_status_code integer,
      last_health_check_error_code text,
      last_health_check_error_message text,
      last_health_check_trace_id text,
      stream_failure_count integer DEFAULT 0,
      stream_failure_window_started_at text,
      balance_query_enabled integer DEFAULT 0,
      balance_query_config_json text DEFAULT '{}',
      balance_query_next_refresh_at text,
      authorization_instance_source_account_id text,
      authorization_instance_authorization_id text,
      authorization_instance_owner_system_account_id text,
      deleted_at text,
      deleted_by text,
      created_at text DEFAULT '',
      updated_at text DEFAULT ''
    );
	CREATE TABLE IF NOT EXISTS juhe_business.group_accounts (
      system_account_id text DEFAULT '',
      group_id text DEFAULT '',
      account_id text DEFAULT '',
      account_authorization_id text,
      local_priority integer DEFAULT 0,
      local_super_priority_enabled integer DEFAULT 0,
      local_fallback_enabled integer DEFAULT 0,
      enabled integer DEFAULT 1,
      created_at text DEFAULT '',
      updated_at text DEFAULT '',
      PRIMARY KEY (group_id, account_id)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.account_circuit_outbox (
      event_id text PRIMARY KEY,
      projection_key text DEFAULT '',
      dedupe_key text DEFAULT '',
      event_type text DEFAULT '',
      account_id text DEFAULT '',
      account_runtime_key text DEFAULT '',
      circuit_scope_key text,
      incident_id text,
      transition_id text DEFAULT '',
      dispatch_revision bigint DEFAULT 0,
      generation bigint,
      ledger_revision bigint,
      status text DEFAULT 'pending',
      available_at_ms bigint DEFAULT 0,
      claim_token text,
      claimed_by text,
      claim_until_ms bigint,
      attempt_count integer DEFAULT 0,
      last_error_class text,
      acknowledged_at_ms bigint,
      created_at_ms bigint DEFAULT 0,
      updated_at_ms bigint DEFAULT 0,
      UNIQUE (projection_key, dedupe_key)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.account_name_search_terms (
      account_id text DEFAULT '',
      system_account_id text DEFAULT '',
      term text DEFAULT '',
      created_at text DEFAULT '',
      PRIMARY KEY (account_id, term)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.account_name_search_documents (
      account_id text PRIMARY KEY,
      system_account_id text DEFAULT '',
      normalized_name text DEFAULT '',
      updated_at text DEFAULT ''
    );
	CREATE TABLE IF NOT EXISTS juhe_business.request_quota_hourly_window_scope_bindings (
      system_account_id text DEFAULT '',
      scope_type text DEFAULT '',
      scope_id text DEFAULT '',
      source_type text DEFAULT '',
      source_id text DEFAULT '',
      window_hours integer DEFAULT 0,
      created_at text DEFAULT '',
      updated_at text DEFAULT '',
      PRIMARY KEY (system_account_id, scope_type, scope_id),
      UNIQUE (source_type, source_id, system_account_id, scope_type, scope_id)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.account_health_jobs_input_versions (
      account_id text PRIMARY KEY,
      current_version integer DEFAULT 0,
      reserved_at text DEFAULT ''
    );
	CREATE TABLE IF NOT EXISTS juhe_business.account_health_jobs_input_outbox (
      event_id text PRIMARY KEY,
      account_id text DEFAULT '',
      input_version integer DEFAULT 0,
      event_kind text DEFAULT '',
      reason text DEFAULT '',
      config_revision integer DEFAULT 0,
      dispatch_revision bigint DEFAULT 0,
      status text DEFAULT '',
      claim_token text,
      claimed_until text,
      attempt_count integer DEFAULT 0,
      available_at text DEFAULT '',
      last_error text,
      created_at text DEFAULT '',
      updated_at text DEFAULT '',
      UNIQUE (account_id, input_version)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.models (
      id text PRIMARY KEY,
      vendor_code text,
      public_model text DEFAULT '',
      upstream_model text DEFAULT '',
      scope text DEFAULT '',
      system_account_id text,
      status text DEFAULT 'active',
      catalog_visible boolean DEFAULT true,
      catalog_order integer,
      release_date text,
      shutdown_date text,
      supported_endpoint_modes_json text DEFAULT '[]',
      supported_service_tiers_json text DEFAULT '[]',
      supported_reasoning_efforts_json text DEFAULT '[]',
      default_reasoning_effort text,
      codex_supported_reasoning_levels_json text DEFAULT '[]',
      codex_default_reasoning_level text,
      codex_multi_agent_version text,
      context_window_tokens integer,
      max_input_tokens integer,
      max_output_tokens integer,
      max_tokens integer,
      supports_prompt_caching boolean DEFAULT false,
      source text,
      pricing_notes text,
      capability_notes text,
      notes text,
      created_by text,
      updated_by text,
      created_at text DEFAULT '',
      updated_at text DEFAULT '',
      input_usd_per_1m double precision,
      output_usd_per_1m double precision,
      cached_input_usd_per_1m double precision,
      cache_write_usd_per_1m double precision,
      cache_write_1h_usd_per_1m double precision,
      cache_storage_usd_per_1m_per_hour double precision,
      service_tier_prices_json text DEFAULT '{}',
      long_context_input_token_threshold integer,
      long_context_input_token_threshold_inclusive boolean DEFAULT false,
      long_context_input_cost_multiplier double precision,
      long_context_output_cost_multiplier double precision,
      image_input_usd_per_1m double precision,
      image_output_usd_per_1m double precision,
      audio_input_usd_per_1m double precision,
      audio_output_usd_per_1m double precision,
      output_usd_per_image double precision,
      currency text DEFAULT 'USD'
    );
	CREATE TABLE IF NOT EXISTS juhe_business.protocols (
      id text PRIMARY KEY,
      code text DEFAULT '',
      version text DEFAULT '',
      name text DEFAULT '',
      description text,
      enabled integer DEFAULT 1,
      created_at text DEFAULT '',
      updated_at text DEFAULT '',
      UNIQUE (code, version)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.account_supported_models (
      account_id text DEFAULT '',
      model_id text DEFAULT '',
      created_at text DEFAULT '',
      PRIMARY KEY (account_id, model_id)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.external_integration_sources (
      id text PRIMARY KEY,
      name text DEFAULT '',
      status text DEFAULT 'active',
      scopes_json text DEFAULT '[]',
      rate_limits_json text DEFAULT '[]',
      expires_at text,
      notes text,
      last_used_at text,
      created_at text DEFAULT '',
      updated_at text DEFAULT ''
    );
	CREATE SCHEMA IF NOT EXISTS juhe_stats;
	CREATE TABLE IF NOT EXISTS juhe_stats.authorization_team_usage_summary_daily (
      system_account_id text DEFAULT '',
      stat_date text DEFAULT '',
      team_filter_id text DEFAULT '',
      resource_filter_type text DEFAULT 'all',
      resource_filter_id text DEFAULT '',
      row_count bigint DEFAULT 0,
      request_count bigint DEFAULT 0,
      success_count bigint DEFAULT 0,
      error_count bigint DEFAULT 0,
      input_tokens bigint DEFAULT 0,
      output_tokens bigint DEFAULT 0,
      cache_read_tokens bigint DEFAULT 0,
      cache_read_cost_usd double precision DEFAULT 0,
      cache_write_tokens bigint DEFAULT 0,
      cache_write_1h_tokens bigint DEFAULT 0,
      cache_write_cost_usd double precision DEFAULT 0,
      thinking_tokens bigint DEFAULT 0,
      input_image_tokens bigint DEFAULT 0,
      output_image_tokens bigint DEFAULT 0,
      total_cost_usd double precision DEFAULT 0,
      duration_ms_sum bigint DEFAULT 0,
      duration_ms_count bigint DEFAULT 0,
      duration_ms_max bigint DEFAULT 0,
      first_token_ms_sum bigint DEFAULT 0,
      first_token_ms_count bigint DEFAULT 0,
      first_token_ms_max bigint DEFAULT 0,
      last_used_at text,
      last_error_at text,
      updated_at text DEFAULT '',
      PRIMARY KEY (system_account_id, stat_date, team_filter_id, resource_filter_type, resource_filter_id)
    );
	CREATE TABLE IF NOT EXISTS juhe_stats.authorization_user_usage_summary_daily (
      system_account_id text DEFAULT '',
      stat_date text DEFAULT '',
      team_filter_id text DEFAULT '',
      grantee_filter_system_account_id text DEFAULT '',
      resource_filter_type text DEFAULT 'all',
      resource_filter_id text DEFAULT '',
      row_count bigint DEFAULT 0,
      request_count bigint DEFAULT 0,
      success_count bigint DEFAULT 0,
      error_count bigint DEFAULT 0,
      input_tokens bigint DEFAULT 0,
      output_tokens bigint DEFAULT 0,
      cache_read_tokens bigint DEFAULT 0,
      cache_read_cost_usd double precision DEFAULT 0,
      cache_write_tokens bigint DEFAULT 0,
      cache_write_1h_tokens bigint DEFAULT 0,
      cache_write_cost_usd double precision DEFAULT 0,
      thinking_tokens bigint DEFAULT 0,
      input_image_tokens bigint DEFAULT 0,
      output_image_tokens bigint DEFAULT 0,
      total_cost_usd double precision DEFAULT 0,
      duration_ms_sum bigint DEFAULT 0,
      duration_ms_count bigint DEFAULT 0,
      duration_ms_max bigint DEFAULT 0,
      first_token_ms_sum bigint DEFAULT 0,
      first_token_ms_count bigint DEFAULT 0,
      first_token_ms_max bigint DEFAULT 0,
      last_used_at text,
      last_error_at text,
      updated_at text DEFAULT '',
      PRIMARY KEY (system_account_id, stat_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id)
    );
	CREATE TABLE IF NOT EXISTS juhe_stats.usage_scope_range_windows (
      system_account_id text DEFAULT '',
      scope_type text DEFAULT '',
      scope_id text DEFAULT '',
      start_date text DEFAULT '',
      end_date text DEFAULT '',
      window_key text GENERATED ALWAYS AS (start_date || ':' || end_date) STORED,
      request_count bigint DEFAULT 0,
      success_count bigint DEFAULT 0,
      error_count bigint DEFAULT 0,
      input_tokens bigint DEFAULT 0,
      output_tokens bigint DEFAULT 0,
      cache_read_tokens bigint DEFAULT 0,
      cache_read_cost_usd double precision DEFAULT 0,
      cache_write_tokens bigint DEFAULT 0,
      cache_write_1h_tokens bigint DEFAULT 0,
      cache_write_cost_usd double precision DEFAULT 0,
      thinking_tokens bigint DEFAULT 0,
      input_image_tokens bigint DEFAULT 0,
      output_image_tokens bigint DEFAULT 0,
      total_cost_usd double precision DEFAULT 0,
      duration_ms_sum bigint DEFAULT 0,
      duration_ms_count bigint DEFAULT 0,
      duration_ms_max bigint DEFAULT 0,
      first_token_ms_sum bigint DEFAULT 0,
      first_token_ms_count bigint DEFAULT 0,
      first_token_ms_max bigint DEFAULT 0,
      active_days integer DEFAULT 0,
      last_used_at text,
      last_error_at text,
      updated_at text DEFAULT '',
      PRIMARY KEY (system_account_id, scope_type, scope_id, start_date, end_date)
    );
	CREATE TABLE IF NOT EXISTS juhe_stats.usage_quota_hourly_window_dirty_scopes (
      system_account_id text DEFAULT '',
      scope_type text DEFAULT '',
      scope_id text DEFAULT '',
      generation bigint DEFAULT 1,
      first_dirty_at text DEFAULT '',
      updated_at text DEFAULT '',
      PRIMARY KEY (system_account_id, scope_type, scope_id)
    );
	CREATE TABLE IF NOT EXISTS juhe_stats.authorization_team_usage_range_windows (
      system_account_id text DEFAULT '',
      start_date text DEFAULT '',
      end_date text DEFAULT '',
      team_filter_id text DEFAULT '',
      resource_filter_type text DEFAULT 'all',
      resource_filter_id text DEFAULT '',
      request_count bigint DEFAULT 0,
      input_tokens bigint DEFAULT 0,
      output_tokens bigint DEFAULT 0,
      cache_read_tokens bigint DEFAULT 0,
      cache_read_cost_usd double precision DEFAULT 0,
      cache_write_tokens bigint DEFAULT 0,
      cache_write_1h_tokens bigint DEFAULT 0,
      cache_write_cost_usd double precision DEFAULT 0,
      thinking_tokens bigint DEFAULT 0,
      input_image_tokens bigint DEFAULT 0,
      output_image_tokens bigint DEFAULT 0,
      total_cost_usd double precision DEFAULT 0,
      last_used_at text,
      updated_at text DEFAULT '',
      PRIMARY KEY (system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id)
    );
`
// newAuthzPGDatabase 创建 pgtestkit 隔离 PG 测试库、注入夹具 schema，并返回带
// juhe_business search_path 的连接池（夹具/测试手写 SQL 的裸表名由 search_path
// 解析；生产 store 的 PG 臂本就 s.table 全限定）。
func newAuthzPGDatabase(t *testing.T) *sql.DB {
	t.Helper()
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, authzPGFixtureSchema)
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