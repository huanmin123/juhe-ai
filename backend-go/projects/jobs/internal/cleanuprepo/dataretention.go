package cleanuprepo

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
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
	{"authorization_team_usage_range_windows", "end_date", "date"},
	{"authorization_user_usage_summary_daily", "stat_date", "date"},
	{"authorization_user_usage_range_windows", "end_date", "date"},
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
	{"system_metrics_samples", "sampled_at", "iso"},
	{"system_metrics_hourly", "stat_hour", "hour"},
	{"system_metrics_trend_windows", "end_date", "date"},
	{"process_event_loop_samples", "sampled_at", "iso"},
	{"process_event_loop_hourly", "stat_hour", "hour"},
	{"process_event_loop_trend_windows", "end_date", "date"},
}

var nonBusinessDatasetCleanupTables = []statsTableRule{
	{"public_api_logs", "created_at", "iso"},
	{"api_key_record_cleanup_targets", "updated_at", "iso"},
	{"account_record_cleanup_targets", "updated_at", "iso"},
}

var nonBusinessUsageCatalogCleanupTables = []statsTableRule{
	{"usage_record_account_shards", "last_seen_at", "iso"},
	{"usage_record_api_key_shards", "last_seen_at", "iso"},
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
	{"authorization_team_usage_range_windows", "end_date", "WindowDate", "stats"},
	{"authorization_user_usage_summary_daily", "stat_date", "Daily", "stats"},
	{"authorization_user_usage_range_windows", "end_date", "WindowDate", "stats"},
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
// （cleanupUsageStatsBucketsBefore / cleanupSystemMetricsBefore /
// cleanupNonBusinessDataBeforeWithResult 的 stats 半区）。
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
func deleteRowsBefore(ctx context.Context, db *DB, schema, tableName, timeColumnName, cutoff string, limit int) (int64, error) {
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
		if target == nil {
			return counts, fmt.Errorf("使用统计保留清理缺少结果字段映射：%s", spec.TableName)
		}
		deleted, err := deleteRowsBefore(ctx, s.DB, s.schemaForStats(), spec.TableName, spec.Column, values[spec.CutoffKey], batchLimit(input.Limit))
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
	case "authorization_team_usage_range_windows":
		return &counts.AuthorizationTeamUsageRangeWindows
	case "authorization_user_usage_summary_daily":
		return &counts.AuthorizationUserUsageSummaryDaily
	case "authorization_user_usage_range_windows":
		return &counts.AuthorizationUserUsageRangeWindows
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

// CleanupSystemMetricsRetention 照 cleanupSystemMetricsBefore（Async 变体）。
func (s *StatsRetentionStore) CleanupSystemMetricsRetention(ctx context.Context, input retention.SystemMetricsRetentionInput) (retention.SystemMetricsRetentionCounts, error) {
	counts := retention.SystemMetricsRetentionCounts{}
	specs := []struct {
		table  string
		column string
		cutoff string
		target *int64
	}{
		{"system_metrics_samples", "sampled_at", input.SamplesCutoffIso, &counts.SystemMetricsSamples},
		{"system_metrics_hourly", "stat_hour", input.HourlyCutoffHour, &counts.SystemMetricsHourly},
		{"system_metrics_trend_windows", "end_date", input.TrendWindowCutoffDate, &counts.SystemMetricsTrendWindows},
		{"process_event_loop_samples", "sampled_at", input.SamplesCutoffIso, &counts.ProcessEventLoopSamples},
		{"process_event_loop_hourly", "stat_hour", input.HourlyCutoffHour, &counts.ProcessEventLoopHourly},
		{"process_event_loop_trend_windows", "end_date", input.TrendWindowCutoffDate, &counts.ProcessEventLoopTrendWindows},
	}
	for _, spec := range specs {
		deleted, err := deleteRowsBefore(ctx, s.DB, s.schemaForStats(), spec.table, spec.column, spec.cutoff, batchLimit(input.Limit))
		if err != nil {
			return counts, err
		}
		*spec.target += deleted
	}
	s.AfterDelete(ctx)
	return counts, nil
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
		deleted, err := deleteRowsBefore(ctx, s.DB, s.schemaForStats(), rule.TableName, rule.TimeColumnName, cutoffs[rule.CutoffKey], batch)
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
// （scope='dataset'：usage records + dataset/usage-catalog 表 + 空分片文件），
// 对应 retention.NonBusinessDataCleaner。
type NonBusinessDatasetStore struct {
	Dataset      *DB
	UsageCatalog *DB
	Stats        *DB
	Shards       *ShardStore
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
	if !usageRecords.HasMore && usageRecords.BlockedReason == "" {
		for _, rule := range nonBusinessUsageCatalogCleanupTables {
			deleted, err := deleteRowsBefore(ctx, s.UsageCatalog, "", rule.TableName, rule.TimeColumnName, cutoffs[rule.CutoffKey], batch)
			if err != nil {
				return counts, err
			}
			addNonBusinessRows(&counts, "usage-catalog."+rule.TableName, deleted, int64(batch))
		}
	}

	if s.Shards != nil {
		empty, err := s.Shards.CleanupEmptyShardFilesBefore(ctx, s.UsageCatalog, cutoffs["iso"], batch)
		if err != nil {
			return counts, err
		}
		addNonBusinessRows(&counts, "usage_catalog.usage_record_shards", empty.UsageRecordShards, int64(batch))
		if empty.UsageShardFiles > 0 {
			counts.FileDeletes["usage_shard_files"] += empty.UsageShardFiles
			counts.DeletedFiles += empty.UsageShardFiles
		}
		if empty.HasMore {
			counts.HasMore = true
		}
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
	Shards  *ShardStore
}

type cleanupCursor struct {
	CreatedAt string
	ID        string
}

// CleanupProcessedBefore 执行一个清理批次（安全游标 + 分区裁剪语义）。
func (s *UsageRecordsStore) CleanupProcessedBefore(ctx context.Context, cutoffCreatedAt string, limit int) (retention.UsageRecordsBatch, error) {
	batch := batchLimit(limit)
	if s.Catalog.Postgres {
		return s.cleanupProcessedBeforePostgres(ctx, cutoffCreatedAt, batch)
	}
	return s.cleanupProcessedBeforeSQLite(ctx, cutoffCreatedAt, batch)
}

func (s *UsageRecordsStore) missingCursorBlockedReason() string {
	return "部分使用记录分片的统计安全游标尚未建立，暂不清理使用记录，避免破坏统计聚合；请确认后台 worker 正常运行后稍后重试"
}

func (s *UsageRecordsStore) cleanupProcessedBeforeSQLite(ctx context.Context, cutoffCreatedAt string, batch int) (retention.UsageRecordsBatch, error) {
	cursor, err := s.sqliteFloorCursor(ctx)
	if err != nil {
		return retention.UsageRecordsBatch{}, err
	}
	if cursor == nil {
		exists, existsErr := s.sqliteHasRecordsBefore(ctx, cutoffCreatedAt)
		if existsErr != nil {
			return retention.UsageRecordsBatch{}, existsErr
		}
		if !exists {
			return retention.UsageRecordsBatch{CutoffCreatedAt: cutoffCreatedAt}, nil
		}
		return retention.UsageRecordsBatch{CutoffCreatedAt: cutoffCreatedAt, BlockedReason: s.missingCursorBlockedReason()}, nil
	}
	rows, err := s.selectSQLiteCleanupRows(ctx, cutoffCreatedAt, *cursor, batch+1)
	if err != nil {
		return retention.UsageRecordsBatch{}, err
	}
	rowsToDelete := rows
	if len(rowsToDelete) > batch {
		rowsToDelete = rowsToDelete[:batch]
	}
	blockedReason, err := s.sqliteBlockedReasonForRows(ctx, rowsToDelete)
	if err != nil {
		return retention.UsageRecordsBatch{}, err
	}
	if blockedReason != "" {
		return retention.UsageRecordsBatch{
			CutoffCreatedAt:       cutoffCreatedAt,
			SafetyCursorCreatedAt: cursor.CreatedAt,
			SafetyCursorID:        cursor.ID,
			BlockedReason:         blockedReason,
		}, nil
	}
	deletedRows, err := s.deleteSQLiteShardRows(ctx, rowsToDelete)
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

func (s *UsageRecordsStore) sqliteFloorCursor(ctx context.Context) (*cleanupCursor, error) {
	query := s.Stats.Bind(fmt.Sprintf(`
      SELECT cursor_created_at, cursor_id
      FROM stats_job_state
      WHERE scope_type = 'usage_shard'
        AND job_name IN (%s)
        AND cursor_created_at IS NOT NULL
        AND cursor_id IS NOT NULL
      ORDER BY cursor_created_at ASC, cursor_id ASC
      LIMIT 1
	`, s.Stats.BindIn(len(usageRecordCleanupRequiredCursorJobNames))))
	rows, err := s.Stats.QueryContext(ctx, query, stringSliceToAny(usageRecordCleanupRequiredCursorJobNames)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var createdAt, id sql.NullString
		if err := rows.Scan(&createdAt, &id); err != nil {
			return nil, err
		}
		cursorCreatedAt := strings.TrimSpace(createdAt.String)
		cursorID := strings.TrimSpace(id.String)
		if cursorCreatedAt != "" && cursorID != "" {
			return &cleanupCursor{CreatedAt: cursorCreatedAt, ID: cursorID}, nil
		}
		return nil, rows.Err()
	}
	return nil, rows.Err()
}

func (s *UsageRecordsStore) sqliteHasRecordsBefore(ctx context.Context, cutoffCreatedAt string) (bool, error) {
	query := s.Catalog.Bind(`
      SELECT ue.usage_id
      FROM usage_record_shard_entries ue
      JOIN usage_record_shards s ON s.shard_key = ue.shard_key
      WHERE s.status = 'active'
        AND ue.created_at < ?
      ORDER BY ue.created_at ASC, ue.usage_id ASC
      LIMIT 1
	`)
	rows, err := s.Catalog.QueryContext(ctx, query, cutoffCreatedAt)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var usageID sql.NullString
		if err := rows.Scan(&usageID); err != nil {
			return false, err
		}
		found = usageID.String != ""
		break
	}
	return found, rows.Err()
}

func (s *UsageRecordsStore) selectSQLiteCleanupRows(ctx context.Context, cutoffCreatedAt string, cursor cleanupCursor, limit int) ([]ShardCleanupRow, error) {
	query := s.Catalog.Bind(fmt.Sprintf(`
      SELECT ue.usage_id, ue.created_at, s.shard_key, s.bucket_date, s.shard_id, s.file_path
      FROM usage_record_shard_entries ue
      JOIN usage_record_shards s ON s.shard_key = ue.shard_key
      WHERE s.status = 'active'
        AND ue.created_at < ?
        AND (ue.created_at < ? OR (ue.created_at = ? AND ue.usage_id <= ?))
      ORDER BY ue.created_at ASC, ue.usage_id ASC
      LIMIT ?
	`))
	rows, err := s.Catalog.QueryContext(ctx, query, cutoffCreatedAt, cursor.CreatedAt, cursor.CreatedAt, cursor.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var output []ShardCleanupRow
	for rows.Next() {
		var usageID, createdAt, shardKey, bucketDate, filePath sql.NullString
		var shardID sql.NullInt64
		if err := rows.Scan(&usageID, &createdAt, &shardKey, &bucketDate, &shardID, &filePath); err != nil {
			return nil, err
		}
		location := shardLocationFromRegistryRow(shardKey.String, bucketDate.String, shardID.Int64, filePath.String)
		id := strings.TrimSpace(usageID.String)
		createdAtText := strings.TrimSpace(createdAt.String)
		if id == "" || createdAtText == "" || location == nil {
			continue
		}
		output = append(output, ShardCleanupRow{ID: id, CreatedAt: createdAtText, Location: *location})
	}
	return output, rows.Err()
}

// ShardCleanupRow 照 UsageRecordShardCleanupRow。
type ShardCleanupRow struct {
	ID        string
	CreatedAt string
	Location  ShardLocation
}

func (s *UsageRecordsStore) sqliteBlockedReasonForRows(ctx context.Context, rows []ShardCleanupRow) (string, error) {
	seen := map[string]bool{}
	shardKeys := []string{}
	for _, row := range rows {
		key := strings.TrimSpace(row.Location.ShardKey)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		shardKeys = append(shardKeys, key)
	}
	if len(shardKeys) == 0 {
		return "", nil
	}
	covered, err := s.sqliteCursorShardKeysForShards(ctx, shardKeys)
	if err != nil {
		return "", err
	}
	for _, key := range shardKeys {
		if !covered[key] {
			return s.missingCursorBlockedReason(), nil
		}
	}
	return "", nil
}

func (s *UsageRecordsStore) sqliteCursorShardKeysForShards(ctx context.Context, shardKeys []string) (map[string]bool, error) {
	covered := map[string]bool{}
	for _, chunk := range chunkValues(uniqueNonEmpty(shardKeys), 900) {
		if len(chunk) == 0 {
			continue
		}
		query := s.Stats.Bind(fmt.Sprintf(`
        SELECT scope_id
        FROM stats_job_state
        WHERE scope_type = 'usage_shard'
          AND scope_id IN (%s)
          AND job_name IN (%s)
          AND cursor_created_at IS NOT NULL
          AND cursor_id IS NOT NULL
        GROUP BY scope_id
        HAVING COUNT(DISTINCT job_name) = ?
			`, s.Stats.BindIn(len(chunk)), s.Stats.BindIn(len(usageRecordCleanupRequiredCursorJobNames))))
		args := stringSliceToAny(chunk)
		args = append(args, stringSliceToAny(usageRecordCleanupRequiredCursorJobNames)...)
		args = append(args, len(usageRecordCleanupRequiredCursorJobNames))
		rows, err := s.Stats.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var scopeID sql.NullString
			if err := rows.Scan(&scopeID); err != nil {
				rows.Close()
				return nil, err
			}
			if normalized := strings.TrimSpace(scopeID.String); normalized != "" {
				covered[normalized] = true
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return covered, nil
}

// deleteSQLiteShardRows 照 deleteUsageRecordShardRows：按分片删行，再删目录条目。
func (s *UsageRecordsStore) deleteSQLiteShardRows(ctx context.Context, rows []ShardCleanupRow) (int64, error) {
	var deletedRows int64
	rowsByShard := map[string][]ShardCleanupRow{}
	var shardOrder []string
	for _, row := range rows {
		if _, ok := rowsByShard[row.Location.ShardKey]; !ok {
			shardOrder = append(shardOrder, row.Location.ShardKey)
		}
		rowsByShard[row.Location.ShardKey] = append(rowsByShard[row.Location.ShardKey], row)
	}
	var processedCatalogIDs []string
	for _, shardKey := range shardOrder {
		shardRows := rowsByShard[shardKey]
		ids := make([]string, 0, len(shardRows))
		for _, row := range shardRows {
			if row.ID != "" {
				ids = append(ids, row.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		db, err := s.Shards.Open(shardRows[0].Location.FilePath)
		if err != nil {
			return deletedRows, err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return deletedRows, err
		}
		for _, chunk := range chunkValues(ids, 900) {
			existingQuery := fmt.Sprintf(`SELECT id FROM usage_records WHERE id IN (%s)`, placeholderList(len(chunk)))
			existingRows, err := tx.QueryContext(ctx, placeholderBind(existingQuery), stringSliceToAny(chunk)...)
			if err != nil {
				_ = tx.Rollback()
				return deletedRows, err
			}
			var existingIDs []string
			for existingRows.Next() {
				var id sql.NullString
				if err := existingRows.Scan(&id); err != nil {
					existingRows.Close()
					_ = tx.Rollback()
					return deletedRows, err
				}
				if id.String != "" {
					existingIDs = append(existingIDs, id.String)
				}
			}
			if err := existingRows.Err(); err != nil {
				existingRows.Close()
				_ = tx.Rollback()
				return deletedRows, err
			}
			existingRows.Close()
			if len(existingIDs) == 0 {
				continue
			}
			result, err := tx.ExecContext(ctx, placeholderBind(fmt.Sprintf(
				`DELETE FROM usage_records WHERE id IN (%s)`, placeholderList(len(existingIDs)))),
				stringSliceToAny(existingIDs)...)
			if err != nil {
				_ = tx.Rollback()
				return deletedRows, err
			}
			affected, err := changes(result)
			if err != nil {
				_ = tx.Rollback()
				return deletedRows, err
			}
			deletedRows += affected
		}
		if err := tx.Commit(); err != nil {
			return deletedRows, err
		}
		processedCatalogIDs = append(processedCatalogIDs, ids...)
	}
	if _, err := s.Shards.DeleteShardEntries(ctx, s.Catalog, processedCatalogIDs); err != nil {
		return deletedRows, err
	}
	return deletedRows, nil
}

// placeholderList / placeholderBind：分片库直连句柄不带 DB 包装，走通用 `?`
// （分片库仅存在于 SQLite 模式）。
func placeholderList(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func placeholderBind(query string) string { return query }

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
		var jobName, createdAt, id sql.NullString
		if err := rows.Scan(&jobName, &createdAt, &id); err != nil {
			return nil, err
		}
		if normalized := strings.TrimSpace(jobName.String); normalized != "" {
			jobNames[normalized] = true
		}
		if firstCursor == nil {
			cursorCreatedAt := strings.TrimSpace(createdAt.String)
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

var postgresPartitionBoundPattern = regexp.MustCompile(`FOR VALUES FROM \('(\d{4}-\d{2}-\d{2})'\) TO \('(\d{4}-\d{2}-\d{2})'\)`)
var postgresPartitionNamePattern = regexp.MustCompile(`^usage_records_(\d{8})$`)

func (s *UsageRecordsStore) dropEligiblePartitions(ctx context.Context, cutoffCreatedAt string, cursor cleanupCursor) (partitionDropOutcome, error) {
	outcome := partitionDropOutcome{}
	cutoffDate := isoDatePrefix(cutoffCreatedAt)
	cursorDate := isoDatePrefix(cursor.CreatedAt)
	if cutoffDate == "" || cursorDate == "" {
		return outcome, nil
	}
	safeEndDate := cutoffDate
	if cursorDate < safeEndDate {
		safeEndDate = cursorDate
	}
	type partition struct {
		name  string
		start string
		end   string
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
	var eligible []partition
	for rows.Next() {
		var name, bound sql.NullString
		if err := rows.Scan(&name, &bound); err != nil {
			rows.Close()
			return outcome, err
		}
		nameText := strings.TrimSpace(name.String)
		if !postgresPartitionNamePattern.MatchString(nameText) {
			continue
		}
		match := postgresPartitionBoundPattern.FindStringSubmatch(bound.String)
		if match == nil {
			continue
		}
		if match[2] <= safeEndDate {
			eligible = append(eligible, partition{name: nameText, start: match[1], end: match[2]})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return outcome, err
	}
	rows.Close()
	sort.Slice(eligible, func(left, right int) bool {
		return eligible[left].start < eligible[right].start
	})
	if len(eligible) == 0 {
		return outcome, nil
	}
	target := eligible[0]
	var rowCount int64
	if err := s.Catalog.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COUNT(*) AS total FROM juhe_usage.%s`, pgQuoteIdentifier(target.name))).Scan(&rowCount); err != nil {
		return outcome, err
	}
	tx, err := s.Catalog.BeginTx(ctx, nil)
	if err != nil {
		return outcome, err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE juhe_usage.usage_records DETACH PARTITION juhe_usage.%s`, pgQuoteIdentifier(target.name))); err != nil {
		_ = tx.Rollback()
		return outcome, err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		`DROP TABLE IF EXISTS juhe_usage.%s`, pgQuoteIdentifier(target.name))); err != nil {
		_ = tx.Rollback()
		return outcome, err
	}
	if err := tx.Commit(); err != nil {
		return outcome, err
	}
	return partitionDropOutcome{DeletedRows: rowCount, DroppedPartitions: 1, HasMore: len(eligible) > 1}, nil
}

func pgQuoteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

// isoDatePrefix 照 Node isoDatePrefix：RFC3339 前缀的 YYYY-MM-DD（校验合法日期）。
func isoDatePrefix(value string) string {
	trimmed := strings.TrimSpace(value)
	pattern := regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})`)
	match := pattern.FindStringSubmatch(trimmed)
	if match == nil {
		return ""
	}
	year, month, day := match[1], match[2], match[3]
	parsed, err := time.Parse("2006-01-02", year+"-"+month+"-"+day)
	if err != nil || parsed.UTC().Year() != atoi(year) || int(parsed.UTC().Month()) != atoi(month) || parsed.UTC().Day() != atoi(day) {
		return ""
	}
	return year + "-" + month + "-" + day
}

func atoi(value string) int {
	out := 0
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return -1
		}
		out = out*10 + int(ch-'0')
	}
	return out
}

func (s *UsageRecordsStore) selectPostgresCleanupRows(ctx context.Context, cutoffCreatedAt string, cursor cleanupCursor, limit int) ([]ShardCleanupRow, error) {
	rows, err := s.Catalog.QueryContext(ctx, s.Catalog.Bind(`
      SELECT id, created_at
      FROM juhe_usage.usage_records
      WHERE created_at < ?
        AND (created_at < ? OR (created_at = ? AND id <= ?))
      ORDER BY created_at ASC, id ASC
      LIMIT ?
	`), cutoffCreatedAt, cursor.CreatedAt, cursor.CreatedAt, cursor.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var output []ShardCleanupRow
	for rows.Next() {
		var id, createdAt sql.NullString
		if err := rows.Scan(&id, &createdAt); err != nil {
			return nil, err
		}
		if strings.TrimSpace(id.String) == "" || strings.TrimSpace(createdAt.String) == "" {
			continue
		}
		output = append(output, ShardCleanupRow{ID: strings.TrimSpace(id.String), CreatedAt: strings.TrimSpace(createdAt.String)})
	}
	return output, rows.Err()
}

// deletePostgresUsageRecordCatalogRowsByUsageIds 照 usage-record-catalog-cleanup.ts。
func deletePostgresUsageRecordCatalogRowsByUsageIds(ctx context.Context, s *UsageRecordsStore, tx *sql.Tx, usageIDs []string) error {
	ids := uniqueNonEmpty(usageIDs)
	if len(ids) == 0 {
		return nil
	}
	scopes, err := listPostgresScopeEntries(ctx, s, tx, ids)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM juhe_usage.usage_record_shard_entries WHERE usage_id = ANY($1::text[])`, ids); err != nil {
		return err
	}
	return cleanupPostgresScopeShardCatalog(ctx, scopes, tx)
}

func listPostgresScopeEntries(ctx context.Context, s *UsageRecordsStore, tx *sql.Tx, ids []string) ([]scopeEntry, error) {
	rows, err := tx.QueryContext(ctx, `
      SELECT usage_id, shard_key, system_account_id, api_key_id, account_id
      FROM juhe_usage.usage_record_shard_entries
      WHERE usage_id = ANY($1::text[])
	`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var scopes []scopeEntry
	for rows.Next() {
		var entry scopeEntry
		var usageID, shardKey, systemAccountID, apiKeyID, accountID sql.NullString
		if err := rows.Scan(&usageID, &shardKey, &systemAccountID, &apiKeyID, &accountID); err != nil {
			return nil, err
		}
		entry.UsageID, entry.ShardKey, entry.SystemAccountID = usageID.String, shardKey.String, systemAccountID.String
		entry.APIKeyID, entry.AccountID = apiKeyID.String, accountID.String
		if entry.UsageID != "" && entry.ShardKey != "" && entry.SystemAccountID != "" {
			scopes = append(scopes, entry)
		}
	}
	return scopes, rows.Err()
}

func cleanupPostgresScopeShardCatalog(ctx context.Context, scopes []scopeEntry, tx *sql.Tx) error {
	accountScopes := map[string]bool{}
	apiKeyScopes := map[string]bool{}
	for _, scope := range scopes {
		if accountID := strings.TrimSpace(scope.AccountID); accountID != "" {
			accountScopes[accountID+"\x00"+scope.ShardKey] = true
		}
		if apiKeyID := strings.TrimSpace(scope.APIKeyID); apiKeyID != "" {
			apiKeyScopes[apiKeyID+"\x00"+scope.SystemAccountID+"\x00"+scope.ShardKey] = true
		}
	}
	for key := range accountScopes {
		parts := strings.SplitN(key, "\x00", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
      DELETE FROM juhe_usage.usage_record_account_shards scope
      WHERE scope.account_id = $1 AND scope.shard_key = $2
        AND NOT EXISTS (
          SELECT 1 FROM juhe_usage.usage_record_shard_entries entry
          WHERE entry.account_id = scope.account_id AND entry.shard_key = scope.shard_key
          LIMIT 1
        )
		`, parts[0], parts[1]); err != nil {
			return err
		}
	}
	for key := range apiKeyScopes {
		parts := strings.SplitN(key, "\x00", 3)
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
      DELETE FROM juhe_usage.usage_record_api_key_shards scope
      WHERE scope.api_key_id = $1 AND scope.system_account_id = $2 AND scope.shard_key = $3
        AND NOT EXISTS (
          SELECT 1 FROM juhe_usage.usage_record_shard_entries entry
          WHERE entry.api_key_id = scope.api_key_id
            AND entry.system_account_id = scope.system_account_id
            AND entry.shard_key = scope.shard_key
          LIMIT 1
        )
		`, parts[0], parts[1], parts[2]); err != nil {
			return err
		}
	}
	return nil
}

// deletePostgresRows 照 deletePostgresUsageRecordRows。
func (s *UsageRecordsStore) deletePostgresRows(ctx context.Context, rows []ShardCleanupRow) (int64, error) {
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
	usageIDs := make([]string, 0, len(keys))
	for _, item := range keys {
		usageIDs = append(usageIDs, item.id)
	}
	tx, err := s.Catalog.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := deletePostgresUsageRecordCatalogRowsByUsageIds(ctx, s, tx, usageIDs); err != nil {
		return 0, err
	}
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
