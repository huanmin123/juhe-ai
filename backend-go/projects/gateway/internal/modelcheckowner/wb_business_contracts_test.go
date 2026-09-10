package modelcheckowner

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
	_ "modernc.org/sqlite"
)

// wbBusinessQualityDDL 建出质量管理 CRUD 依赖的 Business 表。
func wbBusinessQualityDDL(t *testing.T) []string {
	t.Helper()
	return []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,system_account_id TEXT,provider_code TEXT,provider_protocol_profile_id TEXT,deleted_at TEXT,authorization_instance_authorization_id TEXT,name TEXT)`,
		`CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,manual_enforcement_enabled INTEGER,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,created_at TEXT,updated_at TEXT)`,
		`CREATE TABLE model_quality_schedules (id TEXT PRIMARY KEY,system_account_id TEXT,account_id TEXT,model TEXT,interval_minutes INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,enabled INTEGER,revision INTEGER,next_run_at TEXT,last_run_id TEXT,last_run_at TEXT,last_run_status TEXT,created_at TEXT,updated_at TEXT,UNIQUE(system_account_id,account_id))`,
		`CREATE TABLE account_quality_enforcements (account_id TEXT PRIMARY KEY,state TEXT,action TEXT,recovery_due_at TEXT)`,
		`CREATE TABLE account_supported_models (account_id TEXT, model TEXT)`,
		`CREATE TABLE account_model_mappings (account_id TEXT, source_model TEXT, source_endpoint_family TEXT, upstream_model TEXT, upstream_endpoint_family TEXT, enabled INTEGER)`,
	}
}

// 质量管理 CRUD 契约：策略按乐观锁修订；定时检查按账户唯一并
// 在创建/更新时重新校验账户与模型约束。
func TestWBBusinessQualityManagerCRUDContract(t *testing.T) {
	db := wbOpenMemoryDB(t, wbBusinessQualityDDL(t))
	if _, err := db.Exec(`INSERT INTO accounts (id,system_account_id,provider_code,provider_protocol_profile_id,name) VALUES ('acct','sys','openai','profile_openai_openai_v1','Account 1')`); err != nil {
		t.Fatal(err)
	}
	manager, err := NewBusinessQualityManager(db, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	t.Run("default policy without row", func(t *testing.T) {
		view, err := manager.Policy(ctx, "sys")
		if err != nil || view.Revision != 0 || view.PenaltyThreshold != 70 || view.Profile != "quick" {
			t.Fatalf("默认策略 view=%+v err=%v", view, err)
		}
	})
	t.Run("policy requires system account", func(t *testing.T) {
		if _, err := manager.Policy(ctx, " "); err == nil {
			t.Fatal("空租户必须报错")
		}
	})
	t.Run("first patch inserts revision 1", func(t *testing.T) {
		threshold := 82
		view, err := manager.PatchPolicy(ctx, "sys", QualityPolicyPatch{ExpectedRevision: 0, PenaltyThreshold: &threshold})
		if err != nil || view.Revision != 1 || view.PenaltyThreshold != 82 {
			t.Fatalf("首次修订 view=%+v err=%v", view, err)
		}
	})
	t.Run("stale revision rejected", func(t *testing.T) {
		if _, err := manager.PatchPolicy(ctx, "sys", QualityPolicyPatch{ExpectedRevision: 0, PenaltyThreshold: ptrInt(90)}); err == nil || !strings.Contains(err.Error(), "已被其他操作修改") {
			t.Fatalf("过期修订必须冲突: err=%v", err)
		}
	})
	t.Run("no change returns current", func(t *testing.T) {
		view, err := manager.Policy(ctx, "sys")
		if err != nil {
			t.Fatal(err)
		}
		same := view.Profile
		patched, err := manager.PatchPolicy(ctx, "sys", QualityPolicyPatch{ExpectedRevision: 1, Profile: &same})
		if err != nil || patched.Revision != 1 {
			t.Fatalf("无变化修订必须原样返回: view=%+v err=%v", patched, err)
		}
	})
	schedule, err := manager.CreateSchedule(ctx, "sys", QualityScheduleInput{AccountID: "acct", Model: "gpt-5.6-sol", IntervalMinutes: 60, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10})
	if err != nil || !strings.HasPrefix(schedule.ID, "mqs-") || schedule.Revision != 1 {
		t.Fatalf("创建定时检查 view=%+v err=%v", schedule, err)
	}
	t.Run("duplicate schedule rejected", func(t *testing.T) {
		if _, err := manager.CreateSchedule(ctx, "sys", QualityScheduleInput{AccountID: "acct", Model: "gpt-5.6-sol", IntervalMinutes: 60, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10}); err == nil || !strings.Contains(err.Error(), "已存在") {
			t.Fatalf("重复配置必须拒绝: err=%v", err)
		}
	})
	t.Run("schedule for unsupported account rejected", func(t *testing.T) {
		if _, err := db.Exec(`INSERT INTO accounts (id,system_account_id,provider_code,provider_protocol_profile_id,name) VALUES ('acct-2','sys','openai','profile_openai_openai_v1','Account 2')`); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.CreateSchedule(ctx, "sys", QualityScheduleInput{AccountID: "missing", Model: "gpt-5.6-sol", IntervalMinutes: 60, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10}); err == nil || !strings.Contains(err.Error(), "不存在") {
			t.Fatalf("缺失账户必须拒绝: err=%v", err)
		}
	})
	t.Run("list schedules", func(t *testing.T) {
		list, err := manager.ListSchedules(ctx, "sys", 0, 500)
		if err != nil || len(list.Items) != 1 || list.Page != 1 || list.PageSize != 50 || list.Items[0].AccountName != "Account 1" || list.Items[0].ProviderCode != "openai" {
			t.Fatalf("定时检查清单=%+v err=%v", list, err)
		}
	})
	t.Run("patch schedule lifecycle", func(t *testing.T) {
		if _, err := manager.PatchSchedule(ctx, "sys", schedule.ID, QualitySchedulePatch{ExpectedRevision: 99, IntervalMinutes: ptrInt(90)}); err == nil || !strings.Contains(err.Error(), "已变化") {
			t.Fatalf("过期修订必须冲突: err=%v", err)
		}
		patched, err := manager.PatchSchedule(ctx, "sys", schedule.ID, QualitySchedulePatch{ExpectedRevision: 1, IntervalMinutes: ptrInt(90)})
		if err != nil || patched.Revision != 2 || patched.IntervalMinutes != 90 {
			t.Fatalf("字段级更新 view=%+v err=%v", patched, err)
		}
		same := patched.Model
		noChange, err := manager.PatchSchedule(ctx, "sys", schedule.ID, QualitySchedulePatch{ExpectedRevision: 2, Model: &same})
		if err != nil || noChange.Revision != 2 {
			t.Fatalf("无变化更新必须原样返回: view=%+v err=%v", noChange, err)
		}
		if _, err := manager.PatchSchedule(ctx, "sys", schedule.ID, QualitySchedulePatch{ExpectedRevision: 2, Model: ptrString("unknown-model")}); err == nil {
			t.Fatal("不支持的模型必须拒绝更新")
		}
		if _, err := manager.PatchSchedule(ctx, "sys", "mqs-missing", QualitySchedulePatch{ExpectedRevision: 1, IntervalMinutes: ptrInt(60)}); err == nil || !strings.Contains(err.Error(), "不存在") {
			t.Fatalf("缺失配置必须报错: err=%v", err)
		}
	})
	t.Run("delete schedule", func(t *testing.T) {
		deleted, err := manager.DeleteSchedule(ctx, "sys", schedule.ID)
		if err != nil || !deleted {
			t.Fatalf("删除失败: deleted=%v err=%v", deleted, err)
		}
		if deleted, err := manager.DeleteSchedule(ctx, "sys", schedule.ID); err != nil || deleted {
			t.Fatalf("重复删除必须返回 false: deleted=%v err=%v", deleted, err)
		}
	})
	t.Run("validation helpers", func(t *testing.T) {
		if err := validatePolicyPatch(QualityPolicyPatch{ExpectedRevision: -1}); err == nil {
			t.Fatal("负修订必须拒绝")
		}
		if err := validatePolicyPatch(QualityPolicyPatch{}); err == nil {
			t.Fatal("空修订必须拒绝")
		}
		if err := validateSchedulePatch(QualitySchedulePatch{ExpectedRevision: 0}); err == nil {
			t.Fatal("缺修订必须拒绝")
		}
		if err := validateSchedulePatch(QualitySchedulePatch{ExpectedRevision: 1}); err == nil {
			t.Fatal("空更新必须拒绝")
		}
		if err := validateSchedulePatch(QualitySchedulePatch{ExpectedRevision: 1, Model: ptrString(" ")}); err == nil {
			t.Fatal("空白模型必须拒绝")
		}
		if err := validateSchedulePatch(QualitySchedulePatch{ExpectedRevision: 1, IntervalMinutes: ptrInt(1)}); err == nil {
			t.Fatal("非法间隔必须拒绝")
		}
		if err := validateSchedulePatch(QualitySchedulePatch{ExpectedRevision: 1, Profile: ptrString("fast")}); err == nil {
			t.Fatal("非法 profile 必须拒绝")
		}
		for name, input := range map[string]QualityScheduleInput{
			"missing account": {Model: "m", IntervalMinutes: 60, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10},
			"bad interval":    {AccountID: "acct", Model: "m", IntervalMinutes: 1, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10},
			"bad values":      {AccountID: "acct", Model: "m", IntervalMinutes: 60, Profile: "quick", PenaltyThreshold: 10, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10},
		} {
			if err := validateSchedule(input); err == nil {
				t.Fatalf("%s 必须拒绝", name)
			}
		}
		if err := validateQualityValues("fast", 70, "fallback", 10); err == nil {
			t.Fatal("非法 profile 必须拒绝")
		}
		if err := validateQualityValues("quick", 70, "bogus", 10); err == nil {
			t.Fatal("非法 action 必须拒绝")
		}
		if err := validateQualityValues("quick", 70, "fallback", 9); err == nil {
			t.Fatal("非法恢复周期必须拒绝")
		}
	})
}

func ptrInt(v int) *int          { return &v }
func ptrString(v string) *string { return &v }

// BusinessRecoveryApplier 分支契约：租约/代际/账户修订任一漂移都只重排
// 而不恢复；恢复窗口外清除为 disabled；非法输入失败关闭。
func TestWBBusinessRecoveryApplierFencedBranches(t *testing.T) {
	ddl := []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,system_account_id TEXT,status TEXT,config_revision INTEGER,schedulable INTEGER,last_error_code TEXT,last_error_message TEXT,availability_schedule_json TEXT,deleted_at TEXT,updated_at TEXT)`,
		`CREATE TABLE account_quality_enforcements (account_id TEXT PRIMARY KEY,system_account_id TEXT,enforcement_id TEXT,generation INTEGER,state TEXT,action TEXT,policy_revision INTEGER,account_config_revision INTEGER,recovery_lease_owner TEXT,recovery_lease_until TEXT,last_recovery_run_id TEXT,recovery_due_at TEXT,cleared_at TEXT,updated_at TEXT)`,
	}
	seed := func(t *testing.T, db *sql.DB, state, action string, policyRevision int, accountStatus string, accountRevision int, schedule string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO accounts (id,system_account_id,status,config_revision,schedulable,last_error_code,availability_schedule_json,updated_at) VALUES ('acct','sys',?,?,0,'model_quality_failed',?,'')`, accountStatus, accountRevision, schedule); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO account_quality_enforcements VALUES ('acct','sys','enf',2,?,'quality_isolate',?,5,'gateway-1','2030-01-01T00:10:00Z',NULL,'2030-01-01T00:00:00Z',NULL,'')`, state, policyRevision, action); err != nil {
			t.Fatal(err)
		}
	}
	when := time.Date(2030, 1, 1, 0, 1, 0, 0, time.UTC)
	baseInput := RecoveryPayload{OwnerID: "gateway-1", AccountID: "acct", EnforcementID: "enf", RunID: "run-x", Generation: 2, PolicyRevision: 7, RecoveryIntervalMinutes: 10, CompletedAt: when}
	t.Run("input validation", func(t *testing.T) {
		db := wbOpenMemoryDB(t, ddl)
		applier, err := NewBusinessRecoveryApplier(db, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := applier.Complete(ctx2(), RecoveryPayload{}, true); err == nil {
			t.Fatal("空输入必须报错")
		}
		if _, err := NewBusinessRecoveryApplier(nil, false); err == nil {
			t.Fatal("nil 数据库必须报错")
		}
	})
	t.Run("stale policy revision only reschedules", func(t *testing.T) {
		db := wbOpenMemoryDB(t, ddl)
		seed(t, db, "active", "quality_isolate", 8, "quality_isolated", 5, "")
		applier, _ := NewBusinessRecoveryApplier(db, false)
		if err := applier.Complete(ctx2(), baseInput, true); err != nil {
			t.Fatal(err)
		}
		var owner string
		var due string
		var state string
		if err := db.QueryRow(`SELECT COALESCE(recovery_lease_owner,''),recovery_due_at,state FROM account_quality_enforcements WHERE account_id='acct'`).Scan(&owner, &due, &state); err != nil {
			t.Fatal(err)
		}
		if owner != "" || due != when.Add(10*time.Minute).Format(time.RFC3339Nano) || state != "active" {
			t.Fatalf("策略漂移必须只重排: owner=%q due=%q state=%q", owner, due, state)
		}
	})
	t.Run("cleared as disabled outside availability", func(t *testing.T) {
		db := wbOpenMemoryDB(t, ddl)
		seed(t, db, "active", "quality_isolate", 7, "quality_isolated", 5, `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"00:00","end":"01:00"}]}`)
		applier, _ := NewBusinessRecoveryApplier(db, false)
		if err := applier.Complete(ctx2(), baseInput, true); err != nil {
			t.Fatal(err)
		}
		var status string
		var schedulable int
		if err := db.QueryRow(`SELECT status,schedulable FROM accounts WHERE id='acct'`).Scan(&status, &schedulable); err != nil {
			t.Fatal(err)
		}
		if status != "disabled" || schedulable != 0 {
			t.Fatalf("窗口外恢复必须禁用: status=%q schedulable=%d", status, schedulable)
		}
	})
	t.Run("account drift only reschedules", func(t *testing.T) {
		for name, mutate := range map[string]struct {
			status   string
			revision int
			schedule string
		}{
			"active status":    {status: "active", revision: 5, schedule: ""},
			"stale revision":   {status: "quality_isolated", revision: 6, schedule: ""},
			"invalid schedule": {status: "quality_isolated", revision: 5, schedule: `{`},
		} {
			t.Run(name, func(t *testing.T) {
				db := wbOpenMemoryDB(t, ddl)
				seed(t, db, "active", "quality_isolate", 7, mutate.status, mutate.revision, mutate.schedule)
				applier, _ := NewBusinessRecoveryApplier(db, false)
				err := applier.Complete(ctx2(), baseInput, true)
				if mutate.schedule == `{` {
					if err == nil || !strings.Contains(err.Error(), "availability") {
						t.Fatalf("非法时间窗必须报错: err=%v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				var state string
				if err := db.QueryRow(`SELECT state FROM account_quality_enforcements WHERE account_id='acct'`).Scan(&state); err != nil {
					t.Fatal(err)
				}
				if state != "active" {
					t.Fatalf("漂移账户只能重排不得清除: state=%q", state)
				}
			})
		}
	})
	t.Run("missing lease or account commits no-op", func(t *testing.T) {
		db := wbOpenMemoryDB(t, ddl)
		applier, _ := NewBusinessRecoveryApplier(db, false)
		if err := applier.Complete(ctx2(), baseInput, true); err != nil {
			t.Fatalf("缺失租约必须幂等成功: %v", err)
		}
		seed(t, db, "closed", "disable", 7, "active", 5, "")
		wrongAction := baseInput
		if err := applier.Complete(ctx2(), wrongAction, true); err != nil {
			t.Fatalf("非隔离动作必须幂等成功: %v", err)
		}
		if err := applier.Complete(ctx2(), RecoveryPayload{OwnerID: "gateway-1", AccountID: "ghost", EnforcementID: "enf", RunID: "run", Generation: 2, PolicyRevision: 7, RecoveryIntervalMinutes: 10, CompletedAt: when}, true); err != nil {
			t.Fatalf("缺失租约账户必须幂等成功: %v", err)
		}
	})
}

func ctx2() context.Context { return context.Background() }

// BusinessEnforcementApplier 分支契约：配置匹配、账户状态门与 CAS 缺一不可。
func TestWBBusinessEnforcementApplierPolicyAndFences(t *testing.T) {
	ddl := []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,system_account_id TEXT,status TEXT,config_revision INTEGER,fallback_enabled INTEGER,super_priority_enabled INTEGER,deleted_at TEXT,schedulable INTEGER,last_error_code TEXT,last_error_message TEXT,updated_at TEXT)`,
		`CREATE TABLE account_quality_enforcements (account_id TEXT PRIMARY KEY,system_account_id TEXT,enforcement_id TEXT UNIQUE,generation INTEGER,state TEXT,action TEXT,trigger_run_id TEXT,config_source TEXT,config_source_id TEXT,policy_revision INTEGER,profile TEXT,penalty_threshold INTEGER,recovery_interval_minutes INTEGER,account_config_revision INTEGER,before_status TEXT,after_status TEXT,fallback_was_enabled INTEGER,super_priority_was_enabled INTEGER,started_at TEXT,recovery_due_at TEXT,created_at TEXT,updated_at TEXT,cleared_at TEXT)`,
		`CREATE TABLE model_quality_schedules (id TEXT PRIMARY KEY,system_account_id TEXT,account_id TEXT,revision INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,model TEXT)`,
		`CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,manual_enforcement_enabled INTEGER,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER)`,
	}
	open := func(t *testing.T) (*sql.DB, *BusinessEnforcementApplier) {
		t.Helper()
		db := wbOpenMemoryDB(t, ddl)
		applier, err := NewBusinessEnforcementApplier(db, false)
		if err != nil {
			t.Fatal(err)
		}
		return db, applier
	}
	t.Run("constructor and identity validation", func(t *testing.T) {
		if _, err := NewBusinessEnforcementApplier(nil, false); err == nil {
			t.Fatal("nil 数据库必须报错")
		}
		_, applier := open(t)
		if err := applier.Apply(ctx2(), QualityEnforcement{Action: "disable", Threshold: 70, Score: 20, RecoveryIntervalMinutes: 10}); err == nil {
			t.Fatal("缺身份必须报错")
		}
		var nilApplier *BusinessEnforcementApplier
		if err := nilApplier.Apply(ctx2(), QualityEnforcement{}); err == nil {
			t.Fatal("nil 适配器必须报错")
		}
	})
	t.Run("input value validation", func(t *testing.T) {
		_, applier := open(t)
		cases := []struct {
			name  string
			input QualityEnforcement
		}{
			{"threshold below range", QualityEnforcement{AccountID: "a", SystemAccountID: "sys", RunID: "r", Action: "fallback", Threshold: 10, Score: 5, RecoveryIntervalMinutes: 10}},
			{"score above threshold", QualityEnforcement{AccountID: "a", SystemAccountID: "sys", RunID: "r", Action: "fallback", Threshold: 70, Score: 90, RecoveryIntervalMinutes: 10}},
			{"interval out of range", QualityEnforcement{AccountID: "a", SystemAccountID: "sys", RunID: "r", Action: "fallback", Threshold: 70, Score: 20, RecoveryIntervalMinutes: 1}},
			{"unknown action", QualityEnforcement{AccountID: "a", SystemAccountID: "sys", RunID: "r", Action: "explode", Threshold: 70, Score: 20, RecoveryIntervalMinutes: 10}},
			{"bad policy revision", QualityEnforcement{AccountID: "a", SystemAccountID: "sys", RunID: "r", Action: "fallback", Threshold: 70, Score: 20, RecoveryIntervalMinutes: 10, PolicyRevision: "x"}},
			{"bad account revision", QualityEnforcement{AccountID: "a", SystemAccountID: "sys", RunID: "r", Action: "fallback", Threshold: 70, Score: 20, RecoveryIntervalMinutes: 10, PolicyRevision: "0", AccountConfigRevision: "0"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if err := applier.Apply(ctx2(), tc.input); err == nil {
					t.Fatal("非法输入必须报错")
				}
			})
		}
	})
	t.Run("policy snapshot must match defaults", func(t *testing.T) {
		db, applier := open(t)
		if _, err := db.Exec(`INSERT INTO model_quality_policies VALUES ('sys',3,'full',1,80,'quality_isolate',20)`); err != nil {
			t.Fatal(err)
		}
		input := QualityEnforcement{AccountID: "acct", SystemAccountID: "sys", RunID: "run-1", Action: "fallback", Threshold: 70, Score: 20, RecoveryIntervalMinutes: 10, PolicyRevision: "0", AccountConfigRevision: "4", Profile: "quick"}
		if err := applier.Apply(ctx2(), input); err == nil || !strings.Contains(err.Error(), "configuration is stale") {
			t.Fatalf("策略漂移必须失败: err=%v", err)
		}
		matched := input
		matched.Profile = "full"
		matched.Action = "quality_isolate"
		matched.Threshold = 80
		matched.RecoveryIntervalMinutes = 20
		matched.PolicyRevision = "3"
		if _, err := db.Exec(`INSERT INTO accounts VALUES ('acct','sys','active',4,0,1,NULL,1,NULL,NULL,NULL)`); err != nil {
			t.Fatal(err)
		}
		if err := applier.Apply(ctx2(), matched); err != nil {
			t.Fatalf("匹配策略必须执行: %v", err)
		}
		var status string
		if err := db.QueryRow(`SELECT status FROM accounts WHERE id='acct'`).Scan(&status); err != nil || status != "quality_isolated" {
			t.Fatalf("质量隔离未生效: status=%q err=%v", status, err)
		}
	})
	t.Run("account fences", func(t *testing.T) {
		cases := []struct {
			name    string
			account string
			need    string
		}{
			{name: "not found", account: "", need: "not found"},
			{name: "deleted", account: "INSERT", need: "revision is stale"},
			{name: "not enforceable", account: "DISABLED", need: "not enforceable"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				db, applier := open(t)
				switch tc.account {
				case "":
				case "INSERT":
					if _, err := db.Exec(`INSERT INTO accounts VALUES ('acct','sys','active',4,0,1,'2020-01-01',1,NULL,NULL,NULL)`); err != nil {
						t.Fatal(err)
					}
				case "DISABLED":
					if _, err := db.Exec(`INSERT INTO accounts VALUES ('acct','sys','disabled',4,0,1,NULL,1,NULL,NULL,NULL)`); err != nil {
						t.Fatal(err)
					}
				}
				input := QualityEnforcement{AccountID: "acct", SystemAccountID: "sys", RunID: "run-1", Action: "fallback", Threshold: 70, Score: 20, RecoveryIntervalMinutes: 10, PolicyRevision: "0", AccountConfigRevision: "4", Profile: "quick"}
				err := applier.Apply(ctx2(), input)
				if tc.account == "" {
					if err == nil || !strings.Contains(err.Error(), tc.need) {
						t.Fatalf("缺失账户必须报错: err=%v", err)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), tc.need) {
					t.Fatalf("%s 必须报错: err=%v", tc.name, err)
				}
			})
		}
	})
	t.Run("disable and fallback mutate account", func(t *testing.T) {
		for name, action := range map[string]string{"disable": "disable", "fallback": "fallback"} {
			t.Run(name, func(t *testing.T) {
				db, applier := open(t)
				if _, err := db.Exec(`INSERT INTO accounts VALUES ('acct','sys','active',4,0,1,NULL,1,NULL,NULL,NULL)`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO model_quality_policies VALUES ('sys',0,'quick',1,70,?,10)`, action); err != nil {
					t.Fatal(err)
				}
				input := QualityEnforcement{AccountID: "acct", SystemAccountID: "sys", RunID: "run-1", Action: action, Threshold: 70, Score: 20, RecoveryIntervalMinutes: 10, PolicyRevision: "0", AccountConfigRevision: "4", Profile: "quick"}
				if err := applier.Apply(ctx2(), input); err != nil {
					t.Fatalf("%s 执行失败: %v", action, err)
				}
				var status string
				var schedulable, fallbackEnabled, superPriority int
				if err := db.QueryRow(`SELECT status,schedulable,fallback_enabled,super_priority_enabled FROM accounts WHERE id='acct'`).Scan(&status, &schedulable, &fallbackEnabled, &superPriority); err != nil {
					t.Fatal(err)
				}
				if action == "disable" && (status != "disabled" || schedulable != 0) {
					t.Fatalf("禁用未生效: status=%q schedulable=%d", status, schedulable)
				}
				if action == "fallback" && (fallbackEnabled != 1 || superPriority != 0 || status != "active") {
					t.Fatalf("回退未生效: status=%q fallback=%d priority=%d", status, fallbackEnabled, superPriority)
				}
			})
		}
	})
	t.Run("stale account revision after prior enforcement", func(t *testing.T) {
		db, applier := open(t)
		if _, err := db.Exec(`INSERT INTO accounts VALUES ('acct','sys','active',4,0,1,NULL,1,NULL,NULL,NULL)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO account_quality_enforcements (account_id,system_account_id,enforcement_id,generation,state,action,trigger_run_id,policy_revision,account_config_revision) VALUES ('acct','sys','old',1,'active','quality_isolate','run-old',0,4)`); err != nil {
			t.Fatal(err)
		}
		input := QualityEnforcement{AccountID: "acct", SystemAccountID: "sys", RunID: "run-2", Action: "fallback", Threshold: 70, Score: 20, RecoveryIntervalMinutes: 10, PolicyRevision: "0", AccountConfigRevision: "5", Profile: "quick"}
		if err := applier.Apply(ctx2(), input); err == nil || !strings.Contains(err.Error(), "revision is stale") {
			t.Fatalf("历史执行后的陈旧修订必须拒绝: err=%v", err)
		}
	})
}

// HTTP 账户选项查询契约：参数校验失败 400，owner 读取失败 500，成功返回列表。
func TestWBHTTPServeAccountOptionsQueryContract(t *testing.T) {
	cases := []struct {
		name   string
		query  string
		status int
		need   string
	}{
		{name: "invalid purpose", query: "purpose=other", status: http.StatusBadRequest, need: "purpose"},
		{name: "keyword too long", query: "purpose=run&keyword=" + strings.Repeat("k", 101), status: http.StatusBadRequest, need: "keyword"},
		{name: "invalid account id", query: "purpose=run&accountId=a,b", status: http.StatusBadRequest, need: "accountId"},
		{name: "invalid limit", query: "purpose=run&limit=51", status: http.StatusBadRequest, need: "limit"},
		{name: "duplicate selected keys", query: "purpose=run&selectedIds=a&selectedIds[]=b", status: http.StatusBadRequest, need: "selectedIds"},
		{name: "invalid selected value", query: "purpose=run&selectedIds[]=a,b", status: http.StatusBadRequest, need: "selectedIds"},
		{name: "too many selected", query: "purpose=run&" + func() string {
			parts := make([]string, 0, 21)
			for i := 0; i < 21; i++ {
				parts = append(parts, "selectedIds=s"+string(rune('a'+i)))
			}
			return strings.Join(parts, "&")
		}(), status: http.StatusBadRequest, need: "selectedIds"},
		{name: "account id with filters", query: "purpose=run&accountId=a&keyword=k", status: http.StatusBadRequest, need: "定点模型选项"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := newTestHTTPHandler()
			handler.AccountOptions = &wbAccountOptionsStub{items: []AccountOption{{ID: "acct-1"}}}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/account-options?"+tc.query, nil))
			if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.need) {
				t.Fatalf("status=%d body=%s want=%d need=%q", response.Code, response.Body.String(), tc.status, tc.need)
			}
		})
	}
	t.Run("success with filters", func(t *testing.T) {
		handler := newTestHTTPHandler()
		handler.AccountOptions = &wbAccountOptionsStub{items: []AccountOption{{ID: "acct-1", Name: "A1"}}}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/account-options?purpose=run&keyword=a1&limit=5&selectedIds=acct-1", nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":"acct-1"`) {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
	t.Run("target account options", func(t *testing.T) {
		handler := newTestHTTPHandler()
		handler.AccountOptions = &wbAccountOptionsStub{items: []AccountOption{{ID: "acct-1"}}}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/account-options?purpose=history&accountId=acct-1&limit=1", nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":"acct-1"`) {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
	t.Run("owner read error", func(t *testing.T) {
		handler := newTestHTTPHandler()
		handler.AccountOptions = &wbAccountOptionsStub{listEr: errors.New("读取失败")}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/account-options?purpose=run", nil))
		if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "读取失败") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
}

// HTTP 详情与运行错误契约：缺失 ID、读取失败与跨租户详情都以 404/500 失败关闭。
func TestWBHTTPServeDetailAndRunErrorPaths(t *testing.T) {
	t.Run("detail empty id", func(t *testing.T) {
		handler := newTestHTTPHandler()
		response := httptest.NewRecorder()
		// ServeHTTP 会先去掉尾部斜杠，空 run ID 的防御分支只能直接触达。
		handler.serveDetail(response, httptest.NewRequest(http.MethodGet, "/runs/x", nil), " ", ManagementScope{ActorSystemAccountID: "sys-1", SelectedSystemAccountID: "sys-1"})
		if response.Code != http.StatusNotFound {
			t.Fatalf("status=%d", response.Code)
		}
	})
	t.Run("detail read error", func(t *testing.T) {
		handler := newTestHTTPHandler()
		handler.Service = contractRunService{detailErr: errors.New("读取失败")}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/runs/run-1", nil))
		if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "读取失败") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
	t.Run("list read error", func(t *testing.T) {
		handler := newTestHTTPHandler()
		handler.Service = errorListService{}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/runs", nil))
		if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "列表失败") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
	t.Run("run detail scope drift returns 404", func(t *testing.T) {
		handler := newTestHTTPHandler()
		handler.Service = contractRunService{
			runResult: RunResult{RunID: "run-x", Status: "completed"},
			detail: map[string]any{
				"id": "run-x", "systemAccountId": "sys-other",
				"requestSummary": map[string]any{}, "resultSummary": map[string]any{}, "checks": []any{},
			},
			found: true,
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "模型检测记录不存在") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
	t.Run("stream detail generic error has no status code", func(t *testing.T) {
		handler := newTestHTTPHandler()
		handler.Service = contractRunService{streamResult: RunResult{RunID: "run-stream"}, detailErr: errors.New("普通失败")}
		// detailErr 在 contractRunService 中只影响 GetRun；SSE 的 detail 读取错误
		// 走 runDetailForStream，同样以 RequestError 语义映射。
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/run/stream", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "event: error") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
}

type errorListService struct{}

func (errorListService) Run(context.Context, RunRequest) (RunResult, error) { return RunResult{}, nil }
func (errorListService) RunStream(context.Context, RunRequest, func(ProgressEvent)) (RunResult, error) {
	return RunResult{}, nil
}
func (errorListService) ListRuns(context.Context, RunListQuery) (any, error) {
	return nil, errors.New("列表失败")
}
func (errorListService) GetRun(context.Context, string) (any, bool, error) {
	return nil, false, errors.New("读取失败")
}

// Resolver() 是 Runtime 依赖的窄适配器：非空源返回自身 Resolve，空源返回 nil。
func TestWBSourceResolverAdapter(t *testing.T) {
	var nilSource *BusinessTargetSource
	if nilSource.Resolver() != nil {
		t.Fatal("空源必须返回 nil resolver")
	}
	db := wbOpenMemoryDB(t, nil)
	source, err := NewBusinessTargetSource(db, false, "secret")
	if err != nil {
		t.Fatal(err)
	}
	resolver := source.Resolver()
	if resolver == nil {
		t.Fatal("已接线源必须返回 resolver")
	}
	_, err = resolver(context.Background(), RunRequest{SystemAccountID: "sys", TargetType: "account", TargetID: "acct", Model: "m"})
	if err == nil || !strings.Contains(err.Error(), "read J3b Business target") {
		t.Fatalf("resolver 必须把请求转发到 Resolve: err=%v", err)
	}
}

// claim 后的复核契约：目标或可信对比在租约建立后发生变化必须失败关闭。
func TestWBRunRejectsDriftAfterClaim(t *testing.T) {
	store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
	defer store.Close()
	request := RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", ConfigRevision: "cfg-1", PolicyRevision: "pol-1"}
	target := func(endpoint string) Target {
		return Target{Endpoint: endpoint, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "hello", DispatchRevision: 1, ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1"}
	}
	t.Run("target resolve fails after claim", func(t *testing.T) {
		var calls atomic.Int64
		runtime := &Runtime{Store: store, OwnerID: "wb-drift", Resolve: func(context.Context, RunRequest) (Target, error) {
			if calls.Add(1) > 1 {
				return Target{}, errors.New("账户已下线")
			}
			return target("https://stable.example"), nil
		}}
		if _, err := runtime.Run(context.Background(), request); err == nil || !strings.Contains(err.Error(), "after claim") {
			t.Fatalf("claim 后解析失败必须报错: err=%v", err)
		}
	})
	t.Run("trusted comparison drifts after claim", func(t *testing.T) {
		var calls atomic.Int64
		comparison := target("https://comparison.example")
		comparison.UpstreamModel = "gpt-5.6-sol"
		comparison.ConfigRevision = "cfg-2"
		comparison.DispatchRevision = 2
		comparison.SourceConfigRevision = "src-2"
		comparison.SourceDispatchRevision = 3
		runtime := &Runtime{
			Store: store, OwnerID: "wb-drift",
			Resolve: func(context.Context, RunRequest) (Target, error) { return target("https://stable.example"), nil },
			ResolveComparison: func(context.Context, RunRequest) (Target, error) {
				if calls.Add(1) > 1 {
					comparison.Endpoint = "https://rotated.example"
				}
				return comparison, nil
			},
		}
		comparisonRequest := request
		comparisonRequest.Profile = "full"
		comparisonRequest.TrustedComparison = true
		comparisonRequest.TrustedComparisonAccountID = "cmp"
		comparisonRequest.TrustedComparisonSystemAccountID = "sys2"
		comparisonRequest.TrustedComparisonConfigRevision = "cfg-2"
		comparisonRequest.TrustedComparisonDispatchRevision = 2
		comparisonRequest.TrustedComparisonSourceConfigRevision = "src-2"
		comparisonRequest.TrustedComparisonSourceDispatchRevision = 3
		if _, err := runtime.Run(context.Background(), comparisonRequest); err == nil || !strings.Contains(err.Error(), "trusted comparison changed after claim") {
			t.Fatalf("可信对比漂移必须报错: err=%v", err)
		}
	})
}
