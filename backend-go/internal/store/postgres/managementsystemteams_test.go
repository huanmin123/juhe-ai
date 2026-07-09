package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestManagementSystemTeamCreateSQLIsNarrow(t *testing.T) {
	source, err := os.ReadFile("queries/w4_management_system_teams.sql")
	if err != nil {
		t.Fatalf("read system teams query: %v", err)
	}
	sql := sqlBlock(t, string(source), "-- name: CreateManagementSystemTeam :one")
	for _, want := range []string{
		"-- name: CreateManagementSystemTeam :one",
		"INSERT INTO juhe_business.system_teams",
		"id, name, description, status, created_by, created_at, updated_at",
		"RETURNING",
		"id,",
		"name,",
		"description,",
		"status,",
		"created_by,",
		"created_at,",
		"updated_at;",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("system team create query missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"SELECT *",
		"system_team_members",
		"resource_authorizations",
		"authorization_sources",
		"password_hash",
		"token_hash",
		"JOIN",
		"GROUP BY",
		"COUNT(",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("system team create query should not contain %q", forbidden)
		}
	}
}

func TestManagementSystemTeamReadSQLScopeIsNarrow(t *testing.T) {
	source, err := os.ReadFile("queries/w4_management_system_teams.sql")
	if err != nil {
		t.Fatalf("read system teams query: %v", err)
	}
	sql := string(source)
	listBlock := sqlBlock(t, sql, "-- name: ListManagementSystemTeams :many")
	for _, want := range []string{
		"sqlc.arg(system_account_id)::text = ''",
		"EXISTS",
		"system_team_members AS scoped_members",
		"starts_with(teams.name, sqlc.arg(keyword)::text)",
		"ORDER BY teams.status ASC, teams.updated_at DESC, teams.name ASC, teams.id ASC",
	} {
		if !strings.Contains(listBlock, want) {
			t.Fatalf("system team list query missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"teams.description COLLATE",
		"resource_authorizations",
		"resource_authorization_sources",
		"resource_authorization_grants",
		"LEFT JOIN (",
		"GROUP BY team_id",
		"password_hash",
		"token_hash",
		"SELECT *",
	} {
		if strings.Contains(listBlock, forbidden) {
			t.Fatalf("system team list query should not contain %q", forbidden)
		}
	}
	countBlock := sqlBlock(t, sql, "-- name: ListManagementSystemTeamMemberCounts :many")
	for _, want := range []string{
		"team_id = ANY(sqlc.arg(team_ids)::text[])",
		"COUNT(*) FILTER (WHERE status = 'active')::bigint AS active_member_count",
		"GROUP BY team_id",
	} {
		if !strings.Contains(countBlock, want) {
			t.Fatalf("system team count query missing %q", want)
		}
	}
	detailBlock := sqlBlock(t, sql, "-- name: ListManagementSystemTeamMembers :many")
	for _, want := range []string{
		"members.status = 'active'",
		"ROW_NUMBER() OVER",
		"team_member_rank <= 500",
	} {
		if !strings.Contains(detailBlock, want) {
			t.Fatalf("system team detail member query missing %q", want)
		}
	}
}

func sqlBlock(t *testing.T, sql string, marker string) string {
	t.Helper()
	start := strings.Index(sql, marker)
	if start < 0 {
		t.Fatalf("missing SQL marker %q", marker)
	}
	next := strings.Index(sql[start+len(marker):], "-- name: ")
	if next < 0 {
		return sql[start:]
	}
	return sql[start : start+len(marker)+next]
}
