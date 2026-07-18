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

const builtInManagementExternalIntegrationSourceID = "extsrc_builtin_test"

func (s *Store) UpdateManagementExternalIntegrationSource(
	ctx context.Context,
	input port.ManagementExternalIntegrationSourceUpdateInput,
	validate func(port.ManagementExternalIntegrationSourceUpdateResult) error,
) (port.ManagementExternalIntegrationSourceUpdateResult, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementExternalIntegrationSourceUpdateResult{}, fmt.Errorf("begin management external integration source update: %w", err)
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

	result, err := updateManagementExternalIntegrationSourceTx(
		ctx,
		s.queries().WithTx(tx),
		input,
		validate,
	)
	if err != nil {
		return port.ManagementExternalIntegrationSourceUpdateResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return port.ManagementExternalIntegrationSourceUpdateResult{}, fmt.Errorf("commit management external integration source update: %w", err)
	}
	committed = true
	return result, nil
}

type managementExternalIntegrationSourceUpdateQueries interface {
	managementExternalIntegrationSourceDetailQueries
	FindManagementExternalIntegrationSourceForUpdate(
		ctx context.Context,
		sourceID string,
	) (postgresqueries.JuheBusinessExternalIntegrationSource, error)
	UpdateManagementExternalIntegrationSource(
		ctx context.Context,
		arg postgresqueries.UpdateManagementExternalIntegrationSourceParams,
	) (postgresqueries.JuheBusinessExternalIntegrationSource, error)
	SyncManagementExternalIntegrationSourceTokens(
		ctx context.Context,
		arg postgresqueries.SyncManagementExternalIntegrationSourceTokensParams,
	) (int64, error)
}

func updateManagementExternalIntegrationSourceTx(
	ctx context.Context,
	q managementExternalIntegrationSourceUpdateQueries,
	input port.ManagementExternalIntegrationSourceUpdateInput,
	validate func(port.ManagementExternalIntegrationSourceUpdateResult) error,
) (port.ManagementExternalIntegrationSourceUpdateResult, error) {
	current, err := q.FindManagementExternalIntegrationSourceForUpdate(ctx, input.SourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementExternalIntegrationSourceUpdateResult{}, port.ErrManagementExternalIntegrationSourceNotFound
	}
	if err != nil {
		return port.ManagementExternalIntegrationSourceUpdateResult{}, fmt.Errorf("lock management external integration source: %w", err)
	}
	if input.SourceID == builtInManagementExternalIntegrationSourceID &&
		(input.HasName || input.HasScopes || input.HasRateLimits || input.HasExpiresAt || input.HasNotes) {
		return port.ManagementExternalIntegrationSourceUpdateResult{}, port.ErrManagementExternalIntegrationSourceBuiltInUpdateRestricted
	}

	beforeSource, err := managementExternalIntegrationSourceRow(current)
	if err != nil {
		return port.ManagementExternalIntegrationSourceUpdateResult{}, err
	}
	beforeTokens, err := listManagementExternalIntegrationSourceTokens(ctx, q, input.SourceID)
	if err != nil {
		return port.ManagementExternalIntegrationSourceUpdateResult{}, err
	}

	name := current.Name
	if input.HasName {
		name = input.Name
	}
	status := current.Status
	if input.HasStatus {
		status = input.Status
	}
	scopesJSON := current.ScopesJson
	if input.HasScopes {
		scopesJSON = input.ScopesJSON
	}
	rateLimitsJSON := current.RateLimitsJson
	if input.HasRateLimits {
		rateLimitsJSON = input.RateLimitsJSON
	}
	expiresAt := timestamptzPtr(current.ExpiresAt)
	if input.HasExpiresAt {
		expiresAt = input.ExpiresAt
	}
	notes := textPtr(current.Notes)
	if input.HasNotes {
		notes = input.Notes
	}
	updatedAt := input.UpdatedAt.UTC()

	updated, err := q.UpdateManagementExternalIntegrationSource(
		ctx,
		postgresqueries.UpdateManagementExternalIntegrationSourceParams{
			Name:           name,
			Status:         status,
			ScopesJson:     scopesJSON,
			RateLimitsJson: rateLimitsJSON,
			ExpiresAt:      pgTimestamptzPtr(expiresAt),
			Notes:          pgTextFromStringPtr(notes),
			UpdatedAt:      pgTimestamptz(updatedAt),
			SourceID:       input.SourceID,
		},
	)
	if err != nil {
		if managementExternalIntegrationSourceDuplicateNameError(err) {
			return port.ManagementExternalIntegrationSourceUpdateResult{}, port.ErrManagementExternalIntegrationSourceNameExists
		}
		return port.ManagementExternalIntegrationSourceUpdateResult{}, fmt.Errorf("update management external integration source: %w", err)
	}

	if input.SourceID != builtInManagementExternalIntegrationSourceID {
		if _, err := q.SyncManagementExternalIntegrationSourceTokens(
			ctx,
			postgresqueries.SyncManagementExternalIntegrationSourceTokensParams{
				TokenName:    name + " 生产 Token",
				SourceStatus: status,
				ScopesJson:   scopesJSON,
				ExpiresAt:    pgTimestamptzPtr(expiresAt),
				UpdatedAt:    pgTimestamptz(updatedAt),
				SourceID:     input.SourceID,
			},
		); err != nil {
			return port.ManagementExternalIntegrationSourceUpdateResult{}, fmt.Errorf("sync management external integration source tokens: %w", err)
		}
	}

	afterSource, err := managementExternalIntegrationSourceRow(updated)
	if err != nil {
		return port.ManagementExternalIntegrationSourceUpdateResult{}, err
	}
	afterTokens, err := listManagementExternalIntegrationSourceTokens(ctx, q, input.SourceID)
	if err != nil {
		return port.ManagementExternalIntegrationSourceUpdateResult{}, err
	}
	result := port.ManagementExternalIntegrationSourceUpdateResult{
		BeforeSource: beforeSource,
		BeforeTokens: beforeTokens,
		AfterSource:  afterSource,
		AfterTokens:  afterTokens,
	}
	if validate != nil {
		if err := validate(result); err != nil {
			return port.ManagementExternalIntegrationSourceUpdateResult{}, err
		}
	}
	return result, nil
}

func managementExternalIntegrationSourceDuplicateNameError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == "23505" &&
		pgErr.ConstraintName == "idx_external_integration_sources_name_unique_lower"
}

var _ port.ManagementExternalIntegrationSourceUpdater = (*Store)(nil)
