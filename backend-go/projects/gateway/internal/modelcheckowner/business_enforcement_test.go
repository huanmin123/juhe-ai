package modelcheckowner

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestBusinessEnforcementApplierUsesRevisionCASAndAtomicUpsert(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,system_account_id TEXT,status TEXT,config_revision INTEGER,fallback_enabled INTEGER,super_priority_enabled INTEGER,deleted_at TEXT,schedulable INTEGER,last_error_code TEXT,last_error_message TEXT,updated_at TEXT)`,
		`CREATE TABLE account_quality_enforcements (account_id TEXT PRIMARY KEY,system_account_id TEXT,enforcement_id TEXT UNIQUE,generation INTEGER,state TEXT,action TEXT,trigger_run_id TEXT,config_source TEXT,config_source_id TEXT,policy_revision INTEGER,profile TEXT,penalty_threshold INTEGER,recovery_interval_minutes INTEGER,account_config_revision INTEGER,before_status TEXT,after_status TEXT,fallback_was_enabled INTEGER,super_priority_was_enabled INTEGER,started_at TEXT,recovery_due_at TEXT,created_at TEXT,updated_at TEXT,cleared_at TEXT)`,
		`CREATE TABLE model_quality_schedules (id TEXT PRIMARY KEY,system_account_id TEXT,account_id TEXT,revision INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,model TEXT)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('acct-1','sys-1','active',4,0,1,NULL,1,NULL,NULL,NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_quality_schedules VALUES ('sch-1','sys-1','acct-1',7,'full',70,'quality_isolate',15,'')`); err != nil {
		t.Fatal(err)
	}
	applier, err := NewBusinessEnforcementApplier(db, false)
	if err != nil {
		t.Fatal(err)
	}
	input := QualityEnforcement{AccountID: "acct-1", SystemAccountID: "sys-1", RunID: "run-1", PolicyRevision: "7", AccountConfigRevision: "4", ScheduleID: "sch-1", Action: "quality_isolate", Score: 20, Threshold: 70, RecoveryIntervalMinutes: 15, Profile: "full"}
	if err := applier.Apply(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	var status string
	var revision int
	if err := db.QueryRow(`SELECT status,config_revision FROM accounts WHERE id='acct-1'`).Scan(&status, &revision); err != nil {
		t.Fatal(err)
	}
	if status != "quality_isolated" || revision != 5 {
		t.Fatalf("status=%q revision=%d", status, revision)
	}
	var action string
	if err := db.QueryRow(`SELECT action FROM account_quality_enforcements WHERE account_id='acct-1'`).Scan(&action); err != nil {
		t.Fatal(err)
	}
	if action != "quality_isolate" {
		t.Fatalf("action=%q", action)
	}
	if err := applier.Apply(context.Background(), QualityEnforcement{AccountID: "acct-1", SystemAccountID: "sys-1", RunID: "run-1", PolicyRevision: "7", AccountConfigRevision: "4", ScheduleID: "sch-1", Action: "quality_isolate", Score: 20, Threshold: 70, RecoveryIntervalMinutes: 15, Profile: "full"}); err != nil {
		t.Fatalf("same-run health retry should be idempotent: %v", err)
	}
	input.RunID = "run-stale"
	input.AccountConfigRevision = "4"
	if err := applier.Apply(context.Background(), input); err == nil {
		t.Fatal("stale revision must be rejected")
	}
}
