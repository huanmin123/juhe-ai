package statsagg

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// 排行快照 stage，移植 usage-stats-snapshot-helpers.ts
// refreshUsageRankSnapshotFromStats：DELETE 同 scope/window/metric 的旧行后，
// 按窗口 SQL 重排名并取前 limit 名。SQL 文本与 Node 逐字对齐（含
// ROW_NUMBER 排序键 metric_value DESC, last_used_at DESC, scope_id ASC），
// NULL last_used_at 的排序差异按方言各自保留（Node 同样不做 NULLS LAST 归一）。

type rankSnapshotSpec struct {
	scopeType    string
	windowKey    string
	metric       string
	metricColumn string
	sourceTable  string
	timeWhere    string
	timeParam    string
	limit        int
}

func runRankSnapshotStage(ctx context.Context, tx *sql.Tx, dialect Dialect, spec rankSnapshotSpec, snapshotAt, updatedAt string) error {
	deleteQuery := dialect.bind(`DELETE FROM ` + dialect.StatsTable("usage_rank_snapshots") + `
		WHERE scope_type = ?
		  AND window_key = ?
		  AND metric = ?
	`)
	if _, err := tx.ExecContext(ctx, deleteQuery, spec.scopeType, spec.windowKey, spec.metric); err != nil {
		return err
	}
	insertQuery := dialect.bind(`
		INSERT INTO ` + dialect.StatsTable("usage_rank_snapshots") + ` (system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id, metric_value, updated_at)
		SELECT system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id, metric_value, updated_at
		FROM (
		  SELECT
			system_account_id,
			? AS scope_type,
			? AS window_key,
			? AS metric,
			? AS snapshot_at,
			ROW_NUMBER() OVER (
			  PARTITION BY system_account_id
			  ORDER BY metric_value DESC, last_used_at DESC, scope_id ASC
			) AS rank,
			scope_id,
			metric_value,
			? AS updated_at
		  FROM (
			SELECT
			  system_account_id,
			  scope_id,
			  SUM(` + spec.metricColumn + `) AS metric_value,
			  MAX(last_used_at) AS last_used_at
			FROM ` + dialect.StatsTable(spec.sourceTable) + `
			WHERE scope_type = ?
			  AND ` + spec.timeWhere + `
			GROUP BY system_account_id, scope_id
			HAVING SUM(` + spec.metricColumn + `) > 0
		  ) ranked_source
		) ranked_rows
		WHERE rank <= ?
	`)
	_, err := tx.ExecContext(ctx, insertQuery,
		spec.scopeType, spec.windowKey, spec.metric, snapshotAt, updatedAt,
		spec.scopeType, spec.timeParam, spec.limit)
	return err
}

// refreshAccountLast7dRequestRankSnapshot mirrors refreshAccountLast7dRequestRankSnapshot。
func refreshAccountLast7dRequestRankSnapshot(ctx context.Context, tx *sql.Tx, dialect Dialect, snapshotAt, updatedAt string, timezone *time.Location, now time.Time) error {
	return runRankSnapshotStage(ctx, tx, dialect, rankSnapshotSpec{
		scopeType: "account", windowKey: "last7d", metric: "request_count", metricColumn: "request_count",
		sourceTable: "usage_stats_daily", timeWhere: "stat_date >= ?", timeParam: dateKey(now.Add(-6*24*time.Hour), timezone), limit: 50,
	}, snapshotAt, updatedAt)
}

// refreshCallerAccountLast7dRequestRankSnapshot mirrors
// refreshCallerAccountLast7dRequestRankSnapshot。
func refreshCallerAccountLast7dRequestRankSnapshot(ctx context.Context, tx *sql.Tx, dialect Dialect, snapshotAt, updatedAt string, timezone *time.Location, now time.Time) error {
	return runRankSnapshotStage(ctx, tx, dialect, rankSnapshotSpec{
		scopeType: "caller_account", windowKey: "last7d", metric: "request_count", metricColumn: "request_count",
		sourceTable: "usage_stats_daily", timeWhere: "stat_date >= ?", timeParam: dateKey(now.Add(-6*24*time.Hour), timezone), limit: 50,
	}, snapshotAt, updatedAt)
}

// refreshApiKeyCurrentMonthCostRankSnapshot mirrors
// refreshApiKeyCurrentMonthCostRankSnapshot。
func refreshApiKeyCurrentMonthCostRankSnapshot(ctx context.Context, tx *sql.Tx, dialect Dialect, snapshotAt, updatedAt string, timezone *time.Location, now time.Time) error {
	return runRankSnapshotStage(ctx, tx, dialect, rankSnapshotSpec{
		scopeType: "api_key", windowKey: "current_month", metric: "total_cost_usd", metricColumn: "total_cost_usd",
		sourceTable: "usage_stats_monthly", timeWhere: "stat_month = ?", timeParam: monthKey(now, timezone), limit: 50,
	}, snapshotAt, updatedAt)
}

// refreshAuthorizationCurrentMonthCostRankSnapshot mirrors
// refreshAuthorizationCurrentMonthCostRankSnapshot。
func refreshAuthorizationCurrentMonthCostRankSnapshot(ctx context.Context, tx *sql.Tx, dialect Dialect, scopeType, snapshotAt, updatedAt string, timezone *time.Location, now time.Time) error {
	if scopeType != "account_authorization" && scopeType != "group_authorization" {
		return fmt.Errorf("未知授权排行 scope_type: %s", scopeType)
	}
	return runRankSnapshotStage(ctx, tx, dialect, rankSnapshotSpec{
		scopeType: scopeType, windowKey: "current_month", metric: "total_cost_usd", metricColumn: "total_cost_usd",
		sourceTable: "usage_stats_monthly", timeWhere: "stat_month = ?", timeParam: monthKey(now, timezone), limit: 50,
	}, snapshotAt, updatedAt)
}
