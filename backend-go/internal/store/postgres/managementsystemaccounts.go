package postgres

import (
	"context"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const (
	defaultManagementSystemAccountOptionLimit = 50
	maxManagementSystemAccountOptionLimit     = 50
)

func (s *Store) ListManagementSystemAccounts(ctx context.Context, input port.ManagementSystemAccountListInput) (port.ManagementSystemAccountListResult, error) {
	return listManagementSystemAccounts(ctx, s.queries(), input)
}

func (s *Store) ListManagementSystemAccountOptions(ctx context.Context, input port.ManagementSystemAccountOptionListInput) ([]port.ManagementSystemAccountOption, error) {
	return listManagementSystemAccountOptions(ctx, s.queries(), input)
}

func listManagementSystemAccounts(ctx context.Context, q *postgresqueries.Queries, input port.ManagementSystemAccountListInput) (port.ManagementSystemAccountListResult, error) {
	keyword := strings.ToLower(strings.TrimSpace(input.Keyword))
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	limit := input.Limit
	if limit <= 0 {
		return port.ManagementSystemAccountListResult{}, nil
	}
	rows, err := q.ListManagementSystemAccounts(ctx, postgresqueries.ListManagementSystemAccountsParams{
		Keyword:      keyword,
		KeywordUpper: keywordUpper,
		RowLimit:     int32(limit),
		RowOffset:    int32(max(0, input.Offset)),
	})
	if err != nil {
		return port.ManagementSystemAccountListResult{}, fmt.Errorf("list management system accounts: %w", err)
	}
	pageSize := max(0, limit-1)
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}
	items := make([]port.ManagementSystemAccountSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, port.ManagementSystemAccountSummary{
			ID:                     row.ID,
			Username:               row.Username,
			DisplayName:            row.DisplayName,
			Description:            textValue(row.Description),
			Role:                   row.Role,
			Status:                 row.Status,
			MustChangePassword:     row.MustChangePassword,
			ImageGenerationEnabled: row.ImageGenerationEnabled,
			LastLoginAt:            timestamptzPtr(row.LastLoginAt),
			CreatedAt:              timestamptzValue(row.CreatedAt),
			UpdatedAt:              timestamptzValue(row.UpdatedAt),
		})
	}
	return port.ManagementSystemAccountListResult{Items: items, HasMore: hasMore}, nil
}

func listManagementSystemAccountOptions(ctx context.Context, q *postgresqueries.Queries, input port.ManagementSystemAccountOptionListInput) ([]port.ManagementSystemAccountOption, error) {
	keyword := strings.TrimSpace(input.Keyword)
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	ids := uniqueStrings(input.IDs, 50)
	rows, err := q.ListManagementSystemAccountOptions(ctx, postgresqueries.ListManagementSystemAccountOptionsParams{
		HasIds:       len(ids) > 0,
		Ids:          ids,
		HasKeyword:   keyword != "",
		Keyword:      keyword,
		KeywordUpper: keywordUpper,
		RowLimit:     int32(managementSystemAccountOptionLimit(input.Limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("list management system account options: %w", err)
	}
	items := make([]port.ManagementSystemAccountOption, 0, len(rows))
	for _, row := range rows {
		items = append(items, port.ManagementSystemAccountOption{
			ID:          row.ID,
			Username:    row.Username,
			DisplayName: row.DisplayName,
			Status:      row.Status,
		})
	}
	return items, nil
}

func managementSystemAccountOptionLimit(limit int) int {
	if limit <= 0 {
		return defaultManagementSystemAccountOptionLimit
	}
	return min(limit, maxManagementSystemAccountOptionLimit)
}

var _ port.ManagementSystemAccountOptionReader = (*Store)(nil)
