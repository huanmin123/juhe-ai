package auditlog

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRunOwnerRejectsNilKeeper pins the shared-lease entry contract: the
// resident owner fails fast without a keeper instead of running unfenced.
func TestRunOwnerRejectsNilKeeper(t *testing.T) {
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	defer store.Close()
	if err := RunOwner(context.Background(), store, nil, Config{OwnerLease: time.Minute, RetentionInterval: time.Hour}, nil); err == nil {
		t.Fatal("resident owner must refuse a nil keeper")
	}
}

// TestRunOwnerStopsWithContextAndReleasesLease pins the graceful-stop
// contract inherited from the retired RunInputServer: context cancellation
// returns nil, the keeper releases the row, and a successor can acquire it
// immediately.
func TestRunOwnerStopsWithContextAndReleasesLease(t *testing.T) {
	cfg := sqliteConfig(t, t.TempDir())
	cfg.RetentionInterval = time.Hour
	store := openSQLiteStore(t, cfg)
	defer store.Close()

	keeper, ok, err := StartLeaseKeeper(context.Background(), store, cfg.InstanceID, time.Minute, nil)
	if err != nil || !ok {
		t.Fatalf("start keeper: ok=%v err=%v", ok, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunOwner(ctx, store, keeper, cfg, nil) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("graceful stop returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunOwner did not stop after cancellation")
	}
	keeper.Close()

	if _, ok, err := store.AcquireOwnerLease(context.Background(), "successor", time.Minute); err != nil || !ok {
		t.Fatalf("successor acquisition after close: ok=%v err=%v", ok, err)
	}
}

// TestRunOwnerSurfacesLostLeaseAsComponentError pins the supervisor-boundary
// semantics: when the shared keeper loses the fence (keeper renewed by a
// rival after forced release), RunOwner returns the terminal lease error
// instead of serving fenced retention.
func TestRunOwnerSurfacesLostLeaseAsComponentError(t *testing.T) {
	cfg := sqliteConfig(t, t.TempDir())
	cfg.RetentionInterval = time.Hour
	store := openSQLiteStore(t, cfg)
	defer store.Close()

	keeper, ok, err := StartLeaseKeeper(context.Background(), store, cfg.InstanceID, time.Minute, nil)
	if err != nil || !ok {
		t.Fatalf("start keeper: ok=%v err=%v", ok, err)
	}
	defer keeper.Close()

	// A second acquisition by a rival is refused while we hold the row, so
	// force the loss through the keeper's own terminal path instead.
	keeper.fatal(ErrOwnerLeaseLost)
	err = RunOwner(context.Background(), store, keeper, cfg, nil)
	if !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("lost lease must surface as component error, got %v", err)
	}
}

// TestRunOwnerRunsRetentionPass pins the cadence transfer: the retention pass
// scheduled by the resident owner executes within the configured interval and
// deletes an expired success log exactly like the retired listener loop.
func TestRunOwnerRunsRetentionPass(t *testing.T) {
	cfg := sqliteConfig(t, t.TempDir())
	// Retention policy defaults (3 success days) cannot age the fixture; use
	// the zero-success-retention mode so the cutoff is the hot window (1h)
	// and the minimum valid interval (1s, validateRetentionPolicy floor) so
	// the first pass fires within the test deadline.
	cfg.SuccessRetentionDays = 0
	cfg.SuccessHotRetentionHours = 0
	cfg.SuccessSampleRate = 0
	cfg.RetentionInterval = time.Second
	store := openSQLiteStore(t, cfg)
	defer store.Close()

	keeper, ok, err := StartLeaseKeeper(context.Background(), store, cfg.InstanceID, time.Minute, nil)
	if err != nil || !ok {
		t.Fatalf("start keeper: ok=%v err=%v", ok, err)
	}
	defer keeper.Close()

	input := producerTestInput("retention-pass")
	// The keeper already holds the row; write under its fence like the
	// producer would (a second AcquireOwnerLease would be refused).
	if _, err := store.Persist(context.Background(), keeper.Lease(), input); err != nil {
		t.Fatal(err)
	}
	// Age the row past every cutoff by back-dating created_at (the retention
	// scan keys on the canonical created_at column).
	if err := ageAuditRow(t, store, input.ID); err != nil {
		t.Fatal(err)
	}
	remaining := func() int {
		t.Helper()
		implementation := store.(*sqlStore)
		var count int
		if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE id=?`, input.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if remaining() != 1 {
		t.Fatal("fixture row must exist before the retention pass")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunOwner(ctx, store, keeper, cfg, nil) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if remaining() == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("RunOwner after retention pass: %v", err)
	}
	if remaining() != 0 {
		t.Fatal("resident owner retention pass must delete the aged success row")
	}
}

func ageAuditRow(t *testing.T, store Store, id string) error {
	t.Helper()
	implementation, ok := store.(*sqlStore)
	if !ok {
		t.Skip("retention ageing helper requires the SQLite store implementation")
	}
	_, err := implementation.db.Exec(`UPDATE audit_logs SET created_at='2000-01-01T00:00:00.000000000Z' WHERE id=?`, id)
	return err
}
