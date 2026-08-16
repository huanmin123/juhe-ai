package accounthealth

import (
	"context"
	"os"
	"testing"
	"time"
)

// L3 supplies an isolated jobs-owned PostgreSQL database. This proves that a
// graceful shutdown releases the exact lease generation through PgBouncer;
// it must never point at a shared production jobs store.
func TestPostgresStoreReleaseOwnerLeaseSmoke(t *testing.T) {
	postgresURL := os.Getenv("JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL")
	if postgresURL == "" {
		t.Skip("requires isolated J1 PostgreSQL jobs-store smoke environment")
	}
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: postgresURL})
	if err != nil {
		t.Fatalf("open jobs PostgreSQL store: %v", err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure jobs PostgreSQL schema: %v", err)
	}
	first, acquired, err := store.AcquireOwnerLease(ctx, "pg-release-owner-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire first owner: acquired=%t err=%v", acquired, err)
	}
	if err := store.ReleaseOwnerLease(ctx, first); err != nil {
		t.Fatalf("release first owner: %v", err)
	}
	if _, acquired, err := store.AcquireOwnerLease(ctx, "pg-release-owner-b", time.Minute); err != nil || !acquired {
		t.Fatalf("replacement owner must acquire released PostgreSQL lease: acquired=%t err=%v", acquired, err)
	}
}
