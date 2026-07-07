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

func (s *Store) ListManagementAuthorizationGranteeTeams(ctx context.Context, input port.ManagementAuthorizationPrincipalOptionListInput) ([]port.ManagementAuthorizationGranteeTeamOption, error) {
	return listManagementAuthorizationGranteeTeams(ctx, s.queries(), input)
}

func (s *Store) ListManagementAuthorizationGranteeGroups(ctx context.Context, input port.ManagementAuthorizationGranteeGroupOptionListInput) ([]port.ManagementAuthorizationGranteeGroupOption, error) {
	return listManagementAuthorizationGranteeGroups(ctx, s.queries(), input)
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

func listManagementAuthorizationGranteeTeams(ctx context.Context, q *postgresqueries.Queries, input port.ManagementAuthorizationPrincipalOptionListInput) ([]port.ManagementAuthorizationGranteeTeamOption, error) {
	keyword := strings.TrimSpace(input.Keyword)
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	ids := uniqueStrings(input.IDs, 50)
	rows, err := q.ListManagementAuthorizationGranteeTeams(ctx, postgresqueries.ListManagementAuthorizationGranteeTeamsParams{
		HasIds:       len(ids) > 0,
		Ids:          ids,
		HasKeyword:   keyword != "",
		Keyword:      keyword,
		KeywordUpper: keywordUpper,
		RowLimit:     int32(managementAuthorizationPrincipalOptionLimit(input.Limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("list management authorization grantee teams: %w", err)
	}
	items := make([]port.ManagementAuthorizationGranteeTeamOption, 0, len(rows))
	for _, row := range rows {
		items = append(items, port.ManagementAuthorizationGranteeTeamOption{
			ID:     row.ID,
			Name:   row.Name,
			Status: row.Status,
		})
	}
	return items, nil
}

func listManagementAuthorizationGranteeGroups(ctx context.Context, q *postgresqueries.Queries, input port.ManagementAuthorizationGranteeGroupOptionListInput) ([]port.ManagementAuthorizationGranteeGroupOption, error) {
	keyword := strings.TrimSpace(input.Keyword)
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	rows, err := q.ListManagementAuthorizationGranteeGroups(ctx, postgresqueries.ListManagementAuthorizationGranteeGroupsParams{
		GranteeSystemAccountID: strings.TrimSpace(input.GranteeSystemAccountID),
		Ids:                    uniqueStrings(input.IDs, 50),
		ProviderCode:           strings.TrimSpace(input.ProviderCode),
		HasKeyword:             keyword != "",
		Keyword:                keyword,
		KeywordUpper:           keywordUpper,
		PreferDefault:          input.PreferDefault,
		RowLimit:               int32(managementAuthorizationPrincipalOptionLimit(input.Limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("list management authorization grantee groups: %w", err)
	}
	items := make([]port.ManagementAuthorizationGranteeGroupOption, 0, len(rows))
	for _, row := range rows {
		schedulingPolicy, err := managementGroupSchedulingPolicy(row.ID, row.GroupType, row.SchedulingPolicyJson)
		if err != nil {
			return nil, err
		}
		item := port.ManagementAuthorizationGranteeGroupOption{
			ID:                     row.ID,
			OwnerSystemAccountID:   row.SystemAccountID,
			OwnerSystemAccountName: textValue(row.SystemAccountName),
			Name:                   row.Name,
			ProviderCode:           row.ProviderCode,
			Enabled:                row.Enabled,
			IsDefault:              row.IsDefault,
			GroupType:              managementGroupType(row.GroupType),
			SchedulingPolicy:       schedulingPolicy,
			AccessType:             "owner",
		}
		if input.IncludeSystemAccountFields {
			item.SystemAccountID = row.SystemAccountID
			item.SystemAccountName = textValue(row.SystemAccountName)
		} else {
			item.OwnerSystemAccountName = ""
		}
		items = append(items, item)
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
