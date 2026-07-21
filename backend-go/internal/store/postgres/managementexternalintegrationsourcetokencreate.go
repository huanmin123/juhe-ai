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

type managementExternalIntegrationSourceTokenCreateQueries interface {
	FindManagementExternalIntegrationSourceForUpdate(
		ctx context.Context,
		sourceID string,
	) (postgresqueries.JuheBusinessExternalIntegrationSource, error)
	InsertManagementExternalIntegrationSourceToken(
		ctx context.Context,
		arg postgresqueries.InsertManagementExternalIntegrationSourceTokenParams,
	) (postgresqueries.InsertManagementExternalIntegrationSourceTokenRow, error)
}

func (s *Store) CreateManagementExternalIntegrationSourceToken(
	ctx context.Context,
	input port.ManagementExternalIntegrationSourceTokenCreateInput,
) (port.ManagementExternalIntegrationSourceTokenCreateResult, error) {
	return createManagementExternalIntegrationSourceTokenInTx(
		ctx,
		s.pool.BeginTx,
		func(tx pgx.Tx) managementExternalIntegrationSourceTokenCreateQueries {
			return s.queries().WithTx(tx)
		},
		input,
	)
}

func createManagementExternalIntegrationSourceTokenInTx(
	ctx context.Context,
	beginTx func(context.Context, pgx.TxOptions) (pgx.Tx, error),
	queriesForTx func(pgx.Tx) managementExternalIntegrationSourceTokenCreateQueries,
	input port.ManagementExternalIntegrationSourceTokenCreateInput,
) (port.ManagementExternalIntegrationSourceTokenCreateResult, error) {
	tx, err := beginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementExternalIntegrationSourceTokenCreateResult{}, fmt.Errorf(
			"begin management external integration source token create: %w",
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

	result, err := createManagementExternalIntegrationSourceToken(ctx, queriesForTx(tx), input)
	if err != nil {
		return port.ManagementExternalIntegrationSourceTokenCreateResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return port.ManagementExternalIntegrationSourceTokenCreateResult{}, fmt.Errorf(
			"commit management external integration source token create: %w",
			err,
		)
	}
	committed = true
	return result, nil
}

func createManagementExternalIntegrationSourceToken(
	ctx context.Context,
	q managementExternalIntegrationSourceTokenCreateQueries,
	input port.ManagementExternalIntegrationSourceTokenCreateInput,
) (port.ManagementExternalIntegrationSourceTokenCreateResult, error) {
	current, err := q.FindManagementExternalIntegrationSourceForUpdate(ctx, input.SourceID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return port.ManagementExternalIntegrationSourceTokenCreateResult{},
			port.ErrManagementExternalIntegrationSourceNotFound
	case err != nil:
		return port.ManagementExternalIntegrationSourceTokenCreateResult{}, fmt.Errorf(
			"lock management external integration source token create target: %w",
			err,
		)
	case current.ID == publicapi.BuiltInTestSourceID:
		return port.ManagementExternalIntegrationSourceTokenCreateResult{},
			port.ErrManagementExternalIntegrationSourceBuiltInTokenCreateRestricted
	}

	createdRow, err := q.InsertManagementExternalIntegrationSourceToken(
		ctx,
		postgresqueries.InsertManagementExternalIntegrationSourceTokenParams{
			TokenID:              input.TokenID,
			SourceID:             input.SourceID,
			TokenName:            input.Name,
			TokenHash:            input.TokenHash,
			TokenSecretEncrypted: input.TokenSecretEncrypted,
			TokenPrefix:          input.TokenPrefix,
			TokenSuffix:          input.TokenSuffix,
			TokenStatus:          input.Status,
			TokenScopesJson:      input.ScopesJSON,
			TokenExpiresAt:       pgTimestamptzPtr(input.ExpiresAt),
			CreatedAt:            pgTimestamptz(input.CreatedAt),
			UpdatedAt:            pgTimestamptz(input.UpdatedAt),
		},
	)
	if err != nil {
		if managementExternalIntegrationSourceTokenHashExistsError(err) {
			return port.ManagementExternalIntegrationSourceTokenCreateResult{},
				port.ErrManagementExternalIntegrationSourceTokenHashExists
		}
		return port.ManagementExternalIntegrationSourceTokenCreateResult{}, fmt.Errorf(
			"insert management external integration source token: %w",
			err,
		)
	}

	source, err := managementExternalIntegrationSourceRow(current)
	if err != nil {
		return port.ManagementExternalIntegrationSourceTokenCreateResult{}, err
	}
	createdAt, err := managementExternalIntegrationSourceRequiredTime(createdRow.CreatedAt, createdRow.ID, "created_at")
	if err != nil {
		return port.ManagementExternalIntegrationSourceTokenCreateResult{}, err
	}
	updatedAt, err := managementExternalIntegrationSourceRequiredTime(createdRow.UpdatedAt, createdRow.ID, "updated_at")
	if err != nil {
		return port.ManagementExternalIntegrationSourceTokenCreateResult{}, err
	}
	createdToken := port.ManagementExternalIntegrationSourcePrimaryTokenRow{
		SourceRefID: createdRow.SourceRefID, ID: createdRow.ID, Name: createdRow.Name,
		TokenPrefix: createdRow.TokenPrefix, TokenSuffix: createdRow.TokenSuffix, Status: createdRow.Status,
		ScopesJSON: createdRow.ScopesJson, ExpiresAt: timestamptzPtr(createdRow.ExpiresAt),
		LastUsedAt: timestamptzPtr(createdRow.LastUsedAt), CreatedAt: createdAt, UpdatedAt: updatedAt,
		RevokedAt: timestamptzPtr(createdRow.RevokedAt),
	}

	return port.ManagementExternalIntegrationSourceTokenCreateResult{
		Source:         source,
		Tokens:         []port.ManagementExternalIntegrationSourcePrimaryTokenRow{createdToken},
		CreatedTokenID: input.TokenID,
	}, nil
}

var _ port.ManagementExternalIntegrationSourceTokenCreator = (*Store)(nil)
