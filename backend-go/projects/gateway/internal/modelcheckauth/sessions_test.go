package modelcheckauth

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestSessionLifecycleUsesOwnerTransaction(t *testing.T) {
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE system_accounts (id TEXT PRIMARY KEY,status TEXT,password_hash TEXT,last_login_at TEXT)`,
		`CREATE TABLE system_sessions (id TEXT PRIMARY KEY,system_account_id TEXT,token_hash TEXT UNIQUE,expires_at TEXT,created_at TEXT,last_seen_at TEXT)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO system_accounts(id,status,password_hash) VALUES ('acct','active','password-hash')`); err != nil {
		t.Fatal(err)
	}
	auth, err := New(db, SQLite, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	issued, ok, err := auth.CreateAuthenticatedSession(context.Background(), "acct", hashString("password-hash"), 1)
	if err != nil || !ok || issued.Token == "" || issued.SessionID == "" {
		t.Fatalf("issued=%+v ok=%v err=%v", issued, ok, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM system_sessions WHERE system_account_id='acct'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("session count=%d err=%v", count, err)
	}
	if err := auth.RevokeOtherSessions(context.Background(), "acct", issued.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := auth.RevokeToken(context.Background(), issued.Token); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM system_sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("revoke count=%d err=%v", count, err)
	}
}

func TestTemporaryTokenKeepsNodePrefixAndRevisionFence(t *testing.T) {
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE system_accounts (id TEXT PRIMARY KEY,status TEXT,password_hash TEXT,last_login_at TEXT)`,
		`CREATE TABLE system_sessions (id TEXT PRIMARY KEY,system_account_id TEXT,token_hash TEXT UNIQUE,expires_at TEXT,created_at TEXT,last_seen_at TEXT)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO system_accounts(id,status,password_hash) VALUES ('acct','active','password-hash')`); err != nil {
		t.Fatal(err)
	}
	auth, _ := New(db, SQLite, time.Now)
	issued, ok, err := auth.CreateTemporaryAccessToken(context.Background(), "acct", "wrong", 60)
	if err != nil || ok {
		t.Fatalf("wrong revision issued=%+v ok=%v err=%v", issued, ok, err)
	}
	issued, ok, err = auth.CreateTemporaryAccessToken(context.Background(), "acct", hashString("password-hash"), 60)
	if err != nil || !ok || !temporaryToken.MatchString(issued.Token) {
		t.Fatalf("temporary issued=%+v ok=%v err=%v", issued, ok, err)
	}
}
