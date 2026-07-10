package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestManagementAuthorizationPrincipalOptionLimit(t *testing.T) {
	tests := []struct {
		input int
		want  int
	}{
		{input: 0, want: 50},
		{input: -1, want: 50},
		{input: 1, want: 1},
		{input: 50, want: 50},
		{input: 51, want: 50},
	}
	for _, tt := range tests {
		if got := managementAuthorizationPrincipalOptionLimit(tt.input); got != tt.want {
			t.Fatalf("managementAuthorizationPrincipalOptionLimit(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestManagementAuthorizationGranteeAccountsSQLIsLightweight(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_authorization_options.sql")
	if err != nil {
		t.Fatalf("read authorization options query: %v", err)
	}
	sql := querySection(t, string(source), "-- name: ListManagementAuthorizationGranteeAccounts :many", "-- name: ListManagementAuthorizationGranteeTeams :many")
	for _, want := range []string{
		"SELECT id, username, display_name, status",
		"FROM juhe_business.system_accounts",
		"username COLLATE \"C\"",
		"display_name COLLATE \"C\"",
		"starts_with(username, sqlc.arg(keyword)::text)",
		"starts_with(display_name, sqlc.arg(keyword)::text)",
		"ORDER BY status ASC, display_name ASC, username ASC, id ASC",
		"LIMIT sqlc.arg(row_limit)::int",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("authorization grantee accounts query missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"SELECT *",
		"password_hash",
		" role",
		"description",
		"must_change_password",
		"last_login_at",
		"created_at",
		"updated_at",
		"COALESCE",
		"coalesce",
		"LIKE",
		"ILIKE",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("authorization grantee accounts query should not contain %q", forbidden)
		}
	}
}

func TestManagementAuthorizationGranteeTeamsSQLIsLightweight(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_authorization_options.sql")
	if err != nil {
		t.Fatalf("read authorization options query: %v", err)
	}
	sql := querySection(t, string(source), "-- name: ListManagementAuthorizationGranteeTeams :many", "-- name: ListManagementAuthorizationGranteeGroups :many")
	for _, want := range []string{
		"SELECT id, name, status",
		"FROM juhe_business.system_teams",
		"name COLLATE \"C\"",
		"starts_with(name, sqlc.arg(keyword)::text)",
		"ORDER BY status ASC, name ASC, id ASC",
		"LIMIT sqlc.arg(row_limit)::int",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("authorization grantee teams query missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"SELECT *",
		"system_team_members",
		"member_count",
		"description",
		"created_by",
		"created_at",
		"updated_at",
		"resource_authorizations",
		"authorization_sources",
		"LIKE",
		"ILIKE",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("authorization grantee teams query should not contain %q", forbidden)
		}
	}
}

func TestManagementAuthorizationGranteeGroupsSQLIsLightweight(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_authorization_options.sql")
	if err != nil {
		t.Fatalf("read authorization options query: %v", err)
	}
	sql := querySection(t, string(source), "-- name: ListManagementAuthorizationGranteeGroups :many", "")
	for _, want := range []string{
		"WITH active_grantee AS",
		"FROM juhe_business.system_accounts",
		"AND status = 'active'",
		"FROM juhe_business.groups AS groups",
		"INNER JOIN active_grantee",
		"groups.enabled = true",
		"groups.id = ANY(sqlc.arg(ids)::text[])",
		"groups.provider_code = sqlc.arg(provider_code)::text",
		"groups.name COLLATE \"C\"",
		"starts_with(groups.name, sqlc.arg(keyword)::text)",
		"CASE WHEN sqlc.arg(prefer_default)::boolean THEN groups.is_default ELSE false END DESC",
		"groups.updated_at DESC",
		"LIMIT sqlc.arg(row_limit)::int",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("authorization grantee groups query missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"SELECT *",
		"group_accounts",
		"account_stats",
		"usage",
		"resource_authorizations",
		"authorization_sources",
		"group_authorization_settings",
		"description",
		"password_hash",
		"must_change_password",
		"last_login_at",
		"LIKE",
		"ILIKE",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("authorization grantee groups query should not contain %q", forbidden)
		}
	}
}

func querySection(t *testing.T, source string, startMarker string, endMarker string) string {
	t.Helper()
	start := strings.Index(source, startMarker)
	if start < 0 {
		t.Fatalf("query marker %q not found", startMarker)
	}
	source = source[start:]
	if endMarker == "" {
		return source
	}
	end := strings.Index(source[len(startMarker):], endMarker)
	if end < 0 {
		t.Fatalf("query end marker %q not found", endMarker)
	}
	return source[:len(startMarker)+end]
}
