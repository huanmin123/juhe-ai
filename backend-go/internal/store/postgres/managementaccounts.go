package postgres

import (
	"context"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const (
	defaultManagementAccountOptionLimit = 50
	maxManagementAccountOptionLimit     = 50
)

func (s *Store) ListManagementAccountOptions(ctx context.Context, input port.ManagementAccountOptionListInput) ([]port.ManagementAccountOption, error) {
	return listManagementAccountOptions(ctx, s.queries(), input)
}

func (s *Store) ListManagementAccountTags(ctx context.Context, input port.ManagementAccountTagListInput) ([]port.ManagementAccountTag, error) {
	return listManagementAccountTags(ctx, s.queries(), input)
}

func listManagementAccountOptions(ctx context.Context, q *postgresqueries.Queries, input port.ManagementAccountOptionListInput) ([]port.ManagementAccountOption, error) {
	keyword := strings.TrimSpace(input.Keyword)
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	limit := managementAccountOptionLimit(input.Limit)
	offset := input.Offset
	if offset < 0 {
		offset = 0
	}
	rows, err := q.ListManagementAccountOptions(ctx, postgresqueries.ListManagementAccountOptionsParams{
		SystemAccountID: strings.TrimSpace(input.SystemAccountID),
		Ids:             uniqueStrings(input.IDs, 50),
		ProviderCode:    strings.TrimSpace(input.ProviderCode),
		GroupID:         strings.TrimSpace(input.GroupID),
		TagIds:          uniqueStrings(input.TagIDs, 100),
		AccountType:     strings.TrimSpace(input.Type),
		Statuses:        uniqueStrings(input.Statuses, 20),
		Schedulable:     strings.TrimSpace(input.Schedulable),
		HasKeyword:      keyword != "",
		Keyword:         keyword,
		KeywordUpper:    keywordUpper,
		RowLimit:        int32(limit),
		RowOffset:       int32(offset),
	})
	if err != nil {
		return nil, fmt.Errorf("list management account options: %w", err)
	}
	options := make([]port.ManagementAccountOption, 0, len(rows))
	for _, row := range rows {
		option := port.ManagementAccountOption{
			ID:                        row.ID,
			OwnerSystemAccountID:      row.SystemAccountID,
			ProviderCode:              row.ProviderCode,
			ProviderProtocolProfileID: row.ProviderProtocolProfileID,
			ProtocolCode:              row.ProtocolCode,
			ProtocolVersion:           row.ProtocolVersion,
			Name:                      row.Name,
			Type:                      row.Type,
			Status:                    row.Status,
			AccountExpiresAt:          timestamptzPtr(row.AccountExpiresAt),
		}
		if input.IncludeSystemAccountFields {
			option.SystemAccountID = row.SystemAccountID
			option.SystemAccountName = row.SystemAccountName
			option.OwnerSystemAccountName = row.SystemAccountName
		}
		options = append(options, option)
	}
	return options, nil
}

func listManagementAccountTags(ctx context.Context, q *postgresqueries.Queries, input port.ManagementAccountTagListInput) ([]port.ManagementAccountTag, error) {
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	if systemAccountID == "" {
		return nil, fmt.Errorf("management account tag system account id is required")
	}
	rows, err := q.ListManagementAccountTags(ctx, systemAccountID)
	if err != nil {
		return nil, fmt.Errorf("list management account tags: %w", err)
	}
	tags := make([]port.ManagementAccountTag, 0, len(rows))
	for _, row := range rows {
		accountCount := int(row.AccountCount)
		if accountCount < 0 {
			accountCount = 0
		}
		tags = append(tags, port.ManagementAccountTag{
			ID:           row.ID,
			Name:         row.Name,
			AccountCount: accountCount,
			CreatedAt:    timestamptzValue(row.CreatedAt),
			UpdatedAt:    timestamptzValue(row.UpdatedAt),
		})
	}
	return tags, nil
}

func managementAccountOptionLimit(limit int) int {
	if limit <= 0 {
		return defaultManagementAccountOptionLimit
	}
	return min(limit, maxManagementAccountOptionLimit)
}

var _ port.ManagementAccountOptionReader = (*Store)(nil)
