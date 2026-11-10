package statsagg

import (
	"context"
	"database/sql"
	"sort"
)

// markDerivedWindowDirtyScopes mirrors markPostgresDerivedWindowDirtyScopes：
// 聚合写入后标记 overview / ai-performance / quota hourly 三类派生窗口脏范围。
func (a *Aggregator) markDerivedWindowDirtyScopes(ctx context.Context, tx *sql.Tx, dailyEntries []*aggregatedTimeEntry, hourlyQuotaEntries map[statsTotalsKey]*aggregatedTimeEntry, updatedAt string) error {
	type overviewScope struct {
		systemAccountID string
		scopeID         string
		minDate         string
	}
	overviewScopes := map[string]*overviewScope{}
	type aiScope struct {
		systemAccountID string
		minDate         string
		maxDate         string
	}
	aiPerformanceScopes := map[string]*aiScope{}
	for _, entry := range dailyEntries {
		if entry.ScopeType == "system_account" {
			existing, ok := overviewScopes[entry.SystemAccountID]
			if !ok {
				overviewScopes[entry.SystemAccountID] = &overviewScope{
					systemAccountID: entry.SystemAccountID,
					scopeID:         entry.ScopeID,
					minDate:         entry.TimeValue,
				}
			} else {
				existing.scopeID = entry.ScopeID
				if entry.TimeValue < existing.minDate {
					existing.minDate = entry.TimeValue
				}
			}
		}
		if entry.ScopeType == "account" {
			existing, ok := aiPerformanceScopes[entry.SystemAccountID]
			if !ok {
				aiPerformanceScopes[entry.SystemAccountID] = &aiScope{
					systemAccountID: entry.SystemAccountID,
					minDate:         entry.TimeValue,
					maxDate:         entry.TimeValue,
				}
			} else {
				if entry.TimeValue < existing.minDate {
					existing.minDate = entry.TimeValue
				}
				if entry.TimeValue > existing.maxDate {
					existing.maxDate = entry.TimeValue
				}
			}
		}
	}

	overviewKeys := make([]string, 0, len(overviewScopes))
	for key := range overviewScopes {
		overviewKeys = append(overviewKeys, key)
	}
	sort.Strings(overviewKeys)
	for _, key := range overviewKeys {
		scope := overviewScopes[key]
		query := a.Dialect.bind(`
			INSERT INTO ` + a.Dialect.StatsTable("usage_overview_dirty_scopes") + ` (
			  system_account_id, scope_id, min_changed_date, generation, first_dirty_at, updated_at)
			VALUES (?, ?, ?, 1, ?, ?)
			ON CONFLICT(system_account_id) DO UPDATE SET
			  scope_id = excluded.scope_id,
			  min_changed_date = ` + a.Dialect.leastExpr(a.Dialect.qualifiedTarget("usage_overview_dirty_scopes")+".min_changed_date", "excluded.min_changed_date") + `,
			  generation = ` + a.Dialect.qualifiedTarget("usage_overview_dirty_scopes") + `.generation + 1,
			  updated_at = excluded.updated_at
		`)
		if _, err := tx.ExecContext(ctx, query, scope.systemAccountID, scope.scopeID, scope.minDate, updatedAt, updatedAt); err != nil {
			return err
		}
	}

	aiKeys := make([]string, 0, len(aiPerformanceScopes))
	for key := range aiPerformanceScopes {
		aiKeys = append(aiKeys, key)
	}
	sort.Strings(aiKeys)
	for _, key := range aiKeys {
		scope := aiPerformanceScopes[key]
		query := a.Dialect.bind(`
			INSERT INTO ` + a.Dialect.StatsTable("ai_performance_summary_dirty_system_accounts") + ` (
			  system_account_id, min_stat_date, max_stat_date, generation, first_dirty_at, updated_at)
			VALUES (?, ?, ?, 1, ?, ?)
			ON CONFLICT(system_account_id) DO UPDATE SET
			  min_stat_date = ` + a.Dialect.leastExpr(a.Dialect.qualifiedTarget("ai_performance_summary_dirty_system_accounts")+".min_stat_date", "excluded.min_stat_date") + `,
			  max_stat_date = ` + a.Dialect.greatestExpr(a.Dialect.qualifiedTarget("ai_performance_summary_dirty_system_accounts")+".max_stat_date", "excluded.max_stat_date") + `,
			  generation = ` + a.Dialect.qualifiedTarget("ai_performance_summary_dirty_system_accounts") + `.generation + 1,
			  updated_at = excluded.updated_at
		`)
		if _, err := tx.ExecContext(ctx, query, scope.systemAccountID, scope.minDate, scope.maxDate, updatedAt, updatedAt); err != nil {
			return err
		}
	}

	quotaKeys := make([]statsTotalsKey, 0, len(hourlyQuotaEntries))
	for key := range hourlyQuotaEntries {
		quotaKeys = append(quotaKeys, key)
	}
	sort.Slice(quotaKeys, func(i, j int) bool {
		if quotaKeys[i].SystemAccountID != quotaKeys[j].SystemAccountID {
			return quotaKeys[i].SystemAccountID < quotaKeys[j].SystemAccountID
		}
		if quotaKeys[i].ScopeType != quotaKeys[j].ScopeType {
			return quotaKeys[i].ScopeType < quotaKeys[j].ScopeType
		}
		return quotaKeys[i].ScopeID < quotaKeys[j].ScopeID
	})
	for _, key := range quotaKeys {
		query := a.Dialect.bind(`
			INSERT INTO ` + a.Dialect.StatsTable("usage_quota_hourly_window_dirty_scopes") + ` (
			  system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at)
			VALUES (?, ?, ?, 1, ?, ?)
			ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
			  generation = ` + a.Dialect.qualifiedTarget("usage_quota_hourly_window_dirty_scopes") + `.generation + 1,
			  updated_at = excluded.updated_at
		`)
		if _, err := tx.ExecContext(ctx, query, key.SystemAccountID, key.ScopeType, key.ScopeID, updatedAt, updatedAt); err != nil {
			return err
		}
	}
	return nil
}

// authorizationRowQuerier 统一 *sql.Tx / *sql.DB 的查询面。
type authorizationRowQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// authorizationQueryHandle 返回授权链查找的执行句柄。当前 PostgreSQL 是
// 唯一方言：聚合事务与 juhe_business 同池，直接用事务句柄（与聚合读同
// 快照）；保留接口是为后续替换业务库句柄时不动查询逻辑。
func (a *Aggregator) authorizationQueryHandle(tx *sql.Tx) authorizationRowQuerier {
	return tx
}

// createUsageStatsAuthorizationLookup mirrors createPostgresUsageStatsAuthorizationLookup：
// account 授权 → resource_id / instance_account_id 查找表。
func (a *Aggregator) createUsageStatsAuthorizationLookup(ctx context.Context, tx *sql.Tx, records []UsageStatsRecordRow) (*AuthorizationLookup, error) {
	lookup := &AuthorizationLookup{
		AccountAuthorizationInstanceAccountIDs: map[string]string{},
	}
	ids := uniqueNonEmptyIDs(records, func(row UsageStatsRecordRow) string {
		if row.AccountAuthorizationID != nil {
			return *row.AccountAuthorizationID
		}
		return ""
	})
	if len(ids) == 0 {
		return lookup, nil
	}
	query := a.Dialect.bind(`
		SELECT authorizations.id, authorizations.resource_id, instance_accounts.id AS instance_account_id
		FROM ` + a.Dialect.BusinessTable("resource_authorizations") + ` authorizations
		LEFT JOIN ` + a.Dialect.BusinessTable("accounts") + ` instance_accounts
		  ON instance_accounts.authorization_instance_authorization_id = authorizations.id
		 AND instance_accounts.system_account_id = authorizations.grantee_system_account_id
		WHERE authorizations.resource_type = 'account'
		  AND authorizations.id = ANY(?)
		ORDER BY authorizations.id, instance_accounts.id
	`)
	rows, err := a.authorizationQueryHandle(tx).QueryContext(ctx, query, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var authID, resourceID, instanceAccountID sql.NullString
		if err := rows.Scan(&authID, &resourceID, &instanceAccountID); err != nil {
			return nil, err
		}
		if authID.Valid && resourceID.Valid && resourceID.String != "" {
			// accountAuthorizationResourceIds 未被 usageStatsEntries 使用
			//（仅授权日报 writer 使用 resource_id 覆盖），这里保留 resource
			// 查找供授权报表使用。
			lookup.setAccountAuthorizationResourceID(authID.String, resourceID.String)
		}
		if authID.Valid && instanceAccountID.Valid && instanceAccountID.String != "" {
			// 同一授权理论上只有一个实例账户，但历史数据可能重复；按
			// instance_accounts.id 升序取第一个，避免依赖数据库未定义行序。
			if _, exists := lookup.AccountAuthorizationInstanceAccountIDs[authID.String]; !exists {
				lookup.AccountAuthorizationInstanceAccountIDs[authID.String] = instanceAccountID.String
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return lookup, nil
}

func uniqueNonEmptyIDs(records []UsageStatsRecordRow, pick func(UsageStatsRecordRow) string) []string {
	seen := map[string]struct{}{}
	var ids []string
	for _, row := range records {
		value := pick(row)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		ids = append(ids, value)
	}
	sort.Strings(ids)
	return ids
}
