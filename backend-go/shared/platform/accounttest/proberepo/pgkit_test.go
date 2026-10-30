package proberepo

// pgkit_test.go 是探针族仓储测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z SQLite 与内存模式彻底退役，proberepo 测试切片）：
// pgtestkit 每测试独立库 + 生产 store 的 PG 形态（bind() $n 改写、juhe_business
// schema 限定，与生产 jobs 装配同方言）。
//
// 三组分类（D9/D17）：
//   - 组A（bind() 方言共享 SQL，无独立 sqlite 结构臂）：reader/mutation 行为族
//     走本文件 openTestDBPostgres，测试侧 seed/校验 SQL 对齐生产 PG 臂
//     （juhe_business 限定 + $n）；夹具 DDL 与 maintenance 权威生产 DDL
//     （pg_schema_business_tables.go / pg_schema_business_indexes.go）逐列对齐，
//     按 groups 夹具先例放宽：不建 FOREIGN KEY（seed 行不满足生产外键链），
//     保留 store upsert 依赖的 idx_account_api_key_runtime_unique 唯一索引。
//   - 组B（openTestDB SQLite 夹具随 2026-10-25 sqlite 方言退役删除）：
//     TestListDueForProbeClaimsAndSkipsFreshLeases、TestW12HListDueForProbeArms、
//     TestW7CListDueForProbeFiltersBrokenCandidates、
//     TestW12HNewStoreValidationAndListDueClamp、
//     TestW7CAuthorizedInstanceGroupPath——前四者曾被 W27①（time.Time 绑 text
//     时间列致候选扫描恒空）阻塞、末者被 W27③（授权过期臂放行）阻塞，
//     已随 W27 修复以组A 形态重建为真实 PG 断言；仅 key_index='garbage'
//     弱类型注入臂不随迁（PG 生产列 key_index 为 integer，属类型违约非行为臂）。
//   - 纯函数/凭据 helper 测试不依赖真库，保持原状。
//
// 连接串由 pgtestkit 自 .local dev env 解析，内容不进入任何文件、日志或断言。

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// proberepoBusinessSchemaPG 是探针族消费的业务表（与 maintenance 权威生产 DDL
// 逐列对齐；FK 放宽见文件头；idx_account_api_key_runtime_unique 为
// RecordKeySuccess/RecordKeyFailure 的 ON CONFLICT(account_id, key_fingerprint)
// 生产依赖，必须同名创建）。accounts/proxy_profiles 为全列生产 DDL，
// models 为本包读面列集（列型与生产一致）。
const proberepoBusinessSchemaPG = `
CREATE SCHEMA IF NOT EXISTS juhe_business;
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
CREATE TABLE IF NOT EXISTS juhe_business.models (
  id text PRIMARY KEY,
  vendor_code text,
  public_model text NOT NULL,
  upstream_model text NOT NULL,
  scope text NOT NULL,
  system_account_id text,
  status text NOT NULL DEFAULT 'active',
  created_at text NOT NULL
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
CREATE TABLE IF NOT EXISTS juhe_business.accounts (
  id text PRIMARY KEY,
  config_revision integer NOT NULL DEFAULT 1,
  dispatch_revision bigint NOT NULL DEFAULT 1 CHECK (dispatch_revision >= 1),
  circuit_projection_revision bigint NOT NULL DEFAULT 0 CHECK (circuit_projection_revision >= 0 AND circuit_projection_revision <= dispatch_revision),
  system_account_id text NOT NULL,
  vendor_code text,
  connection_driver_code text NOT NULL,
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
  health_check_model text,
  health_check_endpoint_mode text CHECK (health_check_endpoint_mode IN ('images_json', 'chat_json', 'chat_sse', 'responses_json', 'responses_sse', 'messages_json', 'messages_sse', 'message_token_counting', 'generate_content_json', 'generate_content_sse', 'interactions_json', 'interactions_sse', 'count_tokens', 'embed_content', 'audio_speech', 'audio_transcription_json', 'video_create', 'video_get', 'video_content', 'video_cancel', 'audio_job_create', 'audio_job_get', 'audio_job_content', 'audio_job_cancel', 'realtime_session')),
  last_health_check_at text,
  next_health_check_at text,
  last_health_success_at text,
  health_check_failure_count integer NOT NULL DEFAULT 0,
  health_check_failure_started_at text,
  last_health_check_status_code integer,
  last_health_check_error_code text,
  last_health_check_error_message text,
  last_health_check_trace_id text,
  stream_failure_count integer NOT NULL DEFAULT 0,
  stream_failure_window_started_at text,
  balance_query_enabled integer NOT NULL DEFAULT 0,
  balance_query_config_json text NOT NULL DEFAULT '{}',
  balance_query_next_refresh_at text,
  authorization_instance_source_account_id text,
  authorization_instance_authorization_id text,
  authorization_instance_owner_system_account_id text,
  deleted_at text,
  deleted_by text,
  created_at text NOT NULL,
  updated_at text NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_business.account_api_key_runtime_states (
  id text PRIMARY KEY,
  system_account_id text NOT NULL,
  account_id text NOT NULL,
  key_fingerprint text NOT NULL,
  key_index integer NOT NULL DEFAULT 0,
  credential_revision text,
  status text NOT NULL DEFAULT 'active',
  failure_count integer NOT NULL DEFAULT 0,
  consecutive_failures integer NOT NULL DEFAULT 0,
  success_count bigint NOT NULL DEFAULT 0,
  cooldown_until text,
  next_probe_at text,
  probe_backoff_seconds integer NOT NULL DEFAULT 0,
  recovery_started_at text,
  last_attempt_at text,
  last_success_at text,
  last_failure_at text,
  last_error_code text,
  last_error_message text,
  last_trace_id text,
  last_probe_at text,
  probe_claim_token text,
  probe_claimed_until text,
  created_at text NOT NULL,
  updated_at text NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_account_api_key_runtime_unique
  ON juhe_business.account_api_key_runtime_states(account_id, key_fingerprint);
CREATE INDEX IF NOT EXISTS idx_account_api_key_runtime_status
  ON juhe_business.account_api_key_runtime_states(account_id, status, cooldown_until);
CREATE TABLE IF NOT EXISTS juhe_business.account_supported_models (
  account_id text NOT NULL,
  model_id text NOT NULL,
  created_at text NOT NULL,
  PRIMARY KEY (account_id, model_id)
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
CREATE TABLE IF NOT EXISTS juhe_business.group_account_stats_dirty (
  group_id text PRIMARY KEY,
  reason text,
  updated_at text NOT NULL
)`

// openTestDBPostgres 在 pgtestkit 隔离 PG 测试库上构建生产 PG 方言 store。
func openTestDBPostgres(t *testing.T) *testDB {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, proberepoBusinessSchemaPG)
		return err
	})
	store, err := NewStore(Config{DB: db, Secret: testSecret, Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	return &testDB{store: store, db: db}
}

// openBareTestDBPostgres 打开未建 schema 的独立 PG 库（缺表错误臂用）。
func openBareTestDBPostgres(t *testing.T) *testDB {
	t.Helper()
	db := pgtestkit.New(t)
	store, err := NewStore(Config{DB: db, Secret: testSecret, Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	return &testDB{store: store, db: db}
}
