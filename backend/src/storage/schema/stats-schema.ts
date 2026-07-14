import type { DatabaseSync } from 'node:sqlite'

export function applyStatsSchema(database: DatabaseSync): void {
  database.exec(`
    PRAGMA foreign_keys = ON;

    PRAGMA journal_mode = WAL;

    CREATE TABLE IF NOT EXISTS account_quality_minute_stats (
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

    CREATE INDEX IF NOT EXISTS idx_model_account_trust_results_updated ON model_account_trust_results(updated_at, account_id, requested_model);

    CREATE INDEX IF NOT EXISTS idx_model_token_integrity_windows_cohort ON model_token_integrity_windows(cohort_key_hmac, requested_model, updated_at);

    CREATE INDEX IF NOT EXISTS idx_model_token_integrity_rounds_account ON model_token_integrity_rounds(account_id, requested_model, updated_at);

    CREATE INDEX IF NOT EXISTS idx_model_token_intercept_baseline_active ON model_token_intercept_baseline_versions(cohort_key_hmac, requested_model, tokenizer_version, probe_set_version, version_status, baseline_version);

    CREATE INDEX IF NOT EXISTS idx_model_trust_window_sources_cohort ON model_trust_window_sources(cohort_key_hmac, upstream_bucket_hmac);

    CREATE INDEX IF NOT EXISTS idx_model_identity_source_population ON model_identity_source_features(population_key_hmac, requested_model, feature_version, upstream_bucket_hmac);

    CREATE INDEX IF NOT EXISTS idx_model_identity_baseline_active ON model_identity_baseline_versions(population_key_hmac, requested_model, feature_version, version_status, baseline_version);

    CREATE INDEX IF NOT EXISTS idx_model_paired_similarity_account ON model_paired_similarity_windows(account_id, updated_at);

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

    CREATE TABLE IF NOT EXISTS database_storage_snapshots (
          id TEXT PRIMARY KEY,
          database_role TEXT NOT NULL,
          database_path TEXT NOT NULL,
          sampled_at TEXT NOT NULL,
          file_bytes BIGINT,
          wal_bytes BIGINT,
          shm_bytes BIGINT,
          page_size INTEGER,
          page_count INTEGER,
          freelist_count INTEGER,
          used_bytes BIGINT,
          free_bytes BIGINT,
          table_count INTEGER,
          index_count INTEGER,
          created_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS table_storage_snapshots (
          id TEXT PRIMARY KEY,
          database_role TEXT NOT NULL,
          table_name TEXT NOT NULL,
          sampled_at TEXT NOT NULL,
          table_kind TEXT NOT NULL DEFAULT 'table',
          parent_table_name TEXT,
          is_partition INTEGER NOT NULL DEFAULT 0,
          is_archive INTEGER NOT NULL DEFAULT 0,
          row_count INTEGER,
          table_bytes BIGINT,
          index_bytes BIGINT,
          total_bytes BIGINT,
          page_count INTEGER,
          index_count INTEGER NOT NULL DEFAULT 0,
          growth_bytes_1h INTEGER,
          growth_rows_1h INTEGER,
          growth_bytes_24h INTEGER,
          growth_rows_24h INTEGER,
          created_at TEXT NOT NULL,
          UNIQUE(database_role, table_name, sampled_at)
        );

    CREATE INDEX IF NOT EXISTS idx_account_quality_minute_stats_minute ON account_quality_minute_stats(stat_minute, account_id);

    CREATE INDEX IF NOT EXISTS idx_group_account_stats_group ON group_account_stats(group_id);

    CREATE INDEX IF NOT EXISTS idx_account_quality_scores_sort ON account_quality_scores(provider_code, quality_score, quality_state);

    CREATE INDEX IF NOT EXISTS idx_account_quality_scores_failure_precheck
      ON account_quality_scores(recent_error_count DESC, success_rate, updated_at DESC, account_id)
      WHERE recent_request_count >= 5 AND recent_error_count >= 2;

    CREATE INDEX IF NOT EXISTS idx_account_quality_dirty_accounts_updated ON account_quality_dirty_accounts(updated_at, account_id);

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

    CREATE INDEX IF NOT EXISTS idx_usage_stats_minute_scope_minute ON usage_stats_minute(system_account_id, scope_type, scope_id, stat_minute);

    CREATE INDEX IF NOT EXISTS idx_usage_stats_minute_minute ON usage_stats_minute(stat_minute);

    CREATE INDEX IF NOT EXISTS idx_usage_stats_daily_scope_date ON usage_stats_daily(system_account_id, scope_type, scope_id, stat_date);

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

    DROP INDEX IF EXISTS idx_usage_scope_range_windows_request_count;
    DROP INDEX IF EXISTS idx_usage_scope_range_windows_success_count;
    DROP INDEX IF EXISTS idx_usage_scope_range_windows_error_count;
    DROP INDEX IF EXISTS idx_usage_scope_range_windows_error_rate;
    DROP INDEX IF EXISTS idx_usage_scope_range_windows_total_tokens;
    DROP INDEX IF EXISTS idx_usage_scope_range_windows_total_cost;
    DROP INDEX IF EXISTS idx_usage_scope_range_windows_active_days;
    DROP INDEX IF EXISTS idx_usage_scope_range_windows_last_used;

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

    DROP INDEX IF EXISTS idx_client_ip_range_cost;
    DROP INDEX IF EXISTS idx_client_ip_range_tokens;
    DROP INDEX IF EXISTS idx_client_ip_range_total_tokens;
    DROP INDEX IF EXISTS idx_client_ip_range_success;
    DROP INDEX IF EXISTS idx_client_ip_range_errors;
    DROP INDEX IF EXISTS idx_client_ip_range_error_rate;
    DROP INDEX IF EXISTS idx_client_ip_range_active_days;
    DROP INDEX IF EXISTS idx_client_ip_range_last_used;

    CREATE INDEX IF NOT EXISTS idx_client_ip_range_requests ON client_ip_usage_range_windows(start_date, end_date, request_count DESC, ip_hash);

    CREATE INDEX IF NOT EXISTS idx_client_ip_range_end ON client_ip_usage_range_windows(end_date);

    CREATE INDEX IF NOT EXISTS idx_client_ip_range_dirty_updated ON client_ip_range_window_dirty_ips(updated_at ASC, ip_hash);

    CREATE INDEX IF NOT EXISTS idx_client_ip_account_daily_date ON client_ip_account_stats_daily(stat_date, ip_hash, account_id);

    CREATE INDEX IF NOT EXISTS idx_client_ip_account_daily_ip_date ON client_ip_account_stats_daily(ip_hash, stat_date, account_id);

    DROP INDEX IF EXISTS idx_client_ip_account_range_success;
    DROP INDEX IF EXISTS idx_client_ip_account_range_errors;
    DROP INDEX IF EXISTS idx_client_ip_account_range_error_rate;
    DROP INDEX IF EXISTS idx_client_ip_account_range_tokens;
    DROP INDEX IF EXISTS idx_client_ip_account_range_cost;
    DROP INDEX IF EXISTS idx_client_ip_account_range_active_days;
    DROP INDEX IF EXISTS idx_client_ip_account_range_last_used;

    CREATE INDEX IF NOT EXISTS idx_client_ip_account_range_requests ON client_ip_account_usage_range_windows(ip_hash, start_date, end_date, request_count DESC, account_id);

    CREATE INDEX IF NOT EXISTS idx_client_ip_account_range_dirty_updated ON client_ip_account_range_window_dirty_ips(updated_at ASC, ip_hash);

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

    CREATE INDEX IF NOT EXISTS idx_database_storage_snapshots_role_time ON database_storage_snapshots(database_role, sampled_at DESC);

    CREATE INDEX IF NOT EXISTS idx_database_storage_snapshots_role_time_id ON database_storage_snapshots(database_role, sampled_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_table_storage_snapshots_latest ON table_storage_snapshots(database_role, table_name, sampled_at DESC);

    CREATE INDEX IF NOT EXISTS idx_table_storage_snapshots_latest_id ON table_storage_snapshots(database_role, table_name, sampled_at DESC, id DESC);

    CREATE INDEX IF NOT EXISTS idx_table_storage_snapshots_partition ON table_storage_snapshots(database_role, parent_table_name, sampled_at DESC, table_name) WHERE is_partition = 1;

    CREATE INDEX IF NOT EXISTS idx_table_storage_snapshots_time ON table_storage_snapshots(sampled_at DESC);
  `)
  database.exec(`
    CREATE INDEX IF NOT EXISTS idx_usage_record_cleanup_deductions_account
      ON usage_record_cleanup_deductions(account_id, shard_deleted_at);
  `)
}
