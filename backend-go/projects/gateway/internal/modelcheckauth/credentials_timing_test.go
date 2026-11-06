package modelcheckauth

import (
	"context"
	"testing"
)

// A missing username must run the same PBKDF2 verification as an existing
// one: the checker executes exactly once against the fixed dummy hash and the
// response stays unverified. Structural assertion via a counting checker
// override (wall-clock timing is too noisy to assert).
func TestVerifyMissingUserRunsDummyPBKDF2(t *testing.T) {
	db := newModelCheckAuthPGDatabase(t)
	auth, err := New(db, Postgres, nil)
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

// An inactive account must pay the same dummy PBKDF2 cost before the status
// rejection (SEC-07): no early return and no use of the stored hash, so a
// disabled username is not faster than an active or missing one. Structural
// assertion via the counting checker override; wall-clock timing is not
// asserted.
func TestVerifyInactiveAccountRunsDummyPBKDF2(t *testing.T) {
	db := newModelCheckAuthPGDatabase(t)
	const inactiveHash = "pbkdf2$sha512$120000$aW5hY3RpdmUtc2FsdA$MB16ie0MUIkgM1Xio7iCM8x9uCDqJJf5rkQ297w84fg"
	if _, err := db.Exec(`INSERT INTO juhe_business.system_accounts (id,username,display_name,role,status,password_hash,must_change_password,created_at,updated_at) VALUES ('off','off','Off','admin','disabled',$1,0,'','')`, inactiveHash); err != nil {
		t.Fatal(err)
	}
	auth, err := New(db, Postgres, nil)
	if err != nil {
		t.Fatal(err)
	}

	original := verifySystemAccountPassword
	calls := 0
	dummyHashes := 0
	storedHashes := 0
	verifySystemAccountPassword = func(password, passwordHash string) bool {
		calls++
		switch passwordHash {
		case dummySystemAccountPasswordHash:
			dummyHashes++
		case inactiveHash:
			storedHashes++
		}
		return original(password, passwordHash)
	}
	t.Cleanup(func() { verifySystemAccountPassword = original })

	verified, ok, err := auth.VerifySystemAccountCredentials(context.Background(), "off", "wrong-password")
	if err != nil || ok || verified != (VerifiedCredentials{}) {
		t.Fatalf("inactive account = %+v ok=%v err=%v", verified, ok, err)
	}
	if calls != 1 || dummyHashes != 1 || storedHashes != 0 {
		t.Fatalf("inactive arm calls = %d (dummy=%d stored=%d), want exactly 1/1/0", calls, dummyHashes, storedHashes)
	}
}
