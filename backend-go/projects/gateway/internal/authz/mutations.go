// Mutation state machine for the authz slice: create (grant upsert +
// runtime/source sync + effective source), patch, revoke, return, expire
// sweep. Mirrors resource-authorization-write/return.repository.ts.
package authz

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
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
	if input.LimitsJSON != nil {
		normalizedLimits, normalizeErr := normalizeAuthorizationLimitsJSON(*input.LimitsJSON)
		if normalizeErr != nil {
			return nil, failf("%s", normalizeErr.Error())
		}
		input.LimitsJSON = normalizedLimits
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
				if err := tx.Commit(); err != nil {
					return nil, err
				}
				summary, err := s.Find(ctx, existingID.String)
				if err != nil {
					return nil, err
				}
				if summary == nil {
					return nil, failf("创建授权失败")
				}
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
	// one per active member, excluding the owner).
	granteeUsers, err := s.resolveGranteeUsers(ctx, tx, input, ownerID)
	if err != nil {
		return nil, err
	}
	for _, userID := range granteeUsers {
		if err := s.upsertRuntimeForUser(ctx, tx, input, ownerID, userID, actorSystemAccountID, now); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	summary, err := s.Find(ctx, grantID)
	if err != nil {
		return nil, err
	}
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

// upsertRuntimeForUser mirrors upsertResourceAuthorizationForUser: runtime
// row upsert (unique per resource+grantee user), source upsert, effective
// source refresh.
func (s *Store) upsertRuntimeForUser(ctx context.Context, tx *sql.Tx, input CreateInput, ownerID, granteeUserID, actor, now string) error {
	var runtimeID string
	err := tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorizations")+`
		WHERE resource_type = ? AND resource_id = ? AND grantee_system_account_id = ?`),
		input.ResourceType, input.ResourceID, granteeUserID).Scan(&runtimeID)
	if errors.Is(err, sql.ErrNoRows) {
		runtimeID = newRuntimeID(s.now)
		_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("resource_authorizations")+`
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
			 scope, status, activated_at, remark, expires_at, limits_json, created_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'use', 'active', ?, ?, ?, ?, ?, ?, ?)`),
			runtimeID, input.ResourceType, input.ResourceID, ownerID, granteeUserID,
			now, nullableString(input.Remark), nullableString(input.ExpiresAt),
			nullableString(input.LimitsJSON), actor, now, now)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		_, err = tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorizations")+`
			SET status = 'active', remark = ?, expires_at = ?, limits_json = ?,
				revoked_by = NULL, revoked_at = NULL, revoked_reason = NULL, updated_at = ?
			WHERE id = ?`), nullableString(input.Remark), nullableString(input.ExpiresAt),
			nullableString(input.LimitsJSON), now, runtimeID)
		if err != nil {
			return err
		}
	}

	// Source upsert: the new grant becomes an active manual/team source.
	sourceType := "manual"
	var sourceTeamID any
	if input.GranteeType == "team" {
		sourceType = "team"
		sourceTeamID = input.GranteeID
	}
	var sourceID string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorization_sources")+`
		WHERE authorization_id = ? AND source_type = ? AND COALESCE(source_team_id,'') = COALESCE(?,'') AND status = 'active'`),
		runtimeID, sourceType, sourceTeamID).Scan(&sourceID)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("resource_authorization_sources")+`
			(id, authorization_id, source_type, source_team_id, status, activated_at, created_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'active', ?, ?, ?, ?)`),
			newSourceID(s.now), runtimeID, sourceType, sourceTeamID, now, actor, now, now)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	// Effective source refresh for this runtime row.
	return s.refreshEffectiveSourceForRuntime(ctx, tx, runtimeID, actor, now, "", true, StatusRevoked)
}

// refreshEffectiveSourceForRuntime mirrors
// refreshResourceAuthorizationEffectiveSourceAsync for a single runtime row:
// active team source → active/paused/expired; paused team → paused; active
// manual → manual; no active source → terminal.
func (s *Store) refreshEffectiveSourceForRuntime(ctx context.Context, tx *sql.Tx, runtimeID, actor, now, noActiveSourceReason string, preserveExpired bool, terminalStatus string) error {
	return s.refreshEffectiveSourceForRuntimeWithGrant(ctx, tx, runtimeID, actor, now, noActiveSourceReason, preserveExpired, terminalStatus, "")
}

func (s *Store) refreshEffectiveSourceForRuntimeWithGrant(ctx context.Context, tx *sql.Tx, runtimeID, actor, now, noActiveSourceReason string, preserveExpired bool, terminalStatus, grantStatus string) error {
	if terminalStatus == "" {
		terminalStatus = StatusRevoked
	}
	var expiresAt sql.NullString
	var currentStatus string
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT COALESCE(expires_at,''), status FROM `+s.table("resource_authorizations")+`
		WHERE id = ?`), runtimeID).Scan(&expiresAt, &currentStatus); err != nil {
		return err
	}
	expiredAlready := expiresAt.String != "" && expiresAt.String <= now

	// Active team source supported by an active, unexpired team grant.
	var effectiveType any
	var effectiveTeamID any
	nextStatus := ""
	nextReason := ""
	var teamID string
	teamErr := tx.QueryRowContext(ctx, s.bind(`SELECT s.source_team_id
		FROM `+s.table("resource_authorization_sources")+` s
		WHERE s.authorization_id = ? AND s.source_type = 'team' AND s.status = 'active'
		ORDER BY s.activated_at ASC, s.created_at ASC, s.id ASC LIMIT 1`), runtimeID).Scan(&teamID)
	if teamErr == nil && teamID != "" {
		var teamGrantStatus string
		grantErr := tx.QueryRowContext(ctx, s.bind(`SELECT g.status FROM `+s.table("resource_authorization_grants")+` g
			WHERE g.grantee_type = 'team' AND g.grantee_team_id = ? AND g.status IN ('active','paused')
			ORDER BY CASE g.status WHEN 'active' THEN 0 ELSE 1 END, g.created_at ASC, g.id ASC LIMIT 1`), teamID).Scan(&teamGrantStatus)
		if grantErr == nil && teamGrantStatus == "active" {
			effectiveType, effectiveTeamID = "team", teamID
			switch {
			case expiredAlready:
				nextStatus, nextReason = StatusExpired, "authorization_expired"
			case currentStatus == StatusPaused:
				nextStatus = StatusPaused
			default:
				nextStatus = StatusActive
			}
		} else if grantErr == nil && teamGrantStatus == "paused" {
			effectiveType, effectiveTeamID = "team", teamID
			if expiredAlready {
				nextStatus, nextReason = StatusExpired, "authorization_expired"
			} else {
				nextStatus, nextReason = StatusPaused, "authorization_paused"
			}
		}
	}

	if nextStatus == "" {
		var manualCount int
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM `+s.table("resource_authorization_sources")+`
			WHERE authorization_id = ? AND source_type = 'manual' AND status = 'active'`), runtimeID).Scan(&manualCount); err != nil {
			return err
		}
		if manualCount > 0 {
			effectiveType = "manual"
			if expiredAlready {
				nextStatus, nextReason = StatusExpired, "authorization_expired"
			} else if grantStatus == StatusPaused {
				// The governing manual grant is paused: the runtime mirrors it.
				nextStatus = StatusPaused
			} else if currentStatus == StatusPaused {
				nextStatus = StatusPaused
			} else {
				nextStatus = StatusActive
			}
		}
	}

	if effectiveType == nil {
		// No active source: terminal state.
		if preserveExpired && expiredAlready {
			nextStatus, nextReason = StatusExpired, "authorization_expired"
		} else {
			nextStatus = terminalStatus
			if nextReason == "" {
				if noActiveSourceReason != "" {
					nextReason = noActiveSourceReason
				} else {
					nextReason = "no_active_source"
				}
			}
		}
	}

	if nextReason == "" {
		nextReason = "authorization_revoked"
	}
	_, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorizations")+`
		SET status = ?, effective_source_type = ?, effective_source_team_id = ?,
			last_source_changed_at = ?, updated_at = ?
		WHERE id = ?`), nextStatus, effectiveType, effectiveTeamID, now, now, runtimeID)
	return err
}

// Patch mirrors patchResourceAuthorizationAsync (status/expiresAt/limits).
type PatchInput struct {
	Status       *string
	ExpiresAt    *string
	ExpiresAtSet bool
	LimitsJSON   *string
	LimitsSet    bool
}

// PatchOutcome mirrors the Node patch outcome.
type PatchOutcome struct {
	Status string // not_found|conflict|unchanged|updated
	Result *Summary
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

func (s *Store) patchForOwner(ctx context.Context, id string, input PatchInput, expectedUpdatedAt, actor, ownerScope string) (*PatchOutcome, error) {
	ctx = ensureCtx(ctx)
	normalizedExpectedUpdatedAt, normalizeErr := normalizeAuthorizationExpectedUpdatedAt(expectedUpdatedAt)
	if normalizeErr != nil {
		return nil, normalizeErr
	}
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
	if !authorizationUpdatedAtEqual(grant.UpdatedAt, normalizedExpectedUpdatedAt) {
		return &PatchOutcome{Status: "conflict"}, nil
	}
	nowTime := s.now().UTC()
	now := nowTime.Format(time.RFC3339Nano)
	if input.Status != nil && *input.Status != StatusActive && *input.Status != StatusPaused {
		return nil, failf("请提供要修改的授权内容")
	}
	hasExpiresAtInput := input.ExpiresAtSet || input.ExpiresAt != nil
	var nextExpiresAt *string
	if grant.ExpiresAt.Valid {
		value := grant.ExpiresAt.String
		nextExpiresAt = &value
	}
	if hasExpiresAtInput {
		if input.ExpiresAt != nil {
			normalized, normalizeErr := normalizeAuthorizationExpiresAt(input.ExpiresAt)
			if normalizeErr != nil {
				return nil, normalizeErr
			}
			nextExpiresAt = normalized
		} else {
			nextExpiresAt = nil
		}
	}
	requestedStatus := ""
	if input.Status != nil {
		requestedStatus = *input.Status
	}
	if grant.Status == StatusExpired && requestedStatus == StatusActive && !hasExpiresAtInput {
		return nil, failf("到期授权恢复时请同时调整过期时间")
	}
	nextStatus := grant.Status
	expiryExpired := false
	if hasExpiresAtInput && nextExpiresAt != nil {
		parsed, valid := parseAuthorizationRFC3339Instant(*nextExpiresAt)
		if valid && !parsed.After(nowTime) {
			expiryExpired = true
			nextStatus = StatusExpired
		} else if grant.Status == StatusExpired {
			nextStatus = StatusActive
		}
	}
	if !expiryExpired && (requestedStatus == StatusActive || requestedStatus == StatusPaused) {
		nextStatus = requestedStatus
	}
	if grant.Status == StatusExpired && hasExpiresAtInput && !expiryExpired && requestedStatus == "" {
		nextStatus = StatusActive
	}
	validateExpiry := hasExpiresAtInput || (requestedStatus == StatusActive && requestedStatus != grant.Status)
	if validateExpiry && nextExpiresAt != nil {
		accountExpiresAt, accountErr := s.patchAccountExpiry(ctx, tx, grant)
		if accountErr != nil {
			return nil, accountErr
		}
		validated, validateErr := validateAuthorizationPatchExpiresAt(nextExpiresAt, accountExpiresAt, nowTime, nextStatus == StatusExpired)
		if validateErr != nil {
			return nil, validateErr
		}
		nextExpiresAt = validated
	}
	hasLimitsInput := input.LimitsSet || input.LimitsJSON != nil
	var nextLimits *string
	if grant.LimitsJSON.Valid {
		value := grant.LimitsJSON.String
		nextLimits = &value
	}
	if hasLimitsInput {
		if input.LimitsJSON == nil {
			nextLimits = nil
		} else {
			normalized, normalizeErr := normalizeAuthorizationLimitsJSON(*input.LimitsJSON)
			if normalizeErr != nil {
				return nil, failf("%s", normalizeErr.Error())
			}
			nextLimits = normalized
		}
	}

	assignments := []string{}
	args := []any{}
	if nextStatus != grant.Status {
		assignments = append(assignments, "status = ?")
		args = append(args, nextStatus)
		if nextStatus == StatusActive || nextStatus == StatusPaused {
			assignments = append(assignments, "revoked_by = NULL", "revoked_at = NULL")
		} else if nextStatus == StatusExpired {
			assignments = append(assignments, "revoked_by = COALESCE(revoked_by, ?)", "revoked_at = COALESCE(revoked_at, ?)")
			args = append(args, actor, now)
		}
	}
	if hasExpiresAtInput && nullableStringValue(nextExpiresAt) != nullableStringValue(nullStringPtr(grant.ExpiresAt)) {
		assignments = append(assignments, "expires_at = ?")
		args = append(args, nullableString(nextExpiresAt))
	}
	if hasLimitsInput && canonicalAuthorizationLimits(nextLimits) != canonicalAuthorizationLimits(nullStringPtr(grant.LimitsJSON)) {
		assignments = append(assignments, "limits_json = ?")
		args = append(args, nullableString(nextLimits))
	}
	if len(assignments) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		summary := grant.summary()
		return &PatchOutcome{Status: "unchanged", Result: &summary}, nil
	}
	assignments = append(assignments, "updated_at = ?")
	args = append(args, NextVersion(grant.UpdatedAt, s.now()))
	args = append(args, id, grant.UpdatedAt)
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
		SET `+strings.Join(assignments, ", ")+` WHERE id = ? AND updated_at = ?`), args...)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return &PatchOutcome{Status: "conflict"}, nil
	}

	// Runtime status sync: resolve runtime rows by the grant identity
	// (direct grant → its runtime row; team grants fan out via M03 wiring).
	runtimeIDs, err := s.resolveRuntimeIDs(ctx, tx, grant)
	if err != nil {
		return nil, err
	}
	newGrantStatus := nextStatus
	for _, runtimeID := range runtimeIDs {
		if nextStatus == StatusActive || nextStatus == StatusPaused {
			sourceType := "manual"
			var sourceTeamID any
			if grant.GranteeTeamID.Valid {
				sourceType = "team"
				sourceTeamID = grant.GranteeTeamID.String
			}
			if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
				SET status = 'active', ended_at = NULL, ended_reason = NULL,
					revoked_by = NULL, revoked_at = NULL, updated_at = ?
				WHERE authorization_id = ? AND source_type = ?
					AND COALESCE(source_team_id,'') = COALESCE(?,'')
					AND status IN ('revoked', 'superseded')`), now, runtimeID, sourceType, sourceTeamID); err != nil {
				return nil, err
			}
		}
		if hasExpiresAtInput {
			if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorizations")+` SET expires_at = ? WHERE id = ?`), nullableString(nextExpiresAt), runtimeID); err != nil {
				return nil, err
			}
		}
		if hasLimitsInput && (nextStatus == StatusActive || nextStatus == StatusPaused || nextStatus == StatusExpired) {
			if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorizations")+` SET limits_json = ? WHERE id = ?`), nullableString(nextLimits), runtimeID); err != nil {
				return nil, err
			}
		}
		if nextStatus == StatusActive || nextStatus == StatusPaused {
			if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorizations")+` SET status = ?, revoked_by = NULL, revoked_at = NULL, revoked_reason = NULL WHERE id = ?`), nextStatus, runtimeID); err != nil {
				return nil, err
			}
		} else if nextStatus == StatusExpired {
			if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorizations")+` SET status = ?, revoked_by = COALESCE(revoked_by, ?), revoked_at = COALESCE(revoked_at, ?), revoked_reason = 'authorization_expired' WHERE id = ?`), nextStatus, actor, now, runtimeID); err != nil {
				return nil, err
			}
		}
		if err := s.refreshEffectiveSourceForRuntimeWithGrant(ctx, tx, runtimeID, actor, now, "", true, StatusRevoked, newGrantStatus); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	summary, err := s.Find(ctx, id)
	if err != nil {
		return nil, err
	}
	return &PatchOutcome{Status: "updated", Result: summary}, nil
}

func (s *Store) patchAccountExpiry(ctx context.Context, tx *sql.Tx, grant *grantRow) (*string, error) {
	if grant.ResourceType != "account" {
		return nil, nil
	}
	var expiry sql.NullString
	err := tx.QueryRowContext(ctx, s.bind(`SELECT account_expires_at FROM `+s.table("accounts")+` WHERE id = ? AND deleted_at IS NULL AND authorization_instance_authorization_id IS NULL`), grant.ResourceID).Scan(&expiry)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !expiry.Valid || strings.TrimSpace(expiry.String) == "" {
		return nil, nil
	}
	value := expiry.String
	return &value, nil
}

func nullStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func nullableStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func runtimeIDForGrant(grant *grantRow) string {
	if grant.GranteeUserID.Valid {
		return grant.GranteeUserID.String
	}
	if grant.GranteeTeamID.Valid {
		return grant.GranteeTeamID.String
	}
	return ""
}

// TerminalMutation mirrors revoke/return outcomes.
type TerminalMutation struct {
	Status           string // not_found|conflict|unchanged|updated
	Result           *Summary
	CurrentUpdatedAt string
}

// Revoke mirrors revokeResourceAuthorizationMutationAsync (owner side).
func (s *Store) Revoke(ctx context.Context, id, expectedUpdatedAt, actor string) (*TerminalMutation, error) {
	return s.terminalMutation(ctx, id, expectedUpdatedAt, actor, StatusRevoked, "authorization_revoked")
}

// Return mirrors returnResourceAuthorizationForGranteeMutationAsync (grantee
// side; direct grants only; manual sources only).
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
	if grant == nil || grant.Status == StatusRevoked || grant.GranteeType != "system_account" ||
		!grant.GranteeUserID.Valid || grant.GranteeUserID.String != granteeUserID {
		return &TerminalMutation{Status: "not_found"}, nil
	}
	if grant.UpdatedAt != expectedUpdatedAt {
		return &TerminalMutation{Status: "conflict", CurrentUpdatedAt: grant.UpdatedAt}, nil
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if grant.Status == StatusReturned {
		summary := grant.summary()
		return &TerminalMutation{Status: "unchanged", Result: &summary}, nil
	}
	if err := s.applyTerminal(ctx, tx, grant, StatusReturned, granteeUserID, now, "grantee_returned", false); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	summary, err := s.Find(ctx, id)
	if err != nil {
		return nil, err
	}
	return &TerminalMutation{Status: "updated", Result: summary}, nil
}

func (s *Store) terminalMutation(ctx context.Context, id, expectedUpdatedAt, actor, terminalStatus, reason string) (*TerminalMutation, error) {
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
	if grant == nil || grant.Status == terminalStatus {
		return &TerminalMutation{Status: "not_found"}, nil
	}
	if grant.UpdatedAt != expectedUpdatedAt {
		return &TerminalMutation{Status: "conflict", CurrentUpdatedAt: grant.UpdatedAt}, nil
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if err := s.applyTerminal(ctx, tx, grant, terminalStatus, actor, now, reason, false); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	summary, err := s.Find(ctx, id)
	if err != nil {
		return nil, err
	}
	return &TerminalMutation{Status: "updated", Result: summary}, nil
}

// resolveRuntimeIDs finds the runtime rows governed by a grant.
func (s *Store) resolveRuntimeIDs(ctx context.Context, tx *sql.Tx, grant *grantRow) ([]string, error) {
	var runtimeIDs []string
	var rows *sql.Rows
	var err error
	if grant.GranteeType == "system_account" && grant.GranteeUserID.Valid {
		rows, err = tx.QueryContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorizations")+`
			WHERE resource_type = ? AND resource_id = ? AND grantee_system_account_id = ?`),
			grant.ResourceType, grant.ResourceID, grant.GranteeUserID.String)
	} else if grant.GranteeTeamID.Valid {
		rows, err = tx.QueryContext(ctx, s.bind(`SELECT DISTINCT r.id FROM `+s.table("resource_authorizations")+` r
			INNER JOIN `+s.table("resource_authorization_sources")+` s ON s.authorization_id = r.id
			INNER JOIN `+s.table("system_team_members")+` m ON m.team_id = s.source_team_id
				AND m.system_account_id = r.grantee_system_account_id AND m.status = 'active'
			INNER JOIN `+s.table("system_accounts")+` a ON a.id = m.system_account_id AND a.status = 'active'
			WHERE s.source_type = 'team' AND s.source_team_id = ?
				AND r.resource_owner_system_account_id <> m.system_account_id
				AND r.resource_type = ? AND r.resource_id = ?`),
			grant.GranteeTeamID.String, grant.ResourceType, grant.ResourceID)
	} else {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var runtimeID string
		if err := rows.Scan(&runtimeID); err != nil {
			return nil, err
		}
		runtimeIDs = append(runtimeIDs, runtimeID)
	}
	return runtimeIDs, rows.Err()
}

// applyTerminal marks the grant terminal, revokes its sources, and refreshes
// the runtime effective source (preserveExpired=false per Node revoke/return).
func (s *Store) applyTerminal(ctx context.Context, tx *sql.Tx, grant *grantRow, terminalStatus, actor, now, reason string, preserveExpired bool) error {
	newVersion := NextVersion(grant.UpdatedAt, s.now())
	_, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
		SET status = ?, revoked_by = ?, revoked_at = ?, updated_at = ?
		WHERE id = ?`), terminalStatus, actor, now, newVersion, grant.ID)
	if err != nil {
		return err
	}
	runtimeIDs, err := s.resolveRuntimeIDs(ctx, tx, grant)
	if err != nil {
		return err
	}
	for _, runtimeID := range runtimeIDs {
		// Revoke active sources of this grant's provenance.
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
			SET status = 'revoked', ended_at = COALESCE(ended_at, ?), ended_reason = ?,
				revoked_by = ?, revoked_at = ?, updated_at = ?
			WHERE authorization_id = ? AND status = 'active'`), now, reason, actor, now, now, runtimeID); err != nil {
			return err
		}
		if err := s.refreshEffectiveSourceForRuntime(ctx, tx, runtimeID, actor, now, reason, preserveExpired, terminalStatus); err != nil {
			return err
		}
	}
	return nil
}

// ExpireSweep mirrors expireDueResourceAuthorizationsAsync: active/paused
// grants past expires_at become expired and runtime rows refresh.
func (s *Store) ExpireSweep(ctx context.Context, limit int) (int, error) {
	ctx = ensureCtx(ctx)
	if limit <= 0 {
		limit = MaxExpirySweepBatchSize
	}
	now := s.now()
	nowText := now.UTC().Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, updated_at FROM `+s.table("resource_authorization_grants")+`
		WHERE status IN ('active','paused') AND expires_at IS NOT NULL AND expires_at <= ?
		ORDER BY expires_at ASC, updated_at ASC, id ASC LIMIT ?`), nowText, limit)
	if err != nil {
		return 0, err
	}
	type due struct {
		id        string
		updatedAt string
	}
	var dueList []due
	for rows.Next() {
		var item due
		if err := rows.Scan(&item.id, &item.updatedAt); err != nil {
			rows.Close()
			return 0, err
		}
		dueList = append(dueList, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	expired := 0
	for _, item := range dueList {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return expired, err
		}
		grant, err := s.GetGrantForMutation(ctx, tx, item.id)
		if err != nil {
			tx.Rollback()
			return expired, err
		}
		if grant == nil || (grant.Status != StatusActive && grant.Status != StatusPaused) {
			tx.Rollback()
			continue
		}
		if err := s.applyTerminal(ctx, tx, grant, StatusExpired, "system", nowText, "authorization_expired", true); err != nil {
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

func nullableJSONEqual(next *string, current string) bool {
	var nextValue any
	if next == nil || strings.TrimSpace(*next) == "" {
		nextValue = map[string]any{}
	} else if err := json.Unmarshal([]byte(*next), &nextValue); err != nil {
		return current == *next
	}
	var currentValue any
	if strings.TrimSpace(current) == "" {
		currentValue = map[string]any{}
	} else if err := json.Unmarshal([]byte(current), &currentValue); err != nil {
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
