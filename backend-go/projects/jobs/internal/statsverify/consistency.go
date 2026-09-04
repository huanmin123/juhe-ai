package statsverify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

// Usage statistics bucket consistency check mirroring
// checkUsageStatsConsistency / checkUsageStatsConsistencyAsync
// (storage/usage-stats.repository.ts lines 3047-3179) and the scheduler
// wrapper runUsageStatsConsistencyCheck (background-jobs.ts lines 1031-1047).
//
// Semantics, field by field:
//   - sample pool: usage_stats_daily rows with stat_date < today(stats tz),
//     newest-updated first, ordered (updated_at DESC, stat_date DESC,
//     system_account_id ASC, scope_type ASC, scope_id ASC), bounded by
//     boundedConsistencySampleLimit (1..100, default 20);
//   - hourly comparison: SUM over usage_stats_hourly rows in
//     [stat_date+"T00", nextDateKey(stat_date)+"T00");
//   - fourteen metrics are compared; the three cost metrics tolerate a
//     1e-6 absolute drift (float rounding), every other metric must match
//     exactly;
//   - mismatches become UsageStatsConsistencyIssue records; the job only
//     reports them (warn-level surface) and never rewrites either bucket —
//     the Node implementation has no repair write-back either, so "repair
//     direction" is fixed as: the daily bucket is the suspicion target, the
//     hourly SUM is the reference.
const (
	usageStatsConsistencyMetricsCount = 14
	consistencyCostTolerance          = 0.000001
	consistencySampleLimitDefault     = 20
	consistencySampleLimitMax         = 100
)

// UsageStatsConsistencyIssue mirrors UsageStatsConsistencyIssue
// (usage-stats.repository.ts lines 3066-3074).
type UsageStatsConsistencyIssue struct {
	SystemAccountID string
	ScopeType       string
	ScopeID         string
	StatDate        string
	Metric          string
	DailyValue      float64
	HourlyValue     float64
}

// UsageStatsConsistencyOptions carries the sample limit and the injected
// clock instant.
type UsageStatsConsistencyOptions struct {
	SampleLimit int
	Now         time.Time
}

// CheckUsageStatsConsistency returns the drifted buckets inside the sample
// pool; an empty result means the sampled daily buckets agree with their
// hourly sums.
func (s *Store) CheckUsageStatsConsistency(ctx context.Context, options UsageStatsConsistencyOptions) ([]UsageStatsConsistencyIssue, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("statsverify store 未初始化")
	}
	now := options.Now
	location, _, err := s.LoadUsageStatsLocation(ctx, now)
	if err != nil {
		return nil, err
	}
	todayKey := DateKeyIn(now, location)
	sampleLimit := boundConsistencySampleLimit(options.SampleLimit)

	dailyQuery := fmt.Sprintf(`
		SELECT system_account_id, scope_type, scope_id, stat_date,
		  request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
		  cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens,
		  total_cost_usd
		FROM %s
		WHERE stat_date < %s
		ORDER BY updated_at DESC, stat_date DESC, system_account_id ASC, scope_type ASC, scope_id ASC
		LIMIT %s
	`, s.statsTable("usage_stats_daily"), s.placeholder(1), s.placeholder(2))

	rows, err := s.db.QueryContext(ctx, dailyQuery, todayKey, sampleLimit)
	if err != nil {
		return nil, fmt.Errorf("读取 usage_stats_daily 一致性样本失败: %w", err)
	}
	type dailySample struct {
		systemAccountID string
		scopeType       string
		scopeID         string
		statDate        string
		values          [usageStatsConsistencyMetricsCount]float64
	}
	samples := make([]dailySample, 0, sampleLimit)
	for rows.Next() {
		var sample dailySample
		var cells [usageStatsConsistencyMetricsCount]any
		if err := rows.Scan(&sample.systemAccountID, &sample.scopeType, &sample.scopeID, &sample.statDate,
			&cells[0], &cells[1], &cells[2], &cells[3], &cells[4], &cells[5], &cells[6],
			&cells[7], &cells[8], &cells[9], &cells[10], &cells[11], &cells[12], &cells[13]); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("解码 usage_stats_daily 一致性样本失败: %w", err)
		}
		for index, cell := range cells {
			value, err := sqlFloat(cell)
			if err != nil {
				_ = rows.Close()
				return nil, err
			}
			sample.values[index] = value
		}
		samples = append(samples, sample)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("遍历 usage_stats_daily 一致性样本失败: %w", err)
	}
	_ = rows.Close()

	issues := make([]UsageStatsConsistencyIssue, 0)
	for _, sample := range samples {
		hourlyValues, err := s.sumUsageStatsHourly(ctx, sample.systemAccountID, sample.scopeType, sample.scopeID, sample.statDate)
		if err != nil {
			return nil, err
		}
		for index, metric := range usageStatsConsistencyMetricNames {
			dailyValue := sample.values[index]
			hourlyValue := hourlyValues[index]
			if math.Abs(dailyValue-hourlyValue) <= consistencyMetricTolerance(metric) {
				continue
			}
			issues = append(issues, UsageStatsConsistencyIssue{
				SystemAccountID: sample.systemAccountID,
				ScopeType:       sample.scopeType,
				ScopeID:         sample.scopeID,
				StatDate:        sample.statDate,
				Metric:          metric,
				DailyValue:      dailyValue,
				HourlyValue:     hourlyValue,
			})
		}
	}
	return issues, nil
}

// usageStatsConsistencyMetricNames mirrors usageStatsConsistencyMetrics
// (usage-stats.repository.ts lines 3047-3062), in order.
var usageStatsConsistencyMetricNames = [usageStatsConsistencyMetricsCount]string{
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
}

// consistencyMetricTolerance mirrors consistencyMetricTolerance
// (usage-stats.repository.ts lines 3726-3730): 1e-6 for cost metrics, 0 for
// everything else.
func consistencyMetricTolerance(metric string) float64 {
	switch metric {
	case "total_cost_usd", "cache_read_cost_usd", "cache_write_cost_usd":
		return consistencyCostTolerance
	default:
		return 0
	}
}

// boundConsistencySampleLimit mirrors boundedConsistencySampleLimit
// (usage-stats.repository.ts lines 3732-3735): clamp to [1,100]; Node falls
// back to 20 for non-finite input, which cannot occur for an int.
func boundConsistencySampleLimit(value int) int {
	if value < 1 {
		return consistencySampleLimitDefault
	}
	if value > consistencySampleLimitMax {
		return consistencySampleLimitMax
	}
	return value
}

// sumUsageStatsHourly mirrors the per-sample hourly SUM query including the
// `stat_hour >= stat_date+"T00" AND stat_hour < nextDateKey+"T00"` window.
func (s *Store) sumUsageStatsHourly(ctx context.Context, systemAccountID, scopeType, scopeID, statDate string) ([usageStatsConsistencyMetricsCount]float64, error) {
	var values [usageStatsConsistencyMetricsCount]float64
	query := fmt.Sprintf(`
		SELECT
		  COALESCE(SUM(request_count), 0),
		  COALESCE(SUM(success_count), 0),
		  COALESCE(SUM(error_count), 0),
		  COALESCE(SUM(input_tokens), 0),
		  COALESCE(SUM(output_tokens), 0),
		  COALESCE(SUM(cache_read_tokens), 0),
		  COALESCE(SUM(cache_read_cost_usd), 0),
		  COALESCE(SUM(cache_write_tokens), 0),
		  COALESCE(SUM(cache_write_1h_tokens), 0),
		  COALESCE(SUM(cache_write_cost_usd), 0),
		  COALESCE(SUM(thinking_tokens), 0),
		  COALESCE(SUM(input_image_tokens), 0),
		  COALESCE(SUM(output_image_tokens), 0),
		  COALESCE(SUM(total_cost_usd), 0)
		FROM %s
		WHERE system_account_id = %s
		  AND scope_type = %s
		  AND scope_id = %s
		  AND stat_hour >= %s
		  AND stat_hour < %s
	`, s.statsTable("usage_stats_hourly"),
		s.placeholder(1), s.placeholder(2), s.placeholder(3), s.placeholder(4), s.placeholder(5))
	var cells [usageStatsConsistencyMetricsCount]any
	err := s.db.QueryRowContext(ctx, query,
		systemAccountID, scopeType, scopeID,
		statDate+"T00", NextDateKey(statDate)+"T00").Scan(
		&cells[0], &cells[1], &cells[2], &cells[3], &cells[4], &cells[5], &cells[6],
		&cells[7], &cells[8], &cells[9], &cells[10], &cells[11], &cells[12], &cells[13])
	if errors.Is(err, sql.ErrNoRows) {
		return values, nil
	}
	if err != nil {
		return values, fmt.Errorf("读取 usage_stats_hourly 一致性聚合失败: %w", err)
	}
	for index, cell := range cells {
		value, err := sqlFloat(cell)
		if err != nil {
			return values, err
		}
		values[index] = value
	}
	return values, nil
}
