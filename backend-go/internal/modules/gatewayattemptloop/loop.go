package gatewayattemptloop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"juhe-ai/backend-go/internal/gatewayaudit"
	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewayusage"
)

const (
	DefaultMaxAttempts             = 16
	MaxAttempts                    = 512
	MaxAPIKeyAttemptsPerCandidate  = 2
	MaxCandidateAttemptsPerRequest = 4
)

type Outcome string

const (
	OutcomeSucceeded           Outcome = "succeeded"
	OutcomeFailed              Outcome = "failed"
	OutcomeCanceled            Outcome = "canceled"
	OutcomeDeadlineExceeded    Outcome = "deadline_exceeded"
	OutcomeCandidatesExhausted Outcome = "candidates_exhausted"
	OutcomeMaxAttempts         Outcome = "max_attempts"
)

type AttemptBudget struct {
	WallDeadline     time.Time
	FirstByteTimeout time.Duration
}

type Attempt struct {
	Index              int
	CandidateIndex     int
	Candidate          gatewaycandidatewindow.Candidate
	APIKeyIndex        int
	HasAlternativeKeys bool
	Budget             AttemptBudget
	PolicySettings     PolicySettings
	PolicyNow          time.Time
	ReplayAllowed      bool
}

type AttemptResult struct {
	Success          bool
	Committed        bool
	RetryAllowed     bool
	KeyScopedFailure bool
	Failure          FailureFacts
	Usage            gatewayusage.TerminalFacts
	Audit            gatewayaudit.TerminalInput
	PolicyDecision   *PolicyDecision
}

type AttemptExecutor interface {
	Execute(context.Context, Attempt) (AttemptResult, error)
}

type PolicyMutation struct {
	Candidate gatewaycandidatewindow.Candidate
	Decision  PolicyDecision
	Failure   FailureFacts
}

type PolicyApplier interface {
	Apply(context.Context, PolicyMutation) error
}

type AttemptSummary struct {
	Index          int
	CandidateIndex int
	AccountID      string
	APIKeyIndex    int
	Success        bool
	Committed      bool
	RetryAllowed   bool
	PolicyAction   PolicyAction
	StatusCode     int
	ErrorCode      string
	Usage          gatewayusage.TerminalFacts
	Audit          gatewayaudit.TerminalInput
}

type Config struct {
	MaxAttempts      int
	WallTimeout      time.Duration
	FirstByteTimeout time.Duration
	PolicySettings   PolicySettings
}

type Input struct {
	Context       context.Context
	Candidates    []gatewaycandidatewindow.Candidate
	ReplayAllowed bool
}

type Result struct {
	Outcome       Outcome
	Attempts      []AttemptSummary
	Selected      *gatewaycandidatewindow.Candidate
	LastAttempt   *AttemptResult
	TerminalError error
	StartedAt     time.Time
	CompletedAt   time.Time
	WallDeadline  time.Time
}

type Service struct {
	executor AttemptExecutor
	applier  PolicyApplier
	config   Config
	now      func() time.Time
}

func NewService(executor AttemptExecutor, applier PolicyApplier, config Config) (*Service, error) {
	if executor == nil {
		return nil, fmt.Errorf("gateway attempt executor is required")
	}
	if config.MaxAttempts == 0 {
		config.MaxAttempts = DefaultMaxAttempts
	}
	if config.MaxAttempts < 1 || config.MaxAttempts > MaxAttempts {
		return nil, fmt.Errorf("gateway max attempts must be between 1 and %d", MaxAttempts)
	}
	if config.WallTimeout <= 0 || config.WallTimeout > 24*time.Hour {
		return nil, fmt.Errorf("gateway wall timeout must be between 1ns and 24h")
	}
	if config.FirstByteTimeout <= 0 || config.FirstByteTimeout > time.Hour {
		return nil, fmt.Errorf("gateway first-byte timeout must be between 1ns and 1h")
	}
	return &Service{executor: executor, applier: applier, config: config, now: time.Now}, nil
}

func (s *Service) WithNow(now func() time.Time) *Service {
	if now != nil {
		s.now = now
	}
	return s
}

func (s *Service) Run(input Input) (Result, error) {
	if input.Context == nil {
		return Result{}, fmt.Errorf("gateway attempt context is required")
	}
	if len(input.Candidates) > gatewaycandidatewindow.FinalLimit {
		return Result{}, fmt.Errorf("gateway attempt candidates exceed limit: %d", gatewaycandidatewindow.FinalLimit)
	}
	startedAt := s.now().UTC()
	deadline := startedAt.Add(s.config.WallTimeout)
	if current, ok := input.Context.Deadline(); ok && current.Before(deadline) {
		deadline = current
	}
	ctx, cancel := context.WithTimeout(input.Context, s.config.WallTimeout)
	defer cancel()
	result := Result{StartedAt: startedAt, WallDeadline: deadline, Attempts: make([]AttemptSummary, 0, min(s.config.MaxAttempts, len(input.Candidates)))}
	if err := ctx.Err(); err != nil {
		return s.finish(result, contextOutcome(err), nil, err), nil
	}

	attemptIndex := 0
	candidateAttempts := 0
	for candidateIndex, candidate := range input.Candidates {
		keyIndices := eligibleKeyIndices(candidate, startedAt)
		if len(keyIndices) == 0 {
			continue
		}
		if candidateAttempts >= MaxCandidateAttemptsPerRequest {
			return s.finish(result, OutcomeMaxAttempts, result.LastAttempt, nil), nil
		}
		candidateAttempts++
		for keyOffset, keyIndex := range keyIndices {
			if attemptIndex >= s.config.MaxAttempts {
				return s.finish(result, OutcomeMaxAttempts, nil, nil), nil
			}
			if err := ctx.Err(); err != nil {
				return s.finish(result, contextOutcome(err), nil, err), nil
			}
			attempt := Attempt{
				Index: attemptIndex, CandidateIndex: candidateIndex, Candidate: candidate,
				APIKeyIndex: keyIndex, HasAlternativeKeys: keyOffset+1 < len(keyIndices),
				Budget:         AttemptBudget{WallDeadline: deadline, FirstByteTimeout: s.config.FirstByteTimeout},
				PolicySettings: s.config.PolicySettings, PolicyNow: s.now(), ReplayAllowed: input.ReplayAllowed,
			}
			attemptResult, attemptErr := s.executor.Execute(ctx, attempt)
			if validationErr := validateAttemptResult(attemptResult, attemptErr); validationErr != nil {
				return Result{}, validationErr
			}
			if !input.ReplayAllowed {
				attemptResult.RetryAllowed = false
			}
			summary := AttemptSummary{
				Index: attemptIndex, CandidateIndex: candidateIndex, AccountID: candidate.Projection.AccountID,
				APIKeyIndex: keyIndex, Success: attemptResult.Success, Committed: attemptResult.Committed,
				RetryAllowed: attemptResult.RetryAllowed, StatusCode: attemptResult.Failure.StatusCode,
				ErrorCode: boundedText(attemptResult.Failure.ErrorCode, 256),
				Usage:     attemptResult.Usage, Audit: attemptResult.Audit,
			}
			attemptIndex++
			result.Attempts = append(result.Attempts, summary)
			result.LastAttempt = cloneAttemptResult(attemptResult)
			if attemptResult.Success {
				selected := candidate
				result.Selected = &selected
				return s.finish(result, OutcomeSucceeded, &attemptResult, nil), nil
			}
			if err := ctx.Err(); err != nil {
				return s.finish(result, contextOutcome(err), &attemptResult, err), nil
			}
			if attemptResult.Committed {
				return s.finish(result, OutcomeFailed, &attemptResult, attemptErr), nil
			}

			decision := PolicyDecision{Action: PolicyActionNone}
			var policyErr error
			if attemptResult.PolicyDecision != nil {
				decision = *attemptResult.PolicyDecision
			} else if attemptResult.Failure.StatusCode >= 100 {
				decision, policyErr = s.decide(candidate, attemptResult.Failure, s.now())
			}
			if policyErr != nil {
				return Result{}, policyErr
			}
			result.Attempts[len(result.Attempts)-1].PolicyAction = decision.Action
			if decision.Action == PolicyActionCooldown || decision.Action == PolicyActionDisable {
				if s.applier == nil {
					return Result{}, fmt.Errorf("gateway account policy applier is required for %s", decision.Action)
				}
				mutationFailure := attemptResult.Failure
				mutationFailure.BodyText = ""
				if err := s.applier.Apply(ctx, PolicyMutation{Candidate: candidate, Decision: decision, Failure: mutationFailure}); err != nil {
					return Result{}, fmt.Errorf("apply gateway account policy: %w", err)
				}
			}
			if decision.Action == PolicyActionRetryNext || decision.Action == PolicyActionCooldown || decision.Action == PolicyActionDisable {
				break
			}
			if attemptResult.KeyScopedFailure && attemptResult.RetryAllowed && keyOffset+1 < len(keyIndices) {
				continue
			}
			if attemptResult.RetryAllowed {
				break
			}
			return s.finish(result, OutcomeFailed, &attemptResult, attemptErr), nil
		}
	}
	if len(result.Attempts) >= s.config.MaxAttempts {
		return s.finish(result, OutcomeMaxAttempts, result.LastAttempt, nil), nil
	}
	return s.finish(result, OutcomeCandidatesExhausted, result.LastAttempt, nil), nil
}

func validateAttemptResult(result AttemptResult, err error) error {
	if result.Success && err != nil {
		return fmt.Errorf("successful gateway attempt returned an error")
	}
	if result.Success && result.RetryAllowed {
		return fmt.Errorf("successful gateway attempt cannot be retryable")
	}
	if result.Success && !result.Committed {
		return fmt.Errorf("successful gateway attempt must be committed")
	}
	if result.Committed && result.RetryAllowed {
		return fmt.Errorf("committed gateway attempt cannot be retryable")
	}
	if result.Failure.StatusCode != 0 && (result.Failure.StatusCode < 100 || result.Failure.StatusCode > 599) {
		return fmt.Errorf("gateway attempt status code is invalid")
	}
	return nil
}

func (s *Service) decide(candidate gatewaycandidatewindow.Candidate, failure FailureFacts, now time.Time) (PolicyDecision, error) {
	raw, _ := candidate.Credentials.Value("error_handling_rules")
	failure.BodyText = boundedText(failure.BodyText, 64<<10)
	failure.Message = boundedText(failure.Message, 1000)
	failure.ErrorCode = boundedText(failure.ErrorCode, 256)
	failure.ErrorType = boundedText(failure.ErrorType, 256)
	return DecidePolicy(raw, failure, s.config.PolicySettings, now)
}

func (s *Service) finish(result Result, outcome Outcome, last *AttemptResult, terminalErr error) Result {
	result.Outcome = outcome
	result.CompletedAt = s.now().UTC()
	result.TerminalError = terminalErr
	if last != nil {
		result.LastAttempt = cloneAttemptResult(*last)
	}
	return result
}

func eligibleKeyIndices(candidate gatewaycandidatewindow.Candidate, now time.Time) []int {
	if !strings.EqualFold(effectiveCandidateType(candidate), "api_key") {
		return []int{-1}
	}
	result := make([]int, 0, len(candidate.APIKeyRuntime))
	seen := make(map[int]struct{}, len(candidate.APIKeyRuntime))
	for _, state := range candidate.APIKeyRuntime {
		if _, duplicate := seen[state.KeyIndex]; duplicate {
			continue
		}
		if !keyRuntimeAvailable(state, now) {
			continue
		}
		seen[state.KeyIndex] = struct{}{}
		result = append(result, state.KeyIndex)
		if len(result) == MaxAPIKeyAttemptsPerCandidate {
			break
		}
	}
	return result
}

func keyRuntimeAvailable(state gatewaycandidatewindow.APIKeyRuntime, now time.Time) bool {
	if strings.EqualFold(strings.TrimSpace(state.Status), "active") || strings.TrimSpace(state.Status) == "" {
		return true
	}
	if strings.EqualFold(state.Status, "disabled") {
		return false
	}
	until, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(state.CooldownUntil))
	return err == nil && !until.After(now)
}

func effectiveCandidateType(candidate gatewaycandidatewindow.Candidate) string {
	if candidate.Projection.ResourceAccountID != "" {
		return candidate.Projection.ResourceType
	}
	return candidate.Projection.Type
}

func contextOutcome(err error) Outcome {
	if errors.Is(err, context.DeadlineExceeded) {
		return OutcomeDeadlineExceeded
	}
	return OutcomeCanceled
}

func boundedText(value string, limit int) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, ""))
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}

func cloneAttemptResult(value AttemptResult) *AttemptResult {
	copy := value
	if value.PolicyDecision != nil {
		decision := *value.PolicyDecision
		copy.PolicyDecision = &decision
	}
	copy.Failure.BodyText = boundedText(copy.Failure.BodyText, 64<<10)
	copy.Failure.Message = boundedText(copy.Failure.Message, 1000)
	return &copy
}
