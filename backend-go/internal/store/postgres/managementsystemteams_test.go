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

func TestManagementSystemTeamListSQLProjectsNodeCompactRow(t *testing.T) {
	source, err := os.ReadFile("queries/w4_management_system_teams.sql")
	if err != nil {
		t.Fatalf("read system teams query: %v", err)
	}
	listBlock := sqlBlock(t, string(source), "-- name: ListManagementSystemTeams :many")
	selectEnd := strings.Index(listBlock, "FROM juhe_business.system_teams AS teams")
	if selectEnd < 0 {
		t.Fatalf("system team list SELECT boundary missing:\n%s", listBlock)
	}
	projection := listBlock[:selectEnd]
	for _, want := range []string{
		"teams.id",
		"teams.name",
		"teams.description",
		"teams.status",
		"teams.created_at",
	} {
		if !strings.Contains(projection, want) {
			t.Fatalf("system team compact list projection missing %q:\n%s", want, projection)
		}
	}
	for _, forbidden := range []string{
		"teams.created_by",
		"teams.updated_at",
	} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("system team compact list projection should not contain %q:\n%s", forbidden, projection)
		}
	}
}

func TestManagementSystemTeamWritesBuildReturnedStateBeforeCommit(t *testing.T) {
	source, err := os.ReadFile("managementsystemteams.go")
	if err != nil {
		t.Fatalf("read management system teams store: %v", err)
	}

	for _, test := range []struct {
		name   string
		marker string
	}{
		{name: "Update", marker: "func (s *Store) UpdateManagementSystemTeam("},
		{name: "AddMembers", marker: "func (s *Store) AddManagementSystemTeamMembers("},
		{name: "RemoveMember", marker: "func (s *Store) RemoveManagementSystemTeamMember("},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := goFunctionBlock(t, string(source), test.marker)
			commit := strings.Index(body, "tx.Commit(ctx)")
			if commit < 0 {
				t.Fatal("write method does not commit its transaction")
			}
			if strings.Contains(body[commit:], "FindManagementSystemTeam(") {
				t.Fatal("write method queries returned state after commit; a read failure would report an error after the write was committed")
			}
			if !strings.Contains(body[:commit], "managementSystemTeamDetailTx(") {
				t.Fatal("write method does not build returned team detail inside the transaction before commit")
			}
		})
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

func goFunctionBlock(t *testing.T, source string, marker string) string {
	t.Helper()
	start := strings.Index(source, marker)
	if start < 0 {
		t.Fatalf("missing Go function marker %q", marker)
	}
	next := strings.Index(source[start+len(marker):], "\nfunc ")
	if next < 0 {
		return source[start:]
	}
	return source[start : start+len(marker)+next]
}
