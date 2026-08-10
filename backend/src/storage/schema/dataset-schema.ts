import type { DatabaseSync } from 'node:sqlite'


export function applyDatasetSchema(database: DatabaseSync): void {
  database.exec(`
    PRAGMA foreign_keys = ON;

    PRAGMA journal_mode = WAL;

    CREATE TABLE IF NOT EXISTS model_check_runs (
          id TEXT PRIMARY KEY,
          system_account_id TEXT NOT NULL,
          actor_system_account_id TEXT NOT NULL,
          provider_code TEXT NOT NULL,
          target_type TEXT NOT NULL,
          target_id TEXT NOT NULL,
          target_name TEXT,
          target_owner_system_account_id TEXT,
          account_id TEXT,
          group_id TEXT,
          api_key_id TEXT,
          model TEXT NOT NULL,
          profile TEXT NOT NULL DEFAULT 'quick',
          trigger_kind TEXT NOT NULL DEFAULT 'manual' CHECK (trigger_kind IN ('manual', 'scheduled', 'quality_recovery')),
          schedule_id TEXT,
          trusted_comparison_enabled INTEGER NOT NULL DEFAULT 0,
          trusted_comparison_available INTEGER NOT NULL DEFAULT 0,
          level TEXT NOT NULL DEFAULT 'unavailable',
          score INTEGER NOT NULL DEFAULT 0,
          max_score INTEGER NOT NULL DEFAULT 100,
          status TEXT NOT NULL DEFAULT 'running',
          message TEXT NOT NULL DEFAULT '',
          trace_id TEXT,
          probe_set_version TEXT NOT NULL DEFAULT 'openai-model-check-v1',
          started_at TEXT NOT NULL,
          finished_at TEXT,
          duration_ms INTEGER,
          request_summary_json TEXT NOT NULL DEFAULT '{}',
          result_summary_json TEXT NOT NULL DEFAULT '{}',
          policy_snapshot_json TEXT NOT NULL DEFAULT '{}',
          quality_decision_json TEXT NOT NULL DEFAULT '{}',
          quality_health_sync_status TEXT CHECK (quality_health_sync_status IS NULL OR quality_health_sync_status IN ('applied', 'pending_retry', 'failed')),
          error_code TEXT,
          error_message TEXT,
          created_at TEXT NOT NULL,
          updated_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS model_check_items (
          id TEXT PRIMARY KEY,
          run_id TEXT NOT NULL,
          item_key TEXT NOT NULL,
          item_type TEXT NOT NULL,
          status TEXT NOT NULL,
          score INTEGER NOT NULL DEFAULT 0,
          max_score INTEGER NOT NULL DEFAULT 0,
          duration_ms INTEGER,
          trace_id TEXT,
          evidence_summary_json TEXT NOT NULL DEFAULT '{}',
          error_code TEXT,
          error_message TEXT,
          created_at TEXT NOT NULL,
          updated_at TEXT NOT NULL,
          FOREIGN KEY (run_id) REFERENCES model_check_runs(id) ON DELETE CASCADE
        );

    CREATE TABLE IF NOT EXISTS model_check_observations (
          id TEXT PRIMARY KEY,
          run_id TEXT NOT NULL,
          system_account_id TEXT NOT NULL,
          account_id TEXT NOT NULL,
          provider_code TEXT NOT NULL,
          provider_protocol_profile_id TEXT NOT NULL,
          endpoint_family TEXT NOT NULL,
          requested_model TEXT NOT NULL,
          mapped_upstream_model TEXT NOT NULL,
          observed_model TEXT,
          mapping_applied INTEGER NOT NULL DEFAULT 0,
          upstream_bucket_hmac TEXT NOT NULL,
          cohort_key_hmac TEXT NOT NULL,
          population_key_hmac TEXT NOT NULL,
          probe_key_hmac TEXT NOT NULL,
          system_fingerprint_hmac TEXT,
          probe_family TEXT NOT NULL,
          probe_set_version TEXT NOT NULL,
          tokenizer_version TEXT NOT NULL,
          feature_version TEXT NOT NULL DEFAULT 'none',
          round_index INTEGER NOT NULL,
          padding_tokens INTEGER NOT NULL,
          local_input_tokens INTEGER NOT NULL,
          reported_input_tokens INTEGER,
          cached_input_tokens INTEGER,
          constraint_passed INTEGER,
          feature_1 REAL,
          feature_2 REAL,
          feature_3 REAL,
          feature_4 REAL,
          feature_5 REAL,
          feature_6 REAL,
          feature_7 REAL,
          feature_8 REAL,
          observation_status TEXT NOT NULL,
          identity_status TEXT NOT NULL,
          mapping_status TEXT NOT NULL,
          protocol_status TEXT NOT NULL,
          evidence_coverage INTEGER NOT NULL DEFAULT 0,
          trace_id TEXT,
          created_at TEXT NOT NULL,
          aggregation_completed_at TEXT,
          FOREIGN KEY (run_id) REFERENCES model_check_runs(id) ON DELETE CASCADE
        );

    CREATE TABLE IF NOT EXISTS public_api_logs (
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

    CREATE TABLE IF NOT EXISTS operation_logs (
          id TEXT PRIMARY KEY,
          trace_id TEXT,
          actor_system_account_id TEXT NOT NULL,
          actor_username TEXT,
          actor_display_name TEXT,
          actor_role TEXT NOT NULL,
          operation_scope_system_account_id TEXT,
          mode TEXT NOT NULL DEFAULT 'self',
          module TEXT NOT NULL,
          action TEXT NOT NULL,
          operation_key TEXT NOT NULL,
          resource_type TEXT NOT NULL,
          resource_id TEXT,
          resource_name TEXT,
          summary TEXT NOT NULL,
          detail_level TEXT NOT NULL DEFAULT 'full',
          visibility_scope TEXT NOT NULL DEFAULT 'targeted',
          changes_json TEXT NOT NULL DEFAULT '[]',
          metadata_json TEXT NOT NULL DEFAULT '{}',
          method TEXT,
          path TEXT,
          status_code INTEGER,
          client_ip TEXT,
          user_agent TEXT,
          created_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS operation_log_targets (
          id TEXT PRIMARY KEY,
          operation_log_id TEXT NOT NULL,
          target_type TEXT NOT NULL,
          target_id TEXT,
          target_name TEXT,
          target_owner_system_account_id TEXT,
          relation TEXT NOT NULL DEFAULT 'affected',
          created_at TEXT NOT NULL,
          FOREIGN KEY (operation_log_id) REFERENCES operation_logs(id) ON DELETE CASCADE
        );

    CREATE TABLE IF NOT EXISTS operation_log_viewers (
          operation_log_id TEXT NOT NULL,
          system_account_id TEXT NOT NULL,
          visibility_reason TEXT NOT NULL,
          detail_level TEXT NOT NULL DEFAULT 'full',
          created_at TEXT NOT NULL,
          PRIMARY KEY (operation_log_id, system_account_id, visibility_reason),
          FOREIGN KEY (operation_log_id) REFERENCES operation_logs(id) ON DELETE CASCADE
        );

    CREATE TABLE IF NOT EXISTS operation_log_summary_search_terms (
          operation_log_id TEXT NOT NULL,
          term TEXT NOT NULL,
          created_at TEXT NOT NULL,
          PRIMARY KEY (term, operation_log_id),
          FOREIGN KEY (operation_log_id) REFERENCES operation_logs(id) ON DELETE CASCADE
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

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_created ON model_check_runs(created_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_created ON model_check_runs(system_account_id, created_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_actor_created ON model_check_runs(actor_system_account_id, created_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_model_created ON model_check_runs(model, created_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_level_created ON model_check_runs(level, created_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_status_created ON model_check_runs(status, created_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_target_created ON model_check_runs(target_type, target_id, created_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_account_created ON model_check_runs(account_id, created_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_trigger_created ON model_check_runs(trigger_kind, created_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_quality_health_sync_retry
      ON model_check_runs(quality_health_sync_status, updated_at, id)
      WHERE quality_health_sync_status = 'failed';

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_model_created ON model_check_runs(system_account_id, model, created_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_level_created ON model_check_runs(system_account_id, level, created_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_status_created ON model_check_runs(system_account_id, status, created_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_target_created ON model_check_runs(system_account_id, target_type, target_id, created_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_model_check_items_run_order ON model_check_items(run_id, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_model_check_items_run_key ON model_check_items(run_id, item_key, id);

    CREATE INDEX IF NOT EXISTS idx_model_check_items_run_status ON model_check_items(run_id, status, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_model_check_observations_cursor ON model_check_observations(created_at, id);

    CREATE INDEX IF NOT EXISTS idx_model_check_observations_pending_aggregation
      ON model_check_observations(created_at, id)
      WHERE aggregation_completed_at IS NULL;

    CREATE INDEX IF NOT EXISTS idx_model_check_observations_account_model ON model_check_observations(system_account_id, account_id, requested_model, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_model_check_observations_cohort ON model_check_observations(cohort_key_hmac, mapped_upstream_model, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_model_check_observations_population ON model_check_observations(population_key_hmac, requested_model, probe_family, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_public_api_logs_created ON public_api_logs(created_at, id);

    CREATE INDEX IF NOT EXISTS idx_public_api_logs_source_created ON public_api_logs(source_ref_id, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_operation_logs_created ON operation_logs(created_at, id);

    CREATE INDEX IF NOT EXISTS idx_operation_logs_actor_created ON operation_logs(actor_system_account_id, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_operation_logs_scope_created ON operation_logs(operation_scope_system_account_id, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_operation_logs_module_action_created ON operation_logs(module, action, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_operation_logs_resource_created ON operation_logs(resource_type, resource_id, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_operation_logs_resource_id_created ON operation_logs(resource_id, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_operation_logs_visibility_created ON operation_logs(visibility_scope, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_operation_logs_trace_id ON operation_logs(trace_id);

    CREATE INDEX IF NOT EXISTS idx_operation_logs_trace_created ON operation_logs(trace_id, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_operation_log_targets_target ON operation_log_targets(target_type, target_id, created_at);

    CREATE INDEX IF NOT EXISTS idx_operation_log_targets_log_created ON operation_log_targets(operation_log_id, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_operation_log_viewers_account_created ON operation_log_viewers(system_account_id, created_at, operation_log_id);

    CREATE INDEX IF NOT EXISTS idx_operation_log_viewers_account_log ON operation_log_viewers(system_account_id, operation_log_id);

    CREATE INDEX IF NOT EXISTS idx_operation_log_viewers_log_account ON operation_log_viewers(operation_log_id, system_account_id);

    CREATE INDEX IF NOT EXISTS idx_operation_log_summary_search_terms_term_created ON operation_log_summary_search_terms(term, created_at DESC, operation_log_id DESC);

    CREATE INDEX IF NOT EXISTS idx_operation_log_summary_search_terms_log ON operation_log_summary_search_terms(operation_log_id);

    CREATE INDEX IF NOT EXISTS idx_api_key_record_cleanup_targets_attempt ON api_key_record_cleanup_targets(COALESCE(last_attempt_at, created_at), created_at, api_key_id);

    CREATE INDEX IF NOT EXISTS idx_account_record_cleanup_targets_attempt ON account_record_cleanup_targets(COALESCE(last_attempt_at, created_at), created_at, account_id);

  `)
}
