package statsagg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 窗口刷新引擎，移植 usage-stats.repository.ts 的
// refreshUsageRankSnapshotsInStages / refreshHotUsageWindowSnapshots 编排：
//   - source watermark：源表 updated_at 最大值（空表用 0001-01-01T00:00:00.000Z）；
//   - job state：stats_job_state(scope_type='global', scope_id='', job_name=?)，
//     cursor_created_at=watermark、cursor_id=refreshDate（todayKey）；
//   - skipIfUnchanged：watermark 与 refreshDate 都未变化则整轮跳过；
//   - system_metrics_trend 阶段额外带 sourceVersion 状态行。

// WindowStageName 对齐 UsageRankSnapshotStageName。
type WindowStageName string

const (
	StageAccountLast7dRequestRank             WindowStageName = "account_last7d_request_rank"
	StageCallerAccountLast7dRequestRank       WindowStageName = "caller_account_last7d_request_rank"
	StageApiKeyCurrentMonthCostRank           WindowStageName = "api_key_current_month_cost_rank"
	StageAccountAuthorizationCurrentMonthRank WindowStageName = "account_authorization_current_month_cost_rank"
	StageGroupAuthorizationCurrentMonthRank   WindowStageName = "group_authorization_current_month_cost_rank"
	StageUsageOverviewWindows                 WindowStageName = "usage_overview_windows"
	StageAiPerformanceSummaryWindows          WindowStageName = "ai_performance_summary_windows"
	StageSystemMetricsTrendWindows            WindowStageName = "system_metrics_trend_windows"
	StageUsageScopeRangeWindows               WindowStageName = "usage_scope_range_windows"
	StageAuthorizationUsageRangeWindows       WindowStageName = "authorization_usage_range_windows"
)

// hotUsageWindowStageNames mirrors hotUsageWindowStageNames。
var hotUsageWindowStageNames = []WindowStageName{StageUsageOverviewWindows, StageUsageScopeRangeWindows}

// stageSourceTables mirrors usageRankSnapshotStages 的 sourceTables。
func stageSourceTables(stage WindowStageName) []string {
	switch stage {
	case StageAccountLast7dRequestRank, StageCallerAccountLast7dRequestRank, StageAiPerformanceSummaryWindows:
		return []string{"usage_stats_daily"}
	case StageApiKeyCurrentMonthCostRank, StageAccountAuthorizationCurrentMonthRank, StageGroupAuthorizationCurrentMonthRank:
		return []string{"usage_stats_monthly"}
	case StageUsageOverviewWindows:
		return []string{"usage_stats_totals", "usage_stats_daily", "usage_stats_hourly", "usage_model_daily", "usage_error_daily"}
	case StageSystemMetricsTrendWindows:
		return []string{"system_metrics_hourly", "process_event_loop_hourly"}
	case StageUsageScopeRangeWindows:
		return []string{"usage_stats_daily"}
	case StageAuthorizationUsageRangeWindows:
		return []string{"authorization_team_usage_summary_daily", "authorization_user_usage_summary_daily"}
	}
	return nil
}

// UsageRankSnapshotRefreshResult mirrors UsageRankSnapshotRefreshResult。
type UsageRankSnapshotRefreshResult struct {
	DurationMs      int64               `json:"durationMs"`
	Stages          []UsageRankStageRun `json:"stages"`
	Skipped         bool                `json:"skipped,omitempty"`
	SkipReason      string              `json:"skipReason,omitempty"`
	SourceWatermark string              `json:"sourceWatermark,omitempty"`
	RefreshDate     string              `json:"refreshDate,omitempty"`
	JobName         string              `json:"jobName,omitempty"`
}

// UsageRankStageRun mirrors UsageRankSnapshotStageRuntime。
type UsageRankStageRun struct {
	Name       string `json:"name"`
	DurationMs int64  `json:"durationMs"`
}

// WindowRefresher 执行窗口刷新 job（usage-rank-snapshots-refresh、
// usage-overview-windows-refresh、system-metrics-trend-windows-refresh、
// ai-performance-summary-windows-refresh、usage-scope-range-windows-refresh、
// authorization-usage-range-windows-refresh、usage-hot-window-refresh）。
type WindowRefresher struct {
	DB      *sql.DB
	Dialect Dialect
	Clock   StatsTimezoneProvider
	Now     func() time.Time
}

func (w *WindowRefresher) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *WindowRefresher) selectStages(stageNames []WindowStageName) ([]WindowStageName, error) {
	all := []WindowStageName{
		StageAccountLast7dRequestRank,
		StageCallerAccountLast7dRequestRank,
		StageApiKeyCurrentMonthCostRank,
		StageAccountAuthorizationCurrentMonthRank,
		StageGroupAuthorizationCurrentMonthRank,
		StageUsageOverviewWindows,
		StageAiPerformanceSummaryWindows,
		StageSystemMetricsTrendWindows,
		StageUsageScopeRangeWindows,
		StageAuthorizationUsageRangeWindows,
	}
	if stageNames == nil {
		return all, nil
	}
	if len(stageNames) == 0 {
		return nil, errors.New("用量排行快照刷新至少需要一个阶段")
	}
	selected := make([]WindowStageName, 0, len(stageNames))
	known := map[WindowStageName]bool{}
	for _, stage := range all {
		known[stage] = true
	}
	for _, name := range stageNames {
		if !known[name] {
			return nil, fmt.Errorf("未知用量排行快照刷新阶段: %s", name)
		}
		selected = append(selected, name)
	}
	return selected, nil
}

// DefaultJobName mirrors usageRankSnapshotDefaultJobName。
func DefaultJobName(stages []WindowStageName) string {
	if len(stages) == 10 {
		return "usage_rank_snapshots_refresh"
	}
	result := "usage_rank_snapshots_refresh:"
	for index, stage := range stages {
		if index > 0 {
			result += "+"
		}
		result += string(stage)
	}
	return result
}

// RunStages mirrors refreshUsageRankSnapshotsInStages（PG 路径语义，
// SQLite 测试共用同一编排）。
func (w *WindowRefresher) RunStages(ctx context.Context, stageNames []WindowStageName, options RefreshOptions) (UsageRankSnapshotRefreshResult, error) {
	startedAt := w.now()
	stages, err := w.selectStages(stageNames)
	if err != nil {
		return UsageRankSnapshotRefreshResult{}, err
	}
	jobName := options.JobName
	if jobName == "" {
		jobName = DefaultJobName(stages)
	}
	timezone, err := w.Clock.StatsTimezone(ctx)
	if err != nil {
		return UsageRankSnapshotRefreshResult{}, err
	}
	now := w.now()
	todayKey := dateKey(now, timezone)
	sourceState, err := w.sourceState(ctx, stages)
	if err != nil {
		return UsageRankSnapshotRefreshResult{}, err
	}
	var previousState *rankRefreshJobState
	var previousSourceVersion string
	if options.SkipIfUnchanged && sourceState != nil {
		allowLegacy := len(stages) == 1 && stages[0] == StageSystemMetricsTrendWindows
		previousState, err = w.rankRefreshJobState(ctx, jobName, allowLegacy)
		if err != nil {
			return UsageRankSnapshotRefreshResult{}, err
		}
		if allowLegacy && sourceState.sourceVersion != "" {
			previousSourceVersion, err = w.previousSourceVersion(ctx, jobName, previousState)
			if err != nil {
				return UsageRankSnapshotRefreshResult{}, err
			}
		}
	}
	sourceUnchanged := rankSnapshotSourceUnchanged(previousState, sourceState, todayKey, previousSourceVersion)
	if sourceUnchanged {
		return UsageRankSnapshotRefreshResult{
			DurationMs:      w.now().Sub(startedAt).Milliseconds(),
			Stages:          []UsageRankStageRun{},
			Skipped:         true,
			SkipReason:      "source_watermark_unchanged",
			SourceWatermark: sourceState.sourceWatermark,
			RefreshDate:     todayKey,
			JobName:         jobName,
		}, nil
	}

	stageRuntimes := []UsageRankStageRun{}
	for _, stage := range stages {
		stageStartedAt := w.now()
		if err := w.runStage(ctx, stage, refreshStageContext{
			timezone:   timezone,
			updatedAt:  FormatRFC3339Millis(now),
			snapshotAt: FormatRFC3339Millis(now),
			todayKey:   todayKey,
		}); err != nil {
			return UsageRankSnapshotRefreshResult{}, err
		}
		stageRuntimes = append(stageRuntimes, UsageRankStageRun{Name: string(stage), DurationMs: w.now().Sub(stageStartedAt).Milliseconds()})
	}
	if options.SkipIfUnchanged && sourceState != nil {
		if err := w.updateRankRefreshJobState(ctx, jobName, rankRefreshStateInput{
			SourceWatermark: sourceState.sourceWatermark,
			SourceVersion:   sourceState.sourceVersion,
			RefreshDate:     todayKey,
			LastSuccessAt:   FormatRFC3339Millis(w.now()),
		}); err != nil {
			return UsageRankSnapshotRefreshResult{}, err
		}
	}
	return UsageRankSnapshotRefreshResult{
		DurationMs:      w.now().Sub(startedAt).Milliseconds(),
		Stages:          stageRuntimes,
		SourceWatermark: sourceState.sourceWatermarkOrEmpty(),
		RefreshDate:     todayKey,
		JobName:         jobName,
	}, nil
}

// RunHotWindows mirrors refreshHotUsageWindowSnapshots：固定跑
// usage_overview_windows + usage_scope_range_windows 两个热阶段。
func (w *WindowRefresher) RunHotWindows(ctx context.Context, options RefreshOptions) (UsageRankSnapshotRefreshResult, error) {
	return w.RunStages(ctx, hotUsageWindowStageNames, options)
}

// RefreshOptions mirrors RefreshUsageRankSnapshotsInStagesOptions。
type RefreshOptions struct {
	SkipIfUnchanged bool
	JobName         string
}

type refreshStageContext struct {
	timezone   *time.Location
	updatedAt  string
	snapshotAt string
	todayKey   string
}

func (w *WindowRefresher) runStage(ctx context.Context, stage WindowStageName, stageContext refreshStageContext) error {
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	switch stage {
	case StageAccountLast7dRequestRank:
		err = refreshAccountLast7dRequestRankSnapshot(ctx, tx, w.Dialect, stageContext.snapshotAt, stageContext.updatedAt, stageContext.timezone, w.now())
	case StageCallerAccountLast7dRequestRank:
		err = refreshCallerAccountLast7dRequestRankSnapshot(ctx, tx, w.Dialect, stageContext.snapshotAt, stageContext.updatedAt, stageContext.timezone, w.now())
	case StageApiKeyCurrentMonthCostRank:
		err = refreshApiKeyCurrentMonthCostRankSnapshot(ctx, tx, w.Dialect, stageContext.snapshotAt, stageContext.updatedAt, stageContext.timezone, w.now())
	case StageAccountAuthorizationCurrentMonthRank:
		err = refreshAuthorizationCurrentMonthCostRankSnapshot(ctx, tx, w.Dialect, "account_authorization", stageContext.snapshotAt, stageContext.updatedAt, stageContext.timezone, w.now())
	case StageGroupAuthorizationCurrentMonthRank:
		err = refreshAuthorizationCurrentMonthCostRankSnapshot(ctx, tx, w.Dialect, "group_authorization", stageContext.snapshotAt, stageContext.updatedAt, stageContext.timezone, w.now())
	case StageUsageOverviewWindows:
		err = w.refreshUsageOverviewWindowSnapshots(ctx, tx, stageContext)
	case StageAiPerformanceSummaryWindows:
		err = w.refreshAiPerformanceSummaryWindows(ctx, tx, stageContext)
	case StageSystemMetricsTrendWindows:
		err = w.refreshSystemMetricsTrendWindowsStage(ctx, tx, stageContext)
	case StageUsageScopeRangeWindows:
		err = w.refreshUsageScopeRangeWindows(ctx, tx, stageContext)
	case StageAuthorizationUsageRangeWindows:
		err = w.refreshAuthorizationUsageRangeWindows(ctx, tx, stageContext)
	default:
		err = fmt.Errorf("未知用量排行快照刷新阶段: %s", stage)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ---- source watermark / job state ----

type rankSnapshotSourceState struct {
	sourceWatermark string
	sourceVersion   string
}

func (s *rankSnapshotSourceState) sourceWatermarkOrEmpty() string {
	if s == nil {
		return ""
	}
	return s.sourceWatermark
}

// sourceState mirrors usageRankSnapshotSourceState（system_metrics_trend
// 单阶段时附加 sourceVersion）。
func (w *WindowRefresher) sourceState(ctx context.Context, stages []WindowStageName) (*rankSnapshotSourceState, error) {
	sourceTablesSet := map[string]struct{}{}
	for _, stage := range stages {
		for _, table := range stageSourceTables(stage) {
			sourceTablesSet[table] = struct{}{}
		}
	}
	sourceTables := make([]string, 0, len(sourceTablesSet))
	for table := range sourceTablesSet {
		sourceTables = append(sourceTables, table)
	}
	sortStrings(sourceTables)
	watermark := EmptySourceWatermark
	watermarkMilliseconds := int64(-1 << 62)
	for _, table := range sourceTables {
		query := w.Dialect.bind(`SELECT updated_at FROM ` + w.Dialect.StatsTable(table) + ` WHERE updated_at IS NOT NULL`)
		rows, err := w.DB.QueryContext(ctx, query)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var updatedAt sql.NullString
			if err := rows.Scan(&updatedAt); err != nil {
				rows.Close()
				return nil, err
			}
			if !updatedAt.Valid {
				continue
			}
			normalized, ok := CanonicalizeRFC3339Instant(updatedAt.String)
			if !ok {
				rows.Close()
				return nil, fmt.Errorf("用量排行快照 %s.updated_at必须是带 Z 或数值 offset 的 RFC3339 时间", table)
			}
			milliseconds, ok := RFC3339Milliseconds(normalized)
			if !ok {
				rows.Close()
				return nil, fmt.Errorf("用量排行快照 %s.updated_at必须是带 Z 或数值 offset 的 RFC3339 时间", table)
			}
			if milliseconds > watermarkMilliseconds {
				watermark = normalized
				watermarkMilliseconds = milliseconds
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	state := &rankSnapshotSourceState{sourceWatermark: watermark}
	if len(stages) == 1 && stages[0] == StageSystemMetricsTrendWindows {
		version, err := systemMetricsTrendSourceVersion(ctx, w.DB, w.Dialect)
		if err != nil {
			return nil, err
		}
		state.sourceVersion = version
	}
	return state, nil
}

type rankRefreshJobState struct {
	cursorCreatedAt            *string
	cursorID                   *string
	legacyEmptySourceWatermark bool
	legacySourceVersion        string
}

// rankRefreshJobState mirrors usageRankSnapshotRefreshJobState。
func (w *WindowRefresher) rankRefreshJobState(ctx context.Context, jobName string, allowLegacySourceWatermark bool) (*rankRefreshJobState, error) {
	query := w.Dialect.bind(`SELECT cursor_created_at, cursor_id FROM ` + w.Dialect.StatsTable("stats_job_state") +
		` WHERE scope_type = ? AND scope_id = ? AND job_name = ?`)
	var cursorCreatedAt, cursorID sql.NullString
	err := w.DB.QueryRowContext(ctx, query, RankSnapshotJobStateScopeType, RankSnapshotJobStateScopeID, jobName).Scan(&cursorCreatedAt, &cursorID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	state := &rankRefreshJobState{}
	if cursorCreatedAt.Valid && cursorCreatedAt.String != "" {
		value := cursorCreatedAt.String
		if value == LegacyEmptySourceWatermark {
			state.legacyEmptySourceWatermark = true
			state.cursorCreatedAt = &value
		} else if allowLegacySourceWatermark {
			separator := strings.Index(value, "|")
			if separator >= 0 {
				watermark := value[:separator]
				if _, ok := CanonicalizeRFC3339Instant(watermark); !ok {
					return nil, errors.New("用量排行快照 sourceWatermark必须是带 Z 或数值 offset 的 RFC3339 时间")
				}
				state.cursorCreatedAt = &watermark
				state.legacySourceVersion = value[separator+1:]
			} else {
				if _, ok := CanonicalizeRFC3339Instant(value); !ok {
					return nil, errors.New("用量排行快照 sourceWatermark必须是带 Z 或数值 offset 的 RFC3339 时间")
				}
				state.cursorCreatedAt = &value
			}
		} else {
			if _, ok := CanonicalizeRFC3339Instant(value); !ok {
				return nil, errors.New("用量排行快照 sourceWatermark必须是带 Z 或数值 offset 的 RFC3339 时间")
			}
			state.cursorCreatedAt = &value
		}
	}
	if cursorID.Valid {
		state.cursorID = &cursorID.String
	}
	return state, nil
}

// previousSourceVersion mirrors usageRankSnapshotPreviousSourceVersionFromState。
func (w *WindowRefresher) previousSourceVersion(ctx context.Context, jobName string, previousState *rankRefreshJobState) (string, error) {
	query := w.Dialect.bind(`SELECT cursor_created_at, cursor_id FROM ` + w.Dialect.StatsTable("stats_job_state") +
		` WHERE scope_type = ? AND scope_id = ? AND job_name = ?`)
	var cursorCreatedAt, cursorID sql.NullString
	err := w.DB.QueryRowContext(ctx, query, RankSnapshotSourceVersionScopeType, RankSnapshotSourceVersionScopeID, jobName).Scan(&cursorCreatedAt, &cursorID)
	var versionState *rankSnapshotSourceState
	if !errors.Is(err, sql.ErrNoRows) {
		if err != nil {
			return "", err
		}
		if !cursorCreatedAt.Valid || cursorCreatedAt.String == "" {
			return "", fmt.Errorf("用量排行快照 sourceVersion 状态缺少 sourceWatermark: %s", jobName)
		}
		if !cursorID.Valid || cursorID.String == "" {
			return "", fmt.Errorf("用量排行快照 sourceVersion 状态缺少 sourceVersion: %s", jobName)
		}
		versionState = &rankSnapshotSourceState{sourceWatermark: cursorCreatedAt.String, sourceVersion: cursorID.String}
	}
	// usageRankSnapshotPreviousSourceVersionFromState
	if previousState == nil {
		if versionState != nil {
			return "", fmt.Errorf("用量排行快照 sourceVersion 状态缺少主刷新状态: %s", jobName)
		}
		return "", nil
	}
	if previousState.cursorCreatedAt == nil || *previousState.cursorCreatedAt == "" {
		return "", fmt.Errorf("用量排行快照主刷新状态缺少 sourceWatermark: %s", jobName)
	}
	if previousState.legacySourceVersion != "" {
		if versionState == nil {
			return previousState.legacySourceVersion, nil
		}
		if versionState.sourceWatermark == *previousState.cursorCreatedAt && versionState.sourceVersion == previousState.legacySourceVersion {
			return previousState.legacySourceVersion, nil
		}
		return "", fmt.Errorf("用量排行快照 legacy sourceVersion 状态与当前状态不一致: %s", jobName)
	}
	if versionState == nil {
		return "", fmt.Errorf("用量排行快照 sourceVersion 状态缺失: %s", jobName)
	}
	if versionState.sourceWatermark != *previousState.cursorCreatedAt {
		return "", fmt.Errorf("用量排行快照 sourceVersion 状态与主刷新水位不一致: %s", jobName)
	}
	return versionState.sourceVersion, nil
}

// rankSnapshotSourceUnchanged mirrors usageRankSnapshotSourceUnchanged。
func rankSnapshotSourceUnchanged(previousState *rankRefreshJobState, sourceState *rankSnapshotSourceState, refreshDate, previousSourceVersion string) bool {
	if previousState == nil || sourceState == nil {
		return false
	}
	if previousState.legacyEmptySourceWatermark {
		return false
	}
	// 旧布局将时间戳与版本存同一字段，强制刷新一次以迁移到双行形态。
	if previousState.legacySourceVersion != "" {
		return false
	}
	if previousState.cursorCreatedAt == nil || *previousState.cursorCreatedAt != sourceState.sourceWatermark {
		return false
	}
	if previousState.cursorID == nil || *previousState.cursorID != refreshDate {
		return false
	}
	if sourceState.sourceVersion != "" && previousSourceVersion != sourceState.sourceVersion {
		return false
	}
	return true
}

type rankRefreshStateInput struct {
	SourceWatermark string
	SourceVersion   string
	RefreshDate     string
	LastSuccessAt   string
}

// updateRankRefreshJobState mirrors updateUsageRankSnapshotRefreshJobState：
// 主状态 upsert；sourceVersion 存在时同步 sourceVersion 状态行，否则删除。
func (w *WindowRefresher) updateRankRefreshJobState(ctx context.Context, jobName string, input rankRefreshStateInput) error {
	sourceWatermark, err := RequiredRFC3339Instant(input.SourceWatermark, "用量排行快照 sourceWatermark")
	if err != nil {
		return err
	}
	if input.SourceVersion != "" {
		if err := requireSystemMetricsTrendSourceVersion(input.SourceVersion); err != nil {
			return err
		}
	}
	lastSuccessAt, err := RequiredRFC3339Instant(input.LastSuccessAt, "用量排行快照 lastSuccessAt")
	if err != nil {
		return err
	}
	updatedAt := FormatRFC3339Millis(w.now())
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	upsert := w.Dialect.bind(`
		INSERT INTO ` + w.Dialect.StatsTable("stats_job_state") + ` (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, ?)
		ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
		  cursor_created_at = excluded.cursor_created_at,
		  cursor_id = excluded.cursor_id,
		  last_success_at = excluded.last_success_at,
		  last_error_message = NULL,
		  lag_seconds = NULL,
		  updated_at = excluded.updated_at
	`)
	if _, err := tx.ExecContext(ctx, upsert,
		RankSnapshotJobStateScopeType, RankSnapshotJobStateScopeID, jobName,
		sourceWatermark, input.RefreshDate, lastSuccessAt, updatedAt); err != nil {
		return err
	}
	deleteVersion := w.Dialect.bind(`DELETE FROM ` + w.Dialect.StatsTable("stats_job_state") + ` WHERE scope_type = ? AND scope_id = ? AND job_name = ?`)
	if input.SourceVersion == "" {
		if _, err := tx.ExecContext(ctx, deleteVersion, RankSnapshotSourceVersionScopeType, RankSnapshotSourceVersionScopeID, jobName); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, upsert,
			RankSnapshotSourceVersionScopeType, RankSnapshotSourceVersionScopeID, jobName,
			sourceWatermark, input.SourceVersion, lastSuccessAt, updatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
