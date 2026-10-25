package modelcheckowner

import (
	"context"
	"database/sql"
	"testing"

)

// TestBusinessQualityManagerTimeoutPenaltySwitch 覆盖模型检测超时处罚开关
// （timeout_penalty_enabled / timeoutPenaltyEnabled）的配置域贯通：
//   - 手动质量策略：默认视图返回 false；patch 开/关写列并回显；同值 patch
//     不推进 revision（与 manualEnforcementEnabled 同一指针 diff 语义）；
//   - 定时计划：create 缺省 false（默认关闭），显式 true 落列；patch 开/关
//     同语义；scheduleByID 读回一致。
//
// 运行时消费（modelcheckprobe/modelcheckowner runtime、health、summary）不
// 在本文件：这里只验证配置从 API 输入到存储、再回到视图的完整闭环。
func TestBusinessQualityManagerTimeoutPenaltySwitch(t *testing.T) {
	db, err := openPGTestDB(t)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,manual_enforcement_enabled INTEGER,timeout_penalty_enabled BOOLEAN NOT NULL DEFAULT FALSE,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,created_at TEXT,updated_at TEXT,custom_question_ids TEXT)`,
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
	// 第二个账户：schedule 的唯一键是 (system_account_id, account_id)，
	// 显式开启开关的 create 需要独立账户。
	w11eExec(t, db, `INSERT INTO accounts (id,system_account_id,name,vendor_code,connection_driver_code,health_check_connection_id,status) VALUES ('acct-b','sys','Secondary account','gpt','gpt','conn-acct-b','active')`)
	w11eExec(t, db, `INSERT INTO account_connections (id,account_id,connection_preset_code,base_url,enabled) VALUES ('conn-acct-b','acct-b','gpt_openai_v1','https://quality.invalid/v1', 1)`)
	m, err := NewBusinessQualityManager(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	policyColumn := func(t *testing.T) int {
		t.Helper()
		var enabled bool
		if err := db.QueryRow(`SELECT timeout_penalty_enabled FROM model_quality_policies WHERE system_account_id='sys'`).Scan(&enabled); err != nil {
			if err == sql.ErrNoRows {
				return -1
			}
			t.Fatal(err)
		}
		if enabled {
			return 1
		}
		return 0
	}
	scheduleColumn := func(t *testing.T, id string) int {
		t.Helper()
		var enabled bool
		if err := db.QueryRow(`SELECT timeout_penalty_enabled FROM model_quality_schedules WHERE id=?`, id).Scan(&enabled); err != nil {
			t.Fatal(err)
		}
		if enabled {
			return 1
		}
		return 0
	}

	t.Run("policy default view and patch lifecycle", func(t *testing.T) {
		// 无策略行：默认视图关闭（false），不落行。
		view, err := m.Policy(ctx, "sys")
		if err != nil {
			t.Fatal(err)
		}
		if view.TimeoutPenaltyEnabled {
			t.Fatalf("默认视图必须关闭: %+v", view)
		}
		if got := policyColumn(t); got != -1 {
			t.Fatalf("默认视图不得落策略行, got column=%d", got)
		}

		// 开启：patch 写列、回显 true、revision 前进。
		on := true
		enabled, err := m.PatchPolicy(ctx, "sys", QualityPolicyPatch{ExpectedRevision: 0, TimeoutPenaltyEnabled: &on})
		if err != nil {
			t.Fatal(err)
		}
		if !enabled.TimeoutPenaltyEnabled || enabled.Revision != 1 {
			t.Fatalf("开启后视图=%+v", enabled)
		}
		if got := policyColumn(t); got != 1 {
			t.Fatalf("timeout_penalty_enabled=%d, want 1", got)
		}

		// 同值 patch：无操作，revision 不前进。
		same, err := m.PatchPolicy(ctx, "sys", QualityPolicyPatch{ExpectedRevision: enabled.Revision, TimeoutPenaltyEnabled: &on})
		if err != nil {
			t.Fatal(err)
		}
		if same.Revision != enabled.Revision || !same.TimeoutPenaltyEnabled {
			t.Fatalf("同值 patch 必须无操作: %+v", same)
		}

		// 关闭：列回到 0，revision 继续前进。
		off := false
		disabled, err := m.PatchPolicy(ctx, "sys", QualityPolicyPatch{ExpectedRevision: same.Revision, TimeoutPenaltyEnabled: &off})
		if err != nil {
			t.Fatal(err)
		}
		if disabled.TimeoutPenaltyEnabled || disabled.Revision != same.Revision+1 {
			t.Fatalf("关闭后视图=%+v", disabled)
		}
		if got := policyColumn(t); got != 0 {
			t.Fatalf("timeout_penalty_enabled=%d, want 0", got)
		}

		// 与其他字段并存的 patch：开关不回退（指针 diff 只改显式提交的字段）。
		threshold := 88
		mixed, err := m.PatchPolicy(ctx, "sys", QualityPolicyPatch{ExpectedRevision: disabled.Revision, PenaltyThreshold: &threshold, TimeoutPenaltyEnabled: &on})
		if err != nil {
			t.Fatal(err)
		}
		if !mixed.TimeoutPenaltyEnabled || mixed.PenaltyThreshold != 88 {
			t.Fatalf("混合 patch 视图=%+v", mixed)
		}
		if got := policyColumn(t); got != 1 {
			t.Fatalf("混合 patch 后列值=%d, want 1", got)
		}
	})

	t.Run("schedule create and patch lifecycle", func(t *testing.T) {
		// create 缺省：开关默认关闭。
		created, err := m.CreateSchedule(ctx, "sys", QualityScheduleInput{AccountID: "acct", Model: "gpt-5.6-sol", IntervalMinutes: 60, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10})
		if err != nil {
			t.Fatal(err)
		}
		if created.TimeoutPenaltyEnabled {
			t.Fatalf("缺省 create 必须关闭: %+v", created)
		}
		if got := scheduleColumn(t, created.ID); got != 0 {
			t.Fatalf("缺省 create 列值=%d, want 0", got)
		}

		// patch 开启：写列并回显，revision 前进。
		on := true
		enabled, err := m.PatchSchedule(ctx, "sys", created.ID, QualitySchedulePatch{ExpectedRevision: created.Revision, TimeoutPenaltyEnabled: &on})
		if err != nil {
			t.Fatal(err)
		}
		if !enabled.TimeoutPenaltyEnabled || enabled.Revision != created.Revision+1 {
			t.Fatalf("开启后视图=%+v", enabled)
		}
		if got := scheduleColumn(t, created.ID); got != 1 {
			t.Fatalf("patch 后列值=%d, want 1", got)
		}

		// 同值 patch：无操作。
		same, err := m.PatchSchedule(ctx, "sys", created.ID, QualitySchedulePatch{ExpectedRevision: enabled.Revision, TimeoutPenaltyEnabled: &on})
		if err != nil {
			t.Fatal(err)
		}
		if same.Revision != enabled.Revision || !same.TimeoutPenaltyEnabled {
			t.Fatalf("同值 patch 必须无操作: %+v", same)
		}

		// 关闭。
		off := false
		disabled, err := m.PatchSchedule(ctx, "sys", created.ID, QualitySchedulePatch{ExpectedRevision: same.Revision, TimeoutPenaltyEnabled: &off})
		if err != nil {
			t.Fatal(err)
		}
		if disabled.TimeoutPenaltyEnabled || disabled.Revision != same.Revision+1 {
			t.Fatalf("关闭后视图=%+v", disabled)
		}
		if got := scheduleColumn(t, created.ID); got != 0 {
			t.Fatalf("关闭后列值=%d, want 0", got)
		}

		// create 显式开启：新计划直接携带开关。
		explicit, err := m.CreateSchedule(ctx, "sys", QualityScheduleInput{AccountID: "acct-b", Model: "gpt-5.6-sol", IntervalMinutes: 120, Profile: "full", PenaltyThreshold: 80, PenaltyAction: "fallback", RecoveryIntervalMinutes: 30, TimeoutPenaltyEnabled: true})
		if err != nil {
			t.Fatal(err)
		}
		if !explicit.TimeoutPenaltyEnabled {
			t.Fatalf("显式 create 视图=%+v", explicit)
		}
		if got := scheduleColumn(t, explicit.ID); got != 1 {
			t.Fatalf("显式 create 列值=%d, want 1", got)
		}

		// 列表视图与 scheduleByID 读回一致。
		list, err := m.ListSchedules(ctx, "sys", 1, 50)
		if err != nil || list.Total != 2 || len(list.Items) != 2 {
			t.Fatalf("list=%+v err=%v", list, err)
		}
		seen := map[string]bool{}
		for _, item := range list.Items {
			seen[item.ID] = item.TimeoutPenaltyEnabled
		}
		if seen[explicit.ID] != true || seen[created.ID] != false {
			t.Fatalf("列表开关回显不一致: %+v", seen)
		}
		byID, err := m.scheduleByID(ctx, "sys", explicit.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !byID.TimeoutPenaltyEnabled {
			t.Fatalf("scheduleByID 视图=%+v", byID)
		}
	})
}

// TestBusinessQualityManagerBoolBindDialect 锁定 timeout_penalty_enabled 的
// 方言绑定契约：SQLite 绑 INTEGER 0/1，PG 必须绑 Go bool——pgx 拒绝把整数
// 编码进 boolean 列，回归整数绑定会让策略/计划的全部写路径失败。
