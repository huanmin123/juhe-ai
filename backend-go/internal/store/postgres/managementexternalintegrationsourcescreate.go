package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const managementExternalIntegrationSourceTokenHashUniqueConstraint = "external_integration_source_tokens_token_hash_key"

func (s *Store) CreateManagementExternalIntegrationSource(
	ctx context.Context,
	input port.ManagementExternalIntegrationSourceCreateInput,
) (port.ManagementExternalIntegrationSourceCreateResult, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementExternalIntegrationSourceCreateResult{}, fmt.Errorf(
			"begin management external integration source create: %w",
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

	result, err := createManagementExternalIntegrationSourceTx(
		ctx,
		s.queries().WithTx(tx),
		input,
	)
	if err != nil {
		return port.ManagementExternalIntegrationSourceCreateResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return port.ManagementExternalIntegrationSourceCreateResult{}, fmt.Errorf(
			"commit management external integration source create: %w",
			err,
		)
	}
	committed = true
	return result, nil
}

type managementExternalIntegrationSourceCreateQueries interface {
	InsertManagementExternalIntegrationSource(
		ctx context.Context,
		arg postgresqueries.InsertManagementExternalIntegrationSourceParams,
	) (postgresqueries.JuheBusinessExternalIntegrationSource, error)
	InsertManagementExternalIntegrationSourceToken(
		ctx context.Context,
		arg postgresqueries.InsertManagementExternalIntegrationSourceTokenParams,
	) (postgresqueries.InsertManagementExternalIntegrationSourceTokenRow, error)
}

func createManagementExternalIntegrationSourceTx(
	ctx context.Context,
	q managementExternalIntegrationSourceCreateQueries,
	input port.ManagementExternalIntegrationSourceCreateInput,
) (port.ManagementExternalIntegrationSourceCreateResult, error) {
	sourceRow, err := q.InsertManagementExternalIntegrationSource(
		ctx,
		postgresqueries.InsertManagementExternalIntegrationSourceParams{
			SourceID:       input.SourceID,
			Name:           input.Name,
			Status:         input.Status,
			ScopesJson:     input.ScopesJSON,
			RateLimitsJson: input.RateLimitsJSON,
			ExpiresAt:      pgTimestamptzPtr(input.ExpiresAt),
			Notes:          pgTextFromStringPtr(input.Notes),
			CreatedAt:      pgTimestamptz(input.CreatedAt),
			UpdatedAt:      pgTimestamptz(input.UpdatedAt),
		},
	)
	if err != nil {
		if managementExternalIntegrationSourceDuplicateNameError(err) {
			return port.ManagementExternalIntegrationSourceCreateResult{},
				port.ErrManagementExternalIntegrationSourceNameExists
		}
		return port.ManagementExternalIntegrationSourceCreateResult{}, fmt.Errorf(
			"insert management external integration source: %w",
			err,
		)
	}

	tokenRow, err := q.InsertManagementExternalIntegrationSourceToken(
		ctx,
		postgresqueries.InsertManagementExternalIntegrationSourceTokenParams{
			TokenID:              input.TokenID,
			SourceID:             input.SourceID,
			TokenName:            input.TokenName,
			TokenHash:            input.TokenHash,
			TokenSecretEncrypted: input.TokenSecretEncrypted,
			TokenPrefix:          input.TokenPrefix,
			TokenSuffix:          input.TokenSuffix,
			TokenStatus:          input.TokenStatus,
			TokenScopesJson:      input.TokenScopesJSON,
			TokenExpiresAt:       pgTimestamptzPtr(input.TokenExpiresAt),
			CreatedAt:            pgTimestamptz(input.CreatedAt),
			UpdatedAt:            pgTimestamptz(input.UpdatedAt),
		},
	)
	if err != nil {
		if managementExternalIntegrationSourceTokenHashExistsError(err) {
			return port.ManagementExternalIntegrationSourceCreateResult{},
				port.ErrManagementExternalIntegrationSourceTokenHashExists
		}
		return port.ManagementExternalIntegrationSourceCreateResult{}, fmt.Errorf(
			"insert management external integration source token: %w",
			err,
		)
	}

	source, err := managementExternalIntegrationSourceRow(sourceRow)
	if err != nil {
		return port.ManagementExternalIntegrationSourceCreateResult{}, err
	}
	token, err := managementExternalIntegrationSourceCreatedTokenRow(tokenRow)
	if err != nil {
		return port.ManagementExternalIntegrationSourceCreateResult{}, err
	}
	return port.ManagementExternalIntegrationSourceCreateResult{Source: source, Token: token}, nil
}

func managementExternalIntegrationSourceCreatedTokenRow(
	row postgresqueries.InsertManagementExternalIntegrationSourceTokenRow,
) (port.ManagementExternalIntegrationSourcePrimaryTokenRow, error) {
	createdAt, err := managementExternalIntegrationSourceRequiredTime(row.CreatedAt, row.ID, "created_at")
	if err != nil {
		return port.ManagementExternalIntegrationSourcePrimaryTokenRow{}, err
	}
	updatedAt, err := managementExternalIntegrationSourceRequiredTime(row.UpdatedAt, row.ID, "updated_at")
	if err != nil {
		return port.ManagementExternalIntegrationSourcePrimaryTokenRow{}, err
	}
	return port.ManagementExternalIntegrationSourcePrimaryTokenRow{
		SourceRefID: row.SourceRefID,
		ID:          row.ID,
		Name:        row.Name,
		TokenPrefix: row.TokenPrefix,
		TokenSuffix: row.TokenSuffix,
		Status:      row.Status,
		ScopesJSON:  row.ScopesJson,
		ExpiresAt:   timestamptzPtr(row.ExpiresAt),
		LastUsedAt:  timestamptzPtr(row.LastUsedAt),
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
		RevokedAt:   timestamptzPtr(row.RevokedAt),
	}, nil
}

func managementExternalIntegrationSourceTokenHashExistsError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == "23505" &&
		pgErr.ConstraintName == managementExternalIntegrationSourceTokenHashUniqueConstraint
}

var _ port.ManagementExternalIntegrationSourceCreator = (*Store)(nil)
