package modelcheckowner

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestBusinessQualityManagerPolicyAndScheduleCAS(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,manual_enforcement_enabled INTEGER,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,created_at TEXT,updated_at TEXT)`,
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,system_account_id TEXT,provider_code TEXT,provider_protocol_profile_id TEXT,deleted_at TEXT,authorization_instance_authorization_id TEXT)`,
		`CREATE TABLE account_supported_models (account_id TEXT,model TEXT)`,
		`CREATE TABLE account_model_mappings (account_id TEXT,source_model TEXT,source_endpoint_family TEXT,upstream_model TEXT,enabled INTEGER)`,
		`CREATE TABLE model_quality_schedules (id TEXT PRIMARY KEY,system_account_id TEXT,account_id TEXT,model TEXT,interval_minutes INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,enabled INTEGER,revision INTEGER,next_run_at TEXT,created_at TEXT,updated_at TEXT,last_run_id TEXT,last_run_at TEXT,last_run_status TEXT,UNIQUE(system_account_id,account_id))`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('acct','sys','openai','profile_openai_openai_v1',NULL,NULL)`); err != nil {
		t.Fatal(err)
	}
	m, err := NewBusinessQualityManager(db, false)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := m.Policy(context.Background(), "sys")
	if err != nil {
		t.Fatal(err)
	}
	if policy.Revision != 0 || policy.PenaltyAction != "fallback" {
		t.Fatalf("default policy=%+v", policy)
	}
	profile := "full"
	threshold := 81
	policy, err = m.PatchPolicy(context.Background(), "sys", QualityPolicyPatch{ExpectedRevision: 0, Profile: &profile, PenaltyThreshold: &threshold})
	if err != nil {
		t.Fatal(err)
	}
	if policy.Revision != 1 || policy.Profile != "full" || policy.PenaltyThreshold != 81 {
		t.Fatalf("saved policy=%+v", policy)
	}
	if _, err := m.PatchPolicy(context.Background(), "sys", QualityPolicyPatch{ExpectedRevision: 0, Profile: &profile}); err == nil {
		t.Fatal("stale policy revision must be rejected")
	}
	schedule, err := m.CreateSchedule(context.Background(), "sys", QualityScheduleInput{AccountID: "acct", Model: "gpt-5.6-sol", IntervalMinutes: 60, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreateSchedule(context.Background(), "sys", QualityScheduleInput{AccountID: "acct", Model: "gpt-5.6-sol", IntervalMinutes: 60, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10}); err == nil {
		t.Fatal("duplicate account schedule must be rejected")
	}
	if _, err := m.CreateSchedule(context.Background(), "sys", QualityScheduleInput{AccountID: "acct", Model: "gpt-5.6-terra", IntervalMinutes: 1, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10}); err == nil {
		t.Fatal("schedule interval below the contract minimum must be rejected")
	}
	list, err := m.ListSchedules(context.Background(), "sys", 1, 50)
	if err != nil || list.Total != 1 || len(list.Items) != 1 {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	interval := 120
	updated, err := m.PatchSchedule(context.Background(), "sys", schedule.ID, QualitySchedulePatch{ExpectedRevision: schedule.Revision, IntervalMinutes: &interval})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != schedule.Revision+1 || updated.IntervalMinutes != 120 {
		t.Fatalf("updated=%+v", updated)
	}
	unchanged, err := m.PatchSchedule(context.Background(), "sys", schedule.ID, QualitySchedulePatch{ExpectedRevision: updated.Revision, IntervalMinutes: &interval})
	if err != nil || unchanged.Revision != updated.Revision {
		t.Fatalf("unchanged=%+v err=%v", unchanged, err)
	}
	if _, err := m.PatchSchedule(context.Background(), "sys", schedule.ID, QualitySchedulePatch{ExpectedRevision: schedule.Revision, IntervalMinutes: &interval}); err == nil {
		t.Fatal("stale schedule revision must be rejected")
	}
	deleted, err := m.DeleteSchedule(context.Background(), "sys", schedule.ID)
	if err != nil || !deleted {
		t.Fatalf("deleted=%v err=%v", deleted, err)
	}
}
