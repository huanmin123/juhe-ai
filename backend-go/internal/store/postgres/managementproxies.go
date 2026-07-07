package postgres

import (
	"context"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func (s *Store) ListManagementProxyOptions(ctx context.Context, input port.ManagementProxyOptionListInput) ([]port.ManagementProxyOption, error) {
	return listManagementProxyOptions(ctx, s.queries(), input)
}

func listManagementProxyOptions(ctx context.Context, q *postgresqueries.Queries, input port.ManagementProxyOptionListInput) ([]port.ManagementProxyOption, error) {
	keyword := strings.TrimSpace(input.Keyword)
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	rows, err := q.ListManagementProxyOptions(ctx, postgresqueries.ListManagementProxyOptionsParams{
		HasKeyword:   keyword != "",
		Keyword:      keyword,
		KeywordUpper: keywordUpper,
		RowLimit:     int32(normalizeManagementProxyOptionLimit(input.Limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("list management proxy options: %w", err)
	}
	items := make([]port.ManagementProxyOption, 0, len(rows))
	for _, row := range rows {
		items = append(items, port.ManagementProxyOption{
			ID:      row.ID,
			Name:    row.Name,
			Type:    row.Type,
			Enabled: row.Enabled,
		})
	}
	return items, nil
}

func normalizeManagementProxyOptionLimit(value int) int {
	if value <= 0 {
		return 50
	}
	if value > 50 {
		return 50
	}
	return value
}

var _ port.ManagementProxyOptionReader = (*Store)(nil)
