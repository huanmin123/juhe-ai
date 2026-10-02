package modelcheckowner

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func businessEnforcementTestDDL() []string {
	return []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,system_account_id TEXT,status TEXT,config_revision INTEGER,fallback_enabled INTEGER,super_priority_enabled INTEGER,deleted_at TEXT,schedulable INTEGER,last_error_code TEXT,last_error_message TEXT,updated_at TEXT)`,
		`CREATE TABLE account_quality_enforcements (account_id TEXT PRIMARY KEY,system_account_id TEXT,enforcement_id TEXT UNIQUE,generation INTEGER,state TEXT,action TEXT,trigger_run_id TEXT,config_source TEXT,config_source_id TEXT,policy_revision INTEGER,profile TEXT,penalty_threshold INTEGER,recovery_interval_minutes INTEGER,recovery_model TEXT,account_config_revision INTEGER,before_status TEXT,after_status TEXT,fallback_was_enabled INTEGER,super_priority_was_enabled INTEGER,started_at TEXT,recovery_due_at TEXT,created_at TEXT,updated_at TEXT,cleared_at TEXT)`,
		`CREATE TABLE model_quality_schedules (id TEXT PRIMARY KEY,system_account_id TEXT,account_id TEXT,revision INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,model TEXT,custom_question_ids TEXT)`,
		// 无 ScheduleID 的 Apply 走全局策略路径，configurationMatches 读取
		// model_quality_policies；列集与真实 schema 的查询投影保持一致。
		`CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER)`,
	}
}

func TestBusinessEnforcementApplierUsesRevisionCASAndAtomicUpsert(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range businessEnforcementTestDDL() {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('acct-1','sys-1','active',4,0,1,NULL,1,NULL,NULL,NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_quality_schedules VALUES ('sch-1','sys-1','acct-1',7,'full',70,'quality_isolate',15,'gpt-5.6-sol',NULL)`); err != nil {
		t.Fatal(err)
	}
	applier, err := NewBusinessEnforcementApplier(db, false)
	if err != nil {
		t.Fatal(err)
	}
	input := QualityEnforcement{AccountID: "acct-1", SystemAccountID: "sys-1", RunID: "run-1", ProviderCode: "openai", Model: "gpt-5.6-sol", PolicyRevision: "7", AccountConfigRevision: "4", ScheduleID: "sch-1", Action: "quality_isolate", Score: 20, Threshold: 70, RecoveryIntervalMinutes: 15, Profile: "full", OccurredAt: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)}
	outcome, err := applier.Apply(context.Background(), input)
	if err != nil {
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
	// 处罚结果必须携带可观察事实：处罚 id、代数与前后状态。
	if outcome.AlreadyEffective || outcome.EnforcementID == "" || outcome.Generation != 1 || outcome.BeforeStatus != "active" || outcome.AfterStatus != "quality_isolated" || outcome.RecoveryDueAt == "" {
		t.Fatalf("outcome=%+v", outcome)
	}
	if !strings.HasPrefix(outcome.EnforcementID, "mqe-") {
		t.Fatalf("enforcement id=%q", outcome.EnforcementID)
	}
	retry, err := applier.Apply(context.Background(), QualityEnforcement{AccountID: "acct-1", SystemAccountID: "sys-1", RunID: "run-1", ProviderCode: "openai", Model: "gpt-5.6-sol", PolicyRevision: "7", AccountConfigRevision: "4", ScheduleID: "sch-1", Action: "quality_isolate", Score: 20, Threshold: 70, RecoveryIntervalMinutes: 15, Profile: "full", OccurredAt: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("same-run health retry should be idempotent: %v", err)
	}
	// 同 run 重试命中幂等分支：AlreadyEffective 而非新开一代。
	if !retry.AlreadyEffective || retry.Generation != 1 {
		t.Fatalf("same-run retry outcome=%+v", retry)
	}
	input.RunID = "run-stale"
	input.AccountConfigRevision = "4"
	if _, err := applier.Apply(context.Background(), input); err == nil {
		t.Fatal("stale revision must be rejected")
	}
}

// 硬失败 + 分数达标（96 >= 70）必须被接受并落 recovery_model：
// 恢复调度按冻结模型回放探针，不得回退到账户当前 health_check_model。
func TestBusinessEnforcementApplierAcceptsHardFailureAboveThresholdAndStoresRecoveryModel(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business-hard.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range businessEnforcementTestDDL() {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('acct-2','sys-2','active',2,0,0,NULL,1,NULL,NULL,NULL)`); err != nil {
		t.Fatal(err)
	}
	// 全局策略行需与冻结的策略快照一致（revision/profile/threshold/action/
	// recovery），否则 Apply 判定配置过期。
	if _, err := db.Exec(`INSERT INTO model_quality_policies VALUES ('sys-2',0,'quick',70,'quality_isolate',10)`); err != nil {
		t.Fatal(err)
	}
	applier, err := NewBusinessEnforcementApplier(db, false)
	if err != nil {
		t.Fatal(err)
	}
	input := QualityEnforcement{AccountID: "acct-2", SystemAccountID: "sys-2", RunID: "run-hard", ProviderCode: "openai", Model: "gpt-5.6-sol", PolicyRevision: "0", AccountConfigRevision: "2", Action: "quality_isolate", Score: 96, Threshold: 70, RecoveryIntervalMinutes: 10, Profile: "quick", HardQualityFailure: true, OccurredAt: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)}
	outcome, err := applier.Apply(context.Background(), input)
	if err != nil {
		t.Fatalf("hard failure above threshold must be enforceable: %v", err)
	}
	if outcome.AlreadyEffective || outcome.AfterStatus != "quality_isolated" {
		t.Fatalf("outcome=%+v", outcome)
	}
	var recoveryModel string
	var valid sql.NullString
	if err := db.QueryRow(`SELECT recovery_model FROM account_quality_enforcements WHERE account_id='acct-2'`).Scan(&valid); err != nil {
		t.Fatal(err)
	}
	recoveryModel = valid.String
	if recoveryModel != "gpt-5.6-sol" {
		t.Fatalf("recovery_model=%q, want frozen probe model", recoveryModel)
	}
	// 反向校验：无硬失败且分数达标仍必须拒绝。
	soft := input
	soft.RunID = "run-soft"
	soft.HardQualityFailure = false
	if _, err := applier.Apply(context.Background(), soft); err == nil {
		t.Fatal("score above threshold without hard failure must be rejected")
	}
}

// 账户已处于同 action 的有效处罚时返回 AlreadyEffective；不同 action 冲突仍报错。
func TestBusinessEnforcementApplierSameActionIsAlreadyEffective(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business-effective.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range businessEnforcementTestDDL() {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('acct-3','sys-3','quality_isolated',6,0,0,NULL,0,'model_quality_failed','bad',NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_quality_enforcements (account_id,system_account_id,enforcement_id,generation,state,action,trigger_run_id,before_status,after_status,recovery_due_at) VALUES ('acct-3','sys-3','enf-old',3,'active','quality_isolate','run-old','active','quality_isolated','2026-09-01T10:10:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_quality_policies VALUES ('sys-3',0,'quick',70,'quality_isolate',10)`); err != nil {
		t.Fatal(err)
	}
	applier, err := NewBusinessEnforcementApplier(db, false)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := applier.Apply(context.Background(), QualityEnforcement{AccountID: "acct-3", SystemAccountID: "sys-3", RunID: "run-new", ProviderCode: "openai", Model: "gpt-5.6-sol", PolicyRevision: "0", AccountConfigRevision: "6", Action: "quality_isolate", Score: 20, Threshold: 70, RecoveryIntervalMinutes: 10, Profile: "quick", OccurredAt: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("same-action effective enforcement must not error: %v", err)
	}
	if !outcome.AlreadyEffective || outcome.EnforcementID != "enf-old" || outcome.Generation != 3 || outcome.AfterStatus != "quality_isolated" || outcome.RecoveryDueAt != "2026-09-01T10:10:00Z" {
		t.Fatalf("outcome=%+v", outcome)
	}
	var generation int
	if err := db.QueryRow(`SELECT generation FROM account_quality_enforcements WHERE account_id='acct-3'`).Scan(&generation); err != nil || generation != 3 {
		t.Fatalf("already-effective must not open a new generation: generation=%d err=%v", generation, err)
	}
	// 不同 action 冲突仍 fail-closed：先把全局策略切换为 disable（revision
	// 不变），使配置校验通过并暴露账户状态与处罚 action 的冲突。
	if _, err := db.Exec(`UPDATE model_quality_policies SET penalty_action='disable' WHERE system_account_id='sys-3'`); err != nil {
		t.Fatal(err)
	}
	if _, err := applier.Apply(context.Background(), QualityEnforcement{AccountID: "acct-3", SystemAccountID: "sys-3", RunID: "run-conflict", ProviderCode: "openai", Model: "gpt-5.6-sol", PolicyRevision: "0", AccountConfigRevision: "6", Action: "disable", Score: 20, Threshold: 70, RecoveryIntervalMinutes: 10, Profile: "quick", OccurredAt: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)}); err == nil || !strings.Contains(err.Error(), "not enforceable") {
		t.Fatalf("different action conflict must fail closed, err=%v", err)
	}
}
