package aipublic

// pgkit_test.go 是 aipublic 测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z SQLite 与内存模式彻底退役，aipublic/oauthmgmt
// 测试迁移切片，T65）：pgtestkit 每测试独立库 + 生产 PG 臂（组合根恒
// pgDialect=true 的 juhe_business/juhe_dataset schema 限定 + $n 占位符，
// 与生产 PG 装配同一方言）。
//
// 分组处置（D9）：
//   - 组A（被测对象有 PG 臂且断言可逐字节保持）：HTTP/store 级测试走本文件
//     newAIPublicPGDatabase 夹具，测试体断言逐字节保持；夹具 seed 与测试体
//     直连 db 的手写 SQL 经 fixtureBind 对齐生产 PG 臂（$n；裸表名由连接级
//     search_path=juhe_business,juhe_dataset 解析，生产 aipublic 臂本就
//     Deps.table 全限定）。schema 列集与原共享夹具 aipublicSchema 同一子集，
//     类型逐表对齐 maintenance 生产 DDL（pg_schema_business_tables.go /
//     pg_schema_dataset.go）；FK 按 accounts/apikeys 夹具先例放宽（store 删除
//     级联为显式 DELETE，seed 行不满足生产外键链）；行为性约束逐字保留
//     （idx_accounts_owner_name_unique 重名 409 映射依赖约束名、models 的
//     scope/status/jsonb 形状 CHECK、account_endpoint_modes 25 值词表、
//     account_lock_states / account_health_jobs_input_outbox /
//     account_circuit_outbox 的生产 CHECK）。必要偏离登记：生产
//     external_integration_source_tokens.token_secret_encrypted 为 NOT NULL，
//     夹具放宽为 nullable（seedSource 对公开来源 token 恒插 NULL，生产写面
//     不经该列）；accounts.connection_driver_code 生产 NOT NULL，夹具放宽为
//     nullable（同 accounts pgkit 先例）。
//   - 死表退役（原 sqlite 夹具的迁移壳残留，生产 fresh DDL 已无这些表，
//     aipublic 读写面（vendors/models 单表 + registry 派生档案）不触及）：
//     provider_model_catalog、custom_provider_models、
//     provider_protocol_profile_families、protocol_endpoint_families、
//     account_model_mappings 不再建表，对应 seed（seedProviderProfile）随
//     wc_provider_test.go 的死表臂退役。
//   - closed/不可达 DB 臂走 newUnreachablePGDatabase（accounts 包
//     w13a_patch_arms_test.go 先例：pgx Open 恒成功、首查连接拒绝）。
//
// 连接串由 pgtestkit 自 .local dev env 解析，内容不进入任何文件、日志或断言。

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// aipublicPGFixtureSchema 是 aipublic 面测试的 PG schema。列集与原共享夹具
// aipublicSchema 同一子集，类型照抄 maintenance 生产 DDL（pg_schema_business_
// tables.go / pg_schema_dataset.go）；FK 放宽与约束保留理由见文件头注释。
const aipublicPGFixtureSchema = `
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
	CREATE TABLE IF NOT EXISTS juhe_business.external_integration_sources (
      id text PRIMARY KEY,
      name text NOT NULL,
      status text NOT NULL DEFAULT 'active',
      scopes_json text NOT NULL DEFAULT '[]',
      rate_limits_json text NOT NULL DEFAULT '[]',
      expires_at text,
      notes text,
      last_used_at text,
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.external_integration_source_tokens (
      id text PRIMARY KEY,
      source_ref_id text NOT NULL,
      name text NOT NULL,
      token_hash text NOT NULL UNIQUE,
      token_secret_encrypted text,
      token_prefix text NOT NULL,
      token_suffix text NOT NULL,
      status text NOT NULL DEFAULT 'active',
      scopes_json text NOT NULL DEFAULT '[]',
      expires_at text,
      last_used_at text,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      revoked_at text
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
	CREATE TABLE IF NOT EXISTS juhe_business.super_group_members (
      id text PRIMARY KEY,
      super_group_id text NOT NULL,
      member_group_id text NOT NULL,
      priority integer NOT NULL CHECK (priority >= 0),
      enabled integer NOT NULL DEFAULT 1,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      CONSTRAINT super_group_members_unique_member UNIQUE (super_group_id, member_group_id),
      CONSTRAINT super_group_members_unique_priority UNIQUE (super_group_id, priority)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.group_authorization_settings (
      authorization_id text PRIMARY KEY,
      system_account_id text NOT NULL,
      group_id text NOT NULL,
      enabled integer NOT NULL DEFAULT 1,
      group_type text NOT NULL DEFAULT 'personal',
      scheduling_policy_json text,
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.group_account_stats_dirty (
      group_id text PRIMARY KEY,
      reason text,
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
      PRIMARY KEY (system_account_id, scope_type, scope_id)
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
      health_check_connection_id text,
      last_health_check_at text,
      last_health_success_at text,
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
      scope text NOT NULL,
      system_account_id text,
      status text NOT NULL DEFAULT 'active',
      catalog_visible boolean NOT NULL DEFAULT true,
      catalog_order integer,
      release_date text,
      shutdown_date text,
      supported_endpoint_modes_json text NOT NULL DEFAULT '[]',
      supported_service_tiers_json text NOT NULL DEFAULT '[]',
      supported_reasoning_efforts_json text NOT NULL DEFAULT '[]',
      default_reasoning_effort text,
      codex_supported_reasoning_levels_json text NOT NULL DEFAULT '[]',
      codex_default_reasoning_level text,
      codex_multi_agent_version text,
      context_window_tokens integer,
      max_input_tokens integer,
      max_output_tokens integer,
      max_tokens integer,
      supports_prompt_caching boolean NOT NULL DEFAULT false,
      source text,
      pricing_notes text,
      capability_notes text,
      notes text,
      created_by text,
      updated_by text,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      input_usd_per_1m double precision,
      output_usd_per_1m double precision,
      cached_input_usd_per_1m double precision,
      cache_write_usd_per_1m double precision,
      cache_write_1h_usd_per_1m double precision,
      cache_storage_usd_per_1m_per_hour double precision,
      service_tier_prices_json text NOT NULL DEFAULT '{}',
      long_context_input_token_threshold integer,
      long_context_input_token_threshold_inclusive boolean NOT NULL DEFAULT false,
      long_context_input_cost_multiplier double precision,
      long_context_output_cost_multiplier double precision,
      image_input_usd_per_1m double precision,
      image_output_usd_per_1m double precision,
      audio_input_usd_per_1m double precision,
      audio_output_usd_per_1m double precision,
      output_usd_per_image double precision,
      currency text NOT NULL DEFAULT 'USD',
      CHECK (status IN ('draft', 'active', 'disabled')),
      CHECK (scope IN ('builtin', 'global', 'personal')),
      CHECK (
        (scope = 'personal' AND system_account_id IS NOT NULL)
        OR (scope IN ('builtin', 'global') AND system_account_id IS NULL)
      )
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
      PRIMARY KEY (connection_id, endpoint_mode),
      CHECK (endpoint_mode IN ('images_json', 'chat_json', 'chat_sse', 'responses_json', 'responses_sse', 'messages_json', 'messages_sse', 'message_token_counting', 'generate_content_json', 'generate_content_sse', 'interactions_json', 'interactions_sse', 'count_tokens', 'embed_content', 'audio_speech', 'audio_transcription_json', 'video_create', 'video_get', 'video_content', 'video_cancel', 'audio_job_create', 'audio_job_get', 'audio_job_content', 'audio_job_cancel', 'realtime_session'))
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
      UNIQUE (projection_key, dedupe_key)
    );
`

// newAIPublicPGDatabase 创建 pgtestkit 隔离 PG 测试库、注入 aipublic 夹具
// schema，并返回带 juhe_business,juhe_dataset search_path 的连接池（夹具/
// 测试手写 SQL 的裸表名由 search_path 解析；生产 aipublic 臂本就 Deps.table
// 全限定）。pgtestkit 自带连接池先行关闭，测试结束统一由 pgtestkit 删库。
func newAIPublicPGDatabase(t *testing.T) *sql.DB {
	t.Helper()
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, aipublicPGFixtureSchema)
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

// newUnreachablePGDatabase 返回一个已打开但底层不可达的 pgx 连接池（accounts
// 包 w13a_patch_arms_test.go 先例）：Open 恒成功，首次使用即连接拒绝，锁定
// closed/存储不可达臂的错误语义。
func newUnreachablePGDatabase(t *testing.T) *sql.DB {
	t.Helper()
	pool, err := sql.Open("pgx", "host=127.0.0.1 port=1 sslmode=disable connect_timeout=1")
	if err != nil {
		t.Fatalf("pgtestkit: 打开不可达连接池失败: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

// fixtureBind 把夹具/测试手写 SQL 的 ? 占位符按序改写为 $n（与生产 Deps.bind
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
