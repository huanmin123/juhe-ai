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

type managementAPIKeySecretQueries interface {
	FindManagementAPIKeySecret(
		ctx context.Context,
		arg postgresqueries.FindManagementAPIKeySecretParams,
	) (postgresqueries.FindManagementAPIKeySecretRow, error)
	LockManagementAPIKeySecretRefreshTarget(
		ctx context.Context,
		arg postgresqueries.LockManagementAPIKeySecretRefreshTargetParams,
	) (postgresqueries.LockManagementAPIKeySecretRefreshTargetRow, error)
	UpdateManagementAPIKeySecret(
		ctx context.Context,
		arg postgresqueries.UpdateManagementAPIKeySecretParams,
	) (string, error)
}

type managementAPIKeySecretTxStore struct {
	queries managementAPIKeySecretQueries
}

func (s *Store) FindManagementAPIKeySecret(
	ctx context.Context,
	input port.ManagementAPIKeySecretScope,
) (port.ManagementAPIKeySecretRow, bool, error) {
	return findManagementAPIKeySecret(ctx, s.queries(), input)
}

func (s *Store) LockManagementAPIKeySecretRefreshTarget(
	ctx context.Context,
	input port.ManagementAPIKeySecretScope,
) (port.ManagementAPIKeyListRow, bool, error) {
	return lockManagementAPIKeySecretRefreshTarget(ctx, s.queries(), input)
}

func (s *Store) UpdateManagementAPIKeySecret(
	ctx context.Context,
	input port.ManagementAPIKeySecretUpdateInput,
) (bool, error) {
	return updateManagementAPIKeySecret(ctx, s.queries(), input)
}

func (s *Store) ManagementAPIKeySecretInTx(
	ctx context.Context,
	fn func(context.Context, port.ManagementAPIKeySecretStore) error,
) error {
	return managementAPIKeySecretInTx(
		ctx,
		s.pool.BeginTx,
		func(tx pgx.Tx) port.ManagementAPIKeySecretStore {
			return managementAPIKeySecretTxStore{queries: s.queries().WithTx(tx)}
		},
		fn,
	)
}

func (s managementAPIKeySecretTxStore) FindManagementAPIKeySecret(
	ctx context.Context,
	input port.ManagementAPIKeySecretScope,
) (port.ManagementAPIKeySecretRow, bool, error) {
	return findManagementAPIKeySecret(ctx, s.queries, input)
}

func (s managementAPIKeySecretTxStore) LockManagementAPIKeySecretRefreshTarget(
	ctx context.Context,
	input port.ManagementAPIKeySecretScope,
) (port.ManagementAPIKeyListRow, bool, error) {
	return lockManagementAPIKeySecretRefreshTarget(ctx, s.queries, input)
}

func (s managementAPIKeySecretTxStore) UpdateManagementAPIKeySecret(
	ctx context.Context,
	input port.ManagementAPIKeySecretUpdateInput,
) (bool, error) {
	return updateManagementAPIKeySecret(ctx, s.queries, input)
}

func findManagementAPIKeySecret(
	ctx context.Context,
	q managementAPIKeySecretQueries,
	input port.ManagementAPIKeySecretScope,
) (port.ManagementAPIKeySecretRow, bool, error) {
	row, err := q.FindManagementAPIKeySecret(ctx, postgresqueries.FindManagementAPIKeySecretParams{
		ApiKeyID:        strings.TrimSpace(input.APIKeyID),
		SystemAccountID: strings.TrimSpace(input.SystemAccountID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementAPIKeySecretRow{}, false, nil
	}
	if err != nil {
		return port.ManagementAPIKeySecretRow{}, false, fmt.Errorf("find management API Key secret: %w", err)
	}
	return port.ManagementAPIKeySecretRow{
		ID:                 row.ID,
		SystemAccountID:    row.SystemAccountID,
		Name:               row.Name,
		KeyPrefix:          row.KeyPrefix,
		KeySuffix:          row.KeySuffix,
		KeySecretEncrypted: textPtr(row.KeySecretEncrypted),
	}, true, nil
}

func lockManagementAPIKeySecretRefreshTarget(
	ctx context.Context,
	q managementAPIKeySecretQueries,
	input port.ManagementAPIKeySecretScope,
) (port.ManagementAPIKeyListRow, bool, error) {
	row, err := q.LockManagementAPIKeySecretRefreshTarget(
		ctx,
		postgresqueries.LockManagementAPIKeySecretRefreshTargetParams{
			ApiKeyID:        strings.TrimSpace(input.APIKeyID),
			SystemAccountID: strings.TrimSpace(input.SystemAccountID),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementAPIKeyListRow{}, false, nil
	}
	if err != nil {
		return port.ManagementAPIKeyListRow{}, false, fmt.Errorf("lock management API Key secret refresh target: %w", err)
	}
	return port.ManagementAPIKeyListRow{
		ID:                       row.ID,
		SystemAccountID:          row.SystemAccountID,
		SystemAccountName:        row.SystemAccountName,
		Name:                     row.Name,
		Description:              textPtr(row.Description),
		KeyPrefix:                row.KeyPrefix,
		KeySuffix:                row.KeySuffix,
		Status:                   row.Status,
		IsDefault:                row.IsDefault,
		RouteStrategyID:          row.RouteStrategyID,
		RouteStrategyName:        row.RouteStrategyName,
		RouteStrategyMode:        row.RouteStrategyMode,
		RouteStrategyStatus:      row.RouteStrategyStatus,
		ExpiresAt:                timestamptzPtr(row.ExpiresAt),
		QuotaLimitsJSON:          textPtr(row.QuotaLimitsJson),
		AvailabilityScheduleJSON: textPtr(row.AvailabilityScheduleJson),
	}, true, nil
}

func updateManagementAPIKeySecret(
	ctx context.Context,
	q managementAPIKeySecretQueries,
	input port.ManagementAPIKeySecretUpdateInput,
) (bool, error) {
	_, err := q.UpdateManagementAPIKeySecret(ctx, postgresqueries.UpdateManagementAPIKeySecretParams{
		KeyHash:            input.KeyHash,
		KeyPrefix:          input.KeyPrefix,
		KeySuffix:          input.KeySuffix,
		KeySecretEncrypted: input.KeySecretEncrypted,
		UpdatedAt:          pgTimestamptz(input.UpdatedAt),
		ApiKeyID:           strings.TrimSpace(input.APIKeyID),
		SystemAccountID:    strings.TrimSpace(input.SystemAccountID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("update management API Key secret: %w", err)
	}
	return true, nil
}

func managementAPIKeySecretInTx(
	ctx context.Context,
	beginTx func(context.Context, pgx.TxOptions) (pgx.Tx, error),
	storeForTx func(pgx.Tx) port.ManagementAPIKeySecretStore,
	fn func(context.Context, port.ManagementAPIKeySecretStore) error,
) error {
	tx, err := beginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin management API Key secret tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	if err := fn(ctx, storeForTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return fmt.Errorf("commit management API Key secret tx rolled back: %w", err)
		}
		return fmt.Errorf("commit management API Key secret tx: %w", err)
	}
	committed = true
	return nil
}

var _ port.ManagementAPIKeySecretStore = (*Store)(nil)
var _ port.ManagementAPIKeySecretStore = managementAPIKeySecretTxStore{}
var _ port.ManagementAPIKeySecretTransactor = (*Store)(nil)
