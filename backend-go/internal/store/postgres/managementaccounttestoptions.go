package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

type managementAccountTestOptionsQueries interface {
	GetManagementAccountTestOptionsSource(
		ctx context.Context,
		arg postgresqueries.GetManagementAccountTestOptionsSourceParams,
	) (postgresqueries.GetManagementAccountTestOptionsSourceRow, error)
}

func (s *Store) GetManagementAccountTestOptionsSource(
	ctx context.Context,
	input port.ManagementAccountTestOptionsInput,
) (port.ManagementAccountTestOptionsSource, bool, error) {
	return getManagementAccountTestOptionsSource(ctx, s.queries(), input)
}

func getManagementAccountTestOptionsSource(
	ctx context.Context,
	q managementAccountTestOptionsQueries,
	input port.ManagementAccountTestOptionsInput,
) (port.ManagementAccountTestOptionsSource, bool, error) {
	row, err := q.GetManagementAccountTestOptionsSource(
		ctx,
		postgresqueries.GetManagementAccountTestOptionsSourceParams{
			AccountID:       strings.TrimSpace(input.AccountID),
			SystemAccountID: strings.TrimSpace(input.SystemAccountID),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementAccountTestOptionsSource{}, false, nil
	}
	if err != nil {
		return port.ManagementAccountTestOptionsSource{}, false, fmt.Errorf(
			"get management account test options source: %w",
			err,
		)
	}

	return port.ManagementAccountTestOptionsSource{
		ID:                        row.ID,
		OwnerSystemAccountID:      row.OwnerSystemAccountID,
		ProviderCode:              row.ProviderCode,
		ProviderProtocolProfileID: row.ProviderProtocolProfileID,
		ProtocolCode:              row.ProtocolCode,
		ProtocolVersion:           row.ProtocolVersion,
		Type:                      row.Type,
		ClientCompatibility:       row.ClientCompatibility,
		HealthCheckModel:          row.HealthCheckModel,
		HealthCheckEndpointMode:   row.HealthCheckEndpointMode,
		CredentialsEncrypted:      row.CredentialsEncrypted,
	}, true, nil
}

var _ port.ManagementAccountTestOptionsReader = (*Store)(nil)
