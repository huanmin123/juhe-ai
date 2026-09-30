package modelcheckowner

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func wbScheduledPayload() ScheduledPayload {
	return ScheduledPayload{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", ProviderCode: "openai", Threshold: 70, PenaltyAction: "quality_isolate", ConfigRevision: "cfg-1", DispatchRevision: 1, SourceConfigRevision: "src-1", SourceDispatchRevision: 1, PolicyRevision: "7", ProbeSetVersion: "probe-v1", IdentityKey: "sys:acct:model", OwnerID: "wb-owner", ScheduleID: "sch-1", ScheduleRevision: 3, IntervalMinutes: 60}
}

// SchedulerRunExecutor 契约：定时任务必须带完成回调；恢复任务必须带
// Business 租约回调；payload 快照不完整一律失败关闭。
func TestWBSchedulerRunExecutorContract(t *testing.T) {
	completions := make(chan ScheduledPayload, 4)
	scheduledCompletion := ScheduledCompletion(func(_ context.Context, payload ScheduledPayload, _ RunResult) error {
		completions <- payload
		return nil
	})
	recoveries := make(chan RecoveryPayload, 4)
	recoveryCompletion := RecoveryCompletion(func(_ context.Context, payload RecoveryPayload, passed bool) error {
		payload.CompletedAt = time.Time{}
		if passed {
			payload.PolicyRevision = -payload.PolicyRevision
		}
		recoveries <- payload
		return nil
	})
	eligibleResult := RunResult{RunID: "run-ok", Status: string(RunCompleted), Data: map[string]any{"evidenceFormed": true, "trustFormed": true, "score": 90, "level": "likely"}}
	executor := func(run SchedulerRunner, build SchedulerRunBuilder) *SchedulerRunExecutor {
		return &SchedulerRunExecutor{Runtime: run, Build: build, Scheduled: scheduledCompletion, Recovery: recoveryCompletion}
	}
	t.Run("uninitialized", func(t *testing.T) {
		if err := (*SchedulerRunExecutor)(nil).Execute(context.Background(), ScheduleTask{}); err == nil {
			t.Fatal("nil 执行器必须报错")
		}
		if err := (&SchedulerRunExecutor{}).Execute(context.Background(), ScheduleTask{}); err == nil {
			t.Fatal("缺 Runtime 的执行器必须报错")
		}
	})
	t.Run("unsupported kind", func(t *testing.T) {
		if err := (&SchedulerRunExecutor{Runtime: wbStubRunner{}, Build: func(context.Context, ScheduledPayload) (RunRequest, error) { return RunRequest{}, nil }}).Execute(context.Background(), ScheduleTask{Kind: SchedulerHealthRetry}); err == nil {
			t.Fatal("健康重试不得进入 run 执行器")
		}
	})
	t.Run("recovery requires completion owner", func(t *testing.T) {
		exec := &SchedulerRunExecutor{Runtime: wbStubRunner{}, Build: func(context.Context, ScheduledPayload) (RunRequest, error) { return RunRequest{}, nil }}
		if err := exec.Execute(context.Background(), ScheduleTask{Kind: SchedulerQualityRecovery, Payload: wbPayloadBytes(t, wbScheduledPayload())}); err == nil {
			t.Fatal("恢复任务缺少租约回调必须报错")
		}
	})
	t.Run("invalid payload scope", func(t *testing.T) {
		payload := wbScheduledPayload()
		payload.PenaltyAction = "bogus"
		if err := executor(wbStubRunner{}, nil).Execute(context.Background(), ScheduleTask{Kind: SchedulerScheduled, Payload: wbPayloadBytes(t, payload)}); err == nil {
			t.Fatal("非法处罚方式必须失败关闭")
		}
	})
	t.Run("scheduled missing completion metadata", func(t *testing.T) {
		payload := wbScheduledPayload()
		payload.OwnerID = ""
		exec := &SchedulerRunExecutor{Runtime: wbStubRunner{run: func(context.Context, RunRequest) (RunResult, error) { return RunResult{}, nil }}, Build: func(context.Context, ScheduledPayload) (RunRequest, error) { return RunRequest{}, nil }, Scheduled: scheduledCompletion}
		if err := exec.Execute(context.Background(), ScheduleTask{Kind: SchedulerScheduled, Payload: wbPayloadBytes(t, payload)}); err == nil || !strings.Contains(err.Error(), "completion metadata") {
			t.Fatalf("缺完成元数据必须报错: err=%v", err)
		}
	})
	t.Run("scheduled build failure still completes", func(t *testing.T) {
		buildErr := errors.New("目标不可用")
		exec := &SchedulerRunExecutor{Runtime: wbStubRunner{}, Build: func(context.Context, ScheduledPayload) (RunRequest, error) { return RunRequest{}, buildErr }, Scheduled: scheduledCompletion}
		if err := exec.Execute(context.Background(), ScheduleTask{Kind: SchedulerScheduled, Payload: wbPayloadBytes(t, wbScheduledPayload())}); err == nil || !strings.Contains(err.Error(), "目标不可用") {
			t.Fatalf("构建失败必须上报: err=%v", err)
		}
		select {
		case payload := <-completions:
			if payload.ScheduleID != "sch-1" {
				t.Fatalf("失败任务仍必须完成: payload=%+v", payload)
			}
		default:
			t.Fatal("构建失败必须触发完成回调")
		}
	})
	t.Run("scheduled run freezes request scope", func(t *testing.T) {
		var got RunRequest
		runner := wbStubRunner{run: func(_ context.Context, request RunRequest) (RunResult, error) {
			got = request
			return RunResult{RunID: "run-1", Status: string(RunCompleted)}, nil
		}}
		if err := executor(runner, func(_ context.Context, payload ScheduledPayload) (RunRequest, error) { return RunRequest{}, nil }).Execute(context.Background(), ScheduleTask{Kind: SchedulerScheduled, Payload: wbPayloadBytes(t, wbScheduledPayload())}); err != nil {
			t.Fatalf("定时执行失败: %v", err)
		}
		if got.TriggerKind != string(SchedulerScheduled) || got.SystemAccountID != "sys" || got.Threshold != 70 || got.IdentityKey != "sys:acct:model" {
			t.Fatalf("请求快照=%+v", got)
		}
		select {
		case <-completions:
		default:
			t.Fatal("成功执行必须触发完成回调")
		}
	})
	t.Run("recovery passes durable gates", func(t *testing.T) {
		payload := wbScheduledPayload()
		payload.EnforcementID = "enf-1"
		payload.RecoveryIntervalMinutes = 15
		payload.Generation = 2
		runner := wbStubRunner{run: func(context.Context, RunRequest) (RunResult, error) { return eligibleResult, nil }}
		if err := executor(runner, func(context.Context, ScheduledPayload) (RunRequest, error) { return RunRequest{}, nil }).Execute(context.Background(), ScheduleTask{Kind: SchedulerQualityRecovery, Payload: wbPayloadBytes(t, payload)}); err != nil {
			t.Fatalf("恢复执行失败: %v", err)
		}
		select {
		case received := <-recoveries:
			if received.EnforcementID != "enf-1" || received.Generation != 2 || received.PolicyRevision >= 0 {
				t.Fatalf("合格恢复必须以 passed=true 回调: %+v", received)
			}
		default:
			t.Fatal("恢复执行必须调用租约回调")
		}
	})
	t.Run("recovery missing lease metadata fails closed", func(t *testing.T) {
		payload := wbScheduledPayload()
		payload.EnforcementID = ""
		payload.Generation = 0
		runner := wbStubRunner{run: func(context.Context, RunRequest) (RunResult, error) { return eligibleResult, nil }}
		if err := executor(runner, func(context.Context, ScheduledPayload) (RunRequest, error) { return RunRequest{}, nil }).Execute(context.Background(), ScheduleTask{Kind: SchedulerQualityRecovery, Payload: wbPayloadBytes(t, payload)}); err == nil || !strings.Contains(err.Error(), "lease metadata") {
			t.Fatalf("恢复元数据缺失必须失败关闭: err=%v", err)
		}
	})
	t.Run("recovery ineligible result still reports", func(t *testing.T) {
		payload := wbScheduledPayload()
		payload.EnforcementID = "enf-2"
		payload.RecoveryIntervalMinutes = 15
		payload.Generation = 3
		runner := wbStubRunner{run: func(context.Context, RunRequest) (RunResult, error) {
			return RunResult{RunID: "run-bad", Status: string(RunCompleted), Data: map[string]any{"evidenceFormed": false, "trustFormed": false, "score": 5, "level": "unavailable"}}, nil
		}}
		if err := executor(runner, func(context.Context, ScheduledPayload) (RunRequest, error) { return RunRequest{}, nil }).Execute(context.Background(), ScheduleTask{Kind: SchedulerQualityRecovery, Payload: wbPayloadBytes(t, payload)}); err != nil {
			t.Fatalf("不合格恢复也必须回调: %v", err)
		}
		select {
		case received := <-recoveries:
			if received.EnforcementID != "enf-2" || received.PolicyRevision < 0 {
				t.Fatalf("不合格恢复必须以 passed=false 回调: %+v", received)
			}
		default:
			t.Fatal("恢复回调必须被调用")
		}
	})
	t.Run("recovery invalid policy revision", func(t *testing.T) {
		payload := wbScheduledPayload()
		payload.PolicyRevision = "not-a-number"
		payload.EnforcementID = "enf-3"
		payload.Generation = 1
		runner := wbStubRunner{run: func(context.Context, RunRequest) (RunResult, error) { return eligibleResult, nil }}
		if err := executor(runner, func(context.Context, ScheduledPayload) (RunRequest, error) { return RunRequest{}, nil }).Execute(context.Background(), ScheduleTask{Kind: SchedulerQualityRecovery, Payload: wbPayloadBytes(t, payload)}); err == nil || !strings.Contains(err.Error(), "policy revision") {
			t.Fatalf("非法策略修订必须报错: err=%v", err)
		}
	})
}

type wbStubRunner struct {
	run func(context.Context, RunRequest) (RunResult, error)
}

func (r wbStubRunner) Run(ctx context.Context, request RunRequest) (RunResult, error) {
	return r.run(ctx, request)
}

func wbPayloadBytes(t *testing.T, payload ScheduledPayload) []byte {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// 恢复资格判定契约：只有 formed+trusted、分数达阈值且非 unavailable 的
// 完成结果才能清除质量隔离。
func TestWBRunResultRecoveryEligible(t *testing.T) {
	eligible := RunResult{Status: string(RunCompleted), Data: map[string]any{"evidenceFormed": true, "trustFormed": true, "score": 90, "level": "likely"}}
	if !runResultRecoveryEligible(eligible, 70) {
		t.Fatal("formed+trusted 且达标的结果必须可恢复")
	}
	cases := []struct {
		name   string
		result RunResult
	}{
		{name: "evidence missing", result: RunResult{Status: string(RunCompleted), Data: map[string]any{"evidenceFormed": false, "trustFormed": true, "score": 90, "level": "likely"}}},
		{name: "score below threshold", result: RunResult{Status: string(RunCompleted), Data: map[string]any{"evidenceFormed": true, "trustFormed": true, "score": 5, "level": "likely"}}},
		{name: "unavailable level", result: RunResult{Status: string(RunCompleted), Data: map[string]any{"evidenceFormed": true, "trustFormed": true, "score": 90, "level": "unavailable"}}},
		{name: "non map data", result: RunResult{Status: string(RunCompleted), Data: "text"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if runResultRecoveryEligible(tc.result, 70) {
				t.Fatal("不满足质量门的结果不得恢复")
			}
		})
	}
	if runResultRecoveryEligible(eligible, 10) {
		t.Fatal("阈值越界必须拒绝恢复")
	}
}

// SchedulerExecutorMux 契约：三种任务族各自路由，未知任务族失败关闭。
func TestWBSchedulerExecutorMuxRoutes(t *testing.T) {
	mux := &SchedulerExecutorMux{}
	for kind, need := range map[SchedulerKind]string{
		SchedulerScheduled:       "scheduled executor",
		SchedulerQualityRecovery: "scheduled executor",
		SchedulerHealthRetry:     "health retry executor",
		SchedulerKind("bogus"):   "unsupported J3b scheduler kind",
	} {
		if err := mux.Execute(context.Background(), ScheduleTask{Kind: kind}); err == nil || !strings.Contains(err.Error(), need) {
			t.Fatalf("kind=%s err=%v want 包含 %q", kind, err, need)
		}
	}
	if err := (*SchedulerExecutorMux)(nil).Execute(context.Background(), ScheduleTask{}); err == nil {
		t.Fatal("nil mux 必须报错")
	}
	stub := &SchedulerExecutorMux{
		Runs: &SchedulerRunExecutor{
			Runtime:   wbStubRunner{run: func(context.Context, RunRequest) (RunResult, error) { return RunResult{}, nil }},
			Build:     func(context.Context, ScheduledPayload) (RunRequest, error) { return RunRequest{}, errors.New("skip") },
			Scheduled: func(context.Context, ScheduledPayload, RunResult) error { return nil },
		},
		Health: &HealthSyncRetryExecutor{},
	}
	if err := stub.Execute(context.Background(), ScheduleTask{Kind: SchedulerScheduled, Payload: wbPayloadBytes(t, wbScheduledPayload())}); err == nil || !strings.Contains(err.Error(), "skip") {
		t.Fatalf("scheduled 任务必须路由到 run 执行器: err=%v", err)
	}
}

// HealthSyncRetryExecutor 契约：按 runId 定位失败发布并重新投影。
func TestWBHealthSyncRetryExecutorContract(t *testing.T) {
	j3b := wbOpenMemoryDB(t, wbHealthRuntimeDDL(t))
	store := &Store{db: j3b, mode: "sqlite"}
	defer store.Close()
	statHour, err := NewHealthStatHourFunc("UTC")
	if err != nil {
		t.Fatal(err)
	}
	store.HealthStatHour = statHour
	wbSeedFailedHealthRun(t, store, "run-retry-exec", map[string]any{"account": "acct", "profile": "quick", "policy": `{"revision":"1","threshold":70,"action":"fallback","recoveryIntervalMinutes":10}`, "decision": `{"evidenceFormed":false,"trustFormed":false}`, "request": `{"configRevision":"cfg-1"}`, "finished": "2026-09-05T09:29:00Z"})
	projector := &QualityProjector{Store: store, Enforcement: EnforcementApplierFunc(func(context.Context, QualityEnforcement) error { return nil })}
	executor := &HealthSyncRetryExecutor{Projector: projector}
	if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerScheduled, Payload: []byte(`{}`)}); err == nil {
		t.Fatal("非健康重试任务必须报错")
	}
	if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerHealthRetry, Payload: []byte(`{}`)}); err == nil {
		t.Fatal("缺 runId 的载荷必须报错")
	}
	if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerHealthRetry, Payload: []byte(`{"runId":"run-missing"}`)}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("缺失 run 必须报错: err=%v", err)
	}
	if err := (&HealthSyncRetryExecutor{}).Execute(context.Background(), ScheduleTask{}); err == nil {
		t.Fatal("未初始化执行器必须报错")
	}
	if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerHealthRetry, Payload: []byte(`{"runId":"run-retry-exec"}`)}); err != nil {
		t.Fatalf("健康重试执行失败: %v", err)
	}
	var factCount int
	if err := j3b.QueryRow(`SELECT COUNT(*) FROM account_quality_health_hourly WHERE account_id='acct'`).Scan(&factCount); err != nil || factCount != 1 {
		t.Fatalf("健康事实行数=%d err=%v", factCount, err)
	}
	var syncStatus string
	if err := j3b.QueryRow(`SELECT quality_health_sync_status FROM model_check_runs WHERE id='run-retry-exec'`).Scan(&syncStatus); err != nil || syncStatus != "applied" {
		t.Fatalf("重试后同步状态=%q err=%v", syncStatus, err)
	}
}

// LoadConfig 契约：owner 开关与全部就绪门禁逐项强制。
func TestWBVerifyConfiguredCutoverEvidenceFailsClosed(t *testing.T) {
	t.Skip("清理批次 C1（2026-09-30）：VerifyConfiguredCutoverEvidence 已随切流门禁退役")
}
