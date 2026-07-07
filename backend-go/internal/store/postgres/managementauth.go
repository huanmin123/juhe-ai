package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func (s *Store) FindManagementSessionByTokenHash(ctx context.Context, tokenHash string) (port.ManagementSessionAccount, bool, error) {
	row, err := s.queries().FindManagementSessionByTokenHash(ctx, tokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementSessionAccount{}, false, nil
	}
	if err != nil {
		return port.ManagementSessionAccount{}, false, fmt.Errorf("find management session by token hash: %w", err)
	}
	session, err := managementSessionFromRow(row)
	if err != nil {
		return port.ManagementSessionAccount{}, false, err
	}
	return session, true, nil
}

func managementSessionFromRow(row postgresqueries.FindManagementSessionByTokenHashRow) (port.ManagementSessionAccount, error) {
	if !row.ExpiresAt.Valid {
		return port.ManagementSessionAccount{}, fmt.Errorf("management session expires_at is null")
	}
	if !row.LastSeenAt.Valid {
		return port.ManagementSessionAccount{}, fmt.Errorf("management session last_seen_at is null")
	}
	return port.ManagementSessionAccount{
		SessionID:          row.ID,
		TokenHash:          row.TokenHash,
		ExpiresAt:          row.ExpiresAt.Time.UTC(),
		LastSeenAt:         row.LastSeenAt.Time.UTC(),
		AccountID:          row.AccountID,
		Username:           row.Username,
		DisplayName:        row.DisplayName,
		Role:               row.Role,
		Status:             row.Status,
		MustChangePassword: row.MustChangePassword,
	}, nil
}

var _ port.ManagementSessionReader = (*Store)(nil)
