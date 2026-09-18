package modelcheckowner

// w14f_owner_store_arms_test.go：store/input/outcome/query/durable/
// model_limits/config 的剩余错误臂与过滤分支。复用 w13g2 的失败注入驱动
// （w13g2McFailStore）与 wb 的内存库（wbOpenMemoryDB + runtimeTestDDL）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---- OpenStore / CheckSchema ----

func TestW14fOwnerOpenStoreModeArms(t *testing.T) {
	if _, err := OpenStore(Config{Enabled: true, BusinessHandoffConfirmed: true, NodeWriterStopped: true, SchemaReady: true, HealthBoundaryReady: true, RuntimeReady: true, StoreMode: "w14f-bogus"}); err == nil ||
		!strings.Contains(err.Error(), "unsupported J3b store mode") {
		t.Fatalf("不支持的模式应报错: %v", err)
	}
}

func TestW14fOwnerCheckSchemaArms(t *testing.T) {
	// 缺表 → 报错。
	store := &Store{db: wbOpenMemoryDB(t, nil), mode: "sqlite"}
	if err := store.CheckSchema(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "missing table") {
		t.Fatalf("缺表应报错: %v", err)
	}
	// 缺列 → 报错：为全部必需表建单列壳表，必需列校验必然失败。
	statements := []string{}
	for _, table := range requiredTables {
		statements = append(statements, "CREATE TABLE "+table+" (placeholder TEXT)")
	}
	db := wbOpenMemoryDB(t, statements)
	store = &Store{db: db, mode: "sqlite"}
	if err := store.CheckSchema(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "missing columns") {
		t.Fatalf("缺列应报错: %v", err)
	}
}

// ---- ActivateTokenInterceptBaseline 事务臂 ----

func w14fBaselineActivation() TokenInterceptBaselineActivation {
	return TokenInterceptBaselineActivation{
		CohortKeyHMAC: "hmac-sha256-v1:" + strings.Repeat("a", 64), RequestedModel: "gpt-5.6-sol", TokenizerVersion: "tok-v1",
		ProbeSetVersion: "probe-v1", BaselineVersion: 3, StrongThresholdIntercept: 0.4,
		CalibrationNote: "w14f 校准记录",
	}
}

const w14fBaselineDDL = `CREATE TABLE model_token_intercept_baseline_versions (
	cohort_key_hmac TEXT NOT NULL, requested_model TEXT NOT NULL, tokenizer_version TEXT NOT NULL,
	probe_set_version TEXT NOT NULL, baseline_version INTEGER NOT NULL, version_status TEXT NOT NULL,
	evidence_status TEXT NOT NULL, independent_source_count INTEGER NOT NULL, q90_intercept REAL,
	strong_threshold_intercept REAL NOT NULL DEFAULT 0, strong_gate_enabled INTEGER NOT NULL DEFAULT 0,
	calibration_note TEXT, updated_at TEXT, PRIMARY KEY (cohort_key_hmac, requested_model, tokenizer_version, probe_set_version, baseline_version))`

func w14fInsertBaseline(t *testing.T, db *sql.DB, status, evidence string, independent int, q90 any) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO model_token_intercept_baseline_versions
		(cohort_key_hmac,requested_model,tokenizer_version,probe_set_version,baseline_version,version_status,evidence_status,independent_source_count,q90_intercept)
		VALUES (?, 'gpt-5.6-sol','tok-v1','probe-v1',3,?,?,?,?)`, "hmac-sha256-v1:"+strings.Repeat("a", 64), status, evidence, independent, q90)
	if err != nil {
		t.Fatal(err)
	}
}

func TestW14fOwnerActivateBaselineArms(t *testing.T) {
	ctx := context.Background()
	// PostgreSQL 分支：FOR UPDATE 拼接 + $N 绑定在 SQLite 注入库上必然失败，
	// 覆盖 PG 分支与读取失败臂。
	pgStore := &Store{db: wbOpenMemoryDB(t, []string{w14fBaselineDDL}), mode: "postgres", schema: "juhe_j3b"}
	if err := pgStore.ActivateTokenInterceptBaseline(ctx, w14fBaselineActivation()); err == nil ||
		!strings.Contains(err.Error(), "storage unavailable") {
		t.Fatalf("PG 分支应报 storage unavailable: %v", err)
	}

	s, fp := w13g2McFailStore(t)
	if _, err := s.db.Exec(w14fBaselineDDL); err != nil {
		t.Fatal(err)
	}
	// 候选读取失败（failpoint）。
	fp.arm("FROM model_token_intercept_baseline_versions")
	if err := s.ActivateTokenInterceptBaseline(ctx, w14fBaselineActivation()); err == nil ||
		!strings.Contains(err.Error(), "read candidate version") {
		t.Fatalf("候选读取失败应报错: %v", err)
	}
	fp.disarm()
	// 候选不存在 → 冲突。
	if err := s.ActivateTokenInterceptBaseline(ctx, w14fBaselineActivation()); !errors.Is(err, ErrTokenInterceptBaselineConflict) {
		t.Fatalf("候选不存在应冲突: %v", err)
	}
	// 候选不合格（independent < 10）。
	w14fInsertBaseline(t, s.db, "calibration_pending", "stable", 3, 0.1)
	if err := s.ActivateTokenInterceptBaseline(ctx, w14fBaselineActivation()); !errors.Is(err, ErrTokenInterceptBaselineConflict) {
		t.Fatalf("不合格候选应冲突: %v", err)
	}
	// 候选合格：退役旧版本失败。
	if _, err := s.db.Exec(`UPDATE model_token_intercept_baseline_versions SET independent_source_count=12 WHERE version_status='calibration_pending'`); err != nil {
		t.Fatal(err)
	}
	fp.arm("SET version_status='retired'")
	if err := s.ActivateTokenInterceptBaseline(ctx, w14fBaselineActivation()); err == nil ||
		!strings.Contains(err.Error(), "retire active version") {
		t.Fatalf("退役失败应报错: %v", err)
	}
	fp.disarm()
	// 合格候选激活成功。
	if err := s.ActivateTokenInterceptBaseline(ctx, w14fBaselineActivation()); err != nil {
		t.Fatalf("合格候选应激活成功: %v", err)
	}
}

// ---- MarkHealthSync / ReadHealthFact ----

func TestW14fOwnerHealthSyncArms(t *testing.T) {
	ctx := context.Background()
	s, fp := w13g2McFailStore(t)
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	run := RunRecord{ID: "w14f-run", SystemAccountID: "sys", ActorSystemAccountID: "actor", ProviderCode: "openai",
		TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", TriggerKind: "manual",
		ProbeSetVersion: "probe-v1", StartedAt: now, RequestSummary: []byte(`{}`), PolicySnapshot: []byte(`{}`)}
	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	// 未知 run → not found。
	if err := s.MarkHealthSync(ctx, "w14f-missing", "applied"); err == nil {
		t.Fatalf("未知 run 应报错")
	}
	// 执行失败（failpoint）。
	fp.arm("SET quality_health_sync_status")
	defer fp.disarm()
	if err := s.MarkHealthSync(ctx, "w14f-run", "applied"); err == nil ||
		!strings.Contains(err.Error(), "mark J3b health sync") {
		t.Fatalf("标记执行失败应报错: %v", err)
	}
	fp.disarm()

	// ReadHealthFact：observed_at 非法 → 解析错误。
	health := &Store{db: wbOpenMemoryDB(t, wbHealthRuntimeDDL(t)), mode: "sqlite"}
	if _, err := health.db.Exec(`INSERT INTO account_quality_health_hourly
		(account_id,system_account_id,provider_code,stat_hour,observed_at,model_check_run_id,model,profile,score,threshold,level,error_code,error_message,updated_at)
		VALUES ('acct','sys','openai','2026-09-16T10','not-a-time','run','m','quick',1,2,'ok','e','m','x')`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := health.ReadHealthFact(ctx, "acct", "2026-09-16T10"); err == nil ||
		!strings.Contains(err.Error(), "parse J3b health observed_at") {
		t.Fatalf("observed_at 解析失败应报错: %v", err)
	}
}

// ---- ListHealthSyncRetries 过滤分支 ----

func w14fInsertRetryRun(t *testing.T, db *sql.DB, id string, policy, decision, request, finished string, score int, level string) {
	t.Helper()
	if finished == "" {
		finished = "2026-09-16T10:00:00.000Z"
	}
	_, err := db.Exec(`INSERT INTO model_check_runs
		(id,system_account_id,actor_system_account_id,provider_code,target_type,target_id,account_id,model,profile,trigger_kind,status,level,score,max_score,message,request_summary_json,result_summary_json,policy_snapshot_json,quality_decision_json,probe_set_version,started_at,created_at,updated_at,finished_at,quality_health_sync_status)
		VALUES (?, 'sys','actor','openai','account','acct','acct','gpt-5.6-sol','full','manual','completed',?,?,100,'',?,?,?,?,'probe-v1','2026-09-16T09:00:00.000Z','2026-09-16T09:00:00.000Z','2026-09-16T09:00:00.000Z',?, 'failed')`,
		id, level, score, request, "{}", policy, decision, finished)
	if err != nil {
		t.Fatal(err)
	}
}

func TestW14fOwnerHealthSyncRetryFilterArms(t *testing.T) {
	ctx := context.Background()
	store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
	statHour, err := NewHealthStatHourFunc("UTC")
	if err != nil {
		t.Fatal(err)
	}
	store.HealthStatHour = statHour
	goodPolicy := `{"threshold":60,"revision":"pol-1","action":"quality_isolate","recoveryIntervalMinutes":60,"manualEnforcementEligible":true}`
	goodDecision := `{"evidenceFormed":true,"trustFormed":true,"hardQualityFailure":true,"enforcementAllowed":false}`
	goodRequest := `{"configRevision":"cfg-1"}`
	w14fInsertRetryRun(t, store.db, "w14f-ok", goodPolicy, goodDecision, goodRequest, "2026-09-16T10:00:00.000Z", 40, "suspicious")

	// 必填字段缺失 → 跳过。
	w14fInsertRetryRun(t, store.db, "w14f-empty-account", goodPolicy, goodDecision, goodRequest, "", 40, "suspicious")
	if _, err := store.db.Exec(`UPDATE model_check_runs SET account_id=NULL WHERE id='w14f-empty-account'`); err != nil {
		t.Fatal(err)
	}
	// policy 非法 JSON → 跳过。
	w14fInsertRetryRun(t, store.db, "w14f-bad-policy", "not-json", goodDecision, goodRequest, "", 40, "suspicious")
	// threshold 越界 → 跳过。
	w14fInsertRetryRun(t, store.db, "w14f-bad-threshold", `{"threshold":10,"revision":"p","action":"a","recoveryIntervalMinutes":60}`, goodDecision, goodRequest, "", 40, "suspicious")
	// decision 非法 JSON → 跳过。
	w14fInsertRetryRun(t, store.db, "w14f-bad-decision", goodPolicy, "not-json", goodRequest, "", 40, "suspicious")
	// policy snapshot revision 为空 → 跳过。
	w14fInsertRetryRun(t, store.db, "w14f-empty-revision", `{"threshold":60,"revision":"","action":"a","recoveryIntervalMinutes":60}`, goodDecision, goodRequest, "", 40, "suspicious")
	// action 为空 / recovery 越界 → 跳过。
	w14fInsertRetryRun(t, store.db, "w14f-bad-action", `{"threshold":60,"revision":"p","action":"","recoveryIntervalMinutes":60}`, goodDecision, goodRequest, "", 40, "suspicious")
	w14fInsertRetryRun(t, store.db, "w14f-bad-recovery", `{"threshold":60,"revision":"p","action":"a","recoveryIntervalMinutes":1}`, goodDecision, goodRequest, "", 40, "suspicious")
	// request snapshot 缺 configRevision → 跳过。
	w14fInsertRetryRun(t, store.db, "w14f-bad-request", goodPolicy, goodDecision, `{}`, "", 40, "suspicious")
	// finished_at 非法 → 跳过。
	w14fInsertRetryRun(t, store.db, "w14f-bad-finished", goodPolicy, goodDecision, goodRequest, "not-a-time", 40, "suspicious")
	// 健康达标的行 → 跳过。
	w14fInsertRetryRun(t, store.db, "w14f-healthy", goodPolicy, `{"evidenceFormed":true,"trustFormed":true}`, goodRequest, "", 90, "likely")

	retries, err := store.ListHealthSyncRetries(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(retries) != 1 || retries[0].RunID != "w14f-ok" {
		t.Fatalf("仅合格行应保留: %+v", retries)
	}
	if retries[0].EnforcementAllowed {
		t.Fatalf("decision 明确 false 时不得允许执行: %+v", retries[0])
	}
	// HealthStatHour 失败 → 全部行在 stat hour 处跳过（531 臂）。
	store.HealthStatHour = func(time.Time) (string, error) { return "", errors.New("w14f stat hour failure") }
	if _, err := store.ListHealthSyncRetries(ctx, 100); err != nil {
		t.Fatalf("单行跳过不应中断扫描: %v", err)
	}
}

// ---- input / outcome 完整性臂 ----

func TestW14fOwnerInputIntegrityArms(t *testing.T) {
	ctx := context.Background()
	store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
	// 非法 payload JSON。
	bad := wbInputFixture()
	bad.Payload = json.RawMessage(`not-json`)
	if _, err := store.IssueInput(ctx, bad); err == nil {
		t.Fatalf("非法 payload 应报错")
	}
	// 幂等重放：入库时间损坏 → ErrInputTampered。
	now := time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC)
	if _, err := store.db.Exec(`INSERT INTO model_check_inputs
		(input_id,identity_key,input_version,input_digest,target_id,config_revision,policy_revision,trigger,issued_at,expires_at,payload)
		VALUES ('w14f-bad-time','sys:acct:model',1,'digest','acct','cfg','pol','manual','bad-time','bad-time','{}')`); err != nil {
		t.Fatal(err)
	}
	replay := wbInputFixture()
	replay.InputID = "w14f-bad-time"
	if _, err := store.IssueInput(ctx, replay); !errors.Is(err, ErrInputTampered) {
		t.Fatalf("损坏时间应 ErrInputTampered: %v", err)
	}
	// 存量 payload 非法 → ErrInputTampered。
	if _, err := store.db.Exec(`INSERT INTO model_check_inputs
		(input_id,identity_key,input_version,input_digest,target_id,config_revision,policy_revision,trigger,issued_at,expires_at,payload)
		VALUES ('w14f-bad-payload','sys:acct:model',1,'digest','acct','cfg','pol','manual',?,?,'not-json')`,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	replay2 := wbInputFixture()
	replay2.InputID = "w14f-bad-payload"
	if _, err := store.IssueInput(ctx, replay2); !errors.Is(err, ErrInputTampered) {
		t.Fatalf("损坏 payload 应 ErrInputTampered: %v", err)
	}
	// LoadInput：损坏时间。
	if _, err := store.LoadInput(ctx, "w14f-bad-time", now); err == nil {
		t.Fatalf("LoadInput 损坏时间应报错")
	}
	// canonicalJSON / jsonEqual 直驱。
	if _, err := canonicalJSON([]byte("not-json")); err == nil {
		t.Fatalf("canonicalJSON 非法输入应报错")
	}
	if jsonEqual([]byte("not-json"), []byte(`{}`)) {
		t.Fatalf("非法 JSON 不应相等")
	}
}

func TestW14fOwnerCommittedOutcomeIntegrityArms(t *testing.T) {
	ctx := context.Background()
	now := "2026-09-16T10:00:00.000Z"
	newStore := func() *Store {
		db, err := sql.Open("sqlite", "file:"+strings.ReplaceAll(t.TempDir(), "\\", "/")+"/w14f-outcome.db?mode=rwc")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		for _, statement := range runtimeTestDDL() {
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
		return &Store{db: db, mode: "sqlite"}
	}
	store := newStore()
	insert := func(id, observed, stored, issued, expires, inputDigest string) {
		t.Helper()
		if _, err := store.db.Exec(`INSERT INTO model_check_inputs
			(input_id,identity_key,input_version,input_digest,target_id,config_revision,policy_revision,trigger,issued_at,expires_at,payload)
			VALUES (?, 'sys:acct:model',1,'input-digest','acct','cfg','pol','manual',?,?,'{}')`, id, issued, expires); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`INSERT INTO model_check_outcomes
			(outcome_id,input_id,input_digest,fence_token,observed_at,stored_at,payload,payload_digest,committed)
			VALUES (?, ?, 'input-digest',1,?,?,'{}','payload-digest',1)`, id, id, observed, stored); err != nil {
			t.Fatal(err)
		}
	}
	insert("w14f-bad-observed", "bad-time", now, now, now, "input-digest")
	if _, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{}, 10); err == nil {
		t.Fatalf("observed_at 损坏应报错")
	}
	// stored_at 损坏（独立库，避免先前坏行短路）。
	store = newStore()
	insert("w14f-bad-stored", now, "bad-time", now, now, "input-digest")
	if _, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{}, 10); err == nil {
		t.Fatalf("stored_at 损坏应报错")
	}
	// issued_at 损坏。
	store = newStore()
	insert("w14f-bad-issued", now, now, "bad-time", now, "input-digest")
	if _, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{}, 10); err == nil {
		t.Fatalf("issued_at 损坏应报错")
	}
	// expires_at 损坏。
	store = newStore()
	insert("w14f-bad-expires", now, now, now, "bad-time", "input-digest")
	if _, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{}, 10); err == nil {
		t.Fatalf("expires_at 损坏应报错")
	}
	// input digest 不一致 → ErrInputTampered。
	store = newStore()
	insert("w14f-digest-mismatch", now, now, now, now, "other-digest")
	if _, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{}, 10); !errors.Is(err, ErrInputTampered) {
		t.Fatalf("digest 不一致应 ErrInputTampered: %v", err)
	}
}

// ---- durable 剩余错误臂 ----

func TestW14fOwnerDurableErrorArms(t *testing.T) {
	ctx := context.Background()
	s, fp := w13g2McFailStore(t)
	issued, err := s.IssueInput(ctx, wbInputFixture())
	if err != nil {
		t.Fatal(err)
	}
	input := issued
	now := input.IssuedAt.Add(time.Second)
	claim, err := s.ClaimInput(ctx, input.InputID, "w14f-token", "w14f-outcome", "w14f-owner", time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	// ReleaseClaim 执行失败（fence_token=? 片段唯一命中 release）。
	fp.arm("AND fence_token=?")
	if err := s.ReleaseClaim(ctx, claim, now); err == nil || !strings.Contains(err.Error(), "release J3b claim") {
		t.Fatalf("release 失败应报错: %v", err)
	}
	fp.disarm()

	// CommitOutcome：payload digest 不匹配。
	outcome := Outcome{InputID: input.InputID, OutcomeID: "w14f-outcome", InputDigest: input.InputDigest, Payload: []byte(`{}`), PayloadDigest: "mismatch", ObservedAt: now, StoredAt: now}
	if err := s.CommitOutcome(ctx, outcome, claim, now); err == nil ||
		!strings.Contains(err.Error(), "payload digest mismatch") {
		t.Fatalf("payload digest 不匹配应报错: %v", err)
	}
	// 各中途读取/写入失败臂。
	claim2, err := s.ClaimInput(ctx, input.InputID, "w14f-token", "w14f-outcome", "w14f-owner", time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	outcome2 := Outcome{InputID: input.InputID, OutcomeID: "w14f-outcome", InputDigest: input.InputDigest, Payload: []byte(`{}`), ObservedAt: now, StoredAt: now}
	fp.arm("SELECT input_digest FROM model_check_inputs")
	if err := s.CommitOutcome(ctx, outcome2, claim2, now); err == nil {
		t.Fatalf("input digest 读取失败应报错")
	}
	fp.disarm()
	fp.arm("SELECT claim_token,owner_id,outcome_id,fence_token")
	if err := s.CommitOutcome(ctx, outcome2, claim2, now); err == nil {
		t.Fatalf("claim 读取失败应报错")
	}
	fp.disarm()
	// claim_until 损坏 → parse 失败。
	if _, err := s.db.Exec(`UPDATE model_check_execution_claims SET claim_until='bad-time' WHERE input_id=?`, input.InputID); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitOutcome(ctx, outcome2, claim2, now); err == nil {
		t.Fatalf("claim_until 损坏应报错")
	}
	if _, err := s.db.Exec(`UPDATE model_check_execution_claims SET claim_until=? WHERE input_id=?`, now.Add(time.Minute).Format(time.RFC3339Nano), input.InputID); err != nil {
		t.Fatal(err)
	}
	fp.arm("SELECT outcome_id,payload_digest FROM model_check_outcomes")
	if err := s.CommitOutcome(ctx, outcome2, claim2, now); err == nil {
		t.Fatalf("existing outcome 读取失败应报错")
	}
	fp.disarm()
	fp.arm("INSERT INTO model_check_outcomes")
	if err := s.CommitOutcome(ctx, outcome2, claim2, now); err == nil ||
		!strings.Contains(err.Error(), "persist J3b outcome") {
		t.Fatalf("outcome 持久化失败应报错: %v", err)
	}
	fp.disarm()

	// 输入过期 → ClaimInput 报 expired（签发合法，租约时钟越过过期点）。
	expired := wbInputFixture()
	expired.InputID = "w14f-expired"
	if _, err := s.IssueInput(ctx, expired); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimInput(ctx, expired.InputID, "t", "o", "w", time.Minute, expired.ExpiresAt.Add(time.Minute)); err == nil ||
		!strings.Contains(err.Error(), "expired") {
		t.Fatalf("过期输入应报错: %v", err)
	}
}

// ---- model limits / config ----

func TestW14fOwnerModelLimitsArms(t *testing.T) {
	ddl := []string{`CREATE TABLE provider_model_catalog (provider_code TEXT, model TEXT, status TEXT, catalog_visible INTEGER, max_input_tokens INTEGER, context_window_tokens INTEGER)`}
	db := wbOpenMemoryDB(t, ddl)
	limits := &VersionedModelLimits{db: db}
	// 未知模型 → not found。
	if _, err := limits.MaxInputTokens("openai", "w14f-missing", ""); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("未知模型应报错: %v", err)
	}
	// limit 非法（0）→ invalid。
	if _, err := db.Exec(`INSERT INTO provider_model_catalog VALUES ('openai','w14f-zero','active',1,0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := limits.MaxInputTokens("openai", "w14f-zero", ""); err == nil ||
		!strings.Contains(err.Error(), "invalid") {
		t.Fatalf("非法 limit 应报错: %v", err)
	}
	// PostgreSQL 方言分支：$N 绑定在 SQLite 上必然失败 → 覆盖 PG 分支与读取失败臂。
	pgLimits := &VersionedModelLimits{db: db, postgres: true}
	if _, err := pgLimits.MaxInputTokens("openai", "w14f-zero", ""); err == nil ||
		!strings.Contains(err.Error(), "read J3b model limit snapshot") {
		t.Fatalf("PG 分支读取失败应报错: %v", err)
	}
}

func TestW14fOwnerLoadConfigNilGetenv(t *testing.T) {
	if _, err := LoadConfig(nil); err != nil {
		t.Fatalf("nil getenv 应回落 os.Getenv: %v", err)
	}
}
