package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const (
	maxManagementAPIKeyListRowLimit = 201
	maxManagementAPIKeyListBatch    = 200
)

type managementAPIKeyListQueries interface {
	ListManagementAPIKeys(ctx context.Context, arg postgresqueries.ListManagementAPIKeysParams) ([]postgresqueries.ListManagementAPIKeysRow, error)
	ListManagementAPIKeysByKeyword(ctx context.Context, arg postgresqueries.ListManagementAPIKeysByKeywordParams) ([]postgresqueries.ListManagementAPIKeysByKeywordRow, error)
	ListManagementAPIKeyUsageTotals(ctx context.Context, arg postgresqueries.ListManagementAPIKeyUsageTotalsParams) ([]postgresqueries.ListManagementAPIKeyUsageTotalsRow, error)
}

type managementAPIKeyListRecord struct {
	ID                       string
	SystemAccountID          string
	SystemAccountName        string
	Name                     string
	Description              pgtype.Text
	KeyPrefix                string
	KeySuffix                string
	Status                   string
	IsDefault                bool
	RouteStrategyID          string
	RouteStrategyName        string
	RouteStrategyMode        string
	RouteStrategyStatus      string
	ExpiresAt                pgtype.Timestamptz
	QuotaLimitsJSON          pgtype.Text
	AvailabilityScheduleJSON pgtype.Text
}

func (s *Store) ListManagementAPIKeys(
	ctx context.Context,
	input port.ManagementAPIKeyListInput,
) (port.ManagementAPIKeyListPage, error) {
	return listManagementAPIKeys(ctx, s.queries(), input)
}

func (s *Store) ListManagementAPIKeyUsageTotals(
	ctx context.Context,
	scopes []port.ManagementAPIKeyUsageScope,
) ([]port.ManagementAPIKeyUsageRow, error) {
	return listManagementAPIKeyUsageTotals(ctx, s.queries(), scopes)
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
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	status := strings.TrimSpace(input.Status)
	routeStrategyID := strings.TrimSpace(input.RouteStrategyID)
	rowLimit := int32(limit)
	rowOffset := int32(max(0, input.Offset))
	var records []managementAPIKeyListRecord
	if keyword == "" {
		rows, err := q.ListManagementAPIKeys(ctx, postgresqueries.ListManagementAPIKeysParams{
			SystemAccountID: systemAccountID,
			Status:          status,
			RouteStrategyID: routeStrategyID,
			RowLimit:        rowLimit,
			RowOffset:       rowOffset,
		})
		if err != nil {
			return port.ManagementAPIKeyListPage{}, fmt.Errorf("list management API Keys: %w", err)
		}
		records = managementAPIKeyListRecords(rows)
	} else {
		rows, err := q.ListManagementAPIKeysByKeyword(ctx, postgresqueries.ListManagementAPIKeysByKeywordParams{
			SystemAccountID: systemAccountID,
			Keyword:         keyword,
			KeywordUpper:    textPrefixUpperBound(keyword),
			Status:          status,
			RouteStrategyID: routeStrategyID,
			RowLimit:        rowLimit,
			RowOffset:       rowOffset,
		})
		if err != nil {
			return port.ManagementAPIKeyListPage{}, fmt.Errorf("list management API Keys by keyword: %w", err)
		}
		records = managementAPIKeyListKeywordRecords(rows)
	}
	pageSize := max(0, limit-1)
	hasMore := len(records) > pageSize
	if hasMore {
		records = records[:pageSize]
	}
	items := make([]port.ManagementAPIKeyListRow, 0, len(records))
	for _, row := range records {
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
			QuotaLimitsJSON:          textPtr(row.QuotaLimitsJSON),
			AvailabilityScheduleJSON: textPtr(row.AvailabilityScheduleJSON),
		})
	}
	return port.ManagementAPIKeyListPage{Rows: items, HasMore: hasMore}, nil
}

func managementAPIKeyListRecords(
	rows []postgresqueries.ListManagementAPIKeysRow,
) []managementAPIKeyListRecord {
	result := make([]managementAPIKeyListRecord, 0, len(rows))
	for _, row := range rows {
		result = append(result, managementAPIKeyListRecord{
			ID:                       row.ID,
			SystemAccountID:          row.SystemAccountID,
			SystemAccountName:        row.SystemAccountName,
			Name:                     row.Name,
			Description:              row.Description,
			KeyPrefix:                row.KeyPrefix,
			KeySuffix:                row.KeySuffix,
			Status:                   row.Status,
			IsDefault:                row.IsDefault,
			RouteStrategyID:          row.RouteStrategyID,
			RouteStrategyName:        row.RouteStrategyName,
			RouteStrategyMode:        row.RouteStrategyMode,
			RouteStrategyStatus:      row.RouteStrategyStatus,
			ExpiresAt:                row.ExpiresAt,
			QuotaLimitsJSON:          row.QuotaLimitsJson,
			AvailabilityScheduleJSON: row.AvailabilityScheduleJson,
		})
	}
	return result
}

func managementAPIKeyListKeywordRecords(
	rows []postgresqueries.ListManagementAPIKeysByKeywordRow,
) []managementAPIKeyListRecord {
	result := make([]managementAPIKeyListRecord, 0, len(rows))
	for _, row := range rows {
		result = append(result, managementAPIKeyListRecord{
			ID:                       row.ID,
			SystemAccountID:          row.SystemAccountID,
			SystemAccountName:        row.SystemAccountName,
			Name:                     row.Name,
			Description:              row.Description,
			KeyPrefix:                row.KeyPrefix,
			KeySuffix:                row.KeySuffix,
			Status:                   row.Status,
			IsDefault:                row.IsDefault,
			RouteStrategyID:          row.RouteStrategyID,
			RouteStrategyName:        row.RouteStrategyName,
			RouteStrategyMode:        row.RouteStrategyMode,
			RouteStrategyStatus:      row.RouteStrategyStatus,
			ExpiresAt:                row.ExpiresAt,
			QuotaLimitsJSON:          row.QuotaLimitsJson,
			AvailabilityScheduleJSON: row.AvailabilityScheduleJson,
		})
	}
	return result
}

func listManagementAPIKeyUsageTotals(
	ctx context.Context,
	q managementAPIKeyListQueries,
	scopes []port.ManagementAPIKeyUsageScope,
) ([]port.ManagementAPIKeyUsageRow, error) {
	scopes = uniqueManagementAPIKeyUsageScopes(scopes, maxManagementAPIKeyListBatch)
	if len(scopes) == 0 {
		return []port.ManagementAPIKeyUsageRow{}, nil
	}
	systemAccountIDs := make([]string, 0, len(scopes))
	apiKeyIDs := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		systemAccountIDs = append(systemAccountIDs, scope.SystemAccountID)
		apiKeyIDs = append(apiKeyIDs, scope.APIKeyID)
	}
	rows, err := q.ListManagementAPIKeyUsageTotals(ctx, postgresqueries.ListManagementAPIKeyUsageTotalsParams{
		SystemAccountIds: systemAccountIDs,
		ApiKeyIds:        apiKeyIDs,
	})
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
			SystemAccountID: row.SystemAccountID,
			APIKeyID:        row.ScopeID,
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

func uniqueManagementAPIKeyUsageScopes(
	values []port.ManagementAPIKeyUsageScope,
	limit int,
) []port.ManagementAPIKeyUsageScope {
	if limit <= 0 {
		return nil
	}
	result := make([]port.ManagementAPIKeyUsageScope, 0, min(len(values), limit))
	seen := make(map[port.ManagementAPIKeyUsageScope]struct{}, min(len(values), limit))
	for _, value := range values {
		value.SystemAccountID = strings.TrimSpace(value.SystemAccountID)
		value.APIKeyID = strings.TrimSpace(value.APIKeyID)
		if value.SystemAccountID == "" || value.APIKeyID == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) == limit {
			break
		}
	}
	return result
}

var _ port.ManagementAPIKeyListReader = (*Store)(nil)
