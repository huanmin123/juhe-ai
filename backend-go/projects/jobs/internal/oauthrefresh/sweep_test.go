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
	_, err := db.Exec(`INSERT INTO juhe_business.resource_authorization_grants (id, resource_type, resource_id, resource_owner_system_account_id, grantee_type, grantee_system_account_id, status, revoked_at, revoked_by, created_by, expires_at, updated_at, created_at)
		VALUES ($1, 'api_key', 'res', 'owner', 'system_account', 'grantee', $2, $3, '', $4, $5, $6, $6)`,
		id, status, revokedArg, createdBy, expiresArg, isoMillis(defaultNow()))
	if err != nil {
		t.Fatal(err)
	}
}

func assertGrantStatus(t *testing.T, db *sql.DB, id, want string) {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM juhe_business.resource_authorization_grants WHERE id = $1`, id).Scan(&status); err != nil {
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
	if err := db.QueryRow(`SELECT revoked_at FROM juhe_business.resource_authorization_grants WHERE id = 'g-paused'`).Scan(&revokedAt); err != nil {
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
	finalizer := FinalizerFunc(func(_ context.Context, _ *sql.Tx, grant ResourceAuthorizationGrant, actor string) error {
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
	finalizer := FinalizerFunc(func(_ context.Context, _ *sql.Tx, grant ResourceAuthorizationGrant, actor string) error {
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

// TestSweepMarksEndedReasonAuthorizationExpired 钉 sweep 的 ended_reason
// 契约：grants 翻 expired（ended_reason=authorization_expired），同一事务把
// 该 grant 的非终态 sources 也翻 expired 且 ended_reason 同值——extend 的
// 「当前 expired 周期」判定依赖该字面量。
func TestSweepMarksEndedReasonAuthorizationExpired(t *testing.T) {
	store, db, clock := newSweepStore(t)
	seedGrantRow(t, db, "g-reason", "active", isoMillis(clock.time().Add(-timeHour)), "", "")
	_, err := db.Exec(`INSERT INTO juhe_business.resource_authorization_sources (id, grant_id, authorization_id, status, updated_at)
		VALUES ('src-reason-pending', 'g-reason', 'rauth-reason', 'pending', $1)`, isoMillis(defaultNow()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO juhe_business.resource_authorization_sources (id, grant_id, authorization_id, status, updated_at)
		VALUES ('src-reason-active', 'g-reason', 'rauth-reason2', 'active', $1)`, isoMillis(defaultNow()))
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.RunAuthorizationExpirySweep(context.Background(), nil, 0)
	if err != nil || result.Expired != 1 {
		t.Fatalf("sweep = %+v, %v", result, err)
	}
	assertGrantStatus(t, db, "g-reason", "expired")
	var reason string
	if err := db.QueryRow(`SELECT ended_reason FROM juhe_business.resource_authorization_grants WHERE id = 'g-reason'`).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "authorization_expired" {
		t.Fatalf("grant ended_reason=%q want authorization_expired", reason)
	}
	rows, err := db.Query(`SELECT id, status, ended_reason FROM juhe_business.resource_authorization_sources
		WHERE grant_id = 'g-reason' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var id, status, endedReason string
		if err := rows.Scan(&id, &status, &endedReason); err != nil {
			t.Fatal(err)
		}
		seen++
		if status != "expired" || endedReason != "authorization_expired" {
			t.Fatalf("source %s = %s/%s, want expired/authorization_expired", id, status, endedReason)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen != 2 {
		t.Fatalf("sources swept = %d, want 2", seen)
	}
}
