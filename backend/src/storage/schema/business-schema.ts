import type { DatabaseSync } from 'node:sqlite'

export function applyBusinessSchema(database: DatabaseSync): void {
  database.exec(`
    PRAGMA foreign_keys = ON;
    PRAGMA journal_mode = WAL;

    CREATE TABLE IF NOT EXISTS system_accounts (
      id TEXT PRIMARY KEY,
      username TEXT NOT NULL UNIQUE,
      display_name TEXT NOT NULL,
      description TEXT,
      role TEXT NOT NULL DEFAULT 'user',
      status TEXT NOT NULL DEFAULT 'active',
      password_hash TEXT NOT NULL,
      must_change_password INTEGER NOT NULL DEFAULT 0,
      image_generation_enabled INTEGER NOT NULL DEFAULT 0,
      last_login_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS system_sessions (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      token_hash TEXT NOT NULL UNIQUE,
      expires_at TEXT NOT NULL,
      created_at TEXT NOT NULL,
      last_seen_at TEXT NOT NULL,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS global_settings (
      key TEXT PRIMARY KEY,
      value_json TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS request_quota_hourly_window_configs (
      window_hours INTEGER PRIMARY KEY,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      CHECK (window_hours BETWEEN 1 AND 720)
    );

    CREATE TABLE IF NOT EXISTS providers (
      id TEXT PRIMARY KEY,
      code TEXT NOT NULL UNIQUE,
      name TEXT NOT NULL,
      description TEXT,
      parent_code TEXT,
      enabled INTEGER NOT NULL DEFAULT 1,
      default_supported_models_json TEXT NOT NULL DEFAULT '[]',
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (parent_code) REFERENCES providers(code)
    );

    CREATE TABLE IF NOT EXISTS protocols (
      id TEXT PRIMARY KEY,
      code TEXT NOT NULL,
      version TEXT NOT NULL,
      name TEXT NOT NULL,
      description TEXT,
      enabled INTEGER NOT NULL DEFAULT 1,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      UNIQUE (code, version)
    );

    CREATE TABLE IF NOT EXISTS protocol_endpoint_families (
      id TEXT PRIMARY KEY,
      protocol_code TEXT NOT NULL,
      protocol_version TEXT NOT NULL,
      family_code TEXT NOT NULL,
      name TEXT NOT NULL,
      description TEXT,
      enabled INTEGER NOT NULL DEFAULT 1,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      UNIQUE (protocol_code, protocol_version, family_code),
      FOREIGN KEY (protocol_code, protocol_version) REFERENCES protocols(code, version)
    );

    CREATE TABLE IF NOT EXISTS provider_protocol_profiles (
      id TEXT PRIMARY KEY,
      provider_code TEXT NOT NULL,
      name TEXT NOT NULL,
      description TEXT,
      enabled INTEGER NOT NULL DEFAULT 1,
      protocol_code TEXT NOT NULL,
      protocol_version TEXT NOT NULL,
      base_url TEXT NOT NULL,
      default_health_check_model TEXT NOT NULL,
      account_types_json TEXT NOT NULL,
      capabilities_json TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (provider_code) REFERENCES providers(code),
      FOREIGN KEY (protocol_code, protocol_version) REFERENCES protocols(code, version)
    );

    CREATE TABLE IF NOT EXISTS provider_protocol_profile_families (
      profile_id TEXT NOT NULL,
      family_code TEXT NOT NULL,
      enabled INTEGER NOT NULL DEFAULT 1,
      default_health_check_model TEXT,
      capabilities_json TEXT NOT NULL DEFAULT '[]',
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (profile_id, family_code),
      FOREIGN KEY (profile_id) REFERENCES provider_protocol_profiles(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS provider_model_catalog (
      id TEXT PRIMARY KEY,
      provider_code TEXT NOT NULL,
      model TEXT NOT NULL,
      status TEXT NOT NULL DEFAULT 'active',
      mode TEXT,
      catalog_order INTEGER,
      release_date TEXT,
      shutdown_date TEXT,
      supported_api_protocols_json TEXT NOT NULL DEFAULT '[]',
      supported_service_tiers_json TEXT NOT NULL DEFAULT '[]',
      supported_reasoning_efforts_json TEXT NOT NULL DEFAULT '[]',
      default_reasoning_effort TEXT,
      codex_supported_reasoning_levels_json TEXT NOT NULL DEFAULT '[]',
      codex_default_reasoning_level TEXT,
      codex_multi_agent_version TEXT,
      context_window_tokens INTEGER,
      max_input_tokens INTEGER,
      max_output_tokens INTEGER,
      max_tokens INTEGER,
      input_usd_per_1m REAL,
      output_usd_per_1m REAL,
      cached_input_usd_per_1m REAL,
      cache_write_usd_per_1m REAL,
      cache_write_1h_usd_per_1m REAL,
      service_tier_prices_json TEXT NOT NULL DEFAULT '{}',
      long_context_input_token_threshold INTEGER,
      long_context_input_token_threshold_inclusive INTEGER NOT NULL DEFAULT 0,
      long_context_input_cost_multiplier REAL,
      long_context_output_cost_multiplier REAL,
      image_input_usd_per_1m REAL,
      image_output_usd_per_1m REAL,
      audio_input_usd_per_1m REAL,
      audio_output_usd_per_1m REAL,
      output_usd_per_image REAL,
      supports_prompt_caching INTEGER NOT NULL DEFAULT 0,
      catalog_visible INTEGER NOT NULL DEFAULT 1,
      source TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      UNIQUE (provider_code, model),
      FOREIGN KEY (provider_code) REFERENCES providers(code),
      CHECK (status IN ('active', 'disabled')),
      CHECK (json_valid(service_tier_prices_json) AND json_type(service_tier_prices_json) = 'object')
    );

    CREATE INDEX IF NOT EXISTS idx_provider_model_catalog_lookup
      ON provider_model_catalog(provider_code, status, catalog_visible, catalog_order, model);

    CREATE TABLE IF NOT EXISTS custom_provider_models (
      id TEXT PRIMARY KEY,
      provider_code TEXT NOT NULL,
      model TEXT NOT NULL,
      scope TEXT NOT NULL DEFAULT 'personal',
      system_account_id TEXT,
      status TEXT NOT NULL DEFAULT 'active',
      catalog_visible INTEGER NOT NULL DEFAULT 1,
      mode TEXT,
      supported_api_protocols_json TEXT NOT NULL DEFAULT '[]',
      supported_service_tiers_json TEXT NOT NULL DEFAULT '[]',
      supported_reasoning_efforts_json TEXT NOT NULL DEFAULT '[]',
      default_reasoning_effort TEXT,
      release_date TEXT,
      shutdown_date TEXT,
      context_window_tokens INTEGER,
      max_input_tokens INTEGER,
      max_output_tokens INTEGER,
      input_usd_per_1m REAL,
      output_usd_per_1m REAL,
      cached_input_usd_per_1m REAL,
      cache_write_usd_per_1m REAL,
      cache_write_1h_usd_per_1m REAL,
      service_tier_prices_json TEXT NOT NULL DEFAULT '{}',
      image_input_usd_per_1m REAL,
      image_output_usd_per_1m REAL,
      audio_input_usd_per_1m REAL,
      audio_output_usd_per_1m REAL,
      output_usd_per_image REAL,
      currency TEXT NOT NULL DEFAULT 'USD',
      pricing_notes TEXT,
      capability_notes TEXT,
      notes TEXT,
      created_by TEXT NOT NULL,
      updated_by TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (provider_code) REFERENCES providers(code),
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      CHECK (scope IN ('personal', 'global')),
      CHECK (status IN ('draft', 'active', 'disabled')),
      CHECK (json_valid(service_tier_prices_json) AND json_type(service_tier_prices_json) = 'object'),
      CHECK (
        (scope = 'personal' AND system_account_id IS NOT NULL)
        OR (scope = 'global' AND system_account_id IS NULL)
      )
    );

    CREATE TABLE IF NOT EXISTS provider_default_health_check_models (
      system_account_id TEXT NOT NULL,
      provider_code TEXT NOT NULL,
      model TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, provider_code),
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (provider_code) REFERENCES providers(code)
    );

    CREATE TABLE IF NOT EXISTS provider_system_default_health_check_models (
      provider_code TEXT PRIMARY KEY,
      model TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (provider_code) REFERENCES providers(code)
    );

    CREATE TABLE IF NOT EXISTS proxy_profiles (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      name TEXT NOT NULL,
      description TEXT,
      type TEXT NOT NULL,
      host TEXT NOT NULL,
      port INTEGER NOT NULL,
      username TEXT,
      password_encrypted TEXT,
      enabled INTEGER NOT NULL DEFAULT 1,
      test_status TEXT NOT NULL DEFAULT 'unknown',
      latency_ms INTEGER,
      outbound_ip TEXT,
      outbound_region TEXT,
      last_test_message TEXT,
      last_tested_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS response_inspection_policies (
      id TEXT PRIMARY KEY,
      name TEXT NOT NULL,
      enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
      priority INTEGER NOT NULL DEFAULT 100 CHECK (priority BETWEEN 1 AND 9999),
      scope_type TEXT NOT NULL DEFAULT 'protocol',
      protocol_code TEXT NOT NULL,
      provider_code TEXT,
      match_json TEXT NOT NULL CHECK (json_valid(match_json) AND json_type(match_json) = 'object'),
      action TEXT NOT NULL,
      notes TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      CHECK (scope_type IN ('protocol', 'provider')),
      CHECK (action IN ('observe', 'drop_event', 'retry_no_avoidance', 'retry_next_account', 'avoid_account_ttl', 'avoid_upstream_bucket_ttl')),
      CHECK (
        (scope_type = 'protocol' AND provider_code IS NULL)
        OR (scope_type = 'provider' AND provider_code IS NOT NULL)
      )
    );

    CREATE TABLE IF NOT EXISTS external_integration_sources (
      id TEXT PRIMARY KEY,
      name TEXT NOT NULL,
      status TEXT NOT NULL DEFAULT 'active',
      scopes_json TEXT NOT NULL DEFAULT '[]',
      rate_limits_json TEXT NOT NULL DEFAULT '[]',
      expires_at TEXT,
      notes TEXT,
      last_used_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS external_integration_source_tokens (
      id TEXT PRIMARY KEY,
      source_ref_id TEXT NOT NULL,
      name TEXT NOT NULL,
      token_hash TEXT NOT NULL UNIQUE,
      token_secret_encrypted TEXT NOT NULL,
      token_prefix TEXT NOT NULL,
      token_suffix TEXT NOT NULL,
      status TEXT NOT NULL DEFAULT 'active',
      scopes_json TEXT NOT NULL DEFAULT '[]',
      expires_at TEXT,
      last_used_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      revoked_at TEXT,
      FOREIGN KEY (source_ref_id) REFERENCES external_integration_sources(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS accounts (
      id TEXT PRIMARY KEY,
      config_revision INTEGER NOT NULL DEFAULT 1,
      system_account_id TEXT NOT NULL,
      provider_code TEXT NOT NULL,
      provider_protocol_profile_id TEXT NOT NULL,
      protocol_code TEXT NOT NULL,
      protocol_version TEXT NOT NULL,
      name TEXT NOT NULL,
      type TEXT NOT NULL,
      status TEXT NOT NULL DEFAULT 'pending_test',
      credentials_encrypted TEXT NOT NULL,
      credential_fingerprint TEXT,
      credential_mask TEXT NOT NULL DEFAULT '',
      oauth_access_token_expires_at TEXT,
      oauth_refresh_token_present INTEGER NOT NULL DEFAULT 0,
      proxy_profile_id TEXT,
      concurrency_limit INTEGER NOT NULL DEFAULT 20,
      priority INTEGER NOT NULL DEFAULT 0,
      super_priority_enabled INTEGER NOT NULL DEFAULT 0,
      fallback_enabled INTEGER NOT NULL DEFAULT 0,
      client_compatibility TEXT NOT NULL DEFAULT 'openai_standard',
      schedulable INTEGER NOT NULL DEFAULT 1,
      availability_schedule_json TEXT,
      availability_schedule_next_check_at TEXT,
      notes TEXT,
      account_expires_at TEXT,
      last_used_at TEXT,
      cooldown_until TEXT,
      last_error_code TEXT,
      last_error_message TEXT,
      last_error_trace_id TEXT,
      cooldown_retest_failure_count INTEGER NOT NULL DEFAULT 0,
      cooldown_retest_observation_started_at TEXT,
      cooldown_retest_last_at TEXT,
      cooldown_retest_last_status_code INTEGER,
      temporary_unavailable_continuous_probe_enabled INTEGER NOT NULL DEFAULT 1 CHECK (temporary_unavailable_continuous_probe_enabled IN (0, 1)),
      health_check_model TEXT NOT NULL,
      health_check_endpoint_mode TEXT NOT NULL CHECK (health_check_endpoint_mode IN ('chat_json', 'chat_sse', 'responses_json', 'responses_sse', 'messages_json', 'messages_sse', 'generate_content_json', 'generate_content_sse', 'interactions_json', 'interactions_sse')),
      last_health_check_at TEXT,
      next_health_check_at TEXT,
      last_health_success_at TEXT,
      health_check_failure_count INTEGER NOT NULL DEFAULT 0,
      health_check_failure_started_at TEXT,
      last_health_check_status_code INTEGER,
      last_health_check_error_code TEXT,
      last_health_check_error_message TEXT,
      last_health_check_trace_id TEXT,
      stream_failure_count INTEGER NOT NULL DEFAULT 0,
      stream_failure_window_started_at TEXT,
      balance_query_enabled INTEGER NOT NULL DEFAULT 0,
      balance_query_config_json TEXT NOT NULL DEFAULT '{}',
      balance_query_next_refresh_at TEXT,
      authorization_instance_source_account_id TEXT,
      authorization_instance_authorization_id TEXT,
      authorization_instance_owner_system_account_id TEXT,
      deleted_at TEXT,
      deleted_by TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (provider_code) REFERENCES providers(code),
      FOREIGN KEY (provider_protocol_profile_id) REFERENCES provider_protocol_profiles(id),
      FOREIGN KEY (proxy_profile_id) REFERENCES proxy_profiles(id),
      FOREIGN KEY (authorization_instance_source_account_id) REFERENCES accounts(id),
      FOREIGN KEY (authorization_instance_authorization_id) REFERENCES resource_authorizations(id)
    );

    CREATE TABLE IF NOT EXISTS account_name_search_terms (
      account_id TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      term TEXT NOT NULL,
      created_at TEXT NOT NULL,
      PRIMARY KEY (account_id, term),
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS account_name_search_documents (
      account_id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      normalized_name TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS account_api_key_runtime_states (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      account_id TEXT NOT NULL,
      key_fingerprint TEXT NOT NULL,
      key_index INTEGER NOT NULL DEFAULT 0,
      credential_revision TEXT,
      status TEXT NOT NULL DEFAULT 'active',
      failure_count INTEGER NOT NULL DEFAULT 0,
      consecutive_failures INTEGER NOT NULL DEFAULT 0,
      success_count INTEGER NOT NULL DEFAULT 0,
      cooldown_until TEXT,
      next_probe_at TEXT,
      probe_backoff_seconds INTEGER NOT NULL DEFAULT 0,
      recovery_started_at TEXT,
      last_attempt_at TEXT,
      last_success_at TEXT,
      last_failure_at TEXT,
      last_error_code TEXT,
      last_error_message TEXT,
      last_trace_id TEXT,
      last_probe_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS account_supported_models (
      account_id TEXT NOT NULL,
      provider_code TEXT NOT NULL,
      model TEXT NOT NULL,
      created_at TEXT NOT NULL,
      PRIMARY KEY (account_id, model),
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (provider_code) REFERENCES providers(code)
    );

    CREATE TABLE IF NOT EXISTS account_model_mappings (
      account_id TEXT NOT NULL,
      provider_code TEXT NOT NULL,
      source_model TEXT NOT NULL,
      source_endpoint_family TEXT NOT NULL,
      upstream_model TEXT NOT NULL,
      upstream_endpoint_family TEXT NOT NULL,
      enabled INTEGER NOT NULL DEFAULT 1,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (account_id, source_model, source_endpoint_family),
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (provider_code) REFERENCES providers(code)
    );

    CREATE TABLE IF NOT EXISTS account_tags (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      name TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS account_tag_bindings (
      account_id TEXT NOT NULL,
      tag_id TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      created_at TEXT NOT NULL,
      PRIMARY KEY (account_id, tag_id),
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (tag_id) REFERENCES account_tags(id) ON DELETE CASCADE,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS account_test_tasks (
      id TEXT PRIMARY KEY,
      account_id TEXT NOT NULL,
      account_name TEXT NOT NULL,
      provider_code TEXT NOT NULL,
      provider_protocol_profile_id TEXT NOT NULL,
      protocol_code TEXT NOT NULL,
      protocol_version TEXT NOT NULL,
      account_type TEXT NOT NULL,
      request_system_account_id TEXT NOT NULL,
      request_role TEXT NOT NULL,
      request_system_account_filter_id TEXT,
      diagnostics TEXT NOT NULL DEFAULT 'full',
      model TEXT,
      test_endpoint_mode TEXT,
      draft_account_encrypted TEXT,
      status TEXT NOT NULL DEFAULT 'queued',
      status_message TEXT,
      result_json TEXT,
      error_message TEXT,
      cancel_requested INTEGER NOT NULL DEFAULT 0,
      queued_at TEXT NOT NULL,
      started_at TEXT,
      finished_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS account_test_sessions (
      id TEXT PRIMARY KEY,
      request_system_account_id TEXT NOT NULL,
      request_role TEXT NOT NULL,
      request_system_account_filter_id TEXT,
      status TEXT NOT NULL DEFAULT 'running',
      cancel_reason TEXT,
      last_heartbeat_at TEXT NOT NULL,
      cancel_requested_at TEXT,
      finished_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      CHECK (status IN ('running', 'canceled', 'expired', 'completed'))
    );

    CREATE TABLE IF NOT EXISTS account_test_session_tasks (
      session_id TEXT NOT NULL,
      task_id TEXT NOT NULL,
      created_at TEXT NOT NULL,
      PRIMARY KEY (session_id, task_id),
      FOREIGN KEY (session_id) REFERENCES account_test_sessions(id) ON DELETE CASCADE,
      FOREIGN KEY (task_id) REFERENCES account_test_tasks(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS system_teams (
      id TEXT PRIMARY KEY,
      name TEXT NOT NULL,
      description TEXT,
      status TEXT NOT NULL DEFAULT 'active',
      created_by TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS system_team_members (
      id TEXT PRIMARY KEY,
      team_id TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      member_role TEXT NOT NULL DEFAULT 'member',
      status TEXT NOT NULL DEFAULT 'active',
      joined_at TEXT NOT NULL,
      removed_at TEXT,
      created_by TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (team_id) REFERENCES system_teams(id) ON DELETE CASCADE,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS resource_authorizations (
      id TEXT PRIMARY KEY,
      resource_type TEXT NOT NULL,
      resource_id TEXT NOT NULL,
      resource_owner_system_account_id TEXT NOT NULL,
      grantee_system_account_id TEXT NOT NULL,
      scope TEXT NOT NULL DEFAULT 'use',
      status TEXT NOT NULL DEFAULT 'active',
      effective_source_type TEXT,
      effective_source_team_id TEXT,
      activated_at TEXT,
      last_source_changed_at TEXT,
      remark TEXT,
      expires_at TEXT,
      limits_json TEXT,
      created_by TEXT NOT NULL,
      created_at TEXT NOT NULL,
      revoked_by TEXT,
      revoked_at TEXT,
      revoked_reason TEXT,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (grantee_system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS resource_authorization_sources (
      id TEXT PRIMARY KEY,
      authorization_id TEXT NOT NULL,
      source_type TEXT NOT NULL,
      source_team_id TEXT,
      status TEXT NOT NULL DEFAULT 'active',
      activated_at TEXT,
      ended_at TEXT,
      ended_reason TEXT,
      created_by TEXT NOT NULL,
      created_at TEXT NOT NULL,
      revoked_by TEXT,
      revoked_at TEXT,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (authorization_id) REFERENCES resource_authorizations(id) ON DELETE CASCADE,
      FOREIGN KEY (source_team_id) REFERENCES system_teams(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS resource_authorization_grants (
      id TEXT PRIMARY KEY,
      resource_type TEXT NOT NULL,
      resource_id TEXT NOT NULL,
      resource_owner_system_account_id TEXT NOT NULL,
      grantee_type TEXT NOT NULL,
      grantee_system_account_id TEXT,
      grantee_team_id TEXT,
      scope TEXT NOT NULL DEFAULT 'use',
      status TEXT NOT NULL DEFAULT 'active',
      remark TEXT,
      expires_at TEXT,
      limits_json TEXT,
      created_by TEXT NOT NULL,
      created_at TEXT NOT NULL,
      revoked_by TEXT,
      revoked_at TEXT,
      updated_at TEXT NOT NULL,
      CHECK (
        (grantee_type = 'system_account' AND grantee_system_account_id IS NOT NULL AND grantee_team_id IS NULL)
        OR
        (grantee_type = 'team' AND grantee_team_id IS NOT NULL AND grantee_system_account_id IS NULL)
      ),
      FOREIGN KEY (grantee_system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (grantee_team_id) REFERENCES system_teams(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS groups (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      name TEXT NOT NULL,
      provider_code TEXT NOT NULL,
      description TEXT,
      enabled INTEGER NOT NULL DEFAULT 1,
      is_default INTEGER NOT NULL DEFAULT 0,
      group_type TEXT NOT NULL DEFAULT 'personal',
      scheduling_policy_json TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (provider_code) REFERENCES providers(code)
    );

    CREATE TABLE IF NOT EXISTS group_authorization_settings (
      authorization_id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      group_id TEXT NOT NULL,
      enabled INTEGER NOT NULL DEFAULT 1,
      group_type TEXT NOT NULL DEFAULT 'personal',
      scheduling_policy_json TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (authorization_id) REFERENCES resource_authorizations(id) ON DELETE CASCADE,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS group_accounts (
      system_account_id TEXT NOT NULL,
      group_id TEXT NOT NULL,
      account_id TEXT NOT NULL,
      account_authorization_id TEXT,
      local_priority INTEGER NOT NULL DEFAULT 0,
      local_super_priority_enabled INTEGER NOT NULL DEFAULT 0,
      local_fallback_enabled INTEGER NOT NULL DEFAULT 0,
      enabled INTEGER NOT NULL DEFAULT 1,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (group_id, account_id),
      FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE CASCADE,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (account_authorization_id) REFERENCES resource_authorizations(id)
    );

    CREATE TABLE IF NOT EXISTS group_account_stats_dirty (
      group_id TEXT PRIMARY KEY,
      reason TEXT,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS route_strategies (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      name TEXT NOT NULL,
      description TEXT,
      mode TEXT NOT NULL DEFAULT 'normal',
      status TEXT NOT NULL DEFAULT 'active',
      is_default INTEGER NOT NULL DEFAULT 0,
      config_json TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS route_strategy_groups (
      id TEXT PRIMARY KEY,
      route_strategy_id TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      group_id TEXT NOT NULL,
      priority INTEGER NOT NULL DEFAULT 1,
      weight INTEGER NOT NULL DEFAULT 1,
      status TEXT NOT NULL DEFAULT 'active',
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (route_strategy_id) REFERENCES route_strategies(id) ON DELETE CASCADE,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS api_keys (
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
      expires_at TEXT,
      quota_limits_json TEXT,
      availability_schedule_json TEXT,
      availability_schedule_next_check_at TEXT,
      last_used_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (route_strategy_id) REFERENCES route_strategies(id)
    );

    CREATE TABLE IF NOT EXISTS api_key_schedule_status_events (
      event_key TEXT PRIMARY KEY,
      api_key_id TEXT NOT NULL,
      status TEXT NOT NULL,
      executed_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS openai_compatible_files (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      api_key_id TEXT NOT NULL,
      purpose TEXT NOT NULL,
      container_id TEXT,
      filename TEXT NOT NULL,
      bytes INTEGER NOT NULL,
      media_type TEXT,
      storage_key TEXT NOT NULL UNIQUE,
      sha256 TEXT NOT NULL,
      status TEXT NOT NULL DEFAULT 'processed',
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      expires_at TEXT,
      deleted_at TEXT,
      FOREIGN KEY (api_key_id) REFERENCES api_keys(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS openai_compatible_vector_stores (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      api_key_id TEXT NOT NULL,
      name TEXT,
      description TEXT,
      metadata_json TEXT NOT NULL DEFAULT '{}',
      bytes INTEGER NOT NULL DEFAULT 0,
      status TEXT NOT NULL DEFAULT 'active',
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      expires_after_anchor TEXT,
      expires_after_days INTEGER,
      expires_at TEXT,
      deleted_at TEXT,
      FOREIGN KEY (api_key_id) REFERENCES api_keys(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS openai_compatible_vector_store_files (
      vector_store_id TEXT NOT NULL,
      file_id TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      api_key_id TEXT NOT NULL,
      attributes_json TEXT NOT NULL DEFAULT '{}',
      chunking_strategy_json TEXT NOT NULL DEFAULT '{}',
      status TEXT NOT NULL DEFAULT 'in_progress',
      usage_bytes INTEGER NOT NULL DEFAULT 0,
      last_error_json TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      deleted_at TEXT,
      PRIMARY KEY (vector_store_id, file_id),
      FOREIGN KEY (vector_store_id) REFERENCES openai_compatible_vector_stores(id) ON DELETE CASCADE,
      FOREIGN KEY (file_id) REFERENCES openai_compatible_files(id) ON DELETE CASCADE,
      FOREIGN KEY (api_key_id) REFERENCES api_keys(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS openai_compatible_vector_store_chunks (
      id TEXT PRIMARY KEY,
      vector_store_id TEXT NOT NULL,
      file_id TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      api_key_id TEXT NOT NULL,
      chunk_index INTEGER NOT NULL,
      content_text TEXT NOT NULL,
      content_preview TEXT NOT NULL,
      token_estimate INTEGER NOT NULL DEFAULT 0,
      keyword_index_text TEXT NOT NULL,
      created_at TEXT NOT NULL,
      FOREIGN KEY (vector_store_id, file_id) REFERENCES openai_compatible_vector_store_files(vector_store_id, file_id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS account_schedule_status_events (
      event_key TEXT PRIMARY KEY,
      account_id TEXT NOT NULL,
      status TEXT NOT NULL,
      executed_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS system_settings (
      system_account_id TEXT NOT NULL,
      key TEXT NOT NULL,
      value_json TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, key),
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS gateway_model_catalog_snapshots (
      system_account_id TEXT NOT NULL,
      protocol TEXT NOT NULL,
      variant TEXT NOT NULL,
      payload_json TEXT NOT NULL,
      model_count INTEGER NOT NULL DEFAULT 0,
      revision TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, protocol, variant),
      CHECK (protocol IN ('openai', 'anthropic', 'gemini')),
      CHECK (variant IN ('default', 'codex') OR variant LIKE 'chat_list:%' OR variant LIKE 'chat_model:%'),
      CHECK (model_count >= 0),
      CHECK (json_valid(payload_json) AND json_type(payload_json) = 'object')
    );

    CREATE INDEX IF NOT EXISTS idx_gateway_model_catalog_snapshots_updated
      ON gateway_model_catalog_snapshots(updated_at, system_account_id, protocol, variant);

    CREATE TABLE IF NOT EXISTS announcements (
      id TEXT PRIMARY KEY,
      title TEXT NOT NULL,
      content TEXT NOT NULL,
      level TEXT NOT NULL DEFAULT 'info',
      status TEXT NOT NULL DEFAULT 'draft',
      created_by TEXT NOT NULL,
      updated_by TEXT,
      published_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (created_by) REFERENCES system_accounts(id),
      FOREIGN KEY (updated_by) REFERENCES system_accounts(id)
    );

    CREATE TABLE IF NOT EXISTS announcement_reads (
      announcement_id TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      read_at TEXT NOT NULL,
      PRIMARY KEY (announcement_id, system_account_id),
      FOREIGN KEY (announcement_id) REFERENCES announcements(id) ON DELETE CASCADE,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    );

    CREATE INDEX IF NOT EXISTS idx_accounts_provider_status ON accounts(provider_code, status);
    CREATE INDEX IF NOT EXISTS idx_accounts_protocol_profile_status ON accounts(provider_protocol_profile_id, status);
    CREATE INDEX IF NOT EXISTS idx_groups_provider ON groups(provider_code);
    CREATE INDEX IF NOT EXISTS idx_system_sessions_expires_at ON system_sessions(expires_at);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_system_accounts_username_unique_lower ON system_accounts(lower(username));
    CREATE UNIQUE INDEX IF NOT EXISTS idx_system_accounts_display_name_unique_lower ON system_accounts(lower(display_name));
    CREATE INDEX IF NOT EXISTS idx_response_inspection_policies_enabled_priority ON response_inspection_policies(enabled, priority, updated_at DESC, id);
    CREATE INDEX IF NOT EXISTS idx_external_integration_sources_updated ON external_integration_sources(updated_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_external_integration_sources_status_updated ON external_integration_sources(status, updated_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_external_integration_sources_name_lookup ON external_integration_sources(name COLLATE NOCASE, id);
    CREATE INDEX IF NOT EXISTS idx_external_integration_source_tokens_source ON external_integration_source_tokens(source_ref_id, status, expires_at);
    CREATE INDEX IF NOT EXISTS idx_system_accounts_updated_lookup ON system_accounts(updated_at, id);
    CREATE INDEX IF NOT EXISTS idx_system_accounts_username_lookup ON system_accounts(username COLLATE NOCASE, id);
    CREATE INDEX IF NOT EXISTS idx_system_accounts_display_name_lookup ON system_accounts(display_name COLLATE NOCASE, id);
    CREATE INDEX IF NOT EXISTS idx_accounts_credential_fingerprint ON accounts(credential_fingerprint) WHERE credential_fingerprint IS NOT NULL;
    CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_owner_name_unique ON accounts(system_account_id, name) WHERE deleted_at IS NULL;
    CREATE INDEX IF NOT EXISTS idx_accounts_owner_all_name_lookup
      ON accounts(system_account_id, name, id)
      WHERE deleted_at IS NULL;
    CREATE INDEX IF NOT EXISTS idx_accounts_owner_name_lookup
      ON accounts(system_account_id, name, id)
      WHERE deleted_at IS NULL AND authorization_instance_authorization_id IS NULL;
    CREATE INDEX IF NOT EXISTS idx_accounts_name_lookup ON accounts(name, id) WHERE deleted_at IS NULL;
    CREATE INDEX IF NOT EXISTS idx_accounts_system_account_name_lookup ON accounts(system_account_id, name, id);
    CREATE INDEX IF NOT EXISTS idx_account_name_search_terms_term_owner
      ON account_name_search_terms(term, system_account_id, account_id);
    CREATE INDEX IF NOT EXISTS idx_account_name_search_terms_owner_term
      ON account_name_search_terms(system_account_id, term, account_id);
    CREATE INDEX IF NOT EXISTS idx_account_name_search_terms_account
      ON account_name_search_terms(account_id);
    CREATE INDEX IF NOT EXISTS idx_account_name_search_documents_owner
      ON account_name_search_documents(system_account_id, account_id);
    CREATE INDEX IF NOT EXISTS idx_accounts_provider_lookup ON accounts(provider_code, id);
    CREATE INDEX IF NOT EXISTS idx_accounts_protocol_profile_lookup ON accounts(provider_protocol_profile_id, id);
    CREATE INDEX IF NOT EXISTS idx_accounts_system_account_provider_lookup ON accounts(system_account_id, provider_code, id);
    CREATE INDEX IF NOT EXISTS idx_accounts_system_account_protocol_profile_lookup ON accounts(system_account_id, provider_protocol_profile_id, id);
    CREATE INDEX IF NOT EXISTS idx_accounts_type_lookup ON accounts(type, id);
    CREATE INDEX IF NOT EXISTS idx_accounts_system_account_type_lookup ON accounts(system_account_id, type, id);
    CREATE INDEX IF NOT EXISTS idx_accounts_system_account ON accounts(system_account_id);
    CREATE INDEX IF NOT EXISTS idx_accounts_owner_list_order
      ON accounts(system_account_id, priority ASC, created_at ASC, id ASC)
      WHERE deleted_at IS NULL AND authorization_instance_authorization_id IS NULL;
    CREATE INDEX IF NOT EXISTS idx_accounts_proxy_profile ON accounts(proxy_profile_id, id);
    CREATE INDEX IF NOT EXISTS idx_accounts_system_account_last_used ON accounts(system_account_id, last_used_at);
    CREATE INDEX IF NOT EXISTS idx_accounts_system_account_concurrency ON accounts(system_account_id, concurrency_limit);
    CREATE INDEX IF NOT EXISTS idx_accounts_expiry_sweep
      ON accounts(account_expires_at ASC, updated_at ASC, id ASC)
      WHERE account_expires_at IS NOT NULL;
    CREATE INDEX IF NOT EXISTS idx_accounts_owner_expiry_sweep
      ON accounts(system_account_id, account_expires_at ASC, updated_at ASC, id ASC)
      WHERE account_expires_at IS NOT NULL;
    CREATE INDEX IF NOT EXISTS idx_accounts_availability_schedule_next_check
      ON accounts(availability_schedule_next_check_at ASC, id ASC)
      WHERE availability_schedule_json IS NOT NULL AND deleted_at IS NULL;
    CREATE INDEX IF NOT EXISTS idx_accounts_super_priority ON accounts(super_priority_enabled, status, priority);
    CREATE INDEX IF NOT EXISTS idx_accounts_dispatch_priority ON accounts(fallback_enabled, super_priority_enabled, status, priority);
    CREATE INDEX IF NOT EXISTS idx_accounts_openai_oauth_refresh_due
      ON accounts(provider_code, type, oauth_refresh_token_present, oauth_access_token_expires_at, status, id);
    CREATE INDEX IF NOT EXISTS idx_accounts_openai_oauth_refresh_pg_due
      ON accounts(provider_protocol_profile_id, type, oauth_refresh_token_present, (oauth_access_token_expires_at IS NOT NULL), oauth_access_token_expires_at ASC, updated_at ASC, id ASC)
      WHERE authorization_instance_authorization_id IS NULL AND deleted_at IS NULL;
    CREATE INDEX IF NOT EXISTS idx_accounts_health_check_due
      ON accounts(status, next_health_check_at, updated_at, id)
      WHERE deleted_at IS NULL;
    CREATE INDEX IF NOT EXISTS idx_accounts_health_check_candidate_order
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
        AND type IN ('api_key', 'oauth', 'google_oauth');
    CREATE INDEX IF NOT EXISTS idx_accounts_cooldown_retest_candidate_order
      ON accounts(cooldown_until ASC, priority ASC, created_at ASC, id ASC, health_check_endpoint_mode)
      WHERE deleted_at IS NULL
        AND cooldown_until IS NOT NULL
        AND schedulable = 1
        AND type IN ('api_key', 'oauth', 'google_oauth')
        AND status IN ('temporary_unavailable', 'rate_limited');
    CREATE INDEX IF NOT EXISTS idx_accounts_deleted_cleanup
      ON accounts(deleted_at ASC, updated_at ASC, id ASC)
      WHERE deleted_at IS NOT NULL;
    CREATE INDEX IF NOT EXISTS idx_accounts_balance_query_due
      ON accounts(balance_query_next_refresh_at ASC, id ASC)
      WHERE balance_query_enabled = 1
        AND deleted_at IS NULL
        AND authorization_instance_authorization_id IS NULL;
    CREATE UNIQUE INDEX IF NOT EXISTS idx_account_api_key_runtime_unique
      ON account_api_key_runtime_states(account_id, key_fingerprint);
    CREATE INDEX IF NOT EXISTS idx_account_api_key_runtime_status
      ON account_api_key_runtime_states(account_id, status, cooldown_until);
    CREATE INDEX IF NOT EXISTS idx_account_api_key_runtime_probe
      ON account_api_key_runtime_states(account_id, status, next_probe_at ASC, updated_at ASC, key_index ASC)
      WHERE next_probe_at IS NOT NULL;
    CREATE INDEX IF NOT EXISTS idx_account_api_key_runtime_owner
      ON account_api_key_runtime_states(system_account_id, account_id);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_custom_provider_models_personal_unique
      ON custom_provider_models(provider_code, system_account_id, model)
      WHERE scope = 'personal';
    CREATE UNIQUE INDEX IF NOT EXISTS idx_custom_provider_models_global_unique
      ON custom_provider_models(provider_code, model)
      WHERE scope = 'global';
    CREATE INDEX IF NOT EXISTS idx_custom_provider_models_catalog_lookup
      ON custom_provider_models(provider_code, status, catalog_visible, scope, system_account_id, model);
    CREATE INDEX IF NOT EXISTS idx_provider_default_health_check_models_model
      ON provider_default_health_check_models(provider_code, model, system_account_id);
    CREATE INDEX IF NOT EXISTS idx_provider_system_default_health_check_models_model
      ON provider_system_default_health_check_models(model, provider_code);
    CREATE INDEX IF NOT EXISTS idx_account_supported_models_provider_model ON account_supported_models(provider_code, model, account_id);
    CREATE INDEX IF NOT EXISTS idx_account_model_mappings_source ON account_model_mappings(provider_code, source_model, source_endpoint_family, account_id);
    CREATE INDEX IF NOT EXISTS idx_account_model_mappings_upstream ON account_model_mappings(provider_code, upstream_model, upstream_endpoint_family, account_id);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_account_tags_owner_name_unique ON account_tags(system_account_id, name);
    CREATE INDEX IF NOT EXISTS idx_account_tags_owner_name_lookup ON account_tags(system_account_id, name, id);
    CREATE INDEX IF NOT EXISTS idx_account_tag_bindings_owner_tag ON account_tag_bindings(system_account_id, tag_id, account_id);
    CREATE INDEX IF NOT EXISTS idx_account_tag_bindings_tag_owner ON account_tag_bindings(tag_id, system_account_id, account_id);
    CREATE INDEX IF NOT EXISTS idx_account_tag_bindings_tag ON account_tag_bindings(tag_id, account_id);
    CREATE INDEX IF NOT EXISTS idx_account_test_tasks_request_updated ON account_test_tasks(request_system_account_id, updated_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_account_test_tasks_status_queued ON account_test_tasks(status, queued_at ASC, id ASC);
    CREATE INDEX IF NOT EXISTS idx_account_test_tasks_finished_cleanup ON account_test_tasks(finished_at ASC, id ASC) WHERE finished_at IS NOT NULL;
    CREATE INDEX IF NOT EXISTS idx_account_test_sessions_request_updated ON account_test_sessions(request_system_account_id, updated_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_account_test_sessions_status_heartbeat ON account_test_sessions(status, last_heartbeat_at ASC, id ASC);
    CREATE INDEX IF NOT EXISTS idx_account_test_session_tasks_task ON account_test_session_tasks(task_id, session_id);
    CREATE INDEX IF NOT EXISTS idx_account_test_session_tasks_session ON account_test_session_tasks(session_id, task_id);
    CREATE INDEX IF NOT EXISTS idx_groups_system_account ON groups(system_account_id);
    CREATE INDEX IF NOT EXISTS idx_groups_updated ON groups(updated_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_groups_system_account_updated ON groups(system_account_id, updated_at DESC, id DESC);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_groups_owner_provider_name_unique ON groups(system_account_id, provider_code, name);
    CREATE INDEX IF NOT EXISTS idx_groups_name_lookup ON groups(name, id);
    CREATE INDEX IF NOT EXISTS idx_groups_system_account_name_lookup ON groups(system_account_id, name, id);
    CREATE INDEX IF NOT EXISTS idx_groups_provider_name_lookup ON groups(provider_code, name, id);
    CREATE INDEX IF NOT EXISTS idx_groups_system_account_provider_name_lookup ON groups(system_account_id, provider_code, name, id);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_groups_owner_provider_default_unique ON groups(system_account_id, provider_code) WHERE is_default = 1;
    CREATE INDEX IF NOT EXISTS idx_system_teams_status ON system_teams(status, updated_at);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_system_teams_name_unique ON system_teams(name);
    CREATE INDEX IF NOT EXISTS idx_system_teams_name_lookup ON system_teams(name, id);
    CREATE INDEX IF NOT EXISTS idx_system_team_members_team ON system_team_members(team_id, status);
    CREATE INDEX IF NOT EXISTS idx_system_teams_list_order ON system_teams(status, updated_at DESC, name ASC, id ASC);
    CREATE INDEX IF NOT EXISTS idx_system_team_members_team_status_joined ON system_team_members(team_id, status, joined_at ASC, id ASC);
    CREATE INDEX IF NOT EXISTS idx_system_team_members_account ON system_team_members(system_account_id, status);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_system_team_members_active_unique ON system_team_members(team_id, system_account_id) WHERE status = 'active';
    CREATE INDEX IF NOT EXISTS idx_resource_authorizations_resource ON resource_authorizations(resource_type, resource_id, status);
    CREATE INDEX IF NOT EXISTS idx_resource_authorizations_owner ON resource_authorizations(resource_owner_system_account_id, status);
    CREATE INDEX IF NOT EXISTS idx_resource_authorizations_grantee ON resource_authorizations(grantee_system_account_id, status);
    CREATE INDEX IF NOT EXISTS idx_resource_authorizations_expires_at ON resource_authorizations(expires_at, status);
    CREATE INDEX IF NOT EXISTS idx_resource_authorizations_quota_snapshot
      ON resource_authorizations(status, updated_at DESC, id)
      WHERE limits_json IS NOT NULL;
    CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorizations_user_unique ON resource_authorizations(resource_type, resource_id, grantee_system_account_id);
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_sources_authorization ON resource_authorization_sources(authorization_id, status);
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_sources_team ON resource_authorization_sources(source_team_id, status);
    CREATE INDEX IF NOT EXISTS idx_group_accounts_account_authorization ON group_accounts(account_authorization_id);
    CREATE INDEX IF NOT EXISTS idx_group_accounts_owner_group_enabled ON group_accounts(system_account_id, group_id, enabled, account_id);
    CREATE INDEX IF NOT EXISTS idx_group_accounts_group_enabled ON group_accounts(group_id, enabled, account_id);
    CREATE INDEX IF NOT EXISTS idx_group_accounts_dispatch_candidate_window
      ON group_accounts(group_id, system_account_id, enabled, local_fallback_enabled ASC, local_super_priority_enabled DESC, local_priority ASC, created_at ASC, account_id ASC);
    CREATE INDEX IF NOT EXISTS idx_group_accounts_account_scope_enabled ON group_accounts(account_id, system_account_id, enabled);
    CREATE INDEX IF NOT EXISTS idx_group_accounts_scope_enabled_updated ON group_accounts(system_account_id, account_id, enabled, updated_at DESC);
    CREATE INDEX IF NOT EXISTS idx_group_authorization_settings_scope_group
      ON group_authorization_settings(system_account_id, group_id);
    CREATE INDEX IF NOT EXISTS idx_group_account_stats_dirty_updated ON group_account_stats_dirty(updated_at);
    CREATE INDEX IF NOT EXISTS idx_api_keys_system_account ON api_keys(system_account_id);
    CREATE INDEX IF NOT EXISTS idx_api_keys_route_strategy ON api_keys(route_strategy_id);
    CREATE INDEX IF NOT EXISTS idx_api_keys_system_account_updated ON api_keys(system_account_id, updated_at DESC, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_api_keys_default_updated ON api_keys(is_default DESC, updated_at DESC, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_api_keys_system_account_default_updated ON api_keys(system_account_id, is_default DESC, updated_at DESC, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_api_keys_quota_snapshot
      ON api_keys(status, updated_at DESC, id)
      WHERE quota_limits_json IS NOT NULL;
    CREATE INDEX IF NOT EXISTS idx_api_keys_availability_schedule_next_check
      ON api_keys(availability_schedule_next_check_at ASC, id ASC)
      WHERE availability_schedule_json IS NOT NULL;
    CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_owner_name_unique ON api_keys(system_account_id, name);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_route_default_unique ON api_keys(route_strategy_id) WHERE is_default = 1;
    CREATE INDEX IF NOT EXISTS idx_api_keys_name_lookup ON api_keys(name, id);
    CREATE INDEX IF NOT EXISTS idx_api_keys_system_account_name_lookup ON api_keys(system_account_id, name, id);
    CREATE INDEX IF NOT EXISTS idx_route_strategies_owner_mode ON route_strategies(system_account_id, mode, status, updated_at DESC, id DESC);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_route_strategies_owner_name_unique ON route_strategies(system_account_id, name);
    CREATE INDEX IF NOT EXISTS idx_route_strategies_name_lookup ON route_strategies(name, id);
    CREATE INDEX IF NOT EXISTS idx_route_strategies_system_account_name_lookup ON route_strategies(system_account_id, name, id);
    CREATE INDEX IF NOT EXISTS idx_route_strategy_groups_strategy_priority ON route_strategy_groups(route_strategy_id, status, priority ASC, created_at ASC, id ASC);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_route_strategy_groups_unique ON route_strategy_groups(route_strategy_id, group_id);
    CREATE INDEX IF NOT EXISTS idx_route_strategy_groups_group_strategy ON route_strategy_groups(group_id, route_strategy_id);
    CREATE INDEX IF NOT EXISTS idx_route_strategy_groups_owner_group ON route_strategy_groups(system_account_id, group_id, route_strategy_id);
    CREATE INDEX IF NOT EXISTS idx_api_key_schedule_status_events_api_key
      ON api_key_schedule_status_events(api_key_id, executed_at DESC);
    CREATE INDEX IF NOT EXISTS idx_openai_compatible_files_owner_created
      ON openai_compatible_files(system_account_id, api_key_id, created_at DESC, id DESC)
      WHERE deleted_at IS NULL;
    CREATE INDEX IF NOT EXISTS idx_openai_compatible_files_purpose_created
      ON openai_compatible_files(system_account_id, api_key_id, purpose, created_at DESC, id DESC)
      WHERE deleted_at IS NULL;
    CREATE INDEX IF NOT EXISTS idx_openai_compatible_files_container_created
      ON openai_compatible_files(system_account_id, api_key_id, container_id, created_at DESC, id DESC)
      WHERE deleted_at IS NULL AND container_id IS NOT NULL;
    CREATE INDEX IF NOT EXISTS idx_openai_compatible_vector_stores_owner_created
      ON openai_compatible_vector_stores(system_account_id, api_key_id, created_at DESC, id DESC)
      WHERE deleted_at IS NULL;
    CREATE INDEX IF NOT EXISTS idx_openai_compatible_vector_store_files_owner_created
      ON openai_compatible_vector_store_files(system_account_id, api_key_id, vector_store_id, created_at DESC, file_id DESC)
      WHERE deleted_at IS NULL;
    CREATE INDEX IF NOT EXISTS idx_openai_compatible_vector_store_chunks_search
      ON openai_compatible_vector_store_chunks(system_account_id, api_key_id, vector_store_id, file_id, chunk_index);
    CREATE INDEX IF NOT EXISTS idx_account_schedule_status_events_account
      ON account_schedule_status_events(account_id, executed_at DESC);
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_owner ON resource_authorization_grants(resource_owner_system_account_id, status);
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_resource ON resource_authorization_grants(resource_type, resource_id, status);
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_grantee_user ON resource_authorization_grants(grantee_system_account_id, status);
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_grantee_team ON resource_authorization_grants(grantee_team_id, status);
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_created ON resource_authorization_grants(created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_owner_created ON resource_authorization_grants(resource_owner_system_account_id, status, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_resource_created ON resource_authorization_grants(resource_type, resource_id, status, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_grantee_user_created ON resource_authorization_grants(grantee_system_account_id, status, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_grantee_team_created ON resource_authorization_grants(grantee_team_id, status, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_team_quota_snapshot
      ON resource_authorization_grants(resource_type, resource_id, grantee_team_id, status, updated_at DESC, id)
      WHERE grantee_type = 'team' AND limits_json IS NOT NULL;
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_expiry_sweep
      ON resource_authorization_grants(expires_at ASC, updated_at ASC, id ASC)
      WHERE status IN ('active', 'paused') AND expires_at IS NOT NULL;
    CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_grants_active_user_unique ON resource_authorization_grants(resource_type, resource_id, grantee_system_account_id) WHERE status = 'active' AND grantee_type = 'system_account';
    CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_grants_active_team_unique ON resource_authorization_grants(resource_type, resource_id, grantee_team_id) WHERE status = 'active' AND grantee_type = 'team';
    CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_sources_active_manual_unique ON resource_authorization_sources(authorization_id, source_type) WHERE status = 'active' AND source_type = 'manual';
    CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_sources_active_team_unique ON resource_authorization_sources(authorization_id, source_type, source_team_id) WHERE status = 'active' AND source_type = 'team';
    CREATE INDEX IF NOT EXISTS idx_proxy_profiles_system_account ON proxy_profiles(system_account_id);
    CREATE INDEX IF NOT EXISTS idx_proxy_profiles_updated ON proxy_profiles(updated_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_proxy_profiles_enabled_name_lookup ON proxy_profiles(enabled, name, updated_at DESC, id ASC);
    CREATE INDEX IF NOT EXISTS idx_proxy_profiles_latency_refresh_due
      ON proxy_profiles(enabled, (last_tested_at IS NOT NULL), last_tested_at ASC, updated_at DESC, id ASC);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_proxy_profiles_name_unique ON proxy_profiles(name);
    CREATE INDEX IF NOT EXISTS idx_proxy_profiles_name_lookup ON proxy_profiles(name, id);
    CREATE INDEX IF NOT EXISTS idx_announcements_public ON announcements(status, published_at DESC, created_at DESC);
    CREATE INDEX IF NOT EXISTS idx_announcements_admin ON announcements(updated_at DESC, created_at DESC);
    CREATE INDEX IF NOT EXISTS idx_announcements_admin_page ON announcements(updated_at DESC, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_announcement_reads_account ON announcement_reads(system_account_id, read_at DESC);
  `)
  ensureResponseInspectionPolicyIndexes(database)
  ensureExternalIntegrationSourceIndexes(database)
  ensureAuthorizationInstanceIndexes(database)
}

function ensureExternalIntegrationSourceIndexes(database: DatabaseSync): void {
  database.exec(`
    CREATE INDEX IF NOT EXISTS idx_external_integration_sources_status ON external_integration_sources(status, name);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_external_integration_sources_name_unique_lower ON external_integration_sources(lower(name));
  `)
}

function ensureResponseInspectionPolicyIndexes(database: DatabaseSync): void {
  database.exec(`
    CREATE INDEX IF NOT EXISTS idx_response_inspection_policies_enabled_priority ON response_inspection_policies(enabled, priority, updated_at DESC, id);
    CREATE INDEX IF NOT EXISTS idx_response_inspection_policies_protocol_priority ON response_inspection_policies(protocol_code, priority, updated_at DESC, id);
    CREATE INDEX IF NOT EXISTS idx_response_inspection_policies_scope_priority ON response_inspection_policies(protocol_code, scope_type, provider_code, priority, updated_at DESC, id);
  `)
}

function ensureAuthorizationInstanceIndexes(database: DatabaseSync): void {
  database.exec(`
    CREATE INDEX IF NOT EXISTS idx_accounts_authorization_instance_authorization ON accounts(authorization_instance_authorization_id);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_authorization_instance_active_unique
      ON accounts(authorization_instance_authorization_id)
      WHERE authorization_instance_authorization_id IS NOT NULL AND deleted_at IS NULL;
    CREATE INDEX IF NOT EXISTS idx_accounts_authorization_instance_source ON accounts(authorization_instance_source_account_id);
    CREATE INDEX IF NOT EXISTS idx_accounts_authorization_instance_source_owner_lookup
      ON accounts(authorization_instance_source_account_id, system_account_id, id)
      WHERE deleted_at IS NULL;
    CREATE INDEX IF NOT EXISTS idx_accounts_deleted_cleanup
      ON accounts(deleted_at ASC, updated_at ASC, id ASC)
      WHERE deleted_at IS NOT NULL;
    CREATE INDEX IF NOT EXISTS idx_group_accounts_dispatch_priority ON group_accounts(group_id, enabled, local_fallback_enabled, local_super_priority_enabled, local_priority, created_at, account_id);
    CREATE INDEX IF NOT EXISTS idx_group_accounts_dispatch_candidate_window
      ON group_accounts(group_id, system_account_id, enabled, local_fallback_enabled ASC, local_super_priority_enabled DESC, local_priority ASC, created_at ASC, account_id ASC);
  `)
}
