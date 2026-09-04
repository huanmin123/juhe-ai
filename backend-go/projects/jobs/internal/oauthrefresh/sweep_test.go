package oauthrefresh

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

const (
	timeMillisecond = time.Millisecond
	timeHour        = time.Hour
)

// newSweepStore shares the sqlite fixture for the sweep family. The clock is
// fixed at defaultNow.
func newSweepStore(t *testing.T) (*Store, *sql.DB, *fixedClock) {
	t.Helper()
	store, db, _ := newTestStore(t)
	clock := &fixedClock{current: defaultNow()}
	return store.WithClock(func() time.Time { return clock.Now() }), db, clock
}

func (c *fixedClock) time() time.Time { return c.current }

// clock.nowISO exists on Store; keep the sweep tests explicit.
func seedGrantRow(t *testing.T, db *sql.DB, id, status, expiresAt, revokedAt, createdBy string) {
	t.Helper()
	// Empty strings render as NULL columns (the Node rows never store '').
	var expiresArg, revokedArg any
	if expiresAt != "" {
		expiresArg = expiresAt
	}
	if revokedAt != "" {
		revokedArg = revokedAt
	}
	_, err := db.Exec(`INSERT INTO resource_authorization_grants (id, resource_type, resource_id, owner_system_account_id, grantee_type, grantee_id,
		status, revoked_at, revoked_by, created_by, expires_at, updated_at)
		VALUES (?, 'api_key', 'res', 'owner', 'system_account', 'grantee', ?, ?, '', ?, ?, ?)`,
		id, status, revokedArg, createdBy, expiresArg, isoMillis(defaultNow()))
	if err != nil {
		t.Fatal(err)
	}
}

func assertGrantStatus(t *testing.T, db *sql.DB, id, want string) {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM resource_authorization_grants WHERE id = ?`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != want {
		t.Fatalf("grant %s status=%q want %q", id, status, want)
	}
}

func TestSweepExpiryBoundary(t *testing.T) {
	store, db, clock := newSweepStore(t)
	now := isoMillis(clock.Now())
	// Exactly at now → expired (expires_at <= now).
	seedGrantRow(t, db, "g-at", "active", now, "", "")
	// One millisecond in the future → stays.
	seedGrantRow(t, db, "g-future", "active", isoMillis(clock.time().Add(timeMillisecond)), "", "")
	// Already past → expired.
	seedGrantRow(t, db, "g-past", "active", isoMillis(clock.time().Add(-timeHour)), "", "")
	// Paused grants expire too; an existing revoked_at is preserved.
	seedGrantRow(t, db, "g-paused", "paused", isoMillis(clock.time().Add(-timeHour)), "keep-revoked-at", "")
	// Revoked grants are invisible.
	seedGrantRow(t, db, "g-revoked", "revoked", isoMillis(clock.time().Add(-timeHour)), "", "")
	// NULL expires_at never expires.
	seedGrantRow(t, db, "g-null", "active", "", "", "")

	result, err := store.RunAuthorizationExpirySweep(context.Background(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Expired != 3 {
		t.Fatalf("expired=%d", result.Expired)
	}
	assertGrantStatus(t, db, "g-at", "expired")
	assertGrantStatus(t, db, "g-past", "expired")
	assertGrantStatus(t, db, "g-paused", "expired")
	assertGrantStatus(t, db, "g-future", "active")
	assertGrantStatus(t, db, "g-revoked", "revoked")
	assertGrantStatus(t, db, "g-null", "active")

	// revoked_at = COALESCE(revoked_at, now).
	var revokedAt string
	if err := db.QueryRow(`SELECT revoked_at FROM resource_authorization_grants WHERE id = 'g-paused'`).Scan(&revokedAt); err != nil {
		t.Fatal(err)
	}
	if revokedAt != "keep-revoked-at" {
		t.Fatalf("revoked_at=%q", revokedAt)
	}
}

func TestSweepBatchLimitAndFinalizer(t *testing.T) {
	store, db, clock := newSweepStore(t)
	for i := 0; i < 5; i++ {
		seedGrantRow(t, db, grantID(i), "active", isoMillis(clock.time().Add(-timeHour)), "", "")
	}
	finalized := []string{}
	finalizer := FinalizerFunc(func(_ context.Context, grant ResourceAuthorizationGrant, actor string) error {
		finalized = append(finalized, grant.ID+"|"+actor+"|"+grant.Status)
		return nil
	})
	result, err := store.RunAuthorizationExpirySweep(context.Background(), finalizer, 3)
	if err != nil {
		t.Fatal(err)
	}
	if result.Expired != 3 || len(finalized) != 3 {
		t.Fatalf("result=%+v finalized=%v", result, finalized)
	}
	// Ordering: expires_at ASC, then updated_at ASC, then id ASC.
	if finalized[0] != "grant-a||expired" {
		t.Fatalf("finalized[0]=%q", finalized[0])
	}
	// The remaining two grants expire on the next sweep.
	second, err := store.RunAuthorizationExpirySweep(context.Background(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if second.Expired != 2 {
		t.Fatalf("second=%+v", second)
	}
}

func TestSweepDefaultLimitIs20(t *testing.T) {
	store, db, clock := newSweepStore(t)
	for i := 0; i < 22; i++ {
		seedGrantRow(t, db, grantID(i), "active", isoMillis(clock.time().Add(-timeHour)), "", "")
	}
	result, err := store.RunAuthorizationExpirySweep(context.Background(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Expired != MaxAuthorizationExpirySweepBatchSize {
		t.Fatalf("expired=%d want %d", result.Expired, MaxAuthorizationExpirySweepBatchSize)
	}
	// Drain the remainder.
	if _, err := store.RunAuthorizationExpirySweep(context.Background(), nil, 0); err != nil {
		t.Fatal(err)
	}
	final, err := store.RunAuthorizationExpirySweep(context.Background(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if final.Expired != 0 {
		t.Fatalf("final=%+v", final)
	}
}

func grantID(i int) string {
	return "grant-" + string(rune('a'+i))
}

func TestSweepActorFallsBackToCreator(t *testing.T) {
	store, db, clock := newSweepStore(t)
	seedGrantRow(t, db, "g-actor", "active", isoMillis(clock.time().Add(-timeHour)), "", "creator-1")
	seen := ""
	finalizer := FinalizerFunc(func(_ context.Context, grant ResourceAuthorizationGrant, actor string) error {
		seen = actor
		return nil
	})
	if _, err := store.RunAuthorizationExpirySweep(context.Background(), finalizer, 0); err != nil {
		t.Fatal(err)
	}
	if seen != "creator-1" {
		t.Fatalf("actor=%q", seen)
	}
}
