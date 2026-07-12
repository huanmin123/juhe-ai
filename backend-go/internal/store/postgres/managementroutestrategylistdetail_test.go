package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestW5ManagementRouteStrategyListDetailSQLContract(t *testing.T) {
	source, err := os.ReadFile("queries/w5_management_route_strategy_list_detail.sql")
	if err != nil {
		t.Fatalf("read W5 management route strategy list/detail query: %v", err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")

	listSQL := querySection(
		t,
		sql,
		"-- name: ListManagementRouteStrategies :many",
		"-- name: ListManagementOwnedRouteStrategies :many",
	)
	assertSQLContainsAll(t, listSQL, []string{
		"sqlc.arg(mode)::text = ''",
		"route_strategies.mode = sqlc.arg(mode)::text",
		"sqlc.arg(status)::text = ''",
		"route_strategies.status = sqlc.arg(status)::text",
		"ORDER BY",
		"route_strategies.updated_at DESC",
		"route_strategies.created_at DESC",
		"route_strategies.id DESC",
		"LIMIT sqlc.arg(row_limit)::bigint",
		"OFFSET sqlc.arg(row_offset)::bigint",
	})
	assertSQLExcludesAll(t, listSQL, []string{
		"system_account_id)::text",
		"starts_with(",
		"binding_count",
		"api_key_count",
		"route_strategy_groups",
		"1000",
	})

	ownerListSQL := querySection(
		t,
		sql,
		"-- name: ListManagementOwnedRouteStrategies :many",
		"-- name: ListManagementRouteStrategiesByKeyword :many",
	)
	assertSQLContainsAll(t, ownerListSQL, []string{
		"route_strategies.system_account_id = sqlc.arg(system_account_id)::text",
		"sqlc.arg(mode)::text = ''",
		"route_strategies.mode = sqlc.arg(mode)::text",
		"sqlc.arg(status)::text = ''",
		"route_strategies.status = sqlc.arg(status)::text",
		"route_strategies.updated_at DESC",
		"route_strategies.created_at DESC",
		"route_strategies.id DESC",
	})
	assertSQLExcludesAll(t, ownerListSQL, []string{
		"sqlc.arg(system_account_id)::text = ''",
		"starts_with(",
	})

	keywordSQL := querySection(
		t,
		sql,
		"-- name: ListManagementRouteStrategiesByKeyword :many",
		"-- name: ListManagementOwnedRouteStrategiesByKeyword :many",
	)
	assertSQLContainsAll(t, keywordSQL, []string{
		"matched_route_strategy_scopes AS MATERIALIZED",
		`route_strategies.name COLLATE "C" >= sqlc.arg(keyword)::text`,
		`route_strategies.name COLLATE "C" < sqlc.arg(keyword_upper)::text`,
		"starts_with(route_strategies.name, sqlc.arg(keyword)::text)",
		"route_strategies.system_account_id = matched_route_strategy_scopes.system_account_id",
		"route_strategies.id = matched_route_strategy_scopes.route_strategy_id",
		"route_strategies.updated_at DESC",
		"route_strategies.created_at DESC",
		"route_strategies.id DESC",
		"LIMIT sqlc.arg(row_limit)::bigint",
		"OFFSET sqlc.arg(row_offset)::bigint",
	})
	assertSQLExcludesAll(t, keywordSQL, []string{
		"system_account_id)::text",
		"LIKE",
		"ILIKE",
		"description COLLATE",
		"1000",
	})

	ownerKeywordSQL := querySection(
		t,
		sql,
		"-- name: ListManagementOwnedRouteStrategiesByKeyword :many",
		"-- name: ListManagementRouteStrategyListEnrichment :many",
	)
	assertSQLContainsAll(t, ownerKeywordSQL, []string{
		"matched_route_strategy_scopes AS MATERIALIZED",
		"route_strategies.system_account_id = sqlc.arg(system_account_id)::text",
		`route_strategies.name COLLATE "C" >= sqlc.arg(keyword)::text`,
		`route_strategies.name COLLATE "C" < sqlc.arg(keyword_upper)::text`,
		"starts_with(route_strategies.name, sqlc.arg(keyword)::text)",
		"route_strategies.updated_at DESC",
		"route_strategies.created_at DESC",
		"route_strategies.id DESC",
	})
	assertSQLExcludesAll(t, ownerKeywordSQL, []string{
		"sqlc.arg(system_account_id)::text = ''",
		"LIKE",
		"ILIKE",
	})

	enrichmentSQL := querySection(
		t,
		sql,
		"-- name: ListManagementRouteStrategyListEnrichment :many",
		"-- name: FindManagementRouteStrategyDetail :one",
	)
	assertSQLContainsAll(t, enrichmentSQL, []string{
		"requested_scopes AS MATERIALIZED",
		"FROM unnest(sqlc.arg(route_strategy_ids)::text[])",
		"(sqlc.arg(system_account_ids)::text[])[requested.ordinality::int]",
		"route_strategy_groups.route_strategy_id = requested_scopes.route_strategy_id",
		"route_strategy_groups.system_account_id = requested_scopes.system_account_id",
		"api_keys.route_strategy_id = requested_scopes.route_strategy_id",
		"api_keys.system_account_id = requested_scopes.system_account_id",
		"ROW_NUMBER() OVER",
		"PARTITION BY",
		"ranked_bindings.row_number <= 3",
		"CASE WHEN route_strategy_groups.status = 'active' THEN 0 ELSE 1 END ASC",
		"route_strategy_groups.priority ASC",
		"route_strategy_groups.created_at ASC",
		"route_strategy_groups.id ASC",
		"groups.system_account_id = requested_scopes.system_account_id",
		"group_authorization.grantee_system_account_id = requested_scopes.system_account_id",
		"group_authorization.resource_owner_system_account_id = groups.system_account_id",
		"group_authorization.status = 'active'",
		"group_authorization.expires_at > CURRENT_TIMESTAMP",
		"WHEN groups.enabled THEN coalesce(group_authorization_settings.enabled, true)",
	})
	assertSQLExcludesAll(t, enrichmentSQL, []string{
		"usage_records",
		"LIMIT 1000",
	})

	detailSQL := querySection(
		t,
		sql,
		"-- name: FindManagementRouteStrategyDetail :one",
		"-- name: ListManagementRouteStrategyDetailBindings :many",
	)
	assertSQLContainsAll(t, detailSQL, []string{
		"route_strategies.id = sqlc.arg(route_strategy_id)::text",
		"sqlc.arg(system_account_id)::text = ''",
		"route_strategies.system_account_id = sqlc.arg(system_account_id)::text",
		"api_keys.route_strategy_id = route_strategies.id",
		"api_keys.system_account_id = route_strategies.system_account_id",
	})
	assertSQLExcludesAll(t, detailSQL, []string{
		"OFFSET",
		"usage_records",
		"route_strategy_groups",
	})

	bindingsSQL := querySection(t, sql, "-- name: ListManagementRouteStrategyDetailBindings :many", "")
	assertSQLContainsAll(t, bindingsSQL, []string{
		"route_strategies.id = sqlc.arg(route_strategy_id)::text",
		"sqlc.arg(system_account_id)::text = ''",
		"route_strategies.system_account_id = sqlc.arg(system_account_id)::text",
		"route_strategy_groups.route_strategy_id = visible_route.id",
		"route_strategy_groups.system_account_id = visible_route.system_account_id",
		"LIMIT 21",
		"CASE WHEN route_strategy_groups.status = 'active' THEN 0 ELSE 1 END ASC",
		"route_strategy_groups.priority ASC",
		"route_strategy_groups.created_at ASC",
		"route_strategy_groups.id ASC",
		"group_authorization.grantee_system_account_id = visible_route.system_account_id",
		"group_authorization.resource_owner_system_account_id = groups.system_account_id",
		"group_authorization.status = 'active'",
		"group_authorization.expires_at > CURRENT_TIMESTAMP",
		"WHEN groups.enabled THEN coalesce(group_authorization_settings.enabled, true)",
	})
	assertSQLExcludesAll(t, bindingsSQL, []string{
		"OFFSET",
		"usage_records",
		"LIMIT 20",
	})
}

func TestW5ManagementRouteStrategyListDetailIndexes(t *testing.T) {
	source, err := os.ReadFile("../../../db/migrations/000038_w5_management_route_strategy_list_detail.sql")
	if err != nil {
		t.Fatalf("read W5 management route strategy list/detail migration: %v", err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")
	assertSQLContainsAll(t, sql, []string{
		"idx_route_strategies_management_updated",
		"(updated_at DESC, created_at DESC, id DESC)",
		"idx_route_strategies_owner_management_updated",
		"(system_account_id, updated_at DESC, created_at DESC, id DESC)",
		"idx_route_strategies_name_lookup",
		`(name COLLATE "C", id)`,
	})
	assertSQLExcludesAll(t, sql, []string{
		"DROP INDEX",
		"idx_route_strategies_management_mode_updated",
		"idx_route_strategies_management_status_updated",
		"idx_route_strategies_management_mode_status_updated",
		"idx_route_strategies_owner_management_mode_updated",
		"idx_route_strategies_owner_management_status_updated",
		"CREATE INDEX idx_route_strategies_owner_mode",
		"CREATE INDEX IF NOT EXISTS idx_route_strategies_owner_name_lookup",
	})
}
