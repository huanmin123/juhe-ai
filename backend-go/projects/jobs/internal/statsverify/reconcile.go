package statsverify

// T93-6 / C2-H1：按 usage_records 重算最近窗口并与统计表对账。
//
// 场景：gateway 进程内 buffer（spool 落盘之前）不可观测，记录迟到入表时
// statsagg 的严格单调游标可能已经越过其 created_at → 记录永久不进入
// usage_stats_* 表。ingestgate 侧已把安全水位压到 now-5min 缩小窗口，但
// 边界仍可能存在单边缺失；本对账按 usage_records 重算 system_account 小时桶
// 并与 usage_stats_hourly 对比，发现差异只上报（Warn + 任务结果），不改写
// 任何表（与既有 daily/hourly 一致性检查同款「只检测不修复」语义）。
//
// 窗口语义（保证不产生虚假差异）：
//   - 上界取「统计聚合游标所在小时的起点」——只比较整小时，且这些小时内的
//     记录都已聚合（游标之后/未完成的当前小时不参与）；
//   - 下界 = 上界 - Window（默认 1 小时，按整小时对齐）；
//   - 记录侧按 created_at ∈ [下界, 上界) 重算，统计侧按 stat_hour ∈
//     [下界小时键, 上界小时键) 读取，两侧覆盖同一批整小时。
//
// 游标缺失（首次部署 / stats_job_state 尚未写入）时跳过并携带原因，不猜
// 聚合边界。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

const (
	// DefaultUsageStatsReconciliationWindow 是默认对账窗口（最近 1 小时）。
	DefaultUsageStatsReconciliationWindow = time.Hour
	// usageStatsReconciliationLoggedIssues 是日志里展示的差异条数上限（任务
	// 结果仍携带完整计数，不静默截断事实）。
	usageStatsReconciliationLoggedIssues = 20
)

// UsageStatsReconciliationOptions 承载对账窗口、时钟与聚合侧估算器。
type UsageStatsReconciliationOptions struct {
	// Now 是本轮时钟；零值取 time.Now。
	Now time.Time
	// Window 覆盖默认 1 小时窗口（<=0 取 DefaultUsageStatsReconciliationWindow）。
	Window time.Duration
	// CacheReadCostEstimator 必须与聚合器一致（cache_read_cost_usd 缺失时
	// 的估算口径），否则重算侧会出现口径性差异。
	CacheReadCostEstimator statsagg.CacheReadCostEstimator
}

// UsageStatsReconciliationIssue 是一条 (system account, stat_hour, metric)
// 的记录侧 / 统计侧差异。
type UsageStatsReconciliationIssue struct {
	SystemAccountID string
	ScopeID         string
	StatHour        string
	Metric          string
	RecordsValue    float64
	StatsValue      float64
}

// UsageStatsReconciliationResult 是对账结果（差异只上报、不修复）。
type UsageStatsReconciliationResult struct {
	WindowStartHour string
	WindowEndHour   string
	CheckedHours    int
	CheckedBuckets  int
	CheckedRecords  int
	IssueCount      int
	Issues          []UsageStatsReconciliationIssue
	Skipped         bool
	SkipReason      string
}

// usageStatsReconciliationMetrics 是对账比较的指标（顺序与
// usageStatsReconciliationValues 一一对应；成本类沿用一致性检查的 1e-6 容差，
// 其余精确比较）。
const usageStatsReconciliationMetricsCount = 15

var usageStatsReconciliationMetrics = [usageStatsReconciliationMetricsCount]string{
	"request_count",
	"success_count",
	"error_count",
	"input_tokens",
	"output_tokens",
	"cache_read_tokens",
	"cache_read_cost_usd",
	"cache_write_tokens",
	"cache_write_1h_tokens",
	"cache_write_cost_usd",
	"thinking_tokens",
	"input_image_tokens",
	"output_image_tokens",
	"total_cost_usd",
	"success_cost_usd",
}

// usageStatsReconciliationValues 按 usageStatsReconciliationMetrics 的顺序
// 取出累加器指标；两侧（重算 / 统计表）共用同一映射，避免口径错位。
func usageStatsReconciliationValues(accumulator statsagg.UsageStatsAccumulator) [usageStatsReconciliationMetricsCount]float64 {
	return [usageStatsReconciliationMetricsCount]float64{
		accumulator.RequestCount,
		accumulator.SuccessCount,
		accumulator.ErrorCount,
		accumulator.InputTokens,
		accumulator.OutputTokens,
		accumulator.CacheReadTokens,
		accumulator.CacheReadCostUsd,
		accumulator.CacheWriteTokens,
		accumulator.CacheWrite1hTokens,
		accumulator.CacheWriteCostUsd,
		accumulator.ThinkingTokens,
		accumulator.InputImageTokens,
		accumulator.OutputImageTokens,
		accumulator.TotalCostUsd,
		accumulator.SuccessCostUsd,
	}
}

// ReconcileUsageStatsHourly 执行一轮 usage_records → usage_stats_hourly 对账。
func (s *Store) ReconcileUsageStatsHourly(ctx context.Context, options UsageStatsReconciliationOptions) (UsageStatsReconciliationResult, error) {
	result := UsageStatsReconciliationResult{Issues: []UsageStatsReconciliationIssue{}}
	if s == nil || s.db == nil {
		return result, errors.New("statsverify store 未初始化")
	}
	now := options.Now
	if now.IsZero() {
		now = time.Now()
	}
	window := options.Window
	if window <= 0 {
		window = DefaultUsageStatsReconciliationWindow
	}
	location, _, err := s.LoadUsageStatsLocation(ctx, now)
	if err != nil {
		return result, err
	}
	cursor, ok, err := s.usageStatsAggregationCursor(ctx)
	if err != nil {
		return result, err
	}
	if !ok {
		result.Skipped = true
		result.SkipReason = "统计聚合游标尚未建立（stats_job_state 无 usage_stats_aggregation 行），无法确定已聚合边界"
		return result, nil
	}
	// 已聚合边界取「最旧未消费记录」的时刻：晚到记录被消费水位列兜住后，游标
	// 可能已经越过它（BUG-0345），拿游标当边界会把只聚合了一部分的小时计入
	// 比较面，制造假差异。无待消费记录时退回游标（它天然滞后于 now，保留
	// 「不比较当前小时」的保守口径）。
	reference := cursor
	if oldestPending, pending, err := statsagg.OldestPendingUsageRecordCreatedAt(ctx, s.db, statsagg.UsageRecordConsumptionUsageStats); err != nil {
		return result, err
	} else if pending {
		if parsed, parsedOK := statsagg.ParseRFC3339Instant(oldestPending); parsedOK {
			reference = parsed.UTC()
		}
	}
	// 时钟偏斜防御：边界晚于本机 now 时按 now 收口，绝不比较未聚合区间。
	if now.Before(reference) {
		reference = now
	}
	// 只比较整小时：游标所在小时可能只聚合了一部分记录，整小时区间才是
	// usage_stats_hourly 行与 usage_records 重算的可比面。
	endHour := truncateToHour(reference, location)
	hours := int(math.Ceil(window.Hours()))
	if hours < 1 {
		hours = 1
	}
	startHour := endHour.Add(-time.Duration(hours) * time.Hour)
	if !startHour.Before(endHour) {
		result.Skipped = true
		result.SkipReason = "对账窗口为空（聚合游标早于一个整小时）"
		return result, nil
	}
	result.WindowStartHour = HourKeyIn(startHour, location)
	result.WindowEndHour = HourKeyIn(endHour, location)
	result.CheckedHours = hours

	recomputed, admitted, err := statsagg.RecomputeSystemAccountHourlyTotals(
		ctx, s.db, options.CacheReadCostEstimator,
		statsagg.FormatRFC3339Millis(startHour.UTC()), statsagg.FormatRFC3339Millis(endHour.UTC()),
		location, statsagg.UsageStatsReconciliationBatchSize)
	if err != nil {
		return result, err
	}
	result.CheckedRecords = admitted

	persisted, err := s.sumUsageStatsHourlySystemScope(ctx, result.WindowStartHour, result.WindowEndHour)
	if err != nil {
		return result, err
	}
	keys := make([]statsagg.SystemHourlyTotalsKey, 0, len(recomputed)+len(persisted))
	seen := map[statsagg.SystemHourlyTotalsKey]bool{}
	for key := range recomputed {
		seen[key] = true
		keys = append(keys, key)
	}
	for key := range persisted {
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(left, right int) bool {
		if keys[left].StatHour != keys[right].StatHour {
			return keys[left].StatHour < keys[right].StatHour
		}
		if keys[left].SystemAccountID != keys[right].SystemAccountID {
			return keys[left].SystemAccountID < keys[right].SystemAccountID
		}
		return keys[left].ScopeID < keys[right].ScopeID
	})
	result.CheckedBuckets = len(keys)
	for _, key := range keys {
		recordValues := usageStatsReconciliationValues(recomputed[key])
		statsValues := usageStatsReconciliationValues(persisted[key])
		for index, metric := range usageStatsReconciliationMetrics {
			if math.Abs(recordValues[index]-statsValues[index]) <= consistencyMetricTolerance(metric) {
				continue
			}
			result.IssueCount++
			if len(result.Issues) < usageStatsReconciliationLoggedIssues {
				result.Issues = append(result.Issues, UsageStatsReconciliationIssue{
					SystemAccountID: key.SystemAccountID,
					ScopeID:         key.ScopeID,
					StatHour:        key.StatHour,
					Metric:          metric,
					RecordsValue:    recordValues[index],
					StatsValue:      statsValues[index],
				})
			}
		}
	}
	return result, nil
}

// RunUsageStatsReconciliation 是 usage-stats-consistency-check 的对账入口：
// 执行重算对账并把差异输出为 Warn（非静默）；返回结果供任务结果携带。
// window 由组合根传入（<=0 取 DefaultUsageStatsReconciliationWindow），
// estimator 必须与聚合器使用同一个 CacheReadCostEstimator。
func (s *Store) RunUsageStatsReconciliation(ctx context.Context, now time.Time, window time.Duration, estimator statsagg.CacheReadCostEstimator, logger *slog.Logger) (UsageStatsReconciliationResult, error) {
	result, err := s.ReconcileUsageStatsHourly(ctx, UsageStatsReconciliationOptions{Now: now, Window: window, CacheReadCostEstimator: estimator})
	if err != nil {
		return result, err
	}
	if logger == nil {
		return result, nil
	}
	if result.Skipped {
		logger.Info("usage_stats_reconciliation_skipped",
			"event", "usage_stats_reconciliation_skipped",
			"reason", result.SkipReason)
		return result, nil
	}
	if result.IssueCount > 0 {
		logger.Warn("usage_stats_reconciliation_mismatch",
			"event", "usage_stats_reconciliation_mismatch",
			"windowStartHour", result.WindowStartHour,
			"windowEndHour", result.WindowEndHour,
			"checkedHours", result.CheckedHours,
			"checkedBuckets", result.CheckedBuckets,
			"checkedRecords", result.CheckedRecords,
			"issueCount", result.IssueCount,
			"issues", result.Issues,
		)
	}
	return result, nil
}

// usageStatsAggregationCursor 读取用量聚合的全局游标（stats_job_state
// global/”/usage_stats_aggregation）。缺行 / NULL / 空串返回 ok=false。
func (s *Store) usageStatsAggregationCursor(ctx context.Context) (time.Time, bool, error) {
	query := `SELECT cursor_created_at FROM ` + s.statsTable("stats_job_state") +
		` WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_stats_aggregation'`
	var raw sql.NullString
	err := s.db.QueryRowContext(ctx, query).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("读取用量聚合游标失败: %w", err)
	}
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return time.Time{}, false, nil
	}
	parsed, ok := statsagg.ParseRFC3339Instant(raw.String)
	if !ok {
		return time.Time{}, false, fmt.Errorf("用量聚合游标不是合法的 RFC3339 时间: %q", raw.String)
	}
	return parsed.UTC(), true, nil
}

// sumUsageStatsHourlySystemScope 读取 [startHourKey, endHourKey) 的
// system_account 作用域小时行；键与聚合器 UsageStatsEntries 一致
// （caller 账户 system_account_id == scope_id，global 为 global/global）。
func (s *Store) sumUsageStatsHourlySystemScope(ctx context.Context, startHourKey, endHourKey string) (map[statsagg.SystemHourlyTotalsKey]statsagg.UsageStatsAccumulator, error) {
	query := fmt.Sprintf(`
		SELECT system_account_id, scope_id, to_char(stat_hour, 'YYYY-MM-DD"T"HH24') AS stat_hour,
		  request_count, success_count, error_count, input_tokens, output_tokens,
		  cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens,
		  cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens,
		  total_cost_usd, success_cost_usd
		FROM %s
		WHERE scope_type = 'system_account'
		  AND stat_hour >= %s
		  AND stat_hour < %s
	`, s.statsTable("usage_stats_hourly"), s.placeholder(1), s.placeholder(2))
	rows, err := s.db.QueryContext(ctx, query,
		statsagg.CalendarDBValue("stat_hour", startHourKey), statsagg.CalendarDBValue("stat_hour", endHourKey))
	if err != nil {
		return nil, fmt.Errorf("读取 usage_stats_hourly 对账样本失败: %w", err)
	}
	defer rows.Close()
	buckets := map[statsagg.SystemHourlyTotalsKey]statsagg.UsageStatsAccumulator{}
	for rows.Next() {
		var key statsagg.SystemHourlyTotalsKey
		var cells [len(usageStatsReconciliationMetrics)]any
		scan := []any{&key.SystemAccountID, &key.ScopeID, &key.StatHour}
		for index := range cells {
			scan = append(scan, &cells[index])
		}
		if err := rows.Scan(scan...); err != nil {
			return nil, fmt.Errorf("解码 usage_stats_hourly 对账样本失败: %w", err)
		}
		var accumulator statsagg.UsageStatsAccumulator
		var values [usageStatsReconciliationMetricsCount]float64
		for index, cell := range cells {
			value, err := sqlFloat(cell)
			if err != nil {
				return nil, err
			}
			values[index] = value
		}
		accumulator.RequestCount = values[0]
		accumulator.SuccessCount = values[1]
		accumulator.ErrorCount = values[2]
		accumulator.InputTokens = values[3]
		accumulator.OutputTokens = values[4]
		accumulator.CacheReadTokens = values[5]
		accumulator.CacheReadCostUsd = values[6]
		accumulator.CacheWriteTokens = values[7]
		accumulator.CacheWrite1hTokens = values[8]
		accumulator.CacheWriteCostUsd = values[9]
		accumulator.ThinkingTokens = values[10]
		accumulator.InputImageTokens = values[11]
		accumulator.OutputImageTokens = values[12]
		accumulator.TotalCostUsd = values[13]
		accumulator.SuccessCostUsd = values[14]
		buckets[key] = accumulator
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 usage_stats_hourly 对账样本失败: %w", err)
	}
	return buckets, nil
}

// truncateToHour 返回 t 在统计时区的整小时起点（本地墙钟构造，与
// usage-stats-helpers hourKey 的日历语义一致）。
func truncateToHour(t time.Time, location *time.Location) time.Time {
	local := t.In(location)
	return time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, location)
}
