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

func (s *Store) ListManagementSessionsForAccount(ctx context.Context, input port.ManagementSessionListInput) (port.ManagementSessionListResult, error) {
	limit := input.Limit
	if limit <= 0 {
		return port.ManagementSessionListResult{}, nil
	}
	rows, err := s.queries().ListManagementSessionsForAccount(ctx, postgresqueries.ListManagementSessionsForAccountParams{
		SystemAccountID: input.SystemAccountID,
		NowAt:           pgtype.Timestamptz{Time: input.Now.UTC(), Valid: true},
		OffsetRows:      int32(max(0, input.Offset)),
		LimitRows:       int32(limit),
	})
	if err != nil {
		return port.ManagementSessionListResult{}, fmt.Errorf("list management sessions for account: %w", err)
	}
	pageSize := max(0, limit-1)
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}
	items := make([]port.ManagementSessionSummary, 0, len(rows))
	for _, row := range rows {
		item, err := managementSessionSummaryFromRow(row)
		if err != nil {
			return port.ManagementSessionListResult{}, err
		}
		items = append(items, item)
	}
	return port.ManagementSessionListResult{Items: items, HasMore: hasMore}, nil
}

func (s *Store) RevokeManagementSessionForAccount(ctx context.Context, input port.ManagementSessionRevokeInput) (bool, error) {
	rowsAffected, err := s.queries().RevokeManagementSessionForAccount(ctx, postgresqueries.RevokeManagementSessionForAccountParams{
		SystemAccountID: input.SystemAccountID,
		SessionID:       input.SessionID,
	})
	if err != nil {
		return false, fmt.Errorf("revoke management session for account: %w", err)
	}
	return rowsAffected > 0, nil
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

func (s *Store) CompleteManagementLogin(ctx context.Context, input port.ManagementLoginSessionInput) (port.ManagementLoginSessionResult, bool, error) {
	row, err := s.queries().CompleteManagementLogin(ctx, postgresqueries.CompleteManagementLoginParams{
		LoggedInAt:           pgtype.Timestamptz{Time: input.LoggedInAt.UTC(), Valid: true},
		SystemAccountID:      input.SystemAccountID,
		VerifiedPasswordHash: input.VerifiedPasswordHash,
		SessionID:            input.SessionID,
		TokenHash:            input.TokenHash,
		ExpiresAt:            pgtype.Timestamptz{Time: input.ExpiresAt.UTC(), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementLoginSessionResult{}, false, nil
	}
	if err != nil {
		return port.ManagementLoginSessionResult{}, false, fmt.Errorf("complete management login: %w", err)
	}
	result, err := managementLoginSessionResultFromRow(row)
	if err != nil {
		return port.ManagementLoginSessionResult{}, false, err
	}
	return result, true, nil
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

func managementSessionSummaryFromRow(row postgresqueries.ListManagementSessionsForAccountRow) (port.ManagementSessionSummary, error) {
	if !row.ExpiresAt.Valid {
		return port.ManagementSessionSummary{}, fmt.Errorf("management session summary expires_at is null")
	}
	if !row.CreatedAt.Valid {
		return port.ManagementSessionSummary{}, fmt.Errorf("management session summary created_at is null")
	}
	if !row.LastSeenAt.Valid {
		return port.ManagementSessionSummary{}, fmt.Errorf("management session summary last_seen_at is null")
	}
	return port.ManagementSessionSummary{
		ID:         row.ID,
		ExpiresAt:  row.ExpiresAt.Time.UTC(),
		CreatedAt:  row.CreatedAt.Time.UTC(),
		LastSeenAt: row.LastSeenAt.Time.UTC(),
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

func managementLoginSessionResultFromRow(row postgresqueries.CompleteManagementLoginRow) (port.ManagementLoginSessionResult, error) {
	if !row.SessionExpiresAt.Valid {
		return port.ManagementLoginSessionResult{}, fmt.Errorf("management login session expires_at is null")
	}
	return port.ManagementLoginSessionResult{
		Account: port.ManagementSystemAccountSummary{
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
		},
		SessionID:        row.SessionID,
		SessionExpiresAt: row.SessionExpiresAt.Time.UTC(),
	}, nil
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
var _ port.ManagementSessionManager = (*Store)(nil)
var _ port.ManagementCurrentUserProfileWriter = (*Store)(nil)
var _ port.ManagementCurrentUserPasswordChanger = (*Store)(nil)
var _ port.ManagementLoginStore = (*Store)(nil)
