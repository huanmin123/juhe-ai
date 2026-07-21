package postgres

import (
	"strings"
	"testing"
)

func TestAccountTestWorkerSQLUsesAtomicStateTransitions(t *testing.T) {
	for _, fragment := range []string{"status='queued'", "cancel_requested=false", "status='running'"} {
		if !strings.Contains(claimAccountTestTaskSQL, fragment) {
			t.Fatalf("claim SQL missing %q", fragment)
		}
	}
	for _, fragment := range []string{"WHERE id=$1 AND status='running'", "finished_at=COALESCE(finished_at,now())"} {
		if !strings.Contains(finishAccountTestTaskSQL, fragment) {
			t.Fatalf("finish SQL missing %q", fragment)
		}
	}
}
