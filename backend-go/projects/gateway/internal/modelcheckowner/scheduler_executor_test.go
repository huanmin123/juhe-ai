package modelcheckowner

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type schedulerRunnerStub struct {
	request    RunRequest
	resultData any
	err        error
}

func (s *schedulerRunnerStub) Run(_ context.Context, request RunRequest) (RunResult, error) {
	s.request = request
	return RunResult{RunID: "run-1", Status: string(RunCompleted), Data: s.resultData}, s.err
}

func TestSchedulerRunExecutorRejectsIncompleteDurablePolicy(t *testing.T) {
	executor := &SchedulerRunExecutor{Runtime: &Runtime{}, Build: func(context.Context, ScheduledPayload) (RunRequest, error) { return RunRequest{}, nil }}
	payload, _ := json.Marshal(ScheduledPayload{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6"})
	if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerScheduled, Payload: payload}); err == nil {
		t.Fatal("missing policy revision/threshold must fail closed")
	}
}

func TestSchedulerRunExecutorRejectsLegacyPayloadWithoutSourceRevisions(t *testing.T) {
	buildCalled := false
	executor := &SchedulerRunExecutor{
		Runtime: &Runtime{},
		Build: func(context.Context, ScheduledPayload) (RunRequest, error) {
			buildCalled = true
			return RunRequest{}, nil
		},
		Scheduled: func(context.Context, ScheduledPayload, RunResult) error { return nil },
	}
	payload, _ := json.Marshal(ScheduledPayload{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6", Profile: "quick", ProviderCode: "openai", Threshold: 70, PenaltyAction: "fallback", ConfigRevision: "cfg-4", DispatchRevision: 4, PolicyRevision: "pol-2", ProbeSetVersion: "probe-1", IdentityKey: "identity-1", ScheduleID: "schedule-1", OwnerID: "gateway-1", ScheduleRevision: 1, IntervalMinutes: 60})
	if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerScheduled, Payload: payload}); err == nil {
		t.Fatal("legacy payload without source revisions must fail closed")
	}
	if buildCalled {
		t.Fatal("legacy payload must be rejected before resolving a runtime request")
	}
}

func TestSchedulerRunExecutorRejectsPayloadWithoutDispatchRevision(t *testing.T) {
	buildCalled := false
	executor := &SchedulerRunExecutor{
		Runtime: &Runtime{},
		Build: func(context.Context, ScheduledPayload) (RunRequest, error) {
			buildCalled = true
			return RunRequest{}, nil
		},
		Scheduled: func(context.Context, ScheduledPayload, RunResult) error { return nil },
	}
	payload, _ := json.Marshal(ScheduledPayload{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6", Profile: "quick", ProviderCode: "openai", Threshold: 70, PenaltyAction: "fallback", ConfigRevision: "cfg-4", SourceConfigRevision: "src-4", SourceDispatchRevision: 4, PolicyRevision: "pol-2", ProbeSetVersion: "probe-1", IdentityKey: "identity-1", ScheduleID: "schedule-1", OwnerID: "gateway-1", ScheduleRevision: 1, IntervalMinutes: 60})
	if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerScheduled, Payload: payload}); err == nil {
		t.Fatal("payload without dispatch revision must fail closed")
	}
	if buildCalled {
		t.Fatal("payload without dispatch revision must be rejected before resolving a runtime request")
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
	payload, _ := json.Marshal(ScheduledPayload{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6", Profile: "full", ProviderCode: "openai", Threshold: 70, PenaltyAction: "fallback", ConfigRevision: "cfg-2", DispatchRevision: 2, SourceConfigRevision: "src-2", SourceDispatchRevision: 2, PolicyRevision: "pol-3", ProbeSetVersion: "probe-4", IdentityKey: "identity-5", ScheduleID: "schedule-6", OwnerID: "gateway-1", ScheduleRevision: 3, IntervalMinutes: 60})
	if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerScheduled, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if runner.request.TriggerKind != string(SchedulerScheduled) || runner.request.ScheduleID != "schedule-6" || runner.request.SystemAccountID != "sys" || runner.request.ActorSystemAccountID != "actor" || runner.request.TargetID != "acct" || runner.request.ProviderCode != "openai" || runner.request.Threshold != 70 || runner.request.PenaltyAction != "fallback" || runner.request.ConfigRevision != "cfg-2" || runner.request.DispatchRevision != 2 || runner.request.SourceConfigRevision != "src-2" || runner.request.SourceDispatchRevision != 2 || runner.request.PolicyRevision != "pol-3" {
		t.Fatalf("mapped request=%+v", runner.request)
	}
}

func TestSchedulerRunExecutorFailsClosedForStaleDispatchPayload(t *testing.T) {
	var resolved RunRequest
	var completion RunResult
	runtime := &Runtime{
		Store: &Store{},
		Resolve: func(_ context.Context, request RunRequest) (Target, error) {
			resolved = request
			return Target{Endpoint: "https://example.invalid", Prompt: "probe", DispatchRevision: 7}, nil
		},
	}
	executor := &SchedulerRunExecutor{
		Runtime: runtime,
		Build: func(_ context.Context, _ ScheduledPayload) (RunRequest, error) {
			return RunRequest{DispatchRevision: 99}, nil
		},
		Scheduled: func(_ context.Context, _ ScheduledPayload, result RunResult) error {
			completion = result
			return nil
		},
	}
	payload, _ := json.Marshal(ScheduledPayload{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6", Profile: "quick", ProviderCode: "openai", Threshold: 70, PenaltyAction: "fallback", ConfigRevision: "cfg-2", DispatchRevision: 6, SourceConfigRevision: "src-2", SourceDispatchRevision: 2, PolicyRevision: "pol-3", ProbeSetVersion: "probe-4", IdentityKey: "identity-5", ScheduleID: "schedule-6", OwnerID: "gateway-1", ScheduleRevision: 3, IntervalMinutes: 60})
	if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerScheduled, Payload: payload}); err != nil {
		t.Fatalf("stale scheduled dispatch should be recorded as failed: %v", err)
	}
	if resolved.DispatchRevision != 6 {
		t.Fatalf("scheduler must preserve payload dispatch revision, request=%+v", resolved)
	}
	if completion.Status != string(RunFailed) {
		t.Fatalf("stale dispatch should fail closed, completion=%+v", completion)
	}
}

func TestSchedulerRunExecutorFailsClosedWithoutRecoveryCAS(t *testing.T) {
	runner := &schedulerRunnerStub{}
	executor := &SchedulerRunExecutor{Runtime: runner, Build: func(_ context.Context, payload ScheduledPayload) (RunRequest, error) {
		return RunRequest{Endpoint: "https://example.invalid", Prompt: "probe"}, nil
	}}
	payload, _ := json.Marshal(ScheduledPayload{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6", Profile: "full", ProviderCode: "openai", Threshold: 70, PenaltyAction: "quality_isolate", ConfigRevision: "4", DispatchRevision: 4, SourceConfigRevision: "src-4", SourceDispatchRevision: 4, PolicyRevision: "3", ProbeSetVersion: "probe-4", IdentityKey: "identity-5"})
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
	base := ScheduledPayload{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6", Profile: "full", ProviderCode: "openai", Threshold: 70, PenaltyAction: "quality_isolate", ConfigRevision: "4", DispatchRevision: 4, SourceConfigRevision: "src-4", SourceDispatchRevision: 4, PolicyRevision: "3", ProbeSetVersion: "probe-4", IdentityKey: "identity-5", OwnerID: "gateway-1", EnforcementID: "enf-1", Generation: 2, RecoveryIntervalMinutes: 10}
	encoded, _ := json.Marshal(base)
	if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerQualityRecovery, Payload: encoded}); err != nil {
		t.Fatal(err)
	}
	if len(passed) != 1 || passed[0] {
		t.Fatalf("missing evidence/trust must keep account isolated: %#v", passed)
	}
	// A runtime result carrying formed/trusted evidence but a low score must
	// keep the account isolated.
	runner.resultData = map[string]any{"evidenceFormed": true, "trustFormed": true, "score": 69, "level": "success"}
	encoded, _ = json.Marshal(base)
	if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerQualityRecovery, Payload: encoded}); err != nil {
		t.Fatal(err)
	}
	if len(passed) != 2 || passed[1] {
		t.Fatalf("low score must keep account isolated: %#v", passed)
	}
	// A complete, formed/trusted quality result at the frozen threshold is
	// eligible for the Business generation/CAS completion callback.
	runner.resultData = map[string]any{"evidenceFormed": true, "trustFormed": true, "score": 70, "level": "success"}
	if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerQualityRecovery, Payload: encoded}); err != nil {
		t.Fatal(err)
	}
	if len(passed) != 3 || !passed[2] {
		t.Fatalf("formed quality success should pass recovery: %#v", passed)
	}
	runner.resultData = map[string]any{"evidenceFormed": true, "trustFormed": true, "score": 100, "level": "unavailable"}
	if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerQualityRecovery, Payload: encoded}); err != nil {
		t.Fatal(err)
	}
	if len(passed) != 4 || passed[3] {
		t.Fatalf("unavailable result must keep account isolated: %#v", passed)
	}
}

func TestSchedulerRecoveryRunErrorReschedulesThroughCompletion(t *testing.T) {
	runner := &schedulerRunnerStub{err: errors.New("upstream unavailable")}
	var passed bool
	called := false
	executor := &SchedulerRunExecutor{
		Runtime: runner,
		Build: func(_ context.Context, _ ScheduledPayload) (RunRequest, error) {
			return RunRequest{Endpoint: "https://example.invalid", Prompt: "probe"}, nil
		},
		Recovery: func(_ context.Context, _ RecoveryPayload, value bool) error {
			called, passed = true, value
			return nil
		},
	}
	payload, _ := json.Marshal(ScheduledPayload{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6", Profile: "full", ProviderCode: "openai", Threshold: 70, PenaltyAction: "quality_isolate", ConfigRevision: "4", DispatchRevision: 4, SourceConfigRevision: "src-4", SourceDispatchRevision: 4, PolicyRevision: "3", ProbeSetVersion: "probe-4", IdentityKey: "identity-5", OwnerID: "gateway-1", EnforcementID: "enf-1", Generation: 2, RecoveryIntervalMinutes: 10})
	if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerQualityRecovery, Payload: payload}); err != nil {
		t.Fatalf("runtime error should be handled by recovery completion: %v", err)
	}
	if !called || passed {
		t.Fatalf("recovery completion=%v passed=%v, want called with false", called, passed)
	}
}

func TestSchedulerScheduledRunErrorCompletesAsFailed(t *testing.T) {
	runner := &schedulerRunnerStub{err: errors.New("upstream unavailable")}
	var status string
	executor := &SchedulerRunExecutor{
		Runtime: runner,
		Build: func(_ context.Context, _ ScheduledPayload) (RunRequest, error) {
			return RunRequest{Endpoint: "https://example.invalid", Prompt: "probe"}, nil
		},
		Scheduled: func(_ context.Context, _ ScheduledPayload, result RunResult) error {
			status = result.Status
			return nil
		},
	}
	payload, _ := json.Marshal(ScheduledPayload{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6", Profile: "full", ProviderCode: "openai", Threshold: 70, PenaltyAction: "fallback", ConfigRevision: "4", DispatchRevision: 4, SourceConfigRevision: "src-4", SourceDispatchRevision: 4, PolicyRevision: "3", ProbeSetVersion: "probe-4", IdentityKey: "identity-5", OwnerID: "gateway-1", ScheduleID: "sch-1", ScheduleRevision: 2, IntervalMinutes: 60})
	if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerScheduled, Payload: payload}); err != nil {
		t.Fatalf("runtime error should be recorded by scheduled completion: %v", err)
	}
	if status != string(RunFailed) {
		t.Fatalf("scheduled completion status=%q, want failed", status)
	}
}
