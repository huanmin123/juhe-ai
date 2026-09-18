package main

import (
	"context"
	"testing"
)

func TestZZProbeClosedLocks(t *testing.T) {
	fixture := newAccountLocksFixture(t)
	if err := fixture.db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	row, err := fixture.locks.findState(context.Background(), "w14a-acc")
	t.Logf("findState -> row=%v err=%v", row, err)
	err = fixture.locks.CompleteSuccessAsync(context.Background(), "w14a-acc", "lease", nil)
	t.Logf("CompleteSuccessAsync err=%v", err)
	_, err = fixture.locks.AcquireRetryLeaseAsync(context.Background(), "w14a-acc", 5000)
	t.Logf("AcquireRetryLeaseAsync err=%v", err)
}
