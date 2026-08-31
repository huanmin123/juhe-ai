package modelcheckquality

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestApplyEnforcementSQLiteCASAndReplay(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE accounts(id TEXT PRIMARY KEY,system_account_id TEXT NOT NULL,status TEXT NOT NULL,config_revision INTEGER NOT NULL,fallback_enabled INTEGER NOT NULL,super_priority_enabled INTEGER NOT NULL,schedulable INTEGER NOT NULL,last_error_code TEXT,last_error_message TEXT,authorization_instance_authorization_id TEXT,deleted_at TEXT,updated_at TEXT); CREATE TABLE model_quality_policies(system_account_id TEXT PRIMARY KEY,revision INTEGER NOT NULL,profile TEXT NOT NULL,penalty_threshold INTEGER NOT NULL,penalty_action TEXT NOT NULL,recovery_interval_minutes INTEGER NOT NULL); CREATE TABLE account_quality_enforcements(account_id TEXT PRIMARY KEY,system_account_id TEXT NOT NULL,enforcement_id TEXT NOT NULL UNIQUE,generation INTEGER NOT NULL,state TEXT NOT NULL,action TEXT NOT NULL,trigger_run_id TEXT NOT NULL,config_source TEXT NOT NULL,config_source_id TEXT,policy_revision INTEGER NOT NULL,profile TEXT NOT NULL,penalty_threshold INTEGER NOT NULL,recovery_interval_minutes INTEGER NOT NULL,recovery_model TEXT,account_config_revision INTEGER NOT NULL,before_status TEXT NOT NULL,after_status TEXT NOT NULL,fallback_was_enabled INTEGER NOT NULL,super_priority_was_enabled INTEGER NOT NULL,started_at TEXT NOT NULL,recovery_due_at TEXT,created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO accounts VALUES('acct','sys','active',3,0,1,1,NULL,NULL,NULL,NULL,NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_quality_policies VALUES('sys',2,'quick',70,'quality_isolate',10)`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	in := EnforcementInput{SystemAccountID: "sys", AccountID: "acct", RunID: "run-1", Action: "quality_isolate", Profile: "quick", Model: "gpt-5.6-sol", Message: "quality failed", PolicyRevision: 2, PenaltyThreshold: 70, RecoveryIntervalMinutes: 10, AccountConfigRevision: 3, DecidedAt: now}
	got, err := ApplyEnforcement(context.Background(), db, false, in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Result != "applied" || got.BeforeStatus != "active" || got.AfterStatus != "quality_isolated" || got.Generation != 1 || got.RecoveryDueAt == nil {
		t.Fatalf("result=%+v", got)
	}
	if replay, err := ApplyEnforcement(context.Background(), db, false, in); err != nil || replay.Result != "stale" {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	in.RunID = "run-2"
	if stale, err := ApplyEnforcement(context.Background(), db, false, in); err != nil || stale.Result != "stale" {
		t.Fatalf("second action=%+v err=%v", stale, err)
	}
}

func TestApplyEnforcementRejectsConfigurationAndAuthorizationDrift(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE accounts(id TEXT PRIMARY KEY,system_account_id TEXT NOT NULL,status TEXT NOT NULL,config_revision INTEGER NOT NULL,fallback_enabled INTEGER NOT NULL,super_priority_enabled INTEGER NOT NULL,schedulable INTEGER NOT NULL,last_error_code TEXT,last_error_message TEXT,authorization_instance_authorization_id TEXT,deleted_at TEXT,updated_at TEXT); CREATE TABLE model_quality_policies(system_account_id TEXT PRIMARY KEY,revision INTEGER NOT NULL,profile TEXT NOT NULL,penalty_threshold INTEGER NOT NULL,penalty_action TEXT NOT NULL,recovery_interval_minutes INTEGER NOT NULL); CREATE TABLE account_quality_enforcements(account_id TEXT PRIMARY KEY,system_account_id TEXT NOT NULL,enforcement_id TEXT NOT NULL UNIQUE,generation INTEGER NOT NULL,state TEXT NOT NULL,action TEXT NOT NULL,trigger_run_id TEXT NOT NULL,config_source TEXT NOT NULL,config_source_id TEXT,policy_revision INTEGER NOT NULL,profile TEXT NOT NULL,penalty_threshold INTEGER NOT NULL,recovery_interval_minutes INTEGER NOT NULL,recovery_model TEXT,account_config_revision INTEGER NOT NULL,before_status TEXT NOT NULL,after_status TEXT NOT NULL,fallback_was_enabled INTEGER NOT NULL,super_priority_was_enabled INTEGER NOT NULL,started_at TEXT NOT NULL,recovery_due_at TEXT,created_at TEXT NOT NULL,updated_at TEXT NOT NULL); INSERT INTO accounts VALUES('acct','sys','active',5,0,0,1,NULL,NULL,'authorization',NULL,NULL); INSERT INTO model_quality_policies VALUES('sys',1,'quick',70,'fallback',10)`)
	if err != nil {
		t.Fatal(err)
	}
	input := EnforcementInput{SystemAccountID: "sys", AccountID: "acct", RunID: "run", Action: "fallback", Profile: "quick", Model: "gpt-5.6-sol", PolicyRevision: 1, PenaltyThreshold: 70, RecoveryIntervalMinutes: 10, AccountConfigRevision: 5, DecidedAt: time.Date(2026, 8, 27, 13, 0, 0, 0, time.UTC)}
	result, err := ApplyEnforcement(context.Background(), db, false, input)
	if err != nil || result.Result != "skipped" {
		t.Fatalf("authorization instance result=%+v err=%v", result, err)
	}
	if _, err := db.Exec(`UPDATE accounts SET authorization_instance_authorization_id=NULL`); err != nil {
		t.Fatal(err)
	}
	input.PenaltyThreshold = 71
	result, err = ApplyEnforcement(context.Background(), db, false, input)
	if err != nil || result.Result != "stale" {
		t.Fatalf("policy drift result=%+v err=%v", result, err)
	}
	var fallback int
	if err := db.QueryRow(`SELECT fallback_enabled FROM accounts WHERE id='acct'`).Scan(&fallback); err != nil || fallback != 0 {
		t.Fatalf("drift must not change account fallback=%d err=%v", fallback, err)
	}
}

func TestQualityRecoveryClaimCompletionAndAvailabilitySchedule(t *testing.T) {
	db := newRecoveryTestDB(t)
	defer db.Close()
	now := time.Date(2026, 9, 1, 0, 30, 0, 0, time.UTC) // Tuesday, inside Monday's overnight window.
	schedule := `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"23:00","end":"01:00"}]}`
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,status,config_revision,fallback_enabled,super_priority_enabled,schedulable,health_check_model,availability_schedule_json) VALUES('acct','sys','quality_isolated',4,0,0,0,'recovery-model',?)`, schedule); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_quality_enforcements(account_id,system_account_id,enforcement_id,generation,state,action,trigger_run_id,config_source,policy_revision,profile,penalty_threshold,recovery_interval_minutes,recovery_model,account_config_revision,before_status,after_status,fallback_was_enabled,super_priority_was_enabled,started_at,recovery_due_at,created_at,updated_at) VALUES('acct','sys','enf',2,'active','quality_isolate','run-penalty','manual',8,'quick',70,10,'','4','active','quality_isolated',0,0,?,?,?,?)`, now.Format(time.RFC3339Nano), now.Add(-time.Minute).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	claimed, err := ClaimDueRecoveries(context.Background(), db, false, RecoveryClaimInput{OwnerID: "worker", Now: now, Limit: 2, Lease: 6 * time.Minute})
	if err != nil || len(claimed) != 1 || claimed[0].Model != "recovery-model" || claimed[0].AccountConfigRevision != 4 {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	result, err := CompleteRecovery(context.Background(), db, false, RecoveryCompletionInput{OwnerID: "worker", AccountID: "acct", EnforcementID: "enf", Generation: 2, PolicyRevision: 8, RunID: "recovery-run", Passed: true, RecoveryIntervalMinutes: 10, CompletedAt: now})
	if err != nil || result.Result != "recovered" || result.AfterStatus != "active" {
		t.Fatalf("completion=%+v err=%v", result, err)
	}
	var status, state string
	if err := db.QueryRow(`SELECT status FROM accounts WHERE id='acct'`).Scan(&status); err != nil || status != "active" {
		t.Fatalf("account status=%q err=%v", status, err)
	}
	if err := db.QueryRow(`SELECT state FROM account_quality_enforcements WHERE account_id='acct'`).Scan(&state); err != nil || state != "cleared" {
		t.Fatalf("enforcement state=%q err=%v", state, err)
	}
}

func TestQualityRecoveryFailureReschedulesAndInvalidScheduleFailsClosed(t *testing.T) {
	db := newRecoveryTestDB(t)
	defer db.Close()
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,status,config_revision,fallback_enabled,super_priority_enabled,schedulable,health_check_model,availability_schedule_json) VALUES('acct','sys','quality_isolated',4,0,0,0,'m','{bad json')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_quality_enforcements(account_id,system_account_id,enforcement_id,generation,state,action,trigger_run_id,config_source,policy_revision,profile,penalty_threshold,recovery_interval_minutes,recovery_model,account_config_revision,before_status,after_status,fallback_was_enabled,super_priority_was_enabled,started_at,recovery_due_at,recovery_lease_owner,recovery_lease_until,created_at,updated_at) VALUES('acct','sys','enf',1,'active','quality_isolate','run','manual',3,'quick',70,10,'m',4,'active','quality_isolated',0,0,? ,? ,'worker',? ,?,?)`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Add(time.Minute).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	failed, err := CompleteRecovery(context.Background(), db, false, RecoveryCompletionInput{OwnerID: "worker", AccountID: "acct", EnforcementID: "enf", Generation: 1, PolicyRevision: 3, RunID: "failed-run", Passed: false, RecoveryIntervalMinutes: 10, CompletedAt: now})
	if err != nil || failed.Result != "kept_isolated" || failed.NextRecoveryAt == nil {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
	if _, err := db.Exec(`UPDATE account_quality_enforcements SET recovery_lease_owner='worker',recovery_lease_until=? WHERE account_id='acct'`, now.Add(time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	broken, err := CompleteRecovery(context.Background(), db, false, RecoveryCompletionInput{OwnerID: "worker", AccountID: "acct", EnforcementID: "enf", Generation: 1, PolicyRevision: 3, RunID: "passed-run", Passed: true, RecoveryIntervalMinutes: 10, CompletedAt: now})
	if err == nil || broken.Result != "" {
		t.Fatalf("invalid schedule must fail closed result=%+v err=%v", broken, err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM accounts WHERE id='acct'`).Scan(&status); err != nil || status != "quality_isolated" {
		t.Fatalf("invalid schedule changed account=%q err=%v", status, err)
	}
}

func TestAvailabilityScheduleExceptionWindowSchema(t *testing.T) {
	base := `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"00:00","end":"23:59"}],"exceptions":[%s]}`
	tests := []struct {
		name      string
		exception string
		wantErr   bool
	}{
		{name: "deny windows null", exception: `{"date":"2030-01-07","action":"deny","windows":null}`, wantErr: true},
		{name: "allow exception daysOfWeek", exception: `{"date":"2030-01-07","action":"allow","windows":[{"daysOfWeek":[1],"start":"10:00","end":"11:00"}]}`, wantErr: true},
		{name: "valid omitted deny windows", exception: `{"date":"2030-01-07","action":"deny"}`},
		{name: "valid allow start end", exception: `{"date":"2030-01-07","action":"allow","windows":[{"start":"10:00","end":"11:00"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := availabilityAllowed(fmt.Sprintf(base, tt.exception), time.Date(2030, 1, 8, 12, 0, 0, 0, time.UTC))
			if (err != nil) != tt.wantErr {
				t.Fatalf("error=%v, wantErr=%v", err, tt.wantErr)
			}
		})
	}
	tooMany := `{"date":"2030-01-07","action":"allow","windows":[%s]}`
	window := `{"start":"10:00","end":"11:00"}`
	windows := strings.TrimSuffix(strings.Repeat(window+",", 33), ",")
	if _, err := availabilityAllowed(fmt.Sprintf(base, fmt.Sprintf(tooMany, windows)), time.Date(2030, 1, 8, 12, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("exception window count above the schedule limit must fail closed")
	}
}

func TestQualityScheduledClaimAndCompletionFence(t *testing.T) {
	db := newRecoveryTestDB(t)
	defer db.Close()
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,status,config_revision,fallback_enabled,super_priority_enabled,schedulable,health_check_model) VALUES('eligible','sys','active',1,0,0,1,'m'),('authorized','sys','active',1,0,0,1,'m')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE accounts SET authorization_instance_authorization_id='auth' WHERE id='authorized'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_quality_schedules(id,system_account_id,account_id,model,interval_minutes,profile,penalty_threshold,penalty_action,recovery_interval_minutes,enabled,revision,next_run_at,created_at,updated_at) VALUES('due','sys','eligible','gpt-5.6-sol',15,'quick',70,'fallback',10,1,4,?,?,?),('not-owned','sys','authorized','gpt-5.6-sol',15,'quick',70,'fallback',10,1,1,?,?,?)`, now.Add(-time.Minute).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Add(-time.Minute).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	candidates, err := ClaimDueSchedules(context.Background(), db, false, ScheduledClaimInput{OwnerID: "scheduler", Now: now, Limit: 10, Lease: 5 * time.Minute})
	if err != nil || len(candidates) != 1 || candidates[0].ScheduleID != "due" || candidates[0].Revision != 4 {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
	completed, err := CompleteScheduledRun(context.Background(), db, false, ScheduledCompletionInput{OwnerID: "scheduler", ScheduleID: "due", Revision: 4, IntervalMinutes: 15, RunID: "run-1", Status: "completed", CompletedAt: now})
	if err != nil || !completed {
		t.Fatalf("completion=%t err=%v", completed, err)
	}
	var status, owner string
	if err := db.QueryRow(`SELECT last_run_status,COALESCE(lease_owner,'') FROM model_quality_schedules WHERE id='due'`).Scan(&status, &owner); err != nil || status != "completed" || owner != "" {
		t.Fatalf("stored status=%q owner=%q err=%v", status, owner, err)
	}
	stale, err := CompleteScheduledRun(context.Background(), db, false, ScheduledCompletionInput{OwnerID: "scheduler", ScheduleID: "due", Revision: 4, IntervalMinutes: 15, RunID: "run-2", Status: "failed", CompletedAt: now})
	if err != nil || stale {
		t.Fatalf("lease replay must be stale result=%t err=%v", stale, err)
	}
}

func newRecoveryTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE accounts(id TEXT PRIMARY KEY,system_account_id TEXT NOT NULL,status TEXT NOT NULL,config_revision INTEGER NOT NULL,fallback_enabled INTEGER NOT NULL,super_priority_enabled INTEGER NOT NULL,schedulable INTEGER NOT NULL,health_check_model TEXT,availability_schedule_json TEXT,last_error_code TEXT,last_error_message TEXT,authorization_instance_authorization_id TEXT,deleted_at TEXT,updated_at TEXT); CREATE TABLE account_quality_enforcements(account_id TEXT PRIMARY KEY,system_account_id TEXT NOT NULL,enforcement_id TEXT NOT NULL UNIQUE,generation INTEGER NOT NULL,state TEXT NOT NULL,action TEXT NOT NULL,trigger_run_id TEXT NOT NULL,config_source TEXT NOT NULL,config_source_id TEXT,policy_revision INTEGER NOT NULL,profile TEXT NOT NULL,penalty_threshold INTEGER NOT NULL,recovery_interval_minutes INTEGER NOT NULL,recovery_model TEXT,account_config_revision INTEGER NOT NULL,before_status TEXT NOT NULL,after_status TEXT NOT NULL,fallback_was_enabled INTEGER NOT NULL,super_priority_was_enabled INTEGER NOT NULL,started_at TEXT NOT NULL,recovery_due_at TEXT,recovery_lease_owner TEXT,recovery_lease_until TEXT,last_recovery_run_id TEXT,cleared_at TEXT,created_at TEXT NOT NULL,updated_at TEXT NOT NULL); CREATE TABLE model_quality_schedules(id TEXT PRIMARY KEY,system_account_id TEXT NOT NULL,account_id TEXT NOT NULL,model TEXT NOT NULL,interval_minutes INTEGER NOT NULL,profile TEXT NOT NULL,penalty_threshold INTEGER NOT NULL,penalty_action TEXT NOT NULL,recovery_interval_minutes INTEGER NOT NULL,enabled INTEGER NOT NULL,revision INTEGER NOT NULL,next_run_at TEXT NOT NULL,lease_owner TEXT,lease_until TEXT,last_run_id TEXT,last_run_at TEXT,last_run_status TEXT,created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}
