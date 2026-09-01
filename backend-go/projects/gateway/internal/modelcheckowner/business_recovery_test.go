package modelcheckowner

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestBusinessRecoveryApplierClearsOnlyMatchingLease(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,system_account_id TEXT,status TEXT,config_revision INTEGER,schedulable INTEGER,last_error_code TEXT,last_error_message TEXT,availability_schedule_json TEXT,deleted_at TEXT,updated_at TEXT)`,
		`CREATE TABLE account_quality_enforcements (account_id TEXT PRIMARY KEY,system_account_id TEXT,enforcement_id TEXT,generation INTEGER,state TEXT,action TEXT,policy_revision INTEGER,account_config_revision INTEGER,recovery_lease_owner TEXT,recovery_lease_until TEXT,last_recovery_run_id TEXT,recovery_due_at TEXT,cleared_at TEXT,updated_at TEXT)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('acct','sys','quality_isolated',5,0,'model_quality_failed','bad','',NULL,'')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_quality_enforcements VALUES ('acct','sys','enf',2,'active','quality_isolate',7,5,'gateway-1','2030-01-01T00:10:00Z',NULL,'2030-01-01T00:00:00Z',NULL,'')`); err != nil {
		t.Fatal(err)
	}
	applier, err := NewBusinessRecoveryApplier(db, false)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2030, 1, 1, 0, 1, 0, 0, time.UTC)
	input := RecoveryPayload{OwnerID: "gateway-1", AccountID: "acct", EnforcementID: "enf", RunID: "run-1", Generation: 2, PolicyRevision: 7, RecoveryIntervalMinutes: 10, CompletedAt: when}
	if err := applier.Complete(context.Background(), input, true); err != nil {
		t.Fatal(err)
	}
	var status, state string
	var revision, schedulable int
	if err := db.QueryRow(`SELECT status,config_revision,schedulable FROM accounts WHERE id='acct'`).Scan(&status, &revision, &schedulable); err != nil {
		t.Fatal(err)
	}
	if status != "active" || revision != 6 || schedulable != 1 {
		t.Fatalf("account status=%q revision=%d schedulable=%d", status, revision, schedulable)
	}
	if err := db.QueryRow(`SELECT state FROM account_quality_enforcements WHERE account_id='acct'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "cleared" {
		t.Fatalf("enforcement state=%q", state)
	}
	// The old lease cannot restore or reschedule a later state.
	if err := applier.Complete(context.Background(), input, true); err != nil {
		t.Fatal(err)
	}
}

func TestBusinessRecoveryApplierFailedProbeReschedules(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE account_quality_enforcements (account_id TEXT PRIMARY KEY,system_account_id TEXT,enforcement_id TEXT,generation INTEGER,state TEXT,action TEXT,policy_revision INTEGER,account_config_revision INTEGER,recovery_lease_owner TEXT,recovery_lease_until TEXT,last_recovery_run_id TEXT,recovery_due_at TEXT,cleared_at TEXT,updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_quality_enforcements VALUES ('acct','sys','enf',2,'active','quality_isolate',7,5,'gateway-1','2030-01-01T00:10:00Z',NULL,'2030-01-01T00:00:00Z',NULL,'')`); err != nil {
		t.Fatal(err)
	}
	applier, _ := NewBusinessRecoveryApplier(db, false)
	when := time.Date(2030, 1, 1, 0, 1, 0, 0, time.UTC)
	if err := applier.Complete(context.Background(), RecoveryPayload{OwnerID: "gateway-1", AccountID: "acct", EnforcementID: "enf", RunID: "run-failed", Generation: 2, PolicyRevision: 7, RecoveryIntervalMinutes: 10, CompletedAt: when}, false); err != nil {
		t.Fatal(err)
	}
	var state, owner, due, runID string
	if err := db.QueryRow(`SELECT state,COALESCE(recovery_lease_owner,''),recovery_due_at,last_recovery_run_id FROM account_quality_enforcements WHERE account_id='acct'`).Scan(&state, &owner, &due, &runID); err != nil {
		t.Fatal(err)
	}
	if state != "active" || owner != "" || runID != "run-failed" || due != when.Add(10*time.Minute).Format(time.RFC3339Nano) {
		t.Fatalf("state=%q owner=%q due=%q run=%q", state, owner, due, runID)
	}
}

func TestAvailabilityAllowedGatewayCrossDayAndStrictJSON(t *testing.T) {
	raw := `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"23:00","end":"01:00"}],"exceptions":[]}`
	allowed, err := availabilityAllowedGateway(raw, time.Date(2030, 1, 8, 0, 30, 0, 0, time.UTC)) // Tuesday, Monday window carries over.
	if err != nil || !allowed {
		t.Fatalf("cross-day allowed=%v err=%v", allowed, err)
	}
	if _, err := availabilityAllowedGateway(`{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[],"extra":true}`, time.Now()); err == nil {
		t.Fatal("unknown or invalid schedule must fail closed")
	}
}

func TestAvailabilityGatewayExceptionWindowSchema(t *testing.T) {
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
			_, err := availabilityAllowedGateway(fmt.Sprintf(base, tt.exception), time.Date(2030, 1, 8, 12, 0, 0, 0, time.UTC))
			if (err != nil) != tt.wantErr {
				t.Fatalf("error=%v, wantErr=%v", err, tt.wantErr)
			}
		})
	}
}
