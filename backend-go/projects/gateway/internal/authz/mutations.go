// Mutation state machine for the authz slice: create (grant upsert +
// runtime/source sync + effective source), patch, revoke, return, expire
// sweep. Mirrors resource-authorization-write/return.repository.ts.
package authz

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"time"
)

// CreateInput mirrors the create zod contract (normalized).
type CreateInput struct {
	ResourceType  string // account|group
	ResourceID    string
	GranteeType   string // system_account|team
	GranteeID     string
	TargetGroupID *string
	Remark        *string
	ExpiresAt     *string
	LimitsJSON    *string
}

// CreateResult mirrors the create mutation outcome.
type CreateResult struct {
	Item           Summary
	Created        bool
	PreviousStatus *string
}

// Create mirrors createResourceAuthorizationMutationAsync: active-duplicate
// rejection, idempotent identical re-create, revival of terminal rows,
// optimistic-conflict failure, runtime/source/effective-source sync.
func (s *Store) Create(ctx context.Context, input CreateInput, actorSystemAccountID string) (*CreateResult, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Node normalizes the input before looking up the resource, so a malformed
	// expiry always remains an input error. The time- and account-bound checks
	// deliberately happen after resource resolution below.
	normalizedExpiresAt, err := normalizeAuthorizationExpiresAt(input.ExpiresAt)
	if err != nil {
		return nil, err
	}
	input.ExpiresAt = normalizedExpiresAt
	// Claim #8: create limits run through the shared quota schema
	// normalization (requestQuotaLimitsSchema → normalizeRequestQuotaLimits,
	// write.repository.ts:2379/1157). Canonical form: "" means SQL NULL.
	if input.LimitsJSON != nil {
		normalizedLimits, err := normalizeAuthorizationLimitsJSON(*input.LimitsJSON)
		if err != nil {
			return nil, err
		}
		if normalizedLimits != nil {
			input.LimitsJSON = normalizedLimits
		} else {
			empty := ""
			input.LimitsJSON = &empty
		}
	}
	nowTime := s.now().UTC()

	// Owner resolution: the resource must exist and belong to the actor scope.
	ownerID, accountExpiresAt, err := s.resolveResourceOwner(ctx, tx, input.ResourceType, input.ResourceID)
	if err != nil {
		return nil, err
	}
	if ownerID == "" {
		return nil, failf("授权资源不存在")
	}
	if actorSystemAccountID == "" || ownerID != actorSystemAccountID {
		return nil, failf("授权资源不存在")
	}
	normalizedExpiresAt, err = validateAuthorizationCreateExpiresAt(input.ExpiresAt, accountExpiresAt, nowTime)
	if err != nil {
		return nil, err
	}
	input.ExpiresAt = normalizedExpiresAt
	now := nowTime.Format(time.RFC3339Nano)
	if input.GranteeType == "system_account" && input.GranteeID == ownerID {
		return nil, failf("不能授权给资源所有者自己")
	}

	// Grantee existence checks.
	if err := s.checkGrantee(ctx, tx, input); err != nil {
		return nil, err
	}
	if input.GranteeType == "team" {
		var nonOwnerMembers int
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*)
			FROM `+s.table("system_team_members")+` m
			INNER JOIN `+s.table("system_accounts")+` a ON a.id = m.system_account_id
			WHERE m.team_id = ? AND m.status = 'active' AND a.status = 'active' AND m.system_account_id <> ?`),
			input.GranteeID, ownerID).Scan(&nonOwnerMembers); err != nil {
			return nil, err
		}
		if nonOwnerMembers == 0 {
			return nil, failf("团队暂无可授权成员，请先添加非归属人成员后再授权")
		}
	}

	// Active duplicate handling mirrors Node's upsert mutation: an identical
	// request is idempotent (created=false), while a changed request is
	// rejected. Omitted remark preserves an existing remark; omitted expiry or
	// limits mean an unset value, matching Node normalization.
	var existingID, existingStatus, existingRemark, existingExpires, existingLimits sql.NullString
	granteeColumn := "grantee_system_account_id"
	if input.GranteeType == "team" {
		granteeColumn = "grantee_team_id"
	}
	dupQuery := `SELECT id, status, COALESCE(remark,''), COALESCE(expires_at,''), COALESCE(limits_json,'')
		FROM ` + s.table("resource_authorization_grants") + `
		WHERE resource_type = ? AND resource_id = ? AND ` + granteeColumn + ` = ?
		ORDER BY CASE status WHEN 'active' THEN 0 WHEN 'paused' THEN 1 WHEN 'expired' THEN 2 WHEN 'revoked' THEN 3 WHEN 'returned' THEN 4 ELSE 5 END,
			created_at ASC, id ASC LIMIT 1`
	err = tx.QueryRowContext(ctx, s.bind(dupQuery), input.ResourceType, input.ResourceID, input.GranteeID).
		Scan(&existingID, &existingStatus, &existingRemark, &existingExpires, &existingLimits)
	var reviveID, reviveStatus string
	if err == nil {
		if existingStatus.String != StatusActive {
			reviveID = existingID.String
			reviveStatus = existingStatus.String
		} else {
			nextRemark := existingRemark.String
			if input.Remark != nil {
				if value := strings.TrimSpace(*input.Remark); value != "" {
					nextRemark = value
				}
			}
			nextExpires := ""
			if input.ExpiresAt != nil {
				nextExpires = strings.TrimSpace(*input.ExpiresAt)
			}
			sameRemark := nextRemark == existingRemark.String
			sameExpires := nextExpires == existingExpires.String
			sameLimits := nullableJSONEqual(input.LimitsJSON, existingLimits.String)
			if sameRemark && sameExpires && sameLimits {
				// Commit before the post-commit read so single-connection
				// fixtures (SQLite) cannot deadlock on the read.
				if err := tx.Commit(); err != nil {
					return nil, err
				}
				summary, limits, err := s.findSummaryWithLimits(ctx, existingID.String)
				if err != nil {
					return nil, err
				}
				if summary == nil {
					return nil, failf("创建授权失败")
				}
				summary.Limits = limits
				return &CreateResult{Item: *summary, Created: false}, nil
			}
			kind := "用户"
			if input.GranteeType == "team" {
				kind = "团队"
			}
			return nil, failf("该资源已授权给该%s，请勿重复授权", kind)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	// Team grant fanout validation (contract #6/#8).
	if input.GranteeType == "team" {
		var activeGrants int
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM `+s.table("resource_authorization_grants")+`
			WHERE grantee_team_id = ? AND status = 'active'`), input.GranteeID).Scan(&activeGrants); err != nil {
			return nil, err
		}
		if activeGrants+1 > MaxTeamActiveGrantCount {
			return nil, failf("单个授权团队最多支持 20 条有效授权，请先回收或停用部分授权")
		}
	}

	// Insert a new grant, or revive the existing terminal row. The schema only
	// enforces uniqueness for active rows, so terminal rows must be selected
	// explicitly rather than relying on an insert conflict.
	var grantID string
	created := true
	var previousStatus *string
	if reviveID != "" {
		nextRemark := strings.TrimSpace(existingRemark.String)
		if input.Remark != nil {
			if value := strings.TrimSpace(*input.Remark); value != "" {
				nextRemark = value
			}
		}
		result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
			SET status = 'active', remark = ?, expires_at = ?, limits_json = ?,
				revoked_by = NULL, revoked_at = NULL, updated_at = ?
			WHERE id = ? AND status = ?`),
			nullableString(&nextRemark), nullableString(input.ExpiresAt), nullableString(input.LimitsJSON),
			now, reviveID, reviveStatus)
		if err != nil {
			return nil, err
		}
		affected, _ := result.RowsAffected()
		if affected != 1 {
			return nil, &Conflict{CurrentUpdatedAt: ""}
		}
		grantID = reviveID
		created = false
		previousStatus = &reviveStatus
	} else {
		insertErr := tx.QueryRowContext(ctx, s.bind(`INSERT INTO `+s.table("resource_authorization_grants")+`
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
		 `+granteeColumn+`, scope, status, remark, expires_at, limits_json, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'use', 'active', ?, ?, ?, ?, ?, ?)
		RETURNING id`),
			newGrantID(s.now), input.ResourceType, input.ResourceID, ownerID, input.GranteeType,
			input.GranteeID, nullableString(input.Remark), nullableString(input.ExpiresAt),
			nullableString(input.LimitsJSON), actorSystemAccountID, now, now).Scan(&grantID)
		if insertErr != nil {
			return nil, insertErr
		}
	}

	// Runtime upsert per grantee user (direct grant: one row; team grant:
	// one per active member, excluding the owner). Mirrors the Node create
	// fanout (:384-401 team, :424-438 direct) through
	// upsertResourceAuthorizationForUserAsync.
	granteeUsers, err := s.resolveGranteeUsers(ctx, tx, input, ownerID)
	if err != nil {
		return nil, err
	}
	var sourceTeamID *string
	if input.GranteeType == "team" {
		sourceTeamID = &input.GranteeID
	}
	projection := runtimeProjection{Remark: input.Remark, ExpiresAt: input.ExpiresAt, LimitsJSON: input.LimitsJSON}
	for _, userID := range granteeUsers {
		if err := s.upsertRuntimeForUser(ctx, tx, input.ResourceType, input.ResourceID, ownerID, userID, sourceTeamID, projection, actorSystemAccountID, now); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	summary, limits, err := s.findSummaryWithLimits(ctx, grantID)
	if err != nil {
		return nil, err
	}
	summary.Limits = limits
	return &CreateResult{Item: *summary, Created: created, PreviousStatus: previousStatus}, nil
}

func (s *Store) resolveResourceOwner(ctx context.Context, tx *sql.Tx, resourceType, resourceID string) (string, *string, error) {
	var ownerID string
	var accountExpiresAt sql.NullString
	var err error
	switch resourceType {
	case "account":
		// Mirrors Node resourceOwnerSystemAccountId: the owning namespace
		// column is system_account_id; instance rows (already authorized
		// clones) are never grantable resources.
		err = tx.QueryRowContext(ctx, s.bind(`SELECT system_account_id, account_expires_at FROM `+s.table("accounts")+`
			WHERE id = ? AND deleted_at IS NULL AND authorization_instance_authorization_id IS NULL`), resourceID).Scan(&ownerID, &accountExpiresAt)
	case "group":
		err = tx.QueryRowContext(ctx, s.bind(`SELECT system_account_id FROM `+s.table("groups")+` WHERE id = ?`), resourceID).Scan(&ownerID)
	default:
		return "", nil, failf("请选择授权资源")
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	if accountExpiresAt.Valid {
		value := accountExpiresAt.String
		return ownerID, &value, nil
	}
	return ownerID, nil, nil
}

func (s *Store) checkGrantee(ctx context.Context, tx *sql.Tx, input CreateInput) error {
	switch input.GranteeType {
	case "system_account":
		var status string
		err := tx.QueryRowContext(ctx, s.bind(`SELECT status FROM `+s.table("system_accounts")+` WHERE id = ?`), input.GranteeID).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && status != "active") {
			return failf("被授权用户不存在或已停用")
		}
		return err
	case "team":
		var status string
		err := tx.QueryRowContext(ctx, s.bind(`SELECT status FROM `+s.table("system_teams")+` WHERE id = ?`), input.GranteeID).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && status != "active") {
			return failf("团队不存在或已停用")
		}
		if err != nil {
			return err
		}
		var memberCount int
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM `+s.table("system_team_members")+` m
			INNER JOIN `+s.table("system_accounts")+` a ON a.id = m.system_account_id
			WHERE m.team_id = ? AND m.status = 'active' AND a.status = 'active'`), input.GranteeID).Scan(&memberCount); err != nil {
			return err
		}
		if memberCount == 0 {
			return failf("团队暂无可授权成员，请先添加非归属人成员后再授权")
		}
		if memberCount > MaxTeamMembersPerTeam {
			return failf("授权团队最多支持 %d 个成员，请先移除部分成员后再继续", MaxTeamMembersPerTeam)
		}
		return nil
	default:
		return failf("被授权对象类型无效")
	}
}

// resolveGranteeUsers expands the grantee to runtime user ids: direct grant
// → the grantee; team grant → active members excluding the resource owner.
func (s *Store) resolveGranteeUsers(ctx context.Context, tx *sql.Tx, input CreateInput, ownerID string) ([]string, error) {
	if input.GranteeType == "system_account" {
		return []string{input.GranteeID}, nil
	}
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT m.system_account_id
		FROM `+s.table("system_team_members")+` m
		INNER JOIN `+s.table("system_accounts")+` a ON a.id = m.system_account_id
		WHERE m.team_id = ? AND m.status = 'active' AND a.status = 'active'`), input.GranteeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []string{}
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			return nil, err
		}
		if userID != ownerID {
			users = append(users, userID)
		}
	}
	return users, rows.Err()
}

// PatchInput mirrors the Node patch contract with explicit field presence
// (resourceAuthorizationPatch :824-826): a nil pointer means the JSON key was
// absent; a set pointer with a nil value is the explicit JSON null that clears
// the column.
type PatchInput struct {
	Status       *string
	ExpiresAt    *string
	ExpiresAtSet bool
	LimitsJSON   *string
	LimitsSet    bool
}

// PatchOutcome mirrors the Node patch outcome
// (resourceAuthorizationPatchSuccess :880-898).
type PatchOutcome struct {
	Status           string   // not_found|conflict|unchanged|updated
	Result           *Summary // the current row for unchanged, the next row for updated
	Limits           any      // normalized limits echo (map or nil → JSON null)
	CurrentUpdatedAt string
}

func (s *Store) Patch(ctx context.Context, id string, input PatchInput, expectedUpdatedAt, actor string) (*PatchOutcome, error) {
	return s.patchForOwner(ctx, id, input, expectedUpdatedAt, actor, "")
}

// PatchForOwner applies the same owner filter carried by Node's
// RequestAccessScope when an administrator selects ?systemAccountId. An empty
// ownerScope keeps the unscoped administrator contract.
func (s *Store) PatchForOwner(ctx context.Context, id string, input PatchInput, expectedUpdatedAt, actor, ownerScope string) (*PatchOutcome, error) {
	return s.patchForOwner(ctx, id, input, expectedUpdatedAt, actor, ownerScope)
}

// patchForOwner mirrors patchResourceAuthorizationPostgresAsync (:773-816)
// with the resourceAuthorizationPatch computation (:818-878).
func (s *Store) patchForOwner(ctx context.Context, id string, input PatchInput, expectedUpdatedAt, actor, ownerScope string) (*PatchOutcome, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	grant, err := s.GetGrantForMutation(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if grant == nil {
		return &PatchOutcome{Status: "not_found"}, nil
	}
	if ownerScope != "" && grant.OwnerID != ownerScope {
		return &PatchOutcome{Status: "not_found"}, nil
	}
	// Node :792: the CAS conflict decision precedes every input-derived
	// outcome, so a malformed body on a stale version still reports conflict.
	if !authorizationVersionEqual(expectedUpdatedAt, grant.UpdatedAt) {
		return &PatchOutcome{Status: "conflict", CurrentUpdatedAt: grant.UpdatedAt}, nil
	}
	nowTime := s.now().UTC()
	now := NextVersion(grant.UpdatedAt, s.now())

	if input.Status != nil && *input.Status != StatusActive && *input.Status != StatusPaused {
		return nil, failf("授权状态无效")
	}
	// Claim #3: presence-aware expiresAt (:824-827). Absent keeps the stored
	// value; explicit null clears it; an empty or malformed string is rejected.
	var nextExpires *string
	if input.ExpiresAtSet {
		if input.ExpiresAt == nil {
			nextExpires = nil
		} else {
			normalized, err := normalizeAuthorizationExpiresAt(input.ExpiresAt)
			if err != nil {
				return nil, err
			}
			nextExpires = normalized
		}
	} else if grant.ExpiresAt.Valid && grant.ExpiresAt.String != "" {
		value := grant.ExpiresAt.String
		nextExpires = &value
	}
	// Node :829-831: an expired grant may only return to active together with
	// an explicit new expiry.
	if grant.Status == StatusExpired && input.Status != nil && *input.Status == StatusActive && !input.ExpiresAtSet {
		return nil, failf("到期授权恢复时请同时调整过期时间")
	}
	// Node :832-838: an explicit past expiry forces the expired state machine.
	nextStatus := grant.Status
	switch {
	case input.ExpiresAtSet && nextExpires != nil && authorizationExpiresPassed(*nextExpires, nowTime):
		nextStatus = StatusExpired
	case input.Status != nil:
		nextStatus = *input.Status
	case grant.Status == StatusExpired && input.ExpiresAtSet:
		nextStatus = StatusActive
	}
	// Claim #4/#10 (Node :867): validation runs for an explicit expiry and for
	// every activation transition, against the next version instant.
	if input.ExpiresAtSet || (input.Status != nil && *input.Status == StatusActive && *input.Status != grant.Status) {
		validateNowMs, ok := instantMilliseconds(now)
		if !ok {
			validateNowMs = nowTime.UnixMilli()
		}
		if err := s.validateAuthorizationExpiresAtForWrite(ctx, tx, grant.ResourceType, grant.ResourceID,
			nextExpires, time.UnixMilli(validateNowMs), nextStatus == StatusExpired); err != nil {
			return nil, err
		}
	}
	// Claim #8 (Node :839-844): limits normalize through the shared quota
	// schema; a semantically unchanged candidate keeps the stored value so no
	// assignment (and no new version) is produced.
	var nextLimits *string
	if input.LimitsSet {
		if input.LimitsJSON != nil {
			normalized, err := normalizeAuthorizationLimitsJSON(*input.LimitsJSON)
			if err != nil {
				return nil, err
			}
			nextLimits = normalized
		}
	} else if grant.LimitsJSON.Valid && grant.LimitsJSON.String != "" {
		value := grant.LimitsJSON.String
		nextLimits = &value
	}
	if input.LimitsSet && authorizationLimitsSemanticallyEqual(nextLimits, grant.LimitsJSON) {
		if grant.LimitsJSON.Valid && grant.LimitsJSON.String != "" {
			value := grant.LimitsJSON.String
			nextLimits = &value
		} else {
			nextLimits = nil
		}
	}
	// Node :845-851: revoked_* bookkeeping only moves on a status change.
	statusChanged := nextStatus != grant.Status
	nextRevokedBy := grantNullStringPointer(grant.RevokedBy)
	nextRevokedAt := grantNullStringPointer(grant.RevokedAt)
	if statusChanged {
		if nextStatus == StatusActive || nextStatus == StatusPaused {
			nextRevokedBy, nextRevokedAt = nil, nil
		} else {
			if nextRevokedBy == nil {
				value := actor
				nextRevokedBy = &value
			}
			if nextRevokedAt == nil {
				value := now
				nextRevokedAt = &value
			}
		}
	}

	assignments := []string{}
	args := []any{}
	add := func(column string, value any) {
		assignments = append(assignments, column+" = ?")
		args = append(args, value)
	}
	if statusChanged {
		add("status", nextStatus)
	}
	if !nullableTextEqual(nextExpires, grant.ExpiresAt) {
		add("expires_at", nullableString(nextExpires))
	}
	if !authorizationLimitsAssignmentEqual(nextLimits, grant.LimitsJSON) {
		add("limits_json", nullableString(nextLimits))
	}
	if !nullableTextEqual(nextRevokedBy, grant.RevokedBy) {
		add("revoked_by", nullableString(nextRevokedBy))
	}
	if !nullableTextEqual(nextRevokedAt, grant.RevokedAt) {
		add("revoked_at", nullableString(nextRevokedAt))
	}
	if len(assignments) == 0 {
		// Node :801: no-op patches are unchanged and never bump the version.
		summary := grant.summary()
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &PatchOutcome{Status: "unchanged", Result: &summary,
			Limits: decodeAuthorizationLimits(grantLimitsText(grant.LimitsJSON))}, nil
	}
	add("updated_at", now)
	args = append(args, id, grant.UpdatedAt)
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
		SET `+strings.Join(assignments, ", ")+` WHERE id = ? AND updated_at = ?`), args...)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return &PatchOutcome{Status: "conflict", CurrentUpdatedAt: grant.UpdatedAt}, nil
	}

	// Node :809: the synced projection writes runtime status, expires_at,
	// limits_json, revoked_* and sources explicitly before the refresh.
	next := *grant
	next.Status = nextStatus
	next.ExpiresAt = pointerNullString(nextExpires)
	next.LimitsJSON = pointerNullString(nextLimits)
	next.RevokedBy = pointerNullString(nextRevokedBy)
	next.RevokedAt = pointerNullString(nextRevokedAt)
	next.UpdatedAt = now
	if err := s.syncGrantRuntime(ctx, tx, &next, actor, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	summary := next.summary()
	return &PatchOutcome{Status: "updated", Result: &summary,
		Limits: decodeAuthorizationLimits(grantLimitsText(next.LimitsJSON))}, nil
}

// TerminalMutation mirrors the Node terminal mutation outcome
// (resourceAuthorizationTerminalMutationSuccess :2533-2552).
type TerminalMutation struct {
	Status           string // not_found|conflict|unchanged|updated
	Result           *Summary
	PreviousStatus   *string
	CurrentUpdatedAt string
}

// Revoke mirrors revokeResourceAuthorizationMutationAsync (:506-545).
func (s *Store) Revoke(ctx context.Context, id, expectedUpdatedAt, actor string) (*TerminalMutation, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	grant, err := s.GetGrantForMutation(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if grant == nil {
		return &TerminalMutation{Status: "not_found"}, nil
	}
	// Claim #11 (Node :527-534): the CAS conflict decision comes before the
	// revoked idempotency short-circuit.
	if !authorizationVersionEqual(expectedUpdatedAt, grant.UpdatedAt) {
		return &TerminalMutation{Status: "conflict", CurrentUpdatedAt: grant.UpdatedAt}, nil
	}
	if grant.Status == StatusRevoked {
		summary := grant.summary()
		previous := grant.Status
		return &TerminalMutation{Status: "unchanged", Result: &summary, PreviousStatus: &previous}, nil
	}
	now := NextVersion(grant.UpdatedAt, s.now())
	// Node revokeResourceAuthorizationGrantAsync (:983-993): grant write then
	// the full runtime sync (direct → manual source revoke; team → team source
	// revoke) so member runtime rows land in the Node terminal shape.
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
		SET status = 'revoked', revoked_by = ?, revoked_at = ?, updated_at = ?
		WHERE id = ?`), actor, now, now, grant.ID); err != nil {
		return nil, err
	}
	next := *grant
	next.Status = StatusRevoked
	next.RevokedBy = sql.NullString{String: actor, Valid: true}
	next.RevokedAt = sql.NullString{String: now, Valid: true}
	next.UpdatedAt = now
	if err := s.syncGrantRuntime(ctx, tx, &next, actor, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	summary := next.summary()
	previous := grant.Status
	return &TerminalMutation{Status: "updated", Result: &summary, PreviousStatus: &previous}, nil
}

// Return mirrors returnResourceAuthorizationForGranteeMutationPostgresAsync
// (return.repository.ts:161-190): direct grants only, conflict decision before
// the terminal checks, and the runtime must still carry an active manual
// source.
func (s *Store) Return(ctx context.Context, id, expectedUpdatedAt, granteeUserID string) (*TerminalMutation, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	grant, err := s.GetGrantForMutation(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if grant == nil || grant.GranteeType != "system_account" ||
		!grant.GranteeUserID.Valid || grant.GranteeUserID.String != granteeUserID {
		return &TerminalMutation{Status: "not_found"}, nil
	}
	if grant.OwnerID == granteeUserID {
		return &TerminalMutation{Status: "not_found"}, nil
	}
	// Node :173: conflict first.
	if !authorizationVersionEqual(expectedUpdatedAt, grant.UpdatedAt) {
		return &TerminalMutation{Status: "conflict", CurrentUpdatedAt: grant.UpdatedAt}, nil
	}
	// Node :174-177: returned → unchanged, revoked → not_found.
	if grant.Status == StatusReturned {
		summary := grant.summary()
		previous := grant.Status
		return &TerminalMutation{Status: "unchanged", Result: &summary, PreviousStatus: &previous}, nil
	}
	if grant.Status == StatusRevoked {
		return &TerminalMutation{Status: "not_found"}, nil
	}
	runtimeID, err := s.findRuntimeIDForUserGrant(ctx, tx, grant.ResourceType, grant.ResourceID, grant.GranteeUserID.String)
	if err != nil {
		return nil, err
	}
	if runtimeID == "" {
		return &TerminalMutation{Status: "not_found"}, nil
	}
	hasManual, err := s.hasActiveManualSource(ctx, tx, runtimeID)
	if err != nil {
		return nil, err
	}
	if !hasManual {
		return &TerminalMutation{Status: "not_found"}, nil
	}
	now := NextVersion(grant.UpdatedAt, s.now())
	// Node returnResourceAuthorizationGrantAsync (:534-586).
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
		SET status = 'returned', revoked_by = ?, revoked_at = ?, updated_at = ?
		WHERE id = ?`), granteeUserID, now, now, grant.ID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
		SET status = 'revoked',
			ended_at = COALESCE(ended_at, ?),
			ended_reason = COALESCE(ended_reason, 'grantee_returned'),
			revoked_by = ?,
			revoked_at = ?,
			updated_at = ?
		WHERE authorization_id = ?
			AND source_type = 'manual'
			AND status IN ('active', 'superseded')`), now, granteeUserID, now, now, runtimeID); err != nil {
		return nil, err
	}
	noPreserve := false
	if err := s.refreshEffectiveSource(ctx, tx, runtimeID, granteeUserID, now, refreshOptions{
		noActiveSourceReason:              "grantee_returned",
		preserveExpiredWhenNoActiveSource: &noPreserve,
		terminalStatus:                    StatusReturned,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	summary, err := s.Find(ctx, id)
	if err != nil {
		return nil, err
	}
	previous := grant.Status
	return &TerminalMutation{Status: "updated", Result: summary, PreviousStatus: &previous}, nil
}

// hasActiveManualSource mirrors
// hasActiveManualRuntimeAuthorizationSourceAsync (return.repository.ts:508-518).
func (s *Store) hasActiveManualSource(ctx context.Context, tx *sql.Tx, runtimeID string) (bool, error) {
	var id string
	err := tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorization_sources")+`
		WHERE authorization_id = ? AND source_type = 'manual' AND status = 'active'
		LIMIT 1`), runtimeID).Scan(&id)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ExpireSweep mirrors expireDueResourceAuthorizationsAsync (:930-970):
// active/paused grants past expires_at become expired and the runtime rows are
// re-projected through the grant sync (no source revocation).
func (s *Store) ExpireSweep(ctx context.Context, limit int) (int, error) {
	ctx = ensureCtx(ctx)
	if limit <= 0 {
		limit = MaxExpirySweepBatchSize
	}
	now := s.now()
	nowText := now.UTC().Format("2006-01-02T15:04:05.000Z")
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorization_grants")+`
		WHERE status IN ('active','paused') AND expires_at IS NOT NULL AND expires_at <= ?
		ORDER BY expires_at ASC, updated_at ASC, id ASC LIMIT ?`), nowText, limit)
	if err != nil {
		return 0, err
	}
	var dueIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		dueIDs = append(dueIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	expired := 0
	for _, id := range dueIDs {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return expired, err
		}
		grant, err := s.GetGrantForMutation(ctx, tx, id)
		if err != nil {
			tx.Rollback()
			return expired, err
		}
		if grant == nil || (grant.Status != StatusActive && grant.Status != StatusPaused) {
			tx.Rollback()
			continue
		}
		// Node :949-956: the grant keeps revoked_by and only stamps revoked_at.
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
			SET status = 'expired', revoked_at = COALESCE(revoked_at, ?), updated_at = ?
			WHERE id = ? AND status IN ('active','paused')`), nowText, nowText, grant.ID); err != nil {
			tx.Rollback()
			return expired, err
		}
		next := *grant
		next.Status = StatusExpired
		if !next.RevokedAt.Valid || next.RevokedAt.String == "" {
			next.RevokedAt = sql.NullString{String: nowText, Valid: true}
		}
		next.UpdatedAt = nowText
		// Node :957-962: sync actor is revoked_by ?? created_by.
		actor := ""
		if grant.RevokedBy.Valid && grant.RevokedBy.String != "" {
			actor = grant.RevokedBy.String
		} else if grant.CreatedBy.Valid {
			actor = grant.CreatedBy.String
		}
		if err := s.syncGrantRuntime(ctx, tx, &next, actor, nowText); err != nil {
			tx.Rollback()
			return expired, err
		}
		if err := tx.Commit(); err != nil {
			return expired, err
		}
		expired++
	}
	return expired, nil
}

func newGrantID(now func() time.Time) string {
	return "rauthgrant_" + randomSuffix()
}

// randomSuffix mirrors Node newId: 16 random bytes hex.
func randomSuffix() string {
	var buf [16]byte
	if _, err := crand.Read(buf[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf[:])
}
func newRuntimeID(now func() time.Time) string {
	return "rauth_" + randomSuffix()
}
func newSourceID(now func() time.Time) string {
	return "rauthsrc_" + randomSuffix()
}

func nullableString(value *string) any {
	if value == nil || *value == "" {
		return nil
	}
	return *value
}

func grantNullStringPointer(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	v := value.String
	return &v
}

func pointerNullString(value *string) sql.NullString {
	if value == nil || *value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

func grantLimitsText(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

// nullableTextEqual compares an optional column value with the stored
// NULL-able text (absence and empty string are both NULL).
func nullableTextEqual(value *string, stored sql.NullString) bool {
	if value == nil || *value == "" {
		return !stored.Valid || stored.String == ""
	}
	return stored.Valid && stored.String == *value
}

// authorizationLimitsSemanticallyEqual mirrors
// resourceAuthorizationLimitsEqual (:912-914): NULL and "{}" are the same
// limits document.
func authorizationLimitsSemanticallyEqual(value *string, stored sql.NullString) bool {
	return canonicalAuthorizationLimits(value) == canonicalAuthorizationLimits(grantLimitsPointer(stored))
}

// authorizationLimitsAssignmentEqual decides the limits_json assignment the
// same way Node's raw compare after the semantic swap (:842-844, :860).
func authorizationLimitsAssignmentEqual(value *string, stored sql.NullString) bool {
	if value != nil && stored.Valid && *value == stored.String {
		return true
	}
	return authorizationLimitsSemanticallyEqual(value, stored)
}

func grantLimitsPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	v := value.String
	return &v
}

func nullableJSONEqual(next *string, current string) bool {
	var nextValue any
	if next == nil || strings.TrimSpace(*next) == "" {
		nextValue = map[string]any{}
	} else if err := jsonUnmarshal([]byte(*next), &nextValue); err != nil {
		return current == *next
	}
	var currentValue any
	if strings.TrimSpace(current) == "" {
		currentValue = map[string]any{}
	} else if err := jsonUnmarshal([]byte(current), &currentValue); err != nil {
		return next != nil && *next == current
	}
	// Node's requestQuotaLimitsJson(normalizeRequestQuotaLimits(...)) stores
	// an empty limits object as NULL. Compare decoded values so JSON key order
	// and the NULL/{} representation do not create a false conflict.
	if object, ok := nextValue.(map[string]any); ok && len(object) == 0 {
		nextValue = map[string]any{}
	}
	if object, ok := currentValue.(map[string]any); ok && len(object) == 0 {
		currentValue = map[string]any{}
	}
	return reflect.DeepEqual(nextValue, currentValue)
}
