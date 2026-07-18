package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/modules/publicapi"
	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func (s *Store) DeleteManagementExternalIntegrationSource(
	ctx context.Context,
	sourceID string,
) (port.ManagementExternalIntegrationSourceDeleteResult, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementExternalIntegrationSourceDeleteResult{}, fmt.Errorf(
			"begin management external integration source delete: %w",
			err,
		)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()

	result, err := deleteManagementExternalIntegrationSourceTx(
		ctx,
		s.queries().WithTx(tx),
		sourceID,
	)
	if err != nil {
		return port.ManagementExternalIntegrationSourceDeleteResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return port.ManagementExternalIntegrationSourceDeleteResult{}, fmt.Errorf(
			"commit management external integration source delete: %w",
			err,
		)
	}
	committed = true
	return result, nil
}

type managementExternalIntegrationSourceDeleteQueries interface {
	FindManagementExternalIntegrationSourceForUpdate(
		ctx context.Context,
		sourceID string,
	) (postgresqueries.JuheBusinessExternalIntegrationSource, error)
	CountManagementExternalIntegrationSourceTokensForDelete(
		ctx context.Context,
		sourceID string,
	) (int64, error)
	DeleteManagementExternalIntegrationSource(
		ctx context.Context,
		sourceID string,
	) (string, error)
}

func deleteManagementExternalIntegrationSourceTx(
	ctx context.Context,
	q managementExternalIntegrationSourceDeleteQueries,
	sourceID string,
) (port.ManagementExternalIntegrationSourceDeleteResult, error) {
	current, err := q.FindManagementExternalIntegrationSourceForUpdate(ctx, sourceID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return port.ManagementExternalIntegrationSourceDeleteResult{},
			port.ErrManagementExternalIntegrationSourceNotFound
	case err != nil:
		return port.ManagementExternalIntegrationSourceDeleteResult{}, fmt.Errorf(
			"lock management external integration source delete target: %w",
			err,
		)
	case current.ID == publicapi.BuiltInTestSourceID:
		return port.ManagementExternalIntegrationSourceDeleteResult{},
			port.ErrManagementExternalIntegrationSourceBuiltInDeleteRestricted
	}

	tokenCount, err := q.CountManagementExternalIntegrationSourceTokensForDelete(ctx, sourceID)
	if err != nil {
		return port.ManagementExternalIntegrationSourceDeleteResult{}, fmt.Errorf(
			"count management external integration source tokens: %w",
			err,
		)
	}
	deletedID, err := q.DeleteManagementExternalIntegrationSource(ctx, sourceID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return port.ManagementExternalIntegrationSourceDeleteResult{},
			port.ErrManagementExternalIntegrationSourceNotFound
	case err != nil:
		return port.ManagementExternalIntegrationSourceDeleteResult{}, fmt.Errorf(
			"delete management external integration source: %w",
			err,
		)
	}
	return port.ManagementExternalIntegrationSourceDeleteResult{
		SourceID:   deletedID,
		SourceName: current.Name,
		TokenCount: tokenCount,
	}, nil
}

var _ port.ManagementExternalIntegrationSourceDeleter = (*Store)(nil)
