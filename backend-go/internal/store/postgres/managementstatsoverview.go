package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"juhe-ai/backend-go/internal/store/port"
)

const managementStatsOverviewSummarySQL = `
SELECT request_count, success_count, error_count,
  input_tokens, output_tokens, cache_read_tokens,
  CAST(cache_read_cost_usd AS double precision), cache_write_tokens,
  cache_write_1h_tokens, CAST(cache_write_cost_usd AS double precision),
  thinking_tokens, input_image_tokens, output_image_tokens,
  CAST(total_cost_usd AS double precision), duration_ms_sum, duration_ms_count,
  first_token_ms_sum, first_token_ms_count, last_used_at
FROM juhe_stats.usage_overview_summary_windows
WHERE system_account_id = $1 AND window_key = $2 AND start_date = $3 AND end_date = $4
ORDER BY updated_at DESC, system_account_id ASC
LIMIT 1`

const managementStatsOverviewTrendSQL = `
SELECT bucket_key, request_count, error_count, input_tokens, output_tokens,
  cache_read_tokens, cache_write_tokens, cache_write_1h_tokens,
  CAST(cache_write_cost_usd AS double precision), thinking_tokens,
  input_image_tokens, output_image_tokens, CAST(total_cost_usd AS double precision),
  duration_ms_sum, duration_ms_count
FROM juhe_stats.usage_overview_trend_windows
WHERE system_account_id = $1 AND window_key = $2 AND start_date = $3 AND end_date = $4
ORDER BY bucket_key ASC
LIMIT 744`

const managementStatsOverviewModelsSQL = `
SELECT provider_code, model, request_count, input_tokens, output_tokens,
  cache_read_tokens, cache_write_tokens, cache_write_1h_tokens,
  CAST(cache_write_cost_usd AS double precision), thinking_tokens,
  input_image_tokens, output_image_tokens, CAST(total_cost_usd AS double precision)
FROM juhe_stats.usage_model_rank_windows
WHERE system_account_id = $1 AND window_key = $2 AND start_date = $3 AND end_date = $4
ORDER BY rank ASC, provider_code ASC, model ASC
LIMIT 10`

const managementStatsOverviewErrorsSQL = `
SELECT provider_code, error_code, status_code, error_message, error_count
FROM juhe_stats.usage_error_rank_windows
WHERE system_account_id = $1 AND window_key = $2 AND start_date = $3 AND end_date = $4
ORDER BY rank ASC, provider_code ASC, error_code ASC, status_code ASC
LIMIT 10`

type managementStatsOverviewQueries interface {
	summaryRow(context.Context, port.ManagementStatsOverviewReadInput) (port.ManagementStatsOverviewSummaryRow, bool, error)
	trendRows(context.Context, port.ManagementStatsOverviewReadInput) ([]port.ManagementStatsOverviewTrendRow, error)
	modelRows(context.Context, port.ManagementStatsOverviewReadInput) ([]port.ManagementStatsOverviewModelRow, error)
	errorRows(context.Context, port.ManagementStatsOverviewReadInput) ([]port.ManagementStatsOverviewErrorRow, error)
}

type managementStatsOverviewPGQueries struct {
	pool *pgxpool.Pool
}

func (s *Store) ReadManagementStatsOverview(ctx context.Context, input port.ManagementStatsOverviewReadInput) (port.ManagementStatsOverviewWindow, error) {
	return readManagementStatsOverview(ctx, &managementStatsOverviewPGQueries{pool: s.pool}, input)
}

func readManagementStatsOverview(ctx context.Context, queries managementStatsOverviewQueries, input port.ManagementStatsOverviewReadInput) (port.ManagementStatsOverviewWindow, error) {
	result := port.ManagementStatsOverviewWindow{
		HourlyTrend:       []port.ManagementStatsOverviewTrendRow{},
		ModelDistribution: []port.ManagementStatsOverviewModelRow{},
		Errors:            []port.ManagementStatsOverviewErrorRow{},
	}
	summary, found, err := queries.summaryRow(ctx, input)
	if err != nil {
		return port.ManagementStatsOverviewWindow{}, fmt.Errorf("summary overview read: %w", err)
	}
	if found {
		result.Summary = &summary
	}
	result.HourlyTrend, err = queries.trendRows(ctx, input)
	if err != nil {
		return port.ManagementStatsOverviewWindow{}, fmt.Errorf("trend overview read: %w", err)
	}
	result.ModelDistribution, err = queries.modelRows(ctx, input)
	if err != nil {
		return port.ManagementStatsOverviewWindow{}, fmt.Errorf("models overview read: %w", err)
	}
	result.Errors, err = queries.errorRows(ctx, input)
	if err != nil {
		return port.ManagementStatsOverviewWindow{}, fmt.Errorf("errors overview read: %w", err)
	}
	if result.HourlyTrend == nil {
		result.HourlyTrend = []port.ManagementStatsOverviewTrendRow{}
	}
	if result.ModelDistribution == nil {
		result.ModelDistribution = []port.ManagementStatsOverviewModelRow{}
	}
	if result.Errors == nil {
		result.Errors = []port.ManagementStatsOverviewErrorRow{}
	}
	return result, nil
}

func (q *managementStatsOverviewPGQueries) summaryRow(ctx context.Context, input port.ManagementStatsOverviewReadInput) (port.ManagementStatsOverviewSummaryRow, bool, error) {
	var row port.ManagementStatsOverviewSummaryRow
	var lastUsedAt pgtype.Text
	err := q.pool.QueryRow(ctx, managementStatsOverviewSummarySQL, input.SystemAccountID, input.WindowKey, input.StartDate, input.EndDate).Scan(
		&row.RequestCount, &row.SuccessCount, &row.ErrorCount, &row.InputTokens, &row.OutputTokens,
		&row.CacheReadTokens, &row.CacheReadCost, &row.CacheWriteTokens, &row.CacheWrite1hTokens,
		&row.CacheWriteCost, &row.ThinkingTokens, &row.InputImageTokens, &row.OutputImageTokens,
		&row.TotalCost, &row.DurationMsSum, &row.DurationMsCount, &row.FirstTokenMsSum,
		&row.FirstTokenMsCount, &lastUsedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementStatsOverviewSummaryRow{}, false, nil
	}
	if err != nil {
		return port.ManagementStatsOverviewSummaryRow{}, false, err
	}
	if lastUsedAt.Valid {
		value := lastUsedAt.String
		row.LastUsedAt = &value
	}
	return row, true, nil
}

func (q *managementStatsOverviewPGQueries) trendRows(ctx context.Context, input port.ManagementStatsOverviewReadInput) ([]port.ManagementStatsOverviewTrendRow, error) {
	rows, err := q.pool.Query(ctx, managementStatsOverviewTrendSQL, input.SystemAccountID, input.WindowKey, input.StartDate, input.EndDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []port.ManagementStatsOverviewTrendRow{}
	for rows.Next() {
		var row port.ManagementStatsOverviewTrendRow
		if err := rows.Scan(&row.StatHour, &row.RequestCount, &row.ErrorCount, &row.InputTokens, &row.OutputTokens,
			&row.CacheReadTokens, &row.CacheWriteTokens, &row.CacheWrite1hTokens, &row.CacheWriteCost,
			&row.ThinkingTokens, &row.InputImageTokens, &row.OutputImageTokens, &row.TotalCost,
			&row.DurationMsSum, &row.DurationMsCount); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (q *managementStatsOverviewPGQueries) modelRows(ctx context.Context, input port.ManagementStatsOverviewReadInput) ([]port.ManagementStatsOverviewModelRow, error) {
	rows, err := q.pool.Query(ctx, managementStatsOverviewModelsSQL, input.SystemAccountID, input.WindowKey, input.StartDate, input.EndDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []port.ManagementStatsOverviewModelRow{}
	for rows.Next() {
		var row port.ManagementStatsOverviewModelRow
		if err := rows.Scan(&row.ProviderCode, &row.Model, &row.RequestCount, &row.InputTokens, &row.OutputTokens,
			&row.CacheReadTokens, &row.CacheWriteTokens, &row.CacheWrite1hTokens, &row.CacheWriteCost,
			&row.ThinkingTokens, &row.InputImageTokens, &row.OutputImageTokens, &row.TotalCost); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (q *managementStatsOverviewPGQueries) errorRows(ctx context.Context, input port.ManagementStatsOverviewReadInput) ([]port.ManagementStatsOverviewErrorRow, error) {
	rows, err := q.pool.Query(ctx, managementStatsOverviewErrorsSQL, input.SystemAccountID, input.WindowKey, input.StartDate, input.EndDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []port.ManagementStatsOverviewErrorRow{}
	for rows.Next() {
		var row port.ManagementStatsOverviewErrorRow
		var errorMessage pgtype.Text
		if err := rows.Scan(&row.ProviderCode, &row.ErrorCode, &row.StatusCode, &errorMessage, &row.ErrorCount); err != nil {
			return nil, err
		}
		if errorMessage.Valid {
			value := errorMessage.String
			row.ErrorMessage = &value
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

var _ port.ManagementStatsOverviewReader = (*Store)(nil)
