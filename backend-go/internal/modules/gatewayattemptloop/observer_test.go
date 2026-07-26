package gatewayattemptloop

import (
	"context"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
)

func TestRunObserverSeesEachActualAttemptAndOnlyOneFirstByte(t *testing.T) {
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	executor := &observingExecutor{results: []AttemptResult{{RetryAllowed: true}, {Success: true, Committed: true}}, now: now}
	service := newTestService(t, executor, nil, Config{MaxAttempts: 4, WallTimeout: time.Minute, FirstByteTimeout: time.Second}).WithNow(func() time.Time { return now })
	observer := &observerStub{}
	result, err := service.Run(Input{Context: context.Background(), MutationID: "request-1", Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a"), oauthCandidate("b")}, Request: replaySafeRequest(), Observer: observer})
	if err != nil || result.Outcome != OutcomeSucceeded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(observer.started) != 2 || len(observer.firstBytes) != 2 || len(observer.terminals) != 2 {
		t.Fatalf("observer start=%d first=%d terminal=%d", len(observer.started), len(observer.firstBytes), len(observer.terminals))
	}
	for index, attempt := range observer.started {
		if attempt.AttemptIndex != index || !attempt.StartedAt.Equal(now) || observer.firstBytes[index].AttemptIndex != index || observer.terminals[index].attempt.AttemptIndex != index || attempt.ID == "" || attempt.ModelBucket == "" || attempt.AccountRuntime == "" {
			t.Fatalf("observation[%d] start=%+v first=%+v terminal=%+v", index, attempt, observer.firstBytes[index], observer.terminals[index])
		}
	}
}

func TestRunObserverNeutralizesInvalidExecutorResult(t *testing.T) {
	executor := &executorStub{fallback: AttemptResult{Success: true}}
	service := newTestService(t, executor, nil, Config{WallTimeout: time.Minute, FirstByteTimeout: time.Second})
	observer := &observerStub{}
	if _, err := service.Run(Input{Context: context.Background(), MutationID: "request-1", Candidates: []gatewaycandidatewindow.Candidate{oauthCandidate("a")}, Request: replaySafeRequest(), Observer: observer}); err == nil {
		t.Fatal("invalid executor result was accepted")
	}
	if len(observer.terminals) != 1 || observer.terminals[0].result.Valid || observer.terminals[0].result.Success || observer.terminals[0].result.ErrorCode != "invalid_attempt_result" {
		t.Fatalf("terminal observations = %#v", observer.terminals)
	}
}

type observingExecutor struct {
	results []AttemptResult
	now     time.Time
}

func (e *observingExecutor) Execute(_ context.Context, attempt Attempt) (AttemptResult, error) {
	if attempt.OnFirstByte != nil {
		attempt.OnFirstByte(e.now.Add(10 * time.Millisecond))
		attempt.OnFirstByte(e.now.Add(20 * time.Millisecond))
	}
	result := e.results[0]
	e.results = e.results[1:]
	return result, nil
}

type terminalObserverEvent struct {
	attempt AttemptObservation
	result  AttemptTerminalObservation
}

type observerStub struct {
	started    []AttemptObservation
	firstBytes []AttemptObservation
	terminals  []terminalObserverEvent
}

func (s *observerStub) Start(_ context.Context, attempt AttemptObservation) {
	s.started = append(s.started, attempt)
}

func (s *observerStub) FirstByte(_ context.Context, attempt AttemptObservation, _ time.Time) {
	s.firstBytes = append(s.firstBytes, attempt)
}

func (s *observerStub) Terminal(_ context.Context, attempt AttemptObservation, result AttemptTerminalObservation) {
	s.terminals = append(s.terminals, terminalObserverEvent{attempt: attempt, result: result})
}
