package modelcheckowner

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestSQLSchedulerSourceClaimsDueTasksWithLease(t *testing.T) {
	db := newJ3BPGDatabase(t, j3bSchedulerTasksTextPayloadDDL())
	if _, err := db.Exec(j3bQ(`INSERT INTO model_check_scheduler_tasks(id,kind,due_at,payload,updated_at) VALUES ('task-1','scheduled','2026-08-27T10:00:00Z','{"targetId":"acct-1"}','2026-08-27T10:00:00Z')`)); err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db, schema: j3bTestSchema}
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
	if err := db.QueryRow(j3bQ(`SELECT claim_owner,fence_token FROM model_check_scheduler_tasks WHERE id='task-1'`)).Scan(&owner, &fence); err != nil || owner != "gateway-1" || fence != 1 {
		t.Fatalf("owner=%s fence=%d err=%v", owner, fence, err)
	}
	again, err := source.Claim(context.Background(), SchedulerScheduled, time.Date(2026, 8, 27, 10, 1, 0, 0, time.UTC), 10)
	if err != nil || len(again) != 0 {
		t.Fatalf("reclaim=%v err=%v", again, err)
	}
}

func TestSQLSchedulerSourceFencesCompletionAndFailure(t *testing.T) {
	db := newJ3BPGDatabase(t, j3bSchedulerTasksTextPayloadDDL())
	if _, err := db.Exec(j3bQ(`INSERT INTO model_check_scheduler_tasks(id,kind,due_at,payload,updated_at) VALUES ('task-1','scheduled','2026-08-27T10:00:00Z','{}','2026-08-27T10:00:00Z')`)); err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db, schema: j3bTestSchema}
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
	if err := db.QueryRow(j3bQ(`SELECT state,claim_owner FROM model_check_scheduler_tasks WHERE id='task-1'`)).Scan(&state, &owner); err != nil {
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
	db := newJ3BPGDatabase(t, j3bRetryRunsFixtureDDL(), j3bSchedulerTasksProdDDL())
	if _, err := db.Exec(j3bQ(`INSERT INTO model_check_runs VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?),(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?),(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		"run-1", "completed", "acct-1", "sys-1", "openai", "gpt-5.6", "quick", "failure", 20, nil, `{"revision":"policy-1","threshold":70,"action":"quality_isolate","recoveryIntervalMinutes":10}`, `{"evidenceFormed":true,"trustFormed":true}`, `{"configRevision":"3"}`, "2030-01-01T00:00:00Z", "failed", "2030-01-01T00:00:00Z",
		"run-2", "completed", "acct-2", "sys-2", "openai", "gpt-5.6", "quick", "success", 92, nil, `{"revision":"policy-1","threshold":70,"action":"quality_isolate","recoveryIntervalMinutes":10}`, `{"evidenceFormed":true,"trustFormed":true}`, `{"configRevision":"3"}`, "2030-01-01T00:00:00Z", "failed", "2030-01-01T00:00:00Z",
		"run-3", "running", "acct-3", "sys-3", "openai", "gpt-5.6", "quick", "failure", 20, nil, `{"revision":"policy-1","threshold":70,"action":"quality_isolate","recoveryIntervalMinutes":10}`, `{"evidenceFormed":true,"trustFormed":true}`, `{"configRevision":"3"}`, "2030-01-01T00:00:00Z", "failed", "2030-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db, schema: j3bTestSchema, HealthStatHour: mustHealthStatHourFunc(t, "Asia/Shanghai")}
	if err := store.EnsureHealthRetryTasks(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureHealthRetryTasks(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(j3bQ(`SELECT COUNT(*) FROM model_check_scheduler_tasks WHERE kind='health_sync_retry'`)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("health retry task count=%d", count)
	}
}
