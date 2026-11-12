package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/instantmigration"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
)

func quotePGIdentifier(identifier string) string { return instantmigration.QuoteIdentifier(identifier) }

func revision28Precision(target string) (int, bool) {
	const prefix = "timestamptz("
	if !strings.HasPrefix(target, prefix) || !strings.HasSuffix(target, ")") {
		return 0, false
	}
	var precision int
	if _, err := fmt.Sscanf(target, "timestamptz(%d)", &precision); err != nil {
		return 0, false
	}
	return precision, true
}

// revision28Column 是统计域同表重写的单列定义。
type revision28Column struct {
	Table  string
	Column string
	Target string
	Kind   string // instant | calendar_date | calendar_hour
}

// revision28Columns 是 Phase 3 统计域 juhe_stats 的静态列清单：instant 列迁
// timestamptz，日历日/周/月迁 date，业务墙钟小时/分钟迁 timestamp(0)。
// 排除 background_task_runs/background_job_leases（Revision 27 已迁）与
// stats_job_state.cursor_created_at（Phase 4 usage_records 水位）。
var revision28Columns = []revision28Column{
	{"account_health_hourly", "last_observed_at", "timestamptz(3)", "instant"},
	{"account_health_hourly", "stat_hour", "timestamp(0) without time zone", "calendar_hour"},
	{"account_health_hourly", "updated_at", "timestamptz(3)", "instant"},
	{"account_quality_dirty_accounts", "first_dirty_at", "timestamptz(3)", "instant"},
	{"account_quality_dirty_accounts", "updated_at", "timestamptz(3)", "instant"},
	{"account_quality_minute_stats", "last_error_at", "timestamptz(3)", "instant"},
	{"account_quality_minute_stats", "last_sample_at", "timestamptz(3)", "instant"},
	{"account_quality_minute_stats", "last_success_at", "timestamptz(3)", "instant"},
	{"account_quality_minute_stats", "stat_minute", "timestamp(0) without time zone", "calendar_hour"},
	{"account_quality_minute_stats", "updated_at", "timestamptz(3)", "instant"},
	{"account_quality_scores", "last_error_at", "timestamptz(3)", "instant"},
	{"account_quality_scores", "last_sample_at", "timestamptz(3)", "instant"},
	{"account_quality_scores", "last_success_at", "timestamptz(3)", "instant"},
	{"account_quality_scores", "updated_at", "timestamptz(3)", "instant"},
	{"account_quality_scores", "window_ended_at", "timestamptz(3)", "instant"},
	{"account_quality_scores", "window_started_at", "timestamptz(3)", "instant"},
	{"account_usage_snapshots", "created_at", "timestamptz(6)", "instant"},
	{"account_usage_snapshots", "last_attempt_at", "timestamptz(6)", "instant"},
	{"account_usage_snapshots", "last_success_at", "timestamptz(6)", "instant"},
	{"account_usage_snapshots", "updated_at", "timestamptz(6)", "instant"},
	{"ai_performance_summary_dirty_system_accounts", "first_dirty_at", "timestamptz(3)", "instant"},
	{"ai_performance_summary_dirty_system_accounts", "max_stat_date", "date", "calendar_date"},
	{"ai_performance_summary_dirty_system_accounts", "min_stat_date", "date", "calendar_date"},
	{"ai_performance_summary_dirty_system_accounts", "updated_at", "timestamptz(3)", "instant"},
	{"ai_performance_summary_windows", "updated_at", "timestamptz(3)", "instant"},
	{"authorization_team_usage_summary_daily", "last_error_at", "timestamptz(3)", "instant"},
	{"authorization_team_usage_summary_daily", "last_used_at", "timestamptz(3)", "instant"},
	{"authorization_team_usage_summary_daily", "stat_date", "date", "calendar_date"},
	{"authorization_team_usage_summary_daily", "updated_at", "timestamptz(3)", "instant"},
	{"authorization_user_usage_summary_daily", "last_error_at", "timestamptz(3)", "instant"},
	{"authorization_user_usage_summary_daily", "last_used_at", "timestamptz(3)", "instant"},
	{"authorization_user_usage_summary_daily", "stat_date", "date", "calendar_date"},
	{"authorization_user_usage_summary_daily", "updated_at", "timestamptz(3)", "instant"},
	{"backfill_progress", "updated_at", "timestamptz(3)", "instant"},
	{"client_ip_account_range_window_dirty_ips", "first_dirty_at", "timestamptz(3)", "instant"},
	{"client_ip_account_range_window_dirty_ips", "updated_at", "timestamptz(3)", "instant"},
	{"client_ip_account_stats_daily", "last_error_at", "timestamptz(3)", "instant"},
	{"client_ip_account_stats_daily", "last_used_at", "timestamptz(3)", "instant"},
	{"client_ip_account_stats_daily", "stat_date", "date", "calendar_date"},
	{"client_ip_account_stats_daily", "updated_at", "timestamptz(3)", "instant"},
	{"client_ip_account_usage_range_windows", "last_error_at", "timestamptz(3)", "instant"},
	{"client_ip_account_usage_range_windows", "last_used_at", "timestamptz(3)", "instant"},
	{"client_ip_account_usage_range_windows", "updated_at", "timestamptz(3)", "instant"},
	{"client_ip_policies", "created_at", "timestamptz(3)", "instant"},
	{"client_ip_policies", "disabled_at", "timestamptz(3)", "instant"},
	{"client_ip_policies", "expires_at", "timestamptz(3)", "instant"},
	{"client_ip_policies", "updated_at", "timestamptz(3)", "instant"},
	{"client_ip_policy_hits", "last_hit_at", "timestamptz(3)", "instant"},
	{"client_ip_policy_hits", "stat_date", "date", "calendar_date"},
	{"client_ip_policy_hits", "updated_at", "timestamptz(3)", "instant"},
	{"client_ip_range_window_dirty_ips", "first_dirty_at", "timestamptz(3)", "instant"},
	{"client_ip_range_window_dirty_ips", "updated_at", "timestamptz(3)", "instant"},
	{"client_ip_registry", "created_at", "timestamptz(3)", "instant"},
	{"client_ip_registry", "first_seen_at", "timestamptz(3)", "instant"},
	{"client_ip_registry", "last_seen_at", "timestamptz(3)", "instant"},
	{"client_ip_registry", "updated_at", "timestamptz(3)", "instant"},
	{"client_ip_stats_daily", "last_error_at", "timestamptz(3)", "instant"},
	{"client_ip_stats_daily", "last_used_at", "timestamptz(3)", "instant"},
	{"client_ip_stats_daily", "stat_date", "date", "calendar_date"},
	{"client_ip_stats_daily", "updated_at", "timestamptz(3)", "instant"},
	{"client_ip_usage_range_windows", "last_error_at", "timestamptz(3)", "instant"},
	{"client_ip_usage_range_windows", "last_used_at", "timestamptz(3)", "instant"},
	{"client_ip_usage_range_windows", "updated_at", "timestamptz(3)", "instant"},
	{"group_account_stats", "updated_at", "timestamptz(3)", "instant"},
	{"stats_job_state", "last_success_at", "timestamptz(3)", "instant"},
	{"stats_job_state", "updated_at", "timestamptz(3)", "instant"},
	{"usage_error_daily", "stat_date", "date", "calendar_date"},
	{"usage_error_daily", "updated_at", "timestamptz(3)", "instant"},
	{"usage_error_hourly", "stat_hour", "timestamp(0) without time zone", "calendar_hour"},
	{"usage_error_hourly", "updated_at", "timestamptz(3)", "instant"},
	{"usage_error_minute", "stat_minute", "timestamp(0) without time zone", "calendar_hour"},
	{"usage_error_minute", "updated_at", "timestamptz(3)", "instant"},
	{"usage_error_monthly", "stat_month", "date", "calendar_date"},
	{"usage_error_monthly", "updated_at", "timestamptz(3)", "instant"},
	{"usage_error_rank_windows", "updated_at", "timestamptz(3)", "instant"},
	{"usage_error_weekly", "stat_week", "date", "calendar_date"},
	{"usage_error_weekly", "updated_at", "timestamptz(3)", "instant"},
	{"usage_latency_daily", "stat_date", "date", "calendar_date"},
	{"usage_latency_daily", "updated_at", "timestamptz(3)", "instant"},
	{"usage_latency_hourly", "stat_hour", "timestamp(0) without time zone", "calendar_hour"},
	{"usage_latency_hourly", "updated_at", "timestamptz(3)", "instant"},
	{"usage_latency_minute", "stat_minute", "timestamp(0) without time zone", "calendar_hour"},
	{"usage_latency_minute", "updated_at", "timestamptz(3)", "instant"},
	{"usage_latency_monthly", "stat_month", "date", "calendar_date"},
	{"usage_latency_monthly", "updated_at", "timestamptz(3)", "instant"},
	{"usage_latency_weekly", "stat_week", "date", "calendar_date"},
	{"usage_latency_weekly", "updated_at", "timestamptz(3)", "instant"},
	{"usage_model_daily", "stat_date", "date", "calendar_date"},
	{"usage_model_daily", "updated_at", "timestamptz(3)", "instant"},
	{"usage_model_hourly", "stat_hour", "timestamp(0) without time zone", "calendar_hour"},
	{"usage_model_hourly", "updated_at", "timestamptz(3)", "instant"},
	{"usage_model_minute", "stat_minute", "timestamp(0) without time zone", "calendar_hour"},
	{"usage_model_minute", "updated_at", "timestamptz(3)", "instant"},
	{"usage_model_monthly", "stat_month", "date", "calendar_date"},
	{"usage_model_monthly", "updated_at", "timestamptz(3)", "instant"},
	{"usage_model_rank_windows", "updated_at", "timestamptz(3)", "instant"},
	{"usage_model_weekly", "stat_week", "date", "calendar_date"},
	{"usage_model_weekly", "updated_at", "timestamptz(3)", "instant"},
	{"usage_overview_dirty_scopes", "first_dirty_at", "timestamptz(3)", "instant"},
	{"usage_overview_dirty_scopes", "min_changed_date", "date", "calendar_date"},
	{"usage_overview_dirty_scopes", "updated_at", "timestamptz(3)", "instant"},
	{"usage_overview_summary_windows", "last_used_at", "timestamptz(3)", "instant"},
	{"usage_overview_summary_windows", "updated_at", "timestamptz(3)", "instant"},
	{"usage_overview_trend_windows", "updated_at", "timestamptz(3)", "instant"},
	{"usage_quota_hourly_window_dirty_scopes", "first_dirty_at", "timestamptz(3)", "instant"},
	{"usage_quota_hourly_window_dirty_scopes", "updated_at", "timestamptz(3)", "instant"},
	{"usage_quota_hourly_windows", "updated_at", "timestamptz(3)", "instant"},
	{"usage_range_window_requests", "created_at", "timestamptz(3)", "instant"},
	{"usage_range_window_requests", "expires_at", "timestamptz(3)", "instant"},
	{"usage_range_window_requests", "last_processed_at", "timestamptz(3)", "instant"},
	{"usage_range_window_requests", "last_requested_at", "timestamptz(3)", "instant"},
	{"usage_range_window_requests", "updated_at", "timestamptz(3)", "instant"},
	{"usage_rank_snapshots", "snapshot_at", "timestamptz(3)", "instant"},
	{"usage_rank_snapshots", "updated_at", "timestamptz(3)", "instant"},
	{"usage_record_cleanup_deductions", "created_at", "timestamptz(3)", "instant"},
	{"usage_record_cleanup_deductions", "shard_deleted_at", "timestamptz(3)", "instant"},
	{"usage_record_cleanup_deductions", "stats_subtracted_at", "timestamptz(3)", "instant"},
	{"usage_record_cleanup_deductions", "updated_at", "timestamptz(3)", "instant"},
	{"usage_scope_range_windows", "last_error_at", "timestamptz(3)", "instant"},
	{"usage_scope_range_windows", "last_used_at", "timestamptz(3)", "instant"},
	{"usage_scope_range_windows", "updated_at", "timestamptz(3)", "instant"},
	{"usage_stats_daily", "last_error_at", "timestamptz(3)", "instant"},
	{"usage_stats_daily", "last_used_at", "timestamptz(3)", "instant"},
	{"usage_stats_daily", "stat_date", "date", "calendar_date"},
	{"usage_stats_daily", "updated_at", "timestamptz(3)", "instant"},
	{"usage_stats_hourly", "last_error_at", "timestamptz(3)", "instant"},
	{"usage_stats_hourly", "last_used_at", "timestamptz(3)", "instant"},
	{"usage_stats_hourly", "stat_hour", "timestamp(0) without time zone", "calendar_hour"},
	{"usage_stats_hourly", "updated_at", "timestamptz(3)", "instant"},
	{"usage_stats_minute", "last_error_at", "timestamptz(3)", "instant"},
	{"usage_stats_minute", "last_used_at", "timestamptz(3)", "instant"},
	{"usage_stats_minute", "stat_minute", "timestamp(0) without time zone", "calendar_hour"},
	{"usage_stats_minute", "updated_at", "timestamptz(3)", "instant"},
	{"usage_stats_monthly", "last_error_at", "timestamptz(3)", "instant"},
	{"usage_stats_monthly", "last_used_at", "timestamptz(3)", "instant"},
	{"usage_stats_monthly", "stat_month", "date", "calendar_date"},
	{"usage_stats_monthly", "updated_at", "timestamptz(3)", "instant"},
	{"usage_stats_totals", "last_error_at", "timestamptz(3)", "instant"},
	{"usage_stats_totals", "last_used_at", "timestamptz(3)", "instant"},
	{"usage_stats_totals", "updated_at", "timestamptz(3)", "instant"},
	{"usage_stats_weekly", "last_error_at", "timestamptz(3)", "instant"},
	{"usage_stats_weekly", "last_used_at", "timestamptz(3)", "instant"},
	{"usage_stats_weekly", "stat_week", "date", "calendar_date"},
	{"usage_stats_weekly", "updated_at", "timestamptz(3)", "instant"},
}

func revision28AlterSQL(c revision28Column) string {
	table := quotePGIdentifier("juhe_stats") + "." + quotePGIdentifier(c.Table)
	column := quotePGIdentifier(c.Column)
	switch c.Kind {
	case "calendar_date":
		if c.Column == "stat_month" {
			return fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN %s TYPE date USING to_date(%s || '-01', 'YYYY-MM-DD')`, table, column, column)
		}
		return fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN %s TYPE date USING %s::date`, table, column, column)
	case "calendar_hour":
		if c.Column == "stat_hour" {
			return fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN %s TYPE timestamp(0) USING (%s || ':00')::timestamp(0)`, table, column, column)
		}
		return fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN %s TYPE timestamp(0) USING %s::timestamp(0)`, table, column, column)
	default:
		return fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN %s TYPE %s USING %s::timestamptz`, table, column, c.Target, column)
	}
}

func revision28ColumnSatisfied(ctx context.Context, db *sql.DB, c revision28Column) (bool, error) {
	var dataType string
	var precision sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT data_type, datetime_precision FROM information_schema.columns
		WHERE table_schema='juhe_stats' AND table_name=$1 AND column_name=$2`, c.Table, c.Column).Scan(&dataType, &precision)
	if err != nil {
		return false, err
	}
	switch c.Kind {
	case "calendar_date":
		return dataType == "date", nil
	case "calendar_hour":
		return dataType == "timestamp without time zone" && precision.Valid && precision.Int64 == 0, nil
	default:
		want, ok := revision28Precision(c.Target)
		if !ok {
			return dataType == "timestamp with time zone", nil
		}
		return dataType == "timestamp with time zone" && precision.Valid && int(precision.Int64) == want, nil
	}
}

func revision28Satisfied(ctx context.Context, dsn string) (bool, error) {
	db, err := openPG(dsn)
	if err != nil {
		return false, err
	}
	defer db.Close()
	seen := map[string]bool{}
	for _, c := range revision28Columns {
		if seen[c.Table] {
			continue
		}
		present, err := relationPresent(ctx, db, "juhe_stats", c.Table)
		if err != nil {
			return false, err
		}
		seen[c.Table] = true
		if !present {
			continue
		}
		ok, err := revision28ColumnSatisfied(ctx, db, c)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

func revision28Preflight(ctx context.Context, tx *sql.Tx, c revision28Column) error {
	var invalid int64
	var predicate string
	switch c.Kind {
	case "calendar_date":
		if c.Column == "stat_month" {
			col := quotePGIdentifier(c.Column) + "::text"
			predicate = fmt.Sprintf(`%s IS NOT NULL AND (%s = '' OR NOT pg_input_is_valid(%s || '-01', 'date'))`, quotePGIdentifier(c.Column), col, col)
		} else {
			col := quotePGIdentifier(c.Column) + "::text"
			predicate = fmt.Sprintf(`%s IS NOT NULL AND (%s = '' OR NOT pg_input_is_valid(%s, 'date'))`, quotePGIdentifier(c.Column), col, col)
		}
	case "calendar_hour":
		if c.Column == "stat_hour" {
			col := quotePGIdentifier(c.Column) + "::text"
			predicate = fmt.Sprintf(`%s IS NOT NULL AND (%s = '' OR NOT pg_input_is_valid(%s || ':00', 'timestamp'))`, quotePGIdentifier(c.Column), col, col)
		} else {
			col := quotePGIdentifier(c.Column) + "::text"
			predicate = fmt.Sprintf(`%s IS NOT NULL AND (%s = '' OR NOT pg_input_is_valid(%s, 'timestamp'))`, quotePGIdentifier(c.Column), col, col)
		}
	default:
		col := quotePGIdentifier(c.Column) + "::text"
		predicate = fmt.Sprintf(`%s IS NOT NULL AND (%s = '' OR NOT pg_input_is_valid(%s, 'timestamptz'))`, quotePGIdentifier(c.Column), col, col)
	}
	table := quotePGIdentifier("juhe_stats") + "." + quotePGIdentifier(c.Table)
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s`, table, predicate)).Scan(&invalid); err != nil {
		return err
	}
	if invalid > 0 {
		return fmt.Errorf("%s.%s 存在 %d 行非法时间值", c.Table, c.Column, invalid)
	}
	return nil
}

func applyRevision28(ctx context.Context, dsn string) (upgradepipeline.Detail, error) {
	db, err := openPG(dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SET LOCAL timezone='UTC'; SET LOCAL lock_timeout='2s'`); err != nil {
		return nil, err
	}
	// 预检在锁前完成：逐表扫描，避免持锁期间才发现脏值。
	for _, c := range revision28Columns {
		present, err := relationPresent(ctx, db, "juhe_stats", c.Table)
		if err != nil {
			return nil, err
		}
		if present {
			if err := revision28Preflight(ctx, tx, c); err != nil {
				return nil, err
			}
		}
	}
	applied := 0
	for _, c := range revision28Columns {
		present, err := relationPresent(ctx, db, "juhe_stats", c.Table)
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		ok, err := revision28ColumnSatisfied(ctx, db, c)
		if err != nil {
			return nil, err
		}
		if ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, revision28AlterSQL(c)); err != nil {
			return nil, fmt.Errorf("迁移 juhe_stats.%s.%s 失败: %w", c.Table, c.Column, err)
		}
		applied++
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return upgradepipeline.Detail{"applied": applied, "columns": len(revision28Columns)}, nil
}
