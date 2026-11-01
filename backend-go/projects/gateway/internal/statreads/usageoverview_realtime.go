package statreads

// T93-5（R7-C C1-C2 裁决「修」）：usage-overview 窗口行缺失时的实时回退。
//
// summary / hourly-trend / model-distribution / errors 四个窗口分节正常读
// jobs 每 5 分钟刷新的 usage_overview_* 窗口；窗口行缺失（刷新滞后或新 scope
// 尚未进入窗口）时不再静默返回全 0，而是在请求链路按同一 startDate/endDate
// 实时聚合对应统计事实表。只读事实表、不写任何表、不读 usage_records；区间由
// normalizeRange 保持 ≤31 天。
//
// 聚合口径逐项对齐 statsagg 的窗口生成
// （projects/jobs/internal/statsagg/stages_window_snapshots.go）：
//   - 日期/小时桶键已由聚合侧按 usageStatsTimezone 写成统计时区本地键，回退
//     直接按这些键过滤，不做二次时区换算；
//   - summary 汇总 usage_stats_daily 的 system_account scope 行（同
//     loadDailyWindowRows 的 scope_type/scope_id 条件）；
//   - trend 汇总 usage_stats_hourly 的同一 scope 行，并按窗口天数映射回
//     1h / 6h / 24h 趋势桶（同 TrendBucketHours / TrendBucketKey）；
//   - totalCost 取 total_cost_usd（全部尝试总成本，不是 success_cost_usd）；
//   - Token 为 input_tokens + output_tokens；
//   - 错误 Top 按 error_group + provider_code + error_code 聚合，取
//     max(status_code) / max(error_message) / sum(error_count)，排序与
//     aggregateUsageErrorRows 一致。

import (
	"net/http"
	"sort"
	"strconv"
)

// 响应只读来源标记：窗口快照命中为 window，事实表实时聚合为 realtime。
const (
	usageOverviewDataSourceWindow   = "window"
	usageOverviewDataSourceRealtime = "realtime"
)

// usageOverviewSummaryRealtime 实时聚合 usage_stats_daily 的 system_account
// scope 行，生成与 usage_overview_summary_windows 行同形的摘要。
func (d *Deps) usageOverviewSummaryRealtime(r *http.Request, rng Range, systemAccountID, scopeID string) (usageSummaryOverview, error) {
	rows, err := d.queryStats(r, `
		SELECT
			COALESCE(SUM(request_count), 0) AS request_count,
			COALESCE(SUM(success_count), 0) AS success_count,
			COALESCE(SUM(error_count), 0) AS error_count,
			COALESCE(SUM(input_tokens), 0) AS input_tokens,
			COALESCE(SUM(output_tokens), 0) AS output_tokens,
			COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
			COALESCE(SUM(total_cost_usd), 0) AS total_cost,
			COALESCE(SUM(duration_ms_sum), 0) AS duration_ms_sum,
			COALESCE(SUM(duration_ms_count), 0) AS duration_ms_count,
			COALESCE(SUM(first_token_ms_sum), 0) AS first_token_ms_sum,
			COALESCE(SUM(first_token_ms_count), 0) AS first_token_ms_count
		FROM `+d.statsTable("usage_stats_daily")+`
		WHERE system_account_id = ?
			AND scope_type = 'system_account'
			AND scope_id = ?
			AND stat_date >= ?
			AND stat_date <= ?
	`, systemAccountID, scopeID, rng.StartDate, rng.EndDate)
	if err != nil {
		return usageSummaryOverview{}, err
	}
	if len(rows) == 0 {
		return emptyUsageOverviewSummary(), nil
	}
	return mapUsageOverviewSummary(rows[0]), nil
}

// usageOverviewTrendBucket 是实时趋势回退的桶累加器：字段集与
// usage_overview_trend_windows 中被小时趋势 DTO 消费的列一致。
type usageOverviewTrendBucket struct {
	requestCount    float64
	errorCount      float64
	durationMsSum   float64
	durationMsCount float64
}

// usageOverviewHourlyTrendRealtime 实时聚合 usage_stats_hourly 的
// system_account scope 行，并按 statsagg 的窗口分桶规则合并成趋势点。
func (d *Deps) usageOverviewHourlyTrendRealtime(r *http.Request, rng Range, systemAccountID, scopeID string) ([]usageHourlyPoint, error) {
	rows, err := d.queryStats(r, `
		SELECT
			stat_hour,
			COALESCE(SUM(request_count), 0) AS request_count,
			COALESCE(SUM(error_count), 0) AS error_count,
			COALESCE(SUM(duration_ms_sum), 0) AS duration_ms_sum,
			COALESCE(SUM(duration_ms_count), 0) AS duration_ms_count
		FROM `+d.statsTable("usage_stats_hourly")+`
		WHERE system_account_id = ?
			AND scope_type = 'system_account'
			AND scope_id = ?
			AND stat_hour >= ?
			AND stat_hour <= ?
		GROUP BY stat_hour
		ORDER BY stat_hour ASC
	`, systemAccountID, scopeID, rng.StartDate+"T00", rng.EndDate+"T23")
	if err != nil {
		return nil, err
	}
	buckets := map[string]*usageOverviewTrendBucket{}
	for _, row := range rows {
		key := usageOverviewTrendBucketKey(row.text("stat_hour"), rng.Days)
		bucket, ok := buckets[key]
		if !ok {
			bucket = &usageOverviewTrendBucket{}
			buckets[key] = bucket
		}
		bucket.requestCount += row.number("request_count")
		bucket.errorCount += row.number("error_count")
		bucket.durationMsSum += row.number("duration_ms_sum")
		bucket.durationMsCount += row.number("duration_ms_count")
	}
	keys := make([]string, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hourly := make([]usageHourlyPoint, 0, len(keys))
	for _, key := range keys {
		bucket := buckets[key]
		hourly = append(hourly, usageHourlyPoint{
			StatHour:          key,
			RequestCount:      bucket.requestCount,
			AverageDurationMs: averageFromSum(bucket.durationMsSum, bucket.durationMsCount),
			ErrorCount:        bucket.errorCount,
		})
	}
	return hourly, nil
}

// usageOverviewTrendBucketKey 镜像 statsagg.TrendBucketKey：≤1 天按小时、
// ≤3 天按 6 小时、更长的区间按自然日聚合。
func usageOverviewTrendBucketKey(statHour string, days int) string {
	bucketHours := 1
	if days > 3 {
		bucketHours = 24
	} else if days > 1 {
		bucketHours = 6
	}
	if bucketHours >= 24 {
		if len(statHour) >= 10 {
			return statHour[:10]
		}
		return statHour
	}
	if bucketHours <= 1 {
		return statHour
	}
	if len(statHour) < 13 {
		return statHour
	}
	hour, err := strconv.Atoi(statHour[11:13])
	if err != nil {
		return statHour
	}
	return statHour[:11] + pad2(hour/bucketHours*bucketHours)
}

// usageOverviewModelDistributionRealtime 实时聚合 usage_model_daily，按
// aggregateUsageModelRows 的口径取 Top 10（请求数降序、provider/model 升序）。
func (d *Deps) usageOverviewModelDistributionRealtime(r *http.Request, rng Range, systemAccountID string) ([]usageModelPoint, error) {
	rows, err := d.queryStats(r, `
		SELECT
			CASE WHEN provider_code = '' THEN 'unknown' ELSE provider_code END AS provider_code,
			CASE WHEN model = '' THEN 'unknown' ELSE model END AS model,
			COALESCE(SUM(request_count), 0) AS request_count,
			COALESCE(SUM(input_tokens), 0) AS input_tokens,
			COALESCE(SUM(output_tokens), 0) AS output_tokens,
			COALESCE(SUM(total_cost_usd), 0) AS total_cost
		FROM `+d.statsTable("usage_model_daily")+`
		WHERE system_account_id = ?
			AND stat_date >= ?
			AND stat_date <= ?
		GROUP BY CASE WHEN provider_code = '' THEN 'unknown' ELSE provider_code END,
			CASE WHEN model = '' THEN 'unknown' ELSE model END
		ORDER BY SUM(request_count) DESC, provider_code ASC, model ASC
		LIMIT 10
	`, systemAccountID, rng.StartDate, rng.EndDate)
	if err != nil {
		return nil, err
	}
	distribution := make([]usageModelPoint, 0, len(rows))
	for _, row := range rows {
		distribution = append(distribution, usageModelPoint{
			ProviderCode: row.text("provider_code"),
			Model:        row.text("model"),
			RequestCount: row.number("request_count"),
			TotalTokens:  row.number("input_tokens") + row.number("output_tokens"),
			TotalCost:    row.number("total_cost"),
		})
	}
	return distribution, nil
}

// usageOverviewErrorsRealtime 实时聚合 usage_error_daily，按
// aggregateUsageErrorRows 的口径取 Top 10（错误数降序、provider/error/group 升序）。
func (d *Deps) usageOverviewErrorsRealtime(r *http.Request, rng Range, systemAccountID string) ([]usageErrorPoint, error) {
	rows, err := d.queryStats(r, `
		SELECT
			CASE WHEN provider_code = '' THEN 'unknown' ELSE provider_code END AS provider_code,
			CASE WHEN error_code = '' THEN 'unknown' ELSE error_code END AS error_code,
			MAX(status_code) AS status_code,
			MAX(NULLIF(error_message, '')) AS error_message,
			COALESCE(SUM(error_count), 0) AS error_count
		FROM `+d.statsTable("usage_error_daily")+`
		WHERE system_account_id = ?
			AND stat_date >= ?
			AND stat_date <= ?
		GROUP BY CASE WHEN error_group = '' THEN 'unknown' ELSE error_group END,
			CASE WHEN provider_code = '' THEN 'unknown' ELSE provider_code END,
			CASE WHEN error_code = '' THEN 'unknown' ELSE error_code END
		ORDER BY SUM(error_count) DESC, provider_code ASC, error_code ASC,
			CASE WHEN error_group = '' THEN 'unknown' ELSE error_group END ASC
		LIMIT 10
	`, systemAccountID, rng.StartDate, rng.EndDate)
	if err != nil {
		return nil, err
	}
	errorsList := make([]usageErrorPoint, 0, len(rows))
	for _, row := range rows {
		errorsList = append(errorsList, usageErrorPoint{
			ProviderCode: row.text("provider_code"),
			ErrorCode:    row.nullText("error_code"),
			StatusCode:   row.nullNumber("status_code"),
			ErrorMessage: row.nullText("error_message"),
			ErrorCount:   row.number("error_count"),
		})
	}
	return errorsList, nil
}
