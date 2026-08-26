package pgpool

import (
	"database/sql"
	"sync"
	"testing"
)

func TestRegistryReusesSameURLAndRole(t *testing.T) {
	registry := NewRegistry()
	first, err := registry.Acquire("pgx", "postgres://same", "jobs", 1000, 10)
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.Acquire("pgx", "postgres://same", "jobs", 1000, 10)
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
	first, err := registry.Acquire("pgx", "postgres://same", "jobs", 1000, 10)
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.Acquire("pgx", "postgres://same", "business", 1000, 10)
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

func TestZeroValueRegistryInitializesOnceUnderConcurrency(t *testing.T) {
	var registry Registry
	const workers = 16
	handles := make([]*Handle, workers)
	errs := make([]error, workers)
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := range handles {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			handles[index], errs[index] = registry.Acquire("pgx", "postgres://same", "jobs", 4, 4)
		}(index)
	}
	close(start)
	group.Wait()
	var first *sql.DB
	for index, handle := range handles {
		if errs[index] != nil {
			t.Fatal(errs[index])
		}
		if first == nil {
			first = handle.DB()
		} else if handle.DB() != first {
			t.Fatal("zero-value registry must retain one shared pool")
		}
		if err := handle.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
