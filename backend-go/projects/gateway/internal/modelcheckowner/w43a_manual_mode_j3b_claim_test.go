package modelcheckowner

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// W43-A（后台自动探测总开关 W3）：J3b 两条候选 SQL 排除手动账户（真实 PG，
// JUHE_AI_TEST_EXTERNAL=1）。契约源 docs/functions/AI账户后台自动探测总开关
// 设计.md §3：定时 schedule 与质量隔离恢复候选各加 background_probe_enabled
// 过滤；质量隔离恢复的授权实例同时检查来源账户开关（LEFT JOIN sa）。

// 定时 schedule 候选：手动账户（background_probe_enabled=0）不进入候选，
// 开启账户照常领取（默认路径不变）。
func TestW43AJ3bScheduledClaimExcludesManualModeAccounts(t *testing.T) {
	db := newJ3BPGDatabase(t, append(businessSchedulerAccountsDDL(), businessSchedulerSchedulesDDL(),
		`CREATE TABLE juhe_business.model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,manual_enforcement_enabled INTEGER,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,timeout_penalty_enabled BOOLEAN NOT NULL DEFAULT FALSE,custom_question_ids TEXT)`,
		`CREATE TABLE juhe_business.account_quality_enforcements (account_id TEXT PRIMARY KEY,system_account_id TEXT,enforcement_id TEXT,generation INTEGER,state TEXT,action TEXT,recovery_model TEXT,account_config_revision INTEGER,policy_revision INTEGER,config_source_id TEXT,profile TEXT,penalty_threshold INTEGER,recovery_interval_minutes INTEGER,recovery_due_at TEXT,recovery_lease_owner TEXT,recovery_lease_until TEXT,updated_at TEXT,config_source TEXT)`)...)
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := db.Exec(j3bQ(`INSERT INTO accounts (id,vendor_code,config_revision,deleted_at,authorization_instance_authorization_id,status,health_check_model,background_probe_enabled) VALUES ('acct-on','gpt',4,NULL,NULL,'active','gpt-5.6-sol',1)`)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(j3bQ(`INSERT INTO accounts (id,vendor_code,config_revision,deleted_at,authorization_instance_authorization_id,status,health_check_model,background_probe_enabled) VALUES ('acct-off','gpt',4,NULL,NULL,'active','gpt-5.6-sol',0)`)); err != nil {
		t.Fatal(err)
	}
	dueAt := now.Add(-time.Minute).Format(time.RFC3339Nano)
	if _, err := db.Exec(j3bQ(`INSERT INTO model_quality_schedules VALUES ('sch-on',3,'sys','acct-on','gpt-5.6-sol',60,'quick',70,'fallback',15,FALSE,1,?,NULL,NULL,NULL,NULL,NULL,'','[]')`), dueAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(j3bQ(`INSERT INTO model_quality_schedules VALUES ('sch-off',3,'sys','acct-off','gpt-5.6-sol',60,'quick',70,'fallback',15,FALSE,1,?,NULL,NULL,NULL,NULL,NULL,'','[]')`), dueAt); err != nil {
		t.Fatal(err)
	}
	store := schedulerStoreFixture(t)
	defer store.db.Close()
	source := &BusinessSchedulerSource{Business: db, Store: store, OwnerID: "gateway-1"}
	tasks, err := source.Claim(context.Background(), SchedulerScheduled, now, 10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%d err=%v，期望仅领取手动模式之外的 1 条", len(tasks), err)
	}
	var payload ScheduledPayload
	if err := json.Unmarshal(tasks[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.TargetID != "acct-on" || payload.ScheduleID != "sch-on" {
		t.Fatalf("领取了错误候选: target=%s schedule=%s", payload.TargetID, payload.ScheduleID)
	}
	var leaseOwner string
	if err := db.QueryRow(j3bQ(`SELECT COALESCE(lease_owner,'') FROM model_quality_schedules WHERE id='sch-off'`)).Scan(&leaseOwner); err != nil {
		t.Fatal(err)
	}
	if leaseOwner != "" {
		t.Fatalf("手动账户 schedule 不得被领取租约: owner=%q", leaseOwner)
	}
}

// 质量隔离恢复候选：手动账户排除；授权实例跟随来源账户开关（来源关闭的
// 实例排除，来源开启的实例照常领取）。
func TestW43AJ3bRecoveryClaimExcludesManualModeAndInstanceFollowsSource(t *testing.T) {
	db := newJ3BPGDatabase(t, append(businessSchedulerAccountsDDL(), businessSchedulerSchedulesDDL(),
		`CREATE TABLE juhe_business.model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,manual_enforcement_enabled INTEGER,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,timeout_penalty_enabled BOOLEAN NOT NULL DEFAULT FALSE,custom_question_ids TEXT)`,
		`CREATE TABLE juhe_business.account_quality_enforcements (account_id TEXT PRIMARY KEY,system_account_id TEXT,enforcement_id TEXT,generation INTEGER,state TEXT,action TEXT,recovery_model TEXT,account_config_revision INTEGER,policy_revision INTEGER,config_source_id TEXT,profile TEXT,penalty_threshold INTEGER,recovery_interval_minutes INTEGER,recovery_due_at TEXT,recovery_lease_owner TEXT,recovery_lease_until TEXT,updated_at TEXT,config_source TEXT)`)...)
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	dueAt := now.Add(-time.Minute).Format(time.RFC3339Nano)
	insertAccount := func(id string, probeEnabled int, source string) {
		t.Helper()
		if _, err := db.Exec(j3bQ(`INSERT INTO accounts (id,vendor_code,config_revision,deleted_at,authorization_instance_authorization_id,status,health_check_model,background_probe_enabled,dispatch_revision,authorization_instance_source_account_id) VALUES (?, 'gpt',8,NULL,NULL,'quality_isolated','gpt-5.6-sol',?,11, ?)`), id, probeEnabled, source); err != nil {
			t.Fatal(err)
		}
	}
	// 三个候选：owner 手动账户（排除）；来源关闭的授权实例（跟随来源排除）；
	// 来源开启的授权实例（领取）。
	insertAccount("acct-off", 0, "")
	insertAccount("inst-src-off", 1, "src-off")
	insertAccount("inst-src-on", 1, "src-on")
	insertAccount("src-off", 0, "")
	insertAccount("src-on", 1, "")
	insertEnforcement := func(accountID string) {
		t.Helper()
		if _, err := db.Exec(j3bQ(`INSERT INTO account_quality_enforcements VALUES (?, 'sys', 'enf-'||?, 2,'active','quality_isolate','',8,7,'sch','full',71,15,?,NULL,NULL,'','schedule')`), accountID, accountID, dueAt); err != nil {
			t.Fatal(err)
		}
	}
	insertEnforcement("acct-off")
	insertEnforcement("inst-src-off")
	insertEnforcement("inst-src-on")
	if _, err := db.Exec(j3bQ(`INSERT INTO model_quality_policies(system_account_id,revision,profile,manual_enforcement_enabled,penalty_threshold,penalty_action,recovery_interval_minutes,timeout_penalty_enabled,custom_question_ids) VALUES ('sys',7,'full',1,70,'quality_isolate',15,FALSE,'[]')`)); err != nil {
		t.Fatal(err)
	}
	store := schedulerStoreFixture(t)
	defer store.db.Close()
	source := &BusinessSchedulerSource{Business: db, Store: store, OwnerID: "gateway-1"}
	tasks, err := source.Claim(context.Background(), SchedulerQualityRecovery, now, 10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%d err=%v，期望仅领取来源开关开启的 1 条", len(tasks), err)
	}
	var payload ScheduledPayload
	if err := json.Unmarshal(tasks[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.TargetID != "inst-src-on" {
		t.Fatalf("领取了错误候选: target=%s", payload.TargetID)
	}
}
