package modelcheckowner

// D1-D6 run-now / 调度容量 / 立即复测恢复 / 计划列表执行态 / 构建失败记录
// 契约测试（问题-0257 收口批次）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckactive"
	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// 共享 fixture
// ---------------------------------------------------------------------------

// wbRunNowBusinessFixture 建出 run-now 全链所需的 Business 表（含处罚行的
// before/fallback 字段）。
func wbRunNowBusinessFixture(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/run-now-business.db?mode=rwc&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,system_account_id TEXT,name TEXT,provider_code TEXT,config_revision INTEGER,dispatch_revision INTEGER,deleted_at TEXT,authorization_instance_authorization_id TEXT,status TEXT,schedulable INTEGER,fallback_enabled INTEGER,super_priority_enabled INTEGER,last_error_code TEXT,last_error_message TEXT,availability_schedule_json TEXT,updated_at TEXT)`,
		`CREATE TABLE model_quality_schedules (id TEXT PRIMARY KEY,revision INTEGER,system_account_id TEXT,account_id TEXT,model TEXT,interval_minutes INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,enabled INTEGER,next_run_at TEXT,lease_owner TEXT,lease_until TEXT,last_run_id TEXT,last_run_at TEXT,last_run_status TEXT,created_at TEXT,updated_at TEXT,custom_question_ids TEXT)`,
		`CREATE TABLE account_quality_enforcements (account_id TEXT PRIMARY KEY,system_account_id TEXT,enforcement_id TEXT,generation INTEGER,state TEXT,action TEXT,trigger_run_id TEXT,config_source TEXT,config_source_id TEXT,policy_revision INTEGER,profile TEXT,penalty_threshold INTEGER,recovery_interval_minutes INTEGER,recovery_model TEXT,account_config_revision INTEGER,before_status TEXT,after_status TEXT,fallback_was_enabled INTEGER,super_priority_was_enabled INTEGER,started_at TEXT,recovery_due_at TEXT,recovery_lease_owner TEXT,recovery_lease_until TEXT,last_recovery_run_id TEXT,cleared_at TEXT,created_at TEXT,updated_at TEXT)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func wbInsertRunNowAccount(t *testing.T, db *sql.DB, id, systemID, status string, configRevision int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO accounts (id,system_account_id,provider_code,config_revision,dispatch_revision,deleted_at,authorization_instance_authorization_id,status,schedulable,fallback_enabled,super_priority_enabled) VALUES (?,?,'openai',?,7,NULL,NULL,?,1,0,0)`, id, systemID, configRevision, status); err != nil {
		t.Fatal(err)
	}
}

func wbInsertRunNowSchedule(t *testing.T, db *sql.DB, id, systemID, accountID string, enabled int, intervalMinutes int, nextRunAt, leaseUntil string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO model_quality_schedules (id,revision,system_account_id,account_id,model,interval_minutes,profile,penalty_threshold,penalty_action,recovery_interval_minutes,enabled,next_run_at,lease_owner,lease_until,last_run_id,last_run_at,last_run_status,created_at,updated_at,custom_question_ids) VALUES (?,1,?,?,'gpt-5.6-sol',?,'quick',70,'fallback',10,?,?,NULL,?,NULL,NULL,NULL,'2026-10-01T00:00:00Z','','[]')`, id, systemID, accountID, intervalMinutes, enabled, nextRunAt, leaseUntil); err != nil {
		t.Fatal(err)
	}
}

// wbRunNowJ3bFixture 建出 CreateFailedRun / MergeRunQualityDecision /
// RunScoresByIDs 需要的 J3b 表（列面与 requiredColumns 契约对齐）。
func wbRunNowJ3bFixture(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/run-now-j3b.db?mode=rwc&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range []string{
		`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY,system_account_id TEXT NOT NULL,actor_system_account_id TEXT NOT NULL,provider_code TEXT NOT NULL,target_type TEXT NOT NULL,target_id TEXT NOT NULL,target_name TEXT,target_owner_system_account_id TEXT,account_id TEXT,group_id TEXT,api_key_id TEXT,model TEXT NOT NULL,profile TEXT NOT NULL,trigger_kind TEXT NOT NULL,schedule_id TEXT,trusted_comparison_enabled INTEGER NOT NULL DEFAULT 0,trusted_comparison_available INTEGER NOT NULL DEFAULT 0,status TEXT NOT NULL,level TEXT NOT NULL,score INTEGER NOT NULL,max_score INTEGER NOT NULL,message TEXT NOT NULL,request_summary_json TEXT NOT NULL,result_summary_json TEXT NOT NULL,policy_snapshot_json TEXT NOT NULL,quality_decision_json TEXT NOT NULL,probe_set_version TEXT NOT NULL,started_at TEXT NOT NULL,trace_id TEXT,quality_health_sync_status TEXT,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,finished_at TEXT,duration_ms INTEGER,error_code TEXT,error_message TEXT)`,
		`CREATE TABLE model_check_items (id TEXT PRIMARY KEY,run_id TEXT NOT NULL,item_key TEXT NOT NULL,item_type TEXT NOT NULL,status TEXT NOT NULL,score INTEGER NOT NULL,max_score INTEGER NOT NULL,duration_ms INTEGER,trace_id TEXT,evidence_summary_json TEXT NOT NULL,error_code TEXT,error_message TEXT,created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`,
		`CREATE TABLE model_check_scheduler_tasks (id TEXT PRIMARY KEY,kind TEXT,due_at TEXT,claim_owner TEXT,claim_until TEXT,fence_token INTEGER,state TEXT,last_error TEXT,completed_at TEXT,payload TEXT,updated_at TEXT)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	return &Store{db: db, mode: "sqlite"}
}

// fakeRunNowRunner 记录收到的请求并以预设结果返回；OnStarted 被模拟为
// Runtime 在 CreateRun 后立即回调。
type fakeRunNowRunner struct {
	mu         sync.Mutex
	requests   []RunRequest
	resultData any
	runErr     error
	runID      string
	onStarted  bool
	block      chan struct{}
}

func (r *fakeRunNowRunner) Run(_ context.Context, request RunRequest) (RunResult, error) {
	if r.block != nil {
		<-r.block
	}
	r.mu.Lock()
	r.requests = append(r.requests, request)
	r.mu.Unlock()
	if request.OnStarted != nil && r.onStarted {
		request.OnStarted(r.runID)
	}
	if r.runErr != nil {
		return RunResult{}, r.runErr
	}
	return RunResult{RunID: r.runID, Status: string(RunCompleted), Data: r.resultData}, nil
}

func wbNewRunNowService(t *testing.T, db *sql.DB, store *Store, runner SchedulerRunner) (*ScheduleRunNowService, *modelcheckactive.Registry) {
	t.Helper()
	registry := modelcheckactive.NewRegistry()
	sourceStore := schedulerStoreFixture(t)
	t.Cleanup(func() { _ = sourceStore.db.Close() })
	service := &ScheduleRunNowService{
		Source:  &BusinessSchedulerSource{Business: db, Store: sourceStore, OwnerID: "gateway-1"},
		Store:   store,
		Runtime: runner,
		Active:  registry,
		Build: func(_ context.Context, snapshot ScheduleRunNowSnapshot) (RunRequest, error) {
			return RunRequest{SystemAccountID: snapshot.SystemAccountID, ActorSystemAccountID: snapshot.SystemAccountID, TargetType: "account", TargetID: snapshot.AccountID, Model: snapshot.Model, Profile: snapshot.Profile, ProviderCode: snapshot.ProviderCode, ConfigRevision: strconv.Itoa(snapshot.ConfigRevision), DispatchRevision: int64(snapshot.DispatchRevision)}, nil
		},
		RunIDWait: 50 * time.Millisecond,
	}
	return service, registry
}

// wbWaitUntil 轮询直到条件满足或超时。
func wbWaitUntil(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(message)
}

// ---------------------------------------------------------------------------
// D1：调度容量领取
// ---------------------------------------------------------------------------

// claimLimitProbeSource 记录每轮每 kind Claim 收到的 limit（D1：领取上限
// = MaxConcurrency，Batch>0 时取 min(Batch, MaxConcurrency)）。
type claimLimitProbeSource struct {
	mu     sync.Mutex
	limits map[SchedulerKind]int
	kinds  int
	done   chan struct{}
}

func (s *claimLimitProbeSource) Claim(_ context.Context, kind SchedulerKind, _ time.Time, limit int) ([]ScheduleTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.limits == nil {
		s.limits = map[SchedulerKind]int{}
	}
	if _, seen := s.limits[kind]; !seen {
		s.limits[kind] = limit
		s.kinds++
		if s.kinds == 3 {
			close(s.done)
		}
	}
	return nil, nil
}

func TestSchedulerClaimLimitEqualsConcurrencyCapacity(t *testing.T) {
	for _, tc := range []struct {
		name           string
		batch          int
		maxConcurrency int
		want           int
	}{
		{name: "default cap", batch: 0, maxConcurrency: 0, want: 4},
		{name: "batch below cap", batch: 2, maxConcurrency: 0, want: 2},
		{name: "batch above cap", batch: 500, maxConcurrency: 0, want: 4},
		{name: "custom cap", batch: 0, maxConcurrency: 3, want: 3},
		{name: "custom cap with smaller batch", batch: 1, maxConcurrency: 8, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &claimLimitProbeSource{done: make(chan struct{})}
			scheduler := &Scheduler{Source: source, Executor: wbNoopSchedulerExecutor{}, Interval: time.Hour, Batch: tc.batch, MaxConcurrency: tc.maxConcurrency}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runDone := make(chan error, 1)
			go func() { runDone <- scheduler.Run(ctx) }()
			select {
			case <-source.done:
			case <-time.After(2 * time.Second):
				t.Fatal("scheduler did not claim all three kinds")
			}
			cancel()
			if err := <-runDone; err != context.Canceled {
				t.Fatalf("scheduler err=%v", err)
			}
			source.mu.Lock()
			defer source.mu.Unlock()
			if len(source.limits) != 3 {
				t.Fatalf("claimed kinds=%d, want 3", len(source.limits))
			}
			for kind, limit := range source.limits {
				if limit != tc.want {
					t.Fatalf("kind %s claim limit=%d, want %d", kind, limit, tc.want)
				}
			}
		})
	}
}

type wbNoopSchedulerExecutor struct{}

func (wbNoopSchedulerExecutor) Execute(context.Context, ScheduleTask) error { return nil }

// 一次 10 条到期计划按容量领取只领 4 条，且租约只落在被领取的 4 条上
// （租约自领取起算，D1）。
func TestBusinessSchedulerClaimsOnlyConcurrencyCapacity(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,provider_code TEXT,config_revision INTEGER,dispatch_revision INTEGER NOT NULL DEFAULT 1,deleted_at TEXT,authorization_instance_authorization_id TEXT,authorization_instance_source_account_id TEXT,status TEXT)`,
		`CREATE TABLE model_quality_schedules (id TEXT PRIMARY KEY,revision INTEGER,system_account_id TEXT,account_id TEXT,model TEXT,interval_minutes INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,enabled INTEGER,next_run_at TEXT,lease_owner TEXT,lease_until TEXT,last_run_id TEXT,last_run_at TEXT,last_run_status TEXT,updated_at TEXT,custom_question_ids TEXT)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		accountID := "acct-" + strconv.Itoa(i)
		if _, err := db.Exec(`INSERT INTO accounts (id,provider_code,config_revision,deleted_at,authorization_instance_authorization_id,status) VALUES (?,'openai',4,NULL,NULL,'active')`, accountID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO model_quality_schedules (id,revision,system_account_id,account_id,model,interval_minutes,profile,penalty_threshold,penalty_action,recovery_interval_minutes,enabled,next_run_at,custom_question_ids) VALUES (?,1,'sys',?,'gpt-5.6-sol',60,'quick',70,'fallback',15,1,?,'[]')`, "sch-"+strconv.Itoa(i), accountID, now.Add(-time.Minute).Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	store := schedulerStoreFixture(t)
	defer store.db.Close()
	source := &BusinessSchedulerSource{Business: db, Store: store, OwnerID: "gateway-1"}
	tasks, err := source.Claim(context.Background(), SchedulerScheduled, now, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 4 {
		t.Fatalf("claimed=%d tasks, want capacity 4", len(tasks))
	}
	var leased int
	if err := db.QueryRow(`SELECT COUNT(*) FROM model_quality_schedules WHERE lease_owner='gateway-1'`).Scan(&leased); err != nil {
		t.Fatal(err)
	}
	if leased != 4 {
		t.Fatalf("leased rows=%d, want 4 (lease starts at claim time)", leased)
	}
	for _, task := range tasks {
		var until string
		scheduleID := strings.TrimSuffix(strings.TrimPrefix(task.ID, "schedule:"), ":1")
		if err := db.QueryRow(`SELECT lease_until FROM model_quality_schedules WHERE id=?`, scheduleID).Scan(&until); err != nil {
			t.Fatal(err)
		}
		if parsed, parseErr := time.Parse(time.RFC3339Nano, until); parseErr != nil || !parsed.Equal(now.Add(6*time.Minute)) {
			t.Fatalf("lease_until=%s must equal claim time + default lease", until)
		}
	}
}

// ---------------------------------------------------------------------------
// D2：interval=1 全链
// ---------------------------------------------------------------------------

// CompleteScheduled 接受 interval=1 的完成回写（下限收敛到 1）。
func TestBusinessSchedulerCompletesIntervalOneSchedule(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,provider_code TEXT,config_revision INTEGER,dispatch_revision INTEGER NOT NULL DEFAULT 1,deleted_at TEXT,authorization_instance_authorization_id TEXT,authorization_instance_source_account_id TEXT,status TEXT)`,
		`CREATE TABLE model_quality_schedules (id TEXT PRIMARY KEY,revision INTEGER,system_account_id TEXT,account_id TEXT,model TEXT,interval_minutes INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,enabled INTEGER,next_run_at TEXT,lease_owner TEXT,lease_until TEXT,last_run_id TEXT,last_run_at TEXT,last_run_status TEXT,updated_at TEXT,custom_question_ids TEXT)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO accounts (id,provider_code,config_revision,deleted_at,authorization_instance_authorization_id,status) VALUES ('acct','openai',4,NULL,NULL,'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_quality_schedules (id,revision,system_account_id,account_id,model,interval_minutes,profile,penalty_threshold,penalty_action,recovery_interval_minutes,enabled,next_run_at,custom_question_ids) VALUES ('sch',3,'sys','acct','gpt-5.6-sol',1,'quick',70,'fallback',15,1,?,'[]')`, now.Add(-time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE model_quality_schedules SET lease_owner='gateway-1',lease_until=? WHERE id='sch'`, now.Add(6*time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	store := schedulerStoreFixture(t)
	defer store.db.Close()
	source := &BusinessSchedulerSource{Business: db, Store: store, OwnerID: "gateway-1"}
	payload := ScheduledPayload{SystemAccountID: "sys", ActorSystemAccountID: "sys", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", ProviderCode: "openai", Threshold: 70, PenaltyAction: "fallback", ConfigRevision: "4", DispatchRevision: 1, SourceConfigRevision: "4", SourceDispatchRevision: 1, PolicyRevision: "3", ProbeSetVersion: probeSetForProfile("quick"), IdentityKey: "sys:acct:gpt-5.6-sol", ScheduleID: "sch", OwnerID: "gateway-1", ScheduleRevision: 3, IntervalMinutes: 1}
	if err := source.CompleteScheduled(context.Background(), payload, RunResult{RunID: "run-1", Status: string(RunCompleted)}); err != nil {
		t.Fatalf("interval=1 completion must be legal: %v", err)
	}
	var lastStatus, nextRunAt string
	if err := db.QueryRow(`SELECT last_run_status,next_run_at FROM model_quality_schedules WHERE id='sch'`).Scan(&lastStatus, &nextRunAt); err != nil {
		t.Fatal(err)
	}
	if lastStatus != "completed" {
		t.Fatalf("last_run_status=%q", lastStatus)
	}
	if parsed, parseErr := time.Parse(time.RFC3339Nano, nextRunAt); parseErr != nil || !parsed.After(time.Now().UTC()) {
		t.Fatalf("next_run_at=%s must advance beyond completion time", nextRunAt)
	}
}

// ---------------------------------------------------------------------------
// D3：run-now 源方法 + 服务受理
// ---------------------------------------------------------------------------

func TestBusinessSchedulerRunNowSourceContract(t *testing.T) {
	db := wbRunNowBusinessFixture(t)
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	wbInsertRunNowAccount(t, db, "acct", "sys", "active", 4)
	wbInsertRunNowAccount(t, db, "acct-other", "other", "active", 4)
	wbInsertRunNowSchedule(t, db, "sch", "sys", "acct", 1, 30, now.Add(-time.Minute).Format(time.RFC3339Nano), "")
	wbInsertRunNowSchedule(t, db, "sch-paused", "sys", "acct", 0, 45, now.Add(-time.Minute).Format(time.RFC3339Nano), "")
	wbInsertRunNowSchedule(t, db, "sch-other", "other", "acct-other", 1, 60, now.Format(time.RFC3339Nano), "")
	store := schedulerStoreFixture(t)
	defer store.db.Close()
	source := &BusinessSchedulerSource{Business: db, Store: store, OwnerID: "gateway-1"}
	snapshot, found, err := source.ScheduleForRunNow(context.Background(), "sys", "sch")
	if err != nil || !found {
		t.Fatalf("snapshot found=%t err=%v", found, err)
	}
	if snapshot.ScheduleID != "sch" || snapshot.SystemAccountID != "sys" || snapshot.AccountID != "acct" || snapshot.Model != "gpt-5.6-sol" || snapshot.Profile != "quick" || snapshot.Threshold != 70 || snapshot.PenaltyAction != "fallback" || snapshot.IntervalMinutes != 30 || snapshot.RecoveryIntervalMinutes != 10 || snapshot.Revision != 1 || !snapshot.Enabled || snapshot.ConfigRevision != 4 || snapshot.DispatchRevision != 7 || snapshot.ProviderCode != "openai" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if _, found, err := source.ScheduleForRunNow(context.Background(), "sys", "sch-other"); err != nil || found {
		t.Fatalf("cross-tenant schedule must be not_found: found=%t err=%v", found, err)
	}
	if _, found, err := source.ScheduleForRunNow(context.Background(), "sys", "sch-missing"); err != nil || found {
		t.Fatalf("missing schedule must be not_found: found=%t err=%v", found, err)
	}
	active, err := source.AccountRecoveryLeaseActive(context.Background(), "acct", now)
	if err != nil || active {
		t.Fatalf("no recovery lease expected: active=%t err=%v", active, err)
	}
	if _, err := db.Exec(`INSERT INTO account_quality_enforcements (account_id,system_account_id,enforcement_id,generation,state,action,recovery_lease_until,updated_at) VALUES ('acct','sys','enf',1,'active','quality_isolate',?,'')`, now.Add(time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if active, err = source.AccountRecoveryLeaseActive(context.Background(), "acct", now); err != nil || !active {
		t.Fatalf("active recovery lease expected: active=%t err=%v", active, err)
	}
	completedAt := now.Add(2 * time.Minute)
	if err := source.CompleteRunNow(context.Background(), "sch", 1, true, 30, "run-9", "completed", completedAt); err != nil {
		t.Fatal(err)
	}
	var next string
	if err := db.QueryRow(`SELECT next_run_at FROM model_quality_schedules WHERE id='sch'`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if want := completedAt.Add(30 * time.Minute).Format(time.RFC3339Nano); next != want {
		t.Fatalf("enabled schedule next_run_at=%s, want %s", next, want)
	}
	pausedNext := now.Add(-time.Minute).Format(time.RFC3339Nano)
	if err := source.CompleteRunNow(context.Background(), "sch-paused", 1, false, 45, "run-9", "failed", completedAt); err != nil {
		t.Fatal(err)
	}
	var pausedStatus string
	if err := db.QueryRow(`SELECT next_run_at,last_run_status FROM model_quality_schedules WHERE id='sch-paused'`).Scan(&next, &pausedStatus); err != nil {
		t.Fatal(err)
	}
	if next != pausedNext {
		t.Fatalf("paused schedule next_run_at=%s must stay %s", next, pausedNext)
	}
	if pausedStatus != "failed" {
		t.Fatalf("paused schedule last_run_status=%q", pausedStatus)
	}
	if err := source.CompleteRunNow(context.Background(), "sch", 99, true, 30, "run-9", "completed", completedAt); !errors.Is(err, ErrRunNowCompletionStale) {
		t.Fatalf("stale revision must be reported: err=%v", err)
	}
}

// 单条受理全链：受理成功 + schedule_now 请求字段 + 完成回写（启用计划
// next_run_at=完成时间+间隔）+ runId 早期可取 + 幂等回放。
func TestScheduleRunNowServiceSingleAcceptanceAndCompletion(t *testing.T) {
	db := wbRunNowBusinessFixture(t)
	store := wbRunNowJ3bFixture(t)
	runner := &fakeRunNowRunner{runID: "run-now-1", onStarted: true}
	service, _ := wbNewRunNowService(t, db, store, runner)
	wbInsertRunNowAccount(t, db, "acct", "sys", "active", 4)
	wbInsertRunNowSchedule(t, db, "sch", "sys", "acct", 1, 30, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), "")
	before := time.Now().UTC()
	outcome, err := service.RunNow(context.Background(), "sys", "sch", nil, "req-1")
	if err != nil || !outcome.Accepted || outcome.Status != ScheduleRunNowStarted {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	if outcome.RunID == nil || *outcome.RunID != "run-now-1" {
		t.Fatalf("runId must be captured via OnStarted: %+v", outcome)
	}
	wbWaitUntil(t, 2*time.Second, func() bool {
		return runner.Len() == 1
	}, "run-now must execute asynchronously")
	request := runner.At(0)
	if request.TriggerKind != TriggerKindScheduleNow || request.ScheduleID != "sch" || request.TargetID != "acct" || request.SystemAccountID != "sys" || request.ActorSystemAccountID != "sys" || request.Model != "gpt-5.6-sol" || request.Profile != "quick" || request.Threshold != 70 || request.PenaltyAction != "fallback" || request.PolicyRevision != "1" {
		t.Fatalf("schedule_now request=%+v", request)
	}
	wbWaitUntil(t, 2*time.Second, func() bool {
		var status, runID string
		if err := db.QueryRow(`SELECT last_run_status,last_run_id FROM model_quality_schedules WHERE id='sch'`).Scan(&status, &runID); err != nil {
			return false
		}
		return status == "completed" && runID == "run-now-1"
	}, "completion write-back must record the run")
	var nextRunAt string
	if err := db.QueryRow(`SELECT next_run_at FROM model_quality_schedules WHERE id='sch'`).Scan(&nextRunAt); err != nil {
		t.Fatal(err)
	}
	parsed, parseErr := time.Parse(time.RFC3339Nano, nextRunAt)
	if parseErr != nil || !parsed.After(before.Add(29*time.Minute)) || parsed.Before(before.Add(30*time.Minute)) {
		t.Fatalf("enabled schedule next_run_at=%s must be completion + interval", nextRunAt)
	}
	// 幂等：同 (scheduleId, requestId) 10 分钟内返回上次受理结果。
	replay, err := service.RunNow(context.Background(), "sys", "sch", nil, "req-1")
	if err != nil || !replay.Accepted || replay.RunID == nil || *replay.RunID != "run-now-1" {
		t.Fatalf("idempotent replay outcome=%+v err=%v", replay, err)
	}
	if runner.Len() != 1 {
		t.Fatalf("idempotent replay must not execute again: executed=%d", runner.Len())
	}
}

// 受理互斥与错误态：not_found / stale_revision / 计划租约在执行 / 恢复租约
// 在执行 / 手动检测在跑 / 同计划在途。
func TestScheduleRunNowServiceAcceptanceConflicts(t *testing.T) {
	now := time.Now().UTC()
	t.Run("not found", func(t *testing.T) {
		db := wbRunNowBusinessFixture(t)
		service, _ := wbNewRunNowService(t, db, wbRunNowJ3bFixture(t), &fakeRunNowRunner{runID: "run-x", onStarted: true})
		outcome, err := service.RunNow(context.Background(), "sys", "sch-missing", nil, "")
		if err != nil || outcome.Status != ScheduleRunNowNotFound || outcome.Accepted {
			t.Fatalf("outcome=%+v err=%v", outcome, err)
		}
	})
	t.Run("stale revision", func(t *testing.T) {
		db := wbRunNowBusinessFixture(t)
		service, _ := wbNewRunNowService(t, db, wbRunNowJ3bFixture(t), &fakeRunNowRunner{runID: "run-x", onStarted: true})
		wbInsertRunNowAccount(t, db, "acct", "sys", "active", 4)
		wbInsertRunNowSchedule(t, db, "sch", "sys", "acct", 1, 30, now.Format(time.RFC3339Nano), "")
		stale := 99
		outcome, err := service.RunNow(context.Background(), "sys", "sch", &stale, "")
		if err != nil || outcome.Status != ScheduleRunNowStaleRevision || outcome.Accepted {
			t.Fatalf("outcome=%+v err=%v", outcome, err)
		}
	})
	t.Run("schedule lease running", func(t *testing.T) {
		db := wbRunNowBusinessFixture(t)
		service, _ := wbNewRunNowService(t, db, wbRunNowJ3bFixture(t), &fakeRunNowRunner{runID: "run-x", onStarted: true})
		wbInsertRunNowAccount(t, db, "acct", "sys", "active", 4)
		wbInsertRunNowSchedule(t, db, "sch", "sys", "acct", 1, 30, now.Format(time.RFC3339Nano), now.Add(time.Minute).Format(time.RFC3339Nano))
		outcome, err := service.RunNow(context.Background(), "sys", "sch", nil, "")
		if err != nil || outcome.Status != ScheduleRunNowAlreadyRunning || outcome.Accepted {
			t.Fatalf("outcome=%+v err=%v", outcome, err)
		}
	})
	t.Run("recovery lease running", func(t *testing.T) {
		db := wbRunNowBusinessFixture(t)
		service, _ := wbNewRunNowService(t, db, wbRunNowJ3bFixture(t), &fakeRunNowRunner{runID: "run-x", onStarted: true})
		wbInsertRunNowAccount(t, db, "acct", "sys", "quality_isolated", 4)
		wbInsertRunNowSchedule(t, db, "sch", "sys", "acct", 1, 30, now.Format(time.RFC3339Nano), "")
		if _, err := db.Exec(`INSERT INTO account_quality_enforcements (account_id,system_account_id,enforcement_id,generation,state,action,recovery_lease_until,updated_at) VALUES ('acct','sys','enf',1,'active','quality_isolate',?,'')`, now.Add(time.Minute).Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		outcome, err := service.RunNow(context.Background(), "sys", "sch", nil, "")
		if err != nil || outcome.Status != ScheduleRunNowAlreadyRunning || outcome.Accepted {
			t.Fatalf("outcome=%+v err=%v", outcome, err)
		}
	})
	t.Run("manual run active", func(t *testing.T) {
		db := wbRunNowBusinessFixture(t)
		service, registry := wbNewRunNowService(t, db, wbRunNowJ3bFixture(t), &fakeRunNowRunner{runID: "run-x", onStarted: true})
		wbInsertRunNowAccount(t, db, "acct", "sys", "active", 4)
		wbInsertRunNowSchedule(t, db, "sch", "sys", "acct", 1, 30, now.Format(time.RFC3339Nano), "")
		manualCtx, manualCancel := context.WithCancel(context.Background())
		defer manualCancel()
		if _, acquired, _ := registry.TryStart(manualCtx, scheduleRunNowTargetKey("acct"), modelcheckactive.Summary{TargetID: "acct"}); !acquired {
			t.Fatal("manual target slot must be acquirable")
		}
		defer registry.Stop(scheduleRunNowTargetKey("acct"))
		outcome, err := service.RunNow(context.Background(), "sys", "sch", nil, "")
		if err != nil || outcome.Status != ScheduleRunNowAlreadyRunning || outcome.Accepted || outcome.Active == nil {
			t.Fatalf("outcome=%+v err=%v", outcome, err)
		}
	})
	t.Run("same schedule in flight", func(t *testing.T) {
		db := wbRunNowBusinessFixture(t)
		block := make(chan struct{})
		runner := &fakeRunNowRunner{runID: "run-x", onStarted: true, block: block}
		service, _ := wbNewRunNowService(t, db, wbRunNowJ3bFixture(t), runner)
		wbInsertRunNowAccount(t, db, "acct", "sys", "active", 4)
		wbInsertRunNowSchedule(t, db, "sch", "sys", "acct", 1, 30, now.Format(time.RFC3339Nano), "")
		first, err := service.RunNow(context.Background(), "sys", "sch", nil, "")
		if err != nil || !first.Accepted {
			t.Fatalf("first outcome=%+v err=%v", first, err)
		}
		second, err := service.RunNow(context.Background(), "sys", "sch", nil, "")
		if err != nil || second.Status != ScheduleRunNowAlreadyRunning || second.Accepted {
			t.Fatalf("second outcome=%+v err=%v", second, err)
		}
		close(block)
		wbWaitUntil(t, 2*time.Second, func() bool {
			_, running := service.Active.Get(scheduleRunNowTargetKey("acct"))
			return !running
		}, "target slot must be released after completion")
		third, err := service.RunNow(context.Background(), "sys", "sch", nil, "")
		if err != nil || !third.Accepted {
			t.Fatalf("post-completion re-accept outcome=%+v err=%v", third, err)
		}
		wbWaitUntil(t, 2*time.Second, func() bool {
			var count int
			return db.QueryRow(`SELECT COUNT(*) FROM model_quality_schedules WHERE id='sch' AND last_run_id IS NOT NULL`).Scan(&count) == nil && count == 1 && runner.Len() == 2
		}, "third acceptance must finish before test end")
	})
	t.Run("paused schedule keeps next_run_at", func(t *testing.T) {
		db := wbRunNowBusinessFixture(t)
		store := wbRunNowJ3bFixture(t)
		runner := &fakeRunNowRunner{runID: "run-p", onStarted: true}
		service, _ := wbNewRunNowService(t, db, store, runner)
		wbInsertRunNowAccount(t, db, "acct", "sys", "active", 4)
		frozen := now.Add(3 * time.Hour).Format(time.RFC3339Nano)
		wbInsertRunNowSchedule(t, db, "sch-paused", "sys", "acct", 0, 45, frozen, "")
		outcome, err := service.RunNow(context.Background(), "sys", "sch-paused", nil, "")
		if err != nil || !outcome.Accepted {
			t.Fatalf("paused schedule run-now outcome=%+v err=%v", outcome, err)
		}
		wbWaitUntil(t, 2*time.Second, func() bool {
			var status, runID string
			if err := db.QueryRow(`SELECT last_run_status,last_run_id FROM model_quality_schedules WHERE id='sch-paused'`).Scan(&status, &runID); err != nil {
				return false
			}
			return status == "completed" && runID == "run-p"
		}, "paused schedule completion must still be written")
		var nextRunAt string
		if err := db.QueryRow(`SELECT next_run_at FROM model_quality_schedules WHERE id='sch-paused'`).Scan(&nextRunAt); err != nil {
			t.Fatal(err)
		}
		if nextRunAt != frozen {
			t.Fatalf("paused schedule next_run_at=%s must stay %s", nextRunAt, frozen)
		}
	})
}

// 批量受理：逐条独立，部分成功不影响其余条目；构建失败的条目落可查
// failed run 记录并回写 last_run_status=failed。
func TestScheduleRunNowServiceBatchPartialSuccess(t *testing.T) {
	db := wbRunNowBusinessFixture(t)
	store := wbRunNowJ3bFixture(t)
	runner := &fakeRunNowRunner{runID: "run-b", onStarted: true}
	service, _ := wbNewRunNowService(t, db, store, runner)
	wbInsertRunNowAccount(t, db, "acct", "sys", "active", 4)
	wbInsertRunNowSchedule(t, db, "sch-ok", "sys", "acct", 1, 30, time.Now().UTC().Format(time.RFC3339Nano), "")
	wbInsertRunNowSchedule(t, db, "sch-stale", "sys", "acct", 1, 30, time.Now().UTC().Format(time.RFC3339Nano), "")
	stale := 42
	outcomes, err := service.RunNowBatch(context.Background(), "sys", []ScheduleRunNowBatchCommand{
		{ScheduleID: "sch-ok"},
		{ScheduleID: "sch-missing"},
		{ScheduleID: "sch-stale", Revision: &stale},
		{ScheduleID: " "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 4 {
		t.Fatalf("outcomes=%d, want 4", len(outcomes))
	}
	if !outcomes[0].Accepted || outcomes[0].Status != ScheduleRunNowStarted {
		t.Fatalf("outcome[0]=%+v", outcomes[0])
	}
	if outcomes[1].Status != ScheduleRunNowNotFound || outcomes[1].Accepted {
		t.Fatalf("outcome[1]=%+v", outcomes[1])
	}
	if outcomes[2].Status != ScheduleRunNowStaleRevision || outcomes[2].Accepted {
		t.Fatalf("outcome[2]=%+v", outcomes[2])
	}
	if outcomes[3].Status != ScheduleRunNowInvalid || outcomes[3].Accepted {
		t.Fatalf("outcome[3]=%+v", outcomes[3])
	}
	wbWaitUntil(t, 2*time.Second, func() bool {
		var status string
		if err := db.QueryRow(`SELECT last_run_status FROM model_quality_schedules WHERE id='sch-ok'`).Scan(&status); err != nil {
			return false
		}
		return status == "completed"
	}, "accepted batch item must complete")
}

// 构建失败的 run-now：failed run 记录可查，last_run_status=failed。
func TestScheduleRunNowServiceBuildFailureRecordsFailedRun(t *testing.T) {
	db := wbRunNowBusinessFixture(t)
	store := wbRunNowJ3bFixture(t)
	runner := &fakeRunNowRunner{runID: "run-never", onStarted: true}
	service, _ := wbNewRunNowService(t, db, store, runner)
	service.Build = func(context.Context, ScheduleRunNowSnapshot) (RunRequest, error) {
		return RunRequest{}, errors.New("目标解析失败")
	}
	wbInsertRunNowAccount(t, db, "acct", "sys", "active", 4)
	wbInsertRunNowSchedule(t, db, "sch", "sys", "acct", 1, 30, time.Now().UTC().Format(time.RFC3339Nano), "")
	outcome, err := service.RunNow(context.Background(), "sys", "sch", nil, "")
	if err != nil || !outcome.Accepted {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	wbWaitUntil(t, 2*time.Second, func() bool {
		var status string
		if err := db.QueryRow(`SELECT last_run_status FROM model_quality_schedules WHERE id='sch'`).Scan(&status); err != nil {
			return false
		}
		return status == "failed"
	}, "build failure must complete as failed")
	var runID string
	if err := db.QueryRow(`SELECT last_run_id FROM model_quality_schedules WHERE id='sch'`).Scan(&runID); err != nil || runID == "" {
		t.Fatalf("last_run_id must point at the failed run record: %q err=%v", runID, err)
	}
	var status, triggerKind, errorCode string
	if err := store.db.QueryRow(`SELECT status,trigger_kind,error_code FROM model_check_runs WHERE id=?`, runID).Scan(&status, &triggerKind, &errorCode); err != nil {
		t.Fatalf("failed run must be queryable: %v", err)
	}
	if status != string(RunFailed) || triggerKind != TriggerKindScheduleNow || errorCode != "model_check_build_failed" {
		t.Fatalf("failed run status=%q trigger=%q error=%q", status, triggerKind, errorCode)
	}
}

func (r *fakeRunNowRunner) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

func (r *fakeRunNowRunner) At(i int) RunRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests[i]
}

// ---------------------------------------------------------------------------
// D4：立即复测恢复
// ---------------------------------------------------------------------------

type wbRestoreFixture struct {
	db      *sql.DB
	applier *BusinessRecoveryApplier
}

func wbNewRestoreFixture(t *testing.T) *wbRestoreFixture {
	db := wbRunNowBusinessFixture(t)
	applier, err := NewBusinessRecoveryApplier(db, false)
	if err != nil {
		t.Fatal(err)
	}
	return &wbRestoreFixture{db: db, applier: applier}
}

func (f *wbRestoreFixture) insertEnforcement(t *testing.T, accountID, state, action, source, sourceID, beforeStatus string, accountRevision int, fallbackWas, superWas int) {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO account_quality_enforcements (account_id,system_account_id,enforcement_id,generation,state,action,trigger_run_id,config_source,config_source_id,policy_revision,profile,penalty_threshold,recovery_interval_minutes,account_config_revision,before_status,after_status,fallback_was_enabled,super_priority_was_enabled,updated_at) VALUES (?,'sys','enf-1',1,?,?,'run-old',?,?,1,'quick',70,10,?,?,?,?,?,'')`, accountID, state, action, source, sourceID, accountRevision, beforeStatus, action, fallbackWas, superWas); err != nil {
		t.Fatal(err)
	}
}

// disable 处罚恢复：账户回滚到 before_status（空则 active），处罚行 cleared。
// 夹具手工把账户与处罚行构造为同 revision（=6），模拟的是 claimRecoveries
// 领取刷新后的状态；真实 Apply 产生的恒差 1 场景由
// TestScheduleRunNowRestoresRealApplyPenalty 端到端覆盖。
func TestRestoreSchedulePenaltiesDisable(t *testing.T) {
	f := wbNewRestoreFixture(t)
	wbInsertRunNowAccount(t, f.db, "acct", "sys", "disabled", 6)
	f.insertEnforcement(t, "acct", "active", "disable", "schedule", "sch", "active", 6, 0, 0)
	outcome, err := f.applier.RestoreSchedulePenalties(context.Background(), SchedulePenaltyRestoreInput{AccountID: "acct", SystemAccountID: "sys", ScheduleID: "sch", RunID: "run-new"})
	if err != nil || outcome.Restored != 1 || outcome.Skipped != 0 {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	var status string
	var schedulable int
	var revision int
	if err := f.db.QueryRow(`SELECT status,schedulable,config_revision FROM accounts WHERE id='acct'`).Scan(&status, &schedulable, &revision); err != nil {
		t.Fatal(err)
	}
	if status != "active" || schedulable != 1 || revision != 7 {
		t.Fatalf("account status=%q schedulable=%d revision=%d, want active/1/7", status, schedulable, revision)
	}
	var state string
	if err := f.db.QueryRow(`SELECT state FROM account_quality_enforcements WHERE account_id='acct'`).Scan(&state); err != nil || state != "cleared" {
		t.Fatalf("enforcement state=%q err=%v", state, err)
	}
}

// quality_isolate 处罚恢复：账户 quality_isolated -> active，处罚行 cleared。
// 夹具手工把账户与处罚行构造为同 revision（=6），模拟的是 claimRecoveries
// 领取刷新后的状态；真实 Apply 产生的恒差 1 场景由
// TestScheduleRunNowRestoresRealApplyPenalty 端到端覆盖。
func TestRestoreSchedulePenaltiesQualityIsolate(t *testing.T) {
	f := wbNewRestoreFixture(t)
	wbInsertRunNowAccount(t, f.db, "acct", "sys", "quality_isolated", 6)
	if _, err := f.db.Exec(`UPDATE accounts SET schedulable=0,last_error_code='model_quality_failed' WHERE id='acct'`); err != nil {
		t.Fatal(err)
	}
	f.insertEnforcement(t, "acct", "active", "quality_isolate", "schedule", "sch", "active", 6, 0, 0)
	outcome, err := f.applier.RestoreSchedulePenalties(context.Background(), SchedulePenaltyRestoreInput{AccountID: "acct", SystemAccountID: "sys", ScheduleID: "sch", RunID: "run-new"})
	if err != nil || outcome.Restored != 1 {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	var status string
	var lastError sql.NullString
	if err := f.db.QueryRow(`SELECT status,last_error_code FROM accounts WHERE id='acct'`).Scan(&status, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "active" || lastError.Valid {
		t.Fatalf("account status=%q lastError=%v, want active with cleared error", status, lastError)
	}
	var state string
	if err := f.db.QueryRow(`SELECT state FROM account_quality_enforcements WHERE account_id='acct'`).Scan(&state); err != nil || state != "cleared" {
		t.Fatalf("enforcement state=%q err=%v", state, err)
	}
}

// fallback 处罚恢复：降级标记按行内记录的前值恢复。
func TestRestoreSchedulePenaltiesFallback(t *testing.T) {
	f := wbNewRestoreFixture(t)
	if _, err := f.db.Exec(`INSERT INTO accounts (id,system_account_id,provider_code,config_revision,dispatch_revision,deleted_at,authorization_instance_authorization_id,status,schedulable,fallback_enabled,super_priority_enabled,last_error_code) VALUES ('acct','sys','openai',6,7,NULL,NULL,'active',1,1,0,'model_quality_failed')`); err != nil {
		t.Fatal(err)
	}
	f.insertEnforcement(t, "acct", "active", "fallback", "schedule", "sch", "", 6, 0, 1)
	outcome, err := f.applier.RestoreSchedulePenalties(context.Background(), SchedulePenaltyRestoreInput{AccountID: "acct", SystemAccountID: "sys", ScheduleID: "sch", RunID: "run-new"})
	if err != nil || outcome.Restored != 1 {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	var fallback, superPriority int
	if err := f.db.QueryRow(`SELECT fallback_enabled,super_priority_enabled FROM accounts WHERE id='acct'`).Scan(&fallback, &superPriority); err != nil {
		t.Fatal(err)
	}
	if fallback != 0 || superPriority != 1 {
		t.Fatalf("fallback=%d super=%d, want pre-penalty 0/1", fallback, superPriority)
	}
}

// manual 处罚不碰；纯 revision 漂移（账户仍处于本处罚的目标状态）按
// claimRecoveries 刷新语义以当前 revision 为 CAS 基准照常恢复；执行期
// 并发修改且状态不再匹配的 Skipped 负例由
// TestRestoreSchedulePenaltiesSkipsOnConcurrentAccountChange 覆盖。
func TestRestoreSchedulePenaltiesManualUntouchedAndCASMismatch(t *testing.T) {
	t.Run("manual source untouched", func(t *testing.T) {
		f := wbNewRestoreFixture(t)
		wbInsertRunNowAccount(t, f.db, "acct", "sys", "disabled", 6)
		f.insertEnforcement(t, "acct", "active", "disable", "manual", "", "active", 6, 0, 0)
		outcome, err := f.applier.RestoreSchedulePenalties(context.Background(), SchedulePenaltyRestoreInput{AccountID: "acct", SystemAccountID: "sys", ScheduleID: "sch", RunID: "run-new"})
		if err != nil || outcome.Restored != 0 || outcome.Skipped != 0 {
			t.Fatalf("outcome=%+v err=%v", outcome, err)
		}
		var status, state string
		if err := f.db.QueryRow(`SELECT status FROM accounts WHERE id='acct'`).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if err := f.db.QueryRow(`SELECT state FROM account_quality_enforcements WHERE account_id='acct'`).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if status != "disabled" || state != "active" {
			t.Fatalf("manual penalty must stay: account=%q enforcement=%q", status, state)
		}
	})
	t.Run("pure revision drift rebases to current revision", func(t *testing.T) {
		f := wbNewRestoreFixture(t)
		// 处罚行记录处罚前值 6，账户在执行窗口内被并发编辑到 9（纯
		// revision 漂移，disabled 状态仍由本计划处罚造成）：恢复以当前 9
		// 为 CAS 基准照常回滚（修复前此场景 100% 被 Skipped）。
		wbInsertRunNowAccount(t, f.db, "acct", "sys", "disabled", 9)
		f.insertEnforcement(t, "acct", "active", "disable", "schedule", "sch", "active", 6, 0, 0)
		outcome, err := f.applier.RestoreSchedulePenalties(context.Background(), SchedulePenaltyRestoreInput{AccountID: "acct", SystemAccountID: "sys", ScheduleID: "sch", RunID: "run-new"})
		if err != nil || outcome.Restored != 1 || outcome.Skipped != 0 {
			t.Fatalf("outcome=%+v err=%v", outcome, err)
		}
		var status string
		var revision int
		if err := f.db.QueryRow(`SELECT status,config_revision FROM accounts WHERE id='acct'`).Scan(&status, &revision); err != nil || status != "active" || revision != 10 {
			t.Fatalf("drifted account must restore on current revision: status=%q revision=%d err=%v", status, revision, err)
		}
		var state string
		var rowRevision int
		if err := f.db.QueryRow(`SELECT state,account_config_revision FROM account_quality_enforcements WHERE account_id='acct'`).Scan(&state, &rowRevision); err != nil || state != "cleared" || rowRevision != 9 {
			t.Fatalf("enforcement must clear with refreshed revision: state=%q revision=%d err=%v", state, rowRevision, err)
		}
	})
}

// 达标 run-now 全链恢复：score 达标 -> 恢复 + decision 回写；不达标不恢复。
func TestScheduleRunNowRestoreOnEligibleCompletion(t *testing.T) {
	newCase := func(t *testing.T, score int, wantRestored bool) {
		db := wbRunNowBusinessFixture(t)
		store := wbRunNowJ3bFixture(t)
		runner := &fakeRunNowRunner{runID: "run-r", onStarted: true, resultData: map[string]any{"score": score, "level": "success", "hardFailure": false, "evidenceFormed": true, "trustFormed": true}}
		service, _ := wbNewRunNowService(t, db, store, runner)
		applier, err := NewBusinessRecoveryApplier(db, false)
		if err != nil {
			t.Fatal(err)
		}
		service.Restore = applier
		wbInsertRunNowAccount(t, db, "acct", "sys", "disabled", 6)
		wbInsertRunNowSchedule(t, db, "sch", "sys", "acct", 1, 30, time.Now().UTC().Format(time.RFC3339Nano), "")
		// 手工把账户与处罚行构造为同 revision（=6），模拟 claimRecoveries
		// 领取刷新后的状态；真实 Apply 的恒差 1 场景由
		// TestScheduleRunNowRestoresRealApplyPenalty 端到端覆盖。
		if _, err := db.Exec(`INSERT INTO account_quality_enforcements (account_id,system_account_id,enforcement_id,generation,state,action,trigger_run_id,config_source,config_source_id,policy_revision,profile,penalty_threshold,recovery_interval_minutes,account_config_revision,before_status,after_status,fallback_was_enabled,super_priority_was_enabled,updated_at) VALUES ('acct','sys','enf-1',1,'active','disable','run-old','schedule','sch',1,'quick',70,10,6,'active','disabled',0,0,'')`); err != nil {
			t.Fatal(err)
		}
		// decision 回写目标 run 行。
		if _, err := store.db.Exec(`INSERT INTO model_check_runs (id,system_account_id,actor_system_account_id,provider_code,target_type,target_id,model,profile,trigger_kind,status,level,score,max_score,message,request_summary_json,result_summary_json,policy_snapshot_json,quality_decision_json,probe_set_version,started_at,created_at,updated_at) VALUES ('run-r','sys','sys','openai','account','acct','gpt-5.6-sol','quick','schedule_now','completed','success',100,100,'','{}','{}','{}','{"reasonCodes":["trust"]}','probe','2026-10-01T00:00:00Z','2026-10-01T00:00:00Z','2026-10-01T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		outcome, err := service.RunNow(context.Background(), "sys", "sch", nil, "")
		if err != nil || !outcome.Accepted {
			t.Fatalf("outcome=%+v err=%v", outcome, err)
		}
		wbWaitUntil(t, 2*time.Second, func() bool {
			var status string
			if err := db.QueryRow(`SELECT last_run_status FROM model_quality_schedules WHERE id='sch'`).Scan(&status); err != nil {
				return false
			}
			return status == "completed"
		}, "run-now must settle before assertions")
		var status string
		if err := db.QueryRow(`SELECT status FROM accounts WHERE id='acct'`).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if wantRestored && status != "active" {
			t.Fatalf("eligible run must restore account: %q", status)
		}
		if !wantRestored && status != "disabled" {
			t.Fatalf("ineligible run must not restore account: %q", status)
		}
		if wantRestored {
			var decision string
			if err := store.db.QueryRow(`SELECT quality_decision_json FROM model_check_runs WHERE id='run-r'`).Scan(&decision); err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal([]byte(decision), &fields); err != nil {
				t.Fatal(err)
			}
			codes, _ := fields["restoreReasonCodes"].([]any)
			if len(codes) != 1 || codes[0] != "schedule_now_restore_applied" {
				t.Fatalf("restore decision=%v", fields)
			}
			if _, ok := fields["reasonCodes"]; !ok {
				t.Fatal("finalize reasonCodes must be preserved by restore patch")
			}
		}
	}
	t.Run("eligible restores", func(t *testing.T) { newCase(t, 70, true) })
	t.Run("ineligible keeps penalty", func(t *testing.T) { newCase(t, 69, false) })
}

// 真实 Apply -> run-now 达标 -> 恢复的全链（问题-0257 终审 blocker 回归）：
// 处罚事务把账户 revision+1 而处罚行记录处罚前值（恒差 1），恢复必须以
// 锁定账户行读到的当前 revision 为 CAS 基准，"处罚后马上立即复测"这一
// 最常见场景不允许 100% 被 Skipped。
func TestScheduleRunNowRestoresRealApplyPenalty(t *testing.T) {
	db := wbRunNowBusinessFixture(t)
	// Apply 的配置校验夹具（参照 businessEnforcementTestDDL；带 ScheduleID
	// 时读取 schedule 行，全局策略行仅作契约面陪衬）。
	if _, err := db.Exec(`CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_quality_policies VALUES ('sys',1,'quick',70,'quality_isolate',10)`); err != nil {
		t.Fatal(err)
	}
	wbInsertRunNowAccount(t, db, "acct", "sys", "active", 4)
	if _, err := db.Exec(`INSERT INTO model_quality_schedules (id,revision,system_account_id,account_id,model,interval_minutes,profile,penalty_threshold,penalty_action,recovery_interval_minutes,enabled,next_run_at,lease_owner,lease_until,last_run_id,last_run_at,last_run_status,created_at,updated_at,custom_question_ids) VALUES ('sch',1,'sys','acct','gpt-5.6-sol',30,'quick',70,'quality_isolate',10,1,'2026-10-01T00:00:00Z',NULL,NULL,NULL,NULL,NULL,'2026-10-01T00:00:00Z','','[]')`); err != nil {
		t.Fatal(err)
	}
	enforcer, err := NewBusinessEnforcementApplier(db, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enforcer.Apply(context.Background(), QualityEnforcement{AccountID: "acct", SystemAccountID: "sys", RunID: "run-penalty", ProviderCode: "openai", Model: "gpt-5.6-sol", PolicyRevision: "1", AccountConfigRevision: "4", ScheduleID: "sch", Action: "quality_isolate", Score: 20, Threshold: 70, RecoveryIntervalMinutes: 10, Profile: "quick", OccurredAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatalf("real Apply must create the penalty: %v", err)
	}
	var status string
	var revision int
	if err := db.QueryRow(`SELECT status,config_revision FROM accounts WHERE id='acct'`).Scan(&status, &revision); err != nil || status != "quality_isolated" || revision != 5 {
		t.Fatalf("after Apply account status=%q revision=%d err=%v, want quality_isolated/5", status, revision, err)
	}
	var rowState, rowSource, rowSourceID string
	var rowRevision int
	if err := db.QueryRow(`SELECT state,config_source,config_source_id,account_config_revision FROM account_quality_enforcements WHERE account_id='acct'`).Scan(&rowState, &rowSource, &rowSourceID, &rowRevision); err != nil || rowState != "active" || rowSource != "schedule" || rowSourceID != "sch" || rowRevision != 4 {
		t.Fatalf("enforcement row state=%q source=%q/%q revision=%d err=%v, want active schedule/sch with pre-penalty revision 4", rowState, rowSource, rowSourceID, rowRevision, err)
	}
	// schedule_now 达标 run（quick 70 分 success）触发 D4 恢复。
	store := wbRunNowJ3bFixture(t)
	runner := &fakeRunNowRunner{runID: "run-r2", onStarted: true, resultData: map[string]any{"score": 70, "level": "success", "hardFailure": false}}
	service, _ := wbNewRunNowService(t, db, store, runner)
	recovery, err := NewBusinessRecoveryApplier(db, false)
	if err != nil {
		t.Fatal(err)
	}
	service.Restore = recovery
	if _, err := store.db.Exec(`INSERT INTO model_check_runs (id,system_account_id,actor_system_account_id,provider_code,target_type,target_id,model,profile,trigger_kind,status,level,score,max_score,message,request_summary_json,result_summary_json,policy_snapshot_json,quality_decision_json,probe_set_version,started_at,created_at,updated_at) VALUES ('run-r2','sys','sys','openai','account','acct','gpt-5.6-sol','quick','schedule_now','completed','success',100,100,'','{}','{}','{}','{"reasonCodes":["trust"]}','probe','2026-10-01T00:00:00Z','2026-10-01T00:00:00Z','2026-10-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	outcome, err := service.RunNow(context.Background(), "sys", "sch", nil, "")
	if err != nil || !outcome.Accepted {
		t.Fatalf("run-now outcome=%+v err=%v", outcome, err)
	}
	wbWaitUntil(t, 2*time.Second, func() bool {
		var current string
		return db.QueryRow(`SELECT status FROM accounts WHERE id='acct'`).Scan(&current) == nil && current == "active"
	}, "eligible schedule_now run must restore the real-Apply penalty without claimRecoveries refresh")
	var schedulable int
	if err := db.QueryRow(`SELECT status,schedulable,config_revision FROM accounts WHERE id='acct'`).Scan(&status, &schedulable, &revision); err != nil || status != "active" || schedulable != 1 || revision != 6 {
		t.Fatalf("restored account status=%q schedulable=%d revision=%d err=%v, want active/1/6", status, schedulable, revision, err)
	}
	if err := db.QueryRow(`SELECT state,account_config_revision FROM account_quality_enforcements WHERE account_id='acct'`).Scan(&rowState, &rowRevision); err != nil || rowState != "cleared" || rowRevision != 5 {
		t.Fatalf("cleared enforcement state=%q revision=%d err=%v, want cleared with refreshed revision 5", rowState, rowRevision, err)
	}
	var decision string
	// merge 在恢复之后异步落库：轮询等待 restoreReasonCodes 出现。
	wbWaitUntil(t, 2*time.Second, func() bool {
		if err := store.db.QueryRow(`SELECT quality_decision_json FROM model_check_runs WHERE id='run-r2'`).Scan(&decision); err != nil {
			return false
		}
		return strings.Contains(decision, "schedule_now_restore_applied")
	}, "restore decision must merge the applied code")
	var fields map[string]any
	if err := json.Unmarshal([]byte(decision), &fields); err != nil {
		t.Fatal(err)
	}
	codes, _ := fields["restoreReasonCodes"].([]any)
	if len(codes) != 1 || codes[0] != "schedule_now_restore_applied" {
		t.Fatalf("restore decision=%v", fields)
	}
	if strings.Contains(decision, "skipped_cas_mismatch") {
		t.Fatalf("real-Apply penalty must not hit skipped_cas_mismatch: %s", decision)
	}
}

// 执行期账户被并发修改（revision 漂移且状态不再匹配处罚目标态）：
// 恢复计 Skipped，不覆盖并发变更，处罚行保持 active。
func TestRestoreSchedulePenaltiesSkipsOnConcurrentAccountChange(t *testing.T) {
	db := wbRunNowBusinessFixture(t)
	if _, err := db.Exec(`CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER)`); err != nil {
		t.Fatal(err)
	}
	wbInsertRunNowAccount(t, db, "acct", "sys", "active", 4)
	if _, err := db.Exec(`INSERT INTO model_quality_schedules (id,revision,system_account_id,account_id,model,interval_minutes,profile,penalty_threshold,penalty_action,recovery_interval_minutes,enabled,next_run_at,lease_owner,lease_until,last_run_id,last_run_at,last_run_status,created_at,updated_at,custom_question_ids) VALUES ('sch',1,'sys','acct','gpt-5.6-sol',30,'quick',70,'quality_isolate',10,1,'2026-10-01T00:00:00Z',NULL,NULL,NULL,NULL,NULL,'2026-10-01T00:00:00Z','','[]')`); err != nil {
		t.Fatal(err)
	}
	enforcer, err := NewBusinessEnforcementApplier(db, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enforcer.Apply(context.Background(), QualityEnforcement{AccountID: "acct", SystemAccountID: "sys", RunID: "run-penalty", ProviderCode: "openai", Model: "gpt-5.6-sol", PolicyRevision: "1", AccountConfigRevision: "4", ScheduleID: "sch", Action: "quality_isolate", Score: 20, Threshold: 70, RecoveryIntervalMinutes: 10, Profile: "quick", OccurredAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatalf("real Apply must create the penalty: %v", err)
	}
	// 执行窗口内并发修改：人工把 quality_isolated 账户改回 active 并推进
	// revision（漂移 + 状态不再匹配处罚目标态）。
	if _, err := db.Exec(`UPDATE accounts SET status='active',schedulable=1,config_revision=config_revision+1 WHERE id='acct'`); err != nil {
		t.Fatal(err)
	}
	applier, err := NewBusinessRecoveryApplier(db, false)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := applier.RestoreSchedulePenalties(context.Background(), SchedulePenaltyRestoreInput{AccountID: "acct", SystemAccountID: "sys", ScheduleID: "sch", RunID: "run-r3"})
	if err != nil || outcome.Restored != 0 || outcome.Skipped != 1 {
		t.Fatalf("outcome=%+v err=%v, want Skipped=1", outcome, err)
	}
	var status string
	var revision int
	if err := db.QueryRow(`SELECT status,config_revision FROM accounts WHERE id='acct'`).Scan(&status, &revision); err != nil || status != "active" || revision != 6 {
		t.Fatalf("concurrent change must survive: status=%q revision=%d err=%v", status, revision, err)
	}
	var state string
	var rowRevision int
	if err := db.QueryRow(`SELECT state,account_config_revision FROM account_quality_enforcements WHERE account_id='acct'`).Scan(&state, &rowRevision); err != nil || state != "active" || rowRevision != 4 {
		t.Fatalf("skipped enforcement must stay active untouched: state=%q revision=%d err=%v", state, rowRevision, err)
	}
}

// ---------------------------------------------------------------------------
// D5：计划列表执行态 + lastRunScore
// ---------------------------------------------------------------------------

func TestScheduleExecutionStatePriority(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour).Format(time.RFC3339Nano)
	past := now.Add(-time.Hour).Format(time.RFC3339Nano)
	cases := []struct {
		name         string
		enabled      bool
		leaseUntil   string
		accountState string
		nextRunAt    string
		want         string
	}{
		{name: "paused wins over running", enabled: false, leaseUntil: future, accountState: "active", nextRunAt: past, want: "paused"},
		{name: "running lease", enabled: true, leaseUntil: future, accountState: "active", nextRunAt: past, want: "running"},
		{name: "blocked account", enabled: true, leaseUntil: "", accountState: "disabled", nextRunAt: past, want: "blocked_account"},
		{name: "blocked quality isolated", enabled: true, leaseUntil: "", accountState: "quality_isolated", nextRunAt: past, want: "blocked_account"},
		{name: "queued due", enabled: true, leaseUntil: "", accountState: "active", nextRunAt: past, want: "queued"},
		{name: "enabled future", enabled: true, leaseUntil: "", accountState: "active", nextRunAt: future, want: "enabled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scheduleExecutionState(tc.enabled, tc.leaseUntil, tc.accountState, tc.nextRunAt, now); got != tc.want {
				t.Fatalf("state=%q, want %q", got, tc.want)
			}
		})
	}
}

type fakeRunScoreReader struct {
	scores map[string]int
	err    error
}

func (f fakeRunScoreReader) RunScoresByIDs(_ context.Context, ids []string) (map[string]int, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]int{}
	for _, id := range ids {
		if score, ok := f.scores[id]; ok {
			out[id] = score
		}
	}
	return out, nil
}

// 列表 SQL 补选 lease_until/a.status 后五态可观察，lastRunScore 经端口注入。
func TestBusinessQualityManagerListExecutionStateAndScore(t *testing.T) {
	db := wbRunNowBusinessFixture(t)
	if _, err := db.Exec(`CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,manual_enforcement_enabled INTEGER,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,created_at TEXT,updated_at TEXT,custom_question_ids TEXT)`); err != nil {
		t.Fatal(err)
	}
	manager, err := NewBusinessQualityManager(db, false)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	wbInsertRunNowAccount(t, db, "acct-a", "sys", "active", 4)
	wbInsertRunNowAccount(t, db, "acct-b", "sys", "active", 4)
	wbInsertRunNowAccount(t, db, "acct-c", "sys", "disabled", 4)
	wbInsertRunNowAccount(t, db, "acct-d", "sys", "active", 4)
	wbInsertRunNowAccount(t, db, "acct-e", "sys", "active", 4)
	wbInsertRunNowSchedule(t, db, "sch-paused", "sys", "acct-a", 0, 30, now.Format(time.RFC3339Nano), "")
	wbInsertRunNowSchedule(t, db, "sch-running", "sys", "acct-b", 1, 30, now.Format(time.RFC3339Nano), now.Add(time.Minute).Format(time.RFC3339Nano))
	wbInsertRunNowSchedule(t, db, "sch-blocked", "sys", "acct-c", 1, 30, now.Format(time.RFC3339Nano), "")
	wbInsertRunNowSchedule(t, db, "sch-queued", "sys", "acct-d", 1, 30, now.Add(-time.Minute).Format(time.RFC3339Nano), "")
	wbInsertRunNowSchedule(t, db, "sch-enabled", "sys", "acct-e", 1, 30, now.Add(time.Hour).Format(time.RFC3339Nano), "")
	if _, err := db.Exec(`UPDATE model_quality_schedules SET last_run_id='run-scored' WHERE id='sch-enabled'`); err != nil {
		t.Fatal(err)
	}
	manager.SetRunScoreReader(fakeRunScoreReader{scores: map[string]int{"run-scored": 0}})
	list, err := manager.ListSchedules(context.Background(), "sys", 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]QualityScheduleView{}
	for _, item := range list.Items {
		states[item.ID] = item
	}
	if len(states) != 5 {
		t.Fatalf("list items=%d", len(states))
	}
	for id, want := range map[string]string{"sch-paused": "paused", "sch-running": "running", "sch-blocked": "blocked_account", "sch-queued": "queued", "sch-enabled": "enabled"} {
		if states[id].ExecutionState != want {
			t.Fatalf("%s state=%q, want %q", id, states[id].ExecutionState, want)
		}
	}
	if states["sch-blocked"].AccountStatus == nil || *states["sch-blocked"].AccountStatus != "disabled" {
		t.Fatalf("blocked account status snapshot=%+v", states["sch-blocked"].AccountStatus)
	}
	if states["sch-enabled"].LastRunScore == nil || *states["sch-enabled"].LastRunScore != 0 {
		t.Fatalf("score 0 must survive pointer+omitempty encoding: %+v", states["sch-enabled"].LastRunScore)
	}
	if states["sch-paused"].LastRunScore != nil {
		t.Fatalf("missing run must yield nil score: %+v", states["sch-paused"].LastRunScore)
	}
	// 端口未注入时列表仍可用，lastRunScore 恒为 null。
	bare, err := NewBusinessQualityManager(db, false)
	if err != nil {
		t.Fatal(err)
	}
	if list, err = bare.ListSchedules(context.Background(), "sys", 1, 50); err != nil {
		t.Fatal(err)
	}
	for _, item := range list.Items {
		if item.LastRunScore != nil {
			t.Fatalf("bare manager score must be nil: %+v", item.LastRunScore)
		}
		if item.ExecutionState == "" {
			t.Fatalf("executionState must always be derived: %+v", item)
		}
	}
}

// Store.RunScoresByIDs：按 id 批量取分，未知 id 不出现。
func TestStoreRunScoresByIDs(t *testing.T) {
	store := wbRunNowJ3bFixture(t)
	for i, score := range map[string]int{"run-a": 90, "run-b": 0} {
		if _, err := store.db.Exec(`INSERT INTO model_check_runs (id,system_account_id,actor_system_account_id,provider_code,target_type,target_id,model,profile,trigger_kind,status,level,score,max_score,message,request_summary_json,result_summary_json,policy_snapshot_json,quality_decision_json,probe_set_version,started_at,created_at,updated_at) VALUES (?,?,'sys','openai','account','acct','m','quick','manual','completed','success',?,100,'','{}','{}','{}','{}','probe','2026-10-01T00:00:00Z','2026-10-01T00:00:00Z','2026-10-01T00:00:00Z')`, i, "sys", score); err != nil {
			t.Fatal(err)
		}
	}
	scores, err := store.RunScoresByIDs(context.Background(), []string{"run-a", "run-b", "run-missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(scores) != 2 || scores["run-a"] != 90 || scores["run-b"] != 0 {
		t.Fatalf("scores=%+v", scores)
	}
	if scores, err = store.RunScoresByIDs(context.Background(), nil); err != nil || len(scores) != 0 {
		t.Fatalf("empty input scores=%+v err=%v", scores, err)
	}
}

// ---------------------------------------------------------------------------
// D6：构建失败产生可查 run 记录
// ---------------------------------------------------------------------------

func TestStoreCreateFailedRun(t *testing.T) {
	store := wbRunNowJ3bFixture(t)
	record := RunRecord{ID: "run-failed-1", SystemAccountID: "sys", ActorSystemAccountID: "sys", ProviderCode: "openai", TargetType: "account", TargetID: "acct", AccountID: "acct", Model: "gpt-5.6-sol", Profile: "quick", TriggerKind: string(SchedulerScheduled), ScheduleID: "sch-1", ProbeSetVersion: "probe-1", RequestSummary: []byte(`{"targetId":"acct"}`), PolicySnapshot: []byte(`{"threshold":70}`), StartedAt: time.Now().UTC()}
	if err := store.CreateFailedRun(context.Background(), record, "model_check_build_failed", "目标解析失败", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var status, errorCode, message string
	if err := store.db.QueryRow(`SELECT status,error_code,error_message FROM model_check_runs WHERE id='run-failed-1'`).Scan(&status, &errorCode, &message); err != nil {
		t.Fatal(err)
	}
	if status != string(RunFailed) || errorCode != "model_check_build_failed" || message != "目标解析失败" {
		t.Fatalf("failed run status=%q error=%q message=%q", status, errorCode, message)
	}
	var itemCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_check_items WHERE run_id='run-failed-1'`).Scan(&itemCount); err != nil || itemCount != 1 {
		t.Fatalf("items=%d err=%v, want 1 execution item", itemCount, err)
	}
}

// 调度 executor Build 失败：failed run 记录 + CompleteScheduled 拿到 runId。
func TestSchedulerExecutorBuildFailureRecordsFailedRun(t *testing.T) {
	recorded := map[string]RunRecord{}
	var completion RunResult
	recorder := fakeFailedRunRecorder{records: recorded}
	var completedPayload ScheduledPayload
	executor := &SchedulerRunExecutor{
		Runtime: &schedulerRunnerStub{},
		Build: func(context.Context, ScheduledPayload) (RunRequest, error) {
			return RunRequest{}, errors.New("resolve J3b target: account deleted")
		},
		Scheduled: func(_ context.Context, payload ScheduledPayload, result RunResult) error {
			completedPayload, completion = payload, result
			return nil
		},
		FailedRuns: recorder,
	}
	payload, _ := json.Marshal(ScheduledPayload{SystemAccountID: "sys", ActorSystemAccountID: "sys", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", ProviderCode: "openai", Threshold: 70, PenaltyAction: "fallback", ConfigRevision: "4", DispatchRevision: 4, SourceConfigRevision: "src-4", SourceDispatchRevision: 4, PolicyRevision: "3", ProbeSetVersion: "probe-4", IdentityKey: "identity-5", ScheduleID: "sch-6", OwnerID: "gateway-1", ScheduleRevision: 2, IntervalMinutes: 60})
	err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerScheduled, Payload: payload})
	if err == nil || !strings.Contains(err.Error(), "build J3b scheduled request") {
		t.Fatalf("build error must surface: %v", err)
	}
	if completion.Status != string(RunFailed) || completion.RunID == "" {
		t.Fatalf("completion=%+v must carry the failed run id", completion)
	}
	record, ok := recorded[completion.RunID]
	if !ok {
		t.Fatalf("failed run %q must be recorded", completion.RunID)
	}
	if record.TriggerKind != string(SchedulerScheduled) || record.ScheduleID != "sch-6" || record.AccountID != "acct" {
		t.Fatalf("failed run record=%+v", record)
	}
	if completedPayload.ScheduleID != "sch-6" {
		t.Fatalf("completion payload=%+v", completedPayload)
	}
}

// 恢复类 Build 失败同样落 run 记录，并保持既有"返回错误、租约到期重试"
// 语义（不调 Recovery 完成回调）。
func TestSchedulerExecutorRecoveryBuildFailureRecordsFailedRun(t *testing.T) {
	recorded := map[string]RunRecord{}
	recoveryCalled := false
	executor := &SchedulerRunExecutor{
		Runtime: &schedulerRunnerStub{},
		Build: func(context.Context, ScheduledPayload) (RunRequest, error) {
			return RunRequest{}, errors.New("resolve J3b target: unavailable")
		},
		Recovery: func(context.Context, RecoveryPayload, bool) error {
			recoveryCalled = true
			return nil
		},
		FailedRuns: fakeFailedRunRecorder{records: recorded},
	}
	payload, _ := json.Marshal(ScheduledPayload{SystemAccountID: "sys", ActorSystemAccountID: "sys", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "full", ProviderCode: "openai", Threshold: 70, PenaltyAction: "quality_isolate", ConfigRevision: "4", DispatchRevision: 4, SourceConfigRevision: "src-4", SourceDispatchRevision: 4, PolicyRevision: "3", ProbeSetVersion: "probe-4", IdentityKey: "identity-5", OwnerID: "gateway-1", EnforcementID: "enf-1", Generation: 2, RecoveryIntervalMinutes: 10})
	if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerQualityRecovery, Payload: payload}); err == nil {
		t.Fatal("recovery build failure must return an error for lease-expiry retry")
	}
	if recoveryCalled {
		t.Fatal("recovery completion must not run on build failure")
	}
	if len(recorded) != 1 {
		t.Fatalf("failed run must be recorded once: %d", len(recorded))
	}
	for _, record := range recorded {
		if record.TriggerKind != string(SchedulerQualityRecovery) {
			t.Fatalf("failed run trigger=%q", record.TriggerKind)
		}
	}
}

type fakeFailedRunRecorder struct {
	records map[string]RunRecord
}

func (f fakeFailedRunRecorder) CreateFailedRun(_ context.Context, run RunRecord, errorCode, message string, _ time.Time) error {
	if f.records == nil {
		return errors.New("recorder not initialized")
	}
	if errorCode == "" || message == "" {
		return errors.New("error context required")
	}
	f.records[run.ID] = run
	return nil
}

// ---------------------------------------------------------------------------
// D3：HTTP 面
// ---------------------------------------------------------------------------

func wbNewRunNowHTTPHandler(t *testing.T, service *ScheduleRunNowService) *HTTPHandler {
	t.Helper()
	handler := newTestHTTPHandler()
	handler.RunNow = service
	return handler
}

func wbRunNowServiceForHTTP(t *testing.T, db *sql.DB, runner SchedulerRunner) *ScheduleRunNowService {
	service, _ := wbNewRunNowService(t, db, wbRunNowJ3bFixture(t), runner)
	return service
}

func TestHTTPRunNowSingleRouteContract(t *testing.T) {
	now := time.Now().UTC()
	t.Run("accepted", func(t *testing.T) {
		db := wbRunNowBusinessFixture(t)
		handler := wbNewRunNowHTTPHandler(t, wbRunNowServiceForHTTP(t, db, &fakeRunNowRunner{runID: "run-h", onStarted: true}))
		wbInsertRunNowAccount(t, db, "acct", "sys-1", "active", 4)
		wbInsertRunNowSchedule(t, db, "sch", "sys-1", "acct", 1, 30, now.Format(time.RFC3339Nano), "")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/quality-schedules/sch/run-now", strings.NewReader(`{"requestId":"req-h"}`)))
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		body := response.Body.String()
		for _, need := range []string{`"accepted":true`, `"status":"started"`, `"scheduleId":"sch"`, `"runId":"run-h"`} {
			if !strings.Contains(body, need) {
				t.Fatalf("body=%s missing %s", body, need)
			}
		}
		wbWaitUntil(t, 2*time.Second, func() bool {
			var status string
			return db.QueryRow(`SELECT last_run_status FROM model_quality_schedules WHERE id='sch'`).Scan(&status) == nil && status != ""
		}, "accepted single run-now must settle before cleanup")
	})
	t.Run("already running maps to 409", func(t *testing.T) {
		db := wbRunNowBusinessFixture(t)
		handler := wbNewRunNowHTTPHandler(t, wbRunNowServiceForHTTP(t, db, &fakeRunNowRunner{runID: "run-h", onStarted: true}))
		wbInsertRunNowAccount(t, db, "acct", "sys-1", "active", 4)
		wbInsertRunNowSchedule(t, db, "sch", "sys-1", "acct", 1, 30, now.Format(time.RFC3339Nano), now.Add(time.Minute).Format(time.RFC3339Nano))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/quality-schedules/sch/run-now", strings.NewReader(`{}`)))
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "该计划的定时检查正在执行") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
	t.Run("stale revision maps to 409", func(t *testing.T) {
		db := wbRunNowBusinessFixture(t)
		handler := wbNewRunNowHTTPHandler(t, wbRunNowServiceForHTTP(t, db, &fakeRunNowRunner{runID: "run-h", onStarted: true}))
		wbInsertRunNowAccount(t, db, "acct", "sys-1", "active", 4)
		wbInsertRunNowSchedule(t, db, "sch", "sys-1", "acct", 1, 30, now.Format(time.RFC3339Nano), "")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/quality-schedules/sch/run-now", strings.NewReader(`{"revision":99}`)))
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "已变化") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
	t.Run("missing maps to 404", func(t *testing.T) {
		db := wbRunNowBusinessFixture(t)
		handler := wbNewRunNowHTTPHandler(t, wbRunNowServiceForHTTP(t, db, &fakeRunNowRunner{runID: "run-h", onStarted: true}))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/quality-schedules/sch-missing/run-now", strings.NewReader(`{}`)))
		if response.Code != http.StatusNotFound {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
	t.Run("unwired owner maps to 503", func(t *testing.T) {
		handler := newTestHTTPHandler()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/quality-schedules/sch/run-now", strings.NewReader(`{}`)))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
	t.Run("invalid body maps to 400", func(t *testing.T) {
		db := wbRunNowBusinessFixture(t)
		handler := wbNewRunNowHTTPHandler(t, wbRunNowServiceForHTTP(t, db, &fakeRunNowRunner{runID: "run-h", onStarted: true}))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/quality-schedules/sch/run-now", strings.NewReader(`{"unexpected":1}`)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
}

func TestHTTPRunNowBatchRouteContract(t *testing.T) {
	now := time.Now().UTC()
	t.Run("partial success always 200", func(t *testing.T) {
		db := wbRunNowBusinessFixture(t)
		handler := wbNewRunNowHTTPHandler(t, wbRunNowServiceForHTTP(t, db, &fakeRunNowRunner{runID: "run-hb", onStarted: true}))
		wbInsertRunNowAccount(t, db, "acct", "sys-1", "active", 4)
		wbInsertRunNowSchedule(t, db, "sch", "sys-1", "acct", 1, 30, now.Format(time.RFC3339Nano), "")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/quality-schedules/run-now-batch", strings.NewReader(`{"items":[{"scheduleId":"sch"},{"scheduleId":"sch-missing"},{"scheduleId":" "}]}`)))
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		body := response.Body.String()
		for _, need := range []string{`"status":"started"`, `"status":"not_found"`, `"status":"invalid"`} {
			if !strings.Contains(body, need) {
				t.Fatalf("body=%s missing %s", body, need)
			}
		}
		wbWaitUntil(t, 2*time.Second, func() bool {
			var status string
			return db.QueryRow(`SELECT last_run_status FROM model_quality_schedules WHERE id='sch'`).Scan(&status) == nil && status != ""
		}, "accepted batch item must settle before cleanup")
	})
	t.Run("empty items 400", func(t *testing.T) {
		db := wbRunNowBusinessFixture(t)
		handler := wbNewRunNowHTTPHandler(t, wbRunNowServiceForHTTP(t, db, &fakeRunNowRunner{runID: "run-hb", onStarted: true}))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/quality-schedules/run-now-batch", strings.NewReader(`{"items":[]}`)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
	t.Run("over limit 400", func(t *testing.T) {
		db := wbRunNowBusinessFixture(t)
		handler := wbNewRunNowHTTPHandler(t, wbRunNowServiceForHTTP(t, db, &fakeRunNowRunner{runID: "run-hb", onStarted: true}))
		items := make([]string, 0, 101)
		items = append(items, `{"items":[`)
		for i := 0; i < 101; i++ {
			items = append(items, `{"scheduleId":"sch-`+strconv.Itoa(i)+`"},`)
		}
		items = append(items, `]}`)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/quality-schedules/run-now-batch", strings.NewReader(strings.Join(items, ""))))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
}

// ---------------------------------------------------------------------------
// D3 补充：幂等缓存只记录最终结果（问题-0257 终审 major 回归）
// ---------------------------------------------------------------------------

// stale_revision 拒绝后同 (scheduleId, requestId) 重放必须返回相同拒绝
// 结果：修复前 accept 先无条件写入 Accepted=true/started 的 early 条目，
// 拒绝路径不覆盖，重放会命中假 started 并短路互斥检查。
func TestScheduleRunNowIdempotencyReplaysRejection(t *testing.T) {
	db := wbRunNowBusinessFixture(t)
	runner := &fakeRunNowRunner{runID: "run-x", onStarted: true}
	service, _ := wbNewRunNowService(t, db, wbRunNowJ3bFixture(t), runner)
	wbInsertRunNowAccount(t, db, "acct", "sys", "active", 4)
	wbInsertRunNowSchedule(t, db, "sch", "sys", "acct", 1, 30, time.Now().UTC().Format(time.RFC3339Nano), "")
	stale := 99
	first, err := service.RunNow(context.Background(), "sys", "sch", &stale, "req-rej")
	if err != nil || first.Accepted || first.Status != ScheduleRunNowStaleRevision {
		t.Fatalf("first outcome=%+v err=%v", first, err)
	}
	// 重放去掉 revision（不带它会通过校验被受理）：命中缓存的拒绝结果才
	// 说明缓存记录的是最终结果而非 early started。
	replay, err := service.RunNow(context.Background(), "sys", "sch", nil, "req-rej")
	if err != nil || replay.Accepted || replay.Status != ScheduleRunNowStaleRevision {
		t.Fatalf("replay must return the recorded rejection, got %+v err=%v", replay, err)
	}
	if replay.Message != first.Message {
		t.Fatalf("replay message=%q, want recorded %q", replay.Message, first.Message)
	}
	if runner.Len() != 0 {
		t.Fatalf("rejected replay must not execute: executed=%d", runner.Len())
	}
	// not_found 拒绝同样记录：重放返回相同拒绝而非 started。
	missingFirst, err := service.RunNow(context.Background(), "sys", "sch-missing", nil, "req-nf")
	if err != nil || missingFirst.Status != ScheduleRunNowNotFound {
		t.Fatalf("missing first outcome=%+v err=%v", missingFirst, err)
	}
	missingReplay, err := service.RunNow(context.Background(), "sys", "sch-missing", nil, "req-nf")
	if err != nil || missingReplay.Accepted || missingReplay.Status != ScheduleRunNowNotFound {
		t.Fatalf("missing replay outcome=%+v err=%v", missingReplay, err)
	}
	if runner.Len() != 0 {
		t.Fatalf("not_found replay must not execute: executed=%d", runner.Len())
	}
}

// ---------------------------------------------------------------------------
// D3 补充：HTTP 面缺口（runId 可为 null / self 面 scope 越权 / 路由顺序）
// ---------------------------------------------------------------------------

func TestHTTPRunNowNullRunIDScopeAndRouteOrder(t *testing.T) {
	now := time.Now().UTC()
	t.Run("accepted with null runId", func(t *testing.T) {
		db := wbRunNowBusinessFixture(t)
		// onStarted=false：OnStarted 不回调，RunIDWait 超时后 runId=null，
		// 受理仍成立（runId 可为 null）。
		handler := wbNewRunNowHTTPHandler(t, wbRunNowServiceForHTTP(t, db, &fakeRunNowRunner{runID: "run-late", onStarted: false}))
		wbInsertRunNowAccount(t, db, "acct", "sys-1", "active", 4)
		wbInsertRunNowSchedule(t, db, "sch", "sys-1", "acct", 1, 30, now.Format(time.RFC3339Nano), "")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/quality-schedules/sch/run-now", strings.NewReader(`{}`)))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"runId":null`) || !strings.Contains(response.Body.String(), `"accepted":true`) {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		wbWaitUntil(t, 2*time.Second, func() bool {
			var status string
			return db.QueryRow(`SELECT last_run_status FROM model_quality_schedules WHERE id='sch'`).Scan(&status) == nil && status != ""
		}, "null-runId acceptance must still settle before cleanup")
	})
	t.Run("self scope keeps foreign schedule not found", func(t *testing.T) {
		db := wbRunNowBusinessFixture(t)
		handler := wbNewRunNowHTTPHandler(t, wbRunNowServiceForHTTP(t, db, &fakeRunNowRunner{runID: "run-h", onStarted: true}))
		// self 面：ForceActorScope 忽略请求携带的 systemAccountId，作用域
		// 恒为认证主体 sys-1；他人（sys-2）的计划必须 404 而非越权受理。
		handler.ForceActorScope = true
		wbInsertRunNowAccount(t, db, "acct", "sys-2", "active", 4)
		wbInsertRunNowSchedule(t, db, "sch-foreign", "sys-2", "acct", 1, 30, now.Format(time.RFC3339Nano), "")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/quality-schedules/sch-foreign/run-now?systemAccountId=sys-2", strings.NewReader(`{}`)))
		if response.Code != http.StatusNotFound {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
	t.Run("batch fixed segment is not swallowed by id prefix", func(t *testing.T) {
		db := wbRunNowBusinessFixture(t)
		handler := wbNewRunNowHTTPHandler(t, wbRunNowServiceForHTTP(t, db, &fakeRunNowRunner{runID: "run-hb", onStarted: true}))
		wbInsertRunNowAccount(t, db, "acct", "sys-1", "active", 4)
		wbInsertRunNowSchedule(t, db, "sch", "sys-1", "acct", 1, 30, now.Format(time.RFC3339Nano), "")
		batch := httptest.NewRecorder()
		handler.ServeHTTP(batch, httptest.NewRequest(http.MethodPost, "/quality-schedules/run-now-batch", strings.NewReader(`{"items":[{"scheduleId":"sch"}]}`)))
		if batch.Code != http.StatusOK || !strings.Contains(batch.Body.String(), `"results"`) || !strings.Contains(batch.Body.String(), `"status":"started"`) {
			t.Fatalf("batch status=%d body=%s", batch.Code, batch.Body.String())
		}
		// 反向：名为 run-now-batch 的计划 id 经单条前缀路由仍按 {id} 解析，
		// 不与批量固定段互相吞掉（此处无该计划 -> 404 由单条路径给出）。
		single := httptest.NewRecorder()
		handler.ServeHTTP(single, httptest.NewRequest(http.MethodPost, "/quality-schedules/run-now-batch/run-now", strings.NewReader(`{}`)))
		if single.Code != http.StatusNotFound {
			t.Fatalf("single status=%d body=%s", single.Code, single.Body.String())
		}
		wbWaitUntil(t, 2*time.Second, func() bool {
			var status string
			return db.QueryRow(`SELECT last_run_status FROM model_quality_schedules WHERE id='sch'`).Scan(&status) == nil && status != ""
		}, "accepted batch item must settle before cleanup")
	})
}

// ---------------------------------------------------------------------------
// D4 补充：restoreProbe 解析门禁矩阵（business_source.go 分支覆盖）
// ---------------------------------------------------------------------------

// wbRestoreProbeSourceFixture 建出 BusinessTargetSource.Resolve 所需的最小
// Business 表（列面与 business_source_test.go 的 Resolve 夹具一致）。
func wbRestoreProbeSourceFixture(t *testing.T) (*BusinessTargetSource, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/restore-probe.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,system_account_id TEXT,provider_code TEXT,provider_protocol_profile_id TEXT,protocol_code TEXT,type TEXT,config_revision INTEGER,dispatch_revision INTEGER,status TEXT,schedulable INTEGER,health_check_endpoint_mode TEXT,account_expires_at TEXT,cooldown_until TEXT,last_error_code TEXT,credentials_encrypted TEXT,proxy_profile_id TEXT,availability_schedule_json TEXT,authorization_instance_authorization_id TEXT,authorization_instance_source_account_id TEXT,deleted_at TEXT,name TEXT)`,
		`CREATE TABLE provider_protocol_profiles (id TEXT PRIMARY KEY,provider_code TEXT,enabled INTEGER,protocol_code TEXT,base_url TEXT)`,
		`CREATE TABLE proxy_profiles (id TEXT PRIMARY KEY,enabled INTEGER,type TEXT,host TEXT,port INTEGER,username TEXT,password_encrypted TEXT)`,
		`CREATE TABLE group_accounts (account_id TEXT,system_account_id TEXT,group_id TEXT,account_authorization_id TEXT,enabled INTEGER)`,
		`CREATE TABLE groups (id TEXT PRIMARY KEY,system_account_id TEXT,enabled INTEGER)`,
		`CREATE TABLE resource_authorizations (id TEXT PRIMARY KEY,resource_type TEXT,resource_id TEXT,resource_owner_system_account_id TEXT,grantee_system_account_id TEXT,scope TEXT,status TEXT,expires_at TEXT)`,
		`CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,manual_enforcement_enabled INTEGER,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,custom_question_ids TEXT)`,
		`CREATE TABLE account_supported_models (account_id TEXT,model TEXT)`,
		`CREATE TABLE account_model_mappings (account_id TEXT,source_model TEXT,source_endpoint_family TEXT,upstream_model TEXT,upstream_endpoint_family TEXT,enabled INTEGER)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	envelope := testCredentialEnvelope(t, "secret", `{"api_key":"key-1","supported_endpoint_modes":["responses_sse"]}`)
	if _, err := db.Exec(`INSERT INTO provider_protocol_profiles VALUES ('profile_openai_openai_v1','openai',1,'openai_responses','https://example.invalid/v1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO groups VALUES ('group-1','sys',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_accounts(account_id,system_account_id,group_id,enabled) VALUES ('acct','sys','group-1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('acct','sys','openai','profile_openai_openai_v1','openai','api_key',3,7,'active',1,'responses_sse',NULL,NULL,NULL,?,NULL,NULL,NULL,NULL,NULL,'Restore Probe')`, envelope); err != nil {
		t.Fatal(err)
	}
	source, err := NewBusinessTargetSource(db, false, "secret")
	if err != nil {
		t.Fatal(err)
	}
	return source, db
}

// restoreProbe 门禁矩阵（D4 前置）：schedule_now 必须能探测被本计划处罚
// 的账户（disabled / quality_isolated，且 schedulable=0 与处罚错误码放行），
// 对正常态族不回归；quality_recovery 仍仅放行 quality_isolated；manual/
// scheduled 旧语义不变（disabled/quality_isolated 拒绝、可用时段外拒绝）。
func TestBusinessTargetSourceScheduleNowRestoreProbeMatrix(t *testing.T) {
	source, db := wbRestoreProbeSourceFixture(t)
	source.now = func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }
	// 窗口 13:00-14:00（全周）在冻结时刻 12:00 之外：ordinary 拒绝、
	// schedule_now 放行。空串表示无时段约束。
	const deniedWindow = `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"13:00","end":"14:00"}]}`
	setAccount := func(t *testing.T, status string, schedulable int, lastErrorCode, availability string) {
		t.Helper()
		if _, err := db.Exec(`UPDATE accounts SET status=?,schedulable=?,last_error_code=?,availability_schedule_json=? WHERE id='acct'`, status, schedulable, lastErrorCode, availability); err != nil {
			t.Fatal(err)
		}
	}
	resolve := func(trigger string) error {
		_, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", TriggerKind: trigger})
		return err
	}
	for _, tc := range []struct {
		name         string
		status       string
		schedulable  int
		lastError    string
		availability string
		ordinary     bool
		scheduleNow  bool
		recovery     bool
	}{
		{name: "disabled penalty state", status: "disabled", schedulable: 0, ordinary: false, scheduleNow: true, recovery: false},
		{name: "quality isolated penalty state", status: "quality_isolated", schedulable: 0, lastError: "model_quality_failed", ordinary: false, scheduleNow: true, recovery: true},
		{name: "active normal state", status: "active", schedulable: 1, ordinary: true, scheduleNow: true, recovery: false},
		{name: "temporary unavailable normal family", status: "temporary_unavailable", schedulable: 1, ordinary: true, scheduleNow: true, recovery: false},
		{name: "rate limited normal family", status: "rate_limited", schedulable: 1, ordinary: true, scheduleNow: true, recovery: false},
		{name: "outside availability window", status: "active", schedulable: 1, availability: deniedWindow, ordinary: false, scheduleNow: true, recovery: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setAccount(t, tc.status, tc.schedulable, tc.lastError, tc.availability)
			if err := resolve(""); (err == nil) != tc.ordinary {
				t.Fatalf("manual/scheduled resolve err=%v, want ok=%t", err, tc.ordinary)
			}
			if err := resolve(TriggerKindScheduleNow); (err == nil) != tc.scheduleNow {
				t.Fatalf("schedule_now resolve err=%v, want ok=%t", err, tc.scheduleNow)
			}
			if err := resolve(string(SchedulerQualityRecovery)); (err == nil) != tc.recovery {
				t.Fatalf("quality_recovery resolve err=%v, want ok=%t", err, tc.recovery)
			}
		})
	}
}
