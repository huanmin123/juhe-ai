package modelcheckowner

import (
	"context"
	"encoding/json"
	"testing"
)

type schedulerRunnerStub struct {
	request    RunRequest
	resultData any
}

func (s *schedulerRunnerStub) Run(_ context.Context, request RunRequest) (RunResult, error) {
	s.request = request
	return RunResult{RunID: "run-1", Status: string(RunCompleted), Data: s.resultData}, nil
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

func TestSchedulerRecoveryRequiresFormedEvidenceAndTrust(t *testing.T) {
	var passed []bool
	runner := &schedulerRunnerStub{}
	executor := &SchedulerRunExecutor{
		Runtime: runner,
		Build: func(_ context.Context, payload ScheduledPayload) (RunRequest, error) {
			return RunRequest{Endpoint: "https://example.invalid", Prompt: "probe"}, nil
		},
		Recovery: func(_ context.Context, _ RecoveryPayload, value bool) error {
			passed = append(passed, value)
			return nil
		},
	}
	base := ScheduledPayload{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6", Profile: "full", ProviderCode: "openai", Threshold: 70, PenaltyAction: "quality_isolate", ConfigRevision: "4", PolicyRevision: "3", ProbeSetVersion: "probe-4", IdentityKey: "identity-5", OwnerID: "gateway-1", EnforcementID: "enf-1", Generation: 2, RecoveryIntervalMinutes: 10}
	encoded, _ := json.Marshal(base)
	if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerQualityRecovery, Payload: encoded}); err != nil {
		t.Fatal(err)
	}
	if len(passed) != 1 || passed[0] {
		t.Fatalf("missing evidence/trust must keep account isolated: %#v", passed)
	}
	// A runtime result carrying explicit formed/trusted evidence is eligible
	// for the Business generation/CAS completion callback.
	runner.resultData = map[string]any{"evidenceFormed": true, "trustFormed": true}
	encoded, _ = json.Marshal(base)
	if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerQualityRecovery, Payload: encoded}); err != nil {
		t.Fatal(err)
	}
	if len(passed) != 2 || !passed[1] {
		t.Fatalf("formed evidence/trust should pass recovery: %#v", passed)
	}
}
