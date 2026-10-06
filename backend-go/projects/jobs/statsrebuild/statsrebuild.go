// Package statsrebuild 是 jobs 模块的受控导出面：统计缓存离线重建编排
// （BUG-0182）。它只编排 internal/statsagg 的既有聚合与窗口刷新实现——
// 聚合口径、游标推进语义、窗口阶段全部复用 Aggregator /
// WindowRefresher，自身不含第二套口径 SQL；唯一消费方是 maintenance CLI
// 的 rebuild-usage-stats 子命令（基线受控例外登记见
// docs/architecture/Go三项目架构基线.md §2 与
// scripts/regression/go-project-boundary-regression.mjs 白名单）。
//
// 重建语义（对齐 BUG-0182 记载的 Node rebuild-usage-stats 归档脚本）：
//  1. 清空统计缓存面（本包 rebuildClearedStatsTables 清单，全部是
//     statsagg 实际写入的 usage 派生结果表）并重置 stats_job_state 中
//     usage_stats_aggregation（聚合游标回零重放）与
//     usage_quota_hourly_windows_expiry（配额小时窗 expiry 游标）两行；
//  2. 从 usage_records 逐批重放聚合（AggregateUsageStatsBatch，批间各自
//     提交，中断后重跑本编排即重新清空并从零重放）；
//  3. 重放排干后运行 WindowRefresher.RunStages 全部九个窗口/排行阶段
//     （usage_rank_snapshots、overview/model/error rank windows、
//     ai_performance_summary_windows、scope range windows、system metrics
//     trend windows），使统计缓存面在一次离线执行内回到与在线收敛一致的
//     状态；
//  4. 绝不写业务库（juhe_business / 业务 SQLite 文件只读）与 usage_records
//     源表（SQLite standalone 的聚合事实源是 stats 库内 usage_records
//     镜像，PG 是 juhe_usage.usage_records，两者都只读）。
//
// dirty 队列处置（显式契约，见 BUG-0182 验收记录）：
//   - usage_overview_dirty_scopes / ai_performance_summary_dirty_system_accounts：
//     重放会再生标记，但窗口阶段已在同一次执行内全量重建，标记失去语义且
//     Go 侧无增量消费者——完成路径末尾清空；
//   - usage_quota_hourly_window_dirty_scopes：保留重放再生的标记。离线不重刷
//     配额小时窗（SQLite 全量重建需要业务库绑定表输入且在线任务每轮全量重建；
//     PG 增量路径消费这些标记即可把清空后的 usage_quota_hourly_windows 补建），
//     恢复在线后的首轮 usage-quota-hourly-windows-refresh 负责补齐；
//   - account_quality_dirty_accounts：保留（gateway 账号质量刷新按它重算
//     account_quality_scores，重放已为其再生标记）。
//
// 空源语义：usage_records 无可重放记录时（首批即空），历史统计明确放弃，
// 后续从新请求重新累计，正常完成（不作为错误）。
//
// 吞吐：BatchSize 默认 2000（钳 1..50000）、MaxBatches 默认 1000
// （钳 1..10000）；单轮达到批数上限时返回 Drained=false，由调用方提示
// 再次执行完成重建。
package statsrebuild

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
	"github.com/huanminabc/juhe-ai/backend-go-platform/sqldialect"
)

// bind 复用平台方言改写（statsagg.Dialect.bind 的共享实现，导出面不可引用
// 非导出方法）。
func bind(postgres bool, query string) string {
	return sqldialect.BindSQL(postgres, query)
}

// 吞吐参数默认值与钳制边界，对齐 BUG-0182 记载的 Node 脚本缺省值。
const (
	DefaultBatchSize  = 2000
	MinBatchSize      = 1
	MaxBatchSize      = 50000
	DefaultMaxBatches = 1000
	MinMaxBatches     = 1
	MaxMaxBatches     = 10000
)

// rebuildClearedStatsTables 是重建清空的统计结果表清单（以 statsagg 实际
// 写入面为准，rg 求证于 internal/statsagg 的 aggregate.go / upserts.go /
// stages_rank.go / stages_window_snapshots.go / stages_quota.go / dirty_scopes.go）。
// 只允许出现 stats 库结果表；usage_records 源表与业务库表绝不可加入。
var rebuildClearedStatsTables = []string{
	// usage_stats 汇总 + 5 时间桶（upsertUsageStatsTotals / upsertUsageStatsTimeBucket）
	"usage_stats_totals",
	"usage_stats_minute",
	"usage_stats_hourly",
	"usage_stats_daily",
	"usage_stats_weekly",
	"usage_stats_monthly",
	// usage_model_*（upsertUsageModelEntries）
	"usage_model_minute",
	"usage_model_hourly",
	"usage_model_daily",
	"usage_model_weekly",
	"usage_model_monthly",
	// usage_error_*（upsertUsageErrorEntries）
	"usage_error_minute",
	"usage_error_hourly",
	"usage_error_daily",
	"usage_error_weekly",
	"usage_error_monthly",
	// usage_latency_*（upsertUsageLatencyEntries）
	"usage_latency_minute",
	"usage_latency_hourly",
	"usage_latency_daily",
	"usage_latency_weekly",
	"usage_latency_monthly",
	// 账户质量/健康（upsertAccountQualityEntries / upsertAccountHealthEntries）
	"account_quality_minute_stats",
	"account_health_hourly",
	// 授权消耗日摘要（upsertAuthorizationUsageReportRows）
	"authorization_team_usage_summary_daily",
	"authorization_user_usage_summary_daily",
	// 授权范围窗口退役表（StageAuthorizationUsageRangeWindows 已随授权消耗
	// 明细改为日摘要直读而停用：无写入方、无读者，仅 retention 按 end_date
	// 修边）。重建顺带清空，防止僵尸行永久滞留误导排障（边界审计 2026-10-06）。
	"authorization_team_usage_range_windows",
	"authorization_user_usage_range_windows",
	// 排行快照（stages_rank.go）
	"usage_rank_snapshots",
	// 派生窗口（stages_window_snapshots.go）
	"ai_performance_summary_windows",
	"usage_overview_summary_windows",
	"usage_overview_trend_windows",
	"usage_model_rank_windows",
	"usage_error_rank_windows",
	"usage_scope_range_windows",
	// 配额小时窗结果（stages_quota.go；离线不重刷，恢复在线后补建，见包注释）
	"usage_quota_hourly_windows",
}

// rebuildResetJobStateNames 是重建时删除的 stats_job_state 游标行
// （scope_type='global', scope_id=”）：聚合游标回零重放 + 配额小时窗
// expiry 游标回零（其窗口表已清空，游标必须一并归零，否则 PG 增量路径
// 只会从旧水位向前补建）。其余 stats_job_state 行（client-ip 聚合游标、
// 排行刷新水位、配额刷新失败记录等）不动：client-ip 统计面不在本次重建
// 范围，水位行会因源表水位变化自然失效重刷。
var rebuildResetJobStateNames = []string{
	"usage_stats_aggregation",
	"usage_quota_hourly_windows_expiry",
}

// completionClearedDirtyTables 在窗口阶段全量重建完成后清空的脏队列
// （标记已失去语义且 Go 侧无增量消费者；保留只会误导排障）。
var completionClearedDirtyTables = []string{
	"usage_overview_dirty_scopes",
	"ai_performance_summary_dirty_system_accounts",
}

// Options 承载一次离线重建的依赖与吞吐参数。导出面不暴露 internal 类型：
// 方言用 Postgres 布尔承载，窗口阶段结果用本包 WindowStageRun 承载。
type Options struct {
	// DB 是 stats 库句柄：SQLite standalone 传统计结果库 SQLite 连接
	// （usage_records 镜像同库），PG 传含 juhe_usage/juhe_stats/juhe_business
	// schema 的连接池。
	DB *sql.DB
	// Postgres 选择方言：true=PG（juhe_stats./juhe_business. 前缀 + $n 占位符），
	// false=SQLite standalone（裸表名 + ? 占位符）。
	Postgres bool
	// Clock 解析统计时区；nil 时按 dialect 从业务库 system_settings 读取
	// usageStatsTimezone（与 statsverify.LoadUsageStatsTimezone 同源）。
	Clock statsagg.StatsTimezoneProvider
	// BusinessDB 是业务库句柄：SQLite 模式必填（聚合授权链查找
	// resource_authorizations/accounts 与默认时区读取所在，stats 库没有
	// 这些表）；PG 与 stats 同池时可留 nil。
	BusinessDB *sql.DB
	// Now 注入当前时间（测试用）；nil 时取 time.Now。
	Now func() time.Time
	// BatchSize/MaxBatches 吞吐参数；非零值按边界钳制，零值用默认。
	BatchSize  int
	MaxBatches int
	// Logf 接收进度输出；nil 时静默。
	Logf func(format string, args ...any)
}

// WindowStageRun 是窗口阶段执行记录的导出面形态（内部对齐
// statsagg.UsageRankStageRun）。
type WindowStageRun struct {
	Name       string `json:"name"`
	DurationMs int64  `json:"durationMs"`
}

// Result 汇总一次重建执行的报告面（maintenance CLI 原样编码为 JSON）。
type Result struct {
	Dialect    string `json:"dialect"`
	BatchSize  int    `json:"batchSize"`
	MaxBatches int    `json:"maxBatches"`
	// ClearedTables 是清空阶段实际执行 DELETE 的统计结果表（含完成阶段
	// 清理的两个脏队列表）。
	ClearedTables []string `json:"clearedTables"`
	// ResetJobStates 是删除的 stats_job_state 游标行 job_name。
	ResetJobStates []string `json:"resetJobStates"`
	Batches        int      `json:"batches"`
	ProcessedRows  int      `json:"processedRows"`
	// Drained 表示重放已排干（usage_records 全部消费）。false = 达到
	// MaxBatches 上限，统计缓存处于部分重建状态，需再次执行完成。
	Drained bool `json:"drained"`
	// EmptySource 表示无可重放历史记录（首批即空）：历史统计放弃，后续
	// 从新请求重新累计。
	EmptySource  bool             `json:"emptySource"`
	WindowStages []WindowStageRun `json:"windowStages"`
	DurationMs   int64            `json:"durationMs"`
}

// Rebuild 执行一次离线重建编排。返回值总是可编码的报告；错误仅在清空、
// 聚合或窗口阶段执行失败时返回。达到批数上限不是错误（Drained=false）。
func Rebuild(ctx context.Context, options Options) (Result, error) {
	dialect := statsagg.Dialect{Postgres: options.Postgres}
	startedAt := nowFunc(options.Now)
	result := Result{
		Dialect:        dialectName(dialect),
		BatchSize:      clampInt(options.BatchSize, DefaultBatchSize, MinBatchSize, MaxBatchSize),
		MaxBatches:     clampInt(options.MaxBatches, DefaultMaxBatches, MinMaxBatches, MaxMaxBatches),
		ClearedTables:  append([]string{}, rebuildClearedStatsTables...),
		ResetJobStates: append([]string{}, rebuildResetJobStateNames...),
	}
	logf := options.Logf
	if options.DB == nil {
		return Result{}, errors.New("统计缓存重建缺少 stats 库句柄")
	}
	clock := options.Clock
	if clock == nil {
		businessDB := options.BusinessDB
		if businessDB == nil && options.Postgres {
			businessDB = options.DB
		}
		if businessDB == nil {
			return Result{}, errors.New("SQLite 模式统计缓存重建缺少业务库句柄（授权链查找与时区读取必需）")
		}
		clock = settingsTimezoneSource{db: businessDB, postgres: options.Postgres}
	}

	// 1. 清空统计缓存面 + 重置游标（单事务：半清空状态不可见）。
	if err := clearStatsSurface(ctx, options.DB, dialect, logf); err != nil {
		return Result{}, fmt.Errorf("清空统计缓存面失败: %w", err)
	}

	// 2. 从 usage_records 逐批重放聚合（每批独立事务提交，复用
	// statsagg 游标推进语义）。
	aggregator := &statsagg.Aggregator{
		DB:         options.DB,
		Dialect:    dialect,
		Clock:      clock,
		BusinessDB: options.BusinessDB,
		Now:        options.Now,
	}
	for batch := 1; batch <= result.MaxBatches; batch++ {
		processed, err := aggregator.AggregateUsageStatsBatch(ctx, statsagg.AggregateOptions{BatchSize: result.BatchSize})
		if err != nil {
			return Result{}, fmt.Errorf("统计聚合第 %d 批失败: %w", batch, err)
		}
		result.Batches = batch
		result.ProcessedRows += processed
		if logf != nil {
			logf("统计聚合批次 %d/%d 处理 %d 行（累计 %d 行）", batch, result.MaxBatches, processed, result.ProcessedRows)
		}
		if processed == 0 {
			result.Drained = true
			result.EmptySource = batch == 1
			break
		}
		if processed < result.BatchSize {
			result.Drained = true
			break
		}
	}

	// 3. 排干后全量重建窗口/排行阶段；未排干（达到上限）时保持部分重建
	// 状态返回，由调用方提示续跑。
	if result.Drained {
		refresher := &statsagg.WindowRefresher{
			DB:         options.DB,
			Dialect:    dialect,
			Clock:      clock,
			BusinessDB: options.BusinessDB,
			Now:        options.Now,
		}
		windowResult, err := refresher.RunStages(ctx, nil, statsagg.RefreshOptions{})
		if err != nil {
			return Result{}, fmt.Errorf("统计窗口阶段刷新失败: %w", err)
		}
		result.WindowStages = make([]WindowStageRun, 0, len(windowResult.Stages))
		for _, stage := range windowResult.Stages {
			result.WindowStages = append(result.WindowStages, WindowStageRun{Name: stage.Name, DurationMs: stage.DurationMs})
		}
		if err := clearCompletionDirtyQueues(ctx, options.DB, dialect); err != nil {
			return Result{}, fmt.Errorf("清理已完成窗口脏队列失败: %w", err)
		}
	}
	result.DurationMs = nowFunc(options.Now).Sub(startedAt).Milliseconds()
	return result, nil
}

// clearStatsSurface 在单事务内清空全部统计结果表并重置游标行。
func clearStatsSurface(ctx context.Context, db *sql.DB, dialect statsagg.Dialect, logf func(format string, args ...any)) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, table := range rebuildClearedStatsTables {
		if _, err := tx.ExecContext(ctx, clearTableSQL(dialect, table)); err != nil {
			return fmt.Errorf("DELETE FROM %s: %w", dialect.StatsTable(table), err)
		}
		if logf != nil {
			logf("已清空统计结果表 %s", dialect.StatsTable(table))
		}
	}
	args := make([]any, 0, len(rebuildResetJobStateNames))
	for _, jobName := range rebuildResetJobStateNames {
		args = append(args, jobName)
	}
	if _, err := tx.ExecContext(ctx, resetJobStateSQL(dialect), args...); err != nil {
		return fmt.Errorf("重置 stats_job_state 游标失败: %w", err)
	}
	if logf != nil {
		logf("已重置 stats_job_state 游标：%v", rebuildResetJobStateNames)
	}
	return tx.Commit()
}

// clearTableSQL 生成单表清空语句（PG 形态带 juhe_stats. 前缀并经 $n 改写；
// 独立成纯函数供语句形态断言测试核对）。
func clearTableSQL(dialect statsagg.Dialect, table string) string {
	return bind(dialect.Postgres, `DELETE FROM `+dialect.StatsTable(table))
}

// resetJobStateSQL 生成游标重置语句（形态契约同上）。
func resetJobStateSQL(dialect statsagg.Dialect) string {
	return bind(dialect.Postgres, `DELETE FROM `+dialect.StatsTable("stats_job_state")+`
		WHERE scope_type = 'global' AND scope_id = ''
		  AND job_name IN (`+placeholders(len(rebuildResetJobStateNames))+`)`)
}

// clearCompletionDirtyQueues 清空窗口全量重建后已无语义的脏队列。
func clearCompletionDirtyQueues(ctx context.Context, db *sql.DB, dialect statsagg.Dialect) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, table := range completionClearedDirtyTables {
		if _, err := tx.ExecContext(ctx, bind(dialect.Postgres, `DELETE FROM `+dialect.StatsTable(table))); err != nil {
			return fmt.Errorf("DELETE FROM %s: %w", dialect.StatsTable(table), err)
		}
	}
	return tx.Commit()
}

// settingsTimezoneSource 是默认统计时区源：业务库 system_settings 的
// usageStatsTimezone（与 statsverify.LoadUsageStatsTimezone 同一查询语义；
// 不 import statsverify——它是 internal 包，此处只保留读模型）。
type settingsTimezoneSource struct {
	db       *sql.DB
	postgres bool
}

func (s settingsTimezoneSource) StatsTimezone(ctx context.Context) (*time.Location, error) {
	table := "system_settings"
	if s.postgres {
		table = "juhe_business.system_settings"
	}
	var rawValue sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT value_json FROM `+table+
		` WHERE system_account_id = 'sys_admin' AND key = 'usageStatsTimezone' LIMIT 1`).Scan(&rawValue)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("系统设置缺少 usageStatsTimezone")
	}
	if err != nil {
		return nil, fmt.Errorf("读取 usageStatsTimezone 失败: %w", err)
	}
	if !rawValue.Valid || rawValue.String == "" {
		return nil, errors.New("系统设置缺少 usageStatsTimezone")
	}
	var value string
	if err := json.Unmarshal([]byte(rawValue.String), &value); err != nil {
		return nil, fmt.Errorf("系统设置 usageStatsTimezone 无效: %w", err)
	}
	location, err := time.LoadLocation(value)
	if err != nil {
		return nil, fmt.Errorf("统计时区不存在：%s", value)
	}
	return location, nil
}

// placeholders 生成 n 个逗号分隔的 `?` 占位符（经 Dialect.bind 转成 PG
// 连续编号 $n），与 internal/statsagg 同名 helper 语义一致（该符号不导出，
// 此处按值复制以避免为导出而改动既有包）。
func placeholders(count int) string {
	result := ""
	for index := 0; index < count; index++ {
		if index > 0 {
			result += ", "
		}
		result += "?"
	}
	return result
}

func clampInt(value, defaultValue, minValue, maxValue int) int {
	if value == 0 {
		return defaultValue
	}
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func dialectName(dialect statsagg.Dialect) string {
	if dialect.Postgres {
		return "postgres"
	}
	return "sqlite"
}

func nowFunc(now func() time.Time) time.Time {
	if now != nil {
		return now()
	}
	return time.Now()
}
