package statreads

import (
	"net/http"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// writeReadError maps storage failures onto the route contract: invalid
// timezone settings are a server-side configuration fault like Node's thrown
// errors (500 服务器内部错误 via the error middleware).
func (d *Deps) writeReadError(w http.ResponseWriter, err error) {
	kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
}

// ---------------------------------------------------------------------------
// Summary + trend + distribution + errors read models.
// ---------------------------------------------------------------------------

// usageOverviewSummary mirrors getUsageStatsOverviewSummary: today reads
// usage_stats_daily, historical ranges read usage_overview_summary_windows.
func (d *Deps) usageOverviewSummary(r *http.Request, scope AccessScope, rng Range) (any, error) {
	systemAccountID, scopeID := usageOverviewStatsScope(scope)
	current, err := d.isCurrentUsageStatsDay(r, rng)
	if err != nil {
		return nil, err
	}
	statsTable := d.statsTable("usage_stats_daily")
	windowTable := d.statsTable("usage_overview_summary_windows")
	var row Row
	var found bool
	if current {
		rows, queryErr := d.queryStats(r, `
			SELECT request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens,
				total_cost_usd AS total_cost, duration_ms_sum, duration_ms_count, first_token_ms_sum, first_token_ms_count
			FROM `+statsTable+`
			WHERE system_account_id = ?
				AND scope_type = 'system_account'
				AND scope_id = ?
				AND stat_date = ?
		`, systemAccountID, scopeID, rng.StartDate)
		row, found, err = firstRow(rows, queryErr)
	} else {
		rows, queryErr := d.queryStats(r, `
			SELECT request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens,
				total_cost_usd AS total_cost, duration_ms_sum, duration_ms_count, first_token_ms_sum, first_token_ms_count
			FROM `+windowTable+`
			WHERE system_account_id = ? AND window_key = ? AND start_date = ? AND end_date = ?
		`, systemAccountID, rangeWindowKey(rng), rng.StartDate, rng.EndDate)
		row, found, err = firstRow(rows, queryErr)
	}
	if err != nil || !found {
		return usageStatsOverviewResult{Range: rng, Summary: emptyUsageOverviewSummary()}, err
	}
	return usageStatsOverviewResult{Range: rng, Summary: mapUsageOverviewSummary(row)}, nil
}

// usageOverviewDailyTrend mirrors getUsageStatsOverviewDailyTrend.
func (d *Deps) usageOverviewDailyTrend(r *http.Request, scope AccessScope, rng Range) (any, error) {
	systemAccountID, scopeID := usageOverviewStatsScope(scope)
	rows, err := d.queryStats(r, `
		SELECT stat_date, input_tokens, output_tokens, total_cost_usd AS total_cost
		FROM `+d.statsTable("usage_stats_daily")+`
		WHERE system_account_id = ?
			AND scope_type = 'system_account'
			AND scope_id = ?
			AND stat_date >= ?
			AND stat_date <= ?
		ORDER BY stat_date ASC
	`, systemAccountID, scopeID, rng.StartDate, rng.EndDate)
	if err != nil {
		return nil, err
	}
	trend := mapUsageDailyTrendRows(rows, rng)
	return usageStatsDailyTrendResult{Range: rng, DailyTrend: trend}, nil
}

// usageOverviewHourlyTrend mirrors getUsageStatsOverviewHourlyTrend.
func (d *Deps) usageOverviewHourlyTrend(r *http.Request, scope AccessScope, rng Range) (any, error) {
	// The trend/model/error window tables are system-account keyed only
	// (Node queries bind systemAccountId + window_key + start/end only).
	systemAccountID, _ := usageOverviewStatsScope(scope)
	rows, err := d.queryStats(r, `
		SELECT bucket_key AS stat_hour, request_count, error_count, duration_ms_sum, duration_ms_count
		FROM `+d.statsTable("usage_overview_trend_windows")+`
		WHERE system_account_id = ? AND window_key = ? AND start_date = ? AND end_date = ?
		ORDER BY bucket_key ASC
	`, systemAccountID, rangeWindowKey(rng), rng.StartDate, rng.EndDate)
	if err != nil {
		return nil, err
	}
	hourly := make([]usageHourlyPoint, 0, len(rows))
	for _, row := range rows {
		hourly = append(hourly, usageHourlyPoint{
			StatHour:          row.text("stat_hour"),
			RequestCount:      row.number("request_count"),
			AverageDurationMs: averageFromSum(row.value("duration_ms_sum"), row.value("duration_ms_count")),
			ErrorCount:        row.number("error_count"),
		})
	}
	return usageStatsHourlyTrendResult{Range: rng, HourlyTrend: hourly}, nil
}

// usageOverviewModelDistribution mirrors getUsageStatsOverviewModelDistribution.
func (d *Deps) usageOverviewModelDistribution(r *http.Request, scope AccessScope, rng Range) (any, error) {
	systemAccountID, _ := usageOverviewStatsScope(scope)
	rows, err := d.queryStats(r, `
		SELECT provider_code, model, request_count, input_tokens, output_tokens, total_cost_usd AS total_cost
		FROM `+d.statsTable("usage_model_rank_windows")+`
		WHERE system_account_id = ? AND window_key = ? AND start_date = ? AND end_date = ?
		ORDER BY rank ASC
	`, systemAccountID, rangeWindowKey(rng), rng.StartDate, rng.EndDate)
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
	return usageStatsModelDistributionResult{Range: rng, ModelDistribution: distribution}, nil
}

// usageOverviewErrors mirrors getUsageStatsOverviewErrors.
func (d *Deps) usageOverviewErrors(r *http.Request, scope AccessScope, rng Range) (any, error) {
	systemAccountID, _ := usageOverviewStatsScope(scope)
	rows, err := d.queryStats(r, `
		SELECT provider_code, error_code, status_code, error_message, error_count
		FROM `+d.statsTable("usage_error_rank_windows")+`
		WHERE system_account_id = ? AND window_key = ? AND start_date = ? AND end_date = ?
		ORDER BY rank ASC
	`, systemAccountID, rangeWindowKey(rng), rng.StartDate, rng.EndDate)
	if err != nil {
		return nil, err
	}
	errorsList := make([]usageErrorPoint, 0, len(rows))
	for _, row := range rows {
		errorCode := row.nullText("error_code")
		errorMessage := row.nullText("error_message")
		statusCode := row.nullNumber("status_code")
		errorsList = append(errorsList, usageErrorPoint{
			ProviderCode: row.text("provider_code"),
			ErrorCode:    errorCode,
			StatusCode:   statusCode,
			ErrorMessage: errorMessage,
			ErrorCount:   row.number("error_count"),
		})
	}
	return usageStatsErrorsResult{Range: rng, Errors: errorsList}, nil
}

// isCurrentUsageStatsDay mirrors isCurrentUsageStatsDay.
func (d *Deps) isCurrentUsageStatsDay(r *http.Request, rng Range) (bool, error) {
	location, err := d.timezoneLocation(r.Context())
	if err != nil {
		return false, err
	}
	todayKey := dateKeyIn(d.Now(), location)
	return rng.StartDate == todayKey && rng.EndDate == todayKey, nil
}

func mapUsageOverviewSummary(row Row) usageSummaryOverview {
	requestCount := row.number("request_count")
	inputTokens := row.number("input_tokens")
	outputTokens := row.number("output_tokens")
	errorCount := row.number("error_count")
	return usageSummaryOverview{
		RequestCount:        int64(requestCount),
		SuccessCount:        int64(row.number("success_count")),
		ErrorCount:          int64(errorCount),
		ErrorRate:           errorRate(requestCount, errorCount),
		InputTokens:         int64(inputTokens),
		OutputTokens:        int64(outputTokens),
		CacheReadTokens:     int64(row.number("cache_read_tokens")),
		TotalTokens:         int64(inputTokens + outputTokens),
		TotalCost:           row.number("total_cost"),
		AverageDurationMs:   averageFromSum(row.value("duration_ms_sum"), row.value("duration_ms_count")),
		AverageFirstTokenMs: averageFromSum(row.value("first_token_ms_sum"), row.value("first_token_ms_count")),
	}
}

func emptyUsageOverviewSummary() usageSummaryOverview {
	return usageSummaryOverview{
		RequestCount:    0,
		SuccessCount:    0,
		ErrorCount:      0,
		ErrorRate:       0,
		InputTokens:     0,
		OutputTokens:    0,
		CacheReadTokens: 0,
		TotalTokens:     0,
		TotalCost:       0,
	}
}

// errorRate mirrors errorCount / requestCount (0 when requestCount is 0).
func errorRate(requestCount, errorCount float64) float64 {
	if requestCount > 0 {
		return errorCount / requestCount
	}
	return 0
}

func mapUsageDailyTrendRows(rows []Row, rng Range) []usageDailyPoint {
	byDate := map[string]Row{}
	for _, row := range rows {
		byDate[row.text("stat_date")] = row
	}
	keys := dateKeysInRange(rng)
	trend := make([]usageDailyPoint, 0, len(keys))
	for _, statDate := range keys {
		row, ok := byDate[statDate]
		if !ok {
			trend = append(trend, usageDailyPoint{StatDate: statDate, TotalTokens: 0, TotalCost: 0})
			continue
		}
		trend = append(trend, usageDailyPoint{
			StatDate:    statDate,
			TotalTokens: row.number("input_tokens") + row.number("output_tokens"),
			TotalCost:   row.number("total_cost"),
		})
	}
	return trend
}

// JSON payloads mirror the Node response shapes field by field.
type usageStatsOverviewResult struct {
	Range   Range                `json:"range"`
	Summary usageSummaryOverview `json:"summary"`
}

type usageStatsDailyTrendResult struct {
	Range      Range             `json:"range"`
	DailyTrend []usageDailyPoint `json:"dailyTrend"`
}

type usageStatsHourlyTrendResult struct {
	Range       Range              `json:"range"`
	HourlyTrend []usageHourlyPoint `json:"hourlyTrend"`
}

type usageStatsModelDistributionResult struct {
	Range             Range             `json:"range"`
	ModelDistribution []usageModelPoint `json:"modelDistribution"`
}

type usageStatsErrorsResult struct {
	Range  Range             `json:"range"`
	Errors []usageErrorPoint `json:"errors"`
}

type usageSummaryOverview struct {
	RequestCount        int64   `json:"requestCount"`
	SuccessCount        int64   `json:"successCount"`
	ErrorCount          int64   `json:"errorCount"`
	ErrorRate           float64 `json:"errorRate"`
	InputTokens         int64   `json:"inputTokens"`
	OutputTokens        int64   `json:"outputTokens"`
	CacheReadTokens     int64   `json:"cacheReadTokens"`
	TotalTokens         int64   `json:"totalTokens"`
	TotalCost           float64 `json:"totalCost"`
	AverageDurationMs   *int64  `json:"averageDurationMs"`
	AverageFirstTokenMs *int64  `json:"averageFirstTokenMs"`
}

type usageDailyPoint struct {
	StatDate    string  `json:"statDate"`
	TotalTokens float64 `json:"totalTokens"`
	TotalCost   float64 `json:"totalCost"`
}

type usageHourlyPoint struct {
	StatHour          string  `json:"statHour"`
	RequestCount      float64 `json:"requestCount"`
	AverageDurationMs *int64  `json:"averageDurationMs"`
	ErrorCount        float64 `json:"errorCount"`
}

type usageModelPoint struct {
	ProviderCode string  `json:"providerCode"`
	Model        string  `json:"model"`
	RequestCount float64 `json:"requestCount"`
	TotalTokens  float64 `json:"totalTokens"`
	TotalCost    float64 `json:"totalCost"`
}

type usageErrorPoint struct {
	ProviderCode string  `json:"providerCode"`
	ErrorCode    *string `json:"errorCode"`
	StatusCode   *int64  `json:"statusCode"`
	ErrorMessage *string `json:"errorMessage"`
	ErrorCount   float64 `json:"errorCount"`
}

// statsTable qualifies a stats table for the active dialect.
func (d *Deps) statsTable(tableName string) string {
	if d.PGDialect {
		return "juhe_stats." + tableName
	}
	return tableName
}

// businessTable qualifies a business table for the active dialect.
func (d *Deps) businessTable(tableName string) string {
	if d.PGDialect {
		return "juhe_business." + tableName
	}
	return tableName
}
