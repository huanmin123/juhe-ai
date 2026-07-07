package postgres

import (
	"context"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const (
	defaultManagementRouteStrategyOptionLimit = 50
	maxManagementRouteStrategyOptionLimit     = 100
)

func (s *Store) ListManagementRouteStrategyOptions(ctx context.Context, input port.ManagementRouteStrategyOptionListInput) ([]port.ManagementRouteStrategyOption, error) {
	return listManagementRouteStrategyOptions(ctx, s.queries(), input)
}

func listManagementRouteStrategyOptions(ctx context.Context, q *postgresqueries.Queries, input port.ManagementRouteStrategyOptionListInput) ([]port.ManagementRouteStrategyOption, error) {
	keyword := strings.TrimSpace(input.Keyword)
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	rows, err := q.ListManagementRouteStrategyOptions(ctx, postgresqueries.ListManagementRouteStrategyOptionsParams{
		SystemAccountID: strings.TrimSpace(input.SystemAccountID),
		Ids:             uniqueStrings(input.IDs, 50),
		HasKeyword:      keyword != "",
		Keyword:         keyword,
		KeywordUpper:    keywordUpper,
		ActiveOnly:      input.ActiveOnly,
		RowLimit:        int32(managementRouteStrategyOptionLimit(input.Limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("list management route strategy options: %w", err)
	}
	options := make([]port.ManagementRouteStrategyOption, 0, len(rows))
	for _, row := range rows {
		option := port.ManagementRouteStrategyOption{
			ID:        row.ID,
			Name:      row.Name,
			Mode:      row.Mode,
			Status:    row.Status,
			IsDefault: row.IsDefault,
		}
		if input.IncludeSystemAccountFields {
			option.SystemAccountID = row.SystemAccountID
			option.SystemAccountName = textValue(row.SystemAccountName)
		}
		options = append(options, option)
	}
	return options, nil
}

func managementRouteStrategyOptionLimit(limit int) int {
	if limit <= 0 {
		return defaultManagementRouteStrategyOptionLimit
	}
	return min(limit, maxManagementRouteStrategyOptionLimit)
}

func uniqueStrings(values []string, maxItems int) []string {
	seen := make(map[string]struct{}, len(values))
	output := make([]string, 0, len(values))
	for _, value := range values {
		text := strings.TrimSpace(value)
		if text == "" {
			continue
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		output = append(output, text)
		if len(output) >= maxItems {
			break
		}
	}
	return output
}

var _ port.ManagementRouteStrategyOptionReader = (*Store)(nil)
