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
	"juhe-ai/backend-go/internal/modules/gatewayfallbackreason"
	"juhe-ai/backend-go/internal/modules/gatewayingress"
	"juhe-ai/backend-go/internal/modules/gatewayresponse"
	"juhe-ai/backend-go/internal/modules/gatewayresponseinspection"
	"juhe-ai/backend-go/internal/modules/gatewayresponseterminal"
	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
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

var (
	ErrDeferredResponseTerminalLifecycleRequired = errors.New("deferred response terminal requires request lifecycle")
	ErrDeferredResponseTerminalFactsRequired     = errors.New("deferred response terminal requires typed response facts")
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
	Index          int
	CandidateIndex int
	Candidate      gatewaycandidatewindow.Candidate
	APIKeyIndex    int
	// Request facts are copied into each attempt so adapters that perform a
	// final candidate claim do not need to infer them from mutable candidates.
	RequestedModel     string
	EndpointFamily     string
	Lane               string
	HasAlternativeKeys bool
	StartedAt          time.Time
	Budget             AttemptBudget
	PolicySettings     PolicySettings
	PolicyNow          time.Time
	// AvailabilityFailoverAllowed permits an executor to return a pre-commit
	// availability failure as retryable. The executor's typed result and
	// Committed remain the actual retry boundary.
	AvailabilityFailoverAllowed bool
	OnFirstByte                 func(time.Time)
}

// FallbackAccountDisposition is the executor's account-local classification
// for a pre-commit attempt when an outer owner may later prepare another route
// group. It intentionally does not grant fallback: the outer owner still owns
// the reason, route cursor, target preparation, lifecycle, and lease transfer.
//
// Empty is unknown. An exhausted group containing an unknown classification is
// deliberately not usable for cross-group fallback, because Node excludes only
// accounts proved unavailable for the current request rather than every account
// that happened to be attempted.
type FallbackAccountDisposition string

const (
	FallbackAccountUnknown     FallbackAccountDisposition = ""
	FallbackAccountExcluded    FallbackAccountDisposition = "excluded"
	FallbackAccountRecoverable FallbackAccountDisposition = "recoverable"
)

type AttemptResult struct {
	Success   bool
	Committed bool
	// Sink is the executor's final actual downstream state. It is optional for
	// legacy unowned seams, but a request lifecycle requires it for every
	// committed or successful attempt so commit fences are never synthesized.
	Sink             *gatewaystreamrelay.SinkState
	RetryAllowed     bool
	KeyScopedFailure bool
	// FallbackDisposition classifies this candidate account only when the
	// attempt is a pre-commit failure. Executors must leave it unknown when
	// they cannot prove Node-equivalent account exhaustion semantics.
	FallbackDisposition FallbackAccountDisposition
	// FallbackReason is an opaque, Node-originated reason for a pre-commit
	// failure. It is intentionally not inferred from HTTP status or retry
	// state: a future outer route owner may consume it only with complete
	// FallbackAccounts after the current group is exhausted.
	FallbackReason string
	Failure        FailureFacts
	// Response is supplied by the HTTP executor only for the opt-in deferred
	// response-terminal path. It is reduced into a bounded Handoff before the
	// result leaves this package; ordinary attempt results do not expose body or
	// stream payloads through this field.
	Response       *gatewayresponse.Result
	Usage          gatewayusage.TerminalFacts
	Audit          gatewayaudit.TerminalInput
	PolicyDecision *PolicyDecision
	// ResponseInspection marks a pre-commit, policy-owned semantic failure. It
	// must never be reinterpreted as an error_handling_rules input.
	ResponseInspection *gatewayresponseinspection.Handoff
}

type AttemptExecutor interface {
	Execute(context.Context, Attempt) (AttemptResult, error)
}

// AttemptLifecycle is an optional request-local bridge. Its methods contain
// no candidate, API key, credential, routing, or HTTP facts: the attempt loop
// remains their sole owner. A concrete lifecycle adapter owns the opaque
// generation and maps these terminal names to its typed state machine.
type AttemptLifecycle interface {
	Start() error
	ObserveSink(gatewaystreamrelay.SinkState) error
	RetryPreCommit() error
	FinishSuccess() error
	FinishFailure(string) error
	CancelClient() error
}

const (
	lifecycleFailureUpstream       = "upstream"
	lifecycleFailureGateway        = "gateway"
	lifecycleFailureClientCanceled = "client_canceled"
)

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
	Index               int
	CandidateIndex      int
	AccountID           string
	APIKeyIndex         int
	Success             bool
	Committed           bool
	RetryAllowed        bool
	FallbackDisposition FallbackAccountDisposition
	PolicyAction        PolicyAction
	PolicyApply         *PolicyApplyResult
	StatusCode          int
	ErrorCode           string
	Usage               gatewayusage.TerminalFacts
	Audit               gatewayaudit.TerminalInput
	ResponseInspection  *gatewayresponseinspection.Handoff
}

// FallbackAccountFacts is meaningful only for OutcomeCandidatesExhausted. A
// future cross-group owner may consume ExcludedAccountIDs only when Complete is
// true. Recoverable accounts are retained as evidence but are never converted
// into exclusions by this package.
type FallbackAccountFacts struct {
	Complete              bool
	ExcludedAccountIDs    []string
	RecoverableAccountIDs []string
}

type Config struct {
	MaxAttempts int
	WallTimeout time.Duration
	// DisableWallTimeout disables only the attempt-loop-owned wall timer. A
	// caller context deadline/cancellation remains effective and WallTimeout
	// must be zero when this explicit mode is selected.
	DisableWallTimeout bool
	// FirstByteTimeout of zero disables the per-attempt first-byte deadline.
	FirstByteTimeout time.Duration
	PolicySettings   PolicySettings
}

type Input struct {
	Context    context.Context
	MutationID string
	TraceID    string
	Candidates []gatewaycandidatewindow.Candidate
	Request    protocolgateway.RequestShape
	// FinalLane is the immutable post-catalog, post-image-permission lane from
	// gatewayingress. The attempt loop must not reconstruct it from raw request
	// hints because mapping upgrades and permission downgrades are already
	// frozen before any account slot can be claimed.
	FinalLane gatewayingress.Lane
	// PreserveLifecycleOnCandidatesExhausted is only for an outer route-group
	// owner that has already proved another group may be prepared. It preserves
	// a pre-commit lifecycle after this batch exhausts so the next group can
	// start a fresh opaque generation. The default remains a request terminal.
	// It never applies to committed, non-retryable, canceled, or max-attempt
	// outcomes.
	PreserveLifecycleOnCandidatesExhausted bool
	// Lifecycle is optional until an outer request owner constructs the
	// authenticated execution lifecycle. When supplied, every executor call
	// gets a fresh opaque generation and committed results must carry Sink.
	Lifecycle AttemptLifecycle
	// DeferResponseTerminal prevents this loop from finalizing success or a
	// committed failure before the real HTTP listener reports response
	// completion. It applies only to outcomes with typed response facts; the
	// default false path preserves existing attempt-loop ownership.
	DeferResponseTerminal bool
	Profile               *protocolgateway.Profile
	Tracker               *AttemptTracker
	Observer              AttemptObserver
}

type Result struct {
	Outcome       Outcome
	Attempts      []AttemptSummary
	Selected      *gatewaycandidatewindow.Candidate
	LastAttempt   *AttemptResult
	TerminalError error
	// PendingResponseTerminal is set only by DeferResponseTerminal for a
	// successful or committed response. The listener owner must record explicit
	// disposition/writer facts and complete it after the actual response finish.
	PendingResponseTerminal *gatewayresponseterminal.Handoff
	StartedAt               time.Time
	CompletedAt             time.Time
	WallDeadline            time.Time
	// FallbackAccounts is derived solely from explicit executor facts when the
	// current group exhausts pre-commit candidates. It is otherwise empty and
	// must not be inferred by callers.
	FallbackAccounts FallbackAccountFacts
	// FallbackReason is the final pre-commit executor reason only when
	// FallbackAccounts is complete. Empty means the outer owner has no proven
	// Node-equivalent fallback reason and must not infer one.
	FallbackReason         string
	fallbackRoster         []string
	fallbackRosterComplete bool
	fallbackLastReason     string
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
	if config.DisableWallTimeout {
		if config.WallTimeout != 0 {
			return nil, fmt.Errorf("gateway disabled wall timeout must be zero")
		}
	} else if config.WallTimeout <= 0 || config.WallTimeout > 24*time.Hour {
		return nil, fmt.Errorf("gateway wall timeout must be between 1ns and 24h")
	}
	if config.FirstByteTimeout < 0 || config.FirstByteTimeout > time.Hour {
		return nil, fmt.Errorf("gateway first-byte timeout must be between 0 and 1h")
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
	if input.DeferResponseTerminal && input.Lifecycle == nil {
		return Result{}, ErrDeferredResponseTerminalLifecycleRequired
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
	startedAt := s.now().UTC()
	var deadline time.Time
	if !s.config.DisableWallTimeout {
		deadline = startedAt.Add(s.config.WallTimeout)
	}
	if current, ok := input.Context.Deadline(); ok && (deadline.IsZero() || current.Before(deadline)) {
		deadline = current
	}
	ctx, cancel := context.WithCancel(input.Context)
	if !deadline.IsZero() {
		ctx, cancel = context.WithDeadline(input.Context, deadline)
	}
	defer cancel()
	fallbackRoster, fallbackRosterComplete := fallbackAccountRoster(input.Candidates)
	result := Result{StartedAt: startedAt, WallDeadline: deadline, Attempts: make([]AttemptSummary, 0, min(s.config.MaxAttempts, len(input.Candidates))), fallbackRoster: fallbackRoster, fallbackRosterComplete: fallbackRosterComplete}
	tracker := input.Tracker
	if tracker == nil {
		tracker = NewAttemptTracker()
	}
	if err := ctx.Err(); err != nil {
		if lifecycleErr := finishContextAttemptLifecycle(input.Lifecycle, err); lifecycleErr != nil {
			return Result{}, fmt.Errorf("finish gateway attempt lifecycle context: %w", lifecycleErr)
		}
		return s.finish(result, contextOutcome(err), nil, err), nil
	}
	if !validFinalLane(input.FinalLane) {
		return Result{}, fmt.Errorf("gateway attempt final lane is required")
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
				if lifecycleErr := failAttemptLifecycle(input.Lifecycle, lifecycleFailureUpstream); lifecycleErr != nil {
					return Result{}, fmt.Errorf("finish gateway attempt lifecycle: %w", lifecycleErr)
				}
				return s.finish(result, OutcomeMaxAttempts, nil, nil), nil
			}
			if err := ctx.Err(); err != nil {
				if lifecycleErr := finishContextAttemptLifecycle(input.Lifecycle, err); lifecycleErr != nil {
					return Result{}, fmt.Errorf("finish gateway attempt lifecycle context: %w", lifecycleErr)
				}
				return s.finish(result, contextOutcome(err), nil, err), nil
			}
			if !tracker.Claim(candidate, keyIndex, input.Request) {
				continue
			}
			if !claimedCandidate {
				if candidateAttempts >= MaxCandidateAttemptsPerRequest {
					if lifecycleErr := failAttemptLifecycle(input.Lifecycle, lifecycleFailureUpstream); lifecycleErr != nil {
						return Result{}, fmt.Errorf("finish gateway attempt lifecycle: %w", lifecycleErr)
					}
					return s.finish(result, OutcomeMaxAttempts, result.LastAttempt, nil), nil
				}
				candidateAttempts++
				claimedCandidate = true
			}
			attemptStartedAt := s.now()
			var firstByteDeadline time.Time
			if s.config.FirstByteTimeout > 0 {
				firstByteDeadline = attemptStartedAt.Add(s.config.FirstByteTimeout)
				if !deadline.IsZero() && deadline.Before(firstByteDeadline) {
					firstByteDeadline = deadline
				}
			}
			attempt := Attempt{
				Index: attemptIndex, CandidateIndex: candidateIndex, Candidate: candidate,
				APIKeyIndex: keyIndex, RequestedModel: input.Request.Model,
				EndpointFamily:     string(protocolgateway.EndpointFamilyFromPath(protocolForCandidate(candidate), input.Request.Path)),
				Lane:               string(input.FinalLane),
				HasAlternativeKeys: hasClaimableKey(tracker, candidate, keyIndices[keyOffset+1:], input.Request),
				StartedAt:          attemptStartedAt,
				Budget:             AttemptBudget{WallDeadline: deadline, FirstByteTimeout: s.config.FirstByteTimeout, FirstByteDeadline: firstByteDeadline},
				PolicySettings:     s.config.PolicySettings, PolicyNow: s.now(), AvailabilityFailoverAllowed: true,
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
			if lifecycleErr := startAttemptLifecycle(input.Lifecycle); lifecycleErr != nil {
				return Result{}, fmt.Errorf("start gateway attempt lifecycle: %w", lifecycleErr)
			}
			attemptResult, attemptErr := s.executor.Execute(ctx, attempt)
			if validationErr := validateAttemptResult(attemptResult, attemptErr); validationErr != nil {
				if input.Observer != nil {
					observeTerminal(input.Observer, ctx, observation, invalidTerminalObservation())
				}
				if lifecycleErr := failAttemptLifecycle(input.Lifecycle, lifecycleFailureGateway); lifecycleErr != nil {
					return Result{}, fmt.Errorf("finish gateway attempt lifecycle: %w", lifecycleErr)
				}
				return Result{}, validationErr
			}
			pendingTerminal, pendingErr := deferredResponseTerminal(input, attemptResult)
			if pendingErr != nil {
				if input.Observer != nil {
					observeTerminal(input.Observer, ctx, observation, invalidTerminalObservation())
				}
				if lifecycleErr := failAttemptLifecycle(input.Lifecycle, lifecycleFailureGateway); lifecycleErr != nil {
					return Result{}, fmt.Errorf("prepare deferred response terminal: %w; finish lifecycle: %v", pendingErr, lifecycleErr)
				}
				return Result{}, fmt.Errorf("prepare deferred response terminal: %w", pendingErr)
			}
			if pendingTerminal == nil {
				if lifecycleErr := observeAttemptLifecycle(input.Lifecycle, attemptResult); lifecycleErr != nil {
					if terminalErr := failAttemptLifecycle(input.Lifecycle, lifecycleFailureGateway); terminalErr != nil {
						return Result{}, fmt.Errorf("observe gateway attempt lifecycle sink: %w; finish lifecycle: %v", lifecycleErr, terminalErr)
					}
					return Result{}, fmt.Errorf("observe gateway attempt lifecycle sink: %w", lifecycleErr)
				}
			}
			if input.Observer != nil {
				observeTerminal(input.Observer, ctx, observation, terminalObservation(attemptResult))
			}
			identity := gatewaycandidatewindow.EffectiveAccountIdentity(candidate)
			summary := AttemptSummary{
				Index: attemptIndex, CandidateIndex: candidateIndex, AccountID: identity.AccountID,
				APIKeyIndex: keyIndex, Success: attemptResult.Success, Committed: attemptResult.Committed,
				RetryAllowed: attemptResult.RetryAllowed, StatusCode: attemptResult.Failure.StatusCode,
				FallbackDisposition: attemptResult.FallbackDisposition,
				ErrorCode:           boundedText(attemptResult.Failure.ErrorCode, 256),
				PolicyAction:        PolicyActionNone,
				Usage:               attemptResult.Usage, Audit: attemptResult.Audit,
				ResponseInspection: gatewayresponseinspection.CloneHandoff(attemptResult.ResponseInspection),
			}
			attemptIndex++
			result.Attempts = append(result.Attempts, summary)
			result.LastAttempt = cloneAttemptResultForResult(attemptResult)
			result.fallbackLastReason = strings.TrimSpace(attemptResult.FallbackReason)
			if attemptResult.Success {
				if pendingTerminal != nil {
					result.PendingResponseTerminal = pendingTerminal
					selected := candidate
					result.Selected = &selected
					return s.finish(result, OutcomeSucceeded, &attemptResult, nil), nil
				}
				if lifecycleErr := succeedAttemptLifecycle(input.Lifecycle); lifecycleErr != nil {
					return Result{}, fmt.Errorf("finish successful gateway attempt lifecycle: %w", lifecycleErr)
				}
				selected := candidate
				result.Selected = &selected
				return s.finish(result, OutcomeSucceeded, &attemptResult, nil), nil
			}
			if attemptResult.Committed {
				if pendingTerminal != nil {
					result.PendingResponseTerminal = pendingTerminal
					return s.finish(result, OutcomeFailed, &attemptResult, attemptErr), nil
				}
				if err := ctx.Err(); err != nil {
					if lifecycleErr := finishContextAttemptLifecycle(input.Lifecycle, err); lifecycleErr != nil {
						return Result{}, fmt.Errorf("finish gateway attempt lifecycle context: %w", lifecycleErr)
					}
					return s.finish(result, contextOutcome(err), &attemptResult, err), nil
				}
				if lifecycleErr := failAttemptLifecycle(input.Lifecycle, failureKindForAttempt(attemptResult)); lifecycleErr != nil {
					return Result{}, fmt.Errorf("finish gateway attempt lifecycle: %w", lifecycleErr)
				}
				return s.finish(result, OutcomeFailed, &attemptResult, attemptErr), nil
			}
			if err := ctx.Err(); err != nil {
				if lifecycleErr := finishContextAttemptLifecycle(input.Lifecycle, err); lifecycleErr != nil {
					return Result{}, fmt.Errorf("finish gateway attempt lifecycle context: %w", lifecycleErr)
				}
				return s.finish(result, contextOutcome(err), &attemptResult, err), nil
			}
			if attemptResult.ResponseInspection != nil && attemptResult.ResponseInspection.Decision != nil {
				if attemptResult.RetryAllowed {
					if lifecycleErr := retryAttemptLifecycle(input.Lifecycle); lifecycleErr != nil {
						return Result{}, fmt.Errorf("retry gateway attempt lifecycle: %w", lifecycleErr)
					}
					break
				}
				if lifecycleErr := failAttemptLifecycle(input.Lifecycle, failureKindForAttempt(attemptResult)); lifecycleErr != nil {
					return Result{}, fmt.Errorf("finish gateway attempt lifecycle: %w", lifecycleErr)
				}
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
				if lifecycleErr := failAttemptLifecycle(input.Lifecycle, lifecycleFailureGateway); lifecycleErr != nil {
					return Result{}, fmt.Errorf("finish gateway attempt lifecycle: %w", lifecycleErr)
				}
				return s.finish(result, OutcomeFailed, &attemptResult, policyErr), policyErr
			}
			decision, policyErr = normalizePolicyDecision(decision, s.now())
			if policyErr != nil {
				policyErr = fmt.Errorf("invalid gateway account policy decision: %w", policyErr)
				if lifecycleErr := failAttemptLifecycle(input.Lifecycle, lifecycleFailureGateway); lifecycleErr != nil {
					return Result{}, fmt.Errorf("finish gateway attempt lifecycle: %w", lifecycleErr)
				}
				return s.finish(result, OutcomeFailed, &attemptResult, policyErr), policyErr
			}
			result.Attempts[len(result.Attempts)-1].PolicyAction = decision.Action
			if decision.Action == PolicyActionCooldown || decision.Action == PolicyActionDisable {
				if s.applier == nil {
					policyErr = fmt.Errorf("gateway account policy applier is required for %s", decision.Action)
					if lifecycleErr := failAttemptLifecycle(input.Lifecycle, lifecycleFailureGateway); lifecycleErr != nil {
						return Result{}, fmt.Errorf("finish gateway attempt lifecycle: %w", lifecycleErr)
					}
					return s.finish(result, OutcomeFailed, &attemptResult, policyErr), policyErr
				}
				mutation, mutationErr := newPolicyMutation(mutationID, traceID, attempt.Index, candidate, decision, attemptResult.Failure, s.now())
				if mutationErr != nil {
					mutationErr = fmt.Errorf("build gateway account policy mutation: %w", mutationErr)
					if lifecycleErr := failAttemptLifecycle(input.Lifecycle, lifecycleFailureGateway); lifecycleErr != nil {
						return Result{}, fmt.Errorf("finish gateway attempt lifecycle: %w", lifecycleErr)
					}
					return s.finish(result, OutcomeFailed, &attemptResult, mutationErr), mutationErr
				}
				applyResult, applyErr := s.applier.Apply(ctx, mutation)
				if applyErr != nil {
					applyErr = fmt.Errorf("apply gateway account policy: %w", applyErr)
					if lifecycleErr := failAttemptLifecycle(input.Lifecycle, lifecycleFailureGateway); lifecycleErr != nil {
						return Result{}, fmt.Errorf("finish gateway attempt lifecycle: %w", lifecycleErr)
					}
					return s.finish(result, OutcomeFailed, &attemptResult, applyErr), applyErr
				}
				if applyErr = validatePolicyApplyResult(applyResult, mutation.TransitionID); applyErr != nil {
					applyErr = fmt.Errorf("apply gateway account policy result: %w", applyErr)
					if lifecycleErr := failAttemptLifecycle(input.Lifecycle, lifecycleFailureGateway); lifecycleErr != nil {
						return Result{}, fmt.Errorf("finish gateway attempt lifecycle: %w", lifecycleErr)
					}
					return s.finish(result, OutcomeFailed, &attemptResult, applyErr), applyErr
				}
				result.Attempts[len(result.Attempts)-1].PolicyApply = clonePolicyApplyResult(applyResult)
			}
			if decision.Action == PolicyActionRetryNext || decision.Action == PolicyActionCooldown || decision.Action == PolicyActionDisable {
				if !attemptResult.RetryAllowed {
					if lifecycleErr := failAttemptLifecycle(input.Lifecycle, failureKindForAttempt(attemptResult)); lifecycleErr != nil {
						return Result{}, fmt.Errorf("finish gateway attempt lifecycle: %w", lifecycleErr)
					}
					return s.finish(result, OutcomeFailed, &attemptResult, attemptErr), nil
				}
				if lifecycleErr := retryAttemptLifecycle(input.Lifecycle); lifecycleErr != nil {
					return Result{}, fmt.Errorf("retry gateway attempt lifecycle: %w", lifecycleErr)
				}
				break
			}
			if attemptResult.KeyScopedFailure && attemptResult.RetryAllowed && keyOffset+1 < len(keyIndices) {
				if lifecycleErr := retryAttemptLifecycle(input.Lifecycle); lifecycleErr != nil {
					return Result{}, fmt.Errorf("retry gateway attempt lifecycle: %w", lifecycleErr)
				}
				continue
			}
			if attemptResult.RetryAllowed {
				if lifecycleErr := retryAttemptLifecycle(input.Lifecycle); lifecycleErr != nil {
					return Result{}, fmt.Errorf("retry gateway attempt lifecycle: %w", lifecycleErr)
				}
				break
			}
			if lifecycleErr := failAttemptLifecycle(input.Lifecycle, failureKindForAttempt(attemptResult)); lifecycleErr != nil {
				return Result{}, fmt.Errorf("finish gateway attempt lifecycle: %w", lifecycleErr)
			}
			return s.finish(result, OutcomeFailed, &attemptResult, attemptErr), nil
		}
	}
	if len(result.Attempts) >= s.config.MaxAttempts {
		if lifecycleErr := failAttemptLifecycle(input.Lifecycle, lifecycleFailureUpstream); lifecycleErr != nil {
			return Result{}, fmt.Errorf("finish gateway attempt lifecycle: %w", lifecycleErr)
		}
		return s.finish(result, OutcomeMaxAttempts, result.LastAttempt, nil), nil
	}
	if !input.PreserveLifecycleOnCandidatesExhausted {
		if lifecycleErr := failAttemptLifecycle(input.Lifecycle, lifecycleFailureUpstream); lifecycleErr != nil {
			return Result{}, fmt.Errorf("finish gateway attempt lifecycle: %w", lifecycleErr)
		}
	}
	return s.finish(result, OutcomeCandidatesExhausted, result.LastAttempt, nil), nil
}

func validFinalLane(value gatewayingress.Lane) bool {
	return value == gatewayingress.LaneText || value == gatewayingress.LaneImage
}

func startAttemptLifecycle(lifecycle AttemptLifecycle) error {
	if lifecycle == nil {
		return nil
	}
	return lifecycle.Start()
}

func observeAttemptLifecycle(lifecycle AttemptLifecycle, result AttemptResult) error {
	if lifecycle == nil {
		return nil
	}
	if result.Sink == nil {
		if result.Success || result.Committed {
			return fmt.Errorf("committed gateway attempt lifecycle result is missing sink state")
		}
		return nil
	}
	state := *result.Sink
	committed := state.TransportCommitted || state.SemanticCommitted || state.DownstreamBytes > 0
	if result.Committed != committed {
		return fmt.Errorf("gateway attempt committed flag does not match sink state")
	}
	return lifecycle.ObserveSink(state)
}

func retryAttemptLifecycle(lifecycle AttemptLifecycle) error {
	if lifecycle == nil {
		return nil
	}
	return lifecycle.RetryPreCommit()
}

func succeedAttemptLifecycle(lifecycle AttemptLifecycle) error {
	if lifecycle == nil {
		return nil
	}
	return lifecycle.FinishSuccess()
}

func failAttemptLifecycle(lifecycle AttemptLifecycle, kind string) error {
	if lifecycle == nil {
		return nil
	}
	return lifecycle.FinishFailure(kind)
}

func cancelAttemptLifecycle(lifecycle AttemptLifecycle) error {
	if lifecycle == nil {
		return nil
	}
	return lifecycle.CancelClient()
}

func finishContextAttemptLifecycle(lifecycle AttemptLifecycle, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return failAttemptLifecycle(lifecycle, lifecycleFailureGateway)
	}
	return cancelAttemptLifecycle(lifecycle)
}

func failureKindForAttempt(result AttemptResult) string {
	if result.Usage.FailureAttribution == gatewayusage.FailureAttributionDownstreamClosed || result.Audit.DownstreamClosed {
		return lifecycleFailureClientCanceled
	}
	switch result.Usage.FailureAttribution {
	case gatewayusage.FailureAttributionAccountUpstream, gatewayusage.FailureAttributionAccountDependency:
		return lifecycleFailureUpstream
	default:
		return lifecycleFailureGateway
	}
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
		AccountRuntime:  hotQualityRuntimeKey(attempt.Candidate),
		ProtocolProfile: hotQualityProtocolProfile(attempt.Candidate),
		RequestLane:     attempt.Lane,
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

func hotQualityRuntimeKey(candidate gatewaycandidatewindow.Candidate) string {
	projection := candidate.Projection
	accountID := strings.TrimSpace(projection.AccountID)
	if accountID == "" {
		return ""
	}
	authorizationID := strings.TrimSpace(projection.AccountAuthorizationID)
	if authorizationID == "" {
		return accountID
	}
	systemAccountID, groupID := strings.TrimSpace(projection.SystemAccountID), strings.TrimSpace(projection.GroupID)
	if systemAccountID == "" || groupID == "" {
		return ""
	}
	return accountID + ":authorized:" + systemAccountID + ":" + groupID + ":" + authorizationID
}

func hotQualityProtocolProfile(candidate gatewaycandidatewindow.Candidate) string {
	projection := candidate.Projection
	if strings.TrimSpace(projection.ResourceAccountID) != "" {
		if value := strings.TrimSpace(projection.ResourceProviderProtocolProfileID); value != "" {
			return value
		}
		code, version := strings.TrimSpace(projection.ResourceProtocolCode), strings.TrimSpace(projection.ResourceProtocolVersion)
		if code != "" {
			return code + ":" + version
		}
	}
	if value := strings.TrimSpace(projection.ProviderProtocolProfileID); value != "" {
		return value
	}
	code, version := strings.TrimSpace(projection.ProtocolCode), strings.TrimSpace(projection.ProtocolVersion)
	if code == "" {
		return ""
	}
	return code + ":" + version
}

func modelBucket(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" || len(model) > 256 || strings.IndexFunc(model, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "unknown"
	}
	sum := sha256.Sum256([]byte(model))
	return "model-bucket-" + hex.EncodeToString(sum[:1])
}

func deferredResponseTerminal(input Input, result AttemptResult) (*gatewayresponseterminal.Handoff, error) {
	if !input.DeferResponseTerminal || (!result.Success && !result.Committed) {
		return nil, nil
	}
	if result.Response == nil || result.Sink == nil {
		return nil, ErrDeferredResponseTerminalFactsRequired
	}
	return gatewayresponseterminal.NewHandoff(input.Lifecycle, *result.Response, *result.Sink)
}

func validateAttemptResult(result AttemptResult, err error) error {
	if result.FallbackDisposition != FallbackAccountUnknown && result.FallbackDisposition != FallbackAccountExcluded && result.FallbackDisposition != FallbackAccountRecoverable {
		return fmt.Errorf("gateway attempt fallback account disposition is invalid")
	}
	if (result.Success || result.Committed) && result.FallbackDisposition != FallbackAccountUnknown {
		return fmt.Errorf("terminal gateway attempt cannot classify fallback account")
	}
	if strings.TrimSpace(result.FallbackReason) != "" && !gatewayfallbackreason.Valid(result.FallbackReason) {
		return fmt.Errorf("gateway attempt fallback reason is invalid")
	}
	if (result.Success || result.Committed) && strings.TrimSpace(result.FallbackReason) != "" {
		return fmt.Errorf("terminal gateway attempt cannot provide fallback reason")
	}
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
	if result.ResponseInspection != nil && result.ResponseInspection.Decision == nil && len(result.ResponseInspection.Observations) == 0 {
		return fmt.Errorf("gateway response inspection handoff is incomplete")
	}
	if result.Success && result.ResponseInspection != nil && result.ResponseInspection.Decision != nil {
		return fmt.Errorf("successful gateway attempt cannot contain an inspection decision")
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
		result.LastAttempt = cloneAttemptResultForResult(*last)
	}
	if outcome == OutcomeCandidatesExhausted {
		result.FallbackAccounts = fallbackAccountFacts(result.fallbackRoster, result.fallbackRosterComplete, result.Attempts)
		if result.FallbackAccounts.Complete && gatewayfallbackreason.Valid(result.fallbackLastReason) {
			result.FallbackReason = result.fallbackLastReason
		}
	}
	return result
}

func fallbackAccountRoster(candidates []gatewaycandidatewindow.Candidate) ([]string, bool) {
	if len(candidates) == 0 {
		return nil, false
	}
	seen := make(map[string]struct{}, len(candidates))
	roster := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		accountID := strings.TrimSpace(gatewaycandidatewindow.EffectiveAccountIdentity(candidate).AccountID)
		if accountID == "" {
			return nil, false
		}
		if _, duplicate := seen[accountID]; duplicate {
			continue
		}
		seen[accountID] = struct{}{}
		roster = append(roster, accountID)
	}
	return roster, len(roster) > 0
}

func fallbackAccountFacts(roster []string, rosterComplete bool, attempts []AttemptSummary) FallbackAccountFacts {
	if !rosterComplete || len(roster) == 0 || len(attempts) == 0 {
		return FallbackAccountFacts{}
	}
	states := make(map[string]FallbackAccountDisposition, len(attempts))
	for _, attempt := range attempts {
		if attempt.Success || attempt.Committed {
			return FallbackAccountFacts{}
		}
		accountID := strings.TrimSpace(attempt.AccountID)
		if accountID == "" || attempt.FallbackDisposition == FallbackAccountUnknown {
			return FallbackAccountFacts{}
		}
		if !containsFallbackAccount(roster, accountID) {
			return FallbackAccountFacts{}
		}
		states[accountID] = attempt.FallbackDisposition
	}
	facts := FallbackAccountFacts{Complete: true}
	for _, accountID := range roster {
		switch states[accountID] {
		case FallbackAccountExcluded:
			facts.ExcludedAccountIDs = append(facts.ExcludedAccountIDs, accountID)
		case FallbackAccountRecoverable:
			facts.RecoverableAccountIDs = append(facts.RecoverableAccountIDs, accountID)
		default:
			return FallbackAccountFacts{}
		}
	}
	return facts
}

func containsFallbackAccount(roster []string, accountID string) bool {
	for _, value := range roster {
		if value == accountID {
			return true
		}
	}
	return false
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

func protocolForCandidate(candidate gatewaycandidatewindow.Candidate) protocolgateway.ProtocolCode {
	if strings.TrimSpace(candidate.Projection.ResourceAccountID) != "" && strings.TrimSpace(candidate.Projection.ResourceProtocolCode) != "" {
		return protocolgateway.ProtocolCode(strings.TrimSpace(candidate.Projection.ResourceProtocolCode))
	}
	return protocolgateway.ProtocolCode(strings.TrimSpace(candidate.Projection.ProtocolCode))
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
	// Deferred response facts are exposed only through the bounded handoff;
	// never retain the full handler result in the general attempt summary.
	copy.Response = nil
	if value.Sink != nil {
		sink := *value.Sink
		copy.Sink = &sink
	}
	if value.PolicyDecision != nil {
		decision := *value.PolicyDecision
		copy.PolicyDecision = &decision
	}
	copy.ResponseInspection = gatewayresponseinspection.CloneHandoff(value.ResponseInspection)
	copy.Failure.BodyText = boundedText(copy.Failure.BodyText, 64<<10)
	copy.Failure.Message = boundedText(copy.Failure.Message, 1000)
	return &copy
}

func cloneAttemptResultForResult(value AttemptResult) *AttemptResult {
	copy := cloneAttemptResult(value)
	copy.FallbackReason = ""
	return copy
}
