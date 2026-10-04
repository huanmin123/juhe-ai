package cleanuprepo

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// account-delete-cleanup.repository.ts 的过期逻辑删除物理清理移植
// （cleanupExpiredLogicallyDeletedAccounts / Async）。
//
// 边界（显式登记，不静默）：孤儿授权实例扫尾（orphan sweep）双方言执行，
// SQLite 臂与 PG 臂同 bulk 终态语义；Node SQLite 同步臂的 per-grant 链
// （returnResourceAuthorizationGrant → syncUserGrantRuntime →
// refreshResourceAuthorizationEffectiveSource，含 effective_source 落列与
// 进程内缓存失效）不随迁——扫尾场景（资源账户已删/已逻辑删）下两臂终态
// 等价（都到 revoked，对照 gateway internal/accounts/delete.go 双臂注释），
// effective_source 等运行态列随 revoke 一并置空。物理清理主链双模逐函数移植。

// DeletedAccountPhysicalCleanupRetentionMonths 照 Node 常量（1 个月）。
const DeletedAccountPhysicalCleanupRetentionMonths = 1

// DeletedAccountPhysicalCleanupBatchSize 照 Node 常量。
const DeletedAccountPhysicalCleanupBatchSize = 20

// internalAccountReadAccessSystemAccountID 照 internalAccountReadAccess。
const internalAccountReadAccessSystemAccountID = "sys_admin"

// DeletedAccountStore 承载 business 库物理清理访问。
type DeletedAccountStore struct {
	Business *DB
	Dataset  *DB
	Stats    *DB
	// UsageCatalog 是 SQLite usage catalog（SQLite 模式相关记录检查用）。
	UsageCatalog *DB
	Shards       *ShardStore
	// Records 承载 SQLite 相关记录检查（targets/usage shards/stats rows）。
	Records *RecordCleanupStore
	// LastTargetError 记录最近一个目标失败的诊断信息（组合根可上报）。
	LastTargetError string
	Now             func() time.Time
}

func (s *DeletedAccountStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *DeletedAccountStore) table(name string) string {
	return s.Business.Table("juhe_business", name)
}

// CleanupExpired 照 cleanupExpiredLogicallyDeletedAccounts / Async。
func (s *DeletedAccountStore) CleanupExpired(ctx context.Context) (*retention.ExpiredDeletedAccountSummary, error) {
	now := s.now()
	cutoff := now.UTC().AddDate(0, -DeletedAccountPhysicalCleanupRetentionMonths, 0)
	cutoffDeletedAt := ISOOf(cutoff)
	limit := DeletedAccountPhysicalCleanupBatchSize
	summary := &retention.ExpiredDeletedAccountSummary{CutoffDeletedAt: cutoffDeletedAt}

	// 孤儿授权实例扫尾：统一入口按方言分派（hasRelatedRecordData 同款分派
	// 模式），双模交付即生效。
	orphaned, err := s.orphanSweep(ctx, limit)
	if err != nil {
		return nil, err
	}
	summary.OrphanedAuthorizationInstances = int64(len(orphaned))

	candidates, err := s.listCandidates(ctx, cutoffDeletedAt, limit)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		summary.Attempted++
		target, err := s.buildTarget(ctx, candidate)
		if err != nil {
			summary.Failed++
			s.LastTargetError = fmt.Sprintf("buildTarget %s: %v", candidate.ID, err)
			continue
		}
		related, err := s.hasRelatedRecordData(ctx, target)
		if err != nil {
			summary.Failed++
			s.LastTargetError = fmt.Sprintf("hasRelatedRecordData %s: %v", candidate.ID, err)
			continue
		}
		if related {
			summary.Deferred++
			summary.RecordCleanupTargets = append(summary.RecordCleanupTargets, retention.ExpiredDeletedAccountTarget{
				AccountID:         candidate.ID,
				SystemAccountID:   candidate.SystemAccountID,
				RelatedAccountIDs: target.RelatedAccountIDs,
				AuthorizationIDs:  target.AuthorizationIDs,
				TeamScopeIDs:      target.TeamScopeIDs,
			})
			continue
		}
		deleted, err := s.physicallyDelete(ctx, candidate.ID, target)
		if err != nil {
			summary.Failed++
			s.LastTargetError = fmt.Sprintf("physicallyDelete %s: %v", candidate.ID, err)
			continue
		}
		summary.PhysicallyDeletedAccounts += deleted.Accounts
		summary.PhysicallyDeletedAuthorizations += deleted.Authorizations
		summary.PhysicallyDeletedGrants += deleted.Grants
		summary.PhysicallyDeletedGroupBindings += deleted.GroupBindings
		summary.Completed++
	}
	summary.DeletedRows = summary.PhysicallyDeletedAccounts + summary.PhysicallyDeletedAuthorizations +
		summary.PhysicallyDeletedGrants + summary.PhysicallyDeletedGroupBindings
	return summary, nil
}

type candidateRow struct {
	ID                                   string
	SystemAccountID                      string
	AuthorizationInstanceAuthorizationID string
	AuthorizationInstanceSourceAccountID string
	DeletedAt                            string
	UpdatedAt                            string
}

func (s *DeletedAccountStore) listCandidates(ctx context.Context, cutoffDeletedAt string, limit int) ([]candidateRow, error) {
	rootQuery := s.Business.Bind(fmt.Sprintf(`
      SELECT id, system_account_id, authorization_instance_authorization_id,
        authorization_instance_source_account_id, deleted_at, updated_at
      FROM %s
      WHERE deleted_at IS NOT NULL
        AND deleted_at <= ?
        AND authorization_instance_authorization_id IS NULL
      ORDER BY deleted_at ASC, updated_at ASC, id ASC
      LIMIT ?
	`, s.table("accounts")))
	rootRows, err := queryRows(ctx, s.Business, rootQuery, cutoffDeletedAt, limit)
	if err != nil {
		return nil, err
	}
	var output []candidateRow
	for _, row := range rootRows {
		output = append(output, candidateFromRow(row))
	}
	remaining := limit - len(output)
	if remaining <= 0 {
		return output, nil
	}
	instanceQuery := s.Business.Bind(fmt.Sprintf(`
      SELECT child.id, child.system_account_id, child.authorization_instance_authorization_id,
        child.authorization_instance_source_account_id, child.deleted_at, child.updated_at
      FROM %s child
      LEFT JOIN %s source_accounts ON source_accounts.id = child.authorization_instance_source_account_id
      WHERE child.deleted_at IS NOT NULL
        AND child.deleted_at <= ?
        AND child.authorization_instance_authorization_id IS NOT NULL
        AND (
          child.authorization_instance_source_account_id IS NULL
          OR source_accounts.id IS NULL
          OR source_accounts.deleted_at IS NULL
          OR source_accounts.deleted_at > ?
        )
      ORDER BY child.deleted_at ASC, child.updated_at ASC, child.id ASC
      LIMIT ?
	`, s.table("accounts"), s.table("accounts")))
	instanceRows, err := queryRows(ctx, s.Business, instanceQuery, cutoffDeletedAt, cutoffDeletedAt, remaining)
	if err != nil {
		return nil, err
	}
	for _, row := range instanceRows {
		output = append(output, candidateFromRow(row))
	}
	return output, nil
}

func candidateFromRow(row row) candidateRow {
	return candidateRow{
		ID:                                   textOf(row["id"]),
		SystemAccountID:                      textOf(row["system_account_id"]),
		AuthorizationInstanceAuthorizationID: textOf(row["authorization_instance_authorization_id"]),
		AuthorizationInstanceSourceAccountID: textOf(row["authorization_instance_source_account_id"]),
		DeletedAt:                            textOf(row["deleted_at"]),
		UpdatedAt:                            textOf(row["updated_at"]),
	}
}

type cleanupTarget struct {
	AccountID         string
	SystemAccountID   string
	RelatedAccountIDs []string
	AccountIDs        []string
	AuthorizationIDs  []string
	TeamScopeIDs      []string
	GrantIDs          []string
}

// buildTarget 照 buildExpiredDeletedAccountBusinessCleanupTarget / Async。
func (s *DeletedAccountStore) buildTarget(ctx context.Context, candidate candidateRow) (*cleanupTarget, error) {
	isAuthorizationInstance := candidate.AuthorizationInstanceAuthorizationID != ""
	var relatedRows []row
	var err error
	if !isAuthorizationInstance {
		relatedRows, err = queryRows(ctx, s.Business, s.Business.Bind(fmt.Sprintf(`
        SELECT id, authorization_instance_authorization_id
        FROM %s
        WHERE authorization_instance_source_account_id = ?
        ORDER BY created_at ASC, id ASC
		`, s.table("accounts"))), candidate.ID)
		if err != nil {
			return nil, err
		}
	}
	relatedAccountIDs := make([]string, 0, len(relatedRows))
	for _, relatedRow := range relatedRows {
		relatedAccountIDs = append(relatedAccountIDs, textOf(relatedRow["id"]))
	}
	relatedAccountIDs = uniqueNonEmpty(relatedAccountIDs)
	accountIDs := uniqueNonEmpty(append([]string{candidate.ID}, relatedAccountIDs...))
	authorizationInstanceIDsByAuthorizationID := map[string]string{}
	if candidate.AuthorizationInstanceAuthorizationID != "" {
		authorizationInstanceIDsByAuthorizationID[candidate.AuthorizationInstanceAuthorizationID] = candidate.ID
	}
	for _, relatedRow := range relatedRows {
		authorizationID := strings.TrimSpace(textOf(relatedRow["authorization_instance_authorization_id"]))
		accountID := strings.TrimSpace(textOf(relatedRow["id"]))
		if authorizationID != "" && accountID != "" {
			authorizationInstanceIDsByAuthorizationID[authorizationID] = accountID
		}
	}
	authorizationRows, err := s.loadAuthorizationRows(ctx, accountIDs, mapValues(authorizationInstanceIDsByAuthorizationID))
	if err != nil {
		return nil, err
	}
	loadedAuthorizationIDs := make([]string, 0, len(authorizationRows))
	authorizationResourceIDByID := map[string]string{}
	for _, authorizationRow := range authorizationRows {
		loadedAuthorizationIDs = append(loadedAuthorizationIDs, authorizationRow.ID)
		if authorizationRow.ResourceID != "" {
			authorizationResourceIDByID[authorizationRow.ID] = authorizationRow.ResourceID
		}
	}
	loadedAuthorizationIDs = uniqueNonEmpty(loadedAuthorizationIDs)
	activeAuthorizationIDs := map[string]bool{}
	if isAuthorizationInstance {
		activeAuthorizationIDs, err = s.loadActiveAuthorizationInstanceIDs(ctx, loadedAuthorizationIDs)
		if err != nil {
			return nil, err
		}
	}
	authorizationIDs := make([]string, 0, len(loadedAuthorizationIDs))
	for _, authorizationID := range loadedAuthorizationIDs {
		if !activeAuthorizationIDs[authorizationID] {
			authorizationIDs = append(authorizationIDs, authorizationID)
		}
	}
	teamScopeIDs, err := s.loadTeamScopeIDs(ctx, authorizationIDs, authorizationInstanceIDsByAuthorizationID, authorizationResourceIDByID, candidate.ID)
	if err != nil {
		return nil, err
	}
	var grantIDs []string
	if isAuthorizationInstance {
		grantIDs, err = s.loadAuthorizationInstanceGrantIDs(ctx, authorizationIDs)
	} else {
		grantIDs, err = s.loadSourceAccountGrantIDs(ctx, accountIDs)
	}
	if err != nil {
		return nil, err
	}
	return &cleanupTarget{
		AccountID:         candidate.ID,
		SystemAccountID:   candidate.SystemAccountID,
		RelatedAccountIDs: relatedAccountIDs,
		AccountIDs:        accountIDs,
		AuthorizationIDs:  authorizationIDs,
		TeamScopeIDs:      teamScopeIDs,
		GrantIDs:          grantIDs,
	}, nil
}

type authorizationRow struct {
	ID         string
	ResourceID string
	GranteeID  string
}

func (s *DeletedAccountStore) loadAuthorizationRows(ctx context.Context, accountIDs, authorizationInstanceAuthorizationIDs []string) ([]authorizationRow, error) {
	rowsByID := map[string]authorizationRow{}
	for _, chunk := range chunkValues(uniqueNonEmpty(accountIDs), 900) {
		// chunkValues 不产出空块，空块守卫已按 w13e 收尾授权删除。
		rows, err := queryRows(ctx, s.Business, s.Business.Bind(fmt.Sprintf(`
      SELECT id, resource_id, grantee_system_account_id
      FROM %s
      WHERE resource_type = 'account'
        AND resource_id IN (%s)
		`, s.table("resource_authorizations"), s.Business.BindIn(len(chunk)))), stringSliceToAny(chunk)...)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if id := textOf(row["id"]); id != "" {
				rowsByID[id] = authorizationRow{ID: id, ResourceID: textOf(row["resource_id"]), GranteeID: textOf(row["grantee_system_account_id"])}
			}
		}
	}
	for _, chunk := range chunkValues(uniqueNonEmpty(authorizationInstanceAuthorizationIDs), 900) {
		// chunkValues 不产出空块，空块守卫已按 w13e 收尾授权删除。
		rows, err := queryRows(ctx, s.Business, s.Business.Bind(fmt.Sprintf(`
      SELECT id, resource_id, grantee_system_account_id
      FROM %s
      WHERE id IN (%s)
		`, s.table("resource_authorizations"), s.Business.BindIn(len(chunk)))), stringSliceToAny(chunk)...)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if id := textOf(row["id"]); id != "" {
				rowsByID[id] = authorizationRow{ID: id, ResourceID: textOf(row["resource_id"]), GranteeID: textOf(row["grantee_system_account_id"])}
			}
		}
	}
	output := make([]authorizationRow, 0, len(rowsByID))
	for _, value := range rowsByID {
		output = append(output, value)
	}
	return output, nil
}

func mapValues(source map[string]string) []string {
	output := make([]string, 0, len(source))
	for key := range source {
		output = append(output, key)
	}
	return output
}

func (s *DeletedAccountStore) loadActiveAuthorizationInstanceIDs(ctx context.Context, authorizationIDs []string) (map[string]bool, error) {
	output := map[string]bool{}
	for _, chunk := range chunkValues(uniqueNonEmpty(authorizationIDs), 900) {
		// chunkValues 不产出空块，空块守卫已按 w13e 收尾授权删除。
		rows, err := queryRows(ctx, s.Business, s.Business.Bind(fmt.Sprintf(`
      SELECT DISTINCT authorization_instance_authorization_id
      FROM %s
      WHERE authorization_instance_authorization_id IN (%s)
        AND deleted_at IS NULL
		`, s.table("accounts"), s.Business.BindIn(len(chunk)))), stringSliceToAny(chunk)...)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if id := strings.TrimSpace(textOf(row["authorization_instance_authorization_id"])); id != "" {
				output[id] = true
			}
		}
	}
	return output, nil
}

func (s *DeletedAccountStore) loadTeamScopeIDs(ctx context.Context, authorizationIDs []string, authorizationInstanceIDsByAuthorizationID map[string]string, authorizationResourceIDByID map[string]string, fallbackAccountID string) ([]string, error) {
	var teamScopeIDs []string
	for _, chunk := range chunkValues(uniqueNonEmpty(authorizationIDs), 900) {
		// chunkValues 不产出空块，空块守卫已按 w13e 收尾授权删除。
		rows, err := queryRows(ctx, s.Business, s.Business.Bind(fmt.Sprintf(`
      SELECT authorization_id, source_team_id
      FROM %s
      WHERE authorization_id IN (%s)
        AND source_team_id IS NOT NULL
		`, s.table("resource_authorization_sources"), s.Business.BindIn(len(chunk)))), stringSliceToAny(chunk)...)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			authorizationID := strings.TrimSpace(textOf(row["authorization_id"]))
			teamID := strings.TrimSpace(textOf(row["source_team_id"]))
			if authorizationID == "" || teamID == "" {
				continue
			}
			accountID, ok := authorizationInstanceIDsByAuthorizationID[authorizationID]
			if !ok {
				accountID = authorizationResourceIDByID[authorizationID]
			}
			if accountID == "" {
				accountID = fallbackAccountID
			}
			teamScopeIDs = append(teamScopeIDs, accountID+":"+teamID)
		}
	}
	return uniqueNonEmpty(teamScopeIDs), nil
}

func (s *DeletedAccountStore) loadSourceAccountGrantIDs(ctx context.Context, accountIDs []string) ([]string, error) {
	var grantIDs []string
	for _, chunk := range chunkValues(uniqueNonEmpty(accountIDs), 900) {
		// chunkValues 不产出空块，空块守卫已按 w13e 收尾授权删除。
		rows, err := queryRows(ctx, s.Business, s.Business.Bind(fmt.Sprintf(`
      SELECT id
      FROM %s
      WHERE resource_type = 'account'
        AND resource_id IN (%s)
		`, s.table("resource_authorization_grants"), s.Business.BindIn(len(chunk)))), stringSliceToAny(chunk)...)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			grantIDs = append(grantIDs, textOf(row["id"]))
		}
	}
	return uniqueNonEmpty(grantIDs), nil
}

func (s *DeletedAccountStore) loadAuthorizationInstanceGrantIDs(ctx context.Context, authorizationIDs []string) ([]string, error) {
	var grantIDs []string
	for _, chunk := range chunkValues(uniqueNonEmpty(authorizationIDs), 900) {
		// chunkValues 不产出空块，空块守卫已按 w13e 收尾授权删除。
		rows, err := queryRows(ctx, s.Business, s.Business.Bind(fmt.Sprintf(`
      SELECT DISTINCT grants.id
      FROM %s grants
      INNER JOIN %s authorizations
        ON authorizations.resource_type = grants.resource_type
        AND authorizations.resource_id = grants.resource_id
        AND authorizations.resource_owner_system_account_id = grants.resource_owner_system_account_id
        AND grants.grantee_type = 'system_account'
        AND grants.grantee_system_account_id = authorizations.grantee_system_account_id
      INNER JOIN %s sources
        ON sources.authorization_id = authorizations.id
        AND sources.source_type = 'manual'
      WHERE authorizations.id IN (%s)
		`, s.table("resource_authorization_grants"), s.table("resource_authorizations"),
			s.table("resource_authorization_sources"), s.Business.BindIn(len(chunk)))),
			stringSliceToAny(chunk)...)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			grantIDs = append(grantIDs, textOf(row["id"]))
		}
	}
	return uniqueNonEmpty(grantIDs), nil
}

// physicallyDelete 照 physicallyDeleteExpiredDeletedAccountBusinessRows / Async。
func (s *DeletedAccountStore) physicallyDelete(ctx context.Context, rootAccountID string, target *cleanupTarget) (struct {
	Accounts       int64
	Authorizations int64
	Grants         int64
	GroupBindings  int64
}, error) {
	result := struct {
		Accounts       int64
		Authorizations int64
		Grants         int64
		GroupBindings  int64
	}{}
	accountIDs := uniqueNonEmpty(target.AccountIDs)
	var relatedAccountIDs []string
	for _, accountID := range accountIDs {
		if accountID != rootAccountID {
			relatedAccountIDs = append(relatedAccountIDs, accountID)
		}
	}
	authorizationIDs := uniqueNonEmpty(target.AuthorizationIDs)
	grantIDs := uniqueNonEmpty(target.GrantIDs)

	tx, err := s.Business.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()

	exec := func(query string, args ...any) (int64, error) {
		return execChangedQ(ctx, tx, s.Business.Bind(query), args...)
	}
	for _, chunk := range chunkValues(accountIDs, 900) {
		args := stringSliceToAny(chunk)
		affected, err := exec(fmt.Sprintf(`DELETE FROM %s WHERE account_id IN (%s)`, s.table("group_accounts"), s.Business.BindIn(len(chunk))), args...)
		if err != nil {
			return result, err
		}
		result.GroupBindings += affected
		for _, table := range []string{"account_supported_models", "account_model_mappings", "account_tag_bindings"} {
			if _, err = exec(fmt.Sprintf(`DELETE FROM %s WHERE account_id IN (%s)`, s.table(table), s.Business.BindIn(len(chunk))), args...); err != nil {
				return result, err
			}
		}
		// 搜索索引与 API Key 运行态三表清理双方言：SQLite jobs 句柄未开
		// foreign_keys pragma，FK CASCADE 不生效，必须与 PG 同语义显式
		// DELETE（三表在双 schema 均存在）。
		for _, table := range []string{"account_name_search_terms", "account_name_search_documents", "account_api_key_runtime_states"} {
			if _, err = exec(fmt.Sprintf(`DELETE FROM %s WHERE account_id IN (%s)`, s.table(table), s.Business.BindIn(len(chunk))), args...); err != nil {
				return result, err
			}
		}
	}
	for _, chunk := range chunkValues(authorizationIDs, 900) {
		args := stringSliceToAny(chunk)
		affected, err := exec(fmt.Sprintf(`DELETE FROM %s WHERE account_authorization_id IN (%s)`, s.table("group_accounts"), s.Business.BindIn(len(chunk))), args...)
		if err != nil {
			return result, err
		}
		result.GroupBindings += affected
		if _, err = exec(fmt.Sprintf(`DELETE FROM %s WHERE authorization_id IN (%s)`, s.table("resource_authorization_sources"), s.Business.BindIn(len(chunk))), args...); err != nil {
			return result, err
		}
		if _, err = exec(fmt.Sprintf(
			`DELETE FROM %s WHERE scope_type IN ('account_authorization', 'group_authorization') AND scope_id IN (%s)`,
			s.table("request_quota_hourly_window_scope_bindings"), s.Business.BindIn(len(chunk))), args...); err != nil {
			return result, err
		}
	}
	for _, chunk := range chunkValues(grantIDs, 900) {
		args := stringSliceToAny(chunk)
		if _, err = exec(fmt.Sprintf(
			`DELETE FROM %s WHERE source_type = 'resource_authorization_grant' AND source_id IN (%s)`,
			s.table("request_quota_hourly_window_scope_bindings"), s.Business.BindIn(len(chunk))), args...); err != nil {
			return result, err
		}
		affected, err := exec(fmt.Sprintf(`DELETE FROM %s WHERE id IN (%s)`, s.table("resource_authorization_grants"), s.Business.BindIn(len(chunk))), args...)
		if err != nil {
			return result, err
		}
		result.Grants += affected
	}
	for _, chunk := range chunkValues(relatedAccountIDs, 900) {
		affected, err := exec(fmt.Sprintf(`DELETE FROM %s WHERE id IN (%s)`, s.table("accounts"), s.Business.BindIn(len(chunk))), stringSliceToAny(chunk)...)
		if err != nil {
			return result, err
		}
		result.Accounts += affected
	}
	affected, err := exec(fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, s.table("accounts")), rootAccountID)
	if err != nil {
		return result, err
	}
	result.Accounts += affected
	for _, chunk := range chunkValues(authorizationIDs, 900) {
		affected, err := exec(fmt.Sprintf(`DELETE FROM %s WHERE id IN (%s)`, s.table("resource_authorizations"), s.Business.BindIn(len(chunk))), stringSliceToAny(chunk)...)
		if err != nil {
			return result, err
		}
		result.Authorizations += affected
	}
	err = tx.Commit()
	return result, err
}

// orphanSweep 双方言统一入口（hasRelatedRecordData 同款分派模式）。
func (s *DeletedAccountStore) orphanSweep(ctx context.Context, limit int) ([]string, error) {
	if s.Business.Postgres {
		return s.orphanSweepPostgres(ctx, limit)
	}
	return s.orphanSweepSQLite(ctx, limit)
}

// scanOrphanAuthorizationInstanceRows 扫描孤儿授权实例账户行（双方言通用：
// LEFT JOIN 三段孤儿判定，仅 LIMIT ? 一个占位符）。命中条件：账户未逻辑删除
// 且持有实例授权，而授权行缺失 / 资源类型非 account / source 账户行缺失 /
// source 已逻辑删 / 资源账户行缺失 / 资源账户已逻辑删。
func (s *DeletedAccountStore) scanOrphanAuthorizationInstanceRows(ctx context.Context, limit int) ([]row, error) {
	return queryRows(ctx, s.Business, s.Business.Bind(fmt.Sprintf(`
    SELECT accounts.id, accounts.system_account_id,
      accounts.authorization_instance_authorization_id,
      accounts.authorization_instance_source_account_id,
      accounts.deleted_at,
      source_accounts.deleted_at AS source_deleted_at,
      resource_accounts.deleted_at AS resource_deleted_at
    FROM %s accounts
    LEFT JOIN %s ra ON ra.id = accounts.authorization_instance_authorization_id
    LEFT JOIN %s source_accounts ON source_accounts.id = accounts.authorization_instance_source_account_id
    LEFT JOIN %s resource_accounts ON resource_accounts.id = ra.resource_id
    WHERE accounts.deleted_at IS NULL
      AND accounts.authorization_instance_authorization_id IS NOT NULL
      AND (
        ra.id IS NULL
        OR ra.resource_type <> 'account'
        OR (accounts.authorization_instance_source_account_id IS NOT NULL AND source_accounts.id IS NULL)
        OR source_accounts.deleted_at IS NOT NULL
        OR resource_accounts.id IS NULL
        OR resource_accounts.deleted_at IS NOT NULL
      )
    ORDER BY accounts.updated_at ASC, accounts.id ASC
    LIMIT ?
	`, s.table("accounts"), s.table("resource_authorizations"), s.table("accounts"), s.table("accounts"))), limit)
}

// orphanSweepPostgres 照 logicallyDeleteOrphanedAuthorizationInstancesForDeletedSourcesAsync
// （PG bulk 语义：grants/sources/authorizations 批量 revoke + 逻辑删除实例账户 +
// tombstone outbox）。
func (s *DeletedAccountStore) orphanSweepPostgres(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.scanOrphanAuthorizationInstanceRows(ctx, limit)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []string{}, nil
	}
	actor := internalAccountReadAccessSystemAccountID
	fallbackDeletedAt := ISOOf(s.now())
	var deletedIDs []string
	for _, row := range rows {
		accountID := textOf(row["id"])
		authorizationID := textOf(row["authorization_instance_authorization_id"])
		tx, err := s.Business.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		if err := s.revokeAuthorizationInstancePostgres(ctx, tx, authorizationID, actor, fallbackDeletedAt); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		ids, err := s.logicallyDeleteAccountsTx(ctx, tx, []string{accountID}, actor, fallbackDeletedAt)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		deletedIDs = append(deletedIDs, ids...)
	}
	return uniqueNonEmpty(deletedIDs), nil
}

// revokeAuthorizationInstancePostgres 照 revokeAuthorizationInstanceForDeletedSourceAccountAsync。
func (s *DeletedAccountStore) revokeAuthorizationInstancePostgres(ctx context.Context, tx *sql.Tx, authorizationID, actor, deletedAt string) error {
	if authorizationID == "" {
		return nil
	}
	var resourceType sql.NullString
	var resourceID sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT resource_type, resource_id FROM juhe_business.resource_authorizations WHERE id = $1 LIMIT 1`, authorizationID).Scan(&resourceType, &resourceID)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && resourceType.String == "account" && resourceID.String != "" {
		if err := revokeAccountAuthorizationsPostgres(ctx, tx, resourceID.String, actor, deletedAt); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `
    UPDATE juhe_business.resource_authorization_sources
    SET status = 'revoked',
        ended_at = COALESCE(ended_at, $1),
        ended_reason = COALESCE(ended_reason, 'account_deleted'),
        revoked_by = $2,
        revoked_at = $1,
        updated_at = $1
    WHERE authorization_id = $3
      AND status IN ('active', 'superseded')
	`, deletedAt, actor, authorizationID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
    UPDATE juhe_business.resource_authorizations
    SET status = 'revoked',
        effective_source_type = NULL,
        effective_source_team_id = NULL,
        revoked_by = COALESCE(revoked_by, $2),
        revoked_at = COALESCE(revoked_at, $1),
        revoked_reason = COALESCE(revoked_reason, 'account_deleted'),
        last_source_changed_at = $1,
        updated_at = $1
    WHERE id = $3
      AND status <> 'returned'
	`, deletedAt, actor, authorizationID)
	return err
}

// revokeAccountAuthorizationsPostgres 照 revokeAccountAuthorizationsForDeletedResourceAsync。
func revokeAccountAuthorizationsPostgres(ctx context.Context, tx *sql.Tx, accountID, actor, deletedAt string) error {
	rows, err := tx.QueryContext(ctx, `
    SELECT id FROM juhe_business.resource_authorizations
    WHERE resource_type = 'account' AND resource_id = $1 AND status <> 'returned'
	`, accountID)
	if err != nil {
		return err
	}
	var authorizationIDs []string
	for rows.Next() {
		var id sql.NullString
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if id.String != "" {
			authorizationIDs = append(authorizationIDs, id.String)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	if _, err := tx.ExecContext(ctx, `
    DELETE FROM juhe_business.request_quota_hourly_window_scope_bindings
    WHERE source_type = 'resource_authorization_grant'
      AND source_id IN (
        SELECT id FROM juhe_business.resource_authorization_grants
        WHERE resource_type = 'account' AND resource_id = $1
      )
	`, accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
    UPDATE juhe_business.resource_authorization_grants
    SET status = 'revoked',
        revoked_by = COALESCE(revoked_by, $2),
        revoked_at = COALESCE(revoked_at, $1),
        updated_at = $1
    WHERE resource_type = 'account'
      AND resource_id = $3
      AND status NOT IN ('revoked', 'returned')
	`, deletedAt, actor, accountID); err != nil {
		return err
	}
	if len(authorizationIDs) == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
    UPDATE juhe_business.resource_authorization_sources
    SET status = 'revoked',
        ended_at = COALESCE(ended_at, $2),
        ended_reason = COALESCE(ended_reason, 'account_deleted'),
        revoked_by = $3,
        revoked_at = $2,
        updated_at = $2
    WHERE authorization_id = ANY($1::text[])
      AND status IN ('active', 'superseded')
	`, authorizationIDs, deletedAt, actor); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
    UPDATE juhe_business.resource_authorizations
    SET status = 'revoked',
        effective_source_type = NULL,
        effective_source_team_id = NULL,
        revoked_by = COALESCE(revoked_by, $3),
        revoked_at = COALESCE(revoked_at, $2),
        revoked_reason = COALESCE(revoked_reason, 'account_deleted'),
        last_source_changed_at = $2,
        updated_at = $2
    WHERE id = ANY($1::text[])
      AND status <> 'returned'
	`, authorizationIDs, deletedAt, actor)
	return err
}

// orphanSweepSQLite 是孤儿授权实例扫尾的 SQLite 臂，与 orphanSweepPostgres
// 同 bulk 终态语义（同扫描；每孤儿行一个事务：revoke → 逻辑删除 → tombstone
// → tags/搜索清理；LIMIT 20；单行失败整体中断返回错误）。Node SQLite 同步臂
// 的 per-grant 链与进程内缓存失效不随迁（终态等价，见 gateway internal/
// accounts/delete.go 双臂对照）。
func (s *DeletedAccountStore) orphanSweepSQLite(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.scanOrphanAuthorizationInstanceRows(ctx, limit)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []string{}, nil
	}
	actor := internalAccountReadAccessSystemAccountID
	fallbackDeletedAt := ISOOf(s.now())
	var deletedIDs []string
	for _, row := range rows {
		accountID := textOf(row["id"])
		authorizationID := textOf(row["authorization_instance_authorization_id"])
		tx, err := s.Business.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		if err := s.revokeAuthorizationInstanceSQLite(ctx, tx, authorizationID, actor, fallbackDeletedAt); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		ids, err := s.logicallyDeleteAccountsTx(ctx, tx, []string{accountID}, actor, fallbackDeletedAt)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		deletedIDs = append(deletedIDs, ids...)
	}
	return uniqueNonEmpty(deletedIDs), nil
}

// revokeAuthorizationInstanceSQLite 照 revokeAuthorizationInstanceForDeletedSourceAccountAsync
// 的 SQLite 臂（与 revokeAuthorizationInstancePostgres 同 bulk 终态语义；
// authorizationID 为空直通；授权行缺失（ErrNoRows）跳过 grants 回收，仍更新
// sources/authorizations 状态）。
func (s *DeletedAccountStore) revokeAuthorizationInstanceSQLite(ctx context.Context, tx *sql.Tx, authorizationID, actor, deletedAt string) error {
	if authorizationID == "" {
		return nil
	}
	var resourceType sql.NullString
	var resourceID sql.NullString
	err := tx.QueryRowContext(ctx, s.Business.Bind(fmt.Sprintf(
		`SELECT resource_type, resource_id FROM %s WHERE id = ? LIMIT 1`, s.table("resource_authorizations"))),
		authorizationID).Scan(&resourceType, &resourceID)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && resourceType.String == "account" && resourceID.String != "" {
		if err := s.revokeAccountAuthorizationsSQLite(ctx, tx, resourceID.String, actor, deletedAt); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, s.Business.Bind(fmt.Sprintf(`
    UPDATE %s
    SET status = 'revoked',
        ended_at = COALESCE(ended_at, ?),
        ended_reason = COALESCE(ended_reason, 'account_deleted'),
        revoked_by = ?,
        revoked_at = ?,
        updated_at = ?
    WHERE authorization_id = ?
      AND status IN ('active', 'superseded')
	`, s.table("resource_authorization_sources"))), deletedAt, actor, deletedAt, deletedAt, authorizationID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.Business.Bind(fmt.Sprintf(`
    UPDATE %s
    SET status = 'revoked',
        effective_source_type = NULL,
        effective_source_team_id = NULL,
        revoked_by = COALESCE(revoked_by, ?),
        revoked_at = COALESCE(revoked_at, ?),
        revoked_reason = COALESCE(revoked_reason, 'account_deleted'),
        last_source_changed_at = ?,
        updated_at = ?
    WHERE id = ?
      AND status <> 'returned'
	`, s.table("resource_authorizations"))), actor, deletedAt, deletedAt, deletedAt, authorizationID)
	return err
}

// revokeAccountAuthorizationsSQLite 照 revokeAccountAuthorizationsForDeletedResourceAsync
// 的 SQLite 臂（PG 的 ANY($n::text[]) 改写为 placeholderList IN 列表；
// BUG-0239：SET/WHERE 前置 `?` 与 IN 列表必须由同一次 Bind 编号）。
func (s *DeletedAccountStore) revokeAccountAuthorizationsSQLite(ctx context.Context, tx *sql.Tx, accountID, actor, deletedAt string) error {
	rows, err := queryRows(ctx, tx, s.Business.Bind(fmt.Sprintf(`
    SELECT id FROM %s
    WHERE resource_type = 'account' AND resource_id = ? AND status <> 'returned'
	`, s.table("resource_authorizations"))), accountID)
	if err != nil {
		return err
	}
	authorizationIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if id := textOf(row["id"]); id != "" {
			authorizationIDs = append(authorizationIDs, id)
		}
	}
	if _, err := tx.ExecContext(ctx, s.Business.Bind(fmt.Sprintf(`
    DELETE FROM %s
    WHERE source_type = 'resource_authorization_grant'
      AND source_id IN (
        SELECT id FROM %s
        WHERE resource_type = 'account' AND resource_id = ?
      )
	`, s.table("request_quota_hourly_window_scope_bindings"), s.table("resource_authorization_grants"))), accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.Business.Bind(fmt.Sprintf(`
    UPDATE %s
    SET status = 'revoked',
        revoked_by = COALESCE(revoked_by, ?),
        revoked_at = COALESCE(revoked_at, ?),
        updated_at = ?
    WHERE resource_type = 'account'
      AND resource_id = ?
      AND status NOT IN ('revoked', 'returned')
	`, s.table("resource_authorization_grants"))), actor, deletedAt, deletedAt, accountID); err != nil {
		return err
	}
	if len(authorizationIDs) == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, s.Business.Bind(fmt.Sprintf(`
    UPDATE %s
    SET status = 'revoked',
        ended_at = COALESCE(ended_at, ?),
        ended_reason = COALESCE(ended_reason, 'account_deleted'),
        revoked_by = ?,
        revoked_at = ?,
        updated_at = ?
    WHERE authorization_id IN (%s)
      AND status IN ('active', 'superseded')
	`, s.table("resource_authorization_sources"), placeholderList(len(authorizationIDs)))),
		append([]any{deletedAt, actor, deletedAt, deletedAt}, stringSliceToAny(authorizationIDs)...)...); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.Business.Bind(fmt.Sprintf(`
    UPDATE %s
    SET status = 'revoked',
        effective_source_type = NULL,
        effective_source_team_id = NULL,
        revoked_by = COALESCE(revoked_by, ?),
        revoked_at = COALESCE(revoked_at, ?),
        revoked_reason = COALESCE(revoked_reason, 'account_deleted'),
        last_source_changed_at = ?,
        updated_at = ?
    WHERE id IN (%s)
      AND status <> 'returned'
	`, s.table("resource_authorizations"), placeholderList(len(authorizationIDs)))),
		append([]any{actor, deletedAt, deletedAt, deletedAt}, stringSliceToAny(authorizationIDs)...)...)
	return err
}

// logicallyDeleteAccountsTx 照 logicallyDeleteAccountsAsync 的行级语义
// （tombstone outbox + 标签/搜索清理在事务内）。
func (s *DeletedAccountStore) logicallyDeleteAccountsTx(ctx context.Context, tx *sql.Tx, accountIDs []string, actor, deletedAt string) ([]string, error) {
	ids := uniqueNonEmpty(accountIDs)
	if len(ids) == 0 {
		return []string{}, nil
	}
	var deletedIDs []string
	for _, chunk := range chunkValues(ids, 900) {
		// BUG-0239：IN 列表必须用未编号的 `?` 序列（placeholderList）让外层
		// Bind 统一编号；BindIn 已产出 $n，SET 的 `?` 再经 Bind 会从 $1 重编
		// 并与 IN 列表撞号，pgx 按最大序号计参数即报
		// "mismatched param and argument count"。
		if _, err := tx.ExecContext(ctx, s.Business.Bind(fmt.Sprintf(`
      UPDATE %s
      SET status = 'disabled',
          schedulable = 0,
          cooldown_until = NULL,
          deleted_at = ?,
          deleted_by = ?,
          updated_at = ?
      WHERE deleted_at IS NULL
        AND id IN (%s)
		`, s.table("accounts"), placeholderList(len(chunk)))),
			append([]any{deletedAt, actor, deletedAt}, stringSliceToAny(chunk)...)...); err != nil {
			return nil, err
		}
		// 同上（BUG-0239）：WHERE 前置 `?` 与 IN 列表必须由同一次 Bind 编号。
		rows, err := queryRows(ctx, tx, s.Business.Bind(fmt.Sprintf(
			`SELECT id FROM %s WHERE deleted_at = ? AND id IN (%s)`, s.table("accounts"), placeholderList(len(chunk)))),
			append([]any{deletedAt}, stringSliceToAny(chunk)...)...)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if id := textOf(row["id"]); id != "" {
				deletedIDs = append(deletedIDs, id)
			}
		}
	}
	if len(deletedIDs) > 0 {
		lockSuffix := ""
		if s.Business.Postgres {
			lockSuffix = " FOR UPDATE"
		}
		// 同上（BUG-0239）：tombstone SELECT 的前置 `?` 与 IN 列表由同一次
		// Bind 编号；lockSuffix（FOR UPDATE）无占位符不受影响。
		tombstones, err := queryRows(ctx, tx, s.Business.Bind(fmt.Sprintf(`
      SELECT id, config_revision, dispatch_revision
      FROM %s
      WHERE deleted_at = ?
        AND id IN (%s)
        AND provider_code IN ('gpt', 'openai', 'xai', 'anthropic', 'deepseek', 'glm', 'gemini', 'hybrid')
        AND type IN ('api_key', 'oauth', 'google_oauth')
      ORDER BY id ASC%s
		`, s.table("accounts"), placeholderList(len(deletedIDs)), lockSuffix)),
			append([]any{deletedAt}, stringSliceToAny(deletedIDs)...)...)
		if err != nil {
			return nil, err
		}
		for _, tombstone := range tombstones {
			if err := enqueueTombstoneOutboxTx(ctx, tx, s.Business, textOf(tombstone["id"]),
				int64(numberOf(tombstone["config_revision"])), int64(numberOf(tombstone["dispatch_revision"])), deletedAt); err != nil {
				return nil, err
			}
		}
		for _, chunk := range chunkValues(deletedIDs, 900) {
			for _, table := range []string{"account_tag_bindings", "account_name_search_terms", "account_name_search_documents"} {
				if _, err := tx.ExecContext(ctx, s.Business.Bind(fmt.Sprintf(
					`DELETE FROM %s WHERE account_id IN (%s)`, s.table(table), s.Business.BindIn(len(chunk)))),
					stringSliceToAny(chunk)...); err != nil {
					return nil, err
				}
			}
		}
	}
	return deletedIDs, nil
}

// enqueueTombstoneOutboxTx 照 reserveAndEnqueueAccountHealthJobsInputInTransaction
// （kind='tombstone'，reason='account_deleted'）。
func enqueueTombstoneOutboxTx(ctx context.Context, tx *sql.Tx, db *DB, accountID string, configRevision, dispatchRevision int64, now string) error {
	normalizedAccountID := strings.TrimSpace(accountID)
	if normalizedAccountID == "" {
		return fmt.Errorf("J1 snapshot version 缺少 account ID")
	}
	var currentVersion sql.NullInt64
	err := tx.QueryRowContext(ctx, db.Bind(
		`SELECT current_version FROM account_health_jobs_input_versions WHERE account_id = ?`), normalizedAccountID).Scan(&currentVersion)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	nextVersion := int64(1)
	if err == nil && currentVersion.Valid {
		nextVersion = currentVersion.Int64 + 1
	}
	if err == nil && currentVersion.Valid {
		if _, err := tx.ExecContext(ctx, db.Bind(
			`UPDATE account_health_jobs_input_versions SET current_version = ?, reserved_at = ? WHERE account_id = ?`),
			nextVersion, now, normalizedAccountID); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, db.Bind(
			`INSERT INTO account_health_jobs_input_versions (account_id, current_version, reserved_at) VALUES (?, ?, ?)`),
			normalizedAccountID, nextVersion, now); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, db.Bind(fmt.Sprintf(`
    INSERT INTO account_health_jobs_input_outbox (
      event_id, account_id, input_version, event_kind, reason,
      config_revision, dispatch_revision, status, available_at,
      created_at, updated_at
    ) VALUES (?, ?, ?, 'tombstone', 'account_deleted', ?, ?, 'pending', ?, ?, ?)
	`)), newEventID(), normalizedAccountID, nextVersion, configRevision, dispatchRevision, now, now, now)
	return err
}

func newEventID() string {
	return fmt.Sprintf("%s-%s", randomHex32()[:8], randomHex32())
}

// hasRelatedRecordData 分派 SQLite / PG 两套相关记录检查。
func (s *DeletedAccountStore) hasRelatedRecordData(ctx context.Context, target *cleanupTarget) (bool, error) {
	if s.Business.Postgres {
		return s.hasRelatedRecordDataPostgres(ctx, target)
	}
	if s.Records == nil {
		return false, fmt.Errorf("retention record cleanup store 未初始化")
	}
	return s.Records.hasRelatedRecordDataSQLite(ctx, target)
}
