package modelcheckowner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckactive"
	_ "modernc.org/sqlite"
)

func TestScheduleRunBudgetDerivesLeaseWindow(t *testing.T) {
	if got := ScheduleRunBudget(6 * time.Minute); got != 5*time.Minute+30*time.Second {
		t.Fatalf("budget(6m)=%s, want 5m30s", got)
	}
	if got := ScheduleRunBudget(BusinessLeaseExecutionMargin); got != BusinessLeaseExecutionMargin/2 {
		t.Fatalf("budget(=margin)=%s, want half the lease", got)
	}
	if got := ScheduleRunBudget(0); got != 0 {
		t.Fatalf("budget(0)=%s, want disabled", got)
	}
	if got := ScheduleRunBudget(-time.Minute); got != 0 {
		t.Fatalf("budget(negative)=%s, want disabled", got)
	}
}

type budgetBlockingRunner struct {
	started chan struct{}
	once    sync.Once
}

func newBudgetBlockingRunner() *budgetBlockingRunner {
	return &budgetBlockingRunner{started: make(chan struct{})}
}

func (r *budgetBlockingRunner) Run(ctx context.Context, _ RunRequest) (RunResult, error) {
	r.once.Do(func() { close(r.started) })
	<-ctx.Done()
	return RunResult{}, ctx.Err()
}

func TestSchedulerRunExecutorRunBudgetEndsRunInsideBudget(t *testing.T) {
	runner := newBudgetBlockingRunner()
	var status string
	executor := &SchedulerRunExecutor{
		Runtime:   runner,
		RunBudget: 80 * time.Millisecond,
		Build: func(_ context.Context, _ ScheduledPayload) (RunRequest, error) {
			return RunRequest{Endpoint: "https://example.invalid", Prompt: "probe"}, nil
		},
		Scheduled: func(_ context.Context, _ ScheduledPayload, result RunResult) error {
			status = result.Status
			return nil
		},
	}
	payload, _ := json.Marshal(ScheduledPayload{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6", Profile: "full", ProviderCode: "openai", Threshold: 70, PenaltyAction: "fallback", ConfigRevision: "4", DispatchRevision: 4, SourceConfigRevision: "src-4", SourceDispatchRevision: 4, PolicyRevision: "3", ProbeSetVersion: "probe-4", IdentityKey: "identity-5", ScheduleID: "sch-1", OwnerID: "gateway-1", ScheduleRevision: 2, IntervalMinutes: 60})
	started := time.Now()
	if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerScheduled, Payload: payload}); err != nil {
		t.Fatalf("budget timeout must settle through the normal failure completion: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("run exceeded its budget wall clock: %s", elapsed)
	}
	if status != string(RunFailed) {
		t.Fatalf("budget timeout completion status=%q, want failed", status)
	}
}

func TestBusinessSchedulerRunBudgetCompletesInsideLeaseWindow(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,provider_code TEXT,config_revision INTEGER,deleted_at TEXT,authorization_instance_authorization_id TEXT,status TEXT,health_check_model TEXT)`,
		`CREATE TABLE model_quality_schedules (id TEXT PRIMARY KEY,revision INTEGER,system_account_id TEXT,account_id TEXT,model TEXT,interval_minutes INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,enabled INTEGER,next_run_at TEXT,lease_owner TEXT,lease_until TEXT,last_run_id TEXT,last_run_at TEXT,last_run_status TEXT,updated_at TEXT,custom_question_ids TEXT)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`ALTER TABLE accounts ADD COLUMN dispatch_revision INTEGER NOT NULL DEFAULT 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE accounts ADD COLUMN authorization_instance_source_account_id TEXT`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO accounts (id,provider_code,config_revision,deleted_at,authorization_instance_authorization_id,status,health_check_model) VALUES ('acct','openai',4,NULL,NULL,'active','gpt-5.6-sol')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_quality_schedules VALUES ('sch',3,'sys','acct','gpt-5.6-sol',60,'quick',70,'fallback',15,1,?,NULL,NULL,NULL,NULL,NULL,'','')`, now.Add(-time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	store := schedulerStoreFixture(t)
	defer store.db.Close()
	// Lease 2s => budget 1s (ScheduleRunBudget). The blocking runner only
	// returns when the budget context expires, so the whole run is bounded by
	// the budget and CompleteScheduled must still land before lease expiry.
	source := &BusinessSchedulerSource{Business: db, Store: store, OwnerID: "gateway-1", Lease: 2 * time.Second}
	if source.EffectiveLease() != 2*time.Second {
		t.Fatalf("EffectiveLease=%s", source.EffectiveLease())
	}
	tasks, err := source.Claim(context.Background(), SchedulerScheduled, now, 10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
	var leaseUntil string
	if err := db.QueryRow(`SELECT lease_until FROM model_quality_schedules WHERE id='sch'`).Scan(&leaseUntil); err != nil {
		t.Fatal(err)
	}
	parsedLeaseUntil, err := time.Parse(time.RFC3339Nano, leaseUntil)
	if err != nil {
		t.Fatal(err)
	}
	runner := newBudgetBlockingRunner()
	executor := &SchedulerRunExecutor{
		Runtime:   runner,
		RunBudget: ScheduleRunBudget(source.EffectiveLease()),
		Build: func(_ context.Context, _ ScheduledPayload) (RunRequest, error) {
			return RunRequest{}, nil
		},
		Scheduled: source.CompleteScheduled,
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("run never started")
	}
	if err := executor.Execute(context.Background(), tasks[0]); err != nil {
		t.Fatalf("budget-bounded run must complete inside the lease window: %v", err)
	}
	if !time.Now().UTC().Before(parsedLeaseUntil) {
		t.Fatalf("completion landed after lease_until=%s, second claim window opened", parsedLeaseUntil)
	}
	var owner, status, nextRun string
	if err := db.QueryRow(`SELECT COALESCE(lease_owner,''),last_run_status,next_run_at FROM model_quality_schedules WHERE id='sch'`).Scan(&owner, &status, &nextRun); err != nil {
		t.Fatal(err)
	}
	if owner != "" || status != "failed" {
		t.Fatalf("owner=%q status=%q, want cleared lease and failed terminal state", owner, status)
	}
	parsedNext, err := time.Parse(time.RFC3339Nano, nextRun)
	if err != nil {
		t.Fatal(err)
	}
	if !parsedNext.After(now.Add(30 * time.Minute)) {
		t.Fatalf("next_run_at=%s was not advanced by the failed completion", nextRun)
	}
	// The second-claim window stayed closed: an immediate re-claim finds
	// nothing because the lease was released by the in-window completion.
	again, err := source.Claim(context.Background(), SchedulerScheduled, time.Now().UTC().Add(-time.Minute), 10)
	if err != nil || len(again) != 0 {
		t.Fatalf("reclaim=%+v err=%v, want no second claim window", again, err)
	}
}

type panickingExecutor struct{}

func (panickingExecutor) Execute(context.Context, ScheduleTask) error {
	panic("boom")
}

type lifecycleRecorder struct {
	mu        sync.Mutex
	failed    []string
	completed []string
	failCause string
}

// Claim makes the recorder usable as a SchedulerSource, which is where
// executeSchedulerBatch discovers the SchedulerLifecycle.
func (l *lifecycleRecorder) Claim(context.Context, SchedulerKind, time.Time, int) ([]ScheduleTask, error) {
	return nil, nil
}

func (l *lifecycleRecorder) Fail(_ context.Context, task ScheduleTask, cause error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failed = append(l.failed, task.ID)
	l.failCause = cause.Error()
	return nil
}

func (l *lifecycleRecorder) Complete(_ context.Context, task ScheduleTask) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.completed = append(l.completed, task.ID)
	return nil
}

func TestSchedulerPanicWritesFailureFence(t *testing.T) {
	recorder := &lifecycleRecorder{}
	reports := make(chan SchedulerError, 4)
	executeSchedulerBatch(context.Background(), panickingExecutor{}, recorder, SchedulerScheduled, []ScheduleTask{{ID: "panic-task", Kind: SchedulerScheduled}}, func(event SchedulerError) {
		reports <- event
	}, 4)
	recorder.mu.Lock()
	failed := append([]string(nil), recorder.failed...)
	completed := append([]string(nil), recorder.completed...)
	cause := recorder.failCause
	recorder.mu.Unlock()
	if len(failed) != 1 || failed[0] != "panic-task" {
		t.Fatalf("failed=%v, want exactly the panicking task", failed)
	}
	if len(completed) != 0 {
		t.Fatalf("completed=%v, panic must not complete", completed)
	}
	if !strings.Contains(cause, "任务执行异常终止") || !strings.Contains(cause, "boom") {
		t.Fatalf("fail cause=%q, want recovered panic detail", cause)
	}
	select {
	case event := <-reports:
		if event.Operation != SchedulerErrorExecute || event.Task.ID != "panic-task" {
			t.Fatalf("report=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("panic was not reported")
	}
}

type cappedBatchExecutor struct {
	mu               sync.Mutex
	active           int
	maxSeen          int
	executed         int
	allExecuted      chan struct{}
	targetExecutions int
}

func (e *cappedBatchExecutor) Execute(_ context.Context, _ ScheduleTask) error {
	e.mu.Lock()
	e.active++
	if e.active > e.maxSeen {
		e.maxSeen = e.active
	}
	e.mu.Unlock()
	time.Sleep(10 * time.Millisecond)
	e.mu.Lock()
	e.active--
	e.executed++
	done := e.executed == e.targetExecutions
	e.mu.Unlock()
	if done {
		close(e.allExecuted)
	}
	return nil
}

func TestSchedulerCapsConcurrentBatchExecution(t *testing.T) {
	executor := &cappedBatchExecutor{allExecuted: make(chan struct{}), targetExecutions: 8}
	tasks := make([]ScheduleTask, 0, 8)
	for i := 0; i < 8; i++ {
		tasks = append(tasks, ScheduleTask{ID: fmt.Sprintf("task-%d", i), Kind: SchedulerScheduled})
	}
	executeSchedulerBatch(context.Background(), executor, schedulerSource{}, SchedulerScheduled, tasks, nil, 2)
	executor.mu.Lock()
	maxSeen, executed := executor.maxSeen, executor.executed
	executor.mu.Unlock()
	if executed != 8 {
		t.Fatalf("executed=%d, every claimed task must still run", executed)
	}
	if maxSeen > 2 {
		t.Fatalf("max concurrent executions=%d, want capped at 2", maxSeen)
	}
	if maxSeen < 2 {
		t.Fatalf("max concurrent executions=%d, cap must still allow parallel work", maxSeen)
	}
}

func schedulerTasksFixture(t *testing.T) (*sql.DB, *SQLSchedulerSource, time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/tasks.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE model_check_scheduler_tasks (id TEXT PRIMARY KEY,kind TEXT NOT NULL,due_at TEXT NOT NULL,claim_owner TEXT,claim_until TEXT,fence_token INTEGER NOT NULL DEFAULT 0,state TEXT NOT NULL DEFAULT 'pending',last_error TEXT,completed_at TEXT,payload TEXT NOT NULL,updated_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db, mode: "sqlite"}
	source := &SQLSchedulerSource{Store: store, OwnerID: "gateway-1", Lease: time.Minute}
	return db, source, time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
}

func TestSQLSchedulerSourceCountsHealthRetryAttempts(t *testing.T) {
	db, source, now := schedulerTasksFixture(t)
	if _, err := db.Exec(`INSERT INTO model_check_scheduler_tasks(id,kind,due_at,payload,updated_at) VALUES ('health:run-1','health_sync_retry','2026-09-25T11:00:00Z','{"runId":"run-1"}','2026-09-25T11:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	tasks, err := source.Claim(context.Background(), SchedulerHealthRetry, now, 10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
	if err := source.Fail(context.Background(), tasks[0], errors.New("probe still failing")); err != nil {
		t.Fatal(err)
	}
	var state, payload, lastError, dueAtText string
	if err := db.QueryRow(`SELECT state,payload,due_at,COALESCE(last_error,'') FROM model_check_scheduler_tasks WHERE id='health:run-1'`).Scan(&state, &payload, &dueAtText, &lastError); err != nil {
		t.Fatal(err)
	}
	dueAt, err := time.Parse(time.RFC3339Nano, dueAtText)
	if err != nil {
		t.Fatal(err)
	}
	if state != "failed" {
		t.Fatalf("state=%q", state)
	}
	if payload != `{"runId":"run-1","attempts":1}` {
		t.Fatalf("payload=%s, want attempt counter", payload)
	}
	if !strings.Contains(lastError, "probe still failing") {
		t.Fatalf("last_error=%q", lastError)
	}
	if dueAt.Before(now.Add(time.Minute)) || dueAt.After(now.Add(2*time.Minute)) {
		t.Fatalf("due_at=%s, want about one minute retry delay", dueAt)
	}
	// Still claimable after the retry delay: attempts are only exhausted at
	// the cap.
	retried, err := source.Claim(context.Background(), SchedulerHealthRetry, now.Add(2*time.Minute), 10)
	if err != nil || len(retried) != 1 {
		t.Fatalf("reclaim=%+v err=%v", retried, err)
	}
	if string(retried[0].Payload) != `{"runId":"run-1","attempts":1}` {
		t.Fatalf("claimed payload=%s, attempt counter must survive the round trip", retried[0].Payload)
	}
}

func TestSQLSchedulerSourceDeadLettersExhaustedHealthTask(t *testing.T) {
	db, source, now := schedulerTasksFixture(t)
	if _, err := db.Exec(`INSERT INTO model_check_scheduler_tasks(id,kind,due_at,payload,updated_at) VALUES ('health:run-1','health_sync_retry','2026-09-25T11:00:00Z','{"runId":"run-1","attempts":9}','2026-09-25T11:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	tasks, err := source.Claim(context.Background(), SchedulerHealthRetry, now, 10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
	if err := source.Fail(context.Background(), tasks[0], errors.New("permanent failure")); err != nil {
		t.Fatal(err)
	}
	var state, payload, lastError, dueAtText string
	if err := db.QueryRow(`SELECT state,payload,due_at,COALESCE(last_error,'') FROM model_check_scheduler_tasks WHERE id='health:run-1'`).Scan(&state, &payload, &dueAtText, &lastError); err != nil {
		t.Fatal(err)
	}
	dueAt, err := time.Parse(time.RFC3339Nano, dueAtText)
	if err != nil {
		t.Fatal(err)
	}
	if state != "failed" {
		t.Fatalf("state=%q, schema has no dead state; exhaustion is a never-due failed task", state)
	}
	if payload != `{"runId":"run-1","attempts":10}` {
		t.Fatalf("payload=%s", payload)
	}
	if !strings.HasPrefix(lastError, "health retry attempts exhausted after 10") || !strings.Contains(lastError, "permanent failure") {
		t.Fatalf("last_error=%q, want terminal exhaustion cause", lastError)
	}
	if deadline := now.Add(90 * 365 * 24 * time.Hour); !dueAt.After(deadline) {
		t.Fatalf("due_at=%s, want dead-letter horizon beyond %s", dueAt, deadline)
	}
	// Dead letters are never claimed again by any scan, including takeovers.
	dead, err := source.Claim(context.Background(), SchedulerHealthRetry, now.Add(2*time.Minute), 10)
	if err != nil || len(dead) != 0 {
		t.Fatalf("dead letter reclaimed=%+v err=%v", dead, err)
	}
}

func TestSQLSchedulerSourceLeavesNonHealthPayloadUntouchedOnFail(t *testing.T) {
	db, source, now := schedulerTasksFixture(t)
	if _, err := db.Exec(`INSERT INTO model_check_scheduler_tasks(id,kind,due_at,payload,updated_at) VALUES ('task-1','scheduled','2026-09-25T11:00:00Z','{"frozen":true}','2026-09-25T11:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	tasks, err := source.Claim(context.Background(), SchedulerScheduled, now, 10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
	if err := source.Fail(context.Background(), tasks[0], errors.New("failed")); err != nil {
		t.Fatal(err)
	}
	var payload string
	if err := db.QueryRow(`SELECT payload FROM model_check_scheduler_tasks WHERE id='task-1'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload != `{"frozen":true}` {
		t.Fatalf("payload=%s, non-health payloads must keep their frozen shape", payload)
	}
}

func TestListHealthSyncRetriesScanWindowTruncates(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/window.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY,status TEXT,account_id TEXT,system_account_id TEXT,provider_code TEXT,model TEXT,profile TEXT,level TEXT,score INTEGER,schedule_id TEXT,policy_snapshot_json TEXT,quality_decision_json TEXT,request_summary_json TEXT,finished_at TEXT,quality_health_sync_status TEXT,updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	// 40 invalid head rows sort before the single valid row. They pass the
	// SQL filters (durable completed failed-sync facts) but are skipped by the
	// Go-side projector parse: with limit=10 the scan window (4*limit) covers
	// exactly these rows, so the valid row is truncated by the documented
	// window semantics.
	for i := 0; i < 40; i++ {
		if _, err := db.Exec(`INSERT INTO model_check_runs VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			fmt.Sprintf("invalid-%02d", i), "completed", "acct-bad", "sys-bad", "openai", "gpt-5.6", "quick", "failure", 20, nil, "not-json", "{}", "{}", "2030-01-01T00:00:00Z", "failed", "2030-01-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO model_check_runs VALUES ('run-valid','completed','acct-1','sys-1','openai','gpt-5.6','quick','failure',20,NULL,` +
		`'{"revision":"policy-1","threshold":70,"action":"quality_isolate","recoveryIntervalMinutes":10}',` +
		`'{"evidenceFormed":true,"trustFormed":true}','{"configRevision":"3"}','2030-01-02T00:00:00Z','failed','2030-01-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db, mode: "sqlite", HealthStatHour: mustHealthStatHourFunc(t, "Asia/Shanghai")}
	truncated, err := store.ListHealthSyncRetries(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(truncated) != 0 {
		t.Fatalf("truncated=%+v, window must cap the scan", truncated)
	}
	// limit=11 widens the window to 44 rows, so the valid row is discovered.
	discovered, err := store.ListHealthSyncRetries(context.Background(), 11)
	if err != nil {
		t.Fatal(err)
	}
	if len(discovered) != 1 || discovered[0].RunID != "run-valid" {
		t.Fatalf("discovered=%+v, valid row must be discoverable within the window", discovered)
	}
}

func TestTryStartManualRunExcludesDifferentActorsOnSameTarget(t *testing.T) {
	registry := modelcheckactive.NewRegistry()
	actorA := ManagementScope{ActorSystemAccountID: "sys-a"}
	actorB := ManagementScope{ActorSystemAccountID: "sys-b"}
	request := RunRequest{TargetType: "account", TargetID: "acct-1"}
	summary := modelcheckactive.Summary{TargetID: "acct-1", StartedAt: time.Now().UTC()}
	_, releaseA, acquiredA, _ := tryStartManualRun(context.Background(), registry, actorA, request, summary)
	if !acquiredA {
		t.Fatal("first manual run must acquire both slots")
	}
	// A different actor on the same target must be rejected by the target
	// slot, and its own actor slot must be rolled back so it stays reusable.
	_, releaseB, acquiredB, current := tryStartManualRun(context.Background(), registry, actorB, request, summary)
	if acquiredB {
		releaseB()
		t.Fatal("different actor on the same target must conflict")
	}
	if current.TargetID != "acct-1" {
		t.Fatalf("conflict summary=%+v", current)
	}
	if _, ok := registry.Get("system-account:sys-b"); ok {
		t.Fatal("failed attempt must release the conflicting actor slot")
	}
	// The actor slot keeps the existing /run/active and /run/stop contract.
	if _, ok := registry.Get("system-account:sys-a"); !ok {
		t.Fatal("actor slot must stay visible for the active/stop contract")
	}
	// Same actor on a different target still serializes on the actor slot:
	// unchanged historical behavior.
	if _, _, acquiredC, _ := tryStartManualRun(context.Background(), registry, actorA, RunRequest{TargetType: "account", TargetID: "acct-2"}, summary); acquiredC {
		t.Fatal("same actor second target must conflict on the actor slot")
	}
	// The target slot is global: it exists under the target identity only.
	if _, ok := registry.Get(manualRunActiveKey(actorA, request)); !ok {
		t.Fatal("target slot must be registered under the global target identity")
	}
	releaseA()
	if _, ok := registry.Get("system-account:sys-a"); ok {
		t.Fatal("release must free the actor slot")
	}
	if _, ok := registry.Get(manualRunActiveKey(actorA, request)); ok {
		t.Fatal("release must free the target slot")
	}
	// After release the same actor/target combination is runnable again.
	if _, releaseA2, acquiredA2, _ := tryStartManualRun(context.Background(), registry, actorA, request, summary); !acquiredA2 {
		t.Fatal("slots must be reusable after release")
	} else {
		releaseA2()
	}
}
