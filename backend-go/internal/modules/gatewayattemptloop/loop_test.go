package gatewayattemptloop

import (
	"context"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	protocolgateway "juhe-ai/backend-go/internal/protocols/gateway"
	"juhe-ai/backend-go/internal/store/port"
)

func TestRunRetriesNextAPIKeyBeforeNextAccount(t *testing.T) {
	executor := &executorStub{results: []AttemptResult{
		{RetryAllowed: true, KeyScopedFailure: true, Failure: FailureFacts{StatusCode: 429}},
		{Success: true, Committed: true},
	}}
	service := newTestService(t, executor, nil, Config{MaxAttempts: 4, WallTimeout: time.Minute, FirstByteTimeout: 10 * time.Second})
	result, err := service.Run(Input{Context: context.Background(), Candidates: []gatewaycandidatewindow.Candidate{apiKeyCandidate("a", []int{1, 3}), apiKeyCandidate("b", []int{0})}, Request: replaySafeRequest()})
	if err != nil || result.Outcome != OutcomeSucceeded || len(executor.attempts) != 2 {
		t.Fatalf("result = %+v err=%v attempts=%+v", result, err, executor.attempts)
	}
	if executor.attempts[0].CandidateIndex != 0 || executor.attempts[0].APIKeyIndex != 1 || executor.attempts[1].CandidateIndex != 0 || executor.attempts[1].APIKeyIndex != 3 {
		t.Fatalf("attempts = %+v", executor.attempts)
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
	result, err := service.Run(Input{Context: context.Background(), Candidates: []gatewaycandidatewindow.Candidate{first, oauthCandidate("b")}, Request: replaySafeRequest()})
	if err != nil || result.Outcome != OutcomeSucceeded || len(executor.attempts) != 2 || executor.attempts[1].CandidateIndex != 1 {
		t.Fatalf("result = %+v err=%v attempts=%+v", result, err, executor.attempts)
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
	result, err := service.Run(Input{Context: context.Background(), Candidates: []gatewaycandidatewindow.Candidate{first, oauthCandidate("b")}, Request: replaySafeRequest()})
	if err != nil || result.Outcome != OutcomeSucceeded || len(applier.mutations) != 1 || applier.mutations[0].Decision.Action != PolicyActionCooldown {
		t.Fatalf("result = %+v err=%v mutations=%+v", result, err, applier.mutations)
	}
}

func TestRunStopsAfterCommitAndOnNonRetryableFailure(t *testing.T) {
	for _, attemptResult := range []AttemptResult{
		{Committed: true, RetryAllowed: false, Failure: FailureFacts{StatusCode: 500}},
		{RetryAllowed: false, Failure: FailureFacts{StatusCode: 500}},
	} {
		executor := &executorStub{results: []AttemptResult{attemptResult}}
		service := newTestService(t, executor, nil, Config{MaxAttempts: 3, WallTimeout: time.Minute, FirstByteTimeout: time.Second})
		result, err := service.Run(Input{Context: context.Background(), Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a"), oauthCandidate("b")}, Request: replaySafeRequest()})
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
	result, err := service.Run(Input{Context: context.Background(), Candidates: []gatewaycandidatewindow.Candidate{candidate}, Request: replaySafeRequest()})
	if err != nil || result.Outcome != OutcomeMaxAttempts || len(executor.attempts) != 1 || executor.attempts[0].APIKeyIndex != 1 {
		t.Fatalf("result = %+v err=%v attempts=%+v", result, err, executor.attempts)
	}
}

func TestRunCapsAPIKeyAttemptsPerCandidate(t *testing.T) {
	executor := &executorStub{fallback: AttemptResult{RetryAllowed: true, KeyScopedFailure: true}}
	service := newTestService(t, executor, nil, Config{MaxAttempts: 8, WallTimeout: time.Minute, FirstByteTimeout: time.Second})
	result, err := service.Run(Input{Context: context.Background(), Candidates: []gatewaycandidatewindow.Candidate{apiKeyCandidate("a", []int{0, 1, 2, 3}), oauthCandidate("b")}, Request: replaySafeRequest()})
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
	result, err := service.Run(Input{Context: context.Background(), Candidates: candidates, Request: replaySafeRequest()})
	if err != nil || result.Outcome != OutcomeMaxAttempts || len(executor.attempts) != MaxCandidateAttemptsPerRequest {
		t.Fatalf("result = %+v err=%v attempts=%+v", result, err, executor.attempts)
	}
}

func TestRunUnsafePolicyMutationDoesNotReplayNextCandidate(t *testing.T) {
	executor := &executorStub{fallback: AttemptResult{RetryAllowed: true, Failure: FailureFacts{StatusCode: 429, ErrorCode: "rate_limit"}}}
	applier := &applierStub{}
	first := oauthCandidate("a")
	first.Credentials = gatewaycandidatewindow.NewCredentialSet(map[string]any{"error_handling_rules": []any{rule(map[string]any{"action": "rate_limited", "error_codes": []any{"rate_limit"}, "reset_strategy": "duration", "duration_hours": float64(1)})}})
	service := newTestService(t, executor, applier, Config{MaxAttempts: 4, WallTimeout: time.Minute, FirstByteTimeout: time.Second})
	result, err := service.Run(Input{Context: context.Background(), Candidates: []gatewaycandidatewindow.Candidate{first, oauthCandidate("b")}, Request: protocolgateway.RequestShape{Method: "POST", Path: "/v1beta/interactions"}})
	if err != nil || result.Outcome != OutcomeFailed || len(executor.attempts) != 1 || len(applier.mutations) != 1 {
		t.Fatalf("result = %+v err=%v attempts=%+v mutations=%+v", result, err, executor.attempts, applier.mutations)
	}
}

func TestRunDoesNotReplayUnlessRequestIsClassifiedSafe(t *testing.T) {
	executor := &executorStub{fallback: AttemptResult{RetryAllowed: true}}
	service := newTestService(t, executor, nil, Config{MaxAttempts: 4, WallTimeout: time.Minute, FirstByteTimeout: time.Second})
	result, err := service.Run(Input{Context: context.Background(), Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a"), oauthCandidate("b")}})
	if err != nil || result.Outcome != OutcomeFailed || len(executor.attempts) != 1 || result.LastAttempt == nil || result.LastAttempt.RetryAllowed {
		t.Fatalf("result = %+v err=%v attempts=%+v", result, err, executor.attempts)
	}
}

func TestRunPropagatesBudgetsAndContextTerminalStates(t *testing.T) {
	now := time.Now().UTC()
	executor := &executorStub{results: []AttemptResult{{Success: true, Committed: true}}}
	service := newTestService(t, executor, nil, Config{WallTimeout: 30 * time.Second, FirstByteTimeout: 7 * time.Second}).WithNow(func() time.Time { return now })
	result, err := service.Run(Input{Context: context.Background(), Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a")}})
	if err != nil || result.Outcome != OutcomeSucceeded || !executor.attempts[0].Budget.WallDeadline.Equal(now.Add(30*time.Second)) || executor.attempts[0].Budget.FirstByteTimeout != 7*time.Second || !executor.attempts[0].Budget.FirstByteDeadline.Equal(now.Add(7*time.Second)) {
		t.Fatalf("result = %+v err=%v attempt=%+v", result, err, executor.attempts[0])
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = service.Run(Input{Context: canceled, Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a")}})
	if err != nil || result.Outcome != OutcomeCanceled || !errors.Is(result.TerminalError, context.Canceled) {
		t.Fatalf("canceled result = %+v err=%v", result, err)
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
		{WallTimeout: time.Second, FirstByteTimeout: 0},
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

func (s *executorStub) Execute(_ context.Context, attempt Attempt) (AttemptResult, error) {
	s.attempts = append(s.attempts, attempt)
	if len(s.results) == 0 {
		return s.fallback, nil
	}
	result := s.results[0]
	s.results = s.results[1:]
	return result, nil
}

type applierStub struct{ mutations []PolicyMutation }

func (s *applierStub) Apply(_ context.Context, mutation PolicyMutation) error {
	s.mutations = append(s.mutations, mutation)
	return nil
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
		runtime = append(runtime, gatewaycandidatewindow.APIKeyRuntime{KeyIndex: index, Status: "active"})
	}
	return gatewaycandidatewindow.Candidate{Projection: port.GatewayAccountCandidate{AccountID: id, Type: "api_key"}, APIKeyRuntime: runtime}
}

func oauthCandidate(id string) gatewaycandidatewindow.Candidate {
	return gatewaycandidatewindow.Candidate{Projection: port.GatewayAccountCandidate{AccountID: id, Type: "oauth"}}
}

func replaySafeRequest() protocolgateway.RequestShape {
	return protocolgateway.RequestShape{Method: "POST", Path: "/v1/embeddings"}
}
