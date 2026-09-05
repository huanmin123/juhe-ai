// Code generated from the Node PostgreSQL storage sources listed below. The
// statements are the exact output of collectPostgresSchemaStatements() in
// postgres-schema.ts (dumped with tsx and ported verbatim), executed in the
// same order as applyPostgresSchema(). Do not hand-edit the SQL constants;
// regenerate or re-verify against the Node sources when they change.
//
// Node sources (juhe-ai backend/src/storage):
//   - postgres-schema.ts          collectPostgresSchemaStatements / applyPostgresSchema
//   - postgres-seed-defaults.ts   seedPostgresDefaults
//   - schema-defaults.ts          DEFAULT_* seed constants
//   - request-quota-limits.ts     defaultRequestQuotaHourlyWindowHours
//
// Execution model (mirrors applyPostgresSchema):
//   - Before the first statement of each schema the runner executes
//     CREATE SCHEMA IF NOT EXISTS "<schema>".
//   - Every statement is sent as one batch, prefixed with
//     SET search_path TO "<schema>", public; so unqualified names resolve
//     exactly like Node. pgx uses the simple query protocol for
//     zero-argument Exec calls, which allows these multi-statement batches.
//   - Every statement carries its own idempotency guard (IF NOT EXISTS,
//     DROP TRIGGER IF EXISTS + CREATE TRIGGER, guarded DO $$ blocks), so
//     repeated EnsurePostgres calls are no-ops.
//
// Seeds (EnsurePostgresSeeds) port the statements of seedPostgresDefaults
// whose data is static or derived deterministically. Deliberately NOT ported
// (they need human review / application ports first):
//   - the provider_model_catalog bulk upsert and the stale built-in model
//     disable (Node listProviderModelPricing pricing catalog: 105 models x
//     39 parameters),
//   - default route strategy / API key and admin chat API key seeding
//     (Node createApiKey + hashSecret + encryptJson secret material),
//   - the external integration source token creation/update (random token +
//     encryptJson); the token-free source row seeding is ported.
//   - repairBuiltInProviderProfileAccountTypes IS ported (pure read-merge-update).
//
// BUG-0167/0168 follow-up (2026-09-04): the omitted pieces above ARE ported
// now — SeedPostgresDefaults in pg_seed.go is the complete seedPostgresDefaults
// port (bulk upsert + guarded stale disable over the generated pricing
// snapshot in model_catalog_data.go, default route strategies / default API
// keys / admin chat API key with the Node crypto envelopes, and the external
// integration source token). EnsurePostgresSeeds stays unchanged as the
// portable subset for the existing golden test; new callers must use
// SeedPostgresDefaults. The catalog snapshot is data-driven: the header's
// "105 models" reflected the dump date — the 2026-09-04 Node dump yields 106
// rows (see model_catalog_data.go).

package schema

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha512"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// PGStatement mirrors PostgresSchemaStatement in postgres-schema.ts.
type PGStatement struct {
	SchemaName string
	Source     string
	SQL        string
}

// postgresSchemaStatements holds every DDL statement of
// collectPostgresSchemaStatements() in execution order (per-schema table
// dependency order, then ALTER/DO phase, then CREATE FUNCTION blocks, then
// indexes; schema groups in Node first-seen order: juhe_business, juhe_chat,
// juhe_dataset, juhe_usage, juhe_stats, juhe_codex_context).
var postgresSchemaStatements = []PGStatement{
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS system_accounts (
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
      request_limits_json text,
      last_login_at text,
      created_at text NOT NULL,
      updated_at text NOT NULL
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS system_sessions (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      token_hash text NOT NULL UNIQUE,
      expires_at text NOT NULL,
      created_at text NOT NULL,
      last_seen_at text NOT NULL,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS global_settings (
      key text PRIMARY KEY,
      value_json text NOT NULL,
      updated_at text NOT NULL
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS request_quota_hourly_window_configs (
      window_hours integer PRIMARY KEY,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      CHECK (window_hours BETWEEN 1 AND 720)
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS providers (
      id text PRIMARY KEY,
      code text NOT NULL UNIQUE,
      name text NOT NULL,
      description text,
      parent_code text,
      enabled integer NOT NULL DEFAULT 1,
      default_supported_models_json text NOT NULL DEFAULT '[]',
      created_at text NOT NULL,
      updated_at text NOT NULL,
      FOREIGN KEY (parent_code) REFERENCES providers(code)
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS protocols (
      id text PRIMARY KEY,
      code text NOT NULL,
      version text NOT NULL,
      name text NOT NULL,
      description text,
      enabled integer NOT NULL DEFAULT 1,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      UNIQUE (code, version)
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS protocol_endpoint_families (
      id text PRIMARY KEY,
      protocol_code text NOT NULL,
      protocol_version text NOT NULL,
      family_code text NOT NULL,
      name text NOT NULL,
      description text,
      enabled integer NOT NULL DEFAULT 1,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      UNIQUE (protocol_code, protocol_version, family_code),
      FOREIGN KEY (protocol_code, protocol_version) REFERENCES protocols(code, version)
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS provider_protocol_profiles (
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
      updated_at text NOT NULL,
      FOREIGN KEY (provider_code) REFERENCES providers(code),
      FOREIGN KEY (protocol_code, protocol_version) REFERENCES protocols(code, version)
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS provider_protocol_profile_families (
      profile_id text NOT NULL,
      family_code text NOT NULL,
      enabled integer NOT NULL DEFAULT 1,
      default_health_check_model text,
      capabilities_json text NOT NULL DEFAULT '[]',
      created_at text NOT NULL,
      updated_at text NOT NULL,
      PRIMARY KEY (profile_id, family_code),
      FOREIGN KEY (profile_id) REFERENCES provider_protocol_profiles(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS provider_model_catalog (
      id text PRIMARY KEY,
      provider_code text NOT NULL,
      model text NOT NULL,
      status text NOT NULL DEFAULT 'active',
      mode text,
      catalog_order integer,
      release_date text,
      shutdown_date text,
      supported_api_protocols_json text NOT NULL DEFAULT '[]',
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
      supports_prompt_caching boolean NOT NULL DEFAULT false,
      catalog_visible boolean NOT NULL DEFAULT true,
      source text NOT NULL,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      UNIQUE (provider_code, model),
      FOREIGN KEY (provider_code) REFERENCES providers(code),
      CHECK (status IN ('active', 'disabled')),
      CHECK (jsonb_typeof(service_tier_prices_json::jsonb) = 'object')
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS custom_provider_models (
      id text PRIMARY KEY,
      provider_code text NOT NULL,
      model text NOT NULL,
      scope text NOT NULL DEFAULT 'personal',
      system_account_id text,
      status text NOT NULL DEFAULT 'active',
      catalog_visible boolean NOT NULL DEFAULT true,
      mode text,
      supported_api_protocols_json text NOT NULL DEFAULT '[]',
      supported_service_tiers_json text NOT NULL DEFAULT '[]',
      supported_reasoning_efforts_json text NOT NULL DEFAULT '[]',
      default_reasoning_effort text,
      release_date text,
      shutdown_date text,
      context_window_tokens integer,
      max_input_tokens integer,
      max_output_tokens integer,
      input_usd_per_1m double precision,
      output_usd_per_1m double precision,
      cached_input_usd_per_1m double precision,
      cache_write_usd_per_1m double precision,
      cache_write_1h_usd_per_1m double precision,
      cache_storage_usd_per_1m_per_hour double precision,
      service_tier_prices_json text NOT NULL DEFAULT '{}',
      image_input_usd_per_1m double precision,
      image_output_usd_per_1m double precision,
      audio_input_usd_per_1m double precision,
      audio_output_usd_per_1m double precision,
      output_usd_per_image double precision,
      currency text NOT NULL DEFAULT 'USD',
      pricing_notes text,
      capability_notes text,
      notes text,
      created_by text NOT NULL,
      updated_by text,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      FOREIGN KEY (provider_code) REFERENCES providers(code),
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      CHECK (scope IN ('personal', 'global')),
      CHECK (status IN ('draft', 'active', 'disabled')),
      CHECK (jsonb_typeof(service_tier_prices_json::jsonb) = 'object'),
      CHECK (
        (scope = 'personal' AND system_account_id IS NOT NULL)
        OR (scope = 'global' AND system_account_id IS NULL)
      )
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS provider_default_health_check_models (
      system_account_id text NOT NULL,
      provider_code text NOT NULL,
      model text NOT NULL,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      PRIMARY KEY (system_account_id, provider_code),
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (provider_code) REFERENCES providers(code)
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS provider_system_default_health_check_models (
      provider_code text PRIMARY KEY,
      model text NOT NULL,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      FOREIGN KEY (provider_code) REFERENCES providers(code)
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS proxy_profiles (
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
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS proxy_latency_projection_receipts (
      outcome_id text PRIMARY KEY,
      proxy_id text NOT NULL,
      input_version integer NOT NULL CHECK (input_version >= 1),
      disposition text NOT NULL CHECK (disposition IN ('applied', 'stale', 'ignored', 'rejected')),
      reason text,
      applied_at text NOT NULL
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS proxy_latency_projection_cursors (
      consumer_key text PRIMARY KEY,
      stored_at text,
      outcome_id text,
      updated_at text NOT NULL,
      CHECK ((stored_at IS NULL AND outcome_id IS NULL) OR (stored_at IS NOT NULL AND outcome_id IS NOT NULL))
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS response_inspection_policies (
      id text PRIMARY KEY,
      name text NOT NULL,
      enabled integer NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
      priority integer NOT NULL DEFAULT 100 CHECK (priority BETWEEN 1 AND 9999),
      scope_type text NOT NULL DEFAULT 'protocol',
      protocol_code text NOT NULL,
      provider_code text,
      match_json text NOT NULL CHECK (jsonb_typeof(match_json::jsonb) = 'object'),
      action text NOT NULL,
      notes text,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      CHECK (scope_type IN ('protocol', 'provider')),
      CHECK (action IN ('observe', 'drop_event', 'retry_no_avoidance', 'retry_next_account', 'avoid_account_ttl', 'avoid_upstream_bucket_ttl')),
      CHECK (
        (scope_type = 'protocol' AND provider_code IS NULL)
        OR (scope_type = 'provider' AND provider_code IS NOT NULL)
      )
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS external_integration_sources (
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
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS external_integration_source_tokens (
      id text PRIMARY KEY,
      source_ref_id text NOT NULL,
      name text NOT NULL,
      token_hash text NOT NULL UNIQUE,
      token_secret_encrypted text NOT NULL,
      token_prefix text NOT NULL,
      token_suffix text NOT NULL,
      status text NOT NULL DEFAULT 'active',
      scopes_json text NOT NULL DEFAULT '[]',
      expires_at text,
      last_used_at text,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      revoked_at text,
      FOREIGN KEY (source_ref_id) REFERENCES external_integration_sources(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS model_quality_policies (
      system_account_id text PRIMARY KEY,
      revision integer NOT NULL DEFAULT 1 CHECK (revision >= 1),
      profile text NOT NULL DEFAULT 'quick' CHECK (profile IN ('quick', 'full')),
      manual_enforcement_enabled integer NOT NULL DEFAULT 1 CHECK (manual_enforcement_enabled IN (0, 1)),
      penalty_threshold integer NOT NULL DEFAULT 70 CHECK (penalty_threshold BETWEEN 40 AND 100),
      penalty_action text NOT NULL DEFAULT 'fallback' CHECK (penalty_action IN ('disable', 'fallback', 'quality_isolate')),
      recovery_interval_minutes integer NOT NULL DEFAULT 10 CHECK (recovery_interval_minutes BETWEEN 10 AND 10080),
      created_at text NOT NULL,
      updated_at text NOT NULL,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_health_jobs_input_versions (
      account_id text PRIMARY KEY,
      current_version integer NOT NULL CHECK (current_version >= 1),
      reserved_at text NOT NULL
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_health_jobs_input_outbox (
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
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_health_projection_cursors (
      consumer_key text PRIMARY KEY,
      observed_at text,
      outcome_id text,
      updated_at text NOT NULL,
      CHECK ((observed_at IS NULL AND outcome_id IS NULL) OR (observed_at IS NOT NULL AND outcome_id IS NOT NULL))
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_balance_projection_cursors (
      consumer_key text PRIMARY KEY,
      observed_at text,
      outcome_id text,
      updated_at text NOT NULL,
      CHECK ((observed_at IS NULL AND outcome_id IS NULL) OR (observed_at IS NOT NULL AND outcome_id IS NOT NULL))
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_tags (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      name text NOT NULL,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_list_availability_projection_viewer_health (
      viewer_system_account_id text PRIMARY KEY,
      projection_count integer NOT NULL CHECK (projection_count >= 0),
      oldest_projected_at text,
      next_transition_at text,
      is_current integer NOT NULL CHECK (is_current IN (0, 1)),
      updated_at text NOT NULL,
      FOREIGN KEY (viewer_system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_list_availability_projection_dependency_health (
      dependency_name text PRIMARY KEY CHECK (dependency_name = 'runtime_state'),
      state text NOT NULL CHECK (state IN ('healthy', 'unavailable', 'recovering')),
      generation bigint NOT NULL CHECK (generation >= 1),
      reason text,
      updated_at text NOT NULL
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_test_tasks (
      id text PRIMARY KEY,
      account_id text NOT NULL,
      account_name text NOT NULL,
      provider_code text NOT NULL,
      provider_protocol_profile_id text NOT NULL,
      protocol_code text NOT NULL,
      protocol_version text NOT NULL,
      account_type text NOT NULL,
      request_system_account_id text NOT NULL,
      request_role text NOT NULL,
      request_system_account_filter_id text,
      diagnostics text NOT NULL DEFAULT 'full',
      model text,
      test_endpoint_mode text,
      draft_account_encrypted text,
      status text NOT NULL DEFAULT 'queued',
      status_message text,
      result_json text,
      error_message text,
      cancel_requested boolean NOT NULL DEFAULT false,
      queued_at timestamptz NOT NULL,
      queued_deadline_at timestamptz,
      started_at timestamptz,
      finished_at timestamptz,
      created_at timestamptz NOT NULL,
      updated_at timestamptz NOT NULL
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_test_sessions (
      id text PRIMARY KEY,
      request_system_account_id text NOT NULL,
      request_role text NOT NULL,
      request_system_account_filter_id text,
      status text NOT NULL DEFAULT 'running',
      cancel_reason text,
      last_heartbeat_at timestamptz NOT NULL,
      cancel_requested_at timestamptz,
      finished_at timestamptz,
      created_at timestamptz NOT NULL,
      updated_at timestamptz NOT NULL,
      CHECK (status IN ('running', 'canceled', 'expired', 'completed'))
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_test_session_tasks (
      session_id text NOT NULL,
      task_id text NOT NULL,
      created_at timestamptz NOT NULL,
      PRIMARY KEY (session_id, task_id),
      FOREIGN KEY (session_id) REFERENCES account_test_sessions(id) ON DELETE CASCADE,
      FOREIGN KEY (task_id) REFERENCES account_test_tasks(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS system_teams (
      id text PRIMARY KEY,
      name text NOT NULL,
      description text,
      status text NOT NULL DEFAULT 'active',
      created_by text NOT NULL,
      created_at text NOT NULL,
      updated_at text NOT NULL
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS system_team_members (
      id text PRIMARY KEY,
      team_id text NOT NULL,
      system_account_id text NOT NULL,
      member_role text NOT NULL DEFAULT 'member',
      status text NOT NULL DEFAULT 'active',
      joined_at text NOT NULL,
      removed_at text,
      created_by text NOT NULL,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      FOREIGN KEY (team_id) REFERENCES system_teams(id) ON DELETE CASCADE,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS resource_authorizations (
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
      updated_at text NOT NULL,
      FOREIGN KEY (grantee_system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS accounts (
      id text PRIMARY KEY,
      config_revision integer NOT NULL DEFAULT 1,
      dispatch_revision bigint NOT NULL DEFAULT 1 CHECK (dispatch_revision >= 1),
      circuit_projection_revision bigint NOT NULL DEFAULT 0 CHECK (circuit_projection_revision >= 0 AND circuit_projection_revision <= dispatch_revision),
      system_account_id text NOT NULL,
      provider_code text NOT NULL,
      provider_protocol_profile_id text NOT NULL,
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
      client_compatibility text NOT NULL DEFAULT 'openai_standard',
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
      temporary_unavailable_continuous_probe_enabled integer NOT NULL DEFAULT 1 CHECK (temporary_unavailable_continuous_probe_enabled IN (0, 1)),
      health_check_model text NOT NULL,
      health_check_endpoint_mode text NOT NULL CHECK (health_check_endpoint_mode IN ('images_json', 'chat_json', 'chat_sse', 'responses_json', 'responses_sse', 'messages_json', 'messages_sse', 'generate_content_json', 'generate_content_sse', 'interactions_json', 'interactions_sse')),
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
      updated_at text NOT NULL,
      FOREIGN KEY (provider_code) REFERENCES providers(code),
      FOREIGN KEY (provider_protocol_profile_id) REFERENCES provider_protocol_profiles(id),
      FOREIGN KEY (proxy_profile_id) REFERENCES proxy_profiles(id),
      FOREIGN KEY (authorization_instance_source_account_id) REFERENCES accounts(id),
      FOREIGN KEY (authorization_instance_authorization_id) REFERENCES resource_authorizations(id)
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_lock_states (
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
      updated_at text NOT NULL,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      CHECK ((lock_state = 'UNLOCKED' AND enabled = 0) OR (lock_state <> 'UNLOCKED' AND enabled = 1))
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS model_quality_schedules (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      account_id text NOT NULL,
      model text NOT NULL,
      interval_minutes integer NOT NULL DEFAULT 60 CHECK (interval_minutes BETWEEN 10 AND 10080),
      profile text NOT NULL DEFAULT 'quick' CHECK (profile IN ('quick', 'full')),
      penalty_threshold integer NOT NULL DEFAULT 70 CHECK (penalty_threshold BETWEEN 40 AND 100),
      penalty_action text NOT NULL DEFAULT 'fallback' CHECK (penalty_action IN ('disable', 'fallback', 'quality_isolate')),
      recovery_interval_minutes integer NOT NULL DEFAULT 10 CHECK (recovery_interval_minutes BETWEEN 10 AND 10080),
      enabled integer NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
      revision integer NOT NULL DEFAULT 1 CHECK (revision >= 1),
      next_run_at text NOT NULL,
      last_run_id text,
      last_run_at text,
      last_run_status text CHECK (last_run_status IS NULL OR last_run_status IN ('completed', 'failed', 'canceled')),
      lease_owner text,
      lease_until text,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      UNIQUE (system_account_id, account_id)
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_quality_enforcements (
      account_id text PRIMARY KEY,
      system_account_id text NOT NULL,
      enforcement_id text NOT NULL UNIQUE,
      generation bigint NOT NULL DEFAULT 1 CHECK (generation >= 1),
      state text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'cleared')),
      action text NOT NULL CHECK (action IN ('disable', 'fallback', 'quality_isolate')),
      trigger_run_id text NOT NULL,
      config_source text NOT NULL DEFAULT 'manual' CHECK (config_source IN ('manual', 'schedule')),
      config_source_id text,
      policy_revision integer NOT NULL CHECK (policy_revision >= 0),
      profile text NOT NULL DEFAULT 'quick' CHECK (profile IN ('quick', 'full')),
      penalty_threshold integer NOT NULL DEFAULT 70 CHECK (penalty_threshold BETWEEN 40 AND 100),
      recovery_interval_minutes integer NOT NULL DEFAULT 10 CHECK (recovery_interval_minutes BETWEEN 10 AND 10080),
      recovery_model text,
      account_config_revision integer NOT NULL CHECK (account_config_revision >= 1),
      before_status text NOT NULL,
      after_status text NOT NULL,
      fallback_was_enabled integer NOT NULL DEFAULT 0 CHECK (fallback_was_enabled IN (0, 1)),
      super_priority_was_enabled integer NOT NULL DEFAULT 0 CHECK (super_priority_was_enabled IN (0, 1)),
      started_at text NOT NULL,
      recovery_due_at text,
      recovery_lease_owner text,
      recovery_lease_until text,
      last_recovery_run_id text,
      cleared_at text,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      CHECK (
        (config_source = 'manual' AND config_source_id IS NULL)
        OR
        (config_source = 'schedule' AND config_source_id IS NOT NULL AND length(trim(config_source_id)) > 0)
      ),
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_circuit_incidents (
      circuit_scope_key text PRIMARY KEY,
      account_id text NOT NULL,
      account_runtime_key text NOT NULL,
      scope_kind text NOT NULL CHECK (scope_kind IN ('account', 'key', 'protocol_model', 'key_model')),
      key_fingerprint text,
      protocol_code text,
      request_lane text,
      model_family text,
      client_model text,
      capability_hash text,
      credential_source_account_id text,
      client_endpoint_family text,
      final_upstream_model text,
      upstream_endpoint_mode text,
      incident_id text NOT NULL,
      parent_incident_id text,
      child_incident_ids_json text NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(child_incident_ids_json::jsonb) = 'array'),
      caused_by_terminal_outcome_id text,
      state text NOT NULL CHECK (state IN ('CLOSED', 'SUSPECT', 'OPEN', 'HALF_OPEN', 'RECOVERING', 'PERSISTING', 'SHADOWED_BY_PERSISTENT')),
      failure_scope text CHECK (failure_scope IN ('account', 'key', 'protocol_model', 'key_model')),
      generation bigint NOT NULL CHECK (generation >= 0),
      dispatch_revision bigint NOT NULL CHECK (dispatch_revision >= 1),
      ledger_revision bigint NOT NULL CHECK (ledger_revision >= 1),
      projected_ledger_revision bigint NOT NULL DEFAULT 0 CHECK (projected_ledger_revision >= 0 AND projected_ledger_revision <= ledger_revision),
      transition_id text NOT NULL,
      cooldown_observation_generation integer NOT NULL DEFAULT 0 CHECK (cooldown_observation_generation >= 0),
      open_until_ms bigint,
      next_transition_at_ms bigint,
      lease_id text,
      lease_purpose text CHECK (lease_purpose IN ('confirmation', 'half_open', 'recovery', 'cooldown_retest', 'background_probe')),
      lease_owner_run_id text,
      lease_until_ms bigint,
      attempt_started_at_ms bigint,
      attempt_hard_deadline_ms bigint,
      upstream_attempt_observed integer NOT NULL DEFAULT 0 CHECK (upstream_attempt_observed IN (0, 1)),
      backoff_level integer NOT NULL DEFAULT 0 CHECK (backoff_level >= 0),
      consecutive_failures integer NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
      confirmation_failures_required integer NOT NULL DEFAULT 1 CHECK (confirmation_failures_required BETWEEN 1 AND 5),
      confirmation_failure_evidence_keys_json text NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(confirmation_failure_evidence_keys_json::jsonb) = 'array'),
      recovering_successes integer NOT NULL DEFAULT 0 CHECK (recovering_successes >= 0),
      last_failure_class text CHECK (last_failure_class IN ('connect_failed', 'timeout_before_complete', 'read_interrupted', 'incomplete_response', 'explicit_policy')),
      retained_until_ms bigint,
      created_at_ms bigint NOT NULL,
      updated_at_ms bigint NOT NULL,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      CHECK (length(circuit_scope_key) BETWEEN 1 AND 2048),
      CHECK (length(account_runtime_key) BETWEEN 1 AND 1024),
      CHECK (length(incident_id) BETWEEN 1 AND 256),
      CHECK (length(transition_id) BETWEEN 1 AND 256),
      CHECK (consecutive_failures <= confirmation_failures_required),
      CHECK (jsonb_array_length(confirmation_failure_evidence_keys_json::jsonb) <= confirmation_failures_required + 1),
      CHECK ((scope_kind = 'account' AND key_fingerprint IS NULL AND protocol_code IS NULL AND request_lane IS NULL AND model_family IS NULL AND client_model IS NULL AND capability_hash IS NULL AND credential_source_account_id IS NULL AND client_endpoint_family IS NULL AND final_upstream_model IS NULL AND upstream_endpoint_mode IS NULL)
        OR (scope_kind = 'key' AND key_fingerprint IS NOT NULL AND protocol_code IS NULL AND request_lane IS NULL AND model_family IS NULL AND client_model IS NULL AND capability_hash IS NULL AND credential_source_account_id IS NULL AND client_endpoint_family IS NULL AND final_upstream_model IS NULL AND upstream_endpoint_mode IS NULL)
        OR (scope_kind = 'protocol_model' AND key_fingerprint IS NULL AND protocol_code IS NOT NULL AND request_lane IS NOT NULL AND model_family IS NOT NULL AND client_model IS NULL AND capability_hash IS NULL AND credential_source_account_id IS NULL AND client_endpoint_family IS NULL AND final_upstream_model IS NULL AND upstream_endpoint_mode IS NULL)
        OR (scope_kind = 'key_model' AND key_fingerprint IS NOT NULL AND capability_hash IS NOT NULL AND client_model IS NOT NULL AND credential_source_account_id IS NOT NULL AND client_endpoint_family IS NOT NULL AND final_upstream_model IS NOT NULL AND upstream_endpoint_mode IS NOT NULL AND protocol_code IS NULL AND request_lane IS NULL AND model_family IS NULL)),
      CHECK ((state = 'CLOSED' AND retained_until_ms IS NOT NULL) OR (state <> 'CLOSED' AND retained_until_ms IS NULL))
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_circuit_outbox (
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
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      UNIQUE (projection_key, dedupe_key),
      CHECK (length(event_id) BETWEEN 1 AND 256),
      CHECK (length(projection_key) BETWEEN 1 AND 128),
      CHECK (length(dedupe_key) BETWEEN 1 AND 256),
      CHECK (length(account_runtime_key) BETWEEN 1 AND 1024),
      CHECK (length(transition_id) BETWEEN 1 AND 256),
      CHECK (last_error_class IS NULL OR length(last_error_class) BETWEEN 1 AND 64),
      CHECK ((event_type = 'dispatch_revision_changed' AND circuit_scope_key IS NULL AND incident_id IS NULL AND generation IS NULL AND ledger_revision IS NULL)
        OR (event_type = 'incident_changed' AND circuit_scope_key IS NOT NULL AND incident_id IS NOT NULL AND generation IS NOT NULL AND ledger_revision IS NOT NULL)),
      CHECK ((status = 'pending' AND claim_token IS NULL AND claimed_by IS NULL AND claim_until_ms IS NULL AND acknowledged_at_ms IS NULL)
        OR (status = 'processing' AND claim_token IS NOT NULL AND claimed_by IS NOT NULL AND claim_until_ms IS NOT NULL AND acknowledged_at_ms IS NULL)
        OR (status = 'dispatched' AND claim_token IS NULL AND claimed_by IS NULL AND claim_until_ms IS NULL AND acknowledged_at_ms IS NOT NULL))
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_name_search_terms (
      account_id text NOT NULL,
      system_account_id text NOT NULL,
      term text NOT NULL,
      created_at text NOT NULL,
      PRIMARY KEY (account_id, term),
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_name_search_documents (
      account_id text PRIMARY KEY,
      system_account_id text NOT NULL,
      normalized_name text NOT NULL,
      updated_at text NOT NULL,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_api_key_runtime_states (
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
      updated_at text NOT NULL,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_api_key_pool_probe_cursors (
      account_id text NOT NULL,
      purpose text NOT NULL CHECK (purpose IN ('health_check', 'cooldown_retest')),
      last_completed_key_fingerprint text,
      key_set_fingerprint text NOT NULL,
      config_revision integer NOT NULL,
      dispatch_revision bigint,
      cooldown_generation text,
      source_config_revision integer,
      updated_at text NOT NULL,
      PRIMARY KEY (account_id, purpose),
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_health_projection_receipts (
      outcome_id text PRIMARY KEY,
      account_id text NOT NULL,
      input_version integer NOT NULL CHECK (input_version >= 1),
      disposition text NOT NULL CHECK (disposition IN ('applied', 'stale', 'ignored', 'rejected')),
      reason text,
      applied_at text NOT NULL,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_supported_models (
      account_id text NOT NULL,
      provider_code text NOT NULL,
      model text NOT NULL,
      created_at text NOT NULL,
      PRIMARY KEY (account_id, model),
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (provider_code) REFERENCES providers(code)
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_model_mappings (
      account_id text NOT NULL,
      provider_code text NOT NULL,
      source_model text NOT NULL,
      source_endpoint_family text NOT NULL,
      upstream_model text NOT NULL,
      upstream_endpoint_family text NOT NULL,
      enabled integer NOT NULL DEFAULT 1,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      PRIMARY KEY (account_id, source_model, source_endpoint_family),
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (provider_code) REFERENCES providers(code)
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_tag_bindings (
      account_id text NOT NULL,
      tag_id text NOT NULL,
      system_account_id text NOT NULL,
      created_at text NOT NULL,
      PRIMARY KEY (account_id, tag_id),
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (tag_id) REFERENCES account_tags(id) ON DELETE CASCADE,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_list_availability_projections (
      viewer_system_account_id text NOT NULL,
      account_id text NOT NULL,
      source_account_id text,
      authorization_id text,
      effective_status text NOT NULL,
      schedulable_bucket text NOT NULL CHECK (schedulable_bucket IN ('enabled', 'disabled', 'cooling')),
      provider_code text NOT NULL,
      provider_protocol_profile_id text NOT NULL,
      account_type text NOT NULL,
      bound_group_id text,
      name_sort_key text NOT NULL,
      priority_sort_key integer NOT NULL,
      super_priority_sort_key integer NOT NULL,
      fallback_sort_key integer NOT NULL,
      concurrency_sort_key integer NOT NULL,
      account_expires_at_sort_key text,
      last_used_at_sort_key text,
      created_at_sort_key text NOT NULL,
      payload_json text NOT NULL,
      source_generation integer NOT NULL CHECK (source_generation >= 1),
      next_transition_at text,
      projected_at text NOT NULL,
      PRIMARY KEY (viewer_system_account_id, account_id),
      FOREIGN KEY (viewer_system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (source_account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (authorization_id) REFERENCES resource_authorizations(id) ON DELETE CASCADE,
      FOREIGN KEY (provider_protocol_profile_id) REFERENCES provider_protocol_profiles(id)
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_list_availability_projection_index (
      viewer_system_account_id text NOT NULL,
      account_id text NOT NULL,
      effective_status text NOT NULL,
      schedulable_bucket text NOT NULL CHECK (schedulable_bucket IN ('enabled', 'disabled', 'cooling')),
      provider_code text NOT NULL,
      provider_protocol_profile_id text NOT NULL,
      account_type text NOT NULL,
      bound_group_id text,
      name_sort_key text NOT NULL,
      priority_sort_key integer NOT NULL,
      super_priority_sort_key integer NOT NULL,
      fallback_sort_key integer NOT NULL,
      concurrency_sort_key integer NOT NULL,
      account_expires_at_sort_key text,
      last_used_at_sort_key text,
      created_at_sort_key text NOT NULL,
      access_type_sort_key text NOT NULL,
      search_index_complete integer NOT NULL DEFAULT 0 CHECK (search_index_complete IN (0, 1)),
      authorization_quota_exceeded integer NOT NULL DEFAULT 0 CHECK (authorization_quota_exceeded IN (0, 1)),
      PRIMARY KEY (viewer_system_account_id, account_id),
      FOREIGN KEY (viewer_system_account_id, account_id)
        REFERENCES account_list_availability_projections(viewer_system_account_id, account_id)
        ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_list_availability_projection_tags (
      viewer_system_account_id text NOT NULL,
      account_id text NOT NULL,
      tag_id text NOT NULL,
      PRIMARY KEY (viewer_system_account_id, account_id, tag_id),
      FOREIGN KEY (viewer_system_account_id, account_id)
        REFERENCES account_list_availability_projections(viewer_system_account_id, account_id)
        ON DELETE CASCADE,
      FOREIGN KEY (tag_id) REFERENCES account_tags(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_list_availability_projection_search_terms (
      viewer_system_account_id text NOT NULL,
      account_id text NOT NULL,
      term text NOT NULL,
      name_sort_key text NOT NULL,
      created_at_sort_key text NOT NULL,
      PRIMARY KEY (viewer_system_account_id, account_id, term),
      FOREIGN KEY (viewer_system_account_id, account_id)
        REFERENCES account_list_availability_projections(viewer_system_account_id, account_id)
        ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_list_availability_runtime_overlays (
      account_id text PRIMARY KEY,
      current_concurrency integer NOT NULL CHECK (current_concurrency >= 0),
      observed_at text NOT NULL,
      next_reconcile_at text,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_list_availability_dirty (
      account_id text PRIMARY KEY,
      viewer_system_account_id text NOT NULL,
      generation bigint NOT NULL CHECK (generation >= 1),
      applied_generation integer NOT NULL DEFAULT 0 CHECK (applied_generation >= 0 AND applied_generation <= generation),
      reason text NOT NULL,
      available_at_ms bigint NOT NULL,
      claim_token text,
      claimed_by text,
      claim_until_ms bigint,
      attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
      created_at_ms bigint NOT NULL,
      updated_at_ms bigint NOT NULL,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (viewer_system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      CHECK (
        (claim_token IS NULL AND claimed_by IS NULL AND claim_until_ms IS NULL)
        OR (claim_token IS NOT NULL AND claimed_by IS NOT NULL AND claim_until_ms IS NOT NULL)
      )
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS resource_authorization_sources (
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
      updated_at text NOT NULL,
      FOREIGN KEY (authorization_id) REFERENCES resource_authorizations(id) ON DELETE CASCADE,
      FOREIGN KEY (source_team_id) REFERENCES system_teams(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS resource_authorization_grants (
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
      ),
      FOREIGN KEY (grantee_system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (grantee_team_id) REFERENCES system_teams(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS groups (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      name text NOT NULL,
      provider_code text NOT NULL,
      description text,
      enabled integer NOT NULL DEFAULT 1,
      is_default integer NOT NULL DEFAULT 0,
      group_type text NOT NULL DEFAULT 'personal',
      scheduling_policy_json text,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      FOREIGN KEY (provider_code) REFERENCES providers(code)
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS group_authorization_settings (
      authorization_id text PRIMARY KEY,
      system_account_id text NOT NULL,
      group_id text NOT NULL,
      enabled integer NOT NULL DEFAULT 1,
      group_type text NOT NULL DEFAULT 'personal',
      scheduling_policy_json text,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      FOREIGN KEY (authorization_id) REFERENCES resource_authorizations(id) ON DELETE CASCADE,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS group_accounts (
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
      PRIMARY KEY (group_id, account_id),
      FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE CASCADE,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (account_authorization_id) REFERENCES resource_authorizations(id)
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS group_account_stats_dirty (
      group_id text PRIMARY KEY,
      reason text,
      updated_at text NOT NULL
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS route_strategies (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      name text NOT NULL,
      description text,
      mode text NOT NULL DEFAULT 'normal',
      status text NOT NULL DEFAULT 'active',
      is_default integer NOT NULL DEFAULT 0,
      config_json text,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS route_strategy_groups (
      id text PRIMARY KEY,
      route_strategy_id text NOT NULL,
      system_account_id text NOT NULL,
      group_id text NOT NULL,
      priority integer NOT NULL DEFAULT 1,
      weight integer NOT NULL DEFAULT 1,
      status text NOT NULL DEFAULT 'active',
      created_at text NOT NULL,
      updated_at text NOT NULL,
      FOREIGN KEY (route_strategy_id) REFERENCES route_strategies(id) ON DELETE CASCADE,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS api_keys (
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
      purpose text NOT NULL DEFAULT 'general' CHECK (purpose IN ('general', 'chat')),
      expires_at text,
      quota_limits_json text,
      availability_schedule_json text,
      availability_schedule_next_check_at text,
      last_used_at text,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      FOREIGN KEY (route_strategy_id) REFERENCES route_strategies(id)
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS request_quota_hourly_window_scope_bindings (
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
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS api_key_schedule_status_events (
      event_key text PRIMARY KEY,
      api_key_id text NOT NULL,
      status text NOT NULL,
      executed_at text NOT NULL
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS openai_compatible_files (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      api_key_id text NOT NULL,
      purpose text NOT NULL,
      container_id text,
      filename text NOT NULL,
      bytes bigint NOT NULL,
      media_type text,
      storage_key text NOT NULL UNIQUE,
      sha256 text NOT NULL,
      status text NOT NULL DEFAULT 'processed',
      created_at text NOT NULL,
      updated_at text NOT NULL,
      expires_at text,
      deleted_at text,
      FOREIGN KEY (api_key_id) REFERENCES api_keys(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS openai_compatible_vector_stores (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      api_key_id text NOT NULL,
      name text,
      description text,
      metadata_json text NOT NULL DEFAULT '{}',
      bytes bigint NOT NULL DEFAULT 0,
      status text NOT NULL DEFAULT 'active',
      created_at text NOT NULL,
      updated_at text NOT NULL,
      expires_after_anchor text,
      expires_after_days integer,
      expires_at text,
      deleted_at text,
      FOREIGN KEY (api_key_id) REFERENCES api_keys(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS openai_compatible_vector_store_files (
      vector_store_id text NOT NULL,
      file_id text NOT NULL,
      system_account_id text NOT NULL,
      api_key_id text NOT NULL,
      attributes_json text NOT NULL DEFAULT '{}',
      chunking_strategy_json text NOT NULL DEFAULT '{}',
      status text NOT NULL DEFAULT 'in_progress',
      usage_bytes bigint NOT NULL DEFAULT 0,
      last_error_json text,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      deleted_at text,
      PRIMARY KEY (vector_store_id, file_id),
      FOREIGN KEY (vector_store_id) REFERENCES openai_compatible_vector_stores(id) ON DELETE CASCADE,
      FOREIGN KEY (file_id) REFERENCES openai_compatible_files(id) ON DELETE CASCADE,
      FOREIGN KEY (api_key_id) REFERENCES api_keys(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS openai_compatible_vector_store_chunks (
      id text PRIMARY KEY,
      vector_store_id text NOT NULL,
      file_id text NOT NULL,
      system_account_id text NOT NULL,
      api_key_id text NOT NULL,
      chunk_index integer NOT NULL,
      content_text text NOT NULL,
      content_preview text NOT NULL,
      token_estimate integer NOT NULL DEFAULT 0,
      keyword_index_text text NOT NULL,
      created_at text NOT NULL,
      FOREIGN KEY (vector_store_id, file_id) REFERENCES openai_compatible_vector_store_files(vector_store_id, file_id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS account_schedule_status_events (
      event_key text PRIMARY KEY,
      account_id text NOT NULL,
      status text NOT NULL,
      executed_at text NOT NULL
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS system_settings (
      system_account_id text NOT NULL,
      key text NOT NULL,
      value_json text NOT NULL,
      updated_at text NOT NULL,
      PRIMARY KEY (system_account_id, key),
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS announcements (
      id text PRIMARY KEY,
      title text NOT NULL,
      content text NOT NULL,
      level text NOT NULL DEFAULT 'info',
      status text NOT NULL DEFAULT 'draft',
      created_by text NOT NULL,
      updated_by text,
      published_at text,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      FOREIGN KEY (created_by) REFERENCES system_accounts(id),
      FOREIGN KEY (updated_by) REFERENCES system_accounts(id)
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS announcement_reads (
      announcement_id text NOT NULL,
      system_account_id text NOT NULL,
      read_at text NOT NULL,
      PRIMARY KEY (announcement_id, system_account_id),
      FOREIGN KEY (announcement_id) REFERENCES announcements(id) ON DELETE CASCADE,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS oauth_clients (
      id text PRIMARY KEY,
      client_id text NOT NULL UNIQUE,
      display_name text NOT NULL,
      client_type text NOT NULL CHECK (client_type IN ('public', 'confidential')),
      client_secret_hash text,
      client_secret_ciphertext text,
      redirect_uris_json text NOT NULL,
      allowed_scopes_json text NOT NULL,
      status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
      created_at text NOT NULL,
      updated_at text NOT NULL
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS oauth_grants (
      id text PRIMARY KEY,
      client_id text NOT NULL,
      system_account_id text NOT NULL,
      scopes_json text NOT NULL,
      expires_at text NOT NULL,
      revoked_at text,
      created_at text NOT NULL,
      FOREIGN KEY (client_id) REFERENCES oauth_clients(client_id) ON DELETE CASCADE,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS oauth_authorization_transactions (
      id text PRIMARY KEY,
      client_id text NOT NULL,
      redirect_uri text NOT NULL,
      scopes_json text NOT NULL,
      state_ciphertext text NOT NULL,
      code_challenge text NOT NULL,
      csrf_hash text NOT NULL,
      expires_at text NOT NULL,
      completed_at text,
      created_at text NOT NULL,
      FOREIGN KEY (client_id) REFERENCES oauth_clients(client_id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS oauth_authorization_codes (
      id text PRIMARY KEY,
      code_hash text NOT NULL UNIQUE,
      client_id text NOT NULL,
      grant_id text NOT NULL,
      redirect_uri text NOT NULL,
      code_challenge text NOT NULL,
      expires_at text NOT NULL,
      consumed_at text,
      created_at text NOT NULL,
      FOREIGN KEY (client_id) REFERENCES oauth_clients(client_id) ON DELETE CASCADE,
      FOREIGN KEY (grant_id) REFERENCES oauth_grants(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS oauth_access_tokens (
      id text PRIMARY KEY,
      token_hash text NOT NULL UNIQUE,
      client_id text NOT NULL,
      grant_id text NOT NULL,
      issued_at text NOT NULL,
      expires_at text NOT NULL,
      revoked_at text,
      replaced_at text,
      successor_token_id text,
      created_at text NOT NULL,
      FOREIGN KEY (client_id) REFERENCES oauth_clients(client_id) ON DELETE CASCADE,
      FOREIGN KEY (grant_id) REFERENCES oauth_grants(id) ON DELETE CASCADE,
      FOREIGN KEY (successor_token_id) REFERENCES oauth_access_tokens(id)
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS oauth_authorization_code_oidc_contexts (
      code_id text PRIMARY KEY,
      nonce_ciphertext text NOT NULL,
      created_at text NOT NULL,
      FOREIGN KEY (code_id) REFERENCES oauth_authorization_codes(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS oauth_signing_keys (
      id text PRIMARY KEY,
      kid text NOT NULL UNIQUE,
      private_key_ciphertext text NOT NULL,
      public_jwk_json text NOT NULL,
      status text NOT NULL CHECK (status IN ('active', 'retired')),
      created_at text NOT NULL,
      retired_at text
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE TABLE IF NOT EXISTS oauth_device_authorizations (
      id text PRIMARY KEY,
      client_id text NOT NULL,
      device_code_hash text NOT NULL UNIQUE,
      user_code text NOT NULL UNIQUE,
      verification_uri text NOT NULL,
      scopes_json text NOT NULL,
      nonce_ciphertext text,
      expires_at text NOT NULL,
      interval_seconds integer NOT NULL CHECK (interval_seconds BETWEEN 1 AND 60),
      last_polled_at text,
      csrf_hash text,
      status text NOT NULL CHECK (status IN ('pending', 'approved', 'denied', 'consumed', 'expired')),
      system_account_id text,
      approved_at text,
      denied_at text,
      consumed_at text,
      created_at text NOT NULL,
      FOREIGN KEY (client_id) REFERENCES oauth_clients(client_id) ON DELETE CASCADE,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "account-lock-retry-timestamp-pg-column",
		SQL: `DO $$
BEGIN
  IF to_regclass('juhe_business.account_lock_states') IS NOT NULL THEN
    ALTER TABLE account_lock_states ALTER COLUMN next_retry_at_ms TYPE bigint;
  END IF;
END
$$`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "account-test-task-queued-deadline-pg-column",
		SQL:        `ALTER TABLE account_test_tasks ADD COLUMN IF NOT EXISTS queued_deadline_at timestamptz`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "system-account-ai-account-limit-pg-column",
		SQL:        `ALTER TABLE system_accounts ADD COLUMN IF NOT EXISTS ai_account_limit integer CHECK (ai_account_limit BETWEEN 0 AND 1000000)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "account-list-projection-pg-trigram-extension",
		SQL:        `CREATE EXTENSION IF NOT EXISTS pg_trgm`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "account-list-projection-pg-dirty-triggers",
		SQL: `
CREATE OR REPLACE FUNCTION account_list_availability_mark_dirty_accounts(
  p_account_ids text[],
  p_reason text
) RETURNS void
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
DECLARE
  v_now_ms bigint;
BEGIN
  IF COALESCE(array_length(p_account_ids, 1), 0) = 0 THEN
    RETURN;
  END IF;
  v_now_ms := FLOOR(EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::bigint;
  WITH requested_accounts AS (
    SELECT DISTINCT requested.account_id
    FROM unnest(p_account_ids) AS requested(account_id)
    WHERE account_id IS NOT NULL AND btrim(account_id) <> ''
  ), affected_accounts AS (
    SELECT DISTINCT accounts.id
    FROM accounts
    INNER JOIN requested_accounts
      ON accounts.id = requested_accounts.account_id
        OR accounts.authorization_instance_source_account_id = requested_accounts.account_id
  )
  INSERT INTO account_list_availability_dirty (
    account_id, viewer_system_account_id, generation, applied_generation, reason,
    available_at_ms, claim_token, claimed_by, claim_until_ms, attempt_count,
    created_at_ms, updated_at_ms
  )
  SELECT accounts.id, accounts.system_account_id,
    COALESCE((
      SELECT MAX(projections.source_generation)
      FROM account_list_availability_projections projections
      WHERE projections.account_id = accounts.id
    ), 0) + 1,
    0, left(p_reason, 128), v_now_ms, NULL, NULL, NULL, 0, v_now_ms, v_now_ms
  FROM affected_accounts
  INNER JOIN accounts ON accounts.id = affected_accounts.id
  ON CONFLICT (account_id) DO UPDATE SET
    viewer_system_account_id = excluded.viewer_system_account_id,
    generation = account_list_availability_dirty.generation + 1,
    reason = excluded.reason,
    available_at_ms = LEAST(account_list_availability_dirty.available_at_ms, excluded.available_at_ms),
    claim_token = NULL,
    claimed_by = NULL,
    claim_until_ms = NULL,
    updated_at_ms = excluded.updated_at_ms;
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_mark_dirty_account_family(
  p_account_id text,
  p_reason text
) RETURNS void
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY[p_account_id], p_reason);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_mark_dirty_authorization_family(
  p_authorization_id text,
  p_resource_type text,
  p_resource_id text,
  p_reason text
) RETURNS void
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY(
    SELECT accounts.id
    FROM accounts
    WHERE accounts.authorization_instance_authorization_id = p_authorization_id
       OR (
         p_resource_type = 'account'
         AND (accounts.id = p_resource_id OR accounts.authorization_instance_source_account_id = p_resource_id)
       )
  ), p_reason);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_mark_dirty_group(
  p_group_id text,
  p_reason text
) RETURNS void
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY(
    SELECT group_accounts.account_id
    FROM group_accounts
    WHERE group_accounts.group_id = p_group_id
  ), p_reason);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_mark_dirty_tag(
  p_tag_id text,
  p_reason text
) RETURNS void
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY(
    SELECT account_tag_bindings.account_id
    FROM account_tag_bindings
    WHERE account_tag_bindings.tag_id = p_tag_id
  ), p_reason);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_mark_dirty_proxy(
  p_proxy_id text,
  p_reason text
) RETURNS void
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY(
    SELECT accounts.id FROM accounts WHERE accounts.proxy_profile_id = p_proxy_id
  ), p_reason);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_mark_dirty_profile(
  p_profile_id text,
  p_reason text
) RETURNS void
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY(
    SELECT accounts.id FROM accounts WHERE accounts.provider_protocol_profile_id = p_profile_id
  ), p_reason);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_mark_dirty_quota_crossing(
  p_scope_type text,
  p_scope_id text,
  p_period text,
  p_old_cost double precision,
  p_new_cost double precision
) RETURNS void
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  IF p_scope_type NOT IN ('account_authorization', 'account_authorization_team') THEN
    RETURN;
  END IF;
  PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY(
    SELECT accounts.id
    FROM accounts
    INNER JOIN resource_authorizations authorizations
      ON authorizations.id = accounts.authorization_instance_authorization_id
    LEFT JOIN resource_authorization_grants team_grants
      ON p_scope_type = 'account_authorization_team'
      AND team_grants.resource_type = authorizations.resource_type
      AND team_grants.resource_id = authorizations.resource_id
      AND team_grants.grantee_type = 'team'
      AND team_grants.grantee_team_id = authorizations.effective_source_team_id
      AND team_grants.status = 'active'
      AND (team_grants.expires_at IS NULL OR team_grants.expires_at > to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'))
    WHERE ((
          p_scope_type = 'account_authorization'
          AND authorizations.id = p_scope_id
        ) OR (
          p_scope_type = 'account_authorization_team'
          AND accounts.id || ':' || authorizations.effective_source_team_id = p_scope_id
        ))
      AND COALESCE(((CASE
        WHEN p_scope_type = 'account_authorization_team' THEN team_grants.limits_json
        ELSE authorizations.limits_json
      END)::jsonb -> p_period ->> 'enabled')::boolean, false)
      AND (
        COALESCE(p_old_cost, 0) >= COALESCE(((CASE
          WHEN p_scope_type = 'account_authorization_team' THEN team_grants.limits_json
          ELSE authorizations.limits_json
        END)::jsonb -> p_period ->> 'limit')::double precision, 0)
      ) IS DISTINCT FROM (
        COALESCE(p_new_cost, 0) >= COALESCE(((CASE
          WHEN p_scope_type = 'account_authorization_team' THEN team_grants.limits_json
          ELSE authorizations.limits_json
        END)::jsonb -> p_period ->> 'limit')::double precision, 0)
      )
  ), 'authorization_quota_' || p_period || '_crossed');
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_accounts_insert_dirty_statement_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
DECLARE
  v_account_ids text[];
BEGIN
  SELECT array_agg(id) INTO v_account_ids FROM new_accounts;
  PERFORM juhe_business.account_list_availability_mark_dirty_accounts(v_account_ids, 'account_fact_changed');
  RETURN NULL;
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_accounts_update_dirty_statement_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
DECLARE
  v_account_ids text[];
BEGIN
  SELECT array_agg(new_accounts.id) INTO v_account_ids
  FROM new_accounts
  INNER JOIN old_accounts USING (id)
  WHERE ROW(
    new_accounts.config_revision,
    new_accounts.system_account_id,
    new_accounts.provider_code,
    new_accounts.provider_protocol_profile_id,
    new_accounts.protocol_code,
    new_accounts.protocol_version,
    new_accounts.name,
    new_accounts.type,
    new_accounts.status,
    new_accounts.proxy_profile_id,
    new_accounts.concurrency_limit,
    new_accounts.priority,
    new_accounts.super_priority_enabled,
    new_accounts.fallback_enabled,
    new_accounts.client_compatibility,
    new_accounts.schedulable,
    new_accounts.availability_schedule_json,
    new_accounts.availability_schedule_next_check_at,
    new_accounts.notes,
    new_accounts.account_expires_at,
    new_accounts.cooldown_until,
    new_accounts.last_error_code,
    new_accounts.last_error_message,
    new_accounts.last_error_trace_id,
    new_accounts.cooldown_retest_failure_count,
    new_accounts.cooldown_retest_observation_started_at,
    new_accounts.cooldown_retest_last_at,
    new_accounts.cooldown_retest_last_status_code,
    new_accounts.temporary_unavailable_continuous_probe_enabled,
    new_accounts.health_check_model,
    new_accounts.health_check_endpoint_mode,
    new_accounts.last_health_check_at,
    new_accounts.next_health_check_at,
    new_accounts.last_health_success_at,
    new_accounts.health_check_failure_count,
    new_accounts.health_check_failure_started_at,
    new_accounts.last_health_check_status_code,
    new_accounts.last_health_check_error_code,
    new_accounts.last_health_check_error_message,
    new_accounts.last_health_check_trace_id,
    new_accounts.stream_failure_count,
    new_accounts.stream_failure_window_started_at,
    new_accounts.balance_query_enabled,
    new_accounts.balance_query_config_json,
    new_accounts.balance_query_next_refresh_at,
    new_accounts.authorization_instance_source_account_id,
    new_accounts.authorization_instance_authorization_id,
    new_accounts.authorization_instance_owner_system_account_id,
    new_accounts.deleted_at
  ) IS DISTINCT FROM ROW(
    old_accounts.config_revision,
    old_accounts.system_account_id,
    old_accounts.provider_code,
    old_accounts.provider_protocol_profile_id,
    old_accounts.protocol_code,
    old_accounts.protocol_version,
    old_accounts.name,
    old_accounts.type,
    old_accounts.status,
    old_accounts.proxy_profile_id,
    old_accounts.concurrency_limit,
    old_accounts.priority,
    old_accounts.super_priority_enabled,
    old_accounts.fallback_enabled,
    old_accounts.client_compatibility,
    old_accounts.schedulable,
    old_accounts.availability_schedule_json,
    old_accounts.availability_schedule_next_check_at,
    old_accounts.notes,
    old_accounts.account_expires_at,
    old_accounts.cooldown_until,
    old_accounts.last_error_code,
    old_accounts.last_error_message,
    old_accounts.last_error_trace_id,
    old_accounts.cooldown_retest_failure_count,
    old_accounts.cooldown_retest_observation_started_at,
    old_accounts.cooldown_retest_last_at,
    old_accounts.cooldown_retest_last_status_code,
    old_accounts.temporary_unavailable_continuous_probe_enabled,
    old_accounts.health_check_model,
    old_accounts.health_check_endpoint_mode,
    old_accounts.last_health_check_at,
    old_accounts.next_health_check_at,
    old_accounts.last_health_success_at,
    old_accounts.health_check_failure_count,
    old_accounts.health_check_failure_started_at,
    old_accounts.last_health_check_status_code,
    old_accounts.last_health_check_error_code,
    old_accounts.last_health_check_error_message,
    old_accounts.last_health_check_trace_id,
    old_accounts.stream_failure_count,
    old_accounts.stream_failure_window_started_at,
    old_accounts.balance_query_enabled,
    old_accounts.balance_query_config_json,
    old_accounts.balance_query_next_refresh_at,
    old_accounts.authorization_instance_source_account_id,
    old_accounts.authorization_instance_authorization_id,
    old_accounts.authorization_instance_owner_system_account_id,
    old_accounts.deleted_at
  );
  PERFORM juhe_business.account_list_availability_mark_dirty_accounts(v_account_ids, 'account_fact_changed');
  RETURN NULL;
END;
$function$;

/**
 * Usage traffic updates last_used_at continuously. It is a displayed/sorted
 * telemetry value, not an availability decision, so update that projection
 * column in place instead of making the whole viewer unavailable for every
 * gateway request.
 */
CREATE OR REPLACE FUNCTION account_list_availability_accounts_last_used_projection_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  UPDATE account_list_availability_projections projections
  SET last_used_at_sort_key = NEW.last_used_at,
      payload_json = CASE
        WHEN NEW.last_used_at IS NULL THEN (projections.payload_json::jsonb - 'lastUsedAt')::text
        ELSE jsonb_set(
          projections.payload_json::jsonb,
          '{lastUsedAt}',
          to_jsonb(NEW.last_used_at),
          true
        )::text
      END
  WHERE projections.account_id = NEW.id;
  RETURN NEW;
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_authorizations_dirty_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  IF TG_OP <> 'DELETE' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_authorization_family(
      NEW.id, NEW.resource_type, NEW.resource_id, 'authorization_fact_changed'
    );
  END IF;
  IF TG_OP <> 'INSERT' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_authorization_family(
      OLD.id, OLD.resource_type, OLD.resource_id, 'authorization_fact_changed'
    );
  END IF;
  RETURN COALESCE(NEW, OLD);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_authorization_sources_dirty_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  IF TG_OP <> 'DELETE' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_authorization_family(NEW.authorization_id, '', '', 'authorization_source_changed');
  END IF;
  IF TG_OP <> 'INSERT' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_authorization_family(OLD.authorization_id, '', '', 'authorization_source_changed');
  END IF;
  RETURN COALESCE(NEW, OLD);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_authorization_grants_dirty_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  IF TG_OP <> 'DELETE' AND NEW.grantee_type = 'team' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY(
      SELECT accounts.id
      FROM accounts
      INNER JOIN resource_authorizations authorizations
        ON authorizations.id = accounts.authorization_instance_authorization_id
      WHERE authorizations.resource_type = NEW.resource_type
        AND authorizations.resource_id = NEW.resource_id
        AND authorizations.effective_source_team_id = NEW.grantee_team_id
    ), 'authorization_team_grant_changed');
  END IF;
  IF TG_OP <> 'INSERT' AND OLD.grantee_type = 'team' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY(
      SELECT accounts.id
      FROM accounts
      INNER JOIN resource_authorizations authorizations
        ON authorizations.id = accounts.authorization_instance_authorization_id
      WHERE authorizations.resource_type = OLD.resource_type
        AND authorizations.resource_id = OLD.resource_id
        AND authorizations.effective_source_team_id = OLD.grantee_team_id
    ), 'authorization_team_grant_changed');
  END IF;
  RETURN COALESCE(NEW, OLD);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_group_accounts_dirty_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  IF TG_OP <> 'DELETE' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY[NEW.account_id], 'group_binding_changed');
  END IF;
  IF TG_OP <> 'INSERT' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY[OLD.account_id], 'group_binding_changed');
  END IF;
  RETURN COALESCE(NEW, OLD);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_groups_dirty_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  PERFORM juhe_business.account_list_availability_mark_dirty_group(COALESCE(NEW.id, OLD.id), 'group_fact_changed');
  RETURN COALESCE(NEW, OLD);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_tag_bindings_dirty_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  IF TG_OP <> 'DELETE' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY[NEW.account_id], 'tag_binding_changed');
  END IF;
  IF TG_OP <> 'INSERT' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY[OLD.account_id], 'tag_binding_changed');
  END IF;
  RETURN COALESCE(NEW, OLD);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_tags_dirty_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  IF TG_OP <> 'DELETE' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_tag(NEW.id, 'tag_fact_changed');
  END IF;
  IF TG_OP <> 'INSERT' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_tag(OLD.id, 'tag_fact_changed');
  END IF;
  RETURN COALESCE(NEW, OLD);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_name_search_dirty_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
DECLARE
  v_account_id text;
BEGIN
  v_account_id := CASE WHEN TG_OP = 'DELETE' THEN OLD.account_id ELSE NEW.account_id END;
  PERFORM juhe_business.account_list_availability_mark_dirty_accounts(
    ARRAY[v_account_id], 'account_name_search_changed'
  );
  RETURN COALESCE(NEW, OLD);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_runtime_state_dirty_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY[COALESCE(NEW.account_id, OLD.account_id)], 'api_key_runtime_changed');
  RETURN COALESCE(NEW, OLD);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_circuit_dirty_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  PERFORM juhe_business.account_list_availability_mark_dirty_account_family(COALESCE(NEW.account_id, OLD.account_id), 'circuit_changed');
  RETURN COALESCE(NEW, OLD);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_proxy_dirty_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  IF TG_OP <> 'DELETE' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_proxy(NEW.id, 'proxy_fact_changed');
  END IF;
  IF TG_OP <> 'INSERT' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_proxy(OLD.id, 'proxy_fact_changed');
  END IF;
  RETURN COALESCE(NEW, OLD);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_profile_dirty_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  IF TG_OP <> 'DELETE' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_profile(NEW.id, 'profile_fact_changed');
  END IF;
  IF TG_OP <> 'INSERT' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_profile(OLD.id, 'profile_fact_changed');
  END IF;
  RETURN COALESCE(NEW, OLD);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_system_account_health_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  INSERT INTO account_list_availability_projection_viewer_health (
    viewer_system_account_id, projection_count, oldest_projected_at,
    next_transition_at, is_current, updated_at
  ) VALUES (NEW.id, 0, NULL, NULL, 1, to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'))
  ON CONFLICT(viewer_system_account_id) DO NOTHING;
  RETURN NEW;
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_projection_delete_health_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  INSERT INTO account_list_availability_projection_viewer_health (
    viewer_system_account_id, projection_count, oldest_projected_at,
    next_transition_at, is_current, updated_at
  ) VALUES (OLD.viewer_system_account_id, 0, NULL, NULL, 0, to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'))
  ON CONFLICT(viewer_system_account_id) DO UPDATE SET
    is_current = 0,
    updated_at = excluded.updated_at;
  RETURN OLD;
END;
$function$;

DROP TRIGGER IF EXISTS account_list_availability_accounts_insert ON accounts;
CREATE TRIGGER account_list_availability_accounts_insert
AFTER INSERT ON accounts
REFERENCING NEW TABLE AS new_accounts
FOR EACH STATEMENT EXECUTE FUNCTION account_list_availability_accounts_insert_dirty_statement_trigger();
DROP TRIGGER IF EXISTS account_list_availability_accounts_update ON accounts;
CREATE TRIGGER account_list_availability_accounts_update
AFTER UPDATE ON accounts
REFERENCING OLD TABLE AS old_accounts NEW TABLE AS new_accounts
FOR EACH STATEMENT EXECUTE FUNCTION account_list_availability_accounts_update_dirty_statement_trigger();
DROP TRIGGER IF EXISTS account_list_availability_accounts_last_used ON accounts;
CREATE TRIGGER account_list_availability_accounts_last_used
AFTER UPDATE OF last_used_at ON accounts
FOR EACH ROW EXECUTE FUNCTION account_list_availability_accounts_last_used_projection_trigger();
DROP TRIGGER IF EXISTS account_list_availability_authorizations ON resource_authorizations;
CREATE TRIGGER account_list_availability_authorizations
AFTER INSERT OR UPDATE OR DELETE ON resource_authorizations
FOR EACH ROW EXECUTE FUNCTION account_list_availability_authorizations_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_authorization_sources ON resource_authorization_sources;
CREATE TRIGGER account_list_availability_authorization_sources
AFTER INSERT OR UPDATE OR DELETE ON resource_authorization_sources
FOR EACH ROW EXECUTE FUNCTION account_list_availability_authorization_sources_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_authorization_grants ON resource_authorization_grants;
CREATE TRIGGER account_list_availability_authorization_grants
AFTER INSERT OR UPDATE OR DELETE ON resource_authorization_grants
FOR EACH ROW EXECUTE FUNCTION account_list_availability_authorization_grants_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_group_accounts ON group_accounts;
CREATE TRIGGER account_list_availability_group_accounts
AFTER INSERT OR UPDATE OR DELETE ON group_accounts
FOR EACH ROW EXECUTE FUNCTION account_list_availability_group_accounts_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_groups ON groups;
CREATE TRIGGER account_list_availability_groups
AFTER UPDATE OR DELETE ON groups
FOR EACH ROW EXECUTE FUNCTION account_list_availability_groups_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_tag_bindings ON account_tag_bindings;
CREATE TRIGGER account_list_availability_tag_bindings
AFTER INSERT OR UPDATE OR DELETE ON account_tag_bindings
FOR EACH ROW EXECUTE FUNCTION account_list_availability_tag_bindings_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_tags ON account_tags;
CREATE TRIGGER account_list_availability_tags
AFTER UPDATE OR DELETE ON account_tags
FOR EACH ROW EXECUTE FUNCTION account_list_availability_tags_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_name_search_documents ON account_name_search_documents;
CREATE TRIGGER account_list_availability_name_search_documents
AFTER INSERT OR UPDATE OR DELETE ON account_name_search_documents
FOR EACH ROW EXECUTE FUNCTION account_list_availability_name_search_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_name_search_terms ON account_name_search_terms;
CREATE TRIGGER account_list_availability_name_search_terms
AFTER INSERT OR UPDATE OR DELETE ON account_name_search_terms
FOR EACH ROW EXECUTE FUNCTION account_list_availability_name_search_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_api_key_runtime ON account_api_key_runtime_states;
CREATE TRIGGER account_list_availability_api_key_runtime
AFTER INSERT OR UPDATE OR DELETE ON account_api_key_runtime_states
FOR EACH ROW EXECUTE FUNCTION account_list_availability_runtime_state_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_circuits ON account_circuit_incidents;
CREATE TRIGGER account_list_availability_circuits
AFTER INSERT OR UPDATE OR DELETE ON account_circuit_incidents
FOR EACH ROW EXECUTE FUNCTION account_list_availability_circuit_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_proxies ON proxy_profiles;
CREATE TRIGGER account_list_availability_proxies
AFTER UPDATE OR DELETE ON proxy_profiles
FOR EACH ROW EXECUTE FUNCTION account_list_availability_proxy_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_profiles ON provider_protocol_profiles;
CREATE TRIGGER account_list_availability_profiles
AFTER UPDATE OR DELETE ON provider_protocol_profiles
FOR EACH ROW EXECUTE FUNCTION account_list_availability_profile_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_system_account_health ON system_accounts;
CREATE TRIGGER account_list_availability_system_account_health
AFTER INSERT ON system_accounts
FOR EACH ROW EXECUTE FUNCTION account_list_availability_system_account_health_trigger();
DROP TRIGGER IF EXISTS account_list_availability_projection_delete_health ON account_list_availability_projections;
CREATE TRIGGER account_list_availability_projection_delete_health
AFTER DELETE ON account_list_availability_projections
FOR EACH ROW EXECUTE FUNCTION account_list_availability_projection_delete_health_trigger();
`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_provider_model_catalog_lookup
      ON provider_model_catalog(provider_code, status, catalog_visible, catalog_order, model)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_lock_states_deadline ON account_lock_states(lock_state, deadline_at)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_health_jobs_input_outbox_pending
      ON account_health_jobs_input_outbox(status, available_at, created_at, event_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_health_jobs_input_outbox_account
      ON account_health_jobs_input_outbox(account_id, input_version DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_provider_status ON accounts(provider_code, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_protocol_profile_status ON accounts(provider_protocol_profile_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_groups_provider ON groups(provider_code)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_sessions_expires_at ON system_sessions(expires_at)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_system_accounts_username_unique_lower ON system_accounts(lower(username))`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_system_accounts_display_name_unique_lower ON system_accounts(lower(display_name))`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_response_inspection_policies_enabled_priority ON response_inspection_policies(enabled, priority, updated_at DESC, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_external_integration_sources_updated ON external_integration_sources(updated_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_external_integration_sources_status_updated ON external_integration_sources(status, updated_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_external_integration_sources_name_lookup ON external_integration_sources(lower(name), id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_external_integration_source_tokens_source ON external_integration_source_tokens(source_ref_id, status, expires_at)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_accounts_updated_lookup ON system_accounts(updated_at, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_accounts_username_lookup ON system_accounts(lower(username), id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_accounts_display_name_lookup ON system_accounts(lower(display_name), id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_credential_fingerprint ON accounts(credential_fingerprint) WHERE credential_fingerprint IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_owner_name_unique ON accounts(system_account_id, name) WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_owner_all_name_lookup
      ON accounts(system_account_id, name, id)
      WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_owner_name_lookup
      ON accounts(system_account_id, name, id)
      WHERE deleted_at IS NULL AND authorization_instance_authorization_id IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_name_lookup ON accounts(name, id) WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_system_account_name_lookup ON accounts(system_account_id, name, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_name_search_terms_term_owner
      ON account_name_search_terms(term, system_account_id, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_name_search_terms_owner_term
      ON account_name_search_terms(system_account_id, term, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_name_search_terms_account
      ON account_name_search_terms(account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_name_search_documents_owner
      ON account_name_search_documents(system_account_id, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_priority
      ON account_list_availability_projections(
        viewer_system_account_id,
        priority_sort_key ASC,
        created_at_sort_key ASC,
        account_id ASC
      )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_name
      ON account_list_availability_projections(
        viewer_system_account_id,
        name_sort_key ASC,
        created_at_sort_key ASC,
        account_id ASC
      )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_schedulable_priority
      ON account_list_availability_projections(
        viewer_system_account_id,
        schedulable_bucket,
        priority_sort_key ASC,
        created_at_sort_key ASC,
        account_id ASC
      )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_due
      ON account_list_availability_projections(next_transition_at ASC, viewer_system_account_id, account_id)
      WHERE next_transition_at IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_tags_lookup
      ON account_list_availability_projection_tags(viewer_system_account_id, tag_id, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_search_terms_lookup
      ON account_list_availability_projection_search_terms(viewer_system_account_id, term, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_search_terms_name_order
      ON account_list_availability_projection_search_terms(
        viewer_system_account_id,
        term,
        name_sort_key ASC,
        created_at_sort_key ASC,
        account_id ASC
      )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_index_priority
      ON account_list_availability_projection_index(
        viewer_system_account_id,
        priority_sort_key ASC,
        created_at_sort_key ASC,
        account_id ASC
      )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_index_name
      ON account_list_availability_projection_index(
        viewer_system_account_id,
        name_sort_key ASC,
        created_at_sort_key ASC,
        account_id ASC
      )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_index_name_search_incomplete
      ON account_list_availability_projection_index(
        viewer_system_account_id,
        name_sort_key ASC,
        created_at_sort_key ASC,
        account_id ASC
      )
      WHERE search_index_complete = 0`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_index_schedulable_priority
      ON account_list_availability_projection_index(
        viewer_system_account_id,
        schedulable_bucket,
        priority_sort_key ASC,
        created_at_sort_key ASC,
        account_id ASC
      )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_viewer_health_refresh
      ON account_list_availability_projection_viewer_health(is_current, updated_at ASC, viewer_system_account_id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_dirty_claim
      ON account_list_availability_dirty(available_at_ms ASC, created_at_ms ASC, account_id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_dirty_viewer
      ON account_list_availability_dirty(viewer_system_account_id, available_at_ms ASC, account_id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_viewer_projected
      ON account_list_availability_projections(viewer_system_account_id, projected_at ASC, account_id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_viewer_transition
      ON account_list_availability_projections(viewer_system_account_id, next_transition_at ASC, account_id ASC)
      WHERE next_transition_at IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_runtime_overlay_due
      ON account_list_availability_runtime_overlays(next_reconcile_at ASC, account_id ASC)
      WHERE next_reconcile_at IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_provider_lookup ON accounts(provider_code, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_protocol_profile_lookup ON accounts(provider_protocol_profile_id, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_system_account_provider_lookup ON accounts(system_account_id, provider_code, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_system_account_protocol_profile_lookup ON accounts(system_account_id, provider_protocol_profile_id, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_type_lookup ON accounts(type, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_system_account_type_lookup ON accounts(system_account_id, type, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_system_account ON accounts(system_account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_owner_list_order
      ON accounts(system_account_id, priority ASC, created_at ASC, id ASC)
      WHERE deleted_at IS NULL AND authorization_instance_authorization_id IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_proxy_profile ON accounts(proxy_profile_id, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_system_account_last_used ON accounts(system_account_id, last_used_at)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_health_monitor_order
      ON accounts((last_used_at IS NULL) ASC, last_used_at DESC, name ASC, id ASC)
      WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_owner_health_monitor_order
      ON accounts(system_account_id, (last_used_at IS NULL) ASC, last_used_at DESC, name ASC, id ASC)
      WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_system_account_concurrency ON accounts(system_account_id, concurrency_limit)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_expiry_sweep
      ON accounts(account_expires_at ASC, updated_at ASC, id ASC)
      WHERE account_expires_at IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_owner_expiry_sweep
      ON accounts(system_account_id, account_expires_at ASC, updated_at ASC, id ASC)
      WHERE account_expires_at IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_availability_schedule_next_check
      ON accounts(availability_schedule_next_check_at ASC, id ASC)
      WHERE availability_schedule_json IS NOT NULL AND deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_super_priority ON accounts(super_priority_enabled, status, priority)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_dispatch_priority ON accounts(fallback_enabled, super_priority_enabled, status, priority)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_openai_oauth_refresh_due
      ON accounts(provider_code, type, oauth_refresh_token_present, oauth_access_token_expires_at, status, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_openai_oauth_refresh_pg_due
      ON accounts(provider_protocol_profile_id, type, oauth_refresh_token_present, (oauth_access_token_expires_at IS NOT NULL), oauth_access_token_expires_at ASC, updated_at ASC, id ASC)
      WHERE authorization_instance_authorization_id IS NULL AND deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_health_check_due
      ON accounts(status, next_health_check_at, updated_at, id)
      WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_health_check_candidate_order
      ON accounts(
        (CASE WHEN status = 'pending_test' THEN 0 ELSE 1 END) ASC,
        (CASE WHEN status = 'pending_test' THEN updated_at END) DESC,
        (next_health_check_at IS NOT NULL) ASC,
        next_health_check_at ASC,
        last_health_check_at ASC,
        created_at ASC,
        id ASC
      )
      WHERE deleted_at IS NULL
        AND status IN ('active', 'pending_test')
        AND (status = 'pending_test' OR schedulable = 1)
        AND type IN ('api_key', 'oauth', 'google_oauth')`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_model_quality_schedules_due
      ON model_quality_schedules(enabled, next_run_at, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_model_quality_schedules_scope
      ON model_quality_schedules(system_account_id, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_quality_enforcements_recovery
      ON account_quality_enforcements(state, action, recovery_due_at, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_quality_enforcements_scope
      ON account_quality_enforcements(system_account_id, updated_at DESC, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_cooldown_retest_candidate_order
      ON accounts(cooldown_until ASC, priority ASC, created_at ASC, id ASC, health_check_endpoint_mode)
      WHERE deleted_at IS NULL
        AND cooldown_until IS NOT NULL
        AND schedulable = 1
        AND type IN ('api_key', 'oauth', 'google_oauth')
        AND status IN ('temporary_unavailable', 'rate_limited')`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_cooldown_retest_legacy_repair_order
      ON accounts(cooldown_until ASC, priority ASC, created_at ASC, id ASC)
      WHERE deleted_at IS NULL
        AND cooldown_until IS NOT NULL
        AND type IN ('api_key', 'oauth', 'google_oauth')
        AND status IN ('temporary_unavailable', 'rate_limited')`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_deleted_cleanup
      ON accounts(deleted_at ASC, updated_at ASC, id ASC)
      WHERE deleted_at IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_health_projection_receipts_account
      ON account_health_projection_receipts(account_id, applied_at DESC, outcome_id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_health_projection_cursors_updated
      ON account_health_projection_cursors(updated_at ASC, consumer_key ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_health_jobs_input_versions_reserved
      ON account_health_jobs_input_versions(reserved_at ASC, account_id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_balance_query_due
      ON accounts(balance_query_next_refresh_at ASC, id ASC)
      WHERE balance_query_enabled = 1
        AND deleted_at IS NULL
        AND authorization_instance_authorization_id IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_balance_auto_detect_due
      ON accounts(balance_query_next_refresh_at ASC, id ASC)
      WHERE status = 'active'
        AND schedulable = 1
        AND type = 'api_key'
        AND balance_query_enabled = 0
        AND balance_query_config_json = '{}'
        AND deleted_at IS NULL
        AND authorization_instance_authorization_id IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE UNIQUE INDEX IF NOT EXISTS idx_account_api_key_runtime_unique
      ON account_api_key_runtime_states(account_id, key_fingerprint)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_api_key_runtime_status
      ON account_api_key_runtime_states(account_id, status, cooldown_until)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_api_key_runtime_probe
      ON account_api_key_runtime_states(account_id, status, next_probe_at ASC, updated_at ASC, key_index ASC)
      WHERE next_probe_at IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_api_key_runtime_probe_claim
      ON account_api_key_runtime_states(status, next_probe_at ASC, probe_claimed_until ASC)
      WHERE next_probe_at IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_api_key_runtime_owner
      ON account_api_key_runtime_states(system_account_id, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE UNIQUE INDEX IF NOT EXISTS idx_custom_provider_models_personal_unique
      ON custom_provider_models(provider_code, system_account_id, model)
      WHERE scope = 'personal'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE UNIQUE INDEX IF NOT EXISTS idx_custom_provider_models_global_unique
      ON custom_provider_models(provider_code, model)
      WHERE scope = 'global'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_custom_provider_models_catalog_lookup
      ON custom_provider_models(provider_code, status, catalog_visible, scope, system_account_id, model)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_provider_default_health_check_models_model
      ON provider_default_health_check_models(provider_code, model, system_account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_provider_system_default_health_check_models_model
      ON provider_system_default_health_check_models(model, provider_code)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_supported_models_provider_model ON account_supported_models(provider_code, model, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_model_mappings_source ON account_model_mappings(provider_code, source_model, source_endpoint_family, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_model_mappings_upstream ON account_model_mappings(provider_code, upstream_model, upstream_endpoint_family, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_account_tags_owner_name_unique ON account_tags(system_account_id, name)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_tags_owner_name_lookup ON account_tags(system_account_id, name, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_tag_bindings_owner_tag ON account_tag_bindings(system_account_id, tag_id, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_tag_bindings_tag_owner ON account_tag_bindings(tag_id, system_account_id, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_tag_bindings_tag ON account_tag_bindings(tag_id, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_test_tasks_request_updated ON account_test_tasks(request_system_account_id, updated_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_test_tasks_status_queued ON account_test_tasks(status, queued_at ASC, id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_test_tasks_finished_cleanup ON account_test_tasks(finished_at ASC, id ASC) WHERE finished_at IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_test_sessions_request_updated ON account_test_sessions(request_system_account_id, updated_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_test_sessions_status_heartbeat ON account_test_sessions(status, last_heartbeat_at ASC, id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_test_session_tasks_task ON account_test_session_tasks(task_id, session_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_test_session_tasks_session ON account_test_session_tasks(session_id, task_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_groups_system_account ON groups(system_account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_groups_updated ON groups(updated_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_groups_system_account_updated ON groups(system_account_id, updated_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_groups_owner_provider_name_unique ON groups(system_account_id, provider_code, name)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_groups_name_lookup ON groups(name, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_groups_system_account_name_lookup ON groups(system_account_id, name, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_groups_provider_name_lookup ON groups(provider_code, name, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_groups_system_account_provider_name_lookup ON groups(system_account_id, provider_code, name, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_groups_owner_provider_default_unique ON groups(system_account_id, provider_code) WHERE is_default = 1`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_teams_status ON system_teams(status, updated_at)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_system_teams_name_unique ON system_teams(name)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_teams_name_lookup ON system_teams(name, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_team_members_team ON system_team_members(team_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_teams_list_order ON system_teams(status, updated_at DESC, name ASC, id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_team_members_team_status_joined ON system_team_members(team_id, status, joined_at ASC, id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_team_members_account ON system_team_members(system_account_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_system_team_members_active_unique ON system_team_members(team_id, system_account_id) WHERE status = 'active'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorizations_resource ON resource_authorizations(resource_type, resource_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorizations_owner ON resource_authorizations(resource_owner_system_account_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorizations_grantee ON resource_authorizations(grantee_system_account_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorizations_expires_at ON resource_authorizations(expires_at, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_resource_authorizations_quota_snapshot
      ON resource_authorizations(status, updated_at DESC, id)
      WHERE limits_json IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorizations_user_unique ON resource_authorizations(resource_type, resource_id, grantee_system_account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_sources_authorization ON resource_authorization_sources(authorization_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_sources_team ON resource_authorization_sources(source_team_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_group_accounts_account_authorization ON group_accounts(account_authorization_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_group_accounts_owner_group_enabled ON group_accounts(system_account_id, group_id, enabled, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_group_accounts_group_enabled ON group_accounts(group_id, enabled, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_group_accounts_dispatch_candidate_window
      ON group_accounts(group_id, system_account_id, enabled, local_fallback_enabled ASC, local_super_priority_enabled DESC, local_priority ASC, created_at ASC, account_id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_group_accounts_account_scope_enabled ON group_accounts(account_id, system_account_id, enabled)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_group_accounts_scope_enabled_updated ON group_accounts(system_account_id, account_id, enabled, updated_at DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_group_authorization_settings_scope_group
      ON group_authorization_settings(system_account_id, group_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_group_account_stats_dirty_updated ON group_account_stats_dirty(updated_at)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_api_keys_route_strategy ON api_keys(route_strategy_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_api_keys_default_updated ON api_keys(is_default DESC, updated_at DESC, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_api_keys_system_account_default_updated ON api_keys(system_account_id, is_default DESC, updated_at DESC, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_api_keys_quota_snapshot
      ON api_keys(status, updated_at DESC, id)
      WHERE quota_limits_json IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_api_keys_availability_schedule_next_check
      ON api_keys(availability_schedule_next_check_at ASC, id ASC)
      WHERE availability_schedule_json IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_owner_name_unique ON api_keys(system_account_id, name)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_route_default_unique ON api_keys(route_strategy_id) WHERE is_default = 1`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_chat_purpose_unique
      ON api_keys(system_account_id)
      WHERE purpose = 'chat'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_api_keys_name_lookup ON api_keys(name, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_api_keys_system_account_name_lookup ON api_keys(system_account_id, name, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_request_quota_hourly_scope_bindings_window
      ON request_quota_hourly_window_scope_bindings(window_hours, system_account_id, scope_type, scope_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_request_quota_hourly_scope_bindings_source
      ON request_quota_hourly_window_scope_bindings(source_type, source_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_route_strategies_owner_mode ON route_strategies(system_account_id, mode, status, updated_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_route_strategies_owner_name_unique ON route_strategies(system_account_id, name)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_route_strategies_name_lookup ON route_strategies(name, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_route_strategies_system_account_name_lookup ON route_strategies(system_account_id, name, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_route_strategy_groups_strategy_priority ON route_strategy_groups(route_strategy_id, status, priority ASC, created_at ASC, id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_route_strategy_groups_unique ON route_strategy_groups(route_strategy_id, group_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_route_strategy_groups_group_strategy ON route_strategy_groups(group_id, route_strategy_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_route_strategy_groups_owner_group ON route_strategy_groups(system_account_id, group_id, route_strategy_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_api_key_schedule_status_events_api_key
      ON api_key_schedule_status_events(api_key_id, executed_at DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_openai_compatible_files_owner_created
      ON openai_compatible_files(system_account_id, api_key_id, created_at DESC, id DESC)
      WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_openai_compatible_files_purpose_created
      ON openai_compatible_files(system_account_id, api_key_id, purpose, created_at DESC, id DESC)
      WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_openai_compatible_files_container_created
      ON openai_compatible_files(system_account_id, api_key_id, container_id, created_at DESC, id DESC)
      WHERE deleted_at IS NULL AND container_id IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_openai_compatible_vector_stores_owner_created
      ON openai_compatible_vector_stores(system_account_id, api_key_id, created_at DESC, id DESC)
      WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_openai_compatible_vector_store_files_owner_created
      ON openai_compatible_vector_store_files(system_account_id, api_key_id, vector_store_id, created_at DESC, file_id DESC)
      WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_openai_compatible_vector_store_chunks_search
      ON openai_compatible_vector_store_chunks(system_account_id, api_key_id, vector_store_id, file_id, chunk_index)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_schedule_status_events_account
      ON account_schedule_status_events(account_id, executed_at DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_owner ON resource_authorization_grants(resource_owner_system_account_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_resource ON resource_authorization_grants(resource_type, resource_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_grantee_user ON resource_authorization_grants(grantee_system_account_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_grantee_team ON resource_authorization_grants(grantee_team_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_created ON resource_authorization_grants(created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_owner_created ON resource_authorization_grants(resource_owner_system_account_id, status, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_resource_created ON resource_authorization_grants(resource_type, resource_id, status, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_grantee_user_created ON resource_authorization_grants(grantee_system_account_id, status, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_grantee_team_created ON resource_authorization_grants(grantee_team_id, status, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_team_quota_snapshot
      ON resource_authorization_grants(resource_type, resource_id, grantee_team_id, status, updated_at DESC, id)
      WHERE grantee_type = 'team' AND limits_json IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_expiry_sweep
      ON resource_authorization_grants(expires_at ASC, updated_at ASC, id ASC)
      WHERE status IN ('active', 'paused') AND expires_at IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_grants_active_user_unique ON resource_authorization_grants(resource_type, resource_id, grantee_system_account_id) WHERE status = 'active' AND grantee_type = 'system_account'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_grants_active_team_unique ON resource_authorization_grants(resource_type, resource_id, grantee_team_id) WHERE status = 'active' AND grantee_type = 'team'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_sources_active_manual_unique ON resource_authorization_sources(authorization_id, source_type) WHERE status = 'active' AND source_type = 'manual'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_sources_active_team_unique ON resource_authorization_sources(authorization_id, source_type, source_team_id) WHERE status = 'active' AND source_type = 'team'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_proxy_profiles_system_account ON proxy_profiles(system_account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_proxy_profiles_updated ON proxy_profiles(updated_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_proxy_profiles_enabled_name_lookup ON proxy_profiles(enabled, name, updated_at DESC, id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_proxy_profiles_latency_refresh_due
      ON proxy_profiles(enabled, (last_tested_at IS NOT NULL), last_tested_at ASC, updated_at DESC, id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_proxy_profiles_name_unique ON proxy_profiles(name)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_proxy_profiles_name_lookup ON proxy_profiles(name, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_announcements_public ON announcements(status, published_at DESC, created_at DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_announcements_admin ON announcements(updated_at DESC, created_at DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_announcements_admin_page ON announcements(updated_at DESC, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_announcement_reads_account ON announcement_reads(system_account_id, read_at DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_response_inspection_policies_enabled_priority ON response_inspection_policies(enabled, priority, updated_at DESC, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_response_inspection_policies_protocol_priority ON response_inspection_policies(protocol_code, priority, updated_at DESC, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_response_inspection_policies_scope_priority ON response_inspection_policies(protocol_code, scope_type, provider_code, priority, updated_at DESC, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_external_integration_sources_status ON external_integration_sources(status, name)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_external_integration_sources_name_unique_lower ON external_integration_sources(lower(name))`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_oauth_grants_user_client_active
      ON oauth_grants(system_account_id, client_id, expires_at, revoked_at)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_oauth_authorization_codes_expiry
      ON oauth_authorization_codes(expires_at, consumed_at)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_oauth_authorization_transactions_expiry
      ON oauth_authorization_transactions(expires_at, completed_at)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_oauth_access_tokens_grant_expiry
      ON oauth_access_tokens(grant_id, expires_at, revoked_at, replaced_at)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE UNIQUE INDEX IF NOT EXISTS idx_oauth_signing_keys_one_active
      ON oauth_signing_keys(status) WHERE status = 'active'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_oauth_device_authorizations_poll
      ON oauth_device_authorizations(device_code_hash, client_id, expires_at, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_oauth_device_authorizations_user_code
      ON oauth_device_authorizations(user_code, expires_at, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_authorization_instance_authorization ON accounts(authorization_instance_authorization_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_authorization_instance_active_unique
      ON accounts(authorization_instance_authorization_id)
      WHERE authorization_instance_authorization_id IS NOT NULL AND deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_authorization_instance_source ON accounts(authorization_instance_source_account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_authorization_instance_source_owner_lookup
      ON accounts(authorization_instance_source_account_id, system_account_id, id)
      WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_deleted_cleanup
      ON accounts(deleted_at ASC, updated_at ASC, id ASC)
      WHERE deleted_at IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_circuit_incidents_account ON account_circuit_incidents(account_id, updated_at_ms, circuit_scope_key)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_circuit_incidents_runtime_state ON account_circuit_incidents(account_runtime_key, state, updated_at_ms, circuit_scope_key)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_circuit_incidents_projection_gap
      ON account_circuit_incidents(updated_at_ms, circuit_scope_key)
      WHERE projected_ledger_revision < ledger_revision`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_circuit_incidents_closed_cleanup
      ON account_circuit_incidents(retained_until_ms, updated_at_ms, circuit_scope_key)
      WHERE state = 'CLOSED'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_circuit_outbox_account ON account_circuit_outbox(account_id, dispatch_revision, created_at_ms, event_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_circuit_outbox_scope ON account_circuit_outbox(circuit_scope_key, ledger_revision, created_at_ms, event_id)
      WHERE circuit_scope_key IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_circuit_outbox_claim
      ON account_circuit_outbox(status, available_at_ms, claim_until_ms, created_at_ms, event_id)
      WHERE status IN ('pending', 'processing')`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_circuit_outbox_ack_cleanup
      ON account_circuit_outbox(acknowledged_at_ms, event_id)
      WHERE status = 'dispatched'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_group_accounts_dispatch_priority ON group_accounts(group_id, enabled, local_fallback_enabled, local_super_priority_enabled, local_priority, created_at, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_group_accounts_dispatch_candidate_window
      ON group_accounts(group_id, system_account_id, enabled, local_fallback_enabled ASC, local_super_priority_enabled DESC, local_priority ASC, created_at ASC, account_id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "accounts-pg-trigram-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_name_c_trgm_lookup ON accounts USING gin ((name COLLATE "C") juhe_business.gin_trgm_ops) WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "accounts-pg-trigram-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_provider_code_c_trgm_lookup ON accounts USING gin ((provider_code COLLATE "C") juhe_business.gin_trgm_ops) WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "accounts-pg-trigram-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_type_c_trgm_lookup ON accounts USING gin ((type COLLATE "C") juhe_business.gin_trgm_ops) WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "groups-pg-trigram-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_groups_name_c_trgm_lookup ON groups USING gin ((name COLLATE "C") juhe_business.gin_trgm_ops)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "account-list-projection-pg-trigram-index",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_name_trgm ON account_list_availability_projections USING gin (name_sort_key juhe_business.gin_trgm_ops)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "account-list-projection-index-pg-trigram-index",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_index_name_trgm ON account_list_availability_projection_index USING gin (name_sort_key juhe_business.gin_trgm_ops)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "account-list-projection-search-terms-pg-name-order-index",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_alap_search_term_name_c ON account_list_availability_projection_search_terms(viewer_system_account_id, term, (name_sort_key COLLATE "C") ASC, created_at_sort_key ASC, account_id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "account-list-projection-index-pg-name-search-incomplete-index",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_alap_index_name_incomplete_c ON account_list_availability_projection_index(viewer_system_account_id, (name_sort_key COLLATE "C") ASC, created_at_sort_key ASC, account_id ASC) WHERE search_index_complete = 0`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "account-list-projection-pg-name-order-index",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_name_order ON account_list_availability_projections(viewer_system_account_id, ((payload_json::jsonb ->> 'name') COLLATE "C") ASC, created_at_sort_key ASC, account_id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "account-circuit-key-model-capability-index",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_account_circuit_incidents_key_model_capability ON account_circuit_incidents(scope_kind, capability_hash) WHERE scope_kind = 'key_model' AND capability_hash IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "api-keys-pg-prefix-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_api_keys_name_c_lookup ON api_keys((name COLLATE "C"), id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "api-keys-pg-prefix-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_api_keys_system_account_name_c_lookup ON api_keys(system_account_id, (name COLLATE "C"), id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "accounts-pg-prefix-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_name_c_lookup ON accounts((name COLLATE "C"), id) WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "accounts-pg-prefix-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_owner_name_c_lookup ON accounts(system_account_id, (name COLLATE "C"), id) WHERE deleted_at IS NULL AND authorization_instance_authorization_id IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "accounts-pg-prefix-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_owner_all_name_c_lookup ON accounts(system_account_id, (name COLLATE "C"), id) WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "system-teams-pg-prefix-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_teams_name_c_lookup ON system_teams((name COLLATE "C"), id)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE TABLE IF NOT EXISTS chat_conversations (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      api_key_id text,
      api_key_name_snapshot text NOT NULL,
      title text NOT NULL DEFAULT '新对话',
      title_source_message_id text,
      is_pinned integer NOT NULL DEFAULT 0,
      last_model text,
      default_image_model text NOT NULL DEFAULT 'gpt-image-2',
      next_sequence_no bigint NOT NULL DEFAULT 1,
      user_turn_count bigint NOT NULL DEFAULT 0,
      message_revision bigint NOT NULL DEFAULT 0,
      active_turn_id text,
      active_started_at text,
      context_revision bigint NOT NULL DEFAULT 0,
      active_checkpoint_id text,
      compacted_through_sequence bigint NOT NULL DEFAULT 0,
      context_state text NOT NULL DEFAULT 'ready',
      active_context_tokens bigint,
      effective_context_limit_tokens bigint,
      context_usage_estimated integer NOT NULL DEFAULT 1,
      context_claim_id text,
      context_claim_revision bigint,
      context_claim_through_sequence bigint,
      context_claimed_at text,
      context_retry_at text,
      context_attempt_count integer NOT NULL DEFAULT 0,
      context_error_code text,
      context_progress_sequence bigint NOT NULL DEFAULT 0,
      context_progress_earliest_expires_at text,
      last_message_at text NOT NULL,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      CHECK (next_sequence_no >= 1),
      CHECK (user_turn_count >= 0),
      CHECK (message_revision >= 0),
      CHECK (is_pinned IN (0, 1)),
      CHECK (context_revision >= 0),
      CHECK (compacted_through_sequence >= 0 AND compacted_through_sequence < next_sequence_no),
      CHECK (context_state IN ('ready', 'compact_pending', 'compacting', 'compact_failed')),
      CHECK (active_context_tokens IS NULL OR active_context_tokens >= 0),
      CHECK (effective_context_limit_tokens IS NULL OR effective_context_limit_tokens > 0),
      CHECK (context_usage_estimated IN (0, 1)),
      CHECK (context_attempt_count >= 0),
      CHECK (context_progress_sequence >= 0),
      CHECK (
        (active_checkpoint_id IS NULL AND compacted_through_sequence = 0)
        OR active_checkpoint_id IS NOT NULL
      ),
      CHECK (
        (
          context_state = 'compacting'
          AND context_claim_id IS NOT NULL
          AND context_claim_revision = context_revision
          AND context_claim_through_sequence IS NOT NULL
          AND context_claim_through_sequence > compacted_through_sequence
          AND context_claim_through_sequence <= next_sequence_no - 3
          AND context_claimed_at IS NOT NULL
          AND context_progress_sequence >= compacted_through_sequence
          AND context_progress_sequence <= context_claim_through_sequence
        )
        OR (
          context_state != 'compacting'
          AND context_claim_id IS NULL
          AND context_claim_revision IS NULL
          AND context_claim_through_sequence IS NULL
          AND context_claimed_at IS NULL
          AND context_progress_sequence = 0
          AND context_progress_earliest_expires_at IS NULL
        )
      )
    )`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE TABLE IF NOT EXISTS chat_messages (
      id text NOT NULL,
      conversation_id text NOT NULL,
      system_account_id text NOT NULL,
      turn_id text NOT NULL,
      sequence_no bigint NOT NULL,
      client_message_id text,
      role text NOT NULL,
      status text NOT NULL,
      content_text text NOT NULL DEFAULT '',
      content_blocks_json text NOT NULL DEFAULT '[]',
      content_bytes bigint NOT NULL DEFAULT 0,
      storage_reserved_bytes bigint NOT NULL DEFAULT 0,
      model text NOT NULL,
      trace_id text,
      finish_reason text,
      error_code text,
      error_message text,
      created_at text NOT NULL,
      completed_at text,
      expires_at text NOT NULL,
      FOREIGN KEY (conversation_id) REFERENCES chat_conversations(id) ON DELETE CASCADE,
      CHECK (sequence_no >= 1),
      CHECK (content_bytes >= 0),
      CHECK (storage_reserved_bytes >= 0),
      CHECK (role IN ('user', 'assistant')),
      CHECK (status IN ('completed', 'streaming', 'failed', 'canceled')),
      CHECK (
        (role = 'user' AND client_message_id IS NOT NULL AND status = 'completed')
        OR (role = 'assistant' AND client_message_id IS NULL)
      ),
      CHECK (
        (role = 'assistant' AND status = 'streaming' AND storage_reserved_bytes > 0)
        OR (status != 'streaming' AND storage_reserved_bytes = 0)
      ),
      PRIMARY KEY (created_at, id)
    ) PARTITION BY RANGE (created_at)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE TABLE IF NOT EXISTS chat_message_idempotency (
      conversation_id text NOT NULL,
      client_message_id text NOT NULL,
      system_account_id text NOT NULL,
      turn_id text NOT NULL,
      user_message_id text NOT NULL,
      assistant_message_id text NOT NULL,
      created_at text NOT NULL,
      expires_at text NOT NULL,
      PRIMARY KEY (conversation_id, client_message_id),
      FOREIGN KEY (conversation_id) REFERENCES chat_conversations(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE TABLE IF NOT EXISTS chat_user_storage_windows (
      system_account_id text NOT NULL,
      bucket_date text NOT NULL,
      content_bytes bigint NOT NULL DEFAULT 0,
      reserved_bytes bigint NOT NULL DEFAULT 0,
      updated_at text NOT NULL,
      PRIMARY KEY (system_account_id, bucket_date),
      CHECK (content_bytes >= 0),
      CHECK (reserved_bytes >= 0)
    )`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE TABLE IF NOT EXISTS chat_user_asset_usage (
      system_account_id text PRIMARY KEY,
      asset_bytes bigint NOT NULL DEFAULT 0,
      asset_count integer NOT NULL DEFAULT 0,
      updated_at text NOT NULL,
      CHECK (asset_bytes >= 0),
      CHECK (asset_count >= 0)
    )`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE TABLE IF NOT EXISTS chat_context_checkpoints (
      id text PRIMARY KEY,
      conversation_id text NOT NULL,
      system_account_id text NOT NULL,
      version integer NOT NULL,
      source_revision bigint NOT NULL,
      source_from_sequence bigint NOT NULL,
      source_through_sequence bigint NOT NULL,
      recent_tail_from_sequence bigint NOT NULL,
      entry_from_sequence bigint NOT NULL,
      entry_through_sequence bigint NOT NULL,
      payload_digest text NOT NULL,
      estimated_input_tokens bigint,
      upstream_input_tokens bigint,
      request_body_bytes bigint NOT NULL,
      model_id text NOT NULL,
      provider_code text,
      provider_profile_id text,
      endpoint_family text NOT NULL,
      compact_compatibility_hash text,
      prompt_version text NOT NULL,
      status text NOT NULL DEFAULT 'pending',
      quality_status text NOT NULL,
      created_at text NOT NULL,
      expires_at text NOT NULL,
      FOREIGN KEY (conversation_id) REFERENCES chat_conversations(id) ON DELETE CASCADE,
      UNIQUE (conversation_id, version),
      CHECK (version >= 1),
      CHECK (source_revision >= 0),
      CHECK (source_from_sequence >= 1),
      CHECK (source_through_sequence >= source_from_sequence),
      CHECK (recent_tail_from_sequence = source_through_sequence + 1),
      CHECK (entry_from_sequence >= 1),
      CHECK (entry_through_sequence >= entry_from_sequence),
      CHECK (length(payload_digest) = 64),
      CHECK (estimated_input_tokens IS NULL OR estimated_input_tokens >= 0),
      CHECK (upstream_input_tokens IS NULL OR upstream_input_tokens >= 0),
      CHECK (request_body_bytes >= 0),
      CHECK (status IN ('pending', 'active', 'superseded', 'rejected')),
      CHECK (quality_status IN ('passed', 'failed'))
    )`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE TABLE IF NOT EXISTS chat_context_entries (
      conversation_id text NOT NULL,
      checkpoint_id text NOT NULL,
      sequence bigint NOT NULL,
      source_message_id text,
      kind text NOT NULL,
      content_json text NOT NULL,
      content_bytes bigint NOT NULL,
      provenance text NOT NULL,
      trust_level text NOT NULL,
      token_count bigint,
      created_at text NOT NULL,
      expires_at text NOT NULL,
      PRIMARY KEY (checkpoint_id, sequence),
      FOREIGN KEY (conversation_id) REFERENCES chat_conversations(id) ON DELETE CASCADE,
      FOREIGN KEY (checkpoint_id) REFERENCES chat_context_checkpoints(id) ON DELETE CASCADE,
      CHECK (sequence >= 1),
      CHECK (kind IN ('verbatim', 'durable_memory', 'task_state', 'tool_result', 'image_observation', 'provider_compaction')),
      CHECK (content_bytes >= 2),
      CHECK (provenance IN ('user', 'assistant', 'tool', 'asset', 'provider')),
      CHECK (trust_level IN ('untrusted', 'assistant_derived', 'provider_opaque')),
      CHECK (token_count IS NULL OR token_count >= 0)
    )`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE TABLE IF NOT EXISTS chat_assets (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      conversation_id text NOT NULL,
      source_kind text NOT NULL DEFAULT 'user_upload',
      original_filename text NOT NULL,
      original_mime_type text NOT NULL,
      original_width integer,
      original_height integer,
      original_bytes bigint NOT NULL,
      original_sha256 text NOT NULL,
      processed_mime_type text,
      processed_width integer,
      processed_height integer,
      processed_bytes bigint,
      processed_sha256 text,
      storage_key text,
      preview_mime_type text,
      preview_width integer,
      preview_height integer,
      preview_bytes integer,
      preview_sha256 text,
      preview_storage_key text,
      processing_status text NOT NULL DEFAULT 'pending',
      processing_error_code text,
      observation_status text NOT NULL DEFAULT 'not_requested',
      observation_json text,
      observation_revision integer NOT NULL DEFAULT 0,
      observation_claim_id text,
      observation_claimed_at text,
      quota_bytes integer NOT NULL,
      turn_id text,
      message_id text,
      committed_at text,
      cleanup_status text NOT NULL DEFAULT 'active',
      cleanup_claim_id text,
      cleanup_attempt_count integer NOT NULL DEFAULT 0,
      cleanup_claimed_at text,
      cleanup_retry_at text,
      cleanup_error_code text,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      expires_at text NOT NULL,
      UNIQUE (id, conversation_id),
      CHECK (original_width IS NULL OR original_width > 0),
      CHECK (original_height IS NULL OR original_height > 0),
      CHECK ((original_width IS NULL AND original_height IS NULL) OR (original_width IS NOT NULL AND original_height IS NOT NULL)),
      CHECK (original_bytes > 0),
      CHECK (length(original_sha256) = 64),
      CHECK (processed_width IS NULL OR processed_width > 0),
      CHECK (processed_height IS NULL OR processed_height > 0),
      CHECK ((processed_width IS NULL AND processed_height IS NULL) OR (processed_width IS NOT NULL AND processed_height IS NOT NULL)),
      CHECK (processed_bytes IS NULL OR processed_bytes > 0),
      CHECK (source_kind IN ('user_upload', 'assistant_generated')),
      CHECK (processed_mime_type IS NULL OR processed_mime_type IN ('image/jpeg', 'image/png', 'image/webp')),
      CHECK (processed_sha256 IS NULL OR length(processed_sha256) = 64),
      CHECK (preview_mime_type IS NULL OR preview_mime_type = 'image/webp'),
      CHECK (preview_width IS NULL OR preview_width > 0),
      CHECK (preview_height IS NULL OR preview_height > 0),
      CHECK (preview_bytes IS NULL OR preview_bytes > 0),
      CHECK (preview_sha256 IS NULL OR length(preview_sha256) = 64),
      CHECK (
        (preview_mime_type IS NULL AND preview_width IS NULL AND preview_height IS NULL AND preview_bytes IS NULL AND preview_sha256 IS NULL AND preview_storage_key IS NULL)
        OR (preview_mime_type IS NOT NULL AND preview_width IS NOT NULL AND preview_height IS NOT NULL AND preview_bytes IS NOT NULL AND preview_sha256 IS NOT NULL AND preview_storage_key IS NOT NULL)
      ),
      CHECK (source_kind != 'assistant_generated' OR preview_storage_key IS NOT NULL),
      CHECK (processing_status IN ('pending', 'ready', 'failed')),
      CHECK (observation_status IN ('not_requested', 'pending', 'ready', 'failed')),
      CHECK (observation_revision >= 0),
      CHECK (quota_bytes > 0),
      CHECK (cleanup_status IN ('active', 'claimed', 'failed')),
      CHECK (cleanup_attempt_count >= 0),
      CHECK (
        processing_status != 'ready'
        OR (
          processed_mime_type IS NOT NULL
          AND processed_width IS NOT NULL
          AND processed_height IS NOT NULL
          AND processed_bytes IS NOT NULL
          AND processed_sha256 IS NOT NULL
          AND storage_key IS NOT NULL
        )
      ),
      CHECK (
        (observation_status = 'pending' AND observation_claim_id IS NOT NULL AND observation_claimed_at IS NOT NULL)
        OR (observation_status != 'pending' AND observation_claim_id IS NULL AND observation_claimed_at IS NULL)
      ),
      CHECK (
        (turn_id IS NULL AND message_id IS NULL AND committed_at IS NULL)
        OR (turn_id IS NOT NULL AND message_id IS NOT NULL AND committed_at IS NOT NULL)
      ),
      CHECK (
        (cleanup_status = 'claimed' AND cleanup_claim_id IS NOT NULL AND cleanup_claimed_at IS NOT NULL)
        OR (cleanup_status != 'claimed' AND cleanup_claim_id IS NULL AND cleanup_claimed_at IS NULL)
      )
    )`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE TABLE IF NOT EXISTS chat_asset_references (
      asset_id text NOT NULL,
      conversation_id text NOT NULL,
      turn_id text NOT NULL,
      message_id text NOT NULL,
      reference_kind text NOT NULL,
      content_order integer NOT NULL,
      created_at text NOT NULL,
      expires_at text NOT NULL,
      FOREIGN KEY (asset_id, conversation_id) REFERENCES chat_assets(id, conversation_id) ON DELETE CASCADE,
      FOREIGN KEY (conversation_id) REFERENCES chat_conversations(id) ON DELETE CASCADE,
      UNIQUE (message_id, content_order),
      CHECK (reference_kind IN ('user_input', 'assistant_output')),
      CHECK (content_order >= 0)
    )`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE TABLE IF NOT EXISTS chat_image_generations (
      asset_id text PRIMARY KEY,
      conversation_id text NOT NULL,
      system_account_id text NOT NULL,
      operation text NOT NULL,
      model text NOT NULL,
      prompt text NOT NULL,
      source_asset_ids_json text NOT NULL DEFAULT '[]',
      root_asset_id text NOT NULL,
      size text NOT NULL,
      quality text NOT NULL,
      output_format text NOT NULL,
      created_at text NOT NULL,
      expires_at text NOT NULL,
      FOREIGN KEY (asset_id, conversation_id) REFERENCES chat_assets(id, conversation_id) ON DELETE CASCADE,
      FOREIGN KEY (root_asset_id, conversation_id) REFERENCES chat_assets(id, conversation_id) ON DELETE CASCADE,
      FOREIGN KEY (conversation_id) REFERENCES chat_conversations(id) ON DELETE CASCADE,
      CHECK (operation IN ('generate', 'edit')),
      CHECK (jsonb_typeof(source_asset_ids_json::jsonb) = 'array')
    )`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_conversations_owner_recent
      ON chat_conversations(system_account_id, last_message_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_conversations_owner_pinned_recent
      ON chat_conversations(system_account_id, is_pinned DESC, last_message_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_conversations_owner_api_key
      ON chat_conversations(system_account_id, api_key_id)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_conversations_active_started
      ON chat_conversations(active_started_at, id)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_conversations_context_queue
      ON chat_conversations(context_state, context_retry_at, context_claimed_at, updated_at, id)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_messages_conversation_sequence
      ON chat_messages(conversation_id, sequence_no DESC)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_messages_conversation_turn
      ON chat_messages(conversation_id, turn_id)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_messages_context
      ON chat_messages(system_account_id, conversation_id, status, expires_at, sequence_no DESC)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_messages_compaction_source
      ON chat_messages(conversation_id, system_account_id, status, sequence_no)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_messages_expiry
      ON chat_messages(expires_at, id)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_idempotency_expiry
      ON chat_message_idempotency(expires_at, conversation_id, client_message_id)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_context_checkpoints_conversation_version
      ON chat_context_checkpoints(conversation_id, version DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE UNIQUE INDEX IF NOT EXISTS idx_chat_context_checkpoints_one_active
      ON chat_context_checkpoints(conversation_id) WHERE status = 'active'`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_context_checkpoints_cleanup
      ON chat_context_checkpoints(expires_at, status, id)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_context_entries_conversation_checkpoint
      ON chat_context_entries(conversation_id, checkpoint_id, sequence)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_context_entries_expiry
      ON chat_context_entries(expires_at, checkpoint_id, sequence)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_assets_owner_conversation
      ON chat_assets(system_account_id, conversation_id, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_assets_owner_lookup
      ON chat_assets(system_account_id, id, conversation_id)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_assets_message
      ON chat_assets(conversation_id, turn_id, message_id, id)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_assets_uncommitted
      ON chat_assets(system_account_id, conversation_id, expires_at, id)
      WHERE turn_id IS NULL AND message_id IS NULL
        AND processing_status IN ('pending', 'ready') AND cleanup_status = 'active'`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_assets_cleanup
      ON chat_assets(cleanup_status, cleanup_retry_at, expires_at, id)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_asset_references_message
      ON chat_asset_references(conversation_id, message_id, content_order)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_asset_references_asset_valid
      ON chat_asset_references(asset_id, expires_at)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_asset_references_cleanup
      ON chat_asset_references(expires_at, asset_id, message_id)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_image_generations_conversation_recent
      ON chat_image_generations(conversation_id, created_at DESC, asset_id DESC)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_image_generations_expiry
      ON chat_image_generations(expires_at, asset_id)`,
	},
	{
		SchemaName: "juhe_dataset",
		Source:     "dataset",
		SQL: `CREATE TABLE IF NOT EXISTS public_api_logs (
          id text PRIMARY KEY,
          trace_id text,
          source_ref_id text,
          source_name text,
          token_id text,
          token_name text,
          token_prefix text,
          is_test_token integer NOT NULL DEFAULT 0,
          method text NOT NULL,
          path text NOT NULL,
          query_string text,
          client_ip text,
          user_agent text,
          status_code integer,
          success integer NOT NULL DEFAULT 0,
          duration_ms integer,
          request_size_bytes bigint NOT NULL DEFAULT 0,
          response_size_bytes bigint NOT NULL DEFAULT 0,
          request_capture_status text NOT NULL DEFAULT 'empty',
          response_capture_status text NOT NULL DEFAULT 'empty',
          request_data_json text NOT NULL DEFAULT '{}',
          response_data_json text NOT NULL DEFAULT '{}',
          error_code text,
          error_message text,
          started_at text NOT NULL,
          ended_at text NOT NULL,
          created_at text NOT NULL
        )`,
	},
	{
		SchemaName: "juhe_dataset",
		Source:     "dataset",
		SQL: `CREATE TABLE IF NOT EXISTS api_key_record_cleanup_targets (
          api_key_id text PRIMARY KEY,
          system_account_id text NOT NULL,
          created_at text NOT NULL,
          updated_at text NOT NULL,
          attempt_count integer NOT NULL DEFAULT 0,
          last_attempt_at text,
          last_blocked_reason text,
          last_error_message text
        )`,
	},
	{
		SchemaName: "juhe_dataset",
		Source:     "dataset",
		SQL: `CREATE TABLE IF NOT EXISTS account_record_cleanup_targets (
          account_id text PRIMARY KEY,
          system_account_id text NOT NULL,
          related_account_ids_json text NOT NULL DEFAULT '[]',
          authorization_ids_json text NOT NULL DEFAULT '[]',
          team_scope_ids_json text NOT NULL DEFAULT '[]',
          created_at text NOT NULL,
          updated_at text NOT NULL,
          attempt_count integer NOT NULL DEFAULT 0,
          last_attempt_at text,
          last_blocked_reason text,
          last_error_message text
        )`,
	},
	{
		SchemaName: "juhe_dataset",
		Source:     "dataset",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_public_api_logs_created ON public_api_logs(created_at, id)`,
	},
	{
		SchemaName: "juhe_dataset",
		Source:     "dataset",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_public_api_logs_source_created ON public_api_logs(source_ref_id, created_at, id)`,
	},
	{
		SchemaName: "juhe_dataset",
		Source:     "dataset",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_api_key_record_cleanup_targets_attempt ON api_key_record_cleanup_targets(COALESCE(last_attempt_at, created_at), created_at, api_key_id)`,
	},
	{
		SchemaName: "juhe_dataset",
		Source:     "dataset",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_record_cleanup_targets_attempt ON account_record_cleanup_targets(COALESCE(last_attempt_at, created_at), created_at, account_id)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-catalog",
		SQL: `CREATE TABLE IF NOT EXISTS usage_record_shards (
          shard_key text PRIMARY KEY,
          bucket_date text NOT NULL,
          shard_id integer NOT NULL,
          file_path text NOT NULL,
          schema_version integer NOT NULL DEFAULT 1,
          status text NOT NULL DEFAULT 'active',
          first_seen_at text NOT NULL,
          last_write_at text,
          last_error_message text,
          created_at text NOT NULL,
          updated_at text NOT NULL
        )`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-catalog",
		SQL: `CREATE TABLE IF NOT EXISTS usage_record_shard_entries (
          usage_id text PRIMARY KEY,
          shard_key text NOT NULL,
          system_account_id text NOT NULL,
          trace_id text NOT NULL,
          api_key_id text,
          account_id text,
          group_id text,
          model text,
          traffic_source text NOT NULL,
          success integer NOT NULL DEFAULT 0,
          status_code integer,
          client_ip text,
          first_token_ms integer,
          duration_ms integer,
          cost_usd double precision,
          created_at text NOT NULL,
          indexed_at text NOT NULL,
          FOREIGN KEY (shard_key) REFERENCES usage_record_shards(shard_key) ON DELETE CASCADE
        )`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-catalog",
		SQL: `CREATE TABLE IF NOT EXISTS usage_record_account_shards (
          account_id text NOT NULL,
          shard_key text NOT NULL,
          first_created_at text NOT NULL,
          last_seen_at text NOT NULL,
          PRIMARY KEY (account_id, shard_key),
          FOREIGN KEY (shard_key) REFERENCES usage_record_shards(shard_key) ON DELETE CASCADE
        )`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-catalog",
		SQL: `CREATE TABLE IF NOT EXISTS usage_record_api_key_shards (
          api_key_id text NOT NULL,
          system_account_id text NOT NULL,
          shard_key text NOT NULL,
          first_created_at text NOT NULL,
          last_seen_at text NOT NULL,
          PRIMARY KEY (api_key_id, system_account_id, shard_key),
          FOREIGN KEY (shard_key) REFERENCES usage_record_shards(shard_key) ON DELETE CASCADE
        )`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL: `CREATE TABLE IF NOT EXISTS usage_records (
      id text NOT NULL,
      system_account_id text NOT NULL,
      trace_id text NOT NULL,
      traffic_source text NOT NULL,
      client_ip text,
      api_key_id text,
      group_id text,
      account_id text,
      endpoint text,
      provider_code text,
      provider_protocol_profile_id text,
      usage_semantic text,
      model text,
      upstream_model text,
      upstream_response_model text,
      pricing_model text,
      requested_service_tier text NOT NULL DEFAULT 'default',
      effective_service_tier text NOT NULL DEFAULT 'default',
      reported_service_tier text,
      billed_service_tier text NOT NULL DEFAULT 'default',
      requested_reasoning_effort text,
      effective_reasoning_effort text,
      cost_breakdown_snapshot_json text,
      model_mapping_applied integer NOT NULL DEFAULT 0,
      model_mapping_source text,
      source_endpoint_family text,
      upstream_endpoint_family text,
      stream integer NOT NULL DEFAULT 0,
      status_code integer,
      success integer NOT NULL DEFAULT 0,
      failure_attribution text,
      first_token_ms integer,
      duration_ms integer,
      input_tokens bigint,
      output_tokens bigint,
      cache_read_tokens bigint,
      cache_read_cost_usd double precision,
      cache_write_tokens bigint,
      cache_write_1h_tokens bigint,
      cache_write_cost_usd double precision,
      thinking_tokens bigint,
      input_image_tokens bigint,
      output_image_tokens bigint,
      input_audio_tokens integer,
      output_audio_tokens integer,
      output_image_count integer,
      cost_usd double precision,
      error_code text,
      error_message text,
      request_snapshot_json text,
      response_snapshot_json text,
      account_owner_system_account_id text,
      group_owner_system_account_id text,
      account_access_type text,
      group_access_type text,
      account_authorization_id text,
      account_authorization_source_type text,
      account_authorization_source_team_id text,
      group_authorization_id text,
      group_authorization_source_type text,
      group_authorization_source_team_id text,
      created_at text NOT NULL,
      PRIMARY KEY (created_at, id)
    ) PARTITION BY RANGE (created_at)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "upstream-response-model-pg-columns",
		SQL:        `ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS upstream_response_model text`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_created_at`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_system_account_created_at`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_group_real_usage`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_group_created_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_first_token_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_duration_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_cost_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_system_account_first_token_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_system_account_duration_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_system_account_cost_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_api_key_created_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_account_created_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_trace_created_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_model_created_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_system_account_model_created_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_traffic_source_created`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_client_ip_created_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_system_account_client_ip_created_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_provider_protocol_profile_created_at`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-catalog",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_record_shards_bucket ON usage_record_shards(bucket_date, shard_id)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-catalog",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_record_account_shards_account_created ON usage_record_account_shards(account_id, first_created_at, shard_key)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-catalog",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_record_api_key_shards_key_created ON usage_record_api_key_shards(api_key_id, system_account_id, first_created_at, shard_key)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-catalog",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_shard ON usage_record_shard_entries(shard_key, created_at)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-catalog",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_created_sort ON usage_record_shard_entries(created_at, usage_id)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-catalog",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_created_sort ON usage_record_shard_entries(system_account_id, created_at, usage_id)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-catalog",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_trace_created_sort ON usage_record_shard_entries(system_account_id, trace_id, created_at, usage_id)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-catalog",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_api_key_created_sort ON usage_record_shard_entries(system_account_id, api_key_id, created_at, usage_id)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-catalog",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_group_created_sort ON usage_record_shard_entries(system_account_id, group_id, created_at, usage_id)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-catalog",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_account_created_sort ON usage_record_shard_entries(system_account_id, account_id, created_at, usage_id)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_created_sort ON usage_records(system_account_id, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_trace_created_sort ON usage_records(system_account_id, trace_id, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_group_created_sort ON usage_records(system_account_id, group_id, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_api_key_created_sort ON usage_records(system_account_id, api_key_id, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_account_created_sort ON usage_records(system_account_id, account_id, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_account_owner ON usage_records(account_owner_system_account_id, account_id, created_at)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_group_owner ON usage_records(group_owner_system_account_id, group_id, created_at)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_account_authorization ON usage_records(account_authorization_id, created_at)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_group_authorization ON usage_records(group_authorization_id, created_at)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_stats_cursor ON usage_records(created_at, id)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records-pg-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_recent_openai_account_shape ON usage_records(account_id, created_at DESC, id DESC, provider_code) WHERE api_key_id IS NOT NULL AND traffic_source = 'gateway' AND endpoint IS NOT NULL AND btrim(endpoint) <> ''`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records-pg-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_recent_openai_group_shape ON usage_records(group_id, created_at DESC, id DESC, provider_code) WHERE api_key_id IS NOT NULL AND traffic_source = 'gateway' AND endpoint IS NOT NULL AND btrim(endpoint) <> ''`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records-pg-prefix-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_system_trace_c_created_sort ON usage_records(system_account_id, (trace_id COLLATE "C"), created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS client_ip_range_window_dirty_ips (
      ip_hash text PRIMARY KEY,
      generation bigint NOT NULL DEFAULT 1,
      first_dirty_at text NOT NULL,
      updated_at text NOT NULL
    )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS client_ip_account_range_window_dirty_ips (
      ip_hash text PRIMARY KEY,
      generation bigint NOT NULL DEFAULT 1,
      first_dirty_at text NOT NULL,
      updated_at text NOT NULL
    )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS account_quality_minute_stats (
          account_id text NOT NULL,
          system_account_id text NOT NULL,
          provider_code text NOT NULL,
          stat_minute text NOT NULL,
          request_count bigint NOT NULL DEFAULT 0,
          success_count bigint NOT NULL DEFAULT 0,
          error_count bigint NOT NULL DEFAULT 0,
          first_token_ms_sum bigint NOT NULL DEFAULT 0,
          first_token_ms_count bigint NOT NULL DEFAULT 0,
          last_sample_at text,
          last_success_at text,
          last_error_at text,
          last_error_message text,
          updated_at text NOT NULL,
          PRIMARY KEY (account_id, stat_minute)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS account_health_hourly (
          account_id text NOT NULL,
          system_account_id text NOT NULL,
          provider_code text NOT NULL,
          stat_hour text NOT NULL,
          status text NOT NULL CHECK (status IN ('success', 'failure')),
          last_observed_at text NOT NULL,
          last_record_id text NOT NULL,
          status_code integer,
          error_code text,
          error_message text,
          updated_at text NOT NULL,
          PRIMARY KEY (account_id, stat_hour)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS group_account_stats (
          system_account_id text NOT NULL,
          group_id text NOT NULL,
          total integer NOT NULL DEFAULT 0,
          available integer NOT NULL DEFAULT 0,
          active integer NOT NULL DEFAULT 0,
          disabled integer NOT NULL DEFAULT 0,
          error integer NOT NULL DEFAULT 0,
          rate_limited integer NOT NULL DEFAULT 0,
          current_concurrency integer NOT NULL DEFAULT 0,
          concurrency_limit integer NOT NULL DEFAULT 0,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, group_id)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS account_quality_scores (
          account_id text PRIMARY KEY,
          system_account_id text NOT NULL,
          provider_code text NOT NULL,
          quality_score integer NOT NULL DEFAULT 1000000,
          quality_state text NOT NULL DEFAULT 'unknown',
          recent_request_count integer NOT NULL DEFAULT 0,
          recent_success_count integer NOT NULL DEFAULT 0,
          recent_error_count integer NOT NULL DEFAULT 0,
          recent_first_token_sample_count integer NOT NULL DEFAULT 0,
          recent_avg_first_token_ms integer,
          ewma_first_token_ms integer,
          success_rate double precision,
          window_started_at text NOT NULL,
          window_ended_at text NOT NULL,
          last_sample_at text,
          last_success_at text,
          last_error_at text,
          last_error_message text,
          updated_at text NOT NULL
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS account_quality_dirty_accounts (
          account_id text PRIMARY KEY,
          first_dirty_at text NOT NULL,
          updated_at text NOT NULL
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS account_usage_snapshots (
          system_account_id text NOT NULL,
          account_id text NOT NULL,
          kind text NOT NULL CHECK (kind IN ('openai_codex', 'relay_balance')),
          source text,
          snapshot_json text NOT NULL,
          refresh_status text,
          last_attempt_at timestamptz,
          last_success_at timestamptz,
          next_refresh_after timestamptz,
          last_error_message text,
          updated_at timestamptz NOT NULL,
          created_at timestamptz NOT NULL,
          PRIMARY KEY (system_account_id, account_id, kind)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_stats_totals (
          system_account_id text NOT NULL,
          scope_type text NOT NULL,
          scope_id text NOT NULL DEFAULT '',
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
          first_token_ms_sum bigint NOT NULL DEFAULT 0,
          first_token_ms_count bigint NOT NULL DEFAULT 0,
          first_token_ms_max bigint NOT NULL DEFAULT 0,
          last_used_at text,
          last_error_at text,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_stats_minute (
          system_account_id text NOT NULL,
          scope_type text NOT NULL,
          scope_id text NOT NULL DEFAULT '',
          stat_minute text NOT NULL,
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
          first_token_ms_sum bigint NOT NULL DEFAULT 0,
          first_token_ms_count bigint NOT NULL DEFAULT 0,
          first_token_ms_max bigint NOT NULL DEFAULT 0,
          last_used_at text,
          last_error_at text,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, stat_minute)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_stats_daily (
          system_account_id text NOT NULL,
          scope_type text NOT NULL,
          scope_id text NOT NULL DEFAULT '',
          stat_date text NOT NULL,
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
          first_token_ms_sum bigint NOT NULL DEFAULT 0,
          first_token_ms_count bigint NOT NULL DEFAULT 0,
          first_token_ms_max bigint NOT NULL DEFAULT 0,
          last_used_at text,
          last_error_at text,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, stat_date)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_stats_hourly (
          system_account_id text NOT NULL,
          scope_type text NOT NULL,
          scope_id text NOT NULL DEFAULT '',
          stat_hour text NOT NULL,
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
          first_token_ms_sum bigint NOT NULL DEFAULT 0,
          first_token_ms_count bigint NOT NULL DEFAULT 0,
          first_token_ms_max bigint NOT NULL DEFAULT 0,
          last_used_at text,
          last_error_at text,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, stat_hour)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_stats_weekly (
          system_account_id text NOT NULL,
          scope_type text NOT NULL,
          scope_id text NOT NULL DEFAULT '',
          stat_week text NOT NULL,
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
          first_token_ms_sum bigint NOT NULL DEFAULT 0,
          first_token_ms_count bigint NOT NULL DEFAULT 0,
          first_token_ms_max bigint NOT NULL DEFAULT 0,
          last_used_at text,
          last_error_at text,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, stat_week)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_stats_monthly (
          system_account_id text NOT NULL,
          scope_type text NOT NULL,
          scope_id text NOT NULL DEFAULT '',
          stat_month text NOT NULL,
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
          first_token_ms_sum bigint NOT NULL DEFAULT 0,
          first_token_ms_count bigint NOT NULL DEFAULT 0,
          first_token_ms_max bigint NOT NULL DEFAULT 0,
          last_used_at text,
          last_error_at text,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, stat_month)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS authorization_team_usage_summary_daily (
          system_account_id text NOT NULL,
          stat_date text NOT NULL,
          team_filter_id text NOT NULL DEFAULT '',
          resource_filter_type text NOT NULL DEFAULT 'all',
          resource_filter_id text NOT NULL DEFAULT '',
          row_count bigint NOT NULL DEFAULT 0,
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
          first_token_ms_sum bigint NOT NULL DEFAULT 0,
          first_token_ms_count bigint NOT NULL DEFAULT 0,
          first_token_ms_max bigint NOT NULL DEFAULT 0,
          last_used_at text,
          last_error_at text,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, stat_date, team_filter_id, resource_filter_type, resource_filter_id)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS authorization_team_usage_range_windows (
          system_account_id text NOT NULL,
          start_date text NOT NULL,
          end_date text NOT NULL,
          team_filter_id text NOT NULL DEFAULT '',
          resource_filter_type text NOT NULL DEFAULT 'all',
          resource_filter_id text NOT NULL DEFAULT '',
          request_count bigint NOT NULL DEFAULT 0,
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
          last_used_at text,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS authorization_user_usage_summary_daily (
          system_account_id text NOT NULL,
          stat_date text NOT NULL,
          team_filter_id text NOT NULL DEFAULT '',
          grantee_filter_system_account_id text NOT NULL DEFAULT '',
          resource_filter_type text NOT NULL DEFAULT 'all',
          resource_filter_id text NOT NULL DEFAULT '',
          row_count bigint NOT NULL DEFAULT 0,
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
          first_token_ms_sum bigint NOT NULL DEFAULT 0,
          first_token_ms_count bigint NOT NULL DEFAULT 0,
          first_token_ms_max bigint NOT NULL DEFAULT 0,
          last_used_at text,
          last_error_at text,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, stat_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS authorization_user_usage_range_windows (
          system_account_id text NOT NULL,
          start_date text NOT NULL,
          end_date text NOT NULL,
          team_filter_id text NOT NULL DEFAULT '',
          grantee_filter_system_account_id text NOT NULL DEFAULT '',
          resource_filter_type text NOT NULL DEFAULT 'all',
          resource_filter_id text NOT NULL DEFAULT '',
          request_count bigint NOT NULL DEFAULT 0,
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
          last_used_at text,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_model_minute (
          system_account_id text NOT NULL,
          stat_minute text NOT NULL,
          provider_code text NOT NULL DEFAULT 'unknown',
          model text NOT NULL DEFAULT 'unknown',
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
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, stat_minute, provider_code, model)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_model_daily (
          system_account_id text NOT NULL,
          stat_date text NOT NULL,
          provider_code text NOT NULL DEFAULT 'unknown',
          model text NOT NULL DEFAULT 'unknown',
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
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, stat_date, provider_code, model)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_model_hourly (
          system_account_id text NOT NULL,
          stat_hour text NOT NULL,
          provider_code text NOT NULL DEFAULT 'unknown',
          model text NOT NULL DEFAULT 'unknown',
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
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, stat_hour, provider_code, model)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_model_weekly (
          system_account_id text NOT NULL,
          stat_week text NOT NULL,
          provider_code text NOT NULL DEFAULT 'unknown',
          model text NOT NULL DEFAULT 'unknown',
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
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, stat_week, provider_code, model)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_model_monthly (
          system_account_id text NOT NULL,
          stat_month text NOT NULL,
          provider_code text NOT NULL DEFAULT 'unknown',
          model text NOT NULL DEFAULT 'unknown',
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
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, stat_month, provider_code, model)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_error_minute (
          system_account_id text NOT NULL,
          stat_minute text NOT NULL,
          error_group text NOT NULL DEFAULT 'unknown',
          provider_code text NOT NULL DEFAULT 'unknown',
          error_code text NOT NULL DEFAULT 'unknown',
          status_code integer NOT NULL DEFAULT 0,
          error_message text,
          request_count bigint NOT NULL DEFAULT 0,
          error_count bigint NOT NULL DEFAULT 0,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, stat_minute, error_group, provider_code, error_code, status_code)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_error_daily (
          system_account_id text NOT NULL,
          stat_date text NOT NULL,
          error_group text NOT NULL DEFAULT 'unknown',
          provider_code text NOT NULL DEFAULT 'unknown',
          error_code text NOT NULL DEFAULT 'unknown',
          status_code integer NOT NULL DEFAULT 0,
          error_message text,
          request_count bigint NOT NULL DEFAULT 0,
          error_count bigint NOT NULL DEFAULT 0,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, stat_date, error_group, provider_code, error_code, status_code)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_error_hourly (
          system_account_id text NOT NULL,
          stat_hour text NOT NULL,
          error_group text NOT NULL DEFAULT 'unknown',
          provider_code text NOT NULL DEFAULT 'unknown',
          error_code text NOT NULL DEFAULT 'unknown',
          status_code integer NOT NULL DEFAULT 0,
          error_message text,
          request_count bigint NOT NULL DEFAULT 0,
          error_count bigint NOT NULL DEFAULT 0,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, stat_hour, error_group, provider_code, error_code, status_code)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_error_weekly (
          system_account_id text NOT NULL,
          stat_week text NOT NULL,
          error_group text NOT NULL DEFAULT 'unknown',
          provider_code text NOT NULL DEFAULT 'unknown',
          error_code text NOT NULL DEFAULT 'unknown',
          status_code integer NOT NULL DEFAULT 0,
          error_message text,
          request_count bigint NOT NULL DEFAULT 0,
          error_count bigint NOT NULL DEFAULT 0,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, stat_week, error_group, provider_code, error_code, status_code)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_error_monthly (
          system_account_id text NOT NULL,
          stat_month text NOT NULL,
          error_group text NOT NULL DEFAULT 'unknown',
          provider_code text NOT NULL DEFAULT 'unknown',
          error_code text NOT NULL DEFAULT 'unknown',
          status_code integer NOT NULL DEFAULT 0,
          error_message text,
          request_count bigint NOT NULL DEFAULT 0,
          error_count bigint NOT NULL DEFAULT 0,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, stat_month, error_group, provider_code, error_code, status_code)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_latency_minute (
          system_account_id text NOT NULL,
          scope_type text NOT NULL,
          scope_id text NOT NULL DEFAULT '',
          metric_type text NOT NULL,
          stat_minute text NOT NULL,
          bucket_upper_bound_ms integer NOT NULL,
          sample_count bigint NOT NULL DEFAULT 0,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, metric_type, stat_minute, bucket_upper_bound_ms)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_latency_hourly (
          system_account_id text NOT NULL,
          scope_type text NOT NULL,
          scope_id text NOT NULL DEFAULT '',
          metric_type text NOT NULL,
          stat_hour text NOT NULL,
          bucket_upper_bound_ms integer NOT NULL,
          sample_count bigint NOT NULL DEFAULT 0,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, metric_type, stat_hour, bucket_upper_bound_ms)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_latency_daily (
          system_account_id text NOT NULL,
          scope_type text NOT NULL,
          scope_id text NOT NULL DEFAULT '',
          metric_type text NOT NULL,
          stat_date text NOT NULL,
          bucket_upper_bound_ms integer NOT NULL,
          sample_count bigint NOT NULL DEFAULT 0,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, metric_type, stat_date, bucket_upper_bound_ms)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_latency_weekly (
          system_account_id text NOT NULL,
          scope_type text NOT NULL,
          scope_id text NOT NULL DEFAULT '',
          metric_type text NOT NULL,
          stat_week text NOT NULL,
          bucket_upper_bound_ms integer NOT NULL,
          sample_count bigint NOT NULL DEFAULT 0,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, metric_type, stat_week, bucket_upper_bound_ms)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_latency_monthly (
          system_account_id text NOT NULL,
          scope_type text NOT NULL,
          scope_id text NOT NULL DEFAULT '',
          metric_type text NOT NULL,
          stat_month text NOT NULL,
          bucket_upper_bound_ms integer NOT NULL,
          sample_count bigint NOT NULL DEFAULT 0,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, metric_type, stat_month, bucket_upper_bound_ms)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_rank_snapshots (
          system_account_id text NOT NULL,
          scope_type text NOT NULL,
          window_key text NOT NULL,
          metric text NOT NULL,
          snapshot_at text NOT NULL,
          rank integer NOT NULL,
          scope_id text NOT NULL,
          metric_value double precision NOT NULL DEFAULT 0,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_overview_summary_windows (
          system_account_id text NOT NULL,
          window_key text NOT NULL,
          start_date text NOT NULL DEFAULT '',
          end_date text NOT NULL DEFAULT '',
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
          first_token_ms_sum bigint NOT NULL DEFAULT 0,
          first_token_ms_count bigint NOT NULL DEFAULT 0,
          last_used_at text,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, window_key)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_overview_trend_windows (
          system_account_id text NOT NULL,
          window_key text NOT NULL,
          start_date text NOT NULL DEFAULT '',
          end_date text NOT NULL DEFAULT '',
          bucket_key text NOT NULL,
          request_count bigint NOT NULL DEFAULT 0,
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
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, window_key, bucket_key)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_model_rank_windows (
          system_account_id text NOT NULL,
          window_key text NOT NULL,
          start_date text NOT NULL DEFAULT '',
          end_date text NOT NULL DEFAULT '',
          rank integer NOT NULL,
          provider_code text NOT NULL DEFAULT 'unknown',
          model text NOT NULL DEFAULT 'unknown',
          request_count bigint NOT NULL DEFAULT 0,
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
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, window_key, rank, provider_code, model)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_error_rank_windows (
          system_account_id text NOT NULL,
          window_key text NOT NULL,
          start_date text NOT NULL DEFAULT '',
          end_date text NOT NULL DEFAULT '',
          rank integer NOT NULL,
          provider_code text NOT NULL DEFAULT 'unknown',
          error_code text NOT NULL DEFAULT 'unknown',
          status_code integer NOT NULL DEFAULT 0,
          error_message text,
          error_count bigint NOT NULL DEFAULT 0,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, window_key, rank, provider_code, error_code, status_code)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS ai_performance_summary_windows (
          system_account_id text NOT NULL,
          window_key text NOT NULL,
          start_date text NOT NULL DEFAULT '',
          end_date text NOT NULL DEFAULT '',
          request_count bigint NOT NULL DEFAULT 0,
          duration_ms_sum bigint NOT NULL DEFAULT 0,
          duration_ms_count bigint NOT NULL DEFAULT 0,
          duration_ms_max bigint NOT NULL DEFAULT 0,
          first_token_ms_sum bigint NOT NULL DEFAULT 0,
          first_token_ms_count bigint NOT NULL DEFAULT 0,
          first_token_ms_max bigint NOT NULL DEFAULT 0,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, window_key)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_quota_hourly_windows (
          system_account_id text NOT NULL,
          scope_type text NOT NULL,
          scope_id text NOT NULL DEFAULT '',
          window_hours integer NOT NULL,
          total_cost_usd double precision NOT NULL DEFAULT 0,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, window_hours)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_quota_hourly_window_dirty_scopes (
          system_account_id text NOT NULL,
          scope_type text NOT NULL,
          scope_id text NOT NULL DEFAULT '',
          generation bigint NOT NULL DEFAULT 1,
          first_dirty_at text NOT NULL,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_overview_dirty_scopes (
          system_account_id text PRIMARY KEY,
          scope_id text NOT NULL,
          min_changed_date text NOT NULL,
          generation bigint NOT NULL DEFAULT 1,
          first_dirty_at text NOT NULL,
          updated_at text NOT NULL
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS ai_performance_summary_dirty_system_accounts (
          system_account_id text PRIMARY KEY,
          min_stat_date text NOT NULL,
          max_stat_date text NOT NULL,
          generation bigint NOT NULL DEFAULT 1,
          first_dirty_at text NOT NULL,
          updated_at text NOT NULL
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_scope_range_windows (
          system_account_id text NOT NULL,
          scope_type text NOT NULL,
          scope_id text NOT NULL DEFAULT '',
          start_date text NOT NULL,
          end_date text NOT NULL,
          window_key text GENERATED ALWAYS AS (start_date || ':' || end_date) STORED,
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
          first_token_ms_sum bigint NOT NULL DEFAULT 0,
          first_token_ms_count bigint NOT NULL DEFAULT 0,
          first_token_ms_max bigint NOT NULL DEFAULT 0,
          active_days integer NOT NULL DEFAULT 0,
          last_used_at text,
          last_error_at text,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, start_date, end_date)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_range_window_requests (
          id text PRIMARY KEY,
          domain text NOT NULL,
          system_account_id text NOT NULL,
          scope_type text NOT NULL,
          scope_id text NOT NULL DEFAULT '',
          start_date text NOT NULL,
          end_date text NOT NULL,
          window_key text GENERATED ALWAYS AS (start_date || ':' || end_date) STORED,
          status text NOT NULL DEFAULT 'pending',
          requested_count integer NOT NULL DEFAULT 1,
          last_requested_at text NOT NULL,
          last_processed_at text,
          error_message text,
          expires_at text NOT NULL,
          created_at text NOT NULL,
          updated_at text NOT NULL,
          UNIQUE (domain, system_account_id, scope_type, scope_id, start_date, end_date),
          CHECK (status IN ('pending', 'processing', 'completed', 'failed'))
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS client_ip_registry (
          ip_hash text PRIMARY KEY,
          bucket_no integer NOT NULL,
          aggregate_ip_key text NOT NULL,
          client_ip text NOT NULL,
          ip_version integer NOT NULL,
          first_seen_at text NOT NULL,
          last_seen_at text NOT NULL,
          created_at text NOT NULL,
          updated_at text NOT NULL
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS client_ip_stats_daily (
          ip_hash text NOT NULL,
          stat_date text NOT NULL,
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
          first_token_ms_sum bigint NOT NULL DEFAULT 0,
          first_token_ms_count bigint NOT NULL DEFAULT 0,
          last_used_at text,
          last_error_at text,
          updated_at text NOT NULL,
          PRIMARY KEY (ip_hash, stat_date)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS client_ip_usage_range_windows (
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
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS client_ip_range_window_dirty_ips (
          ip_hash text PRIMARY KEY,
          generation bigint NOT NULL DEFAULT 1,
          first_dirty_at text NOT NULL,
          updated_at text NOT NULL
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS client_ip_account_stats_daily (
          ip_hash text NOT NULL,
          account_id text NOT NULL,
          stat_date text NOT NULL,
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
          first_token_ms_sum bigint NOT NULL DEFAULT 0,
          first_token_ms_count bigint NOT NULL DEFAULT 0,
          last_used_at text,
          last_error_at text,
          updated_at text NOT NULL,
          PRIMARY KEY (ip_hash, account_id, stat_date)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS client_ip_account_usage_range_windows (
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
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS client_ip_account_range_window_dirty_ips (
          ip_hash text PRIMARY KEY,
          generation bigint NOT NULL DEFAULT 1,
          first_dirty_at text NOT NULL,
          updated_at text NOT NULL
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS client_ip_policies (
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
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS client_ip_policy_hits (
          ip_hash text NOT NULL,
          stat_date text NOT NULL,
          policy_id text NOT NULL,
          hit_count bigint NOT NULL DEFAULT 0,
          last_hit_at text,
          updated_at text NOT NULL,
          PRIMARY KEY (ip_hash, stat_date, policy_id)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS stats_job_state (
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
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS background_task_runs (
          run_id text PRIMARY KEY,
          job_name text NOT NULL,
          job_type text NOT NULL,
          worker_role text NOT NULL,
          status text NOT NULL,
          lease_key text NOT NULL,
          owner_id text,
          params_json text NOT NULL DEFAULT '{}',
          result_json text NOT NULL DEFAULT '{}',
          error_message text,
          submitted_at text NOT NULL,
          started_at text,
          heartbeat_at text,
          finished_at text,
          duration_ms integer,
          exit_code integer,
          created_at text NOT NULL,
          updated_at text NOT NULL
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS background_job_leases (
          lease_key text PRIMARY KEY,
          job_name text NOT NULL,
          shard_key text NOT NULL DEFAULT '',
          owner_id text NOT NULL,
          run_id text,
          fencing_token bigint NOT NULL DEFAULT 0,
          lease_until text NOT NULL,
          heartbeat_at text NOT NULL,
          started_at text NOT NULL,
          updated_at text NOT NULL
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS usage_record_cleanup_deductions (
          usage_id text NOT NULL,
          api_key_id text NOT NULL,
          account_id text,
          system_account_id text NOT NULL,
          source_shard_key text NOT NULL,
          record_json text NOT NULL,
          stats_subtracted_at text,
          shard_deleted_at text,
          created_at text NOT NULL,
          updated_at text NOT NULL,
          PRIMARY KEY (usage_id, source_shard_key)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS system_metrics_samples (
          id text PRIMARY KEY,
          sampled_at text NOT NULL,
          cpu_percent double precision,
          memory_used_percent double precision,
          memory_total_bytes BIGINT,
          memory_free_bytes BIGINT,
          process_rss_bytes BIGINT,
          process_heap_used_bytes BIGINT,
          process_heap_total_bytes BIGINT,
          event_loop_lag_ms double precision,
          network_rx_bytes_per_sec double precision,
          network_tx_bytes_per_sec double precision,
          network_rx_total_bytes BIGINT,
          network_tx_total_bytes BIGINT,
          db_file_bytes BIGINT,
          stats_lag_seconds integer,
          created_at text NOT NULL
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS system_metrics_hourly (
          stat_hour text PRIMARY KEY,
          sample_count bigint NOT NULL DEFAULT 0,
          cpu_percent_sum double precision NOT NULL DEFAULT 0,
          cpu_percent_max double precision,
          memory_used_percent_sum double precision NOT NULL DEFAULT 0,
          memory_used_percent_max double precision,
          process_rss_bytes_sum BIGINT NOT NULL DEFAULT 0,
          process_rss_bytes_max BIGINT,
          process_heap_used_bytes_sum BIGINT NOT NULL DEFAULT 0,
          process_heap_used_bytes_max BIGINT,
          event_loop_lag_ms_sum double precision NOT NULL DEFAULT 0,
          event_loop_lag_ms_count bigint NOT NULL DEFAULT 0,
          event_loop_lag_ms_max double precision,
          network_rx_bytes_per_sec_sum double precision NOT NULL DEFAULT 0,
          network_rx_bytes_per_sec_max double precision,
          network_rx_bytes_per_sec_count bigint NOT NULL DEFAULT 0,
          network_tx_bytes_per_sec_sum double precision NOT NULL DEFAULT 0,
          network_tx_bytes_per_sec_max double precision,
          network_tx_bytes_per_sec_count bigint NOT NULL DEFAULT 0,
          network_rx_total_bytes_max BIGINT,
          network_tx_total_bytes_max BIGINT,
          db_file_bytes_max BIGINT,
          stats_lag_seconds_max integer,
          updated_at text NOT NULL
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS system_metrics_trend_windows (
          window_key text NOT NULL,
          start_date text NOT NULL DEFAULT '',
          end_date text NOT NULL DEFAULT '',
          bucket_key text NOT NULL,
          sample_count bigint NOT NULL DEFAULT 0,
          cpu_percent_sum double precision NOT NULL DEFAULT 0,
          cpu_percent_max double precision,
          memory_used_percent_sum double precision NOT NULL DEFAULT 0,
          memory_used_percent_max double precision,
          process_rss_bytes_sum BIGINT NOT NULL DEFAULT 0,
          process_rss_bytes_max BIGINT,
          process_heap_used_bytes_sum BIGINT NOT NULL DEFAULT 0,
          process_heap_used_bytes_max BIGINT,
          event_loop_lag_ms_sum double precision NOT NULL DEFAULT 0,
          event_loop_lag_ms_count bigint NOT NULL DEFAULT 0,
          event_loop_lag_ms_max double precision,
          network_rx_bytes_per_sec_sum double precision NOT NULL DEFAULT 0,
          network_rx_bytes_per_sec_max double precision,
          network_rx_bytes_per_sec_count bigint NOT NULL DEFAULT 0,
          network_tx_bytes_per_sec_sum double precision NOT NULL DEFAULT 0,
          network_tx_bytes_per_sec_max double precision,
          network_tx_bytes_per_sec_count bigint NOT NULL DEFAULT 0,
          network_rx_total_bytes_max BIGINT,
          network_tx_total_bytes_max BIGINT,
          db_file_bytes_max BIGINT,
          stats_lag_seconds_max integer,
          updated_at text NOT NULL,
          PRIMARY KEY (window_key, bucket_key)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS process_event_loop_samples (
          id text PRIMARY KEY,
          sampled_at text NOT NULL,
          process_role text NOT NULL,
          process_pid integer,
          event_loop_lag_ms double precision,
          process_rss_bytes BIGINT,
          process_heap_used_bytes BIGINT,
          process_heap_total_bytes BIGINT,
          process_external_bytes BIGINT,
          process_array_buffers_bytes BIGINT,
          created_at text NOT NULL
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS process_event_loop_hourly (
          stat_hour text NOT NULL,
          process_role text NOT NULL,
          sample_count bigint NOT NULL DEFAULT 0,
          event_loop_lag_ms_sum double precision NOT NULL DEFAULT 0,
          event_loop_lag_ms_count bigint NOT NULL DEFAULT 0,
          event_loop_lag_ms_max double precision,
          process_rss_bytes_sum BIGINT NOT NULL DEFAULT 0,
          process_rss_bytes_max BIGINT,
          process_heap_used_bytes_sum BIGINT NOT NULL DEFAULT 0,
          process_heap_used_bytes_max BIGINT,
          process_heap_total_bytes_sum BIGINT NOT NULL DEFAULT 0,
          process_heap_total_bytes_max BIGINT,
          process_external_bytes_sum BIGINT NOT NULL DEFAULT 0,
          process_external_bytes_max BIGINT,
          process_array_buffers_bytes_sum BIGINT NOT NULL DEFAULT 0,
          process_array_buffers_bytes_max BIGINT,
          updated_at text NOT NULL,
          PRIMARY KEY (stat_hour, process_role)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE TABLE IF NOT EXISTS process_event_loop_trend_windows (
          window_key text NOT NULL,
          start_date text NOT NULL DEFAULT '',
          end_date text NOT NULL DEFAULT '',
          bucket_key text NOT NULL,
          process_role text NOT NULL,
          sample_count bigint NOT NULL DEFAULT 0,
          event_loop_lag_ms_sum double precision NOT NULL DEFAULT 0,
          event_loop_lag_ms_count bigint NOT NULL DEFAULT 0,
          event_loop_lag_ms_max double precision,
          process_rss_bytes_sum BIGINT NOT NULL DEFAULT 0,
          process_rss_bytes_max BIGINT,
          process_heap_used_bytes_sum BIGINT NOT NULL DEFAULT 0,
          process_heap_used_bytes_max BIGINT,
          process_heap_total_bytes_sum BIGINT NOT NULL DEFAULT 0,
          process_heap_total_bytes_max BIGINT,
          process_external_bytes_sum BIGINT NOT NULL DEFAULT 0,
          process_external_bytes_max BIGINT,
          process_array_buffers_bytes_sum BIGINT NOT NULL DEFAULT 0,
          process_array_buffers_bytes_max BIGINT,
          updated_at text NOT NULL,
          PRIMARY KEY (window_key, bucket_key, process_role)
        )`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "account-list-projection-pg-quota-crossing-triggers",
		SQL: `
CREATE OR REPLACE FUNCTION account_list_availability_quota_crossing_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
DECLARE
  v_old_cost double precision;
  v_new_cost double precision;
BEGIN
  v_old_cost := CASE WHEN TG_OP = 'INSERT' THEN 0 ELSE COALESCE(OLD.total_cost_usd, 0) END;
  v_new_cost := CASE WHEN TG_OP = 'DELETE' THEN 0 ELSE COALESCE(NEW.total_cost_usd, 0) END;
  PERFORM juhe_business.account_list_availability_mark_dirty_quota_crossing(
    COALESCE(NEW.scope_type, OLD.scope_type),
    COALESCE(NEW.scope_id, OLD.scope_id),
    TG_ARGV[0], v_old_cost, v_new_cost
  );
  RETURN COALESCE(NEW, OLD);
END;
$function$;

DROP TRIGGER IF EXISTS account_list_availability_quota_total ON usage_stats_totals;
CREATE TRIGGER account_list_availability_quota_total
AFTER INSERT OR UPDATE OR DELETE ON usage_stats_totals
FOR EACH ROW EXECUTE FUNCTION account_list_availability_quota_crossing_trigger('total');
DROP TRIGGER IF EXISTS account_list_availability_quota_daily ON usage_stats_daily;
CREATE TRIGGER account_list_availability_quota_daily
AFTER INSERT OR UPDATE OR DELETE ON usage_stats_daily
FOR EACH ROW EXECUTE FUNCTION account_list_availability_quota_crossing_trigger('daily');
DROP TRIGGER IF EXISTS account_list_availability_quota_weekly ON usage_stats_weekly;
CREATE TRIGGER account_list_availability_quota_weekly
AFTER INSERT OR UPDATE OR DELETE ON usage_stats_weekly
FOR EACH ROW EXECUTE FUNCTION account_list_availability_quota_crossing_trigger('weekly');
DROP TRIGGER IF EXISTS account_list_availability_quota_monthly ON usage_stats_monthly;
CREATE TRIGGER account_list_availability_quota_monthly
AFTER INSERT OR UPDATE OR DELETE ON usage_stats_monthly
FOR EACH ROW EXECUTE FUNCTION account_list_availability_quota_crossing_trigger('monthly');
DROP TRIGGER IF EXISTS account_list_availability_quota_hourly ON usage_quota_hourly_windows;
CREATE TRIGGER account_list_availability_quota_hourly
AFTER INSERT OR UPDATE OR DELETE ON usage_quota_hourly_windows
FOR EACH ROW EXECUTE FUNCTION account_list_availability_quota_crossing_trigger('hourly');
`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE INDEX IF NOT EXISTS idx_background_task_runs_status_updated
      ON background_task_runs(status, updated_at DESC, run_id DESC)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE INDEX IF NOT EXISTS idx_background_task_runs_job_created
      ON background_task_runs(job_name, created_at DESC, run_id DESC)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE INDEX IF NOT EXISTS idx_background_job_leases_job
      ON background_job_leases(job_name, shard_key, lease_until)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE INDEX IF NOT EXISTS idx_usage_record_cleanup_deductions_target
      ON usage_record_cleanup_deductions(api_key_id, system_account_id, shard_deleted_at)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_quality_minute_stats_minute ON account_quality_minute_stats(stat_minute, account_id)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_health_hourly_scope
      ON account_health_hourly(system_account_id, stat_hour, account_id)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_group_account_stats_group ON group_account_stats(group_id)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_quality_scores_sort ON account_quality_scores(provider_code, quality_score, quality_state)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_quality_scores_failure_precheck
      ON account_quality_scores(recent_error_count DESC, success_rate, updated_at DESC, account_id)
      WHERE recent_request_count >= 5 AND recent_error_count >= 2`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_quality_dirty_accounts_first_dirty ON account_quality_dirty_accounts(first_dirty_at, account_id)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_usage_snapshots_kind ON account_usage_snapshots(kind, updated_at)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_usage_snapshots_kind_account ON account_usage_snapshots(kind, account_id)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE INDEX IF NOT EXISTS idx_stats_job_state_usage_shard_cursor_floor
      ON stats_job_state(scope_type, job_name, cursor_created_at, cursor_id)
      WHERE cursor_created_at IS NOT NULL
        AND cursor_id IS NOT NULL`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE INDEX IF NOT EXISTS idx_stats_job_state_usage_shard_cursor_floor_any_job
      ON stats_job_state(scope_type, cursor_created_at, cursor_id, job_name)
      WHERE cursor_created_at IS NOT NULL
        AND cursor_id IS NOT NULL`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_stats_totals_updated ON usage_stats_totals(updated_at)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE INDEX IF NOT EXISTS idx_usage_stats_totals_scope_seed
      ON usage_stats_totals(scope_type, system_account_id, scope_id)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_stats_minute_scope_minute ON usage_stats_minute(system_account_id, scope_type, scope_id, stat_minute)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_stats_minute_minute ON usage_stats_minute(stat_minute)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_stats_daily_scope_date ON usage_stats_daily(system_account_id, scope_type, scope_id, stat_date)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE INDEX IF NOT EXISTS idx_usage_stats_daily_system_scope_date
      ON usage_stats_daily(system_account_id, scope_type, stat_date, scope_id)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE INDEX IF NOT EXISTS idx_usage_stats_daily_system_account_top_activity
      ON usage_stats_daily(stat_date, request_count DESC, last_used_at DESC, system_account_id)
      WHERE scope_type = 'system_account'
        AND scope_id = system_account_id
        AND system_account_id <> 'global'`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_stats_daily_date ON usage_stats_daily(stat_date)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_stats_daily_updated ON usage_stats_daily(updated_at)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_stats_hourly_scope_hour ON usage_stats_hourly(system_account_id, scope_type, scope_id, stat_hour)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_stats_hourly_scope_stat_hour ON usage_stats_hourly(system_account_id, scope_type, stat_hour, scope_id)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_stats_hourly_hour ON usage_stats_hourly(stat_hour)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_stats_hourly_updated ON usage_stats_hourly(updated_at)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_stats_weekly_scope_week ON usage_stats_weekly(system_account_id, scope_type, scope_id, stat_week)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_stats_weekly_week ON usage_stats_weekly(stat_week)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_stats_monthly_scope_month ON usage_stats_monthly(system_account_id, scope_type, scope_id, stat_month)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_stats_monthly_month ON usage_stats_monthly(stat_month)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_stats_monthly_updated ON usage_stats_monthly(updated_at)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_authorization_team_usage_summary_daily_lookup ON authorization_team_usage_summary_daily(system_account_id, stat_date, team_filter_id, resource_filter_type, resource_filter_id)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_authorization_team_usage_summary_daily_updated ON authorization_team_usage_summary_daily(updated_at)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_authorization_team_usage_range_lookup ON authorization_team_usage_range_windows(system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_authorization_team_usage_range_sort ON authorization_team_usage_range_windows(system_account_id, start_date, end_date, total_cost_usd DESC, request_count DESC, last_used_at DESC, team_filter_id, resource_filter_type, resource_filter_id)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_authorization_team_usage_range_end ON authorization_team_usage_range_windows(end_date)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_authorization_user_usage_summary_daily_lookup ON authorization_user_usage_summary_daily(system_account_id, stat_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_authorization_user_usage_summary_daily_updated ON authorization_user_usage_summary_daily(updated_at)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_authorization_user_usage_range_lookup ON authorization_user_usage_range_windows(system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_authorization_user_usage_range_sort ON authorization_user_usage_range_windows(system_account_id, start_date, end_date, team_filter_id, total_cost_usd DESC, request_count DESC, last_used_at DESC, grantee_filter_system_account_id, resource_filter_type, resource_filter_id)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_authorization_user_usage_range_end ON authorization_user_usage_range_windows(end_date)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_model_minute_minute ON usage_model_minute(system_account_id, stat_minute, model)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_model_minute_stat_minute ON usage_model_minute(stat_minute)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_model_daily_date ON usage_model_daily(system_account_id, stat_date, model)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_model_daily_stat_date ON usage_model_daily(stat_date)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_model_daily_updated ON usage_model_daily(updated_at)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_model_daily_account_date_provider_model ON usage_model_daily(system_account_id, stat_date, provider_code, model)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_model_hourly_hour ON usage_model_hourly(system_account_id, stat_hour, model)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_model_hourly_stat_hour ON usage_model_hourly(stat_hour)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_model_weekly_week ON usage_model_weekly(system_account_id, stat_week, model)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_model_weekly_stat_week ON usage_model_weekly(stat_week)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_model_monthly_month ON usage_model_monthly(system_account_id, stat_month, model)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_model_monthly_stat_month ON usage_model_monthly(stat_month)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_error_minute_minute ON usage_error_minute(system_account_id, stat_minute, error_code)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_error_minute_stat_minute ON usage_error_minute(stat_minute)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_error_daily_date ON usage_error_daily(system_account_id, stat_date, error_code)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_error_daily_stat_date ON usage_error_daily(stat_date)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_error_daily_updated ON usage_error_daily(updated_at)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_error_hourly_hour ON usage_error_hourly(system_account_id, stat_hour, error_code)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_error_hourly_stat_hour ON usage_error_hourly(stat_hour)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_error_weekly_week ON usage_error_weekly(system_account_id, stat_week, error_code)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_error_weekly_stat_week ON usage_error_weekly(stat_week)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_error_monthly_month ON usage_error_monthly(system_account_id, stat_month, error_code)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_error_monthly_stat_month ON usage_error_monthly(stat_month)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_latency_minute_minute ON usage_latency_minute(stat_minute)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_latency_hourly_hour ON usage_latency_hourly(stat_hour)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_latency_daily_date ON usage_latency_daily(stat_date)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_latency_weekly_week ON usage_latency_weekly(stat_week)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_latency_monthly_month ON usage_latency_monthly(stat_month)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_rank_snapshots_lookup ON usage_rank_snapshots(system_account_id, scope_type, window_key, metric, snapshot_at DESC, rank)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_rank_snapshots_snapshot ON usage_rank_snapshots(snapshot_at)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_overview_summary_windows_end ON usage_overview_summary_windows(end_date)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE INDEX IF NOT EXISTS idx_usage_quota_hourly_window_dirty_updated
      ON usage_quota_hourly_window_dirty_scopes(first_dirty_at, system_account_id, scope_type, scope_id)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE INDEX IF NOT EXISTS idx_usage_overview_dirty_first_dirty
      ON usage_overview_dirty_scopes(first_dirty_at, system_account_id)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE INDEX IF NOT EXISTS idx_ai_performance_summary_dirty_first_dirty
      ON ai_performance_summary_dirty_system_accounts(first_dirty_at, system_account_id)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE INDEX IF NOT EXISTS idx_usage_overview_summary_windows_prewarm_order
      ON usage_overview_summary_windows(window_key, request_count DESC, last_used_at DESC, system_account_id)
      WHERE request_count > 0
        AND system_account_id <> 'global'`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_overview_trend_windows_lookup ON usage_overview_trend_windows(system_account_id, window_key, bucket_key)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_overview_trend_windows_end ON usage_overview_trend_windows(end_date)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_model_rank_windows_lookup ON usage_model_rank_windows(system_account_id, window_key, rank)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_model_rank_windows_end ON usage_model_rank_windows(end_date)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_error_rank_windows_lookup ON usage_error_rank_windows(system_account_id, window_key, rank)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_error_rank_windows_end ON usage_error_rank_windows(end_date)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_ai_performance_summary_windows_lookup ON ai_performance_summary_windows(system_account_id, window_key)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_ai_performance_summary_windows_end ON ai_performance_summary_windows(end_date)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_quota_hourly_windows_lookup ON usage_quota_hourly_windows(system_account_id, scope_type, scope_id, window_hours)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_quota_hourly_windows_updated ON usage_quota_hourly_windows(updated_at)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_lookup ON usage_scope_range_windows(system_account_id, scope_type, scope_id, window_key)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_range_lookup ON usage_scope_range_windows(system_account_id, scope_type, window_key, scope_id)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_account_usage_order ON usage_scope_range_windows(system_account_id, scope_type, window_key, request_count DESC, total_cost_usd DESC, (input_tokens + output_tokens) DESC, last_used_at DESC, scope_id)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_end ON usage_scope_range_windows(end_date)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_end_start ON usage_scope_range_windows(end_date, start_date)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_range_window_requests_pending ON usage_range_window_requests(status, domain, updated_at, id)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_range_window_requests_expires ON usage_range_window_requests(expires_at, domain, status)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_client_ip_registry_bucket ON client_ip_registry(bucket_no, ip_hash)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_client_ip_registry_last_seen ON client_ip_registry(last_seen_at DESC, ip_hash)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_client_ip_registry_ip ON client_ip_registry(aggregate_ip_key)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_client_ip_registry_client_ip ON client_ip_registry(client_ip)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_client_ip_stats_daily_date ON client_ip_stats_daily(stat_date, ip_hash)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_client_ip_range_requests ON client_ip_usage_range_windows(start_date, end_date, request_count DESC, ip_hash)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_client_ip_range_end ON client_ip_usage_range_windows(end_date)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_client_ip_range_dirty_updated ON client_ip_range_window_dirty_ips(first_dirty_at ASC, ip_hash)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_client_ip_account_daily_date ON client_ip_account_stats_daily(stat_date, ip_hash, account_id)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_client_ip_account_daily_ip_date ON client_ip_account_stats_daily(ip_hash, stat_date, account_id)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_client_ip_account_range_requests ON client_ip_account_usage_range_windows(ip_hash, start_date, end_date, request_count DESC, account_id)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_client_ip_account_range_dirty_updated ON client_ip_account_range_window_dirty_ips(first_dirty_at ASC, ip_hash)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_client_ip_policies_active_unique ON client_ip_policies(ip_hash) WHERE status = 'active'`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_client_ip_policies_active ON client_ip_policies(status, policy_type, ip_hash, expires_at)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_client_ip_policies_ip ON client_ip_policies(ip_hash, status, policy_type, created_at DESC)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_client_ip_policy_hits_date ON client_ip_policy_hits(stat_date DESC, ip_hash)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_usage_snapshots_updated ON account_usage_snapshots(updated_at)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_metrics_trend_windows_lookup ON system_metrics_trend_windows(window_key, bucket_key)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_metrics_trend_windows_end ON system_metrics_trend_windows(end_date)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_metrics_samples_sampled_at ON system_metrics_samples(sampled_at)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_metrics_samples_latest ON system_metrics_samples(sampled_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_metrics_hourly_updated ON system_metrics_hourly(updated_at)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_process_event_loop_samples_sampled_at ON process_event_loop_samples(sampled_at)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_process_event_loop_samples_role_latest ON process_event_loop_samples(process_role, sampled_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE INDEX IF NOT EXISTS idx_process_event_loop_samples_role_peak
      ON process_event_loop_samples(process_role, event_loop_lag_ms DESC, sampled_at DESC, id DESC)
      WHERE event_loop_lag_ms IS NOT NULL`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_process_event_loop_hourly_lookup ON process_event_loop_hourly(stat_hour, process_role)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_process_event_loop_hourly_updated ON process_event_loop_hourly(updated_at)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_process_event_loop_trend_windows_lookup ON process_event_loop_trend_windows(window_key, bucket_key, process_role)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_process_event_loop_trend_windows_end ON process_event_loop_trend_windows(end_date)`,
	},
	{
		SchemaName: "juhe_stats",
		Source:     "stats",
		SQL: `CREATE INDEX IF NOT EXISTS idx_usage_record_cleanup_deductions_account
      ON usage_record_cleanup_deductions(account_id, shard_deleted_at)`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL: `CREATE TABLE IF NOT EXISTS codex_context_sessions (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      api_key_id text,
      group_id text NOT NULL,
      provider_code text NOT NULL,
      source_response_id text,
      latest_response_id text,
      latest_compact_id text,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      last_used_at text NOT NULL,
      expires_at text NOT NULL
    )`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL: `CREATE TABLE IF NOT EXISTS codex_context_responses (
      response_id text PRIMARY KEY,
      session_id text NOT NULL,
      previous_response_id text,
      system_account_id text NOT NULL,
      api_key_id text,
      group_id text NOT NULL,
      provider_code text NOT NULL,
      upstream_account_id text,
      model text,
      upstream_model text,
      storage_key text NOT NULL,
      storage_offset_bytes bigint NOT NULL,
      sha256 text NOT NULL,
      raw_size_bytes bigint NOT NULL,
      compressed_size_bytes bigint NOT NULL,
      compression text NOT NULL DEFAULT 'gzip',
      schema_version integer NOT NULL DEFAULT 1,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      last_used_at text NOT NULL,
      expires_at text NOT NULL
    )`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL: `CREATE TABLE IF NOT EXISTS codex_context_compacts (
      compact_id text PRIMARY KEY,
      session_id text NOT NULL,
      source_response_id text,
      summary_digest text NOT NULL,
      system_account_id text NOT NULL,
      api_key_id text,
      group_id text NOT NULL,
      provider_code text NOT NULL,
      upstream_account_id text,
      model text,
      upstream_model text,
      storage_key text NOT NULL,
      storage_offset_bytes bigint NOT NULL,
      sha256 text NOT NULL,
      raw_size_bytes bigint NOT NULL,
      compressed_size_bytes bigint NOT NULL,
      compression text NOT NULL DEFAULT 'gzip',
      schema_version integer NOT NULL DEFAULT 1,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      last_used_at text NOT NULL,
      expires_at text NOT NULL
    )`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL: `CREATE TABLE IF NOT EXISTS codex_context_storage_cleanup_queue (
      storage_key text PRIMARY KEY,
      enqueued_at text NOT NULL,
      updated_at text NOT NULL,
      next_attempt_at text NOT NULL,
      attempt_count integer NOT NULL DEFAULT 0,
      last_error text
    )`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_codex_context_sessions_expires ON codex_context_sessions(expires_at ASC, id ASC)`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_codex_context_sessions_last_used ON codex_context_sessions(last_used_at ASC, id ASC)`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_codex_context_sessions_boundary ON codex_context_sessions(system_account_id, api_key_id, group_id, provider_code)`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_codex_context_responses_session ON codex_context_responses(session_id, created_at ASC, response_id)`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_codex_context_responses_previous ON codex_context_responses(previous_response_id) WHERE previous_response_id IS NOT NULL`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_codex_context_responses_expires ON codex_context_responses(expires_at ASC, response_id)`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_codex_context_responses_boundary ON codex_context_responses(system_account_id, api_key_id, group_id, provider_code, response_id)`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_session ON codex_context_compacts(session_id, created_at ASC, compact_id)`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_source_response ON codex_context_compacts(source_response_id)`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_expires ON codex_context_compacts(expires_at ASC, compact_id)`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_boundary ON codex_context_compacts(system_account_id, api_key_id, group_id, provider_code, compact_id)`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_codex_context_storage_cleanup_due ON codex_context_storage_cleanup_queue(next_attempt_at ASC, enqueued_at ASC, storage_key ASC)`,
	},
}

// Statements returns the raw PostgreSQL statement list in execution order.
// goldenPostgresSchemaStatementCount in pg_schema_test.go pins its length to
// the Node source output.
func Statements() []string {
	statements := make([]string, len(postgresSchemaStatements))
	for i, statement := range postgresSchemaStatements {
		statements[i] = statement.SQL
	}
	return statements
}

// PGResult summarizes EnsurePostgres, mirroring the Node
// applyPostgresSchema return value.
type PGResult struct {
	SchemaCount    int
	StatementCount int
}

// EnsurePostgres applies the full PostgreSQL schema (business, chat, dataset,
// usage, stats and codex context) to db, executing CREATE SCHEMA once per
// schema group and every DDL statement in the Node applyPostgresSchema order.
// All statements are idempotent, so repeated calls are no-ops.
func EnsurePostgres(ctx context.Context, db *sql.DB) (PGResult, error) {
	createdSchemas := make(map[string]bool)
	for i, statement := range postgresSchemaStatements {
		if !createdSchemas[statement.SchemaName] {
			createdSchemas[statement.SchemaName] = true
			createSchema := fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quotePGIdentifier(statement.SchemaName))
			if _, err := db.ExecContext(ctx, createSchema); err != nil {
				return PGResult{}, fmt.Errorf("create postgres schema %s: %w", statement.SchemaName, err)
			}
		}
		execSQL := fmt.Sprintf("SET search_path TO %s, public;\n%s", quotePGIdentifier(statement.SchemaName), statement.SQL)
		if _, err := db.ExecContext(ctx, execSQL); err != nil {
			return PGResult{}, fmt.Errorf("postgres schema statement %d (%s/%s): %w", i, statement.SchemaName, statement.Source, err)
		}
	}
	return PGResult{SchemaCount: len(createdSchemas), StatementCount: len(postgresSchemaStatements)}, nil
}

// quotePGIdentifier mirrors quoteIdentifier in postgres-schema.ts.
func quotePGIdentifier(identifier string) string {
	return "\"" + strings.ReplaceAll(identifier, "\"", "\"\"") + "\""
}

// ---------- seeds (port of postgres-seed-defaults.ts) ----------

// PGSeedResult summarizes EnsurePostgresSeeds.
type PGSeedResult struct {
	StatementCount int
}

// pgSeedSystemAccountsInsert seeds the default super admin account.
const pgSeedSystemAccountsInsert = `
      INSERT INTO "juhe_business"."system_accounts" (
        id, username, display_name, description, role, status, password_hash, must_change_password, image_generation_enabled, created_at, updated_at
      ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
      ON CONFLICT DO NOTHING
    `

// pgSeedGlobalSettingsInsert seeds one global_settings row.
const pgSeedGlobalSettingsInsert = `
        INSERT INTO "juhe_business"."global_settings" (key, value_json, updated_at)
        VALUES ($1, $2, $3)
        ON CONFLICT DO NOTHING
      `

// pgSeedWindowConfigInsert seeds one request_quota_hourly_window_configs row.
const pgSeedWindowConfigInsert = `
        INSERT INTO "juhe_business"."request_quota_hourly_window_configs" (window_hours, created_at, updated_at)
        VALUES ($1, $2, $3)
        ON CONFLICT DO NOTHING
      `

// pgSeedQuotaScopeBindingsFromAPIKeysCTE backfills hourly quota scope bindings from active API keys (parameter-free, idempotent).
const pgSeedQuotaScopeBindingsFromAPIKeysCTE = `
    WITH inserted AS (
      INSERT INTO "juhe_business"."request_quota_hourly_window_scope_bindings" (
        system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at
      )
      SELECT system_account_id, 'api_key', id, 'api_key', id,
        (quota_limits_json::jsonb #>> '{hourly,hours}')::integer, created_at, updated_at
      FROM "juhe_business"."api_keys"
      WHERE status = 'active'
        AND quota_limits_json IS NOT NULL
        AND quota_limits_json::jsonb #>> '{hourly,enabled}' = 'true'
        AND quota_limits_json::jsonb #>> '{hourly,hours}' ~ '^[0-9]+$'
        AND (quota_limits_json::jsonb #>> '{hourly,hours}')::integer BETWEEN 1 AND 720
      ON CONFLICT(system_account_id, scope_type, scope_id) DO NOTHING
      RETURNING system_account_id, scope_type, scope_id, created_at, updated_at
    )
    INSERT INTO juhe_stats.usage_quota_hourly_window_dirty_scopes (
      system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at
    )
    SELECT system_account_id, scope_type, scope_id, 1, created_at, updated_at FROM inserted
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
      generation = usage_quota_hourly_window_dirty_scopes.generation + 1,
      updated_at = EXCLUDED.updated_at
  `

// pgSeedQuotaScopeBindingsFromAuthorizationsCTE backfills quota scope bindings from resource authorizations (parameter-free, idempotent).
const pgSeedQuotaScopeBindingsFromAuthorizationsCTE = `
    WITH inserted AS (
      INSERT INTO "juhe_business"."request_quota_hourly_window_scope_bindings" (
        system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at
      )
      SELECT CASE WHEN ra.resource_type = 'account' THEN ra.grantee_system_account_id ELSE ra.resource_owner_system_account_id END,
        CASE WHEN ra.resource_type = 'account' THEN 'account_authorization' ELSE 'group_authorization' END,
        ra.id, 'resource_authorization_grant', grants.id,
        (ra.limits_json::jsonb #>> '{hourly,hours}')::integer, ra.created_at, ra.updated_at
      FROM "juhe_business"."resource_authorizations" ra
      INNER JOIN "juhe_business"."resource_authorization_grants" grants
        ON grants.resource_type = ra.resource_type
        AND grants.resource_id = ra.resource_id
        AND grants.status = 'active'
        AND (
          (ra.effective_source_type = 'manual' AND grants.grantee_type = 'system_account' AND grants.grantee_system_account_id = ra.grantee_system_account_id)
          OR
          (ra.effective_source_type = 'team' AND grants.grantee_type = 'team' AND grants.grantee_team_id = ra.effective_source_team_id)
        )
      WHERE ra.status = 'active'
        AND ra.limits_json IS NOT NULL
        AND ra.limits_json::jsonb #>> '{hourly,enabled}' = 'true'
        AND ra.limits_json::jsonb #>> '{hourly,hours}' ~ '^[0-9]+$'
        AND (ra.limits_json::jsonb #>> '{hourly,hours}')::integer BETWEEN 1 AND 720
      ON CONFLICT(system_account_id, scope_type, scope_id) DO NOTHING
      RETURNING system_account_id, scope_type, scope_id, created_at, updated_at
    )
    INSERT INTO juhe_stats.usage_quota_hourly_window_dirty_scopes (
      system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at
    )
    SELECT system_account_id, scope_type, scope_id, 1, created_at, updated_at FROM inserted
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
      generation = usage_quota_hourly_window_dirty_scopes.generation + 1,
      updated_at = EXCLUDED.updated_at
  `

// pgSeedQuotaScopeBindingsFromTeamGrantsCTE backfills team quota scope bindings from authorization grants (parameter-free, idempotent).
const pgSeedQuotaScopeBindingsFromTeamGrantsCTE = `
    WITH candidates AS (
      SELECT DISTINCT
        CASE WHEN ra.resource_type = 'account' THEN ra.grantee_system_account_id ELSE ra.resource_owner_system_account_id END AS system_account_id,
        CASE WHEN ra.resource_type = 'account' THEN 'account_authorization_team' ELSE 'group_authorization_team' END AS scope_type,
        CASE WHEN ra.resource_type = 'account' THEN instance_accounts.id || ':' || ra.effective_source_team_id ELSE ra.resource_id || ':' || ra.effective_source_team_id END AS scope_id,
        grants.id AS source_id,
        (ra.limits_json::jsonb #>> '{hourly,hours}')::integer AS window_hours,
        ra.created_at,
        ra.updated_at
      FROM "juhe_business"."resource_authorizations" ra
      INNER JOIN "juhe_business"."resource_authorization_grants" grants
        ON grants.resource_type = ra.resource_type
        AND grants.resource_id = ra.resource_id
        AND grants.grantee_type = 'team'
        AND grants.grantee_team_id = ra.effective_source_team_id
        AND grants.status = 'active'
      LEFT JOIN "juhe_business"."accounts" instance_accounts
        ON ra.resource_type = 'account'
        AND instance_accounts.authorization_instance_authorization_id = ra.id
        AND instance_accounts.system_account_id = ra.grantee_system_account_id
        AND instance_accounts.authorization_instance_source_account_id = ra.resource_id
        AND instance_accounts.deleted_at IS NULL
      WHERE ra.status = 'active'
        AND ra.effective_source_type = 'team'
        AND (ra.resource_type = 'group' OR instance_accounts.id IS NOT NULL)
        AND ra.limits_json IS NOT NULL
        AND ra.limits_json::jsonb #>> '{hourly,enabled}' = 'true'
        AND ra.limits_json::jsonb #>> '{hourly,hours}' ~ '^[0-9]+$'
        AND (ra.limits_json::jsonb #>> '{hourly,hours}')::integer BETWEEN 1 AND 720
    ), inserted AS (
      INSERT INTO "juhe_business"."request_quota_hourly_window_scope_bindings" (
        system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at
      )
      SELECT system_account_id, scope_type, scope_id, 'resource_authorization_grant', source_id,
        window_hours, created_at, updated_at
      FROM candidates
      WHERE true
      ON CONFLICT(system_account_id, scope_type, scope_id) DO NOTHING
      RETURNING system_account_id, scope_type, scope_id, created_at, updated_at
    )
    INSERT INTO juhe_stats.usage_quota_hourly_window_dirty_scopes (
      system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at
    )
    SELECT system_account_id, scope_type, scope_id, 1, created_at, updated_at FROM inserted
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
      generation = usage_quota_hourly_window_dirty_scopes.generation + 1,
      updated_at = EXCLUDED.updated_at
  `

// pgSeedProviderInsert seeds one providers row.
const pgSeedProviderInsert = `
        INSERT INTO "juhe_business"."providers" (
          id, code, name, description, parent_code, enabled, default_supported_models_json, created_at, updated_at
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
        ON CONFLICT DO NOTHING
      `

// pgSeedProviderDefaultModelsRepair refreshes empty provider default model lists.
const pgSeedProviderDefaultModelsRepair = `
        UPDATE "juhe_business"."providers"
        SET default_supported_models_json = $1, updated_at = $2
        WHERE code = $3
          AND (default_supported_models_json IS NULL OR btrim(default_supported_models_json) = '' OR default_supported_models_json = '[]')
      `

// pgSeedProtocolInsert seeds one protocols row.
const pgSeedProtocolInsert = `
        INSERT INTO "juhe_business"."protocols" (
          id, code, version, name, description, enabled, created_at, updated_at
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
        ON CONFLICT DO NOTHING
      `

// pgSeedEndpointFamilyInsert seeds one protocol_endpoint_families row.
const pgSeedEndpointFamilyInsert = `
        INSERT INTO "juhe_business"."protocol_endpoint_families" (
          id, protocol_code, protocol_version, family_code, name, description, enabled, created_at, updated_at
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
        ON CONFLICT DO NOTHING
      `

// pgSeedProfileInsert seeds one provider_protocol_profiles row.
const pgSeedProfileInsert = `
        INSERT INTO "juhe_business"."provider_protocol_profiles" (
          id, provider_code, name, description, enabled, protocol_code, protocol_version,
          base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
        ON CONFLICT DO NOTHING
      `

// pgSeedProfileAccountTypesSelect reads one profile account_types_json for the repair step.
const pgSeedProfileAccountTypesSelect = `
        SELECT account_types_json
        FROM "juhe_business"."provider_protocol_profiles"
        WHERE id = $1
      `

// pgSeedProfileAccountTypesUpdate merges missing built-in account types into one profile (from repairBuiltInProviderProfileAccountTypes).
const pgSeedProfileAccountTypesUpdate = `
        UPDATE "juhe_business"."provider_protocol_profiles"
        SET account_types_json = $1, updated_at = $2
        WHERE id = $3
      `

// pgSeedProfileFamilyInsert seeds one provider_protocol_profile_families row.
const pgSeedProfileFamilyInsert = `
          INSERT INTO "juhe_business"."provider_protocol_profile_families" (
            profile_id, family_code, enabled, capabilities_json, created_at, updated_at
          ) VALUES ($1, $2, 1, '[]', $3, $4)
          ON CONFLICT DO NOTHING
        `

// pgSeedGroupDefaultRepair marks the built-in group as default when no default exists.
const pgSeedGroupDefaultRepair = `
        UPDATE "juhe_business"."groups" AS candidate
        SET is_default = 1
        WHERE candidate.provider_code = $1
          AND candidate.is_default = 0
          AND candidate.system_account_id = $2
          AND candidate.id = $3
          AND NOT EXISTS (
            SELECT 1
            FROM "juhe_business"."groups" AS existing_default
            WHERE existing_default.system_account_id = candidate.system_account_id
              AND existing_default.provider_code = candidate.provider_code
              AND existing_default.is_default = 1
          )
      `

// pgSeedGroupInsert inserts the built-in default group for every system account that lacks it.
const pgSeedGroupInsert = `
        INSERT INTO "juhe_business"."groups" (
          id, system_account_id, name, provider_code,
          description, enabled, is_default, created_at, updated_at
        )
        SELECT
          CASE WHEN system_accounts.id = $1 THEN $2 ELSE $3 || system_accounts.id END,
          system_accounts.id,
          CASE
            WHEN EXISTS (
              SELECT 1
              FROM "juhe_business"."groups" AS same_name
              WHERE same_name.system_account_id = system_accounts.id
                AND same_name.provider_code = $4
                AND lower(same_name.name) = lower($5)
          ) THEN $6 || '（系统默认：' || system_accounts.id || CASE
            WHEN candidate_suffix.suffix = 0 THEN ''
            ELSE ' #' || candidate_suffix.suffix
          END || '）'
          ELSE $5
        END,
          $4, $7, 1, 1, $8, $9
        FROM "juhe_business"."system_accounts" AS system_accounts
        LEFT JOIN LATERAL (
          SELECT candidate_suffix.suffix
          FROM generate_series(
            0,
            (
              SELECT COUNT(*)
              FROM "juhe_business"."groups" AS fallback_name
              WHERE fallback_name.system_account_id = system_accounts.id
                AND fallback_name.provider_code = $4
                AND lower(fallback_name.name) LIKE lower($6) || '（系统默认：%）'
            )
          ) AS candidate_suffix(suffix)
          WHERE NOT EXISTS (
            SELECT 1
            FROM "juhe_business"."groups" AS existing_fallback_name
            WHERE existing_fallback_name.system_account_id = system_accounts.id
              AND existing_fallback_name.provider_code = $4
              AND lower(existing_fallback_name.name) = lower(
                $6 || '（系统默认：' || system_accounts.id || CASE
                  WHEN candidate_suffix.suffix = 0 THEN ''
                  ELSE ' #' || candidate_suffix.suffix
                END || '）'
              )
          )
          ORDER BY candidate_suffix.suffix
          LIMIT 1
        ) AS candidate_suffix ON true
        WHERE NOT EXISTS (
          SELECT 1
          FROM "juhe_business"."groups" AS existing_default
          WHERE existing_default.system_account_id = system_accounts.id
            AND existing_default.provider_code = $4
            AND existing_default.is_default = 1
        )
        ON CONFLICT DO NOTHING
      `

// pgSeedExternalIntegrationSourceInsert seeds the built-in external integration test source row (the secret token part is intentionally not ported).
const pgSeedExternalIntegrationSourceInsert = `
      INSERT INTO "juhe_business"."external_integration_sources" (
        id, name, status, scopes_json, rate_limits_json, expires_at, notes, created_at, updated_at
      ) VALUES ($1, $2, 'active', $3, $4, NULL, $5, $6, $7)
      ON CONFLICT DO NOTHING
    `

// pgSeedExternalIntegrationSourceUpdate repairs the built-in external integration test source row.
const pgSeedExternalIntegrationSourceUpdate = `
      UPDATE "juhe_business"."external_integration_sources"
      SET name = $1,
          scopes_json = $2,
          rate_limits_json = $3,
          expires_at = NULL,
          notes = $4,
          updated_at = $5
      WHERE id = $6
    `

// pgSeedSystemSettingInsert seeds one system_settings row for sys_admin.
const pgSeedSystemSettingInsert = `
        INSERT INTO "juhe_business"."system_settings" (system_account_id, key, value_json, updated_at)
        VALUES ($1, $2, $3, $4)
        ON CONFLICT DO NOTHING
      `

// pgSeedPasswordIterations mirrors passwordIterations in Node src/storage/crypto.ts.
const pgSeedPasswordIterations = 120000

// pgSeedGlobalSettings mirrors DEFAULT_GLOBAL_SETTINGS (value_json already JSON-encoded).
var pgSeedGlobalSettings = []pgSeedKeyValue{
	{Key: "appName", ValueJSON: "\"聚合 AI\""},
	{Key: "appIcon", ValueJSON: "\"/__aisys__/brand-icon.svg\""},
}

var pgSeedRequestQuotaHourlyWindowHours = []int{1, 3, 6, 12, 24, 72, 168, 720}

// pgSeedProvider mirrors DEFAULT_PROVIDER_SEEDS.
type pgSeedProvider struct {
	ID                         string
	Code                       string
	Name                       string
	Description                string
	ParentCode                 string
	Enabled                    int
	DefaultSupportedModelsJSON string
}

var pgSeedProviders = []pgSeedProvider{
	{
		ID:                         "openai",
		Code:                       "openai",
		Name:                       "OpenAI 兼容",
		Description:                "通用 OpenAI-compatible 供应商，用于接入兼容 OpenAI v1 协议的上游服务，默认只提供 API Key 透传能力",
		ParentCode:                 "",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"gpt-5.6-sol\",\"gpt-5.6-terra\",\"gpt-5.6-luna\",\"gpt-5.5\",\"gpt-5.4\",\"gpt-5.4-mini\",\"gpt-image-2\"]",
	},
	{
		ID:                         "gpt",
		Code:                       "gpt",
		Name:                       "GPT",
		Description:                "GPT 官方供应商，继承通用 OpenAI-compatible 能力，并启用 OAuth、Codex Responses 等 GPT 专属能力",
		ParentCode:                 "openai",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"gpt-5.6-sol\",\"gpt-5.6-terra\",\"gpt-5.6-luna\",\"gpt-5.5\",\"gpt-5.4\",\"gpt-5.4-mini\",\"gpt-image-2\"]",
	},
	{
		ID:                         "xai",
		Code:                       "xai",
		Name:                       "xAI / Grok",
		Description:                "xAI 官方供应商，支持 API Key 与 Grok OAuth 接入 OpenAI v1 文本协议",
		ParentCode:                 "openai",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"grok-4.6\",\"grok-4.5\"]",
	},
	{
		ID:                         "deepseek",
		Code:                       "deepseek",
		Name:                       "DeepSeek",
		Description:                "DeepSeek 官方供应商，支持 OpenAI-compatible v1 Chat Completions 与 Responses 直连，也支持 Anthropic v1 Messages 档案兼容 Claude Code",
		ParentCode:                 "",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"deepseek-v4-flash\",\"deepseek-v4-pro\"]",
	},
	{
		ID:                         "anthropic",
		Code:                       "anthropic",
		Name:                       "Anthropic",
		Description:                "Anthropic 官方供应商，支持 API Key 或 OAuth Access Token（Bearer）接入 Anthropic Messages 原生协议",
		ParentCode:                 "",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"claude-opus-5\",\"claude-sonnet-5\",\"claude-haiku-4-5\"]",
	},
	{
		ID:                         "gemini",
		Code:                       "gemini",
		Name:                       "Gemini",
		Description:                "Google Gemini 官方供应商，支持 API Key 或 Google OAuth 接入 Gemini v1beta Generate Content 与 Interactions；OpenAI 客户端可使用兼容档案",
		ParentCode:                 "",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"gemini-3.7-flash\",\"gemini-3.5-flash\",\"gemini-3.1-pro-preview\",\"gemini-2.5-pro\",\"gemini-2.5-flash\"]",
	},
	{
		ID:                         "glm",
		Code:                       "glm",
		Name:                       "智谱 GLM",
		Description:                "智谱 GLM 官方供应商，支持通用 GLM API Key、GLM Coding Plan OpenAI Chat 档案，以及 GLM Coding Anthropic v1 Messages 档案",
		ParentCode:                 "",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"glm-5.3\",\"glm-5.2\",\"glm-5.1\",\"glm-5\",\"glm-5-turbo\",\"glm-4.7-flashx\",\"glm-4.7-flash\"]",
	},
	{
		ID:                         "hybrid",
		Code:                       "hybrid",
		Name:                       "混合供应商",
		Description:                "混合供应商账户用于创建真实上游账户，并在账户内配置允许的下游协议入口和上游模型映射；不指向其他账户、分组或 API Key",
		ParentCode:                 "",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"gpt-5.6-sol\",\"claude-opus-5\",\"gemini-3.7-flash\",\"deepseek-v4-flash\",\"glm-5.3\"]",
	},
}

// pgSeedProtocol mirrors DEFAULT_PROTOCOL_SEEDS.
type pgSeedProtocol struct {
	ID, Code, Version, Name, Description string
	Enabled                              int
}

var pgSeedProtocols = []pgSeedProtocol{
	{ID: "openai_v1", Code: "openai", Version: "v1", Name: "OpenAI v1", Description: "OpenAI-compatible v1 协议；接口族包含 Chat Completions 与 Responses", Enabled: 1},
	{ID: "anthropic_v1", Code: "anthropic", Version: "v1", Name: "Anthropic v1", Description: "Anthropic 官方 v1 协议；接口族包含 Messages、Models 与 Message Token Counting", Enabled: 1},
	{ID: "gemini_v1beta", Code: "gemini", Version: "v1beta", Name: "Gemini v1beta", Description: "Google Gemini v1beta 原生协议；接口族包含 Models、generateContent、streamGenerateContent、countTokens 与 embedContent", Enabled: 1},
}

// pgSeedEndpointFamily mirrors DEFAULT_PROTOCOL_ENDPOINT_FAMILY_SEEDS.
type pgSeedEndpointFamily struct {
	ID, ProtocolCode, ProtocolVersion, Code, Name, Description string
	Enabled                                                    int
}

var pgSeedEndpointFamilies = []pgSeedEndpointFamily{
	{ID: "openai_v1_chat_completions", ProtocolCode: "openai", ProtocolVersion: "v1", Code: "chat_completions", Name: "Chat Completions", Description: "OpenAI v1 /chat/completions 接口族", Enabled: 1},
	{ID: "openai_v1_responses", ProtocolCode: "openai", ProtocolVersion: "v1", Code: "responses", Name: "Responses", Description: "OpenAI v1 /responses 接口族", Enabled: 1},
	{ID: "anthropic_v1_messages", ProtocolCode: "anthropic", ProtocolVersion: "v1", Code: "messages", Name: "Messages", Description: "Anthropic v1 /messages 接口族", Enabled: 1},
	{ID: "anthropic_v1_models", ProtocolCode: "anthropic", ProtocolVersion: "v1", Code: "models", Name: "Models", Description: "Anthropic v1 /models 接口族", Enabled: 1},
	{ID: "anthropic_v1_message_token_counting", ProtocolCode: "anthropic", ProtocolVersion: "v1", Code: "message_token_counting", Name: "Message Token Counting", Description: "Anthropic v1 /messages/count_tokens 接口族", Enabled: 1},
	{ID: "gemini_v1beta_models", ProtocolCode: "gemini", ProtocolVersion: "v1beta", Code: "models", Name: "Models", Description: "Gemini v1beta /models 接口族", Enabled: 1},
	{ID: "gemini_v1beta_generate_content", ProtocolCode: "gemini", ProtocolVersion: "v1beta", Code: "generate_content", Name: "generateContent", Description: "Gemini v1beta :generateContent 接口族", Enabled: 1},
	{ID: "gemini_v1beta_stream_generate_content", ProtocolCode: "gemini", ProtocolVersion: "v1beta", Code: "stream_generate_content", Name: "streamGenerateContent", Description: "Gemini v1beta :streamGenerateContent SSE 接口族", Enabled: 1},
	{ID: "gemini_v1beta_count_tokens", ProtocolCode: "gemini", ProtocolVersion: "v1beta", Code: "count_tokens", Name: "countTokens", Description: "Gemini v1beta :countTokens 接口族", Enabled: 1},
	{ID: "gemini_v1beta_embed_content", ProtocolCode: "gemini", ProtocolVersion: "v1beta", Code: "embed_content", Name: "embedContent", Description: "Gemini v1beta :embedContent 接口族", Enabled: 1},
}

// pgSeedProfile mirrors DEFAULT_PROVIDER_PROTOCOL_PROFILE_SEEDS. AccountTypes
// and Capabilities are kept as slices; the seed parameters are JSON-encoded
// from them exactly like the Node JSON.stringify calls.
type pgSeedProfile struct {
	ID                      string
	ProviderCode            string
	Name                    string
	Description             string
	Enabled                 int
	ProtocolCode            string
	ProtocolVersion         string
	BaseURL                 string
	DefaultHealthCheckModel string
	AccountTypes            []string
	Capabilities            []string
	EndpointFamilies        []string
}

var pgSeedProfiles = []pgSeedProfile{
	{
		ID:                      "profile_openai_openai_v1",
		ProviderCode:            "openai",
		Name:                    "OpenAI 兼容 / OpenAI v1",
		Description:             "通用 OpenAI-compatible 供应商的 OpenAI v1 协议档案，仅承载 API Key 透传、模型目录和通用协议策略",
		Enabled:                 1,
		ProtocolCode:            "openai",
		ProtocolVersion:         "v1",
		BaseURL:                 "https://api.openai.com/v1",
		DefaultHealthCheckModel: "gpt-5.6-sol",
		AccountTypes:            []string{"api_key"},
		Capabilities:            []string{"responses", "chat", "passthrough"},
		EndpointFamilies:        []string{"chat_completions", "responses"},
	},
	{
		ID:                      "profile_gpt_openai_v1",
		ProviderCode:            "gpt",
		Name:                    "GPT / OpenAI v1",
		Description:             "GPT 供应商的 OpenAI v1 协议档案，支持 OAuth 与 API Key 两种账户接入方式",
		Enabled:                 1,
		ProtocolCode:            "openai",
		ProtocolVersion:         "v1",
		BaseURL:                 "https://api.openai.com/v1",
		DefaultHealthCheckModel: "gpt-5.6-sol",
		AccountTypes:            []string{"oauth", "api_key"},
		Capabilities:            []string{"responses", "chat"},
		EndpointFamilies:        []string{"chat_completions", "responses"},
	},
	{
		ID:                      "profile_xai_openai_v1",
		ProviderCode:            "xai",
		Name:                    "xAI / OpenAI v1",
		Description:             "xAI 官方协议档案，支持 API Key 与 Grok OAuth，承载 OpenAI v1 Chat Completions 与 Responses 文本接口",
		Enabled:                 1,
		ProtocolCode:            "openai",
		ProtocolVersion:         "v1",
		BaseURL:                 "https://api.x.ai/v1",
		DefaultHealthCheckModel: "grok-4.6",
		AccountTypes:            []string{"api_key", "oauth"},
		Capabilities:            []string{"responses", "chat", "passthrough"},
		EndpointFamilies:        []string{"chat_completions", "responses"},
	},
	{
		ID:                      "profile_deepseek_anthropic_v1",
		ProviderCode:            "deepseek",
		Name:                    "DeepSeek / Anthropic v1",
		Description:             "DeepSeek 供应商的 Anthropic v1 Messages 协议档案，承载 Claude Code 使用的 /v1/messages 与 /v1/models 直连",
		Enabled:                 1,
		ProtocolCode:            "anthropic",
		ProtocolVersion:         "v1",
		BaseURL:                 "https://api.deepseek.com/anthropic",
		DefaultHealthCheckModel: "deepseek-v4-flash",
		AccountTypes:            []string{"api_key"},
		Capabilities:            []string{"messages", "models", "passthrough"},
		EndpointFamilies:        []string{"messages", "models"},
	},
	{
		ID:                      "profile_deepseek_openai_v1",
		ProviderCode:            "deepseek",
		Name:                    "DeepSeek / OpenAI v1",
		Description:             "DeepSeek 供应商的 OpenAI-compatible v1 协议档案，承载 API Key、Chat Completions、原生 Responses、DeepSeek 响应扩展字段与 Codex Responses 桥接",
		Enabled:                 1,
		ProtocolCode:            "openai",
		ProtocolVersion:         "v1",
		BaseURL:                 "https://api.deepseek.com",
		DefaultHealthCheckModel: "deepseek-v4-flash",
		AccountTypes:            []string{"api_key"},
		Capabilities:            []string{"chat", "responses", "passthrough"},
		EndpointFamilies:        []string{"chat_completions", "responses"},
	},
	{
		ID:                      "profile_anthropic_anthropic_v1",
		ProviderCode:            "anthropic",
		Name:                    "Anthropic / Anthropic v1",
		Description:             "Anthropic 官方协议档案，支持 API Key 或 OAuth Access Token，承载 anthropic-version 与 Messages 原生协议",
		Enabled:                 1,
		ProtocolCode:            "anthropic",
		ProtocolVersion:         "v1",
		BaseURL:                 "https://api.anthropic.com/v1",
		DefaultHealthCheckModel: "claude-opus-5",
		AccountTypes:            []string{"api_key", "oauth"},
		Capabilities:            []string{"messages", "models", "count_tokens", "passthrough"},
		EndpointFamilies:        []string{"messages", "models", "message_token_counting"},
	},
	{
		ID:                      "profile_gemini_openai_chat_v1beta",
		ProviderCode:            "gemini",
		Name:                    "Gemini / OpenAI Chat",
		Description:             "Gemini 官方 OpenAI Chat Completions 兼容档案，仅用于 OpenAI Chat 直连和 Codex Responses 显式模型映射，不承载 Gemini 原生协议",
		Enabled:                 1,
		ProtocolCode:            "openai",
		ProtocolVersion:         "v1",
		BaseURL:                 "https://generativelanguage.googleapis.com/v1beta/openai",
		DefaultHealthCheckModel: "gemini-3.7-flash",
		AccountTypes:            []string{"api_key"},
		Capabilities:            []string{"chat", "passthrough"},
		EndpointFamilies:        []string{"chat_completions"},
	},
	{
		ID:                      "profile_gemini_native_v1beta",
		ProviderCode:            "gemini",
		Name:                    "Gemini / Gemini v1beta",
		Description:             "Gemini 官方 API Key 协议档案，承载 x-goog-api-key 与 Gemini v1beta 原生协议直连",
		Enabled:                 1,
		ProtocolCode:            "gemini",
		ProtocolVersion:         "v1beta",
		BaseURL:                 "https://generativelanguage.googleapis.com",
		DefaultHealthCheckModel: "gemini-3.7-flash",
		AccountTypes:            []string{"api_key", "google_oauth"},
		Capabilities:            []string{"generate_content", "stream_generate_content", "count_tokens", "embed_content", "interactions", "models", "passthrough"},
		EndpointFamilies:        []string{"models", "generate_content", "stream_generate_content", "count_tokens", "embed_content", "interactions"},
	},
	{
		ID:                      "profile_glm_coding_openai_v1",
		ProviderCode:            "glm",
		Name:                    "智谱 GLM Coding / OpenAI Chat",
		Description:             "智谱 GLM Coding Plan Key 协议档案，使用 Coding Plan OpenAI Chat Completions 兼容端点",
		Enabled:                 1,
		ProtocolCode:            "openai",
		ProtocolVersion:         "v1",
		BaseURL:                 "https://open.bigmodel.cn/api/coding/paas/v4",
		DefaultHealthCheckModel: "glm-5.3",
		AccountTypes:            []string{"api_key"},
		Capabilities:            []string{"chat", "passthrough"},
		EndpointFamilies:        []string{"chat_completions"},
	},
	{
		ID:                      "profile_glm_coding_anthropic_v1",
		ProviderCode:            "glm",
		Name:                    "智谱 GLM Coding / Anthropic v1",
		Description:             "智谱 GLM Coding Plan Key 的 Anthropic v1 Messages 协议档案，面向 Anthropic Messages 客户端直连",
		Enabled:                 1,
		ProtocolCode:            "anthropic",
		ProtocolVersion:         "v1",
		BaseURL:                 "https://open.bigmodel.cn/api/anthropic",
		DefaultHealthCheckModel: "glm-5.3",
		AccountTypes:            []string{"api_key"},
		Capabilities:            []string{"messages", "models", "passthrough"},
		EndpointFamilies:        []string{"messages", "models"},
	},
	{
		ID:                      "profile_glm_general_openai_v1",
		ProviderCode:            "glm",
		Name:                    "智谱 GLM 通用 / OpenAI Chat",
		Description:             "智谱通用 GLM API Key 协议档案，使用智谱 OpenAI Chat Completions 兼容端点",
		Enabled:                 1,
		ProtocolCode:            "openai",
		ProtocolVersion:         "v1",
		BaseURL:                 "https://open.bigmodel.cn/api/paas/v4/",
		DefaultHealthCheckModel: "glm-5.3",
		AccountTypes:            []string{"api_key"},
		Capabilities:            []string{"chat", "passthrough"},
		EndpointFamilies:        []string{"chat_completions"},
	},
	{
		ID:                      "profile_hybrid_openai_chat_v1",
		ProviderCode:            "hybrid",
		Name:                    "混合供应商",
		Description:             "混合供应商通用 API Key 档案；真实上游 Base URL 和目标协议由账户模型映射显式声明",
		Enabled:                 1,
		ProtocolCode:            "openai",
		ProtocolVersion:         "v1",
		BaseURL:                 "",
		DefaultHealthCheckModel: "",
		AccountTypes:            []string{"api_key"},
		Capabilities:            []string{"chat", "responses", "messages", "generate_content", "stream_generate_content", "bridge"},
		EndpointFamilies:        []string{"chat_completions", "responses", "messages", "generate_content", "stream_generate_content"},
	},
	{
		ID:                      "profile_hybrid_anthropic_messages_v1",
		ProviderCode:            "hybrid",
		Name:                    "混合供应商 Anthropic Messages",
		Description:             "混合供应商 Anthropic Messages API Key 档案；下游协议由账户模型映射显式声明",
		Enabled:                 1,
		ProtocolCode:            "anthropic",
		ProtocolVersion:         "v1",
		BaseURL:                 "",
		DefaultHealthCheckModel: "",
		AccountTypes:            []string{"api_key"},
		Capabilities:            []string{"messages", "bridge"},
		EndpointFamilies:        []string{"messages"},
	},
}

// pgSeedGroup mirrors DEFAULT_BUILT_IN_GROUPS.
type pgSeedGroup struct {
	ID, SystemAccountID, Name, ProviderCode, Description string
}

var pgSeedGroups = []pgSeedGroup{
	{ID: "grp_default_openai_sys_admin", SystemAccountID: "sys_admin", Name: "默认 OpenAI 兼容分组", ProviderCode: "openai", Description: ""},
	{ID: "grp_default_gpt_sys_admin", SystemAccountID: "sys_admin", Name: "默认 GPT 分组", ProviderCode: "gpt", Description: ""},
	{ID: "grp_default_xai_sys_admin", SystemAccountID: "sys_admin", Name: "默认 xAI 分组", ProviderCode: "xai", Description: ""},
	{ID: "grp_default_deepseek_sys_admin", SystemAccountID: "sys_admin", Name: "默认 DeepSeek 分组", ProviderCode: "deepseek", Description: ""},
	{ID: "grp_default_anthropic_sys_admin", SystemAccountID: "sys_admin", Name: "默认 Anthropic 分组", ProviderCode: "anthropic", Description: ""},
	{ID: "grp_default_gemini_sys_admin", SystemAccountID: "sys_admin", Name: "默认 Gemini 分组", ProviderCode: "gemini", Description: ""},
	{ID: "grp_default_glm_sys_admin", SystemAccountID: "sys_admin", Name: "默认 GLM 分组", ProviderCode: "glm", Description: ""},
	{ID: "grp_default_hybrid_openai_chat_sys_admin", SystemAccountID: "sys_admin", Name: "默认混合供应商分组", ProviderCode: "hybrid", Description: "混合供应商账户保存真实上游凭据和 Base URL，允许账户内配置跨协议入口映射"},
}

// pgSeedExternalIntegrationSource mirrors the built-in external integration
// test source constants recorded from the Node seed run.
var pgSeedExternalIntegrationSource = struct {
	ID, Name, ScopesJSON, RateLimitsJSON, Notes string
}{
	ID:             "extsrc_builtin_test",
	Name:           "内置测试来源",
	ScopesJSON:     "[\"juhe_ai_public:account_add:write\",\"juhe_ai_public:account_delete:write\",\"juhe_ai_public:account_list:read\",\"juhe_ai_public:account_update:write\",\"juhe_ai_public:api_key_add:write\",\"juhe_ai_public:api_key_delete:write\",\"juhe_ai_public:api_key_list:read\",\"juhe_ai_public:api_key_update:write\",\"juhe_ai_public:group_add:write\",\"juhe_ai_public:group_delete:write\",\"juhe_ai_public:group_list:read\",\"juhe_ai_public:group_update:write\",\"juhe_ai_public:route_strategy_add:write\",\"juhe_ai_public:route_strategy_delete:write\",\"juhe_ai_public:route_strategy_list:read\",\"juhe_ai_public:route_strategy_update:write\"]",
	RateLimitsJSON: "[{\"windowSeconds\":60,\"maxRequests\":10}]",
	Notes:          "系统内置测试 Token，只返回 mock 数据；可停用或重置，不支持编辑或删除。",
}

// pgSeedSystemSettings mirrors DEFAULT_SYSTEM_SETTINGS (value_json already JSON-encoded).
var pgSeedSystemSettings = []pgSeedKeyValue{
	{Key: "gatewayTextRawBodyLimitMegabytes", ValueJSON: "16"},
	{Key: "accountCircuitConfirmationFailuresRequired", ValueJSON: "2"},
	{Key: "gatewayUserRequestLimitPerMinute", ValueJSON: "0"},
	{Key: "gatewayUserRequestLimitPerDay", ValueJSON: "0"},
	{Key: "gatewayUserRequestLimitPerWeek", ValueJSON: "0"},
	{Key: "gatewayUserRequestLimitPerMonth", ValueJSON: "0"},
	{Key: "userAiAccountLimit", ValueJSON: "100"},
	{Key: "systemApiRateLimitIpReadPerMinute", ValueJSON: "600"},
	{Key: "systemApiRateLimitIpReadBurstPer10Seconds", ValueJSON: "120"},
	{Key: "systemApiRateLimitIpWritePerMinute", ValueJSON: "180"},
	{Key: "systemApiRateLimitIpWriteBurstPer10Seconds", ValueJSON: "40"},
	{Key: "systemApiRateLimitUserReadPerMinute", ValueJSON: "300"},
	{Key: "systemApiRateLimitUserWritePerMinute", ValueJSON: "120"},
	{Key: "defaultTemporaryUnschedulableMinutes", ValueJSON: "2"},
	{Key: "temporaryUnschedulableRetryIntervalSeconds", ValueJSON: "3"},
	{Key: "temporaryUnschedulableRetryAttempts", ValueJSON: "2"},
	{Key: "textFirstResponseTimeoutSeconds", ValueJSON: "120"},
	{Key: "textStreamIdleTimeoutSeconds", ValueJSON: "30"},
	{Key: "textUncommittedAttemptMaxLifetimeSeconds", ValueJSON: "1800"},
	{Key: "imageFirstResponseTimeoutSeconds", ValueJSON: "600"},
	{Key: "imageStreamIdleTimeoutSeconds", ValueJSON: "120"},
	{Key: "imageUncommittedAttemptMaxLifetimeSeconds", ValueJSON: "3600"},
	{Key: "imageRequestWallTimeoutSeconds", ValueJSON: "3600"},
	{Key: "chatImageGenerationTotalTimeoutSeconds", ValueJSON: "900"},
	{Key: "noAvailableAccountWaitTimeoutSeconds", ValueJSON: "270"},
	{Key: "streamFailureThresholdCount", ValueJSON: "3"},
	{Key: "streamFailureThresholdWindowMinutes", ValueJSON: "5"},
	{Key: "operationLogRetentionDays", ValueJSON: "365"},
	{Key: "operationLogMaxChangesPerRecord", ValueJSON: "100"},
	{Key: "statsAggregationIntervalSeconds", ValueJSON: "60"},
	{Key: "statsAggregationBatchSize", ValueJSON: "2000"},
	{Key: "statsAggregationMaxBatchesPerRun", ValueJSON: "5"},
	{Key: "usageHotWindowRefreshIntervalSeconds", ValueJSON: "600"},
	{Key: "groupAccountStatsRefreshIntervalSeconds", ValueJSON: "60"},
	{Key: "systemMetricsSampleIntervalSeconds", ValueJSON: "30"},
	{Key: "tableMonitorMaxTablesPerRun", ValueJSON: "4"},
	{Key: "accountQualityRefreshIntervalSeconds", ValueJSON: "600"},
	{Key: "accountQualityWindowMinutes", ValueJSON: "10"},
	{Key: "accountHealthCheckIntervalHours", ValueJSON: "1"},
	{Key: "accountHealthCheckJitterMinutes", ValueJSON: "10"},
	{Key: "accountHealthCheckFailureThreshold", ValueJSON: "3"},
	{Key: "cooldownAccountRetestIntervalSeconds", ValueJSON: "3"},
	{Key: "cooldownAccountRetestMaxBackoffHours", ValueJSON: "12"},
	{Key: "oauthAccessTokenRefreshIntervalSeconds", ValueJSON: "60"},
	{Key: "oauthAccessTokenRefreshLeadSeconds", ValueJSON: "300"},
	{Key: "oauthAccessTokenRefreshBatchSize", ValueJSON: "20"},
	{Key: "oauthAccessTokenRefreshRetryBackoffSeconds", ValueJSON: "300"},
	{Key: "modelCheckRetentionDays", ValueJSON: "30"},
	{Key: "runtimeLogIndexRetentionDays", ValueJSON: "14"},
	{Key: "publicApiLogRetentionDays", ValueJSON: "30"},
	{Key: "usageRecordRetentionDays", ValueJSON: "30"},
	{Key: "usageStatsTimezone", ValueJSON: "\"Asia/Shanghai\""},
	{Key: "usageStatsMinuteRetentionHours", ValueJSON: "48"},
	{Key: "usageStatsHourlyRetentionDays", ValueJSON: "60"},
	{Key: "usageStatsDailyRetentionDays", ValueJSON: "400"},
	{Key: "usageStatsWeeklyRetentionWeeks", ValueJSON: "104"},
	{Key: "usageStatsMonthlyRetentionMonths", ValueJSON: "24"},
	{Key: "usageRankSnapshotRetentionDays", ValueJSON: "30"},
	{Key: "systemMetricsRetentionDays", ValueJSON: "7"},
	{Key: "systemMetricsHourlyRetentionDays", ValueJSON: "30"},
}

// pgSeedKeyValue is one key -> JSON-encoded value pair.
type pgSeedKeyValue struct {
	Key       string
	ValueJSON string
}

// EnsurePostgresSeeds applies the portable subset of seedPostgresDefaults in
// Node statement order: default admin account, global settings, quota window
// configs, quota scope binding backfills, providers, protocols, endpoint
// families, provider protocol profiles (with account-type repair), profile
// families, built-in groups, the external integration source row and system
// settings. Every INSERT uses ON CONFLICT DO NOTHING and every UPDATE is a
// guarded repair, so repeated calls are idempotent. See the file header for
// the seed statements that are intentionally not ported yet.
func EnsurePostgresSeeds(ctx context.Context, db *sql.DB) (PGSeedResult, error) {
	var result PGSeedResult
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	exec := func(query string, args ...any) error {
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("postgres seed statement %d: %w", result.StatementCount+1, err)
		}
		result.StatementCount++
		return nil
	}

	adminPasswordHash, err := hashSeedPassword("admin")
	if err != nil {
		return PGSeedResult{}, fmt.Errorf("hash seed admin password: %w", err)
	}
	if err := exec(pgSeedSystemAccountsInsert, "sys_admin", "admin", "超级管理员", "系统默认超级管理员账户", "super_admin", "active", adminPasswordHash, 0, 0, now, now); err != nil {
		return PGSeedResult{}, err
	}
	for _, setting := range pgSeedGlobalSettings {
		if err := exec(pgSeedGlobalSettingsInsert, setting.Key, setting.ValueJSON, now); err != nil {
			return PGSeedResult{}, err
		}
	}
	for _, hours := range pgSeedRequestQuotaHourlyWindowHours {
		if err := exec(pgSeedWindowConfigInsert, hours, now, now); err != nil {
			return PGSeedResult{}, err
		}
	}
	for _, cte := range []string{pgSeedQuotaScopeBindingsFromAPIKeysCTE, pgSeedQuotaScopeBindingsFromAuthorizationsCTE, pgSeedQuotaScopeBindingsFromTeamGrantsCTE} {
		if err := exec(cte); err != nil {
			return PGSeedResult{}, err
		}
	}
	for _, provider := range pgSeedProviders {
		if err := exec(pgSeedProviderInsert, provider.ID, provider.Code, provider.Name, provider.Description, pgNullableText(provider.ParentCode), provider.Enabled, provider.DefaultSupportedModelsJSON, now, now); err != nil {
			return PGSeedResult{}, err
		}
		if err := exec(pgSeedProviderDefaultModelsRepair, provider.DefaultSupportedModelsJSON, now, provider.Code); err != nil {
			return PGSeedResult{}, err
		}
	}
	for _, protocol := range pgSeedProtocols {
		if err := exec(pgSeedProtocolInsert, protocol.ID, protocol.Code, protocol.Version, protocol.Name, protocol.Description, protocol.Enabled, now, now); err != nil {
			return PGSeedResult{}, err
		}
	}
	for _, family := range pgSeedEndpointFamilies {
		if err := exec(pgSeedEndpointFamilyInsert, family.ID, family.ProtocolCode, family.ProtocolVersion, family.Code, family.Name, family.Description, family.Enabled, now, now); err != nil {
			return PGSeedResult{}, err
		}
	}
	nowTime, err := time.Parse("2006-01-02T15:04:05.000Z07:00", now)
	if err != nil {
		return PGSeedResult{}, fmt.Errorf("parse seed timestamp: %w", err)
	}
	for index, profile := range pgSeedProfiles {
		profileUpdatedAt := nowTime.Add(time.Duration(index) * time.Millisecond).UTC().Format("2006-01-02T15:04:05.000Z07:00")
		if err := exec(pgSeedProfileInsert, profile.ID, profile.ProviderCode, profile.Name, profile.Description, profile.Enabled, profile.ProtocolCode, profile.ProtocolVersion, profile.BaseURL, profile.DefaultHealthCheckModel, mustJSON(profile.AccountTypes), mustJSON(profile.Capabilities), now, profileUpdatedAt); err != nil {
			return PGSeedResult{}, err
		}
	}
	for _, profile := range pgSeedProfiles {
		var accountTypesJSON string
		err := db.QueryRowContext(ctx, pgSeedProfileAccountTypesSelect, profile.ID).Scan(&accountTypesJSON)
		if err != nil {
			continue // missing row or non-text value mirrors the Node repair skip
		}
		current, err := parseSeedStringArray(accountTypesJSON)
		if err != nil {
			continue
		}
		merged := mergeSeedDistinct(current, profile.AccountTypes)
		if seedStringSlicesEqual(merged, current) {
			continue
		}
		if err := exec(pgSeedProfileAccountTypesUpdate, mustJSON(merged), now, profile.ID); err != nil {
			return PGSeedResult{}, err
		}
	}
	for _, profile := range pgSeedProfiles {
		for _, familyCode := range profile.EndpointFamilies {
			if err := exec(pgSeedProfileFamilyInsert, profile.ID, familyCode, now, now); err != nil {
				return PGSeedResult{}, err
			}
		}
	}
	for _, group := range pgSeedGroups {
		if err := exec(pgSeedGroupDefaultRepair, group.ProviderCode, group.SystemAccountID, group.ID); err != nil {
			return PGSeedResult{}, err
		}
		defaultGroupIDPrefix := "grp_default_" + group.ProviderCode + "_"
		if err := exec(pgSeedGroupInsert, group.SystemAccountID, group.ID, defaultGroupIDPrefix, group.ProviderCode, group.Name, group.Name, group.Description, now, now); err != nil {
			return PGSeedResult{}, err
		}
	}
	source := pgSeedExternalIntegrationSource
	if err := exec(pgSeedExternalIntegrationSourceInsert, source.ID, source.Name, source.ScopesJSON, source.RateLimitsJSON, source.Notes, now, now); err != nil {
		return PGSeedResult{}, err
	}
	if err := exec(pgSeedExternalIntegrationSourceUpdate, source.Name, source.ScopesJSON, source.RateLimitsJSON, source.Notes, now, source.ID); err != nil {
		return PGSeedResult{}, err
	}
	for _, setting := range pgSeedSystemSettings {
		if err := exec(pgSeedSystemSettingInsert, "sys_admin", setting.Key, setting.ValueJSON, now); err != nil {
			return PGSeedResult{}, err
		}
	}
	return result, nil
}

// hashSeedPassword mirrors hashPassword in Node src/storage/crypto.ts
// (pbkdf2-sha512, 120000 iterations, base64url salt and digest).
func hashSeedPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	derived, err := pbkdf2.Key(sha512.New, password, salt, pgSeedPasswordIterations, 32)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		"pbkdf2",
		"sha512",
		strconv.Itoa(pgSeedPasswordIterations),
		base64.RawURLEncoding.EncodeToString(salt),
		base64.RawURLEncoding.EncodeToString(derived),
	}, "$"), nil
}

// pgNullableText maps the empty string to a SQL NULL like the Node seeds do
// for optional provider parent codes.
func pgNullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal seed json: %v", err))
	}
	return string(encoded)
}

// parseSeedStringArray decodes one JSON string array value.
func parseSeedStringArray(value string) ([]string, error) {
	var items []string
	if err := json.Unmarshal([]byte(value), &items); err != nil {
		return nil, err
	}
	return items, nil
}

// mergeSeedDistinct appends missing additions while preserving order.
func mergeSeedDistinct(current, additions []string) []string {
	seen := make(map[string]bool, len(current)+len(additions))
	merged := make([]string, 0, len(current)+len(additions))
	for _, item := range current {
		if !seen[item] {
			seen[item] = true
			merged = append(merged, item)
		}
	}
	for _, item := range additions {
		if !seen[item] {
			seen[item] = true
			merged = append(merged, item)
		}
	}
	return merged
}

func seedStringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
