package modelcheckowner

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestBusinessSchedulerClaimsScheduleAndCompletesWithLease(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,provider_code TEXT,config_revision INTEGER,deleted_at TEXT,authorization_instance_authorization_id TEXT,status TEXT,health_check_model TEXT)`,
		`CREATE TABLE model_quality_schedules (id TEXT PRIMARY KEY,revision INTEGER,system_account_id TEXT,account_id TEXT,model TEXT,interval_minutes INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,enabled INTEGER,next_run_at TEXT,lease_owner TEXT,lease_until TEXT,last_run_id TEXT,last_run_at TEXT,last_run_status TEXT,updated_at TEXT)`,
		`CREATE TABLE account_quality_enforcements (account_id TEXT PRIMARY KEY,system_account_id TEXT,enforcement_id TEXT,generation INTEGER,state TEXT,action TEXT,recovery_model TEXT,account_config_revision INTEGER,policy_revision INTEGER,config_source_id TEXT,profile TEXT,penalty_threshold INTEGER,recovery_interval_minutes INTEGER,recovery_due_at TEXT,recovery_lease_owner TEXT,recovery_lease_until TEXT,updated_at TEXT)`,
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
	if _, err := db.Exec(`INSERT INTO model_quality_schedules VALUES ('sch',3,'sys','acct','gpt-5.6-sol',60,'quick',70,'fallback',15,1,?,NULL,NULL,NULL,NULL,NULL,'')`, now.Add(-time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	store := schedulerStoreFixture(t)
	defer store.db.Close()
	source := &BusinessSchedulerSource{Business: db, Store: store, OwnerID: "gateway-1"}
	tasks, err := source.Claim(context.Background(), SchedulerScheduled, now, 10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
	var payload ScheduledPayload
	if err := json.Unmarshal(tasks[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ScheduleID != "sch" || payload.ScheduleRevision != 3 || payload.OwnerID != "gateway-1" || payload.ConfigRevision != "4" {
		t.Fatalf("payload=%+v", payload)
	}
	if err := source.CompleteScheduled(context.Background(), payload, RunResult{RunID: "run-1", Status: string(RunCompleted)}); err != nil {
		t.Fatal(err)
	}
	var owner, status, runID string
	if err := db.QueryRow(`SELECT COALESCE(lease_owner,''),last_run_status,last_run_id FROM model_quality_schedules WHERE id='sch'`).Scan(&owner, &status, &runID); err != nil {
		t.Fatal(err)
	}
	if owner != "" || status != "completed" || runID != "run-1" {
		t.Fatalf("owner=%q status=%q run=%q", owner, status, runID)
	}
	if err := source.CompleteScheduled(context.Background(), payload, RunResult{RunID: "run-stale", Status: string(RunCompleted)}); err == nil {
		t.Fatal("stale schedule lease must be rejected")
	}
}

func TestBusinessSchedulerClaimsRecoveryWithImmutableLeasePayload(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,provider_code TEXT,config_revision INTEGER,deleted_at TEXT,authorization_instance_authorization_id TEXT,status TEXT,health_check_model TEXT)`,
		`CREATE TABLE model_quality_schedules (id TEXT PRIMARY KEY,revision INTEGER,system_account_id TEXT,account_id TEXT,model TEXT,interval_minutes INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,enabled INTEGER,next_run_at TEXT,lease_owner TEXT,lease_until TEXT,last_run_id TEXT,last_run_at TEXT,last_run_status TEXT,updated_at TEXT)`,
		`CREATE TABLE account_quality_enforcements (account_id TEXT PRIMARY KEY,system_account_id TEXT,enforcement_id TEXT,generation INTEGER,state TEXT,action TEXT,recovery_model TEXT,account_config_revision INTEGER,policy_revision INTEGER,config_source_id TEXT,profile TEXT,penalty_threshold INTEGER,recovery_interval_minutes INTEGER,recovery_due_at TEXT,recovery_lease_owner TEXT,recovery_lease_until TEXT,updated_at TEXT)`,
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
	if _, err := db.Exec(`INSERT INTO accounts (id,provider_code,config_revision,deleted_at,authorization_instance_authorization_id,status,health_check_model) VALUES ('acct','openai',8,NULL,NULL,'quality_isolated','gpt-5.6-sol')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_quality_enforcements VALUES ('acct','sys','enf',2,'active','quality_isolate','',8,7,'sch','full',71,15,?,NULL,NULL,'')`, now.Add(-time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	store := schedulerStoreFixture(t)
	defer store.db.Close()
	source := &BusinessSchedulerSource{Business: db, Store: store, OwnerID: "gateway-1"}
	tasks, err := source.Claim(context.Background(), SchedulerQualityRecovery, now, 10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
	var payload ScheduledPayload
	if err := json.Unmarshal(tasks[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.EnforcementID != "enf" || payload.Generation != 2 || payload.RecoveryIntervalMinutes != 15 || payload.PolicyRevision != "7" || payload.ConfigRevision != "8" {
		t.Fatalf("payload=%+v", payload)
	}
}

func schedulerStoreFixture(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/j3b.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE model_check_scheduler_tasks (id TEXT PRIMARY KEY,kind TEXT,due_at TEXT,claim_owner TEXT,claim_until TEXT,fence_token INTEGER,state TEXT,last_error TEXT,completed_at TEXT,payload TEXT,updated_at TEXT)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return &Store{db: db, mode: "sqlite"}
}
