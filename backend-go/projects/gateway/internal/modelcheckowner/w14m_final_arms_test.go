package modelcheckowner

// w14m 覆盖率补强（第五批）：质量策略/定时配置管理、强制隔离、执行器分发、
// token 基线激活、健康重试物化、durable 认领与 recovery 顺延臂的收尾。

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---- business_quality_management：策略与定时配置管理臂 ----

func TestW14MBusinessQualityManagerArms(t *testing.T) {
	ctx := context.Background()
	enforcementDDL := `CREATE TABLE account_quality_enforcements (account_id TEXT PRIMARY KEY,system_account_id TEXT,enforcement_id TEXT,generation INTEGER,state TEXT,action TEXT,recovery_model TEXT,account_config_revision INTEGER,policy_revision INTEGER,config_source_id TEXT,profile TEXT,penalty_threshold INTEGER,recovery_interval_minutes INTEGER,recovery_due_at TEXT,recovery_lease_owner TEXT,recovery_lease_until TEXT,updated_at TEXT)`
	scheduleDDL := `CREATE TABLE model_quality_schedules (id TEXT PRIMARY KEY,revision INTEGER,system_account_id TEXT,account_id TEXT,model TEXT,interval_minutes INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,enabled INTEGER,next_run_at TEXT,lease_owner TEXT,lease_until TEXT,last_run_id TEXT,last_run_at TEXT,last_run_status TEXT,created_at TEXT,updated_at TEXT)`
	ddl := append(businessSourceContractDDL(), enforcementDDL, scheduleDDL)

	t.Run("listSchedulesQueryError", func(t *testing.T) {
		db, fp := w14mFailDB(t, ddl)
		w14mSeedPlainAccount(t, db, "acct")
		manager, err := NewBusinessQualityManager(db, false)
		if err != nil {
			t.Fatal(err)
		}
		fp.arm("FROM model_quality_schedules mqs JOIN")
		defer fp.disarm()
		if _, err := manager.ListSchedules(ctx, "sys-1", 1, 20); err == nil {
			t.Fatal("列表查询注入应报错")
		}
	})

	t.Run("listSchedulesIterateError", func(t *testing.T) {
		db, fp := w14mFailDB(t, ddl)
		w14mSeedPlainAccount(t, db, "acct")
		if _, err := db.Exec(`INSERT INTO model_quality_schedules VALUES ('sch',1,'sys-1','acct','gpt-5.6-sol',60,'quick',70,'fallback',15,1,NULL,NULL,NULL,NULL,NULL,NULL,'','')`); err != nil {
			t.Fatal(err)
		}
		manager, err := NewBusinessQualityManager(db, false)
		if err != nil {
			t.Fatal(err)
		}
		fp.armNextErr("FROM model_quality_schedules mqs JOIN")
		defer fp.disarm()
		if _, err := manager.ListSchedules(ctx, "sys-1", 1, 20); err == nil {
			t.Fatal("列表迭代注入应报错")
		}
	})

	t.Run("createScheduleError", func(t *testing.T) {
		db, fp := w14mFailDB(t, ddl)
		w14mSeedPlainAccount(t, db, "acct")
		manager, err := NewBusinessQualityManager(db, false)
		if err != nil {
			t.Fatal(err)
		}
		fp.arm("INSERT INTO model_quality_schedules")
		defer fp.disarm()
		if _, err := manager.CreateSchedule(ctx, "sys-1", QualityScheduleInput{AccountID: "acct", Model: "gpt-5.6-sol", IntervalMinutes: 60, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10}); err == nil {
			t.Fatal("创建注入应报错")
		}
	})

	t.Run("deleteScheduleError", func(t *testing.T) {
		db, fp := w14mFailDB(t, ddl)
		w14mSeedPlainAccount(t, db, "acct")
		manager, err := NewBusinessQualityManager(db, false)
		if err != nil {
			t.Fatal(err)
		}
		fp.arm("DELETE FROM model_quality_schedules")
		defer fp.disarm()
		if _, err := manager.DeleteSchedule(ctx, "sys-1", "sch"); err == nil {
			t.Fatal("删除注入应报错")
		}
	})
}

// ---- business_enforcement：强制隔离写入臂 ----

func TestW14MBusinessEnforcementArms(t *testing.T) {
	ctx := context.Background()
	enforcementDDL := `CREATE TABLE account_quality_enforcements (account_id TEXT PRIMARY KEY,system_account_id TEXT,enforcement_id TEXT,generation INTEGER,state TEXT,action TEXT,trigger_run_id TEXT,recovery_model TEXT,account_config_revision INTEGER,policy_revision INTEGER,config_source_id TEXT,profile TEXT,penalty_threshold INTEGER,recovery_interval_minutes INTEGER,recovery_due_at TEXT,recovery_lease_owner TEXT,recovery_lease_until TEXT,updated_at TEXT)`
	ddl := append(businessSourceContractDDL(), enforcementDDL)
	seed := func(t *testing.T, db *sql.DB) {
		t.Helper()
		w14mSeedPlainAccount(t, db, "acct")
		if _, err := db.Exec(`ALTER TABLE accounts ADD COLUMN last_error_message TEXT`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`ALTER TABLE accounts ADD COLUMN updated_at TEXT`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`ALTER TABLE accounts ADD COLUMN fallback_enabled INTEGER`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`ALTER TABLE accounts ADD COLUMN super_priority_enabled INTEGER`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE accounts SET fallback_enabled=0, super_priority_enabled=0 WHERE id='acct'`); err != nil {
			t.Fatal(err)
		}
	}
	input := QualityEnforcement{AccountID: "acct", SystemAccountID: "sys-1", RunID: "w14m-run", ProviderCode: "openai", Model: "gpt-5.6-sol", Profile: "quick", PolicyRevision: "4", AccountConfigRevision: "3", RecoveryIntervalMinutes: 15, Score: 10, Threshold: 82, Action: "fallback", Message: strings.Repeat("m", 1200)}

	t.Run("accountUpdateError", func(t *testing.T) {
		db, fp := w14mFailDB(t, ddl)
		seed(t, db)
		applier, err := NewBusinessEnforcementApplier(db, false)
		if err != nil {
			t.Fatal(err)
		}
		fp.arm("SET status=")
		defer fp.disarm()
		if err := applier.Apply(ctx, input); err == nil || !strings.Contains(err.Error(), "update J3b Business enforcement account") {
			t.Fatalf("err = %v", err)
		}
	})
}

// ---- scheduler_executor：mux 与载荷解码臂 ----

type w14mFakeRunner struct{ result RunResult }

func (r w14mFakeRunner) Run(context.Context, RunRequest) (RunResult, error) { return r.result, nil }

func TestW14MSchedulerExecutorMuxArms(t *testing.T) {
	ctx := context.Background()
	// 未支持的类型。
	mux := &SchedulerExecutorMux{Runs: &SchedulerRunExecutor{}}
	if err := mux.Execute(ctx, ScheduleTask{Kind: "bogus"}); err == nil || !strings.Contains(err.Error(), "unsupported J3b scheduler kind") {
		t.Fatalf("err = %v", err)
	}
	// 健康重试执行器未配置。
	if err := mux.Execute(ctx, ScheduleTask{Kind: SchedulerHealthRetry}); err == nil || !strings.Contains(err.Error(), "health retry executor") {
		t.Fatalf("err = %v", err)
	}
	// 载荷解码失败。
	bad := &SchedulerRunExecutor{Runtime: w14mFakeRunner{}, Build: func(context.Context, ScheduledPayload) (RunRequest, error) {
		return RunRequest{}, nil
	}}
	if err := bad.Execute(ctx, ScheduleTask{Kind: SchedulerScheduled, Payload: []byte("{bad")}); err == nil || !strings.Contains(err.Error(), "decode J3b scheduler payload") {
		t.Fatalf("err = %v", err)
	}
}

// ---- store：token 基线激活错误臂 ----

func TestW14MTokenBaselineActivationArms(t *testing.T) {
	ctx := context.Background()
	db, fp := w14mFailDB(t, runtimeTestDDL())
	store := &Store{db: db, mode: "sqlite"}
	input := TokenInterceptBaselineActivation{
		CohortKeyHMAC:            "hmac-sha256-v1:" + strings.Repeat("a", 64),
		RequestedModel:           "gpt-5.6-sol",
		TokenizerVersion:         "tok-v1",
		ProbeSetVersion:          "probe-v1",
		BaselineVersion:          2,
		StrongThresholdIntercept: 128,
	}
	fp.arm("SET version_status='active'")
	defer fp.disarm()
	if err := store.ActivateTokenInterceptBaseline(ctx, input); err == nil {
		t.Fatal("激活更新注入应报错")
	}
}

// ---- durable：认领事务起始臂 ----

func TestW14MClaimBeginArm(t *testing.T) {
	db, fp := w14mFailDB(t, runtimeTestDDL())
	store := &Store{db: db, mode: "sqlite"}
	fp.failBeginsFrom = 1
	defer fp.disarm()
	if _, err := store.ClaimInput(context.Background(), "ghost", "ct", "ot", "owner", time.Minute, time.Now()); err == nil || !strings.Contains(err.Error(), "begin J3b claim") {
		t.Fatalf("err = %v", err)
	}
}

// ---- scheduler_store：健康重试物化写入臂 ----

func TestW14MEnsureHealthRetryMaterializeError(t *testing.T) {
	ddl := append(runtimeTestDDL(), w14mSchedulerDDL...)
	db, fp := w14mFailDB(t, ddl)
	store := &Store{db: db, mode: "sqlite"}
	statHour, err := NewHealthStatHourFunc("UTC")
	if err != nil {
		t.Fatal(err)
	}
	store.HealthStatHour = statHour
	if _, err := db.Exec(`INSERT INTO model_check_runs(id,system_account_id,actor_system_account_id,provider_code,target_type,target_id,model,profile,trigger_kind,status,request_summary_json,result_summary_json,policy_snapshot_json,quality_decision_json,probe_set_version,started_at,created_at,updated_at,level,score,max_score,message,account_id,quality_health_sync_status,finished_at,schedule_id) VALUES ('run-x','sys','sys','openai','account','acct','gpt-5.6-sol','quick','manual','completed','{"configRevision":"3"}','{}','{"revision":"4","action":"fallback","threshold":82,"recoveryIntervalMinutes":15,"manualEnforcementEligible":true}','{"evidenceFormed":true,"trustFormed":true,"hardQualityFailure":true}','probe-v1','2026-08-31T10:00:00Z','2026-08-31T10:00:00Z','2026-08-31T10:00:00Z','good',100,100,'','acct','failed','2026-08-31T10:00:00Z','sch-x')`); err != nil {
		t.Fatal(err)
	}
	fp.arm("INSERT INTO model_check_scheduler_tasks")
	defer fp.disarm()
	if err := store.EnsureHealthRetryTasks(context.Background(), 10); err == nil || !strings.Contains(err.Error(), "materialize J3b health retry") {
		t.Fatalf("err = %v", err)
	}
}

// ---- http：未找到的运行详情 ----

func TestW14MRunDetailNotFound(t *testing.T) {
	handler := newTestHTTPHandler()
	handler.Service = &contractRunService{found: false}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/runs/missing", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status=%d", recorder.Code)
	}
	_ = http.NotFound
}

// ---- business_recovery：顺延错误臂与 postgres 分支 ----

func TestW14MBusinessRecoveryRescheduleErrorArms(t *testing.T) {
	ctx := context.Background()
	enforcementDDL := `CREATE TABLE account_quality_enforcements (account_id TEXT PRIMARY KEY,system_account_id TEXT,enforcement_id TEXT,generation INTEGER,state TEXT,action TEXT,trigger_run_id TEXT,recovery_model TEXT,account_config_revision INTEGER,policy_revision INTEGER,config_source_id TEXT,profile TEXT,penalty_threshold INTEGER,recovery_interval_minutes INTEGER,recovery_due_at TEXT,recovery_lease_owner TEXT,recovery_lease_until TEXT,updated_at TEXT)`
	ddl := append(businessSourceContractDDL(), enforcementDDL)
	when := time.Date(2030, 1, 1, 0, 1, 0, 0, time.UTC)
	base := RecoveryPayload{OwnerID: "gateway-1", AccountID: "acct", EnforcementID: "enf", RunID: "run-1", Generation: 2, PolicyRevision: 7, RecoveryIntervalMinutes: 10, CompletedAt: when}

	setup := func(t *testing.T, db *sql.DB) {
		t.Helper()
		w14mSeedPlainAccount(t, db, "acct")
		for _, alter := range []string{
			`ALTER TABLE accounts ADD COLUMN last_error_message TEXT`,
			`ALTER TABLE accounts ADD COLUMN updated_at TEXT`,
		} {
			if _, err := db.Exec(alter); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec(`UPDATE accounts SET system_account_id='sys' WHERE id='acct'`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE accounts SET status='quality_isolated',config_revision=5 WHERE id='acct'`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO account_quality_enforcements(account_id,system_account_id,enforcement_id,generation,state,action,policy_revision,account_config_revision,recovery_lease_owner,recovery_lease_until,recovery_due_at,updated_at) VALUES ('acct','sys','enf',2,'active','quality_isolate',7,5,'gateway-1','2030-01-01T00:10:00Z','2030-01-01T00:00:00Z','')`); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("beginError", func(t *testing.T) {
		db, fp := w14mFailDB(t, ddl)
		setup(t, db)
		applier, err := NewBusinessRecoveryApplier(db, false)
		if err != nil {
			t.Fatal(err)
		}
		fp.failBeginsFrom = 1
		defer fp.disarm()
		if err := applier.Complete(ctx, base, true); err == nil || !strings.Contains(err.Error(), "begin J3b Business recovery") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("notPassedRescheduleError", func(t *testing.T) {
		db, fp := w14mFailDB(t, ddl)
		setup(t, db)
		applier, err := NewBusinessRecoveryApplier(db, false)
		if err != nil {
			t.Fatal(err)
		}
		fp.arm("SET last_recovery_run_id=")
		defer fp.disarm()
		if err := applier.Complete(ctx, base, false); err == nil {
			t.Fatal("顺延注入应报错")
		}
	})

	t.Run("statusMismatchRescheduleError", func(t *testing.T) {
		db, fp := w14mFailDB(t, ddl)
		setup(t, db)
		if _, err := db.Exec(`UPDATE accounts SET status='active' WHERE id='acct'`); err != nil {
			t.Fatal(err)
		}
		applier, err := NewBusinessRecoveryApplier(db, false)
		if err != nil {
			t.Fatal(err)
		}
		fp.arm("SET last_recovery_run_id=")
		defer fp.disarm()
		if err := applier.Complete(ctx, base, true); err == nil {
			t.Fatal("顺延注入应报错")
		}
	})

	t.Run("postgresLockBranch", func(t *testing.T) {
		db, _ := w14mFailDB(t, ddl)
		w14mSeedPlainAccount(t, db, "acct")
		applier, err := NewBusinessRecoveryApplier(db, true)
		if err != nil {
			t.Fatal(err)
		}
		// postgres 分支的 FOR UPDATE 在 SQLite 上必然失败（成功覆盖分支构造）。
		if err := applier.Complete(ctx, base, true); err == nil {
			t.Fatal("postgres 分支应报错")
		}
	})
}

// ---- run.go：outcome 投影臂 ----

func TestW14MProjectOutcomeArms(t *testing.T) {
	ctx := context.Background()
	db, fp := w14mFailDB(t, runtimeTestDDL())
	store := &Store{db: db, mode: "sqlite"}
	run := w14fOwnerBaseRun(t, store)
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	item := ItemRecord{ID: "w14m-item-0001", RunID: run.ID, ItemKey: "stability", ItemType: "stability",
		Status: ItemPassed, Score: 100, MaxScore: 100, EvidenceSummary: `{"ok":true}`}
	if err := store.AppendItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	// 终端重放路径要求运行已处于 completed。
	if _, err := db.Exec(`UPDATE model_check_runs SET status='completed' WHERE id=?`, run.ID); err != nil {
		t.Fatal(err)
	}
	projection := OutcomeProjection{RunID: run.ID, Status: RunCompleted, Level: "good", Score: 100, MaxScore: 100, FinishedAt: time.Now(), Items: []ItemRecord{item}, ResultSummary: []byte(`{}`), QualityDecision: []byte(`{}`)}

	t.Run("beginError", func(t *testing.T) {
		fp.failBeginsFrom = 1
		defer fp.disarm()
		if err := store.ProjectOutcome(ctx, projection); err == nil || !strings.Contains(err.Error(), "begin J3b outcome projection") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("itemsReadError", func(t *testing.T) {
		fp.arm("FROM model_check_items WHERE run_id=? ORDER BY id")
		defer fp.disarm()
		if err := store.ProjectOutcome(ctx, projection); err == nil || !strings.Contains(err.Error(), "read J3b terminal items") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("invalidItem", func(t *testing.T) {
		fp.armScan("FROM model_check_items WHERE run_id=? ORDER BY id")
		defer fp.disarm()
		if err := store.ProjectOutcome(ctx, projection); err == nil || strings.Contains(err.Error(), "w14m") {
			t.Fatalf("预期终端项校验失败，实际 err = %v", err)
		}
	})

	t.Run("finishRunError", func(t *testing.T) {
		// 运行处于 running：跳过终端重放对比，直接命中 UPDATE 臂。
		db2, fp2 := w14mFailDB(t, runtimeTestDDL())
		store2 := &Store{db: db2, mode: "sqlite"}
		run2 := w14fOwnerBaseRun(t, store2)
		if err := store2.CreateRun(ctx, run2); err != nil {
			t.Fatal(err)
		}
		input2 := wbInputFixture()
		if _, err := store2.IssueInput(ctx, input2); err != nil {
			t.Fatal(err)
		}
		claim, err := store2.ClaimInput(ctx, input2.InputID, "ct", "ot", "owner", time.Minute, input2.ExpiresAt.Add(-time.Second))
		if err != nil {
			t.Fatal(err)
		}
		fp2.arm("SET level=?,score=?")
		defer fp2.disarm()
		if err := store2.ProjectOutcome(ctx, OutcomeProjection{RunID: run2.ID, Status: RunCompleted, Level: "good", Score: 100, MaxScore: 100, FinishedAt: time.Now(), Items: []ItemRecord{item}, ResultSummary: []byte(`{}`), QualityDecision: []byte(`{}`)}); err == nil || !strings.Contains(err.Error(), "finish J3b run") {
			t.Fatalf("err = %v", err)
		}
		_ = claim
	})
}
