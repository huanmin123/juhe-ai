package sqlpool

import (
	"database/sql"
	"database/sql/driver"
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
}
