// r7a2_owner_runtime_test.go 钉住 R7-A 落点复核后的调度/持久化层缺陷修复：
//   - ① 终态兜底写（MarkRunFailedTerminal/MarkHealthSync）不得复用已耗尽的
//     finalizeCtx，必须使用新的后台超时上下文；
//   - ② 周期 sweep 的存活判定依赖 run.updated_at，心跳必须在 RenewClaim 成功后
//     TouchRun，sweep 才不会再误收合法的长 run；
//   - ③ 健康重试扫描的死信子查询必须对字符串/小数/缺失/非对象 attempts 免疫；
//   - ④ 终态写失败的错误不得让 HTTP 层 errors.As 命中根因的 *RequestError；
//   - ⑤ 健康重试必须先补齐/重放 trust 回执，TrustFormed 且无 report 时 fail-closed；
//   - ⑥ SweepStaleRuns 必须写 finished_at；
//   - ⑦ rotateSkipped 失败保留 best-effort 语义（不得中断扫描）。
package modelcheckowner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// r7a2RunTerminal 读取 run 的终态字段（status/error_code/finished_at/
// quality_health_sync_status）。
func r7a2RunTerminal(t *testing.T, store *Store, runID string) (string, sql.NullString, sql.NullString, sql.NullString) {
	t.Helper()
	var status string
	var errorCode, finished, syncStatus sql.NullString
	if err := store.db.QueryRow(j3bQ(`SELECT status,error_code,finished_at,quality_health_sync_status FROM model_check_runs WHERE id=?`), runID).Scan(&status, &errorCode, &finished, &syncStatus); err != nil {
		t.Fatal(err)
	}
	return status, errorCode, finished, syncStatus
}

// r7a2InsertRetryRun 以最小 16 列夹具（j3bRetryRunsFixtureDDL）插入一条可被
// ListHealthSyncRetries 发现的 completed + failed 健康发布 run。
func r7a2InsertRetryRun(t *testing.T, db *sql.DB, runID, policyJSON string) {
	t.Helper()
	const finished = "2026-08-27T10:15:00Z"
	if _, err := db.Exec(j3bQ(`INSERT INTO model_check_runs VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		runID, "completed", "acct-"+runID, "sys-1", "openai", "gpt-5.6-sol", "quick", "failure", 20, nil,
		policyJSON, r7aValidDecision, r7aValidRequest, finished, "failed", finished); err != nil {
		t.Fatal(err)
	}
}

// ---- ① finalize 预算耗尽后的终态兜底写 ----

// TestR7A2FinalizeTimeoutStillTerminatesRun 用缩短的 finalize 预算复现
// "finalizeCtx 已耗尽"：run 级别的兜底写必须仍把 run 收口为 failed；
// 健康统计小时格式化在预算耗尽后失败时，flush 仍必须写
// quality_health_sync_status='failed' 进入重试链。
func TestR7A2FinalizeTimeoutStillTerminatesRun(t *testing.T) {
	t.Run("expired finalize budget still fails the run", func(t *testing.T) {
		server := w14mNewProbeServer(t, func(string) (int, string) {
			return http.StatusOK, w14mOKResponse("gpt-5.6-sol", "OK-MODEL-CHECK")
		})
		store, fp := w14mRuntimeDB(t)
		fp.disarm()
		runtime := &Runtime{Store: store, OwnerID: "r7a2-finalize", Now: w14mFrozenNow, Resolve: w14mResolveOK(server.URL), finalizeTimeout: time.Nanosecond}
		request := w14mQuickRequest()
		var runID string
		request.OnStarted = func(id string) { runID = id }
		result, err := runtime.Run(context.Background(), request)
		if err == nil {
			t.Fatalf("finalize 预算耗尽必须返回错误: result=%+v", result)
		}
		if runID == "" {
			t.Fatal("OnStarted 未回调，run 行不存在")
		}
		status, errorCode, finished, syncStatus := r7a2RunTerminal(t, store, runID)
		if status != string(RunFailed) {
			t.Fatalf("run status=%q, want failed（兜底写不得复用已耗尽的 finalizeCtx）", status)
		}
		if errorCode.String != "model_check_execution_failed" {
			t.Fatalf("error_code=%v, want model_check_execution_failed", errorCode)
		}
		if !finished.Valid || finished.String == "" {
			t.Fatal("终态 run 必须带 finished_at")
		}
		if syncStatus.Valid {
			t.Fatalf("quality_health_sync_status=%v, 未进入健康投影的失败 run 必须保持 NULL", syncStatus)
		}
	})

	t.Run("expired finalize budget still marks health sync failed", func(t *testing.T) {
		// 低分快速档 → 健康发布分支；统计小时格式化在 finalize 预算耗尽后
		// 返回错误，兜底 MarkHealthSync 必须用新的后台超时上下文落到 failed，
		// 否则该健康样本永久静默丢失（NULL 不在重试扫描口径内）。
		server := w14mNewProbeServer(t, func(string) (int, string) {
			return http.StatusOK, w14mOKResponse("gpt-5.6-sol", "WRONG-RESPONSE")
		})
		store, fp := w14mRuntimeDB(t)
		fp.disarm()
		store.HealthStatHour = func(time.Time) (string, error) {
			time.Sleep(1500 * time.Millisecond)
			return "", errors.New("r7a2 slow stat hour")
		}
		request := w14mQuickRequest()
		request.Threshold = 80
		request.ProviderCode = "openai"
		runtime := &Runtime{Store: store, OwnerID: "r7a2-health", Now: w14mFrozenNow, Resolve: w14mResolveOK(server.URL), Projector: &QualityProjector{Store: store}, finalizeTimeout: time.Second}
		result, err := runtime.Run(context.Background(), request)
		if err != nil {
			t.Fatalf("健康同步失败不得改写 run 错误臂: %v", err)
		}
		if result.Status != string(RunCompleted) {
			t.Fatalf("result.Status=%q, want completed", result.Status)
		}
		status, _, _, syncStatus := r7a2RunTerminal(t, store, result.RunID)
		if status != string(RunCompleted) {
			t.Fatalf("run status=%q, want completed", status)
		}
		if !syncStatus.Valid || syncStatus.String != "failed" {
			t.Fatalf("quality_health_sync_status=%v, want failed（兜底写不得复用已耗尽的 finalizeCtx）", syncStatus)
		}
	})
}

// ---- ② 心跳推进 run.updated_at ----

// TestR7A2TouchRunAdvancesOnlyRunningRows 钉住 TouchRun 的 CAS 语义：
// running 行推进 updated_at；终态行不被触碰；非法入参拒绝。
func TestR7A2TouchRunAdvancesOnlyRunningRows(t *testing.T) {
	store := newRuntimeTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	running := RunRecord{ID: "r7a2-touch-running", SystemAccountID: "sys", ActorSystemAccountID: "sys", ProviderCode: "openai", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", TriggerKind: "manual", ProbeSetVersion: "p1", StartedAt: now}
	terminal := running
	terminal.ID = "r7a2-touch-terminal"
	for _, run := range []RunRecord{running, terminal} {
		if err := store.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.MarkRunFailedTerminal(ctx, terminal.ID, "model_check_execution_failed", "r7a2 terminal", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	touched := now.Add(30 * time.Minute)
	if err := store.TouchRun(ctx, running.ID, touched); err != nil {
		t.Fatal(err)
	}
	var updated string
	if err := store.db.QueryRow(j3bQ(`SELECT updated_at FROM model_check_runs WHERE id=?`), running.ID).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	if updated != touched.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("running updated_at=%q, want %q", updated, touched.UTC().Format(time.RFC3339Nano))
	}

	var terminalUpdated string
	if err := store.db.QueryRow(j3bQ(`SELECT updated_at FROM model_check_runs WHERE id=?`), terminal.ID).Scan(&terminalUpdated); err != nil {
		t.Fatal(err)
	}
	if err := store.TouchRun(ctx, terminal.ID, touched.Add(time.Hour)); err != nil {
		t.Fatalf("终态行 touch 必须是无害空操作: %v", err)
	}
	var after, terminalStatus string
	if err := store.db.QueryRow(j3bQ(`SELECT updated_at,status FROM model_check_runs WHERE id=?`), terminal.ID).Scan(&after, &terminalStatus); err != nil {
		t.Fatal(err)
	}
	if after != terminalUpdated {
		t.Fatalf("终态行 updated_at=%q, want untouched %q", after, terminalUpdated)
	}
	if terminalStatus != string(RunFailed) {
		t.Fatalf("终态行 status=%q, want failed", terminalStatus)
	}

	if err := store.TouchRun(ctx, "", touched); err == nil {
		t.Fatal("空 runID 必须拒绝")
	}
	if err := store.TouchRun(ctx, running.ID, time.Time{}); err == nil {
		t.Fatal("零时间必须拒绝")
	}
}

// TestR7A2HeartbeatTouchesRunRowWhileProbing 钉住心跳接线：探针仍在执行时，
// RenewClaim 成功会同步 TouchRun，run.updated_at 必须前进（否则周期 sweep
// 会把无墙钟预算的长 run 当僵尸收掉）。
func TestR7A2HeartbeatTouchesRunRowWhileProbing(t *testing.T) {
	release := make(chan struct{})
	var once atomic.Bool
	server := w14mNewProbeServer(t, func(string) (int, string) {
		if once.CompareAndSwap(false, true) {
			select {
			case <-release:
			case <-time.After(2500 * time.Millisecond):
			}
		}
		return http.StatusOK, w14mOKResponse("gpt-5.6-sol", "OK-MODEL-CHECK")
	})
	store, fp := w14mRuntimeDB(t)
	fp.disarm()
	runtime := &Runtime{Store: store, OwnerID: "r7a2-heartbeat", Now: func() time.Time { return time.Now().UTC() }, Resolve: w14mResolveOK(server.URL), Lease: 3 * time.Second}
	request := w14mQuickRequest()
	runIDCh := make(chan string, 1)
	request.OnStarted = func(id string) { runIDCh <- id }
	type runOutcome struct {
		result RunResult
		err    error
	}
	done := make(chan runOutcome, 1)
	go func() {
		result, err := runtime.Run(context.Background(), request)
		done <- runOutcome{result: result, err: err}
	}()
	var runID string
	select {
	case runID = <-runIDCh:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("OnStarted 未回调")
	}
	var startedRaw string
	if err := store.db.QueryRow(j3bQ(`SELECT started_at FROM model_check_runs WHERE id=?`), runID).Scan(&startedRaw); err != nil {
		close(release)
		t.Fatal(err)
	}
	startedAt, err := time.Parse(time.RFC3339Nano, startedRaw)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	advanced := false
	deadline := time.Now().Add(2200 * time.Millisecond)
	for time.Now().Before(deadline) {
		var raw string
		if err := store.db.QueryRow(j3bQ(`SELECT updated_at FROM model_check_runs WHERE id=?`), runID).Scan(&raw); err != nil {
			close(release)
			t.Fatal(err)
		}
		if updated, parseErr := time.Parse(time.RFC3339Nano, raw); parseErr == nil && updated.After(startedAt) {
			advanced = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	close(release)
	outcome := <-done
	if outcome.err != nil {
		t.Fatalf("run err=%v", outcome.err)
	}
	if outcome.result.Status != string(RunCompleted) {
		t.Fatalf("run status=%q, want completed", outcome.result.Status)
	}
	if !advanced {
		t.Fatal("探针执行期间 run.updated_at 未推进：心跳未接线，长 run 会被 sweep 误收")
	}
}

// ---- ③ 死信子查询的健壮性 ----

// TestR7A2HealthRetryScanToleratesMalformedAttemptPayloads 复现 store.go 死信
// 子查询的整型强转缺陷：字符串/小数/缺失/非对象 attempts 都曾让整条健康重试
// 扫描报错。修复后扫描必须正常返回，且只有数值型 attempts >= 上限才按死信排除。
func TestR7A2HealthRetryScanToleratesMalformedAttemptPayloads(t *testing.T) {
	db := newJ3PGDatabaseForRetry(t)
	ctx := context.Background()
	cases := []struct {
		runID        string
		payload      string
		wantDeadline bool
	}{
		{runID: "dead-string", payload: `{"runId":"dead-string","attempts":"12"}`},
		{runID: "dead-decimal", payload: `{"runId":"dead-decimal","attempts":10.5}`, wantDeadline: true},
		{runID: "dead-missing", payload: `{"runId":"dead-missing"}`},
		{runID: "dead-array", payload: `[1,2,3]`},
		{runID: "dead-null", payload: `null`},
	}
	for _, tc := range cases {
		r7a2InsertRetryRun(t, db, tc.runID, r7aValidPolicy)
		if _, err := db.Exec(j3bQ(`INSERT INTO model_check_scheduler_tasks (id,kind,due_at,state,payload,updated_at) VALUES (?,?,?,?,?,?)`),
			"health:"+tc.runID, string(SchedulerHealthRetry), "2330-01-01T00:00:00Z", "failed", tc.payload, "2026-08-27T10:05:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	r7a2InsertRetryRun(t, db, "run-live", r7aValidPolicy)
	store := &Store{schema: j3bTestSchema, db: db, HealthStatHour: mustHealthStatHourFunc(t, "UTC")}
	retries, err := store.ListHealthSyncRetries(ctx, 10)
	if err != nil {
		t.Fatalf("坏 attempts 行不得让整条扫描报错: %v", err)
	}
	found := map[string]bool{}
	for _, retry := range retries {
		found[retry.RunID] = true
	}
	if !found["run-live"] {
		t.Fatalf("正常失败 run 必须仍可被发现: %+v", retries)
	}
	for _, tc := range cases {
		if tc.wantDeadline {
			if found[tc.runID] {
				t.Fatalf("%s 的数值 attempts 已达死信上限，必须被窗口排除", tc.runID)
			}
			continue
		}
		if !found[tc.runID] {
			t.Fatalf("%s 的 attempts 不是数值上限，必须保持可重试（不得被误排除）", tc.runID)
		}
	}
}

// newJ3PGDatabaseForRetry 是健康重试扫描夹具：16 列 run 表 + 生产同型任务表。
func newJ3PGDatabaseForRetry(t *testing.T) *sql.DB {
	t.Helper()
	return newJ3BPGDatabase(t, j3bRetryRunsFixtureDDL(), j3bSchedulerTasksProdDDL())
}

// ---- ④ 终态写失败不得被根因 RequestError 掩盖 ----

// TestR7A2TerminalWriteFailureStaysInternalServerError 复现 errors.Join(cause, err)
// 的错误分类缺陷：根因是 *RequestError、终态写失败时，HTTP 层必须渲染 500，
// 而不是把存储故障伪装成根因的 4xx/503。
func TestR7A2TerminalWriteFailureStaysInternalServerError(t *testing.T) {
	cases := []struct {
		name          string
		armTerminal   bool
		wantRunStatus string
	}{
		{name: "terminal fallback succeeds", armTerminal: false, wantRunStatus: string(RunFailed)},
		{name: "terminal fallback also fails", armTerminal: true, wantRunStatus: string(RunRunning)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := w14mNewProbeServer(t, func(string) (int, string) {
				return http.StatusOK, w14mOKResponse("gpt-5.6-sol", "OK-MODEL-CHECK")
			})
			store, fp := w14mRuntimeDB(t)
			fp.disarm()
			var calls atomic.Int32
			resolve := func(ctx context.Context, request RunRequest) (Target, error) {
				if calls.Add(1) > 1 {
					return Target{}, &RequestError{StatusCode: http.StatusServiceUnavailable, Message: "r7a2 upstream unavailable"}
				}
				return w14mResolveOK(server.URL)(ctx, request)
			}
			runtime := &Runtime{Store: store, OwnerID: "r7a2-http", Now: w14mFrozenNow, Resolve: resolve}
			request := w14mQuickRequest()
			var runID string
			request.OnStarted = func(id string) { runID = id }
			fp.arm("SELECT outcome_id,payload_digest FROM")
			if tc.armTerminal {
				fp.arm("SET status='failed'")
			}
			_, err := runtime.Run(context.Background(), request)
			fp.disarm()
			if err == nil {
				t.Fatal("终态写失败必须返回错误")
			}
			var requestError *RequestError
			if errors.As(err, &requestError) {
				t.Fatalf("终态写失败被识别为根因 RequestError（%d）：%v", requestError.StatusCode, err)
			}
			recorder := httptest.NewRecorder()
			httpRequest := httptest.NewRequest(http.MethodPost, "/model-check/runs", nil)
			writeOwnerFailure(httpRequest, recorder, err)
			if recorder.Code != http.StatusInternalServerError {
				t.Fatalf("HTTP status=%d, want 500（存储故障不得被根因 RequestError 掩盖）: %s", recorder.Code, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), "r7a2 upstream unavailable") {
				t.Fatalf("5xx 响应不得透出内部错误原文: %s", recorder.Body.String())
			}
			if runID == "" {
				t.Fatal("OnStarted 未回调")
			}
			status, _, _, _ := r7a2RunTerminal(t, store, runID)
			if status != tc.wantRunStatus {
				t.Fatalf("run status=%q, want %q", status, tc.wantRunStatus)
			}
		})
	}
}

// ---- ⑤ 健康重试的 trust 回执门 ----

// r7a2TrustReport 构造一个可通过 validTrustReport 且与 observation 一致的回执。
func r7a2TrustReport() TrustReport {
	return TrustReport{IdentityStatus: "consistent", MappingStatus: "direct", UsageIntegrityStatus: "consistent", ProtocolStatus: "consistent", EvidenceStatus: "stable", EvidenceCoverage: 100, EvidenceFormed: true, TrustFormed: true, TrustScore: 0.9, ReasonCodes: []string{}}
}

// r7a2SeedFullRetryRun 在完整运行夹具中插入一条 full run 及其 observation，
// trustReport 为 nil 时写入不含 trustReport 的 `{}`。
func r7a2SeedFullRetryRun(t *testing.T, db *sql.DB, runID string, trustReport *TrustReport) {
	t.Helper()
	const policy = `{"revision":"policy-7","threshold":70,"action":"quality_isolate","recoveryIntervalMinutes":10}`
	resultSummary := `{}`
	if trustReport != nil {
		encoded, err := json.Marshal(map[string]any{"trustReport": trustReport})
		if err != nil {
			t.Fatal(err)
		}
		resultSummary = string(encoded)
	}
	r7a2SeedRunWithPolicy(t, db, runID, policy, resultSummary)
}

// r7a2SeedRunWithPolicy 在完整运行夹具（j3bRuntimeFixtureDDLs）中插入一条
// completed + failed 健康发布的 run 行与一条 observation。
func r7a2SeedRunWithPolicy(t *testing.T, db *sql.DB, runID, policyJSON, resultSummary string) {
	t.Helper()
	const finished = "2026-08-27T10:15:00Z"
	const decision = `{"evidenceFormed":true,"trustFormed":true,"hardQualityFailure":false,"enforcementAllowed":false}`
	const request = `{"configRevision":"3"}`
	if _, err := db.Exec(j3bQ(`INSERT INTO model_check_runs (id,system_account_id,actor_system_account_id,provider_code,target_type,target_id,account_id,model,profile,trigger_kind,status,level,score,max_score,message,request_summary_json,result_summary_json,policy_snapshot_json,quality_decision_json,probe_set_version,started_at,created_at,updated_at,finished_at,quality_health_sync_status) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		runID, "sys-1", "actor-1", "openai", "account", "acct", "acct", "gpt-5.6-sol", "full", "manual", "completed", "failure", 20, 100, "",
		request, resultSummary, policyJSON, decision, "openai-model-check-v1", "2026-08-27T10:00:00Z", "2026-08-27T10:00:00Z", "2026-08-27T10:00:00Z", finished, "failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(j3bQ(`INSERT INTO model_check_observations (id,run_id,system_account_id,account_id,provider_code,requested_model,mapped_upstream_model,probe_family,observation_status,identity_status,mapping_status,protocol_status,evidence_coverage,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		runID+"-observation-0001", runID, "sys-1", "acct", "openai", "gpt-5.6-sol", "gpt-5.6-sol", "protocol_basic", "complete", "consistent", "direct", "consistent", 100, "2026-08-27T10:10:00Z"); err != nil {
		t.Fatal(err)
	}
}

// TestR7A2HealthRetryReplaysTrustBeforePublishing 钉住健康重试的 trust 回执门：
// Trust 从未发布时必须先补齐回执再投影健康事实；TrustFormed 且无可重放 report
// 时必须返回错误并保持 failed，不得发布健康事实。
func TestR7A2HealthRetryReplaysTrustBeforePublishing(t *testing.T) {
	t.Run("missing trust projection is replayed before projection", func(t *testing.T) {
		db := newJ3BPGDatabase(t, j3bRuntimeFixtureDDLs()...)
		report := r7a2TrustReport()
		r7a2SeedFullRetryRun(t, db, "run-full-trust", &report)
		store := &Store{schema: j3bTestSchema, db: db, HealthStatHour: mustHealthStatHourFunc(t, "UTC")}
		executor := &HealthSyncRetryExecutor{Projector: &QualityProjector{Store: store}}
		if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerHealthRetry, Payload: []byte(`{"runId":"run-full-trust"}`)}); err != nil {
			t.Fatalf("健康重试必须能补齐 trust 回执后完成投影: %v", err)
		}
		var receipts int
		if err := db.QueryRow(j3bQ(`SELECT COUNT(*) FROM model_trust_observation_receipts WHERE observation_id=?`), "run-full-trust-observation-0001").Scan(&receipts); err != nil {
			t.Fatal(err)
		}
		if receipts != 1 {
			t.Fatalf("trust receipts=%d, want 1（重试前必须补齐回执）", receipts)
		}
		var syncStatus string
		if err := db.QueryRow(j3bQ(`SELECT quality_health_sync_status FROM model_check_runs WHERE id=?`), "run-full-trust").Scan(&syncStatus); err != nil {
			t.Fatal(err)
		}
		if syncStatus != "applied" {
			t.Fatalf("health sync=%q, want applied", syncStatus)
		}
		var facts int
		if err := db.QueryRow(j3bQ(`SELECT COUNT(*) FROM account_quality_health_hourly WHERE account_id='acct' AND stat_hour='2026-08-27T10'`)).Scan(&facts); err != nil {
			t.Fatal(err)
		}
		if facts != 1 {
			t.Fatalf("health facts=%d, want 1", facts)
		}
	})

	t.Run("formed trust without replayable report fails closed", func(t *testing.T) {
		db := newJ3BPGDatabase(t, j3bRuntimeFixtureDDLs()...)
		r7a2SeedFullRetryRun(t, db, "run-full-no-report", nil)
		store := &Store{schema: j3bTestSchema, db: db, HealthStatHour: mustHealthStatHourFunc(t, "UTC")}
		executor := &HealthSyncRetryExecutor{Projector: &QualityProjector{Store: store}}
		err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerHealthRetry, Payload: []byte(`{"runId":"run-full-no-report"}`)})
		if err == nil || !strings.Contains(err.Error(), "trust") {
			t.Fatalf("TrustFormed 但无可重放 report 必须报错: %v", err)
		}
		var facts int
		if err := db.QueryRow(j3bQ(`SELECT COUNT(*) FROM account_quality_health_hourly WHERE account_id='acct'`)).Scan(&facts); err != nil {
			t.Fatal(err)
		}
		if facts != 0 {
			t.Fatalf("health facts=%d, want 0（fail-closed 不得发布健康事实）", facts)
		}
		var syncStatus string
		if err := db.QueryRow(j3bQ(`SELECT quality_health_sync_status FROM model_check_runs WHERE id=?`), "run-full-no-report").Scan(&syncStatus); err != nil {
			t.Fatal(err)
		}
		if syncStatus != "failed" {
			t.Fatalf("health sync=%q, want failed（保持重试）", syncStatus)
		}
		var receipts int
		if err := db.QueryRow(j3bQ(`SELECT COUNT(*) FROM model_trust_observation_receipts`)).Scan(&receipts); err != nil {
			t.Fatal(err)
		}
		if receipts != 0 {
			t.Fatalf("trust receipts=%d, want 0", receipts)
		}
	})
}

// ---- ⑥ sweep 写 finished_at ----

// TestR7A2SweepStaleRunsWritesFinishedAt 钉住 StaleRuns sweep 的终态完整性：
// 收尾行必须同时写入 finished_at（与 updated_at 同一收尾时刻）。
func TestR7A2SweepStaleRunsWritesFinishedAt(t *testing.T) {
	store := newRuntimeTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	stale := RunRecord{ID: "r7a2-stale-finished", SystemAccountID: "sys", ActorSystemAccountID: "sys", ProviderCode: "openai", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", TriggerKind: "manual", ProbeSetVersion: "p1", StartedAt: now.Add(-2 * time.Hour)}
	if err := store.CreateRun(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	changed, err := store.SweepStaleRuns(context.Background(), now)
	if err != nil || changed != 1 {
		t.Fatalf("sweep changed=%d err=%v, want 1/nil", changed, err)
	}
	status, errorCode, finished, _ := r7a2RunTerminal(t, store, stale.ID)
	if status != string(RunFailed) || errorCode.String != staleRunErrorCode {
		t.Fatalf("status=%q errorCode=%v", status, errorCode)
	}
	if !finished.Valid || finished.String != now.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("finished_at=%v, want %q", finished, now.UTC().Format(time.RFC3339Nano))
	}
}

// ---- ⑦ 轮转失败不中断扫描 ----

// TestR7A2RotateSkippedFailureKeepsScanAlive 钉住 rotateSkipped 的 best-effort
// 语义：轮转 UPDATE 失败只留痕，不得让 ListHealthSyncRetries 报错或改变扫描
// 结果（坏行保持 failed，仍可在后续窗口尾部被发现）。
func TestR7A2RotateSkippedFailureKeepsScanAlive(t *testing.T) {
	store, fp := w14mRuntimeDB(t)
	fp.disarm()
	r7a2SeedRunWithPolicy(t, store.db, "run-rotate-bad", "not-json", "{}")
	fp.arm(`UPDATE model_check_runs SET updated_at=? WHERE id=?`)
	retries, err := store.ListHealthSyncRetries(context.Background(), 10)
	fp.disarm()
	if err != nil {
		t.Fatalf("轮转失败必须保留 best-effort 语义，不得中断扫描: %v", err)
	}
	if len(retries) != 0 {
		t.Fatalf("截断 policy 行必须被跳过: %+v", retries)
	}
	var status string
	if err := store.db.QueryRow(j3bQ(`SELECT quality_health_sync_status FROM model_check_runs WHERE id='run-rotate-bad'`)).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("轮转失败不得改写失败状态: %q", status)
	}
}
