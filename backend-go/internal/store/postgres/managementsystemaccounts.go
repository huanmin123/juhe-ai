package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const (
	defaultManagementSystemAccountOptionLimit = 50
	maxManagementSystemAccountOptionLimit     = 50
)

func (s *Store) ListManagementSystemAccounts(ctx context.Context, input port.ManagementSystemAccountListInput) (port.ManagementSystemAccountListResult, error) {
	return listManagementSystemAccounts(ctx, s.queries(), input)
}

func (s *Store) ListManagementSystemAccountOptions(ctx context.Context, input port.ManagementSystemAccountOptionListInput) ([]port.ManagementSystemAccountOption, error) {
	return listManagementSystemAccountOptions(ctx, s.queries(), input)
}

func (s *Store) ResetManagementSystemAccountPassword(ctx context.Context, input port.ManagementSystemAccountPasswordResetInput) (port.ManagementSystemAccountPasswordResetResult, bool, error) {
	row, err := s.queries().ResetManagementSystemAccountPassword(ctx, postgresqueries.ResetManagementSystemAccountPasswordParams{
		SystemAccountID:       input.SystemAccountID,
		PasswordHash:          input.PasswordHash,
		HasMustChangePassword: input.HasMustChangePassword,
		MustChangePassword:    input.MustChangePassword,
		UpdatedAt:             pgtype.Timestamptz{Time: input.UpdatedAt.UTC(), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementSystemAccountPasswordResetResult{}, false, nil
	}
	if err != nil {
		return port.ManagementSystemAccountPasswordResetResult{}, false, fmt.Errorf("reset management system account password: %w", err)
	}
	return managementSystemAccountPasswordResetResultFromRow(row), true, nil
}

func (s *Store) UpdateManagementSystemAccountStatus(ctx context.Context, input port.ManagementSystemAccountStatusUpdateInput) (port.ManagementSystemAccountStatusUpdateResult, bool, error) {
	row, err := s.queries().UpdateManagementSystemAccountStatus(ctx, postgresqueries.UpdateManagementSystemAccountStatusParams{
		SystemAccountID: input.SystemAccountID,
		Status:          input.Status,
		UpdatedAt:       pgtype.Timestamptz{Time: input.UpdatedAt.UTC(), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementSystemAccountStatusUpdateResult{}, false, nil
	}
	if err != nil {
		return port.ManagementSystemAccountStatusUpdateResult{}, false, fmt.Errorf("update management system account status: %w", err)
	}
	return managementSystemAccountStatusUpdateResultFromRow(row), true, nil
}

func (s *Store) UpdateManagementSystemAccountProfile(ctx context.Context, input port.ManagementSystemAccountProfileUpdateInput) (port.ManagementSystemAccountProfileUpdateResult, bool, error) {
	description := pgtype.Text{}
	if input.Description != nil {
		description = pgtype.Text{String: *input.Description, Valid: true}
	}
	row, err := s.queries().UpdateManagementSystemAccountProfile(ctx, postgresqueries.UpdateManagementSystemAccountProfileParams{
		SystemAccountID:       input.SystemAccountID,
		HasDisplayName:        input.HasDisplayName,
		DisplayName:           input.DisplayName,
		HasDescription:        input.HasDescription,
		Description:           description,
		HasRole:               input.HasRole,
		Role:                  input.Role,
		HasMustChangePassword: input.HasMustChangePassword,
		MustChangePassword:    input.MustChangePassword,
		UpdatedAt:             pgtype.Timestamptz{Time: input.UpdatedAt.UTC(), Valid: true},
	})
	if isManagementSystemAccountDisplayNameUniqueViolation(err) {
		return port.ManagementSystemAccountProfileUpdateResult{}, false, port.ErrManagementSystemAccountDisplayNameExists
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementSystemAccountProfileUpdateResult{}, false, nil
	}
	if err != nil {
		return port.ManagementSystemAccountProfileUpdateResult{}, false, fmt.Errorf("update management system account profile: %w", err)
	}
	return managementSystemAccountProfileUpdateResultFromRow(row), true, nil
}

func listManagementSystemAccounts(ctx context.Context, q *postgresqueries.Queries, input port.ManagementSystemAccountListInput) (port.ManagementSystemAccountListResult, error) {
	keyword := strings.ToLower(strings.TrimSpace(input.Keyword))
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	limit := input.Limit
	if limit <= 0 {
		return port.ManagementSystemAccountListResult{}, nil
	}
	rows, err := q.ListManagementSystemAccounts(ctx, postgresqueries.ListManagementSystemAccountsParams{
		Keyword:      keyword,
		KeywordUpper: keywordUpper,
		RowLimit:     int32(limit),
		RowOffset:    int32(max(0, input.Offset)),
	})
	if err != nil {
		return port.ManagementSystemAccountListResult{}, fmt.Errorf("list management system accounts: %w", err)
	}
	pageSize := max(0, limit-1)
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}
	items := make([]port.ManagementSystemAccountSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, port.ManagementSystemAccountSummary{
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
		})
	}
	return port.ManagementSystemAccountListResult{Items: items, HasMore: hasMore}, nil
}

func listManagementSystemAccountOptions(ctx context.Context, q *postgresqueries.Queries, input port.ManagementSystemAccountOptionListInput) ([]port.ManagementSystemAccountOption, error) {
	keyword := strings.TrimSpace(input.Keyword)
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	ids := uniqueStrings(input.IDs, 50)
	rows, err := q.ListManagementSystemAccountOptions(ctx, postgresqueries.ListManagementSystemAccountOptionsParams{
		HasIds:       len(ids) > 0,
		Ids:          ids,
		HasKeyword:   keyword != "",
		Keyword:      keyword,
		KeywordUpper: keywordUpper,
		RowLimit:     int32(managementSystemAccountOptionLimit(input.Limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("list management system account options: %w", err)
	}
	items := make([]port.ManagementSystemAccountOption, 0, len(rows))
	for _, row := range rows {
		items = append(items, port.ManagementSystemAccountOption{
			ID:          row.ID,
			Username:    row.Username,
			DisplayName: row.DisplayName,
			Status:      row.Status,
		})
	}
	return items, nil
}

func managementSystemAccountOptionLimit(limit int) int {
	if limit <= 0 {
		return defaultManagementSystemAccountOptionLimit
	}
	return min(limit, maxManagementSystemAccountOptionLimit)
}

func managementSystemAccountPasswordResetResultFromRow(row postgresqueries.ResetManagementSystemAccountPasswordRow) port.ManagementSystemAccountPasswordResetResult {
	before := port.ManagementSystemAccountSummary{
		ID:                     row.BeforeID,
		Username:               row.BeforeUsername,
		DisplayName:            row.BeforeDisplayName,
		Description:            textValue(row.BeforeDescription),
		Role:                   row.BeforeRole,
		Status:                 row.BeforeStatus,
		MustChangePassword:     row.BeforeMustChangePassword,
		ImageGenerationEnabled: row.BeforeImageGenerationEnabled,
		LastLoginAt:            timestamptzPtr(row.BeforeLastLoginAt),
		CreatedAt:              timestamptzValue(row.BeforeCreatedAt),
		UpdatedAt:              timestamptzValue(row.BeforeUpdatedAt),
	}
	account := port.ManagementSystemAccountSummary{
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
	return port.ManagementSystemAccountPasswordResetResult{
		Before:              before,
		Account:             account,
		RevokedSessionCount: int(row.RevokedSessionCount),
	}
}

func managementSystemAccountStatusUpdateResultFromRow(row postgresqueries.UpdateManagementSystemAccountStatusRow) port.ManagementSystemAccountStatusUpdateResult {
	before := port.ManagementSystemAccountSummary{
		ID:                     row.BeforeID,
		Username:               row.BeforeUsername,
		DisplayName:            row.BeforeDisplayName,
		Description:            textValue(row.BeforeDescription),
		Role:                   row.BeforeRole,
		Status:                 row.BeforeStatus,
		MustChangePassword:     row.BeforeMustChangePassword,
		ImageGenerationEnabled: row.BeforeImageGenerationEnabled,
		LastLoginAt:            timestamptzPtr(row.BeforeLastLoginAt),
		CreatedAt:              timestamptzValue(row.BeforeCreatedAt),
		UpdatedAt:              timestamptzValue(row.BeforeUpdatedAt),
	}
	account := port.ManagementSystemAccountSummary{
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
	return port.ManagementSystemAccountStatusUpdateResult{
		Before:                      before,
		Account:                     account,
		RevokedSessionCount:         int(row.RevokedSessionCount),
		BlockedLastActiveSuperAdmin: row.BlockedLastActiveSuperAdmin,
	}
}

func managementSystemAccountProfileUpdateResultFromRow(row postgresqueries.UpdateManagementSystemAccountProfileRow) port.ManagementSystemAccountProfileUpdateResult {
	before := port.ManagementSystemAccountSummary{
		ID:                     row.BeforeID,
		Username:               row.BeforeUsername,
		DisplayName:            row.BeforeDisplayName,
		Description:            textValue(row.BeforeDescription),
		Role:                   row.BeforeRole,
		Status:                 row.BeforeStatus,
		MustChangePassword:     row.BeforeMustChangePassword,
		ImageGenerationEnabled: row.BeforeImageGenerationEnabled,
		LastLoginAt:            timestamptzPtr(row.BeforeLastLoginAt),
		CreatedAt:              timestamptzValue(row.BeforeCreatedAt),
		UpdatedAt:              timestamptzValue(row.BeforeUpdatedAt),
	}
	account := port.ManagementSystemAccountSummary{
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
	return port.ManagementSystemAccountProfileUpdateResult{
		Before:                      before,
		Account:                     account,
		BlockedLastActiveSuperAdmin: row.BlockedLastActiveSuperAdmin,
	}
}

func isManagementSystemAccountDisplayNameUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == "23505" &&
		pgErr.ConstraintName == "idx_system_accounts_display_name_unique_lower"
}

var _ port.ManagementSystemAccountOptionReader = (*Store)(nil)
var _ port.ManagementSystemAccountPasswordResetter = (*Store)(nil)
var _ port.ManagementSystemAccountStatusUpdater = (*Store)(nil)
var _ port.ManagementSystemAccountProfileUpdater = (*Store)(nil)
