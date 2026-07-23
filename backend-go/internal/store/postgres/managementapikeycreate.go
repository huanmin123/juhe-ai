package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

type managementAPIKeyCreateQueries interface {
	CreateManagementAPIKey(
		ctx context.Context,
		arg postgresqueries.CreateManagementAPIKeyParams,
	) (postgresqueries.CreateManagementAPIKeyRow, error)
}

func (s *Store) CreateManagementAPIKey(
	ctx context.Context,
	input port.ManagementAPIKeyCreateInput,
) (port.ManagementAPIKeyListRow, error) {
	return createManagementAPIKey(ctx, s.queries(), input)
}

func createManagementAPIKey(
	ctx context.Context,
	q managementAPIKeyCreateQueries,
	input port.ManagementAPIKeyCreateInput,
) (port.ManagementAPIKeyListRow, error) {
	row, err := q.CreateManagementAPIKey(ctx, postgresqueries.CreateManagementAPIKeyParams{
		ID:                              strings.TrimSpace(input.ID),
		SystemAccountID:                 strings.TrimSpace(input.SystemAccountID),
		RouteStrategyID:                 strings.TrimSpace(input.RouteStrategyID),
		Name:                            input.Name,
		Description:                     pgTextPtr(input.Description),
		KeyHash:                         input.KeyHash,
		KeyPrefix:                       input.KeyPrefix,
		KeySuffix:                       input.KeySuffix,
		KeySecretEncrypted:              input.KeySecretEncrypted,
		Status:                          input.Status,
		ExpiresAt:                       pgTimestamptzPtr(input.ExpiresAt),
		QuotaLimitsJson:                 pgTextPtr(input.QuotaLimitsJSON),
		AvailabilityScheduleJson:        pgTextPtr(input.AvailabilityScheduleJSON),
		AvailabilityScheduleNextCheckAt: pgTimestamptzPtr(input.AvailabilityScheduleNextCheckAt),
		CreatedAt:                       pgTimestamptz(input.CreatedAt),
		UpdatedAt:                       pgTimestamptz(input.UpdatedAt),
		HourlyHours:                     managementAPIKeyInt4Ptr(input.HourlyQuotaHours),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementAPIKeyListRow{}, port.ErrManagementAPIKeyRouteStrategyNotFound
	}
	if err != nil {
		switch managementAPIKeyCreateConstraint(err) {
		case "name":
			return port.ManagementAPIKeyListRow{}, port.ErrManagementAPIKeyNameExists
		case "hash":
			return port.ManagementAPIKeyListRow{}, port.ErrManagementAPIKeyHashExists
		default:
			return port.ManagementAPIKeyListRow{}, fmt.Errorf("create management API Key: %w", err)
		}
	}
	if !row.ApiKeyID.Valid {
		if row.RouteStrategyStatus == "disabled" {
			return port.ManagementAPIKeyListRow{}, port.ErrManagementAPIKeyRouteStrategyDisabled
		}
		return port.ManagementAPIKeyListRow{}, port.ErrManagementAPIKeyRouteStrategyNotFound
	}
	return port.ManagementAPIKeyListRow{
		ID:                       row.ApiKeyID.String,
		SystemAccountID:          row.SystemAccountID.String,
		SystemAccountName:        row.SystemAccountName,
		Name:                     row.ApiKeyName.String,
		Description:              textPtr(row.Description),
		KeyPrefix:                row.KeyPrefix.String,
		KeySuffix:                row.KeySuffix.String,
		Status:                   row.ApiKeyStatus.String,
		IsDefault:                row.IsDefault.Bool,
		Purpose:                  row.Purpose.String,
		RouteStrategyID:          row.RouteStrategyID.String,
		RouteStrategyName:        row.RouteStrategyName,
		RouteStrategyMode:        row.RouteStrategyMode,
		RouteStrategyStatus:      row.RouteStrategyStatus,
		ExpiresAt:                timestamptzPtr(row.ExpiresAt),
		QuotaLimitsJSON:          textPtr(row.QuotaLimitsJson),
		AvailabilityScheduleJSON: textPtr(row.AvailabilityScheduleJson),
	}, nil
}

func managementAPIKeyInt4Ptr(value *int) pgtype.Int4 {
	if value == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(*value), Valid: true}
}

func managementAPIKeyCreateConstraint(err error) string {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return ""
	}
	switch pgErr.ConstraintName {
	case "idx_api_keys_owner_name_unique_lower", "idx_api_keys_owner_name_unique":
		return "name"
	case "idx_api_keys_key_hash_unique":
		return "hash"
	default:
		return ""
	}
}

var _ port.ManagementAPIKeyCreator = (*Store)(nil)
