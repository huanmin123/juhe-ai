package pgpool

import "testing"

func TestRegistryReusesSameURLAndRole(t *testing.T) {
	r := NewRegistry()
	a, err := r.Acquire("postgres://same", "gateway", 1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.Acquire("postgres://same", "gateway", 1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if a.DB() != b.DB() {
		t.Fatal("same URL and role must reuse one pool")
	}
	_ = a.Close()
	_ = b.Close()
}
