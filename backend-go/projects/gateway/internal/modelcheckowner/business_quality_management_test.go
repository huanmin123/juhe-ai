package modelcheckowner

import (
	"context"
	"strings"
	"testing"

)

func TestBusinessQualityManagerPolicyAndScheduleCAS(t *testing.T) {
	db, err := openPGTestDB(t)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,manual_enforcement_enabled INTEGER,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,timeout_penalty_enabled BOOLEAN NOT NULL DEFAULT FALSE,created_at TEXT,updated_at TEXT,custom_question_ids TEXT)`,
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,system_account_id TEXT,name TEXT,vendor_code TEXT,connection_driver_code TEXT,health_check_connection_id TEXT,deleted_at TEXT,authorization_instance_authorization_id TEXT,status TEXT NOT NULL DEFAULT 'active')`,
		`CREATE TABLE account_connections (id TEXT PRIMARY KEY,account_id TEXT,connection_preset_code TEXT,base_url TEXT,enabled INTEGER)`,
		`CREATE TABLE models (id TEXT PRIMARY KEY,vendor_code TEXT,public_model TEXT,upstream_model TEXT,scope TEXT,system_account_id TEXT,status TEXT,catalog_visible BOOLEAN)`,
		`CREATE TABLE account_supported_models (account_id TEXT,model_id TEXT,PRIMARY KEY (account_id, model_id))`,
		`CREATE TABLE account_entry_mappings (id TEXT PRIMARY KEY,account_id TEXT,source_model TEXT,source_endpoint_family TEXT,target_connection_id TEXT,target_model_id TEXT,target_endpoint_family TEXT,enabled INTEGER)`,
		`CREATE TABLE account_quality_enforcements (account_id TEXT PRIMARY KEY,action TEXT,state TEXT,recovery_due_at TEXT)`,
		`CREATE TABLE model_quality_schedules (id TEXT PRIMARY KEY,system_account_id TEXT,account_id TEXT,model TEXT,interval_minutes INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,timeout_penalty_enabled BOOLEAN NOT NULL DEFAULT FALSE,enabled INTEGER,revision INTEGER,next_run_at TEXT,lease_owner TEXT,lease_until TEXT,created_at TEXT,updated_at TEXT,last_run_id TEXT,last_run_at TEXT,last_run_status TEXT,custom_question_ids TEXT,UNIQUE(system_account_id,account_id))`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	w11eExec(t, db, `INSERT INTO accounts (id,system_account_id,name,vendor_code,connection_driver_code,health_check_connection_id,status) VALUES ('acct','sys','Primary account','gpt','gpt','conn-acct','active')`)
	w11eExec(t, db, `INSERT INTO account_connections (id,account_id,connection_preset_code,base_url,enabled) VALUES ('conn-acct','acct','gpt_openai_v1','https://quality.invalid/v1', 1)`)
	m, err := NewBusinessQualityManager(db)
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
	if _, err := db.Exec(`INSERT INTO account_quality_enforcements(account_id,action,state,recovery_due_at) VALUES ('acct','quality_isolate','active','2026-08-28T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	w11eExec(t, db, `INSERT INTO accounts (id,system_account_id,name,vendor_code,connection_driver_code,health_check_connection_id,deleted_at,status) VALUES ('acct-deleted','sys','Deleted account','gpt','gpt','conn-acct-deleted','2026-08-28T12:00:00Z','active')`)
	w11eExec(t, db, `INSERT INTO account_connections (id,account_id,connection_preset_code,base_url,enabled) VALUES ('conn-acct-deleted','acct-deleted','gpt_openai_v1','https://quality.invalid/v1', 1)`)
	if _, err := db.Exec(`INSERT INTO model_quality_schedules(id,system_account_id,account_id,model,interval_minutes,profile,penalty_threshold,penalty_action,recovery_interval_minutes,enabled,revision,next_run_at,created_at,updated_at) VALUES ('mqs-deleted','sys','acct-deleted','gpt-5.6-sol',60,'quick',70,'fallback',10, 1,1,'2026-08-28T13:00:00Z','2026-08-28T12:00:00Z','2026-08-28T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	withAccount, err := m.ListSchedules(context.Background(), "sys", 1, 50)
	if err != nil || withAccount.Total != 1 || len(withAccount.Items) != 1 {
		t.Fatalf("schedule enriched list=%+v err=%v", withAccount, err)
	}
	if got := withAccount.Items[0]; got.AccountName != "Primary account" || got.ProviderCode != "gpt" || got.CurrentEnforcementAction != "quality_isolate" || got.CurrentEnforcementRecoveryDueAt != "2026-08-28T12:00:00Z" || got.CreatedAt == "" || got.UpdatedAt == "" {
		t.Fatalf("schedule DTO enrichment=%+v", got)
	}
	if _, err := m.CreateSchedule(context.Background(), "sys", QualityScheduleInput{AccountID: "acct", Model: "gpt-5.6-sol", IntervalMinutes: 60, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10}); err == nil {
		t.Fatal("duplicate account schedule must be rejected")
	}
	if _, err := m.CreateSchedule(context.Background(), "sys", QualityScheduleInput{AccountID: "acct", Model: "gpt-5.6-terra", IntervalMinutes: 0, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10}); err == nil {
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
	// 2026-10-11 下限收紧：1..4 分钟一律拒绝，5 分钟为最低档（字段级更新走同一校验）。
	below := 4
	if _, err := m.PatchSchedule(context.Background(), "sys", schedule.ID, QualitySchedulePatch{ExpectedRevision: updated.Revision, IntervalMinutes: &below}); err == nil || !strings.Contains(err.Error(), "5 到 10080") {
		t.Fatalf("间隔 4 分钟必须报下限错误: %v", err)
	}
	floor := 5
	floored, err := m.PatchSchedule(context.Background(), "sys", schedule.ID, QualitySchedulePatch{ExpectedRevision: updated.Revision, IntervalMinutes: &floor})
	if err != nil {
		t.Fatalf("间隔 5 分钟为合法下限: %v", err)
	}
	if floored.IntervalMinutes != 5 {
		t.Fatalf("interval=%d, want 5", floored.IntervalMinutes)
	}
	// 同值 patch 是无操作：revision 不前进（原 D2 幂等语义；interval 下限
	// 1 分钟的旧契约已由上方 4 拒绝 / 5 合法的收紧回归取代）。
	unchanged, err := m.PatchSchedule(context.Background(), "sys", schedule.ID, QualitySchedulePatch{ExpectedRevision: floored.Revision, IntervalMinutes: &floor})
	if err != nil || unchanged.Revision != floored.Revision {
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
