package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

type managementAPIKeyDeleteQueries interface {
	LockManagementAPIKeyDeleteTarget(
		ctx context.Context,
		input postgresqueries.LockManagementAPIKeyDeleteTargetParams,
	) (postgresqueries.LockManagementAPIKeyDeleteTargetRow, error)
	HardDeleteManagementAPIKey(
		ctx context.Context,
		input postgresqueries.HardDeleteManagementAPIKeyParams,
	) (string, error)
	UpsertAPIKeyRecordCleanupTarget(
		ctx context.Context,
		input postgresqueries.UpsertAPIKeyRecordCleanupTargetParams,
	) error
}

func (s *Store) DeleteManagementAPIKey(
	ctx context.Context,
	input port.ManagementAPIKeyDeleteInput,
) (port.ManagementAPIKeyDeleteResult, error) {
	return deleteManagementAPIKeyInTx(
		ctx,
		s.pool.BeginTx,
		func(tx pgx.Tx) managementAPIKeyDeleteQueries {
			return s.queries().WithTx(tx)
		},
		input,
	)
}

func deleteManagementAPIKeyInTx(
	ctx context.Context,
	beginTx func(context.Context, pgx.TxOptions) (pgx.Tx, error),
	queriesForTx func(pgx.Tx) managementAPIKeyDeleteQueries,
	input port.ManagementAPIKeyDeleteInput,
) (port.ManagementAPIKeyDeleteResult, error) {
	tx, err := beginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementAPIKeyDeleteResult{},
			fmt.Errorf("begin management API Key delete tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	result, err := deleteManagementAPIKey(ctx, queriesForTx(tx), input)
	if err != nil {
		return port.ManagementAPIKeyDeleteResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return port.ManagementAPIKeyDeleteResult{},
				fmt.Errorf("commit management API Key delete tx rolled back: %w", err)
		}
		return port.ManagementAPIKeyDeleteResult{},
			fmt.Errorf("commit management API Key delete tx: %w", err)
	}
	committed = true
	return result, nil
}

func deleteManagementAPIKey(
	ctx context.Context,
	q managementAPIKeyDeleteQueries,
	input port.ManagementAPIKeyDeleteInput,
) (port.ManagementAPIKeyDeleteResult, error) {
	apiKeyID := strings.TrimSpace(input.APIKeyID)
	ownerSystemAccountID := strings.TrimSpace(input.OwnerSystemAccountID)
	current, err := q.LockManagementAPIKeyDeleteTarget(
		ctx,
		postgresqueries.LockManagementAPIKeyDeleteTargetParams{
			ApiKeyID:             apiKeyID,
			OwnerSystemAccountID: ownerSystemAccountID,
		},
	)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return port.ManagementAPIKeyDeleteResult{}, port.ErrManagementAPIKeyNotFound
	case err != nil:
		return port.ManagementAPIKeyDeleteResult{},
			fmt.Errorf("lock management API Key delete target: %w", err)
	case current.IsDefault:
		return port.ManagementAPIKeyDeleteResult{}, port.ErrManagementAPIKeyDefaultDelete
	case current.Purpose == "chat":
		return port.ManagementAPIKeyDeleteResult{}, port.ErrManagementAPIKeyChatDelete
	}

	deletedID, err := q.HardDeleteManagementAPIKey(
		ctx,
		postgresqueries.HardDeleteManagementAPIKeyParams{
			ApiKeyID:             current.ID,
			OwnerSystemAccountID: current.SystemAccountID,
		},
	)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return port.ManagementAPIKeyDeleteResult{}, port.ErrManagementAPIKeyNotFound
	case err != nil:
		return port.ManagementAPIKeyDeleteResult{},
			fmt.Errorf("hard delete management API Key: %w", err)
	case deletedID != current.ID:
		return port.ManagementAPIKeyDeleteResult{}, fmt.Errorf(
			"hard delete management API Key returned id %q, want %q",
			deletedID,
			current.ID,
		)
	}

	if err := q.UpsertAPIKeyRecordCleanupTarget(
		ctx,
		postgresqueries.UpsertAPIKeyRecordCleanupTargetParams{
			ApiKeyID:        current.ID,
			SystemAccountID: current.SystemAccountID,
			CreatedAt:       pgTimestamptz(input.DeletedAt),
			UpdatedAt:       pgTimestamptz(input.DeletedAt),
		},
	); err != nil {
		return port.ManagementAPIKeyDeleteResult{},
			fmt.Errorf("upsert management API Key record cleanup target: %w", err)
	}

	return port.ManagementAPIKeyDeleteResult{
		APIKeyID:             current.ID,
		Name:                 current.Name,
		OwnerSystemAccountID: current.SystemAccountID,
	}, nil
}

var _ port.ManagementAPIKeyDeleter = (*Store)(nil)
