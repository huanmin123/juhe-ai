// Code generated from the Node storage schema sources listed below. The DDL
// statements are byte-for-byte ports of the corresponding Node
// database.exec template literals, executed in the Node call order. Do not
// hand-edit the DDL constants; regenerate or re-verify against the Node
// sources when they change.
//
// Node sources (juhe-ai backend/src/storage/schema):
//   - business-schema.ts             applyBusinessSchema
//   - stats-schema.ts                applyStatsSchema
//   - chat-schema.ts                 applyChatSchema
//   - codex-context-state-schema.ts  applyCodexContextStateSchema
//   - dataset-schema.ts              applyDatasetSchema
//   - usage-catalog-schema.ts        applyUsageCatalogSchema
//
// Porting rules:
//   - PRAGMA foreign_keys = ON is kept in the same position as in Node. Note
//     that it is a per-connection setting: callers that need foreign key
//     enforcement across a connection pool must configure it per connection
//     (for example by capping the pool to one connection).
//   - PRAGMA journal_mode = WAL is intentionally skipped; journal
//     configuration belongs to the caller and is meaningless for in-memory
//     databases.
//   - The stats source keeps the J3b model-integrity DDL inside a SQL block
//     comment ("J3b ownership moved to Gateway; its schemas must not be
//     created by Node"). The comment is preserved verbatim, those statements
//     are not executed and are excluded from SchemaCounts.
//   - Node-only conditional runtime migrations are NOT ported. They are
//     no-ops on a database created from the DDL below (guarded by
//     PRAGMA table_info / sqlite_master lookups) and require human review
//     before legacy-upgrade support is added to Go. BUG-0167/0168 review
//     verdict (2026-09-04, verified against business-schema.ts): every guard
//     is a legacy-upgrade path that exits before writing on a fresh database,
//     so porting stays unnecessary for the fresh dual-mode acceptance:
//       * ensureAccountHealthCheckEndpointModeSchema (business-schema.ts):
//         full accounts table rebuild only when the sqlite_master CHECK
//         constraint lacks 'images_json'; the DDL below already declares
//         images_json inside the health_check_endpoint_mode CHECK, so the
//         guard returns before any write.
//       * ensureSystemAccountRequestLimitsSchema,
//         ensureSystemAccountAiAccountLimitSchema,
//         ensureAccountTestTaskQueuedDeadlineSchema: ALTER TABLE guards that
//         return early when PRAGMA table_info shows the column (or an empty
//         table); request_limits_json / ai_account_limit / queued_deadline_at
//         all exist in the CREATE TABLE statements below.
//       * ensureAccountCircuitControlPlaneSchema: conditional ALTER TABLE ADD
//         COLUMN guards that run only when account_circuit_incidents exists
//         without the confirmation columns (the DDL below creates them);
//         its unconditional CREATE UNIQUE INDEX is ported below.
//       * ensureOidcProviderSchema: conditional
//         ALTER TABLE oauth_clients ADD COLUMN client_secret_ciphertext guard
//         (the column already exists in the CREATE TABLE statement).

package schema

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

const sqliteBusinessMainDDL = `    CREATE TABLE IF NOT EXISTS system_accounts (
      id TEXT PRIMARY KEY,
      username TEXT NOT NULL UNIQUE,
      display_name TEXT NOT NULL,
      description TEXT,
      role TEXT NOT NULL DEFAULT 'user',
      status TEXT NOT NULL DEFAULT 'active',
      password_hash TEXT NOT NULL,
      must_change_password INTEGER NOT NULL DEFAULT 0,
      image_generation_enabled INTEGER NOT NULL DEFAULT 0,
      ai_account_limit INTEGER CHECK (ai_account_limit BETWEEN 0 AND 1000000),
      request_limits_json TEXT,
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
      cache_storage_usd_per_1m_per_hour REAL,
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
      cache_storage_usd_per_1m_per_hour REAL,
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

    -- J3a Go is the sole owner of the fenced business projection, durable
    -- disposition receipts and monotonic jobs-store cursor. Node has no
    -- scheduler, outcome reader/projector, or proxy-latency business writer.
    CREATE TABLE IF NOT EXISTS proxy_latency_projection_receipts (
      outcome_id TEXT PRIMARY KEY,
      proxy_id TEXT NOT NULL,
      input_version INTEGER NOT NULL CHECK (input_version >= 1),
      disposition TEXT NOT NULL CHECK (disposition IN ('applied', 'stale', 'ignored', 'rejected')),
      reason TEXT,
      applied_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS proxy_latency_projection_cursors (
      consumer_key TEXT PRIMARY KEY,
      stored_at TEXT,
      outcome_id TEXT,
      updated_at TEXT NOT NULL,
      CHECK ((stored_at IS NULL AND outcome_id IS NULL) OR (stored_at IS NOT NULL AND outcome_id IS NOT NULL))
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
      dispatch_revision INTEGER NOT NULL DEFAULT 1 CHECK (dispatch_revision >= 1),
      circuit_projection_revision INTEGER NOT NULL DEFAULT 0 CHECK (circuit_projection_revision >= 0 AND circuit_projection_revision <= dispatch_revision),
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
      concurrency_limit INTEGER NOT NULL DEFAULT 5000,
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
      cooldown_retest_generation TEXT,
      cooldown_retest_last_at TEXT,
      cooldown_retest_last_status_code INTEGER,
      temporary_unavailable_continuous_probe_enabled INTEGER NOT NULL DEFAULT 1 CHECK (temporary_unavailable_continuous_probe_enabled IN (0, 1)),
      health_check_model TEXT NOT NULL,
      health_check_endpoint_mode TEXT NOT NULL CHECK (health_check_endpoint_mode IN ('images_json', 'chat_json', 'chat_sse', 'responses_json', 'responses_sse', 'messages_json', 'messages_sse', 'generate_content_json', 'generate_content_sse', 'interactions_json', 'interactions_sse')),
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

    CREATE TABLE IF NOT EXISTS account_lock_states (
      account_id TEXT PRIMARY KEY,
      enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
      lock_state TEXT NOT NULL DEFAULT 'UNLOCKED' CHECK (lock_state IN ('UNLOCKED', 'LOCKED_IDLE', 'ENGAGED', 'DEAD_CONFIRMED')),
      lock_death_timeout_seconds INTEGER NOT NULL DEFAULT 300 CHECK (lock_death_timeout_seconds BETWEEN 30 AND 3600),
      lock_retry_interval_seconds INTEGER NOT NULL DEFAULT 5 CHECK (lock_retry_interval_seconds BETWEEN 5 AND 30),
      incident_id TEXT,
      generation INTEGER NOT NULL DEFAULT 0 CHECK (generation >= 0),
      incident_started_at TEXT,
      deadline_at TEXT,
      original_status TEXT,
      provenance TEXT,
      next_retry_at_ms INTEGER,
      lease_id TEXT,
      lease_until_ms INTEGER,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      CHECK ((lock_state = 'UNLOCKED' AND enabled = 0) OR (lock_state <> 'UNLOCKED' AND enabled = 1))
    );
    CREATE INDEX IF NOT EXISTS idx_account_lock_states_deadline ON account_lock_states(lock_state, deadline_at);

    CREATE TABLE IF NOT EXISTS model_quality_policies (
      system_account_id TEXT PRIMARY KEY,
      revision INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
      profile TEXT NOT NULL DEFAULT 'quick' CHECK (profile IN ('quick', 'full')),
      manual_enforcement_enabled INTEGER NOT NULL DEFAULT 1 CHECK (manual_enforcement_enabled IN (0, 1)),
      penalty_threshold INTEGER NOT NULL DEFAULT 70 CHECK (penalty_threshold BETWEEN 40 AND 100),
      penalty_action TEXT NOT NULL DEFAULT 'fallback' CHECK (penalty_action IN ('disable', 'fallback', 'quality_isolate')),
      recovery_interval_minutes INTEGER NOT NULL DEFAULT 10 CHECK (recovery_interval_minutes BETWEEN 10 AND 10080),
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS model_quality_schedules (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      account_id TEXT NOT NULL,
      model TEXT NOT NULL,
      interval_minutes INTEGER NOT NULL DEFAULT 60 CHECK (interval_minutes BETWEEN 10 AND 10080),
      profile TEXT NOT NULL DEFAULT 'quick' CHECK (profile IN ('quick', 'full')),
      penalty_threshold INTEGER NOT NULL DEFAULT 70 CHECK (penalty_threshold BETWEEN 40 AND 100),
      penalty_action TEXT NOT NULL DEFAULT 'fallback' CHECK (penalty_action IN ('disable', 'fallback', 'quality_isolate')),
      recovery_interval_minutes INTEGER NOT NULL DEFAULT 10 CHECK (recovery_interval_minutes BETWEEN 10 AND 10080),
      enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
      revision INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
      next_run_at TEXT NOT NULL,
      last_run_id TEXT,
      last_run_at TEXT,
      last_run_status TEXT CHECK (last_run_status IS NULL OR last_run_status IN ('completed', 'failed', 'canceled')),
      lease_owner TEXT,
      lease_until TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      UNIQUE (system_account_id, account_id)
    );

    CREATE TABLE IF NOT EXISTS account_quality_enforcements (
      account_id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      enforcement_id TEXT NOT NULL UNIQUE,
      generation INTEGER NOT NULL DEFAULT 1 CHECK (generation >= 1),
      state TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'cleared')),
      action TEXT NOT NULL CHECK (action IN ('disable', 'fallback', 'quality_isolate')),
      trigger_run_id TEXT NOT NULL,
      config_source TEXT NOT NULL DEFAULT 'manual' CHECK (config_source IN ('manual', 'schedule')),
      config_source_id TEXT,
      policy_revision INTEGER NOT NULL CHECK (policy_revision >= 0),
      profile TEXT NOT NULL DEFAULT 'quick' CHECK (profile IN ('quick', 'full')),
      penalty_threshold INTEGER NOT NULL DEFAULT 70 CHECK (penalty_threshold BETWEEN 40 AND 100),
      recovery_interval_minutes INTEGER NOT NULL DEFAULT 10 CHECK (recovery_interval_minutes BETWEEN 10 AND 10080),
      recovery_model TEXT,
      account_config_revision INTEGER NOT NULL CHECK (account_config_revision >= 1),
      before_status TEXT NOT NULL,
      after_status TEXT NOT NULL,
      fallback_was_enabled INTEGER NOT NULL DEFAULT 0 CHECK (fallback_was_enabled IN (0, 1)),
      super_priority_was_enabled INTEGER NOT NULL DEFAULT 0 CHECK (super_priority_was_enabled IN (0, 1)),
      started_at TEXT NOT NULL,
      recovery_due_at TEXT,
      recovery_lease_owner TEXT,
      recovery_lease_until TEXT,
      last_recovery_run_id TEXT,
      cleared_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      CHECK (
        (config_source = 'manual' AND config_source_id IS NULL)
        OR
        (config_source = 'schedule' AND config_source_id IS NOT NULL AND length(trim(config_source_id)) > 0)
      ),
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS account_circuit_incidents (
      circuit_scope_key TEXT PRIMARY KEY,
      account_id TEXT NOT NULL,
      account_runtime_key TEXT NOT NULL,
      scope_kind TEXT NOT NULL CHECK (scope_kind IN ('account', 'key', 'protocol_model', 'key_model')),
      key_fingerprint TEXT,
      protocol_code TEXT,
      request_lane TEXT,
      model_family TEXT,
      client_model TEXT,
      capability_hash TEXT,
      credential_source_account_id TEXT,
      client_endpoint_family TEXT,
      final_upstream_model TEXT,
      upstream_endpoint_mode TEXT,
      incident_id TEXT NOT NULL,
      parent_incident_id TEXT,
      child_incident_ids_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(child_incident_ids_json) AND json_type(child_incident_ids_json) = 'array'),
      caused_by_terminal_outcome_id TEXT,
      state TEXT NOT NULL CHECK (state IN ('CLOSED', 'SUSPECT', 'OPEN', 'HALF_OPEN', 'RECOVERING', 'PERSISTING', 'SHADOWED_BY_PERSISTENT')),
      failure_scope TEXT CHECK (failure_scope IN ('account', 'key', 'protocol_model', 'key_model')),
      generation INTEGER NOT NULL CHECK (generation >= 0),
      dispatch_revision INTEGER NOT NULL CHECK (dispatch_revision >= 1),
      ledger_revision INTEGER NOT NULL CHECK (ledger_revision >= 1),
      projected_ledger_revision INTEGER NOT NULL DEFAULT 0 CHECK (projected_ledger_revision >= 0 AND projected_ledger_revision <= ledger_revision),
      transition_id TEXT NOT NULL,
      cooldown_observation_generation INTEGER NOT NULL DEFAULT 0 CHECK (cooldown_observation_generation >= 0),
      open_until_ms INTEGER,
      next_transition_at_ms INTEGER,
      lease_id TEXT,
      lease_purpose TEXT CHECK (lease_purpose IN ('confirmation', 'half_open', 'recovery', 'cooldown_retest', 'background_probe')),
      lease_owner_run_id TEXT,
      lease_until_ms INTEGER,
      attempt_started_at_ms INTEGER,
      attempt_hard_deadline_ms INTEGER,
      upstream_attempt_observed INTEGER NOT NULL DEFAULT 0 CHECK (upstream_attempt_observed IN (0, 1)),
      backoff_level INTEGER NOT NULL DEFAULT 0 CHECK (backoff_level >= 0),
      consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
      confirmation_failures_required INTEGER NOT NULL DEFAULT 1 CHECK (confirmation_failures_required BETWEEN 1 AND 5),
      confirmation_failure_evidence_keys_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(confirmation_failure_evidence_keys_json) AND json_type(confirmation_failure_evidence_keys_json) = 'array'),
      recovering_successes INTEGER NOT NULL DEFAULT 0 CHECK (recovering_successes >= 0),
      last_failure_class TEXT CHECK (last_failure_class IN ('connect_failed', 'timeout_before_complete', 'read_interrupted', 'incomplete_response', 'explicit_policy')),
      retained_until_ms INTEGER,
      created_at_ms INTEGER NOT NULL,
      updated_at_ms INTEGER NOT NULL,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      CHECK (length(circuit_scope_key) BETWEEN 1 AND 2048),
      CHECK (length(account_runtime_key) BETWEEN 1 AND 1024),
      CHECK (length(incident_id) BETWEEN 1 AND 256),
      CHECK (length(transition_id) BETWEEN 1 AND 256),
      CHECK (consecutive_failures <= confirmation_failures_required),
      CHECK (json_array_length(confirmation_failure_evidence_keys_json) <= confirmation_failures_required + 1),
      CHECK ((scope_kind = 'account' AND key_fingerprint IS NULL AND protocol_code IS NULL AND request_lane IS NULL AND model_family IS NULL AND client_model IS NULL AND capability_hash IS NULL AND credential_source_account_id IS NULL AND client_endpoint_family IS NULL AND final_upstream_model IS NULL AND upstream_endpoint_mode IS NULL)
        OR (scope_kind = 'key' AND key_fingerprint IS NOT NULL AND protocol_code IS NULL AND request_lane IS NULL AND model_family IS NULL AND client_model IS NULL AND capability_hash IS NULL AND credential_source_account_id IS NULL AND client_endpoint_family IS NULL AND final_upstream_model IS NULL AND upstream_endpoint_mode IS NULL)
        OR (scope_kind = 'protocol_model' AND key_fingerprint IS NULL AND protocol_code IS NOT NULL AND request_lane IS NOT NULL AND model_family IS NOT NULL AND client_model IS NULL AND capability_hash IS NULL AND credential_source_account_id IS NULL AND client_endpoint_family IS NULL AND final_upstream_model IS NULL AND upstream_endpoint_mode IS NULL)
        OR (scope_kind = 'key_model' AND key_fingerprint IS NOT NULL AND capability_hash IS NOT NULL AND client_model IS NOT NULL AND credential_source_account_id IS NOT NULL AND client_endpoint_family IS NOT NULL AND final_upstream_model IS NOT NULL AND upstream_endpoint_mode IS NOT NULL AND protocol_code IS NULL AND request_lane IS NULL AND model_family IS NULL)),
      CHECK ((state = 'CLOSED' AND retained_until_ms IS NOT NULL) OR (state <> 'CLOSED' AND retained_until_ms IS NULL))
    );

    CREATE TABLE IF NOT EXISTS account_circuit_outbox (
      event_id TEXT PRIMARY KEY,
      projection_key TEXT NOT NULL,
      dedupe_key TEXT NOT NULL,
      event_type TEXT NOT NULL CHECK (event_type IN ('dispatch_revision_changed', 'incident_changed')),
      account_id TEXT NOT NULL,
      account_runtime_key TEXT NOT NULL,
      circuit_scope_key TEXT,
      incident_id TEXT,
      transition_id TEXT NOT NULL,
      dispatch_revision INTEGER NOT NULL CHECK (dispatch_revision >= 1),
      generation INTEGER,
      ledger_revision INTEGER,
      status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'dispatched')),
      available_at_ms INTEGER NOT NULL,
      claim_token TEXT,
      claimed_by TEXT,
      claim_until_ms INTEGER,
      attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
      last_error_class TEXT,
      acknowledged_at_ms INTEGER,
      created_at_ms INTEGER NOT NULL,
      updated_at_ms INTEGER NOT NULL,
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
      probe_claim_token TEXT,
      probe_claimed_until TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS account_api_key_pool_probe_cursors (
      account_id TEXT NOT NULL,
      purpose TEXT NOT NULL CHECK (purpose IN ('health_check', 'cooldown_retest')),
      last_completed_key_fingerprint TEXT,
      key_set_fingerprint TEXT NOT NULL,
      config_revision INTEGER NOT NULL,
      dispatch_revision INTEGER,
      cooldown_generation TEXT,
      source_config_revision INTEGER,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (account_id, purpose),
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
    );

    -- A snapshot epoch is independent from config/dispatch revisions. It
    -- prevents an already-running Go probe from projecting after a later
    -- tombstone or eligibility-only snapshot has been published.
    CREATE TABLE IF NOT EXISTS account_health_jobs_input_versions (
      account_id TEXT PRIMARY KEY,
      current_version INTEGER NOT NULL CHECK (current_version >= 1),
      reserved_at TEXT NOT NULL
    );

    -- This is an intent outbox, not a task queue. Business mutations reserve
    -- the epoch and write an intent in the same DB-service transaction; a
    -- separate Node input producer may publish the signed immutable snapshot
    -- only after that transaction commits. It never stores credentials.
    CREATE TABLE IF NOT EXISTS account_health_jobs_input_outbox (
      event_id TEXT PRIMARY KEY,
      account_id TEXT NOT NULL,
      input_version INTEGER NOT NULL CHECK (input_version >= 1),
      event_kind TEXT NOT NULL CHECK (event_kind IN ('snapshot', 'tombstone')),
      reason TEXT NOT NULL,
      config_revision INTEGER NOT NULL CHECK (config_revision >= 1),
      dispatch_revision INTEGER NOT NULL CHECK (dispatch_revision >= 1),
      status TEXT NOT NULL CHECK (status IN ('pending', 'leased', 'published', 'failed', 'superseded')),
      claim_token TEXT,
      claimed_until TEXT,
      attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
      available_at TEXT NOT NULL,
      last_error TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      UNIQUE (account_id, input_version),
      CHECK ((status = 'leased' AND claim_token IS NOT NULL AND claimed_until IS NOT NULL) OR (status <> 'leased' AND claim_token IS NULL AND claimed_until IS NULL))
    );

    CREATE INDEX IF NOT EXISTS idx_account_health_jobs_input_outbox_pending
      ON account_health_jobs_input_outbox(status, available_at, created_at, event_id);
    CREATE INDEX IF NOT EXISTS idx_account_health_jobs_input_outbox_account
      ON account_health_jobs_input_outbox(account_id, input_version DESC);

    -- Go jobs owns J1 outcomes; Node DB-service owns only these receipts and
    -- the fenced projection into accounts. A receipt makes replays harmless
    -- without turning Node into a scheduler or second outcome writer.
    CREATE TABLE IF NOT EXISTS account_health_projection_receipts (
      outcome_id TEXT PRIMARY KEY,
      account_id TEXT NOT NULL,
      input_version INTEGER NOT NULL CHECK (input_version >= 1),
      disposition TEXT NOT NULL CHECK (disposition IN ('applied', 'stale', 'ignored', 'rejected')),
      reason TEXT,
      applied_at TEXT NOT NULL,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
    );

    -- Every J1 consumer owns its own monotonic cursor. The tuple cursor keeps
    -- outcomes with the same observed_at visible to projector and Gateway
    -- consumers without making either process an outcome writer.
    CREATE TABLE IF NOT EXISTS account_health_projection_cursors (
      consumer_key TEXT PRIMARY KEY,
      observed_at TEXT,
      outcome_id TEXT,
      updated_at TEXT NOT NULL,
      CHECK ((observed_at IS NULL AND outcome_id IS NULL) OR (observed_at IS NOT NULL AND outcome_id IS NOT NULL))
    );

    -- J2 keeps its own durable consumer cursor; it must never share J1's
    -- state or advance a jobs-store outcome cursor itself.
    CREATE TABLE IF NOT EXISTS account_balance_projection_cursors (
      consumer_key TEXT PRIMARY KEY,
      observed_at TEXT,
      outcome_id TEXT,
      updated_at TEXT NOT NULL,
      CHECK ((observed_at IS NULL AND outcome_id IS NULL) OR (observed_at IS NOT NULL AND outcome_id IS NOT NULL))
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

    -- The management-list projection moves availability filtering and paging off
    -- the request path. SQLite keeps the schema for contract parity; only the
    -- PostgreSQL performance path will consume it after migration cutover.
    CREATE TABLE IF NOT EXISTS account_list_availability_projections (
      viewer_system_account_id TEXT NOT NULL,
      account_id TEXT NOT NULL,
      source_account_id TEXT,
      authorization_id TEXT,
      effective_status TEXT NOT NULL,
      schedulable_bucket TEXT NOT NULL CHECK (schedulable_bucket IN ('enabled', 'disabled', 'cooling')),
      provider_code TEXT NOT NULL,
      provider_protocol_profile_id TEXT NOT NULL,
      account_type TEXT NOT NULL,
      bound_group_id TEXT,
      name_sort_key TEXT NOT NULL,
      priority_sort_key INTEGER NOT NULL,
      super_priority_sort_key INTEGER NOT NULL,
      fallback_sort_key INTEGER NOT NULL,
      concurrency_sort_key INTEGER NOT NULL,
      account_expires_at_sort_key TEXT,
      last_used_at_sort_key TEXT,
      created_at_sort_key TEXT NOT NULL,
      payload_json TEXT NOT NULL,
      source_generation INTEGER NOT NULL CHECK (source_generation >= 1),
      next_transition_at TEXT,
      projected_at TEXT NOT NULL,
      PRIMARY KEY (viewer_system_account_id, account_id),
      FOREIGN KEY (viewer_system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (source_account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (authorization_id) REFERENCES resource_authorizations(id) ON DELETE CASCADE,
      FOREIGN KEY (provider_protocol_profile_id) REFERENCES provider_protocol_profiles(id)
    );

    -- Keeps only predicate and ordering fields. Request-time paging must not
    -- scan the wide JSON payload table before it has narrowed to one page.
    -- It is fenced and refreshed in the same transaction as the payload row.
    CREATE TABLE IF NOT EXISTS account_list_availability_projection_index (
      viewer_system_account_id TEXT NOT NULL,
      account_id TEXT NOT NULL,
      effective_status TEXT NOT NULL,
      schedulable_bucket TEXT NOT NULL CHECK (schedulable_bucket IN ('enabled', 'disabled', 'cooling')),
      provider_code TEXT NOT NULL,
      provider_protocol_profile_id TEXT NOT NULL,
      account_type TEXT NOT NULL,
      bound_group_id TEXT,
      name_sort_key TEXT NOT NULL,
      priority_sort_key INTEGER NOT NULL,
      super_priority_sort_key INTEGER NOT NULL,
      fallback_sort_key INTEGER NOT NULL,
      concurrency_sort_key INTEGER NOT NULL,
      account_expires_at_sort_key TEXT,
      last_used_at_sort_key TEXT,
      created_at_sort_key TEXT NOT NULL,
      access_type_sort_key TEXT NOT NULL,
      search_index_complete INTEGER NOT NULL DEFAULT 0 CHECK (search_index_complete IN (0, 1)),
      authorization_quota_exceeded INTEGER NOT NULL DEFAULT 0 CHECK (authorization_quota_exceeded IN (0, 1)),
      PRIMARY KEY (viewer_system_account_id, account_id),
      FOREIGN KEY (viewer_system_account_id, account_id)
        REFERENCES account_list_availability_projections(viewer_system_account_id, account_id)
        ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS account_list_availability_projection_tags (
      viewer_system_account_id TEXT NOT NULL,
      account_id TEXT NOT NULL,
      tag_id TEXT NOT NULL,
      PRIMARY KEY (viewer_system_account_id, account_id, tag_id),
      FOREIGN KEY (viewer_system_account_id, account_id)
        REFERENCES account_list_availability_projections(viewer_system_account_id, account_id)
        ON DELETE CASCADE,
      FOREIGN KEY (tag_id) REFERENCES account_tags(id) ON DELETE CASCADE
    );

    -- Mirrors only terms backed by a completed search document. This keeps
    -- indexed keyword semantics on the projection read path.
    CREATE TABLE IF NOT EXISTS account_list_availability_projection_search_terms (
      viewer_system_account_id TEXT NOT NULL,
      account_id TEXT NOT NULL,
      term TEXT NOT NULL,
      name_sort_key TEXT NOT NULL,
      created_at_sort_key TEXT NOT NULL,
      PRIMARY KEY (viewer_system_account_id, account_id, term),
      FOREIGN KEY (viewer_system_account_id, account_id)
        REFERENCES account_list_availability_projections(viewer_system_account_id, account_id)
        ON DELETE CASCADE
    );

    -- One row per visible viewer keeps request freshness checks O(1). The
    -- materializer sets is_current only after it has atomically refreshed the
    -- aggregate from all rows for that viewer.
    CREATE TABLE IF NOT EXISTS account_list_availability_projection_viewer_health (
      viewer_system_account_id TEXT PRIMARY KEY,
      projection_count INTEGER NOT NULL CHECK (projection_count >= 0),
      oldest_projected_at TEXT,
      next_transition_at TEXT,
      is_current INTEGER NOT NULL CHECK (is_current IN (0, 1)),
      updated_at TEXT NOT NULL,
      FOREIGN KEY (viewer_system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    );

    -- Request reads are PostgreSQL-only. Redis remains the source for short-
    -- lived runtime state, while this durable overlay carries the last
    -- reconciled concurrency value into the one-query list response.
    CREATE TABLE IF NOT EXISTS account_list_availability_runtime_overlays (
      account_id TEXT PRIMARY KEY,
      current_concurrency INTEGER NOT NULL CHECK (current_concurrency >= 0),
      observed_at TEXT NOT NULL,
      next_reconcile_at TEXT,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
    );

    -- A missing or unhealthy dependency row is intentionally fail-closed.
    -- Recovery remains unavailable until the worker has replayed every dirty
    -- list row, so a Redis outage can never publish an old availability view.
    CREATE TABLE IF NOT EXISTS account_list_availability_projection_dependency_health (
      dependency_name TEXT PRIMARY KEY CHECK (dependency_name = 'runtime_state'),
      state TEXT NOT NULL CHECK (state IN ('healthy', 'unavailable', 'recovering')),
      generation INTEGER NOT NULL CHECK (generation >= 1),
      reason TEXT,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS account_list_availability_dirty (
      account_id TEXT PRIMARY KEY,
      viewer_system_account_id TEXT NOT NULL,
      generation INTEGER NOT NULL CHECK (generation >= 1),
      applied_generation INTEGER NOT NULL DEFAULT 0 CHECK (applied_generation >= 0 AND applied_generation <= generation),
      reason TEXT NOT NULL,
      available_at_ms INTEGER NOT NULL,
      claim_token TEXT,
      claimed_by TEXT,
      claim_until_ms INTEGER,
      attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
      created_at_ms INTEGER NOT NULL,
      updated_at_ms INTEGER NOT NULL,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (viewer_system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      CHECK (
        (claim_token IS NULL AND claimed_by IS NULL AND claim_until_ms IS NULL)
        OR (claim_token IS NOT NULL AND claimed_by IS NOT NULL AND claim_until_ms IS NOT NULL)
      )
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
      queued_deadline_at TEXT,
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
      purpose TEXT NOT NULL DEFAULT 'general' CHECK (purpose IN ('general', 'chat')),
      expires_at TEXT,
      quota_limits_json TEXT,
      availability_schedule_json TEXT,
      availability_schedule_next_check_at TEXT,
      last_used_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (route_strategy_id) REFERENCES route_strategies(id)
    );

    CREATE TABLE IF NOT EXISTS request_quota_hourly_window_scope_bindings (
      system_account_id TEXT NOT NULL,
      scope_type TEXT NOT NULL,
      scope_id TEXT NOT NULL,
      source_type TEXT NOT NULL,
      source_id TEXT NOT NULL,
      window_hours INTEGER NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      CHECK (window_hours BETWEEN 1 AND 720),
      CHECK (scope_type IN ('api_key', 'account_authorization', 'group_authorization', 'account_authorization_team', 'group_authorization_team')),
      CHECK (source_type IN ('api_key', 'resource_authorization_grant')),
      PRIMARY KEY (system_account_id, scope_type, scope_id),
      UNIQUE (source_type, source_id, system_account_id, scope_type, scope_id)
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
    CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_priority
      ON account_list_availability_projections(
        viewer_system_account_id,
        priority_sort_key ASC,
        created_at_sort_key ASC,
        account_id ASC
      );
    CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_name
      ON account_list_availability_projections(
        viewer_system_account_id,
        name_sort_key ASC,
        created_at_sort_key ASC,
        account_id ASC
      );
    CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_schedulable_priority
      ON account_list_availability_projections(
        viewer_system_account_id,
        schedulable_bucket,
        priority_sort_key ASC,
        created_at_sort_key ASC,
        account_id ASC
      );
    CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_due
      ON account_list_availability_projections(next_transition_at ASC, viewer_system_account_id, account_id)
      WHERE next_transition_at IS NOT NULL;
    CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_tags_lookup
      ON account_list_availability_projection_tags(viewer_system_account_id, tag_id, account_id);
    CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_search_terms_lookup
      ON account_list_availability_projection_search_terms(viewer_system_account_id, term, account_id);
    CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_search_terms_name_order
      ON account_list_availability_projection_search_terms(
        viewer_system_account_id,
        term,
        name_sort_key ASC,
        created_at_sort_key ASC,
        account_id ASC
      );
    CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_index_priority
      ON account_list_availability_projection_index(
        viewer_system_account_id,
        priority_sort_key ASC,
        created_at_sort_key ASC,
        account_id ASC
      );
    CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_index_name
      ON account_list_availability_projection_index(
        viewer_system_account_id,
        name_sort_key ASC,
        created_at_sort_key ASC,
        account_id ASC
      );
    CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_index_name_search_incomplete
      ON account_list_availability_projection_index(
        viewer_system_account_id,
        name_sort_key ASC,
        created_at_sort_key ASC,
        account_id ASC
      )
      WHERE search_index_complete = 0;
    CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_index_schedulable_priority
      ON account_list_availability_projection_index(
        viewer_system_account_id,
        schedulable_bucket,
        priority_sort_key ASC,
        created_at_sort_key ASC,
        account_id ASC
      );
    CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_viewer_health_refresh
      ON account_list_availability_projection_viewer_health(is_current, updated_at ASC, viewer_system_account_id ASC);
    CREATE INDEX IF NOT EXISTS idx_account_list_availability_dirty_claim
      ON account_list_availability_dirty(available_at_ms ASC, created_at_ms ASC, account_id ASC);
    CREATE INDEX IF NOT EXISTS idx_account_list_availability_dirty_viewer
      ON account_list_availability_dirty(viewer_system_account_id, available_at_ms ASC, account_id ASC);
    CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_viewer_projected
      ON account_list_availability_projections(viewer_system_account_id, projected_at ASC, account_id ASC);
    CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_viewer_transition
      ON account_list_availability_projections(viewer_system_account_id, next_transition_at ASC, account_id ASC)
      WHERE next_transition_at IS NOT NULL;
    CREATE INDEX IF NOT EXISTS idx_account_list_availability_runtime_overlay_due
      ON account_list_availability_runtime_overlays(next_reconcile_at ASC, account_id ASC)
      WHERE next_reconcile_at IS NOT NULL;
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
    CREATE INDEX IF NOT EXISTS idx_accounts_health_monitor_order
      ON accounts((last_used_at IS NULL) ASC, last_used_at DESC, name ASC, id ASC)
      WHERE deleted_at IS NULL;
    CREATE INDEX IF NOT EXISTS idx_accounts_owner_health_monitor_order
      ON accounts(system_account_id, (last_used_at IS NULL) ASC, last_used_at DESC, name ASC, id ASC)
      WHERE deleted_at IS NULL;
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
    CREATE INDEX IF NOT EXISTS idx_model_quality_schedules_due
      ON model_quality_schedules(enabled, next_run_at, id);
    CREATE INDEX IF NOT EXISTS idx_model_quality_schedules_scope
      ON model_quality_schedules(system_account_id, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_account_quality_enforcements_recovery
      ON account_quality_enforcements(state, action, recovery_due_at, account_id);
    CREATE INDEX IF NOT EXISTS idx_account_quality_enforcements_scope
      ON account_quality_enforcements(system_account_id, updated_at DESC, account_id);
    CREATE INDEX IF NOT EXISTS idx_accounts_cooldown_retest_candidate_order
      ON accounts(cooldown_until ASC, priority ASC, created_at ASC, id ASC, health_check_endpoint_mode)
      WHERE deleted_at IS NULL
        AND cooldown_until IS NOT NULL
        AND schedulable = 1
        AND type IN ('api_key', 'oauth', 'google_oauth')
        AND status IN ('temporary_unavailable', 'rate_limited');
    CREATE INDEX IF NOT EXISTS idx_accounts_cooldown_retest_legacy_repair_order
      ON accounts(cooldown_until ASC, priority ASC, created_at ASC, id ASC)
      WHERE deleted_at IS NULL
        AND cooldown_until IS NOT NULL
        AND type IN ('api_key', 'oauth', 'google_oauth')
        AND status IN ('temporary_unavailable', 'rate_limited');
    CREATE INDEX IF NOT EXISTS idx_accounts_deleted_cleanup
      ON accounts(deleted_at ASC, updated_at ASC, id ASC)
      WHERE deleted_at IS NOT NULL;
    CREATE INDEX IF NOT EXISTS idx_account_health_projection_receipts_account
      ON account_health_projection_receipts(account_id, applied_at DESC, outcome_id DESC);
    CREATE INDEX IF NOT EXISTS idx_account_health_projection_cursors_updated
      ON account_health_projection_cursors(updated_at ASC, consumer_key ASC);
    CREATE INDEX IF NOT EXISTS idx_account_health_jobs_input_versions_reserved
      ON account_health_jobs_input_versions(reserved_at ASC, account_id ASC);
    CREATE INDEX IF NOT EXISTS idx_accounts_balance_query_due
      ON accounts(balance_query_next_refresh_at ASC, id ASC)
      WHERE balance_query_enabled = 1
        AND deleted_at IS NULL
        AND authorization_instance_authorization_id IS NULL;
    CREATE INDEX IF NOT EXISTS idx_accounts_balance_auto_detect_due
      ON accounts(balance_query_next_refresh_at ASC, id ASC)
      WHERE status = 'active'
        AND schedulable = 1
        AND type = 'api_key'
        AND balance_query_enabled = 0
        AND balance_query_config_json = '{}'
        AND deleted_at IS NULL
        AND authorization_instance_authorization_id IS NULL;
    CREATE UNIQUE INDEX IF NOT EXISTS idx_account_api_key_runtime_unique
      ON account_api_key_runtime_states(account_id, key_fingerprint);
    CREATE INDEX IF NOT EXISTS idx_account_api_key_runtime_status
      ON account_api_key_runtime_states(account_id, status, cooldown_until);
    CREATE INDEX IF NOT EXISTS idx_account_api_key_runtime_probe
      ON account_api_key_runtime_states(account_id, status, next_probe_at ASC, updated_at ASC, key_index ASC)
      WHERE next_probe_at IS NOT NULL;
    CREATE INDEX IF NOT EXISTS idx_account_api_key_runtime_probe_claim
      ON account_api_key_runtime_states(status, next_probe_at ASC, probe_claimed_until ASC)
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
    CREATE INDEX IF NOT EXISTS idx_api_keys_route_strategy ON api_keys(route_strategy_id);
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
    CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_chat_purpose_unique
      ON api_keys(system_account_id)
      WHERE purpose = 'chat';
    CREATE INDEX IF NOT EXISTS idx_api_keys_name_lookup ON api_keys(name, id);
    CREATE INDEX IF NOT EXISTS idx_api_keys_system_account_name_lookup ON api_keys(system_account_id, name, id);
    CREATE INDEX IF NOT EXISTS idx_request_quota_hourly_scope_bindings_window
      ON request_quota_hourly_window_scope_bindings(window_hours, system_account_id, scope_type, scope_id);
    CREATE INDEX IF NOT EXISTS idx_request_quota_hourly_scope_bindings_source
      ON request_quota_hourly_window_scope_bindings(source_type, source_id);
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
`
const sqliteBusinessCircuitControlPlaneIndexDDL = `    CREATE UNIQUE INDEX IF NOT EXISTS idx_account_circuit_incidents_key_model_capability
      ON account_circuit_incidents(scope_kind, capability_hash)
      WHERE scope_kind = 'key_model' AND capability_hash IS NOT NULL
`
const sqliteBusinessResponseInspectionPolicyIndexesDDL = `    CREATE INDEX IF NOT EXISTS idx_response_inspection_policies_enabled_priority ON response_inspection_policies(enabled, priority, updated_at DESC, id);
    CREATE INDEX IF NOT EXISTS idx_response_inspection_policies_protocol_priority ON response_inspection_policies(protocol_code, priority, updated_at DESC, id);
    CREATE INDEX IF NOT EXISTS idx_response_inspection_policies_scope_priority ON response_inspection_policies(protocol_code, scope_type, provider_code, priority, updated_at DESC, id);
`
const sqliteBusinessExternalIntegrationSourceIndexesDDL = `    CREATE INDEX IF NOT EXISTS idx_external_integration_sources_status ON external_integration_sources(status, name);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_external_integration_sources_name_unique_lower ON external_integration_sources(lower(name));
`
const sqliteBusinessOIDCProviderDDL = `    CREATE TABLE IF NOT EXISTS oauth_clients (
      id TEXT PRIMARY KEY,
      client_id TEXT NOT NULL UNIQUE,
      display_name TEXT NOT NULL,
      client_type TEXT NOT NULL CHECK (client_type IN ('public', 'confidential')),
      client_secret_hash TEXT,
      client_secret_ciphertext TEXT,
      redirect_uris_json TEXT NOT NULL,
      allowed_scopes_json TEXT NOT NULL,
      status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS oauth_grants (
      id TEXT PRIMARY KEY,
      client_id TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      scopes_json TEXT NOT NULL,
      expires_at TEXT NOT NULL,
      revoked_at TEXT,
      created_at TEXT NOT NULL,
      FOREIGN KEY (client_id) REFERENCES oauth_clients(client_id) ON DELETE CASCADE,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS oauth_authorization_transactions (
      id TEXT PRIMARY KEY,
      client_id TEXT NOT NULL,
      redirect_uri TEXT NOT NULL,
      scopes_json TEXT NOT NULL,
      state_ciphertext TEXT NOT NULL,
      code_challenge TEXT NOT NULL,
      csrf_hash TEXT NOT NULL,
      expires_at TEXT NOT NULL,
      completed_at TEXT,
      created_at TEXT NOT NULL,
      FOREIGN KEY (client_id) REFERENCES oauth_clients(client_id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS oauth_authorization_codes (
      id TEXT PRIMARY KEY,
      code_hash TEXT NOT NULL UNIQUE,
      client_id TEXT NOT NULL,
      grant_id TEXT NOT NULL,
      redirect_uri TEXT NOT NULL,
      code_challenge TEXT NOT NULL,
      expires_at TEXT NOT NULL,
      consumed_at TEXT,
      created_at TEXT NOT NULL,
      FOREIGN KEY (client_id) REFERENCES oauth_clients(client_id) ON DELETE CASCADE,
      FOREIGN KEY (grant_id) REFERENCES oauth_grants(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS oauth_access_tokens (
      id TEXT PRIMARY KEY,
      token_hash TEXT NOT NULL UNIQUE,
      client_id TEXT NOT NULL,
      grant_id TEXT NOT NULL,
      issued_at TEXT NOT NULL,
      expires_at TEXT NOT NULL,
      revoked_at TEXT,
      replaced_at TEXT,
      successor_token_id TEXT,
      created_at TEXT NOT NULL,
      FOREIGN KEY (client_id) REFERENCES oauth_clients(client_id) ON DELETE CASCADE,
      FOREIGN KEY (grant_id) REFERENCES oauth_grants(id) ON DELETE CASCADE,
      FOREIGN KEY (successor_token_id) REFERENCES oauth_access_tokens(id)
    );

    CREATE TABLE IF NOT EXISTS oauth_authorization_code_oidc_contexts (
      code_id TEXT PRIMARY KEY,
      nonce_ciphertext TEXT NOT NULL,
      created_at TEXT NOT NULL,
      FOREIGN KEY (code_id) REFERENCES oauth_authorization_codes(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS oauth_signing_keys (
      id TEXT PRIMARY KEY,
      kid TEXT NOT NULL UNIQUE,
      private_key_ciphertext TEXT NOT NULL,
      public_jwk_json TEXT NOT NULL,
      status TEXT NOT NULL CHECK (status IN ('active', 'retired')),
      created_at TEXT NOT NULL,
      retired_at TEXT
    );

    CREATE TABLE IF NOT EXISTS oauth_device_authorizations (
      id TEXT PRIMARY KEY,
      client_id TEXT NOT NULL,
      device_code_hash TEXT NOT NULL UNIQUE,
      user_code TEXT NOT NULL UNIQUE,
      verification_uri TEXT NOT NULL,
      scopes_json TEXT NOT NULL,
      nonce_ciphertext TEXT,
      expires_at TEXT NOT NULL,
      interval_seconds INTEGER NOT NULL CHECK (interval_seconds BETWEEN 1 AND 60),
      last_polled_at TEXT,
      csrf_hash TEXT,
      status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'denied', 'consumed', 'expired')),
      system_account_id TEXT,
      approved_at TEXT,
      denied_at TEXT,
      consumed_at TEXT,
      created_at TEXT NOT NULL,
      FOREIGN KEY (client_id) REFERENCES oauth_clients(client_id) ON DELETE CASCADE,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    );

    CREATE INDEX IF NOT EXISTS idx_oauth_grants_user_client_active
      ON oauth_grants(system_account_id, client_id, expires_at, revoked_at);
    CREATE INDEX IF NOT EXISTS idx_oauth_authorization_codes_expiry
      ON oauth_authorization_codes(expires_at, consumed_at);
    CREATE INDEX IF NOT EXISTS idx_oauth_authorization_transactions_expiry
      ON oauth_authorization_transactions(expires_at, completed_at);
    CREATE INDEX IF NOT EXISTS idx_oauth_access_tokens_grant_expiry
      ON oauth_access_tokens(grant_id, expires_at, revoked_at, replaced_at);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_oauth_signing_keys_one_active
      ON oauth_signing_keys(status) WHERE status = 'active';
    CREATE INDEX IF NOT EXISTS idx_oauth_device_authorizations_poll
      ON oauth_device_authorizations(device_code_hash, client_id, expires_at, status);
    CREATE INDEX IF NOT EXISTS idx_oauth_device_authorizations_user_code
      ON oauth_device_authorizations(user_code, expires_at, status);
`
const sqliteBusinessAuthorizationInstanceIndexesDDL = `    CREATE INDEX IF NOT EXISTS idx_accounts_authorization_instance_authorization ON accounts(authorization_instance_authorization_id);
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
    CREATE INDEX IF NOT EXISTS idx_account_circuit_incidents_account ON account_circuit_incidents(account_id, updated_at_ms, circuit_scope_key);
    CREATE INDEX IF NOT EXISTS idx_account_circuit_incidents_runtime_state ON account_circuit_incidents(account_runtime_key, state, updated_at_ms, circuit_scope_key);
    CREATE INDEX IF NOT EXISTS idx_account_circuit_incidents_projection_gap
      ON account_circuit_incidents(updated_at_ms, circuit_scope_key)
      WHERE projected_ledger_revision < ledger_revision;
    CREATE INDEX IF NOT EXISTS idx_account_circuit_incidents_closed_cleanup
      ON account_circuit_incidents(retained_until_ms, updated_at_ms, circuit_scope_key)
      WHERE state = 'CLOSED';
    CREATE INDEX IF NOT EXISTS idx_account_circuit_outbox_account ON account_circuit_outbox(account_id, dispatch_revision, created_at_ms, event_id);
    CREATE INDEX IF NOT EXISTS idx_account_circuit_outbox_scope ON account_circuit_outbox(circuit_scope_key, ledger_revision, created_at_ms, event_id)
      WHERE circuit_scope_key IS NOT NULL;
    CREATE INDEX IF NOT EXISTS idx_account_circuit_outbox_claim
      ON account_circuit_outbox(status, available_at_ms, claim_until_ms, created_at_ms, event_id)
      WHERE status IN ('pending', 'processing');
    CREATE INDEX IF NOT EXISTS idx_account_circuit_outbox_ack_cleanup
      ON account_circuit_outbox(acknowledged_at_ms, event_id)
      WHERE status = 'dispatched';
    CREATE INDEX IF NOT EXISTS idx_group_accounts_dispatch_priority ON group_accounts(group_id, enabled, local_fallback_enabled, local_super_priority_enabled, local_priority, created_at, account_id);
    CREATE INDEX IF NOT EXISTS idx_group_accounts_dispatch_candidate_window
      ON group_accounts(group_id, system_account_id, enabled, local_fallback_enabled ASC, local_super_priority_enabled DESC, local_priority ASC, created_at ASC, account_id ASC);
`

const sqliteStatsDirtyIPsDDL = `    CREATE TABLE IF NOT EXISTS client_ip_range_window_dirty_ips (
      ip_hash TEXT PRIMARY KEY,
      generation INTEGER NOT NULL DEFAULT 1,
      first_dirty_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );
    CREATE TABLE IF NOT EXISTS client_ip_account_range_window_dirty_ips (
      ip_hash TEXT PRIMARY KEY,
      generation INTEGER NOT NULL DEFAULT 1,
      first_dirty_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );
`
const sqliteStatsMainDDL = `    CREATE TABLE IF NOT EXISTS account_quality_minute_stats (
          account_id TEXT NOT NULL,
          system_account_id TEXT NOT NULL,
          provider_code TEXT NOT NULL,
          stat_minute TEXT NOT NULL,
          request_count INTEGER NOT NULL DEFAULT 0,
          success_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
          first_token_ms_count INTEGER NOT NULL DEFAULT 0,
          last_sample_at TEXT,
          last_success_at TEXT,
          last_error_at TEXT,
          last_error_message TEXT,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (account_id, stat_minute)
        );

    CREATE TABLE IF NOT EXISTS account_health_hourly (
          account_id TEXT NOT NULL,
          system_account_id TEXT NOT NULL,
          provider_code TEXT NOT NULL,
          stat_hour TEXT NOT NULL,
          status TEXT NOT NULL CHECK (status IN ('success', 'failure')),
          last_observed_at TEXT NOT NULL,
          last_record_id TEXT NOT NULL,
          status_code INTEGER,
          error_code TEXT,
          error_message TEXT,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (account_id, stat_hour)
        );

    CREATE TABLE IF NOT EXISTS group_account_stats (
          system_account_id TEXT NOT NULL,
          group_id TEXT NOT NULL,
          total INTEGER NOT NULL DEFAULT 0,
          available INTEGER NOT NULL DEFAULT 0,
          active INTEGER NOT NULL DEFAULT 0,
          disabled INTEGER NOT NULL DEFAULT 0,
          error INTEGER NOT NULL DEFAULT 0,
          rate_limited INTEGER NOT NULL DEFAULT 0,
          current_concurrency INTEGER NOT NULL DEFAULT 0,
          concurrency_limit INTEGER NOT NULL DEFAULT 0,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, group_id)
        );

    CREATE TABLE IF NOT EXISTS account_quality_scores (
          account_id TEXT PRIMARY KEY,
          system_account_id TEXT NOT NULL,
          provider_code TEXT NOT NULL,
          quality_score INTEGER NOT NULL DEFAULT 1000000,
          quality_state TEXT NOT NULL DEFAULT 'unknown',
          recent_request_count INTEGER NOT NULL DEFAULT 0,
          recent_success_count INTEGER NOT NULL DEFAULT 0,
          recent_error_count INTEGER NOT NULL DEFAULT 0,
          recent_first_token_sample_count INTEGER NOT NULL DEFAULT 0,
          recent_avg_first_token_ms INTEGER,
          ewma_first_token_ms INTEGER,
          success_rate REAL,
          window_started_at TEXT NOT NULL,
          window_ended_at TEXT NOT NULL,
          last_sample_at TEXT,
          last_success_at TEXT,
          last_error_at TEXT,
          last_error_message TEXT,
          updated_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS account_quality_dirty_accounts (
          account_id TEXT PRIMARY KEY,
          first_dirty_at TEXT NOT NULL,
          updated_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS account_usage_snapshots (
          system_account_id TEXT NOT NULL,
          account_id TEXT NOT NULL,
          kind TEXT NOT NULL CHECK (kind IN ('openai_codex', 'relay_balance')),
          source TEXT,
          snapshot_json TEXT NOT NULL,
          refresh_status TEXT,
          last_attempt_at TEXT,
          last_success_at TEXT,
          next_refresh_after TEXT,
          last_error_message TEXT,
          updated_at TEXT NOT NULL,
          created_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, account_id, kind)
        );

    CREATE TABLE IF NOT EXISTS usage_stats_totals (
          system_account_id TEXT NOT NULL,
          scope_type TEXT NOT NULL,
          scope_id TEXT NOT NULL DEFAULT '',
          request_count INTEGER NOT NULL DEFAULT 0,
          success_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          input_tokens INTEGER NOT NULL DEFAULT 0,
          output_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_cost_usd REAL NOT NULL DEFAULT 0,
          cache_write_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_cost_usd REAL NOT NULL DEFAULT 0,
          thinking_tokens INTEGER NOT NULL DEFAULT 0,
          input_image_tokens INTEGER NOT NULL DEFAULT 0,
          output_image_tokens INTEGER NOT NULL DEFAULT 0,
          total_cost_usd REAL NOT NULL DEFAULT 0,
          duration_ms_sum INTEGER NOT NULL DEFAULT 0,
          duration_ms_count INTEGER NOT NULL DEFAULT 0,
          duration_ms_max INTEGER NOT NULL DEFAULT 0,
          first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
          first_token_ms_count INTEGER NOT NULL DEFAULT 0,
          first_token_ms_max INTEGER NOT NULL DEFAULT 0,
          last_used_at TEXT,
          last_error_at TEXT,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id)
        );

    CREATE TABLE IF NOT EXISTS usage_stats_minute (
          system_account_id TEXT NOT NULL,
          scope_type TEXT NOT NULL,
          scope_id TEXT NOT NULL DEFAULT '',
          stat_minute TEXT NOT NULL,
          request_count INTEGER NOT NULL DEFAULT 0,
          success_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          input_tokens INTEGER NOT NULL DEFAULT 0,
          output_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_cost_usd REAL NOT NULL DEFAULT 0,
          cache_write_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_cost_usd REAL NOT NULL DEFAULT 0,
          thinking_tokens INTEGER NOT NULL DEFAULT 0,
          input_image_tokens INTEGER NOT NULL DEFAULT 0,
          output_image_tokens INTEGER NOT NULL DEFAULT 0,
          total_cost_usd REAL NOT NULL DEFAULT 0,
          duration_ms_sum INTEGER NOT NULL DEFAULT 0,
          duration_ms_count INTEGER NOT NULL DEFAULT 0,
          duration_ms_max INTEGER NOT NULL DEFAULT 0,
          first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
          first_token_ms_count INTEGER NOT NULL DEFAULT 0,
          first_token_ms_max INTEGER NOT NULL DEFAULT 0,
          last_used_at TEXT,
          last_error_at TEXT,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, stat_minute)
        );

    CREATE TABLE IF NOT EXISTS usage_stats_daily (
          system_account_id TEXT NOT NULL,
          scope_type TEXT NOT NULL,
          scope_id TEXT NOT NULL DEFAULT '',
          stat_date TEXT NOT NULL,
          request_count INTEGER NOT NULL DEFAULT 0,
          success_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          input_tokens INTEGER NOT NULL DEFAULT 0,
          output_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_cost_usd REAL NOT NULL DEFAULT 0,
          cache_write_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_cost_usd REAL NOT NULL DEFAULT 0,
          thinking_tokens INTEGER NOT NULL DEFAULT 0,
          input_image_tokens INTEGER NOT NULL DEFAULT 0,
          output_image_tokens INTEGER NOT NULL DEFAULT 0,
          total_cost_usd REAL NOT NULL DEFAULT 0,
          duration_ms_sum INTEGER NOT NULL DEFAULT 0,
          duration_ms_count INTEGER NOT NULL DEFAULT 0,
          duration_ms_max INTEGER NOT NULL DEFAULT 0,
          first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
          first_token_ms_count INTEGER NOT NULL DEFAULT 0,
          first_token_ms_max INTEGER NOT NULL DEFAULT 0,
          last_used_at TEXT,
          last_error_at TEXT,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, stat_date)
        );

    CREATE TABLE IF NOT EXISTS usage_stats_hourly (
          system_account_id TEXT NOT NULL,
          scope_type TEXT NOT NULL,
          scope_id TEXT NOT NULL DEFAULT '',
          stat_hour TEXT NOT NULL,
          request_count INTEGER NOT NULL DEFAULT 0,
          success_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          input_tokens INTEGER NOT NULL DEFAULT 0,
          output_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_cost_usd REAL NOT NULL DEFAULT 0,
          cache_write_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_cost_usd REAL NOT NULL DEFAULT 0,
          thinking_tokens INTEGER NOT NULL DEFAULT 0,
          input_image_tokens INTEGER NOT NULL DEFAULT 0,
          output_image_tokens INTEGER NOT NULL DEFAULT 0,
          total_cost_usd REAL NOT NULL DEFAULT 0,
          duration_ms_sum INTEGER NOT NULL DEFAULT 0,
          duration_ms_count INTEGER NOT NULL DEFAULT 0,
          duration_ms_max INTEGER NOT NULL DEFAULT 0,
          first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
          first_token_ms_count INTEGER NOT NULL DEFAULT 0,
          first_token_ms_max INTEGER NOT NULL DEFAULT 0,
          last_used_at TEXT,
          last_error_at TEXT,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, stat_hour)
        );

    CREATE TABLE IF NOT EXISTS usage_stats_weekly (
          system_account_id TEXT NOT NULL,
          scope_type TEXT NOT NULL,
          scope_id TEXT NOT NULL DEFAULT '',
          stat_week TEXT NOT NULL,
          request_count INTEGER NOT NULL DEFAULT 0,
          success_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          input_tokens INTEGER NOT NULL DEFAULT 0,
          output_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_cost_usd REAL NOT NULL DEFAULT 0,
          cache_write_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_cost_usd REAL NOT NULL DEFAULT 0,
          thinking_tokens INTEGER NOT NULL DEFAULT 0,
          input_image_tokens INTEGER NOT NULL DEFAULT 0,
          output_image_tokens INTEGER NOT NULL DEFAULT 0,
          total_cost_usd REAL NOT NULL DEFAULT 0,
          duration_ms_sum INTEGER NOT NULL DEFAULT 0,
          duration_ms_count INTEGER NOT NULL DEFAULT 0,
          duration_ms_max INTEGER NOT NULL DEFAULT 0,
          first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
          first_token_ms_count INTEGER NOT NULL DEFAULT 0,
          first_token_ms_max INTEGER NOT NULL DEFAULT 0,
          last_used_at TEXT,
          last_error_at TEXT,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, stat_week)
        );

    CREATE TABLE IF NOT EXISTS usage_stats_monthly (
          system_account_id TEXT NOT NULL,
          scope_type TEXT NOT NULL,
          scope_id TEXT NOT NULL DEFAULT '',
          stat_month TEXT NOT NULL,
          request_count INTEGER NOT NULL DEFAULT 0,
          success_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          input_tokens INTEGER NOT NULL DEFAULT 0,
          output_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_cost_usd REAL NOT NULL DEFAULT 0,
          cache_write_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_cost_usd REAL NOT NULL DEFAULT 0,
          thinking_tokens INTEGER NOT NULL DEFAULT 0,
          input_image_tokens INTEGER NOT NULL DEFAULT 0,
          output_image_tokens INTEGER NOT NULL DEFAULT 0,
          total_cost_usd REAL NOT NULL DEFAULT 0,
          duration_ms_sum INTEGER NOT NULL DEFAULT 0,
          duration_ms_count INTEGER NOT NULL DEFAULT 0,
          duration_ms_max INTEGER NOT NULL DEFAULT 0,
          first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
          first_token_ms_count INTEGER NOT NULL DEFAULT 0,
          first_token_ms_max INTEGER NOT NULL DEFAULT 0,
          last_used_at TEXT,
          last_error_at TEXT,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, stat_month)
        );

    CREATE TABLE IF NOT EXISTS authorization_team_usage_summary_daily (
          system_account_id TEXT NOT NULL,
          stat_date TEXT NOT NULL,
          team_filter_id TEXT NOT NULL DEFAULT '',
          resource_filter_type TEXT NOT NULL DEFAULT 'all',
          resource_filter_id TEXT NOT NULL DEFAULT '',
          row_count INTEGER NOT NULL DEFAULT 0,
          request_count INTEGER NOT NULL DEFAULT 0,
          success_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          input_tokens INTEGER NOT NULL DEFAULT 0,
          output_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_cost_usd REAL NOT NULL DEFAULT 0,
          cache_write_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_cost_usd REAL NOT NULL DEFAULT 0,
          thinking_tokens INTEGER NOT NULL DEFAULT 0,
          input_image_tokens INTEGER NOT NULL DEFAULT 0,
          output_image_tokens INTEGER NOT NULL DEFAULT 0,
          total_cost_usd REAL NOT NULL DEFAULT 0,
          duration_ms_sum INTEGER NOT NULL DEFAULT 0,
          duration_ms_count INTEGER NOT NULL DEFAULT 0,
          duration_ms_max INTEGER NOT NULL DEFAULT 0,
          first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
          first_token_ms_count INTEGER NOT NULL DEFAULT 0,
          first_token_ms_max INTEGER NOT NULL DEFAULT 0,
          last_used_at TEXT,
          last_error_at TEXT,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, stat_date, team_filter_id, resource_filter_type, resource_filter_id)
        );

    CREATE TABLE IF NOT EXISTS authorization_team_usage_range_windows (
          system_account_id TEXT NOT NULL,
          start_date TEXT NOT NULL,
          end_date TEXT NOT NULL,
          team_filter_id TEXT NOT NULL DEFAULT '',
          resource_filter_type TEXT NOT NULL DEFAULT 'all',
          resource_filter_id TEXT NOT NULL DEFAULT '',
          request_count INTEGER NOT NULL DEFAULT 0,
          input_tokens INTEGER NOT NULL DEFAULT 0,
          output_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_cost_usd REAL NOT NULL DEFAULT 0,
          cache_write_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_cost_usd REAL NOT NULL DEFAULT 0,
          thinking_tokens INTEGER NOT NULL DEFAULT 0,
          input_image_tokens INTEGER NOT NULL DEFAULT 0,
          output_image_tokens INTEGER NOT NULL DEFAULT 0,
          total_cost_usd REAL NOT NULL DEFAULT 0,
          last_used_at TEXT,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id)
        );

    CREATE TABLE IF NOT EXISTS authorization_user_usage_summary_daily (
          system_account_id TEXT NOT NULL,
          stat_date TEXT NOT NULL,
          team_filter_id TEXT NOT NULL DEFAULT '',
          grantee_filter_system_account_id TEXT NOT NULL DEFAULT '',
          resource_filter_type TEXT NOT NULL DEFAULT 'all',
          resource_filter_id TEXT NOT NULL DEFAULT '',
          row_count INTEGER NOT NULL DEFAULT 0,
          request_count INTEGER NOT NULL DEFAULT 0,
          success_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          input_tokens INTEGER NOT NULL DEFAULT 0,
          output_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_cost_usd REAL NOT NULL DEFAULT 0,
          cache_write_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_cost_usd REAL NOT NULL DEFAULT 0,
          thinking_tokens INTEGER NOT NULL DEFAULT 0,
          input_image_tokens INTEGER NOT NULL DEFAULT 0,
          output_image_tokens INTEGER NOT NULL DEFAULT 0,
          total_cost_usd REAL NOT NULL DEFAULT 0,
          duration_ms_sum INTEGER NOT NULL DEFAULT 0,
          duration_ms_count INTEGER NOT NULL DEFAULT 0,
          duration_ms_max INTEGER NOT NULL DEFAULT 0,
          first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
          first_token_ms_count INTEGER NOT NULL DEFAULT 0,
          first_token_ms_max INTEGER NOT NULL DEFAULT 0,
          last_used_at TEXT,
          last_error_at TEXT,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, stat_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id)
        );

    CREATE TABLE IF NOT EXISTS authorization_user_usage_range_windows (
          system_account_id TEXT NOT NULL,
          start_date TEXT NOT NULL,
          end_date TEXT NOT NULL,
          team_filter_id TEXT NOT NULL DEFAULT '',
          grantee_filter_system_account_id TEXT NOT NULL DEFAULT '',
          resource_filter_type TEXT NOT NULL DEFAULT 'all',
          resource_filter_id TEXT NOT NULL DEFAULT '',
          request_count INTEGER NOT NULL DEFAULT 0,
          input_tokens INTEGER NOT NULL DEFAULT 0,
          output_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_cost_usd REAL NOT NULL DEFAULT 0,
          cache_write_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_cost_usd REAL NOT NULL DEFAULT 0,
          thinking_tokens INTEGER NOT NULL DEFAULT 0,
          input_image_tokens INTEGER NOT NULL DEFAULT 0,
          output_image_tokens INTEGER NOT NULL DEFAULT 0,
          total_cost_usd REAL NOT NULL DEFAULT 0,
          last_used_at TEXT,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id)
        );

    CREATE TABLE IF NOT EXISTS usage_model_minute (
          system_account_id TEXT NOT NULL,
          stat_minute TEXT NOT NULL,
          provider_code TEXT NOT NULL DEFAULT 'unknown',
          model TEXT NOT NULL DEFAULT 'unknown',
          request_count INTEGER NOT NULL DEFAULT 0,
          success_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          input_tokens INTEGER NOT NULL DEFAULT 0,
          output_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_cost_usd REAL NOT NULL DEFAULT 0,
          cache_write_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_cost_usd REAL NOT NULL DEFAULT 0,
          thinking_tokens INTEGER NOT NULL DEFAULT 0,
          input_image_tokens INTEGER NOT NULL DEFAULT 0,
          output_image_tokens INTEGER NOT NULL DEFAULT 0,
          total_cost_usd REAL NOT NULL DEFAULT 0,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, stat_minute, provider_code, model)
        );

    CREATE TABLE IF NOT EXISTS usage_model_daily (
          system_account_id TEXT NOT NULL,
          stat_date TEXT NOT NULL,
          provider_code TEXT NOT NULL DEFAULT 'unknown',
          model TEXT NOT NULL DEFAULT 'unknown',
          request_count INTEGER NOT NULL DEFAULT 0,
          success_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          input_tokens INTEGER NOT NULL DEFAULT 0,
          output_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_cost_usd REAL NOT NULL DEFAULT 0,
          cache_write_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_cost_usd REAL NOT NULL DEFAULT 0,
          thinking_tokens INTEGER NOT NULL DEFAULT 0,
          input_image_tokens INTEGER NOT NULL DEFAULT 0,
          output_image_tokens INTEGER NOT NULL DEFAULT 0,
          total_cost_usd REAL NOT NULL DEFAULT 0,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, stat_date, provider_code, model)
        );

    CREATE TABLE IF NOT EXISTS usage_model_hourly (
          system_account_id TEXT NOT NULL,
          stat_hour TEXT NOT NULL,
          provider_code TEXT NOT NULL DEFAULT 'unknown',
          model TEXT NOT NULL DEFAULT 'unknown',
          request_count INTEGER NOT NULL DEFAULT 0,
          success_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          input_tokens INTEGER NOT NULL DEFAULT 0,
          output_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_cost_usd REAL NOT NULL DEFAULT 0,
          cache_write_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_cost_usd REAL NOT NULL DEFAULT 0,
          thinking_tokens INTEGER NOT NULL DEFAULT 0,
          input_image_tokens INTEGER NOT NULL DEFAULT 0,
          output_image_tokens INTEGER NOT NULL DEFAULT 0,
          total_cost_usd REAL NOT NULL DEFAULT 0,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, stat_hour, provider_code, model)
        );

    CREATE TABLE IF NOT EXISTS usage_model_weekly (
          system_account_id TEXT NOT NULL,
          stat_week TEXT NOT NULL,
          provider_code TEXT NOT NULL DEFAULT 'unknown',
          model TEXT NOT NULL DEFAULT 'unknown',
          request_count INTEGER NOT NULL DEFAULT 0,
          success_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          input_tokens INTEGER NOT NULL DEFAULT 0,
          output_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_cost_usd REAL NOT NULL DEFAULT 0,
          cache_write_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_cost_usd REAL NOT NULL DEFAULT 0,
          thinking_tokens INTEGER NOT NULL DEFAULT 0,
          input_image_tokens INTEGER NOT NULL DEFAULT 0,
          output_image_tokens INTEGER NOT NULL DEFAULT 0,
          total_cost_usd REAL NOT NULL DEFAULT 0,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, stat_week, provider_code, model)
        );

    CREATE TABLE IF NOT EXISTS usage_model_monthly (
          system_account_id TEXT NOT NULL,
          stat_month TEXT NOT NULL,
          provider_code TEXT NOT NULL DEFAULT 'unknown',
          model TEXT NOT NULL DEFAULT 'unknown',
          request_count INTEGER NOT NULL DEFAULT 0,
          success_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          input_tokens INTEGER NOT NULL DEFAULT 0,
          output_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_cost_usd REAL NOT NULL DEFAULT 0,
          cache_write_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_cost_usd REAL NOT NULL DEFAULT 0,
          thinking_tokens INTEGER NOT NULL DEFAULT 0,
          input_image_tokens INTEGER NOT NULL DEFAULT 0,
          output_image_tokens INTEGER NOT NULL DEFAULT 0,
          total_cost_usd REAL NOT NULL DEFAULT 0,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, stat_month, provider_code, model)
        );

    CREATE TABLE IF NOT EXISTS usage_error_minute (
          system_account_id TEXT NOT NULL,
          stat_minute TEXT NOT NULL,
          error_group TEXT NOT NULL DEFAULT 'unknown',
          provider_code TEXT NOT NULL DEFAULT 'unknown',
          error_code TEXT NOT NULL DEFAULT 'unknown',
          status_code INTEGER NOT NULL DEFAULT 0,
          error_message TEXT,
          request_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, stat_minute, error_group, provider_code, error_code, status_code)
        );

    CREATE TABLE IF NOT EXISTS usage_error_daily (
          system_account_id TEXT NOT NULL,
          stat_date TEXT NOT NULL,
          error_group TEXT NOT NULL DEFAULT 'unknown',
          provider_code TEXT NOT NULL DEFAULT 'unknown',
          error_code TEXT NOT NULL DEFAULT 'unknown',
          status_code INTEGER NOT NULL DEFAULT 0,
          error_message TEXT,
          request_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, stat_date, error_group, provider_code, error_code, status_code)
        );

    CREATE TABLE IF NOT EXISTS usage_error_hourly (
          system_account_id TEXT NOT NULL,
          stat_hour TEXT NOT NULL,
          error_group TEXT NOT NULL DEFAULT 'unknown',
          provider_code TEXT NOT NULL DEFAULT 'unknown',
          error_code TEXT NOT NULL DEFAULT 'unknown',
          status_code INTEGER NOT NULL DEFAULT 0,
          error_message TEXT,
          request_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, stat_hour, error_group, provider_code, error_code, status_code)
        );

    CREATE TABLE IF NOT EXISTS usage_error_weekly (
          system_account_id TEXT NOT NULL,
          stat_week TEXT NOT NULL,
          error_group TEXT NOT NULL DEFAULT 'unknown',
          provider_code TEXT NOT NULL DEFAULT 'unknown',
          error_code TEXT NOT NULL DEFAULT 'unknown',
          status_code INTEGER NOT NULL DEFAULT 0,
          error_message TEXT,
          request_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, stat_week, error_group, provider_code, error_code, status_code)
        );

    CREATE TABLE IF NOT EXISTS usage_error_monthly (
          system_account_id TEXT NOT NULL,
          stat_month TEXT NOT NULL,
          error_group TEXT NOT NULL DEFAULT 'unknown',
          provider_code TEXT NOT NULL DEFAULT 'unknown',
          error_code TEXT NOT NULL DEFAULT 'unknown',
          status_code INTEGER NOT NULL DEFAULT 0,
          error_message TEXT,
          request_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, stat_month, error_group, provider_code, error_code, status_code)
        );

    CREATE TABLE IF NOT EXISTS usage_latency_minute (
          system_account_id TEXT NOT NULL,
          scope_type TEXT NOT NULL,
          scope_id TEXT NOT NULL DEFAULT '',
          metric_type TEXT NOT NULL,
          stat_minute TEXT NOT NULL,
          bucket_upper_bound_ms INTEGER NOT NULL,
          sample_count INTEGER NOT NULL DEFAULT 0,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, metric_type, stat_minute, bucket_upper_bound_ms)
        );

    CREATE TABLE IF NOT EXISTS usage_latency_hourly (
          system_account_id TEXT NOT NULL,
          scope_type TEXT NOT NULL,
          scope_id TEXT NOT NULL DEFAULT '',
          metric_type TEXT NOT NULL,
          stat_hour TEXT NOT NULL,
          bucket_upper_bound_ms INTEGER NOT NULL,
          sample_count INTEGER NOT NULL DEFAULT 0,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, metric_type, stat_hour, bucket_upper_bound_ms)
        );

    CREATE TABLE IF NOT EXISTS usage_latency_daily (
          system_account_id TEXT NOT NULL,
          scope_type TEXT NOT NULL,
          scope_id TEXT NOT NULL DEFAULT '',
          metric_type TEXT NOT NULL,
          stat_date TEXT NOT NULL,
          bucket_upper_bound_ms INTEGER NOT NULL,
          sample_count INTEGER NOT NULL DEFAULT 0,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, metric_type, stat_date, bucket_upper_bound_ms)
        );

    CREATE TABLE IF NOT EXISTS usage_latency_weekly (
          system_account_id TEXT NOT NULL,
          scope_type TEXT NOT NULL,
          scope_id TEXT NOT NULL DEFAULT '',
          metric_type TEXT NOT NULL,
          stat_week TEXT NOT NULL,
          bucket_upper_bound_ms INTEGER NOT NULL,
          sample_count INTEGER NOT NULL DEFAULT 0,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, metric_type, stat_week, bucket_upper_bound_ms)
        );

    CREATE TABLE IF NOT EXISTS usage_latency_monthly (
          system_account_id TEXT NOT NULL,
          scope_type TEXT NOT NULL,
          scope_id TEXT NOT NULL DEFAULT '',
          metric_type TEXT NOT NULL,
          stat_month TEXT NOT NULL,
          bucket_upper_bound_ms INTEGER NOT NULL,
          sample_count INTEGER NOT NULL DEFAULT 0,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, metric_type, stat_month, bucket_upper_bound_ms)
        );

    CREATE TABLE IF NOT EXISTS usage_rank_snapshots (
          system_account_id TEXT NOT NULL,
          scope_type TEXT NOT NULL,
          window_key TEXT NOT NULL,
          metric TEXT NOT NULL,
          snapshot_at TEXT NOT NULL,
          rank INTEGER NOT NULL,
          scope_id TEXT NOT NULL,
          metric_value REAL NOT NULL DEFAULT 0,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id)
        );

    CREATE TABLE IF NOT EXISTS usage_overview_summary_windows (
          system_account_id TEXT NOT NULL,
          window_key TEXT NOT NULL,
          start_date TEXT NOT NULL DEFAULT '',
          end_date TEXT NOT NULL DEFAULT '',
          request_count INTEGER NOT NULL DEFAULT 0,
          success_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          input_tokens INTEGER NOT NULL DEFAULT 0,
          output_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_cost_usd REAL NOT NULL DEFAULT 0,
          cache_write_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_cost_usd REAL NOT NULL DEFAULT 0,
          thinking_tokens INTEGER NOT NULL DEFAULT 0,
          input_image_tokens INTEGER NOT NULL DEFAULT 0,
          output_image_tokens INTEGER NOT NULL DEFAULT 0,
          total_cost_usd REAL NOT NULL DEFAULT 0,
          duration_ms_sum INTEGER NOT NULL DEFAULT 0,
          duration_ms_count INTEGER NOT NULL DEFAULT 0,
          first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
          first_token_ms_count INTEGER NOT NULL DEFAULT 0,
          last_used_at TEXT,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, window_key)
        );

    CREATE TABLE IF NOT EXISTS usage_overview_trend_windows (
          system_account_id TEXT NOT NULL,
          window_key TEXT NOT NULL,
          start_date TEXT NOT NULL DEFAULT '',
          end_date TEXT NOT NULL DEFAULT '',
          bucket_key TEXT NOT NULL,
          request_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          input_tokens INTEGER NOT NULL DEFAULT 0,
          output_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_cost_usd REAL NOT NULL DEFAULT 0,
          cache_write_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_cost_usd REAL NOT NULL DEFAULT 0,
          thinking_tokens INTEGER NOT NULL DEFAULT 0,
          input_image_tokens INTEGER NOT NULL DEFAULT 0,
          output_image_tokens INTEGER NOT NULL DEFAULT 0,
          total_cost_usd REAL NOT NULL DEFAULT 0,
          duration_ms_sum INTEGER NOT NULL DEFAULT 0,
          duration_ms_count INTEGER NOT NULL DEFAULT 0,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, window_key, bucket_key)
        );

    CREATE TABLE IF NOT EXISTS usage_model_rank_windows (
          system_account_id TEXT NOT NULL,
          window_key TEXT NOT NULL,
          start_date TEXT NOT NULL DEFAULT '',
          end_date TEXT NOT NULL DEFAULT '',
          rank INTEGER NOT NULL,
          provider_code TEXT NOT NULL DEFAULT 'unknown',
          model TEXT NOT NULL DEFAULT 'unknown',
          request_count INTEGER NOT NULL DEFAULT 0,
          input_tokens INTEGER NOT NULL DEFAULT 0,
          output_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_cost_usd REAL NOT NULL DEFAULT 0,
          cache_write_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_cost_usd REAL NOT NULL DEFAULT 0,
          thinking_tokens INTEGER NOT NULL DEFAULT 0,
          input_image_tokens INTEGER NOT NULL DEFAULT 0,
          output_image_tokens INTEGER NOT NULL DEFAULT 0,
          total_cost_usd REAL NOT NULL DEFAULT 0,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, window_key, rank, provider_code, model)
        );

    CREATE TABLE IF NOT EXISTS usage_error_rank_windows (
          system_account_id TEXT NOT NULL,
          window_key TEXT NOT NULL,
          start_date TEXT NOT NULL DEFAULT '',
          end_date TEXT NOT NULL DEFAULT '',
          rank INTEGER NOT NULL,
          provider_code TEXT NOT NULL DEFAULT 'unknown',
          error_code TEXT NOT NULL DEFAULT 'unknown',
          status_code INTEGER NOT NULL DEFAULT 0,
          error_message TEXT,
          error_count INTEGER NOT NULL DEFAULT 0,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, window_key, rank, provider_code, error_code, status_code)
        );

    CREATE TABLE IF NOT EXISTS ai_performance_summary_windows (
          system_account_id TEXT NOT NULL,
          window_key TEXT NOT NULL,
          start_date TEXT NOT NULL DEFAULT '',
          end_date TEXT NOT NULL DEFAULT '',
          request_count INTEGER NOT NULL DEFAULT 0,
          duration_ms_sum INTEGER NOT NULL DEFAULT 0,
          duration_ms_count INTEGER NOT NULL DEFAULT 0,
          duration_ms_max INTEGER NOT NULL DEFAULT 0,
          first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
          first_token_ms_count INTEGER NOT NULL DEFAULT 0,
          first_token_ms_max INTEGER NOT NULL DEFAULT 0,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, window_key)
        );

    CREATE TABLE IF NOT EXISTS usage_quota_hourly_windows (
          system_account_id TEXT NOT NULL,
          scope_type TEXT NOT NULL,
          scope_id TEXT NOT NULL DEFAULT '',
          window_hours INTEGER NOT NULL,
          total_cost_usd REAL NOT NULL DEFAULT 0,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, window_hours)
        );

    CREATE TABLE IF NOT EXISTS usage_quota_hourly_window_dirty_scopes (
          system_account_id TEXT NOT NULL,
          scope_type TEXT NOT NULL,
          scope_id TEXT NOT NULL DEFAULT '',
          generation INTEGER NOT NULL DEFAULT 1,
          first_dirty_at TEXT NOT NULL,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id)
        );

    CREATE TABLE IF NOT EXISTS usage_overview_dirty_scopes (
          system_account_id TEXT PRIMARY KEY,
          scope_id TEXT NOT NULL,
          min_changed_date TEXT NOT NULL,
          generation INTEGER NOT NULL DEFAULT 1,
          first_dirty_at TEXT NOT NULL,
          updated_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS ai_performance_summary_dirty_system_accounts (
          system_account_id TEXT PRIMARY KEY,
          min_stat_date TEXT NOT NULL,
          max_stat_date TEXT NOT NULL,
          generation INTEGER NOT NULL DEFAULT 1,
          first_dirty_at TEXT NOT NULL,
          updated_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS usage_scope_range_windows (
          system_account_id TEXT NOT NULL,
          scope_type TEXT NOT NULL,
          scope_id TEXT NOT NULL DEFAULT '',
          start_date TEXT NOT NULL,
          end_date TEXT NOT NULL,
          window_key TEXT GENERATED ALWAYS AS (start_date || ':' || end_date) STORED,
          request_count INTEGER NOT NULL DEFAULT 0,
          success_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          input_tokens INTEGER NOT NULL DEFAULT 0,
          output_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_cost_usd REAL NOT NULL DEFAULT 0,
          cache_write_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_cost_usd REAL NOT NULL DEFAULT 0,
          thinking_tokens INTEGER NOT NULL DEFAULT 0,
          input_image_tokens INTEGER NOT NULL DEFAULT 0,
          output_image_tokens INTEGER NOT NULL DEFAULT 0,
          total_cost_usd REAL NOT NULL DEFAULT 0,
          duration_ms_sum INTEGER NOT NULL DEFAULT 0,
          duration_ms_count INTEGER NOT NULL DEFAULT 0,
          duration_ms_max INTEGER NOT NULL DEFAULT 0,
          first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
          first_token_ms_count INTEGER NOT NULL DEFAULT 0,
          first_token_ms_max INTEGER NOT NULL DEFAULT 0,
          active_days INTEGER NOT NULL DEFAULT 0,
          last_used_at TEXT,
          last_error_at TEXT,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, start_date, end_date)
        );

    CREATE TABLE IF NOT EXISTS usage_range_window_requests (
          id TEXT PRIMARY KEY,
          domain TEXT NOT NULL,
          system_account_id TEXT NOT NULL,
          scope_type TEXT NOT NULL,
          scope_id TEXT NOT NULL DEFAULT '',
          start_date TEXT NOT NULL,
          end_date TEXT NOT NULL,
          window_key TEXT GENERATED ALWAYS AS (start_date || ':' || end_date) STORED,
          status TEXT NOT NULL DEFAULT 'pending',
          requested_count INTEGER NOT NULL DEFAULT 1,
          last_requested_at TEXT NOT NULL,
          last_processed_at TEXT,
          error_message TEXT,
          expires_at TEXT NOT NULL,
          created_at TEXT NOT NULL,
          updated_at TEXT NOT NULL,
          UNIQUE (domain, system_account_id, scope_type, scope_id, start_date, end_date),
          CHECK (status IN ('pending', 'processing', 'completed', 'failed'))
        );

    CREATE TABLE IF NOT EXISTS client_ip_registry (
          ip_hash TEXT PRIMARY KEY,
          bucket_no INTEGER NOT NULL,
          aggregate_ip_key TEXT NOT NULL,
          client_ip TEXT NOT NULL,
          ip_version INTEGER NOT NULL,
          first_seen_at TEXT NOT NULL,
          last_seen_at TEXT NOT NULL,
          created_at TEXT NOT NULL,
          updated_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS client_ip_stats_daily (
          ip_hash TEXT NOT NULL,
          stat_date TEXT NOT NULL,
          request_count INTEGER NOT NULL DEFAULT 0,
          success_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          input_tokens INTEGER NOT NULL DEFAULT 0,
          output_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_cost_usd REAL NOT NULL DEFAULT 0,
          cache_write_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_cost_usd REAL NOT NULL DEFAULT 0,
          thinking_tokens INTEGER NOT NULL DEFAULT 0,
          input_image_tokens INTEGER NOT NULL DEFAULT 0,
          output_image_tokens INTEGER NOT NULL DEFAULT 0,
          total_cost_usd REAL NOT NULL DEFAULT 0,
          duration_ms_sum INTEGER NOT NULL DEFAULT 0,
          duration_ms_count INTEGER NOT NULL DEFAULT 0,
          duration_ms_max INTEGER NOT NULL DEFAULT 0,
          first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
          first_token_ms_count INTEGER NOT NULL DEFAULT 0,
          last_used_at TEXT,
          last_error_at TEXT,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (ip_hash, stat_date)
        );

    CREATE TABLE IF NOT EXISTS client_ip_usage_range_windows (
          ip_hash TEXT NOT NULL,
          start_date TEXT NOT NULL,
          end_date TEXT NOT NULL,
          request_count INTEGER NOT NULL DEFAULT 0,
          success_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          input_tokens INTEGER NOT NULL DEFAULT 0,
          output_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_cost_usd REAL NOT NULL DEFAULT 0,
          cache_write_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_cost_usd REAL NOT NULL DEFAULT 0,
          thinking_tokens INTEGER NOT NULL DEFAULT 0,
          input_image_tokens INTEGER NOT NULL DEFAULT 0,
          output_image_tokens INTEGER NOT NULL DEFAULT 0,
          total_cost_usd REAL NOT NULL DEFAULT 0,
          duration_ms_sum INTEGER NOT NULL DEFAULT 0,
          duration_ms_count INTEGER NOT NULL DEFAULT 0,
          duration_ms_max INTEGER NOT NULL DEFAULT 0,
          average_duration_ms REAL,
          first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
          first_token_ms_count INTEGER NOT NULL DEFAULT 0,
          average_first_token_ms REAL,
          active_days INTEGER NOT NULL DEFAULT 0,
          last_used_at TEXT,
          last_error_at TEXT,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (ip_hash, start_date, end_date)
        );

    CREATE TABLE IF NOT EXISTS client_ip_range_window_dirty_ips (
          ip_hash TEXT PRIMARY KEY,
          generation INTEGER NOT NULL DEFAULT 1,
          first_dirty_at TEXT NOT NULL,
          updated_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS client_ip_account_stats_daily (
          ip_hash TEXT NOT NULL,
          account_id TEXT NOT NULL,
          stat_date TEXT NOT NULL,
          request_count INTEGER NOT NULL DEFAULT 0,
          success_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          input_tokens INTEGER NOT NULL DEFAULT 0,
          output_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_cost_usd REAL NOT NULL DEFAULT 0,
          cache_write_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_cost_usd REAL NOT NULL DEFAULT 0,
          thinking_tokens INTEGER NOT NULL DEFAULT 0,
          input_image_tokens INTEGER NOT NULL DEFAULT 0,
          output_image_tokens INTEGER NOT NULL DEFAULT 0,
          total_cost_usd REAL NOT NULL DEFAULT 0,
          duration_ms_sum INTEGER NOT NULL DEFAULT 0,
          duration_ms_count INTEGER NOT NULL DEFAULT 0,
          duration_ms_max INTEGER NOT NULL DEFAULT 0,
          first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
          first_token_ms_count INTEGER NOT NULL DEFAULT 0,
          last_used_at TEXT,
          last_error_at TEXT,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (ip_hash, account_id, stat_date)
        );

    CREATE TABLE IF NOT EXISTS client_ip_account_usage_range_windows (
          ip_hash TEXT NOT NULL,
          account_id TEXT NOT NULL,
          start_date TEXT NOT NULL,
          end_date TEXT NOT NULL,
          request_count INTEGER NOT NULL DEFAULT 0,
          success_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          input_tokens INTEGER NOT NULL DEFAULT 0,
          output_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_cost_usd REAL NOT NULL DEFAULT 0,
          cache_write_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
          cache_write_cost_usd REAL NOT NULL DEFAULT 0,
          thinking_tokens INTEGER NOT NULL DEFAULT 0,
          input_image_tokens INTEGER NOT NULL DEFAULT 0,
          output_image_tokens INTEGER NOT NULL DEFAULT 0,
          total_cost_usd REAL NOT NULL DEFAULT 0,
          duration_ms_sum INTEGER NOT NULL DEFAULT 0,
          duration_ms_count INTEGER NOT NULL DEFAULT 0,
          duration_ms_max INTEGER NOT NULL DEFAULT 0,
          average_duration_ms REAL,
          first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
          first_token_ms_count INTEGER NOT NULL DEFAULT 0,
          average_first_token_ms REAL,
          active_days INTEGER NOT NULL DEFAULT 0,
          last_used_at TEXT,
          last_error_at TEXT,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (ip_hash, account_id, start_date, end_date)
        );

    CREATE TABLE IF NOT EXISTS client_ip_account_range_window_dirty_ips (
          ip_hash TEXT PRIMARY KEY,
          generation INTEGER NOT NULL DEFAULT 1,
          first_dirty_at TEXT NOT NULL,
          updated_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS client_ip_policies (
          id TEXT PRIMARY KEY,
          ip_hash TEXT NOT NULL,
          policy_type TEXT NOT NULL,
          status TEXT NOT NULL,
          reason TEXT,
          expires_at TEXT,
          created_by_system_account_id TEXT NOT NULL,
          created_at TEXT NOT NULL,
          updated_at TEXT NOT NULL,
          disabled_at TEXT,
          disabled_by_system_account_id TEXT,
          disabled_reason TEXT
        );

    CREATE TABLE IF NOT EXISTS client_ip_policy_hits (
          ip_hash TEXT NOT NULL,
          stat_date TEXT NOT NULL,
          policy_id TEXT NOT NULL,
          hit_count INTEGER NOT NULL DEFAULT 0,
          last_hit_at TEXT,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (ip_hash, stat_date, policy_id)
        );

    CREATE TABLE IF NOT EXISTS stats_job_state (
          scope_type TEXT NOT NULL,
          scope_id TEXT NOT NULL DEFAULT '',
          job_name TEXT NOT NULL,
          cursor_created_at TEXT,
          cursor_id TEXT,
          last_success_at TEXT,
          last_error_message TEXT,
          lag_seconds INTEGER,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (scope_type, scope_id, job_name)
        );

    /* J3b ownership moved to Gateway; its schemas must not be created by Node. */
    /*
    CREATE TABLE IF NOT EXISTS model_token_integrity_windows (
          system_account_id TEXT NOT NULL,
          account_id TEXT NOT NULL,
          requested_model TEXT NOT NULL,
          cohort_key_hmac TEXT NOT NULL,
          tokenizer_version TEXT NOT NULL,
          probe_set_version TEXT NOT NULL,
          observation_count INTEGER NOT NULL DEFAULT 0,
          valid_sample_count INTEGER NOT NULL DEFAULT 0,
          round_count INTEGER NOT NULL DEFAULT 0,
          sum_local REAL NOT NULL DEFAULT 0,
          sum_reported REAL NOT NULL DEFAULT 0,
          sum_local_squared REAL NOT NULL DEFAULT 0,
          sum_local_reported REAL NOT NULL DEFAULT 0,
          sum_reported_squared REAL NOT NULL DEFAULT 0,
          bucket_aligned_count INTEGER NOT NULL DEFAULT 0,
          slope REAL,
          intercept REAL,
          usage_integrity_status TEXT NOT NULL DEFAULT 'insufficient_evidence',
          first_observed_at TEXT NOT NULL,
          last_observed_at TEXT NOT NULL,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, account_id, requested_model, cohort_key_hmac, tokenizer_version, probe_set_version)
        );

    CREATE TABLE IF NOT EXISTS model_token_integrity_rounds (
          system_account_id TEXT NOT NULL,
          account_id TEXT NOT NULL,
          requested_model TEXT NOT NULL,
          cohort_key_hmac TEXT NOT NULL,
          tokenizer_version TEXT NOT NULL,
          probe_set_version TEXT NOT NULL,
          run_id TEXT NOT NULL,
          round_index INTEGER NOT NULL,
          valid_sample_count INTEGER NOT NULL DEFAULT 0,
          padding_mask INTEGER NOT NULL DEFAULT 0,
          first_observed_at TEXT NOT NULL,
          last_observed_at TEXT NOT NULL,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (
            system_account_id, account_id, requested_model, cohort_key_hmac, tokenizer_version,
            probe_set_version, run_id, round_index
          )
        );

    CREATE TABLE IF NOT EXISTS model_token_intercept_baseline_versions (
          cohort_key_hmac TEXT NOT NULL,
          requested_model TEXT NOT NULL,
          tokenizer_version TEXT NOT NULL,
          probe_set_version TEXT NOT NULL,
          baseline_version INTEGER NOT NULL,
          version_status TEXT NOT NULL DEFAULT 'calibration_pending',
          evidence_status TEXT NOT NULL DEFAULT 'insufficient',
          independent_source_count INTEGER NOT NULL DEFAULT 0,
          retained_source_count INTEGER NOT NULL DEFAULT 0,
          excluded_source_count INTEGER NOT NULL DEFAULT 0,
          median_intercept REAL,
          mad_intercept REAL,
          q10_intercept REAL,
          q90_intercept REAL,
          strong_threshold_intercept REAL,
          strong_gate_enabled INTEGER NOT NULL DEFAULT 0,
          calibration_note TEXT,
          first_observed_at TEXT NOT NULL,
          last_observed_at TEXT NOT NULL,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (cohort_key_hmac, requested_model, tokenizer_version, probe_set_version, baseline_version)
        );

    CREATE TABLE IF NOT EXISTS model_trust_window_sources (
          system_account_id TEXT NOT NULL,
          account_id TEXT NOT NULL,
          cohort_key_hmac TEXT NOT NULL,
          mapped_upstream_model TEXT NOT NULL,
          upstream_bucket_hmac TEXT NOT NULL,
          first_observed_at TEXT NOT NULL,
          last_observed_at TEXT NOT NULL,
          observation_count INTEGER NOT NULL DEFAULT 0,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, account_id, cohort_key_hmac, mapped_upstream_model, upstream_bucket_hmac)
        );

    CREATE TABLE IF NOT EXISTS model_identity_source_features (
          system_account_id TEXT NOT NULL,
          account_id TEXT NOT NULL,
          population_key_hmac TEXT NOT NULL,
          requested_model TEXT NOT NULL,
          upstream_bucket_hmac TEXT NOT NULL,
          probe_key_hmac TEXT NOT NULL,
          feature_version TEXT NOT NULL,
          sample_count INTEGER NOT NULL DEFAULT 0,
          sum_feature_1 REAL NOT NULL DEFAULT 0,
          sum_feature_2 REAL NOT NULL DEFAULT 0,
          sum_feature_3 REAL NOT NULL DEFAULT 0,
          sum_feature_4 REAL NOT NULL DEFAULT 0,
          sum_feature_5 REAL NOT NULL DEFAULT 0,
          sum_feature_6 REAL NOT NULL DEFAULT 0,
          sum_feature_7 REAL NOT NULL DEFAULT 0,
          sum_feature_8 REAL NOT NULL DEFAULT 0,
          latest_feature_1 REAL NOT NULL DEFAULT 0,
          latest_feature_2 REAL NOT NULL DEFAULT 0,
          latest_feature_3 REAL NOT NULL DEFAULT 0,
          latest_feature_4 REAL NOT NULL DEFAULT 0,
          latest_feature_5 REAL NOT NULL DEFAULT 0,
          latest_feature_6 REAL NOT NULL DEFAULT 0,
          latest_feature_7 REAL NOT NULL DEFAULT 0,
          latest_feature_8 REAL NOT NULL DEFAULT 0,
          constraint_pass_count INTEGER NOT NULL DEFAULT 0,
          first_observed_at TEXT NOT NULL,
          last_observed_at TEXT NOT NULL,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, account_id, population_key_hmac, requested_model, upstream_bucket_hmac, probe_key_hmac, feature_version)
        );

    CREATE TABLE IF NOT EXISTS model_identity_baseline_versions (
          population_key_hmac TEXT NOT NULL,
          requested_model TEXT NOT NULL,
          feature_version TEXT NOT NULL,
          baseline_version INTEGER NOT NULL,
          version_status TEXT NOT NULL,
          evidence_status TEXT NOT NULL,
          independent_source_count INTEGER NOT NULL DEFAULT 0,
          retained_source_count INTEGER NOT NULL DEFAULT 0,
          excluded_source_count INTEGER NOT NULL DEFAULT 0,
          median_vector_json TEXT NOT NULL DEFAULT '[]',
          mad_vector_json TEXT NOT NULL DEFAULT '[]',
          q10_vector_json TEXT NOT NULL DEFAULT '[]',
          q90_vector_json TEXT NOT NULL DEFAULT '[]',
          first_observed_at TEXT NOT NULL,
          last_observed_at TEXT NOT NULL,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (population_key_hmac, requested_model, feature_version, baseline_version)
        );

    CREATE TABLE IF NOT EXISTS model_paired_similarity_windows (
          system_account_id TEXT NOT NULL,
          account_id TEXT NOT NULL,
          population_key_hmac TEXT NOT NULL,
          requested_model TEXT NOT NULL,
          pair_key TEXT NOT NULL,
          feature_version TEXT NOT NULL,
          baseline_version INTEGER,
          paired_probe_count INTEGER NOT NULL DEFAULT 0,
          independent_source_count INTEGER NOT NULL DEFAULT 0,
          median_distance REAL,
          loo_median_distance REAL,
          loo_mad_distance REAL,
          loo_q10_distance REAL,
          similarity_status TEXT NOT NULL DEFAULT 'insufficient_evidence',
          last_observed_at TEXT NOT NULL,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, account_id, population_key_hmac, requested_model, pair_key, feature_version)
        );

    CREATE TABLE IF NOT EXISTS model_account_trust_results (
          system_account_id TEXT NOT NULL,
          account_id TEXT NOT NULL,
          requested_model TEXT NOT NULL,
          identity_status TEXT NOT NULL DEFAULT 'insufficient_evidence',
          mapping_status TEXT NOT NULL DEFAULT 'unknown',
          usage_integrity_status TEXT NOT NULL DEFAULT 'insufficient_evidence',
          protocol_status TEXT NOT NULL DEFAULT 'insufficient_evidence',
          evidence_status TEXT NOT NULL DEFAULT 'insufficient',
          evidence_coverage INTEGER NOT NULL DEFAULT 0,
          observation_count INTEGER NOT NULL DEFAULT 0,
          round_count INTEGER NOT NULL DEFAULT 0,
          independent_source_count INTEGER NOT NULL DEFAULT 0,
          identity_observation_count INTEGER NOT NULL DEFAULT 0,
          paired_probe_count INTEGER NOT NULL DEFAULT 0,
          slope REAL,
          intercept REAL,
          intercept_baseline_median REAL,
          intercept_baseline_mad REAL,
          intercept_baseline_version INTEGER,
          intercept_baseline_status TEXT,
          intercept_strong_gate_enabled INTEGER NOT NULL DEFAULT 0,
          identity_distance REAL,
          paired_distance REAL,
          paired_baseline_median REAL,
          paired_baseline_mad REAL,
          baseline_version INTEGER,
          baseline_version_status TEXT,
          feature_version TEXT,
          tokenizer_version TEXT,
          probe_set_version TEXT,
          reason_codes_json TEXT NOT NULL DEFAULT '[]',
          last_observed_at TEXT,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, account_id, requested_model)
        );

    CREATE TABLE IF NOT EXISTS model_trust_latest_dirty_accounts (
          system_account_id TEXT NOT NULL,
          account_id TEXT NOT NULL,
          requested_model TEXT NOT NULL,
          dirty_reason TEXT NOT NULL,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (system_account_id, account_id, requested_model)
        );

    CREATE TABLE IF NOT EXISTS model_trust_observation_receipts (
          observation_id TEXT PRIMARY KEY,
          observation_created_at TEXT NOT NULL,
          processed_at TEXT NOT NULL
        );

    CREATE INDEX IF NOT EXISTS idx_model_account_trust_results_updated ON model_account_trust_results(updated_at, account_id, requested_model);

    CREATE INDEX IF NOT EXISTS idx_model_trust_latest_dirty_updated ON model_trust_latest_dirty_accounts(updated_at, system_account_id, account_id, requested_model);

    CREATE INDEX IF NOT EXISTS idx_model_trust_observation_receipts_processed
      ON model_trust_observation_receipts(processed_at, observation_id);

    CREATE INDEX IF NOT EXISTS idx_model_token_integrity_windows_cohort ON model_token_integrity_windows(cohort_key_hmac, requested_model, updated_at);

    CREATE INDEX IF NOT EXISTS idx_model_token_integrity_windows_activation ON model_token_integrity_windows(cohort_key_hmac, requested_model, tokenizer_version, probe_set_version, account_id);

    CREATE INDEX IF NOT EXISTS idx_model_token_integrity_rounds_account ON model_token_integrity_rounds(account_id, requested_model, updated_at);

    CREATE INDEX IF NOT EXISTS idx_model_token_intercept_baseline_active ON model_token_intercept_baseline_versions(cohort_key_hmac, requested_model, tokenizer_version, probe_set_version, version_status, baseline_version);

    CREATE INDEX IF NOT EXISTS idx_model_trust_window_sources_cohort ON model_trust_window_sources(cohort_key_hmac, upstream_bucket_hmac);

    CREATE INDEX IF NOT EXISTS idx_model_identity_source_population ON model_identity_source_features(population_key_hmac, requested_model, feature_version, upstream_bucket_hmac);

    CREATE INDEX IF NOT EXISTS idx_model_identity_baseline_active ON model_identity_baseline_versions(population_key_hmac, requested_model, feature_version, version_status, baseline_version);

    CREATE INDEX IF NOT EXISTS idx_model_paired_similarity_account ON model_paired_similarity_windows(account_id, updated_at);
    */

    CREATE TABLE IF NOT EXISTS background_task_runs (
          run_id TEXT PRIMARY KEY,
          job_name TEXT NOT NULL,
          job_type TEXT NOT NULL,
          worker_role TEXT NOT NULL,
          status TEXT NOT NULL,
          lease_key TEXT NOT NULL,
          owner_id TEXT,
          params_json TEXT NOT NULL DEFAULT '{}',
          result_json TEXT NOT NULL DEFAULT '{}',
          error_message TEXT,
          submitted_at TEXT NOT NULL,
          started_at TEXT,
          heartbeat_at TEXT,
          finished_at TEXT,
          duration_ms INTEGER,
          exit_code INTEGER,
          created_at TEXT NOT NULL,
          updated_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS background_job_leases (
          lease_key TEXT PRIMARY KEY,
          job_name TEXT NOT NULL,
          shard_key TEXT NOT NULL DEFAULT '',
          owner_id TEXT NOT NULL,
          run_id TEXT,
          fencing_token INTEGER NOT NULL DEFAULT 0,
          lease_until TEXT NOT NULL,
          heartbeat_at TEXT NOT NULL,
          started_at TEXT NOT NULL,
          updated_at TEXT NOT NULL
        );

    CREATE INDEX IF NOT EXISTS idx_background_task_runs_status_updated
      ON background_task_runs(status, updated_at DESC, run_id DESC);

    CREATE INDEX IF NOT EXISTS idx_background_task_runs_job_created
      ON background_task_runs(job_name, created_at DESC, run_id DESC);

    CREATE INDEX IF NOT EXISTS idx_background_job_leases_job
      ON background_job_leases(job_name, shard_key, lease_until);

    CREATE TABLE IF NOT EXISTS usage_record_cleanup_deductions (
          usage_id TEXT NOT NULL,
          api_key_id TEXT NOT NULL,
          account_id TEXT,
          system_account_id TEXT NOT NULL,
          source_shard_key TEXT NOT NULL,
          record_json TEXT NOT NULL,
          stats_subtracted_at TEXT,
          shard_deleted_at TEXT,
          created_at TEXT NOT NULL,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (usage_id, source_shard_key)
        );

    CREATE INDEX IF NOT EXISTS idx_usage_record_cleanup_deductions_target
      ON usage_record_cleanup_deductions(api_key_id, system_account_id, shard_deleted_at);

    CREATE TABLE IF NOT EXISTS system_metrics_samples (
          id TEXT PRIMARY KEY,
          sampled_at TEXT NOT NULL,
          cpu_percent REAL,
          memory_used_percent REAL,
          memory_total_bytes BIGINT,
          memory_free_bytes BIGINT,
          process_rss_bytes BIGINT,
          process_heap_used_bytes BIGINT,
          process_heap_total_bytes BIGINT,
          event_loop_lag_ms REAL,
          network_rx_bytes_per_sec REAL,
          network_tx_bytes_per_sec REAL,
          network_rx_total_bytes BIGINT,
          network_tx_total_bytes BIGINT,
          db_file_bytes BIGINT,
          stats_lag_seconds INTEGER,
          created_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS system_metrics_hourly (
          stat_hour TEXT PRIMARY KEY,
          sample_count INTEGER NOT NULL DEFAULT 0,
          cpu_percent_sum REAL NOT NULL DEFAULT 0,
          cpu_percent_max REAL,
          memory_used_percent_sum REAL NOT NULL DEFAULT 0,
          memory_used_percent_max REAL,
          process_rss_bytes_sum BIGINT NOT NULL DEFAULT 0,
          process_rss_bytes_max BIGINT,
          process_heap_used_bytes_sum BIGINT NOT NULL DEFAULT 0,
          process_heap_used_bytes_max BIGINT,
          event_loop_lag_ms_sum REAL NOT NULL DEFAULT 0,
          event_loop_lag_ms_count INTEGER NOT NULL DEFAULT 0,
          event_loop_lag_ms_max REAL,
          network_rx_bytes_per_sec_sum REAL NOT NULL DEFAULT 0,
          network_rx_bytes_per_sec_max REAL,
          network_rx_bytes_per_sec_count INTEGER NOT NULL DEFAULT 0,
          network_tx_bytes_per_sec_sum REAL NOT NULL DEFAULT 0,
          network_tx_bytes_per_sec_max REAL,
          network_tx_bytes_per_sec_count INTEGER NOT NULL DEFAULT 0,
          network_rx_total_bytes_max BIGINT,
          network_tx_total_bytes_max BIGINT,
          db_file_bytes_max BIGINT,
          stats_lag_seconds_max INTEGER,
          updated_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS system_metrics_trend_windows (
          window_key TEXT NOT NULL,
          start_date TEXT NOT NULL DEFAULT '',
          end_date TEXT NOT NULL DEFAULT '',
          bucket_key TEXT NOT NULL,
          sample_count INTEGER NOT NULL DEFAULT 0,
          cpu_percent_sum REAL NOT NULL DEFAULT 0,
          cpu_percent_max REAL,
          memory_used_percent_sum REAL NOT NULL DEFAULT 0,
          memory_used_percent_max REAL,
          process_rss_bytes_sum BIGINT NOT NULL DEFAULT 0,
          process_rss_bytes_max BIGINT,
          process_heap_used_bytes_sum BIGINT NOT NULL DEFAULT 0,
          process_heap_used_bytes_max BIGINT,
          event_loop_lag_ms_sum REAL NOT NULL DEFAULT 0,
          event_loop_lag_ms_count INTEGER NOT NULL DEFAULT 0,
          event_loop_lag_ms_max REAL,
          network_rx_bytes_per_sec_sum REAL NOT NULL DEFAULT 0,
          network_rx_bytes_per_sec_max REAL,
          network_rx_bytes_per_sec_count INTEGER NOT NULL DEFAULT 0,
          network_tx_bytes_per_sec_sum REAL NOT NULL DEFAULT 0,
          network_tx_bytes_per_sec_max REAL,
          network_tx_bytes_per_sec_count INTEGER NOT NULL DEFAULT 0,
          network_rx_total_bytes_max BIGINT,
          network_tx_total_bytes_max BIGINT,
          db_file_bytes_max BIGINT,
          stats_lag_seconds_max INTEGER,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (window_key, bucket_key)
        );

    CREATE TABLE IF NOT EXISTS process_event_loop_samples (
          id TEXT PRIMARY KEY,
          sampled_at TEXT NOT NULL,
          process_role TEXT NOT NULL,
          process_pid INTEGER,
          event_loop_lag_ms REAL,
          process_rss_bytes BIGINT,
          process_heap_used_bytes BIGINT,
          process_heap_total_bytes BIGINT,
          process_external_bytes BIGINT,
          process_array_buffers_bytes BIGINT,
          created_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS process_event_loop_hourly (
          stat_hour TEXT NOT NULL,
          process_role TEXT NOT NULL,
          sample_count INTEGER NOT NULL DEFAULT 0,
          event_loop_lag_ms_sum REAL NOT NULL DEFAULT 0,
          event_loop_lag_ms_count INTEGER NOT NULL DEFAULT 0,
          event_loop_lag_ms_max REAL,
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
          updated_at TEXT NOT NULL,
          PRIMARY KEY (stat_hour, process_role)
        );

    CREATE TABLE IF NOT EXISTS process_event_loop_trend_windows (
          window_key TEXT NOT NULL,
          start_date TEXT NOT NULL DEFAULT '',
          end_date TEXT NOT NULL DEFAULT '',
          bucket_key TEXT NOT NULL,
          process_role TEXT NOT NULL,
          sample_count INTEGER NOT NULL DEFAULT 0,
          event_loop_lag_ms_sum REAL NOT NULL DEFAULT 0,
          event_loop_lag_ms_count INTEGER NOT NULL DEFAULT 0,
          event_loop_lag_ms_max REAL,
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
          updated_at TEXT NOT NULL,
          PRIMARY KEY (window_key, bucket_key, process_role)
        );

    CREATE INDEX IF NOT EXISTS idx_account_quality_minute_stats_minute ON account_quality_minute_stats(stat_minute, account_id);

    CREATE INDEX IF NOT EXISTS idx_account_health_hourly_scope
      ON account_health_hourly(system_account_id, stat_hour, account_id);

    CREATE INDEX IF NOT EXISTS idx_group_account_stats_group ON group_account_stats(group_id);

    CREATE INDEX IF NOT EXISTS idx_account_quality_scores_sort ON account_quality_scores(provider_code, quality_score, quality_state);

    CREATE INDEX IF NOT EXISTS idx_account_quality_scores_failure_precheck
      ON account_quality_scores(recent_error_count DESC, success_rate, updated_at DESC, account_id)
      WHERE recent_request_count >= 5 AND recent_error_count >= 2;

    CREATE INDEX IF NOT EXISTS idx_account_quality_dirty_accounts_first_dirty ON account_quality_dirty_accounts(first_dirty_at, account_id);

    CREATE INDEX IF NOT EXISTS idx_account_usage_snapshots_kind ON account_usage_snapshots(kind, updated_at);

    CREATE INDEX IF NOT EXISTS idx_account_usage_snapshots_kind_account ON account_usage_snapshots(kind, account_id);

    CREATE INDEX IF NOT EXISTS idx_stats_job_state_usage_shard_cursor_floor
      ON stats_job_state(scope_type, job_name, cursor_created_at, cursor_id)
      WHERE cursor_created_at IS NOT NULL
        AND cursor_id IS NOT NULL;

    CREATE INDEX IF NOT EXISTS idx_stats_job_state_usage_shard_cursor_floor_any_job
      ON stats_job_state(scope_type, cursor_created_at, cursor_id, job_name)
      WHERE cursor_created_at IS NOT NULL
        AND cursor_id IS NOT NULL;

    CREATE INDEX IF NOT EXISTS idx_usage_stats_totals_updated ON usage_stats_totals(updated_at);

    CREATE INDEX IF NOT EXISTS idx_usage_stats_totals_scope_seed
      ON usage_stats_totals(scope_type, system_account_id, scope_id);

    CREATE INDEX IF NOT EXISTS idx_usage_stats_minute_scope_minute ON usage_stats_minute(system_account_id, scope_type, scope_id, stat_minute);

    CREATE INDEX IF NOT EXISTS idx_usage_stats_minute_minute ON usage_stats_minute(stat_minute);

    CREATE INDEX IF NOT EXISTS idx_usage_stats_daily_scope_date ON usage_stats_daily(system_account_id, scope_type, scope_id, stat_date);

    CREATE INDEX IF NOT EXISTS idx_usage_stats_daily_system_scope_date
      ON usage_stats_daily(system_account_id, scope_type, stat_date, scope_id);

    CREATE INDEX IF NOT EXISTS idx_usage_stats_daily_system_account_top_activity
      ON usage_stats_daily(stat_date, request_count DESC, last_used_at DESC, system_account_id)
      WHERE scope_type = 'system_account'
        AND scope_id = system_account_id
        AND system_account_id <> 'global';

    CREATE INDEX IF NOT EXISTS idx_usage_stats_daily_date ON usage_stats_daily(stat_date);

    CREATE INDEX IF NOT EXISTS idx_usage_stats_daily_updated ON usage_stats_daily(updated_at);

    CREATE INDEX IF NOT EXISTS idx_usage_stats_hourly_scope_hour ON usage_stats_hourly(system_account_id, scope_type, scope_id, stat_hour);

    CREATE INDEX IF NOT EXISTS idx_usage_stats_hourly_scope_stat_hour ON usage_stats_hourly(system_account_id, scope_type, stat_hour, scope_id);

    CREATE INDEX IF NOT EXISTS idx_usage_stats_hourly_hour ON usage_stats_hourly(stat_hour);

    CREATE INDEX IF NOT EXISTS idx_usage_stats_hourly_updated ON usage_stats_hourly(updated_at);

    CREATE INDEX IF NOT EXISTS idx_usage_stats_weekly_scope_week ON usage_stats_weekly(system_account_id, scope_type, scope_id, stat_week);

    CREATE INDEX IF NOT EXISTS idx_usage_stats_weekly_week ON usage_stats_weekly(stat_week);

    CREATE INDEX IF NOT EXISTS idx_usage_stats_monthly_scope_month ON usage_stats_monthly(system_account_id, scope_type, scope_id, stat_month);

    CREATE INDEX IF NOT EXISTS idx_usage_stats_monthly_month ON usage_stats_monthly(stat_month);

    CREATE INDEX IF NOT EXISTS idx_usage_stats_monthly_updated ON usage_stats_monthly(updated_at);

    CREATE INDEX IF NOT EXISTS idx_authorization_team_usage_summary_daily_lookup ON authorization_team_usage_summary_daily(system_account_id, stat_date, team_filter_id, resource_filter_type, resource_filter_id);

    CREATE INDEX IF NOT EXISTS idx_authorization_team_usage_summary_daily_updated ON authorization_team_usage_summary_daily(updated_at);

    CREATE INDEX IF NOT EXISTS idx_authorization_team_usage_range_lookup ON authorization_team_usage_range_windows(system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id);

    CREATE INDEX IF NOT EXISTS idx_authorization_team_usage_range_sort ON authorization_team_usage_range_windows(system_account_id, start_date, end_date, total_cost_usd DESC, request_count DESC, last_used_at DESC, team_filter_id, resource_filter_type, resource_filter_id);

    CREATE INDEX IF NOT EXISTS idx_authorization_team_usage_range_end ON authorization_team_usage_range_windows(end_date);

    CREATE INDEX IF NOT EXISTS idx_authorization_user_usage_summary_daily_lookup ON authorization_user_usage_summary_daily(system_account_id, stat_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id);

    CREATE INDEX IF NOT EXISTS idx_authorization_user_usage_summary_daily_updated ON authorization_user_usage_summary_daily(updated_at);

    CREATE INDEX IF NOT EXISTS idx_authorization_user_usage_range_lookup ON authorization_user_usage_range_windows(system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id);

    CREATE INDEX IF NOT EXISTS idx_authorization_user_usage_range_sort ON authorization_user_usage_range_windows(system_account_id, start_date, end_date, team_filter_id, total_cost_usd DESC, request_count DESC, last_used_at DESC, grantee_filter_system_account_id, resource_filter_type, resource_filter_id);

    CREATE INDEX IF NOT EXISTS idx_authorization_user_usage_range_end ON authorization_user_usage_range_windows(end_date);

    CREATE INDEX IF NOT EXISTS idx_usage_model_minute_minute ON usage_model_minute(system_account_id, stat_minute, model);

    CREATE INDEX IF NOT EXISTS idx_usage_model_minute_stat_minute ON usage_model_minute(stat_minute);

    CREATE INDEX IF NOT EXISTS idx_usage_model_daily_date ON usage_model_daily(system_account_id, stat_date, model);

    CREATE INDEX IF NOT EXISTS idx_usage_model_daily_stat_date ON usage_model_daily(stat_date);

    CREATE INDEX IF NOT EXISTS idx_usage_model_daily_updated ON usage_model_daily(updated_at);

    CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_model_daily_account_date_provider_model ON usage_model_daily(system_account_id, stat_date, provider_code, model);

    CREATE INDEX IF NOT EXISTS idx_usage_model_hourly_hour ON usage_model_hourly(system_account_id, stat_hour, model);

    CREATE INDEX IF NOT EXISTS idx_usage_model_hourly_stat_hour ON usage_model_hourly(stat_hour);

    CREATE INDEX IF NOT EXISTS idx_usage_model_weekly_week ON usage_model_weekly(system_account_id, stat_week, model);

    CREATE INDEX IF NOT EXISTS idx_usage_model_weekly_stat_week ON usage_model_weekly(stat_week);

    CREATE INDEX IF NOT EXISTS idx_usage_model_monthly_month ON usage_model_monthly(system_account_id, stat_month, model);

    CREATE INDEX IF NOT EXISTS idx_usage_model_monthly_stat_month ON usage_model_monthly(stat_month);

    CREATE INDEX IF NOT EXISTS idx_usage_error_minute_minute ON usage_error_minute(system_account_id, stat_minute, error_code);

    CREATE INDEX IF NOT EXISTS idx_usage_error_minute_stat_minute ON usage_error_minute(stat_minute);

    CREATE INDEX IF NOT EXISTS idx_usage_error_daily_date ON usage_error_daily(system_account_id, stat_date, error_code);

    CREATE INDEX IF NOT EXISTS idx_usage_error_daily_stat_date ON usage_error_daily(stat_date);

    CREATE INDEX IF NOT EXISTS idx_usage_error_daily_updated ON usage_error_daily(updated_at);

    CREATE INDEX IF NOT EXISTS idx_usage_error_hourly_hour ON usage_error_hourly(system_account_id, stat_hour, error_code);

    CREATE INDEX IF NOT EXISTS idx_usage_error_hourly_stat_hour ON usage_error_hourly(stat_hour);

    CREATE INDEX IF NOT EXISTS idx_usage_error_weekly_week ON usage_error_weekly(system_account_id, stat_week, error_code);

    CREATE INDEX IF NOT EXISTS idx_usage_error_weekly_stat_week ON usage_error_weekly(stat_week);

    CREATE INDEX IF NOT EXISTS idx_usage_error_monthly_month ON usage_error_monthly(system_account_id, stat_month, error_code);

    CREATE INDEX IF NOT EXISTS idx_usage_error_monthly_stat_month ON usage_error_monthly(stat_month);

    CREATE INDEX IF NOT EXISTS idx_usage_latency_minute_minute ON usage_latency_minute(stat_minute);

    CREATE INDEX IF NOT EXISTS idx_usage_latency_hourly_hour ON usage_latency_hourly(stat_hour);

    CREATE INDEX IF NOT EXISTS idx_usage_latency_daily_date ON usage_latency_daily(stat_date);

    CREATE INDEX IF NOT EXISTS idx_usage_latency_weekly_week ON usage_latency_weekly(stat_week);

    CREATE INDEX IF NOT EXISTS idx_usage_latency_monthly_month ON usage_latency_monthly(stat_month);

    CREATE INDEX IF NOT EXISTS idx_usage_rank_snapshots_lookup ON usage_rank_snapshots(system_account_id, scope_type, window_key, metric, snapshot_at DESC, rank);

    CREATE INDEX IF NOT EXISTS idx_usage_rank_snapshots_snapshot ON usage_rank_snapshots(snapshot_at);

    CREATE INDEX IF NOT EXISTS idx_usage_overview_summary_windows_end ON usage_overview_summary_windows(end_date);

    CREATE INDEX IF NOT EXISTS idx_usage_quota_hourly_window_dirty_updated
      ON usage_quota_hourly_window_dirty_scopes(first_dirty_at, system_account_id, scope_type, scope_id);

    CREATE INDEX IF NOT EXISTS idx_usage_overview_dirty_first_dirty
      ON usage_overview_dirty_scopes(first_dirty_at, system_account_id);

    CREATE INDEX IF NOT EXISTS idx_ai_performance_summary_dirty_first_dirty
      ON ai_performance_summary_dirty_system_accounts(first_dirty_at, system_account_id);

    CREATE INDEX IF NOT EXISTS idx_usage_overview_summary_windows_prewarm_order
      ON usage_overview_summary_windows(window_key, request_count DESC, last_used_at DESC, system_account_id)
      WHERE request_count > 0
        AND system_account_id <> 'global';

    CREATE INDEX IF NOT EXISTS idx_usage_overview_trend_windows_lookup ON usage_overview_trend_windows(system_account_id, window_key, bucket_key);

    CREATE INDEX IF NOT EXISTS idx_usage_overview_trend_windows_end ON usage_overview_trend_windows(end_date);

    CREATE INDEX IF NOT EXISTS idx_usage_model_rank_windows_lookup ON usage_model_rank_windows(system_account_id, window_key, rank);

    CREATE INDEX IF NOT EXISTS idx_usage_model_rank_windows_end ON usage_model_rank_windows(end_date);

    CREATE INDEX IF NOT EXISTS idx_usage_error_rank_windows_lookup ON usage_error_rank_windows(system_account_id, window_key, rank);

    CREATE INDEX IF NOT EXISTS idx_usage_error_rank_windows_end ON usage_error_rank_windows(end_date);

    CREATE INDEX IF NOT EXISTS idx_ai_performance_summary_windows_lookup ON ai_performance_summary_windows(system_account_id, window_key);

    CREATE INDEX IF NOT EXISTS idx_ai_performance_summary_windows_end ON ai_performance_summary_windows(end_date);

    CREATE INDEX IF NOT EXISTS idx_usage_quota_hourly_windows_lookup ON usage_quota_hourly_windows(system_account_id, scope_type, scope_id, window_hours);

    CREATE INDEX IF NOT EXISTS idx_usage_quota_hourly_windows_updated ON usage_quota_hourly_windows(updated_at);

    CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_lookup ON usage_scope_range_windows(system_account_id, scope_type, scope_id, window_key);

    CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_range_lookup ON usage_scope_range_windows(system_account_id, scope_type, window_key, scope_id);

    CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_account_usage_order ON usage_scope_range_windows(system_account_id, scope_type, window_key, request_count DESC, total_cost_usd DESC, (input_tokens + output_tokens) DESC, last_used_at DESC, scope_id);

    CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_end ON usage_scope_range_windows(end_date);

    CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_end_start ON usage_scope_range_windows(end_date, start_date);

    CREATE INDEX IF NOT EXISTS idx_usage_range_window_requests_pending ON usage_range_window_requests(status, domain, updated_at, id);

    CREATE INDEX IF NOT EXISTS idx_usage_range_window_requests_expires ON usage_range_window_requests(expires_at, domain, status);

    CREATE INDEX IF NOT EXISTS idx_client_ip_registry_bucket ON client_ip_registry(bucket_no, ip_hash);

    CREATE INDEX IF NOT EXISTS idx_client_ip_registry_last_seen ON client_ip_registry(last_seen_at DESC, ip_hash);

    CREATE INDEX IF NOT EXISTS idx_client_ip_registry_ip ON client_ip_registry(aggregate_ip_key);

    CREATE INDEX IF NOT EXISTS idx_client_ip_registry_client_ip ON client_ip_registry(client_ip);

    CREATE INDEX IF NOT EXISTS idx_client_ip_stats_daily_date ON client_ip_stats_daily(stat_date, ip_hash);

    CREATE INDEX IF NOT EXISTS idx_client_ip_range_requests ON client_ip_usage_range_windows(start_date, end_date, request_count DESC, ip_hash);

    CREATE INDEX IF NOT EXISTS idx_client_ip_range_end ON client_ip_usage_range_windows(end_date);

    CREATE INDEX IF NOT EXISTS idx_client_ip_range_dirty_updated ON client_ip_range_window_dirty_ips(first_dirty_at ASC, ip_hash);

    CREATE INDEX IF NOT EXISTS idx_client_ip_account_daily_date ON client_ip_account_stats_daily(stat_date, ip_hash, account_id);

    CREATE INDEX IF NOT EXISTS idx_client_ip_account_daily_ip_date ON client_ip_account_stats_daily(ip_hash, stat_date, account_id);

    CREATE INDEX IF NOT EXISTS idx_client_ip_account_range_requests ON client_ip_account_usage_range_windows(ip_hash, start_date, end_date, request_count DESC, account_id);

    CREATE INDEX IF NOT EXISTS idx_client_ip_account_range_dirty_updated ON client_ip_account_range_window_dirty_ips(first_dirty_at ASC, ip_hash);

    CREATE UNIQUE INDEX IF NOT EXISTS idx_client_ip_policies_active_unique ON client_ip_policies(ip_hash) WHERE status = 'active';

    CREATE INDEX IF NOT EXISTS idx_client_ip_policies_active ON client_ip_policies(status, policy_type, ip_hash, expires_at);

    CREATE INDEX IF NOT EXISTS idx_client_ip_policies_ip ON client_ip_policies(ip_hash, status, policy_type, created_at DESC);

    CREATE INDEX IF NOT EXISTS idx_client_ip_policy_hits_date ON client_ip_policy_hits(stat_date DESC, ip_hash);

    CREATE INDEX IF NOT EXISTS idx_account_usage_snapshots_updated ON account_usage_snapshots(updated_at);

    CREATE INDEX IF NOT EXISTS idx_system_metrics_trend_windows_lookup ON system_metrics_trend_windows(window_key, bucket_key);

    CREATE INDEX IF NOT EXISTS idx_system_metrics_trend_windows_end ON system_metrics_trend_windows(end_date);

    CREATE INDEX IF NOT EXISTS idx_system_metrics_samples_sampled_at ON system_metrics_samples(sampled_at);

    CREATE INDEX IF NOT EXISTS idx_system_metrics_samples_latest ON system_metrics_samples(sampled_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_system_metrics_hourly_updated ON system_metrics_hourly(updated_at);

    CREATE INDEX IF NOT EXISTS idx_process_event_loop_samples_sampled_at ON process_event_loop_samples(sampled_at);

    CREATE INDEX IF NOT EXISTS idx_process_event_loop_samples_role_latest ON process_event_loop_samples(process_role, sampled_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_process_event_loop_samples_role_peak
      ON process_event_loop_samples(process_role, event_loop_lag_ms DESC, sampled_at DESC, id DESC)
      WHERE event_loop_lag_ms IS NOT NULL;

    CREATE INDEX IF NOT EXISTS idx_process_event_loop_hourly_lookup ON process_event_loop_hourly(stat_hour, process_role);

    CREATE INDEX IF NOT EXISTS idx_process_event_loop_hourly_updated ON process_event_loop_hourly(updated_at);

    CREATE INDEX IF NOT EXISTS idx_process_event_loop_trend_windows_lookup ON process_event_loop_trend_windows(window_key, bucket_key, process_role);

    CREATE INDEX IF NOT EXISTS idx_process_event_loop_trend_windows_end ON process_event_loop_trend_windows(end_date);
`
const sqliteStatsUsageCleanupDeductionsIndexDDL = `    CREATE INDEX IF NOT EXISTS idx_usage_record_cleanup_deductions_account
      ON usage_record_cleanup_deductions(account_id, shard_deleted_at);
`

const sqliteChatDDL = `    CREATE TABLE IF NOT EXISTS chat_conversations (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      api_key_id TEXT,
      api_key_name_snapshot TEXT NOT NULL,
      title TEXT NOT NULL DEFAULT '新对话',
      title_source_message_id TEXT,
      is_pinned INTEGER NOT NULL DEFAULT 0,
      last_model TEXT,
      default_image_model TEXT NOT NULL DEFAULT 'gpt-image-2',
      next_sequence_no INTEGER NOT NULL DEFAULT 1,
      user_turn_count INTEGER NOT NULL DEFAULT 0,
      message_revision INTEGER NOT NULL DEFAULT 0,
      active_turn_id TEXT,
      active_started_at TEXT,
      context_revision INTEGER NOT NULL DEFAULT 0,
      active_checkpoint_id TEXT,
      compacted_through_sequence INTEGER NOT NULL DEFAULT 0,
      context_state TEXT NOT NULL DEFAULT 'ready',
      active_context_tokens INTEGER,
      effective_context_limit_tokens INTEGER,
      context_usage_estimated INTEGER NOT NULL DEFAULT 1,
      context_claim_id TEXT,
      context_claim_revision INTEGER,
      context_claim_through_sequence INTEGER,
      context_claimed_at TEXT,
      context_retry_at TEXT,
      context_attempt_count INTEGER NOT NULL DEFAULT 0,
      context_error_code TEXT,
      context_progress_sequence INTEGER NOT NULL DEFAULT 0,
      context_progress_earliest_expires_at TEXT,
      last_message_at TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
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
    );

    CREATE TABLE IF NOT EXISTS chat_messages (
      id TEXT PRIMARY KEY,
      conversation_id TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      turn_id TEXT NOT NULL,
      sequence_no INTEGER NOT NULL,
      client_message_id TEXT,
      role TEXT NOT NULL,
      status TEXT NOT NULL,
      content_text TEXT NOT NULL DEFAULT '',
      content_blocks_json TEXT NOT NULL DEFAULT '[]',
      content_bytes INTEGER NOT NULL DEFAULT 0,
      storage_reserved_bytes INTEGER NOT NULL DEFAULT 0,
      model TEXT NOT NULL,
      trace_id TEXT,
      finish_reason TEXT,
      error_code TEXT,
      error_message TEXT,
      created_at TEXT NOT NULL,
      completed_at TEXT,
      expires_at TEXT NOT NULL,
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
      )
    );

    CREATE TABLE IF NOT EXISTS chat_message_idempotency (
      conversation_id TEXT NOT NULL,
      client_message_id TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      turn_id TEXT NOT NULL,
      user_message_id TEXT NOT NULL,
      assistant_message_id TEXT NOT NULL,
      created_at TEXT NOT NULL,
      expires_at TEXT NOT NULL,
      PRIMARY KEY (conversation_id, client_message_id),
      FOREIGN KEY (conversation_id) REFERENCES chat_conversations(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS chat_user_storage_windows (
      system_account_id TEXT NOT NULL,
      bucket_date TEXT NOT NULL,
      content_bytes INTEGER NOT NULL DEFAULT 0,
      reserved_bytes INTEGER NOT NULL DEFAULT 0,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, bucket_date),
      CHECK (content_bytes >= 0),
      CHECK (reserved_bytes >= 0)
    );

    CREATE TABLE IF NOT EXISTS chat_user_asset_usage (
      system_account_id TEXT PRIMARY KEY,
      asset_bytes INTEGER NOT NULL DEFAULT 0,
      asset_count INTEGER NOT NULL DEFAULT 0,
      updated_at TEXT NOT NULL,
      CHECK (asset_bytes >= 0),
      CHECK (asset_count >= 0)
    );

    CREATE TABLE IF NOT EXISTS chat_context_checkpoints (
      id TEXT PRIMARY KEY,
      conversation_id TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      version INTEGER NOT NULL,
      source_revision INTEGER NOT NULL,
      source_from_sequence INTEGER NOT NULL,
      source_through_sequence INTEGER NOT NULL,
      recent_tail_from_sequence INTEGER NOT NULL,
      entry_from_sequence INTEGER NOT NULL,
      entry_through_sequence INTEGER NOT NULL,
      payload_digest TEXT NOT NULL,
      estimated_input_tokens INTEGER,
      upstream_input_tokens INTEGER,
      request_body_bytes INTEGER NOT NULL,
      model_id TEXT NOT NULL,
      provider_code TEXT,
      provider_profile_id TEXT,
      endpoint_family TEXT NOT NULL,
      compact_compatibility_hash TEXT,
      prompt_version TEXT NOT NULL,
      status TEXT NOT NULL DEFAULT 'pending',
      quality_status TEXT NOT NULL,
      created_at TEXT NOT NULL,
      expires_at TEXT NOT NULL,
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
    );

    CREATE TABLE IF NOT EXISTS chat_context_entries (
      conversation_id TEXT NOT NULL,
      checkpoint_id TEXT NOT NULL,
      sequence INTEGER NOT NULL,
      source_message_id TEXT,
      kind TEXT NOT NULL,
      content_json TEXT NOT NULL,
      content_bytes INTEGER NOT NULL,
      provenance TEXT NOT NULL,
      trust_level TEXT NOT NULL,
      token_count INTEGER,
      created_at TEXT NOT NULL,
      expires_at TEXT NOT NULL,
      PRIMARY KEY (checkpoint_id, sequence),
      FOREIGN KEY (conversation_id) REFERENCES chat_conversations(id) ON DELETE CASCADE,
      FOREIGN KEY (checkpoint_id) REFERENCES chat_context_checkpoints(id) ON DELETE CASCADE,
      CHECK (sequence >= 1),
      CHECK (kind IN ('verbatim', 'durable_memory', 'task_state', 'tool_result', 'image_observation', 'provider_compaction')),
      CHECK (content_bytes >= 2),
      CHECK (provenance IN ('user', 'assistant', 'tool', 'asset', 'provider')),
      CHECK (trust_level IN ('untrusted', 'assistant_derived', 'provider_opaque')),
      CHECK (token_count IS NULL OR token_count >= 0)
    );

    CREATE TABLE IF NOT EXISTS chat_assets (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      conversation_id TEXT NOT NULL,
      source_kind TEXT NOT NULL DEFAULT 'user_upload',
      original_filename TEXT NOT NULL,
      original_mime_type TEXT NOT NULL,
      original_width INTEGER,
      original_height INTEGER,
      original_bytes INTEGER NOT NULL,
      original_sha256 TEXT NOT NULL,
      processed_mime_type TEXT,
      processed_width INTEGER,
      processed_height INTEGER,
      processed_bytes INTEGER,
      processed_sha256 TEXT,
      storage_key TEXT,
      preview_mime_type TEXT,
      preview_width INTEGER,
      preview_height INTEGER,
      preview_bytes INTEGER,
      preview_sha256 TEXT,
      preview_storage_key TEXT,
      processing_status TEXT NOT NULL DEFAULT 'pending',
      processing_error_code TEXT,
      observation_status TEXT NOT NULL DEFAULT 'not_requested',
      observation_json TEXT,
      observation_revision INTEGER NOT NULL DEFAULT 0,
      observation_claim_id TEXT,
      observation_claimed_at TEXT,
      quota_bytes INTEGER NOT NULL,
      turn_id TEXT,
      message_id TEXT,
      committed_at TEXT,
      cleanup_status TEXT NOT NULL DEFAULT 'active',
      cleanup_claim_id TEXT,
      cleanup_attempt_count INTEGER NOT NULL DEFAULT 0,
      cleanup_claimed_at TEXT,
      cleanup_retry_at TEXT,
      cleanup_error_code TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      expires_at TEXT NOT NULL,
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
    );

    CREATE TABLE IF NOT EXISTS chat_asset_references (
      asset_id TEXT NOT NULL,
      conversation_id TEXT NOT NULL,
      turn_id TEXT NOT NULL,
      message_id TEXT NOT NULL,
      reference_kind TEXT NOT NULL,
      content_order INTEGER NOT NULL,
      created_at TEXT NOT NULL,
      expires_at TEXT NOT NULL,
      FOREIGN KEY (asset_id, conversation_id) REFERENCES chat_assets(id, conversation_id) ON DELETE CASCADE,
      FOREIGN KEY (conversation_id) REFERENCES chat_conversations(id) ON DELETE CASCADE,
      UNIQUE (message_id, content_order),
      CHECK (reference_kind IN ('user_input', 'assistant_output')),
      CHECK (content_order >= 0)
    );

    CREATE TABLE IF NOT EXISTS chat_image_generations (
      asset_id TEXT PRIMARY KEY,
      conversation_id TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      operation TEXT NOT NULL,
      model TEXT NOT NULL,
      prompt TEXT NOT NULL,
      source_asset_ids_json TEXT NOT NULL DEFAULT '[]',
      root_asset_id TEXT NOT NULL,
      size TEXT NOT NULL,
      quality TEXT NOT NULL,
      output_format TEXT NOT NULL,
      created_at TEXT NOT NULL,
      expires_at TEXT NOT NULL,
      FOREIGN KEY (asset_id, conversation_id) REFERENCES chat_assets(id, conversation_id) ON DELETE CASCADE,
      FOREIGN KEY (root_asset_id, conversation_id) REFERENCES chat_assets(id, conversation_id) ON DELETE CASCADE,
      FOREIGN KEY (conversation_id) REFERENCES chat_conversations(id) ON DELETE CASCADE,
      CHECK (operation IN ('generate', 'edit')),
      CHECK (json_valid(source_asset_ids_json) AND json_type(source_asset_ids_json) = 'array')
    );

    CREATE INDEX IF NOT EXISTS idx_chat_conversations_owner_recent
      ON chat_conversations(system_account_id, last_message_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_chat_conversations_owner_pinned_recent
      ON chat_conversations(system_account_id, is_pinned DESC, last_message_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_chat_conversations_owner_api_key
      ON chat_conversations(system_account_id, api_key_id);
    CREATE INDEX IF NOT EXISTS idx_chat_conversations_active_started
      ON chat_conversations(active_started_at, id);
    CREATE INDEX IF NOT EXISTS idx_chat_conversations_context_queue
      ON chat_conversations(context_state, context_retry_at, context_claimed_at, updated_at, id);
    CREATE INDEX IF NOT EXISTS idx_chat_messages_conversation_sequence
      ON chat_messages(conversation_id, sequence_no DESC);
    CREATE INDEX IF NOT EXISTS idx_chat_messages_conversation_turn
      ON chat_messages(conversation_id, turn_id);
    CREATE INDEX IF NOT EXISTS idx_chat_messages_context
      ON chat_messages(system_account_id, conversation_id, status, expires_at, sequence_no DESC);
    CREATE INDEX IF NOT EXISTS idx_chat_messages_compaction_source
      ON chat_messages(conversation_id, system_account_id, status, sequence_no);
    CREATE INDEX IF NOT EXISTS idx_chat_messages_expiry
      ON chat_messages(expires_at, id);
    CREATE INDEX IF NOT EXISTS idx_chat_idempotency_expiry
      ON chat_message_idempotency(expires_at, conversation_id, client_message_id);
    CREATE INDEX IF NOT EXISTS idx_chat_context_checkpoints_conversation_version
      ON chat_context_checkpoints(conversation_id, version DESC, id DESC);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_chat_context_checkpoints_one_active
      ON chat_context_checkpoints(conversation_id) WHERE status = 'active';
    CREATE INDEX IF NOT EXISTS idx_chat_context_checkpoints_cleanup
      ON chat_context_checkpoints(expires_at, status, id);
    CREATE INDEX IF NOT EXISTS idx_chat_context_entries_conversation_checkpoint
      ON chat_context_entries(conversation_id, checkpoint_id, sequence);
    CREATE INDEX IF NOT EXISTS idx_chat_context_entries_expiry
      ON chat_context_entries(expires_at, checkpoint_id, sequence);
    CREATE INDEX IF NOT EXISTS idx_chat_assets_owner_conversation
      ON chat_assets(system_account_id, conversation_id, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_chat_assets_owner_lookup
      ON chat_assets(system_account_id, id, conversation_id);
    CREATE INDEX IF NOT EXISTS idx_chat_assets_message
      ON chat_assets(conversation_id, turn_id, message_id, id);
    CREATE INDEX IF NOT EXISTS idx_chat_assets_uncommitted
      ON chat_assets(system_account_id, conversation_id, expires_at, id)
      WHERE turn_id IS NULL AND message_id IS NULL
        AND processing_status IN ('pending', 'ready') AND cleanup_status = 'active';
    CREATE INDEX IF NOT EXISTS idx_chat_assets_cleanup
      ON chat_assets(cleanup_status, cleanup_retry_at, expires_at, id);
    CREATE INDEX IF NOT EXISTS idx_chat_asset_references_message
      ON chat_asset_references(conversation_id, message_id, content_order);
    CREATE INDEX IF NOT EXISTS idx_chat_asset_references_asset_valid
      ON chat_asset_references(asset_id, expires_at);
    CREATE INDEX IF NOT EXISTS idx_chat_asset_references_cleanup
      ON chat_asset_references(expires_at, asset_id, message_id);
    CREATE INDEX IF NOT EXISTS idx_chat_image_generations_conversation_recent
      ON chat_image_generations(conversation_id, created_at DESC, asset_id DESC);
    CREATE INDEX IF NOT EXISTS idx_chat_image_generations_expiry
      ON chat_image_generations(expires_at, asset_id);
`

const sqliteCodexContextDDL = `    CREATE TABLE IF NOT EXISTS codex_context_sessions (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      api_key_id TEXT,
      group_id TEXT NOT NULL,
      provider_code TEXT NOT NULL,
      source_response_id TEXT,
      latest_response_id TEXT,
      latest_compact_id TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      last_used_at TEXT NOT NULL,
      expires_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS codex_context_responses (
      response_id TEXT PRIMARY KEY,
      session_id TEXT NOT NULL,
      previous_response_id TEXT,
      system_account_id TEXT NOT NULL,
      api_key_id TEXT,
      group_id TEXT NOT NULL,
      provider_code TEXT NOT NULL,
      upstream_account_id TEXT,
      model TEXT,
      upstream_model TEXT,
      storage_key TEXT NOT NULL,
      storage_offset_bytes INTEGER NOT NULL,
      sha256 TEXT NOT NULL,
      raw_size_bytes INTEGER NOT NULL,
      compressed_size_bytes INTEGER NOT NULL,
      compression TEXT NOT NULL DEFAULT 'gzip',
      schema_version INTEGER NOT NULL DEFAULT 1,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      last_used_at TEXT NOT NULL,
      expires_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS codex_context_compacts (
      compact_id TEXT PRIMARY KEY,
      session_id TEXT NOT NULL,
      source_response_id TEXT,
      summary_digest TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      api_key_id TEXT,
      group_id TEXT NOT NULL,
      provider_code TEXT NOT NULL,
      upstream_account_id TEXT,
      model TEXT,
      upstream_model TEXT,
      storage_key TEXT NOT NULL,
      storage_offset_bytes INTEGER NOT NULL,
      sha256 TEXT NOT NULL,
      raw_size_bytes INTEGER NOT NULL,
      compressed_size_bytes INTEGER NOT NULL,
      compression TEXT NOT NULL DEFAULT 'gzip',
      schema_version INTEGER NOT NULL DEFAULT 1,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      last_used_at TEXT NOT NULL,
      expires_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS codex_context_storage_cleanup_queue (
      storage_key TEXT PRIMARY KEY,
      enqueued_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      next_attempt_at TEXT NOT NULL,
      attempt_count INTEGER NOT NULL DEFAULT 0,
      last_error TEXT
    );

    CREATE INDEX IF NOT EXISTS idx_codex_context_sessions_expires ON codex_context_sessions(expires_at ASC, id ASC);
    CREATE INDEX IF NOT EXISTS idx_codex_context_sessions_last_used ON codex_context_sessions(last_used_at ASC, id ASC);
    CREATE INDEX IF NOT EXISTS idx_codex_context_sessions_boundary ON codex_context_sessions(system_account_id, api_key_id, group_id, provider_code);
    CREATE INDEX IF NOT EXISTS idx_codex_context_responses_session ON codex_context_responses(session_id, created_at ASC, response_id);
    CREATE INDEX IF NOT EXISTS idx_codex_context_responses_previous ON codex_context_responses(previous_response_id) WHERE previous_response_id IS NOT NULL;
    CREATE INDEX IF NOT EXISTS idx_codex_context_responses_expires ON codex_context_responses(expires_at ASC, response_id);
    CREATE INDEX IF NOT EXISTS idx_codex_context_responses_boundary ON codex_context_responses(system_account_id, api_key_id, group_id, provider_code, response_id);
    CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_session ON codex_context_compacts(session_id, created_at ASC, compact_id);
    CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_source_response ON codex_context_compacts(source_response_id);
    CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_expires ON codex_context_compacts(expires_at ASC, compact_id);
    CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_boundary ON codex_context_compacts(system_account_id, api_key_id, group_id, provider_code, compact_id);
    CREATE INDEX IF NOT EXISTS idx_codex_context_storage_cleanup_due ON codex_context_storage_cleanup_queue(next_attempt_at ASC, enqueued_at ASC, storage_key ASC);
`

const sqliteDatasetDDL = `    CREATE TABLE IF NOT EXISTS public_api_logs (
          id TEXT PRIMARY KEY,
          trace_id TEXT,
          source_ref_id TEXT,
          source_name TEXT,
          token_id TEXT,
          token_name TEXT,
          token_prefix TEXT,
          is_test_token INTEGER NOT NULL DEFAULT 0,
          method TEXT NOT NULL,
          path TEXT NOT NULL,
          query_string TEXT,
          client_ip TEXT,
          user_agent TEXT,
          status_code INTEGER,
          success INTEGER NOT NULL DEFAULT 0,
          duration_ms INTEGER,
          request_size_bytes INTEGER NOT NULL DEFAULT 0,
          response_size_bytes INTEGER NOT NULL DEFAULT 0,
          request_capture_status TEXT NOT NULL DEFAULT 'empty',
          response_capture_status TEXT NOT NULL DEFAULT 'empty',
          request_data_json TEXT NOT NULL DEFAULT '{}',
          response_data_json TEXT NOT NULL DEFAULT '{}',
          error_code TEXT,
          error_message TEXT,
          started_at TEXT NOT NULL,
          ended_at TEXT NOT NULL,
          created_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS api_key_record_cleanup_targets (
          api_key_id TEXT PRIMARY KEY,
          system_account_id TEXT NOT NULL,
          created_at TEXT NOT NULL,
          updated_at TEXT NOT NULL,
          attempt_count INTEGER NOT NULL DEFAULT 0,
          last_attempt_at TEXT,
          last_blocked_reason TEXT,
          last_error_message TEXT
        );

    CREATE TABLE IF NOT EXISTS account_record_cleanup_targets (
          account_id TEXT PRIMARY KEY,
          system_account_id TEXT NOT NULL,
          related_account_ids_json TEXT NOT NULL DEFAULT '[]',
          authorization_ids_json TEXT NOT NULL DEFAULT '[]',
          team_scope_ids_json TEXT NOT NULL DEFAULT '[]',
          created_at TEXT NOT NULL,
          updated_at TEXT NOT NULL,
          attempt_count INTEGER NOT NULL DEFAULT 0,
          last_attempt_at TEXT,
          last_blocked_reason TEXT,
          last_error_message TEXT
        );

    CREATE INDEX IF NOT EXISTS idx_public_api_logs_created ON public_api_logs(created_at, id);

    CREATE INDEX IF NOT EXISTS idx_public_api_logs_source_created ON public_api_logs(source_ref_id, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_api_key_record_cleanup_targets_attempt ON api_key_record_cleanup_targets(COALESCE(last_attempt_at, created_at), created_at, api_key_id);

    CREATE INDEX IF NOT EXISTS idx_account_record_cleanup_targets_attempt ON account_record_cleanup_targets(COALESCE(last_attempt_at, created_at), created_at, account_id);
`

const sqliteUsageCatalogDDL = `    CREATE TABLE IF NOT EXISTS usage_record_shards (
          shard_key TEXT PRIMARY KEY,
          bucket_date TEXT NOT NULL,
          shard_id INTEGER NOT NULL,
          file_path TEXT NOT NULL,
          schema_version INTEGER NOT NULL DEFAULT 1,
          status TEXT NOT NULL DEFAULT 'active',
          first_seen_at TEXT NOT NULL,
          last_write_at TEXT,
          last_error_message TEXT,
          created_at TEXT NOT NULL,
          updated_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS usage_record_shard_entries (
          usage_id TEXT PRIMARY KEY,
          shard_key TEXT NOT NULL,
          system_account_id TEXT NOT NULL,
          trace_id TEXT NOT NULL,
          api_key_id TEXT,
          account_id TEXT,
          group_id TEXT,
          model TEXT,
          traffic_source TEXT NOT NULL,
          success INTEGER NOT NULL DEFAULT 0,
          status_code INTEGER,
          client_ip TEXT,
          first_token_ms INTEGER,
          duration_ms INTEGER,
          cost_usd REAL,
          created_at TEXT NOT NULL,
          indexed_at TEXT NOT NULL,
          FOREIGN KEY (shard_key) REFERENCES usage_record_shards(shard_key) ON DELETE CASCADE
        );

    CREATE TABLE IF NOT EXISTS usage_record_account_shards (
          account_id TEXT NOT NULL,
          shard_key TEXT NOT NULL,
          first_created_at TEXT NOT NULL,
          last_seen_at TEXT NOT NULL,
          PRIMARY KEY (account_id, shard_key),
          FOREIGN KEY (shard_key) REFERENCES usage_record_shards(shard_key) ON DELETE CASCADE
        );

    CREATE TABLE IF NOT EXISTS usage_record_api_key_shards (
          api_key_id TEXT NOT NULL,
          system_account_id TEXT NOT NULL,
          shard_key TEXT NOT NULL,
          first_created_at TEXT NOT NULL,
          last_seen_at TEXT NOT NULL,
          PRIMARY KEY (api_key_id, system_account_id, shard_key),
          FOREIGN KEY (shard_key) REFERENCES usage_record_shards(shard_key) ON DELETE CASCADE
        );

    CREATE INDEX IF NOT EXISTS idx_usage_record_shards_bucket ON usage_record_shards(bucket_date, shard_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_account_shards_account_created ON usage_record_account_shards(account_id, first_created_at, shard_key);
    CREATE INDEX IF NOT EXISTS idx_usage_record_api_key_shards_key_created ON usage_record_api_key_shards(api_key_id, system_account_id, first_created_at, shard_key);

    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_shard ON usage_record_shard_entries(shard_key, created_at);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_created_sort ON usage_record_shard_entries(created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_created_sort ON usage_record_shard_entries(system_account_id, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_trace_created_sort ON usage_record_shard_entries(system_account_id, trace_id, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_api_key_created_sort ON usage_record_shard_entries(system_account_id, api_key_id, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_group_created_sort ON usage_record_shard_entries(system_account_id, group_id, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_account_created_sort ON usage_record_shard_entries(system_account_id, account_id, created_at, usage_id);
`

// sqliteScript is an ordered list of statements (DDL blocks and pragmas)
// mirroring one Node apply*Schema function.
type sqliteScript []string

// sqliteBusinessScript mirrors applyBusinessSchema in business-schema.ts.
var sqliteBusinessScript = sqliteScript{
	"PRAGMA foreign_keys = ON;",
	sqliteBusinessMainDDL,
	sqliteBusinessCircuitControlPlaneIndexDDL,
	sqliteBusinessResponseInspectionPolicyIndexesDDL,
	sqliteBusinessExternalIntegrationSourceIndexesDDL,
	sqliteBusinessOIDCProviderDDL,
	sqliteBusinessAuthorizationInstanceIndexesDDL,
}

// sqliteStatsScript mirrors applyStatsSchema in stats-schema.ts.
var sqliteStatsScript = sqliteScript{
	sqliteStatsDirtyIPsDDL,
	"PRAGMA foreign_keys = ON;",
	sqliteStatsMainDDL,
	sqliteStatsUsageCleanupDeductionsIndexDDL,
}

// sqliteChatScript mirrors applyChatSchema in chat-schema.ts.
var sqliteChatScript = sqliteScript{
	"PRAGMA foreign_keys = ON;",
	sqliteChatDDL,
}

// sqliteCodexContextScript mirrors applyCodexContextStateSchema in codex-context-state-schema.ts.
var sqliteCodexContextScript = sqliteScript{
	sqliteCodexContextDDL,
}

// sqliteDatasetScript mirrors applyDatasetSchema in dataset-schema.ts.
var sqliteDatasetScript = sqliteScript{
	"PRAGMA foreign_keys = ON;",
	sqliteDatasetDDL,
}

// sqliteUsageCatalogScript mirrors applyUsageCatalogSchema in usage-catalog-schema.ts.
var sqliteUsageCatalogScript = sqliteScript{
	"PRAGMA foreign_keys = ON;",
	sqliteUsageCatalogDDL,
}

// counts reports how many CREATE TABLE and CREATE INDEX statements the script
// ensures. On a fresh database this equals the number of tables and indexes
// created. SQL comments are stripped before counting so statements that the
// Node source keeps inside block comments are excluded.
func (s sqliteScript) counts() SchemaCounts {
	var counts SchemaCounts
	for _, part := range s {
		tables, indexes := countDDLStatements(part)
		counts.Tables += tables
		counts.Indexes += indexes
	}
	return counts
}

var (
	sqliteSQLBlockComment = regexp.MustCompile("(?s)/\\*.*?\\*/")
	sqliteSQLLineComment  = regexp.MustCompile("--[^\\n]*")
)

// countDDLStatements counts CREATE TABLE and CREATE INDEX statements in one
// DDL chunk, ignoring statements that only exist inside SQL comments.
func countDDLStatements(part string) (tables, indexes int) {
	stripped := sqliteSQLLineComment.ReplaceAllString(sqliteSQLBlockComment.ReplaceAllString(part, ""), "")
	tables = strings.Count(stripped, "CREATE TABLE ")
	indexes = strings.Count(stripped, "CREATE INDEX ") + strings.Count(stripped, "CREATE UNIQUE INDEX ")
	return tables, indexes
}

// ensure executes the script statements in order and returns their counts.
func (s sqliteScript) ensure(ctx context.Context, db *sql.DB) (SchemaCounts, error) {
	for i, part := range s {
		if _, err := db.ExecContext(ctx, part); err != nil {
			return SchemaCounts{}, fmt.Errorf("sqlite schema statement %d: %w", i, err)
		}
	}
	return s.counts(), nil
}

// SchemaCounts reports how many CREATE TABLE and CREATE INDEX statements were
// ensured for one schema. On a fresh database this equals the number of
// tables and indexes created.
type SchemaCounts struct {
	Tables  int
	Indexes int
}

// SQLiteResult summarizes EnsureAllSQLite: the DDL statement counts applied
// for every SQLite schema.
type SQLiteResult struct {
	Business     SchemaCounts
	Stats        SchemaCounts
	Chat         SchemaCounts
	CodexContext SchemaCounts
	Dataset      SchemaCounts
	UsageCatalog SchemaCounts
}

// EnsureSQLiteBusiness applies the business schema (system accounts, providers, accounts, groups, routing, API keys, authorizations, OIDC, circuit control plane).
func EnsureSQLiteBusiness(ctx context.Context, db *sql.DB) (SchemaCounts, error) {
	return sqliteBusinessScript.ensure(ctx, db)
}

// EnsureSQLiteStats applies the stats schema (quality/usage aggregations and process samples).
func EnsureSQLiteStats(ctx context.Context, db *sql.DB) (SchemaCounts, error) {
	return sqliteStatsScript.ensure(ctx, db)
}

// EnsureSQLiteChat applies the chat schema (conversations, messages, assets, context checkpoints).
func EnsureSQLiteChat(ctx context.Context, db *sql.DB) (SchemaCounts, error) {
	return sqliteChatScript.ensure(ctx, db)
}

// EnsureSQLiteCodexContext applies the codex context state schema.
func EnsureSQLiteCodexContext(ctx context.Context, db *sql.DB) (SchemaCounts, error) {
	return sqliteCodexContextScript.ensure(ctx, db)
}

// EnsureSQLiteDataset applies the dataset schema (public API logs and record cleanup targets).
func EnsureSQLiteDataset(ctx context.Context, db *sql.DB) (SchemaCounts, error) {
	return sqliteDatasetScript.ensure(ctx, db)
}

// EnsureSQLiteUsageCatalog applies the usage catalog schema (usage record shards).
func EnsureSQLiteUsageCatalog(ctx context.Context, db *sql.DB) (SchemaCounts, error) {
	return sqliteUsageCatalogScript.ensure(ctx, db)
}

// EnsureAllSQLite applies all six SQLite schemas to db in dependency order
// (business first, then stats, chat, codex context state, dataset and usage
// catalog) and returns a per-schema summary. Every statement uses
// IF NOT EXISTS, so repeated calls are idempotent.
func EnsureAllSQLite(ctx context.Context, db *sql.DB) (SQLiteResult, error) {
	var result SQLiteResult
	var err error
	if result.Business, err = EnsureSQLiteBusiness(ctx, db); err != nil {
		return SQLiteResult{}, fmt.Errorf("ensure sqlite business schema: %w", err)
	}
	if result.Stats, err = EnsureSQLiteStats(ctx, db); err != nil {
		return SQLiteResult{}, fmt.Errorf("ensure sqlite stats schema: %w", err)
	}
	if result.Chat, err = EnsureSQLiteChat(ctx, db); err != nil {
		return SQLiteResult{}, fmt.Errorf("ensure sqlite chat schema: %w", err)
	}
	if result.CodexContext, err = EnsureSQLiteCodexContext(ctx, db); err != nil {
		return SQLiteResult{}, fmt.Errorf("ensure sqlite codex context schema: %w", err)
	}
	if result.Dataset, err = EnsureSQLiteDataset(ctx, db); err != nil {
		return SQLiteResult{}, fmt.Errorf("ensure sqlite dataset schema: %w", err)
	}
	if result.UsageCatalog, err = EnsureSQLiteUsageCatalog(ctx, db); err != nil {
		return SQLiteResult{}, fmt.Errorf("ensure sqlite usage catalog schema: %w", err)
	}
	return result, nil
}
