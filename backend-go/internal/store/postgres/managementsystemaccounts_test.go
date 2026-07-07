package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestManagementSystemAccountOptionLimit(t *testing.T) {
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
		if got := managementSystemAccountOptionLimit(tt.input); got != tt.want {
			t.Fatalf("managementSystemAccountOptionLimit(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestManagementSystemAccountOptionsSQLIsLightweight(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_system_account_options.sql")
	if err != nil {
		t.Fatalf("read system account options query: %v", err)
	}
	sql := string(source)
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
			t.Fatalf("system account options query missing %q", want)
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
			t.Fatalf("system account options query should not contain %q", forbidden)
		}
	}
}
