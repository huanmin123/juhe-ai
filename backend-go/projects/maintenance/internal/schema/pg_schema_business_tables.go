// Code generated from the Node PostgreSQL storage sources. The statements
// in this file are a verbatim extract of one schema group from
// pg_schema.go's postgresSchemaStatements literal (see the header of
// pg_schema.go for the full provenance and execution model). The
// statements are data: do not hand-edit them and keep them byte-identical
// to the Node dump order.

package schema

// postgresSchemaBusinessTables holds the juhe_business DDL statements of
// the table-dependency phase in execution order: CREATE TABLE blocks,
// then ALTER TABLE / extension / guarded DO blocks, then CREATE OR
// REPLACE FUNCTION and trigger statements that tables depend on.
var postgresSchemaBusinessTables = []PGStatement{
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
  -- 问题-0192：本函数是全部触发器隐式 dirty 写入的公共汇聚点，与所有应用侧
  -- dirty 写事务（Go advisorylock.AccountListDirty = 7001001）共用 advisory 锁，
  -- 使触发器路径也纳入 dirty 写串行化，消除与批量写事务的锁序死锁（40P01）。
  -- pg_advisory_xact_lock 事务级可重入：已持锁事务经触发器再次取锁无阻塞。
  PERFORM pg_advisory_xact_lock(7001001);
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
		Source:     "model-check-question-bank-table",
		SQL: `CREATE TABLE IF NOT EXISTS model_check_question_bank (
      id text PRIMARY KEY,
      title text NOT NULL,
      title_norm text NOT NULL,
      question_text text NOT NULL,
      reference_answer text NOT NULL,
      key_points_json text,
      status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
      reject_reason text,
      created_by text NOT NULL,
      created_scope text NOT NULL,
      reviewed_by text,
      reviewed_at text,
      created_at text NOT NULL,
      updated_at text NOT NULL
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "model-quality-custom-question-ids-pg-column",
		SQL:        `ALTER TABLE model_quality_policies ADD COLUMN IF NOT EXISTS custom_question_ids text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "model-quality-custom-question-ids-pg-column",
		SQL:        `ALTER TABLE model_quality_schedules ADD COLUMN IF NOT EXISTS custom_question_ids text`,
	},
}
