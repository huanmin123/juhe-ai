package manualtestrepo

// w7c record projection and internal-primitive arms.

import (
	"context"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest"
)

func TestW7CRecordProjectionWithAllFields(t *testing.T) {
	repo, db, _ := openTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// A rich queued task with every optional column populated, canceled
	// upfront: the finalize path reads the full projection without updating.
	seedTask(t, db, "w7c-rich", "queued", 1, now)
	startedAt := now.Add(-time.Minute).Format(time.RFC3339Nano)
	if _, err := db.Exec(`UPDATE account_test_tasks SET
		model = 'gpt-4o', test_endpoint_mode = 'chat', request_system_account_filter_id = 'filter-1',
		draft_account_encrypted = 'enc-draft', started_at = ?, status_message = '进行中'
		WHERE id = 'w7c-rich'`, startedAt); err != nil {
		t.Fatal(err)
	}
	if err := repo.Fail(ctx, "w7c-rich", "late", "", nil); err != nil {
		t.Fatal(err)
	}
	var status, draft string
	if err := db.QueryRow(`SELECT status, draft_account_encrypted FROM account_test_tasks WHERE id = 'w7c-rich'`).Scan(&status, &draft); err != nil {
		t.Fatal(err)
	}
	if status != "canceled" || draft != "enc-draft" {
		t.Fatalf("rich finalize: %s %s", status, draft)
	}

	// The MarkRunning claim of a healthy task exercises the record read with
	// a non-empty started_at on the success path.
	seedTask(t, db, "w7c-rich-claim", "queued", 0, now)
	record, err := repo.MarkRunning(ctx, "w7c-rich-claim")
	if err != nil || record == nil || record.StartedAt == nil {
		t.Fatalf("claim: %#v %v", record, err)
	}
}

func TestW7CInternalPrimitivesRejectCanceledContext(t *testing.T) {
	repo, _, _ := openTestRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := repo.listRunnable(ctx, 1); err == nil {
		t.Fatal("canceled listRunnable must fail")
	}
	if _, err := repo.failExpiredQueued(ctx, 1000, 1); err == nil {
		t.Fatal("canceled failExpiredQueued must fail")
	}
	if _, err := repo.requeueInterrupted(ctx, 60_000, 1); err == nil {
		t.Fatal("canceled requeueInterrupted must fail")
	}
	if _, err := repo.completeIdleSessions(ctx, 1); err == nil {
		t.Fatal("canceled completeIdleSessions must fail")
	}
	if err := repo.cleanupExpired(ctx); err == nil {
		t.Fatal("canceled cleanupExpired must fail")
	}
	// The session-cancel reason lookup tolerates a missing task/session link.
	if reason, err := repo.sessionCancelReason(ctx, repo.db, "w7c-none"); err == nil || reason != "" {
		t.Fatalf("canceled session lookup: %q %v", reason, err)
	}
	_ = accounttest.ManualTestMaintenanceResult{}
}
