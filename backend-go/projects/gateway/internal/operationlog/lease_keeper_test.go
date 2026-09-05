package operationlog

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// openKeeperTestStore opens an isolated SQLite F4 store for keeper tests.
func openKeeperTestStore(t *testing.T) Store {
	t.Helper()
	root := t.TempDir()
	business := filepath.Join(root, "business.sqlite3")
	createBusinessSettings(t, business, "365")
	store, err := OpenStore(Config{Enabled: true, InstanceID: "keeper-owner", Mode: ModeSQLite, DatabasePath: filepath.Join(root, "operation.sqlite3"), BusinessSettingsPath: business})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// waitKeeperPersisted polls until the producer record becomes visible or the
// deadline passes (the producer persists asynchronously).
func waitKeeperPersisted(t *testing.T, store Store, id string) ListResult {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result, err := store.List(context.Background(), ListOptions{})
		if err != nil {
			t.Fatalf("list persisted logs: %v", err)
		}
		for _, item := range result.Items {
			if item.ID == id {
				return result
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("producer record never persisted")
	return ListResult{}
}

// assertKeeperLeaseValid proves the lease row is still alive (lease_until>
// now, same owner+fence) after producer activity: the historical defect was a
// per-record renewal with a zero TTL that wrote lease_until=now and fenced
// every subsequent write out.
func assertKeeperLeaseValid(t *testing.T, store Store, lease OwnerLease, ttl time.Duration) {
	t.Helper()
	renewed, err := store.RenewOwnerLease(context.Background(), lease, ttl)
	if err != nil || !renewed {
		t.Fatalf("owner lease must stay valid after producer writes: renewed=%v err=%v", renewed, err)
	}
}

// TestLeaseKeeperSharedByProducerPersistsManagementLogs is the defect-1
// regression: the system-api producer and the F4 input server share one
// LeaseKeeper (single owner_id/fence_token); producer records persist and the
// lease stays live instead of being self-destructed by a zero-TTL renew.
func TestLeaseKeeperSharedByProducerPersistsManagementLogs(t *testing.T) {
	store := openKeeperTestStore(t)
	keeper, ok, err := StartLeaseKeeper(context.Background(), store, "keeper-owner", time.Minute, nil)
	if err != nil || !ok {
		t.Fatalf("start keeper: ok=%v err=%v", ok, err)
	}
	defer keeper.Close()

	// A second acquisition while the keeper holds the row must fail: the
	// historical in-process fight (producer vs sidecar) must stay impossible.
	if _, ok, err := store.AcquireOwnerLease(context.Background(), "sidecar", time.Minute); err != nil || ok {
		t.Fatalf("second holder must be refused: ok=%v err=%v", ok, err)
	}

	producer := NewProducer(store, keeper.Lease(), Config{OwnerLease: keeper.TTL()}, nil)
	entry := Input{ID: "keeper-1", ActorSystemAccountID: "actor-1", ActorRole: "admin", Module: "accounts", Action: "update", OperationKey: "accounts.update", ResourceType: "account", Summary: "keeper shared lease", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	producer.Record(entry)
	waitKeeperPersisted(t, store, entry.ID)
	assertKeeperLeaseValid(t, store, keeper.Lease(), keeper.TTL())

	// Producer without a configured renewal TTL (Config{}): the zero-TTL
	// renew that used to self-destruct the fence is skipped, so writes still
	// land and the lease survives.
	bareProducer := NewProducer(store, keeper.Lease(), Config{}, nil)
	bare := Input{ID: "keeper-2", ActorSystemAccountID: "actor-1", ActorRole: "admin", Module: "groups", Action: "update", OperationKey: "groups.update", ResourceType: "group", Summary: "zero ttl guard", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	bareProducer.Record(bare)
	waitKeeperPersisted(t, store, bare.ID)
	assertKeeperLeaseValid(t, store, keeper.Lease(), keeper.TTL())
}

// TestLeaseKeeperCloseReleasesLease proves the shutdown contract: after
// Close, a successor can acquire the row immediately (timely handover instead
// of a 30-60s expiry wait).
func TestLeaseKeeperCloseReleasesLease(t *testing.T) {
	store := openKeeperTestStore(t)
	keeper, ok, err := StartLeaseKeeper(context.Background(), store, "keeper-owner", time.Minute, nil)
	if err != nil || !ok {
		t.Fatalf("start keeper: ok=%v err=%v", ok, err)
	}
	keeper.Close()
	if keeper.LostError() != nil {
		t.Fatalf("clean close must not report lease loss: %v", keeper.LostError())
	}
	lease, ok, err := store.AcquireOwnerLease(context.Background(), "successor", time.Minute)
	if err != nil || !ok {
		t.Fatalf("successor acquisition after close: ok=%v err=%v", ok, err)
	}
	_ = lease
}

// TestRunInputServerSharedLeaseRejectsNilKeeper pins the shared-lease entry
// contract: without a keeper the composed process must fail fast instead of
// silently serving unfenced writes.
func TestRunInputServerSharedLeaseRejectsNilKeeper(t *testing.T) {
	store := openKeeperTestStore(t)
	err := RunInputServerSharedLease(context.Background(), store, Config{OwnerLease: time.Minute}, InputServerConfig{ListenAddress: "127.0.0.1:0"}, nil, nil)
	if err == nil {
		t.Fatal("shared-lease server must refuse a nil keeper")
	}
}
