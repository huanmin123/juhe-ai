package pgpool

import "testing"

func TestRegistryReusesSameURLAndRole(t *testing.T) {
	registry := NewRegistry()
	first, err := registry.Acquire("pgx", "postgres://same", "jobs", 1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.Acquire("pgx", "postgres://same", "jobs", 1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if first.DB() != second.DB() {
		t.Fatal("same URL and role must reuse one database/sql pool")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRegistrySeparatesRole(t *testing.T) {
	registry := NewRegistry()
	first, err := registry.Acquire("pgx", "postgres://same", "jobs", 1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.Acquire("pgx", "postgres://same", "business", 1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if first.DB() == second.DB() {
		t.Fatal("different roles must not share a pool")
	}
	_ = first.Close()
	_ = second.Close()
}

func TestRegistryRejectsIdleAboveOpen(t *testing.T) {
	if _, err := NewRegistry().Acquire("pgx", "postgres://same", "jobs", 4, 5); err == nil {
		t.Fatal("idle connections above open connections must be rejected")
	}
}
