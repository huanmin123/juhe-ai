// Team mutation operations, ported from Node system-team.repository.ts
// updateSystemTeamAsync (:492-544), addSystemTeamMembersAsync (:607-714) and
// removeSystemTeamMemberAsync (:736-800). Every authorization cascade runs in
// the caller's transaction via the authz Tx variants (C7/C6/C11), the
// ?systemAccountId scope narrows both the row lookups and the CAS UPDATEs
// (C5), and committed writes fan out the group-stats dirty marker + cache
// invalidation (C9).
package systemteams

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// MutationResult mirrors SystemTeamMutationResult
// (system-team.repository.ts:79-88): id, changedFields, sparse rowPatch
// (description may be JSON null) and updatedAt.
type MutationResult struct {
	ID            string         `json:"id"`
	ChangedFields []string       `json:"changedFields"`
	RowPatch      map[string]any `json:"rowPatch"`
	UpdatedAt     string         `json:"updatedAt"`
}

// PatchOutcome mirrors SystemTeamPatchOutcome (:90-98).
type PatchOutcome struct {
	Status string // not_found|conflict|noop|updated
	Result *MutationResult
}

// AddMembersResult mirrors SystemTeamAddMembersResult (:100-105).
type AddMembersResult struct {
	ID           string         `json:"id"`
	MemberCount  int            `json:"memberCount"`
	UpdatedAt    string         `json:"updatedAt"`
	AddedMembers []MemberDetail `json:"addedMembers"`
}

// AddMembersOutcome mirrors SystemTeamAddMembersOutcome (:120-128).
type AddMembersOutcome struct {
	Status string // not_found|conflict|noop|updated
	Result *AddMembersResult
}

// RemoveMemberResult mirrors SystemTeamRemoveMemberResult (:107-112).
type RemoveMemberResult struct {
	ID              string `json:"id"`
	MemberCount     int    `json:"memberCount"`
	UpdatedAt       string `json:"updatedAt"`
	RemovedMemberID string `json:"removedMemberId"`
}

// RemoveMemberOutcome mirrors SystemTeamRemoveMemberOutcome (:130-139).
type RemoveMemberOutcome struct {
	Status string // not_found|conflict|updated
	Result *RemoveMemberResult
}

// MemberChange records the remove-member target for operation logs (Node
// outcome.removedMember :137-138).
type MemberChange struct {
	Field      string
	TargetID   string
	TargetName string
}

// JSONString is the tri-state patch field (absent | null | string) matching
// the zod `.nullable().optional()` description contract: the Node mutation
// keys off hasOwnProperty (repo :1318-1323), so an explicit null must clear
// the column while an absent key leaves it untouched.
type JSONString struct {
	Present bool
	Null    bool
	Value   string
}

func (j *JSONString) UnmarshalJSON(data []byte) error {
	j.Present = true
	j.Null = false
	j.Value = ""
	if string(data) == "null" {
		j.Null = true
		return nil
	}
	return json.Unmarshal(data, &j.Value)
}

// PatchInput mirrors the update-team zod schema (normalized). Description is
// a value-type tri-state so an explicit JSON null is distinguishable from an
// absent key (Node hasOwnProperty, repo :1318-1323).
type PatchInput struct {
	Name              *string
	Description       JSONString
	Status            *string
	ExpectedUpdatedAt string
}

// canonicalExpectedVersion mirrors requiredSystemTeamPatchVersion
// (:1392-1395) plus the rfc3339InstantSchema canonicalization the routes
// apply (system-teams.routes.ts:42): empty → 缺少团队版本, timezone-less or
// malformed → the verbatim nextSystemTeamUpdatedAt error (C10).
func canonicalExpectedVersion(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", &ValidationError{Message: "缺少团队版本"}
	}
	canonical, ok := CanonicalizeInstant(trimmed)
	if !ok {
		return "", &ValidationError{Message: "系统团队 updatedAt 必须是带 Z 或数值 offset 的 RFC3339 时间：" + trimmed}
	}
	return canonical, nil
}

// NextVersion mirrors nextSystemTeamUpdatedAt (:1397-1401):
// max(now, expected + 1ms) rendered as canonical UTC milliseconds.
func NextVersion(expectedUpdatedAt string, now time.Time) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, expectedUpdatedAt)
	if err != nil {
		return "", &ValidationError{Message: "系统团队 updatedAt 必须是带 Z 或数值 offset 的 RFC3339 时间：" + expectedUpdatedAt}
	}
	floor := parsed.Add(time.Millisecond)
	nowMs := now.UTC().Truncate(time.Millisecond)
	if nowMs.Before(floor) {
		return floor.UTC().Format(canonicalInstantLayout), nil
	}
	return nowMs.Format(canonicalInstantLayout), nil
}

// teamScopeClause mirrors systemTeamPatchScope (:1376-1390): the
// ?systemAccountId (or self) membership narrowing appended to row lookups and
// CAS UPDATEs alike (C5). The scoped account id must be bound as the next
// positional parameter after the clause is appended.
func (s *Store) teamScopeClause() string {
	return ` AND EXISTS (
		SELECT 1
		FROM ` + s.table("system_team_members") + ` scoped_members
		WHERE scoped_members.team_id = ` + s.table("system_teams") + `.id
			AND scoped_members.system_account_id = ?
			AND scoped_members.status = 'active'
	)`
}

// normalizeSystemAccountIDs mirrors normalizeSystemAccountIds (:1448-1469):
// trim per item, empty → 团队成员 ID 不能为空, duplicate → 团队成员不能重复
// (C12).
func normalizeSystemAccountIDs(values []string) ([]string, error) {
	ids := []string{}
	seen := make(map[string]bool, len(values))
	for _, item := range values {
		id := strings.TrimSpace(item)
		if id == "" {
			return nil, &ValidationError{Message: "团队成员 ID 不能为空"}
		}
		if seen[id] {
			return nil, &ValidationError{Message: "团队成员不能重复"}
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}

// Patch mirrors updateSystemTeamAsync (:492-544): canonical expectedUpdatedAt
// optimistic lock, only-changed columns, scoped row lookup + scoped CAS
// UPDATE, and the status-transition authorization cascade inside the same
// transaction.
func (s *Store) Patch(ctx context.Context, id string, input PatchInput, access AccessScope) (*PatchOutcome, error) {
	ctx = ensureCtx(ctx)
	actor, err := access.ActorID()
	if err != nil {
		return nil, err
	}
	expectedUpdatedAt, err := canonicalExpectedVersion(input.ExpectedUpdatedAt)
	if err != nil {
		return nil, err
	}
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
	// Scoped row lookup (findSystemTeamPatchRowForAccessAsync :965-986):
	// INNER-JOIN visibility rendered as EXISTS; postgres takes the row lock.
	query := `SELECT name, description, status, updated_at FROM ` + s.table("system_teams") + ` WHERE id = ?`
	scopeClause := s.teamScopeClause()
	scopeArgs := []any{}
	if scopedID := access.ScopedID(); scopedID != "" {
		query += scopeClause
		scopeArgs = append(scopeArgs, scopedID)
	}
	if s.pg {
		query += " FOR UPDATE"
	}
	if err := tx.QueryRowContext(ctx, s.bind(query), append([]any{id}, scopeArgs...)...).Scan(&row.name, &row.description, &row.status, &row.updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &PatchOutcome{Status: "not_found"}, nil
		}
		return nil, err
	}
	if row.updatedAt != expectedUpdatedAt {
		return &PatchOutcome{Status: "conflict"}, nil
	}

	// buildSystemTeamPatchMutation (:1325-1355): field order name,
	// description, status; only-changed columns with sparse rowPatch.
	assignments := []string{}
	values := []any{}
	changedFields := []string{}
	rowPatch := map[string]any{}
	if input.Name != nil {
		name, err := normalizeName(*input.Name)
		if err != nil {
			return nil, err
		}
		if name != row.name {
			assignments = append(assignments, "name = ?")
			values = append(values, name)
			changedFields = append(changedFields, "name")
			rowPatch["name"] = name
		}
	}
	if input.Description.Present {
		// normalizeSystemTeamDescription (:1426-1435): explicit null → null
		// (column cleared); a string value is trimmed.
		var description *string
		if !input.Description.Null {
			value := input.Description.Value
			description, err = normalizeDescription(&value)
			if err != nil {
				return nil, err
			}
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
			values = append(values, nullableString(description))
			changedFields = append(changedFields, "description")
			if description != nil {
				rowPatch["description"] = *description
			} else {
				rowPatch["description"] = nil // JSON null (Node description: string|null)
			}
		}
	}
	if input.Status != nil {
		status, err := normalizeStatus(input.Status, row.status)
		if err != nil {
			return nil, err
		}
		if status != row.status {
			assignments = append(assignments, "status = ?")
			values = append(values, status)
			changedFields = append(changedFields, "status")
			rowPatch["status"] = status
		}
	}
	if len(assignments) == 0 {
		// systemTeamPatchSuccess('noop', …) (:1357-1374): the unchanged team
		// version rides back as updatedAt.
		return &PatchOutcome{Status: "noop", Result: &MutationResult{
			ID: id, ChangedFields: changedFields, RowPatch: rowPatch, UpdatedAt: expectedUpdatedAt,
		}}, nil
	}
	newUpdatedAt, err := NextVersion(expectedUpdatedAt, s.now())
	if err != nil {
		return nil, err
	}
	updateQuery := `UPDATE ` + s.table("system_teams") + `
		SET ` + strings.Join(assignments, ", ") + `, updated_at = ?
		WHERE id = ? AND updated_at = ?`
	updateArgs := append(append([]any{}, values...), newUpdatedAt, id, expectedUpdatedAt)
	if scopedID := access.ScopedID(); scopedID != "" {
		updateQuery += scopeClause
		updateArgs = append(updateArgs, scopedID)
	}
	result, err := tx.ExecContext(ctx, s.bind(updateQuery), updateArgs...)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "duplicate key") {
			return nil, &ValidationError{Message: "团队名称已存在"}
		}
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return &PatchOutcome{Status: "conflict"}, nil
	}

	// Authorization cascade on status transition (Node :525-533) — same
	// transaction, real actor, the new team version as cascade clock.
	authorizationChanged := false
	if s.authz != nil && changedFieldsContains(changedFields, "status") {
		if row.status != "disabled" && rowPatch["status"] == "disabled" {
			if err := s.authz.RevokeAllTeamSourcesTx(ctx, tx, id, actor, newUpdatedAt, "team_disabled"); err != nil {
				return nil, err
			}
			authorizationChanged = true
		}
		if row.status == "disabled" && rowPatch["status"] == "active" {
			if err := s.authz.ReactivateTeamGrantsTx(ctx, tx, id, actor, newUpdatedAt); err != nil {
				return nil, err
			}
			authorizationChanged = true
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if authorizationChanged {
		// Node :536-539.
		if err := s.afterCommit(ctx, "team_authorization_changed"); err != nil {
			return nil, err
		}
	}
	return &PatchOutcome{Status: "updated", Result: &MutationResult{
		ID: id, ChangedFields: changedFields, RowPatch: rowPatch, UpdatedAt: newUpdatedAt,
	}}, nil
}

func changedFieldsContains(fields []string, field string) bool {
	for _, item := range fields {
		if item == field {
			return true
		}
	}
	return false
}

// AddMembers mirrors addSystemTeamMembersAsync (:607-714): batch cap 20,
// in-batch duplicate rejection, cap computed AFTER excluding already-active
// members, noop success when everything is already active, scoped team lookup
// + scoped CAS team UPDATE, revival/insert with member_role='member' and the
// team-grant fan-out inside the same transaction (C6/C12).
func (s *Store) AddMembers(ctx context.Context, teamID string, systemAccountIDs []string, expectedUpdatedAt string, access AccessScope) (*AddMembersOutcome, error) {
	ctx = ensureCtx(ctx)
	actor, err := access.ActorID()
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeSystemAccountIDs(systemAccountIDs)
	if err != nil {
		return nil, err
	}
	if len(normalized) == 0 {
		return nil, &ValidationError{Message: "请选择团队成员"}
	}
	if len(normalized) > MaxMemberBatchSize {
		return nil, &ValidationError{Message: "单次最多添加 20 个团队成员"}
	}
	expectedUpdatedAt, err = canonicalExpectedVersion(expectedUpdatedAt)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// findSystemTeamMemberMutationTeamAsync(tx, teamId, access, true)
	// (:988-1007): scope clause + active-only + row lock.
	query := `SELECT name, status, updated_at FROM ` + s.table("system_teams") + ` WHERE id = ? AND status = 'active'`
	args := []any{teamID}
	if scopedID := access.ScopedID(); scopedID != "" {
		scopeClause := s.teamScopeClause()
		query += scopeClause
		args = append(args, scopedID)
	}
	if s.pg {
		query += " FOR UPDATE"
	}
	var teamName, teamStatus, teamUpdatedAt string
	if err := tx.QueryRowContext(ctx, s.bind(query), args...).Scan(&teamName, &teamStatus, &teamUpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &AddMembersOutcome{Status: "not_found"}, nil
		}
		return nil, err
	}
	if teamUpdatedAt != expectedUpdatedAt {
		return &AddMembersOutcome{Status: "conflict"}, nil
	}
	if teamStatus != "active" {
		return &AddMembersOutcome{Status: "not_found"}, nil
	}

	// Existing active members, ceiling members+1 (:620-629).
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT system_account_id FROM `+s.table("system_team_members")+`
		WHERE team_id = ? AND status = 'active'
		ORDER BY system_account_id ASC LIMIT ?`), teamID, MaxMembersPerTeam+1)
	if err != nil {
		return nil, err
	}
	existingActive := map[string]bool{}
	for rows.Next() {
		var accountID string
		if err := rows.Scan(&accountID); err != nil {
			rows.Close()
			return nil, err
		}
		existingActive[accountID] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(existingActive) > MaxMembersPerTeam {
		return nil, &ValidationError{Message: "授权团队最多支持 20 个成员，请先移除部分成员后再添加"}
	}
	// Cap AFTER excluding already-active members (:631-633).
	next := make([]string, 0, len(normalized))
	for _, accountID := range normalized {
		if !existingActive[accountID] {
			next = append(next, accountID)
		}
	}
	if len(existingActive)+len(next) > MaxMembersPerTeam {
		return nil, &ValidationError{Message: "授权团队最多支持 20 个成员，请先移除部分成员后再添加"}
	}
	if len(next) == 0 {
		// Everything requested is already active → noop success with the
		// unchanged team version (:635-641).
		return &AddMembersOutcome{Status: "noop", Result: &AddMembersResult{
			ID: teamID, MemberCount: len(existingActive), UpdatedAt: teamUpdatedAt, AddedMembers: []MemberDetail{},
		}}, nil
	}

	// All requested accounts must exist and be active (:642-650).
	displayNames := map[string]sql.NullString{}
	for _, accountID := range next {
		var displayName sql.NullString
		var status string
		err := tx.QueryRowContext(ctx, s.bind(`SELECT display_name, status FROM `+s.table("system_accounts")+` WHERE id = ?`), accountID).
			Scan(&displayName, &status)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && status != "active") {
			return nil, &ValidationError{Message: "团队成员不存在或已停用"}
		}
		if err != nil {
			return nil, err
		}
		displayNames[accountID] = displayName
	}

	newUpdatedAt, err := NextVersion(expectedUpdatedAt, s.now())
	if err != nil {
		return nil, err
	}
	// Scoped team CAS (:663-670).
	teamUpdate := `UPDATE ` + s.table("system_teams") + `
		SET updated_at = ?
		WHERE id = ? AND status = 'active' AND updated_at = ?`
	teamArgs := []any{newUpdatedAt, teamID, expectedUpdatedAt}
	if scopedID := access.ScopedID(); scopedID != "" {
		scopeClause := s.teamScopeClause()
		teamUpdate += scopeClause
		teamArgs = append(teamArgs, scopedID)
	}
	result, err := tx.ExecContext(ctx, s.bind(teamUpdate), teamArgs...)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return &AddMembersOutcome{Status: "conflict"}, nil
	}

	added := []MemberDetail{}
	for _, accountID := range next {
		// Latest member row per account (:651-661, :672-688).
		var existingID, existingStatus string
		err := tx.QueryRowContext(ctx, s.bind(`SELECT id, status FROM `+s.table("system_team_members")+`
			WHERE team_id = ? AND system_account_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`), teamID, accountID).
			Scan(&existingID, &existingStatus)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		memberID := "teammem_" + randomHex()
		if err == nil && existingID != "" {
			memberID = existingID
			if existingStatus == "active" {
				continue // unreachable via `next`, kept as a guard
			}
			if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("system_team_members")+`
				SET status = 'active', joined_at = ?, removed_at = NULL, updated_at = ? WHERE id = ?`),
				newUpdatedAt, newUpdatedAt, memberID); err != nil {
				return nil, err
			}
		} else {
			if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("system_team_members")+`
				(id, team_id, system_account_id, member_role, status, joined_at, removed_at, created_by, created_at, updated_at)
				VALUES (?, ?, ?, 'member', 'active', ?, NULL, ?, ?, ?)`),
				memberID, teamID, accountID, newUpdatedAt, actor, newUpdatedAt, newUpdatedAt); err != nil {
				return nil, err
			}
		}
		name := displayNames[accountID]
		detail := MemberDetail{ID: memberID, SystemAccountID: accountID, JoinedAt: newUpdatedAt}
		if name.Valid {
			displayName := name.String
			detail.SystemAccountName = &displayName
		}
		added = append(added, detail)
	}

	// Team grant fan-out to the newly added members, same transaction
	// (:696; C6).
	if s.authz != nil {
		if err := s.authz.ApplyActiveTeamGrantsToMembersTx(ctx, tx, teamID, next, actor, newUpdatedAt); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// Node :709-711.
	if err := s.afterCommit(ctx, "team_members_changed"); err != nil {
		return nil, err
	}
	return &AddMembersOutcome{Status: "updated", Result: &AddMembersResult{
		ID: teamID, MemberCount: len(existingActive) + len(added), UpdatedAt: newUpdatedAt, AddedMembers: added,
	}}, nil
}

// RemoveMember mirrors removeSystemTeamMemberAsync (:736-800): scoped team
// lookup + scoped CAS team UPDATE, soft member delete, and the team-source
// revocation for the removed member inside the SAME transaction (C11).
func (s *Store) RemoveMember(ctx context.Context, teamID, memberID, expectedUpdatedAt string, access AccessScope) (*RemoveMemberOutcome, *MemberChange, error) {
	ctx = ensureCtx(ctx)
	actor, err := access.ActorID()
	if err != nil {
		return nil, nil, err
	}
	expectedUpdatedAt, err = canonicalExpectedVersion(expectedUpdatedAt)
	if err != nil {
		return nil, nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	// findSystemTeamMemberMutationTeamAsync(tx, teamId, access) (:988-1007).
	query := `SELECT name, status, updated_at FROM ` + s.table("system_teams") + ` WHERE id = ?`
	args := []any{teamID}
	if scopedID := access.ScopedID(); scopedID != "" {
		scopeClause := s.teamScopeClause()
		query += scopeClause
		args = append(args, scopedID)
	}
	if s.pg {
		query += " FOR UPDATE"
	}
	var teamName, teamStatus, teamUpdatedAt string
	if err := tx.QueryRowContext(ctx, s.bind(query), args...).Scan(&teamName, &teamStatus, &teamUpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &RemoveMemberOutcome{Status: "not_found"}, nil, nil
		}
		return nil, nil, err
	}
	if teamUpdatedAt != expectedUpdatedAt {
		return &RemoveMemberOutcome{Status: "conflict"}, nil, nil
	}
	// Member lookup joins accounts for the operation-log display name
	// (:744-753).
	var accountID string
	var displayName sql.NullString
	err = tx.QueryRowContext(ctx, s.bind(`SELECT m.system_account_id, a.display_name
		FROM `+s.table("system_team_members")+` m
		INNER JOIN `+s.table("system_accounts")+` a ON a.id = m.system_account_id
		WHERE m.id = ? AND m.team_id = ? AND m.status = 'active' LIMIT 1`), memberID, teamID).Scan(&accountID, &displayName)
	if errors.Is(err, sql.ErrNoRows) {
		return &RemoveMemberOutcome{Status: "not_found"}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	// Active member rows bound the reported memberCount (:755-761).
	activeMemberRows := 0
	countRows, err := tx.QueryContext(ctx, s.bind(`SELECT system_account_id FROM `+s.table("system_team_members")+`
		WHERE team_id = ? AND status = 'active' ORDER BY system_account_id ASC LIMIT ?`), teamID, MaxMembersPerTeam+1)
	if err != nil {
		return nil, nil, err
	}
	for countRows.Next() {
		if err := countRows.Scan(new(string)); err != nil {
			countRows.Close()
			return nil, nil, err
		}
		activeMemberRows++
	}
	countRows.Close()
	if err := countRows.Err(); err != nil {
		return nil, nil, err
	}
	newUpdatedAt, err := NextVersion(expectedUpdatedAt, s.now())
	if err != nil {
		return nil, nil, err
	}
	// Scoped team CAS (:763-770).
	teamUpdate := `UPDATE ` + s.table("system_teams") + `
		SET updated_at = ?
		WHERE id = ? AND updated_at = ?`
	teamArgs := []any{newUpdatedAt, teamID, expectedUpdatedAt}
	if scopedID := access.ScopedID(); scopedID != "" {
		scopeClause := s.teamScopeClause()
		teamUpdate += scopeClause
		teamArgs = append(teamArgs, scopedID)
	}
	result, err := tx.ExecContext(ctx, s.bind(teamUpdate), teamArgs...)
	if err != nil {
		return nil, nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return &RemoveMemberOutcome{Status: "conflict"}, nil, nil
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("system_team_members")+`
		SET status = 'removed', removed_at = ?, updated_at = ? WHERE id = ? AND team_id = ? AND status = 'active'`),
		newUpdatedAt, newUpdatedAt, memberID, teamID); err != nil {
		return nil, nil, err
	}
	// Same-transaction revocation (:777; C11): a failure here rolls the
	// member delete and the team version bump back.
	if s.authz != nil {
		if err := s.authz.RevokeTeamSourcesForMemberTx(ctx, tx, teamID, accountID, actor, newUpdatedAt); err != nil {
			return nil, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	// Node :795-798.
	if err := s.afterCommit(ctx, "team_members_changed"); err != nil {
		return nil, nil, err
	}
	memberCount := activeMemberRows - 1
	if memberCount < 0 {
		memberCount = 0
	}
	change := &MemberChange{Field: "member", TargetID: accountID}
	if displayName.Valid {
		change.TargetName = displayName.String
	}
	return &RemoveMemberOutcome{Status: "updated", Result: &RemoveMemberResult{
		ID: teamID, MemberCount: memberCount, UpdatedAt: newUpdatedAt, RemovedMemberID: memberID,
	}}, change, nil
}
