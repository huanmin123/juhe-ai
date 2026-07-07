package postgres

import (
	"context"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const (
	defaultManagementAuthorizationPrincipalOptionLimit = 50
	maxManagementAuthorizationPrincipalOptionLimit     = 50
)

func (s *Store) ListManagementAuthorizationGranteeAccounts(ctx context.Context, input port.ManagementAuthorizationPrincipalOptionListInput) ([]port.ManagementAuthorizationGranteeAccountOption, error) {
	return listManagementAuthorizationGranteeAccounts(ctx, s.queries(), input)
}

func listManagementAuthorizationGranteeAccounts(ctx context.Context, q *postgresqueries.Queries, input port.ManagementAuthorizationPrincipalOptionListInput) ([]port.ManagementAuthorizationGranteeAccountOption, error) {
	keyword := strings.TrimSpace(input.Keyword)
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	ids := uniqueStrings(input.IDs, 50)
	rows, err := q.ListManagementAuthorizationGranteeAccounts(ctx, postgresqueries.ListManagementAuthorizationGranteeAccountsParams{
		HasIds:       len(ids) > 0,
		Ids:          ids,
		HasKeyword:   keyword != "",
		Keyword:      keyword,
		KeywordUpper: keywordUpper,
		RowLimit:     int32(managementAuthorizationPrincipalOptionLimit(input.Limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("list management authorization grantee accounts: %w", err)
	}
	items := make([]port.ManagementAuthorizationGranteeAccountOption, 0, len(rows))
	for _, row := range rows {
		items = append(items, port.ManagementAuthorizationGranteeAccountOption{
			ID:          row.ID,
			Username:    row.Username,
			DisplayName: row.DisplayName,
			Status:      row.Status,
		})
	}
	return items, nil
}

func managementAuthorizationPrincipalOptionLimit(limit int) int {
	if limit <= 0 {
		return defaultManagementAuthorizationPrincipalOptionLimit
	}
	return min(limit, maxManagementAuthorizationPrincipalOptionLimit)
}

var _ port.ManagementAuthorizationOptionReader = (*Store)(nil)
