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
	CREATE TABLE IF NOT EXISTS juhe_business.system_teams (
      id text PRIMARY KEY,
      name text NOT NULL,
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
      created_by text NOT NULL,
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.groups (
      id text PRIMARY KEY,
      name text NOT NULL,
      system_account_id text NOT NULL,
      status text NOT NULL DEFAULT 'active'
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
      status text NOT NULL DEFAULT 'pending_test',
      schedulable integer NOT NULL DEFAULT 1,
      notes text,
      config_revision integer NOT NULL DEFAULT 1,
      dispatch_revision bigint NOT NULL DEFAULT 1 CHECK (dispatch_revision >= 1),
      account_expires_at text,
      cooldown_until text,
      last_error_code text,
      last_error_message text,
      cooldown_retest_failure_count integer NOT NULL DEFAULT 0,
      cooldown_retest_observation_started_at text,
      cooldown_retest_last_at text,
      cooldown_retest_last_status_code integer,
      stream_failure_count integer NOT NULL DEFAULT 0,
      stream_failure_window_started_at text,
      temporary_unavailable_continuous_probe_enabled integer NOT NULL DEFAULT 1 CHECK (temporary_unavailable_continuous_probe_enabled IN (0, 1)),
      health_check_model text NOT NULL DEFAULT '',
      health_check_endpoint_mode text NOT NULL DEFAULT '' CHECK (health_check_endpoint_mode IN ('images_json', 'chat_json', 'chat_sse', 'responses_json', 'responses_sse', 'messages_json', 'messages_sse', 'message_token_counting', 'generate_content_json', 'generate_content_sse', 'interactions_json', 'interactions_sse', 'count_tokens', 'embed_content', 'audio_speech', 'audio_transcription_json', 'video_create', 'video_get', 'video_content', 'video_cancel', 'audio_job_create', 'audio_job_get', 'audio_job_content', 'audio_job_cancel', 'realtime_session')),
      capability_domain text NOT NULL DEFAULT 'chat' CHECK (capability_domain IN ('chat','video','audio','image')),
      authorization_instance_source_account_id text,
      authorization_instance_authorization_id text,
      authorization_instance_owner_system_account_id text,
      deleted_at text,
      deleted_by text,
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
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
	CREATE TABLE IF NOT EXISTS juhe_business.account_circuit_outbox (
      event_id text PRIMARY KEY,
      projection_key text NOT NULL,
      dedupe_key text NOT NULL,
      event_type text NOT NULL CHECK (event_type IN ('dispatch_revision_changed', 'incident_changed')),
      account_id text NOT NULL,
      account_runtime_key text NOT NULL CHECK (length(account_runtime_key) BETWEEN 1 AND 1024),
      circuit_scope_key text,
      incident_id text,
      transition_id text NOT NULL CHECK (length(transition_id) BETWEEN 1 AND 256),
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
      UNIQUE (account_id, input_version),
      CHECK ((status = 'leased' AND claim_token IS NOT NULL AND claimed_until IS NOT NULL) OR (status <> 'leased' AND claim_token IS NULL AND claimed_until IS NULL))
    )
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
