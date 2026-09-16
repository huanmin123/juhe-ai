package modelcheckquality

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// w12a_quality_errors_test.go 覆盖 CAS 链路在 schema 漂移（缺表/缺列）下的
// fail-closed 分支、可用性调度解析错误分支，以及 postgres 方言臂（w1cover 门禁）。

func w12aMemoryDB(t *testing.T, schema string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if schema != "" {
		if _, err := db.Exec(schema); err != nil {
			t.Fatalf("fixture 建表失败: %v", err)
		}
	}
	return db
}

func w12aCtxCanceled(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	t.Cleanup(cancel)
	return ctx
}

// ---- BeginTx / 事务前错误 ----

func TestW12aApplyEnforcementBeginTxError(t *testing.T) {
	db := w12aQualityDB(t)
	if _, err := ApplyEnforcement(w12aCtxCanceled(t), db, false, w12aEnforcementInput()); err == nil || !strings.Contains(err.Error(), "begin quality enforcement") {
		t.Fatalf("取消上下文应使事务开启失败: %v", err)
	}
}

func TestW12aCompleteRecoveryBeginTxError(t *testing.T) {
	db := w12aQualityDB(t)
	if _, err := CompleteRecovery(w12aCtxCanceled(t), db, false, w12aRecoveryCompletionInput()); err == nil {
		t.Fatalf("取消上下文应使事务开启失败")
	}
}

// ---- ApplyEnforcement 的读链错误分支 ----

// w12aPartialQualitySchema 建齐策略表但 accounts 缺 UPDATE 所需列，
// 使 SELECT/UPDATE 在各自阶段失败，覆盖 fail-closed 返回。
func TestW12aApplyEnforcementAccountReadError(t *testing.T) {
	db := w12aMemoryDB(t, `
CREATE TABLE model_quality_policies(system_account_id TEXT PRIMARY KEY, revision INTEGER, profile TEXT, penalty_threshold INTEGER, penalty_action TEXT, recovery_interval_minutes INTEGER);
CREATE TABLE accounts(id TEXT PRIMARY KEY, system_account_id TEXT, status TEXT, deleted_at TEXT);
`)
	w12aMustExec(t, db, `INSERT INTO model_quality_policies(system_account_id,revision,profile,penalty_threshold,penalty_action,recovery_interval_minutes) VALUES('w12a-sys',0,'quick',70,'quality_isolate',15)`)
	w12aMustExec(t, db, `INSERT INTO accounts(id,system_account_id,status,deleted_at) VALUES('w12a-acc','w12a-sys','active',NULL)`)
	if _, err := ApplyEnforcement(context.Background(), db, false, w12aEnforcementInput()); err == nil {
		t.Fatalf("accounts 缺列应报读取错误")
	}
}

func TestW12aApplyEnforcementPriorReadError(t *testing.T) {
	// accounts 可读但 enforcement 表缺 enforcement_id 列 → prior 读错误。
	db := w12aMemoryDB(t, `
CREATE TABLE model_quality_policies(system_account_id TEXT PRIMARY KEY, revision INTEGER, profile TEXT, penalty_threshold INTEGER, penalty_action TEXT, recovery_interval_minutes INTEGER);
CREATE TABLE accounts(id TEXT PRIMARY KEY, system_account_id TEXT, status TEXT, fallback_enabled INTEGER, super_priority_enabled INTEGER, authorization_instance_authorization_id TEXT, config_revision INTEGER, last_error_code TEXT, last_error_message TEXT, updated_at TEXT, deleted_at TEXT);
CREATE TABLE account_quality_enforcements(account_id TEXT, generation INTEGER, state TEXT, trigger_run_id TEXT);
`)
	w12aMustExec(t, db, `INSERT INTO model_quality_policies(system_account_id,revision,profile,penalty_threshold,penalty_action,recovery_interval_minutes) VALUES('w12a-sys',0,'quick',70,'quality_isolate',15)`)
	w12aMustExec(t, db, `INSERT INTO accounts(id,system_account_id,status,fallback_enabled,super_priority_enabled,authorization_instance_authorization_id,config_revision,deleted_at) VALUES('w12a-acc','w12a-sys','active',0,0,NULL,5,NULL)`)
	if _, err := ApplyEnforcement(context.Background(), db, false, w12aEnforcementInput()); err == nil {
		t.Fatalf("prior 读错误应返回错误")
	}
}

func TestW12aApplyEnforcementUpdateAccountsError(t *testing.T) {
	// accounts 缺 last_error_code：prior 无冲突行，UPDATE 阶段失败。
	db := w12aMemoryDB(t, `
CREATE TABLE model_quality_policies(system_account_id TEXT PRIMARY KEY, revision INTEGER, profile TEXT, penalty_threshold INTEGER, penalty_action TEXT, recovery_interval_minutes INTEGER);
CREATE TABLE accounts(id TEXT PRIMARY KEY, system_account_id TEXT, status TEXT, fallback_enabled INTEGER, super_priority_enabled INTEGER, authorization_instance_authorization_id TEXT, config_revision INTEGER, schedulable INTEGER, updated_at TEXT, deleted_at TEXT);
CREATE TABLE account_quality_enforcements(account_id TEXT PRIMARY KEY, system_account_id TEXT, enforcement_id TEXT, generation INTEGER, state TEXT, action TEXT, trigger_run_id TEXT);
`)
	w12aMustExec(t, db, `INSERT INTO model_quality_policies(system_account_id,revision,profile,penalty_threshold,penalty_action,recovery_interval_minutes) VALUES('w12a-sys',0,'quick',70,'quality_isolate',15)`)
	w12aMustExec(t, db, `INSERT INTO accounts(id,system_account_id,status,fallback_enabled,super_priority_enabled,authorization_instance_authorization_id,config_revision,schedulable,deleted_at) VALUES('w12a-acc','w12a-sys','active',0,0,NULL,5,1,NULL)`)
	if _, err := ApplyEnforcement(context.Background(), db, false, w12aEnforcementInput()); err == nil {
		t.Fatalf("accounts UPDATE 缺列应报错")
	}
}

// ---- CompleteRecovery 的读链错误分支 ----

func TestW12aCompleteRecoveryEnforcementReadError(t *testing.T) {
	db := w12aMemoryDB(t, `
CREATE TABLE account_quality_enforcements(account_id TEXT, enforcement_id TEXT, generation INTEGER, recovery_lease_owner TEXT);
`)
	input := w12aRecoveryCompletionInput()
	if _, err := CompleteRecovery(context.Background(), db, false, input); err == nil {
		t.Fatalf("enforcement 缺列应报读取错误")
	}
}

func TestW12aCompleteRecoveryRescheduleErrorBranches(t *testing.T) {
	// enforcement 缺 last_recovery_run_id：reschedule UPDATE 必失败。
	// 三个入口：策略漂移、探针未达标、账户配置漂移。
	build := func(t *testing.T) *sql.DB {
		t.Helper()
		db := w12aMemoryDB(t, `
CREATE TABLE account_quality_enforcements(account_id TEXT PRIMARY KEY, system_account_id TEXT, enforcement_id TEXT, generation INTEGER, state TEXT, action TEXT, policy_revision INTEGER, account_config_revision INTEGER, recovery_lease_owner TEXT, recovery_lease_until TEXT, recovery_due_at TEXT, updated_at TEXT);
CREATE TABLE accounts(id TEXT PRIMARY KEY, system_account_id TEXT, status TEXT, config_revision INTEGER, availability_schedule_json TEXT, deleted_at TEXT);
`)
		w12aMustExec(t, db, `INSERT INTO account_quality_enforcements(account_id,system_account_id,enforcement_id,generation,state,action,policy_revision,account_config_revision,recovery_lease_owner,recovery_lease_until) VALUES('w12a-acc','w12a-sys','w12a-enf',2,'active','quality_isolate',3,5,'w12a-owner','2999-01-01T00:00:00Z')`)
		w12aMustExec(t, db, `INSERT INTO accounts(id,system_account_id,status,config_revision,availability_schedule_json,deleted_at) VALUES('w12a-acc','w12a-sys','quality_isolated',5,' ',NULL)`)
		return db
	}

	drifted := build(t)
	input := w12aRecoveryCompletionInput()
	input.PolicyRevision = 99
	if _, err := CompleteRecovery(context.Background(), drifted, false, input); err == nil {
		t.Fatalf("策略漂移 + reschedule 失败应报错")
	}

	failed := build(t)
	input = w12aRecoveryCompletionInput()
	input.Passed = false
	if _, err := CompleteRecovery(context.Background(), failed, false, input); err == nil {
		t.Fatalf("未达标 + reschedule 失败应报错")
	}

	configDrifted := build(t)
	w12aMustExec(t, configDrifted, `UPDATE accounts SET config_revision=6`)
	if _, err := CompleteRecovery(context.Background(), configDrifted, false, w12aRecoveryCompletionInput()); err == nil {
		t.Fatalf("配置漂移 + reschedule 失败应报错")
	}
}

func TestW12aCompleteRecoveryAccountReadAndUpdateError(t *testing.T) {
	// accounts 缺 config_revision：SELECT 失败。
	readErr := w12aMemoryDB(t, `
CREATE TABLE account_quality_enforcements(account_id TEXT PRIMARY KEY, system_account_id TEXT, enforcement_id TEXT, generation INTEGER, state TEXT, action TEXT, policy_revision INTEGER, account_config_revision INTEGER, recovery_lease_owner TEXT, recovery_lease_until TEXT);
CREATE TABLE accounts(id TEXT PRIMARY KEY, system_account_id TEXT, status TEXT, availability_schedule_json TEXT, deleted_at TEXT);
`)
	w12aMustExec(t, readErr, `INSERT INTO account_quality_enforcements(account_id,system_account_id,enforcement_id,generation,state,action,policy_revision,account_config_revision,recovery_lease_owner,recovery_lease_until) VALUES('w12a-acc','w12a-sys','w12a-enf',2,'active','quality_isolate',3,5,'w12a-owner','2999-01-01T00:00:00Z')`)
	w12aMustExec(t, readErr, `INSERT INTO accounts(id,system_account_id,status,availability_schedule_json,deleted_at) VALUES('w12a-acc','w12a-sys','quality_isolated',' ',NULL)`)
	if _, err := CompleteRecovery(context.Background(), readErr, false, w12aRecoveryCompletionInput()); err == nil {
		t.Fatalf("accounts 缺列应报读取错误")
	}

	// accounts 缺 last_error_code：恢复 UPDATE 失败。
	updateErr := w12aMemoryDB(t, `
CREATE TABLE account_quality_enforcements(account_id TEXT PRIMARY KEY, system_account_id TEXT, enforcement_id TEXT, generation INTEGER, state TEXT, action TEXT, policy_revision INTEGER, account_config_revision INTEGER, recovery_lease_owner TEXT, recovery_lease_until TEXT, last_recovery_run_id TEXT);
CREATE TABLE accounts(id TEXT PRIMARY KEY, system_account_id TEXT, status TEXT, config_revision INTEGER, schedulable INTEGER, availability_schedule_json TEXT, updated_at TEXT, deleted_at TEXT);
`)
	w12aMustExec(t, updateErr, `INSERT INTO account_quality_enforcements(account_id,system_account_id,enforcement_id,generation,state,action,policy_revision,account_config_revision,recovery_lease_owner,recovery_lease_until) VALUES('w12a-acc','w12a-sys','w12a-enf',2,'active','quality_isolate',3,5,'w12a-owner','2999-01-01T00:00:00Z')`)
	w12aMustExec(t, updateErr, `INSERT INTO accounts(id,system_account_id,status,config_revision,schedulable,availability_schedule_json,deleted_at) VALUES('w12a-acc','w12a-sys','quality_isolated',5,1,' ',NULL)`)
	if _, err := CompleteRecovery(context.Background(), updateErr, false, w12aRecoveryCompletionInput()); err == nil {
		t.Fatalf("恢复 UPDATE 缺列应报错")
	}
}

// ---- ClaimDueSchedules 扫描与更新错误 ----

func TestW12aClaimDueSchedulesScanAndUpdateErrors(t *testing.T) {
	// revision 为 NULL → 扫描失败。
	scanErr := w12aMemoryDB(t, `
CREATE TABLE model_quality_schedules(id TEXT, revision INTEGER, system_account_id TEXT, account_id TEXT, model TEXT, interval_minutes INTEGER, profile TEXT, penalty_threshold INTEGER, penalty_action TEXT, recovery_interval_minutes INTEGER, enabled INTEGER, next_run_at TEXT, lease_until TEXT);
CREATE TABLE accounts(id TEXT PRIMARY KEY, deleted_at TEXT, authorization_instance_authorization_id TEXT, status TEXT);
`)
	w12aMustExec(t, scanErr, `INSERT INTO model_quality_schedules(id,revision,system_account_id,account_id,model,interval_minutes,profile,penalty_threshold,penalty_action,recovery_interval_minutes,enabled,next_run_at) VALUES('w12a-sched',NULL,'w12a-sys','w12a-acc','m',60,'quick',70,'fallback',20,1,'2026-09-16T00:00:00Z')`)
	w12aMustExec(t, scanErr, `INSERT INTO accounts(id,deleted_at,authorization_instance_authorization_id,status) VALUES('w12a-acc',NULL,NULL,'active')`)
	if _, err := ClaimDueSchedules(context.Background(), scanErr, false, ScheduledClaimInput{OwnerID: "w12a-owner", Now: w12aNow, Limit: 5, Lease: time.Hour}); err == nil {
		t.Fatalf("NULL revision 应触发扫描失败")
	}

	// 缺 lease_owner 列 → 租约 UPDATE 失败。
	updateErr := w12aMemoryDB(t, `
CREATE TABLE model_quality_schedules(id TEXT, revision INTEGER, system_account_id TEXT, account_id TEXT, model TEXT, interval_minutes INTEGER, profile TEXT, penalty_threshold INTEGER, penalty_action TEXT, recovery_interval_minutes INTEGER, enabled INTEGER, next_run_at TEXT, lease_until TEXT, updated_at TEXT);
CREATE TABLE accounts(id TEXT PRIMARY KEY, deleted_at TEXT, authorization_instance_authorization_id TEXT, status TEXT);
`)
	w12aMustExec(t, updateErr, `INSERT INTO model_quality_schedules(id,revision,system_account_id,account_id,model,interval_minutes,profile,penalty_threshold,penalty_action,recovery_interval_minutes,enabled,next_run_at) VALUES('w12a-sched',3,'w12a-sys','w12a-acc','m',60,'quick',70,'fallback',20,1,'2026-09-16T00:00:00Z')`)
	w12aMustExec(t, updateErr, `INSERT INTO accounts(id,deleted_at,authorization_instance_authorization_id,status) VALUES('w12a-acc',NULL,NULL,'active')`)
	if _, err := ClaimDueSchedules(context.Background(), updateErr, false, ScheduledClaimInput{OwnerID: "w12a-owner", Now: w12aNow, Limit: 5, Lease: time.Hour}); err == nil {
		t.Fatalf("租约 UPDATE 缺列应报错")
	}
}

// ---- 可用性调度：例外解析与窗口判断错误分支 ----

func TestW12aExceptionUnmarshalErrorBranches(t *testing.T) {
	if err := json.Unmarshal([]byte(`{"date":"2026-09-10","action":"deny",`), new(availabilityException)); err == nil {
		t.Fatalf("截断 JSON 应报错")
	}
	// 合法的第二个 JSON 值 → trailing content 错误分支。
	// Unmarshal 的整体校验会先拒绝尾随流，因此直接调用 UnmarshalJSON。
	if err := new(availabilityException).UnmarshalJSON([]byte(`{"date":"2026-09-10","action":"deny"}{}`)); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("尾随内容应报错: %v", err)
	}
	// 第二个值非法 → 原样返回解析错误。
	if err := new(availabilityException).UnmarshalJSON([]byte(`{"date":"2026-09-10","action":"deny"} trailing`)); err == nil || strings.Contains(err.Error(), "trailing") {
		t.Fatalf("非法尾随应原样返回错误: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"date":"2026-09-10","action":"allow","windows":[{"start":"09:00","end":"10:00"}]}`), new(availabilityException)); err != nil {
		t.Fatalf("合法例外不应报错: %v", err)
	}
	if err := new(availabilityException).UnmarshalJSON([]byte(`{"date":"2026-09-10","action":"allow","windows":[{"start":"09:00","end":"10:00"}]}{}`)); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("窗口尾随内容应报错: %v", err)
	}
	if err := new(availabilityException).UnmarshalJSON([]byte(`{"date":"2026-09-10","action":"allow","windows":[{"start":"09:00","end":"10:00","unknown":1}]}`)); err == nil {
		t.Fatalf("窗口未知字段应报错")
	}
	// windows 显式 null：字段出现即视为"提供"，但内容为 null 不再解析窗口。
	var exception availabilityException
	if err := json.Unmarshal([]byte(`{"date":"2026-09-10","action":"deny","windows":null}`), &exception); err != nil {
		t.Fatalf("null 窗口应可解析: %v", err)
	}
	if !exception.windowsPresent || exception.Windows != nil {
		t.Fatalf("显式 null 应记为已提供且窗口为空: %#v", exception)
	}
}

func TestW12aAvailabilityAllowedErrorBranches(t *testing.T) {
	// 尾随内容。
	if _, err := availabilityAllowed(`{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"00:00","end":"23:00"}]} extra`, w12aNow); err == nil {
		t.Fatalf("尾随内容应报错")
	}
	// 模式非法。
	if _, err := availabilityAllowed(`{"enabled":true,"timezone":"UTC","mode":"deny_all","windows":[{"daysOfWeek":[1],"start":"00:00","end":"23:00"}]}`, w12aNow); err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("非法模式应报错: %v", err)
	}
	// 时区非法。
	if _, err := availabilityAllowed(`{"enabled":true,"timezone":"Not/AZone","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"00:00","end":"23:00"}]}`, w12aNow); err == nil || !strings.Contains(err.Error(), "timezone") {
		t.Fatalf("非法时区应报错: %v", err)
	}
	// 窗口校验透传（缺天）。
	if _, err := availabilityAllowed(`{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"start":"00:00","end":"23:00"}]}`, w12aNow); err == nil {
		t.Fatalf("缺天窗口应报错")
	}
	// allow 例外带非法窗口 → 校验错误。
	schedule := `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"00:00","end":"23:59"}],"exceptions":[{"date":"2026-09-17","action":"allow","windows":[{"start":"09:00","end":"09:00"}]}]}`
	if _, err := availabilityAllowed(schedule, w12aNow); err == nil {
		t.Fatalf("例外窗口起止相同应报错")
	}
	// 超过 32 个例外窗口 → invalid availability exception。
	exceptionWindows := make([]string, 33)
	for i := range exceptionWindows {
		exceptionWindows[i] = `{"start":"01:00","end":"02:00"}`
	}
	tooMany := `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"00:00","end":"23:00"}],"exceptions":[{"date":"2026-09-17","action":"allow","windows":[` + strings.Join(exceptionWindows, ",") + `]}]}`
	if _, err := availabilityAllowed(tooMany, w12aNow); err == nil || !strings.Contains(err.Error(), "exception") {
		t.Fatalf("超限例外窗口应报错: %v", err)
	}
}

func TestW12aAvailabilityWindowsEdgeBranches(t *testing.T) {
	// 跨夜窗口：UTC 00:30 命中前一天的 22:00-06:00。
	night := w12aNow.Add(-11*time.Hour - 30*time.Minute)
	allowed, err := availabilityAllowed(`{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"22:00","end":"06:00"}]}`, night)
	if err != nil || !allowed {
		t.Fatalf("跨夜窗口应允许: %v %v", allowed, err)
	}
	// 日期范围排除今天 → 不允许。
	denied, err := availabilityAllowed(`{"enabled":true,"timezone":"UTC","mode":"allow_windows","dateRange":{"startDate":"2026-10-01"},"windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"00:00","end":"23:59"}]}`, w12aNow)
	if err != nil || denied {
		t.Fatalf("日期范围外应不允许: %v %v", denied, err)
	}
	// allow 例外窗口不含当前分钟 → 不允许。
	denied, err = availabilityAllowed(`{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"00:00","end":"23:59"}],"exceptions":[{"date":"2026-09-17","action":"allow","windows":[{"start":"01:00","end":"02:00"}]}]}`, w12aNow)
	if err != nil || denied {
		t.Fatalf("例外窗口外应不允许: %v %v", denied, err)
	}
}

// ---- postgres 方言臂（w1cover 门禁）----

func w12aW1CoverDSN(t *testing.T) string {
	t.Helper()
	rawBytes, err := os.ReadFile(`F:\sub2api-lite\.local\project-resources\dev\env\shared.env`)
	raw := string(rawBytes)
	if err != nil {
		t.Skip("w12a PG gated: shared.env 不可读")
	}
	base := ""
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "JUHE_AI_POSTGRES_URL=") {
			base = strings.TrimPrefix(line, "JUHE_AI_POSTGRES_URL=")
		}
	}
	if base == "" {
		t.Skip("w12a PG gated: shared.env 缺少 JUHE_AI_POSTGRES_URL")
	}
	base = strings.Replace(base, ":6432/", ":5432/", 1)
	base = strings.Replace(base, "/juhe_ai_sub2api_dev?", "/juhe_ai_sub2api_dev_w1cover?", 1)
	return base
}

// ---- 补充分支：schedule 配置比对、INSERT 阶段、ClaimDue 更新阶段 ----

func TestW12aApplyEnforcementScheduleCompareBranches(t *testing.T) {
	// schedule 行存在但字段不匹配 → 匹配表达式求值为 false → stale。
	db := w12aMemoryDB(t, `
CREATE TABLE model_quality_policies(system_account_id TEXT PRIMARY KEY, revision INTEGER, profile TEXT, penalty_threshold INTEGER, penalty_action TEXT, recovery_interval_minutes INTEGER);
CREATE TABLE model_quality_schedules(id TEXT PRIMARY KEY, system_account_id TEXT, account_id TEXT, revision INTEGER, profile TEXT, penalty_threshold INTEGER, penalty_action TEXT, recovery_interval_minutes INTEGER, model TEXT);
CREATE TABLE accounts(id TEXT PRIMARY KEY, system_account_id TEXT, config_revision INTEGER, status TEXT, schedulable INTEGER, fallback_enabled INTEGER, super_priority_enabled INTEGER, authorization_instance_authorization_id TEXT, last_error_code TEXT, last_error_message TEXT, updated_at TEXT, deleted_at TEXT);
CREATE TABLE account_quality_enforcements(account_id TEXT PRIMARY KEY, system_account_id TEXT, enforcement_id TEXT, generation INTEGER, state TEXT, action TEXT, trigger_run_id TEXT, config_source TEXT, config_source_id TEXT, policy_revision INTEGER, profile TEXT, penalty_threshold INTEGER, recovery_interval_minutes INTEGER, recovery_model TEXT, account_config_revision INTEGER, before_status TEXT, after_status TEXT, fallback_was_enabled INTEGER, super_priority_was_enabled INTEGER, started_at TEXT, recovery_due_at TEXT, created_at TEXT, updated_at TEXT);
`)
	w12aMustExec(t, db, `INSERT INTO model_quality_schedules(id,system_account_id,account_id,revision,profile,penalty_threshold,penalty_action,recovery_interval_minutes,model) VALUES('w12a-sched','w12a-sys','w12a-acc',0,'quick',70,'quality_isolate',15,'gpt-5.6-sol')`)
	w12aMustExec(t, db, `INSERT INTO accounts(id,system_account_id,config_revision,status,schedulable,fallback_enabled,super_priority_enabled,deleted_at) VALUES('w12a-acc','w12a-sys',5,'active',1,0,0,NULL)`)
	input := w12aEnforcementInput()
	input.ScheduleID = "w12a-sched"
	result, err := ApplyEnforcement(context.Background(), db, false, input)
	if err != nil || result.Result != "applied" {
		t.Fatalf("schedule 字段一致应生效: %#v %v", result, err)
	}
	// 字段不一致 → stale。
	input.ScheduleID = "w12a-sched"
	input.PenaltyThreshold = 71
	result, err = ApplyEnforcement(context.Background(), db, false, input)
	if err != nil || result.Result != "stale" {
		t.Fatalf("schedule 字段漂移应 stale: %#v %v", result, err)
	}
	// schedules 表缺 model 列 → 查询错误（非 ErrNoRows）→ ApplyEnforcement 返回 err。
	broken := w12aMemoryDB(t, `
CREATE TABLE model_quality_schedules(id TEXT PRIMARY KEY, system_account_id TEXT, account_id TEXT, revision INTEGER, profile TEXT, penalty_threshold INTEGER, penalty_action TEXT, recovery_interval_minutes INTEGER);
`)
	input = w12aEnforcementInput()
	input.ScheduleID = "w12a-sched"
	if _, err := ApplyEnforcement(context.Background(), broken, false, input); err == nil {
		t.Fatalf("schedules 缺列应报错")
	}
	// 无 ScheduleID 且 policies 表缺列 → 策略查询错误 → ApplyEnforcement 返回 err。
	brokenPolicy := w12aMemoryDB(t, `
CREATE TABLE model_quality_policies(system_account_id TEXT PRIMARY KEY, revision INTEGER, profile TEXT, penalty_threshold INTEGER, recovery_interval_minutes INTEGER);
`)
	if _, err := ApplyEnforcement(context.Background(), brokenPolicy, false, w12aEnforcementInput()); err == nil {
		t.Fatalf("policies 缺列应报错")
	}
	// 无策略行 + 默认 fallback 输入 → 默认匹配生效 → applied。
	plain := w12aMemoryDB(t, `
CREATE TABLE model_quality_policies(system_account_id TEXT PRIMARY KEY, revision INTEGER, profile TEXT, penalty_threshold INTEGER, penalty_action TEXT, recovery_interval_minutes INTEGER);
CREATE TABLE accounts(id TEXT PRIMARY KEY, system_account_id TEXT, config_revision INTEGER, status TEXT, schedulable INTEGER, fallback_enabled INTEGER, super_priority_enabled INTEGER, authorization_instance_authorization_id TEXT, last_error_code TEXT, last_error_message TEXT, updated_at TEXT, deleted_at TEXT);
CREATE TABLE account_quality_enforcements(account_id TEXT PRIMARY KEY, system_account_id TEXT, enforcement_id TEXT, generation INTEGER, state TEXT, action TEXT, trigger_run_id TEXT, config_source TEXT, config_source_id TEXT, policy_revision INTEGER, profile TEXT, penalty_threshold INTEGER, recovery_interval_minutes INTEGER, recovery_model TEXT, account_config_revision INTEGER, before_status TEXT, after_status TEXT, fallback_was_enabled INTEGER, super_priority_was_enabled INTEGER, started_at TEXT, recovery_due_at TEXT, created_at TEXT, updated_at TEXT);
`)
	w12aMustExec(t, plain, `INSERT INTO accounts(id,system_account_id,config_revision,status,schedulable,fallback_enabled,super_priority_enabled,deleted_at) VALUES('w12a-acc','w12a-sys',5,'active',1,0,0,NULL)`)
	def := EnforcementInput{
		SystemAccountID: "w12a-sys", AccountID: "w12a-acc", RunID: "w12a-run",
		Action: "fallback", Model: "gpt-5.6-sol", Profile: "quick",
		PolicyRevision: 0, PenaltyThreshold: 70, RecoveryIntervalMinutes: 10,
		AccountConfigRevision: 5, DecidedAt: w12aNow,
	}
	result, err = ApplyEnforcement(context.Background(), plain, false, def)
	if err != nil || result.Result != "applied" || result.AfterStatus != "active" {
		t.Fatalf("默认配置应生效: %#v %v", result, err)
	}
}

func TestW12aApplyEnforcementInsertEnforcementError(t *testing.T) {
	// enforcement 表缺 recovery_model 列：账户 UPDATE 成功，INSERT 失败。
	db := w12aMemoryDB(t, `
CREATE TABLE model_quality_policies(system_account_id TEXT PRIMARY KEY, revision INTEGER, profile TEXT, penalty_threshold INTEGER, penalty_action TEXT, recovery_interval_minutes INTEGER);
CREATE TABLE accounts(id TEXT PRIMARY KEY, system_account_id TEXT, config_revision INTEGER, status TEXT, schedulable INTEGER, fallback_enabled INTEGER, super_priority_enabled INTEGER, authorization_instance_authorization_id TEXT, last_error_code TEXT, last_error_message TEXT, updated_at TEXT, deleted_at TEXT);
CREATE TABLE account_quality_enforcements(account_id TEXT PRIMARY KEY, system_account_id TEXT, enforcement_id TEXT, generation INTEGER, state TEXT, action TEXT, trigger_run_id TEXT, config_source TEXT, config_source_id TEXT, policy_revision INTEGER, profile TEXT, penalty_threshold INTEGER, recovery_interval_minutes INTEGER, account_config_revision INTEGER, before_status TEXT, after_status TEXT, fallback_was_enabled INTEGER, super_priority_was_enabled INTEGER, started_at TEXT, recovery_due_at TEXT, created_at TEXT, updated_at TEXT);
`)
	w12aMustExec(t, db, `INSERT INTO model_quality_policies(system_account_id,revision,profile,penalty_threshold,penalty_action,recovery_interval_minutes) VALUES('w12a-sys',0,'quick',70,'quality_isolate',15)`)
	w12aMustExec(t, db, `INSERT INTO accounts(id,system_account_id,config_revision,status,schedulable,fallback_enabled,super_priority_enabled,deleted_at) VALUES('w12a-acc','w12a-sys',5,'active',1,0,0,NULL)`)
	if _, err := ApplyEnforcement(context.Background(), db, false, w12aEnforcementInput()); err == nil {
		t.Fatalf("INSERT 缺列应报错")
	}
}

func TestW12aClaimDueRecoveriesLeaseUpdateError(t *testing.T) {
	// enforcement 表缺 updated_at：SELECT 成功但租约 UPDATE 失败。
	db := w12aMemoryDB(t, `
CREATE TABLE account_quality_enforcements(account_id TEXT PRIMARY KEY, system_account_id TEXT, enforcement_id TEXT, generation INTEGER, state TEXT, action TEXT, recovery_due_at TEXT, recovery_model TEXT, config_source_id TEXT, policy_revision INTEGER, profile TEXT, penalty_threshold INTEGER, recovery_interval_minutes TEXT, recovery_lease_owner TEXT, recovery_lease_until TEXT);
CREATE TABLE accounts(id TEXT PRIMARY KEY, config_revision INTEGER, status TEXT, health_check_model TEXT, deleted_at TEXT);
`)
	w12aMustExec(t, db, `INSERT INTO account_quality_enforcements(account_id,system_account_id,enforcement_id,generation,state,action,recovery_due_at,recovery_model,config_source_id,policy_revision,profile,penalty_threshold,recovery_interval_minutes) VALUES('w12a-acc','w12a-sys','w12a-enf',2,'active','quality_isolate','2026-09-16T00:00:00Z','gpt-5.6-sol','sched-1',3,'quick',70,15)`)
	w12aMustExec(t, db, `INSERT INTO accounts(id,config_revision,status,health_check_model,deleted_at) VALUES('w12a-acc',5,'quality_isolated','w12a-model',NULL)`)
	if _, err := ClaimDueRecoveries(context.Background(), db, false, RecoveryClaimInput{OwnerID: "w12a-owner", Now: w12aNow, Limit: 5, Lease: time.Hour}); err == nil {
		t.Fatalf("租约 UPDATE 缺列应报错")
	}
}

func TestW12aCompleteRecoveryClearUpdateError(t *testing.T) {
	// 账户可恢复但 enforcement 缺 cleared_at：清除 UPDATE 失败。
	db := w12aMemoryDB(t, `
CREATE TABLE account_quality_enforcements(account_id TEXT PRIMARY KEY, system_account_id TEXT, enforcement_id TEXT, generation INTEGER, state TEXT, action TEXT, policy_revision INTEGER, account_config_revision INTEGER, recovery_lease_owner TEXT, recovery_lease_until TEXT, last_recovery_run_id TEXT, recovery_due_at TEXT, updated_at TEXT);
CREATE TABLE accounts(id TEXT PRIMARY KEY, system_account_id TEXT, status TEXT, config_revision INTEGER, schedulable INTEGER, availability_schedule_json TEXT, last_error_code TEXT, last_error_message TEXT, updated_at TEXT, deleted_at TEXT);
`)
	w12aMustExec(t, db, `INSERT INTO account_quality_enforcements(account_id,system_account_id,enforcement_id,generation,state,action,policy_revision,account_config_revision,recovery_lease_owner,recovery_lease_until) VALUES('w12a-acc','w12a-sys','w12a-enf',2,'active','quality_isolate',3,5,'w12a-owner','2999-01-01T00:00:00Z')`)
	w12aMustExec(t, db, `INSERT INTO accounts(id,system_account_id,status,config_revision,schedulable,availability_schedule_json,deleted_at) VALUES('w12a-acc','w12a-sys','quality_isolated',5,1,' ',NULL)`)
	if _, err := CompleteRecovery(context.Background(), db, false, w12aRecoveryCompletionInput()); err == nil {
		t.Fatalf("清除 UPDATE 缺列应报错")
	}
}

// ---- 可用性调度补充分支 ----

func TestW12aAvailabilityExceptionAllowHitAndCrossMidnight(t *testing.T) {
	// 主窗口不含当前分钟，allow 例外窗口命中 → true。
	allowed, err := availabilityAllowed(`{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"01:00","end":"02:00"}],"exceptions":[{"date":"2026-09-17","action":"allow","windows":[{"start":"11:00","end":"13:00"}]}]}`, w12aNow)
	if err != nil || !allowed {
		t.Fatalf("例外窗口命中应允许: %v %v", allowed, err)
	}
	// 跨夜窗口在当天 23:30 命中。
	lateNight := w12aNow.Add(11*time.Hour + 30*time.Minute)
	allowed, err = availabilityAllowed(`{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"22:00","end":"06:00"}]}`, lateNight)
	if err != nil || !allowed {
		t.Fatalf("跨夜当晚应允许: %v %v", allowed, err)
	}
	if !includesDay([]int{1, 2, 3, 4, 5, 6, 7}, dayForDate("2026-09-17")) {
		t.Fatalf("includesDay 应命中全天列表")
	}
	// 周日的 weekday=0 需映射为 7。
	if dayForDate("2026-09-20") != 7 {
		t.Fatalf("周日应映射为 7: %d", dayForDate("2026-09-20"))
	}
}

func TestW12aExceptionUnmarshalWindowsRawGarbage(t *testing.T) {
	// windows 值之后出现非法 token → 外层 Decode(&raw) 失败。
	if err := new(availabilityException).UnmarshalJSON([]byte(`{"date":"2026-09-10","action":"allow","windows":[{"start":"09:00","end":"10:00"}] trailing}`)); err == nil {
		t.Fatalf("windows 后非法 token 应报错")
	}
}

// ---- postgres 方言臂（w1cover 门禁）----

func TestW12aPostgresDialectArmsOnW1Cover(t *testing.T) {
	db, err := sql.Open("pgx", w12aW1CoverDSN(t))
	if err != nil {
		t.Skipf("w12a PG gated: 打开失败: %v", err)
	}
	defer db.Close()
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		t.Skip("w12a PG gated: PG 不可达")
	}
	cleanup := func() {
		for _, q := range []string{
			`DELETE FROM juhe_business.account_quality_enforcements WHERE account_id LIKE 'w12a-%'`,
			`DELETE FROM juhe_business.model_quality_schedules WHERE id LIKE 'w12a-%'`,
			`DELETE FROM juhe_business.accounts WHERE id LIKE 'w12a-%'`,
		} {
			_, _ = db.Exec(q)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	// 注意：pgx 协议下同一连接的 rows 打开期间不能再执行语句，因此 PG 臂
	// 用空结果集命中 FOR UPDATE 方言分支并验证事务提交，租约 UPDATE 分支
	// 由 SQLite 臂覆盖。
	// ClaimDueRecoveries postgres 臂：FOR UPDATE OF ... SKIP LOCKED（空结果）。
	claimCtx, claimCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer claimCancel()
	candidates, err := ClaimDueRecoveries(claimCtx, db, true, RecoveryClaimInput{OwnerID: "w12a-owner", Now: w12aNow, Limit: 5, Lease: time.Hour})
	if err != nil {
		t.Fatalf("PG 臂错误链: %+v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("PG 臂空结果不应有候选: %#v", candidates)
	}

	// 说明：CompleteRecovery 的 PG 分支（enforcement/accounts 的 FOR UPDATE）
	// 在覆盖库的遗留 text 列 schema 上因 int 参数无法编码为 text 而无法执行，
	// 需生产 schema 环境，不在本覆盖臂内。

	// 说明：ClaimDueSchedules/CompleteScheduledRun 的 PG 分支因覆盖库遗留
	// text 列与 `enabled=1` 等 integer 谓词不兼容（operator does not exist:
	// text = integer）而无法执行，方言分支由 SQLite 臂与方言改写单测覆盖。
}
