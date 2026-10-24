package modelcheckauth

import (
	"context"
	"testing"
	"time"
)

func TestSessionLifecycleUsesOwnerTransaction(t *testing.T) {
	db := newModelCheckAuthPGDatabase(t)
	now := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO juhe_business.system_accounts (id,username,display_name,status,password_hash,created_at,updated_at) VALUES ('acct','acct','acct','active','password-hash','','')`); err != nil {
		t.Fatal(err)
	}
	auth, err := New(db, Postgres, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	issued, ok, err := auth.CreateAuthenticatedSession(context.Background(), "acct", hashString("password-hash"), 1)
	if err != nil || !ok || issued.Token == "" || issued.SessionID == "" {
		t.Fatalf("issued=%+v ok=%v err=%v", issued, ok, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM juhe_business.system_sessions WHERE system_account_id='acct'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("session count=%d err=%v", count, err)
	}
	if err := auth.RevokeOtherSessions(context.Background(), "acct", issued.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := auth.RevokeToken(context.Background(), issued.Token); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM juhe_business.system_sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("revoke count=%d err=%v", count, err)
	}
}

func TestTemporaryTokenKeepsNodePrefixAndRevisionFence(t *testing.T) {
	db := newModelCheckAuthPGDatabase(t)
	if _, err := db.Exec(`INSERT INTO juhe_business.system_accounts (id,username,display_name,status,password_hash,created_at,updated_at) VALUES ('acct','acct','acct','active','password-hash','','')`); err != nil {
		t.Fatal(err)
	}
	auth, _ := New(db, Postgres, time.Now)
	issued, ok, err := auth.CreateTemporaryAccessToken(context.Background(), "acct", "wrong", 60)
	if err != nil || ok {
		t.Fatalf("wrong revision issued=%+v ok=%v err=%v", issued, ok, err)
	}
	issued, ok, err = auth.CreateTemporaryAccessToken(context.Background(), "acct", hashString("password-hash"), 60)
	if err != nil || !ok || !temporaryToken.MatchString(issued.Token) {
		t.Fatalf("temporary issued=%+v ok=%v err=%v", issued, ok, err)
	}
}
