package modelcheckowner

import (
	"context"
	"encoding/json"
	"testing"
)

type schedulerRunnerStub struct{ request RunRequest }

func (s *schedulerRunnerStub) Run(_ context.Context, request RunRequest) (RunResult, error) {
	s.request = request
	return RunResult{RunID: "run-1", Status: string(RunCompleted)}, nil
}

func TestSchedulerRunExecutorRejectsIncompleteDurablePolicy(t *testing.T) {
	executor := &SchedulerRunExecutor{Runtime: &Runtime{}, Build: func(context.Context, ScheduledPayload) (RunRequest, error) { return RunRequest{}, nil }}
	payload, _ := json.Marshal(ScheduledPayload{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6"})
	if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerScheduled, Payload: payload}); err == nil {
		t.Fatal("missing policy revision/threshold must fail closed")
	}
}

func TestSchedulerExecutorMuxRoutesHealthSeparately(t *testing.T) {
	mux := &SchedulerExecutorMux{}
	if err := mux.Execute(context.Background(), ScheduleTask{Kind: SchedulerScheduled}); err == nil {
		t.Fatal("scheduled task without run executor must fail")
	}
	if err := mux.Execute(context.Background(), ScheduleTask{Kind: SchedulerHealthRetry, Payload: []byte(`{}`)}); err == nil {
		t.Fatal("health task without retry executor must fail")
	}
	if err := mux.Execute(context.Background(), ScheduleTask{Kind: "unknown"}); err == nil {
		t.Fatal("unknown scheduler kind must fail")
	}
}

func TestSchedulerRunExecutorMapsDurablePayload(t *testing.T) {
	runner := &schedulerRunnerStub{}
	executor := &SchedulerRunExecutor{Runtime: runner, Build: func(_ context.Context, payload ScheduledPayload) (RunRequest, error) {
		return RunRequest{Endpoint: "https://example.invalid", Prompt: "probe"}, nil
	}, Scheduled: func(context.Context, ScheduledPayload, RunResult) error { return nil }}
	payload, _ := json.Marshal(ScheduledPayload{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6", Profile: "full", ProviderCode: "openai", Threshold: 70, PenaltyAction: "fallback", ConfigRevision: "cfg-2", PolicyRevision: "pol-3", ProbeSetVersion: "probe-4", IdentityKey: "identity-5", ScheduleID: "schedule-6", OwnerID: "gateway-1", ScheduleRevision: 3, IntervalMinutes: 60})
	if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerScheduled, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if runner.request.TriggerKind != string(SchedulerScheduled) || runner.request.ScheduleID != "schedule-6" || runner.request.SystemAccountID != "sys" || runner.request.ActorSystemAccountID != "actor" || runner.request.TargetID != "acct" || runner.request.ProviderCode != "openai" || runner.request.Threshold != 70 || runner.request.PenaltyAction != "fallback" || runner.request.PolicyRevision != "pol-3" {
		t.Fatalf("mapped request=%+v", runner.request)
	}
}

func TestSchedulerRunExecutorFailsClosedWithoutRecoveryCAS(t *testing.T) {
	runner := &schedulerRunnerStub{}
	executor := &SchedulerRunExecutor{Runtime: runner, Build: func(_ context.Context, payload ScheduledPayload) (RunRequest, error) {
		return RunRequest{Endpoint: "https://example.invalid", Prompt: "probe"}, nil
	}}
	payload, _ := json.Marshal(ScheduledPayload{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6", Profile: "full", ProviderCode: "openai", Threshold: 70, PenaltyAction: "quality_isolate", ConfigRevision: "4", PolicyRevision: "3", ProbeSetVersion: "probe-4", IdentityKey: "identity-5"})
	if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerQualityRecovery, Payload: payload}); err == nil {
		t.Fatal("quality recovery must fail closed without a Business CAS completion owner")
	}
}
