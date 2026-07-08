package postgres

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
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
	sql := querySection(t, string(source), "-- name: ListManagementSystemAccountOptions :many", "")
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

func TestManagementSystemAccountListSQLUsesSafeFieldsAndStablePagination(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_system_account_options.sql")
	if err != nil {
		t.Fatalf("read system account options query: %v", err)
	}
	sql := querySection(t, string(source), "-- name: ListManagementSystemAccounts :many", "-- name: ListManagementSystemAccountOptions :many")
	for _, want := range []string{
		"id",
		"username",
		"display_name",
		"description",
		"role",
		"status",
		"must_change_password",
		"image_generation_enabled",
		"last_login_at",
		"created_at",
		"updated_at",
		"FROM juhe_business.system_accounts",
		"lower(username)",
		"lower(display_name)",
		"starts_with(lower(username), sqlc.arg(keyword)::text)",
		"starts_with(lower(display_name), sqlc.arg(keyword)::text)",
		"ORDER BY updated_at DESC, id DESC",
		"LIMIT sqlc.arg(row_limit)::int",
		"OFFSET sqlc.arg(row_offset)::int",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("system account list query missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"SELECT *",
		"password_hash",
		"LIKE",
		"ILIKE",
		"COUNT(",
		"count(",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("system account list query should not contain %q", forbidden)
		}
	}
}

func TestManagementSystemAccountPasswordResetSQLIsSingleStatementAndDoesNotReturnPasswordHash(t *testing.T) {
	source, err := os.ReadFile("queries/w3_management_system_accounts.sql")
	if err != nil {
		t.Fatalf("read system account write query: %v", err)
	}
	sql := querySection(t, string(source), "-- name: ResetManagementSystemAccountPassword :one", "")
	for _, want := range []string{
		"WITH current_account AS",
		"FOR UPDATE",
		"password_hash = sqlc.arg(password_hash)::text",
		"current_account.role IN ('super_admin', 'admin')",
		"DELETE FROM juhe_business.system_sessions",
		"WHERE system_account_id IN (SELECT id FROM updated_account)",
		"(SELECT count(*)::int FROM revoked_sessions) AS revoked_session_count",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("password reset query missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"SELECT *",
		"COALESCE",
		"coalesce",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("password reset query should not contain %q", forbidden)
		}
	}
	returningIndex := strings.Index(sql, "RETURNING")
	if returningIndex < 0 {
		t.Fatal("password reset query missing RETURNING block")
	}
	if strings.Contains(sql[returningIndex:], "password_hash") {
		t.Fatal("password reset query must not return password_hash")
	}
}

func TestManagementSystemAccountPasswordResetResultFromRowMapsBeforeAndAccount(t *testing.T) {
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	lastLoginAt := now.Add(-time.Hour)
	result := managementSystemAccountPasswordResetResultFromRow(postgresqueries.ResetManagementSystemAccountPasswordRow{
		BeforeID:                     "sys_user",
		BeforeUsername:               "user",
		BeforeDisplayName:            "旧用户",
		BeforeDescription:            pgtype.Text{String: "旧描述", Valid: true},
		BeforeRole:                   "user",
		BeforeStatus:                 "active",
		BeforeMustChangePassword:     false,
		BeforeImageGenerationEnabled: true,
		BeforeLastLoginAt:            pgtype.Timestamptz{Time: lastLoginAt, Valid: true},
		BeforeCreatedAt:              pgtype.Timestamptz{Time: now, Valid: true},
		BeforeUpdatedAt:              pgtype.Timestamptz{Time: now, Valid: true},
		ID:                           "sys_user",
		Username:                     "user",
		DisplayName:                  "用户",
		Description:                  pgtype.Text{String: "描述", Valid: true},
		Role:                         "user",
		Status:                       "active",
		MustChangePassword:           true,
		ImageGenerationEnabled:       true,
		LastLoginAt:                  pgtype.Timestamptz{Time: lastLoginAt, Valid: true},
		CreatedAt:                    pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt:                    pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true},
		RevokedSessionCount:          3,
	})

	if result.Before.DisplayName != "旧用户" ||
		result.Before.Description != "旧描述" ||
		result.Account.DisplayName != "用户" ||
		result.Account.MustChangePassword != true ||
		result.RevokedSessionCount != 3 {
		t.Fatalf("result = %+v", result)
	}
}
