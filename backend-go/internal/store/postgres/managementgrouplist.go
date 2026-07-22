package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const (
	maxManagementGroupListRowLimit = 501
	maxManagementGroupListBatch    = 500
)

type managementGroupListQueries interface {
	ListManagementGroups(ctx context.Context, arg postgresqueries.ListManagementGroupsParams) ([]postgresqueries.ListManagementGroupsRow, error)
	ListManagementGroupStatusSnapshotRows(ctx context.Context, arg postgresqueries.ListManagementGroupStatusSnapshotRowsParams) ([]postgresqueries.ListManagementGroupStatusSnapshotRowsRow, error)
	ListManagementGroupConcurrencyAccountIDs(ctx context.Context, groupIDs []string) ([]postgresqueries.ListManagementGroupConcurrencyAccountIDsRow, error)
	ListManagementGroupAccountStats(ctx context.Context, groupIDs []string) ([]postgresqueries.ListManagementGroupAccountStatsRow, error)
	ListManagementGroupUsageTotals(ctx context.Context, arg postgresqueries.ListManagementGroupUsageTotalsParams) ([]postgresqueries.ListManagementGroupUsageTotalsRow, error)
	ListManagementGroupUsageDaily(ctx context.Context, arg postgresqueries.ListManagementGroupUsageDailyParams) ([]postgresqueries.ListManagementGroupUsageDailyRow, error)
	ListManagementGroupAuthorizationSources(ctx context.Context, authorizationIDs []string) ([]postgresqueries.ListManagementGroupAuthorizationSourcesRow, error)
}

func (s *Store) ListManagementGroups(ctx context.Context, input port.ManagementGroupListInput) (port.ManagementGroupListPage, error) {
	return listManagementGroups(ctx, s.queries(), input)
}

func (s *Store) ListManagementGroupStatusSnapshotRows(ctx context.Context, input port.ManagementGroupStatusSnapshotInput) ([]port.ManagementGroupStatusSnapshotRow, error) {
	return listManagementGroupStatusSnapshotRows(ctx, s.queries(), input)
}

func (s *Store) ListManagementGroupConcurrencyAccountIDs(ctx context.Context, groupIDs []string) ([]port.ManagementGroupConcurrencyAccountIDRow, error) {
	ids := uniqueStrings(groupIDs, maxManagementGroupListBatch)
	if len(ids) == 0 {
		return []port.ManagementGroupConcurrencyAccountIDRow{}, nil
	}
	rows, err := s.queries().ListManagementGroupConcurrencyAccountIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list management group concurrency account ids: %w", err)
	}
	items := make([]port.ManagementGroupConcurrencyAccountIDRow, 0, len(rows))
	for _, row := range rows {
		items = append(items, port.ManagementGroupConcurrencyAccountIDRow{GroupID: row.GroupID, AccountID: row.AccountID})
	}
	return items, nil
}

func (s *Store) ListManagementGroupAccountStats(ctx context.Context, groupIDs []string) ([]port.ManagementGroupAccountStatsRow, error) {
	return listManagementGroupAccountStats(ctx, s.queries(), groupIDs)
}

func (s *Store) ListManagementGroupUsageTotals(ctx context.Context, inputs []port.ManagementGroupUsageLookupInput) ([]port.ManagementGroupUsageRow, error) {
	return listManagementGroupUsageTotals(ctx, s.queries(), inputs)
}

func (s *Store) ListManagementGroupUsageDaily(ctx context.Context, statDate string, inputs []port.ManagementGroupUsageLookupInput) ([]port.ManagementGroupUsageRow, error) {
	return listManagementGroupUsageDaily(ctx, s.queries(), statDate, inputs)
}

func (s *Store) ListManagementGroupAuthorizationSources(ctx context.Context, authorizationIDs []string) ([]port.ManagementGroupAuthorizationSourceRow, error) {
	return listManagementGroupAuthorizationSources(ctx, s.queries(), authorizationIDs)
}

func listManagementGroups(
	ctx context.Context,
	q managementGroupListQueries,
	input port.ManagementGroupListInput,
) (port.ManagementGroupListPage, error) {
	if input.Limit <= 0 {
		return port.ManagementGroupListPage{Rows: []port.ManagementGroupListRow{}}, nil
	}
	limit := min(input.Limit, maxManagementGroupListRowLimit)
	rows, err := q.ListManagementGroups(ctx, postgresqueries.ListManagementGroupsParams{
		RowOffset:       int32(max(0, input.Offset)),
		RowLimit:        int32(limit),
		SystemAccountID: strings.TrimSpace(input.SystemAccountID),
	})
	if err != nil {
		return port.ManagementGroupListPage{}, fmt.Errorf("list management groups: %w", err)
	}

	pageSize := max(0, limit-1)
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}
	items := make([]port.ManagementGroupListRow, 0, len(rows))
	for _, row := range rows {
		if !row.EffectiveUpdatedAt.Valid {
			return port.ManagementGroupListPage{}, fmt.Errorf("list management groups returned invalid effective updated time for group %q", row.ID)
		}
		items = append(items, port.ManagementGroupListRow{
			ID:                     row.ID,
			SystemAccountID:        row.SystemAccountID,
			SystemAccountName:      row.SystemAccountName,
			Name:                   row.Name,
			ProviderCode:           row.ProviderCode,
			Description:            textPtr(row.Description),
			Enabled:                row.Enabled,
			IsDefault:              row.IsDefault,
			GroupType:              row.GroupType,
			AccessType:             row.AccessType,
			GroupAuthorizationID:   textValue(row.GroupAuthorizationID),
			AuthorizationStatus:    textValue(row.AuthorizationStatus),
			AuthorizationExpiresAt: timestamptzPtr(row.AuthorizationExpiresAt),
			EffectiveUpdatedAt:     row.EffectiveUpdatedAt.Time.UTC(),
		})
	}
	return port.ManagementGroupListPage{Rows: items, HasMore: hasMore}, nil
}

func listManagementGroupStatusSnapshotRows(
	ctx context.Context,
	q managementGroupListQueries,
	input port.ManagementGroupStatusSnapshotInput,
) ([]port.ManagementGroupStatusSnapshotRow, error) {
	ids := uniqueStrings(input.GroupIDs, maxManagementGroupListBatch)
	if len(ids) == 0 {
		return []port.ManagementGroupStatusSnapshotRow{}, nil
	}
	rows, err := q.ListManagementGroupStatusSnapshotRows(ctx, postgresqueries.ListManagementGroupStatusSnapshotRowsParams{
		SystemAccountID: strings.TrimSpace(input.SystemAccountID),
		GroupIds:        ids,
	})
	if err != nil {
		return nil, fmt.Errorf("list management group status snapshot rows: %w", err)
	}
	items := make([]port.ManagementGroupStatusSnapshotRow, 0, len(rows))
	for _, row := range rows {
		items = append(items, port.ManagementGroupStatusSnapshotRow{
			ID:                   row.ID,
			SystemAccountID:      row.SystemAccountID,
			AccessType:           row.AccessType,
			GroupAuthorizationID: textValue(row.GroupAuthorizationID),
		})
	}
	return items, nil
}

func listManagementGroupAccountStats(
	ctx context.Context,
	q managementGroupListQueries,
	groupIDs []string,
) ([]port.ManagementGroupAccountStatsRow, error) {
	ids := uniqueStrings(groupIDs, maxManagementGroupListBatch)
	if len(ids) == 0 {
		return []port.ManagementGroupAccountStatsRow{}, nil
	}
	rows, err := q.ListManagementGroupAccountStats(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list management group account stats: %w", err)
	}
	items := make([]port.ManagementGroupAccountStatsRow, 0, len(rows))
	for _, row := range rows {
		items = append(items, port.ManagementGroupAccountStatsRow{
			SystemAccountID:    row.SystemAccountID,
			GroupID:            row.GroupID,
			Total:              int(row.Total),
			Available:          int(row.Available),
			Active:             int(row.Active),
			Disabled:           int(row.Disabled),
			Error:              int(row.Error),
			RateLimited:        int(row.RateLimited),
			CurrentConcurrency: int(row.CurrentConcurrency),
			ConcurrencyLimit:   int(row.ConcurrencyLimit),
		})
	}
	return items, nil
}

func listManagementGroupUsageTotals(
	ctx context.Context,
	q managementGroupListQueries,
	inputs []port.ManagementGroupUsageLookupInput,
) ([]port.ManagementGroupUsageRow, error) {
	batch := managementGroupUsageBatch(inputs)
	if len(batch.lookupKeys) == 0 {
		return []port.ManagementGroupUsageRow{}, nil
	}
	rows, err := q.ListManagementGroupUsageTotals(ctx, postgresqueries.ListManagementGroupUsageTotalsParams{
		SystemAccountIds: batch.systemAccountIDs,
		ScopeTypes:       batch.scopeTypes,
		ScopeIds:         batch.scopeIDs,
		LookupKeys:       batch.lookupKeys,
	})
	if err != nil {
		return nil, fmt.Errorf("list management group usage totals: %w", err)
	}
	items := make([]port.ManagementGroupUsageRow, 0, len(rows))
	for _, row := range rows {
		item, err := managementGroupUsageRow(
			row.LookupKey,
			row.SystemAccountID,
			row.ScopeType,
			row.ScopeID,
			row.RequestCount,
			row.InputTokens,
			row.OutputTokens,
			row.CacheReadTokens,
			row.CacheReadCostUsd,
			row.CacheWriteTokens,
			row.CacheWrite1hTokens,
			row.CacheWriteCostUsd,
			row.ThinkingTokens,
			row.InputImageTokens,
			row.OutputImageTokens,
			row.TotalCostUsd,
			row.LastUsedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("map management group usage total %q: %w", row.LookupKey, err)
		}
		items = append(items, item)
	}
	return items, nil
}

func listManagementGroupUsageDaily(
	ctx context.Context,
	q managementGroupListQueries,
	statDate string,
	inputs []port.ManagementGroupUsageLookupInput,
) ([]port.ManagementGroupUsageRow, error) {
	batch := managementGroupUsageBatch(inputs)
	if len(batch.lookupKeys) == 0 {
		return []port.ManagementGroupUsageRow{}, nil
	}
	rows, err := q.ListManagementGroupUsageDaily(ctx, postgresqueries.ListManagementGroupUsageDailyParams{
		StatDate:         strings.TrimSpace(statDate),
		SystemAccountIds: batch.systemAccountIDs,
		ScopeTypes:       batch.scopeTypes,
		ScopeIds:         batch.scopeIDs,
		LookupKeys:       batch.lookupKeys,
	})
	if err != nil {
		return nil, fmt.Errorf("list management group daily usage: %w", err)
	}
	items := make([]port.ManagementGroupUsageRow, 0, len(rows))
	for _, row := range rows {
		item, err := managementGroupUsageRow(
			row.LookupKey,
			row.SystemAccountID,
			row.ScopeType,
			row.ScopeID,
			row.RequestCount,
			row.InputTokens,
			row.OutputTokens,
			row.CacheReadTokens,
			row.CacheReadCostUsd,
			row.CacheWriteTokens,
			row.CacheWrite1hTokens,
			row.CacheWriteCostUsd,
			row.ThinkingTokens,
			row.InputImageTokens,
			row.OutputImageTokens,
			row.TotalCostUsd,
			row.LastUsedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("map management group daily usage %q: %w", row.LookupKey, err)
		}
		items = append(items, item)
	}
	return items, nil
}

func listManagementGroupAuthorizationSources(
	ctx context.Context,
	q managementGroupListQueries,
	authorizationIDs []string,
) ([]port.ManagementGroupAuthorizationSourceRow, error) {
	ids := uniqueStrings(authorizationIDs, maxManagementGroupListBatch)
	if len(ids) == 0 {
		return []port.ManagementGroupAuthorizationSourceRow{}, nil
	}
	rows, err := q.ListManagementGroupAuthorizationSources(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list management group authorization sources: %w", err)
	}
	items := make([]port.ManagementGroupAuthorizationSourceRow, 0, len(rows))
	for _, row := range rows {
		items = append(items, port.ManagementGroupAuthorizationSourceRow{
			AuthorizationID: row.AuthorizationID,
			SourceType:      row.SourceType,
			Status:          row.Status,
			SourceTeamName:  row.SourceTeamName,
		})
	}
	return items, nil
}

type managementGroupUsageBatchParams struct {
	lookupKeys       []string
	systemAccountIDs []string
	scopeTypes       []string
	scopeIDs         []string
}

func managementGroupUsageBatch(inputs []port.ManagementGroupUsageLookupInput) managementGroupUsageBatchParams {
	batch := managementGroupUsageBatchParams{
		lookupKeys:       make([]string, 0, min(len(inputs), maxManagementGroupListBatch)),
		systemAccountIDs: make([]string, 0, min(len(inputs), maxManagementGroupListBatch)),
		scopeTypes:       make([]string, 0, min(len(inputs), maxManagementGroupListBatch)),
		scopeIDs:         make([]string, 0, min(len(inputs), maxManagementGroupListBatch)),
	}
	seen := make(map[string]struct{}, min(len(inputs), maxManagementGroupListBatch))
	for _, input := range inputs {
		key := strings.TrimSpace(input.Key)
		systemAccountID := strings.TrimSpace(input.SystemAccountID)
		scopeType := strings.TrimSpace(input.ScopeType)
		scopeID := strings.TrimSpace(input.ScopeID)
		if key == "" || systemAccountID == "" || scopeType == "" || scopeID == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		batch.lookupKeys = append(batch.lookupKeys, key)
		batch.systemAccountIDs = append(batch.systemAccountIDs, systemAccountID)
		batch.scopeTypes = append(batch.scopeTypes, scopeType)
		batch.scopeIDs = append(batch.scopeIDs, scopeID)
		if len(batch.lookupKeys) == maxManagementGroupListBatch {
			break
		}
	}
	return batch
}

func managementGroupUsageRow(
	key string,
	systemAccountID string,
	scopeType string,
	scopeID string,
	requestCount int64,
	inputTokens int64,
	outputTokens int64,
	cacheReadTokens int64,
	cacheReadCost float64,
	cacheWriteTokens int64,
	cacheWrite1hTokens int64,
	cacheWriteCost float64,
	thinkingTokens int64,
	inputImageTokens int64,
	outputImageTokens int64,
	totalCost float64,
	lastUsedAt pgtype.Text,
) (port.ManagementGroupUsageRow, error) {
	parsedLastUsedAt, err := managementGroupUsageLastUsedAt(lastUsedAt)
	if err != nil {
		return port.ManagementGroupUsageRow{}, err
	}
	return port.ManagementGroupUsageRow{
		Key:             key,
		SystemAccountID: systemAccountID,
		ScopeType:       scopeType,
		ScopeID:         scopeID,
		Usage: port.ManagementAccountUsageSummary{
			RequestCount:       requestCount,
			InputTokens:        inputTokens,
			OutputTokens:       outputTokens,
			CacheReadTokens:    cacheReadTokens,
			CacheReadCost:      cacheReadCost,
			CacheWriteTokens:   cacheWriteTokens,
			CacheWrite1hTokens: cacheWrite1hTokens,
			CacheWriteCost:     cacheWriteCost,
			ThinkingTokens:     thinkingTokens,
			InputImageTokens:   inputImageTokens,
			OutputImageTokens:  outputImageTokens,
			TotalTokens:        inputTokens + outputTokens,
			TotalCost:          totalCost,
			LastUsedAt:         parsedLastUsedAt,
		},
	}, nil
}

func managementGroupUsageLastUsedAt(value pgtype.Text) (*time.Time, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value.String))
	if err != nil {
		return nil, fmt.Errorf("parse last used time: %w", err)
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

var _ port.ManagementGroupListReader = (*Store)(nil)
