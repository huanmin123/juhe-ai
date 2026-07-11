package postgres

import (
	"context"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const (
	maxManagementAPIKeyListRowLimit = 201
	maxManagementAPIKeyListBatch    = 200
)

type managementAPIKeyListQueries interface {
	ListManagementAPIKeys(ctx context.Context, arg postgresqueries.ListManagementAPIKeysParams) ([]postgresqueries.ListManagementAPIKeysRow, error)
	ListManagementAPIKeyUsageTotals(ctx context.Context, apiKeyIDs []string) ([]postgresqueries.ListManagementAPIKeyUsageTotalsRow, error)
}

func (s *Store) ListManagementAPIKeys(
	ctx context.Context,
	input port.ManagementAPIKeyListInput,
) (port.ManagementAPIKeyListPage, error) {
	return listManagementAPIKeys(ctx, s.queries(), input)
}

func (s *Store) ListManagementAPIKeyUsageTotals(
	ctx context.Context,
	apiKeyIDs []string,
) ([]port.ManagementAPIKeyUsageRow, error) {
	return listManagementAPIKeyUsageTotals(ctx, s.queries(), apiKeyIDs)
}

func listManagementAPIKeys(
	ctx context.Context,
	q managementAPIKeyListQueries,
	input port.ManagementAPIKeyListInput,
) (port.ManagementAPIKeyListPage, error) {
	if input.Limit <= 0 {
		return port.ManagementAPIKeyListPage{Rows: []port.ManagementAPIKeyListRow{}}, nil
	}
	limit := min(input.Limit, maxManagementAPIKeyListRowLimit)
	keyword := strings.TrimSpace(input.Keyword)
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	rows, err := q.ListManagementAPIKeys(ctx, postgresqueries.ListManagementAPIKeysParams{
		SystemAccountID: strings.TrimSpace(input.SystemAccountID),
		HasKeyword:      keyword != "",
		Keyword:         keyword,
		KeywordUpper:    keywordUpper,
		Status:          strings.TrimSpace(input.Status),
		RouteStrategyID: strings.TrimSpace(input.RouteStrategyID),
		RowLimit:        int32(limit),
		RowOffset:       int32(max(0, input.Offset)),
	})
	if err != nil {
		return port.ManagementAPIKeyListPage{}, fmt.Errorf("list management API Keys: %w", err)
	}
	pageSize := max(0, limit-1)
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}
	items := make([]port.ManagementAPIKeyListRow, 0, len(rows))
	for _, row := range rows {
		items = append(items, port.ManagementAPIKeyListRow{
			ID:                       row.ID,
			SystemAccountID:          row.SystemAccountID,
			SystemAccountName:        row.SystemAccountName,
			Name:                     row.Name,
			Description:              textPtr(row.Description),
			KeyPrefix:                row.KeyPrefix,
			KeySuffix:                row.KeySuffix,
			Status:                   row.Status,
			IsDefault:                row.IsDefault,
			RouteStrategyID:          row.RouteStrategyID,
			RouteStrategyName:        row.RouteStrategyName,
			RouteStrategyMode:        row.RouteStrategyMode,
			RouteStrategyStatus:      row.RouteStrategyStatus,
			ExpiresAt:                timestamptzPtr(row.ExpiresAt),
			QuotaLimitsJSON:          textPtr(row.QuotaLimitsJson),
			AvailabilityScheduleJSON: textPtr(row.AvailabilityScheduleJson),
		})
	}
	return port.ManagementAPIKeyListPage{Rows: items, HasMore: hasMore}, nil
}

func listManagementAPIKeyUsageTotals(
	ctx context.Context,
	q managementAPIKeyListQueries,
	apiKeyIDs []string,
) ([]port.ManagementAPIKeyUsageRow, error) {
	ids := uniqueStrings(apiKeyIDs, maxManagementAPIKeyListBatch)
	if len(ids) == 0 {
		return []port.ManagementAPIKeyUsageRow{}, nil
	}
	rows, err := q.ListManagementAPIKeyUsageTotals(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list management API Key usage totals: %w", err)
	}
	items := make([]port.ManagementAPIKeyUsageRow, 0, len(rows))
	for _, row := range rows {
		lastUsedAt, err := managementGroupUsageLastUsedAt(row.LastUsedAt)
		if err != nil {
			return nil, fmt.Errorf("map management API Key usage %q: %w", row.ScopeID, err)
		}
		items = append(items, port.ManagementAPIKeyUsageRow{
			APIKeyID: row.ScopeID,
			Usage: port.ManagementAccountUsageSummary{
				RequestCount:       row.RequestCount,
				InputTokens:        row.InputTokens,
				OutputTokens:       row.OutputTokens,
				CacheReadTokens:    row.CacheReadTokens,
				CacheReadCost:      row.CacheReadCostUsd,
				CacheWriteTokens:   row.CacheWriteTokens,
				CacheWrite1hTokens: row.CacheWrite1hTokens,
				CacheWriteCost:     row.CacheWriteCostUsd,
				ThinkingTokens:     row.ThinkingTokens,
				InputImageTokens:   row.InputImageTokens,
				OutputImageTokens:  row.OutputImageTokens,
				TotalTokens:        row.InputTokens + row.OutputTokens,
				TotalCost:          row.TotalCostUsd,
				LastUsedAt:         lastUsedAt,
			},
		})
	}
	return items, nil
}

var _ port.ManagementAPIKeyListReader = (*Store)(nil)
