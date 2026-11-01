// r7a_fixes_test.go 钉住深挖复审 R7-A 四条确认修复（S-H1/S-H5/S-H6/W-H2）：
//   - ① run 错误路径（ClaimInput / CommitOutcome / ProjectOutcome 失败）必须
//     就地写终态，不得残留永久 running（S-H1）；
//   - ② Scheduler.Run 运行期周期 SweepStaleRuns 被调度且失败不中断循环（S-H1②）；
//   - ③ ProjectTrust 失败臂把 run 标记进健康重试链（quality_health_sync_status
//     = 'failed' 且 ListHealthSyncRetries 可发现）（S-H5）；
//   - ④ 死信 run 不再占满健康重试扫描窗口；被跳过的截断行轮转出窗口头部
//     （S-H6）；
//   - ⑤ 超时处罚组合（分数达标、无硬失败、TimeoutPenalty）通过真实
//     BusinessEnforcementApplier 校验并 applied（W-H2）。
package modelcheckowner

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// ---- ① S-H1：错误路径终态写 ----

// r7aRunTerminal 读取 run 行的终态事实（status / error_code / finished_at /
// quality_health_sync_status）。
func r7aRunTerminal(t *testing.T, store *Store, runID string) (string, string, string, any) {
	t.Helper()
	var status, errorCode string
	var finished string
	var syncStatus any
	if err := store.db.QueryRow(j3bQ(`SELECT status,error_code,finished_at,quality_health_sync_status FROM model_check_runs WHERE id=?`), runID).Scan(&status, &errorCode, &finished, &syncStatus); err != nil {
		t.Fatal(err)
	}
	return status, errorCode, finished, syncStatus
}

// TestR7ARunErrorPathsReachTerminalState 覆盖三个绕过 finishFailure 的错误臂：
// ClaimInput 失败、CommitOutcome 失败（经 finishFailure 重试收口）、
// ProjectOutcome 失败（outcome 已提交）。三者都不许残留 running。
func TestR7ARunErrorPathsReachTerminalState(t *testing.T) {
	cases := []struct {
		name      string
		fpPattern string
		need      string
		wantCode  string
	}{
		{name: "claimInputFailure", fpPattern: "SELECT expires_at FROM", need: "w14m", wantCode: "model_check_claim_failed"},
		{name: "commitOutcomeFailure", fpPattern: "SELECT outcome_id,payload_digest FROM", need: "commit J3b outcome", wantCode: "model_check_execution_failed"},
		{name: "projectOutcomeFailure", fpPattern: "SELECT status,level,score,max_score,message", need: "w14m", wantCode: "model_check_projection_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := w14mNewProbeServer(t, func(string) (int, string) { return http.StatusOK, w14mOKResponse("gpt-5.6-sol", "OK-MODEL-CHECK") })
			store, fp := w14mRuntimeDB(t)
			fp.disarm()
			runtime := &Runtime{Store: store, OwnerID: "r7a-terminal", Now: w14mFrozenNow, Resolve: w14mResolveOK(server.URL)}
			request := w14mQuickRequest()
			var runID string
			request.OnStarted = func(id string) { runID = id }
			fp.arm(tc.fpPattern)
			defer fp.disarm()
			if _, err := runtime.Run(context.Background(), request); err == nil || !strings.Contains(err.Error(), tc.need) {
				t.Fatalf("err = %v, want 包含 %q", err, tc.need)
			}
			fp.disarm()
			if runID == "" {
				t.Fatal("OnStarted 未回调，run 行不存在")
			}
			status, errorCode, finished, syncStatus := r7aRunTerminal(t, store, runID)
			if status != string(RunFailed) {
				t.Fatalf("run status=%q, want failed（错误臂必须就地收口，不得残留 running）", status)
			}
			if errorCode != tc.wantCode {
				t.Fatalf("error_code=%q, want %q", errorCode, tc.wantCode)
			}
			if finished == "" {
				t.Fatal("终态 run 必须带 finished_at")
			}
			if syncStatus != nil {
				t.Fatalf("quality_health_sync_status=%v, 该链路未产出质量结果必须保持 NULL", syncStatus)
			}
		})
	}
}

// MarkRunFailedTerminal 的 CAS 幂等契约：仅 running 行可收口；已终态行是
// 空操作（接管方已写终态时不强写）。
func TestR7AMarkRunFailedTerminalIsRunningCASOnly(t *testing.T) {
	store := newRuntimeTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	running := RunRecord{ID: "r7a-mark-running", SystemAccountID: "sys", ActorSystemAccountID: "sys", ProviderCode: "openai", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", TriggerKind: "manual", ProbeSetVersion: "p1", StartedAt: now}
	if err := store.CreateRun(ctx, running); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunFailedTerminal(ctx, "r7a-mark-running", "model_check_claim_failed", "认领失败", now); err != nil {
		t.Fatal(err)
	}
	status, errorCode, _, _ := r7aRunTerminal(t, store, "r7a-mark-running")
	if status != string(RunFailed) || errorCode != "model_check_claim_failed" {
		t.Fatalf("status=%q errorCode=%q", status, errorCode)
	}
	// 已终态行再次收口：幂等空操作，不改写既有终态字段。
	if err := store.MarkRunFailedTerminal(ctx, "r7a-mark-running", "other_code", "二次收口", now.Add(time.Minute)); err != nil {
		t.Fatalf("幂等收口不得报错: %v", err)
	}
	status, errorCode, _, _ = r7aRunTerminal(t, store, "r7a-mark-running")
	if status != string(RunFailed) || errorCode != "model_check_claim_failed" {
		t.Fatalf("二次收口改写了终态: status=%q errorCode=%q", status, errorCode)
	}
}

// ---- ② S-H1②：运行期周期 sweep ----

type r7aSweepRecorder struct {
	mu    sync.Mutex
	calls []time.Time
	err   error
}

func (r *r7aSweepRecorder) SweepStaleRuns(_ context.Context, now time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, now)
	return 0, r.err
}

func (r *r7aSweepRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// TestR7ASchedulerRunsPeriodicStaleSweep 用可注入 sweep 端口与缩短的周期，
// 钉住 Scheduler.Run 在运行期按 StaleSweepInterval 反复调用 SweepStaleRuns
// （长驻进程无重启，启动期 sweep 不再发生）；sweep 失败只记录，不得中断
// 调度循环。
func TestR7ASchedulerRunsPeriodicStaleSweep(t *testing.T) {
	t.Run("periodic invocation", func(t *testing.T) {
		recorder := &r7aSweepRecorder{}
		executor := &schedulerExecutor{done: make(chan struct{})}
		scheduler := &Scheduler{Source: schedulerSource{}, Executor: executor, Interval: 5 * time.Millisecond, StaleRuns: recorder, StaleSweepInterval: 30 * time.Millisecond, Now: func() time.Time { return time.Now() }}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- scheduler.Run(ctx) }()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && recorder.count() < 2 {
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
		if err := <-done; err != context.Canceled {
			t.Fatalf("scheduler err=%v", err)
		}
		if got := recorder.count(); got < 2 {
			t.Fatalf("周期 sweep 调用次数=%d, want >=2（运行期必须被调度）", got)
		}
		recorder.mu.Lock()
		defer recorder.mu.Unlock()
		for i := 1; i < len(recorder.calls); i++ {
			if recorder.calls[i].Before(recorder.calls[i-1]) {
				t.Fatalf("sweep 时间戳非单调: %v", recorder.calls)
			}
		}
	})
	t.Run("sweep failure does not stop the loop", func(t *testing.T) {
		recorder := &r7aSweepRecorder{err: errors.New("r7a sweep boom")}
		executor := &schedulerExecutor{done: make(chan struct{})}
		scheduler := &Scheduler{Source: schedulerSource{}, Executor: executor, Interval: 5 * time.Millisecond, StaleRuns: recorder, StaleSweepInterval: 20 * time.Millisecond, Now: func() time.Time { return time.Now() }}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- scheduler.Run(ctx) }()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && recorder.count() < 2 {
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
		if err := <-done; err != context.Canceled {
			t.Fatalf("sweep 失败不得改变循环退出语义: err=%v", err)
		}
		if recorder.count() < 2 {
			t.Fatalf("sweep 失败后仍须继续调度, calls=%d", recorder.count())
		}
	})
}

// ---- ③ S-H5：ProjectTrust 失败进入健康重试链 ----

// TestR7AProjectTrustFailureEntersHealthRetryChain 驱动完整 full 运行，
// 在 ProjectTrust 的凭据回执写入点注入失败：run 已终态 completed，健康发布
// 尚未开始——修复后该 run 必须被标记 quality_health_sync_status='failed' 并
// 能被 ListHealthSyncRetries 重新发现（此前 NULL 状态永久静默丢失健康事实）。
func TestR7AProjectTrustFailureEntersHealthRetryChain(t *testing.T) {
	// 探针服务器响应面与 TestRuntimeFullRunFormsUniversalEvidenceBothWays
	// 保持同一形态（含 VECTOR 分布族与 token 完整性 usage 计数），确保
	// full 运行形成 formed+trusted 证据，重试扫描的 admission 门可放行。
	server := w14mNewProbeServer(t, func(body string) (int, string) {
		if strings.Contains(body, "Controlled token integrity probe") {
			local := strings.Count(body, " x") + 1
			return http.StatusOK, fmt.Sprintf(`{"model":"gpt-5.6-sol","output_text":"OK","usage":{"input_tokens":%d,"total_tokens":%d}}`, local, local+1)
		}
		if strings.Contains(body, "record_model_check") {
			return http.StatusOK, `{"model":"gpt-5.6-sol","output":[{"type":"function_call","name":"record_model_check","arguments":"{\"code\":\"ok\",\"count\":1}"}],"usage":{"total_tokens":2}}`
		}
		if strings.Contains(body, "VECTOR") {
			return http.StatusOK, w14mOKResponse("gpt-5.6-sol", "VECTOR")
		}
		if strings.Contains(body, `"status"`) {
			return http.StatusOK, w14mOKResponse("gpt-5.6-sol", `{"status":"ok","value":7}`)
		}
		return http.StatusOK, w14mOKResponse("gpt-5.6-sol", "OK-MODEL-CHECK")
	})
	store, fp := w14mRuntimeDB(t)
	fp.disarm()
	store.HealthStatHour = mustHealthStatHourFunc(t, "UTC")
	runtime := &Runtime{Store: store, OwnerID: "r7a-trust", Tokenizer: runtimeTestTokenizer{}, ModelLimits: runtimeTestModelLimits{}, Now: w14mFrozenNow, Resolve: func(context.Context, RunRequest) (Target, error) {
		// Target 形态与 TestRuntimeFullRunFormsUniversalEvidenceBothWays 一致：
		// 携带 provider/profile 使目标落在目录内，full 运行执行完整探针族
		// （catalog 外目标只跑 protocol_consistency 子集，证据不形成）。
		return Target{Endpoint: server.URL, TargetName: "Runtime Account", TargetOwnerSystemAccountID: "sys", GroupID: "group-runtime", Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "hello", UpstreamModel: "gpt-5.6-sol", ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1", CredentialType: "api_key", ConfigRevision: "cfg-1", DispatchRevision: 3, SourceConfigRevision: "src-1", SourceDispatchRevision: 4}, nil
	}}
	request := w14mQuickRequest()
	request.Profile = "full"
	request.Threshold = 70
	var runID string
	request.OnStarted = func(id string) { runID = id }
	fp.arm("INSERT INTO model_trust_observation_receipts")
	_, err := runtime.Run(context.Background(), request)
	fp.disarm()
	if err == nil || !strings.Contains(err.Error(), "project J3b trust") {
		t.Fatalf("err = %v, want 包含 %q", err, "project J3b trust")
	}
	if runID == "" {
		t.Fatal("OnStarted 未回调")
	}
	var status, syncStatus string
	if err := store.db.QueryRow(j3bQ(`SELECT status,quality_health_sync_status FROM model_check_runs WHERE id=?`), runID).Scan(&status, &syncStatus); err != nil {
		t.Fatal(err)
	}
	if status != string(RunCompleted) {
		t.Fatalf("run status=%q, outcome 已提交的 run 必须保持 completed", status)
	}
	if syncStatus != "failed" {
		t.Fatalf("quality_health_sync_status=%q, ProjectTrust 失败臂必须标记 failed 进入重试链（S-H5）", syncStatus)
	}
	retries, err := store.ListHealthSyncRetries(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, retry := range retries {
		if retry.RunID == runID {
			found = true
		}
	}
	if !found {
		t.Fatalf("失败健康发布必须进入重试扫描: retries=%+v", retries)
	}
}

// ---- ④ S-H6：死信排除与跳过行轮转 ----

var (
	r7aValidPolicy   = `{"revision":"policy-1","threshold":70,"action":"quality_isolate","recoveryIntervalMinutes":10}`
	r7aValidDecision = `{"evidenceFormed":true,"trustFormed":true}`
	r7aValidRequest  = `{"configRevision":"3"}`
)

// TestR7AHealthRetryWindowNotStarvedByDeadLetters 复现 S-H6 主场景：
// ≥16 条死信 run（重试任务 attempts 已达 HealthRetryMaxAttempts、updated_at
// 冻结）+ 1 条新失败——窗口（limit=4 → 16 行）必须容纳新失败，死信被 SQL
// 排除。
func TestR7AHealthRetryWindowNotStarvedByDeadLetters(t *testing.T) {
	db := newJ3BPGDatabase(t, j3bRetryRunsFixtureDDL(), j3bSchedulerTasksProdDDL())
	ctx := context.Background()
	seed := func(t *testing.T, id, finished string) {
		t.Helper()
		if _, err := db.Exec(j3bQ(`INSERT INTO model_check_runs VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
			id, "completed", "acct-"+id, "sys", "openai", "gpt-5.6", "quick", "failure", 20, nil,
			r7aValidPolicy, r7aValidDecision, r7aValidRequest, finished, "failed", finished); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 16; i++ {
		id := fmt.Sprintf("dead-%02d", i)
		seed(t, id, "2026-08-27T10:00:00Z")
		payload := fmt.Sprintf(`{"runId":%q,"attempts":%d}`, id, HealthRetryMaxAttempts)
		if _, err := db.Exec(j3bQ(`INSERT INTO model_check_scheduler_tasks (id,kind,due_at,state,payload,updated_at) VALUES (?,?,?,?,?,?)`),
			"health:"+id, string(SchedulerHealthRetry), "2330-01-01T00:00:00Z", "failed", payload, "2026-08-27T10:05:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	seed(t, "run-new", "2026-08-27T11:00:00Z")
	store := &Store{schema: j3bTestSchema, db: db, HealthStatHour: mustHealthStatHourFunc(t, "Asia/Shanghai")}
	retries, err := store.ListHealthSyncRetries(ctx, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(retries) != 1 || retries[0].RunID != "run-new" {
		t.Fatalf("死信必须被窗口排除，新失败必须可见: retries=%+v", retries)
	}
}

// TestR7AHealthRetrySkippedRowsRotateOutOfWindowHead 覆盖 S-H6 的同型饿死
// （截断 JSON 行）与轮转语义：第一次扫描窗口被截断行占满（合法行被截断、
// 结果为空）；跳过行被轮转后第二次扫描合法行进入窗口，截断行保持 failed
// 且 updated_at 已推进（仍可在窗口尾部被再次发现）。
func TestR7AHealthRetrySkippedRowsRotateOutOfWindowHead(t *testing.T) {
	db := newJ3BPGDatabase(t, j3bRetryRunsFixtureDDL(), j3bSchedulerTasksProdDDL())
	ctx := context.Background()
	const validFinished = "2026-08-27T10:30:00Z"
	for i := 0; i < 16; i++ {
		id := fmt.Sprintf("truncated-%02d", i)
		if _, err := db.Exec(j3bQ(`INSERT INTO model_check_runs VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
			id, "completed", "acct-"+id, "sys", "openai", "gpt-5.6", "quick", "failure", 20, nil,
			"not-json", r7aValidDecision, r7aValidRequest, "2026-08-27T10:00:00Z", "failed", "2026-08-27T10:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(j3bQ(`INSERT INTO model_check_runs VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		"run-valid", "completed", "acct-valid", "sys", "openai", "gpt-5.6", "quick", "failure", 20, nil,
		r7aValidPolicy, r7aValidDecision, r7aValidRequest, validFinished, "failed", validFinished); err != nil {
		t.Fatal(err)
	}
	store := &Store{schema: j3bTestSchema, db: db, HealthStatHour: mustHealthStatHourFunc(t, "Asia/Shanghai")}
	first, err := store.ListHealthSyncRetries(ctx, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 0 {
		t.Fatalf("轮转前窗口被 16 条截断行占满，合法行必须被截断: retries=%+v", first)
	}
	second, err := store.ListHealthSyncRetries(ctx, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].RunID != "run-valid" {
		t.Fatalf("跳过行轮转后合法行必须进入窗口: retries=%+v", second)
	}
	var status, updatedAt string
	if err := db.QueryRow(j3bQ(`SELECT quality_health_sync_status,updated_at FROM model_check_runs WHERE id='truncated-00'`)).Scan(&status, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("轮转不得改写失败状态: %q", status)
	}
	if updatedAt <= validFinished {
		t.Fatalf("截断行 updated_at=%q 必须已轮转到 %q 之后", updatedAt, validFinished)
	}
}

// ---- ⑤ W-H2：超时处罚贯通真实 Apply ----

// TestR7ARealApplierAcceptsTimeoutPenaltyAtFullScore 用真实
// BusinessEnforcementApplier（含事务、配置匹配与 enforcement PG 写路径）钉住
// W-H2：unavailable 超时处罚主场景分数达标（100>=70）且无硬失败，必须被
// Apply 接受并生效；无标记的同形态输入保持拒绝。
func TestR7ARealApplierAcceptsTimeoutPenaltyAtFullScore(t *testing.T) {
	db := newJ3BPGDatabase(t, businessEnforcementTestDDL()...)
	if _, err := db.Exec(`INSERT INTO accounts (id,system_account_id,status,config_revision,fallback_enabled,super_priority_enabled,deleted_at,schedulable,last_error_code,last_error_message,updated_at) VALUES ('acct-r7a','sys-r7a','active',1,0,0,NULL,1,NULL,NULL,NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_quality_policies (system_account_id,revision,profile,penalty_threshold,penalty_action,recovery_interval_minutes) VALUES ('sys-r7a',0,'quick',70,'quality_isolate',10)`); err != nil {
		t.Fatal(err)
	}
	applier, err := NewBusinessEnforcementApplier(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	occurredAt := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	input := QualityEnforcement{AccountID: "acct-r7a", SystemAccountID: "sys-r7a", RunID: "run-timeout-full", ProviderCode: "openai", Model: "gpt-5.6-sol", PolicyRevision: "0", AccountConfigRevision: "1", Action: "quality_isolate", Score: 100, Threshold: 70, RecoveryIntervalMinutes: 10, Profile: "quick", TimeoutPenalty: true, OccurredAt: occurredAt}
	outcome, err := applier.Apply(ctx, input)
	if err != nil {
		t.Fatalf("超时处罚组合（分数达标无硬失败）必须被真实 Apply 接受: %v", err)
	}
	if outcome.AlreadyEffective || outcome.AfterStatus != "quality_isolated" || outcome.Generation != 1 {
		t.Fatalf("outcome=%+v", outcome)
	}
	var status string
	var revision int
	if err := db.QueryRow(`SELECT status,config_revision FROM accounts WHERE id='acct-r7a'`).Scan(&status, &revision); err != nil || status != "quality_isolated" || revision != 2 {
		t.Fatalf("status=%q revision=%d err=%v", status, revision, err)
	}
	// 反向：无标记的同形态输入保持拒绝，且不得留下任何副作用。
	soft := input
	soft.RunID = "run-soft-full"
	soft.TimeoutPenalty = false
	if _, err := applier.Apply(ctx, soft); err == nil || !strings.Contains(err.Error(), "input is invalid") {
		t.Fatalf("无超时标记的分数达标输入必须拒绝: err=%v", err)
	}
	if err := db.QueryRow(`SELECT config_revision FROM accounts WHERE id='acct-r7a'`).Scan(&revision); err != nil || revision != 2 {
		t.Fatalf("被拒绝的输入不得改写账户: revision=%d err=%v", revision, err)
	}
	// 第二代：账户仍在隔离态时不同 action/新 run 会命中 AlreadyEffective，
	// 这里先把账户恢复为 active（模拟恢复通道），再以新 CAS revision 施加
	// 带标记处罚——走 enforcement 表 ON CONFLICT 升代路径，证明 PG 冲突写
	// 完整可用。
	if _, err := db.Exec(`UPDATE accounts SET status='active',config_revision=3,schedulable=1 WHERE id='acct-r7a'`); err != nil {
		t.Fatal(err)
	}
	next := input
	next.RunID = "run-timeout-full-2"
	next.AccountConfigRevision = "3"
	outcome2, err := applier.Apply(ctx, next)
	if err != nil {
		t.Fatalf("第二代超时处罚必须成功: %v", err)
	}
	if outcome2.Generation != 2 || outcome2.AlreadyEffective {
		t.Fatalf("outcome2=%+v", outcome2)
	}
}

// TestR7AQualityProjectorAppliesTimeoutPenaltyWithRealApplier 端到端钉住
// W-H2 标记贯通：QualityProjector.Project 在 unavailable + 超时触发 +
// 分数达标组合下把 TimeoutPenalty 传给真实 applier，处罚生效且健康发布
// applied（不再被 Apply 输入校验拒绝后伪装成功）。
func TestR7AQualityProjectorAppliesTimeoutPenaltyWithRealApplier(t *testing.T) {
	db := newJ3BPGDatabase(t, append([]string{j3bRunsSyncDecisionDDL, j3bHealthHourlyDDL()}, businessEnforcementTestDDL()...)...)
	if _, err := db.Exec(j3bQ(`INSERT INTO model_check_runs(id,quality_health_sync_status,quality_decision_json,updated_at) VALUES ('run-r7a-timeout','pending_retry','{"triggered":false,"result":"not_triggered"}','2026-08-27T10:00:00Z')`)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts (id,system_account_id,status,config_revision,fallback_enabled,super_priority_enabled,deleted_at,schedulable,last_error_code,last_error_message,updated_at) VALUES ('acct-e2e','sys-r7a','active',1,0,0,NULL,1,NULL,NULL,NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_quality_policies (system_account_id,revision,profile,penalty_threshold,penalty_action,recovery_interval_minutes) VALUES ('sys-r7a',0,'quick',70,'quality_isolate',10)`); err != nil {
		t.Fatal(err)
	}
	applier, err := NewBusinessEnforcementApplier(db)
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{schema: j3bTestSchema, db: db, HealthStatHour: mustHealthStatHourFunc(t, "UTC")}
	projector := &QualityProjector{Store: store, Enforcement: applier}
	fact := HealthFact{AccountID: "acct-e2e", SystemAccountID: "sys-r7a", StatHour: "2026-08-27T10:00:00Z", RunID: "run-r7a-timeout", ProviderCode: "openai", Model: "gpt-5.6-sol", Profile: "quick", ObservedAt: time.Date(2026, 8, 27, 10, 1, 0, 0, time.UTC), Score: 100, Threshold: 70, Level: "unavailable", PenaltyAction: "quality_isolate", PolicyRevision: "0", AccountConfigRevision: "1", RecoveryIntervalMinutes: 10, TimeoutPenaltyTriggered: true, EnforcementAllowed: true}
	if err := projector.Project(context.Background(), fact.RunID, EvidenceAggregate{Formed: true, TrustFormed: true}, fact); err != nil {
		t.Fatalf("超时处罚组合必须完整走通 Project→Apply→applied: %v", err)
	}
	var syncStatus string
	if err := db.QueryRow(j3bQ(`SELECT quality_health_sync_status FROM model_check_runs WHERE id='run-r7a-timeout'`)).Scan(&syncStatus); err != nil || syncStatus != "applied" {
		t.Fatalf("health sync=%q err=%v, want applied", syncStatus, err)
	}
	decision := readMergedDecision(t, db, "run-r7a-timeout")
	if decision["result"] != "applied" || decision["healthSyncResult"] != "applied" || decision["afterStatus"] != "quality_isolated" {
		t.Fatalf("decision=%+v", decision)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM accounts WHERE id='acct-e2e'`).Scan(&status); err != nil || status != "quality_isolated" {
		t.Fatalf("账户状态=%q err=%v", status, err)
	}
}
