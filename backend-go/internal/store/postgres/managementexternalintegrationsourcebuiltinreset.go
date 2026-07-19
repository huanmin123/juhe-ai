package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

type managementExternalIntegrationSourceBuiltInResetQueries interface {
	LockBuiltInExternalIntegrationSourceForReset(context.Context) (postgresqueries.JuheBusinessExternalIntegrationSource, error)
	LockBuiltInExternalIntegrationSourceTokenForReset(context.Context) (postgresqueries.LockBuiltInExternalIntegrationSourceTokenForResetRow, error)
	ResetBuiltInExternalIntegrationSourceToken(context.Context, postgresqueries.ResetBuiltInExternalIntegrationSourceTokenParams) (int64, error)
	TouchBuiltInExternalIntegrationSource(context.Context, pgtype.Timestamptz) (int64, error)
	ReadBuiltInExternalIntegrationSourceAfterReset(context.Context) (postgresqueries.JuheBusinessExternalIntegrationSource, error)
	ReadBuiltInExternalIntegrationSourceTokenAfterReset(context.Context) (postgresqueries.ReadBuiltInExternalIntegrationSourceTokenAfterResetRow, error)
}

func (s *Store) ResetManagementExternalIntegrationSourceBuiltInToken(
	ctx context.Context,
	input port.ManagementExternalIntegrationSourceBuiltInResetInput,
) (port.ManagementExternalIntegrationSourceBuiltInResetResult, error) {
	return resetManagementExternalIntegrationSourceBuiltInTokenInTx(
		ctx,
		s.pool.BeginTx,
		func(tx pgx.Tx) managementExternalIntegrationSourceBuiltInResetQueries { return s.queries().WithTx(tx) },
		input,
	)
}

func resetManagementExternalIntegrationSourceBuiltInTokenInTx(
	ctx context.Context,
	beginTx func(context.Context, pgx.TxOptions) (pgx.Tx, error),
	queriesForTx func(pgx.Tx) managementExternalIntegrationSourceBuiltInResetQueries,
	input port.ManagementExternalIntegrationSourceBuiltInResetInput,
) (port.ManagementExternalIntegrationSourceBuiltInResetResult, error) {
	tx, err := beginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementExternalIntegrationSourceBuiltInResetResult{}, fmt.Errorf("begin management external integration source built-in reset: %w", err)
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

	result, err := resetManagementExternalIntegrationSourceBuiltInToken(ctx, queriesForTx(tx), input)
	if err != nil {
		return port.ManagementExternalIntegrationSourceBuiltInResetResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return port.ManagementExternalIntegrationSourceBuiltInResetResult{}, fmt.Errorf("commit management external integration source built-in reset: %w", err)
	}
	committed = true
	return result, nil
}

func resetManagementExternalIntegrationSourceBuiltInToken(
	ctx context.Context,
	q managementExternalIntegrationSourceBuiltInResetQueries,
	input port.ManagementExternalIntegrationSourceBuiltInResetInput,
) (port.ManagementExternalIntegrationSourceBuiltInResetResult, error) {
	if _, err := q.LockBuiltInExternalIntegrationSourceForReset(ctx); err != nil {
		return port.ManagementExternalIntegrationSourceBuiltInResetResult{}, mapBuiltInResetMissing("lock source", err)
	}
	lockedToken, err := q.LockBuiltInExternalIntegrationSourceTokenForReset(ctx)
	if err != nil {
		return port.ManagementExternalIntegrationSourceBuiltInResetResult{}, mapBuiltInResetMissing("lock token", err)
	}
	updatedAt := pgTimestamptz(input.UpdatedAt.UTC())
	rows, err := q.ResetBuiltInExternalIntegrationSourceToken(ctx, postgresqueries.ResetBuiltInExternalIntegrationSourceTokenParams{
		TokenHash: input.TokenHash, TokenSecretEncrypted: input.TokenSecretEncrypted,
		TokenPrefix: input.TokenPrefix, TokenSuffix: input.TokenSuffix, UpdatedAt: updatedAt,
	})
	if err != nil {
		if managementExternalIntegrationSourceTokenHashExistsError(err) {
			return port.ManagementExternalIntegrationSourceBuiltInResetResult{}, port.ErrManagementExternalIntegrationSourceTokenHashExists
		}
		return port.ManagementExternalIntegrationSourceBuiltInResetResult{}, fmt.Errorf("reset management external integration source built-in token: %w", err)
	}
	if rows != 1 {
		return port.ManagementExternalIntegrationSourceBuiltInResetResult{}, port.ErrManagementExternalIntegrationSourceBuiltInResetNotFound
	}
	rows, err = q.TouchBuiltInExternalIntegrationSource(ctx, updatedAt)
	if err != nil {
		return port.ManagementExternalIntegrationSourceBuiltInResetResult{}, fmt.Errorf("touch management external integration source built-in source: %w", err)
	}
	if rows != 1 {
		return port.ManagementExternalIntegrationSourceBuiltInResetResult{}, port.ErrManagementExternalIntegrationSourceBuiltInResetNotFound
	}

	sourceRow, err := q.ReadBuiltInExternalIntegrationSourceAfterReset(ctx)
	if err != nil {
		return port.ManagementExternalIntegrationSourceBuiltInResetResult{}, mapBuiltInResetMissing("read source", err)
	}
	tokenRow, err := q.ReadBuiltInExternalIntegrationSourceTokenAfterReset(ctx)
	if err != nil {
		return port.ManagementExternalIntegrationSourceBuiltInResetResult{}, mapBuiltInResetMissing("read token", err)
	}
	source, err := managementExternalIntegrationSourceRow(sourceRow)
	if err != nil {
		return port.ManagementExternalIntegrationSourceBuiltInResetResult{}, fmt.Errorf("map built-in reset source: %w", err)
	}
	token, err := managementExternalIntegrationSourceBuiltInResetTokenRow(tokenRow)
	if err != nil {
		return port.ManagementExternalIntegrationSourceBuiltInResetResult{}, fmt.Errorf("map built-in reset token: %w", err)
	}
	return port.ManagementExternalIntegrationSourceBuiltInResetResult{OldTokenHash: lockedToken.TokenHash, Source: source, Token: token}, nil
}

func mapBuiltInResetMissing(operation string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ErrManagementExternalIntegrationSourceBuiltInResetNotFound
	}
	return fmt.Errorf("%s for management external integration source built-in reset: %w", operation, err)
}

func managementExternalIntegrationSourceBuiltInResetTokenRow(row postgresqueries.ReadBuiltInExternalIntegrationSourceTokenAfterResetRow) (port.ManagementExternalIntegrationSourcePrimaryTokenRow, error) {
	createdAt, err := managementExternalIntegrationSourceRequiredTime(row.CreatedAt, row.ID, "created_at")
	if err != nil {
		return port.ManagementExternalIntegrationSourcePrimaryTokenRow{}, err
	}
	updatedAt, err := managementExternalIntegrationSourceRequiredTime(row.UpdatedAt, row.ID, "updated_at")
	if err != nil {
		return port.ManagementExternalIntegrationSourcePrimaryTokenRow{}, err
	}
	return port.ManagementExternalIntegrationSourcePrimaryTokenRow{
		SourceRefID: row.SourceRefID, ID: row.ID, Name: row.Name,
		TokenPrefix: row.TokenPrefix, TokenSuffix: row.TokenSuffix, Status: row.Status,
		ScopesJSON: row.ScopesJson, ExpiresAt: timestamptzPtr(row.ExpiresAt), LastUsedAt: timestamptzPtr(row.LastUsedAt),
		CreatedAt: createdAt, UpdatedAt: updatedAt, RevokedAt: timestamptzPtr(row.RevokedAt),
	}, nil
}

var _ port.ManagementExternalIntegrationSourceBuiltInResetter = (*Store)(nil)
