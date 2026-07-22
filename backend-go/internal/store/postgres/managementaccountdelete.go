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

type managementAccountDeleteQueries interface {
	LockManagementAccountDeleteTarget(context.Context, postgresqueries.LockManagementAccountDeleteTargetParams) (postgresqueries.LockManagementAccountDeleteTargetRow, error)
	ListManagementAccountDeleteInstances(context.Context, string) ([]postgresqueries.ListManagementAccountDeleteInstancesRow, error)
	ListManagementAccountDeleteAuthorizationIDs(context.Context, string) ([]string, error)
	RevokeManagementAccountDeleteGrants(context.Context, postgresqueries.RevokeManagementAccountDeleteGrantsParams) error
	RevokeManagementAccountDeleteSources(context.Context, postgresqueries.RevokeManagementAccountDeleteSourcesParams) error
	RevokeManagementAccountDeleteAuthorizations(context.Context, postgresqueries.RevokeManagementAccountDeleteAuthorizationsParams) error
	LogicallyDeleteManagementAccounts(context.Context, postgresqueries.LogicallyDeleteManagementAccountsParams) ([]string, error)
	DeleteManagementAccountTagBindings(context.Context, []string) error
	DeleteManagementAccountSearchTerms(context.Context, []string) error
	DeleteManagementAccountSearchDocuments(context.Context, []string) error
}

func (s *Store) DeleteManagementAccount(ctx context.Context, input port.ManagementAccountDeleteInput) (port.ManagementAccountDeleteResult, error) {
	return deleteManagementAccountInTx(ctx, s.pool.BeginTx, func(tx pgx.Tx) managementAccountDeleteQueries {
		return s.queries().WithTx(tx)
	}, input)
}

func deleteManagementAccountInTx(
	ctx context.Context,
	beginTx func(context.Context, pgx.TxOptions) (pgx.Tx, error),
	queriesForTx func(pgx.Tx) managementAccountDeleteQueries,
	input port.ManagementAccountDeleteInput,
) (port.ManagementAccountDeleteResult, error) {
	tx, err := beginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementAccountDeleteResult{}, fmt.Errorf("begin management account delete tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	result, err := deleteManagementAccount(ctx, queriesForTx(tx), input)
	if err != nil {
		return port.ManagementAccountDeleteResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return port.ManagementAccountDeleteResult{}, fmt.Errorf("commit management account delete tx: %w", err)
	}
	committed = true
	return result, nil
}

func deleteManagementAccount(
	ctx context.Context,
	q managementAccountDeleteQueries,
	input port.ManagementAccountDeleteInput,
) (port.ManagementAccountDeleteResult, error) {
	accountID := strings.TrimSpace(input.AccountID)
	target, err := q.LockManagementAccountDeleteTarget(ctx, postgresqueries.LockManagementAccountDeleteTargetParams{
		AccountID:                accountID,
		CanAccessAll:             input.CanAccessAll,
		EffectiveSystemAccountID: strings.TrimSpace(input.EffectiveSystemAccountID),
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return port.ManagementAccountDeleteResult{}, port.ErrManagementAccountDeleteNotFound
	case err != nil:
		return port.ManagementAccountDeleteResult{}, fmt.Errorf("lock management account delete target: %w", err)
	case target.AuthorizationInstanceAuthorizationID.Valid && strings.TrimSpace(target.AuthorizationInstanceAuthorizationID.String) != "":
		return port.ManagementAccountDeleteResult{}, port.ErrManagementAccountDeleteAuthorizationInstance
	}

	instances, err := q.ListManagementAccountDeleteInstances(ctx, target.ID)
	if err != nil {
		return port.ManagementAccountDeleteResult{}, fmt.Errorf("list management account delete instances: %w", err)
	}
	authorizationIDs, err := q.ListManagementAccountDeleteAuthorizationIDs(ctx, target.ID)
	if err != nil {
		return port.ManagementAccountDeleteResult{}, fmt.Errorf("list management account delete authorization ids: %w", err)
	}

	deletedAt := pgTimestamptz(input.DeletedAt)
	if err := q.RevokeManagementAccountDeleteGrants(ctx, postgresqueries.RevokeManagementAccountDeleteGrantsParams{
		RevokedBy: strings.TrimSpace(input.DeletedBy),
		RevokedAt: deletedAt,
		UpdatedAt: deletedAt,
		AccountID: target.ID,
	}); err != nil {
		return port.ManagementAccountDeleteResult{}, fmt.Errorf("revoke management account delete grants: %w", err)
	}
	if len(authorizationIDs) > 0 {
		if err := q.RevokeManagementAccountDeleteSources(ctx, postgresqueries.RevokeManagementAccountDeleteSourcesParams{
			EndedAt:          deletedAt,
			RevokedBy:        strings.TrimSpace(input.DeletedBy),
			RevokedAt:        deletedAt,
			UpdatedAt:        deletedAt,
			AuthorizationIds: authorizationIDs,
		}); err != nil {
			return port.ManagementAccountDeleteResult{}, fmt.Errorf("revoke management account delete sources: %w", err)
		}
		if err := q.RevokeManagementAccountDeleteAuthorizations(ctx, postgresqueries.RevokeManagementAccountDeleteAuthorizationsParams{
			RevokedBy:           strings.TrimSpace(input.DeletedBy),
			RevokedAt:           deletedAt,
			LastSourceChangedAt: deletedAt,
			UpdatedAt:           deletedAt,
			AuthorizationIds:    authorizationIDs,
		}); err != nil {
			return port.ManagementAccountDeleteResult{}, fmt.Errorf("revoke management account delete authorizations: %w", err)
		}
	}

	accountIDs := make([]string, 0, len(instances)+1)
	accountIDs = append(accountIDs, target.ID)
	for _, instance := range instances {
		accountIDs = append(accountIDs, instance.ID)
	}
	deletedIDs, err := q.LogicallyDeleteManagementAccounts(ctx, postgresqueries.LogicallyDeleteManagementAccountsParams{
		DeletedAt:  deletedAt,
		DeletedBy:  strings.TrimSpace(input.DeletedBy),
		UpdatedAt:  deletedAt,
		AccountIds: accountIDs,
	})
	if err != nil {
		return port.ManagementAccountDeleteResult{}, fmt.Errorf("logically delete management accounts: %w", err)
	}
	deletedIDs = stableDeletedAccountIDs(accountIDs, deletedIDs)
	if len(deletedIDs) != len(accountIDs) {
		return port.ManagementAccountDeleteResult{}, fmt.Errorf("logically delete management accounts returned %d ids, want %d", len(deletedIDs), len(accountIDs))
	}
	if err := q.DeleteManagementAccountTagBindings(ctx, deletedIDs); err != nil {
		return port.ManagementAccountDeleteResult{}, fmt.Errorf("delete management account tag bindings: %w", err)
	}
	if err := q.DeleteManagementAccountSearchTerms(ctx, deletedIDs); err != nil {
		return port.ManagementAccountDeleteResult{}, fmt.Errorf("delete management account search terms: %w", err)
	}
	if err := q.DeleteManagementAccountSearchDocuments(ctx, deletedIDs); err != nil {
		return port.ManagementAccountDeleteResult{}, fmt.Errorf("delete management account search documents: %w", err)
	}

	return port.ManagementAccountDeleteResult{
		Before: port.ManagementAccountDeleteSummary{
			ID:              target.ID,
			SystemAccountID: target.SystemAccountID,
			Name:            target.Name,
		},
		DeletedAccountIDs: deletedIDs,
	}, nil
}

func stableDeletedAccountIDs(expected, returned []string) []string {
	returnedSet := make(map[string]struct{}, len(returned))
	for _, value := range returned {
		returnedSet[strings.TrimSpace(value)] = struct{}{}
	}
	result := make([]string, 0, len(expected))
	for _, value := range expected {
		value = strings.TrimSpace(value)
		if _, ok := returnedSet[value]; ok {
			result = append(result, value)
		}
	}
	return result
}

var _ port.ManagementAccountDeleter = (*Store)(nil)
