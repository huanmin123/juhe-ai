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

    CREATE INDEX IF NOT EXISTS idx_api_key_record_cleanup_targets_attempt ON api_key_record_cleanup_targets(COALESCE(last_attempt_at, created_at), created_at, api_key_id);

    CREATE INDEX IF NOT EXISTS idx_account_record_cleanup_targets_attempt ON account_record_cleanup_targets(COALESCE(last_attempt_at, created_at), created_at, account_id);

  `)
}
