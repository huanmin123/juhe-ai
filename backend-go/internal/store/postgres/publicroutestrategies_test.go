package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestW1bPublicRouteStrategyBindableGroupsSQLMatchesAuthorizationSemantics(t *testing.T) {
	source, err := os.ReadFile("queries/w1b_public_route_strategies.sql")
	if err != nil {
		t.Fatalf("read W1b public route strategy query: %v", err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")
	query := querySection(
		t,
		sql,
		"-- name: FindPublicRouteStrategyBindableGroups :many",
		"-- name: InsertPublicRouteStrategy :one",
	)

	assertSQLContainsAll(t, query, []string{
		"locked_groups AS MATERIALIZED",
		"FROM juhe_business.groups AS groups",
		"WHERE groups.id = ANY(sqlc.arg(group_ids)::text[])",
		"ORDER BY groups.id ASC",
		"FOR UPDATE OF groups",
		"locked_group_authorizations AS MATERIALIZED",
		"FROM juhe_business.resource_authorizations AS group_authorization",
		"JOIN locked_groups AS groups",
		"group_authorization.resource_type = 'group'",
		"group_authorization.resource_id = groups.id",
		"group_authorization.resource_owner_system_account_id = groups.system_account_id",
		"group_authorization.grantee_system_account_id = sqlc.arg(system_account_id)",
		"ORDER BY group_authorization.resource_id ASC, group_authorization.id ASC",
		"FOR UPDATE OF group_authorization",
		"locked_group_authorization_settings AS MATERIALIZED",
		"FROM juhe_business.group_authorization_settings AS group_authorization_settings",
		"JOIN locked_group_authorizations AS group_authorization",
		"ORDER BY group_authorization_settings.group_id ASC, group_authorization_settings.authorization_id ASC",
		"FOR UPDATE OF group_authorization_settings",
		"FROM locked_groups AS groups",
		"LEFT JOIN locked_group_authorizations AS group_authorization",
		"group_authorization.status = 'active'",
		"group_authorization.expires_at > CURRENT_TIMESTAMP",
		"group_authorization_settings.authorization_id = group_authorization.id",
		"group_authorization_settings.system_account_id = sqlc.arg(system_account_id)",
		"group_authorization_settings.group_id = groups.id",
		"WHEN groups.enabled THEN coalesce(group_authorization_settings.enabled, true)",
		"OR group_authorization.id IS NOT NULL",
		"FOR UPDATE OF groups",
	})
	assertSQLExcludesAll(t, query, []string{
		"WHERE system_account_id = sqlc.arg(system_account_id)",
		"LEFT JOIN juhe_business.resource_authorizations",
		"LEFT JOIN juhe_business.group_authorization_settings",
		"FOR UPDATE OF groups, group_authorization",
		"FOR UPDATE OF groups, group_authorization_settings",
	})

	lockStages := []string{
		"locked_groups AS MATERIALIZED",
		"locked_group_authorizations AS MATERIALIZED",
		"locked_group_authorization_settings AS MATERIALIZED",
		"\nSELECT\n  groups.id,",
	}
	lastIndex := -1
	for _, stage := range lockStages {
		index := strings.Index(query, stage)
		if index <= lastIndex {
			t.Fatalf("SQL lock stage %q index = %d, want after %d", stage, index, lastIndex)
		}
		lastIndex = index
	}
	assertSQLExcludesAll(t, query[lastIndex:], []string{"FOR UPDATE"})
}

func TestW1bPublicRouteStrategyBindingSummarySQLUsesEffectiveAuthorizationState(t *testing.T) {
	source, err := os.ReadFile("queries/w1b_public_route_strategies.sql")
	if err != nil {
		t.Fatalf("read W1b public route strategy query: %v", err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")
	query := querySection(
		t,
		sql,
		"-- name: ListPublicRouteStrategyBindingsByStrategyIDs :many",
		"-- name: FindPublicRouteStrategyBindableGroups :many",
	)

	assertSQLContainsAll(t, query, []string{
		"LEFT JOIN juhe_business.groups AS groups",
		"ON groups.id = route_strategy_groups.group_id",
		"WHEN groups.system_account_id = route_strategy_groups.system_account_id THEN groups.enabled",
		"group_authorization.resource_type = 'group'",
		"group_authorization.resource_id = groups.id",
		"group_authorization.resource_owner_system_account_id = groups.system_account_id",
		"group_authorization.grantee_system_account_id = route_strategy_groups.system_account_id",
		"group_authorization.status = 'active'",
		"group_authorization.expires_at > CURRENT_TIMESTAMP",
		"group_authorization_settings.authorization_id = group_authorization.id",
		"group_authorization_settings.system_account_id = route_strategy_groups.system_account_id",
		"group_authorization_settings.group_id = groups.id",
		"WHEN groups.enabled THEN coalesce(group_authorization_settings.enabled, true)",
	})
	assertSQLExcludesAll(t, query, []string{
		"ON groups.id = route_strategy_groups.group_id\n    AND groups.system_account_id = route_strategy_groups.system_account_id",
	})
}
