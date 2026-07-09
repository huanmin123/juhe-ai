package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/text/unicode/norm"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func (s *Store) ListManagementResourceAuthorizations(ctx context.Context, input port.ManagementResourceAuthorizationListInput) (port.ManagementResourceAuthorizationListResult, error) {
	limit := input.Limit
	if limit <= 0 {
		return port.ManagementResourceAuthorizationListResult{}, nil
	}
	query, args := managementResourceAuthorizationListQuery(input)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return port.ManagementResourceAuthorizationListResult{}, fmt.Errorf("list management resource authorizations: %w", err)
	}
	defer rows.Close()
	items := make([]port.ManagementResourceAuthorizationSummary, 0, limit)
	for rows.Next() {
		var row managementAuthorizationSummaryRow
		if err := rows.Scan(
			&row.Grant.ID,
			&row.Grant.ResourceType,
			&row.Grant.ResourceID,
			&row.Grant.ResourceOwnerSystemAccountID,
			&row.Grant.GranteeType,
			&row.Grant.GranteeSystemAccountID,
			&row.Grant.GranteeTeamID,
			&row.Grant.Scope,
			&row.Grant.Status,
			&row.Grant.Remark,
			&row.Grant.ExpiresAt,
			&row.Grant.LimitsJson,
			&row.Grant.CreatedBy,
			&row.Grant.CreatedAt,
			&row.Grant.RevokedBy,
			&row.Grant.RevokedAt,
			&row.Grant.UpdatedAt,
			&row.AccountName,
			&row.GroupName,
			&row.AccountExpiresAt,
			&row.OwnerDisplayName,
			&row.GranteeDisplayName,
			&row.GranteeUsername,
			&row.TeamName,
		); err != nil {
			return port.ManagementResourceAuthorizationListResult{}, fmt.Errorf("scan management resource authorization: %w", err)
		}
		summary, err := managementAuthorizationSummaryFromRow(row)
		if err != nil {
			return port.ManagementResourceAuthorizationListResult{}, err
		}
		items = append(items, summary)
	}
	if err := rows.Err(); err != nil {
		return port.ManagementResourceAuthorizationListResult{}, fmt.Errorf("iterate management resource authorizations: %w", err)
	}
	pageSize := max(0, limit-1)
	hasMore := len(items) > pageSize
	if hasMore {
		items = items[:pageSize]
	}
	return port.ManagementResourceAuthorizationListResult{Items: items, HasMore: hasMore}, nil
}

func managementResourceAuthorizationListQuery(input port.ManagementResourceAuthorizationListInput) (string, []any) {
	args := []any{}
	clauses := []string{}
	addArg := func(value any) string {
		args = append(args, value)
		return "$" + strconv.Itoa(len(args))
	}
	addClause := func(clause string) {
		clauses = append(clauses, clause)
	}

	if resourceType := strings.TrimSpace(input.ResourceType); resourceType != "" {
		addClause("rag.resource_type = " + addArg(resourceType))
	}
	if resourceID := strings.TrimSpace(input.ResourceID); resourceID != "" {
		addClause("rag.resource_id = " + addArg(resourceID))
	}
	if granteeID := strings.TrimSpace(input.GranteeSystemAccountID); granteeID != "" {
		addClause("rag.grantee_type = " + addArg("system_account"))
		addClause("rag.grantee_system_account_id = " + addArg(granteeID))
	}
	if status := strings.TrimSpace(input.Status); status != "" {
		addClause("rag.status = " + addArg(status))
	}
	if sourceType := strings.TrimSpace(input.SourceType); sourceType == "manual" {
		addClause("rag.grantee_type = " + addArg("system_account"))
	} else if sourceType == "team" {
		addClause("rag.grantee_type = " + addArg("team"))
	}
	if teamID := strings.TrimSpace(input.TeamID); teamID != "" {
		if !input.CanAccessAll {
			addClause(`EXISTS (
  SELECT 1
  FROM juhe_business.system_team_members AS stm_team_scope
  WHERE stm_team_scope.team_id = ` + addArg(teamID) + `
    AND stm_team_scope.system_account_id = ` + addArg(strings.TrimSpace(input.ActorSystemAccountID)) + `
    AND stm_team_scope.status = 'active'
)`)
		}
		addClause("rag.grantee_type = " + addArg("team"))
		addClause("rag.grantee_team_id = " + addArg(teamID))
	}
	if ownerID := strings.TrimSpace(input.ResourceOwnerSystemAccountID); ownerID != "" {
		addClause("rag.resource_owner_system_account_id = " + addArg(ownerID))
	}
	if keyword := strings.TrimSpace(input.Keyword); keyword != "" {
		addClause(managementResourceAuthorizationKeywordClause(keyword, addArg))
	}

	scopeSystemAccountID := strings.TrimSpace(input.ScopedSystemAccountID)
	if scopeSystemAccountID != "" {
		addClause(managementResourceAuthorizationVisibleToAccountClause(scopeSystemAccountID, addArg))
	} else if !input.CanAccessAll {
		addClause(managementResourceAuthorizationVisibleToAccountClause(strings.TrimSpace(input.ActorSystemAccountID), addArg))
	}

	directionSystemAccountID := scopeSystemAccountID
	if directionSystemAccountID == "" && !input.CanAccessAll {
		directionSystemAccountID = strings.TrimSpace(input.ActorSystemAccountID)
	}
	if direction := strings.TrimSpace(input.Direction); direction != "" && directionSystemAccountID != "" {
		if direction == "outbound" {
			addClause("rag.resource_owner_system_account_id = " + addArg(directionSystemAccountID))
		} else if direction == "inbound" {
			accountArg := addArg(directionSystemAccountID)
			teamArg := addArg(directionSystemAccountID)
			addClause(`(rag.grantee_system_account_id = ` + accountArg + ` OR EXISTS (
  SELECT 1
  FROM juhe_business.system_team_members AS stm_direction
  WHERE stm_direction.team_id = rag.grantee_team_id
    AND stm_direction.system_account_id = ` + teamArg + `
    AND stm_direction.status = 'active'
))`)
		}
	}

	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, "\n  AND ")
	}
	limitArg := addArg(max(0, input.Limit))
	offsetArg := addArg(max(0, input.Offset))
	query := `
SELECT rag.id, rag.resource_type, rag.resource_id, rag.resource_owner_system_account_id, rag.grantee_type,
  rag.grantee_system_account_id, rag.grantee_team_id, rag.scope, rag.status, rag.remark, rag.expires_at,
  rag.limits_json, rag.created_by, rag.created_at, rag.revoked_by, rag.revoked_at, rag.updated_at,
  COALESCE(accounts.name, authorization_instance.name) AS account_name,
  groups.name AS group_name,
  accounts.account_expires_at,
  owner_accounts.display_name AS owner_display_name,
  grantee_accounts.display_name AS grantee_display_name,
  grantee_accounts.username AS grantee_username,
  teams.name AS team_name
FROM juhe_business.resource_authorization_grants AS rag
LEFT JOIN juhe_business.accounts AS accounts
  ON accounts.id = rag.resource_id
  AND rag.resource_type = 'account'
LEFT JOIN LATERAL (
  SELECT authorization_instances.name
  FROM juhe_business.resource_authorizations AS resource_runtime
  INNER JOIN juhe_business.accounts AS authorization_instances
    ON authorization_instances.authorization_instance_authorization_id = resource_runtime.id
  WHERE resource_runtime.resource_type = 'account'
    AND resource_runtime.resource_id = rag.resource_id
  ORDER BY resource_runtime.created_at ASC, resource_runtime.id ASC
  LIMIT 1
) AS authorization_instance ON rag.resource_type = 'account'
LEFT JOIN juhe_business.groups AS groups
  ON groups.id = rag.resource_id
  AND rag.resource_type = 'group'
LEFT JOIN juhe_business.system_accounts AS owner_accounts
  ON owner_accounts.id = rag.resource_owner_system_account_id
LEFT JOIN juhe_business.system_accounts AS grantee_accounts
  ON grantee_accounts.id = rag.grantee_system_account_id
LEFT JOIN juhe_business.system_teams AS teams
  ON teams.id = rag.grantee_team_id
` + where + `
ORDER BY rag.created_at DESC, rag.id DESC
LIMIT ` + limitArg + ` OFFSET ` + offsetArg
	return query, args
}

func managementResourceAuthorizationVisibleToAccountClause(systemAccountID string, addArg func(any) string) string {
	ownerArg := addArg(systemAccountID)
	granteeArg := addArg(systemAccountID)
	teamArg := addArg(systemAccountID)
	return `(rag.resource_owner_system_account_id = ` + ownerArg + ` OR rag.grantee_system_account_id = ` + granteeArg + ` OR EXISTS (
  SELECT 1
  FROM juhe_business.system_team_members AS stm_scope
  WHERE stm_scope.team_id = rag.grantee_team_id
    AND stm_scope.system_account_id = ` + teamArg + `
    AND stm_scope.status = 'active'
))`
}

func managementResourceAuthorizationKeywordClause(keyword string, addArg func(any) string) string {
	upperBound := textPrefixUpperBound(keyword)
	matchText := func(expression string) string {
		lowerArg := addArg(keyword)
		upperArg := addArg(upperBound)
		prefixArg := addArg(keyword)
		return `(` + expression + ` COLLATE "C" >= ` + lowerArg + ` AND ` + expression + ` COLLATE "C" < ` + upperArg + ` AND starts_with(` + expression + `, ` + prefixArg + `))`
	}
	return `(
  ` + matchText("rag.id") + `
  OR ` + matchText("rag.resource_id") + `
  OR ` + matchText("rag.remark") + `
  OR ` + matchText("owner_accounts.username") + `
  OR ` + matchText("owner_accounts.display_name") + `
  OR (
    rag.grantee_type = 'system_account'
    AND (
      ` + matchText("grantee_accounts.username") + `
      OR ` + matchText("grantee_accounts.display_name") + `
    )
  )
  OR (
    rag.grantee_type = 'team'
    AND ` + matchText("teams.name") + `
  )
  OR (
    rag.resource_type = 'account'
    AND ` + matchText("accounts.name") + `
  )
  OR (
    rag.resource_type = 'account'
    AND ` + matchText("authorization_instance.name") + `
  )
  OR (
    rag.resource_type = 'group'
    AND ` + matchText("groups.name") + `
  )
)`
}

func (s *Store) CreateManagementResourceAuthorization(ctx context.Context, input port.ManagementResourceAuthorizationCreateInput) (port.ManagementResourceAuthorizationSummary, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementResourceAuthorizationSummary{}, fmt.Errorf("begin management resource authorization create tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	now := input.CreatedAt.UTC()
	ownerID, found, err := managementAuthorizationResourceOwnerTx(ctx, tx, input.ResourceType, input.ResourceID)
	if err != nil {
		return port.ManagementResourceAuthorizationSummary{}, err
	}
	if !found || ownerID != strings.TrimSpace(input.ResourceOwnerSystemAccountID) {
		return port.ManagementResourceAuthorizationSummary{}, fmt.Errorf("授权资源不存在")
	}
	if err := validateManagementAuthorizationExpiresAtTx(ctx, tx, input.ResourceType, input.ResourceID, input.ExpiresAt, now); err != nil {
		return port.ManagementResourceAuthorizationSummary{}, err
	}

	var grant postgresqueries.JuheBusinessResourceAuthorizationGrant
	switch input.GranteeType {
	case "team":
		if err := assertActiveManagementSystemTeamTx(ctx, tx, input.GranteeID); err != nil {
			return port.ManagementResourceAuthorizationSummary{}, err
		}
		members, err := activeManagementTeamMemberIDsTx(ctx, tx, input.GranteeID)
		if err != nil {
			return port.ManagementResourceAuthorizationSummary{}, err
		}
		members = managementAuthorizationNonOwnerMemberIDs(members, ownerID)
		if len(members) == 0 {
			return port.ManagementResourceAuthorizationSummary{}, fmt.Errorf("团队暂无可授权成员，请先添加非归属人成员后再授权")
		}
		grant, err = upsertManagementAuthorizationGrantTx(ctx, tx, input)
		if err != nil {
			return port.ManagementResourceAuthorizationSummary{}, err
		}
		if _, err := activeManagementTeamGrantRowsTx(ctx, tx, input.GranteeID); err != nil {
			return port.ManagementResourceAuthorizationSummary{}, err
		}
		for _, memberID := range members {
			if err := upsertManagementTeamAuthorizationForUserTx(ctx, tx, managementTeamAuthorizationUpsertInput{
				resourceType:                 input.ResourceType,
				resourceID:                   input.ResourceID,
				resourceOwnerSystemAccountID: ownerID,
				granteeSystemAccountID:       memberID,
				sourceTeamID:                 input.GranteeID,
				remark:                       pgTextFromOptional(input.Remark, input.HasRemark),
				expiresAt:                    pgTimestamptzPtr(input.ExpiresAt),
				limitsJSON:                   pgTextFromStringPtr(input.LimitsJSON),
				actor:                        input.ActorSystemAccountID,
				now:                          now,
			}); err != nil {
				return port.ManagementResourceAuthorizationSummary{}, err
			}
		}
	case "system_account":
		if err := assertActiveManagementSystemAccountTx(ctx, tx, input.GranteeID); err != nil {
			return port.ManagementResourceAuthorizationSummary{}, fmt.Errorf("被授权用户不存在或已停用")
		}
		if input.GranteeID == ownerID {
			return port.ManagementResourceAuthorizationSummary{}, fmt.Errorf("不能授权给资源所有者自己")
		}
		grant, err = upsertManagementAuthorizationGrantTx(ctx, tx, input)
		if err != nil {
			return port.ManagementResourceAuthorizationSummary{}, err
		}
		authorization, err := upsertManagementManualAuthorizationForUserTx(ctx, tx, input, ownerID, now)
		if err != nil {
			return port.ManagementResourceAuthorizationSummary{}, err
		}
		if err := bindActiveManagementAccountAuthorizationToGranteeGroupTx(ctx, tx, authorization, input.TargetGroupID, input.AuthorizationInstanceSecretJSON, now); err != nil {
			return port.ManagementResourceAuthorizationSummary{}, err
		}
	default:
		return port.ManagementResourceAuthorizationSummary{}, fmt.Errorf("被授权对象类型无效")
	}

	if err := markAllGroupAccountStatsDirtyIfPresentTx(ctx, tx, "resource_authorization_created", now); err != nil {
		return port.ManagementResourceAuthorizationSummary{}, err
	}
	summary, err := managementAuthorizationSummaryByGrantIDTx(ctx, tx, grant.ID)
	if err != nil {
		return port.ManagementResourceAuthorizationSummary{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return port.ManagementResourceAuthorizationSummary{}, fmt.Errorf("commit management resource authorization create tx rolled back: %w", err)
		}
		return port.ManagementResourceAuthorizationSummary{}, fmt.Errorf("commit management resource authorization create tx: %w", err)
	}
	committed = true
	return summary, nil
}

func (s *Store) ReturnManagementResourceAuthorizationForGrantee(ctx context.Context, input port.ManagementResourceAuthorizationReturnInput) (port.ManagementResourceAuthorizationSummary, bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementResourceAuthorizationSummary{}, false, fmt.Errorf("begin management resource authorization return tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	now := input.ReturnedAt.UTC()
	grant, found, err := findReturnableManagementDirectGrantForGranteeTx(ctx, tx, input.AuthorizationID, input.GranteeSystemAccountID)
	if err != nil {
		return port.ManagementResourceAuthorizationSummary{}, false, err
	}
	if !found {
		return port.ManagementResourceAuthorizationSummary{}, false, nil
	}
	runtimeAuthorization, found, err := findManagementRuntimeAuthorizationForDirectGrantTx(ctx, tx, grant)
	if err != nil {
		return port.ManagementResourceAuthorizationSummary{}, false, err
	}
	if !found || runtimeAuthorization.ResourceOwnerSystemAccountID == input.GranteeSystemAccountID {
		return port.ManagementResourceAuthorizationSummary{}, false, nil
	}
	hasManualSource, err := hasActiveManagementManualAuthorizationSourceTx(ctx, tx, runtimeAuthorization.ID)
	if err != nil {
		return port.ManagementResourceAuthorizationSummary{}, false, err
	}
	if !hasManualSource {
		return port.ManagementResourceAuthorizationSummary{}, false, nil
	}

	if _, err := tx.Exec(ctx, `
UPDATE juhe_business.resource_authorization_grants
SET status = 'returned',
    revoked_by = $1,
    revoked_at = $2,
    updated_at = $2
WHERE id = $3
`, input.ActorSystemAccountID, now, grant.ID); err != nil {
		return port.ManagementResourceAuthorizationSummary{}, false, fmt.Errorf("return resource authorization grant: %w", err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE juhe_business.resource_authorization_sources
SET status = 'revoked',
    ended_at = COALESCE(ended_at, $1),
    ended_reason = COALESCE(ended_reason, 'grantee_returned'),
    revoked_by = $2,
    revoked_at = $1,
    updated_at = $1
WHERE authorization_id = $3
  AND source_type = 'manual'
  AND status IN ('active', 'superseded')
`, now, input.ActorSystemAccountID, runtimeAuthorization.ID); err != nil {
		return port.ManagementResourceAuthorizationSummary{}, false, fmt.Errorf("return manual resource authorization source: %w", err)
	}
	if err := refreshManagementResourceAuthorizationEffectiveSourceWithOptionsTx(ctx, tx, runtimeAuthorization.ID, input.ActorSystemAccountID, now, managementAuthorizationEffectiveSourceRefreshOptions{
		noActiveSourceReason:              "grantee_returned",
		preserveExpiredWhenNoActiveSource: false,
		terminalStatus:                    "returned",
	}); err != nil {
		return port.ManagementResourceAuthorizationSummary{}, false, err
	}
	if err := markAllGroupAccountStatsDirtyIfPresentTx(ctx, tx, "resource_authorization_returned", now); err != nil {
		return port.ManagementResourceAuthorizationSummary{}, false, err
	}
	summary, err := managementAuthorizationSummaryByGrantIDTx(ctx, tx, grant.ID)
	if err != nil {
		return port.ManagementResourceAuthorizationSummary{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return port.ManagementResourceAuthorizationSummary{}, false, fmt.Errorf("commit management resource authorization return tx rolled back: %w", err)
		}
		return port.ManagementResourceAuthorizationSummary{}, false, fmt.Errorf("commit management resource authorization return tx: %w", err)
	}
	committed = true
	return summary, true, nil
}

func findReturnableManagementDirectGrantForGranteeTx(ctx context.Context, tx pgx.Tx, authorizationID string, granteeSystemAccountID string) (postgresqueries.JuheBusinessResourceAuthorizationGrant, bool, error) {
	var row postgresqueries.JuheBusinessResourceAuthorizationGrant
	err := tx.QueryRow(ctx, `
SELECT grant_row.id, grant_row.resource_type, grant_row.resource_id, grant_row.resource_owner_system_account_id,
  grant_row.grantee_type, grant_row.grantee_system_account_id, grant_row.grantee_team_id, grant_row.scope,
  grant_row.status, grant_row.remark, grant_row.expires_at, grant_row.limits_json, grant_row.created_by,
  grant_row.created_at, grant_row.revoked_by, grant_row.revoked_at, grant_row.updated_at
FROM juhe_business.resource_authorization_grants AS grant_row
INNER JOIN juhe_business.resource_authorizations AS runtime_authorization
  ON runtime_authorization.resource_type = grant_row.resource_type
  AND runtime_authorization.resource_id = grant_row.resource_id
  AND runtime_authorization.resource_owner_system_account_id = grant_row.resource_owner_system_account_id
  AND runtime_authorization.grantee_system_account_id = grant_row.grantee_system_account_id
WHERE grant_row.id = $1
  AND grant_row.grantee_type = 'system_account'
  AND grant_row.grantee_system_account_id = $2
  AND grant_row.status NOT IN ('revoked', 'returned')
LIMIT 1
FOR UPDATE OF grant_row
`, authorizationID, granteeSystemAccountID).Scan(
		&row.ID,
		&row.ResourceType,
		&row.ResourceID,
		&row.ResourceOwnerSystemAccountID,
		&row.GranteeType,
		&row.GranteeSystemAccountID,
		&row.GranteeTeamID,
		&row.Scope,
		&row.Status,
		&row.Remark,
		&row.ExpiresAt,
		&row.LimitsJson,
		&row.CreatedBy,
		&row.CreatedAt,
		&row.RevokedBy,
		&row.RevokedAt,
		&row.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return postgresqueries.JuheBusinessResourceAuthorizationGrant{}, false, nil
	}
	if err != nil {
		return postgresqueries.JuheBusinessResourceAuthorizationGrant{}, false, fmt.Errorf("find returnable resource authorization grant: %w", err)
	}
	return row, true, nil
}

func findManagementRuntimeAuthorizationForDirectGrantTx(ctx context.Context, tx pgx.Tx, grant postgresqueries.JuheBusinessResourceAuthorizationGrant) (postgresqueries.JuheBusinessResourceAuthorization, bool, error) {
	granteeSystemAccountID := textValue(grant.GranteeSystemAccountID)
	if granteeSystemAccountID == "" {
		return postgresqueries.JuheBusinessResourceAuthorization{}, false, nil
	}
	row, err := findManagementRuntimeAuthorizationByColumnsTx(ctx, tx, `
WHERE resource_type = $1
  AND resource_id = $2
  AND resource_owner_system_account_id = $3
  AND grantee_system_account_id = $4
LIMIT 1
`, grant.ResourceType, grant.ResourceID, grant.ResourceOwnerSystemAccountID, granteeSystemAccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return postgresqueries.JuheBusinessResourceAuthorization{}, false, nil
	}
	if err != nil {
		return postgresqueries.JuheBusinessResourceAuthorization{}, false, err
	}
	return row, true, nil
}

func hasActiveManagementManualAuthorizationSourceTx(ctx context.Context, tx pgx.Tx, authorizationID string) (bool, error) {
	var id string
	err := tx.QueryRow(ctx, `
SELECT id
FROM juhe_business.resource_authorization_sources
WHERE authorization_id = $1
  AND source_type = 'manual'
  AND status = 'active'
LIMIT 1
`, authorizationID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("find active manual authorization source: %w", err)
	}
	return id != "", nil
}

func managementAuthorizationResourceOwnerTx(ctx context.Context, tx pgx.Tx, resourceType string, resourceID string) (string, bool, error) {
	switch resourceType {
	case "account":
		var ownerID string
		var instanceAuthorizationID pgtype.Text
		err := tx.QueryRow(ctx, `
SELECT system_account_id, authorization_instance_authorization_id
FROM juhe_business.accounts
WHERE id = $1
  AND deleted_at IS NULL
LIMIT 1
`, resourceID).Scan(&ownerID, &instanceAuthorizationID)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		if err != nil {
			return "", false, fmt.Errorf("find authorization resource account: %w", err)
		}
		if instanceAuthorizationID.Valid {
			return "", false, nil
		}
		return ownerID, true, nil
	case "group":
		var ownerID string
		err := tx.QueryRow(ctx, `
SELECT system_account_id
FROM juhe_business.groups
WHERE id = $1
LIMIT 1
`, resourceID).Scan(&ownerID)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		if err != nil {
			return "", false, fmt.Errorf("find authorization resource group: %w", err)
		}
		return ownerID, true, nil
	default:
		return "", false, fmt.Errorf("授权资源不存在")
	}
}

func validateManagementAuthorizationExpiresAtTx(ctx context.Context, tx pgx.Tx, resourceType string, resourceID string, expiresAt *time.Time, now time.Time) error {
	if expiresAt == nil {
		return nil
	}
	if !expiresAt.After(now) {
		return fmt.Errorf("授权到期时间不能早于当前时间")
	}
	if resourceType != "account" {
		return nil
	}
	var accountExpiresAt pgtype.Timestamptz
	err := tx.QueryRow(ctx, `
SELECT account_expires_at
FROM juhe_business.accounts
WHERE id = $1
  AND deleted_at IS NULL
LIMIT 1
`, resourceID).Scan(&accountExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find authorization resource account expiry: %w", err)
	}
	if accountExpiresAt.Valid && expiresAt.After(accountExpiresAt.Time) {
		return fmt.Errorf("授权到期时间不能晚于账户到期时间")
	}
	return nil
}

func assertActiveManagementSystemTeamTx(ctx context.Context, tx pgx.Tx, teamID string) error {
	var id string
	err := tx.QueryRow(ctx, `
SELECT id
FROM juhe_business.system_teams
WHERE id = $1
  AND status = 'active'
LIMIT 1
`, teamID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("团队不存在或已停用")
	}
	if err != nil {
		return fmt.Errorf("find active authorization team: %w", err)
	}
	return nil
}

func managementAuthorizationNonOwnerMemberIDs(memberIDs []string, ownerID string) []string {
	out := make([]string, 0, len(memberIDs))
	for _, id := range memberIDs {
		if id != ownerID {
			out = append(out, id)
		}
	}
	return out
}

func upsertManagementAuthorizationGrantTx(ctx context.Context, tx pgx.Tx, input port.ManagementResourceAuthorizationCreateInput) (postgresqueries.JuheBusinessResourceAuthorizationGrant, error) {
	systemGranteeID, teamGranteeID := "", ""
	if input.GranteeType == "system_account" {
		systemGranteeID = input.GranteeID
	} else {
		teamGranteeID = input.GranteeID
	}
	var activeID string
	err := tx.QueryRow(ctx, `
SELECT id
FROM juhe_business.resource_authorization_grants
WHERE resource_type = $1
  AND resource_id = $2
  AND grantee_type = $3
  AND COALESCE(grantee_system_account_id, '') = $4
  AND COALESCE(grantee_team_id, '') = $5
  AND status = 'active'
LIMIT 1
`, input.ResourceType, input.ResourceID, input.GranteeType, systemGranteeID, teamGranteeID).Scan(&activeID)
	if err == nil {
		if input.GranteeType == "team" {
			return postgresqueries.JuheBusinessResourceAuthorizationGrant{}, fmt.Errorf("该资源已授权给该团队，请勿重复授权")
		}
		return postgresqueries.JuheBusinessResourceAuthorizationGrant{}, fmt.Errorf("该资源已授权给该用户，请勿重复授权")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return postgresqueries.JuheBusinessResourceAuthorizationGrant{}, fmt.Errorf("find active resource authorization grant: %w", err)
	}

	existing, found, err := findReusableManagementAuthorizationGrantTx(ctx, tx, input.ResourceType, input.ResourceID, input.GranteeType, systemGranteeID, teamGranteeID)
	if err != nil {
		return postgresqueries.JuheBusinessResourceAuthorizationGrant{}, err
	}
	id := prefixedUUID("rauthgrant")
	createdBy := input.ActorSystemAccountID
	createdAt := pgTimestamptz(input.CreatedAt)
	if found {
		id = existing.ID
		createdBy = existing.CreatedBy
		createdAt = existing.CreatedAt
		if _, err := tx.Exec(ctx, `
UPDATE juhe_business.resource_authorization_grants
SET status = 'active',
    remark = COALESCE($1::text, remark),
    expires_at = $2,
    limits_json = $3::text,
    revoked_by = NULL,
    revoked_at = NULL,
    updated_at = $4
WHERE id = $5
`, pgTextFromOptional(input.Remark, input.HasRemark), pgTimestamptzPtr(input.ExpiresAt), pgTextFromStringPtr(input.LimitsJSON), input.CreatedAt.UTC(), id); err != nil {
			return postgresqueries.JuheBusinessResourceAuthorizationGrant{}, fmt.Errorf("reactivate resource authorization grant: %w", err)
		}
	} else {
		if _, err := tx.Exec(ctx, `
INSERT INTO juhe_business.resource_authorization_grants (
  id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
  grantee_system_account_id, grantee_team_id, scope, status, remark, expires_at,
  limits_json, created_by, created_at, revoked_by, revoked_at, updated_at
) VALUES (
  $1, $2, $3, $4, $5,
  $6, $7, 'use', 'active', $8::text, $9,
  $10::text, $11, $12, NULL, NULL, $12
)
`, id, input.ResourceType, input.ResourceID, input.ResourceOwnerSystemAccountID, input.GranteeType, pgTextFromString(systemGranteeID), pgTextFromString(teamGranteeID), pgTextFromOptional(input.Remark, input.HasRemark), pgTimestamptzPtr(input.ExpiresAt), pgTextFromStringPtr(input.LimitsJSON), input.ActorSystemAccountID, input.CreatedAt.UTC()); err != nil {
			return postgresqueries.JuheBusinessResourceAuthorizationGrant{}, fmt.Errorf("insert resource authorization grant: %w", err)
		}
	}
	if err := rememberManagementRequestQuotaHourlyWindowTx(ctx, tx, input.LimitHourlyWindowHours, input.CreatedAt.UTC()); err != nil {
		return postgresqueries.JuheBusinessResourceAuthorizationGrant{}, err
	}
	return postgresqueries.JuheBusinessResourceAuthorizationGrant{
		ID:                           id,
		ResourceType:                 input.ResourceType,
		ResourceID:                   input.ResourceID,
		ResourceOwnerSystemAccountID: input.ResourceOwnerSystemAccountID,
		GranteeType:                  input.GranteeType,
		GranteeSystemAccountID:       pgTextFromString(systemGranteeID),
		GranteeTeamID:                pgTextFromString(teamGranteeID),
		Scope:                        "use",
		Status:                       "active",
		Remark:                       pgTextFromOptional(input.Remark, input.HasRemark),
		ExpiresAt:                    pgTimestamptzPtr(input.ExpiresAt),
		LimitsJson:                   pgTextFromStringPtr(input.LimitsJSON),
		CreatedBy:                    createdBy,
		CreatedAt:                    createdAt,
		UpdatedAt:                    pgTimestamptz(input.CreatedAt),
	}, nil
}

func findReusableManagementAuthorizationGrantTx(ctx context.Context, tx pgx.Tx, resourceType string, resourceID string, granteeType string, systemGranteeID string, teamGranteeID string) (postgresqueries.JuheBusinessResourceAuthorizationGrant, bool, error) {
	var row postgresqueries.JuheBusinessResourceAuthorizationGrant
	err := tx.QueryRow(ctx, `
SELECT id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
  grantee_system_account_id, grantee_team_id, scope, status, remark, expires_at,
  limits_json, created_by, created_at, revoked_by, revoked_at, updated_at
FROM juhe_business.resource_authorization_grants
WHERE resource_type = $1
  AND resource_id = $2
  AND grantee_type = $3
  AND COALESCE(grantee_system_account_id, '') = $4
  AND COALESCE(grantee_team_id, '') = $5
  AND status IN ('paused', 'expired', 'revoked', 'returned')
ORDER BY CASE status WHEN 'active' THEN 0 WHEN 'paused' THEN 1 WHEN 'expired' THEN 2 WHEN 'revoked' THEN 3 WHEN 'returned' THEN 4 ELSE 5 END,
  created_at ASC,
  id ASC
LIMIT 1
`, resourceType, resourceID, granteeType, systemGranteeID, teamGranteeID).Scan(
		&row.ID,
		&row.ResourceType,
		&row.ResourceID,
		&row.ResourceOwnerSystemAccountID,
		&row.GranteeType,
		&row.GranteeSystemAccountID,
		&row.GranteeTeamID,
		&row.Scope,
		&row.Status,
		&row.Remark,
		&row.ExpiresAt,
		&row.LimitsJson,
		&row.CreatedBy,
		&row.CreatedAt,
		&row.RevokedBy,
		&row.RevokedAt,
		&row.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return postgresqueries.JuheBusinessResourceAuthorizationGrant{}, false, nil
	}
	if err != nil {
		return postgresqueries.JuheBusinessResourceAuthorizationGrant{}, false, fmt.Errorf("find reusable resource authorization grant: %w", err)
	}
	return row, true, nil
}

func rememberManagementRequestQuotaHourlyWindowTx(ctx context.Context, tx pgx.Tx, hours int, now time.Time) error {
	if hours < 1 || hours > 24*30 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO juhe_business.request_quota_hourly_window_configs (window_hours, created_at, updated_at)
VALUES ($1, $2, $2)
ON CONFLICT (window_hours) DO UPDATE SET updated_at = EXCLUDED.updated_at
`, hours, now.UTC()); err != nil {
		return fmt.Errorf("remember authorization quota hourly window: %w", err)
	}
	return nil
}

func upsertManagementManualAuthorizationForUserTx(ctx context.Context, tx pgx.Tx, input port.ManagementResourceAuthorizationCreateInput, ownerID string, now time.Time) (postgresqueries.JuheBusinessResourceAuthorization, error) {
	existing, found, err := findManagementRuntimeAuthorizationForUserTx(ctx, tx, input.ResourceType, input.ResourceID, input.GranteeID)
	if err != nil {
		return postgresqueries.JuheBusinessResourceAuthorization{}, err
	}
	authorizationID := prefixedUUID("rauth")
	hasActiveTeamSource := false
	firstTeamSourceID := ""
	nextLimitsJSON := pgTextFromStringPtr(input.LimitsJSON)
	if found {
		authorizationID = existing.ID
		hasActiveTeamSource, err = hasActiveManagementTeamAuthorizationSourceTx(ctx, tx, authorizationID, now)
		if err != nil {
			return postgresqueries.JuheBusinessResourceAuthorization{}, err
		}
		if hasActiveTeamSource {
			firstTeamSourceID, err = firstActiveManagementTeamAuthorizationSourceIDTx(ctx, tx, authorizationID, now)
			if err != nil {
				return postgresqueries.JuheBusinessResourceAuthorization{}, err
			}
			nextLimitsJSON = existing.LimitsJson
		}
	}
	effectiveSourceType := "manual"
	effectiveSourceTeamID := pgtype.Text{}
	if hasActiveTeamSource {
		effectiveSourceType = "team"
		effectiveSourceTeamID = pgTextFromString(firstTeamSourceID)
	}
	if found {
		if _, err := tx.Exec(ctx, `
UPDATE juhe_business.resource_authorizations
SET resource_owner_system_account_id = $1,
    status = 'active',
    effective_source_type = $2::text,
    effective_source_team_id = $3::text,
    activated_at = COALESCE(activated_at, $4),
    last_source_changed_at = $4,
    remark = COALESCE($5::text, remark),
    expires_at = $6,
    limits_json = $7::text,
    revoked_by = NULL,
    revoked_at = NULL,
    revoked_reason = NULL,
    updated_at = $4
WHERE id = $8
`, ownerID, effectiveSourceType, effectiveSourceTeamID, now.UTC(), pgTextFromOptional(input.Remark, input.HasRemark), pgTimestamptzPtr(input.ExpiresAt), nextLimitsJSON, authorizationID); err != nil {
			return postgresqueries.JuheBusinessResourceAuthorization{}, fmt.Errorf("update manual resource authorization: %w", err)
		}
	} else {
		if _, err := tx.Exec(ctx, `
INSERT INTO juhe_business.resource_authorizations (
  id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
  scope, status, effective_source_type, effective_source_team_id, activated_at,
  last_source_changed_at, remark, expires_at, limits_json,
  created_by, created_at, revoked_by, revoked_at, revoked_reason, updated_at
) VALUES (
  $1, $2, $3, $4, $5,
  'use', 'active', $6::text, $7::text, $8,
  $8, $9::text, $10, $11::text,
  $12, $8, NULL, NULL, NULL, $8
)
`, authorizationID, input.ResourceType, input.ResourceID, ownerID, input.GranteeID, effectiveSourceType, effectiveSourceTeamID, now.UTC(), pgTextFromOptional(input.Remark, input.HasRemark), pgTimestamptzPtr(input.ExpiresAt), nextLimitsJSON, input.ActorSystemAccountID); err != nil {
			return postgresqueries.JuheBusinessResourceAuthorization{}, fmt.Errorf("insert manual resource authorization: %w", err)
		}
	}
	if err := rememberManagementRequestQuotaHourlyWindowTx(ctx, tx, input.LimitHourlyWindowHours, now); err != nil {
		return postgresqueries.JuheBusinessResourceAuthorization{}, err
	}
	sourceStatus := "active"
	if hasActiveTeamSource {
		sourceStatus = "superseded"
	}
	if err := upsertManagementManualAuthorizationSourceTx(ctx, tx, authorizationID, input.ActorSystemAccountID, now, sourceStatus); err != nil {
		return postgresqueries.JuheBusinessResourceAuthorization{}, err
	}
	if err := refreshManagementResourceAuthorizationEffectiveSourceTx(ctx, tx, authorizationID, input.ActorSystemAccountID, now); err != nil {
		return postgresqueries.JuheBusinessResourceAuthorization{}, err
	}
	return findManagementRuntimeAuthorizationByIDTx(ctx, tx, authorizationID)
}

func findManagementRuntimeAuthorizationForUserTx(ctx context.Context, tx pgx.Tx, resourceType string, resourceID string, granteeSystemAccountID string) (postgresqueries.JuheBusinessResourceAuthorization, bool, error) {
	row, err := findManagementRuntimeAuthorizationByColumnsTx(ctx, tx, `
WHERE resource_type = $1
  AND resource_id = $2
  AND grantee_system_account_id = $3
LIMIT 1
`, resourceType, resourceID, granteeSystemAccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return postgresqueries.JuheBusinessResourceAuthorization{}, false, nil
	}
	if err != nil {
		return postgresqueries.JuheBusinessResourceAuthorization{}, false, err
	}
	return row, true, nil
}

func findManagementRuntimeAuthorizationByIDTx(ctx context.Context, tx pgx.Tx, authorizationID string) (postgresqueries.JuheBusinessResourceAuthorization, error) {
	return findManagementRuntimeAuthorizationByColumnsTx(ctx, tx, "WHERE id = $1 LIMIT 1", authorizationID)
}

func findManagementRuntimeAuthorizationByColumnsTx(ctx context.Context, tx pgx.Tx, where string, args ...any) (postgresqueries.JuheBusinessResourceAuthorization, error) {
	var row postgresqueries.JuheBusinessResourceAuthorization
	err := tx.QueryRow(ctx, `
SELECT id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
  scope, status, effective_source_type, effective_source_team_id, activated_at,
  last_source_changed_at, remark, expires_at, limits_json,
  created_by, created_at, revoked_by, revoked_at, revoked_reason, updated_at
FROM juhe_business.resource_authorizations
`+where, args...).Scan(
		&row.ID,
		&row.ResourceType,
		&row.ResourceID,
		&row.ResourceOwnerSystemAccountID,
		&row.GranteeSystemAccountID,
		&row.Scope,
		&row.Status,
		&row.EffectiveSourceType,
		&row.EffectiveSourceTeamID,
		&row.ActivatedAt,
		&row.LastSourceChangedAt,
		&row.Remark,
		&row.ExpiresAt,
		&row.LimitsJson,
		&row.CreatedBy,
		&row.CreatedAt,
		&row.RevokedBy,
		&row.RevokedAt,
		&row.RevokedReason,
		&row.UpdatedAt,
	)
	if err != nil {
		return postgresqueries.JuheBusinessResourceAuthorization{}, err
	}
	return row, nil
}

func hasActiveManagementTeamAuthorizationSourceTx(ctx context.Context, tx pgx.Tx, authorizationID string, now time.Time) (bool, error) {
	var id string
	err := tx.QueryRow(ctx, `
SELECT ras.id
FROM juhe_business.resource_authorization_sources AS ras
INNER JOIN juhe_business.resource_authorizations AS ra ON ra.id = ras.authorization_id
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
LIMIT 1
`, now.UTC(), authorizationID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("find active team authorization source: %w", err)
	}
	return id != "", nil
}

func firstActiveManagementTeamAuthorizationSourceIDTx(ctx context.Context, tx pgx.Tx, authorizationID string, now time.Time) (string, error) {
	var sourceTeamID string
	err := tx.QueryRow(ctx, `
SELECT ras.source_team_id
FROM juhe_business.resource_authorization_sources AS ras
INNER JOIN juhe_business.resource_authorizations AS ra ON ra.id = ras.authorization_id
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
`, now.UTC(), authorizationID).Scan(&sourceTeamID)
	if err != nil {
		return "", fmt.Errorf("find first active team authorization source: %w", err)
	}
	return sourceTeamID, nil
}

func upsertManagementManualAuthorizationSourceTx(ctx context.Context, tx pgx.Tx, authorizationID string, actor string, now time.Time, requestedStatus string) error {
	var sourceID string
	err := tx.QueryRow(ctx, `
SELECT id
FROM juhe_business.resource_authorization_sources
WHERE authorization_id = $1
  AND source_type = 'manual'
ORDER BY created_at DESC, id DESC
LIMIT 1
`, authorizationID).Scan(&sourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := tx.Exec(ctx, `
INSERT INTO juhe_business.resource_authorization_sources (
  id, authorization_id, source_type, source_team_id, status,
  activated_at, ended_at, ended_reason, created_by, created_at,
  revoked_by, revoked_at, updated_at
) VALUES (
  $1, $2, 'manual', NULL, $3,
  $4, CASE WHEN $3 = 'active' THEN NULL ELSE $4 END,
  CASE WHEN $3 = 'superseded' THEN 'covered_by_team' ELSE NULL END,
  $5, $4, NULL, NULL, $4
)
`, prefixedUUID("rauthsrc"), authorizationID, requestedStatus, now.UTC(), actor); err != nil {
			return fmt.Errorf("insert manual authorization source: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("find manual authorization source: %w", err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE juhe_business.resource_authorization_sources
SET status = $1,
    activated_at = COALESCE(activated_at, $2),
    ended_at = CASE WHEN $1 = 'active' THEN NULL ELSE COALESCE(ended_at, $2) END,
    ended_reason = CASE WHEN $1 = 'active' THEN NULL ELSE COALESCE(ended_reason, 'covered_by_team') END,
    revoked_by = CASE WHEN $1 = 'active' THEN NULL ELSE revoked_by END,
    revoked_at = CASE WHEN $1 = 'active' THEN NULL ELSE revoked_at END,
    updated_at = $2
WHERE id = $3
`, requestedStatus, now.UTC(), sourceID); err != nil {
		return fmt.Errorf("update manual authorization source: %w", err)
	}
	return nil
}

func bindActiveManagementAccountAuthorizationToGranteeGroupTx(ctx context.Context, tx pgx.Tx, authorization postgresqueries.JuheBusinessResourceAuthorization, targetGroupID string, credentialEncrypted string, now time.Time) error {
	if authorization.ResourceType != "account" || authorization.Status != "active" {
		return nil
	}
	if authorization.ExpiresAt.Valid && !authorization.ExpiresAt.Time.After(now) {
		return nil
	}
	instance, ok, err := ensureManagementAuthorizationAccountInstanceTx(ctx, tx, authorization, credentialEncrypted, now)
	if err != nil || !ok {
		return err
	}
	requestedGroupID := strings.TrimSpace(targetGroupID)
	existingGroupID := ""
	err = tx.QueryRow(ctx, `
SELECT group_id
FROM juhe_business.group_accounts
WHERE account_id = $1
  AND system_account_id = $2
  AND account_authorization_id = $3
  AND enabled = true
ORDER BY updated_at DESC, group_id ASC, account_id ASC
LIMIT 1
`, instance.ID, authorization.GranteeSystemAccountID, authorization.ID).Scan(&existingGroupID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("find authorization instance group binding: %w", err)
	}
	if existingGroupID != "" && (requestedGroupID == "" || existingGroupID == requestedGroupID) {
		return nil
	}
	bindGroupID, err := managementAuthorizationBindingGroupIDTx(ctx, tx, instance.ProviderCode, authorization.GranteeSystemAccountID, requestedGroupID)
	if err != nil {
		return err
	}
	if existingGroupID != "" && existingGroupID != bindGroupID {
		if _, err := tx.Exec(ctx, `
DELETE FROM juhe_business.group_accounts
WHERE account_id = $1
  AND system_account_id = $2
  AND account_authorization_id = $3
`, instance.ID, authorization.GranteeSystemAccountID, authorization.ID); err != nil {
			return fmt.Errorf("delete old authorization instance group binding: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO juhe_business.group_accounts (
  system_account_id, group_id, account_id, account_authorization_id, enabled, created_at, updated_at
) VALUES (
  $1, $2, $3, $4, true, $5, $5
)
ON CONFLICT (group_id, account_id) DO UPDATE SET
  system_account_id = EXCLUDED.system_account_id,
  account_authorization_id = EXCLUDED.account_authorization_id,
  enabled = true,
  updated_at = EXCLUDED.updated_at
`, authorization.GranteeSystemAccountID, bindGroupID, instance.ID, authorization.ID, now.UTC()); err != nil {
		return fmt.Errorf("bind authorization instance to group: %w", err)
	}
	return nil
}

type managementAuthorizationAccountInstance struct {
	ID           string
	ProviderCode string
}

func ensureManagementAuthorizationAccountInstanceTx(ctx context.Context, tx pgx.Tx, authorization postgresqueries.JuheBusinessResourceAuthorization, credentialEncrypted string, now time.Time) (managementAuthorizationAccountInstance, bool, error) {
	var existing managementAuthorizationAccountInstance
	err := tx.QueryRow(ctx, `
SELECT id, provider_code
FROM juhe_business.accounts
WHERE authorization_instance_authorization_id = $1
  AND deleted_at IS NULL
LIMIT 1
`, authorization.ID).Scan(&existing.ID, &existing.ProviderCode)
	if err == nil {
		return existing, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return managementAuthorizationAccountInstance{}, false, fmt.Errorf("find authorization account instance: %w", err)
	}
	source, ok, err := managementAuthorizationSourceAccountTx(ctx, tx, authorization.ResourceID)
	if err != nil || !ok {
		return managementAuthorizationAccountInstance{}, false, err
	}
	if source.SystemAccountID == authorization.GranteeSystemAccountID {
		return managementAuthorizationAccountInstance{}, false, nil
	}
	deleted, deletedFound, err := managementAuthorizationDeletedAccountInstanceTx(ctx, tx, authorization.ID)
	if err != nil {
		return managementAuthorizationAccountInstance{}, false, err
	}
	if deletedFound {
		name, err := uniqueManagementAuthorizationInstanceNameTx(ctx, tx, source.Name, authorization.GranteeSystemAccountID, authorization.ID, deleted.ID)
		if err != nil {
			return managementAuthorizationAccountInstance{}, false, err
		}
		if _, err := tx.Exec(ctx, `
UPDATE juhe_business.accounts
SET provider_code = $1,
    provider_protocol_profile_id = $2,
    protocol_code = $3,
    protocol_version = $4,
    name = $5,
    type = $6,
    status = 'active',
    schedulable = true,
    cooldown_until = NULL,
    last_error_code = NULL,
    last_error_message = NULL,
    authorization_instance_source_account_id = $7,
    authorization_instance_owner_system_account_id = $8,
    deleted_at = NULL,
    deleted_by = NULL,
    updated_at = $9
WHERE id = $10
`, source.ProviderCode, source.ProviderProtocolProfileID, source.ProtocolCode, source.ProtocolVersion, name, source.Type, authorization.ResourceID, authorization.ResourceOwnerSystemAccountID, now.UTC(), deleted.ID); err != nil {
			if isPGUniqueViolation(err) {
				return activeManagementAuthorizationAccountInstanceTx(ctx, tx, authorization.ID)
			}
			return managementAuthorizationAccountInstance{}, false, fmt.Errorf("restore authorization account instance: %w", err)
		}
		if err := replaceManagementAccountNameSearchTermsTx(ctx, tx, deleted.ID, authorization.GranteeSystemAccountID, name, now); err != nil {
			return managementAuthorizationAccountInstance{}, false, err
		}
		return managementAuthorizationAccountInstance{ID: deleted.ID, ProviderCode: source.ProviderCode}, true, nil
	}
	id := prefixedUUID("acc")
	name, err := uniqueManagementAuthorizationInstanceNameTx(ctx, tx, source.Name, authorization.GranteeSystemAccountID, authorization.ID, "")
	if err != nil {
		return managementAuthorizationAccountInstance{}, false, err
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO juhe_business.accounts (
  id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
  name, type, status, credentials_encrypted, credential_fingerprint, credential_mask,
  concurrency_limit, priority, super_priority_enabled, fallback_enabled, schedulable,
  authorization_instance_source_account_id, authorization_instance_authorization_id, authorization_instance_owner_system_account_id,
  created_at, updated_at
) VALUES (
  $1, $2, $3, $4, $5, $6,
  $7, $8, 'active', $9, NULL, '',
  $10, 0, false, false, true,
  $11, $12, $13,
  $14, $14
)
`, id, authorization.GranteeSystemAccountID, source.ProviderCode, source.ProviderProtocolProfileID, source.ProtocolCode, source.ProtocolVersion, name, source.Type, credentialEncrypted, source.ConcurrencyLimit, authorization.ResourceID, authorization.ID, authorization.ResourceOwnerSystemAccountID, now.UTC()); err != nil {
		if isPGUniqueViolation(err) {
			return activeManagementAuthorizationAccountInstanceTx(ctx, tx, authorization.ID)
		}
		return managementAuthorizationAccountInstance{}, false, fmt.Errorf("insert authorization account instance: %w", err)
	}
	if err := replaceManagementAccountNameSearchTermsTx(ctx, tx, id, authorization.GranteeSystemAccountID, name, now); err != nil {
		return managementAuthorizationAccountInstance{}, false, err
	}
	return managementAuthorizationAccountInstance{ID: id, ProviderCode: source.ProviderCode}, true, nil
}

type managementAuthorizationAccountRow struct {
	ID                        string
	SystemAccountID           string
	ProviderCode              string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
	Name                      string
	Type                      string
	ConcurrencyLimit          int32
}

func managementAuthorizationSourceAccountTx(ctx context.Context, tx pgx.Tx, accountID string) (managementAuthorizationAccountRow, bool, error) {
	var row managementAuthorizationAccountRow
	err := tx.QueryRow(ctx, `
SELECT id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code,
  protocol_version, name, type, concurrency_limit
FROM juhe_business.accounts
WHERE id = $1
  AND deleted_at IS NULL
LIMIT 1
`, accountID).Scan(&row.ID, &row.SystemAccountID, &row.ProviderCode, &row.ProviderProtocolProfileID, &row.ProtocolCode, &row.ProtocolVersion, &row.Name, &row.Type, &row.ConcurrencyLimit)
	if errors.Is(err, pgx.ErrNoRows) {
		return managementAuthorizationAccountRow{}, false, nil
	}
	if err != nil {
		return managementAuthorizationAccountRow{}, false, fmt.Errorf("find authorization source account: %w", err)
	}
	return row, true, nil
}

func managementAuthorizationDeletedAccountInstanceTx(ctx context.Context, tx pgx.Tx, authorizationID string) (managementAuthorizationAccountInstance, bool, error) {
	var row managementAuthorizationAccountInstance
	err := tx.QueryRow(ctx, `
SELECT id, provider_code
FROM juhe_business.accounts
WHERE authorization_instance_authorization_id = $1
  AND deleted_at IS NOT NULL
ORDER BY deleted_at DESC, updated_at DESC, id ASC
LIMIT 1
`, authorizationID).Scan(&row.ID, &row.ProviderCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return managementAuthorizationAccountInstance{}, false, nil
	}
	if err != nil {
		return managementAuthorizationAccountInstance{}, false, fmt.Errorf("find deleted authorization account instance: %w", err)
	}
	return row, true, nil
}

func activeManagementAuthorizationAccountInstanceTx(ctx context.Context, tx pgx.Tx, authorizationID string) (managementAuthorizationAccountInstance, bool, error) {
	var row managementAuthorizationAccountInstance
	err := tx.QueryRow(ctx, `
SELECT id, provider_code
FROM juhe_business.accounts
WHERE authorization_instance_authorization_id = $1
  AND deleted_at IS NULL
LIMIT 1
`, authorizationID).Scan(&row.ID, &row.ProviderCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return managementAuthorizationAccountInstance{}, false, nil
	}
	if err != nil {
		return managementAuthorizationAccountInstance{}, false, fmt.Errorf("find active authorization account instance: %w", err)
	}
	return row, true, nil
}

func uniqueManagementAuthorizationInstanceNameTx(ctx context.Context, tx pgx.Tx, sourceName string, systemAccountID string, authorizationID string, exceptAccountID string) (string, error) {
	baseName := strings.TrimSpace(sourceName)
	if baseName == "" {
		baseName = "授权账户"
	}
	shortID := authorizationID
	if idx := strings.LastIndex(shortID, "_"); idx >= 0 && idx+1 < len(shortID) {
		shortID = shortID[idx+1:]
	}
	if len(shortID) > 6 {
		shortID = shortID[:6]
	}
	candidates := []string{baseName, baseName + "-" + shortID}
	for index := 2; index <= 1000; index++ {
		candidates = append(candidates, fmt.Sprintf("%s-%s-%d", baseName, shortID, index))
	}
	for _, candidate := range candidates {
		available, err := managementAuthorizationAccountNameAvailableTx(ctx, tx, systemAccountID, candidate, exceptAccountID)
		if err != nil {
			return "", err
		}
		if available {
			return candidate, nil
		}
	}
	return fmt.Sprintf("%s-%s-%d", baseName, shortID, time.Now().UnixMilli()), nil
}

func managementAuthorizationAccountNameAvailableTx(ctx context.Context, tx pgx.Tx, systemAccountID string, name string, exceptAccountID string) (bool, error) {
	args := []any{systemAccountID, strings.ToLower(name)}
	exceptClause := ""
	if exceptAccountID != "" {
		args = append(args, exceptAccountID)
		exceptClause = " AND id <> $3"
	}
	var id string
	err := tx.QueryRow(ctx, `
SELECT id
FROM juhe_business.accounts
WHERE system_account_id = $1
  AND lower(name) = $2
  AND deleted_at IS NULL`+exceptClause+`
LIMIT 1
`, args...).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("check authorization account instance name: %w", err)
	}
	return false, nil
}

func managementAuthorizationBindingGroupIDTx(ctx context.Context, tx pgx.Tx, providerCode string, systemAccountID string, targetGroupID string) (string, error) {
	if strings.TrimSpace(targetGroupID) != "" {
		var id, ownerID, groupProviderCode string
		var enabled bool
		err := tx.QueryRow(ctx, `
SELECT id, system_account_id, provider_code, enabled
FROM juhe_business.groups
WHERE id = $1
LIMIT 1
	`, targetGroupID).Scan(&id, &ownerID, &groupProviderCode, &enabled)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("目标分组不存在或不属于被授权用户")
		}
		if err != nil {
			return "", fmt.Errorf("find authorization target group: %w", err)
		}
		if ownerID != systemAccountID {
			return "", fmt.Errorf("目标分组不存在或不属于被授权用户")
		}
		if groupProviderCode != providerCode {
			return "", fmt.Errorf("目标分组供应商与授权账户不一致")
		}
		if !enabled {
			return "", fmt.Errorf("目标分组已停用，请选择启用分组")
		}
		return id, nil
	}
	var id string
	err := tx.QueryRow(ctx, `
SELECT id
FROM juhe_business.groups
WHERE system_account_id = $1
  AND provider_code = $2
  AND is_default = true
  AND enabled = true
ORDER BY updated_at DESC, id ASC
LIMIT 1
`, systemAccountID, providerCode).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("目标用户缺少启用的默认分组，请按当前数据契约修复目标用户分组后再授权")
	}
	if err != nil {
		return "", fmt.Errorf("find authorization default target group: %w", err)
	}
	return id, nil
}

func replaceManagementAccountNameSearchTermsTx(ctx context.Context, tx pgx.Tx, accountID string, systemAccountID string, name string, now time.Time) error {
	if _, err := tx.Exec(ctx, `DELETE FROM juhe_business.account_name_search_terms WHERE account_id = $1`, accountID); err != nil {
		return fmt.Errorf("delete account name search terms: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM juhe_business.account_name_search_documents WHERE account_id = $1`, accountID); err != nil {
		return fmt.Errorf("delete account name search document: %w", err)
	}
	normalizedName := strings.TrimSpace(norm.NFKC.String(name))
	if normalizedName == "" {
		return nil
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO juhe_business.account_name_search_documents (
  account_id, system_account_id, normalized_name, updated_at
) VALUES ($1, $2, $3, $4)
`, accountID, systemAccountID, normalizedName, now.UTC()); err != nil {
		return fmt.Errorf("insert account name search document: %w", err)
	}
	for _, term := range managementAccountNameSearchTerms(normalizedName) {
		if _, err := tx.Exec(ctx, `
INSERT INTO juhe_business.account_name_search_terms (
  account_id, system_account_id, term, created_at
) VALUES ($1, $2, $3, $4)
ON CONFLICT (account_id, term) DO NOTHING
`, accountID, systemAccountID, term, now.UTC()); err != nil {
			return fmt.Errorf("insert account name search term: %w", err)
		}
	}
	return nil
}

func managementAccountNameSearchTerms(normalizedName string) []string {
	terms := map[string]bool{}
	runes := []rune(normalizedName)
	for length := 1; length <= 3; length++ {
		if len(runes) < length {
			continue
		}
		for index := 0; index+length <= len(runes); index++ {
			term := string(runes[index : index+length])
			if strings.TrimSpace(term) != "" {
				terms[term] = true
			}
		}
	}
	out := make([]string, 0, len(terms))
	for term := range terms {
		out = append(out, term)
	}
	return out
}

type managementAuthorizationSummaryRow struct {
	Grant              postgresqueries.JuheBusinessResourceAuthorizationGrant
	AccountName        pgtype.Text
	GroupName          pgtype.Text
	AccountExpiresAt   pgtype.Timestamptz
	OwnerDisplayName   pgtype.Text
	GranteeDisplayName pgtype.Text
	GranteeUsername    pgtype.Text
	TeamName           pgtype.Text
}

func managementAuthorizationSummaryByGrantIDTx(ctx context.Context, tx pgx.Tx, grantID string) (port.ManagementResourceAuthorizationSummary, error) {
	var row managementAuthorizationSummaryRow
	err := tx.QueryRow(ctx, `
SELECT rag.id, rag.resource_type, rag.resource_id, rag.resource_owner_system_account_id, rag.grantee_type,
  rag.grantee_system_account_id, rag.grantee_team_id, rag.scope, rag.status, rag.remark, rag.expires_at,
  rag.limits_json, rag.created_by, rag.created_at, rag.revoked_by, rag.revoked_at, rag.updated_at,
  accounts.name AS account_name,
  groups.name AS group_name,
  accounts.account_expires_at,
  owner_accounts.display_name AS owner_display_name,
  grantee_accounts.display_name AS grantee_display_name,
  grantee_accounts.username AS grantee_username,
  teams.name AS team_name
FROM juhe_business.resource_authorization_grants AS rag
LEFT JOIN juhe_business.accounts AS accounts
  ON accounts.id = rag.resource_id
  AND rag.resource_type = 'account'
LEFT JOIN juhe_business.groups AS groups
  ON groups.id = rag.resource_id
  AND rag.resource_type = 'group'
LEFT JOIN juhe_business.system_accounts AS owner_accounts
  ON owner_accounts.id = rag.resource_owner_system_account_id
LEFT JOIN juhe_business.system_accounts AS grantee_accounts
  ON grantee_accounts.id = rag.grantee_system_account_id
LEFT JOIN juhe_business.system_teams AS teams
  ON teams.id = rag.grantee_team_id
WHERE rag.id = $1
LIMIT 1
`, grantID).Scan(
		&row.Grant.ID,
		&row.Grant.ResourceType,
		&row.Grant.ResourceID,
		&row.Grant.ResourceOwnerSystemAccountID,
		&row.Grant.GranteeType,
		&row.Grant.GranteeSystemAccountID,
		&row.Grant.GranteeTeamID,
		&row.Grant.Scope,
		&row.Grant.Status,
		&row.Grant.Remark,
		&row.Grant.ExpiresAt,
		&row.Grant.LimitsJson,
		&row.Grant.CreatedBy,
		&row.Grant.CreatedAt,
		&row.Grant.RevokedBy,
		&row.Grant.RevokedAt,
		&row.Grant.UpdatedAt,
		&row.AccountName,
		&row.GroupName,
		&row.AccountExpiresAt,
		&row.OwnerDisplayName,
		&row.GranteeDisplayName,
		&row.GranteeUsername,
		&row.TeamName,
	)
	if err != nil {
		return port.ManagementResourceAuthorizationSummary{}, fmt.Errorf("find created resource authorization summary: %w", err)
	}
	return managementAuthorizationSummaryFromRow(row)
}

func managementAuthorizationSummaryFromRow(row managementAuthorizationSummaryRow) (port.ManagementResourceAuthorizationSummary, error) {
	limits, err := managementAuthorizationLimitsFromJSON(row.Grant.LimitsJson)
	if err != nil {
		return port.ManagementResourceAuthorizationSummary{}, err
	}
	resourceName := textValue(row.AccountName)
	if row.Grant.ResourceType == "group" {
		resourceName = textValue(row.GroupName)
	}
	sourceType := "manual"
	if row.Grant.GranteeType == "team" {
		sourceType = "team"
	}
	sourceStatus := "active"
	if row.Grant.Status != "active" && row.Grant.Status != "paused" {
		sourceStatus = "revoked"
	}
	source := port.ManagementResourceAuthorizationSourceSummary{
		ID:              row.Grant.ID,
		AuthorizationID: row.Grant.ID,
		SourceType:      sourceType,
		SourceTeamID:    textValue(row.Grant.GranteeTeamID),
		SourceTeamName:  textValue(row.TeamName),
		Status:          sourceStatus,
		ActivatedAt:     timePtr(timestamptzValue(row.Grant.CreatedAt)),
		CreatedBy:       row.Grant.CreatedBy,
		CreatedAt:       timestamptzValue(row.Grant.CreatedAt),
		RevokedBy:       textValue(row.Grant.RevokedBy),
		RevokedAt:       timePtrFromTimestamptz(row.Grant.RevokedAt),
		UpdatedAt:       timestamptzValue(row.Grant.UpdatedAt),
	}
	if row.Grant.Status == "expired" {
		source.EndedAt = managementAuthorizationEndedAt(row.Grant)
		source.EndedReason = "authorization_expired"
	} else if row.Grant.Status == "returned" {
		source.EndedAt = managementAuthorizationEndedAt(row.Grant)
		source.EndedReason = "grantee_returned"
	} else if row.Grant.Status == "revoked" {
		source.EndedAt = managementAuthorizationEndedAt(row.Grant)
		source.EndedReason = "authorization_revoked"
	}
	return port.ManagementResourceAuthorizationSummary{
		ID:                             row.Grant.ID,
		ResourceType:                   row.Grant.ResourceType,
		ResourceID:                     row.Grant.ResourceID,
		ResourceName:                   resourceName,
		ResourceOwnerSystemAccountID:   row.Grant.ResourceOwnerSystemAccountID,
		ResourceOwnerSystemAccountName: textValue(row.OwnerDisplayName),
		GranteeType:                    row.Grant.GranteeType,
		GranteeSystemAccountID:         textValue(row.Grant.GranteeSystemAccountID),
		GranteeSystemAccountName:       textValue(row.GranteeDisplayName),
		GranteeUsername:                textValue(row.GranteeUsername),
		GranteeTeamID:                  textValue(row.Grant.GranteeTeamID),
		GranteeTeamName:                textValue(row.TeamName),
		Scope:                          "use",
		Status:                         row.Grant.Status,
		Remark:                         textValue(row.Grant.Remark),
		ExpiresAt:                      timePtrFromTimestamptz(row.Grant.ExpiresAt),
		Limits:                         limits,
		ResourceAccountExpiresAt:       timePtrFromTimestamptz(row.AccountExpiresAt),
		EffectiveSourceType:            sourceType,
		EffectiveSourceTeamID:          textValue(row.Grant.GranteeTeamID),
		EffectiveSourceTeamName:        textValue(row.TeamName),
		ActivatedAt:                    timePtr(timestamptzValue(row.Grant.CreatedAt)),
		LastSourceChangedAt:            timePtr(timestamptzValue(row.Grant.UpdatedAt)),
		AuthorizationSources:           []port.ManagementResourceAuthorizationSourceSummary{source},
		Usage:                          port.ManagementAccountUsageSummary{},
		CreatedBy:                      row.Grant.CreatedBy,
		CreatedAt:                      timestamptzValue(row.Grant.CreatedAt),
		RevokedBy:                      textValue(row.Grant.RevokedBy),
		RevokedAt:                      timePtrFromTimestamptz(row.Grant.RevokedAt),
		RevokedReason:                  managementAuthorizationRevokedReason(row.Grant.Status),
		UpdatedAt:                      timestamptzValue(row.Grant.UpdatedAt),
	}, nil
}

func managementAuthorizationEndedAt(grant postgresqueries.JuheBusinessResourceAuthorizationGrant) *time.Time {
	if grant.RevokedAt.Valid {
		return timePtr(grant.RevokedAt.Time)
	}
	return timePtr(timestamptzValue(grant.UpdatedAt))
}

func managementAuthorizationRevokedReason(status string) string {
	switch status {
	case "expired":
		return "authorization_expired"
	case "returned":
		return "grantee_returned"
	case "revoked":
		return "authorization_revoked"
	default:
		return ""
	}
}

func managementAuthorizationLimitsFromJSON(value pgtype.Text) (port.ManagementRequestQuotaLimits, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return port.ManagementRequestQuotaLimits{}, nil
	}
	var limits port.ManagementRequestQuotaLimits
	if err := json.Unmarshal([]byte(value.String), &limits); err != nil {
		return port.ManagementRequestQuotaLimits{}, fmt.Errorf("authorization limits json is invalid: %w", err)
	}
	return limits, nil
}

func pgTextFromOptional(value string, valid bool) pgtype.Text {
	if !valid {
		return pgtype.Text{}
	}
	return pgtype.Text{String: strings.TrimSpace(value), Valid: true}
}

func pgTextFromString(value string) pgtype.Text {
	if strings.TrimSpace(value) == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: strings.TrimSpace(value), Valid: true}
}

func pgTextFromStringPtr(value *string) pgtype.Text {
	if value == nil || strings.TrimSpace(*value) == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: strings.TrimSpace(*value), Valid: true}
}

func timePtr(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func timePtrFromTimestamptz(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	return timePtr(value.Time)
}

var _ port.ManagementResourceAuthorizationCreator = (*Store)(nil)
var _ port.ManagementResourceAuthorizationLister = (*Store)(nil)
var _ port.ManagementResourceAuthorizationReturner = (*Store)(nil)
