package sqlpool

import (
	"database/sql"
	"database/sql/driver"
	"sync"
	"testing"
)

func init() {
	sql.Register("sqlpool-test", noopDriver{})
}

type noopDriver struct{}

func (noopDriver) Open(string) (driver.Conn, error) { return noopConn{}, nil }

type noopConn struct{}

func (noopConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (noopConn) Close() error                        { return nil }
func (noopConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }

func TestRegistryReusesAndClosesWithReferences(t *testing.T) {
	registry := NewRegistry()
	open := func() (*sql.DB, error) { return sql.Open("sqlpool-test", "") }
	first, err := registry.Acquire(open, "memory", "jobs", 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.Acquire(open, "memory", "jobs", 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	if first.DB() != second.DB() {
		t.Fatal("same URL and role must reuse one pool")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRegistrySeparatesRoleAndRejectsInvalidBounds(t *testing.T) {
	registry := NewRegistry()
	open := func() (*sql.DB, error) { return sql.Open("sqlpool-test", "") }
	first, err := registry.Acquire(open, "memory", "jobs", 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.Acquire(open, "memory", "business", 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	if first.DB() == second.DB() {
		t.Fatal("different roles must not share a pool")
	}
	_ = first.Close()
	_ = second.Close()
	if _, err := registry.Acquire(open, "memory", "jobs", 4, 5); err == nil {
		t.Fatal("idle connections above open connections must be rejected")
	}
	if _, err := registry.Acquire(open, "memory", "jobs", 12, MaxIdleConns+1); err == nil {
		t.Fatalf("idle connections above the platform limit %d must be rejected", MaxIdleConns)
	}
}

func TestRegistryObserverAndStatsAreCredentialFree(t *testing.T) {
	registry := NewRegistry()
	var mu sync.Mutex
	events := make([]PoolEvent, 0, 3)
	registry.SetObserver(func(event PoolEvent) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	})
	open := func() (*sql.DB, error) { return sql.Open("sqlpool-test", "") }
	handle, err := registry.Acquire(open, "postgres://user:secret@db/app", "jobs", 8, 3)
	if err != nil {
		t.Fatal(err)
	}
	snapshots := registry.Stats()
	if len(snapshots) != 1 || snapshots[0].Role != "jobs" || snapshots[0].MaxOpen != 8 || snapshots[0].MaxIdle != 3 {
		t.Fatalf("unexpected pool snapshot: %+v", snapshots)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 || events[0].Kind != "open" || events[1].Kind != "close" {
		t.Fatalf("unexpected lifecycle events: %+v", events)
	}
	for _, event := range events {
		if event.Role != "jobs" || event.MaxOpen != 8 || event.MaxIdle != 3 {
			t.Fatalf("unexpected lifecycle event: %+v", event)
		}
	}
}
