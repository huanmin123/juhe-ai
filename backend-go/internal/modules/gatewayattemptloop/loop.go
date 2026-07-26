package gatewayattemptloop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"juhe-ai/backend-go/internal/gatewayaudit"
	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewayusage"
	protocolgateway "juhe-ai/backend-go/internal/protocols/gateway"
	"juhe-ai/backend-go/internal/store/port"
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
	WallDeadline      time.Time
	FirstByteTimeout  time.Duration
	FirstByteDeadline time.Time
}

type Attempt struct {
	Index              int
	CandidateIndex     int
	Candidate          gatewaycandidatewindow.Candidate
	APIKeyIndex        int
	HasAlternativeKeys bool
	StartedAt          time.Time
	Budget             AttemptBudget
	PolicySettings     PolicySettings
	PolicyNow          time.Time
	ReplayAllowed      bool
	OnFirstByte        func(time.Time)
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

// AttemptObservation is the deliberately redacted value exposed to an
// observation sink. ModelBucket is a SHA-256-derived bucket, never the raw
// requested model; it and the remaining scope hints can build a future
// hot-quality scope without exposing Candidate or CredentialSet.
type AttemptObservation struct {
	ID              string
	AttemptIndex    int
	CandidateIndex  int
	APIKeyIndex     int
	AccountRuntime  string
	ProtocolProfile string
	RequestLane     string
	ModelBucket     string
	StartedAt       time.Time
}

type AttemptTerminalObservation struct {
	Valid              bool
	Success            bool
	Committed          bool
	RetryAllowed       bool
	StatusCode         int
	ErrorCode          string
	FailureAttribution gatewayusage.FailureAttribution
	CompletedAt        time.Time
}

// AttemptObserver is an opt-in, best-effort observation seam. It receives one
// Start and one Terminal per actual executor call; FirstByte is emitted only
// after a downstream write makes a transport byte visible. It receives only
// redacted facts and owns no retry or policy decision.
type AttemptObserver interface {
	Start(context.Context, AttemptObservation)
	FirstByte(context.Context, AttemptObservation, time.Time)
	Terminal(context.Context, AttemptObservation, AttemptTerminalObservation)
}

type PolicyMutation struct {
	TransitionID string
	Target       port.GatewayAccountPolicyTarget
	Source       port.GatewayAccountPolicyRevisionFence
	Decision     PolicyDecision
	Reason       string
	TraceID      string
	AppliedAt    time.Time
}

type PolicyApplyStatus = port.GatewayAccountPolicyWriteStatus

const (
	PolicyApplyApplied     = port.GatewayAccountPolicyWriteApplied
	PolicyApplyIdempotent  = port.GatewayAccountPolicyWriteIdempotent
	PolicyApplyStaleTarget = port.GatewayAccountPolicyWriteStaleTarget
	PolicyApplyStaleSource = port.GatewayAccountPolicyWriteStaleSource
	PolicyApplyIneligible  = port.GatewayAccountPolicyWriteIneligible
)

type PolicyApplyResult = port.GatewayAccountPolicyWriteResult

type PolicyApplier interface {
	Apply(context.Context, PolicyMutation) (PolicyApplyResult, error)
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
	PolicyApply    *PolicyApplyResult
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
	Context    context.Context
	MutationID string
	TraceID    string
	Candidates []gatewaycandidatewindow.Candidate
	Request    protocolgateway.RequestShape
	Profile    *protocolgateway.Profile
	Tracker    *AttemptTracker
	Observer   AttemptObserver
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
	mutationID, err := stableInputID(input.MutationID, 256)
	if err != nil {
		return Result{}, fmt.Errorf("gateway attempt mutation ID: %w", err)
	}
	traceID, err := optionalStableInputID(input.TraceID, 200)
	if err != nil {
		return Result{}, fmt.Errorf("gateway attempt trace ID: %w", err)
	}
	if len(input.Candidates) > gatewaycandidatewindow.FinalLimit {
		return Result{}, fmt.Errorf("gateway attempt candidates exceed limit: %d", gatewaycandidatewindow.FinalLimit)
	}
	replayPolicy := protocolgateway.ClassifyReplay(input.Request, input.Profile)
	startedAt := s.now().UTC()
	deadline := startedAt.Add(s.config.WallTimeout)
	if current, ok := input.Context.Deadline(); ok && current.Before(deadline) {
		deadline = current
	}
	ctx, cancel := context.WithDeadline(input.Context, deadline)
	defer cancel()
	result := Result{StartedAt: startedAt, WallDeadline: deadline, Attempts: make([]AttemptSummary, 0, min(s.config.MaxAttempts, len(input.Candidates)))}
	tracker := input.Tracker
	if tracker == nil {
		tracker = NewAttemptTracker()
	}
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
		claimedCandidate := false
		for keyOffset, keyIndex := range keyIndices {
			if !tracker.CanClaim(candidate, keyIndex, input.Request) {
				continue
			}
			if attemptIndex >= s.config.MaxAttempts {
				return s.finish(result, OutcomeMaxAttempts, nil, nil), nil
			}
			if err := ctx.Err(); err != nil {
				return s.finish(result, contextOutcome(err), nil, err), nil
			}
			if !tracker.Claim(candidate, keyIndex, input.Request) {
				continue
			}
			if !claimedCandidate {
				if candidateAttempts >= MaxCandidateAttemptsPerRequest {
					return s.finish(result, OutcomeMaxAttempts, result.LastAttempt, nil), nil
				}
				candidateAttempts++
				claimedCandidate = true
			}
			attemptStartedAt := s.now()
			firstByteDeadline := attemptStartedAt.Add(s.config.FirstByteTimeout)
			if deadline.Before(firstByteDeadline) {
				firstByteDeadline = deadline
			}
			attempt := Attempt{
				Index: attemptIndex, CandidateIndex: candidateIndex, Candidate: candidate,
				APIKeyIndex: keyIndex, HasAlternativeKeys: hasClaimableKey(tracker, candidate, keyIndices[keyOffset+1:], input.Request),
				StartedAt:      attemptStartedAt,
				Budget:         AttemptBudget{WallDeadline: deadline, FirstByteTimeout: s.config.FirstByteTimeout, FirstByteDeadline: firstByteDeadline},
				PolicySettings: s.config.PolicySettings, PolicyNow: s.now(), ReplayAllowed: replayPolicy.Allowed,
			}
			var observation AttemptObservation
			if input.Observer != nil {
				observation = newAttemptObservation(attempt, input.Request)
				var firstByteOnce sync.Once
				attempt.OnFirstByte = func(observedAt time.Time) {
					firstByteOnce.Do(func() { observeFirstByte(input.Observer, ctx, observation, observedAt) })
				}
				observeStart(input.Observer, ctx, observation)
			}
			attemptResult, attemptErr := s.executor.Execute(ctx, attempt)
			if validationErr := validateAttemptResult(attemptResult, attemptErr); validationErr != nil {
				if input.Observer != nil {
					observeTerminal(input.Observer, ctx, observation, invalidTerminalObservation())
				}
				return Result{}, validationErr
			}
			if input.Observer != nil {
				observeTerminal(input.Observer, ctx, observation, terminalObservation(attemptResult))
			}
			if !replayPolicy.Allowed {
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
				return s.finish(result, OutcomeFailed, &attemptResult, policyErr), policyErr
			}
			decision, policyErr = normalizePolicyDecision(decision, s.now())
			if policyErr != nil {
				policyErr = fmt.Errorf("invalid gateway account policy decision: %w", policyErr)
				return s.finish(result, OutcomeFailed, &attemptResult, policyErr), policyErr
			}
			result.Attempts[len(result.Attempts)-1].PolicyAction = decision.Action
			if decision.Action == PolicyActionCooldown || decision.Action == PolicyActionDisable {
				if s.applier == nil {
					policyErr = fmt.Errorf("gateway account policy applier is required for %s", decision.Action)
					return s.finish(result, OutcomeFailed, &attemptResult, policyErr), policyErr
				}
				mutation, mutationErr := newPolicyMutation(mutationID, traceID, attempt.Index, candidate, decision, attemptResult.Failure, s.now())
				if mutationErr != nil {
					mutationErr = fmt.Errorf("build gateway account policy mutation: %w", mutationErr)
					return s.finish(result, OutcomeFailed, &attemptResult, mutationErr), mutationErr
				}
				applyResult, applyErr := s.applier.Apply(ctx, mutation)
				if applyErr != nil {
					applyErr = fmt.Errorf("apply gateway account policy: %w", applyErr)
					return s.finish(result, OutcomeFailed, &attemptResult, applyErr), applyErr
				}
				if applyErr = validatePolicyApplyResult(applyResult, mutation.TransitionID); applyErr != nil {
					applyErr = fmt.Errorf("apply gateway account policy result: %w", applyErr)
					return s.finish(result, OutcomeFailed, &attemptResult, applyErr), applyErr
				}
				result.Attempts[len(result.Attempts)-1].PolicyApply = clonePolicyApplyResult(applyResult)
			}
			if decision.Action == PolicyActionRetryNext || decision.Action == PolicyActionCooldown || decision.Action == PolicyActionDisable {
				if !replayPolicy.Allowed {
					return s.finish(result, OutcomeFailed, &attemptResult, attemptErr), nil
				}
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

func observeStart(observer AttemptObserver, ctx context.Context, observation AttemptObservation) {
	defer func() { _ = recover() }()
	observer.Start(ctx, observation)
}

func observeFirstByte(observer AttemptObserver, ctx context.Context, observation AttemptObservation, observedAt time.Time) {
	defer func() { _ = recover() }()
	observer.FirstByte(ctx, observation, observedAt)
}

func observeTerminal(observer AttemptObserver, ctx context.Context, observation AttemptObservation, terminal AttemptTerminalObservation) {
	defer func() { _ = recover() }()
	observer.Terminal(ctx, observation, terminal)
}

func newAttemptObservation(attempt Attempt, request protocolgateway.RequestShape) AttemptObservation {
	return AttemptObservation{
		ID:              "hotq:" + uuid.NewString(),
		AttemptIndex:    attempt.Index,
		CandidateIndex:  attempt.CandidateIndex,
		APIKeyIndex:     attempt.APIKeyIndex,
		AccountRuntime:  runtimeKey(attempt.Candidate),
		ProtocolProfile: protocolProfile(attempt.Candidate),
		RequestLane:     requestLane(request),
		ModelBucket:     modelBucket(request.Model),
		StartedAt:       attempt.StartedAt.UTC(),
	}
}

func terminalObservation(result AttemptResult) AttemptTerminalObservation {
	completedAt := result.Usage.CompletedAt.UTC()
	return AttemptTerminalObservation{
		Valid:   true,
		Success: result.Success, Committed: result.Committed, RetryAllowed: result.RetryAllowed,
		StatusCode: result.Failure.StatusCode, ErrorCode: boundedText(result.Failure.ErrorCode, 256),
		FailureAttribution: result.Usage.FailureAttribution, CompletedAt: completedAt,
	}
}

func invalidTerminalObservation() AttemptTerminalObservation {
	return AttemptTerminalObservation{ErrorCode: "invalid_attempt_result"}
}

func protocolProfile(candidate gatewaycandidatewindow.Candidate) string {
	projection := candidate.Projection
	for _, value := range []string{projection.ResourceProviderProtocolProfileID, projection.ProviderProtocolProfileID} {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	code, version := strings.TrimSpace(projection.ResourceProtocolCode), strings.TrimSpace(projection.ResourceProtocolVersion)
	if code == "" {
		code, version = strings.TrimSpace(projection.ProtocolCode), strings.TrimSpace(projection.ProtocolVersion)
	}
	if code == "" {
		return "unknown"
	}
	if version == "" {
		return code
	}
	return code + ":" + version
}

func requestLane(request protocolgateway.RequestShape) string {
	if request.ImageGenerationHint {
		return "image"
	}
	return "text"
}

func modelBucket(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "unknown"
	}
	sum := sha256.Sum256([]byte(model))
	return "model-bucket-" + hex.EncodeToString(sum[:1])
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

func hasClaimableKey(tracker *AttemptTracker, candidate gatewaycandidatewindow.Candidate, indices []int, request protocolgateway.RequestShape) bool {
	for _, index := range indices {
		if tracker.CanClaim(candidate, index, request) {
			return true
		}
	}
	return false
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
	if candidate.Projection.ResourceAccountID != "" && strings.TrimSpace(candidate.Projection.ResourceType) != "" {
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
