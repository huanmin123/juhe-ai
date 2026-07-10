package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const maxManagementSystemTeamMembersPerTeam = 500

const (
	maxManagementSystemTeamAuthorizationMembersPerTeam = 20
	maxManagementSystemTeamActiveGrantCount            = 20
)

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

func (s *Store) UpdateManagementSystemTeam(ctx context.Context, input port.ManagementSystemTeamUpdateInput) (port.ManagementSystemTeamUpdateResult, bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementSystemTeamUpdateResult{}, false, fmt.Errorf("begin update management system team tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	q := s.queries().WithTx(tx)
	teamID := strings.TrimSpace(input.TeamID)
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	beforeRow, err := q.FindManagementSystemTeamForUpdate(ctx, postgresqueries.FindManagementSystemTeamForUpdateParams{
		TeamID:          teamID,
		SystemAccountID: systemAccountID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementSystemTeamUpdateResult{}, false, nil
	}
	if err != nil {
		return port.ManagementSystemTeamUpdateResult{}, false, fmt.Errorf("find management system team for update: %w", err)
	}
	before := managementSystemTeamSummaryFromTeamRow(beforeRow, 0)

	description := pgtype.Text{}
	if input.Description != nil {
		description = pgtype.Text{String: *input.Description, Valid: true}
	}
	updatedRow, err := q.UpdateManagementSystemTeam(ctx, postgresqueries.UpdateManagementSystemTeamParams{
		TeamID:         teamID,
		HasName:        input.HasName,
		Name:           input.Name,
		HasDescription: input.HasDescription,
		Description:    description,
		HasStatus:      input.HasStatus,
		Status:         input.Status,
		UpdatedAt:      pgTimestamptz(input.UpdatedAt),
	})
	if err != nil {
		if isPGUniqueViolation(err) && strings.Contains(err.Error(), "idx_system_teams_name_unique") {
			return port.ManagementSystemTeamUpdateResult{}, false, port.ErrManagementSystemTeamNameExists
		}
		return port.ManagementSystemTeamUpdateResult{}, false, fmt.Errorf("update management system team: %w", err)
	}

	authorizationChanged := false
	if beforeRow.Status != "disabled" && updatedRow.Status == "disabled" {
		if err := revokeAllManagementTeamSourcesTx(ctx, tx, teamID, strings.TrimSpace(input.UpdatedBy), input.UpdatedAt, "team_disabled"); err != nil {
			return port.ManagementSystemTeamUpdateResult{}, false, err
		}
		authorizationChanged = true
	}
	if beforeRow.Status == "disabled" && updatedRow.Status == "active" {
		if err := reactivateManagementTeamGrantSourcesTx(ctx, tx, teamID, strings.TrimSpace(input.UpdatedBy), input.UpdatedAt); err != nil {
			return port.ManagementSystemTeamUpdateResult{}, false, err
		}
		authorizationChanged = true
	}
	if authorizationChanged {
		if err := markAllGroupAccountStatsDirtyIfPresentTx(ctx, tx, "team_authorization_changed", input.UpdatedAt); err != nil {
			return port.ManagementSystemTeamUpdateResult{}, false, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return port.ManagementSystemTeamUpdateResult{}, false, fmt.Errorf("commit update management system team tx rolled back: %w", err)
		}
		return port.ManagementSystemTeamUpdateResult{}, false, fmt.Errorf("commit update management system team tx: %w", err)
	}
	committed = true

	team, found, err := s.FindManagementSystemTeam(ctx, teamID, systemAccountID)
	if err != nil {
		return port.ManagementSystemTeamUpdateResult{}, false, err
	}
	if !found {
		return port.ManagementSystemTeamUpdateResult{}, false, fmt.Errorf("find updated management system team: not found")
	}
	return port.ManagementSystemTeamUpdateResult{
		Before:               before,
		Team:                 team,
		AuthorizationChanged: authorizationChanged,
	}, true, nil
}

func (s *Store) AddManagementSystemTeamMembers(ctx context.Context, input port.ManagementSystemTeamMemberAddInput) (port.ManagementSystemTeamMemberAddResult, bool, error) {
	teamID := strings.TrimSpace(input.TeamID)
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	before, _, err := s.FindManagementSystemTeam(ctx, teamID, systemAccountID)
	if err != nil {
		return port.ManagementSystemTeamMemberAddResult{}, false, err
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementSystemTeamMemberAddResult{}, false, fmt.Errorf("begin add management system team members tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	q := s.queries().WithTx(tx)
	if _, err := q.FindActiveManagementSystemTeamForUpdate(ctx, postgresqueries.FindActiveManagementSystemTeamForUpdateParams{
		TeamID:          teamID,
		SystemAccountID: systemAccountID,
	}); errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementSystemTeamMemberAddResult{}, false, nil
	} else if err != nil {
		return port.ManagementSystemTeamMemberAddResult{}, false, fmt.Errorf("find active management system team for member add: %w", err)
	}

	existingActiveMemberIDs, err := activeManagementTeamMemberIDsForLimitTx(ctx, tx, teamID)
	if err != nil {
		return port.ManagementSystemTeamMemberAddResult{}, false, err
	}
	existingActiveMemberSet := make(map[string]struct{}, len(existingActiveMemberIDs))
	for _, id := range existingActiveMemberIDs {
		existingActiveMemberSet[id] = struct{}{}
	}
	nextActiveMemberCount := len(existingActiveMemberSet)
	for _, id := range input.SystemAccountIDs {
		if _, ok := existingActiveMemberSet[id]; ok {
			continue
		}
		nextActiveMemberCount++
	}
	if nextActiveMemberCount > maxManagementSystemTeamAuthorizationMembersPerTeam {
		return port.ManagementSystemTeamMemberAddResult{}, false, fmt.Errorf("授权团队最多支持 %d 个成员，请先移除部分成员后再添加", maxManagementSystemTeamAuthorizationMembersPerTeam)
	}

	now := input.UpdatedAt.UTC()
	for _, systemAccountID := range input.SystemAccountIDs {
		if err := assertActiveManagementSystemAccountTx(ctx, tx, systemAccountID); err != nil {
			return port.ManagementSystemTeamMemberAddResult{}, false, err
		}
		memberID, memberStatus, found, err := latestManagementTeamMemberTx(ctx, tx, teamID, systemAccountID)
		if err != nil {
			return port.ManagementSystemTeamMemberAddResult{}, false, err
		}
		if found && memberStatus == "active" {
			continue
		}
		if found {
			if _, err := tx.Exec(ctx, `
UPDATE juhe_business.system_team_members
SET status = 'active',
    joined_at = $1,
    removed_at = NULL,
    updated_at = $1
WHERE id = $2
`, now, memberID); err != nil {
				return port.ManagementSystemTeamMemberAddResult{}, false, fmt.Errorf("reactivate management system team member: %w", err)
			}
		} else if _, err := tx.Exec(ctx, `
INSERT INTO juhe_business.system_team_members (
  id, team_id, system_account_id, member_role, status, joined_at, removed_at,
  created_by, created_at, updated_at
) VALUES (
  $1, $2, $3, 'member', 'active', $5, NULL,
  $4, $5, $5
)
`, prefixedUUID("teammem"), teamID, systemAccountID, strings.TrimSpace(input.CreatedBy), now); err != nil {
			return port.ManagementSystemTeamMemberAddResult{}, false, fmt.Errorf("insert management system team member: %w", err)
		}
		if err := applyActiveManagementTeamGrantsToMemberTx(ctx, tx, teamID, systemAccountID, strings.TrimSpace(input.CreatedBy), now); err != nil {
			return port.ManagementSystemTeamMemberAddResult{}, false, err
		}
	}

	if err := markAllGroupAccountStatsDirtyIfPresentTx(ctx, tx, "team_members_changed", now); err != nil {
		return port.ManagementSystemTeamMemberAddResult{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return port.ManagementSystemTeamMemberAddResult{}, false, fmt.Errorf("commit add management system team members tx rolled back: %w", err)
		}
		return port.ManagementSystemTeamMemberAddResult{}, false, fmt.Errorf("commit add management system team members tx: %w", err)
	}
	committed = true

	team, found, err := s.FindManagementSystemTeam(ctx, teamID, systemAccountID)
	if err != nil {
		return port.ManagementSystemTeamMemberAddResult{}, false, err
	}
	if !found {
		return port.ManagementSystemTeamMemberAddResult{}, false, fmt.Errorf("find added management system team: not found")
	}
	return port.ManagementSystemTeamMemberAddResult{Before: before, Team: team}, true, nil
}

func (s *Store) RemoveManagementSystemTeamMember(ctx context.Context, input port.ManagementSystemTeamMemberRemoveInput) (port.ManagementSystemTeamMemberRemoveResult, bool, error) {
	teamID := strings.TrimSpace(input.TeamID)
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	before, _, err := s.FindManagementSystemTeam(ctx, teamID, systemAccountID)
	if err != nil {
		return port.ManagementSystemTeamMemberRemoveResult{}, false, err
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementSystemTeamMemberRemoveResult{}, false, fmt.Errorf("begin remove management system team member tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	removedMember, found, err := activeManagementTeamMemberForAccessTx(ctx, tx, teamID, strings.TrimSpace(input.MemberID), systemAccountID)
	if err != nil {
		return port.ManagementSystemTeamMemberRemoveResult{}, false, err
	}
	if !found {
		return port.ManagementSystemTeamMemberRemoveResult{}, false, nil
	}
	now := input.UpdatedAt.UTC()
	if _, err := tx.Exec(ctx, `
UPDATE juhe_business.system_team_members
SET status = 'removed',
    removed_at = $1,
    updated_at = $1
WHERE id = $2
`, now, strings.TrimSpace(input.MemberID)); err != nil {
		return port.ManagementSystemTeamMemberRemoveResult{}, false, fmt.Errorf("remove management system team member: %w", err)
	}
	if err := revokeManagementTeamSourcesForMemberTx(ctx, tx, teamID, removedMember.SystemAccountID, strings.TrimSpace(input.UpdatedBy), now); err != nil {
		return port.ManagementSystemTeamMemberRemoveResult{}, false, err
	}
	if err := markAllGroupAccountStatsDirtyIfPresentTx(ctx, tx, "team_members_changed", now); err != nil {
		return port.ManagementSystemTeamMemberRemoveResult{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return port.ManagementSystemTeamMemberRemoveResult{}, false, fmt.Errorf("commit remove management system team member tx rolled back: %w", err)
		}
		return port.ManagementSystemTeamMemberRemoveResult{}, false, fmt.Errorf("commit remove management system team member tx: %w", err)
	}
	committed = true

	team, found, err := s.FindManagementSystemTeam(ctx, teamID, systemAccountID)
	if err != nil {
		return port.ManagementSystemTeamMemberRemoveResult{}, false, err
	}
	if !found {
		team, found, err = s.FindManagementSystemTeam(ctx, teamID, "")
		if err != nil {
			return port.ManagementSystemTeamMemberRemoveResult{}, false, err
		}
	}
	if !found {
		return port.ManagementSystemTeamMemberRemoveResult{}, false, fmt.Errorf("find removed management system team: not found")
	}
	return port.ManagementSystemTeamMemberRemoveResult{
		Before:        before,
		Team:          team,
		RemovedMember: removedMember,
	}, true, nil
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

func activeManagementTeamMemberIDsForLimitTx(ctx context.Context, tx pgx.Tx, teamID string) ([]string, error) {
	limit := maxManagementSystemTeamAuthorizationMembersPerTeam + 1
	rows, err := tx.Query(ctx, `
SELECT system_account_id
FROM juhe_business.system_team_members
WHERE team_id = $1
  AND status = 'active'
ORDER BY system_account_id ASC
LIMIT $2
`, teamID, limit)
	if err != nil {
		return nil, fmt.Errorf("list active management team member ids: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan active management team member id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active management team member ids: %w", err)
	}
	if len(ids) > maxManagementSystemTeamAuthorizationMembersPerTeam {
		return nil, fmt.Errorf("授权团队最多支持 %d 个成员，请先移除部分成员后再添加", maxManagementSystemTeamAuthorizationMembersPerTeam)
	}
	return ids, nil
}

func assertActiveManagementSystemAccountTx(ctx context.Context, tx pgx.Tx, systemAccountID string) error {
	var status string
	err := tx.QueryRow(ctx, `
SELECT status
FROM juhe_business.system_accounts
WHERE id = $1
LIMIT 1
`, systemAccountID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("团队成员不存在或已停用")
	}
	if err != nil {
		return fmt.Errorf("find management system account for team member: %w", err)
	}
	if status != "active" {
		return fmt.Errorf("团队成员不存在或已停用")
	}
	return nil
}

func latestManagementTeamMemberTx(ctx context.Context, tx pgx.Tx, teamID string, systemAccountID string) (string, string, bool, error) {
	var memberID string
	var status string
	err := tx.QueryRow(ctx, `
SELECT id, status
FROM juhe_business.system_team_members
WHERE team_id = $1
  AND system_account_id = $2
ORDER BY created_at DESC, id DESC
LIMIT 1
FOR UPDATE
`, teamID, systemAccountID).Scan(&memberID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("find latest management system team member: %w", err)
	}
	return memberID, status, true, nil
}

func activeManagementTeamMemberForAccessTx(ctx context.Context, tx pgx.Tx, teamID string, memberID string, systemAccountID string) (port.ManagementSystemTeamMemberSummary, bool, error) {
	row := struct {
		ID                string
		TeamID            string
		SystemAccountID   string
		SystemAccountName string
		Username          string
		MemberRole        string
		Status            string
		JoinedAt          pgtype.Timestamptz
		RemovedAt         pgtype.Timestamptz
		CreatedAt         pgtype.Timestamptz
		UpdatedAt         pgtype.Timestamptz
	}{}
	err := tx.QueryRow(ctx, `
SELECT
  members.id,
  members.team_id,
  members.system_account_id,
  accounts.display_name AS system_account_name,
  accounts.username,
  members.member_role,
  members.status,
  members.joined_at,
  members.removed_at,
  members.created_at,
  members.updated_at
FROM juhe_business.system_team_members AS members
INNER JOIN juhe_business.system_teams AS teams
  ON teams.id = members.team_id
INNER JOIN juhe_business.system_accounts AS accounts
  ON accounts.id = members.system_account_id
WHERE members.id = $1
  AND members.team_id = $2
  AND members.status = 'active'
  AND (
    $3::text = ''
    OR EXISTS (
      SELECT 1
      FROM juhe_business.system_team_members AS scoped_members
      WHERE scoped_members.team_id = teams.id
        AND scoped_members.system_account_id = $3::text
        AND scoped_members.status = 'active'
    )
  )
LIMIT 1
FOR UPDATE OF members
`, memberID, teamID, systemAccountID).Scan(
		&row.ID,
		&row.TeamID,
		&row.SystemAccountID,
		&row.SystemAccountName,
		&row.Username,
		&row.MemberRole,
		&row.Status,
		&row.JoinedAt,
		&row.RemovedAt,
		&row.CreatedAt,
		&row.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementSystemTeamMemberSummary{}, false, nil
	}
	if err != nil {
		return port.ManagementSystemTeamMemberSummary{}, false, fmt.Errorf("find active management system team member for access: %w", err)
	}
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
	}, true, nil
}

func revokeAllManagementTeamSourcesTx(ctx context.Context, tx pgx.Tx, teamID string, actor string, now time.Time, reason string) error {
	limit := maxManagementSystemTeamAuthorizationMembersPerTeam*maxManagementSystemTeamActiveGrantCount + 1
	rows, err := tx.Query(ctx, `
SELECT DISTINCT authorization_id
FROM juhe_business.resource_authorization_sources
WHERE source_type = 'team'
  AND source_team_id = $1
  AND status = 'active'
ORDER BY authorization_id ASC
LIMIT $2
`, teamID, limit)
	if err != nil {
		return fmt.Errorf("list active team authorization sources: %w", err)
	}
	defer rows.Close()

	authorizationIDs := make([]string, 0)
	for rows.Next() {
		var authorizationID string
		if err := rows.Scan(&authorizationID); err != nil {
			return fmt.Errorf("scan active team authorization source: %w", err)
		}
		authorizationIDs = append(authorizationIDs, authorizationID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate active team authorization sources: %w", err)
	}
	if len(authorizationIDs) > maxManagementSystemTeamAuthorizationMembersPerTeam*maxManagementSystemTeamActiveGrantCount {
		return fmt.Errorf("授权团队来源展开超过当前系统上限，请先拆分团队或回收部分授权")
	}
	for _, authorizationID := range authorizationIDs {
		if _, err := tx.Exec(ctx, `
UPDATE juhe_business.resource_authorization_sources
SET status = 'revoked',
    ended_at = COALESCE(ended_at, $1),
    ended_reason = COALESCE(ended_reason, $2),
    revoked_by = $3,
    revoked_at = $1,
    updated_at = $1
WHERE authorization_id = $4
  AND source_type = 'team'
  AND source_team_id = $5
  AND status = 'active'
`, now.UTC(), reason, actor, authorizationID, teamID); err != nil {
			return fmt.Errorf("revoke team authorization source: %w", err)
		}
		if err := refreshManagementResourceAuthorizationEffectiveSourceTx(ctx, tx, authorizationID, actor, now); err != nil {
			return err
		}
	}
	return nil
}

func revokeManagementTeamSourcesForMemberTx(ctx context.Context, tx pgx.Tx, teamID string, systemAccountID string, actor string, now time.Time) error {
	limit := maxManagementSystemTeamActiveGrantCount + 1
	rows, err := tx.Query(ctx, `
SELECT ras.authorization_id
FROM juhe_business.resource_authorization_sources AS ras
INNER JOIN juhe_business.resource_authorizations AS ra
  ON ra.id = ras.authorization_id
WHERE ras.source_type = 'team'
  AND ras.source_team_id = $1
  AND ras.status = 'active'
  AND ra.grantee_system_account_id = $2
ORDER BY ras.authorization_id ASC
LIMIT $3
`, teamID, systemAccountID, limit)
	if err != nil {
		return fmt.Errorf("list active member team authorization sources: %w", err)
	}
	defer rows.Close()

	authorizationIDs := make([]string, 0)
	for rows.Next() {
		var authorizationID string
		if err := rows.Scan(&authorizationID); err != nil {
			return fmt.Errorf("scan active member team authorization source: %w", err)
		}
		authorizationIDs = append(authorizationIDs, authorizationID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate active member team authorization sources: %w", err)
	}
	if len(authorizationIDs) > maxManagementSystemTeamActiveGrantCount {
		return fmt.Errorf("单个授权团队最多支持 %d 条有效授权，请先回收或停用部分授权", maxManagementSystemTeamActiveGrantCount)
	}
	for _, authorizationID := range authorizationIDs {
		if _, err := tx.Exec(ctx, `
UPDATE juhe_business.resource_authorization_sources
SET status = 'revoked',
    ended_at = COALESCE(ended_at, $1),
    ended_reason = COALESCE(ended_reason, 'member_removed'),
    revoked_by = $2,
    revoked_at = $1,
    updated_at = $1
WHERE authorization_id = $3
  AND source_type = 'team'
  AND source_team_id = $4
  AND status = 'active'
`, now.UTC(), actor, authorizationID, teamID); err != nil {
			return fmt.Errorf("revoke member team authorization source: %w", err)
		}
		if err := refreshManagementResourceAuthorizationEffectiveSourceTx(ctx, tx, authorizationID, actor, now); err != nil {
			return err
		}
	}
	return nil
}

func reactivateManagementTeamGrantSourcesTx(ctx context.Context, tx pgx.Tx, teamID string, actor string, now time.Time) error {
	memberRows, err := activeManagementTeamMemberIDsTx(ctx, tx, teamID)
	if err != nil {
		return err
	}
	for _, systemAccountID := range memberRows {
		if err := applyActiveManagementTeamGrantsToMemberTx(ctx, tx, teamID, systemAccountID, actor, now); err != nil {
			return err
		}
	}
	return nil
}

func activeManagementTeamMemberIDsTx(ctx context.Context, tx pgx.Tx, teamID string) ([]string, error) {
	limit := maxManagementSystemTeamAuthorizationMembersPerTeam + 1
	rows, err := tx.Query(ctx, `
SELECT members.system_account_id
FROM juhe_business.system_team_members AS members
INNER JOIN juhe_business.system_accounts AS accounts
  ON accounts.id = members.system_account_id
WHERE members.team_id = $1
  AND members.status = 'active'
  AND accounts.status = 'active'
ORDER BY members.joined_at ASC, members.id ASC
LIMIT $2
`, teamID, limit)
	if err != nil {
		return nil, fmt.Errorf("list active management team members: %w", err)
	}
	defer rows.Close()

	memberIDs := make([]string, 0)
	for rows.Next() {
		var systemAccountID string
		if err := rows.Scan(&systemAccountID); err != nil {
			return nil, fmt.Errorf("scan active management team member: %w", err)
		}
		memberIDs = append(memberIDs, systemAccountID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active management team members: %w", err)
	}
	if len(memberIDs) > maxManagementSystemTeamAuthorizationMembersPerTeam {
		return nil, fmt.Errorf("授权团队最多支持 %d 个成员，请先移除部分成员后再继续", maxManagementSystemTeamAuthorizationMembersPerTeam)
	}
	return memberIDs, nil
}

type managementTeamGrantRow struct {
	resourceType                 string
	resourceID                   string
	resourceOwnerSystemAccountID string
	remark                       pgtype.Text
	expiresAt                    pgtype.Timestamptz
	limitsJSON                   pgtype.Text
}

func activeManagementTeamGrantRowsTx(ctx context.Context, tx pgx.Tx, teamID string) ([]managementTeamGrantRow, error) {
	limit := maxManagementSystemTeamActiveGrantCount + 1
	rows, err := tx.Query(ctx, `
SELECT resource_type, resource_id, resource_owner_system_account_id, remark, expires_at, limits_json
FROM juhe_business.resource_authorization_grants
WHERE grantee_type = 'team'
  AND grantee_team_id = $1
  AND status = 'active'
ORDER BY created_at ASC, id ASC
LIMIT $2
`, teamID, limit)
	if err != nil {
		return nil, fmt.Errorf("list active management team grants: %w", err)
	}
	defer rows.Close()

	grants := make([]managementTeamGrantRow, 0)
	for rows.Next() {
		var grant managementTeamGrantRow
		if err := rows.Scan(&grant.resourceType, &grant.resourceID, &grant.resourceOwnerSystemAccountID, &grant.remark, &grant.expiresAt, &grant.limitsJSON); err != nil {
			return nil, fmt.Errorf("scan active management team grant: %w", err)
		}
		grants = append(grants, grant)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active management team grants: %w", err)
	}
	if len(grants) > maxManagementSystemTeamActiveGrantCount {
		return nil, fmt.Errorf("单个授权团队最多支持 %d 条有效授权，请先回收或停用部分授权", maxManagementSystemTeamActiveGrantCount)
	}
	return grants, nil
}

func applyActiveManagementTeamGrantsToMemberTx(ctx context.Context, tx pgx.Tx, teamID string, systemAccountID string, actor string, now time.Time) error {
	grants, err := activeManagementTeamGrantRowsTx(ctx, tx, teamID)
	if err != nil {
		return err
	}
	for _, grant := range grants {
		if grant.resourceOwnerSystemAccountID == systemAccountID {
			continue
		}
		if err := upsertManagementTeamAuthorizationForUserTx(ctx, tx, managementTeamAuthorizationUpsertInput{
			resourceType:                 grant.resourceType,
			resourceID:                   grant.resourceID,
			resourceOwnerSystemAccountID: grant.resourceOwnerSystemAccountID,
			granteeSystemAccountID:       systemAccountID,
			sourceTeamID:                 teamID,
			remark:                       grant.remark,
			expiresAt:                    grant.expiresAt,
			limitsJSON:                   grant.limitsJSON,
			actor:                        actor,
			now:                          now,
		}); err != nil {
			return err
		}
	}
	return nil
}

type managementTeamAuthorizationUpsertInput struct {
	resourceType                 string
	resourceID                   string
	resourceOwnerSystemAccountID string
	granteeSystemAccountID       string
	sourceTeamID                 string
	remark                       pgtype.Text
	expiresAt                    pgtype.Timestamptz
	limitsJSON                   pgtype.Text
	actor                        string
	now                          time.Time
}

func upsertManagementTeamAuthorizationForUserTx(ctx context.Context, tx pgx.Tx, input managementTeamAuthorizationUpsertInput) error {
	if input.granteeSystemAccountID == input.resourceOwnerSystemAccountID {
		return fmt.Errorf("不能授权给资源所有者自己")
	}
	now := input.now.UTC()
	var authorizationID string
	err := tx.QueryRow(ctx, `
SELECT id
FROM juhe_business.resource_authorizations
WHERE resource_type = $1
  AND resource_id = $2
  AND grantee_system_account_id = $3
LIMIT 1
`, input.resourceType, input.resourceID, input.granteeSystemAccountID).Scan(&authorizationID)
	if errors.Is(err, pgx.ErrNoRows) {
		authorizationID = prefixedUUID("rauth")
		if _, err := tx.Exec(ctx, `
INSERT INTO juhe_business.resource_authorizations (
  id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
  scope, status, effective_source_type, effective_source_team_id, activated_at,
  last_source_changed_at, remark, expires_at, limits_json, created_by, created_at,
  revoked_by, revoked_at, revoked_reason, updated_at
) VALUES (
  $1, $2, $3, $4, $5,
  'use', CASE WHEN $10::timestamptz IS NOT NULL AND $10::timestamptz <= $9 THEN 'expired' ELSE 'active' END,
  'team', $6, $9,
  $9, $7::text, $10, $11::text, $8, $9,
  CASE WHEN $10::timestamptz IS NOT NULL AND $10::timestamptz <= $9 THEN $8 ELSE NULL END,
  CASE WHEN $10::timestamptz IS NOT NULL AND $10::timestamptz <= $9 THEN $9 ELSE NULL END,
  CASE WHEN $10::timestamptz IS NOT NULL AND $10::timestamptz <= $9 THEN 'authorization_expired' ELSE NULL END,
  $9
)
`, authorizationID, input.resourceType, input.resourceID, input.resourceOwnerSystemAccountID, input.granteeSystemAccountID, input.sourceTeamID, input.remark, input.actor, now, input.expiresAt, input.limitsJSON); err != nil {
			return fmt.Errorf("insert team resource authorization: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("find team resource authorization: %w", err)
	} else if _, err := tx.Exec(ctx, `
UPDATE juhe_business.resource_authorizations
SET resource_owner_system_account_id = $1,
    status = CASE WHEN $6::timestamptz IS NOT NULL AND $6::timestamptz <= $5 THEN 'expired' ELSE 'active' END,
    effective_source_type = 'team',
    effective_source_team_id = $2,
    activated_at = COALESCE(activated_at, $5),
    last_source_changed_at = $5,
    remark = COALESCE($3::text, remark),
    expires_at = $6::timestamptz,
    limits_json = $7::text,
    revoked_by = CASE WHEN $6::timestamptz IS NOT NULL AND $6::timestamptz <= $5 THEN COALESCE(revoked_by, $4) ELSE NULL END,
    revoked_at = CASE WHEN $6::timestamptz IS NOT NULL AND $6::timestamptz <= $5 THEN COALESCE(revoked_at, $5) ELSE NULL END,
    revoked_reason = CASE WHEN $6::timestamptz IS NOT NULL AND $6::timestamptz <= $5 THEN 'authorization_expired' ELSE NULL END,
    updated_at = $5
WHERE id = $8
`, input.resourceOwnerSystemAccountID, input.sourceTeamID, input.remark, input.actor, now, input.expiresAt, input.limitsJSON, authorizationID); err != nil {
		return fmt.Errorf("update team resource authorization: %w", err)
	}

	if err := upsertManagementTeamAuthorizationSourceTx(ctx, tx, authorizationID, input.sourceTeamID, input.actor, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
UPDATE juhe_business.resource_authorization_sources
SET status = 'superseded',
    ended_at = COALESCE(ended_at, $1),
    ended_reason = COALESCE(ended_reason, 'covered_by_team'),
    updated_at = $1
WHERE authorization_id = $2
  AND source_type = 'manual'
  AND status = 'active'
`, now, authorizationID); err != nil {
		return fmt.Errorf("supersede manual authorization source: %w", err)
	}
	return refreshManagementResourceAuthorizationEffectiveSourceTx(ctx, tx, authorizationID, input.actor, now)
}

func upsertManagementTeamAuthorizationSourceTx(ctx context.Context, tx pgx.Tx, authorizationID string, sourceTeamID string, actor string, now time.Time) error {
	var sourceID string
	err := tx.QueryRow(ctx, `
SELECT id
FROM juhe_business.resource_authorization_sources
WHERE authorization_id = $1
  AND source_type = 'team'
  AND source_team_id = $2
ORDER BY created_at DESC, id DESC
LIMIT 1
`, authorizationID, sourceTeamID).Scan(&sourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := tx.Exec(ctx, `
INSERT INTO juhe_business.resource_authorization_sources (
  id, authorization_id, source_type, source_team_id, status,
  activated_at, ended_at, ended_reason, created_by, created_at,
  revoked_by, revoked_at, updated_at
) VALUES (
  $1, $2, 'team', $3, 'active',
  $5, NULL, NULL, $4, $5,
  NULL, NULL, $5
)
`, prefixedUUID("rauthsrc"), authorizationID, sourceTeamID, actor, now.UTC()); err != nil {
			return fmt.Errorf("insert team authorization source: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("find team authorization source: %w", err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE juhe_business.resource_authorization_sources
SET status = 'active',
    activated_at = COALESCE(activated_at, $1),
    ended_at = NULL,
    ended_reason = NULL,
    revoked_by = NULL,
    revoked_at = NULL,
    updated_at = $1
WHERE id = $2
`, now.UTC(), sourceID); err != nil {
		return fmt.Errorf("reactivate team authorization source: %w", err)
	}
	return nil
}

type managementAuthorizationEffectiveSourceRefreshOptions struct {
	noActiveSourceReason              string
	preserveExpiredWhenNoActiveSource bool
	terminalStatus                    string
}

func refreshManagementResourceAuthorizationEffectiveSourceTx(ctx context.Context, tx pgx.Tx, authorizationID string, actor string, now time.Time) error {
	return refreshManagementResourceAuthorizationEffectiveSourceWithOptionsTx(ctx, tx, authorizationID, actor, now, managementAuthorizationEffectiveSourceRefreshOptions{
		preserveExpiredWhenNoActiveSource: true,
		terminalStatus:                    "revoked",
	})
}

func refreshManagementResourceAuthorizationEffectiveSourceWithOptionsTx(ctx context.Context, tx pgx.Tx, authorizationID string, actor string, now time.Time, options managementAuthorizationEffectiveSourceRefreshOptions) error {
	now = now.UTC()
	terminalStatus := options.terminalStatus
	if terminalStatus == "" {
		terminalStatus = "revoked"
	}
	var activeTeamSourceID string
	err := tx.QueryRow(ctx, `
SELECT ras.source_team_id
FROM juhe_business.resource_authorization_sources AS ras
INNER JOIN juhe_business.resource_authorizations AS ra
  ON ra.id = ras.authorization_id
INNER JOIN juhe_business.resource_authorization_grants AS trg
  ON trg.resource_type = ra.resource_type
  AND trg.resource_id = ra.resource_id
  AND trg.grantee_type = 'team'
  AND trg.grantee_team_id = ras.source_team_id
  AND trg.status = 'active'
  AND (trg.expires_at IS NULL OR trg.expires_at > $1)
WHERE ras.authorization_id = $2
  AND ras.source_type = 'team'
  AND ras.status = 'active'
ORDER BY ras.activated_at ASC, ras.created_at ASC, ras.id ASC
LIMIT 1
`, now, authorizationID).Scan(&activeTeamSourceID)
	if err == nil {
		if _, err := tx.Exec(ctx, `
UPDATE juhe_business.resource_authorizations
SET status = CASE
      WHEN expires_at IS NOT NULL AND expires_at <= $1 THEN 'expired'
      WHEN status = 'paused' THEN 'paused'
      ELSE 'active'
    END,
    effective_source_type = 'team',
    effective_source_team_id = $2,
    revoked_by = CASE WHEN expires_at IS NOT NULL AND expires_at <= $1 THEN COALESCE(revoked_by, $3) ELSE NULL END,
    revoked_at = CASE WHEN expires_at IS NOT NULL AND expires_at <= $1 THEN COALESCE(revoked_at, $1) ELSE NULL END,
    revoked_reason = CASE WHEN expires_at IS NOT NULL AND expires_at <= $1 THEN 'authorization_expired' ELSE NULL END,
    last_source_changed_at = $1,
    updated_at = $1
WHERE id = $4
`, now, activeTeamSourceID, actor, authorizationID); err != nil {
			return fmt.Errorf("refresh active team authorization source: %w", err)
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("find active team authorization source: %w", err)
	}

	var pausedTeamSourceID string
	err = tx.QueryRow(ctx, `
SELECT ras.source_team_id
FROM juhe_business.resource_authorization_sources AS ras
INNER JOIN juhe_business.resource_authorizations AS ra
  ON ra.id = ras.authorization_id
INNER JOIN juhe_business.resource_authorization_grants AS trg
  ON trg.resource_type = ra.resource_type
  AND trg.resource_id = ra.resource_id
  AND trg.grantee_type = 'team'
  AND trg.grantee_team_id = ras.source_team_id
  AND trg.status = 'paused'
WHERE ras.authorization_id = $1
  AND ras.source_type = 'team'
  AND ras.status = 'active'
ORDER BY ras.activated_at ASC, ras.created_at ASC, ras.id ASC
LIMIT 1
`, authorizationID).Scan(&pausedTeamSourceID)
	if err == nil {
		if _, err := tx.Exec(ctx, `
UPDATE juhe_business.resource_authorizations
SET status = CASE
      WHEN expires_at IS NOT NULL AND expires_at <= $1 THEN 'expired'
      ELSE 'paused'
    END,
    effective_source_type = 'team',
    effective_source_team_id = $2,
    revoked_by = CASE WHEN expires_at IS NOT NULL AND expires_at <= $1 THEN COALESCE(revoked_by, $3) ELSE NULL END,
    revoked_at = CASE WHEN expires_at IS NOT NULL AND expires_at <= $1 THEN COALESCE(revoked_at, $1) ELSE NULL END,
    revoked_reason = CASE WHEN expires_at IS NOT NULL AND expires_at <= $1 THEN 'authorization_expired' ELSE 'authorization_paused' END,
    last_source_changed_at = $1,
    updated_at = $1
WHERE id = $4
`, now, pausedTeamSourceID, actor, authorizationID); err != nil {
			return fmt.Errorf("refresh paused team authorization source: %w", err)
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("find paused team authorization source: %w", err)
	}

	var manualSourceID string
	err = tx.QueryRow(ctx, `
SELECT id
FROM juhe_business.resource_authorization_sources
WHERE authorization_id = $1
  AND source_type = 'manual'
  AND status = 'active'
ORDER BY activated_at ASC, created_at ASC, id ASC
LIMIT 1
`, authorizationID).Scan(&manualSourceID)
	if err == nil {
		if _, err := tx.Exec(ctx, `
UPDATE juhe_business.resource_authorizations
SET status = CASE
      WHEN expires_at IS NOT NULL AND expires_at <= $1 THEN 'expired'
      WHEN status = 'paused' THEN 'paused'
      ELSE 'active'
    END,
    effective_source_type = 'manual',
    effective_source_team_id = NULL,
    revoked_by = CASE WHEN expires_at IS NOT NULL AND expires_at <= $1 THEN COALESCE(revoked_by, $2) ELSE NULL END,
    revoked_at = CASE WHEN expires_at IS NOT NULL AND expires_at <= $1 THEN COALESCE(revoked_at, $1) ELSE NULL END,
    revoked_reason = CASE WHEN expires_at IS NOT NULL AND expires_at <= $1 THEN 'authorization_expired' ELSE NULL END,
    last_source_changed_at = $1,
    updated_at = $1
WHERE id = $3
`, now, actor, authorizationID); err != nil {
			return fmt.Errorf("refresh manual authorization source: %w", err)
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("find manual authorization source: %w", err)
	}

	preserveExpired := 0
	if options.preserveExpiredWhenNoActiveSource {
		preserveExpired = 1
	}
	noActiveSourceReason := strings.TrimSpace(options.noActiveSourceReason)
	hasNoActiveSourceReason := 0
	if noActiveSourceReason != "" {
		hasNoActiveSourceReason = 1
	}
	if _, err := tx.Exec(ctx, `
UPDATE juhe_business.resource_authorizations
SET status = CASE WHEN $1 = 1 AND expires_at IS NOT NULL AND expires_at <= $2 THEN 'expired' ELSE $3 END,
    effective_source_type = NULL,
    effective_source_team_id = NULL,
    revoked_by = CASE WHEN $4 = 1 THEN $5 ELSE COALESCE(revoked_by, $5) END,
    revoked_at = CASE WHEN $4 = 1 THEN $2 ELSE COALESCE(revoked_at, $2) END,
    revoked_reason = CASE
      WHEN $1 = 1 AND expires_at IS NOT NULL AND expires_at <= $2 THEN 'authorization_expired'
      WHEN $4 = 1 THEN $6
      ELSE COALESCE(revoked_reason, 'no_active_source')
    END,
    last_source_changed_at = $2,
    updated_at = $2
WHERE id = $7
`, preserveExpired, now, terminalStatus, hasNoActiveSourceReason, actor, noActiveSourceReason, authorizationID); err != nil {
		return fmt.Errorf("refresh empty authorization source: %w", err)
	}
	return nil
}

func markAllGroupAccountStatsDirtyIfPresentTx(ctx context.Context, tx pgx.Tx, reason string, now time.Time) error {
	var tableName pgtype.Text
	if err := tx.QueryRow(ctx, `SELECT to_regclass('juhe_business.group_account_stats_dirty')::text`).Scan(&tableName); err != nil {
		return fmt.Errorf("check group account stats dirty table: %w", err)
	}
	if !tableName.Valid || tableName.String == "" {
		return nil
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO juhe_business.group_account_stats_dirty (group_id, reason, updated_at)
VALUES ('__all__', $1, $2)
ON CONFLICT (group_id) DO UPDATE SET
  reason = excluded.reason,
  updated_at = excluded.updated_at
`, reason, now.UTC()); err != nil {
		return fmt.Errorf("mark all group account stats dirty: %w", err)
	}
	return nil
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
	return managementSystemTeamSummaryFromTeamRow(row, activeMemberCount)
}

func managementSystemTeamSummaryFromTeamRow(row postgresqueries.JuheBusinessSystemTeam, activeMemberCount int) port.ManagementSystemTeamSummary {
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
var _ port.ManagementSystemTeamUpdater = (*Store)(nil)
var _ port.ManagementSystemTeamMemberManager = (*Store)(nil)
