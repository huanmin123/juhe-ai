package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

type managementGroupDetailQueries interface {
	FindManagementGroupDetail(ctx context.Context, arg postgresqueries.FindManagementGroupDetailParams) (postgresqueries.FindManagementGroupDetailRow, error)
	ListManagementGroupDetailAccountIDs(ctx context.Context, arg postgresqueries.ListManagementGroupDetailAccountIDsParams) ([]string, error)
	ListManagementGroupDetailAuthorizationSources(ctx context.Context, arg postgresqueries.ListManagementGroupDetailAuthorizationSourcesParams) ([]postgresqueries.ListManagementGroupDetailAuthorizationSourcesRow, error)
}

func (s *Store) FindManagementGroupDetail(ctx context.Context, input port.ManagementGroupDetailInput) (port.ManagementGroupListRow, bool, error) {
	return findManagementGroupDetail(ctx, s.queries(), input)
}

func (s *Store) ListManagementGroupDetailAccountIDs(ctx context.Context, input port.ManagementGroupDetailInput) ([]string, error) {
	return listManagementGroupDetailAccountIDs(ctx, s.queries(), input)
}

func (s *Store) ListManagementGroupDetailAuthorizationSources(ctx context.Context, input port.ManagementGroupDetailInput) ([]port.ManagementResourceAuthorizationSourceSummary, error) {
	return listManagementGroupDetailAuthorizationSources(ctx, s.queries(), input)
}

func findManagementGroupDetail(
	ctx context.Context,
	q managementGroupDetailQueries,
	input port.ManagementGroupDetailInput,
) (port.ManagementGroupListRow, bool, error) {
	row, err := q.FindManagementGroupDetail(ctx, managementGroupDetailParams(input))
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementGroupListRow{}, false, nil
	}
	if err != nil {
		return port.ManagementGroupListRow{}, false, fmt.Errorf("find management group detail: %w", err)
	}
	if !row.EffectiveUpdatedAt.Valid {
		return port.ManagementGroupListRow{}, false, fmt.Errorf("find management group detail returned invalid effective updated time for group %q", row.ID)
	}
	return port.ManagementGroupListRow{
		ID:                      row.ID,
		SystemAccountID:         row.SystemAccountID,
		SystemAccountName:       row.SystemAccountName,
		Name:                    row.Name,
		ProviderCode:            row.ProviderCode,
		Description:             textPtr(row.Description),
		Enabled:                 row.Enabled,
		IsDefault:               row.IsDefault,
		GroupType:               row.GroupType,
		SchedulingPolicyJSON:    textPtr(row.SchedulingPolicyJson),
		AccessType:              row.AccessType,
		GroupAuthorizationID:    textValue(row.GroupAuthorizationID),
		AuthorizationStatus:     textValue(row.AuthorizationStatus),
		AuthorizationExpiresAt:  timestamptzPtr(row.AuthorizationExpiresAt),
		AuthorizationLimitsJSON: textPtr(row.AuthorizationLimitsJson),
		EffectiveUpdatedAt:      row.EffectiveUpdatedAt.Time.UTC(),
	}, true, nil
}

func listManagementGroupDetailAccountIDs(
	ctx context.Context,
	q managementGroupDetailQueries,
	input port.ManagementGroupDetailInput,
) ([]string, error) {
	ids, err := q.ListManagementGroupDetailAccountIDs(ctx, postgresqueries.ListManagementGroupDetailAccountIDsParams(managementGroupDetailParams(input)))
	if err != nil {
		return nil, fmt.Errorf("list management group detail account ids: %w", err)
	}
	if ids == nil {
		return []string{}, nil
	}
	return ids, nil
}

func listManagementGroupDetailAuthorizationSources(
	ctx context.Context,
	q managementGroupDetailQueries,
	input port.ManagementGroupDetailInput,
) ([]port.ManagementResourceAuthorizationSourceSummary, error) {
	rows, err := q.ListManagementGroupDetailAuthorizationSources(
		ctx,
		postgresqueries.ListManagementGroupDetailAuthorizationSourcesParams(managementGroupDetailParams(input)),
	)
	if err != nil {
		return nil, fmt.Errorf("list management group detail authorization sources: %w", err)
	}
	items := make([]port.ManagementResourceAuthorizationSourceSummary, 0, len(rows))
	for _, row := range rows {
		if !row.CreatedAt.Valid || !row.UpdatedAt.Valid {
			return nil, fmt.Errorf("management group authorization source %q has invalid timestamps", row.ID)
		}
		items = append(items, port.ManagementResourceAuthorizationSourceSummary{
			ID:              row.ID,
			AuthorizationID: row.AuthorizationID,
			SourceType:      row.SourceType,
			SourceTeamID:    row.SourceTeamID,
			SourceTeamName:  row.SourceTeamName,
			Status:          row.Status,
			ActivatedAt:     timestamptzPtr(row.ActivatedAt),
			EndedAt:         timestamptzPtr(row.EndedAt),
			EndedReason:     row.EndedReason,
			CreatedBy:       row.CreatedBy,
			CreatedAt:       row.CreatedAt.Time.UTC(),
			RevokedBy:       row.RevokedBy,
			RevokedAt:       timestamptzPtr(row.RevokedAt),
			UpdatedAt:       row.UpdatedAt.Time.UTC(),
		})
	}
	return items, nil
}

func managementGroupDetailParams(input port.ManagementGroupDetailInput) postgresqueries.FindManagementGroupDetailParams {
	return postgresqueries.FindManagementGroupDetailParams{
		GroupID:         input.GroupID,
		SystemAccountID: input.SystemAccountID,
	}
}

var _ port.ManagementGroupDetailReader = (*Store)(nil)
