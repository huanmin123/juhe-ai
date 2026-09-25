package modelcheckauth

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// A missing username must run the same PBKDF2 verification as an existing
// one: the checker executes exactly once against the fixed dummy hash and the
// response stays unverified. Structural assertion via a counting checker
// override (wall-clock timing is too noisy to assert).
func TestVerifyMissingUserRunsDummyPBKDF2(t *testing.T) {
	db, err := sql.Open("sqlite", "file:modelcheckauth-dummy-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE system_accounts (id TEXT PRIMARY KEY,username TEXT NOT NULL,display_name TEXT,role TEXT NOT NULL,status TEXT NOT NULL,password_hash TEXT NOT NULL,must_change_password INTEGER NOT NULL,last_login_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE system_sessions (id TEXT PRIMARY KEY,system_account_id TEXT,token_hash TEXT UNIQUE,expires_at TEXT,created_at TEXT,last_seen_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	auth, err := New(db, SQLite, nil)
	if err != nil {
		t.Fatal(err)
	}

	original := verifySystemAccountPassword
	calls := 0
	dummyHashes := 0
	verifySystemAccountPassword = func(password, passwordHash string) bool {
		calls++
		if passwordHash == dummySystemAccountPasswordHash {
			dummyHashes++
		}
		return original(password, passwordHash)
	}
	t.Cleanup(func() { verifySystemAccountPassword = original })

	verified, ok, err := auth.VerifySystemAccountCredentials(context.Background(), "ghost-user", "some-password")
	if err != nil || ok || verified != (VerifiedCredentials{}) {
		t.Fatalf("missing user = %+v ok=%v err=%v", verified, ok, err)
	}
	if calls != 1 || dummyHashes != 1 {
		t.Fatalf("dummy PBKDF2 calls = %d (dummy-hash hits = %d), want exactly 1/1", calls, dummyHashes)
	}
}
