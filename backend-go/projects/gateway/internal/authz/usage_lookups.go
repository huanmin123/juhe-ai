// Name projection lookups for the authz usage reads, mirroring the
// repository-lookups.ts helpers consumed by authorization-usage.repository.ts:
// loadSystemTeamLookupMap, loadAccountLookupMap, loadGroupLookupMap,
// loadSystemAccountPrincipalMapByIds and loadSystemAccountNameMapByIds (no
// status/deleted filtering — the raw lookup maps).
package authz

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// errSettingsMissing mirrors statreads "系统设置缺少 usageStatsTimezone": the
// timezone setting is required for the usage reads; the route renders it as a
// 500 like the Node usageStatsTimezoneAsync failure.
var errSettingsMissing = errors.New("系统设置缺少 usageStatsTimezone")

// resourceInfo mirrors AuthorizationResourceInfo
// (authorization-usage.repository.ts:88-91).
type resourceInfo struct {
	name    string
	ownerID string
}

func resourceKey(resourceType, resourceID string) string {
	return resourceType + ":" + resourceID
}

type accountPrincipal struct {
	displayName string
	username    string
}

// loadTeamNameMap mirrors loadTeamRowsByIds → loadSystemTeamLookupMap.
func (s *Store) loadTeamNameMap(ctx context.Context, teamIDs []string) (map[string]string, error) {
	result := map[string]string{}
	if len(teamIDs) == 0 {
		return result, nil
	}
	unique := uniqueStrings(teamIDs)
	placeholders := strings.TrimRight(strings.Repeat("?, ", len(unique)), ", ")
	query := `SELECT id, name FROM ` + s.table("system_teams") + ` WHERE id IN (` + placeholders + `)`
	rows, err := s.db.QueryContext(ctx, s.bind(query), toStrings(unique)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		result[id] = name
	}
	return result, rows.Err()
}

// loadPrincipalMap mirrors loadSystemAccountPrincipalMapByIds (display name +
// username projections).
func (s *Store) loadPrincipalMap(ctx context.Context, accountIDs []string) (map[string]accountPrincipal, error) {
	result := map[string]accountPrincipal{}
	if len(accountIDs) == 0 {
		return result, nil
	}
	unique := uniqueStrings(accountIDs)
	placeholders := strings.TrimRight(strings.Repeat("?, ", len(unique)), ", ")
	query := `SELECT id, username, display_name FROM ` + s.table("system_accounts") + ` WHERE id IN (` + placeholders + `)`
	rows, err := s.db.QueryContext(ctx, s.bind(query), toStrings(unique)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, username, displayName string
		if err := rows.Scan(&id, &username, &displayName); err != nil {
			return nil, err
		}
		result[id] = accountPrincipal{displayName: displayName, username: username}
	}
	return result, rows.Err()
}

// loadOwnerNameMap mirrors loadAuthorizationResourceOwners:
// loadSystemAccountNameMapByIds over every resource owner id.
func (s *Store) loadOwnerNameMap(ctx context.Context, resources map[string]resourceInfo) (map[string]string, error) {
	ownerIDs := make([]string, 0, len(resources))
	for _, resource := range resources {
		if resource.ownerID != "" {
			ownerIDs = append(ownerIDs, resource.ownerID)
		}
	}
	principals, err := s.loadPrincipalMap(ctx, ownerIDs)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(principals))
	for id, principal := range principals {
		names[id] = principal.displayName
	}
	return names, nil
}

// loadResourceInfoMap mirrors loadAuthorizationResourceInfoMap (:700-711):
// account rows resolve name + owning system account, group rows resolve name
// + owner; missing rows leave the resource unprojected.
func (s *Store) loadResourceInfoMap(ctx context.Context, rows []usageWindowRow) (map[string]resourceInfo, error) {
	result := map[string]resourceInfo{}
	var accountIDs, groupIDs []string
	seenAccount := map[string]bool{}
	seenGroup := map[string]bool{}
	for _, row := range rows {
		switch row.ResourceFilterType {
		case "account":
			if row.ResourceFilterID != "" && !seenAccount[row.ResourceFilterID] {
				seenAccount[row.ResourceFilterID] = true
				accountIDs = append(accountIDs, row.ResourceFilterID)
			}
		case "group":
			if row.ResourceFilterID != "" && !seenGroup[row.ResourceFilterID] {
				seenGroup[row.ResourceFilterID] = true
				groupIDs = append(groupIDs, row.ResourceFilterID)
			}
		}
	}
	if len(accountIDs) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?, ", len(accountIDs)), ", ")
		query := `SELECT id, name, system_account_id FROM ` + s.table("accounts") + ` WHERE id IN (` + placeholders + `)`
		accountRows, err := s.db.QueryContext(ctx, s.bind(query), toStrings(accountIDs)...)
		if err != nil {
			return nil, err
		}
		for accountRows.Next() {
			var id, name, ownerID string
			if err := accountRows.Scan(&id, &name, &ownerID); err != nil {
				accountRows.Close()
				return nil, err
			}
			result[resourceKey("account", id)] = resourceInfo{name: name, ownerID: ownerID}
		}
		if err := accountRows.Err(); err != nil {
			accountRows.Close()
			return nil, err
		}
		accountRows.Close()
	}
	if len(groupIDs) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?, ", len(groupIDs)), ", ")
		query := `SELECT id, name, system_account_id FROM ` + s.table("groups") + ` WHERE id IN (` + placeholders + `)`
		groupRows, err := s.db.QueryContext(ctx, s.bind(query), toStrings(groupIDs)...)
		if err != nil {
			return nil, err
		}
		for groupRows.Next() {
			var id, name, ownerID string
			if err := groupRows.Scan(&id, &name, &ownerID); err != nil {
				groupRows.Close()
				return nil, err
			}
			result[resourceKey("group", id)] = resourceInfo{name: name, ownerID: ownerID}
		}
		if err := groupRows.Err(); err != nil {
			groupRows.Close()
			return nil, err
		}
		groupRows.Close()
	}
	return result, nil
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

// readSystemSettingsTimezone is the default TimezoneSource: the
// usageStatsTimezone system setting from the business system_settings table
// (Node usage-stats-helpers.ts usageStatsTimezoneAsync; SQLite reads the bare
// table, PostgreSQL qualifies juhe_business). The statreads package owns the
// same query for the stats surfaces.
func readSystemSettingsTimezone(ctx context.Context, db *sql.DB, postgres bool) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	table := "system_settings"
	if postgres {
		table = "juhe_business.system_settings"
	}
	var raw sql.NullString
	err := db.QueryRowContext(ctx, `SELECT value_json FROM `+table+`
		WHERE system_account_id = 'sys_admin' AND key = 'usageStatsTimezone' LIMIT 1`).Scan(&raw)
	if err == sql.ErrNoRows || (err == nil && !raw.Valid) {
		return "", errSettingsMissing
	}
	if err != nil {
		return "", err
	}
	var value string
	if err := jsonUnmarshal([]byte(raw.String), &value); err != nil {
		return "", errSettingsMissing
	}
	return strings.TrimSpace(value), nil
}
