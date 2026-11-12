package cleanuprepo

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
	"github.com/huanminabc/juhe-ai/backend-go-platform/dbtime"
	"github.com/huanminabc/juhe-ai/backend-go-platform/statcalendar"
)

// data-retention.repository.ts 的清理侧移植（public_api_logs / system_sessions /
// stats+metrics 保留 / usage records / 非业务数据硬清理），双模方言对齐。

// statsTableRule 照 Node 表规则 { tableName, timeColumnName, cutoffKey }。
type statsTableRule struct {
	TableName      string
	TimeColumnName string
	CutoffKey      string // iso | minute | hour | date | week | month
}

// postgresHardCleanupTables['stats'] 与 nonBusinessStatsCleanupTables 的合并表
// （两处清单在 Node 中逐项一致）。
var nonBusinessStatsCleanupTables = []statsTableRule{
	{"account_quality_minute_stats", "stat_minute", "minute"},
	{"group_account_stats", "updated_at", "iso"},
	{"account_quality_scores", "updated_at", "iso"},
	{"account_quality_dirty_accounts", "updated_at", "iso"},
	{"account_usage_snapshots", "updated_at", "iso"},
	{"usage_stats_totals", "updated_at", "iso"},
	{"usage_stats_minute", "stat_minute", "minute"},
	{"usage_stats_hourly", "stat_hour", "hour"},
	{"usage_stats_daily", "stat_date", "date"},
	{"usage_stats_weekly", "stat_week", "week"},
	{"usage_stats_monthly", "stat_month", "month"},
	{"authorization_team_usage_summary_daily", "stat_date", "date"},
	{"authorization_user_usage_summary_daily", "stat_date", "date"},
	{"usage_model_minute", "stat_minute", "minute"},
	{"usage_model_hourly", "stat_hour", "hour"},
	{"usage_model_daily", "stat_date", "date"},
	{"usage_model_weekly", "stat_week", "week"},
	{"usage_model_monthly", "stat_month", "month"},
	{"usage_error_minute", "stat_minute", "minute"},
	{"usage_error_hourly", "stat_hour", "hour"},
	{"usage_error_daily", "stat_date", "date"},
	{"usage_error_weekly", "stat_week", "week"},
	{"usage_error_monthly", "stat_month", "month"},
	{"usage_latency_minute", "stat_minute", "minute"},
	{"usage_latency_hourly", "stat_hour", "hour"},
	{"usage_latency_daily", "stat_date", "date"},
	{"usage_latency_weekly", "stat_week", "week"},
	{"usage_latency_monthly", "stat_month", "month"},
	{"usage_rank_snapshots", "snapshot_at", "iso"},
	{"usage_overview_summary_windows", "end_date", "date"},
	{"usage_overview_trend_windows", "end_date", "date"},
	{"usage_model_rank_windows", "end_date", "date"},
	{"usage_error_rank_windows", "end_date", "date"},
	{"ai_performance_summary_windows", "end_date", "date"},
	{"usage_quota_hourly_windows", "updated_at", "iso"},
	{"usage_scope_range_windows", "end_date", "date"},
	{"usage_range_window_requests", "expires_at", "iso"},
	{"client_ip_registry", "last_seen_at", "iso"},
	{"client_ip_stats_daily", "stat_date", "date"},
	{"client_ip_usage_range_windows", "end_date", "date"},
	{"client_ip_range_window_dirty_ips", "updated_at", "iso"},
	{"client_ip_policy_hits", "stat_date", "date"},
	{"client_ip_account_stats_daily", "stat_date", "date"},
	{"client_ip_account_usage_range_windows", "end_date", "date"},
	{"client_ip_account_range_window_dirty_ips", "updated_at", "iso"},
	{"usage_record_cleanup_deductions", "updated_at", "iso"},
}

var nonBusinessDatasetCleanupTables = []statsTableRule{
	{"public_api_logs", "created_at", "iso"},
	{"api_key_record_cleanup_targets", "updated_at", "iso"},
	{"account_record_cleanup_targets", "updated_at", "iso"},
}

// usageStatsRetentionSpec 照 cleanupUsageStatsBucketsBefore 的表/列/结果字段
// 映射（field 为 retention.UsageStatsRetentionCounts 的赋值序）。
type usageStatsRetentionSpec struct {
	TableName string
	Column    string
	CutoffKey string // AccountQualityMinute | Minute | Hourly | Daily | Weekly | Monthly | RankSnapshot | WindowDate | WindowIso
	Kind      string // stats | accountHealth
}

var usageStatsRetentionSpecs = []usageStatsRetentionSpec{
	{"account_quality_minute_stats", "stat_minute", "AccountQualityMinute", "stats"},
	{"account_health_hourly", "stat_hour", "Hourly", "accountHealth"},
	{"usage_stats_minute", "stat_minute", "Minute", "stats"},
	{"usage_model_minute", "stat_minute", "Minute", "stats"},
	{"usage_error_minute", "stat_minute", "Minute", "stats"},
	{"usage_latency_minute", "stat_minute", "Minute", "stats"},
	{"usage_stats_daily", "stat_date", "Daily", "stats"},
	{"usage_model_daily", "stat_date", "Daily", "stats"},
	{"usage_error_daily", "stat_date", "Daily", "stats"},
	{"usage_latency_daily", "stat_date", "Daily", "stats"},
	{"usage_stats_hourly", "stat_hour", "Hourly", "stats"},
	{"usage_model_hourly", "stat_hour", "Hourly", "stats"},
	{"usage_error_hourly", "stat_hour", "Hourly", "stats"},
	{"usage_latency_hourly", "stat_hour", "Hourly", "stats"},
	{"usage_stats_weekly", "stat_week", "Weekly", "stats"},
	{"usage_model_weekly", "stat_week", "Weekly", "stats"},
	{"usage_error_weekly", "stat_week", "Weekly", "stats"},
	{"usage_latency_weekly", "stat_week", "Weekly", "stats"},
	{"usage_stats_monthly", "stat_month", "Monthly", "stats"},
	{"usage_model_monthly", "stat_month", "Monthly", "stats"},
	{"usage_error_monthly", "stat_month", "Monthly", "stats"},
	{"usage_latency_monthly", "stat_month", "Monthly", "stats"},
	{"authorization_team_usage_summary_daily", "stat_date", "Daily", "stats"},
	{"authorization_user_usage_summary_daily", "stat_date", "Daily", "stats"},
	{"usage_rank_snapshots", "snapshot_at", "RankSnapshot", "stats"},
	{"usage_overview_summary_windows", "end_date", "WindowDate", "stats"},
	{"usage_overview_trend_windows", "end_date", "WindowDate", "stats"},
	{"usage_model_rank_windows", "end_date", "WindowDate", "stats"},
	{"usage_error_rank_windows", "end_date", "WindowDate", "stats"},
	{"ai_performance_summary_windows", "end_date", "WindowDate", "stats"},
	{"usage_quota_hourly_windows", "updated_at", "WindowIso", "stats"},
	{"usage_scope_range_windows", "end_date", "WindowDate", "stats"},
	{"client_ip_usage_range_windows", "end_date", "WindowDate", "stats"},
	{"client_ip_range_window_dirty_ips", "updated_at", "WindowIso", "stats"},
	{"client_ip_account_stats_daily", "stat_date", "Daily", "stats"},
	{"client_ip_account_usage_range_windows", "end_date", "WindowDate", "stats"},
	{"client_ip_account_range_window_dirty_ips", "updated_at", "WindowIso", "stats"},
	{"account_usage_snapshots", "updated_at", "WindowIso", "stats"},
}

// PublicApiLogsStore 是 dataset 库 public_api_logs 的保留清理
// （对应 retention.PublicApiLogsCleaner；PG schema juhe_dataset）。
type PublicApiLogsStore struct {
	DB *DB
}

// CleanupBefore 删除 created_at 严格早于 cutoff 的行，最旧优先，最多 limit 行。
func (s *PublicApiLogsStore) CleanupBefore(ctx context.Context, cutoffCreatedAt string, limit int) (int64, error) {
	db := s.DB
	if db.Postgres {
		return execChanged(ctx, db, `
      DELETE FROM juhe_dataset.public_api_logs
      WHERE ctid IN (
        SELECT ctid FROM juhe_dataset.public_api_logs
        WHERE created_at < ?
        ORDER BY created_at ASC, ctid ASC
        LIMIT ?
      )
		`, cutoffCreatedAt, batchLimit(limit))
	}
	return execChanged(ctx, db, `
    DELETE FROM public_api_logs
    WHERE rowid IN (
      SELECT rowid FROM public_api_logs
      WHERE created_at < ?
      ORDER BY created_at ASC, rowid ASC
      LIMIT ?
    )
	`, cutoffCreatedAt, batchLimit(limit))
}

// SystemSessionsStore 是 business 库 system_sessions 的过期清理
// （cleanupExpiredSystemSessions / cleanupExpiredSystemSessionsAsync）。
type SystemSessionsStore struct {
	DB *DB
}

// CleanupExpired 删除 expires_at 严格早于 expiredBefore 的会话。
func (s *SystemSessionsStore) CleanupExpired(ctx context.Context, expiredBefore string, limit int) (int64, error) {
	db := s.DB
	if db.Postgres {
		return execChanged(ctx, db, `
      DELETE FROM juhe_business.system_sessions
      WHERE ctid IN (
        SELECT ctid FROM juhe_business.system_sessions
        WHERE expires_at < ?
        ORDER BY expires_at ASC, ctid ASC
        LIMIT ?
      )
		`, expiredBefore, batchLimit(limit))
	}
	return execChanged(ctx, db, `
    DELETE FROM system_sessions
    WHERE rowid IN (
      SELECT rowid FROM system_sessions
      WHERE expires_at < ?
      ORDER BY expires_at ASC, rowid ASC
      LIMIT ?
    )
	`, expiredBefore, batchLimit(limit))
}

// StatsRetentionStore 是 stats 库保留/硬清理入口
// （cleanupUsageStatsBucketsBefore / cleanupNonBusinessDataBeforeWithResult 的
// stats 半区）。
type StatsRetentionStore struct {
	DB *DB
	// Checkpoint 照 cleanupStatsDatabaseAfterDelete：SQLite 模式删除后做 WAL
	// checkpoint（PG 模式为空）。
	Checkpoint func(ctx context.Context) error
}

// AfterDelete 执行删除后的 checkpoint（失败由调用方决定是否告警）。
func (s *StatsRetentionStore) AfterDelete(ctx context.Context) {
	if s != nil && s.Checkpoint != nil {
		_ = s.Checkpoint(ctx)
	}
}

// deleteRowsBefore 按 rowid（SQLite）/ ctid（PG）删除时间列早于 cutoff 的行。
func deleteRowsBefore(ctx context.Context, db *DB, schema, tableName, timeColumnName string, cutoff any, limit int) (int64, error) {
	table := db.Table(schema, tableName)
	quotedTable := quoteIdentifierTable(db, table)
	quotedColumn := quoteIdentifierColumn(db, timeColumnName)
	if db.Postgres {
		return execChanged(ctx, db, fmt.Sprintf(`
      DELETE FROM %s
      WHERE ctid IN (
        SELECT ctid FROM %s
        WHERE %s < ?
        ORDER BY %s ASC, ctid ASC
        LIMIT ?
      )
		`, quotedTable, quotedTable, quotedColumn, quotedColumn), cutoff, batchLimit(limit))
	}
	return execChanged(ctx, db, fmt.Sprintf(`
    DELETE FROM %s
    WHERE rowid IN (
      SELECT rowid FROM %s
      WHERE %s < ?
      ORDER BY %s ASC, rowid ASC
      LIMIT ?
    )
	`, quotedTable, quotedTable, quotedColumn, quotedColumn), cutoff, batchLimit(limit))
}

// quoteIdentifierTable 对 SQLite 的裸表名加引号（Node quoteSqliteIdentifier）。
func quoteIdentifierTable(db *DB, table string) string {
	if db.Postgres {
		return table
	}
	return `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
}

func quoteIdentifierColumn(db *DB, column string) string {
	return `"` + strings.ReplaceAll(column, `"`, `""`) + `"`
}

// CleanupUsageStatsRetention 照 cleanupUsageStatsBucketsBefore（Async 变体）。
func (s *StatsRetentionStore) CleanupUsageStatsRetention(ctx context.Context, input retention.UsageStatsRetentionInput) (retention.UsageStatsRetentionCounts, error) {
	counts := retention.UsageStatsRetentionCounts{}
	values := map[string]string{
		"AccountQualityMinute": input.AccountQualityMinuteCutoffMinute,
		"Minute":               input.MinuteCutoffMinute,
		"Hourly":               input.HourlyCutoffHour,
		"Daily":                input.DailyCutoffDate,
		"Weekly":               input.WeeklyCutoffWeek,
		"Monthly":              input.MonthlyCutoffMonth,
		"RankSnapshot":         input.RankSnapshotCutoffIso,
		"WindowDate":           input.WindowCutoffDate,
		"WindowIso":            input.WindowCutoffIso,
	}
	countsBySpec := map[string]*int64{}
	for index := range usageStatsRetentionSpecs {
		spec := usageStatsRetentionSpecs[index]
		if spec.Kind == "accountHealth" {
			countsBySpec[spec.TableName] = &counts.AccountHealthHourly
		} else {
			countsBySpec[spec.TableName] = statsCountField(&counts, spec.TableName)
		}
		target := countsBySpec[spec.TableName]
		// usageStatsRetentionSpecs 与 statsCountField 的表清单静态对齐，
		// target 不可能为 nil；该防御守卫已按 w13e 收尾授权删除。
		cutoff := statsagg.CalendarDBValue(spec.Column, values[spec.CutoffKey])
		deleted, err := deleteRowsBefore(ctx, s.DB, s.schemaForStats(), spec.TableName, spec.Column, cutoff, batchLimit(input.Limit))
		if err != nil {
			return counts, err
		}
		*target += deleted
	}
	s.AfterDelete(ctx)
	return counts, nil
}

func (s *StatsRetentionStore) schemaForStats() string {
	if s.DB.Postgres {
		return "juhe_stats"
	}
	return ""
}

// statsCountField 返回 counts 中与 Node 结果字段对应的指针（表名 → 字段）。
func statsCountField(counts *retention.UsageStatsRetentionCounts, tableName string) *int64 {
	switch tableName {
	case "account_quality_minute_stats":
		return &counts.AccountQualityMinuteStats
	case "usage_stats_minute":
		return &counts.UsageStatsMinute
	case "usage_model_minute":
		return &counts.UsageModelMinute
	case "usage_error_minute":
		return &counts.UsageErrorMinute
	case "usage_latency_minute":
		return &counts.UsageLatencyMinute
	case "usage_stats_daily":
		return &counts.UsageStatsDaily
	case "usage_model_daily":
		return &counts.UsageModelDaily
	case "usage_error_daily":
		return &counts.UsageErrorDaily
	case "usage_latency_daily":
		return &counts.UsageLatencyDaily
	case "usage_stats_hourly":
		return &counts.UsageStatsHourly
	case "usage_model_hourly":
		return &counts.UsageModelHourly
	case "usage_error_hourly":
		return &counts.UsageErrorHourly
	case "usage_latency_hourly":
		return &counts.UsageLatencyHourly
	case "usage_stats_weekly":
		return &counts.UsageStatsWeekly
	case "usage_model_weekly":
		return &counts.UsageModelWeekly
	case "usage_error_weekly":
		return &counts.UsageErrorWeekly
	case "usage_latency_weekly":
		return &counts.UsageLatencyWeekly
	case "usage_stats_monthly":
		return &counts.UsageStatsMonthly
	case "usage_model_monthly":
		return &counts.UsageModelMonthly
	case "usage_error_monthly":
		return &counts.UsageErrorMonthly
	case "usage_latency_monthly":
		return &counts.UsageLatencyMonthly
	case "authorization_team_usage_summary_daily":
		return &counts.AuthorizationTeamUsageSummaryDaily
	case "authorization_user_usage_summary_daily":
		return &counts.AuthorizationUserUsageSummaryDaily
	case "usage_rank_snapshots":
		return &counts.UsageRankSnapshots
	case "usage_overview_summary_windows":
		return &counts.UsageOverviewSummaryWindows
	case "usage_overview_trend_windows":
		return &counts.UsageOverviewTrendWindows
	case "usage_model_rank_windows":
		return &counts.UsageModelRankWindows
	case "usage_error_rank_windows":
		return &counts.UsageErrorRankWindows
	case "ai_performance_summary_windows":
		return &counts.AIPerformanceSummaryWindows
	case "usage_quota_hourly_windows":
		return &counts.UsageQuotaHourlyWindows
	case "usage_scope_range_windows":
		return &counts.UsageScopeRangeWindows
	case "client_ip_usage_range_windows":
		return &counts.ClientIpUsageRangeWindows
	case "client_ip_range_window_dirty_ips":
		return &counts.ClientIpRangeWindowDirtyIps
	case "client_ip_account_stats_daily":
		return &counts.ClientIpAccountStatsDaily
	case "client_ip_account_usage_range_windows":
		return &counts.ClientIpAccountUsageRangeWindows
	case "client_ip_account_range_window_dirty_ips":
		return &counts.ClientIpAccountRangeWindowDirtyIps
	case "account_usage_snapshots":
		return &counts.AccountUsageSnapshots
	}
	return nil
}

// hardCleanupCutoffs 照 hardCleanupCutoffs：iso + 业务时区 minute/hour/date/week/month。
func hardCleanupCutoffs(cutoffAt string, location *time.Location) (map[string]string, error) {
	parsed, ok := parseInstant(cutoffAt)
	if !ok {
		return nil, fmt.Errorf("非业务数据清理截止时间必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	timeKeys, err := statsagg.UsageStatsTimeKeysFor(parsed.UTC().Format("2006-01-02T15:04:05.000Z07:00"), location)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"iso":    strings.TrimSpace(cutoffAt),
		"minute": timeKeys.StatMinute,
		"hour":   timeKeys.StatHour,
		"date":   timeKeys.StatDate,
		"week":   timeKeys.StatWeek,
		"month":  timeKeys.StatMonth,
	}, nil
}

// CleanupNonBusinessStatsData 照 cleanupNonBusinessDataBeforeWithResult 的
// scope='stats' 半区（对应 retention.StatsWriter.CleanupNonBusinessStatsData）。
func (s *StatsRetentionStore) CleanupNonBusinessStatsData(ctx context.Context, cutoffAt string, limit int, location *time.Location) (retention.NonBusinessDataCleanupCounts, error) {
	counts := retention.NonBusinessDataCleanupCounts{CutoffAt: cutoffAt, TableRows: map[string]int64{}, FileDeletes: map[string]int64{}}
	cutoffs, err := hardCleanupCutoffs(cutoffAt, location)
	if err != nil {
		return counts, err
	}
	batch := positiveLimit(limit)
	for _, rule := range nonBusinessStatsCleanupTables {
		cutoff := statsagg.CalendarDBValue(rule.TimeColumnName, cutoffs[rule.CutoffKey])
		deleted, err := deleteRowsBefore(ctx, s.DB, s.schemaForStats(), rule.TableName, rule.TimeColumnName, cutoff, batch)
		if err != nil {
			return counts, err
		}
		addNonBusinessRows(&counts, "juhe_stats."+rule.TableName, deleted, int64(batch))
	}
	s.AfterDelete(ctx)
	return counts, nil
}

func addNonBusinessRows(counts *retention.NonBusinessDataCleanupCounts, key string, count, batch int64) {
	if count <= 0 {
		return
	}
	counts.TableRows[key] += count
	counts.DeletedRows += count
	if count >= batch {
		counts.HasMore = true
	}
}

// NonBusinessDatasetStore 是非业务数据硬清理的 dataset 半区
// （scope='dataset'：usage records + dataset 表），对应
// retention.NonBusinessDataCleaner。
type NonBusinessDatasetStore struct {
	Dataset      *DB
	Stats        *DB
	UsageRecords *UsageRecordsStore
	Timezone     func(ctx context.Context) (*time.Location, error)
}

// CleanupBefore 照 cleanupNonBusinessDataBeforeWithResult 的 scope='dataset' 半区。
func (s *NonBusinessDatasetStore) CleanupBefore(ctx context.Context, cutoffAt string, limit int) (retention.NonBusinessDataCleanupCounts, error) {
	counts := retention.NonBusinessDataCleanupCounts{CutoffAt: cutoffAt, TableRows: map[string]int64{}, FileDeletes: map[string]int64{}}
	location, err := s.Timezone(ctx)
	if err != nil {
		return counts, err
	}
	cutoffs, err := hardCleanupCutoffs(cutoffAt, location)
	if err != nil {
		return counts, err
	}
	batch := positiveLimit(limit)

	usageRecords, err := s.UsageRecords.CleanupProcessedBefore(ctx, cutoffs["iso"], batch)
	if err != nil {
		return counts, err
	}
	addNonBusinessRows(&counts, "usage_shards.usage_records", usageRecords.DeletedRows, int64(batch))
	if usageRecords.HasMore || usageRecords.BlockedReason != "" {
		counts.HasMore = true
	}

	for _, rule := range nonBusinessDatasetCleanupTables {
		deleted, err := deleteRowsBefore(ctx, s.Dataset, "", rule.TableName, rule.TimeColumnName, cutoffs[rule.CutoffKey], batch)
		if err != nil {
			return counts, err
		}
		addNonBusinessRows(&counts, "dataset."+rule.TableName, deleted, int64(batch))
	}

	return counts, nil
}

// usageRecordCleanupRequiredCursorJobNames 照 Node 常量。
var usageRecordCleanupRequiredCursorJobNames = []string{"usage_stats_aggregation", "client_ip_stats_aggregation"}

// UsageRecordsStore 是使用记录清理（cleanupProcessedUsageRecordsBeforeWithResult
// / Async），对应 retention.UsageRecordsCleaner。
type UsageRecordsStore struct {
	Catalog *DB
	Stats   *DB
}

type cleanupCursor struct {
	CreatedAt string
	ID        string
}

// CleanupProcessedBefore 执行一个清理批次（分片安全游标 + PG 分区裁剪语义）。
func (s *UsageRecordsStore) CleanupProcessedBefore(ctx context.Context, cutoffCreatedAt string, limit int) (retention.UsageRecordsBatch, error) {
	return s.cleanupProcessedBeforePostgres(ctx, cutoffCreatedAt, batchLimit(limit))
}

func (s *UsageRecordsStore) missingCursorBlockedReason() string {
	return "使用记录清理的统计聚合安全游标尚未建立，暂不清理使用记录，避免破坏统计聚合；请确认后台 worker 正常运行后稍后重试"
}

type UsageCleanupRow struct {
	ID        string
	CreatedAt string
}

func placeholderList(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func (s *UsageRecordsStore) cleanupProcessedBeforePostgres(ctx context.Context, cutoffCreatedAt string, batch int) (retention.UsageRecordsBatch, error) {
	cursor, err := s.postgresFloorCursor(ctx)
	if err != nil {
		return retention.UsageRecordsBatch{}, err
	}
	if cursor == nil {
		exists, existsErr := s.postgresHasRecordsBefore(ctx, cutoffCreatedAt)
		if existsErr != nil {
			return retention.UsageRecordsBatch{}, existsErr
		}
		if !exists {
			return retention.UsageRecordsBatch{CutoffCreatedAt: cutoffCreatedAt}, nil
		}
		return retention.UsageRecordsBatch{CutoffCreatedAt: cutoffCreatedAt, BlockedReason: s.missingCursorBlockedReason()}, nil
	}
	partitionDrop, err := s.dropEligiblePartitions(ctx, cutoffCreatedAt, *cursor)
	if err != nil {
		return retention.UsageRecordsBatch{}, err
	}
	if partitionDrop.DroppedPartitions > 0 {
		return retention.UsageRecordsBatch{
			CutoffCreatedAt:       cutoffCreatedAt,
			SafetyCursorCreatedAt: cursor.CreatedAt,
			SafetyCursorID:        cursor.ID,
			DeletedRows:           partitionDrop.DeletedRows,
			DroppedPartitions:     partitionDrop.DroppedPartitions,
			HasMore:               partitionDrop.HasMore,
		}, nil
	}
	rows, err := s.selectPostgresCleanupRows(ctx, cutoffCreatedAt, *cursor, batch+1)
	if err != nil {
		return retention.UsageRecordsBatch{}, err
	}
	rowsToDelete := rows
	if len(rowsToDelete) > batch {
		rowsToDelete = rowsToDelete[:batch]
	}
	deletedRows, err := s.deletePostgresRows(ctx, rowsToDelete)
	if err != nil {
		return retention.UsageRecordsBatch{}, err
	}
	return retention.UsageRecordsBatch{
		CutoffCreatedAt:       cutoffCreatedAt,
		SafetyCursorCreatedAt: cursor.CreatedAt,
		SafetyCursorID:        cursor.ID,
		DeletedRows:           deletedRows,
		HasMore:               len(rows) > batch,
	}, nil
}

func (s *UsageRecordsStore) postgresFloorCursor(ctx context.Context) (*cleanupCursor, error) {
	query := s.Stats.Bind(`
      SELECT job_name, cursor_created_at, cursor_id
      FROM juhe_stats.stats_job_state
      WHERE scope_type = 'global'
        AND scope_id = ''
        AND job_name = ANY(?::text[])
        AND cursor_created_at IS NOT NULL
        AND cursor_id IS NOT NULL
      ORDER BY cursor_created_at ASC, cursor_id ASC
	`)
	rows, err := s.Stats.QueryContext(ctx, query, usageRecordCleanupRequiredCursorJobNames)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobNames := map[string]bool{}
	firstCursor := (*cleanupCursor)(nil)
	for rows.Next() {
		var jobName, id sql.NullString
		var createdAt dbtime.Nullable
		if err := rows.Scan(&jobName, &createdAt, &id); err != nil {
			return nil, err
		}
		if normalized := strings.TrimSpace(jobName.String); normalized != "" {
			jobNames[normalized] = true
		}
		if firstCursor == nil {
			cursorCreatedAt := createdAt.APIString()
			cursorID := strings.TrimSpace(id.String)
			if cursorCreatedAt != "" && cursorID != "" {
				firstCursor = &cleanupCursor{CreatedAt: cursorCreatedAt, ID: cursorID}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, jobName := range usageRecordCleanupRequiredCursorJobNames {
		if !jobNames[jobName] {
			return nil, nil
		}
	}
	return firstCursor, nil
}

func (s *UsageRecordsStore) postgresHasRecordsBefore(ctx context.Context, cutoffCreatedAt string) (bool, error) {
	rows, err := s.Catalog.QueryContext(ctx, s.Catalog.Bind(`
      SELECT 1 AS found
      FROM juhe_usage.usage_records
      WHERE created_at < ?
      ORDER BY created_at ASC, id ASC
      LIMIT 1
	`), cutoffCreatedAt)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	return rows.Next(), rows.Err()
}

type partitionDropOutcome struct {
	DeletedRows       int64
	DroppedPartitions int64
	HasMore           bool
}

var postgresPartitionNamePattern = regexp.MustCompile(`^usage_records_(\d{8})$`)

type usageRecordPartition struct {
	name  string
	start time.Time
	end   time.Time
	bound string
}

// dropEligiblePartitions 按“分区名 + 数据守卫”判断可删分区，不解析
// pg_get_expr 的边界文本：
//   - 分区名必须是 usage_records_YYYYMMDD 且是合法 UTC 日历日；
//   - 整个 UTC 日的结束必须 <= min(cutoff, cursor)；
//   - 分区内不能有未结算水位行；
//   - 非空分区的 min/max(created_at) 必须落在名字对应的 UTC 日内，否则
//     说明名字与数据不一致，跳过并告警。
func (s *UsageRecordsStore) dropEligiblePartitions(ctx context.Context, cutoffCreatedAt string, cursor cleanupCursor) (partitionDropOutcome, error) {
	outcome := partitionDropOutcome{}
	cutoffInstant, ok := dbtime.ParseDB(cutoffCreatedAt)
	if !ok {
		slog.Warn("usage records 分区清理跳过：cutoff 时间格式非法",
			"cutoff", cutoffCreatedAt)
		return outcome, nil
	}
	cursorInstant, ok := dbtime.ParseDB(cursor.CreatedAt)
	if !ok {
		slog.Warn("usage records 分区清理跳过：cursor 时间格式非法",
			"cursor_created_at", cursor.CreatedAt, "cursor_id", cursor.ID)
		return outcome, nil
	}
	safeEnd := cutoffInstant
	if cursorInstant.Before(safeEnd) {
		safeEnd = cursorInstant
	}

	rows, err := s.Catalog.QueryContext(ctx, `
      SELECT child.relname AS partition_name,
             pg_get_expr(child.relpartbound, child.oid) AS partition_bound
      FROM pg_inherits inherit
      JOIN pg_class parent ON parent.oid = inherit.inhparent
      JOIN pg_namespace parent_namespace ON parent_namespace.oid = parent.relnamespace
      JOIN pg_class child ON child.oid = inherit.inhrelid
      WHERE parent_namespace.nspname = 'juhe_usage'
        AND parent.relname = 'usage_records'
        AND child.relname LIKE 'usage_records_%'
      ORDER BY child.relname ASC
	`)
	if err != nil {
		return outcome, err
	}
	var candidates []usageRecordPartition
	for rows.Next() {
		var name, bound sql.NullString
		if err := rows.Scan(&name, &bound); err != nil {
			rows.Close()
			return outcome, err
		}
		nameText := strings.TrimSpace(name.String)
		start, ok := usageRecordPartitionDayStart(nameText)
		if !ok {
			continue
		}
		end := start.AddDate(0, 0, 1)
		if end.After(safeEnd) {
			continue
		}
		candidates = append(candidates, usageRecordPartition{
			name: nameText, start: start, end: end, bound: strings.TrimSpace(bound.String),
		})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return outcome, err
	}
	rows.Close()
	sort.Slice(candidates, func(left, right int) bool {
		return candidates[left].start.Before(candidates[right].start)
	})
	for index, candidate := range candidates {
		// 分区 DROP 前必须确认分区内没有未结算水位行：cursor 只能证明“已消费到
		// 哪个时间点”，不能证明 cursor 覆盖范围内的晚到行已被消费。存在 NULL
		// 行的分区跳过，等聚合器结算后再删。
		var total, pending int64
		var minCreated, maxCreated dbtime.Nullable
		if err := s.Catalog.QueryRowContext(ctx, fmt.Sprintf(
			`SELECT COUNT(*) AS total,
			        COUNT(*) FILTER (WHERE usage_stats_aggregated_at IS NULL OR client_ip_stats_aggregated_at IS NULL) AS pending,
			        MIN(created_at) AS min_created,
			        MAX(created_at) AS max_created
			 FROM juhe_usage.%s`, pgQuoteIdentifier(candidate.name))).Scan(
			&total, &pending, &minCreated, &maxCreated); err != nil {
			return outcome, err
		}
		if pending > 0 {
			continue
		}
		if minCreated.Valid || maxCreated.Valid {
			if !minCreated.Valid || !maxCreated.Valid ||
				minCreated.Time.Before(candidate.start) || !maxCreated.Time.Before(candidate.end) {
				slog.Warn("使用记录分区名与数据范围不一致，跳过清理",
					"partition", candidate.name,
					"partition_bound", candidate.bound,
					"min_created_at", nullableLogTime(minCreated),
					"max_created_at", nullableLogTime(maxCreated),
					"expected_start", candidate.start.Format(time.RFC3339),
					"expected_end", candidate.end.Format(time.RFC3339))
				continue
			}
		}
		tx, err := s.Catalog.BeginTx(ctx, nil)
		if err != nil {
			return outcome, err
		}
		if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = '2s'`); err != nil {
			_ = tx.Rollback()
			return outcome, err
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(
			`ALTER TABLE juhe_usage.usage_records DETACH PARTITION juhe_usage.%s`, pgQuoteIdentifier(candidate.name))); err != nil {
			_ = tx.Rollback()
			return outcome, err
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(
			`DROP TABLE IF EXISTS juhe_usage.%s`, pgQuoteIdentifier(candidate.name))); err != nil {
			_ = tx.Rollback()
			return outcome, err
		}
		if err := tx.Commit(); err != nil {
			return outcome, err
		}
		return partitionDropOutcome{
			DeletedRows:       total,
			DroppedPartitions: 1,
			HasMore:           index+1 < len(candidates),
		}, nil
	}
	return outcome, nil
}

// usageRecordPartitionDayStart 从分区名解析 UTC 日 00:00；名字不匹配或日期
// 非法返回 false。
func usageRecordPartitionDayStart(name string) (time.Time, bool) {
	match := postgresPartitionNamePattern.FindStringSubmatch(strings.TrimSpace(name))
	if match == nil {
		return time.Time{}, false
	}
	raw := match[1]
	return statcalendar.ParseDay(statcalendar.Day(raw[0:4] + "-" + raw[4:6] + "-" + raw[6:8]))
}

func nullableLogTime(value dbtime.Nullable) any {
	if !value.Valid {
		return nil
	}
	return dbtime.API6(value.Time)
}

func pgQuoteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func (s *UsageRecordsStore) selectPostgresCleanupRows(ctx context.Context, cutoffCreatedAt string, cursor cleanupCursor, limit int) ([]UsageCleanupRow, error) {
	rows, err := s.Catalog.QueryContext(ctx, s.Catalog.Bind(`
      SELECT id, created_at
      FROM juhe_usage.usage_records
      WHERE created_at < ?
        AND (created_at < ? OR (created_at = ? AND id <= ?))
        AND usage_stats_aggregated_at IS NOT NULL
        AND client_ip_stats_aggregated_at IS NOT NULL
      ORDER BY created_at ASC, id ASC
      LIMIT ?
	`), cutoffCreatedAt, cursor.CreatedAt, cursor.CreatedAt, cursor.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var output []UsageCleanupRow
	for rows.Next() {
		var id, createdAt sql.NullString
		if err := rows.Scan(&id, &createdAt); err != nil {
			return nil, err
		}
		if strings.TrimSpace(id.String) == "" || strings.TrimSpace(createdAt.String) == "" {
			continue
		}
		output = append(output, UsageCleanupRow{ID: strings.TrimSpace(id.String), CreatedAt: strings.TrimSpace(createdAt.String)})
	}
	return output, rows.Err()
}

// deletePostgresRows 照 deletePostgresUsageRecordRows。
func (s *UsageRecordsStore) deletePostgresRows(ctx context.Context, rows []UsageCleanupRow) (int64, error) {
	type key struct{ createdAt, id string }
	var keys []key
	seen := map[string]bool{}
	for _, row := range rows {
		id := strings.TrimSpace(row.ID)
		createdAt := strings.TrimSpace(row.CreatedAt)
		if id == "" || createdAt == "" || seen[id] {
			continue
		}
		seen[id] = true
		keys = append(keys, key{createdAt, id})
	}
	if len(keys) == 0 {
		return 0, nil
	}
	tx, err := s.Catalog.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	placeholders := strings.TrimSuffix(strings.Repeat("(?, ?),", len(keys)), ",")
	args := make([]any, 0, len(keys)*2)
	for _, item := range keys {
		args = append(args, item.createdAt, item.id)
	}
	result, err := tx.ExecContext(ctx, s.Catalog.Bind(fmt.Sprintf(
		`DELETE FROM juhe_usage.usage_records WHERE (created_at, id) IN (%s)`, placeholders)), args...)
	if err != nil {
		return 0, err
	}
	deletedRows, err := changes(result)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deletedRows, nil
}
