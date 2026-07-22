package postgres

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

type managementAccountListQueries interface {
	ListManagementAccounts(context.Context, postgresqueries.ListManagementAccountsParams) ([]postgresqueries.ListManagementAccountsRow, error)
}

func (s *Store) ListManagementAccounts(ctx context.Context, input port.ManagementAccountListInput) (port.ManagementAccountListPage, error) {
	return listManagementAccounts(ctx, s.queries(), input)
}

func listManagementAccounts(ctx context.Context, q managementAccountListQueries, input port.ManagementAccountListInput) (port.ManagementAccountListPage, error) {
	var sortField, sortOrder string
	if len(input.Sorts) > 0 {
		sortField, sortOrder = input.Sorts[0].Field, input.Sorts[0].Order
	}
	rows, err := q.ListManagementAccounts(ctx, postgresqueries.ListManagementAccountsParams{SystemAccountID: strings.TrimSpace(input.SystemAccountID), Keyword: strings.TrimSpace(input.Keyword), ProviderCode: strings.TrimSpace(input.ProviderCode), AccountType: strings.TrimSpace(input.Type), Statuses: input.Statuses, TagIds: input.TagIDs, Schedulable: input.Schedulable, GroupID: strings.TrimSpace(input.GroupID), SortField: sortField, SortOrder: sortOrder, RowLimit: int32(input.Limit), RowOffset: int32(input.Offset)})
	if err != nil {
		return port.ManagementAccountListPage{}, fmt.Errorf("list management accounts: %w", err)
	}
	items := make([]port.ManagementAccountListRow, 0, len(rows))
	for _, row := range rows {
		items = append(items, port.ManagementAccountListRow{ID: row.ID, SystemAccountID: row.SystemAccountID, SystemAccountName: row.SystemAccountName, Name: row.Name, ProviderCode: row.ProviderCode, Type: row.Type, Status: row.Status, Schedulable: row.Schedulable, ConcurrencyLimit: int(row.ConcurrencyLimit), Priority: int(row.Priority), SuperPriorityEnabled: row.SuperPriorityEnabled, FallbackEnabled: row.FallbackEnabled, AccountExpiresAt: nullableTime(row.AccountExpiresAt), LastUsedAt: nullableTime(row.LastUsedAt), AccessType: row.AccessType, AccountAuthorizationID: nullableText(row.AccountAuthorizationID), AuthorizationStatus: nullableText(row.AuthorizationStatus), AuthorizationExpiresAt: nullableTime(row.AuthorizationExpiresAt), RequestCount: row.RequestCount, InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, TotalCost: row.TotalCost, QualityScore: qualityScore(row.RequestCount, row.SuccessCount), ProxyProfileID: nullableText(row.ProxyProfileID), ProxyProfileName: nullableText(row.ProxyProfileName), ProxyProfileType: nullableText(row.ProxyProfileType), ProxyProfileEnabled: nullableBool(row.ProxyProfileEnabled)})
	}
	return port.ManagementAccountListPage{Rows: items, HasMore: len(items) > input.Limit}, nil
}

func nullableTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}
func nullableText(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}
func qualityScore(requestCount, successCount int64) *int64 {
	if requestCount <= 0 {
		return nil
	}
	result := int64(math.Round(float64(successCount) * 1_000_000 / float64(requestCount)))
	return &result
}

func nullableBool(value pgtype.Bool) *bool {
	if !value.Valid {
		return nil
	}
	result := value.Bool
	return &result
}

var _ port.ManagementAccountListReader = (*Store)(nil)
