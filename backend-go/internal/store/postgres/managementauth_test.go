package postgres

import (
	"os"
	"strings"
	"testing"
	"time"

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
}
