package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const maxManagementSystemTeamMembersPerTeam = 500

func (s *Store) ListManagementSystemTeams(ctx context.Context, input port.ManagementSystemTeamListInput) (port.ManagementSystemTeamListResult, error) {
	keyword := strings.TrimSpace(input.Keyword)
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	limit := input.Limit
	if limit <= 0 {
		return port.ManagementSystemTeamListResult{}, nil
	}
	rows, err := s.queries().ListManagementSystemTeams(ctx, postgresqueries.ListManagementSystemTeamsParams{
		SystemAccountID: strings.TrimSpace(input.SystemAccountID),
		Keyword:         keyword,
		KeywordUpper:    keywordUpper,
		RowLimit:        int32(limit),
		RowOffset:       int32(max(0, input.Offset)),
	})
	if err != nil {
		return port.ManagementSystemTeamListResult{}, fmt.Errorf("list management system teams: %w", err)
	}
	pageSize := max(0, limit-1)
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}
	counts, err := s.managementSystemTeamMemberCounts(ctx, managementSystemTeamIDsFromListRows(rows))
	if err != nil {
		return port.ManagementSystemTeamListResult{}, err
	}
	items := make([]port.ManagementSystemTeamSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, managementSystemTeamSummaryFromListRow(row, counts[row.ID]))
	}
	return port.ManagementSystemTeamListResult{Items: items, HasMore: hasMore}, nil
}

func (s *Store) FindManagementSystemTeam(ctx context.Context, teamID string, systemAccountID string) (port.ManagementSystemTeamDetail, bool, error) {
	row, err := s.queries().FindManagementSystemTeam(ctx, postgresqueries.FindManagementSystemTeamParams{
		TeamID:          strings.TrimSpace(teamID),
		SystemAccountID: strings.TrimSpace(systemAccountID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementSystemTeamDetail{}, false, nil
	}
	if err != nil {
		return port.ManagementSystemTeamDetail{}, false, fmt.Errorf("find management system team: %w", err)
	}
	members, err := s.queries().ListManagementSystemTeamMembers(ctx, strings.TrimSpace(teamID))
	if err != nil {
		return port.ManagementSystemTeamDetail{}, false, fmt.Errorf("list management system team members: %w", err)
	}
	detail := port.ManagementSystemTeamDetail{
		ManagementSystemTeamSummary: managementSystemTeamSummaryFromFindRow(row),
		Members:                     make([]port.ManagementSystemTeamMemberSummary, 0, len(members)),
	}
	for _, member := range members {
		detail.Members = append(detail.Members, managementSystemTeamMemberFromRow(member))
	}
	detail.MemberCount = len(detail.Members)
	detail.ActiveMemberCount = len(detail.Members)
	return detail, true, nil
}

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

func (s *Store) managementSystemTeamMemberCounts(ctx context.Context, teamIDs []string) (map[string]int, error) {
	counts := make(map[string]int, len(teamIDs))
	if len(teamIDs) == 0 {
		return counts, nil
	}
	rows, err := s.queries().ListManagementSystemTeamMemberCounts(ctx, teamIDs)
	if err != nil {
		return nil, fmt.Errorf("list management system team member counts: %w", err)
	}
	for _, row := range rows {
		counts[row.TeamID] = int(row.ActiveMemberCount)
	}
	return counts, nil
}

func managementSystemTeamIDsFromListRows(rows []postgresqueries.JuheBusinessSystemTeam) []string {
	ids := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if row.ID == "" {
			continue
		}
		if _, ok := seen[row.ID]; ok {
			continue
		}
		seen[row.ID] = struct{}{}
		ids = append(ids, row.ID)
	}
	return ids
}

func managementSystemTeamSummaryFromListRow(row postgresqueries.JuheBusinessSystemTeam, activeMemberCount int) port.ManagementSystemTeamSummary {
	return port.ManagementSystemTeamSummary{
		ID:                row.ID,
		Name:              row.Name,
		Description:       textValue(row.Description),
		Status:            row.Status,
		MemberCount:       activeMemberCount,
		ActiveMemberCount: activeMemberCount,
		CreatedBy:         row.CreatedBy,
		CreatedAt:         timestamptzValue(row.CreatedAt),
		UpdatedAt:         timestamptzValue(row.UpdatedAt),
	}
}

func managementSystemTeamSummaryFromFindRow(row postgresqueries.FindManagementSystemTeamRow) port.ManagementSystemTeamSummary {
	activeMemberCount := int(row.ActiveMemberCount)
	return port.ManagementSystemTeamSummary{
		ID:                row.ID,
		Name:              row.Name,
		Description:       textValue(row.Description),
		Status:            row.Status,
		MemberCount:       activeMemberCount,
		ActiveMemberCount: activeMemberCount,
		CreatedBy:         row.CreatedBy,
		CreatedAt:         timestamptzValue(row.CreatedAt),
		UpdatedAt:         timestamptzValue(row.UpdatedAt),
	}
}

func managementSystemTeamMemberFromRow(row postgresqueries.ListManagementSystemTeamMembersRow) port.ManagementSystemTeamMemberSummary {
	return port.ManagementSystemTeamMemberSummary{
		ID:                row.ID,
		TeamID:            row.TeamID,
		SystemAccountID:   row.SystemAccountID,
		SystemAccountName: row.SystemAccountName,
		Username:          row.Username,
		MemberRole:        row.MemberRole,
		Status:            row.Status,
		JoinedAt:          timestamptzValue(row.JoinedAt),
		RemovedAt:         timestamptzPtr(row.RemovedAt),
		CreatedAt:         timestamptzValue(row.CreatedAt),
		UpdatedAt:         timestamptzValue(row.UpdatedAt),
	}
}

var _ port.ManagementSystemTeamCreator = (*Store)(nil)
var _ port.ManagementSystemTeamReader = (*Store)(nil)
