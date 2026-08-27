package modelcheckauth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestAuthenticateMatchesNodeCookieBearerAndSessionTouch(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	db := newAuthDB(t)
	defer db.Close()
	insertSession(t, db, "session-token", "admin", "admin", false, now.Add(time.Hour), now.Add(-2*time.Minute))
	auth, err := New(db, SQLite, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.CheckContract(context.Background()); err != nil {
		t.Fatal(err)
	}
	actor, err := auth.RequireAdmin(context.Background(), "", "other=value; juhe_ai_session=session-token")
	if err != nil || actor.SystemAccountID != "admin" || actor.SessionID == "" {
		t.Fatalf("actor=%+v err=%v", actor, err)
	}
	var lastSeen string
	if err := db.QueryRow(`SELECT last_seen_at FROM system_sessions`).Scan(&lastSeen); err != nil || lastSeen != "2026-08-27T12:00:00.000Z" {
		t.Fatalf("lastSeen=%q err=%v", lastSeen, err)
	}
	temporary := "juhe_tmp_" + strings.Repeat("A", 43)
	insertSession(t, db, temporary, "super", "super_admin", false, now.Add(time.Hour), now)
	actor, err = auth.RequireAdmin(context.Background(), "Bearer "+temporary, "juhe_ai_session=session-token")
	if err != nil || actor.SystemAccountID != "super" {
		t.Fatalf("bearer precedence actor=%+v err=%v", actor, err)
	}
	if _, err = auth.Authenticate(context.Background(), "Bearer ordinary-session", "juhe_ai_session=session-token"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("ordinary bearer err=%v", err)
	}
}

func TestAuthenticateRejectsExpiredPasswordChangeAndUser(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	db := newAuthDB(t)
	defer db.Close()
	insertSession(t, db, "expired", "admin", "admin", false, now.Add(-time.Second), now)
	insertSession(t, db, "change", "admin2", "admin", true, now.Add(time.Hour), now)
	insertSession(t, db, "user", "user", "user", false, now.Add(time.Hour), now)
	auth, _ := New(db, SQLite, func() time.Time { return now })
	if _, err := auth.Authenticate(context.Background(), "", "juhe_ai_session=expired"); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expired err=%v", err)
	}
	if _, err := auth.RequireAdmin(context.Background(), "", "juhe_ai_session=change"); !errors.Is(err, ErrMustChange) {
		t.Fatalf("must-change err=%v", err)
	}
	if _, err := auth.RequireAdmin(context.Background(), "", "juhe_ai_session=user"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("user err=%v", err)
	}
	if _, err := auth.Authenticate(context.Background(), "", ""); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("missing err=%v", err)
	}
}

func TestCookieValueMatchesNodePercentDecoding(t *testing.T) {
	if got := cookieValue("juhe_ai_session=a%2Bb%3D%3D", SessionCookieName); got != "a+b==" {
		t.Fatalf("decoded=%q", got)
	}
	if got := cookieValue("juhe_ai_session=first; juhe_ai_session=last", SessionCookieName); got != "last" {
		t.Fatalf("duplicate cookie=%q", got)
	}
	if got := cookieValue("juhe_ai_session=bad%ZZ", SessionCookieName); got != "" {
		t.Fatalf("bad cookie=%q", got)
	}
}

func newAuthDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE system_accounts(id TEXT PRIMARY KEY,username TEXT NOT NULL,display_name TEXT NOT NULL,role TEXT NOT NULL,status TEXT NOT NULL,must_change_password INTEGER NOT NULL); CREATE TABLE system_sessions(id TEXT PRIMARY KEY,system_account_id TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,expires_at TEXT NOT NULL,last_seen_at TEXT NOT NULL)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}
func insertSession(t *testing.T, db *sql.DB, token, account, role string, mustChange bool, expiresAt, lastSeen time.Time) {
	t.Helper()
	sum := sha256.Sum256([]byte(token))
	if _, err := db.Exec(`INSERT INTO system_accounts(id,username,display_name,role,status,must_change_password) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, account, account, account, role, "active", boolToInt(mustChange)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_sessions(id,system_account_id,token_hash,expires_at,last_seen_at) VALUES(?,?,?,?,?)`, "session-"+account+"-"+token[:min(4, len(token))], account, hex.EncodeToString(sum[:]), expiresAt.Format(time.RFC3339Nano), lastSeen.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}
func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
