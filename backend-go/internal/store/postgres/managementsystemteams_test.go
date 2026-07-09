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
	sql := string(source)
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
