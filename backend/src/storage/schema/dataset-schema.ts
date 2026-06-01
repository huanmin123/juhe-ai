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
          profile TEXT NOT NULL DEFAULT 'full',
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

    CREATE TABLE IF NOT EXISTS audit_logs (
          id TEXT PRIMARY KEY,
          trace_id TEXT NOT NULL,
          traffic_source TEXT NOT NULL,
          system_account_id TEXT,
          api_key_id TEXT,
          group_id TEXT,
          account_id TEXT,
          provider_code TEXT,
          method TEXT NOT NULL,
          path TEXT NOT NULL,
          query_string TEXT,
          model TEXT,
          stream INTEGER NOT NULL DEFAULT 0,
          client_ip TEXT,
          user_agent TEXT,
          audit_outcome TEXT NOT NULL,
          success INTEGER NOT NULL DEFAULT 0,
          final_status_code INTEGER,
          error_phase TEXT,
          error_code TEXT,
          error_message TEXT,
          sample_bucket INTEGER NOT NULL,
          sample_reason TEXT NOT NULL,
          attempt_count INTEGER NOT NULL DEFAULT 0,
          payload_count INTEGER NOT NULL DEFAULT 0,
          raw_payload_bytes INTEGER NOT NULL DEFAULT 0,
          compressed_payload_bytes INTEGER NOT NULL DEFAULT 0,
          compression_saved_bytes INTEGER NOT NULL DEFAULT 0,
          error_group_id TEXT,
          capture_status TEXT NOT NULL DEFAULT 'complete',
          started_at TEXT NOT NULL,
          ended_at TEXT NOT NULL,
          duration_ms INTEGER,
          first_token_ms INTEGER,
          created_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS audit_log_attempts (
          id TEXT PRIMARY KEY,
          audit_log_id TEXT NOT NULL,
          attempt_index INTEGER NOT NULL,
          account_id TEXT,
          account_owner_system_account_id TEXT,
          group_id TEXT,
          proxy_url TEXT,
          provider_code TEXT,
          upstream_method TEXT NOT NULL,
          upstream_url TEXT NOT NULL,
          upstream_status_code INTEGER,
          success INTEGER NOT NULL DEFAULT 0,
          error_phase TEXT,
          error_code TEXT,
          error_message TEXT,
          started_at TEXT NOT NULL,
          ended_at TEXT,
          duration_ms INTEGER,
          FOREIGN KEY (audit_log_id) REFERENCES audit_logs(id) ON DELETE CASCADE
        );

    CREATE TABLE IF NOT EXISTS audit_payload_blobs (
          id TEXT PRIMARY KEY,
          sha256 TEXT NOT NULL,
          raw_size_bytes INTEGER NOT NULL DEFAULT 0,
          compressed_size_bytes INTEGER NOT NULL DEFAULT 0,
          content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
          content_encoding TEXT,
          compression TEXT NOT NULL DEFAULT 'none',
          storage_key TEXT NOT NULL,
          ref_count INTEGER NOT NULL DEFAULT 0,
          first_seen_at TEXT NOT NULL,
          last_seen_at TEXT NOT NULL,
          created_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS audit_payload_refs (
          id TEXT PRIMARY KEY,
          audit_log_id TEXT NOT NULL,
          attempt_id TEXT,
          part_type TEXT NOT NULL,
          sequence_index INTEGER NOT NULL DEFAULT 0,
          content_type TEXT,
          content_encoding TEXT,
          headers_blob_id TEXT,
          body_blob_id TEXT,
          headers_sha256 TEXT,
          body_sha256 TEXT,
          raw_size_bytes INTEGER NOT NULL DEFAULT 0,
          compressed_size_bytes INTEGER NOT NULL DEFAULT 0,
          capture_status TEXT NOT NULL DEFAULT 'complete',
          created_at TEXT NOT NULL,
          FOREIGN KEY (audit_log_id) REFERENCES audit_logs(id) ON DELETE CASCADE,
          FOREIGN KEY (attempt_id) REFERENCES audit_log_attempts(id) ON DELETE SET NULL,
          FOREIGN KEY (headers_blob_id) REFERENCES audit_payload_blobs(id) ON DELETE SET NULL,
          FOREIGN KEY (body_blob_id) REFERENCES audit_payload_blobs(id) ON DELETE SET NULL
        );

    CREATE TABLE IF NOT EXISTS audit_error_groups (
          id TEXT PRIMARY KEY,
          fingerprint TEXT NOT NULL,
          window_started_at TEXT NOT NULL,
          window_ended_at TEXT NOT NULL,
          system_account_id TEXT,
          api_key_id TEXT,
          group_id TEXT,
          account_id TEXT,
          provider_code TEXT,
          path TEXT,
          model TEXT,
          status_code INTEGER,
          error_phase TEXT,
          error_code TEXT,
          error_type TEXT,
          request_fingerprint TEXT,
          error_fingerprint TEXT,
          count INTEGER NOT NULL DEFAULT 0,
          first_event_id TEXT,
          last_event_id TEXT,
          sample_event_id TEXT,
          last_message TEXT,
          created_at TEXT NOT NULL,
          updated_at TEXT NOT NULL,
          UNIQUE(fingerprint, window_started_at)
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

    CREATE TABLE IF NOT EXISTS operation_log_search_terms (
          operation_log_id TEXT NOT NULL,
          term TEXT NOT NULL,
          created_at TEXT NOT NULL,
          PRIMARY KEY (term, operation_log_id),
          FOREIGN KEY (operation_log_id) REFERENCES operation_logs(id) ON DELETE CASCADE
        );

    CREATE TABLE IF NOT EXISTS runtime_logs (
          id TEXT PRIMARY KEY,
          log_file TEXT,
          log_offset INTEGER,
          line_number INTEGER,
          time TEXT NOT NULL,
          level TEXT NOT NULL,
          trace_id TEXT,
          event TEXT,
          message TEXT,
          error_message TEXT,
          raw_json TEXT NOT NULL,
          created_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS runtime_log_file_cursors (
          log_file TEXT PRIMARY KEY,
          file_identity TEXT,
          cursor_offset INTEGER NOT NULL DEFAULT 0,
          line_number INTEGER NOT NULL DEFAULT 0,
          file_size INTEGER NOT NULL DEFAULT 0,
          file_mtime_ms INTEGER,
          last_read_at TEXT,
          last_error_message TEXT,
          created_at TEXT NOT NULL,
          updated_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS runtime_log_facet_summary (
          bucket_key TEXT PRIMARY KEY,
          total_count INTEGER NOT NULL DEFAULT 0,
          earliest_time TEXT,
          latest_time TEXT,
          updated_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS runtime_log_level_facets (
          bucket_key TEXT NOT NULL,
          level TEXT NOT NULL,
          count INTEGER NOT NULL DEFAULT 0,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (bucket_key, level)
        );

    CREATE TABLE IF NOT EXISTS runtime_log_event_facets (
          bucket_key TEXT NOT NULL,
          event TEXT NOT NULL,
          count INTEGER NOT NULL DEFAULT 0,
          latest_time TEXT,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (bucket_key, event)
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
          authorization_ids_json TEXT NOT NULL DEFAULT '[]',
          team_scope_ids_json TEXT NOT NULL DEFAULT '[]',
          created_at TEXT NOT NULL,
          updated_at TEXT NOT NULL,
          attempt_count INTEGER NOT NULL DEFAULT 0,
          last_attempt_at TEXT,
          last_blocked_reason TEXT,
          last_error_message TEXT
        );

    CREATE TABLE IF NOT EXISTS usage_record_shards (
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

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_created ON model_check_runs(created_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_created ON model_check_runs(system_account_id, created_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_actor_created ON model_check_runs(actor_system_account_id, created_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_model_created ON model_check_runs(model, created_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_level_created ON model_check_runs(level, created_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_status_created ON model_check_runs(status, created_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_target_created ON model_check_runs(target_type, target_id, created_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_model_created ON model_check_runs(system_account_id, model, created_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_level_created ON model_check_runs(system_account_id, level, created_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_status_created ON model_check_runs(system_account_id, status, created_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_target_created ON model_check_runs(system_account_id, target_type, target_id, created_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_model_check_items_run_order ON model_check_items(run_id, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_model_check_items_run_key ON model_check_items(run_id, item_key, id);

    CREATE INDEX IF NOT EXISTS idx_model_check_items_run_status ON model_check_items(run_id, status, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON audit_logs(created_at, id);

    CREATE INDEX IF NOT EXISTS idx_audit_logs_trace_id ON audit_logs(trace_id);

    CREATE INDEX IF NOT EXISTS idx_audit_logs_system_account_created ON audit_logs(system_account_id, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_audit_logs_outcome_created ON audit_logs(audit_outcome, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_audit_logs_status_created ON audit_logs(final_status_code, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_audit_logs_path_created ON audit_logs(path, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_audit_logs_model_created ON audit_logs(model, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_audit_logs_client_ip_created ON audit_logs(client_ip, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_audit_logs_api_key_created ON audit_logs(api_key_id, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_audit_logs_group_created ON audit_logs(group_id, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_audit_logs_account_created ON audit_logs(account_id, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_audit_logs_error_group_created ON audit_logs(error_group_id, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_audit_log_attempts_log_index ON audit_log_attempts(audit_log_id, attempt_index);

    CREATE UNIQUE INDEX IF NOT EXISTS idx_audit_payload_blobs_unique ON audit_payload_blobs(sha256, raw_size_bytes, content_type);

    CREATE INDEX IF NOT EXISTS idx_audit_payload_blobs_created ON audit_payload_blobs(created_at, id);

    CREATE INDEX IF NOT EXISTS idx_audit_payload_refs_log_part ON audit_payload_refs(audit_log_id, part_type, sequence_index);

    CREATE INDEX IF NOT EXISTS idx_audit_payload_refs_log_sequence ON audit_payload_refs(audit_log_id, sequence_index);

    CREATE INDEX IF NOT EXISTS idx_audit_payload_refs_headers_blob ON audit_payload_refs(headers_blob_id);

    CREATE INDEX IF NOT EXISTS idx_audit_payload_refs_body_blob ON audit_payload_refs(body_blob_id);

    CREATE INDEX IF NOT EXISTS idx_audit_error_groups_window ON audit_error_groups(window_started_at, id);

    CREATE INDEX IF NOT EXISTS idx_audit_error_groups_fingerprint_window ON audit_error_groups(fingerprint, window_started_at);

    CREATE INDEX IF NOT EXISTS idx_audit_error_groups_updated ON audit_error_groups(updated_at, id);

    CREATE INDEX IF NOT EXISTS idx_audit_error_groups_path_updated ON audit_error_groups(path, updated_at, id);

    CREATE INDEX IF NOT EXISTS idx_audit_error_groups_model_updated ON audit_error_groups(model, updated_at, id);

    CREATE INDEX IF NOT EXISTS idx_audit_error_groups_status_updated ON audit_error_groups(status_code, updated_at, id);

    CREATE INDEX IF NOT EXISTS idx_audit_error_groups_api_key_account ON audit_error_groups(api_key_id, system_account_id);

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

    CREATE INDEX IF NOT EXISTS idx_operation_log_search_terms_term_created ON operation_log_search_terms(term, created_at DESC, operation_log_id DESC);

    CREATE INDEX IF NOT EXISTS idx_operation_log_search_terms_log ON operation_log_search_terms(operation_log_id);

    CREATE INDEX IF NOT EXISTS idx_runtime_logs_time ON runtime_logs(time DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_runtime_logs_trace_id_time ON runtime_logs(trace_id, time DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_runtime_logs_level_time ON runtime_logs(level, time DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_runtime_logs_event_time ON runtime_logs(event, time DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_runtime_logs_created_at ON runtime_logs(created_at);

    CREATE INDEX IF NOT EXISTS idx_runtime_logs_created_id ON runtime_logs(created_at, id);

    CREATE INDEX IF NOT EXISTS idx_runtime_log_file_cursors_updated ON runtime_log_file_cursors(updated_at);

    CREATE INDEX IF NOT EXISTS idx_runtime_log_facet_summary_latest ON runtime_log_facet_summary(latest_time);

    CREATE INDEX IF NOT EXISTS idx_runtime_log_event_facets_latest ON runtime_log_event_facets(latest_time DESC, event);

    CREATE INDEX IF NOT EXISTS idx_api_key_record_cleanup_targets_attempt ON api_key_record_cleanup_targets(COALESCE(last_attempt_at, created_at), created_at, api_key_id);

    CREATE INDEX IF NOT EXISTS idx_account_record_cleanup_targets_attempt ON account_record_cleanup_targets(COALESCE(last_attempt_at, created_at), created_at, account_id);

    CREATE INDEX IF NOT EXISTS idx_usage_record_shards_bucket ON usage_record_shards(bucket_date, shard_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_account_shards_account_created ON usage_record_account_shards(account_id, first_created_at, shard_key);
    CREATE INDEX IF NOT EXISTS idx_usage_record_api_key_shards_key_created ON usage_record_api_key_shards(api_key_id, system_account_id, first_created_at, shard_key);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_shard ON usage_record_shard_entries(shard_key, created_at);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_created_sort ON usage_record_shard_entries(created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_created_sort ON usage_record_shard_entries(system_account_id, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_api_key_created_sort ON usage_record_shard_entries(api_key_id, system_account_id, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_group_created_sort ON usage_record_shard_entries(group_id, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_group_created_sort ON usage_record_shard_entries(system_account_id, group_id, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_model_created_sort ON usage_record_shard_entries(model, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_model_created_sort ON usage_record_shard_entries(system_account_id, model, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_client_ip_created_sort ON usage_record_shard_entries(client_ip, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_client_ip_created_sort ON usage_record_shard_entries(system_account_id, client_ip, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_account_created_sort ON usage_record_shard_entries(account_id, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_account_created_sort ON usage_record_shard_entries(system_account_id, account_id, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_traffic_source_created_sort ON usage_record_shard_entries(traffic_source, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_success_created_sort ON usage_record_shard_entries(success, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_success_created_sort ON usage_record_shard_entries(system_account_id, success, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_status_created_sort ON usage_record_shard_entries(status_code, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_status_created_sort ON usage_record_shard_entries(system_account_id, status_code, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_first_token_sort ON usage_record_shard_entries(first_token_ms, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_duration_sort ON usage_record_shard_entries(duration_ms, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_cost_sort ON usage_record_shard_entries(cost_usd, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_first_token_sort ON usage_record_shard_entries(system_account_id, first_token_ms, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_duration_sort ON usage_record_shard_entries(system_account_id, duration_ms, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_cost_sort ON usage_record_shard_entries(system_account_id, cost_usd, created_at, usage_id);
  `)
  database.exec(`
    CREATE INDEX IF NOT EXISTS idx_audit_logs_traffic_source_created ON audit_logs(traffic_source, created_at, id);
  `)
}
