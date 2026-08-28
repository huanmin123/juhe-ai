package businessauth

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
	_ "modernc.org/sqlite"
)

func openBusinessAuthDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:businessauth-"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for _, ddl := range []string{
		`CREATE TABLE system_accounts (id TEXT PRIMARY KEY,username TEXT NOT NULL,display_name TEXT,role TEXT NOT NULL,status TEXT NOT NULL,password_hash TEXT NOT NULL,must_change_password INTEGER NOT NULL,last_login_at TEXT,updated_at TEXT)`,
		`CREATE TABLE system_sessions (id TEXT PRIMARY KEY,system_account_id TEXT NOT NULL,token_hash TEXT UNIQUE,expires_at TEXT NOT NULL,created_at TEXT NOT NULL,last_seen_at TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	return db
}

func TestServiceRequiresCompleteOwnerGate(t *testing.T) {
	db := openBusinessAuthDB(t)
	defer db.Close()
	service, err := New(db, modelcheckauth.SQLite, time.Now, OwnerGate{Confirmed: true, SchemaReady: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.Login(context.Background(), "admin", "secret", 1); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("expected owner gate error, got %v", err)
	}
}

func TestServiceSessionLifecycleAndPasswordCAS(t *testing.T) {
	db := openBusinessAuthDB(t)
	defer db.Close()
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	// Generated using Node's pbkdf2$sha512 representation; the test never
	// stores or compares submitted plaintext outside the verification call.
	const passwordHash = "pbkdf2$sha512$120000$MDEyMzQ1Njc4OWFiY2RlZg$MB16ie0MUIkgM1Xio7iCM8x9uCDqJJf5rkQ297w84fg"
	if _, err := db.Exec(`INSERT INTO system_accounts(id,username,display_name,role,status,password_hash,must_change_password) VALUES ('acct','admin','Admin','admin','active',?,0)`, passwordHash); err != nil {
		t.Fatal(err)
	}
	service, err := New(db, modelcheckauth.SQLite, func() time.Time { return now }, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	issued, creds, ok, err := service.Login(context.Background(), "ADMIN", "correct horse battery staple", 1)
	if err != nil || !ok || issued.Token == "" || issued.SessionID == "" || creds.SystemAccountID != "acct" {
		t.Fatalf("login issued=%+v creds=%+v ok=%v err=%v", issued, creds, ok, err)
	}
	actor, err := service.Touch(context.Background(), issued.Token)
	if err != nil || actor.SessionID != issued.SessionID {
		t.Fatalf("touch actor=%+v err=%v", actor, err)
	}
	if err := service.Logout(context.Background(), issued.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Touch(context.Background(), issued.Token); !errors.Is(err, modelcheckauth.ErrSessionExpired) {
		t.Fatalf("expected revoked session, got %v", err)
	}

	issued, _, ok, err = service.Login(context.Background(), "admin", "correct horse battery staple", 1)
	if err != nil || !ok {
		t.Fatalf("second login ok=%v err=%v", ok, err)
	}
	revision, err := service.CurrentCredentialRevision(context.Background(), "acct")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := service.ChangePassword(context.Background(), "acct", "stale-revision", "new-password", issued.SessionID)
	if err != nil || changed {
		t.Fatalf("stale CAS changed=%v err=%v", changed, err)
	}
	changed, err = service.ChangePassword(context.Background(), "acct", revision, "new-password", issued.SessionID)
	if err != nil || !changed {
		t.Fatalf("password change changed=%v err=%v", changed, err)
	}
	if _, err := service.Touch(context.Background(), issued.Token); err != nil {
		t.Fatalf("kept session should remain valid: %v", err)
	}
}
