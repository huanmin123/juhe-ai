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
		"groups.id = ANY(sqlc.arg(group_ids)::text[])",
		"groups.system_account_id = sqlc.arg(system_account_id)",
		"group_authorization.resource_type = 'group'",
		"group_authorization.resource_id = groups.id",
		"group_authorization.resource_owner_system_account_id = groups.system_account_id",
		"group_authorization.grantee_system_account_id = sqlc.arg(system_account_id)",
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
		"FOR UPDATE;",
	})
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
