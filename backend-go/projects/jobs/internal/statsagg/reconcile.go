package statsagg

// T93-6 / C2-H1 对账支撑：从 usage_records 重算最近窗口的 system_account 作用域
// 小时桶，供 statsverify 与 usage_stats_hourly 对比。
//
// 重算必须与聚合写侧逐行同口径，否则对账会把口径差异当成缺口不断报警：
//   - 逐行准入与聚合一致：先按 usageStatsRowSkipReason 逐行隔离（坏行不进任何
//     统计桶，聚合侧同样跳过），再按 ShouldAggregateUsageStatsRecord 过滤；
//   - cache_read_cost_usd 缺失时走同一个 CacheReadCostEstimator（组合根注入
//     的 estimator 必须与聚合器一致）；
//   - 累加器复用 UsageStatsAccumulatorFromRecord / MergeAccumulator。
//
// 只重算 system_account 作用域（caller 账户 + global 两份），该作用域不依赖
// 授权链查找（AuthorizationLookup），是 usage_records 与统计表之间最直接、无
// 二级依赖的可比面。窗口必须按整小时对齐（statsverify 负责），这样统计表的
// 小时行是完整小时的累计，与按 created_at 范围重算的结果可逐项比较。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// UsageStatsReconciliationBatchSize 是重算的 keyset 分页大小（有界内存；
// 1 小时窗口常规远小于该值）。
const UsageStatsReconciliationBatchSize = 2000

// SystemHourlyTotalsKey 标识一个重算出的 system_account 小时桶。
type SystemHourlyTotalsKey struct {
	SystemAccountID string
	ScopeID         string
	StatHour        string
}

// RecomputeSystemAccountHourlyTotals 读取 created_at ∈ [startCreatedAt,
// endCreatedAt) 的 usage_records，并按聚合写侧同一口径重算 system_account
// 小时桶。返回 (桶 → 累加器, 参与聚合的行数, 错误)。start/end 必须是规范
// RFC3339 时间串且按统计时区整小时对齐（调用方负责），否则结果不可与
// usage_stats_hourly 的长时段行直接比较。
func RecomputeSystemAccountHourlyTotals(ctx context.Context, db *sql.DB, estimator CacheReadCostEstimator, startCreatedAt, endCreatedAt string, location *time.Location, batchSize int) (map[SystemHourlyTotalsKey]UsageStatsAccumulator, int, error) {
	if db == nil {
		return nil, 0, errors.New("statsagg 重算缺少数据库句柄")
	}
	if location == nil {
		return nil, 0, errors.New("statsagg 重算缺少统计时区")
	}
	if _, ok := CanonicalizeRFC3339Instant(startCreatedAt); !ok {
		return nil, 0, errors.New("statsagg 重算起始时间必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	if _, ok := CanonicalizeRFC3339Instant(endCreatedAt); !ok {
		return nil, 0, errors.New("statsagg 重算结束时间必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	if startCreatedAt >= endCreatedAt {
		return nil, 0, errors.New("statsagg 重算窗口必须非空")
	}
	if batchSize < 1 {
		batchSize = UsageStatsReconciliationBatchSize
	}
	dialect := Dialect{}
	totals := map[SystemHourlyTotalsKey]UsageStatsAccumulator{}
	admitted := 0
	cursorCreatedAt, cursorID := "", ""
	for {
		query := `
			SELECT ` + usageStatsRecordSelectColumns + `
			FROM ` + dialect.UsageRecordsTable() + `
			WHERE created_at >= ? AND created_at < ?`
		args := []any{startCreatedAt, endCreatedAt}
		if cursorCreatedAt != "" {
			query += ` AND (created_at > ? OR (created_at = ? AND id > ?))`
			args = append(args, cursorCreatedAt, cursorCreatedAt, cursorID)
		}
		query += ` ORDER BY created_at ASC, id ASC LIMIT ?`
		args = append(args, batchSize)
		rows, err := db.QueryContext(ctx, dialect.bind(query), args...)
		if err != nil {
			return nil, 0, fmt.Errorf("读取 usage_records 重算样本失败: %w", err)
		}
		records, err := scanUsageStatsRecordRows(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("解码 usage_records 重算样本失败: %w", err)
		}
		for _, record := range records {
			row := record
			if usageStatsRowSkipReason(row) != "" {
				continue
			}
			if !ShouldAggregateUsageStatsRecord(row) {
				continue
			}
			applyEstimatedCacheReadCost(&row, estimator)
			timeKeys, err := UsageStatsTimeKeysFor(row.CreatedAt, location)
			if err != nil {
				return nil, 0, err
			}
			accumulator := UsageStatsAccumulatorFromRecord(row)
			for _, key := range []SystemHourlyTotalsKey{
				{SystemAccountID: row.SystemAccountID, ScopeID: row.SystemAccountID, StatHour: timeKeys.StatHour},
				{SystemAccountID: GlobalStatsSystemAccountID, ScopeID: GlobalStatsScopeID, StatHour: timeKeys.StatHour},
			} {
				merged := totals[key]
				if err := MergeAccumulator(&merged, accumulator); err != nil {
					return nil, 0, err
				}
				totals[key] = merged
			}
			admitted++
		}
		if len(records) < batchSize {
			break
		}
		last := records[len(records)-1]
		cursorCreatedAt, cursorID = last.CreatedAt, last.ID
	}
	return totals, admitted, nil
}

// applyEstimatedCacheReadCost 是 Aggregator.applyPostgresEstimatedCacheReadCost
// 的 estimator 参数化形态（重算不走聚合器实例，避免装配第二个聚合器）。
func applyEstimatedCacheReadCost(row *UsageStatsRecordRow, estimator CacheReadCostEstimator) {
	if row.CacheReadCostUsd != nil || estimator == nil {
		return
	}
	providerCode := ""
	if row.ProviderCode != nil {
		providerCode = *row.ProviderCode
	}
	model := ""
	if row.Model != nil {
		model = *row.Model
	}
	cost, ok := estimator(providerCode, model, orZero(row.CacheReadTokens))
	if !ok || cost <= 0 {
		return
	}
	value := cost
	row.CacheReadCostUsd = &value
}
