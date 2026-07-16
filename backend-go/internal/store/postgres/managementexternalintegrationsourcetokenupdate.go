package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/modules/publicapi"
	"juhe-ai/backend-go/internal/store/port"
)

const managementExternalIntegrationSourceTokenUpdateSourceLockSQL = `
SELECT sources.id
FROM juhe_business.external_integration_sources AS sources
WHERE sources.id = $1::text
FOR UPDATE
`

const managementExternalIntegrationSourceTokenUpdateTokenLockSQL = `
SELECT
  tokens.source_ref_id,
  tokens.id,
  tokens.name,
  tokens.token_prefix,
  tokens.token_suffix,
  tokens.status,
  tokens.scopes_json,
  tokens.expires_at,
  tokens.last_used_at,
  tokens.created_at,
  tokens.updated_at,
  tokens.revoked_at
FROM juhe_business.external_integration_source_tokens AS tokens
WHERE tokens.source_ref_id = $1::text
  AND tokens.id = $2::text
FOR UPDATE
`

const managementExternalIntegrationSourceTokenUpdateSQL = `
UPDATE juhe_business.external_integration_source_tokens
SET
  name = $1::text,
  status = $2::text,
  scopes_json = $3::text,
  expires_at = $4::timestamptz,
  updated_at = $5::timestamptz,
  revoked_at = $6::timestamptz
WHERE source_ref_id = $7::text
  AND id = $8::text
RETURNING
  source_ref_id,
  id,
  name,
  token_prefix,
  token_suffix,
  status,
  scopes_json,
  expires_at,
  last_used_at,
  created_at,
  updated_at,
  revoked_at
`

type managementExternalIntegrationSourceTokenUpdateQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type managementExternalIntegrationSourceTokenUpdateRecord struct {
	SourceRefID string
	ID          string
	Name        string
	TokenPrefix string
	TokenSuffix string
	Status      string
	ScopesJSON  string
	ExpiresAt   pgtype.Timestamptz
	LastUsedAt  pgtype.Timestamptz
	CreatedAt   pgtype.Timestamptz
	UpdatedAt   pgtype.Timestamptz
	RevokedAt   pgtype.Timestamptz
}

func (s *Store) UpdateManagementExternalIntegrationSourceToken(
	ctx context.Context,
	input port.ManagementExternalIntegrationSourceTokenUpdateInput,
	validate func(port.ManagementExternalIntegrationSourceTokenUpdateResult) error,
) (port.ManagementExternalIntegrationSourceTokenUpdateResult, error) {
	return updateManagementExternalIntegrationSourceTokenInTx(
		ctx,
		s.pool.BeginTx,
		input,
		validate,
	)
}

func updateManagementExternalIntegrationSourceTokenInTx(
	ctx context.Context,
	beginTx func(context.Context, pgx.TxOptions) (pgx.Tx, error),
	input port.ManagementExternalIntegrationSourceTokenUpdateInput,
	validate func(port.ManagementExternalIntegrationSourceTokenUpdateResult) error,
) (port.ManagementExternalIntegrationSourceTokenUpdateResult, error) {
	tx, err := beginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementExternalIntegrationSourceTokenUpdateResult{}, fmt.Errorf(
			"begin management external integration source token update: %w",
			err,
		)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()

	result, err := updateManagementExternalIntegrationSourceTokenTx(ctx, tx, input, validate)
	if err != nil {
		return port.ManagementExternalIntegrationSourceTokenUpdateResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return port.ManagementExternalIntegrationSourceTokenUpdateResult{}, fmt.Errorf(
			"commit management external integration source token update: %w",
			err,
		)
	}
	committed = true
	return result, nil
}

func updateManagementExternalIntegrationSourceTokenTx(
	ctx context.Context,
	q managementExternalIntegrationSourceTokenUpdateQuerier,
	input port.ManagementExternalIntegrationSourceTokenUpdateInput,
	validate func(port.ManagementExternalIntegrationSourceTokenUpdateResult) error,
) (port.ManagementExternalIntegrationSourceTokenUpdateResult, error) {
	var sourceID string
	err := q.QueryRow(
		ctx,
		managementExternalIntegrationSourceTokenUpdateSourceLockSQL,
		input.SourceID,
	).Scan(&sourceID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return port.ManagementExternalIntegrationSourceTokenUpdateResult{},
			port.ErrManagementExternalIntegrationSourceNotFound
	case err != nil:
		return port.ManagementExternalIntegrationSourceTokenUpdateResult{}, fmt.Errorf(
			"lock management external integration source token update source: %w",
			err,
		)
	case sourceID == publicapi.BuiltInTestSourceID:
		return port.ManagementExternalIntegrationSourceTokenUpdateResult{},
			port.ErrManagementExternalIntegrationSourceBuiltInTokenUpdateRestricted
	}

	current, err := scanManagementExternalIntegrationSourceTokenUpdateRecord(
		q.QueryRow(
			ctx,
			managementExternalIntegrationSourceTokenUpdateTokenLockSQL,
			input.SourceID,
			input.TokenID,
		),
	)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return port.ManagementExternalIntegrationSourceTokenUpdateResult{},
			port.ErrManagementExternalIntegrationSourceTokenNotFound
	case err != nil:
		return port.ManagementExternalIntegrationSourceTokenUpdateResult{}, fmt.Errorf(
			"lock management external integration source token update token: %w",
			err,
		)
	case current.ID == publicapi.BuiltInTestTokenID:
		return port.ManagementExternalIntegrationSourceTokenUpdateResult{},
			port.ErrManagementExternalIntegrationSourceBuiltInTokenUpdateRestricted
	}

	beforeToken, err := managementExternalIntegrationSourceTokenUpdatePortRow(current)
	if err != nil {
		return port.ManagementExternalIntegrationSourceTokenUpdateResult{}, fmt.Errorf(
			"map management external integration source token update before token: %w",
			err,
		)
	}

	name := current.Name
	if input.HasName {
		name = input.Name
	}
	status := current.Status
	if input.HasStatus {
		status = input.Status
	}
	scopesJSON := current.ScopesJSON
	if input.HasScopes {
		scopesJSON = input.ScopesJSON
	}
	expiresAt := timestamptzPtr(current.ExpiresAt)
	if input.HasExpiresAt {
		expiresAt = input.ExpiresAt
	}
	updatedAt := input.UpdatedAt.UTC()
	revokedAt := managementExternalIntegrationSourceTokenUpdateRevokedAt(
		current.Status,
		timestamptzPtr(current.RevokedAt),
		input.HasStatus,
		status,
		updatedAt,
	)

	updated, err := scanManagementExternalIntegrationSourceTokenUpdateRecord(
		q.QueryRow(
			ctx,
			managementExternalIntegrationSourceTokenUpdateSQL,
			name,
			status,
			scopesJSON,
			pgTimestamptzPtr(expiresAt),
			pgTimestamptz(updatedAt),
			pgTimestamptzPtr(revokedAt),
			input.SourceID,
			input.TokenID,
		),
	)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return port.ManagementExternalIntegrationSourceTokenUpdateResult{},
			port.ErrManagementExternalIntegrationSourceTokenNotFound
	case err != nil:
		return port.ManagementExternalIntegrationSourceTokenUpdateResult{}, fmt.Errorf(
			"update management external integration source token: %w",
			err,
		)
	}

	afterToken, err := managementExternalIntegrationSourceTokenUpdatePortRow(updated)
	if err != nil {
		return port.ManagementExternalIntegrationSourceTokenUpdateResult{}, fmt.Errorf(
			"map management external integration source token update after token: %w",
			err,
		)
	}
	result := port.ManagementExternalIntegrationSourceTokenUpdateResult{
		BeforeToken: beforeToken,
		AfterToken:  afterToken,
	}
	if validate == nil {
		return port.ManagementExternalIntegrationSourceTokenUpdateResult{}, errors.New(
			"validate management external integration source token update is required",
		)
	}
	if err := validate(result); err != nil {
		return port.ManagementExternalIntegrationSourceTokenUpdateResult{}, err
	}
	return result, nil
}

func scanManagementExternalIntegrationSourceTokenUpdateRecord(
	row pgx.Row,
) (managementExternalIntegrationSourceTokenUpdateRecord, error) {
	var record managementExternalIntegrationSourceTokenUpdateRecord
	err := row.Scan(
		&record.SourceRefID,
		&record.ID,
		&record.Name,
		&record.TokenPrefix,
		&record.TokenSuffix,
		&record.Status,
		&record.ScopesJSON,
		&record.ExpiresAt,
		&record.LastUsedAt,
		&record.CreatedAt,
		&record.UpdatedAt,
		&record.RevokedAt,
	)
	return record, err
}

func managementExternalIntegrationSourceTokenUpdatePortRow(
	record managementExternalIntegrationSourceTokenUpdateRecord,
) (port.ManagementExternalIntegrationSourcePrimaryTokenRow, error) {
	createdAt, err := managementExternalIntegrationSourceRequiredTime(record.CreatedAt, record.ID, "created_at")
	if err != nil {
		return port.ManagementExternalIntegrationSourcePrimaryTokenRow{}, err
	}
	updatedAt, err := managementExternalIntegrationSourceRequiredTime(record.UpdatedAt, record.ID, "updated_at")
	if err != nil {
		return port.ManagementExternalIntegrationSourcePrimaryTokenRow{}, err
	}
	return port.ManagementExternalIntegrationSourcePrimaryTokenRow{
		SourceRefID: record.SourceRefID,
		ID:          record.ID,
		Name:        record.Name,
		TokenPrefix: record.TokenPrefix,
		TokenSuffix: record.TokenSuffix,
		Status:      record.Status,
		ScopesJSON:  record.ScopesJSON,
		ExpiresAt:   timestamptzPtr(record.ExpiresAt),
		LastUsedAt:  timestamptzPtr(record.LastUsedAt),
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
		RevokedAt:   timestamptzPtr(record.RevokedAt),
	}, nil
}

func managementExternalIntegrationSourceTokenUpdateRevokedAt(
	currentStatus string,
	currentRevokedAt *time.Time,
	hasStatus bool,
	status string,
	updatedAt time.Time,
) *time.Time {
	if hasStatus {
		switch status {
		case publicapi.TokenStatusRevoked:
			if currentStatus == publicapi.TokenStatusRevoked {
				return currentRevokedAt
			}
			value := updatedAt.UTC()
			return &value
		case publicapi.TokenStatusActive, publicapi.TokenStatusDisabled:
			return nil
		}
	}
	if currentStatus == publicapi.TokenStatusRevoked {
		return currentRevokedAt
	}
	return nil
}

var _ port.ManagementExternalIntegrationSourceTokenUpdater = (*Store)(nil)
