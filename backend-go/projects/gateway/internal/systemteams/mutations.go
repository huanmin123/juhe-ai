// Team mutation operations: update (with grant-source cascade on status
// change), member add (batch, cap, revival), member remove (soft delete).
package systemteams

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
)

// PatchInput mirrors the update-team zod schema (normalized).
type PatchInput struct {
	Name              *string
	Description       *string
	Status            *string
	ExpectedUpdatedAt string
}

// NextVersion mirrors nextSystemTeamUpdatedAt.
func NextVersion(current string, now time.Time) string {
	parsed, err := time.Parse(time.RFC3339Nano, current)
	if err != nil {
		return now.UTC().Format(time.RFC3339Nano)
	}
	floor := parsed.Add(time.Millisecond)
	if now.Before(floor) {
		return floor.UTC().Format(time.RFC3339Nano)
	}
	return now.UTC().Format(time.RFC3339Nano)
}

// Patch mirrors updateSystemTeamAsync: expectedUpdatedAt optimistic lock,
// only-changed columns, disable → revoke all team sources, enable →
// reactivate team grants (authorization cascade via the authz store).
func (s *Store) Patch(ctx context.Context, id string, input PatchInput, access AccessScope) (*PatchOutcome, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var row struct {
		name        string
		description sql.NullString
		status      string
		updatedAt   string
	}
	query := `SELECT name, description, status, updated_at FROM ` + s.table("system_teams") + ` WHERE id = ?`
	if s.pg {
		query += " FOR UPDATE"
	}
	if err := tx.QueryRowContext(ctx, s.bind(query), id).Scan(&row.name, &row.description, &row.status, &row.updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &PatchOutcome{Status: "not_found"}, nil
		}
		return nil, err
	}
	if row.updatedAt != input.ExpectedUpdatedAt {
		return &PatchOutcome{Status: "conflict"}, nil
	}

	assignments := []string{}
	args := []any{}
	if input.Name != nil {
		name, err := normalizeName(*input.Name)
		if err != nil {
			return nil, err
		}
		if name != row.name {
			assignments = append(assignments, "name = ?")
			args = append(args, name)
		}
	}
	if input.Description != nil {
		description, err := normalizeDescription(input.Description)
		if err != nil {
			return nil, err
		}
		current := ""
		if row.description.Valid {
			current = row.description.String
		}
		next := ""
		if description != nil {
			next = *description
		}
		if current != next {
			assignments = append(assignments, "description = ?")
			args = append(args, nullableString(description))
		}
	}
	if input.Status != nil {
		if *input.Status != row.status {
			assignments = append(assignments, "status = ?")
			args = append(args, *input.Status)
		}
	}
	if len(assignments) == 0 {
		return &PatchOutcome{Status: "noop"}, nil
	}
	newUpdatedAt := NextVersion(row.updatedAt, s.now())
	assignments = append(assignments, "updated_at = ?")
	args = append(args, newUpdatedAt, id, input.ExpectedUpdatedAt)
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("system_teams")+`
		SET `+strings.Join(assignments, ", ")+` WHERE id = ? AND updated_at = ?`), args...)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "duplicate key") {
			return nil, &ValidationError{Message: "团队名称已存在"}
		}
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return &PatchOutcome{Status: "conflict"}, nil
	}

	// Authorization cascade on status transition (Node revokeAllTeamSources /
	// reactivateTeamGrantSources). Team grants fan out through the authz
	// runtime state machine.
	if s.authz != nil && input.Status != nil && row.status != *input.Status {
		if *input.Status == "disabled" {
			if err := s.authz.RevokeAllTeamSourcesTx(ctx, tx, id, "team_disabled"); err != nil {
				return nil, err
			}
		} else if row.status == "disabled" && *input.Status == "active" {
			if err := s.authz.ReactivateTeamGrants(ctx, id); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &PatchOutcome{Status: "updated"}, nil
}

// AddMembers mirrors addSystemTeamMembersAsync: active-team lock, member cap
// 20, dedupe, reactivation of previously removed rows, team version bump.
func (s *Store) AddMembers(ctx context.Context, teamID string, systemAccountIDs []string, expectedUpdatedAt string, access AccessScope) (*PatchOutcome, *[]MemberDetail, error) {
	ctx = ensureCtx(ctx)
	if len(systemAccountIDs) > MaxMemberBatchSize {
		return &PatchOutcome{Status: "conflict"}, nil, &ValidationError{Message: "单次最多添加 20 个团队成员"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	var teamName, teamStatus, teamUpdatedAt string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT name, status, updated_at FROM `+s.table("system_teams")+` WHERE id = ?`), teamID).
		Scan(&teamName, &teamStatus, &teamUpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return &PatchOutcome{Status: "not_found"}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if teamUpdatedAt != expectedUpdatedAt {
		return &PatchOutcome{Status: "conflict"}, nil, nil
	}
	if teamStatus != "active" {
		return &PatchOutcome{Status: "not_found"}, nil, nil
	}

	// Dedupe within batch and against existing active members.
	seen := map[string]bool{}
	normalized := []string{}
	for _, id := range systemAccountIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		normalized = append(normalized, trimmed)
	}
	if len(normalized) == 0 {
		return &PatchOutcome{Status: "conflict"}, nil, &ValidationError{Message: "请至少选择一个团队成员"}
	}
	var activeCount int
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM `+s.table("system_team_members")+`
		WHERE team_id = ? AND status = 'active'`), teamID).Scan(&activeCount); err != nil {
		return nil, nil, err
	}
	if activeCount+len(normalized) > MaxMembersPerTeam {
		return &PatchOutcome{Status: "conflict"}, nil, &ValidationError{Message: "团队已满员"}
	}

	// All requested accounts must exist and be active.
	for _, accountID := range normalized {
		var status string
		err := tx.QueryRowContext(ctx, s.bind(`SELECT status FROM `+s.table("system_accounts")+` WHERE id = ?`), accountID).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && status != "active") {
			return &PatchOutcome{Status: "conflict"}, nil, &ValidationError{Message: "团队成员不存在或已停用"}
		}
		if err != nil {
			return nil, nil, err
		}
	}

	newUpdatedAt := NextVersion(teamUpdatedAt, s.now())
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("system_teams")+`
		SET updated_at = ? WHERE id = ? AND status = 'active' AND updated_at = ?`),
		newUpdatedAt, teamID, expectedUpdatedAt)
	if err != nil {
		return nil, nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return &PatchOutcome{Status: "conflict"}, nil, nil
	}

	added := []MemberDetail{}
	for _, accountID := range normalized {
		var existingID, existingStatus string
		err := tx.QueryRowContext(ctx, s.bind(`SELECT id, status FROM `+s.table("system_team_members")+`
			WHERE team_id = ? AND system_account_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`), teamID, accountID).
			Scan(&existingID, &existingStatus)
		if err != nil && !strings.Contains(err.Error(), "no rows") {
			return nil, nil, err
		}
		if existingID != "" && existingStatus == "active" {
			continue // already active: not added again
		}
		now := newUpdatedAt
		memberID := "teammem_" + randomHex()
		if existingID != "" {
			_, err = tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("system_team_members")+`
				SET status = 'active', joined_at = ?, removed_at = NULL, updated_at = ? WHERE id = ?`),
				now, now, existingID)
			if err != nil {
				return nil, nil, err
			}
			memberID = existingID
		} else {
			_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("system_team_members")+`
				(id, team_id, system_account_id, status, joined_at, created_at, updated_at)
				VALUES (?, ?, ?, 'active', ?, ?, ?)`), memberID, teamID, accountID, now, now, now)
			if err != nil {
				return nil, nil, err
			}
		}
		var displayName string
		_ = tx.QueryRowContext(ctx, s.bind(`SELECT COALESCE(display_name,'') FROM `+s.table("system_accounts")+` WHERE id = ?`), accountID).Scan(&displayName)
		added = append(added, MemberDetail{ID: memberID, SystemAccountID: accountID, DisplayName: displayName, JoinedAt: now})
	}

	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	// Team grant application to newly added members is owned by the M04
	// wiring (applyActiveTeamGrantsToMember) and hooks in via the authz store.
	outcome := &PatchOutcome{Status: "updated"}
	if len(added) == 0 {
		outcome.Status = "noop"
	}
	return outcome, &added, nil
}

// RemoveMember mirrors removeSystemTeamMemberAsync: soft delete + team
// version bump + grant-source revocation for the member via the authz store.
func (s *Store) RemoveMember(ctx context.Context, teamID, memberID, expectedUpdatedAt string, access AccessScope) (*PatchOutcome, *MemberChange, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	var teamName, teamUpdatedAt string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT name, updated_at FROM `+s.table("system_teams")+` WHERE id = ?`), teamID).
		Scan(&teamName, &teamUpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return &PatchOutcome{Status: "not_found"}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if teamUpdatedAt != expectedUpdatedAt {
		return &PatchOutcome{Status: "conflict"}, nil, nil
	}
	var accountID string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT system_account_id FROM `+s.table("system_team_members")+`
		WHERE id = ? AND team_id = ? AND status = 'active'`), memberID, teamID).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return &PatchOutcome{Status: "not_found"}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	newUpdatedAt := NextVersion(teamUpdatedAt, s.now())
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("system_teams")+`
		SET updated_at = ? WHERE id = ? AND updated_at = ?`), newUpdatedAt, teamID, expectedUpdatedAt)
	if err != nil {
		return nil, nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return &PatchOutcome{Status: "conflict"}, nil, nil
	}
	now := newUpdatedAt
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("system_team_members")+`
		SET status = 'removed', removed_at = ?, updated_at = ? WHERE id = ? AND team_id = ? AND status = 'active'`),
		now, now, memberID, teamID); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	if s.authz != nil {
		// Grant-source revocation for the removed member (team sources).
		if err := s.authz.RevokeTeamSourcesForMember(ctx, teamID, accountID); err != nil {
			return nil, nil, err
		}
	}
	change := &MemberChange{Field: "member", TargetID: accountID}
	return &PatchOutcome{Status: "updated"}, change, nil
}

var _ = time.Now
var _ = authz.Conflict{}
