package gatewayattemptloop

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/gatewayaudit"
	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewayhttpcompletion"
	"juhe-ai/backend-go/internal/modules/gatewayingress"
	"juhe-ai/backend-go/internal/modules/gatewayresponse"
	"juhe-ai/backend-go/internal/modules/gatewayresponseinspection"
	"juhe-ai/backend-go/internal/modules/gatewayresponseterminal"
	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
	"juhe-ai/backend-go/internal/modules/gatewayusage"
	protocolgateway "juhe-ai/backend-go/internal/protocols/gateway"
	"juhe-ai/backend-go/internal/store/port"
)

func TestRunRetriesNextAPIKeyBeforeNextAccount(t *testing.T) {
	executor := &executorStub{results: []AttemptResult{
		{RetryAllowed: true, KeyScopedFailure: true, Failure: FailureFacts{StatusCode: 429}},
		{Success: true, Committed: true},
	}}
	service := newTestService(t, executor, nil, Config{MaxAttempts: 4, WallTimeout: time.Minute, FirstByteTimeout: 10 * time.Second})
	result, err := service.Run(Input{Context: context.Background(), MutationID: "request-1", Candidates: []gatewaycandidatewindow.Candidate{apiKeyCandidate("a", []int{1, 3}), apiKeyCandidate("b", []int{0})}, Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText})
	if err != nil || result.Outcome != OutcomeSucceeded || len(executor.attempts) != 2 {
		t.Fatalf("result = %+v err=%v attempts=%+v", result, err, executor.attempts)
	}
	if executor.attempts[0].CandidateIndex != 0 || executor.attempts[0].APIKeyIndex != 1 || executor.attempts[1].CandidateIndex != 0 || executor.attempts[1].APIKeyIndex != 3 {
		t.Fatalf("attempts = %+v", executor.attempts)
	}
}

func TestRunCopiesFinalClaimFactsIntoAttempt(t *testing.T) {
	executor := &executorStub{results: []AttemptResult{{Success: true, Committed: true}}}
	service := newTestService(t, executor, nil, Config{MaxAttempts: 1, WallTimeout: time.Minute})
	candidate := oauthCandidate("a")
	candidate.Projection.ProtocolCode = "openai"
	request := protocolgateway.RequestShape{Method: "POST", Path: "/v1/images/generations", Model: "gpt-image", ImageGenerationHint: true}
	result, err := service.Run(Input{Context: context.Background(), MutationID: "request-claim-facts", Candidates: []gatewaycandidatewindow.Candidate{candidate}, Request: request, FinalLane: gatewayingress.LaneText})
	if err != nil || result.Outcome != OutcomeSucceeded || len(executor.attempts) != 1 {
		t.Fatalf("result = %+v err=%v attempts=%+v", result, err, executor.attempts)
	}
	attempt := executor.attempts[0]
	if attempt.RequestedModel != "gpt-image" || attempt.EndpointFamily != "images" || attempt.Lane != "text" {
		t.Fatalf("claim facts = %+v", attempt)
	}
}

func TestRunAllowsDisabledFirstByteDeadlineWithoutRemovingWallDeadline(t *testing.T) {
	now := time.Now().UTC()
	executor := &executorStub{results: []AttemptResult{{Success: true, Committed: true}}}
	service := newTestService(t, executor, nil, Config{MaxAttempts: 1, WallTimeout: time.Minute, FirstByteTimeout: 0}).WithNow(func() time.Time { return now })

	result, err := service.Run(Input{
		Context: context.Background(), MutationID: "request-1",
		Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a")}, Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText,
	})
	if err != nil || result.Outcome != OutcomeSucceeded || len(executor.attempts) != 1 {
		t.Fatalf("result = %+v err=%v attempts=%+v", result, err, executor.attempts)
	}
	attempt := executor.attempts[0]
	if !attempt.Budget.FirstByteDeadline.IsZero() || attempt.Budget.FirstByteTimeout != 0 {
		t.Fatalf("first-byte budget = %+v, want disabled", attempt.Budget)
	}
	if !attempt.Budget.WallDeadline.Equal(now.Add(time.Minute)) || !result.WallDeadline.Equal(now.Add(time.Minute)) {
		t.Fatalf("wall deadline must remain active: attempt=%v result=%v", attempt.Budget.WallDeadline, result.WallDeadline)
	}
}

func TestRunAllowsExplicitlyDisabledWallTimeoutButKeepsParentDeadline(t *testing.T) {
	now := time.Now().UTC()
	parentDeadline := now.Add(time.Minute)
	parent, cancel := context.WithDeadline(context.Background(), parentDeadline)
	defer cancel()
	executor := &executorStub{results: []AttemptResult{{Success: true, Committed: true}}}
	service := newTestService(t, executor, nil, Config{MaxAttempts: 1, DisableWallTimeout: true, FirstByteTimeout: 0}).WithNow(func() time.Time { return now })

	result, err := service.Run(Input{
		Context: parent, MutationID: "request-1", Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a")}, Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText,
	})
	if err != nil || result.Outcome != OutcomeSucceeded || len(executor.attempts) != 1 {
		t.Fatalf("result = %+v err=%v attempts=%+v", result, err, executor.attempts)
	}
	attempt := executor.attempts[0]
	if !attempt.Budget.FirstByteDeadline.IsZero() || !attempt.Budget.WallDeadline.Equal(parentDeadline) || !result.WallDeadline.Equal(parentDeadline) {
		t.Fatalf("budget/result = %+v/%+v", attempt.Budget, result)
	}

	unboundedExecutor := &executorStub{results: []AttemptResult{{Success: true, Committed: true}}}
	unboundedService := newTestService(t, unboundedExecutor, nil, Config{MaxAttempts: 1, DisableWallTimeout: true, FirstByteTimeout: 0}).WithNow(func() time.Time { return now })
	result, err = unboundedService.Run(Input{
		Context: context.Background(), MutationID: "request-2", Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a")}, Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText,
	})
	if err != nil || result.Outcome != OutcomeSucceeded || len(unboundedExecutor.attempts) != 1 || !result.WallDeadline.IsZero() || !unboundedExecutor.attempts[0].Budget.WallDeadline.IsZero() {
		t.Fatalf("unbounded result=%+v err=%v attempt=%+v", result, err, unboundedExecutor.attempts)
	}
}

func TestRunExplicitRetryNextSkipsRemainingKeys(t *testing.T) {
	executor := &executorStub{results: []AttemptResult{{
		RetryAllowed: true, KeyScopedFailure: true,
		Failure: FailureFacts{StatusCode: 429, BodyText: "quota"},
	}, {Success: true, Committed: true}}}
	first := apiKeyCandidate("a", []int{0, 1})
	first.Credentials = gatewaycandidatewindow.NewCredentialSet(map[string]any{"error_handling_rules": []any{rule(map[string]any{"action": "retry_next"})}})
	service := newTestService(t, executor, nil, Config{MaxAttempts: 4, WallTimeout: time.Minute, FirstByteTimeout: time.Second})
	result, err := service.Run(Input{Context: context.Background(), MutationID: "request-1", Candidates: []gatewaycandidatewindow.Candidate{first, oauthCandidate("b")}, Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText})
	if err != nil || result.Outcome != OutcomeSucceeded || len(executor.attempts) != 2 || executor.attempts[1].CandidateIndex != 1 {
		t.Fatalf("result = %+v err=%v attempts=%+v", result, err, executor.attempts)
	}
}

func TestRunResponseInspectionHandoffSkipsGenericAccountPolicy(t *testing.T) {
	invalid := PolicyDecision{Action: PolicyActionCooldown, RuleName: "must-not-run"}
	executor := &executorStub{results: []AttemptResult{
		{RetryAllowed: false, ResponseInspection: &gatewayresponseinspection.Handoff{Decision: &gatewayresponseinspection.Decision{}}, Failure: FailureFacts{StatusCode: 200, ErrorCode: "response_inspection_matched"}, PolicyDecision: &invalid},
	}}
	service := newTestService(t, executor, nil, Config{MaxAttempts: 2, WallTimeout: time.Minute, FirstByteTimeout: time.Second})
	result, err := service.Run(Input{Context: context.Background(), MutationID: "response-inspection", Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a"), oauthCandidate("b")}, Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText})
	if err != nil || result.Outcome != OutcomeFailed || len(executor.attempts) != 1 || result.Attempts[0].PolicyAction != PolicyActionNone || result.Attempts[0].ResponseInspection == nil || result.Attempts[0].ResponseInspection.Decision == nil {
		t.Fatalf("result=%+v error=%v attempts=%+v", result, err, executor.attempts)
	}
}

func TestRunAppliesCooldownThenAdvancesAccount(t *testing.T) {
	executor := &executorStub{results: []AttemptResult{{
		RetryAllowed: true, Failure: FailureFacts{StatusCode: 429, ErrorCode: "rate_limit"},
	}, {Success: true, Committed: true}}}
	applier := &applierStub{}
	first := oauthCandidate("a")
	first.Credentials = gatewaycandidatewindow.NewCredentialSet(map[string]any{"error_handling_rules": []any{rule(map[string]any{
		"action": "rate_limited", "error_codes": []any{"rate_limit"}, "reset_strategy": "duration", "duration_hours": float64(1),
	})}})
	service := newTestService(t, executor, applier, Config{MaxAttempts: 3, WallTimeout: time.Minute, FirstByteTimeout: time.Second})
	result, err := service.Run(Input{Context: context.Background(), MutationID: "request-1", TraceID: "trace-1", Candidates: []gatewaycandidatewindow.Candidate{first, oauthCandidate("b")}, Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText})
	if err != nil || result.Outcome != OutcomeSucceeded || len(applier.mutations) != 1 || applier.mutations[0].Decision.Action != PolicyActionCooldown {
		t.Fatalf("result = %+v err=%v mutations=%+v", result, err, applier.mutations)
	}
	mutation := applier.mutations[0]
	if mutation.Target.AccountID != "a" || mutation.Target.ExpectedConfigRevision != 1 || mutation.Target.ExpectedDispatchRevision != 1 || mutation.Source.AccountID != "a" {
		t.Fatalf("mutation identity = %+v", mutation)
	}
	if mutation.TraceID != "trace-1" || len(mutation.TransitionID) > 256 || !strings.HasPrefix(mutation.TransitionID, policyTransitionPrefix) || strings.Contains(mutation.Reason, "quota") {
		t.Fatalf("mutation diagnostics = %+v", mutation)
	}
	if result.Attempts[0].PolicyApply == nil || result.Attempts[0].PolicyApply.Status != PolicyApplyApplied {
		t.Fatalf("policy apply summary = %+v", result.Attempts[0].PolicyApply)
	}
}

func TestRunStopsAfterCommitAndOnNonRetryableFailure(t *testing.T) {
	for _, attemptResult := range []AttemptResult{
		{Committed: true, RetryAllowed: false, Failure: FailureFacts{StatusCode: 500}},
		{RetryAllowed: false, Failure: FailureFacts{StatusCode: 500}},
	} {
		executor := &executorStub{results: []AttemptResult{attemptResult}}
		service := newTestService(t, executor, nil, Config{MaxAttempts: 3, WallTimeout: time.Minute, FirstByteTimeout: time.Second})
		result, err := service.Run(Input{Context: context.Background(), MutationID: "request-1", Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a"), oauthCandidate("b")}, Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText})
		if err != nil || result.Outcome != OutcomeFailed || len(executor.attempts) != 1 {
			t.Fatalf("result = %+v err=%v attempts=%d", result, err, len(executor.attempts))
		}
	}
}

func TestRunHonorsMaxAttemptsAndSkipsUnavailableKeys(t *testing.T) {
	executor := &executorStub{fallback: AttemptResult{RetryAllowed: true, KeyScopedFailure: true}}
	candidate := apiKeyCandidate("a", []int{0, 1, 2})
	candidate.APIKeyRuntime[0].Status = "disabled"
	service := newTestService(t, executor, nil, Config{MaxAttempts: 1, WallTimeout: time.Minute, FirstByteTimeout: time.Second})
	result, err := service.Run(Input{Context: context.Background(), MutationID: "request-1", Candidates: []gatewaycandidatewindow.Candidate{candidate}, Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText})
	if err != nil || result.Outcome != OutcomeMaxAttempts || len(executor.attempts) != 1 || executor.attempts[0].APIKeyIndex != 1 {
		t.Fatalf("result = %+v err=%v attempts=%+v", result, err, executor.attempts)
	}
}

func TestRunCapsAPIKeyAttemptsPerCandidate(t *testing.T) {
	executor := &executorStub{fallback: AttemptResult{RetryAllowed: true, KeyScopedFailure: true}}
	service := newTestService(t, executor, nil, Config{MaxAttempts: 8, WallTimeout: time.Minute, FirstByteTimeout: time.Second})
	result, err := service.Run(Input{Context: context.Background(), MutationID: "request-1", Candidates: []gatewaycandidatewindow.Candidate{apiKeyCandidate("a", []int{0, 1, 2, 3}), oauthCandidate("b")}, Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText})
	if err != nil || result.Outcome != OutcomeCandidatesExhausted || len(executor.attempts) != 3 {
		t.Fatalf("result = %+v err=%v attempts=%+v", result, err, executor.attempts)
	}
	if executor.attempts[2].CandidateIndex != 1 {
		t.Fatalf("third attempt should advance account: %+v", executor.attempts)
	}
}

func TestRunCapsDistinctCandidateAttempts(t *testing.T) {
	executor := &executorStub{fallback: AttemptResult{RetryAllowed: true}}
	candidates := []gatewaycandidatewindow.Candidate{oauthCandidate("a"), oauthCandidate("b"), oauthCandidate("c"), oauthCandidate("d"), oauthCandidate("e")}
	service := newTestService(t, executor, nil, Config{MaxAttempts: 16, WallTimeout: time.Minute, FirstByteTimeout: time.Second})
	result, err := service.Run(Input{Context: context.Background(), MutationID: "request-1", Candidates: candidates, Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText})
	if err != nil || result.Outcome != OutcomeMaxAttempts || len(executor.attempts) != MaxCandidateAttemptsPerRequest {
		t.Fatalf("result = %+v err=%v attempts=%+v", result, err, executor.attempts)
	}
}

func TestRunPolicyMutationAllowsTypedAvailabilityFailover(t *testing.T) {
	for _, status := range []PolicyApplyStatus{PolicyApplyApplied, PolicyApplyIdempotent, PolicyApplyStaleTarget, PolicyApplyStaleSource, PolicyApplyIneligible} {
		t.Run(string(status), func(t *testing.T) {
			executor := &executorStub{fallback: AttemptResult{RetryAllowed: true, Failure: FailureFacts{StatusCode: 429, ErrorCode: "rate_limit"}}}
			applier := &applierStub{result: validPolicyApplyResult(status)}
			first := oauthCandidate("a")
			first.Credentials = gatewaycandidatewindow.NewCredentialSet(map[string]any{"error_handling_rules": []any{rule(map[string]any{"action": "rate_limited", "error_codes": []any{"rate_limit"}, "reset_strategy": "duration", "duration_hours": float64(1)})}})
			service := newTestService(t, executor, applier, Config{MaxAttempts: 4, WallTimeout: time.Minute, FirstByteTimeout: time.Second})
			result, err := service.Run(Input{Context: context.Background(), MutationID: "request-1", Candidates: []gatewaycandidatewindow.Candidate{first, oauthCandidate("b")}, Request: protocolgateway.RequestShape{Method: "POST", Path: "/v1beta/interactions"}, FinalLane: gatewayingress.LaneText})
			if err != nil || result.Outcome != OutcomeCandidatesExhausted || len(executor.attempts) != 2 || executor.attempts[1].CandidateIndex != 1 || len(applier.mutations) != 1 || result.Attempts[0].PolicyApply == nil || result.Attempts[0].PolicyApply.Status != status {
				t.Fatalf("result = %+v err=%v attempts=%+v mutations=%+v", result, err, executor.attempts, applier.mutations)
			}
		})
	}
}

func TestRunPolicyWriterErrorPreservesAttemptEvidenceAndStops(t *testing.T) {
	storeErr := errors.New("postgres unavailable")
	usage := gatewayusage.TerminalFacts{Outcome: gatewayusage.OutcomeFailed, ErrorCode: "usage-proof"}
	audit := gatewayaudit.TerminalInput{RequestedOutcome: gatewayaudit.OutcomeUpstreamFailed, ErrorCode: "audit-proof"}
	executor := &executorStub{results: []AttemptResult{{
		RetryAllowed: true,
		Failure:      FailureFacts{StatusCode: 429, ErrorCode: "rate_limit"},
		Usage:        usage,
		Audit:        audit,
	}}}
	applier := &applierStub{err: storeErr}
	first := oauthCandidate("a")
	first.Credentials = gatewaycandidatewindow.NewCredentialSet(map[string]any{"error_handling_rules": []any{rule(map[string]any{"action": "rate_limited", "error_codes": []any{"rate_limit"}, "reset_strategy": "duration", "duration_hours": float64(1)})}})
	service := newTestService(t, executor, applier, Config{MaxAttempts: 4, WallTimeout: time.Minute, FirstByteTimeout: time.Second})
	result, err := service.Run(Input{Context: context.Background(), MutationID: "request-error", Candidates: []gatewaycandidatewindow.Candidate{first, oauthCandidate("b")}, Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText})
	if !errors.Is(err, storeErr) || result.Outcome != OutcomeFailed || !errors.Is(result.TerminalError, storeErr) || len(executor.attempts) != 1 || len(result.Attempts) != 1 || result.LastAttempt == nil {
		t.Fatalf("result = %+v err=%v attempts=%d", result, err, len(executor.attempts))
	}
	if result.Attempts[0].Usage.ErrorCode != "usage-proof" || result.Attempts[0].Audit.ErrorCode != "audit-proof" || result.LastAttempt.Usage.ErrorCode != "usage-proof" || result.LastAttempt.Audit.ErrorCode != "audit-proof" {
		t.Fatalf("attempt evidence was lost: %+v / %+v", result.Attempts[0], result.LastAttempt)
	}
}

func TestRunRejectsInvalidExternalPolicyDecisionBeforeWriter(t *testing.T) {
	invalid := PolicyDecision{Action: PolicyActionCooldown, RuleName: "external", CooldownStatus: CooldownRateLimited}
	executor := &executorStub{results: []AttemptResult{{RetryAllowed: true, Failure: FailureFacts{StatusCode: 429}, PolicyDecision: &invalid}}}
	applier := &applierStub{}
	service := newTestService(t, executor, applier, Config{MaxAttempts: 4, WallTimeout: time.Minute, FirstByteTimeout: time.Second})
	result, err := service.Run(Input{Context: context.Background(), MutationID: "request-invalid", Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a"), oauthCandidate("b")}, Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText})
	if err == nil || result.Outcome != OutcomeFailed || len(result.Attempts) != 1 || len(executor.attempts) != 1 || len(applier.mutations) != 0 {
		t.Fatalf("result = %+v err=%v attempts=%d mutations=%d", result, err, len(executor.attempts), len(applier.mutations))
	}
}

func TestRunRequiresStableMutationIDBeforeExecuting(t *testing.T) {
	executor := &executorStub{fallback: AttemptResult{Success: true, Committed: true}}
	service := newTestService(t, executor, nil, Config{WallTimeout: time.Minute, FirstByteTimeout: time.Second})
	if _, err := service.Run(Input{Context: context.Background(), Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a")}}); err == nil || len(executor.attempts) != 0 {
		t.Fatalf("err=%v attempts=%d", err, len(executor.attempts))
	}
}

func TestRunFailsClosedWithoutFrozenFinalLane(t *testing.T) {
	executor := &executorStub{fallback: AttemptResult{Success: true, Committed: true}}
	service := newTestService(t, executor, nil, Config{WallTimeout: time.Minute, FirstByteTimeout: time.Second})
	_, err := service.Run(Input{
		Context: context.Background(), MutationID: "request-missing-lane",
		Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a")},
		Request:    protocolgateway.RequestShape{Method: "POST", Path: "/v1/images/generations", ImageGenerationHint: true},
	})
	if err == nil || len(executor.attempts) != 0 {
		t.Fatalf("err=%v attempts=%d", err, len(executor.attempts))
	}
}

func TestRunAvailabilityFailoverDoesNotDependOnRequestTaxonomy(t *testing.T) {
	for _, path := range []string{
		"/v1/responses",
		"/v1/files",
		"/v1/vector_stores",
		"/v1/unknown-operation",
	} {
		t.Run(path, func(t *testing.T) {
			executor := &executorStub{results: []AttemptResult{{RetryAllowed: true}, {Success: true, Committed: true}}}
			service := newTestService(t, executor, nil, Config{MaxAttempts: 4, WallTimeout: time.Minute, FirstByteTimeout: time.Second})
			request := protocolgateway.RequestShape{Method: "POST", Path: path}
			result, err := service.Run(Input{Context: context.Background(), MutationID: "request-1", Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a"), oauthCandidate("b")}, Request: request, FinalLane: gatewayingress.LaneText})
			if err != nil || result.Outcome != OutcomeSucceeded || len(executor.attempts) != 2 || !executor.attempts[0].AvailabilityFailoverAllowed {
				t.Fatalf("result = %+v err=%v attempts=%+v", result, err, executor.attempts)
			}
		})
	}
}

func TestRunPropagatesBudgetsAndContextTerminalStates(t *testing.T) {
	now := time.Now().UTC()
	executor := &executorStub{results: []AttemptResult{{Success: true, Committed: true}}}
	service := newTestService(t, executor, nil, Config{WallTimeout: 30 * time.Second, FirstByteTimeout: 7 * time.Second}).WithNow(func() time.Time { return now })
	result, err := service.Run(Input{Context: context.Background(), MutationID: "request-1", Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a")}, FinalLane: gatewayingress.LaneText})
	if err != nil || result.Outcome != OutcomeSucceeded || !executor.attempts[0].Budget.WallDeadline.Equal(now.Add(30*time.Second)) || executor.attempts[0].Budget.FirstByteTimeout != 7*time.Second || !executor.attempts[0].Budget.FirstByteDeadline.Equal(now.Add(7*time.Second)) {
		t.Fatalf("result = %+v err=%v attempt=%+v", result, err, executor.attempts[0])
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	lifecycle := &lifecycleStub{}
	result, err = service.Run(Input{Context: canceled, MutationID: "request-1", Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a")}, FinalLane: gatewayingress.LaneText, Lifecycle: lifecycle})
	if err != nil || result.Outcome != OutcomeCanceled || !errors.Is(result.TerminalError, context.Canceled) {
		t.Fatalf("canceled result = %+v err=%v", result, err)
	}
	if strings.Join(lifecycle.operations, ",") != "cancel" {
		t.Fatalf("canceled lifecycle operations=%v", lifecycle.operations)
	}

	expired, expiredCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expiredCancel()
	lifecycle = &lifecycleStub{}
	result, err = service.Run(Input{Context: expired, MutationID: "request-deadline", Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a")}, FinalLane: gatewayingress.LaneText, Lifecycle: lifecycle})
	if err != nil || result.Outcome != OutcomeDeadlineExceeded || !errors.Is(result.TerminalError, context.DeadlineExceeded) || strings.Join(lifecycle.operations, ",") != "failure:gateway" {
		t.Fatalf("deadline result=%+v err=%v lifecycle=%v", result, err, lifecycle.operations)
	}
}

func TestRunDrivesRequestLifecycleAcrossPreCommitRetryAndSuccess(t *testing.T) {
	sink := gatewaystreamrelay.SinkState{TransportCommitted: true, SemanticCommitted: true, DownstreamBytes: 11}
	executor := &executorStub{results: []AttemptResult{
		{RetryAllowed: true},
		{Success: true, Committed: true, Sink: &sink},
	}}
	lifecycle := &lifecycleStub{}
	service := newTestService(t, executor, nil, Config{MaxAttempts: 2, WallTimeout: time.Minute})
	result, err := service.Run(Input{
		Context: context.Background(), MutationID: "lifecycle-retry", Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a"), oauthCandidate("b")},
		Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText, Lifecycle: lifecycle,
	})
	if err != nil || result.Outcome != OutcomeSucceeded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	want := []string{"start", "retry", "start", "observe", "success"}
	if strings.Join(lifecycle.operations, ",") != strings.Join(want, ",") || lifecycle.sink != sink {
		t.Fatalf("operations=%v sink=%+v", lifecycle.operations, lifecycle.sink)
	}
}

func TestRunDefersSuccessfulLifecycleTerminalToResponseHandoff(t *testing.T) {
	sink := gatewaystreamrelay.SinkState{TransportCommitted: true, SemanticCommitted: true, DownstreamBytes: 11}
	response := &gatewayresponse.Result{State: gatewayresponse.StateSucceeded, TransportCommitted: true, SemanticCommitted: true}
	executor := &executorStub{results: []AttemptResult{{Success: true, Committed: true, Sink: &sink, Response: response}}}
	lifecycle := &lifecycleStub{}
	service := newTestService(t, executor, nil, Config{MaxAttempts: 1, WallTimeout: time.Minute})
	result, err := service.Run(Input{
		Context: context.Background(), MutationID: "deferred-success", Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a")},
		Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText, Lifecycle: lifecycle, DeferResponseTerminal: true,
	})
	if err != nil || result.Outcome != OutcomeSucceeded || result.PendingResponseTerminal == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if got := strings.Join(lifecycle.operations, ","); got != "start" {
		t.Fatalf("lifecycle operations=%s", got)
	}
	terminal := gatewayhttpcompletion.New(nil)
	adapter, err := gatewayresponseterminal.NewFromHandoff(terminal, result.PendingResponseTerminal)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.RecordHandoff(gatewayresponseterminal.DispositionProtocolValidatedSuccess, gatewayresponseterminal.WriterActionProtocolSuccess, true); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(lifecycle.operations, ","); got != "start,observe" {
		t.Fatalf("lifecycle after record=%s", got)
	}
	if err := adapter.CompleteResponse(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(lifecycle.operations, ","); got != "start,observe,success" {
		t.Fatalf("lifecycle after complete=%s", got)
	}
}

func TestRunDefersCommittedFailureLifecycleTerminalToResponseHandoff(t *testing.T) {
	sink := gatewaystreamrelay.SinkState{TransportCommitted: true, SemanticCommitted: true, DownstreamBytes: 11}
	response := &gatewayresponse.Result{State: gatewayresponse.StateFailedAfterCommit, TransportCommitted: true, SemanticCommitted: true}
	executor := &executorStub{results: []AttemptResult{{Committed: true, Sink: &sink, Response: response}}}
	lifecycle := &lifecycleStub{}
	service := newTestService(t, executor, nil, Config{MaxAttempts: 1, WallTimeout: time.Minute})
	result, err := service.Run(Input{
		Context: context.Background(), MutationID: "deferred-failure", Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a")},
		Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText, Lifecycle: lifecycle, DeferResponseTerminal: true,
	})
	if err != nil || result.Outcome != OutcomeFailed || result.PendingResponseTerminal == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if got := strings.Join(lifecycle.operations, ","); got != "start" {
		t.Fatalf("lifecycle operations=%s", got)
	}
}

func TestRunRejectsDeferredTerminalWithoutTypedResponseFacts(t *testing.T) {
	sink := gatewaystreamrelay.SinkState{TransportCommitted: true, SemanticCommitted: true, DownstreamBytes: 11}
	executor := &executorStub{results: []AttemptResult{{Success: true, Committed: true, Sink: &sink}}}
	lifecycle := &lifecycleStub{}
	service := newTestService(t, executor, nil, Config{MaxAttempts: 1, WallTimeout: time.Minute})
	_, err := service.Run(Input{
		Context: context.Background(), MutationID: "deferred-missing-facts", Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a")},
		Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText, Lifecycle: lifecycle, DeferResponseTerminal: true,
	})
	if !errors.Is(err, ErrDeferredResponseTerminalFactsRequired) {
		t.Fatalf("run error=%v", err)
	}
	if got := strings.Join(lifecycle.operations, ","); got != "start,failure:gateway" {
		t.Fatalf("lifecycle operations=%s", got)
	}
}

func TestRunDeferredCommittedFailureDoesNotPreemptResponseTerminalOnContextRace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := gatewaystreamrelay.SinkState{TransportCommitted: true, SemanticCommitted: true, DownstreamBytes: 11}
	executor := &executorFunc{run: func(context.Context, Attempt) (AttemptResult, error) {
		cancel()
		return AttemptResult{
			Committed: true, Sink: &sink,
			Response: &gatewayresponse.Result{State: gatewayresponse.StateFailedAfterCommit, TransportCommitted: true, SemanticCommitted: true},
		}, nil
	}}
	lifecycle := &lifecycleStub{}
	service := newTestService(t, executor, nil, Config{MaxAttempts: 1, WallTimeout: time.Minute})
	result, err := service.Run(Input{
		Context: ctx, MutationID: "deferred-context-race", Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a")},
		Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText, Lifecycle: lifecycle, DeferResponseTerminal: true,
	})
	if err != nil || result.PendingResponseTerminal == nil || result.Outcome != OutcomeFailed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if got := strings.Join(lifecycle.operations, ","); got != "start" {
		t.Fatalf("lifecycle operations=%s", got)
	}
}

func TestRunFailsClosedWhenLifecycleCommittedResultHasNoSink(t *testing.T) {
	executor := &executorStub{results: []AttemptResult{{Success: true, Committed: true}}}
	lifecycle := &lifecycleStub{}
	service := newTestService(t, executor, nil, Config{MaxAttempts: 1, WallTimeout: time.Minute})
	_, err := service.Run(Input{
		Context: context.Background(), MutationID: "lifecycle-missing-sink", Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a")},
		Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText, Lifecycle: lifecycle,
	})
	if err == nil || !strings.Contains(err.Error(), "missing sink state") || strings.Join(lifecycle.operations, ",") != "start,failure:gateway" {
		t.Fatalf("err=%v operations=%v", err, lifecycle.operations)
	}
}

func TestRunClosesRequestLifecycleWhenCandidatesExhaustAfterRetry(t *testing.T) {
	executor := &executorStub{results: []AttemptResult{{RetryAllowed: true}}}
	lifecycle := &lifecycleStub{}
	service := newTestService(t, executor, nil, Config{MaxAttempts: 2, WallTimeout: time.Minute})
	result, err := service.Run(Input{
		Context: context.Background(), MutationID: "lifecycle-exhausted", Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a")},
		Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText, Lifecycle: lifecycle,
	})
	if err != nil || result.Outcome != OutcomeCandidatesExhausted || strings.Join(lifecycle.operations, ",") != "start,retry,failure:upstream" {
		t.Fatalf("result=%+v err=%v operations=%v", result, err, lifecycle.operations)
	}
}

func TestRunPreservesRequestLifecycleForExplicitGroupContinuation(t *testing.T) {
	executor := &executorStub{results: []AttemptResult{{RetryAllowed: true}}}
	lifecycle := &lifecycleStub{}
	service := newTestService(t, executor, nil, Config{MaxAttempts: 2, WallTimeout: time.Minute})
	result, err := service.Run(Input{
		Context: context.Background(), MutationID: "lifecycle-group-continuation", Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a")},
		Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText, Lifecycle: lifecycle,
		PreserveLifecycleOnCandidatesExhausted: true,
	})
	if err != nil || result.Outcome != OutcomeCandidatesExhausted || strings.Join(lifecycle.operations, ",") != "start,retry" {
		t.Fatalf("result=%+v err=%v operations=%v", result, err, lifecycle.operations)
	}
}

func TestRunReturnsOnlyExplicitFallbackAccountFactsWhenCandidatesExhaust(t *testing.T) {
	tests := []struct {
		name        string
		candidates  []gatewaycandidatewindow.Candidate
		results     []AttemptResult
		excluded    []string
		recoverable []string
		complete    bool
	}{
		{
			name:       "distinct explicit classifications",
			candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("excluded"), oauthCandidate("recoverable")},
			results: []AttemptResult{
				{RetryAllowed: true, FallbackDisposition: FallbackAccountExcluded},
				{RetryAllowed: true, FallbackDisposition: FallbackAccountRecoverable},
			},
			excluded: []string{"excluded"}, recoverable: []string{"recoverable"}, complete: true,
		},
		{
			name:       "latest account classification wins",
			candidates: []gatewaycandidatewindow.Candidate{apiKeyCandidate("account", []int{0, 1})},
			results: []AttemptResult{
				{RetryAllowed: true, KeyScopedFailure: true, FallbackDisposition: FallbackAccountRecoverable},
				{RetryAllowed: true, FallbackDisposition: FallbackAccountExcluded},
			},
			excluded: []string{"account"}, complete: true,
		},
		{
			name:       "unknown remains unusable",
			candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("unknown")},
			results:    []AttemptResult{{RetryAllowed: true}},
			complete:   false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &executorStub{results: test.results}
			service := newTestService(t, executor, nil, Config{MaxAttempts: 4, WallTimeout: time.Minute})
			result, err := service.Run(Input{
				Context: t.Context(), MutationID: "fallback-account-facts", Candidates: test.candidates,
				Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText,
			})
			if err != nil || result.Outcome != OutcomeCandidatesExhausted {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if result.FallbackAccounts.Complete != test.complete || !reflect.DeepEqual(result.FallbackAccounts.ExcludedAccountIDs, test.excluded) || !reflect.DeepEqual(result.FallbackAccounts.RecoverableAccountIDs, test.recoverable) {
				t.Fatalf("fallback facts=%+v", result.FallbackAccounts)
			}
		})
	}
}

func TestRunFallbackReasonRequiresCompleteFactsAndUsesFinalAttempt(t *testing.T) {
	t.Run("complete facts retain the final attempt reason", func(t *testing.T) {
		executor := &executorStub{results: []AttemptResult{
			{RetryAllowed: true, FallbackDisposition: FallbackAccountExcluded, FallbackReason: "upstream_accounts_exhausted"},
			{RetryAllowed: true, FallbackDisposition: FallbackAccountExcluded, FallbackReason: "account_scoped_agent_guidance_exhausted"},
		}}
		service := newTestService(t, executor, nil, Config{MaxAttempts: 4, WallTimeout: time.Minute})
		result, err := service.Run(Input{
			Context: t.Context(), MutationID: "fallback-reason", Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("first"), oauthCandidate("second")},
			Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText,
		})
		if err != nil || result.Outcome != OutcomeCandidatesExhausted || !result.FallbackAccounts.Complete || result.FallbackReason != "account_scoped_agent_guidance_exhausted" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})

	t.Run("incomplete facts cannot expose a reason", func(t *testing.T) {
		executor := &executorStub{results: []AttemptResult{{RetryAllowed: true, FallbackReason: "upstream_accounts_exhausted"}}}
		service := newTestService(t, executor, nil, Config{MaxAttempts: 4, WallTimeout: time.Minute})
		result, err := service.Run(Input{
			Context: t.Context(), MutationID: "fallback-reason-incomplete", Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("unknown")},
			Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText,
		})
		if err != nil || result.Outcome != OutcomeCandidatesExhausted || result.FallbackAccounts.Complete || result.FallbackReason != "" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
}

func TestRunFallbackAccountFactsRejectUnattemptedRoster(t *testing.T) {
	request := replaySafeRequest()
	tests := []struct {
		name       string
		candidates []gatewaycandidatewindow.Candidate
		tracker    *AttemptTracker
		results    []AttemptResult
	}{
		{name: "candidate without eligible key", candidates: []gatewaycandidatewindow.Candidate{apiKeyCandidate("no-key", nil)}},
		{name: "partially skipped candidate", candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("attempted"), apiKeyCandidate("no-key", nil)}, results: []AttemptResult{{RetryAllowed: true, FallbackDisposition: FallbackAccountExcluded, FallbackReason: "upstream_accounts_exhausted"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newTestService(t, &executorStub{results: test.results}, nil, Config{MaxAttempts: 4, WallTimeout: time.Minute})
			result, err := service.Run(Input{Context: t.Context(), MutationID: "unattempted-fallback-roster", Candidates: test.candidates, Request: request, FinalLane: gatewayingress.LaneText, Tracker: test.tracker})
			if err != nil || result.Outcome != OutcomeCandidatesExhausted || result.FallbackAccounts.Complete || result.FallbackReason != "" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestRunFallbackReasonNeverLeaksFromResultDiagnostics(t *testing.T) {
	t.Run("incomplete account facts", func(t *testing.T) {
		service := newTestService(t, &executorStub{results: []AttemptResult{{RetryAllowed: true, FallbackReason: "upstream_accounts_exhausted"}}}, nil, Config{MaxAttempts: 4, WallTimeout: time.Minute})
		result, err := service.Run(Input{Context: t.Context(), MutationID: "reason-incomplete", Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("account")}, Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText})
		if err != nil || result.FallbackReason != "" || result.LastAttempt == nil || result.LastAttempt.FallbackReason != "" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})

	for _, terminal := range []AttemptResult{{Success: true, Committed: true}, {Committed: true}} {
		service := newTestService(t, &executorStub{results: []AttemptResult{
			{RetryAllowed: true, KeyScopedFailure: true, FallbackDisposition: FallbackAccountExcluded, FallbackReason: "upstream_accounts_exhausted"}, terminal,
		}}, nil, Config{MaxAttempts: 4, WallTimeout: time.Minute})
		result, err := service.Run(Input{Context: t.Context(), MutationID: "reason-terminal", Candidates: []gatewaycandidatewindow.Candidate{apiKeyCandidate("account", []int{0, 1})}, Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText})
		if err != nil || result.FallbackReason != "" || result.LastAttempt == nil || result.LastAttempt.FallbackReason != "" {
			t.Fatalf("terminal=%+v result=%+v err=%v", terminal, result, err)
		}
	}
}

func TestRunRejectsInvalidOrTerminalFallbackReason(t *testing.T) {
	for _, result := range []AttemptResult{
		{RetryAllowed: true, FallbackReason: "not a node reason"},
		{RetryAllowed: true, FallbackReason: "unknown_node_reason"},
		{Success: true, Committed: true, FallbackReason: "upstream_accounts_exhausted"},
	} {
		executor := &executorStub{fallback: result}
		service := newTestService(t, executor, nil, Config{MaxAttempts: 4, WallTimeout: time.Minute})
		if _, err := service.Run(Input{Context: t.Context(), MutationID: "invalid-fallback-reason", Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("account")}, Request: replaySafeRequest(), FinalLane: gatewayingress.LaneText}); err == nil {
			t.Fatalf("result=%+v error=nil", result)
		}
	}
}

func TestNewServiceValidatesDependenciesAndBounds(t *testing.T) {
	if _, err := NewService(nil, nil, Config{}); err == nil {
		t.Fatal("missing executor error = nil")
	}
	executor := &executorStub{}
	for _, config := range []Config{
		{MaxAttempts: MaxAttempts + 1, WallTimeout: time.Second, FirstByteTimeout: time.Second},
		{WallTimeout: 0, FirstByteTimeout: time.Second},
		{WallTimeout: time.Second, DisableWallTimeout: true, FirstByteTimeout: time.Second},
		{WallTimeout: time.Second, FirstByteTimeout: -time.Nanosecond},
		{WallTimeout: time.Second, FirstByteTimeout: time.Hour + time.Nanosecond},
	} {
		if _, err := NewService(executor, nil, config); err == nil {
			t.Fatalf("config %+v error = nil", config)
		}
	}
}

type executorStub struct {
	results  []AttemptResult
	fallback AttemptResult
	attempts []Attempt
}

type executorFunc struct {
	run func(context.Context, Attempt) (AttemptResult, error)
}

func (f *executorFunc) Execute(ctx context.Context, attempt Attempt) (AttemptResult, error) {
	return f.run(ctx, attempt)
}

type lifecycleStub struct {
	operations []string
	sink       gatewaystreamrelay.SinkState
}

func (s *lifecycleStub) Start() error {
	s.operations = append(s.operations, "start")
	return nil
}

func (s *lifecycleStub) ObserveSink(value gatewaystreamrelay.SinkState) error {
	s.operations = append(s.operations, "observe")
	s.sink = value
	return nil
}

func (s *lifecycleStub) RetryPreCommit() error {
	s.operations = append(s.operations, "retry")
	return nil
}

func (s *lifecycleStub) FinishSuccess() error {
	s.operations = append(s.operations, "success")
	return nil
}

func (s *lifecycleStub) FinishFailure(kind string) error {
	s.operations = append(s.operations, "failure:"+kind)
	return nil
}

func (s *lifecycleStub) CancelClient() error {
	s.operations = append(s.operations, "cancel")
	return nil
}

func (s *executorStub) Execute(_ context.Context, attempt Attempt) (AttemptResult, error) {
	s.attempts = append(s.attempts, attempt)
	if len(s.results) == 0 {
		return s.fallback, nil
	}
	result := s.results[0]
	s.results = s.results[1:]
	return result, nil
}

type applierStub struct {
	mutations []PolicyMutation
	result    PolicyApplyResult
	err       error
}

func (s *applierStub) Apply(_ context.Context, mutation PolicyMutation) (PolicyApplyResult, error) {
	s.mutations = append(s.mutations, mutation)
	if s.err != nil {
		return PolicyApplyResult{}, s.err
	}
	if s.result.Status != "" {
		if s.result.TransitionID == "" {
			s.result.TransitionID = mutation.TransitionID
		}
		return s.result, nil
	}
	return PolicyApplyResult{Status: PolicyApplyApplied, TransitionID: mutation.TransitionID, TargetDispatchRevision: mutation.Target.ExpectedDispatchRevision + 1, OutboxEventID: "event-1"}, nil
}

func validPolicyApplyResult(status PolicyApplyStatus) PolicyApplyResult {
	result := PolicyApplyResult{Status: status}
	if status == PolicyApplyApplied || status == PolicyApplyIdempotent {
		result.TargetDispatchRevision = 2
		result.OutboxEventID = "event-1"
	}
	return result
}

func newTestService(t *testing.T, executor AttemptExecutor, applier PolicyApplier, config Config) *Service {
	t.Helper()
	service, err := NewService(executor, applier, config)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func apiKeyCandidate(id string, indices []int) gatewaycandidatewindow.Candidate {
	runtime := make([]gatewaycandidatewindow.APIKeyRuntime, 0, len(indices))
	for _, index := range indices {
		runtime = append(runtime, gatewaycandidatewindow.APIKeyRuntime{KeyIndex: index, KeyFingerprint: strings.Repeat(string(rune('a'+index)), 64), Status: "active"})
	}
	return gatewaycandidatewindow.Candidate{Projection: testProjection(id, "api_key"), APIKeyRuntime: runtime}
}

func oauthCandidate(id string) gatewaycandidatewindow.Candidate {
	return gatewaycandidatewindow.Candidate{Projection: testProjection(id, "oauth")}
}

func testProjection(id, accountType string) port.GatewayAccountCandidate {
	return port.GatewayAccountCandidate{
		AccountID: id, SystemAccountID: "system-1", GroupID: "group-1", Type: accountType,
		Status: "active", ConfigRevision: 1, DispatchRevision: 1,
	}
}

func replaySafeRequest() protocolgateway.RequestShape {
	return protocolgateway.RequestShape{Method: "POST", Path: "/v1/embeddings"}
}
