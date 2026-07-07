package postgres

import (
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
