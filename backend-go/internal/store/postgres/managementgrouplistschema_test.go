package postgres

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestW5ManagementGroupListStatsMigrationMatchesNodeSchema(t *testing.T) {
	source, err := os.ReadFile("../../../db/migrations/000026_w5_management_group_list_stats.sql")
	if err != nil {
		t.Fatalf("read W5 management group list stats migration: %v", err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")
	if count := strings.Count(sql, "CREATE TABLE IF NOT EXISTS juhe_stats."); count != 4 {
		t.Fatalf("stats table count = %d, want 4", count)
	}

	assertMigrationTableColumns(t, sql, "group_account_stats", []string{
		"system_account_id text NOT NULL",
		"group_id text NOT NULL",
		"total integer NOT NULL DEFAULT 0",
		"available integer NOT NULL DEFAULT 0",
		"active integer NOT NULL DEFAULT 0",
		"disabled integer NOT NULL DEFAULT 0",
		"error integer NOT NULL DEFAULT 0",
		"rate_limited integer NOT NULL DEFAULT 0",
		"current_concurrency integer NOT NULL DEFAULT 0",
		"concurrency_limit integer NOT NULL DEFAULT 0",
		"updated_at text NOT NULL",
		"PRIMARY KEY (system_account_id, group_id)",
	})

	usageKeyColumns := []string{
		"system_account_id text NOT NULL",
		"scope_type text NOT NULL",
		"scope_id text NOT NULL DEFAULT ''",
	}
	usageMetricColumns := []string{
		"request_count bigint NOT NULL DEFAULT 0",
		"success_count bigint NOT NULL DEFAULT 0",
		"error_count bigint NOT NULL DEFAULT 0",
		"input_tokens bigint NOT NULL DEFAULT 0",
		"output_tokens bigint NOT NULL DEFAULT 0",
		"cache_read_tokens bigint NOT NULL DEFAULT 0",
		"cache_read_cost_usd double precision NOT NULL DEFAULT 0",
		"cache_write_tokens bigint NOT NULL DEFAULT 0",
		"cache_write_1h_tokens bigint NOT NULL DEFAULT 0",
		"cache_write_cost_usd double precision NOT NULL DEFAULT 0",
		"thinking_tokens bigint NOT NULL DEFAULT 0",
		"input_image_tokens bigint NOT NULL DEFAULT 0",
		"output_image_tokens bigint NOT NULL DEFAULT 0",
		"total_cost_usd double precision NOT NULL DEFAULT 0",
		"duration_ms_sum bigint NOT NULL DEFAULT 0",
		"duration_ms_count bigint NOT NULL DEFAULT 0",
		"duration_ms_max bigint NOT NULL DEFAULT 0",
		"first_token_ms_sum bigint NOT NULL DEFAULT 0",
		"first_token_ms_count bigint NOT NULL DEFAULT 0",
		"first_token_ms_max bigint NOT NULL DEFAULT 0",
		"last_used_at text",
		"last_error_at text",
		"updated_at text NOT NULL",
	}
	totalsColumns := append([]string{}, usageKeyColumns...)
	totalsColumns = append(totalsColumns, usageMetricColumns...)
	totalsColumns = append(totalsColumns, "PRIMARY KEY (system_account_id, scope_type, scope_id)")
	assertMigrationTableColumns(t, sql, "usage_stats_totals", totalsColumns)

	dailyColumns := append([]string{}, usageKeyColumns...)
	dailyColumns = append(dailyColumns, "stat_date text NOT NULL")
	dailyColumns = append(dailyColumns, usageMetricColumns...)
	dailyColumns = append(dailyColumns, "PRIMARY KEY (system_account_id, scope_type, scope_id, stat_date)")
	assertMigrationTableColumns(t, sql, "usage_stats_daily", dailyColumns)

	assertMigrationTableColumns(t, sql, "stats_job_state", []string{
		"scope_type text NOT NULL",
		"scope_id text NOT NULL DEFAULT ''",
		"job_name text NOT NULL",
		"cursor_created_at text",
		"cursor_id text",
		"last_success_at text",
		"last_error_message text",
		"lag_seconds integer",
		"updated_at text NOT NULL",
		"PRIMARY KEY (scope_type, scope_id, job_name)",
	})

	if count := strings.Count(sql, "CREATE INDEX IF NOT EXISTS"); count != 7 {
		t.Fatalf("stats index count = %d, want 7", count)
	}
	assertSQLContainsAll(t, sql, []string{
		"CREATE INDEX IF NOT EXISTS idx_group_account_stats_group",
		"ON juhe_stats.group_account_stats(group_id)",
		"CREATE INDEX IF NOT EXISTS idx_stats_job_state_usage_shard_cursor_floor",
		"ON juhe_stats.stats_job_state(scope_type, job_name, cursor_created_at, cursor_id)",
		"CREATE INDEX IF NOT EXISTS idx_stats_job_state_usage_shard_cursor_floor_any_job",
		"ON juhe_stats.stats_job_state(scope_type, cursor_created_at, cursor_id, job_name)",
		"WHERE cursor_created_at IS NOT NULL",
		"AND cursor_id IS NOT NULL",
		"CREATE INDEX IF NOT EXISTS idx_usage_stats_totals_updated",
		"ON juhe_stats.usage_stats_totals(updated_at)",
		"CREATE INDEX IF NOT EXISTS idx_usage_stats_daily_scope_date",
		"ON juhe_stats.usage_stats_daily(system_account_id, scope_type, scope_id, stat_date)",
		"CREATE INDEX IF NOT EXISTS idx_usage_stats_daily_date",
		"ON juhe_stats.usage_stats_daily(stat_date)",
		"CREATE INDEX IF NOT EXISTS idx_usage_stats_daily_updated",
		"ON juhe_stats.usage_stats_daily(updated_at)",
		"-- +goose Down",
		"-- no-op:",
	})
	for _, forbidden := range []string{
		"DROP TABLE",
		"usage_records",
		"usage_stats_minute",
		"usage_stats_hourly",
		"usage_stats_weekly",
		"usage_stats_monthly",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("W5 management group list stats migration should not contain %q", forbidden)
		}
	}
}

func TestW5ManagementGroupListSQLUsesUnionOverridesAndStablePaging(t *testing.T) {
	source, err := os.ReadFile("queries/w5_management_group_list.sql")
	if err != nil {
		t.Fatalf("read W5 management group list query: %v", err)
	}
	sql := string(source)
	listSQL := querySection(t, sql, "-- name: ListManagementGroups :many", "-- name: ListManagementGroupAccountIDs :many")
	assertSQLContainsAll(t, listSQL, []string{
		"WITH group_rows AS",
		"sqlc.arg(system_account_id)::text = ''",
		"groups.system_account_id = sqlc.arg(system_account_id)::text",
		"'owner'::text AS access_type",
		"UNION ALL",
		"sqlc.arg(system_account_id)::text <> ''",
		"resource_authorizations.grantee_system_account_id = sqlc.arg(system_account_id)::text",
		"resource_authorizations.status IN ('active', 'paused', 'expired')",
		"groups.system_account_id <> sqlc.arg(system_account_id)::text",
		"LEFT JOIN juhe_business.group_authorization_settings",
		"WHEN groups.enabled THEN coalesce(group_authorization_settings.enabled, true)",
		"coalesce(group_authorization_settings.group_type, groups.group_type)",
		"coalesce(group_authorization_settings.scheduling_policy_json, groups.scheduling_policy_json)",
		"false AS is_default",
		"coalesce(group_authorization_settings.updated_at, groups.updated_at) AS effective_updated_at",
		"ORDER BY group_rows.effective_updated_at DESC, group_rows.id DESC",
		"LIMIT sqlc.arg(row_limit)::int",
		"OFFSET sqlc.arg(row_offset)::int",
	})
	assertSQLExcludesAll(t, listSQL, []string{
		"juhe_business.group_accounts",
		"usage_records",
		"COUNT(",
		"SUM(",
		"GROUP BY",
	})
}

func TestW5ManagementGroupListSQLReadsCurrentPageSummariesInBatches(t *testing.T) {
	source, err := os.ReadFile("queries/w5_management_group_list.sql")
	if err != nil {
		t.Fatalf("read W5 management group list query: %v", err)
	}
	sql := string(source)
	accountIDsSQL := querySection(t, sql, "-- name: ListManagementGroupAccountIDs :many", "-- name: ListManagementGroupAccountStats :many")
	assertSQLContainsAll(t, accountIDsSQL, []string{
		"FROM juhe_business.group_accounts AS group_accounts",
		"groups.system_account_id = group_accounts.system_account_id",
		"accounts.system_account_id = group_accounts.system_account_id",
		"group_accounts.group_id = ANY(sqlc.arg(group_ids)::text[])",
		"group_accounts.enabled = true",
		"accounts.deleted_at IS NULL",
		"group_accounts.account_authorization_id IS NULL",
		"accounts.authorization_instance_authorization_id IS NULL",
		"account_authorizations.id = group_accounts.account_authorization_id",
		"account_authorizations.id = accounts.authorization_instance_authorization_id",
		"account_authorizations.resource_type = 'account'",
		"account_authorizations.status IN ('active', 'paused', 'expired')",
		"ORDER BY",
		"group_accounts.group_id ASC",
		"group_accounts.created_at ASC",
		"group_accounts.account_id ASC",
	})
	assertSQLExcludesAll(t, accountIDsSQL, []string{
		"usage_records",
		"COUNT(",
		"SUM(",
		"GROUP BY",
		"LIMIT",
		"OFFSET",
	})

	statsSQL := querySection(t, sql, "-- name: ListManagementGroupAccountStats :many", "-- name: ListManagementGroupUsageTotals :many")
	assertSQLContainsAll(t, statsSQL, []string{
		"FROM juhe_stats.group_account_stats",
		"WHERE group_id = ANY(sqlc.arg(group_ids)::text[])",
	})
	assertSQLExcludesAll(t, statsSQL, []string{"group_accounts", "usage_records", "COUNT(", "SUM(", "GROUP BY"})

	totalsSQL := querySection(t, sql, "-- name: ListManagementGroupUsageTotals :many", "-- name: ListManagementGroupUsageDaily :many")
	dailySQL := querySection(t, sql, "-- name: ListManagementGroupUsageDaily :many", "-- name: ListManagementGroupAuthorizationSources :many")
	for name, usageSQL := range map[string]string{"totals": totalsSQL, "daily": dailySQL} {
		assertSQLContainsAll(t, usageSQL, []string{
			"FROM unnest(sqlc.arg(lookup_keys)::text[])",
			"WITH ORDINALITY",
			"(sqlc.arg(system_account_ids)::text[])[requested.ordinality::int]",
			"(sqlc.arg(scope_types)::text[])[requested.ordinality::int]",
			"(sqlc.arg(scope_ids)::text[])[requested.ordinality::int]",
			"usage_stats.system_account_id = requested_scopes.system_account_id",
			"usage_stats.scope_type = requested_scopes.scope_type",
			"usage_stats.scope_id = requested_scopes.scope_id",
			"ORDER BY requested_scopes.ordinality ASC",
		})
		assertSQLExcludesAll(t, usageSQL, []string{"usage_records", "COUNT(", "SUM(", "GROUP BY"})
		if name == "totals" && !strings.Contains(usageSQL, "juhe_stats.usage_stats_totals") {
			t.Fatal("totals query must read juhe_stats.usage_stats_totals")
		}
		if name == "daily" {
			assertSQLContainsAll(t, usageSQL, []string{
				"juhe_stats.usage_stats_daily",
				"usage_stats.stat_date = sqlc.arg(stat_date)::text",
			})
		}
	}

	sourcesSQL := querySection(t, sql, "-- name: ListManagementGroupAuthorizationSources :many", "")
	assertSQLContainsAll(t, sourcesSQL, []string{
		"FROM juhe_business.resource_authorization_sources AS authorization_sources",
		"LEFT JOIN juhe_business.system_teams AS system_teams",
		"authorization_sources.authorization_id = ANY(sqlc.arg(authorization_ids)::text[])",
		"coalesce(system_teams.name, '')::text AS source_team_name",
	})
	assertSQLExcludesAll(t, sourcesSQL, []string{"usage_records", "COUNT(", "SUM(", "GROUP BY"})
}

func migrationTableSQL(t *testing.T, sql string, tableName string) string {
	t.Helper()
	marker := "CREATE TABLE IF NOT EXISTS juhe_stats." + tableName + " ("
	start := strings.Index(sql, marker)
	if start < 0 {
		t.Fatalf("migration table %q not found", tableName)
	}
	tableSQL := sql[start:]
	end := strings.Index(tableSQL, "\n);")
	if end < 0 {
		t.Fatalf("migration table %q end not found", tableName)
	}
	return tableSQL[:end+3]
}

func assertMigrationTableColumns(t *testing.T, sql string, tableName string, want []string) {
	t.Helper()
	tableSQL := migrationTableSQL(t, sql, tableName)
	start := strings.Index(tableSQL, "(\n")
	end := strings.LastIndex(tableSQL, "\n);")
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("parse migration table %q columns", tableName)
	}
	lines := strings.Split(tableSQL[start+2:end], "\n")
	got := make([]string, 0, len(lines))
	for _, line := range lines {
		column := strings.TrimSuffix(strings.TrimSpace(line), ",")
		if column != "" {
			got = append(got, column)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("migration table %q columns:\n got: %#v\nwant: %#v", tableName, got, want)
	}
}

func assertSQLContainsAll(t *testing.T, sql string, values []string) {
	t.Helper()
	for _, value := range values {
		if !strings.Contains(sql, value) {
			t.Fatalf("SQL missing %q", value)
		}
	}
}

func assertSQLExcludesAll(t *testing.T, sql string, values []string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(sql, value) {
			t.Fatalf("SQL should not contain %q", value)
		}
	}
}
