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
