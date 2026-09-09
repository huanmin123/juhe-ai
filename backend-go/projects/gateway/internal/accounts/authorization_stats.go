// authorization_stats.go 补齐 verdict-ak/ap TRULY_MISSING 的账户摘要授权统计
// 投影：Node account-summary.repository.ts:1444 用
// loadResourceAuthorizationStatsByResourceIds('account', accountIds) 聚合
// resource_authorizations 活跃授权数，:1608-1610 投影出
// authorizationUsageAvailable / authorizationCount / authorizationTeamCount
// 三个字段（authorized 视图恒 0/false）。Go 侧聚合 loader 住在 authz 切片
// （stats_loader.go），经本文件的窄端口注入账户 Store，nil 端口保持三字段
// 零值降级（与 UsageSource 同款组装根契约）。
package accounts

import "context"

// AuthorizationStats 等价 Node ResourceAuthorizationStats（端口载荷）。
type AuthorizationStats struct {
	AuthorizationCount     int
	AuthorizationTeamCount int
}

// AuthorizationStatsSource is the cross-package read port of the authz slice
// (Node loadResourceAuthorizationStatsByResourceIdsAsync). resourceType is the
// Node ResourceAuthorizationResourceType ('account' for the management list).
// Implementations must be safe for concurrent use.
type AuthorizationStatsSource interface {
	ResourceAuthorizationStatsByResourceIds(ctx context.Context, resourceType string, resourceIDs []string) (map[string]AuthorizationStats, error)
}

// SetAuthorizationStatsSource wires the authorization-stats reader
// (composition-root handover); nil (the default) keeps the zero fields.
func (s *Store) SetAuthorizationStatsSource(source AuthorizationStatsSource) {
	s.authorizationStats = source
}

// canManageResourceOwner mirrors Node canManageResourceOwner over the Go
// AccessScope: admins manage every owner; scoped viewers manage their own rows
// (the same visibility predicate FindAdvancedDetail applies to owner rows).
func canManageResourceOwner(ownerSystemAccountID string, access AccessScope) bool {
	if access.canAccessAll() {
		return true
	}
	return ownerSystemAccountID != "" && ownerSystemAccountID == access.ViewerID
}

// hydrateAuthorizationStats fills the three authorization projection fields
// (Node account-summary.repository.ts:1608-1610): authorized-instance rows
// stay 0/false (isAuthorizedView branch), owner rows carry the loader counts
// with authorizationUsageAvailable = count > 0 && canManageResourceOwner.
// Missing ids degrade to the zero stats (Node `?? {0, 0}`); a source error
// FAILS the list, matching the hydrateListUsage contract.
func (s *Store) hydrateAuthorizationStats(ctx context.Context, access AccessScope, items []ListItem, records []listRow) error {
	if len(items) == 0 || len(items) != len(records) {
		return nil
	}
	if s.authorizationStats == nil {
		return nil
	}
	ids := make([]string, 0, len(records))
	for _, row := range records {
		ids = append(ids, row.id)
	}
	statsByAccount, err := s.authorizationStats.ResourceAuthorizationStatsByResourceIds(ctx, "account", ids)
	if err != nil {
		return err
	}
	for index := range items {
		row := records[index]
		if items[index].AccessType == "authorized" {
			continue // isAuthorizedView: the zero fields are already in place.
		}
		stats := statsByAccount[row.id]
		items[index].AuthorizationCount = stats.AuthorizationCount
		items[index].AuthorizationTeamCount = stats.AuthorizationTeamCount
		items[index].AuthorizationUsageAvailable = stats.AuthorizationCount > 0 &&
			canManageResourceOwner(row.systemAccountID, access)
	}
	return nil
}
