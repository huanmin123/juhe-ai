package modelcheckowner

// w14m 覆盖率补强（第三批）：scheduler store、durable claim、input、query、
// run 纯函数、host 构造、business recovery/scheduler 的数据驱动臂，以及
// cutover 证据校验与 trust 纯函数的剩余分支。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

var w14mSchedulerDDL = []string{
	`CREATE TABLE model_check_scheduler_tasks (id TEXT PRIMARY KEY,kind TEXT NOT NULL,due_at TEXT NOT NULL,claim_owner TEXT,claim_until TEXT,fence_token INTEGER NOT NULL DEFAULT 0,state TEXT NOT NULL DEFAULT 'pending',last_error TEXT,completed_at TEXT,payload TEXT NOT NULL,updated_at TEXT NOT NULL)`,
}

func w14mSchedulerStore(t *testing.T) (*Store, *SQLSchedulerSource, *w14mFailpoint) {
	t.Helper()
	ddl := append(runtimeTestDDL(), w14mSchedulerDDL...)
	db, fp := w14mFailDB(t, ddl)
	store := &Store{db: db, mode: "sqlite"}
	source := &SQLSchedulerSource{Store: store, OwnerID: "w14m-gateway", Lease: time.Minute}
	if _, err := db.Exec(`INSERT INTO model_check_scheduler_tasks(id,kind,due_at,payload,updated_at) VALUES ('task-1','scheduled','2026-08-27T10:00:00Z','{"targetId":"acct-1"}','2026-08-27T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	return store, source, fp
}

func TestW14MSchedulerStoreArms(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 27, 10, 1, 0, 0, time.UTC)

	t.Run("beginError", func(t *testing.T) {
		_, source, fp := w14mSchedulerStore(t)
		fp.failBeginsFrom = 1
		defer fp.disarm()
		if _, err := source.Claim(ctx, SchedulerScheduled, now, 10); err == nil {
			t.Fatal("begin 注入应报错")
		}
	})

	t.Run("scanError", func(t *testing.T) {
		_, source, fp := w14mSchedulerStore(t)
		fp.armScan("WHERE kind=? AND state IN")
		defer fp.disarm()
		if _, err := source.Claim(ctx, SchedulerScheduled, now, 10); err == nil {
			t.Fatal("scan 注入应报错")
		}
	})

	t.Run("iterateError", func(t *testing.T) {
		_, source, fp := w14mSchedulerStore(t)
		fp.armNextErr("WHERE kind=? AND state IN")
		defer fp.disarm()
		if _, err := source.Claim(ctx, SchedulerScheduled, now, 10); err == nil {
			t.Fatal("iterate 注入应报错")
		}
	})

	t.Run("leaseError", func(t *testing.T) {
		_, source, fp := w14mSchedulerStore(t)
		fp.arm("SET claim_owner=?,claim_until=?,fence_token=?")
		defer fp.disarm()
		if _, err := source.Claim(ctx, SchedulerScheduled, now, 10); err == nil || !strings.Contains(err.Error(), "lease J3b task") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("commitError", func(t *testing.T) {
		_, source, fp := w14mSchedulerStore(t)
		fp.failCommitsFrom = 1
		defer fp.disarm()
		if _, err := source.Claim(ctx, SchedulerScheduled, now, 10); err == nil {
			t.Fatal("commit 注入应报错")
		}
	})

	t.Run("completeArms", func(t *testing.T) {
		store, source, fp := w14mSchedulerStore(t)
		tasks, err := source.Claim(ctx, SchedulerScheduled, now, 10)
		if err != nil || len(tasks) != 1 {
			t.Fatalf("claim = (%d, %v)", len(tasks), err)
		}
		// 非法输入。
		if err := source.Complete(ctx, ScheduleTask{}); err == nil || !strings.Contains(err.Error(), "input is invalid") {
			t.Fatalf("err = %v", err)
		}
		// 执行错误。
		fp.arm("SET state='completed'")
		if err := source.Complete(ctx, tasks[0]); err == nil || !strings.Contains(err.Error(), "complete J3b task") {
			t.Fatalf("err = %v", err)
		}
		fp.disarm()
		// 长错误消息截断 + 失败写入。
		if err := source.Fail(ctx, tasks[0], errors.New(strings.Repeat("x", 1200))); err != nil {
			t.Fatalf("fail = %v", err)
		}
		var lastError string
		if err := store.db.QueryRow(`SELECT last_error FROM model_check_scheduler_tasks WHERE id='task-1'`).Scan(&lastError); err != nil || len(lastError) != 1000 {
			t.Fatalf("lastError len=%d err=%v", len(lastError), err)
		}
		// 失败执行错误臂。Fail 使用真实时钟顺延 due_at，重取按真实时间推进。
		tasks2, err := source.Claim(ctx, SchedulerScheduled, time.Now().Add(2*time.Minute), 10)
		if err != nil || len(tasks2) != 1 {
			t.Fatalf("reclaim = (%d, %v)", len(tasks2), err)
		}
		fp.arm("SET state='failed'")
		defer fp.disarm()
		if err := source.Fail(ctx, tasks2[0], errors.New("w14m-cause")); err == nil || !strings.Contains(err.Error(), "fail J3b task") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("ensureHealthRetryListError", func(t *testing.T) {
		store, _, fp := w14mSchedulerStore(t)
		fp.arm("quality_health_sync_status='failed'")
		defer fp.disarm()
		if err := store.EnsureHealthRetryTasks(ctx, 10); err == nil {
			t.Fatal("重试扫描注入应报错")
		}
	})
}

func TestW14MDurableClaimArms(t *testing.T) {
	ctx := context.Background()

	t.Run("inputNotFoundAndReadError", func(t *testing.T) {
		db, fp := w14mFailDB(t, runtimeTestDDL())
		store := &Store{db: db, mode: "sqlite"}
		// 输入不存在。
		if _, err := store.ClaimInput(ctx, "ghost", "ct", "ot", "owner", time.Minute, time.Now()); err == nil || !strings.Contains(err.Error(), "input not found") {
			t.Fatalf("err = %v", err)
		}
		// 读取错误。
		fp.arm("SELECT expires_at FROM model_check_inputs")
		defer fp.disarm()
		if _, err := store.ClaimInput(ctx, "ghost", "ct", "ot", "owner", time.Minute, time.Now()); err == nil || strings.Contains(err.Error(), "input not found") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("persistError", func(t *testing.T) {
		db, fp := w14mFailDB(t, runtimeTestDDL())
		store := &Store{db: db, mode: "sqlite"}
		run := w14fOwnerBaseRun(t, store)
		if err := store.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		input := wbInputFixture()
		if _, err := store.IssueInput(ctx, input); err != nil {
			t.Fatal(err)
		}
		// 在输入过期前认领。
		claimNow := input.ExpiresAt.Add(-time.Second)
		fp.arm("INSERT INTO model_check_execution_claims")
		defer fp.disarm()
		if _, err := store.ClaimInput(ctx, input.InputID, "ct", "ot", "owner", time.Minute, claimNow); err == nil || !strings.Contains(err.Error(), "persist J3b claim") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestW14MInputIssueArms(t *testing.T) {
	ctx := context.Background()
	db, fp := w14mFailDB(t, runtimeTestDDL())
	store := &Store{db: db, mode: "sqlite"}
	base := wbInputFixture()

	// 非法 payload。
	bad := base
	bad.Payload = json.RawMessage("{bad-json")
	if _, err := store.IssueInput(ctx, bad); err == nil {
		t.Fatal("非法 payload 应报错")
	}
	if _, err := digestInput(InputRecord{Payload: json.RawMessage("{bad")}); err == nil {
		t.Fatal("非法 payload 的 digest 应报错")
	}
	// 首次签发成功后再次签发命中幂等提交。
	if _, err := store.IssueInput(ctx, base); err != nil {
		t.Fatal(err)
	}
	fp.failCommitsFrom = 1
	defer fp.disarm()
	if _, err := store.IssueInput(ctx, base); err == nil {
		t.Fatal("幂等提交注入应报错")
	}
	fp.disarm()
	// 新输入的新版本提交注入。
	next := base
	next.InputID = "w14m-input-2"
	fp.failCommitsFrom = 1
	if _, err := store.IssueInput(ctx, next); err == nil {
		t.Fatal("签发提交注入应报错")
	}
	fp.disarm()
}

func TestW14MQueryRuntimeArms(t *testing.T) {
	ctx := context.Background()
	db, fp := w14mFailDB(t, runtimeTestDDL())
	store := &Store{db: db, mode: "sqlite"}
	runtime := &Runtime{Store: store}
	run := w14fOwnerBaseRun(t, store)
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	t.Run("listRunsArms", func(t *testing.T) {
		fp.arm("ORDER BY created_at DESC")
		if _, err := runtime.ListRuns(ctx, RunListQuery{SystemAccountID: "sys"}); err == nil || !strings.Contains(err.Error(), "list J3b runs") {
			t.Fatalf("err = %v", err)
		}
		fp.disarm()
		fp.armScan("ORDER BY created_at DESC")
		if _, err := runtime.ListRuns(ctx, RunListQuery{SystemAccountID: "sys"}); err == nil {
			t.Fatal("scan 注入应报错")
		}
		fp.disarm()
		fp.armNextErr("ORDER BY created_at DESC")
		if _, err := runtime.ListRuns(ctx, RunListQuery{SystemAccountID: "sys"}); err == nil {
			t.Fatal("iterate 注入应报错")
		}
		fp.disarm()
	})

	t.Run("getRunReadError", func(t *testing.T) {
		fp.arm("FROM model_check_runs WHERE id=?")
		defer fp.disarm()
		if _, _, err := runtime.GetRun(ctx, run.ID); err == nil || !strings.Contains(err.Error(), "read J3b run") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("runChecksArms", func(t *testing.T) {
		item := ItemRecord{ID: "w14m-run-item-0001", RunID: run.ID, ItemKey: "stability", ItemType: "stability",
			Status: ItemPassed, Score: 100, MaxScore: 100, EvidenceSummary: `{"ok":true}`}
		if err := store.AppendItem(ctx, item); err != nil {
			t.Fatal(err)
		}
		fp.arm("FROM model_check_items")
		if _, _, err := runtime.GetRun(ctx, run.ID); err == nil || !strings.Contains(err.Error(), "read J3b run checks") {
			t.Fatalf("err = %v", err)
		}
		fp.disarm()
		fp.armScan("FROM model_check_items")
		if _, _, err := runtime.GetRun(ctx, run.ID); err == nil {
			t.Fatal("check scan 注入应报错")
		}
		fp.disarm()
		fp.armNextErr("FROM model_check_items")
		if _, _, err := runtime.GetRun(ctx, run.ID); err == nil {
			t.Fatal("check iterate 注入应报错")
		}
		fp.disarm()
	})

	t.Run("mergeNilDetail", func(t *testing.T) {
		runtime.mergeLatestTrustReport(ctx, nil)
	})
}

func TestW14MRunPureHelpers(t *testing.T) {
	// 非对象 JSON → 空 {}。
	if string(normalizeJSON([]byte("not-json"))) != "{}" {
		t.Fatalf("normalizeJSON bad = %s", normalizeJSON([]byte("not-json")))
	}
	// 数组 JSON → 空 {}。
	if string(normalizeJSON([]byte(`[1,2]`))) != "{}" {
		t.Fatalf("normalizeJSON array = %s", normalizeJSON([]byte(`[1,2]`)))
	}
	// 超限键裁剪。
	big := map[string]any{}
	for i := 0; i < 64; i++ {
		big["k"+strings.Repeat("a", i)] = i
	}
	result := sanitizeSummaryValue(big, 0)
	if _, ok := result.(map[string]any); !ok {
		t.Fatalf("summary type = %T", result)
	}
	// 标量 default 分支。
	if got := sanitizeSummaryValue(42, 0); got != "42" {
		t.Fatalf("scalar = %v", got)
	}
}

func TestW14MHostConstructionArms(t *testing.T) {
	// Ready 前置守卫：未装配的 host 必须拒绝挂载。
	var nilHost *Host
	if err := nilHost.MountScoped(nil, "/x/", nil, false); err == nil {
		t.Fatal("nil host 应报错")
	}
	ready := &Host{}
	if err := ready.MountScoped(nil, "/x/", nil, false); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("err = %v", err)
	}
}

func TestW14MHTTPDetailArms(t *testing.T) {
	handler := newTestHTTPHandler()

	t.Run("deleteEmptyID", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		handler.deleteQualitySchedule(recorder, httptest.NewRequest(http.MethodDelete, "/quality-schedules", nil), "sys-1", "  ")
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status=%d", recorder.Code)
		}
	})

	t.Run("detailShape", func(t *testing.T) {
		if hasCompleteRunDetailShape([]byte("{bad")) {
			t.Fatal("坏 JSON 不应判为完整")
		}
		if scopeAllowsRun(ManagementScope{}, map[string]any{}) {
			t.Fatal("无效 scope 不应放行")
		}
	})

	t.Run("streamBuildError", func(t *testing.T) {
		local := newTestHTTPHandler()
		local.BuildScoped = func(context.Context, ManagementScope, RunCommand) (RunRequest, error) {
			return RunRequest{}, errors.New("w14m-build-boom")
		}
		recorder := httptest.NewRecorder()
		local.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/run/stream", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})
}

func TestW14MBusinessModelsPostgresArms(t *testing.T) {
	ctx := context.Background()
	db, fp := w14mBusinessDB(t)
	w14mSeedPlainAccount(t, db, "acct-1")
	profile, _ := modelcheckprofile.Find("openai", "profile_openai_openai_v1")

	// postgres 分支：juhe_business 前缀在 SQLite 上必然失败（覆盖字符串构造臂）。
	if _, err := resolveConfiguredUpstreamModelMapping(ctx, db, true, "acct-1", profile, "gpt-5.6-sol"); err == nil {
		t.Fatal("postgres 模式在 sqlite 上应报错")
	}
	// 支持模型扫描错误。
	fp.armScan("FROM account_supported_models WHERE account_id=?")
	defer fp.disarm()
	if _, err := resolveConfiguredUpstreamModelMapping(ctx, db, false, "acct-1", profile, "gpt-5.6-sol"); err == nil || !strings.Contains(err.Error(), "scan J3b account supported model") {
		t.Fatalf("err = %v", err)
	}
	fp.disarm()
	// 迭代错误。
	fp.armNextErr("FROM account_supported_models WHERE account_id=?")
	if _, err := resolveConfiguredUpstreamModelMapping(ctx, db, false, "acct-1", profile, "gpt-5.6-sol"); err == nil || !strings.Contains(err.Error(), "iterate J3b account supported models") {
		t.Fatalf("err = %v", err)
	}
	fp.disarm()
}

func TestW14MBusinessModelsMappingSupportArms(t *testing.T) {
	ctx := context.Background()
	db, _ := w14mBusinessDB(t)
	w14mSeedPlainAccount(t, db, "acct-1")
	profile, _ := modelcheckprofile.Find("openai", "profile_openai_openai_v1")
	statements := []string{
		`INSERT INTO account_supported_models VALUES ('acct-1','gpt-5.6')`,
		`INSERT INTO account_model_mappings VALUES ('acct-1','gpt-5.6-sol','responses','other-model','responses',1)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	// 映射命中但 upstream 不在 supported 集合 → 拒绝（UpstreamModel 空）。
	resolution, err := resolveConfiguredUpstreamModelMapping(ctx, db, false, "acct-1", profile, "gpt-5.6-sol")
	if err != nil {
		t.Fatal(err)
	}
	if resolution.UpstreamModel == "gpt-5.6" {
		t.Fatalf("supported 之外的 upstream 应被拒绝: %+v", resolution)
	}
}

func TestW14MBusinessRecoveryArms(t *testing.T) {
	ctx := context.Background()
	enforcementDDL := `CREATE TABLE account_quality_enforcements (account_id TEXT PRIMARY KEY,system_account_id TEXT,enforcement_id TEXT,generation INTEGER,state TEXT,action TEXT,policy_revision INTEGER,account_config_revision INTEGER,recovery_lease_owner TEXT,recovery_lease_until TEXT,last_recovery_run_id TEXT,recovery_due_at TEXT,cleared_at TEXT,updated_at TEXT)`
	newDB := func(t *testing.T) (*sql.DB, *w14mFailpoint) {
		ddl := append(businessSourceContractDDL(), enforcementDDL)
		return w14mFailDB(t, ddl)
	}
	when := time.Date(2030, 1, 1, 0, 1, 0, 0, time.UTC)
	base := RecoveryPayload{OwnerID: "gateway-1", AccountID: "acct", EnforcementID: "enf", RunID: "run-1", Generation: 2, PolicyRevision: 7, RecoveryIntervalMinutes: 10, CompletedAt: when}

	t.Run("readError", func(t *testing.T) {
		db, fp := newDB(t)
		w14mSeedPlainAccount(t, db, "acct")
		if _, err := db.Exec(`INSERT INTO account_quality_enforcements VALUES ('acct','sys','enf',2,'active','quality_isolate',7,5,'gateway-1','2030-01-01T00:10:00Z',NULL,'2030-01-01T00:00:00Z',NULL,'')`); err != nil {
			t.Fatal(err)
		}
		fp.arm("FROM account_quality_enforcements WHERE account_id=? AND enforcement_id=?")
		defer fp.disarm()
		applier, err := NewBusinessRecoveryApplier(db, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := applier.Complete(ctx, base, true); err == nil || !strings.Contains(err.Error(), "read J3b recovery lease") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("accountMissing", func(t *testing.T) {
		db, fp := newDB(t)
		if _, err := db.Exec(`INSERT INTO account_quality_enforcements VALUES ('acct','sys','enf',2,'active','quality_isolate',7,5,'gateway-1','2030-01-01T00:10:00Z',NULL,'2030-01-01T00:00:00Z',NULL,'')`); err != nil {
			t.Fatal(err)
		}
		fp.arm("FROM accounts WHERE id=? AND system_account_id=?")
		defer fp.disarm()
		applier, err := NewBusinessRecoveryApplier(db, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := applier.Complete(ctx, base, true); err == nil {
			t.Fatal("账户读取注入应报错")
		}
	})

	t.Run("statusMismatchReschedules", func(t *testing.T) {
		db, fp := newDB(t)
		w14mSeedPlainAccount(t, db, "acct")
		if _, err := db.Exec(`INSERT INTO account_quality_enforcements VALUES ('acct','sys','enf',2,'active','quality_isolate',7,5,'gateway-1','2030-01-01T00:10:00Z',NULL,'2030-01-01T00:00:00Z',NULL,'')`); err != nil {
			t.Fatal(err)
		}
		fp.arm("UPDATE account_quality_enforcements SET recovery_due_at=")
		defer fp.disarm()
		applier, err := NewBusinessRecoveryApplier(db, false)
		if err != nil {
			t.Fatal(err)
		}
		// 账户状态不是 quality_isolated → 仅顺延。
		if err := applier.Complete(ctx, base, true); err != nil {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("notPassedReschedules", func(t *testing.T) {
		db, fp := newDB(t)
		w14mSeedPlainAccount(t, db, "acct")
		if _, err := db.Exec(`UPDATE accounts SET status='quality_isolated',config_revision=5 WHERE id='acct'`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO account_quality_enforcements VALUES ('acct','sys','enf',2,'active','quality_isolate',7,5,'gateway-1','2030-01-01T00:10:00Z',NULL,'2030-01-01T00:00:00Z',NULL,'')`); err != nil {
			t.Fatal(err)
		}
		fp.arm("UPDATE account_quality_enforcements SET recovery_due_at=")
		defer fp.disarm()
		applier, err := NewBusinessRecoveryApplier(db, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := applier.Complete(ctx, base, false); err != nil {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("policyMismatchReschedules", func(t *testing.T) {
		db, fp := newDB(t)
		w14mSeedPlainAccount(t, db, "acct")
		if _, err := db.Exec(`INSERT INTO account_quality_enforcements VALUES ('acct','sys','enf',2,'active','quality_isolate',9,5,'gateway-1','2030-01-01T00:10:00Z',NULL,'2030-01-01T00:00:00Z',NULL,'')`); err != nil {
			t.Fatal(err)
		}
		// 策略修订不一致 → 顺延且不触碰账户。
		fp.arm("UPDATE account_quality_enforcements SET last_recovery_run_id=")
		defer fp.disarm()
		applier, err := NewBusinessRecoveryApplier(db, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := applier.Complete(ctx, base, true); err == nil {
			t.Fatal("顺延写入注入应报错")
		}
	})

	t.Run("restoreSuccess", func(t *testing.T) {
		db, fp := newDB(t)
		w14mSeedPlainAccount(t, db, "acct")
		// 对齐 enforcement 行的租户字段；恢复更新会触碰 last_error_message/updated_at。
		for _, alter := range []string{
			`ALTER TABLE accounts ADD COLUMN last_error_message TEXT`,
			`ALTER TABLE accounts ADD COLUMN updated_at TEXT`,
		} {
			if _, err := db.Exec(alter); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec(`UPDATE accounts SET system_account_id='sys', status='quality_isolated',config_revision=5 WHERE id='acct'`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO account_quality_enforcements VALUES ('acct','sys','enf',2,'active','quality_isolate',7,5,'gateway-1','2030-01-01T00:10:00Z',NULL,'2030-01-01T00:00:00Z',NULL,'')`); err != nil {
			t.Fatal(err)
		}
		fp.arm("SET state='cleared'")
		defer fp.disarm()
		applier, err := NewBusinessRecoveryApplier(db, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := applier.Complete(ctx, base, true); err == nil || !strings.Contains(err.Error(), "w14m") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestW14MCutoverEvidenceArms(t *testing.T) {
	backup := contracts.J3bBackupArtifact{Path: "missing.db", Hash: ""}
	// 不存在的路径。
	if err := verifyConfiguredBackupArtifact(backup); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("err = %v", err)
	}
	// 目录而非常规文件。
	backup.Path = t.TempDir()
	if err := verifyConfiguredBackupArtifact(backup); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("err = %v", err)
	}
	// 清单读取失败。
	reference := contracts.J3bReadbackManifestReference{Path: "missing.json"}
	if err := verifyConfiguredReadbackManifest(reference, contracts.J3bCutoverEvidence{}, time.Now()); err == nil || !strings.Contains(err.Error(), "read manifest") {
		t.Fatalf("err = %v", err)
	}
	// 清单内容非法。
	dir := t.TempDir()
	reference.Path = filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(reference.Path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyConfiguredReadbackManifest(reference, contracts.J3bCutoverEvidence{}, time.Now()); err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatal("非法清单应报错")
	}
}

func TestW14MTrustPureHelpers(t *testing.T) {
	// appendReason 去重。
	reasons := appendReason([]string{"a"}, "a")
	if len(reasons) != 1 {
		t.Fatalf("reasons = %v", reasons)
	}
	// 无证据探针的覆盖率。
	if coverage := evidenceCompleteness(nil); coverage != 0 {
		t.Fatalf("coverage = %d", coverage)
	}
}

var _ = os.Stdout

// ---- trust_store 剩余臂：回执读取、映射状态非法、latest 更新、游标读取 ----

func TestW14MTrustProjectionMoreArms(t *testing.T) {
	ctx := context.Background()

	t.Run("receiptSelectError", func(t *testing.T) {
		db, fp := w14mFailDB(t, runtimeTestDDL())
		store := &Store{db: db, mode: "sqlite"}
		projection := w14mSeedTrustObservation(t, store)
		// 首次投影写入回执；重放时 INSERT 冲突后才走回执读取。
		if err := store.ProjectTrust(ctx, projection); err != nil {
			t.Fatal(err)
		}
		fp.arm("SELECT observation_created_at FROM")
		defer fp.disarm()
		if err := store.ProjectTrust(ctx, projection); err == nil || !strings.Contains(err.Error(), "read existing J3b trust observation receipt") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("mappingStatusInvalid", func(t *testing.T) {
		db, fp := w14mFailDB(t, runtimeTestDDL())
		store := &Store{db: db, mode: "sqlite"}
		if _, err := db.Exec(`INSERT INTO model_check_observations(id,run_id,system_account_id,account_id,provider_code,requested_model,mapped_upstream_model,probe_family,observation_status,identity_status,mapping_status,protocol_status,evidence_coverage,created_at) VALUES ('obs-a','run-1','sys','acct','openai','gpt-5.6','gpt-5.6','protocol_basic','complete','consistent','bogus','consistent',100,'2026-08-31T10:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		projection := TrustProjection{RunID: "run-1", SystemAccountID: "sys", AccountID: "acct", RequestedModel: "gpt-5.6", Report: TrustReport{IdentityStatus: "consistent", MappingStatus: "direct", UsageIntegrityStatus: "insufficient_evidence", ProtocolStatus: "consistent", EvidenceStatus: "stable", EvidenceFormed: true, TrustFormed: true, TrustScore: 1, EvidenceCoverage: 100}}
		fp.arm("SELECT identity_status,mapping_status")
		defer fp.disarm()
		// 非法 mapping_status 折叠为 unknown；最新结果读取注入先命中。
		err := store.ProjectTrust(ctx, projection)
		if err == nil {
			t.Fatal("注入应报错")
		}
	})

	t.Run("cursorSelectError", func(t *testing.T) {
		db, fp := w14mFailDB(t, runtimeTestDDL())
		store := &Store{db: db, mode: "sqlite"}
		projection := w14mSeedTrustObservation(t, store)
		if _, err := store.db.Exec(`INSERT INTO model_trust_aggregation_state(scope_key,cursor_created_at,cursor_id,last_success_at,updated_at) VALUES (?,?,?,?,'')`, trustAggregationScope, "2026-08-30T10:00:00Z", "obs-0", "2026-08-30T10:00:00Z"); err != nil {
			t.Fatal(err)
		}
		fp.arm("SELECT cursor_created_at,cursor_id FROM")
		defer fp.disarm()
		if err := store.ProjectTrust(ctx, projection); err == nil || !strings.Contains(err.Error(), "read J3b trust cursor") {
			t.Fatalf("err = %v", err)
		}
	})
}

// ---- business_scheduler：健康重试与认领错误臂 ----

func w14mSchedulerBusinessDDL(t *testing.T) []string {
	t.Helper()
	ddl := businessSourceContractDDL()
	ddl = append(ddl, `CREATE TABLE model_quality_schedules (id TEXT PRIMARY KEY,revision INTEGER,system_account_id TEXT,account_id TEXT,model TEXT,interval_minutes INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,enabled INTEGER,next_run_at TEXT,lease_owner TEXT,lease_until TEXT,last_run_id TEXT,last_run_at TEXT,last_run_status TEXT,updated_at TEXT)`)
	ddl = append(ddl, `CREATE TABLE account_quality_enforcements (account_id TEXT PRIMARY KEY,system_account_id TEXT,enforcement_id TEXT,generation INTEGER,state TEXT,action TEXT,recovery_model TEXT,account_config_revision INTEGER,policy_revision INTEGER,config_source_id TEXT,profile TEXT,penalty_threshold INTEGER,recovery_interval_minutes INTEGER,recovery_due_at TEXT,recovery_lease_owner TEXT,recovery_lease_until TEXT,updated_at TEXT)`)
	return ddl
}

func TestW14MBusinessSchedulerArms(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	envelope := testCredentialEnvelope(t, "secret", `{"api_key":"key","supported_endpoint_modes":["responses_sse"]}`)

	t.Run("healthRetryEnsureError", func(t *testing.T) {
		business, fp := w14mFailDB(t, w14mSchedulerBusinessDDL(t))
		store := &Store{db: business, mode: "sqlite"}
		source := &BusinessSchedulerSource{Business: business, Store: store, OwnerID: "w14m-owner"}
		fp.arm("quality_health_sync_status='failed'")
		defer fp.disarm()
		if _, err := source.Claim(ctx, SchedulerHealthRetry, now, 10); err == nil {
			t.Fatal("健康重试扫描注入应报错")
		}
	})

	t.Run("scheduledClaimQueryError", func(t *testing.T) {
		business, fp := w14mFailDB(t, w14mSchedulerBusinessDDL(t))
		w14mSeedPlainAccount(t, business, "acct")
		store := &Store{db: business, mode: "sqlite"}
		source := &BusinessSchedulerSource{Business: business, Store: store, OwnerID: "w14m-owner"}
		if _, err := business.Exec(`INSERT INTO model_quality_schedules VALUES ('sch',3,'sys-1','acct','gpt-5.6-sol',60,'quick',70,'fallback',15,1,?,NULL,NULL,NULL,NULL,NULL,'')`, now.Add(-time.Minute).Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		fp.arm("FROM model_quality_schedules")
		defer fp.disarm()
		if _, err := source.Claim(ctx, SchedulerScheduled, now, 10); err == nil {
			t.Fatal("认领查询注入应报错")
		}
	})

	t.Run("scheduledClaimScanError", func(t *testing.T) {
		business, fp := w14mFailDB(t, w14mSchedulerBusinessDDL(t))
		w14mSeedPlainAccount(t, business, "acct")
		store := &Store{db: business, mode: "sqlite"}
		source := &BusinessSchedulerSource{Business: business, Store: store, OwnerID: "w14m-owner"}
		if _, err := business.Exec(`INSERT INTO model_quality_schedules VALUES ('sch',3,'sys-1','acct','gpt-5.6-sol',60,'quick',70,'fallback',15,1,?,NULL,NULL,NULL,NULL,NULL,'')`, now.Add(-time.Minute).Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		fp.armScan("FROM model_quality_schedules")
		defer fp.disarm()
		if _, err := source.Claim(ctx, SchedulerScheduled, now, 10); err == nil {
			t.Fatal("认领扫描注入应报错")
		}
	})

	t.Run("recoveryClaimQueryError", func(t *testing.T) {
		business, fp := w14mFailDB(t, w14mSchedulerBusinessDDL(t))
		w14mSeedPlainAccount(t, business, "acct")
		store := &Store{db: business, mode: "sqlite"}
		source := &BusinessSchedulerSource{Business: business, Store: store, OwnerID: "w14m-owner"}
		if _, err := business.Exec(`INSERT INTO account_quality_enforcements VALUES ('acct','sys','enf',2,'active','quality_isolate','gpt-5.6-sol',5,7,'','quick',70,10,?,NULL,NULL,'')`, now.Add(-time.Minute).Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		fp.arm("aqe.state='active' AND aqe.action='quality_isolate'")
		defer fp.disarm()
		if _, err := source.Claim(ctx, SchedulerQualityRecovery, now, 10); err == nil {
			t.Fatal("恢复认领查询注入应报错")
		}
	})

	t.Run("recoveryClaimScanError", func(t *testing.T) {
		business, fp := w14mFailDB(t, w14mSchedulerBusinessDDL(t))
		w14mSeedPlainAccount(t, business, "acct")
		if _, err := business.Exec(`UPDATE accounts SET credentials_encrypted='` + envelope + `' WHERE id='acct'`); err != nil {
			t.Fatal(err)
		}
		store := &Store{db: business, mode: "sqlite"}
		source := &BusinessSchedulerSource{Business: business, Store: store, OwnerID: "w14m-owner"}
		if _, err := business.Exec(`INSERT INTO account_quality_enforcements VALUES ('acct','sys','enf',2,'active','quality_isolate','gpt-5.6-sol',5,7,'','quick',70,10,?,NULL,NULL,'')`, now.Add(-time.Minute).Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		fp.armScan("aqe.state='active' AND aqe.action='quality_isolate'")
		defer fp.disarm()
		if _, err := source.Claim(ctx, SchedulerQualityRecovery, now, 10); err == nil {
			t.Fatal("恢复认领扫描注入应报错")
		}
	})
}

// ---- business_source：可信对比冻结臂 ----

func TestW14MBuildRequestComparisonFreezeArms(t *testing.T) {
	ctx := context.Background()
	db, fp := w14mBusinessDB(t)
	w14mSeedPlainAccount(t, db, "acct-a")
	w14mSeedPlainAccount(t, db, "acct-b")
	source := w11eNewSource(t, db)
	command := RunCommand{TargetType: "account", TargetID: "acct-a", Model: "gpt-5.6-sol", TrustedComparison: true, TrustedComparisonID: "acct-b"}

	cases := []struct {
		name      string
		threshold int
		need      string
	}{
		{"comparisonResolveError", 4, "可信对比账户不支持请求模型或协议"},
		{"comparisonFenceError", 5, "read J3b trusted comparison fence"},
		{"comparisonRecheckError", 6, "trusted comparison changed while freezing"},
		{"comparisonFenceChanged", 7, "trusted comparison fence changed while freezing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fp.armAfter("ORDER BY model", tc.threshold)
			defer fp.disarm()
			_, err := source.BuildRequest(ctx, "sys-1", command)
			if err == nil || !strings.Contains(err.Error(), tc.need) {
				t.Fatalf("err = %v, want 包含 %q", err, tc.need)
			}
		})
	}
}
