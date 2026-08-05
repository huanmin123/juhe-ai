// Package gatewaycurrentgroupexecution composes one frozen W10 execution plan
// with the existing normal and high-concurrency attempt owners. It remains an
// unregistered seam: HTTP response ownership, terminal audit/usage persistence,
// and cross-group fallback stay with a later listener owner.
package gatewaycurrentgroupexecution

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/modules/gatewayattemptloop"
	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewayclientipconcurrency"
	"juhe-ai/backend-go/internal/modules/gatewayhighconcurrencyexecution"
	"juhe-ai/backend-go/internal/modules/gatewayhighconcurrencyorchestration"
	"juhe-ai/backend-go/internal/modules/gatewayingress"
	"juhe-ai/backend-go/internal/modules/gatewayrequestexecution"
	"juhe-ai/backend-go/internal/modules/gatewayrequestlifecycle"
	protocolgateway "juhe-ai/backend-go/internal/protocols/gateway"
)

var (
	ErrExecutionNotFinalized = errors.New("gateway current-group execution is not a finalized orchestration handoff")
	ErrExecutionEmpty        = errors.New("gateway current-group execution has no runnable batch")
	ErrNormalRunnerMissing   = errors.New("gateway current-group normal runner is required")
	ErrHighRunnerMissing     = errors.New("gateway current-group high-concurrency runner is required")
	ErrLifecycleProvided     = errors.New("gateway current-group lifecycle must be created from finalized execution")
)

// NormalRunner is implemented by gatewayattemptloop.Service. It receives only
// the first frozen group window; it cannot advance to a later route group.
type NormalRunner interface {
	Run(gatewayattemptloop.Input) (gatewayattemptloop.Result, error)
}

// HighConcurrencyRunner is implemented by gatewayhighconcurrencyexecution.Service.
// Its returned fallback outcome is data for a later owner, not permission to
// reuse a later execution batch.
type HighConcurrencyRunner interface {
	Run(context.Context, gatewayhighconcurrencyexecution.Input) (gatewayhighconcurrencyexecution.Result, error)
}

type Options struct {
	Normal          NormalRunner
	HighConcurrency HighConcurrencyRunner
}

// Input is request-local. ClientIP has already been extracted by a future HTTP
// owner; it is passed through only to the high-concurrency client-IP scope and
// is never inferred from transport state here.
type Input struct {
	Context   context.Context
	Execution gatewayrequestexecution.Execution
	ClientIP  string
	// PreAcquiredClientIP and RetainPreAcquiredClientIPLease apply only to a
	// freshly prepared high-concurrency target. The concrete high runner proves
	// scope before consuming the decision.
	PreAcquiredClientIP            *gatewayclientipconcurrency.Decision
	RetainPreAcquiredClientIPLease bool
	// Fallback is an optional request-local route-owner callback. It is passed
	// only to the high-concurrency pre-lease seam; normal groups do not infer
	// or own cross-group fallback.
	Fallback gatewayhighconcurrencyorchestration.FallbackRequester
	// PostSourceLeaseFallback is the strict high-concurrency callback used when
	// a source client-IP lease is already held. It is intentionally unavailable
	// to normal groups, which have no corresponding source lease to transfer.
	PostSourceLeaseFallback gatewayhighconcurrencyexecution.PostSourceLeaseFallbackPreparer
	Lifecycle               gatewayattemptloop.AttemptLifecycle
	// DeferResponseTerminal is an explicit listener-owner opt-in. It prevents
	// committed response lifecycle settlement until the returned handoff is
	// recorded and the actual HTTP completion is observed.
	DeferResponseTerminal bool
	// OnRequestLifecycleReady is called only by RunWithRequestLifecycle after it
	// has validated the immutable execution and created its request-local
	// lifecycle, but before any adapter or attempt can start. A future request
	// owner uses this narrow seam to bind its terminal observer without injecting
	// a detached lifecycle into the current-group runner.
	OnRequestLifecycleReady func(*gatewayrequestlifecycle.Lifecycle)
	// PreserveLifecycleOnCandidatesExhausted must be enabled only after an
	// outer route owner proves that another group can be prepared. This service
	// never advances itself to a later batch.
	PreserveLifecycleOnCandidatesExhausted bool
	Profile                                *protocolgateway.Profile
	Tracker                                *gatewayattemptloop.AttemptTracker
	Observer                               gatewayattemptloop.AttemptObserver
}

type Result struct {
	BindingID string
	GroupID   string
	GroupType string
	Normal    *gatewayattemptloop.Result
	High      *gatewayhighconcurrencyexecution.Result
}

type Service struct {
	normal NormalRunner
	high   HighConcurrencyRunner
}

func NewService(options Options) (*Service, error) {
	if options.Normal == nil {
		return nil, ErrNormalRunnerMissing
	}
	return &Service{normal: options.Normal, high: options.HighConcurrency}, nil
}

// Run executes exactly the first runnable group of a finalized execution. It
// never consumes subsequent batches, even after a pre-commit failure or a
// high-concurrency fallback outcome: Node-compatible cross-group fallback must
// fully prepare its target group and re-acquire a target-scoped client-IP lease.
func (s *Service) Run(input Input) (Result, error) {
	facts, err := factsFromExecution(input.Execution)
	if err != nil {
		return Result{}, err
	}
	return s.run(input, facts)
}

// RunWithRequestLifecycle creates the only request-local lifecycle that may
// drive this finalized execution, then runs its first frozen group. It returns
// the concrete lifecycle so a later cross-group or HTTP owner can observe or
// terminally close the same request after this service returns. Callers must
// not inject another lifecycle here, because that could be detached from the
// immutable execution plan's initial-commit fence.
func (s *Service) RunWithRequestLifecycle(input Input) (Result, *gatewayrequestlifecycle.Lifecycle, error) {
	facts, err := factsFromExecution(input.Execution)
	if err != nil {
		return Result{}, nil, err
	}
	if input.Lifecycle != nil {
		return Result{}, nil, ErrLifecycleProvided
	}
	lifecycle, err := gatewayrequestlifecycle.New(input.Execution)
	if err != nil {
		return Result{}, nil, fmt.Errorf("create gateway request lifecycle: %w", err)
	}
	if input.OnRequestLifecycleReady != nil {
		input.OnRequestLifecycleReady(lifecycle)
	}
	adapter, err := gatewayrequestlifecycle.NewAttemptLoopAdapter(lifecycle)
	if err != nil {
		return Result{}, nil, fmt.Errorf("create gateway attempt lifecycle adapter: %w", err)
	}
	input.Lifecycle = adapter
	result, err := s.run(input, facts)
	if err != nil {
		// The outer HTTP/cross-group owner still needs this same lifecycle to
		// record a request terminal after an infrastructure error.
		return Result{}, lifecycle, err
	}
	return result, lifecycle, nil
}

// RunWithExistingRequestLifecycle runs one freshly prepared later target with
// the request lifecycle created for an earlier group. The caller must be the
// outer route owner: it has already proved the target is later, fresh and
// pre-commit. This method deliberately creates a new opaque attempt adapter;
// reusing the prior adapter could let stale attempt callbacks affect the new
// target generation.
func (s *Service) RunWithExistingRequestLifecycle(input Input, lifecycle *gatewayrequestlifecycle.Lifecycle) (Result, error) {
	facts, err := factsFromExecution(input.Execution)
	if err != nil {
		return Result{}, err
	}
	if lifecycle == nil {
		return Result{}, fmt.Errorf("gateway current-group existing request lifecycle is required")
	}
	if input.Lifecycle != nil {
		return Result{}, ErrLifecycleProvided
	}
	if input.OnRequestLifecycleReady != nil {
		return Result{}, fmt.Errorf("gateway current-group existing lifecycle hook is not allowed")
	}
	if err := lifecycle.ValidateContinuation(input.Execution); err != nil {
		return Result{}, fmt.Errorf("validate gateway existing request lifecycle: %w", err)
	}
	adapter, err := gatewayrequestlifecycle.NewAttemptLoopAdapter(lifecycle)
	if err != nil {
		return Result{}, fmt.Errorf("create gateway existing request lifecycle adapter: %w", err)
	}
	input.Lifecycle = adapter
	return s.run(input, facts)
}

type executionFacts struct {
	identity gatewayrequestexecution.Identity
	apiKeyID string
	request  protocolgateway.RequestShape
	lane     gatewayingress.Lane
	batch    batchFacts
}

type batchFacts struct {
	bindingID string
	groupID   string
	window    gatewaycandidatewindow.Window
}

func factsFromExecution(execution gatewayrequestexecution.Execution) (executionFacts, error) {
	request, hasRequest := execution.RequestShape()
	lane, hasLane := execution.FinalLane()
	if !hasRequest || !hasLane || (lane != gatewayingress.LaneText && lane != gatewayingress.LaneImage) {
		return executionFacts{}, ErrExecutionNotFinalized
	}
	batches := execution.Batches()
	if len(batches) == 0 {
		return executionFacts{}, ErrExecutionEmpty
	}
	current := batches[0]
	window := current.RuntimeWindow()
	facts := executionFacts{
		identity: execution.Identity(), apiKeyID: execution.APIKeyID(), request: request, lane: lane,
		batch: batchFacts{bindingID: current.BindingID(), groupID: current.GroupID(), window: window},
	}
	if err := validateFacts(facts); err != nil {
		return executionFacts{}, err
	}
	return facts, nil
}

func (s *Service) run(input Input, facts executionFacts) (Result, error) {
	if s == nil || s.normal == nil {
		return Result{}, ErrNormalRunnerMissing
	}
	if input.Context == nil {
		return Result{}, fmt.Errorf("gateway current-group execution context is required")
	}
	if err := validateFacts(facts); err != nil {
		return Result{}, err
	}
	result := Result{BindingID: facts.batch.bindingID, GroupID: facts.batch.groupID, GroupType: facts.batch.window.Access.GroupType}
	if facts.batch.window.Access.GroupType == "high_concurrency" {
		if s.high == nil {
			return Result{}, ErrHighRunnerMissing
		}
		high, err := s.high.Run(input.Context, gatewayhighconcurrencyexecution.Input{
			Orchestration: gatewayhighconcurrencyorchestration.Input{
				Window: facts.batch.window, Lane: facts.lane, APIKeyID: facts.apiKeyID, Fallback: input.Fallback,
			},
			MutationID: facts.identity.MutationID, TraceID: facts.identity.TraceID,
			Request: facts.request, FinalLane: facts.lane, ClientIP: input.ClientIP, Fallback: input.Fallback,
			PostSourceLeaseFallback: input.PostSourceLeaseFallback,
			PreAcquiredClientIP:     input.PreAcquiredClientIP, RetainPreAcquiredClientIPLease: input.RetainPreAcquiredClientIPLease,
			PreserveLifecycleOnCandidatesExhausted: input.PreserveLifecycleOnCandidatesExhausted,
			Lifecycle:                              input.Lifecycle, DeferResponseTerminal: input.DeferResponseTerminal,
			Profile: input.Profile, Tracker: input.Tracker, Observer: input.Observer,
		})
		if err != nil {
			return Result{}, fmt.Errorf("run current high-concurrency group: %w", err)
		}
		result.High = &high
		return result, nil
	}
	if input.PreAcquiredClientIP != nil || input.RetainPreAcquiredClientIPLease || input.PostSourceLeaseFallback != nil {
		return Result{}, fmt.Errorf("gateway normal current group cannot consume a high-concurrency client-IP fallback handoff")
	}
	normal, err := s.normal.Run(gatewayattemptloop.Input{
		Context: input.Context, MutationID: facts.identity.MutationID, TraceID: facts.identity.TraceID,
		Candidates: facts.batch.window.Candidates, Request: facts.request, FinalLane: facts.lane,
		PreserveLifecycleOnCandidatesExhausted: input.PreserveLifecycleOnCandidatesExhausted,
		Lifecycle:                              input.Lifecycle, DeferResponseTerminal: input.DeferResponseTerminal,
		Profile: input.Profile, Tracker: input.Tracker, Observer: input.Observer,
	})
	if err != nil {
		return Result{}, fmt.Errorf("run current normal group: %w", err)
	}
	result.Normal = &normal
	return result, nil
}

func validateFacts(facts executionFacts) error {
	if strings.TrimSpace(facts.apiKeyID) == "" || strings.TrimSpace(facts.batch.bindingID) == "" || strings.TrimSpace(facts.batch.groupID) == "" ||
		strings.TrimSpace(facts.batch.window.Access.GroupID) != facts.batch.groupID || strings.TrimSpace(facts.batch.window.Access.CallerSystemAccountID) == "" ||
		strings.TrimSpace(facts.batch.window.Access.GroupType) == "" || len(facts.batch.window.Candidates) == 0 {
		return ErrExecutionEmpty
	}
	return nil
}
