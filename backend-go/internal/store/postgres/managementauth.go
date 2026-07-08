package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

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

func (s *Store) RevokeManagementSessionByTokenHash(ctx context.Context, tokenHash string) error {
	if err := s.queries().RevokeManagementSessionByTokenHash(ctx, tokenHash); err != nil {
		return fmt.Errorf("revoke management session by token hash: %w", err)
	}
	return nil
}

func (s *Store) TouchManagementSession(ctx context.Context, input port.ManagementSessionTouchInput) error {
	if err := s.queries().TouchManagementSession(ctx, postgresqueries.TouchManagementSessionParams{
		SessionID:  input.SessionID,
		LastSeenAt: pgtype.Timestamptz{Time: input.TouchedAt.UTC(), Valid: true},
		Cutoff:     pgtype.Timestamptz{Time: input.Cutoff.UTC(), Valid: true},
	}); err != nil {
		return fmt.Errorf("touch management session: %w", err)
	}
	return nil
}

func (s *Store) FindManagementSystemAccountPasswordByUsername(ctx context.Context, username string) (port.ManagementSystemAccountPasswordCredential, bool, error) {
	row, err := s.queries().FindManagementSystemAccountPasswordByUsername(ctx, username)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementSystemAccountPasswordCredential{}, false, nil
	}
	if err != nil {
		return port.ManagementSystemAccountPasswordCredential{}, false, fmt.Errorf("find management system account password by username: %w", err)
	}
	return port.ManagementSystemAccountPasswordCredential{
		ID:           row.ID,
		Username:     row.Username,
		Status:       row.Status,
		PasswordHash: row.PasswordHash,
	}, true, nil
}

func (s *Store) UpdateManagementCurrentUserProfile(ctx context.Context, input port.ManagementCurrentUserProfileUpdateInput) (port.ManagementCurrentUserProfileUpdateResult, bool, error) {
	row, err := s.queries().UpdateManagementCurrentUserProfile(ctx, postgresqueries.UpdateManagementCurrentUserProfileParams{
		SystemAccountID: input.SystemAccountID,
		DisplayName:     input.DisplayName,
		UpdatedAt:       pgtype.Timestamptz{Time: input.UpdatedAt.UTC(), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementCurrentUserProfileUpdateResult{}, false, nil
	}
	if isManagementProfileDisplayNameUniqueViolation(err) {
		return port.ManagementCurrentUserProfileUpdateResult{}, false, port.ErrManagementProfileDisplayNameExists
	}
	if err != nil {
		return port.ManagementCurrentUserProfileUpdateResult{}, false, fmt.Errorf("update management current user profile: %w", err)
	}
	return managementProfileUpdateResultFromRow(row), true, nil
}

func (s *Store) UpdateManagementCurrentUserPassword(ctx context.Context, input port.ManagementCurrentUserPasswordUpdateInput) (port.ManagementSystemAccountSummary, bool, error) {
	row, err := s.queries().UpdateManagementCurrentUserPassword(ctx, postgresqueries.UpdateManagementCurrentUserPasswordParams{
		SystemAccountID: input.SystemAccountID,
		PasswordHash:    input.PasswordHash,
		UpdatedAt:       pgtype.Timestamptz{Time: input.UpdatedAt.UTC(), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementSystemAccountSummary{}, false, nil
	}
	if err != nil {
		return port.ManagementSystemAccountSummary{}, false, fmt.Errorf("update management current user password: %w", err)
	}
	return managementPasswordAccountFromRow(row), true, nil
}

func (s *Store) RevokeOtherManagementSessionsForAccount(ctx context.Context, systemAccountID string, keepSessionID string) error {
	if err := s.queries().RevokeOtherManagementSessionsForAccount(ctx, postgresqueries.RevokeOtherManagementSessionsForAccountParams{
		SystemAccountID: systemAccountID,
		ID:              keepSessionID,
	}); err != nil {
		return fmt.Errorf("revoke other management sessions for account: %w", err)
	}
	return nil
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

func managementProfileUpdateResultFromRow(row postgresqueries.UpdateManagementCurrentUserProfileRow) port.ManagementCurrentUserProfileUpdateResult {
	before := port.ManagementCurrentUserProfile{
		ID:                 row.ID,
		Username:           row.Username,
		DisplayName:        row.PreviousDisplayName,
		Role:               row.Role,
		MustChangePassword: row.MustChangePassword,
	}
	account := port.ManagementCurrentUserProfile{
		ID:                 row.ID,
		Username:           row.Username,
		DisplayName:        row.DisplayName,
		Role:               row.Role,
		MustChangePassword: row.MustChangePassword,
	}
	return port.ManagementCurrentUserProfileUpdateResult{
		Before:  before,
		Account: account,
	}
}

func managementPasswordAccountFromRow(row postgresqueries.UpdateManagementCurrentUserPasswordRow) port.ManagementSystemAccountSummary {
	return port.ManagementSystemAccountSummary{
		ID:                     row.ID,
		Username:               row.Username,
		DisplayName:            row.DisplayName,
		Description:            textValue(row.Description),
		Role:                   row.Role,
		Status:                 row.Status,
		MustChangePassword:     row.MustChangePassword,
		ImageGenerationEnabled: row.ImageGenerationEnabled,
		LastLoginAt:            timestamptzPtr(row.LastLoginAt),
		CreatedAt:              timestamptzValue(row.CreatedAt),
		UpdatedAt:              timestamptzValue(row.UpdatedAt),
	}
}

func isManagementProfileDisplayNameUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == "23505" &&
		pgErr.ConstraintName == "idx_system_accounts_display_name_unique_lower"
}

var _ port.ManagementSessionReader = (*Store)(nil)
var _ port.ManagementSessionRevoker = (*Store)(nil)
var _ port.ManagementSessionToucher = (*Store)(nil)
var _ port.ManagementCurrentUserProfileWriter = (*Store)(nil)
var _ port.ManagementCurrentUserPasswordChanger = (*Store)(nil)
