package statsagg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// 窗口刷新引擎，移植 usage-stats.repository.ts 的
// refreshUsageRankSnapshotsInStages / refreshHotUsageWindowSnapshots 编排：
//   - source watermark：源表 updated_at 最大值（空表用 0001-01-01T00:00:00.000Z）；
//   - job state：stats_job_state(scope_type='global', scope_id='', job_name=?)，
//     cursor_created_at=watermark、cursor_id=refreshDate（todayKey）；
//   - skipIfUnchanged：watermark 与 refreshDate 都未变化则整轮跳过。

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
	StageUsageScopeRangeWindows               WindowStageName = "usage_scope_range_windows"
	// StageAuthorizationUsageRangeWindows 已随授权消耗明细改为日摘要表现场
	// 聚合而停用：不再注册进刷新编排（selectStages/runStage 均不认领），组合
	// 根装配已移除；常量仅由停用语义断言测试（w10c“未知阶段应报错”）引用。
	StageAuthorizationUsageRangeWindows WindowStageName = "authorization_usage_range_windows"
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
	case StageUsageScopeRangeWindows:
		return []string{"usage_stats_daily"}
	}
	return nil
}

// 窗口 stage 刷新模式（UsageRankStageRun.Mode 取值，契约见
// 《统计指标与分层聚合设计》「概览与 AI 性能窗口的脏范围增量重建」）：
//   - full：todayKey 与 lastFullRebuildDayKey 不同（含启动首轮）的天滚动
//     全量重建；
//   - incremental：同日内按脏表 generation CAS 消费并逐账户重建；
//   - noop：增量轮脏表为空，零 DELETE 零 INSERT。
const (
	WindowRefreshModeFull        = "full"
	WindowRefreshModeIncremental = "incremental"
	WindowRefreshModeNoop        = "noop"
)

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
	// Mode 记录本轮刷新模式（常量 WindowRefreshMode*）；仅
	// StageUsageOverviewWindows / StageAiPerformanceSummaryWindows 两个
	// stage 填充，其余 stage 恒全量语义，字段省略。
	Mode string `json:"mode,omitempty"`
}

// WindowRefresher 执行窗口刷新 job（usage-rank-snapshots-refresh、
// usage-overview-windows-refresh、ai-performance-summary-windows-refresh、
// usage-scope-range-windows-refresh、usage-hot-window-refresh、
// usage-quota-hourly-windows-refresh）。授权范围
// 窗口刷新（authorization-usage-range-windows-refresh）已停用：授权消耗明细
// 读端改为按日范围直读 authorization_*_usage_summary_daily。
type WindowRefresher struct {
	DB      *sql.DB
	Dialect Dialect
	Clock   StatsTimezoneProvider
	Now     func() time.Time
	// BusinessDB 是业务库句柄（request_quota_hourly_window_scope_bindings
	// 绑定表所在）。PG 与 stats 同池时可留 nil（回退 DB + juhe_business.
	// 前缀）；SQLite 必须由组合根注入独立业务库连接，配额小时窗全量重建
	// 依赖它读绑定表。
	BusinessDB *sql.DB
	// ScopeLimit 覆盖配额小时窗单轮消费的脏 scope 上限（归档 :1871 固定
	// 128）；0 用默认值，测试可调小以覆盖分页续跑。
	ScopeLimit int
	// overviewFullRebuildDayKey / aiPerformanceFullRebuildDayKey 是两个窗口
	// stage 各自的全量重建日键（进程内内存态，不持久化、无配置项）：
	// todayKey 与记录值不同（含零值 = 进程启动后首轮）时该 stage 走全量
	// 重建并在事务提交成功后记录当日键，同一天内的后续轮走脏范围增量。
	// 组合根共享同一 WindowRefresher 实例，字段随实例在 job 间自然共享；
	// 离线重建进程一次性 RunStages(nil) 首轮即全量。
	overviewFullRebuildDayKey      string
	aiPerformanceFullRebuildDayKey string
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
		StageUsageScopeRangeWindows,
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
	if len(stages) == 8 {
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
	if options.SkipIfUnchanged && sourceState != nil {
		previousState, err = w.rankRefreshJobState(ctx, jobName)
		if err != nil {
			return UsageRankSnapshotRefreshResult{}, err
		}
	}
	sourceUnchanged := rankSnapshotSourceUnchanged(previousState, sourceState, todayKey)
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
	// dirtyRemaining 汇总各 stage 的事务内脏表剩余标记：任一 stage 报告有
	// 残留（含 CAS miss 与并发再打脏）时本轮不推进 job 水位，确保下轮
	// watermark-skip 不会把残留脏行跳过去。
	dirtyRemaining := false
	for _, stage := range stages {
		stageStartedAt := w.now()
		mode, stageDirtyRemaining, err := w.runStage(ctx, stage, refreshStageContext{
			timezone:   timezone,
			updatedAt:  FormatRFC3339Millis(now),
			snapshotAt: FormatRFC3339Millis(now),
			todayKey:   todayKey,
		})
		if err != nil {
			return UsageRankSnapshotRefreshResult{}, err
		}
		stageRuntimes = append(stageRuntimes, UsageRankStageRun{Name: string(stage), DurationMs: w.now().Sub(stageStartedAt).Milliseconds(), Mode: mode})
		dirtyRemaining = dirtyRemaining || stageDirtyRemaining
	}
	if options.SkipIfUnchanged && sourceState != nil && !dirtyRemaining {
		if err := w.updateRankRefreshJobState(ctx, jobName, rankRefreshStateInput{
			SourceWatermark: sourceState.sourceWatermark,
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

// runStage 执行单个 stage（既有 BeginTx/Commit 边界不变），返回该 stage 的
// 刷新模式（无模式语义的 stage 返回空串）与"事务内脏表是否仍有剩余行"：
// 后者为 true 时 RunStages 保留该 job 的 stats_job_state 水位不推进，防止
// 下轮 watermark-skip 把残留脏行跳过去。
func (w *WindowRefresher) runStage(ctx context.Context, stage WindowStageName, stageContext refreshStageContext) (string, bool, error) {
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback() }()
	var mode string
	var dirtyRemaining bool
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
		mode, dirtyRemaining, err = w.refreshUsageOverviewWindowSnapshots(ctx, tx, stageContext)
	case StageAiPerformanceSummaryWindows:
		mode, dirtyRemaining, err = w.refreshAiPerformanceSummaryWindows(ctx, tx, stageContext)
	case StageUsageScopeRangeWindows:
		err = w.refreshUsageScopeRangeWindows(ctx, tx, stageContext)
	default:
		err = fmt.Errorf("未知用量排行快照刷新阶段: %s", stage)
	}
	if err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	// 全量重建日键在事务提交成功后才记录：回滚不得推进内存游标。
	switch stage {
	case StageUsageOverviewWindows:
		if mode == WindowRefreshModeFull {
			w.overviewFullRebuildDayKey = stageContext.todayKey
		}
	case StageAiPerformanceSummaryWindows:
		if mode == WindowRefreshModeFull {
			w.aiPerformanceFullRebuildDayKey = stageContext.todayKey
		}
	}
	return mode, dirtyRemaining, nil
}

// ---- source watermark / job state ----

type rankSnapshotSourceState struct {
	sourceWatermark string
}

func (s *rankSnapshotSourceState) sourceWatermarkOrEmpty() string {
	if s == nil {
		return ""
	}
	return s.sourceWatermark
}

// sourceState mirrors usageRankSnapshotSourceState。
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
	return &rankSnapshotSourceState{sourceWatermark: watermark}, nil
}

type rankRefreshJobState struct {
	cursorCreatedAt            *string
	cursorID                   *string
	legacyEmptySourceWatermark bool
}

// rankRefreshJobState mirrors usageRankSnapshotRefreshJobState。
func (w *WindowRefresher) rankRefreshJobState(ctx context.Context, jobName string) (*rankRefreshJobState, error) {
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

// rankSnapshotSourceUnchanged mirrors usageRankSnapshotSourceUnchanged。
func rankSnapshotSourceUnchanged(previousState *rankRefreshJobState, sourceState *rankSnapshotSourceState, refreshDate string) bool {
	if previousState == nil || sourceState == nil {
		return false
	}
	if previousState.legacyEmptySourceWatermark {
		return false
	}
	if previousState.cursorCreatedAt == nil || *previousState.cursorCreatedAt != sourceState.sourceWatermark {
		return false
	}
	if previousState.cursorID == nil || *previousState.cursorID != refreshDate {
		return false
	}
	return true
}

type rankRefreshStateInput struct {
	SourceWatermark string
	RefreshDate     string
	LastSuccessAt   string
}

// updateRankRefreshJobState mirrors updateUsageRankSnapshotRefreshJobState：
// 主状态 upsert。
func (w *WindowRefresher) updateRankRefreshJobState(ctx context.Context, jobName string, input rankRefreshStateInput) error {
	sourceWatermark, err := RequiredRFC3339Instant(input.SourceWatermark, "用量排行快照 sourceWatermark")
	if err != nil {
		return err
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
	return tx.Commit()
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
