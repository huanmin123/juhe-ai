package modelcheckauth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestAuthenticateBearerTouchesSessionAndRequiresAdmin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "business.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE system_accounts (id TEXT PRIMARY KEY,username TEXT NOT NULL,display_name TEXT,status TEXT NOT NULL,role TEXT NOT NULL,must_change_password INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE system_sessions (id TEXT PRIMARY KEY,system_account_id TEXT NOT NULL,token_hash TEXT NOT NULL,expires_at TEXT NOT NULL,last_seen_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	token := "juhe_tmp_" + "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLM1234"
	if len(token) != len("juhe_tmp_")+43 {
		t.Fatalf("test token length=%d", len(token))
	}
	digest := sha256.Sum256([]byte(token))
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO system_accounts VALUES ('sys-1','admin','Admin','active','admin',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_sessions VALUES ('session-1','sys-1',?,?,?)`, hex.EncodeToString(digest[:]), now.Add(time.Hour).Format(time.RFC3339Nano), now.Add(-2*time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	auth, err := New(db, SQLite, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	actor, err := auth.RequireAdmin(context.Background(), "Bearer "+token, "")
	if err != nil || actor.SystemAccountID != "sys-1" || actor.Role != "admin" {
		t.Fatalf("actor=%+v err=%v", actor, err)
	}
	var seen string
	if err := db.QueryRow(`SELECT last_seen_at FROM system_sessions WHERE id='session-1'`).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if seen != "2026-08-27T12:00:00.000Z" {
		t.Fatalf("last_seen_at=%q", seen)
	}
}

func TestResolveTokenRejectsInvalidBearer(t *testing.T) {
	if _, err := resolveToken("Bearer invalid", ""); err != ErrInvalidToken {
		t.Fatalf("err=%v, want ErrInvalidToken", err)
	}
}
