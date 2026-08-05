// Package gatewayhighconcurrencyexecution joins the Node-shaped high
// concurrency all-busy path to the existing claimed attempt loop. It is an
// unregistered seam: a future listener still owns request parsing, client-IP
// capacity, response disposition, route fallback, and audit finalization.
package gatewayhighconcurrencyexecution

import (
	"context"
	"fmt"

	"juhe-ai/backend-go/internal/domain/groupscheduling"
	"juhe-ai/backend-go/internal/modules/gatewayattemptloop"
	"juhe-ai/backend-go/internal/modules/gatewaycandidateclaim"
	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewayclientipconcurrency"
	"juhe-ai/backend-go/internal/modules/gatewayhighconcurrencyorchestration"
	"juhe-ai/backend-go/internal/modules/gatewayingress"
	protocolgateway "juhe-ai/backend-go/internal/protocols/gateway"
)

// Orchestrator keeps the all-busy ordering testable without letting this
// package reimplement capacity observation, fallback, queueing, or refresh.
type Orchestrator interface {
	PrepareBeforeClientIP(context.Context, gatewayhighconcurrencyorchestration.Input) (gatewayhighconcurrencyorchestration.PreLeaseResult, error)
	RunAfterClientIP(context.Context, gatewayhighconcurrencyorchestration.Input, gatewayhighconcurrencyorchestration.PreLeaseResult) (gatewayhighconcurrencyorchestration.Result, error)
}

// ClientIPAcquirer acquires the high-concurrency client-IP slot after the
// initial all-busy fallback decision and before queueing or attempting. The
// request owner remains responsible for translating a rejected decision into
// its protocol response.
type ClientIPAcquirer interface {
	Acquire(context.Context, gatewayclientipconcurrency.Input) (gatewayclientipconcurrency.Decision, error)
}

// PostSourceLeaseFallbackPreparer is an opt-in strict fallback boundary for a
// route owner that can fully prepare a target group while the source client-IP
// lease is still held. It must call CompleteTargetPreparation with an acquired
// target decision before it reports Attempted. An enabled target must own a
// lease; a disabled target is Node's acquired no-op path. Any target lease
// stays with that outer owner and is never released by this package.
type PostSourceLeaseFallbackPreparer interface {
	PrepareFallbackTarget(context.Context, string, gatewayclientipconcurrency.TargetPreparationHandoff) (gatewayhighconcurrencyorchestration.FallbackResult, error)
}

type Options struct {
	Orchestrator     Orchestrator
	ClientIP         ClientIPAcquirer
	ClaimingExecutor *gatewaycandidateclaim.ClaimingExecutor
	PolicyApplier    gatewayattemptloop.PolicyApplier
	AttemptConfig    gatewayattemptloop.Config
}

type Input struct {
	Orchestration gatewayhighconcurrencyorchestration.Input
	MutationID    string
	TraceID       string
	Request       protocolgateway.RequestShape
	// FinalLane must be copied from the immutable ingress finalization. It is
	// intentionally distinct from Request so raw body hints cannot override a
	// catalog mapping upgrade or an image-permission downgrade.
	FinalLane gatewayingress.Lane
	// ClientIP is the already-extracted client address. An empty value keeps
	// Node's disabled client-IP concurrency behavior; it must not be replaced
	// with a guessed transport address here.
	ClientIP string
	// PreAcquiredClientIP is a target-group decision acquired during complete
	// fallback preparation. It is accepted only when it is still active and
	// exactly matches this frozen high-concurrency group scope.
	PreAcquiredClientIP *gatewayclientipconcurrency.Decision
	// RetainPreAcquiredClientIPLease transfers terminal release responsibility to
	// the future outer response owner. It is legal only with a validated
	// PreAcquiredClientIP decision; normal/source execution keeps internal
	// once-only release behavior.
	RetainPreAcquiredClientIPLease bool
	// Fallback is forwarded as a request-local override to the high-concurrency
	// orchestration. It is never inferred from the current group or transport.
	Fallback gatewayhighconcurrencyorchestration.FallbackRequester
	// PostSourceLeaseFallback is an explicit strict handoff opt-in. It is used
	// only for a fallback requested after this service has acquired the source
	// client-IP lease. Initial all-busy fallback keeps Fallback's existing
	// behavior and occurs before source client-IP acquisition.
	PostSourceLeaseFallback PostSourceLeaseFallbackPreparer
	// Lifecycle is supplied only by the outer request owner. It remains
	// request-local and gives each claimed executor call an opaque generation.
	Lifecycle gatewayattemptloop.AttemptLifecycle
	// DeferResponseTerminal leaves committed response settlement to the
	// listener owner. The default keeps the existing attempt-loop terminal.
	DeferResponseTerminal bool
	// PreserveLifecycleOnCandidatesExhausted is set only by an outer owner that
	// has proved another route group may be prepared after this one exhausts.
	PreserveLifecycleOnCandidatesExhausted bool
	Profile                                *protocolgateway.Profile
	Tracker                                *gatewayattemptloop.AttemptTracker
	Observer                               gatewayattemptloop.AttemptObserver
}

type Result struct {
	Orchestration gatewayhighconcurrencyorchestration.Result
	ClientIP      *gatewayclientipconcurrency.Decision
	Attempts      *gatewayattemptloop.Result
}

type Service struct {
	orchestrator Orchestrator
	clientIP     ClientIPAcquirer
	attempts     *gatewayattemptloop.Service
}

// NewService deliberately accepts only a concrete ClaimingExecutor. This
// guarantees every candidate handed off after the fresh high-concurrency
// window is rechecked and receives a token-fenced account slot before the
// inner HTTP executor can use its credentials.
func NewService(options Options) (*Service, error) {
	if options.Orchestrator == nil {
		return nil, fmt.Errorf("high concurrency execution orchestrator is required")
	}
	if options.ClientIP == nil {
		return nil, fmt.Errorf("high concurrency execution client IP acquirer is required")
	}
	if options.ClaimingExecutor == nil {
		return nil, fmt.Errorf("high concurrency execution claiming executor is required")
	}
	attempts, err := gatewayattemptloop.NewService(options.ClaimingExecutor, options.PolicyApplier, options.AttemptConfig)
	if err != nil {
		return nil, fmt.Errorf("build high concurrency attempt loop: %w", err)
	}
	return &Service{orchestrator: options.Orchestrator, clientIP: options.ClientIP, attempts: attempts}, nil
}

// Run preserves Node's sequence: resolve initial all-busy fallback, acquire
// one client-IP slot, then queue/refresh and hand only a ready window to the
// candidate claim boundary and attempt loop. The lease is released once on
// every terminal, fallback, busy, or error path; a future listener must not
// retain it across a different route-group preparation.
func (s *Service) Run(ctx context.Context, input Input) (Result, error) {
	if s == nil || s.orchestrator == nil || s.clientIP == nil || s.attempts == nil {
		return Result{}, fmt.Errorf("high concurrency execution service is not configured")
	}
	if ctx == nil {
		return Result{}, fmt.Errorf("high concurrency execution context is required")
	}
	if input.FinalLane != gatewayingress.LaneText && input.FinalLane != gatewayingress.LaneImage {
		return Result{}, fmt.Errorf("high concurrency execution final lane is required")
	}
	if input.Orchestration.Lane != input.FinalLane {
		return Result{}, fmt.Errorf("high concurrency execution lane does not match frozen ingress")
	}
	if input.RetainPreAcquiredClientIPLease && input.PreAcquiredClientIP == nil {
		return Result{}, fmt.Errorf("retain high concurrency client IP lease requires a pre-acquired decision")
	}
	clientIPInput, err := ClientIPInputForWindow(input.Orchestration.Window, input.Orchestration.APIKeyID, input.ClientIP)
	if err != nil {
		return Result{}, err
	}
	var clientIP gatewayclientipconcurrency.Decision
	var retainedHandoff *gatewayclientipconcurrency.LeaseHandoff
	if input.PreAcquiredClientIP != nil {
		clientIP = *input.PreAcquiredClientIP
		if err := gatewayclientipconcurrency.ValidateAcquiredDecisionForInput(clientIPInput, clientIP); err != nil {
			return Result{}, fmt.Errorf("validate pre-acquired high concurrency client IP slot: %w", err)
		}
		if input.PostSourceLeaseFallback != nil {
			retainedHandoff, err = gatewayclientipconcurrency.NewLeaseHandoff(clientIPInput, clientIP)
			if err != nil {
				return Result{}, fmt.Errorf("create retained high concurrency client IP lease handoff: %w", err)
			}
			if !input.RetainPreAcquiredClientIPLease {
				defer retainedHandoff.CloseSource()
			}
		} else if clientIP.Lease != nil && !input.RetainPreAcquiredClientIPLease {
			defer clientIP.Lease.Release()
		}
	}
	orchestrationInput := input.Orchestration
	if input.Fallback != nil {
		orchestrationInput.Fallback = input.Fallback
	}
	// A retained pre-acquired lease is already this group's source lease. If
	// its pre-lease all-busy check requests another group, use the strict
	// adapter immediately so the next target is proved before this lease moves.
	if retainedHandoff != nil {
		orchestrationInput.Fallback = postSourceLeaseFallbackAdapter{preparer: input.PostSourceLeaseFallback, handoff: retainedHandoff}
	}
	preLease, err := s.orchestrator.PrepareBeforeClientIP(ctx, orchestrationInput)
	if err != nil {
		return Result{}, fmt.Errorf("prepare high concurrency before client IP: %w", err)
	}
	if err := validatePreLease(preLease); err != nil {
		return Result{}, err
	}
	result := Result{Orchestration: preLease.Result}
	if preLease.Result.Outcome == gatewayhighconcurrencyorchestration.OutcomeFallback {
		return result, nil
	}
	if input.PreAcquiredClientIP == nil {
		clientIP, err = s.clientIP.Acquire(ctx, clientIPInput)
		if err != nil {
			return Result{}, fmt.Errorf("acquire high concurrency client IP slot: %w", err)
		}
	}
	if clientIP.Enabled && clientIP.Acquired && clientIP.Lease == nil {
		return Result{}, fmt.Errorf("acquired high concurrency client IP slot has no lease")
	}
	if !clientIP.Acquired && clientIP.Lease != nil {
		return Result{}, fmt.Errorf("rejected high concurrency client IP slot has a lease")
	}
	result.ClientIP = &clientIP
	if !clientIP.Acquired {
		return result, nil
	}
	postLeaseInput := orchestrationInput
	if input.PostSourceLeaseFallback != nil {
		handoff := retainedHandoff
		if handoff == nil {
			handoff, err = gatewayclientipconcurrency.NewLeaseHandoff(clientIPInput, clientIP)
			if err != nil {
				return Result{}, fmt.Errorf("create high concurrency client IP lease handoff: %w", err)
			}
			defer handoff.CloseSource()
		}
		postLeaseInput.Fallback = postSourceLeaseFallbackAdapter{preparer: input.PostSourceLeaseFallback, handoff: handoff}
	} else if input.PreAcquiredClientIP == nil && clientIP.Lease != nil && !input.RetainPreAcquiredClientIPLease {
		defer clientIP.Lease.Release()
	}
	orchestration, err := s.orchestrator.RunAfterClientIP(ctx, postLeaseInput, preLease)
	if err != nil {
		return Result{}, fmt.Errorf("run high concurrency orchestration: %w", err)
	}
	result.Orchestration = orchestration
	if orchestration.Outcome != gatewayhighconcurrencyorchestration.OutcomeReady {
		return result, nil
	}
	if len(orchestration.Window.Candidates) == 0 {
		return Result{}, fmt.Errorf("ready high concurrency window has no candidates")
	}
	attempts, err := s.attempts.Run(gatewayattemptloop.Input{
		Context: ctx, MutationID: input.MutationID, TraceID: input.TraceID,
		Candidates: orchestration.Window.Candidates, Request: input.Request,
		FinalLane:                              input.FinalLane,
		PreserveLifecycleOnCandidatesExhausted: input.PreserveLifecycleOnCandidatesExhausted,
		Lifecycle:                              input.Lifecycle, DeferResponseTerminal: input.DeferResponseTerminal,
		Profile: input.Profile, Tracker: input.Tracker, Observer: input.Observer,
	})
	if err != nil {
		return Result{}, fmt.Errorf("run claimed high concurrency attempts: %w", err)
	}
	result.Attempts = &attempts
	return result, nil
}

type postSourceLeaseFallbackAdapter struct {
	preparer PostSourceLeaseFallbackPreparer
	handoff  *gatewayclientipconcurrency.LeaseHandoff
}

func (a postSourceLeaseFallbackAdapter) RequestFallback(ctx context.Context, reason string) (gatewayhighconcurrencyorchestration.FallbackResult, error) {
	if a.preparer == nil || a.handoff == nil {
		return gatewayhighconcurrencyorchestration.FallbackResult{}, fmt.Errorf("post-source-lease fallback preparation is not configured")
	}
	result, err := a.preparer.PrepareFallbackTarget(ctx, reason, a.handoff.TargetPreparation())
	if err != nil {
		return gatewayhighconcurrencyorchestration.FallbackResult{}, err
	}
	targetPrepared := a.handoff.TargetPrepared()
	if result.Attempted && !targetPrepared {
		return gatewayhighconcurrencyorchestration.FallbackResult{}, fmt.Errorf("post-source-lease fallback reported attempted without completed target preparation")
	}
	if !result.Attempted && targetPrepared {
		return gatewayhighconcurrencyorchestration.FallbackResult{}, fmt.Errorf("post-source-lease fallback completed target preparation without reporting attempted")
	}
	return result, nil
}

func validatePreLease(value gatewayhighconcurrencyorchestration.PreLeaseResult) error {
	switch value.Result.Outcome {
	case gatewayhighconcurrencyorchestration.OutcomeReady:
		if value.RequiresQueue {
			return fmt.Errorf("ready high concurrency pre-lease result requires queue")
		}
	case gatewayhighconcurrencyorchestration.OutcomeFallback:
		if value.RequiresQueue {
			return fmt.Errorf("fallback high concurrency pre-lease result requires queue")
		}
	case gatewayhighconcurrencyorchestration.OutcomeQueue:
		if !value.RequiresQueue {
			return fmt.Errorf("queue high concurrency pre-lease result does not require queue")
		}
	default:
		return fmt.Errorf("high concurrency pre-lease outcome is invalid")
	}
	return nil
}

// ClientIPInputForWindow derives the exact client-IP concurrency scope for a
// frozen high-concurrency target. Cross-group owners use it while fully
// preparing a later target before they transfer a source lease; it performs no
// acquisition and cannot infer a group from mutable route state.
func ClientIPInputForWindow(window gatewaycandidatewindow.Window, apiKeyID, clientIP string) (gatewayclientipconcurrency.Input, error) {
	policyJSON := window.Access.SchedulingPolicyJSON
	policy, err := groupscheduling.ParseStoredJSON(&policyJSON, window.Access.GroupType)
	if err != nil {
		return gatewayclientipconcurrency.Input{}, fmt.Errorf("parse high concurrency client IP policy: %w", err)
	}
	if policy == nil {
		return gatewayclientipconcurrency.Input{}, fmt.Errorf("high concurrency client IP policy is missing")
	}
	return gatewayclientipconcurrency.Input{
		SystemAccountID: window.Access.CallerSystemAccountID,
		GroupID:         window.Access.GroupID,
		APIKeyID:        apiKeyID,
		ClientIP:        clientIP,
		Policy:          policy,
	}, nil
}

// clientIPInputFor preserves the focused execution-level test seam while the
// public helper serves a separately prepared fallback target.
func clientIPInputFor(input Input) (gatewayclientipconcurrency.Input, error) {
	return ClientIPInputForWindow(input.Orchestration.Window, input.Orchestration.APIKeyID, input.ClientIP)
}
