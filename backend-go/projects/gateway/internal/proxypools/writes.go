package proxypools

// Write half of the pool family (代理池设计 §8.1): create, management patch
// (updated_at CAS + full member replacement with two-phase priority
// normalization) and the guarded delete. All member/pool mutations run in ONE
// transaction; removed members lose their leases in the same transaction
// while kept members keep theirs untouched; the committed-write runtime
// invalidation and the Redis member-health cleanup run strictly after commit.

import (
	"context"
	"database/sql"
	"strings"
)

// memberInput carries one parsed member entry (strict field set).
type memberInput struct {
	ProxyProfileID string
	Weight         int
	Enabled        bool
}

// createInput carries the parsed create body (strict field set).
type createInput struct {
	Name        string
	Description *string
	Strategy    string
	Enabled     bool
	Members     []memberInput
}

// validateMembers enforces §8.1: non-empty profile ids, no duplicates,
// weight 1..100, count ≤ 50.
func validateMembers(members []memberInput) error {
	if len(members) > memberLimit {
		return &MemberLimitError{}
	}
	seen := map[string]bool{}
	for _, member := range members {
		if strings.TrimSpace(member.ProxyProfileID) == "" {
			return &MemberInvalidError{}
		}
		if seen[member.ProxyProfileID] {
			return &MemberDuplicateError{}
		}
		seen[member.ProxyProfileID] = true
		if member.Weight < 1 || member.Weight > 100 {
			return &MemberInvalidError{ProfileID: member.ProxyProfileID}
		}
	}
	return nil
}

// rowQuerier abstracts the query entries shared by *sql.DB and *sql.Tx (both
// satisfy it).
type rowQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// rowScanner is the query-result surface the member scans consume.
type rowScanner interface {
	Next() bool
	Scan(dest ...any) error
	Close() error
	Err() error
}

// validateMembersExist checks every member against proxy_profiles inside the
// write transaction (§8.1 成员代理存在；停用代理允许入池，由成员行/健康投影
// 控制可分配性).
func (s *Store) validateMembersExist(ctx context.Context, querier rowQuerier, members []memberInput) error {
	if len(members) == 0 {
		return nil
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, member := range members {
		if !seen[member.ProxyProfileID] {
			seen[member.ProxyProfileID] = true
			ids = append(ids, member.ProxyProfileID)
		}
	}
	query := `SELECT id FROM ` + s.table("proxy_profiles") + ` WHERE id IN (` + placeholders(len(ids)) + `)`
	rows, err := querier.QueryContext(ctx, s.bind(query), idsToAny(ids)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		found[id] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if !found[id] {
			return &MemberInvalidError{ProfileID: id}
		}
	}
	return nil
}

// insertPoolMembers writes the member rows with their final priorities
// (0..n-1 by list position) plus the given timestamps.
func insertPoolMembers(ctx context.Context, tx *sql.Tx, bind func(string) string, poolID string, members []memberInput, now string) error {
	for index, member := range members {
		query := `
			INSERT INTO juhe_business.proxy_pool_members (pool_id, proxy_profile_id, priority, weight, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`
		if _, err := tx.ExecContext(ctx, bind(query), poolID, member.ProxyProfileID, index, member.Weight, member.Enabled, now, now); err != nil {
			return err
		}
	}
	return nil
}

// Create mirrors the §8.1 POST: one transaction inserts the pool and its
// members (priority 0..n-1 by list position); the runtime invalidation
// publishes strictly after the commit succeeded.
func (s *Store) Create(ctx context.Context, input createInput, systemAccountID string) (*PoolDetail, error) {
	if strings.TrimSpace(input.Name) == "" {
		return nil, ErrNameRequired
	}
	if input.Strategy != StrategyRoundRobin && input.Strategy != StrategyWeighted {
		return nil, ErrStrategyInvalid
	}
	if err := validateMembers(input.Members); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := s.validateMembersExist(ctx, tx, input.Members); err != nil {
		return nil, err
	}
	poolID := s.newID("proxy_pool")
	now := s.now().UTC().Format(revisionLayout)
	insertQuery := `
		INSERT INTO ` + s.table("proxy_pools") + ` (id, system_account_id, name, description, strategy, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	if _, err := tx.ExecContext(ctx, s.bind(insertQuery),
		poolID, systemAccountID, input.Name, nullIfEmpty(input.Description), input.Strategy, input.Enabled, now, now); err != nil {
		if isPoolNameDuplicate(err) {
			return nil, &PoolNameDuplicateError{Name: input.Name}
		}
		return nil, err
	}
	if err := insertPoolMembers(ctx, tx, s.bind, poolID, input.Members, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.notifyRuntimeUpdated()
	return s.GetDetail(ctx, poolID)
}

// patchInput carries the parsed patch body (strict field set).
type patchInput struct {
	Name              *string
	Description       *string
	HasDescription    bool
	Strategy          *string
	Enabled           *bool
	Members           []memberInput
	HasMembers        bool
	ExpectedUpdatedAt string
}

// patchOutcome mirrors the mutation envelope plus the member change summary
// used by the operation log.
type patchOutcome struct {
	ID        string
	UpdatedAt string
	Changed   bool
	Values    map[string]any
	Name      string
	Before    map[string]any
	After     map[string]any
	// MembersSummary renders the §8.1 变更摘要 (counts only, no ids beyond
	// the add/remove counts) — no host/port/credentials.
	MembersSummary    string
	RemovedProfileIDs []string
}

type poolRow struct {
	id          string
	name        string
	description *string
	strategy    string
	enabled     bool
	updatedAt   string
}

func (s *Store) loadPoolRow(ctx context.Context, querier rowQuerier, id string, lockClause string) (*poolRow, error) {
	var (
		row         poolRow
		description sql.NullString
		updatedAt   any
		enabled     any
	)
	query := `
		SELECT id, name, description, strategy, enabled, ` + s.revisionSelectExpression() + `
		FROM ` + s.table("proxy_pools") + `
		WHERE id = ?` + lockClause
	err := querier.QueryRowContext(ctx, s.bind(query), id).Scan(
		&row.id, &row.name, &description, &row.strategy, &enabled, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row.description = nullStringPtr(description)
	row.enabled = boolFromValue(enabled)
	row.updatedAt = toTextValue(updatedAt)
	return &row, nil
}

type memberState struct {
	profileID string
	priority  int
	weight    int
	enabled   bool
}

// loadMemberStates returns the current member rows ordered by priority.
func (s *Store) loadMemberStates(ctx context.Context, querier rowQuerier, poolID string) ([]memberState, error) {
	query := `
		SELECT proxy_profile_id, priority, weight, enabled
		FROM ` + s.table("proxy_pool_members") + `
		WHERE pool_id = ?
		ORDER BY priority ASC`
	rows, err := querier.QueryContext(ctx, s.bind(query), poolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []memberState{}
	for rows.Next() {
		var member memberState
		var priority, weight any
		var enabled any
		if err := rows.Scan(&member.profileID, &priority, &weight, &enabled); err != nil {
			return nil, err
		}
		member.priority = intFromValue(priority)
		member.weight = intFromValue(weight)
		member.enabled = boolFromValue(enabled)
		members = append(members, member)
	}
	return members, rows.Err()
}

// memberReplacePlan is the diff between the current member set and the
// replacement list (§8.1 保留交集原地更新，差集增删).
type memberReplacePlan struct {
	toAdd    []memberInput
	toRemove []string
	kept     []memberInput // new spec for surviving profiles
	changed  bool
	// removedProfileIDs mirrors toRemove (kept separate for readability).
	removedProfileIDs []string
}

// planMemberReplace diffs the current member states against the normalized
// replacement list. Priority comes from list position, so a pure reorder
// also counts as a change.
func planMemberReplace(current []memberState, next []memberInput) memberReplacePlan {
	currentByProfile := map[string]memberState{}
	for _, member := range current {
		currentByProfile[member.profileID] = member
	}
	nextByProfile := map[string]memberInput{}
	for _, member := range next {
		nextByProfile[member.ProxyProfileID] = member
	}
	plan := memberReplacePlan{}
	for index, member := range next {
		if _, ok := currentByProfile[member.ProxyProfileID]; !ok {
			plan.toAdd = append(plan.toAdd, memberInput{
				ProxyProfileID: member.ProxyProfileID,
				Weight:         member.Weight,
				Enabled:        member.Enabled,
			})
			plan.changed = true
			continue
		}
		plan.kept = append(plan.kept, member)
		state := currentByProfile[member.ProxyProfileID]
		if state.weight != member.Weight || state.enabled != member.Enabled || state.priority != index {
			plan.changed = true
		}
	}
	for _, member := range current {
		if _, ok := nextByProfile[member.profileID]; !ok {
			plan.toRemove = append(plan.toRemove, member.profileID)
			plan.removedProfileIDs = append(plan.removedProfileIDs, member.profileID)
			plan.changed = true
		}
	}
	return plan
}

// applyMemberReplace runs the §8.1 two-phase replacement inside the open
// transaction:
//  1. shift every existing member row by priorityTempOffset so the
//     UNIQUE(pool_id, priority) range 0..49 frees up (injective shift keeps
//     uniqueness);
//  2. delete the removed members' leases then their rows (lease FK RESTRICT
//     requires the lease to go first; kept members keep their leases);
//  3. update kept rows in place (weight/enabled only);
//  4. insert added rows on the memberTempBase range (no transient clash with
//     the shifted values);
//  5. normalize priorities to 0..n-1 with one CASE statement carrying the new
//     updated_at.
func (s *Store) applyMemberReplace(ctx context.Context, tx *sql.Tx, poolID string, current []memberState, next []memberInput, plan memberReplacePlan, now string) error {
	shiftQuery := `
		UPDATE ` + s.table("proxy_pool_members") + `
		SET priority = priority + ?
		WHERE pool_id = ?`
	if _, err := tx.ExecContext(ctx, s.bind(shiftQuery), priorityTempOffset, poolID); err != nil {
		return err
	}
	if len(plan.toRemove) > 0 {
		deleteLeasesQuery := `
			DELETE FROM ` + s.table("account_proxy_leases") + `
			WHERE pool_id = ? AND proxy_profile_id IN (` + placeholders(len(plan.toRemove)) + `)`
		if _, err := tx.ExecContext(ctx, s.bind(deleteLeasesQuery), append([]any{poolID}, idsToAny(plan.toRemove)...)...); err != nil {
			return err
		}
		deleteMembersQuery := `
			DELETE FROM ` + s.table("proxy_pool_members") + `
			WHERE pool_id = ? AND proxy_profile_id IN (` + placeholders(len(plan.toRemove)) + `)`
		if _, err := tx.ExecContext(ctx, s.bind(deleteMembersQuery), append([]any{poolID}, idsToAny(plan.toRemove)...)...); err != nil {
			return err
		}
	}
	for _, member := range plan.kept {
		state := memberState{}
		for _, candidate := range current {
			if candidate.profileID == member.ProxyProfileID {
				state = candidate
				break
			}
		}
		if state.weight == member.Weight && state.enabled == member.Enabled {
			continue
		}
		updateQuery := `
			UPDATE ` + s.table("proxy_pool_members") + `
			SET weight = ?, enabled = ?, updated_at = ?
			WHERE pool_id = ? AND proxy_profile_id = ?`
		if _, err := tx.ExecContext(ctx, s.bind(updateQuery), member.Weight, member.Enabled, now, poolID, member.ProxyProfileID); err != nil {
			return err
		}
	}
	for index, member := range plan.toAdd {
		insertQuery := `
			INSERT INTO ` + s.table("proxy_pool_members") + ` (pool_id, proxy_profile_id, priority, weight, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`
		if _, err := tx.ExecContext(ctx, s.bind(insertQuery), poolID, member.ProxyProfileID, memberTempBase+index, member.Weight, member.Enabled, now, now); err != nil {
			return err
		}
	}
	// Phase 2: one statement rewrites the final contiguous priorities; the
	// rows currently sit on the shifted/temporary ranges so no transient
	// UNIQUE(pool_id, priority) conflict is possible. PostgreSQL cannot infer
	// the CASE result parameter type, so the THEN value is cast explicitly.
	caseClauses := make([]string, 0, len(next))
	params := make([]any, 0, len(next)+2)
	for index, member := range next {
		caseClauses = append(caseClauses, "WHEN ? THEN ?::integer")
		params = append(params, member.ProxyProfileID, index)
	}
	normalizeQuery := `
		UPDATE ` + s.table("proxy_pool_members") + `
		SET priority = CASE proxy_profile_id ` + strings.Join(caseClauses, " ") + ` END,
			updated_at = ?
		WHERE pool_id = ?`
	params = append(params, now, poolID)
	if _, err := tx.ExecContext(ctx, s.bind(normalizeQuery), params...); err != nil {
		return err
	}
	return nil
}

// Patch mirrors the §8.1 PATCH: CAS on updated_at (409
// proxy_pool_revision_conflict), full member replacement when the members
// key is present, everything in one transaction. A patch that changes
// nothing answers changed:false without touching any row.
func (s *Store) Patch(ctx context.Context, id string, input patchInput) (*patchOutcome, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	current, err := s.loadPoolRow(ctx, tx, id, " FOR UPDATE")
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, nil
	}
	currentRevision := normalizeRevision(current.updatedAt)
	if currentRevision != normalizeRevision(input.ExpectedUpdatedAt) {
		return nil, ErrRevisionConflict
	}
	if input.Strategy != nil && *input.Strategy != StrategyRoundRobin && *input.Strategy != StrategyWeighted {
		return nil, ErrStrategyInvalid
	}
	var members []memberInput
	if input.HasMembers {
		members = input.Members
		if err := validateMembers(members); err != nil {
			return nil, err
		}
		if err := s.validateMembersExist(ctx, tx, members); err != nil {
			return nil, err
		}
	}
	name := current.name
	before := map[string]any{}
	after := map[string]any{}
	values := map[string]any{}
	assignments := []struct {
		column string
		value  any
	}{}
	add := func(key, column string, currentValue, nextValue any, changed *bool) {
		before[key] = currentValue
		after[key] = nextValue
		if currentValue == nextValue {
			return
		}
		assignments = append(assignments, struct {
			column string
			value  any
		}{column, nextValue})
		values[key] = nextValue
		*changed = true
	}
	fieldsChanged := false
	if input.Name != nil {
		name = *input.Name
		add("name", "name", current.name, *input.Name, &fieldsChanged)
	}
	if input.HasDescription {
		var currentDescription any
		if current.description != nil {
			currentDescription = *current.description
		}
		var nextDescription any
		if input.Description != nil {
			nextDescription = *input.Description
		}
		add("description", "description", currentDescription, nextDescription, &fieldsChanged)
	}
	if input.Strategy != nil {
		add("strategy", "strategy", current.strategy, *input.Strategy, &fieldsChanged)
	}
	if input.Enabled != nil {
		// PG boolean columns bind the bool directly (proxyprofiles 契约).
		add("enabled", "enabled", current.enabled, *input.Enabled, &fieldsChanged)
	}
	membersChanged := false
	membersSummary := ""
	var removedProfileIDs []string
	if input.HasMembers {
		currentMembers, err := s.loadMemberStates(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		plan := planMemberReplace(currentMembers, members)
		membersChanged = plan.changed
		removedProfileIDs = plan.removedProfileIDs
		if membersChanged {
			added := len(plan.toAdd)
			removed := len(plan.toRemove)
			membersSummary = "成员 " + itoa(len(currentMembers)) + "→" + itoa(len(members)) +
				"（新增 " + itoa(added) + "，移除 " + itoa(removed) + "）"
			now := s.now().UTC().Format(revisionLayout)
			if err := s.applyMemberReplace(ctx, tx, id, currentMembers, members, plan, now); err != nil {
				return nil, err
			}
			values["members"] = membersSummary
			before["members"] = "成员 " + itoa(len(currentMembers))
			after["members"] = "成员 " + itoa(len(members))
		}
	}
	if !fieldsChanged && !membersChanged {
		return &patchOutcome{
			ID:        id,
			UpdatedAt: currentRevision,
			Changed:   false,
			Values:    map[string]any{},
			Name:      name,
			Before:    before,
			After:     after,
		}, nil
	}
	// 微秒形态与 revisionSelectExpression 的 to_char US 读回逐字一致。
	updatedAtCandidate := s.now().UTC().Format(revisionLayout)
	setClauses := make([]string, 0, len(assignments)+1)
	params := make([]any, 0, len(assignments)+3)
	for _, assignment := range assignments {
		setClauses = append(setClauses, assignment.column+" = ?")
		params = append(params, assignment.value)
	}
	setClauses = append(setClauses, "updated_at = GREATEST(updated_at + INTERVAL '1 millisecond', CAST(? AS timestamptz))")
	params = append(params, updatedAtCandidate)
	params = append(params, id, currentRevision)
	updateQuery := `
		UPDATE ` + s.table("proxy_pools") + `
		SET ` + strings.Join(setClauses, ", ") + `
		WHERE id = ? AND updated_at = ` + s.revisionCastPlaceholder()
	result, err := tx.ExecContext(ctx, s.bind(updateQuery), params...)
	if err != nil {
		if isPoolNameDuplicate(err) {
			return nil, &PoolNameDuplicateError{Name: name}
		}
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, ErrRevisionConflict
	}
	updated, err := s.loadPoolRow(ctx, tx, id, "")
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return nil, ErrRevisionConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// §10.2：池/成员写入提交后发布 proxy_pool_updated；§7.4 被移除成员的
	// 健康键 best-effort 清理（失败告警靠 TTL）。
	s.notifyRuntimeUpdated()
	s.resetMemberHealth(removedProfileIDs)
	return &patchOutcome{
		ID:                id,
		UpdatedAt:         normalizeRevision(updated.updatedAt),
		Changed:           true,
		Values:            values,
		Name:              name,
		Before:            before,
		After:             after,
		MembersSummary:    membersSummary,
		RemovedProfileIDs: removedProfileIDs,
	}, nil
}

// Delete mirrors the §4.5 delete rules: one transaction previews and rejects
// live account bindings (409 proxy_pool_bound_accounts), clears soft-deleted
// accounts' proxy_pool_id (FK RESTRICT would otherwise block), then deletes
// leases, member rows and finally the pool. Committed-write invalidation and
// the member-health key cleanup run strictly after the commit succeeded.
func (s *Store) Delete(ctx context.Context, id string) (string, []string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()
	current, err := s.loadPoolRow(ctx, tx, id, " FOR UPDATE")
	if err != nil {
		return "", nil, err
	}
	if current == nil {
		return "", nil, nil
	}
	usageRows, err := tx.QueryContext(ctx, s.bind(`
		SELECT id, name
		FROM `+s.table("accounts")+`
		WHERE proxy_pool_id = ? AND deleted_at IS NULL
		ORDER BY id ASC
		LIMIT ?
	`), id, memberCountWindowLimit)
	if err != nil {
		return "", nil, err
	}
	type usageRow struct {
		id   string
		name sql.NullString
	}
	usage := []usageRow{}
	for usageRows.Next() {
		var row usageRow
		if err := usageRows.Scan(&row.id, &row.name); err != nil {
			usageRows.Close()
			return "", nil, err
		}
		usage = append(usage, row)
	}
	if err := usageRows.Err(); err != nil {
		usageRows.Close()
		return "", nil, err
	}
	usageRows.Close()
	if len(usage) > 0 {
		names := []string{}
		for index, row := range usage {
			if index >= memberCountPreviewLimit {
				break
			}
			if row.name.Valid && row.name.String != "" {
				names = append(names, row.name.String)
			}
		}
		return "", nil, &PoolBoundAccountsError{
			AccountCount:             len(usage),
			AccountNames:             names,
			AccountCountIsLowerBound: len(usage) >= memberCountWindowLimit,
		}
	}
	// §4.5：软删除账户的 proxy_pool_id 先清 NULL，避免 FK 阻塞池删除。
	if _, err := tx.ExecContext(ctx, s.bind(`
		UPDATE `+s.table("accounts")+`
		SET proxy_pool_id = NULL
		WHERE proxy_pool_id = ? AND deleted_at IS NOT NULL
	`), id); err != nil {
		return "", nil, err
	}
	memberProfileIDs, err := s.loadMemberProfileIDs(ctx, tx, id)
	if err != nil {
		return "", nil, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_proxy_leases")+` WHERE pool_id = ?`), id); err != nil {
		return "", nil, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("proxy_pool_members")+` WHERE pool_id = ?`), id); err != nil {
		return "", nil, err
	}
	result, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("proxy_pools")+` WHERE id = ?`), id)
	if err != nil {
		return "", nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return "", nil, err
	}
	if affected != 1 {
		return "", nil, nil
	}
	if err := tx.Commit(); err != nil {
		return "", nil, err
	}
	// §10.2 池删除失效；§7.4 成员/池删除清理成员健康键（best-effort）。
	s.notifyRuntimeUpdated()
	s.resetMemberHealth(memberProfileIDs)
	return current.name, memberProfileIDs, nil
}

func (s *Store) loadMemberProfileIDs(ctx context.Context, querier rowQuerier, poolID string) ([]string, error) {
	rows, err := querier.QueryContext(ctx, s.bind(`
		SELECT proxy_profile_id FROM `+s.table("proxy_pool_members")+` WHERE pool_id = ?
	`), poolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
