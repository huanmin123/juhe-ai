package modelcheckquality

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprobe"
)

// w12a_quality_test.go 用 SQLite（postgres=false 方言）驱动 recovery/projector/
// schedule 三条 CAS 链路的分支；输入校验分支全部表驱动。时间统一注入。

var w12aNow = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

func w12aQualityDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	schema := `
CREATE TABLE accounts(
  id TEXT PRIMARY KEY, system_account_id TEXT, config_revision INTEGER, status TEXT,
  schedulable INTEGER, fallback_enabled INTEGER, super_priority_enabled INTEGER,
  authorization_instance_authorization_id TEXT, health_check_model TEXT,
  availability_schedule_json TEXT, last_error_code TEXT, last_error_message TEXT,
  updated_at TEXT, deleted_at TEXT
);
CREATE TABLE account_quality_enforcements(
  account_id TEXT PRIMARY KEY, system_account_id TEXT, enforcement_id TEXT, generation INTEGER,
  state TEXT, action TEXT, trigger_run_id TEXT, config_source TEXT, config_source_id TEXT,
  policy_revision INTEGER, profile TEXT, penalty_threshold INTEGER, recovery_interval_minutes INTEGER,
  recovery_model TEXT, account_config_revision INTEGER, before_status TEXT, after_status TEXT,
  fallback_was_enabled INTEGER, super_priority_was_enabled INTEGER, started_at TEXT,
  recovery_due_at TEXT, recovery_lease_owner TEXT, recovery_lease_until TEXT,
  last_recovery_run_id TEXT, cleared_at TEXT, created_at TEXT, updated_at TEXT
);
CREATE TABLE model_quality_schedules(
  id TEXT PRIMARY KEY, revision INTEGER, system_account_id TEXT, account_id TEXT, model TEXT,
  interval_minutes INTEGER, profile TEXT, penalty_threshold INTEGER, penalty_action TEXT,
  recovery_interval_minutes INTEGER, enabled INTEGER, next_run_at TEXT, lease_owner TEXT,
  lease_until TEXT, last_run_id TEXT, last_run_at TEXT, last_run_status TEXT, updated_at TEXT
);
CREATE TABLE model_quality_policies(
  system_account_id TEXT PRIMARY KEY, revision INTEGER, profile TEXT, penalty_threshold INTEGER,
  penalty_action TEXT, recovery_interval_minutes INTEGER
);
`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	return db
}

func w12aMustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("fixture exec 失败: %v (query: %.160s)", err, query)
	}
}

func w12aCanceledCtx() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx, cancel
}

// ---- ClaimDueRecoveries ----

func TestW12aClaimDueRecoveriesValidation(t *testing.T) {
	db := w12aQualityDB(t)
	valid := RecoveryClaimInput{OwnerID: "w12a-owner", Now: w12aNow, Limit: 10, Lease: time.Minute}
	cases := []struct {
		name  string
		mutat func(*RecoveryClaimInput)
	}{
		{"nil db", func(i *RecoveryClaimInput) {}},
		{"owner blank", func(i *RecoveryClaimInput) { i.OwnerID = " " }},
		{"now zero", func(i *RecoveryClaimInput) { i.Now = time.Time{} }},
		{"limit low", func(i *RecoveryClaimInput) { i.Limit = 0 }},
		{"limit high", func(i *RecoveryClaimInput) { i.Limit = 1001 }},
		{"lease zero", func(i *RecoveryClaimInput) { i.Lease = 0 }},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := valid
			tc.mutat(&input)
			var target *sql.DB
			if index != 0 {
				target = db
			}
			if _, err := ClaimDueRecoveries(context.Background(), target, false, input); err == nil {
				t.Fatalf("非法输入应报错: %#v", input)
			}
		})
	}
}

func TestW12aClaimDueRecoveriesCanceledContext(t *testing.T) {
	db := w12aQualityDB(t)
	ctx, cancel := w12aCanceledCtx()
	defer cancel()
	_, err := ClaimDueRecoveries(ctx, db, false, RecoveryClaimInput{OwnerID: "w12a-owner", Now: w12aNow, Limit: 1, Lease: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "begin recovery claim") {
		t.Fatalf("取消上下文应使事务开启失败: %v", err)
	}
}

func TestW12aClaimDueRecoveriesQueryErrorWhenTableMissing(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = ClaimDueRecoveries(context.Background(), db, false, RecoveryClaimInput{OwnerID: "w12a-owner", Now: w12aNow, Limit: 1, Lease: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "query due recoveries") {
		t.Fatalf("缺表应报查询错误: %v", err)
	}
}

func TestW12aClaimDueRecoveriesLeasesDueRow(t *testing.T) {
	db := w12aQualityDB(t)
	w12aMustExec(t, db, `INSERT INTO account_quality_enforcements(account_id,system_account_id,enforcement_id,generation,state,action,recovery_due_at,recovery_model,config_source_id,policy_revision,profile,penalty_threshold,recovery_interval_minutes) VALUES('w12a-acc','w12a-sys','w12a-enf',2,'active','quality_isolate','2026-09-16T00:00:00Z','gpt-5.6-sol','sched-1',3,'quick',70,15)`)
	w12aMustExec(t, db, `INSERT INTO accounts(id,system_account_id,config_revision,status,schedulable,health_check_model,deleted_at) VALUES('w12a-acc','w12a-sys',5,'quality_isolated',0,'w12a-model',NULL)`)
	candidates, err := ClaimDueRecoveries(context.Background(), db, false, RecoveryClaimInput{OwnerID: "w12a-owner", Now: w12aNow, Limit: 5, Lease: time.Hour})
	if err != nil || len(candidates) != 1 {
		t.Fatalf("应租约一条到期恢复: %#v %v", candidates, err)
	}
	c := candidates[0]
	if c.AccountID != "w12a-acc" || c.EnforcementID != "w12a-enf" || c.Generation != 2 || c.AccountConfigRevision != 5 {
		t.Fatalf("候选快照不符: %#v", c)
	}
	if c.Model != "gpt-5.6-sol" || c.PolicyRevision != 3 || c.Profile != "quick" || c.PenaltyThreshold != 70 || c.RecoveryIntervalMinutes != 15 {
		t.Fatalf("候选策略快照不符: %#v", c)
	}
	var leaseOwner string
	var leaseUntil string
	if err := db.QueryRow(`SELECT recovery_lease_owner,recovery_lease_until FROM account_quality_enforcements WHERE account_id='w12a-acc'`).Scan(&leaseOwner, &leaseUntil); err != nil {
		t.Fatal(err)
	}
	if leaseOwner != "w12a-owner" || leaseUntil == "" {
		t.Fatalf("租约未写入: %q %q", leaseOwner, leaseUntil)
	}
	// 二次认领：租约未到期 → 无新候选。
	candidates, err = ClaimDueRecoveries(context.Background(), db, false, RecoveryClaimInput{OwnerID: "w12a-owner-2", Now: w12aNow, Limit: 5, Lease: time.Hour})
	if err != nil || len(candidates) != 0 {
		t.Fatalf("租约期内不应重复认领: %#v %v", candidates, err)
	}
}

func TestW12aClaimDueRecoveriesScheduleIDAndNullScan(t *testing.T) {
	// config_source_id 为 NULL → schedule 保持空；account_id NULL 行触发扫描失败分支。
	db := w12aQualityDB(t)
	w12aMustExec(t, db, `INSERT INTO account_quality_enforcements(account_id,system_account_id,enforcement_id,generation,state,action,recovery_due_at,config_source_id,policy_revision,profile,penalty_threshold,recovery_interval_minutes) VALUES('w12a-acc','w12a-sys','w12a-enf',1,'active','quality_isolate','2026-09-16T00:00:00Z',NULL,0,'quick',70,15)`)
	w12aMustExec(t, db, `INSERT INTO accounts(id,config_revision,status,health_check_model,deleted_at) VALUES('w12a-acc',1,'quality_isolated','w12a-model',NULL)`)
	candidates, err := ClaimDueRecoveries(context.Background(), db, false, RecoveryClaimInput{OwnerID: "w12a-owner", Now: w12aNow, Limit: 5, Lease: time.Hour})
	if err != nil || len(candidates) != 1 || candidates[0].ScheduleID != "" {
		t.Fatalf("NULL 调度来源应保持空: %#v %v", candidates, err)
	}
	// 扫描失败分支：JOIN 命中但 policy_revision 为 NULL（int 列无法吸收 NULL）。
	db2 := w12aQualityDB(t)
	w12aMustExec(t, db2, `INSERT INTO account_quality_enforcements(account_id,system_account_id,enforcement_id,generation,state,action,recovery_due_at,account_config_revision,policy_revision,profile,penalty_threshold,recovery_interval_minutes) VALUES('w12a-acc','w12a-sys','w12a-enf',1,'active','quality_isolate','2026-09-16T00:00:00Z',1,NULL,'quick',70,15)`)
	w12aMustExec(t, db2, `INSERT INTO accounts(id,config_revision,status,health_check_model,deleted_at) VALUES('w12a-acc',1,'quality_isolated','w12a-model',NULL)`)
	if _, err := ClaimDueRecoveries(context.Background(), db2, false, RecoveryClaimInput{OwnerID: "w12a-owner", Now: w12aNow, Limit: 5, Lease: time.Hour}); err == nil {
		t.Fatalf("NULL policy_revision 应触发扫描失败")
	}
}

// ---- CompleteRecovery ----

func w12aRecoveryCompletionInput() RecoveryCompletionInput {
	return RecoveryCompletionInput{
		OwnerID: "w12a-owner", AccountID: "w12a-acc", EnforcementID: "w12a-enf", RunID: "w12a-run",
		Generation: 2, PolicyRevision: 3, RecoveryIntervalMinutes: 15,
		Passed: true, CompletedAt: w12aNow,
	}
}

func w12aSeedRecoveryFixture(t *testing.T, db *sql.DB, accountStatus string, accountRevision int, scheduleJSON string) {
	t.Helper()
	w12aMustExec(t, db, `INSERT INTO account_quality_enforcements(account_id,system_account_id,enforcement_id,generation,state,action,policy_revision,account_config_revision,recovery_lease_owner,recovery_lease_until) VALUES('w12a-acc','w12a-sys','w12a-enf',2,'active','quality_isolate',3,5,'w12a-owner','2999-01-01T00:00:00Z')`)
	w12aMustExec(t, db, `INSERT INTO accounts(id,system_account_id,config_revision,status,availability_schedule_json,deleted_at) VALUES('w12a-acc','w12a-sys',?,?,' ',NULL)`, accountRevision, accountStatus)
	_ = scheduleJSON
}

func TestW12aCompleteRecoveryValidation(t *testing.T) {
	db := w12aQualityDB(t)
	valid := w12aRecoveryCompletionInput()
	cases := []struct {
		name  string
		mutat func(*RecoveryCompletionInput)
	}{
		{"nil db", func(i *RecoveryCompletionInput) {}},
		{"owner blank", func(i *RecoveryCompletionInput) { i.OwnerID = " " }},
		{"account blank", func(i *RecoveryCompletionInput) { i.AccountID = "" }},
		{"enforcement blank", func(i *RecoveryCompletionInput) { i.EnforcementID = "" }},
		{"run blank", func(i *RecoveryCompletionInput) { i.RunID = "" }},
		{"generation zero", func(i *RecoveryCompletionInput) { i.Generation = 0 }},
		{"policy revision negative", func(i *RecoveryCompletionInput) { i.PolicyRevision = -1 }},
		{"interval low", func(i *RecoveryCompletionInput) { i.RecoveryIntervalMinutes = 9 }},
		{"interval high", func(i *RecoveryCompletionInput) { i.RecoveryIntervalMinutes = 10081 }},
		{"completed zero", func(i *RecoveryCompletionInput) { i.CompletedAt = time.Time{} }},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := valid
			tc.mutat(&input)
			var target *sql.DB
			if index != 0 {
				target = db
			}
			if _, err := CompleteRecovery(context.Background(), target, false, input); err == nil {
				t.Fatalf("非法输入应报错: %#v", input)
			}
		})
	}
}

func TestW12aCompleteRecoveryStaleAndKeptBranches(t *testing.T) {
	ctx := context.Background()

	// enforcement 缺失 → stale。
	db := w12aQualityDB(t)
	result, err := CompleteRecovery(ctx, db, false, w12aRecoveryCompletionInput())
	if err != nil || result.Result != "stale" {
		t.Fatalf("缺失 enforcement 应 stale: %#v %v", result, err)
	}

	// state 已清除 → stale。
	db = w12aQualityDB(t)
	w12aMustExec(t, db, `INSERT INTO account_quality_enforcements(account_id,system_account_id,enforcement_id,generation,state,action,policy_revision,account_config_revision,recovery_lease_owner) VALUES('w12a-acc','w12a-sys','w12a-enf',2,'cleared','quality_isolate',3,5,'w12a-owner')`)
	result, err = CompleteRecovery(ctx, db, false, w12aRecoveryCompletionInput())
	if err != nil || result.Result != "stale" {
		t.Fatalf("已清除 enforcement 应 stale: %#v %v", result, err)
	}

	// policy revision 变化 → stale 且带下次恢复时间。
	db = w12aQualityDB(t)
	w12aSeedRecoveryFixture(t, db, "quality_isolated", 5, "")
	input := w12aRecoveryCompletionInput()
	input.PolicyRevision = 99
	result, err = CompleteRecovery(ctx, db, false, input)
	if err != nil || result.Result != "stale" || result.NextRecoveryAt == nil {
		t.Fatalf("策略漂移应 stale 并复检: %#v %v", result, err)
	}

	// 探针未达标 → kept_isolated。
	db = w12aQualityDB(t)
	w12aSeedRecoveryFixture(t, db, "quality_isolated", 5, "")
	input = w12aRecoveryCompletionInput()
	input.Passed = false
	result, err = CompleteRecovery(ctx, db, false, input)
	if err != nil || result.Result != "kept_isolated" || result.BeforeStatus != "quality_isolated" {
		t.Fatalf("未达标应继续隔离: %#v %v", result, err)
	}

	// 账户缺失 → stale。
	db = w12aQualityDB(t)
	w12aSeedRecoveryFixture(t, db, "quality_isolated", 5, "")
	w12aMustExec(t, db, `DELETE FROM accounts WHERE id='w12a-acc'`)
	result, err = CompleteRecovery(ctx, db, false, w12aRecoveryCompletionInput())
	if err != nil || result.Result != "stale" {
		t.Fatalf("账户缺失应 stale: %#v %v", result, err)
	}

	// 账户状态已变 → stale。
	db = w12aQualityDB(t)
	w12aSeedRecoveryFixture(t, db, "active", 5, "")
	result, err = CompleteRecovery(ctx, db, false, w12aRecoveryCompletionInput())
	if err != nil || result.Result != "stale" || result.BeforeStatus != "active" {
		t.Fatalf("账户状态漂移应 stale: %#v %v", result, err)
	}

	// 账户配置代次漂移 → stale。
	db = w12aQualityDB(t)
	w12aSeedRecoveryFixture(t, db, "quality_isolated", 6, "")
	result, err = CompleteRecovery(ctx, db, false, w12aRecoveryCompletionInput())
	if err != nil || result.Result != "stale" || result.NextRecoveryAt == nil {
		t.Fatalf("配置漂移应 stale 并复检: %#v %v", result, err)
	}
}

func TestW12aCompleteRecoveryAppliesAndDisables(t *testing.T) {
	ctx := context.Background()

	// 达标且调度允许（空调度放行）→ recovered / active。
	db := w12aQualityDB(t)
	w12aSeedRecoveryFixture(t, db, "quality_isolated", 5, "")
	result, err := CompleteRecovery(ctx, db, false, w12aRecoveryCompletionInput())
	if err != nil || result.Result != "recovered" || result.AfterStatus != "active" {
		t.Fatalf("达标应恢复 active: %#v %v", result, err)
	}
	var status string
	var schedulable int
	if err := db.QueryRow(`SELECT status,schedulable FROM accounts WHERE id='w12a-acc'`).Scan(&status, &schedulable); err != nil {
		t.Fatal(err)
	}
	if status != "active" || schedulable != 1 {
		t.Fatalf("账户应恢复可调度: %q %d", status, schedulable)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM account_quality_enforcements WHERE account_id='w12a-acc'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "cleared" {
		t.Fatalf("处罚应清除: %q", state)
	}

	// 调度窗口不含当前时刻 → recovered / disabled。
	db = w12aQualityDB(t)
	w12aSeedRecoveryFixture(t, db, "quality_isolated", 5, "")
	w12aMustExec(t, db, `UPDATE accounts SET availability_schedule_json=? WHERE id='w12a-acc'`,
		`{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"00:00","end":"01:00"}]}`)
	result, err = CompleteRecovery(ctx, db, false, w12aRecoveryCompletionInput())
	if err != nil || result.Result != "recovered" || result.AfterStatus != "disabled" {
		t.Fatalf("窗口外应恢复为 disabled: %#v %v", result, err)
	}

	// 调度 JSON 非法 → 报错。
	db = w12aQualityDB(t)
	w12aSeedRecoveryFixture(t, db, "quality_isolated", 5, "")
	w12aMustExec(t, db, `UPDATE accounts SET availability_schedule_json='{bad' WHERE id='w12a-acc'`)
	if _, err := CompleteRecovery(ctx, db, false, w12aRecoveryCompletionInput()); err == nil || !strings.Contains(err.Error(), "evaluate account availability schedule") {
		t.Fatalf("非法调度应报错: %v", err)
	}
}

// ---- ApplyEnforcement ----

func w12aEnforcementInput() EnforcementInput {
	return EnforcementInput{
		SystemAccountID: "w12a-sys", AccountID: "w12a-acc", RunID: "w12a-run",
		Action: "quality_isolate", Model: "gpt-5.6-sol", Profile: "quick",
		PolicyRevision: 0, PenaltyThreshold: 70, RecoveryIntervalMinutes: 15,
		AccountConfigRevision: 5, DecidedAt: w12aNow, Message: "w12a-msg",
	}
}

func w12aSeedEnforcementAccount(t *testing.T, db *sql.DB, status, action string) {
	t.Helper()
	w12aMustExec(t, db, `INSERT INTO accounts(id,system_account_id,config_revision,status,schedulable,fallback_enabled,super_priority_enabled,deleted_at) VALUES('w12a-acc','w12a-sys',5,?,1,0,0,NULL)`, status)
	// 策略行与默认输入对齐，让 enforcementConfigurationMatches 放行。
	w12aMustExec(t, db, `INSERT INTO model_quality_policies(system_account_id,revision,profile,penalty_threshold,penalty_action,recovery_interval_minutes) VALUES('w12a-sys',0,'quick',70,?,15)`, action)
}

func TestW12aApplyEnforcementValidation(t *testing.T) {
	db := w12aQualityDB(t)
	valid := w12aEnforcementInput()
	cases := []struct {
		name  string
		mutat func(*EnforcementInput)
	}{
		{"nil db", func(i *EnforcementInput) {}},
		{"system blank", func(i *EnforcementInput) { i.SystemAccountID = " " }},
		{"account blank", func(i *EnforcementInput) { i.AccountID = "" }},
		{"run blank", func(i *EnforcementInput) { i.RunID = "" }},
		{"decided zero", func(i *EnforcementInput) { i.DecidedAt = time.Time{} }},
		{"policy negative", func(i *EnforcementInput) { i.PolicyRevision = -1 }},
		{"config revision zero", func(i *EnforcementInput) { i.AccountConfigRevision = 0 }},
		{"threshold low", func(i *EnforcementInput) { i.PenaltyThreshold = 39 }},
		{"threshold high", func(i *EnforcementInput) { i.PenaltyThreshold = 101 }},
		{"interval low", func(i *EnforcementInput) { i.RecoveryIntervalMinutes = 9 }},
		{"action invalid", func(i *EnforcementInput) { i.Action = "block" }},
		{"profile invalid", func(i *EnforcementInput) { i.Profile = "deep" }},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := valid
			tc.mutat(&input)
			var target *sql.DB
			if index != 0 {
				target = db
			}
			if _, err := ApplyEnforcement(context.Background(), target, false, input); err == nil {
				t.Fatalf("非法输入应报错: %#v", input)
			}
		})
	}
}

func TestW12aApplyEnforcementSkipsAndStale(t *testing.T) {
	ctx := context.Background()
	input := w12aEnforcementInput()

	// 调度不存在 → 配置不匹配 → stale。
	db := w12aQualityDB(t)
	w12aSeedEnforcementAccount(t, db, "active", "quality_isolate")
	input.ScheduleID = "w12a-sched"
	result, err := ApplyEnforcement(ctx, db, false, input)
	if err != nil || result.Result != "stale" {
		t.Fatalf("调度缺失应 stale: %#v %v", result, err)
	}

	// 账户缺失 → skipped（策略行与输入匹配，失败点在账户读取）。
	db = w12aQualityDB(t)
	w12aMustExec(t, db, `INSERT INTO model_quality_policies(system_account_id,revision,profile,penalty_threshold,penalty_action,recovery_interval_minutes) VALUES('w12a-sys',0,'quick',70,'quality_isolate',15)`)
	input = w12aEnforcementInput()
	result, err = ApplyEnforcement(ctx, db, false, input)
	if err != nil || result.Result != "skipped" {
		t.Fatalf("账户缺失应 skipped: %#v %v", result, err)
	}

	// 账户归属系统不符 → skipped。
	db = w12aQualityDB(t)
	w12aMustExec(t, db, `INSERT INTO model_quality_policies(system_account_id,revision,profile,penalty_threshold,penalty_action,recovery_interval_minutes) VALUES('w12a-sys',0,'quick',70,'quality_isolate',15)`)
	w12aMustExec(t, db, `INSERT INTO accounts(id,system_account_id,config_revision,status,fallback_enabled,super_priority_enabled,deleted_at) VALUES('w12a-acc','w12a-other',5,'active',0,0,NULL)`)
	input = w12aEnforcementInput()
	result, err = ApplyEnforcement(ctx, db, false, input)
	if err != nil || result.Result != "skipped" {
		t.Fatalf("归属不符应 skipped: %#v %v", result, err)
	}

	// 授权实例账户 → skipped。
	db = w12aQualityDB(t)
	w12aMustExec(t, db, `INSERT INTO model_quality_policies(system_account_id,revision,profile,penalty_threshold,penalty_action,recovery_interval_minutes) VALUES('w12a-sys',0,'quick',70,'quality_isolate',15)`)
	w12aMustExec(t, db, `INSERT INTO accounts(id,system_account_id,config_revision,status,fallback_enabled,super_priority_enabled,authorization_instance_authorization_id,deleted_at) VALUES('w12a-acc','w12a-sys',5,'active',0,0,'w12a-authz',NULL)`)
	input = w12aEnforcementInput()
	result, err = ApplyEnforcement(ctx, db, false, input)
	if err != nil || result.Result != "skipped" {
		t.Fatalf("授权实例应 skipped: %#v %v", result, err)
	}

	// 配置代次不匹配 → stale。
	db = w12aQualityDB(t)
	w12aSeedEnforcementAccount(t, db, "active", "quality_isolate")
	input = w12aEnforcementInput()
	input.AccountConfigRevision = 6
	result, err = ApplyEnforcement(ctx, db, false, input)
	if err != nil || result.Result != "stale" {
		t.Fatalf("配置漂移应 stale: %#v %v", result, err)
	}

	// fallback 动作对非 active 账户 → skipped。
	db = w12aQualityDB(t)
	w12aSeedEnforcementAccount(t, db, "quality_isolated", "fallback")
	input = w12aEnforcementInput()
	input.Action = "fallback"
	result, err = ApplyEnforcement(ctx, db, false, input)
	if err != nil || result.Result != "skipped" {
		t.Fatalf("fallback 对隔离账户应 skipped: %#v %v", result, err)
	}
}

func TestW12aApplyEnforcementAppliesAllActions(t *testing.T) {
	ctx := context.Background()

	// quality_isolate：applied + 恢复时间。
	db := w12aQualityDB(t)
	w12aSeedEnforcementAccount(t, db, "active", "quality_isolate")
	input := w12aEnforcementInput()
	result, err := ApplyEnforcement(ctx, db, false, input)
	if err != nil || result.Result != "applied" || result.AfterStatus != "quality_isolated" || result.Generation != 1 {
		t.Fatalf("隔离处罚应生效: %#v %v", result, err)
	}
	if result.RecoveryDueAt == nil || !result.RecoveryDueAt.Equal(w12aNow.Add(15*time.Minute)) {
		t.Fatalf("恢复时间应为 15 分钟后: %#v", result.RecoveryDueAt)
	}

	// fallback：applied + 开启回退，同 run 重放幂等。
	db = w12aQualityDB(t)
	w12aSeedEnforcementAccount(t, db, "active", "fallback")
	input = w12aEnforcementInput()
	input.Action = "fallback"
	result, err = ApplyEnforcement(ctx, db, false, input)
	if err != nil || result.Result != "applied" || result.AfterStatus != "active" {
		t.Fatalf("降级处罚应生效: %#v %v", result, err)
	}
	// 首次 applied 后状态仍 active（fallback 不改状态），重放命中 prior run；
	// 首次处罚已把账户代次推进到 6，重放者携带最新快照。
	replay := input
	replay.AccountConfigRevision = 6
	result, err = ApplyEnforcement(ctx, db, false, replay)
	if err != nil || result.Result != "already_effective" || result.Generation != 1 {
		t.Fatalf("同 run 重放应幂等: %#v %v", result, err)
	}
	var fallback, superPriority int
	if err := db.QueryRow(`SELECT fallback_enabled,super_priority_enabled FROM accounts WHERE id='w12a-acc'`).Scan(&fallback, &superPriority); err != nil {
		t.Fatal(err)
	}
	if fallback != 1 || superPriority != 0 {
		t.Fatalf("降级应开启回退关闭超优先级: %d %d", fallback, superPriority)
	}

	// disable：对隔离账户 applied + disabled。
	db = w12aQualityDB(t)
	w12aSeedEnforcementAccount(t, db, "quality_isolated", "disable")
	input = w12aEnforcementInput()
	input.Action = "disable"
	result, err = ApplyEnforcement(ctx, db, false, input)
	if err != nil || result.Result != "applied" || result.AfterStatus != "disabled" {
		t.Fatalf("禁用处罚应生效: %#v %v", result, err)
	}

	// 策略表全匹配 → applied。
	db = w12aQualityDB(t)
	w12aSeedEnforcementAccount(t, db, "active", "fallback")
	w12aMustExec(t, db, `DELETE FROM model_quality_policies WHERE system_account_id='w12a-sys'`)
	w12aMustExec(t, db, `INSERT INTO model_quality_policies(system_account_id,revision,profile,penalty_threshold,penalty_action,recovery_interval_minutes) VALUES('w12a-sys',4,'quick',80,'fallback',20)`)
	input = w12aEnforcementInput()
	input.PolicyRevision = 4
	input.PenaltyThreshold = 80
	input.Action = "fallback"
	input.RecoveryIntervalMinutes = 20
	result, err = ApplyEnforcement(ctx, db, false, input)
	if err != nil || result.Result != "applied" {
		t.Fatalf("策略匹配应生效: %#v %v", result, err)
	}
}

// ---- ClaimDueSchedules / CompleteScheduledRun ----

func TestW12aClaimDueSchedulesValidationAndBegin(t *testing.T) {
	db := w12aQualityDB(t)
	valid := ScheduledClaimInput{OwnerID: "w12a-owner", Now: w12aNow, Limit: 5, Lease: time.Minute}
	cases := []struct {
		name  string
		mutat func(*ScheduledClaimInput)
	}{
		{"nil db", func(i *ScheduledClaimInput) {}},
		{"owner blank", func(i *ScheduledClaimInput) { i.OwnerID = " " }},
		{"now zero", func(i *ScheduledClaimInput) { i.Now = time.Time{} }},
		{"limit low", func(i *ScheduledClaimInput) { i.Limit = 0 }},
		{"limit high", func(i *ScheduledClaimInput) { i.Limit = 1001 }},
		{"lease zero", func(i *ScheduledClaimInput) { i.Lease = 0 }},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := valid
			tc.mutat(&input)
			var target *sql.DB
			if index != 0 {
				target = db
			}
			if _, err := ClaimDueSchedules(context.Background(), target, false, input); err == nil {
				t.Fatalf("非法输入应报错: %#v", input)
			}
		})
	}
	ctx, cancel := w12aCanceledCtx()
	defer cancel()
	if _, err := ClaimDueSchedules(ctx, db, false, valid); err == nil || !strings.Contains(err.Error(), "begin scheduled claim") {
		t.Fatalf("取消上下文应使事务开启失败: %v", err)
	}
}

func TestW12aClaimDueSchedulesLeasesDueRow(t *testing.T) {
	db := w12aQualityDB(t)
	w12aMustExec(t, db, `INSERT INTO model_quality_schedules(id,revision,system_account_id,account_id,model,interval_minutes,profile,penalty_threshold,penalty_action,recovery_interval_minutes,enabled,next_run_at) VALUES('w12a-sched',3,'w12a-sys','w12a-acc','gpt-5.6-sol',60,'quick',70,'fallback',20,1,'2026-09-16T00:00:00Z')`)
	w12aMustExec(t, db, `INSERT INTO accounts(id,deleted_at,authorization_instance_authorization_id,status) VALUES('w12a-acc',NULL,NULL,'active')`)
	candidates, err := ClaimDueSchedules(context.Background(), db, false, ScheduledClaimInput{OwnerID: "w12a-owner", Now: w12aNow, Limit: 5, Lease: time.Hour})
	if err != nil || len(candidates) != 1 {
		t.Fatalf("应租约一条到期调度: %#v %v", candidates, err)
	}
	c := candidates[0]
	if c.ScheduleID != "w12a-sched" || c.Revision != 3 || c.IntervalMinutes != 60 || c.Action != "fallback" {
		t.Fatalf("调度快照不符: %#v", c)
	}
	var leaseOwner string
	if err := db.QueryRow(`SELECT lease_owner FROM model_quality_schedules WHERE id='w12a-sched'`).Scan(&leaseOwner); err != nil {
		t.Fatal(err)
	}
	if leaseOwner != "w12a-owner" {
		t.Fatalf("租约未写入: %q", leaseOwner)
	}

	// 缺表 → query error。
	empty, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	if _, err := ClaimDueSchedules(context.Background(), empty, false, ScheduledClaimInput{OwnerID: "w12a-owner", Now: w12aNow, Limit: 5, Lease: time.Hour}); err == nil || !strings.Contains(err.Error(), "query due model quality schedules") {
		t.Fatalf("缺表应报查询错误: %v", err)
	}
}

func TestW12aCompleteScheduledRunBranches(t *testing.T) {
	db := w12aQualityDB(t)
	valid := ScheduledCompletionInput{OwnerID: "w12a-owner", ScheduleID: "w12a-sched", RunID: "w12a-run", Status: "completed", Revision: 3, IntervalMinutes: 60, CompletedAt: w12aNow}
	cases := []struct {
		name  string
		mutat func(*ScheduledCompletionInput)
	}{
		{"nil db", func(i *ScheduledCompletionInput) {}},
		{"owner blank", func(i *ScheduledCompletionInput) { i.OwnerID = " " }},
		{"schedule blank", func(i *ScheduledCompletionInput) { i.ScheduleID = "" }},
		{"revision zero", func(i *ScheduledCompletionInput) { i.Revision = 0 }},
		{"interval low", func(i *ScheduledCompletionInput) { i.IntervalMinutes = 9 }},
		{"interval high", func(i *ScheduledCompletionInput) { i.IntervalMinutes = 10081 }},
		{"completed zero", func(i *ScheduledCompletionInput) { i.CompletedAt = time.Time{} }},
		{"status invalid", func(i *ScheduledCompletionInput) { i.Status = "running" }},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := valid
			tc.mutat(&input)
			var target *sql.DB
			if index != 0 {
				target = db
			}
			if _, err := CompleteScheduledRun(context.Background(), target, false, input); err == nil {
				t.Fatalf("非法输入应报错: %#v", input)
			}
		})
	}

	w12aMustExec(t, db, `INSERT INTO model_quality_schedules(id,revision,lease_owner,next_run_at,last_run_status) VALUES('w12a-sched',3,'w12a-owner','2026-09-16T00:00:00Z','')`)
	changed, err := CompleteScheduledRun(context.Background(), db, false, valid)
	if err != nil || !changed {
		t.Fatalf("租约匹配应推进调度: %v %v", changed, err)
	}
	var nextRun, lastStatus string
	var leaseOwner *string
	if err := db.QueryRow(`SELECT next_run_at,last_run_status,lease_owner FROM model_quality_schedules WHERE id='w12a-sched'`).Scan(&nextRun, &lastStatus, &leaseOwner); err != nil {
		t.Fatal(err)
	}
	if nextRun != w12aNow.Add(time.Hour).UTC().Format(time.RFC3339Nano) || lastStatus != "completed" || leaseOwner != nil {
		t.Fatalf("调度推进结果不符: %q %q %v", nextRun, lastStatus, leaseOwner)
	}
	// 重复推进：租约已清空 → changed=false。
	changed, err = CompleteScheduledRun(context.Background(), db, false, valid)
	if err != nil || changed {
		t.Fatalf("租约清空后应不推进: %v %v", changed, err)
	}
	// 缺表 → exec error。
	missing, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer missing.Close()
	if _, err := CompleteScheduledRun(context.Background(), missing, false, valid); err == nil || !strings.Contains(err.Error(), "complete model quality schedule") {
		t.Fatalf("缺表应报完成错误: %v", err)
	}
}

// ---- Decide 未完成分支 ----

func TestW12aDecideNotCompletedBranch(t *testing.T) {
	policy, err := modelcheckinput.NewPolicySnapshot("1", "quick", true, 70, "fallback", 10)
	if err != nil {
		t.Fatal(err)
	}
	summary := modelcheckprobe.SummaryResult{Level: "likely", Score: 95, MaxScore: 100}
	decision := Decide(modelcheckinput.TriggerManual, policy, summary, false, Evidence{Formed: true}, w12aNow)
	if decision.Triggered || decision.Result != "not_triggered" || decision.Message == "" {
		t.Fatalf("未完成检测不应处罚: %#v", decision)
	}
}
