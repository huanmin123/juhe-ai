package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

type managementAPIKeyUpdateQueries interface {
	UpdateManagementAPIKey(
		ctx context.Context,
		input postgresqueries.UpdateManagementAPIKeyParams,
	) (postgresqueries.UpdateManagementAPIKeyRow, error)
}

func (s *Store) UpdateManagementAPIKey(
	ctx context.Context,
	input port.ManagementAPIKeyUpdateInput,
) (port.ManagementAPIKeyUpdateResult, error) {
	return updateManagementAPIKey(ctx, s.queries(), input)
}

func updateManagementAPIKey(
	ctx context.Context,
	q managementAPIKeyUpdateQueries,
	input port.ManagementAPIKeyUpdateInput,
) (port.ManagementAPIKeyUpdateResult, error) {
	row, err := q.UpdateManagementAPIKey(ctx, postgresqueries.UpdateManagementAPIKeyParams{
		ApiKeyID:                        strings.TrimSpace(input.APIKeyID),
		OwnerSystemAccountID:            strings.TrimSpace(input.OwnerSystemAccountID),
		HasRouteStrategyID:              input.HasRouteStrategyID,
		RouteStrategyID:                 strings.TrimSpace(input.RouteStrategyID),
		HasName:                         input.HasName,
		Name:                            input.Name,
		HasDescription:                  input.HasDescription,
		Description:                     pgTextPtr(input.Description),
		HasStatus:                       input.HasStatus,
		Status:                          input.Status,
		HasExpiresAt:                    input.HasExpiresAt,
		ExpiresAt:                       pgTimestamptzPtr(input.ExpiresAt),
		HasQuotaLimits:                  input.HasQuotaLimits,
		QuotaLimitsJson:                 pgTextPtr(input.QuotaLimitsJSON),
		HasAvailabilitySchedule:         input.HasAvailabilitySchedule,
		AvailabilityScheduleJson:        pgTextPtr(input.AvailabilityScheduleJSON),
		AvailabilityScheduleNextCheckAt: pgTimestamptzPtr(input.AvailabilityScheduleNextCheckAt),
		UpdatedAt:                       pgTimestamptz(input.UpdatedAt),
		HourlyHours:                     managementAPIKeyInt4Ptr(input.HourlyQuotaHours),
	})
	if err != nil {
		switch {
		case managementAPIKeyCreateConstraint(err) == "name":
			return port.ManagementAPIKeyUpdateResult{}, port.ErrManagementAPIKeyNameExists
		case errors.Is(err, pgx.ErrNoRows):
			return port.ManagementAPIKeyUpdateResult{}, port.ErrManagementAPIKeyNotFound
		default:
			return port.ManagementAPIKeyUpdateResult{},
				fmt.Errorf("update management API Key: %w", err)
		}
	}
	switch {
	case !row.BeforeApiKeyID.Valid:
		return port.ManagementAPIKeyUpdateResult{}, port.ErrManagementAPIKeyNotFound
	case row.DefaultRouteChange:
		return port.ManagementAPIKeyUpdateResult{}, port.ErrManagementAPIKeyDefaultRouteChange
	case row.RouteChanged && !row.RouteFound:
		return port.ManagementAPIKeyUpdateResult{}, port.ErrManagementAPIKeyRouteStrategyNotFound
	case row.RouteChanged && !row.RouteActive:
		return port.ManagementAPIKeyUpdateResult{}, port.ErrManagementAPIKeyRouteStrategyDisabled
	case !row.AfterApiKeyID.Valid:
		return port.ManagementAPIKeyUpdateResult{},
			fmt.Errorf("update management API Key returned no updated row")
	}
	return port.ManagementAPIKeyUpdateResult{
		Before: managementAPIKeyUpdateBeforeRow(row),
		After:  managementAPIKeyUpdateAfterRow(row),
	}, nil
}

func managementAPIKeyUpdateBeforeRow(
	row postgresqueries.UpdateManagementAPIKeyRow,
) port.ManagementAPIKeyListRow {
	return managementAPIKeyUpdateListRow(
		row.BeforeApiKeyID,
		row.BeforeSystemAccountID,
		row.BeforeSystemAccountName,
		row.BeforeName,
		row.BeforeDescription,
		row.BeforeKeyPrefix,
		row.BeforeKeySuffix,
		row.BeforeStatus,
		row.BeforeIsDefault,
		row.BeforePurpose,
		row.BeforeRouteStrategyID,
		textValue(row.BeforeRouteStrategyName),
		textValue(row.BeforeRouteStrategyMode),
		textValue(row.BeforeRouteStrategyStatus),
		row.BeforeExpiresAt,
		row.BeforeQuotaLimitsJson,
		row.BeforeAvailabilityScheduleJson,
	)
}

func managementAPIKeyUpdateAfterRow(
	row postgresqueries.UpdateManagementAPIKeyRow,
) port.ManagementAPIKeyListRow {
	return managementAPIKeyUpdateListRow(
		row.AfterApiKeyID,
		row.AfterSystemAccountID,
		row.AfterSystemAccountName,
		row.AfterName,
		row.AfterDescription,
		row.AfterKeyPrefix,
		row.AfterKeySuffix,
		row.AfterStatus,
		row.AfterIsDefault,
		row.AfterPurpose,
		row.AfterRouteStrategyID,
		row.AfterRouteStrategyName,
		row.AfterRouteStrategyMode,
		row.AfterRouteStrategyStatus,
		row.AfterExpiresAt,
		row.AfterQuotaLimitsJson,
		row.AfterAvailabilityScheduleJson,
	)
}

func managementAPIKeyUpdateListRow(
	id pgtype.Text,
	systemAccountID pgtype.Text,
	systemAccountName pgtype.Text,
	name pgtype.Text,
	description pgtype.Text,
	keyPrefix pgtype.Text,
	keySuffix pgtype.Text,
	status pgtype.Text,
	isDefault pgtype.Bool,
	purpose pgtype.Text,
	routeStrategyID pgtype.Text,
	routeStrategyName string,
	routeStrategyMode string,
	routeStrategyStatus string,
	expiresAt pgtype.Timestamptz,
	quotaLimitsJSON pgtype.Text,
	availabilityScheduleJSON pgtype.Text,
) port.ManagementAPIKeyListRow {
	return port.ManagementAPIKeyListRow{
		ID:                       textValue(id),
		SystemAccountID:          textValue(systemAccountID),
		SystemAccountName:        textValue(systemAccountName),
		Name:                     textValue(name),
		Description:              textPtr(description),
		KeyPrefix:                textValue(keyPrefix),
		KeySuffix:                textValue(keySuffix),
		Status:                   textValue(status),
		IsDefault:                isDefault.Bool,
		Purpose:                  textValue(purpose),
		RouteStrategyID:          textValue(routeStrategyID),
		RouteStrategyName:        routeStrategyName,
		RouteStrategyMode:        routeStrategyMode,
		RouteStrategyStatus:      routeStrategyStatus,
		ExpiresAt:                timestamptzPtr(expiresAt),
		QuotaLimitsJSON:          textPtr(quotaLimitsJSON),
		AvailabilityScheduleJSON: textPtr(availabilityScheduleJSON),
	}
}

var _ port.ManagementAPIKeyUpdater = (*Store)(nil)
