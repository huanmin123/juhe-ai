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

func (s *Store) ListManagementSystemAccountOptions(ctx context.Context, input port.ManagementSystemAccountOptionListInput) ([]port.ManagementSystemAccountOption, error) {
	return listManagementSystemAccountOptions(ctx, s.queries(), input)
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
