package accounthealth

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPostgresSchemaDoesNotCreateDatabaseSchema(t *testing.T) {
	if strings.Contains(strings.ToUpper(postgresSchema), "CREATE SCHEMA") {
		t.Fatal("PostgreSQL jobs role must not require database-level CREATE to initialize juhe_jobs")
	}
}

func TestSQLiteStoreOwnerLeaseAndIdempotentOutcome(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "account-health.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := store.AcquireOwnerLease(ctx, "owner-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire lease=%#v acquired=%t err=%v", lease, acquired, err)
	}
	if _, acquired, err := store.AcquireOwnerLease(ctx, "owner-b", time.Minute); err != nil || acquired {
		t.Fatalf("second owner must be blocked: acquired=%t err=%v", acquired, err)
	}
	if renewed, err := store.RenewOwnerLease(ctx, lease, time.Minute); err != nil || !renewed {
		t.Fatalf("lease renewal must succeed: renewed=%t err=%v", renewed, err)
	}
	observed := time.Now().UTC()
	fence := &CooldownFence{ObservationStartedAt: observed.Add(-time.Minute), Generation: "generation-1"}
	outcome := Outcome{OutcomeID: "outcome-1", RequestID: "request-1", AccountID: "account-1", Outcome: OutcomeSuccess, ObservedAt: observed, InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1, StatusCode: 200, AccountStatus: "temporary_unavailable", CooldownFence: fence}
	inserted, err := store.AppendOutcome(ctx, lease, outcome)
	if err != nil || !inserted {
		t.Fatalf("append outcome inserted=%t err=%v", inserted, err)
	}
	inserted, err = store.AppendOutcome(ctx, lease, outcome)
	if err != nil || inserted {
		t.Fatalf("same request must be idempotent: inserted=%t err=%v", inserted, err)
	}
	state, found, err := store.LoadCurrentState(ctx, "account-1")
	if err != nil || !found || state.CooldownFence == nil || state.CooldownFence.Generation != fence.Generation || !state.CooldownFence.ObservationStartedAt.Equal(fence.ObservationStartedAt) {
		t.Fatalf("cooldown fence must survive current-state read: found=%t state=%#v err=%v", found, state, err)
	}
	if _, err := store.AppendOutcome(ctx, OwnerLease{OwnerID: "owner-a", FenceToken: lease.FenceToken + 1}, Outcome{OutcomeID: "outcome-2", RequestID: "request-2", AccountID: "account-1", Outcome: OutcomeSuccess, ObservedAt: time.Now().UTC(), InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1}); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("stale lease must fail: %v", err)
	}
}
