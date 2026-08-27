package proxylatency

import (
	"context"
	"testing"
	"time"
)

func TestDBConcurrencyGateBoundsWorkersAndQueue(t *testing.T) {
	gate := NewDBConcurrencyGate(2, 3)
	first, _, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	second, _, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer second()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, _, err := gate.Acquire(ctx); err == nil {
		t.Fatal("expected bounded gate acquisition to honor context")
	}
}

func TestDBConcurrencyGateNilIsNoop(t *testing.T) {
	gate := NewDBConcurrencyGate(0, 0)
	release, wait, err := gate.Acquire(context.Background())
	if err != nil || release == nil || wait != 0 {
		t.Fatalf("nil gate acquire returned nil=%t wait=%s err=%v", release == nil, wait, err)
	}
	release()
}
