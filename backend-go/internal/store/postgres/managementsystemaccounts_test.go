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

func TestManagementSystemAccountStatusUpdateSQLGuardsSessionsAndSuperAdmin(t *testing.T) {
	source, err := os.ReadFile("queries/w3_management_system_accounts.sql")
	if err != nil {
		t.Fatalf("read system account write query: %v", err)
	}
	sql := querySection(t, string(source), "-- name: UpdateManagementSystemAccountStatus :one", "")
	for _, want := range []string{
		"WITH locked_active_super_admins AS MATERIALIZED",
		"active_super_admin_guard AS MATERIALIZED",
		"count(*) FILTER (WHERE id <> sqlc.arg(system_account_id)::text)::int AS other_active_super_admin_count",
		"FROM active_super_admin_guard",
		"FOR UPDATE OF system_accounts",
		"WHERE role = 'super_admin'",
		"AND status = 'active'",
		"FOR UPDATE",
		"current_account.role = 'super_admin'",
		"current_account.other_active_super_admin_count = 0 AS blocked_last_active_super_admin",
		"blocked_last_active_super_admin",
		"status = sqlc.arg(status)::text",
		"updated_at = sqlc.arg(updated_at)::timestamptz",
		"DELETE FROM juhe_business.system_sessions",
		"AND sqlc.arg(status)::text = 'disabled'",
		"(SELECT count(*)::int FROM revoked_sessions) AS revoked_session_count",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("status update query missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"SELECT *",
		"password_hash",
		"COALESCE",
		"coalesce",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("status update query should not contain %q", forbidden)
		}
	}
}

func TestManagementSystemAccountProfileUpdateSQLGuardsSuperAdminAndSafeFields(t *testing.T) {
	source, err := os.ReadFile("queries/w3_management_system_accounts.sql")
	if err != nil {
		t.Fatalf("read system account write query: %v", err)
	}
	sql := querySection(t, string(source), "-- name: UpdateManagementSystemAccountProfile :one", "")
	for _, want := range []string{
		"WITH locked_active_super_admins AS MATERIALIZED",
		"active_super_admin_guard AS MATERIALIZED",
		"count(*) FILTER (WHERE id <> sqlc.arg(system_account_id)::text)::int AS other_active_super_admin_count",
		"FROM active_super_admin_guard",
		"FOR UPDATE OF system_accounts",
		"CASE",
		"WHEN sqlc.arg(has_display_name)::boolean THEN sqlc.arg(display_name)::text",
		"WHEN sqlc.arg(has_description)::boolean THEN sqlc.narg(description)::text",
		"WHEN sqlc.arg(has_role)::boolean THEN sqlc.arg(role)::text",
		"WHEN profile_guard.next_role IN ('super_admin', 'admin') THEN false",
		"WHEN sqlc.arg(has_must_change_password)::boolean THEN sqlc.arg(must_change_password)::boolean",
		"profile_guard.blocked_last_active_super_admin = false",
		"blocked_last_active_super_admin",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("profile update query missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"SELECT *",
		"password_hash",
		"image_generation_enabled =",
		"status = sqlc.arg",
		"COALESCE",
		"coalesce",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("profile update query should not contain %q", forbidden)
		}
	}
}

func TestManagementSystemAccountImageGenerationUpdateSQLIsNarrow(t *testing.T) {
	source, err := os.ReadFile("queries/w3_management_system_accounts.sql")
	if err != nil {
		t.Fatalf("read system account write query: %v", err)
	}
	sql := querySection(t, string(source), "-- name: UpdateManagementSystemAccountImageGeneration :one", "-- name: UpdateManagementSystemAccountProfile :one")
	for _, want := range []string{
		"WITH current_account AS",
		"FOR UPDATE",
		"image_generation_enabled = sqlc.arg(image_generation_enabled)::boolean",
		"updated_at = sqlc.arg(updated_at)::timestamptz",
		"current_account.image_generation_enabled AS before_image_generation_enabled",
		"system_accounts.image_generation_enabled",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("image generation update query missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"SELECT *",
		"password_hash",
		"status = sqlc.arg",
		"role =",
		"must_change_password =",
		"DELETE FROM juhe_business.system_sessions",
		"COALESCE",
		"coalesce",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("image generation update query should not contain %q", forbidden)
		}
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

func TestManagementSystemAccountProfileUpdateResultFromRowMapsBeforeAccountAndGuard(t *testing.T) {
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	lastLoginAt := now.Add(-time.Hour)
	result := managementSystemAccountProfileUpdateResultFromRow(postgresqueries.UpdateManagementSystemAccountProfileRow{
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
		DisplayName:                  "新用户",
		Description:                  pgtype.Text{},
		Role:                         "admin",
		Status:                       "active",
		MustChangePassword:           true,
		ImageGenerationEnabled:       true,
		LastLoginAt:                  pgtype.Timestamptz{Time: lastLoginAt, Valid: true},
		CreatedAt:                    pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt:                    pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true},
		BlockedLastActiveSuperAdmin:  true,
	})

	if result.Before.DisplayName != "旧用户" ||
		result.Before.Description != "旧描述" ||
		result.Account.DisplayName != "新用户" ||
		result.Account.Description != "" ||
		result.Account.Role != "admin" ||
		result.Account.MustChangePassword != true ||
		!result.BlockedLastActiveSuperAdmin {
		t.Fatalf("result = %+v", result)
	}
}

func TestManagementSystemAccountStatusUpdateResultFromRowMapsBeforeAccountAndGuard(t *testing.T) {
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	lastLoginAt := now.Add(-time.Hour)
	result := managementSystemAccountStatusUpdateResultFromRow(postgresqueries.UpdateManagementSystemAccountStatusRow{
		BeforeID:                     "sys_user",
		BeforeUsername:               "user",
		BeforeDisplayName:            "用户",
		BeforeDescription:            pgtype.Text{String: "描述", Valid: true},
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
		Status:                       "disabled",
		MustChangePassword:           false,
		ImageGenerationEnabled:       true,
		LastLoginAt:                  pgtype.Timestamptz{Time: lastLoginAt, Valid: true},
		CreatedAt:                    pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt:                    pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true},
		RevokedSessionCount:          2,
		BlockedLastActiveSuperAdmin:  true,
	})

	if result.Before.Status != "active" ||
		result.Account.Status != "disabled" ||
		result.RevokedSessionCount != 2 ||
		!result.BlockedLastActiveSuperAdmin {
		t.Fatalf("result = %+v", result)
	}
}

func TestManagementSystemAccountImageGenerationUpdateResultFromRowMapsBeforeAndAccount(t *testing.T) {
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	lastLoginAt := now.Add(-time.Hour)
	result := managementSystemAccountImageGenerationUpdateResultFromRow(postgresqueries.UpdateManagementSystemAccountImageGenerationRow{
		BeforeID:                     "sys_user",
		BeforeUsername:               "user",
		BeforeDisplayName:            "用户",
		BeforeDescription:            pgtype.Text{String: "描述", Valid: true},
		BeforeRole:                   "user",
		BeforeStatus:                 "active",
		BeforeMustChangePassword:     false,
		BeforeImageGenerationEnabled: false,
		BeforeLastLoginAt:            pgtype.Timestamptz{Time: lastLoginAt, Valid: true},
		BeforeCreatedAt:              pgtype.Timestamptz{Time: now, Valid: true},
		BeforeUpdatedAt:              pgtype.Timestamptz{Time: now, Valid: true},
		ID:                           "sys_user",
		Username:                     "user",
		DisplayName:                  "用户",
		Description:                  pgtype.Text{String: "描述", Valid: true},
		Role:                         "user",
		Status:                       "active",
		MustChangePassword:           false,
		ImageGenerationEnabled:       true,
		LastLoginAt:                  pgtype.Timestamptz{Time: lastLoginAt, Valid: true},
		CreatedAt:                    pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt:                    pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true},
	})

	if result.Before.ImageGenerationEnabled ||
		!result.Account.ImageGenerationEnabled ||
		result.Account.UpdatedAt != now.Add(time.Minute) {
		t.Fatalf("result = %+v", result)
	}
}

func TestManagementSystemAccountCreateSQLFansOutDefaultResources(t *testing.T) {
	source, err := os.ReadFile("queries/w3_management_system_account_create.sql")
	if err != nil {
		t.Fatalf("read system account create query: %v", err)
	}
	sections := []struct {
		name  string
		start string
		end   string
		want  []string
	}{
		{
			name:  "account",
			start: "-- name: CreateManagementSystemAccount :one",
			end:   "-- name: CreateManagementDefaultGroup :one",
			want: []string{
				"INSERT INTO juhe_business.system_accounts",
				"password_hash",
				"must_change_password",
				"image_generation_enabled",
				"RETURNING",
			},
		},
		{
			name:  "group",
			start: "-- name: CreateManagementDefaultGroup :one",
			end:   "-- name: CreateManagementDefaultRouteStrategy :one",
			want: []string{
				"INSERT INTO juhe_business.groups",
				"provider_code",
				"true,",
				"RETURNING id",
			},
		},
		{
			name:  "route strategy",
			start: "-- name: CreateManagementDefaultRouteStrategy :one",
			end:   "-- name: CreateManagementDefaultRouteStrategyGroup :exec",
			want: []string{
				"INSERT INTO juhe_business.route_strategies",
				"'normal'",
				"'active'",
				"true,",
			},
		},
		{
			name:  "route strategy group",
			start: "-- name: CreateManagementDefaultRouteStrategyGroup :exec",
			end:   "-- name: CreateManagementDefaultAPIKey :one",
			want: []string{
				"INSERT INTO juhe_business.route_strategy_groups",
				"priority, weight",
				"1,",
				"'active'",
			},
		},
		{
			name:  "api key",
			start: "-- name: CreateManagementDefaultAPIKey :one",
			end:   "-- name: CountManagementDefaultGroupsForProvider :one",
			want: []string{
				"INSERT INTO juhe_business.api_keys",
				"key_hash",
				"key_prefix",
				"key_suffix",
				"key_secret_encrypted",
				"true,",
			},
		},
	}
	for _, section := range sections {
		t.Run(section.name, func(t *testing.T) {
			sql := querySection(t, string(source), section.start, section.end)
			for _, want := range section.want {
				if !strings.Contains(sql, want) {
					t.Fatalf("%s create query missing %q", section.name, want)
				}
			}
			for _, forbidden := range []string{"SELECT *", "coalesce", "COALESCE"} {
				if strings.Contains(sql, forbidden) {
					t.Fatalf("%s create query should not contain %q", section.name, forbidden)
				}
			}
		})
	}
}

func TestManagementSystemAccountCreateDefaultGroupsMatchSeedProviders(t *testing.T) {
	if len(defaultBuiltInGroups) != 7 {
		t.Fatalf("default built-in groups = %d, want 7", len(defaultBuiltInGroups))
	}
	got := make([]string, 0, len(defaultBuiltInGroups))
	for _, group := range defaultBuiltInGroups {
		got = append(got, group.ProviderCode)
	}
	want := []string{"openai", "gpt", "deepseek", "anthropic", "gemini", "glm", "hybrid"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("default provider codes = %#v, want %#v", got, want)
	}
	for _, forbidden := range got {
		if forbidden == "openai_compatible" {
			t.Fatal("default groups must use seeded provider code openai, not openai_compatible")
		}
	}
}

func TestManagementSystemAccountCreateDefaultRouteResourcesExcludeHybrid(t *testing.T) {
	if defaultRouteGroupCount() != 6 {
		t.Fatalf("default route group count = %d, want 6", defaultRouteGroupCount())
	}
	if got := defaultRouteStrategyNameForGroup("默认 OpenAI 兼容分组"); got != "默认 OpenAI 兼容路由" {
		t.Fatalf("route strategy name = %q", got)
	}
	if got := defaultAPIKeyNameForRouteStrategy("默认 OpenAI 兼容路由"); got != "默认 OpenAI 兼容API Key" {
		t.Fatalf("api key name = %q", got)
	}
	for _, group := range defaultBuiltInGroups {
		if group.ProviderCode == hybridProviderCode && group.Name == "" {
			t.Fatal("hybrid default group should still be present")
		}
	}
}
