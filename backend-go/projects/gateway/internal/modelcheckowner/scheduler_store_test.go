package modelcheckowner

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLSchedulerSourceClaimsDueTasksWithLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduler.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE model_check_scheduler_tasks (id TEXT PRIMARY KEY,kind TEXT NOT NULL,due_at TEXT NOT NULL,claim_owner TEXT,claim_until TEXT,fence_token INTEGER NOT NULL DEFAULT 0,state TEXT NOT NULL DEFAULT 'pending',last_error TEXT,completed_at TEXT,payload TEXT NOT NULL,updated_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_check_scheduler_tasks(id,kind,due_at,payload,updated_at) VALUES ('task-1','scheduled','2026-08-27T10:00:00Z','{"targetId":"acct-1"}','2026-08-27T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db, mode: "sqlite"}
	source := &SQLSchedulerSource{Store: store, OwnerID: "gateway-1", Lease: time.Minute}
	tasks, err := source.Claim(context.Background(), SchedulerScheduled, time.Date(2026, 8, 27, 10, 1, 0, 0, time.UTC), 10)
	if err != nil || len(tasks) != 1 || tasks[0].ID != "task-1" {
		t.Fatalf("tasks=%v err=%v", tasks, err)
	}
	if string(tasks[0].Payload) != `{"targetId":"acct-1"}` {
		t.Fatalf("payload=%s", tasks[0].Payload)
	}
	var owner string
	var fence int
	if err := db.QueryRow(`SELECT claim_owner,fence_token FROM model_check_scheduler_tasks WHERE id='task-1'`).Scan(&owner, &fence); err != nil || owner != "gateway-1" || fence != 1 {
		t.Fatalf("owner=%s fence=%d err=%v", owner, fence, err)
	}
	again, err := source.Claim(context.Background(), SchedulerScheduled, time.Date(2026, 8, 27, 10, 1, 0, 0, time.UTC), 10)
	if err != nil || len(again) != 0 {
		t.Fatalf("reclaim=%v err=%v", again, err)
	}
}

func TestSQLSchedulerSourceFencesCompletionAndFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduler-lifecycle.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE model_check_scheduler_tasks (id TEXT PRIMARY KEY,kind TEXT NOT NULL,due_at TEXT NOT NULL,claim_owner TEXT,claim_until TEXT,fence_token INTEGER NOT NULL DEFAULT 0,state TEXT NOT NULL DEFAULT 'pending',last_error TEXT,completed_at TEXT,payload TEXT NOT NULL,updated_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_check_scheduler_tasks(id,kind,due_at,payload,updated_at) VALUES ('task-1','scheduled','2026-08-27T10:00:00Z','{}','2026-08-27T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db, mode: "sqlite"}
	source := &SQLSchedulerSource{Store: store, OwnerID: "gateway-1", Lease: time.Minute}
	tasks, err := source.Claim(context.Background(), SchedulerScheduled, time.Date(2026, 8, 27, 10, 1, 0, 0, time.UTC), 10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%v err=%v", tasks, err)
	}
	if err := source.Fail(context.Background(), tasks[0], context.Canceled); err != nil {
		t.Fatal(err)
	}
	var state string
	var owner sql.NullString
	if err := db.QueryRow(`SELECT state,claim_owner FROM model_check_scheduler_tasks WHERE id='task-1'`).Scan(&state, &owner); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || owner.Valid {
		t.Fatalf("state=%q owner=%v", state, owner)
	}
	if err := source.Complete(context.Background(), ScheduleTask{ID: "task-1", OwnerID: "stale", FenceToken: 1}); err == nil {
		t.Fatal("stale owner must not complete task")
	}
}

func TestEnsureHealthRetryTasksMaterializesOneTaskPerFailedRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduler-health.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY,quality_health_sync_status TEXT,finished_at TEXT,updated_at TEXT)`,
		`CREATE TABLE model_check_scheduler_tasks (id TEXT PRIMARY KEY,kind TEXT NOT NULL,due_at TEXT NOT NULL,claim_owner TEXT,claim_until TEXT,fence_token INTEGER NOT NULL DEFAULT 0,state TEXT NOT NULL DEFAULT 'pending',last_error TEXT,completed_at TEXT,payload TEXT NOT NULL,updated_at TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO model_check_runs VALUES ('run-1','failed','2030-01-01T00:00:00Z','2030-01-01T00:00:00Z'),('run-2','applied','2030-01-01T00:00:00Z','2030-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db, mode: "sqlite"}
	if err := store.EnsureHealthRetryTasks(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureHealthRetryTasks(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM model_check_scheduler_tasks WHERE kind='health_sync_retry'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("health retry task count=%d", count)
	}
}
