package postgres

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestManagementSessionFromRow(t *testing.T) {
	expiresAt := time.Date(2026, 7, 7, 10, 0, 0, 0, time.FixedZone("test", 8*3600))
	lastSeenAt := expiresAt.Add(-time.Minute)

	session, err := managementSessionFromRow(postgresqueries.FindManagementSessionByTokenHashRow{
		ID:                 "sess_1",
		TokenHash:          "hash",
		ExpiresAt:          pgtype.Timestamptz{Time: expiresAt, Valid: true},
		LastSeenAt:         pgtype.Timestamptz{Time: lastSeenAt, Valid: true},
		AccountID:          "sys_admin",
		Username:           "admin",
		DisplayName:        "管理员",
		Role:               "admin",
		Status:             "active",
		MustChangePassword: false,
	})
	if err != nil {
		t.Fatalf("managementSessionFromRow() error = %v", err)
	}
	if session.SessionID != "sess_1" || session.AccountID != "sys_admin" || session.Role != "admin" {
		t.Fatalf("session = %+v", session)
	}
	if !session.ExpiresAt.Equal(expiresAt.UTC()) || !session.LastSeenAt.Equal(lastSeenAt.UTC()) {
		t.Fatalf("session times = %v/%v, want UTC %v/%v", session.ExpiresAt, session.LastSeenAt, expiresAt.UTC(), lastSeenAt.UTC())
	}
}

func TestManagementSessionFromRowRejectsMissingTimes(t *testing.T) {
	if _, err := managementSessionFromRow(postgresqueries.FindManagementSessionByTokenHashRow{
		LastSeenAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err == nil {
		t.Fatal("managementSessionFromRow() error = nil, want missing expires_at error")
	}
	if _, err := managementSessionFromRow(postgresqueries.FindManagementSessionByTokenHashRow{
		ExpiresAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err == nil {
		t.Fatal("managementSessionFromRow() error = nil, want missing last_seen_at error")
	}
}

func TestManagementProfileUpdateResultFromRow(t *testing.T) {
	result := managementProfileUpdateResultFromRow(postgresqueries.UpdateManagementCurrentUserProfileRow{
		PreviousDisplayName: "旧名称",
		ID:                  "sys_user",
		Username:            "user",
		DisplayName:         "新名称",
		Role:                "user",
		MustChangePassword:  false,
	})
	if result.Before.ID != "sys_user" || result.Before.DisplayName != "旧名称" {
		t.Fatalf("before = %+v", result.Before)
	}
	if result.Account.ID != "sys_user" || result.Account.DisplayName != "新名称" {
		t.Fatalf("account = %+v", result.Account)
	}
}

func TestManagementProfileDisplayNameUniqueViolation(t *testing.T) {
	err := &pgconn.PgError{Code: "23505", ConstraintName: "idx_system_accounts_display_name_unique_lower"}
	if !isManagementProfileDisplayNameUniqueViolation(err) {
		t.Fatal("display name unique violation was not recognized")
	}
	other := &pgconn.PgError{Code: "23505", ConstraintName: "idx_system_accounts_username_unique_lower"}
	if isManagementProfileDisplayNameUniqueViolation(other) {
		t.Fatal("username unique violation should not map to profile display name conflict")
	}
}

func TestManagementAuthSQLSourceGuards(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_auth.sql")
	if err != nil {
		t.Fatalf("read query source: %v", err)
	}
	normalized := strings.ToLower(string(source))
	if strings.Contains(normalized, "password_hash") {
		t.Fatal("management auth SQL must not read password_hash")
	}
	if !strings.Contains(normalized, "delete from juhe_business.system_sessions") || !strings.Contains(normalized, "where token_hash = $1") {
		t.Fatal("management logout SQL must delete sessions by token_hash only")
	}
	if strings.Contains(normalized, "delete from juhe_business.system_sessions\nwhere system_account_id") {
		t.Fatal("management logout SQL must not revoke all sessions for an account")
	}
	if strings.Contains(normalized, "select *") || strings.Contains(normalized, "count(") {
		t.Fatal("management auth SQL must keep profile requests field-bounded and avoid aggregate scans")
	}
	if !strings.Contains(normalized, "update juhe_business.system_accounts as system_accounts") ||
		!strings.Contains(normalized, "display_name = sqlc.arg(display_name)::text") ||
		!strings.Contains(normalized, "updated_at = sqlc.arg(updated_at)::timestamptz") {
		t.Fatal("profile update SQL must only update display_name and updated_at")
	}
	if !strings.Contains(normalized, "where system_accounts.id = current_account.id") ||
		!strings.Contains(normalized, "current_account.status = 'active'") {
		t.Fatal("profile update SQL must target the current active system account by id")
	}
	if !strings.Contains(normalized, "returning") ||
		!strings.Contains(normalized, "current_account.display_name as previous_display_name") ||
		!strings.Contains(normalized, "system_accounts.must_change_password") {
		t.Fatal("profile update SQL must return only the current user summary and previous display name")
	}
}
