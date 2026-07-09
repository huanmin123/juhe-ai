package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func (s *Store) CreateManagementSystemTeam(ctx context.Context, input port.ManagementSystemTeamCreateInput) (port.ManagementSystemTeamSummary, error) {
	description := pgtype.Text{}
	if input.Description != nil && *input.Description != "" {
		description = pgtype.Text{String: *input.Description, Valid: true}
	}
	row, err := s.queries().CreateManagementSystemTeam(ctx, postgresqueries.CreateManagementSystemTeamParams{
		ID:          input.ID,
		Name:        input.Name,
		Description: description,
		Status:      input.Status,
		CreatedBy:   input.CreatedBy,
		CreatedAt:   pgTimestamptz(input.CreatedAt),
		UpdatedAt:   pgTimestamptz(input.UpdatedAt),
	})
	if err != nil {
		if isPGUniqueViolation(err) && strings.Contains(err.Error(), "idx_system_teams_name_unique") {
			return port.ManagementSystemTeamSummary{}, port.ErrManagementSystemTeamNameExists
		}
		return port.ManagementSystemTeamSummary{}, fmt.Errorf("create management system team: %w", err)
	}
	return port.ManagementSystemTeamSummary{
		ID:                row.ID,
		Name:              row.Name,
		Description:       textValue(row.Description),
		Status:            row.Status,
		MemberCount:       0,
		ActiveMemberCount: 0,
		CreatedBy:         row.CreatedBy,
		CreatedAt:         timestamptzValue(row.CreatedAt),
		UpdatedAt:         timestamptzValue(row.UpdatedAt),
	}, nil
}

var _ port.ManagementSystemTeamCreator = (*Store)(nil)
