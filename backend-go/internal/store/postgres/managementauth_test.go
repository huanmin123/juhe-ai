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

func TestManagementSessionSummaryFromRow(t *testing.T) {
	expiresAt := time.Date(2026, 7, 10, 10, 0, 0, 0, time.FixedZone("test", 8*3600))
	createdAt := expiresAt.Add(-24 * time.Hour)
	lastSeenAt := expiresAt.Add(-time.Hour)

	session, err := managementSessionSummaryFromRow(postgresqueries.ListManagementSessionsForAccountRow{
		ID:         "sess_1",
		ExpiresAt:  pgtype.Timestamptz{Time: expiresAt, Valid: true},
		CreatedAt:  pgtype.Timestamptz{Time: createdAt, Valid: true},
		LastSeenAt: pgtype.Timestamptz{Time: lastSeenAt, Valid: true},
	})
	if err != nil {
		t.Fatalf("managementSessionSummaryFromRow() error = %v", err)
	}
	if session.ID != "sess_1" ||
		!session.ExpiresAt.Equal(expiresAt.UTC()) ||
		!session.CreatedAt.Equal(createdAt.UTC()) ||
		!session.LastSeenAt.Equal(lastSeenAt.UTC()) {
		t.Fatalf("session = %+v", session)
	}
}

func TestManagementSessionSummaryFromRowRejectsMissingTimes(t *testing.T) {
	if _, err := managementSessionSummaryFromRow(postgresqueries.ListManagementSessionsForAccountRow{
		CreatedAt:  pgtype.Timestamptz{Time: time.Now(), Valid: true},
		LastSeenAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err == nil {
		t.Fatal("managementSessionSummaryFromRow() error = nil, want missing expires_at error")
	}
	if _, err := managementSessionSummaryFromRow(postgresqueries.ListManagementSessionsForAccountRow{
		ExpiresAt:  pgtype.Timestamptz{Time: time.Now(), Valid: true},
		LastSeenAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err == nil {
		t.Fatal("managementSessionSummaryFromRow() error = nil, want missing created_at error")
	}
	if _, err := managementSessionSummaryFromRow(postgresqueries.ListManagementSessionsForAccountRow{
		ExpiresAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err == nil {
		t.Fatal("managementSessionSummaryFromRow() error = nil, want missing last_seen_at error")
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

func TestManagementPasswordAccountFromRow(t *testing.T) {
	createdAt := time.Date(2026, 7, 7, 10, 0, 0, 0, time.FixedZone("test", 8*3600))
	updatedAt := createdAt.Add(time.Hour)
	lastLoginAt := createdAt.Add(30 * time.Minute)
	account := managementPasswordAccountFromRow(postgresqueries.UpdateManagementCurrentUserPasswordRow{
		ID:                     "sys_user",
		Username:               "user",
		DisplayName:            "用户",
		Description:            pgtype.Text{String: "说明", Valid: true},
		Role:                   "user",
		Status:                 "active",
		MustChangePassword:     false,
		ImageGenerationEnabled: true,
		LastLoginAt:            pgtype.Timestamptz{Time: lastLoginAt, Valid: true},
		CreatedAt:              pgtype.Timestamptz{Time: createdAt, Valid: true},
		UpdatedAt:              pgtype.Timestamptz{Time: updatedAt, Valid: true},
	})
	if account.ID != "sys_user" ||
		account.Description != "说明" ||
		!account.ImageGenerationEnabled ||
		account.MustChangePassword ||
		account.LastLoginAt == nil ||
		!account.LastLoginAt.Equal(lastLoginAt.UTC()) ||
		!account.CreatedAt.Equal(createdAt.UTC()) ||
		!account.UpdatedAt.Equal(updatedAt.UTC()) {
		t.Fatalf("account = %+v", account)
	}
}

func TestManagementLoginSessionResultFromRow(t *testing.T) {
	createdAt := time.Date(2026, 7, 8, 10, 0, 0, 0, time.FixedZone("test", 8*3600))
	updatedAt := createdAt.Add(time.Hour)
	lastLoginAt := createdAt.Add(2 * time.Hour)
	sessionExpiresAt := createdAt.Add(14 * 24 * time.Hour)

	result, err := managementLoginSessionResultFromRow(postgresqueries.CompleteManagementLoginRow{
		ID:                     "sys_user",
		Username:               "user",
		DisplayName:            "用户",
		Description:            pgtype.Text{String: "说明", Valid: true},
		Role:                   "user",
		Status:                 "active",
		MustChangePassword:     true,
		ImageGenerationEnabled: true,
		LastLoginAt:            pgtype.Timestamptz{Time: lastLoginAt, Valid: true},
		CreatedAt:              pgtype.Timestamptz{Time: createdAt, Valid: true},
		UpdatedAt:              pgtype.Timestamptz{Time: updatedAt, Valid: true},
		SessionID:              "sess_login",
		SessionExpiresAt:       pgtype.Timestamptz{Time: sessionExpiresAt, Valid: true},
	})
	if err != nil {
		t.Fatalf("managementLoginSessionResultFromRow() error = %v", err)
	}
	if result.Account.ID != "sys_user" ||
		result.Account.Description != "说明" ||
		!result.Account.MustChangePassword ||
		result.Account.LastLoginAt == nil ||
		!result.Account.LastLoginAt.Equal(lastLoginAt.UTC()) ||
		result.SessionID != "sess_login" ||
		!result.SessionExpiresAt.Equal(sessionExpiresAt.UTC()) {
		t.Fatalf("result = %+v", result)
	}
}

func TestManagementLoginSessionResultFromRowRejectsMissingSessionExpiry(t *testing.T) {
	if _, err := managementLoginSessionResultFromRow(postgresqueries.CompleteManagementLoginRow{}); err == nil {
		t.Fatal("managementLoginSessionResultFromRow() error = nil, want missing session expiry error")
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
	sessionQuery := managementAuthSQLBlock(t, normalized, "FindManagementSessionByTokenHash")
	if strings.Contains(sessionQuery, "password_hash") {
		t.Fatal("session auth SQL must not read password_hash")
	}
	logoutQuery := managementAuthSQLBlock(t, normalized, "RevokeManagementSessionByTokenHash")
	if !strings.Contains(logoutQuery, "delete from juhe_business.system_sessions") || !strings.Contains(logoutQuery, "where token_hash = $1") {
		t.Fatal("management logout SQL must delete sessions by token_hash only")
	}
	if strings.Contains(logoutQuery, "system_account_id") {
		t.Fatal("management logout SQL must not revoke all sessions for an account")
	}
	touchQuery := managementAuthSQLBlock(t, normalized, "TouchManagementSession")
	if !strings.Contains(touchQuery, "update juhe_business.system_sessions") ||
		!strings.Contains(touchQuery, "set last_seen_at = sqlc.arg(last_seen_at)::timestamptz") ||
		!strings.Contains(touchQuery, "where id = sqlc.arg(session_id)::text") ||
		!strings.Contains(touchQuery, "and last_seen_at < sqlc.arg(cutoff)::timestamptz") {
		t.Fatal("management session touch SQL must only update stale last_seen_at by session id")
	}
	if strings.Contains(touchQuery, "expires_at") || strings.Contains(touchQuery, "system_account_id") || strings.Contains(touchQuery, "password_hash") {
		t.Fatal("management session touch SQL must not refresh expiry, account sessions or password fields")
	}
	sessionListQuery := managementAuthSQLBlock(t, normalized, "ListManagementSessionsForAccount")
	if strings.Contains(sessionListQuery, "token_hash") || strings.Contains(sessionListQuery, "password_hash") {
		t.Fatal("management session list SQL must not read token or password hashes")
	}
	if !strings.Contains(sessionListQuery, "from juhe_business.system_sessions") ||
		!strings.Contains(sessionListQuery, "where system_account_id = sqlc.arg(system_account_id)::text") ||
		!strings.Contains(sessionListQuery, "and expires_at > sqlc.arg(now_at)::timestamptz") ||
		!strings.Contains(sessionListQuery, "order by last_seen_at desc, created_at desc, id desc") ||
		!strings.Contains(sessionListQuery, "limit sqlc.arg(limit_rows)::int") ||
		!strings.Contains(sessionListQuery, "offset sqlc.arg(offset_rows)::int") {
		t.Fatal("management session list SQL must be account-scoped, active-windowed and cursor-window limited")
	}
	revokeAccountSessionQuery := managementAuthSQLBlock(t, normalized, "RevokeManagementSessionForAccount")
	if !strings.Contains(revokeAccountSessionQuery, "delete from juhe_business.system_sessions") ||
		!strings.Contains(revokeAccountSessionQuery, "where system_account_id = sqlc.arg(system_account_id)::text") ||
		!strings.Contains(revokeAccountSessionQuery, "and id = sqlc.arg(session_id)::text") {
		t.Fatal("management account session revoke SQL must delete by current account id and session id")
	}
	if strings.Contains(revokeAccountSessionQuery, "token_hash") {
		t.Fatal("management account session revoke SQL must not revoke by raw or hashed token")
	}
	if strings.Contains(normalized, "select *") || strings.Contains(normalized, "count(") {
		t.Fatal("management auth SQL must keep profile requests field-bounded and avoid aggregate scans")
	}
	profileQuery := managementAuthSQLBlock(t, normalized, "UpdateManagementCurrentUserProfile")
	if strings.Contains(profileQuery, "password_hash") {
		t.Fatal("profile update SQL must not read or update password_hash")
	}
	if !strings.Contains(profileQuery, "update juhe_business.system_accounts as system_accounts") ||
		!strings.Contains(profileQuery, "display_name = sqlc.arg(display_name)::text") ||
		!strings.Contains(profileQuery, "updated_at = sqlc.arg(updated_at)::timestamptz") {
		t.Fatal("profile update SQL must only update display_name and updated_at")
	}
	if !strings.Contains(profileQuery, "where system_accounts.id = current_account.id") ||
		!strings.Contains(profileQuery, "current_account.status = 'active'") {
		t.Fatal("profile update SQL must target the current active system account by id")
	}
	if !strings.Contains(profileQuery, "returning") ||
		!strings.Contains(profileQuery, "current_account.display_name as previous_display_name") ||
		!strings.Contains(profileQuery, "system_accounts.must_change_password") {
		t.Fatal("profile update SQL must return only the current user summary and previous display name")
	}
	credentialQuery := managementAuthSQLBlock(t, normalized, "FindManagementSystemAccountPasswordByUsername")
	if !strings.Contains(credentialQuery, "password_hash") ||
		!strings.Contains(credentialQuery, "where lower(username) = lower(sqlc.arg(username)::text)") {
		t.Fatal("password credential SQL must read password_hash only through username credential lookup")
	}
	loginQuery := managementAuthSQLBlock(t, normalized, "CompleteManagementLogin")
	if !strings.Contains(loginQuery, "update juhe_business.system_accounts") ||
		!strings.Contains(loginQuery, "last_login_at = sqlc.arg(logged_in_at)::timestamptz") ||
		!strings.Contains(loginQuery, "updated_at = sqlc.arg(logged_in_at)::timestamptz") ||
		!strings.Contains(loginQuery, "and status = 'active'") ||
		!strings.Contains(loginQuery, "and password_hash = sqlc.arg(verified_password_hash)::text") {
		t.Fatal("login SQL must update last_login_at and updated_at only for active account with the verified password hash")
	}
	if !strings.Contains(loginQuery, "insert into juhe_business.system_sessions") ||
		!strings.Contains(loginQuery, "sqlc.arg(token_hash)::text") ||
		!strings.Contains(loginQuery, "sqlc.arg(expires_at)::timestamptz") ||
		!strings.Contains(loginQuery, "sqlc.arg(logged_in_at)::timestamptz") {
		t.Fatal("login SQL must insert session token_hash, expiry, created_at and last_seen_at")
	}
	loginReturningIndex := strings.Index(loginQuery, "select")
	if loginReturningIndex < 0 || strings.Contains(loginQuery[loginReturningIndex:], "password_hash") {
		t.Fatal("login SQL must not return password_hash")
	}
	passwordUpdateQuery := managementAuthSQLBlock(t, normalized, "UpdateManagementCurrentUserPassword")
	if !strings.Contains(passwordUpdateQuery, "password_hash = sqlc.arg(password_hash)::text") ||
		!strings.Contains(passwordUpdateQuery, "must_change_password = false") ||
		!strings.Contains(passwordUpdateQuery, "updated_at = sqlc.arg(updated_at)::timestamptz") ||
		!strings.Contains(passwordUpdateQuery, "where id = sqlc.arg(system_account_id)::text") ||
		!strings.Contains(passwordUpdateQuery, "and status = 'active'") {
		t.Fatal("password update SQL must only update password_hash, must_change_password and updated_at for the current active account")
	}
	returningIndex := strings.Index(passwordUpdateQuery, "returning")
	if returningIndex < 0 || strings.Contains(passwordUpdateQuery[returningIndex:], "password_hash") {
		t.Fatal("password update SQL must not return password_hash")
	}
	revokeOtherQuery := managementAuthSQLBlock(t, normalized, "RevokeOtherManagementSessionsForAccount")
	if !strings.Contains(revokeOtherQuery, "delete from juhe_business.system_sessions") ||
		!strings.Contains(revokeOtherQuery, "where system_account_id = $1") ||
		!strings.Contains(revokeOtherQuery, "and id <> $2") {
		t.Fatal("password change SQL must revoke only other sessions for the same account")
	}
}

func managementAuthSQLBlock(t *testing.T, source string, queryName string) string {
	t.Helper()
	marker := "-- name: " + strings.ToLower(queryName)
	start := strings.Index(source, marker)
	if start < 0 {
		t.Fatalf("query %s not found", queryName)
	}
	next := strings.Index(source[start+len(marker):], "-- name: ")
	if next < 0 {
		return source[start:]
	}
	return source[start : start+len(marker)+next]
}
