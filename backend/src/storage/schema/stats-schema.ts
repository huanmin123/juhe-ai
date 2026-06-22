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
          kind TEXT NOT NULL,
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
          request_count INTEGER NOT NULL DEFAULT 0,
          success_count INTEGER NOT NULL DEFAULT 0,
          error_count INTEGER NOT NULL DEFAULT 0,
          input_tokens INTEGER NOT NULL DEFAULT 0,
          output_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_tokens INTEGER NOT NULL DEFAULT 0,
          cache_read_cost_usd REAL NOT NULL DEFAULT 0,
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
          memory_total_bytes INTEGER,
          memory_free_bytes INTEGER,
          process_rss_bytes INTEGER,
          process_heap_used_bytes INTEGER,
          process_heap_total_bytes INTEGER,
          event_loop_lag_ms REAL,
          network_rx_bytes_per_sec REAL,
          network_tx_bytes_per_sec REAL,
          network_rx_total_bytes INTEGER,
          network_tx_total_bytes INTEGER,
          db_file_bytes INTEGER,
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
          process_rss_bytes_sum INTEGER NOT NULL DEFAULT 0,
          process_rss_bytes_max INTEGER,
          process_heap_used_bytes_sum INTEGER NOT NULL DEFAULT 0,
          process_heap_used_bytes_max INTEGER,
          event_loop_lag_ms_sum REAL NOT NULL DEFAULT 0,
          event_loop_lag_ms_count INTEGER NOT NULL DEFAULT 0,
          event_loop_lag_ms_max REAL,
          network_rx_bytes_per_sec_sum REAL NOT NULL DEFAULT 0,
          network_rx_bytes_per_sec_max REAL,
          network_rx_bytes_per_sec_count INTEGER NOT NULL DEFAULT 0,
          network_tx_bytes_per_sec_sum REAL NOT NULL DEFAULT 0,
          network_tx_bytes_per_sec_max REAL,
          network_tx_bytes_per_sec_count INTEGER NOT NULL DEFAULT 0,
          network_rx_total_bytes_max INTEGER,
          network_tx_total_bytes_max INTEGER,
          db_file_bytes_max INTEGER,
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
          process_rss_bytes_sum INTEGER NOT NULL DEFAULT 0,
          process_rss_bytes_max INTEGER,
          process_heap_used_bytes_sum INTEGER NOT NULL DEFAULT 0,
          process_heap_used_bytes_max INTEGER,
          event_loop_lag_ms_sum REAL NOT NULL DEFAULT 0,
          event_loop_lag_ms_count INTEGER NOT NULL DEFAULT 0,
          event_loop_lag_ms_max REAL,
          network_rx_bytes_per_sec_sum REAL NOT NULL DEFAULT 0,
          network_rx_bytes_per_sec_max REAL,
          network_rx_bytes_per_sec_count INTEGER NOT NULL DEFAULT 0,
          network_tx_bytes_per_sec_sum REAL NOT NULL DEFAULT 0,
          network_tx_bytes_per_sec_max REAL,
          network_tx_bytes_per_sec_count INTEGER NOT NULL DEFAULT 0,
          network_rx_total_bytes_max INTEGER,
          network_tx_total_bytes_max INTEGER,
          db_file_bytes_max INTEGER,
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
          process_rss_bytes INTEGER,
          process_heap_used_bytes INTEGER,
          process_heap_total_bytes INTEGER,
          process_external_bytes INTEGER,
          process_array_buffers_bytes INTEGER,
          created_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS process_event_loop_hourly (
          stat_hour TEXT NOT NULL,
          process_role TEXT NOT NULL,
          sample_count INTEGER NOT NULL DEFAULT 0,
          event_loop_lag_ms_sum REAL NOT NULL DEFAULT 0,
          event_loop_lag_ms_count INTEGER NOT NULL DEFAULT 0,
          event_loop_lag_ms_max REAL,
          process_rss_bytes_sum INTEGER NOT NULL DEFAULT 0,
          process_rss_bytes_max INTEGER,
          process_heap_used_bytes_sum INTEGER NOT NULL DEFAULT 0,
          process_heap_used_bytes_max INTEGER,
          process_heap_total_bytes_sum INTEGER NOT NULL DEFAULT 0,
          process_heap_total_bytes_max INTEGER,
          process_external_bytes_sum INTEGER NOT NULL DEFAULT 0,
          process_external_bytes_max INTEGER,
          process_array_buffers_bytes_sum INTEGER NOT NULL DEFAULT 0,
          process_array_buffers_bytes_max INTEGER,
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
          process_rss_bytes_sum INTEGER NOT NULL DEFAULT 0,
          process_rss_bytes_max INTEGER,
          process_heap_used_bytes_sum INTEGER NOT NULL DEFAULT 0,
          process_heap_used_bytes_max INTEGER,
          process_heap_total_bytes_sum INTEGER NOT NULL DEFAULT 0,
          process_heap_total_bytes_max INTEGER,
          process_external_bytes_sum INTEGER NOT NULL DEFAULT 0,
          process_external_bytes_max INTEGER,
          process_array_buffers_bytes_sum INTEGER NOT NULL DEFAULT 0,
          process_array_buffers_bytes_max INTEGER,
          updated_at TEXT NOT NULL,
          PRIMARY KEY (window_key, bucket_key, process_role)
        );

    CREATE TABLE IF NOT EXISTS database_storage_snapshots (
          id TEXT PRIMARY KEY,
          database_role TEXT NOT NULL,
          database_path TEXT NOT NULL,
          sampled_at TEXT NOT NULL,
          file_bytes INTEGER,
          wal_bytes INTEGER,
          shm_bytes INTEGER,
          page_size INTEGER,
          page_count INTEGER,
          freelist_count INTEGER,
          used_bytes INTEGER,
          free_bytes INTEGER,
          table_count INTEGER,
          index_count INTEGER,
          created_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS table_storage_snapshots (
          id TEXT PRIMARY KEY,
          database_role TEXT NOT NULL,
          table_name TEXT NOT NULL,
          sampled_at TEXT NOT NULL,
          row_count INTEGER,
          table_bytes INTEGER,
          index_bytes INTEGER,
          total_bytes INTEGER,
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

    CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_lookup ON usage_scope_range_windows(system_account_id, scope_type, scope_id, start_date, end_date);

    CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_range_lookup ON usage_scope_range_windows(system_account_id, scope_type, start_date, end_date, scope_id);

    CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_request_count ON usage_scope_range_windows(system_account_id, scope_type, start_date, end_date, request_count DESC, scope_id);

    CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_account_usage_order ON usage_scope_range_windows(system_account_id, scope_type, start_date, end_date, request_count DESC, total_cost_usd DESC, (input_tokens + output_tokens) DESC, last_used_at DESC, scope_id);

    CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_success_count ON usage_scope_range_windows(system_account_id, scope_type, start_date, end_date, success_count DESC, scope_id);

    CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_error_count ON usage_scope_range_windows(system_account_id, scope_type, start_date, end_date, error_count DESC, scope_id);

    CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_error_rate ON usage_scope_range_windows(system_account_id, scope_type, start_date, end_date, (CASE WHEN request_count > 0 THEN CAST(error_count AS REAL) / request_count ELSE 0 END) DESC, scope_id);

    CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_total_tokens ON usage_scope_range_windows(system_account_id, scope_type, start_date, end_date, (input_tokens + output_tokens) DESC, scope_id);

    CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_total_cost ON usage_scope_range_windows(system_account_id, scope_type, start_date, end_date, total_cost_usd DESC, scope_id);

    CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_active_days ON usage_scope_range_windows(system_account_id, scope_type, start_date, end_date, active_days DESC, scope_id);

    CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_last_used ON usage_scope_range_windows(system_account_id, scope_type, start_date, end_date, last_used_at DESC, scope_id);

    CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_end ON usage_scope_range_windows(end_date);

    CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_end_start ON usage_scope_range_windows(end_date, start_date);

    CREATE INDEX IF NOT EXISTS idx_client_ip_registry_bucket ON client_ip_registry(bucket_no, ip_hash);

    CREATE INDEX IF NOT EXISTS idx_client_ip_registry_last_seen ON client_ip_registry(last_seen_at DESC, ip_hash);

    CREATE INDEX IF NOT EXISTS idx_client_ip_registry_ip ON client_ip_registry(aggregate_ip_key);

    CREATE INDEX IF NOT EXISTS idx_client_ip_registry_client_ip ON client_ip_registry(client_ip);

    CREATE INDEX IF NOT EXISTS idx_client_ip_stats_daily_date ON client_ip_stats_daily(stat_date, ip_hash);

    CREATE INDEX IF NOT EXISTS idx_client_ip_range_cost ON client_ip_usage_range_windows(start_date, end_date, total_cost_usd DESC, ip_hash);

    CREATE INDEX IF NOT EXISTS idx_client_ip_range_tokens ON client_ip_usage_range_windows(start_date, end_date, input_tokens DESC, output_tokens DESC, ip_hash);

    CREATE INDEX IF NOT EXISTS idx_client_ip_range_total_tokens ON client_ip_usage_range_windows(start_date, end_date, (input_tokens + output_tokens) DESC, ip_hash);

    CREATE INDEX IF NOT EXISTS idx_client_ip_range_requests ON client_ip_usage_range_windows(start_date, end_date, request_count DESC, ip_hash);

    CREATE INDEX IF NOT EXISTS idx_client_ip_range_success ON client_ip_usage_range_windows(start_date, end_date, success_count DESC, ip_hash);

    CREATE INDEX IF NOT EXISTS idx_client_ip_range_errors ON client_ip_usage_range_windows(start_date, end_date, error_count DESC, ip_hash);

    CREATE INDEX IF NOT EXISTS idx_client_ip_range_error_rate ON client_ip_usage_range_windows(start_date, end_date, (CASE WHEN request_count > 0 THEN CAST(error_count AS REAL) / request_count ELSE 0 END) DESC, ip_hash);

    CREATE INDEX IF NOT EXISTS idx_client_ip_range_active_days ON client_ip_usage_range_windows(start_date, end_date, active_days DESC, ip_hash);

    CREATE INDEX IF NOT EXISTS idx_client_ip_range_last_used ON client_ip_usage_range_windows(start_date, end_date, last_used_at DESC, ip_hash);

    CREATE INDEX IF NOT EXISTS idx_client_ip_range_end ON client_ip_usage_range_windows(end_date);

    CREATE INDEX IF NOT EXISTS idx_client_ip_range_dirty_updated ON client_ip_range_window_dirty_ips(updated_at ASC, ip_hash);

    CREATE INDEX IF NOT EXISTS idx_client_ip_account_daily_date ON client_ip_account_stats_daily(stat_date, ip_hash, account_id);

    CREATE INDEX IF NOT EXISTS idx_client_ip_account_daily_ip_date ON client_ip_account_stats_daily(ip_hash, stat_date, account_id);

    CREATE INDEX IF NOT EXISTS idx_client_ip_account_range_requests ON client_ip_account_usage_range_windows(ip_hash, start_date, end_date, request_count DESC, account_id);

    CREATE INDEX IF NOT EXISTS idx_client_ip_account_range_success ON client_ip_account_usage_range_windows(ip_hash, start_date, end_date, success_count DESC, account_id);

    CREATE INDEX IF NOT EXISTS idx_client_ip_account_range_errors ON client_ip_account_usage_range_windows(ip_hash, start_date, end_date, error_count DESC, account_id);

    CREATE INDEX IF NOT EXISTS idx_client_ip_account_range_error_rate ON client_ip_account_usage_range_windows(ip_hash, start_date, end_date, (CASE WHEN request_count > 0 THEN CAST(error_count AS REAL) / request_count ELSE 0 END) DESC, account_id);

    CREATE INDEX IF NOT EXISTS idx_client_ip_account_range_tokens ON client_ip_account_usage_range_windows(ip_hash, start_date, end_date, (input_tokens + output_tokens) DESC, account_id);

    CREATE INDEX IF NOT EXISTS idx_client_ip_account_range_cost ON client_ip_account_usage_range_windows(ip_hash, start_date, end_date, total_cost_usd DESC, account_id);

    CREATE INDEX IF NOT EXISTS idx_client_ip_account_range_active_days ON client_ip_account_usage_range_windows(ip_hash, start_date, end_date, active_days DESC, account_id);

    CREATE INDEX IF NOT EXISTS idx_client_ip_account_range_last_used ON client_ip_account_usage_range_windows(ip_hash, start_date, end_date, last_used_at DESC, account_id);

    CREATE INDEX IF NOT EXISTS idx_client_ip_account_range_dirty_updated ON client_ip_account_range_window_dirty_ips(updated_at ASC, ip_hash);

    CREATE INDEX IF NOT EXISTS idx_client_ip_policies_active ON client_ip_policies(status, ip_hash, expires_at);

    CREATE INDEX IF NOT EXISTS idx_client_ip_policies_ip ON client_ip_policies(ip_hash, status, created_at DESC);

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

    CREATE INDEX IF NOT EXISTS idx_table_storage_snapshots_time ON table_storage_snapshots(sampled_at DESC);
  `)
  database.exec(`
    CREATE INDEX IF NOT EXISTS idx_usage_record_cleanup_deductions_account
      ON usage_record_cleanup_deductions(account_id, shard_deleted_at);
  `)
}
