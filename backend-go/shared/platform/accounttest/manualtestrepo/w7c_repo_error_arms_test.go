package manualtestrepo

// w7c error-injection arms: canceled contexts fail transaction starts and
// reads, and narrowly scoped RAISE(ABORT) triggers surface Exec failures
// through the maintenance and lifecycle entrypoints.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest"
)

func TestW7CCanceledContextArms(t *testing.T) {
	repo, db, _ := openTestRepo(t)
	seedTask(t, db, "w7c-cx-ctx", "queued", 0, time.Now().UTC())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := repo.MarkRunning(ctx, "w7c-cx-ctx"); err == nil {
		t.Fatal("canceled MarkRunning must fail")
	}
	if err := repo.Complete(ctx, "w7c-cx-ctx", accounttest.ManualTestTaskExecutorResult{Success: true}, nil); err == nil {
		t.Fatal("canceled Complete must fail")
	}
	if err := repo.Fail(ctx, "w7c-cx-ctx", "m", "", nil); err == nil {
		t.Fatal("canceled Fail must fail")
	}
	if err := repo.Cancel(ctx, "w7c-cx-ctx", "m", nil); err == nil {
		t.Fatal("canceled Cancel must fail")
	}
	if err := repo.UpdateMessage(ctx, "w7c-cx-ctx", "m", nil); err == nil {
		t.Fatal("canceled UpdateMessage must fail")
	}
	if _, err := repo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "start"}); err == nil {
		t.Fatal("canceled Maintenance must fail")
	}

	// Cancel with an invalid id is a documented no-op, even on a live ctx.
	if err := repo.Cancel(context.Background(), "  ", "m", nil); err != nil {
		t.Fatalf("invalid cancel id: %v", err)
	}
}

func TestW7CExecFailureInjectionArms(t *testing.T) {
	ctx := context.Background()

	// A tasks-table update abort fails markCanceledTx through Cancel.
	cancelDBRepo, cancelDB, _ := openTestRepo(t)
	seedTask(t, cancelDB, "w7c-abort-cancel", "queued", 0, time.Now().UTC())
	pgAbortTrigger(t, cancelDB, "w7c_abort_task_update", "UPDATE", "account_test_tasks", "w7c task update aborted")
	if err := cancelDBRepo.Cancel(ctx, "w7c-abort-cancel", "m", nil); err == nil || !strings.Contains(err.Error(), "w7c task update aborted") {
		t.Fatalf("aborted cancel: %v", err)
	}
	// A tasks-table delete abort fails cleanupExpired through Maintenance.
	cleanupRepo, cleanupDB, _ := openTestRepo(t)
	seedTask(t, cleanupDB, "w7c-abort-cleanup", "success", 0, time.Now().UTC().Add(-48*time.Hour))
	if _, err := cleanupDB.Exec(`UPDATE juhe_business.account_test_tasks SET finished_at = $1 WHERE id = 'w7c-abort-cleanup'`, time.Now().UTC().Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	pgAbortTrigger(t, cleanupDB, "w7c_abort_task_delete", "DELETE", "account_test_tasks", "w7c task delete aborted")
	_, err := cleanupRepo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "sweep"})
	if err == nil || !strings.Contains(err.Error(), "w7c task delete aborted") {
		t.Fatalf("aborted cleanup: %v", err)
	}

	// A sessions-table update abort fails completeIdleSessions through
	// Maintenance.
	idleRepo, idleDB, _ := openTestRepo(t)
	now := time.Now().UTC()
	seedTask(t, idleDB, "w7c-idle-done", "success", 0, now.Add(-time.Hour))
	w7cSeedSession(t, idleDB, "w7c-abort-idle", "running", nil, now.Add(-time.Hour))
	if _, err := idleDB.Exec(`UPDATE juhe_business.account_test_tasks SET finished_at = $1 WHERE id = 'w7c-idle-done'`, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	pgAbortTrigger(t, idleDB, "w7c_abort_session_update", "UPDATE", "account_test_sessions", "w7c session update aborted")
	_, err = idleRepo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "sweep"})
	if err == nil || !strings.Contains(err.Error(), "w7c session update aborted") {
		t.Fatalf("aborted idle completion: %v", err)
	}

	// A requeue-abort trigger fails the start action's interrupted recovery.
	requeueRepo, requeueDB, _ := openTestRepo(t)
	seedTask(t, requeueDB, "w7c-abort-requeue", "running", 0, now.Add(-time.Hour))
	pgAbortTrigger(t, requeueDB, "w7c_abort_requeue", "UPDATE", "account_test_tasks", "w7c requeue aborted")
	stale := int64(60_000)
	if _, err := requeueRepo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "start", StaleRunningMS: &stale}); err == nil || !strings.Contains(err.Error(), "w7c requeue aborted") {
		t.Fatalf("aborted requeue: %v", err)
	}

	// listRunnable surfaces row-scan level failures through sweep as well.
	failExpiredRepo, failExpiredDB, _ := openTestRepo(t)
	seedTask(t, failExpiredDB, "w7c-abort-expired", "queued", 0, now.Add(-time.Hour))
	pgAbortTrigger(t, failExpiredDB, "w7c_abort_expire_update", "UPDATE", "account_test_tasks", "w7c expire aborted")
	result, err := failExpiredRepo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "sweep", MaxQueuedMS: 1000})
	if err == nil || !strings.Contains(err.Error(), "w7c expire aborted") {
		t.Fatalf("aborted expire: %v %v", result, err)
	}
}
