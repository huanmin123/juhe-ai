package pgpool

import (
	"database/sql"
	"sync"
	"testing"
)

func TestRegistryReusesSameURLAndRole(t *testing.T) {
	r := NewRegistry()
	a, err := r.Acquire("postgres://same", "gateway", 1000, 10)
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.Acquire("postgres://same", "gateway", 1000, 10)
	if err != nil {
		t.Fatal(err)
	}
	if a.DB() != b.DB() {
		t.Fatal("same URL and role must reuse one pool")
	}
	_ = a.Close()
	_ = b.Close()
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
			handles[index], errs[index] = registry.Acquire("postgres://same", "gateway", 4, 4)
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
