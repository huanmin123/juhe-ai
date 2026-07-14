package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const (
	maxManagementClientIPStatsDetailRowLimit = 101
	maxManagementClientIPStatsDetailOffset   = 999
)

type managementClientIPStatsDetailQueries interface {
	GetManagementClientIPStatsRegistry(
		ctx context.Context,
		ipHash string,
	) (postgresqueries.GetManagementClientIPStatsRegistryRow, error)
	ManagementClientIPStatsRangeReady(
		ctx context.Context,
		arg postgresqueries.ManagementClientIPStatsRangeReadyParams,
	) (bool, error)
	ListManagementClientIPAccountUsage(
		ctx context.Context,
		arg postgresqueries.ListManagementClientIPAccountUsageParams,
	) ([]postgresqueries.ListManagementClientIPAccountUsageRow, error)
	ListManagementClientIPAccountUsageRequestCountDesc(
		ctx context.Context,
		arg postgresqueries.ListManagementClientIPAccountUsageRequestCountDescParams,
	) ([]postgresqueries.ListManagementClientIPAccountUsageRequestCountDescRow, error)
}

func (s *Store) GetManagementClientIPStatsDetail(
	ctx context.Context,
	input port.ManagementClientIPStatsDetailInput,
) (port.ManagementClientIPStatsDetailPage, error) {
	return getManagementClientIPStatsDetail(ctx, s.queries(), input)
}

func getManagementClientIPStatsDetail(
	ctx context.Context,
	q managementClientIPStatsDetailQueries,
	input port.ManagementClientIPStatsDetailInput,
) (port.ManagementClientIPStatsDetailPage, error) {
	registry, err := q.GetManagementClientIPStatsRegistry(ctx, input.IPHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementClientIPStatsDetailPage{}, nil
	}
	if err != nil {
		return port.ManagementClientIPStatsDetailPage{}, fmt.Errorf(
			"get management client IP stats registry: %w",
			err,
		)
	}

	result := port.ManagementClientIPStatsDetailPage{
		Found:          true,
		IPHash:         registry.IpHash,
		AggregateIPKey: registry.AggregateIpKey,
		LastSeenAt:     registry.LastSeenAt,
		Rows:           []port.ManagementClientIPAccountUsageRow{},
	}
	ready, err := q.ManagementClientIPStatsRangeReady(
		ctx,
		postgresqueries.ManagementClientIPStatsRangeReadyParams{
			StartDate: input.StartDate,
			EndDate:   input.EndDate,
		},
	)
	if err != nil {
		return port.ManagementClientIPStatsDetailPage{}, fmt.Errorf(
			"check management client IP stats detail range readiness: %w",
			err,
		)
	}
	result.RangeReady = ready
	if !ready || input.Limit <= 0 {
		return result, nil
	}

	sortField := managementClientIPStatsSortField(input.SortField)
	sortOrder := managementClientIPStatsSortOrder(input.SortOrder)
	rowLimit := int32(min(input.Limit, maxManagementClientIPStatsDetailRowLimit))
	rowOffset := int32(min(max(0, input.Offset), maxManagementClientIPStatsDetailOffset))
	var rows []postgresqueries.ListManagementClientIPAccountUsageRow
	if sortField == port.ManagementClientIPStatsSortRequestCount &&
		sortOrder == port.ManagementClientIPStatsSortDescending {
		requestCountRows, listErr := q.ListManagementClientIPAccountUsageRequestCountDesc(
			ctx,
			postgresqueries.ListManagementClientIPAccountUsageRequestCountDescParams{
				IpHash:    registry.IpHash,
				StartDate: input.StartDate,
				EndDate:   input.EndDate,
				RowLimit:  rowLimit,
				RowOffset: rowOffset,
			},
		)
		err = listErr
		if err == nil {
			rows = make([]postgresqueries.ListManagementClientIPAccountUsageRow, len(requestCountRows))
			for index, row := range requestCountRows {
				rows[index] = postgresqueries.ListManagementClientIPAccountUsageRow(row)
			}
		}
	} else {
		rows, err = q.ListManagementClientIPAccountUsage(
			ctx,
			postgresqueries.ListManagementClientIPAccountUsageParams{
				IpHash:    registry.IpHash,
				StartDate: input.StartDate,
				EndDate:   input.EndDate,
				SortField: string(sortField),
				SortOrder: string(sortOrder),
				RowLimit:  rowLimit,
				RowOffset: rowOffset,
			},
		)
	}
	if err != nil {
		return port.ManagementClientIPStatsDetailPage{}, fmt.Errorf(
			"list management client IP account usage: %w",
			err,
		)
	}

	pageSize := max(0, int(rowLimit)-1)
	result.HasMore = len(rows) > pageSize
	if result.HasMore {
		rows = rows[:pageSize]
	}
	result.Rows = make([]port.ManagementClientIPAccountUsageRow, 0, len(rows))
	for _, row := range rows {
		result.Rows = append(result.Rows, managementClientIPAccountUsageRow(row))
	}
	return result, nil
}

func managementClientIPAccountUsageRow(
	row postgresqueries.ListManagementClientIPAccountUsageRow,
) port.ManagementClientIPAccountUsageRow {
	var errorRate float64
	if row.RequestCount > 0 {
		errorRate = float64(row.ErrorCount) / float64(row.RequestCount)
	}
	return port.ManagementClientIPAccountUsageRow{
		AccountID:                     row.AccountID,
		AccountName:                   textPtr(row.AccountName),
		AccountOwnerSystemAccountID:   textPtr(row.AccountOwnerSystemAccountID),
		AccountOwnerSystemAccountName: textPtr(row.AccountOwnerSystemAccountName),
		RangeUsage: port.ManagementClientIPUsageSummary{
			RequestCount:        row.RequestCount,
			SuccessCount:        row.SuccessCount,
			ErrorCount:          row.ErrorCount,
			ErrorRate:           errorRate,
			InputTokens:         row.InputTokens,
			OutputTokens:        row.OutputTokens,
			CacheReadTokens:     row.CacheReadTokens,
			CacheReadCost:       row.CacheReadCostUsd,
			CacheWriteTokens:    row.CacheWriteTokens,
			CacheWrite1hTokens:  row.CacheWrite1hTokens,
			CacheWriteCost:      row.CacheWriteCostUsd,
			ThinkingTokens:      row.ThinkingTokens,
			InputImageTokens:    row.InputImageTokens,
			OutputImageTokens:   row.OutputImageTokens,
			TotalTokens:         row.InputTokens + row.OutputTokens,
			TotalCost:           row.TotalCostUsd,
			ActiveDays:          row.ActiveDays,
			AverageDurationMs:   managementClientIPStatsAverage(row.AverageDurationMs, row.DurationMsSum, row.DurationMsCount),
			AverageFirstTokenMs: managementClientIPStatsAverage(row.AverageFirstTokenMs, row.FirstTokenMsSum, row.FirstTokenMsCount),
			MaxDurationMs:       managementClientIPStatsMaxDuration(row.DurationMsMax, row.DurationMsCount),
			LastUsedAt:          textPtr(row.LastUsedAt),
			LastErrorAt:         textPtr(row.LastErrorAt),
		},
	}
}

var _ port.ManagementClientIPStatsDetailReader = (*Store)(nil)
