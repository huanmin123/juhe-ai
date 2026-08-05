// Package gatewayhighconcurrencyorchestration preserves the Node ordering for
// high-concurrency all-busy routing. It is an unregistered request-local seam.
package gatewayhighconcurrencyorchestration

import (
	"context"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/domain/groupscheduling"
	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewaycapacityrouting"
	"juhe-ai/backend-go/internal/modules/gatewayhighconcurrencyqueue"
	"juhe-ai/backend-go/internal/modules/gatewayingress"
)

type Outcome string

const (
	OutcomeReady    Outcome = "ready"
	OutcomeFallback Outcome = "fallback"
	OutcomeBusy     Outcome = "busy"
	OutcomeAborted  Outcome = "aborted"
	// OutcomeQueue is an internal phase outcome. It is returned only by
	// PrepareBeforeClientIP and must be completed through RunAfterClientIP.
	OutcomeQueue Outcome = "queue"
)

type CapacityEvaluator interface {
	Evaluate(context.Context, gatewaycandidatewindow.Window, gatewayingress.Lane) (gatewaycapacityrouting.Result, error)
}

type FallbackRequester interface {
	RequestFallback(context.Context, string) (FallbackResult, error)
}

type FallbackResult struct{ Attempted bool }

// CandidateRefresher refreshes only the candidates of the same route window.
// It must not substitute a different group, caller, group type, or policy.
type CandidateRefresher interface {
	RefreshHighConcurrencyCandidates(context.Context, gatewaycandidatewindow.Window) (gatewaycandidatewindow.Window, error)
}

type QueueWaiter interface {
	Wait(context.Context, gatewayhighconcurrencyqueue.Input) (gatewayhighconcurrencyqueue.Result, error)
}

type Options struct {
	Capacity  CapacityEvaluator
	Fallback  FallbackRequester
	Refresher CandidateRefresher
	Queue     QueueWaiter
}

type Input struct {
	Window         gatewaycandidatewindow.Window
	Lane           gatewayingress.Lane
	APIKeyID       string
	MaxQueueWaitMS *int
	// Fallback is an optional request-local override. A future outer route
	// owner uses it to keep the fallback decision and target preparation in the
	// same request state; nil preserves the service-level default requester.
	Fallback FallbackRequester
}

type Result struct {
	Outcome        Outcome
	Window         gatewaycandidatewindow.Window
	Initial        gatewaycapacityrouting.Result
	Final          gatewaycapacityrouting.Result
	Queue          *gatewayhighconcurrencyqueue.Result
	FallbackReason string
}

// PreLeaseResult separates the initial Node all-busy/fallback observation
// from the client-IP lease. A high-concurrency request must attempt an
// initial route fallback before it consumes a source-group client-IP slot.
//
// When RequiresQueue is false, Result is terminal for orchestration. When it
// is true, the caller must acquire the source-group client-IP lease and pass
// this value to RunAfterClientIP before queueing and refreshing candidates.
type PreLeaseResult struct {
	Result        Result
	RequiresQueue bool
}

type Service struct {
	capacity  CapacityEvaluator
	fallback  FallbackRequester
	refresher CandidateRefresher
	queue     QueueWaiter
}

func NewService(options Options) (*Service, error) {
	if options.Capacity == nil || options.Fallback == nil || options.Refresher == nil || options.Queue == nil {
		return nil, fmt.Errorf("high concurrency orchestration dependencies are required")
	}
	return &Service{capacity: options.Capacity, fallback: options.Fallback, refresher: options.Refresher, queue: options.Queue}, nil
}

// Run follows Node preparation's high-concurrency path:
// observe -> initial fallback -> queue -> refresh -> observe -> final fallback.
// It returns a fresh window for the existing candidate claim boundary; it never
// selects an account, acquires a slot, or decides the final HTTP response.
func (s *Service) Run(ctx context.Context, input Input) (Result, error) {
	preLease, err := s.PrepareBeforeClientIP(ctx, input)
	if err != nil {
		return Result{}, err
	}
	if !preLease.RequiresQueue {
		return preLease.Result, nil
	}
	return s.RunAfterClientIP(ctx, input, preLease)
}

// PrepareBeforeClientIP performs only the Node operations that must happen
// before acquiring the current group's client-IP lease: observe capacity and,
// if every account is busy, request the initial route fallback.
func (s *Service) PrepareBeforeClientIP(ctx context.Context, input Input) (PreLeaseResult, error) {
	if err := s.validateInput(ctx, input); err != nil {
		return PreLeaseResult{}, err
	}
	initial, err := s.capacity.Evaluate(ctx, input.Window, input.Lane)
	if err != nil {
		return PreLeaseResult{}, fmt.Errorf("observe initial high concurrency capacity: %w", err)
	}
	if !initial.Observation.AllBusy {
		return PreLeaseResult{Result: Result{Outcome: OutcomeReady, Window: input.Window, Initial: initial, Final: initial}}, nil
	}
	if initial.FallbackReason != gatewaycapacityrouting.FallbackHighConcurrencyGroupBusy || initial.SchedulingPolicy == nil {
		return PreLeaseResult{}, fmt.Errorf("high concurrency capacity decision is incomplete")
	}
	firstFallback, err := s.fallbackFor(input).RequestFallback(ctx, initial.FallbackReason)
	if err != nil {
		return PreLeaseResult{}, fmt.Errorf("request initial high concurrency fallback: %w", err)
	}
	result := Result{Window: input.Window, Initial: initial, Final: initial, FallbackReason: initial.FallbackReason}
	if firstFallback.Attempted {
		result.Outcome = OutcomeFallback
		return PreLeaseResult{Result: result}, nil
	}
	result.Outcome = OutcomeQueue
	return PreLeaseResult{Result: result, RequiresQueue: true}, nil
}

// RunAfterClientIP completes a pre-lease all-busy decision after the request
// owner has acquired the current group's client-IP lease. It intentionally
// owns only queue/refresh/final-fallback behavior.
func (s *Service) RunAfterClientIP(ctx context.Context, input Input, preLease PreLeaseResult) (Result, error) {
	if err := s.validateInput(ctx, input); err != nil {
		return Result{}, err
	}
	if err := sameWindowScope(input.Window, preLease.Result.Window); err != nil {
		return Result{}, fmt.Errorf("pre-lease high concurrency window scope changed: %w", err)
	}
	if !preLease.RequiresQueue {
		if preLease.Result.Outcome != OutcomeReady {
			return Result{}, fmt.Errorf("high concurrency pre-lease result is not runnable")
		}
		// Node checks capacity again after acquiring the client-IP slot. A group
		// that was initially ready may become busy while that slot is acquired;
		// that branch queues directly and only requests fallback after recheck.
		postLease, err := s.capacity.Evaluate(ctx, input.Window, input.Lane)
		if err != nil {
			return Result{}, fmt.Errorf("observe post-client-IP high concurrency capacity: %w", err)
		}
		if !postLease.Observation.AllBusy {
			return Result{Outcome: OutcomeReady, Window: input.Window, Initial: preLease.Result.Initial, Final: postLease}, nil
		}
		if err := validQueueDecision(postLease); err != nil {
			return Result{}, err
		}
		return s.runQueueAfterClientIP(ctx, input, preLease.Result.Initial, postLease)
	}
	if preLease.Result.Outcome != OutcomeQueue {
		return Result{}, fmt.Errorf("high concurrency pre-lease queue decision is incomplete")
	}
	if err := validQueueDecision(preLease.Result.Initial); err != nil {
		return Result{}, err
	}
	return s.runQueueAfterClientIP(ctx, input, preLease.Result.Initial, preLease.Result.Initial)
}

func (s *Service) runQueueAfterClientIP(ctx context.Context, input Input, initial, queueDecision gatewaycapacityrouting.Result) (Result, error) {
	queueInput, err := queueInputForWindow(input.Window, input.Lane, input.APIKeyID, queueDecision.SchedulingPolicy, input.MaxQueueWaitMS)
	if err != nil {
		return Result{}, err
	}
	queueResult, err := s.queue.Wait(ctx, queueInput)
	if err != nil {
		return Result{}, fmt.Errorf("wait for high concurrency capacity: %w", err)
	}
	queueCopy := queueResult
	if ctx.Err() != nil || queueResult.Reason == gatewayhighconcurrencyqueue.RejectAborted {
		return Result{Outcome: OutcomeAborted, Window: input.Window, Initial: initial, Final: initial, Queue: &queueCopy}, nil
	}
	refreshed, err := s.refresher.RefreshHighConcurrencyCandidates(ctx, input.Window)
	if err != nil {
		return Result{}, fmt.Errorf("refresh high concurrency candidates: %w", err)
	}
	if err := sameWindowScope(input.Window, refreshed); err != nil {
		return Result{}, err
	}
	if len(refreshed.Candidates) == 0 {
		return Result{}, fmt.Errorf("refreshed high concurrency candidates are missing")
	}
	final, err := s.capacity.Evaluate(ctx, refreshed, input.Lane)
	if err != nil {
		return Result{}, fmt.Errorf("observe refreshed high concurrency capacity: %w", err)
	}
	result := Result{Window: refreshed, Initial: initial, Final: final, Queue: &queueCopy}
	if !final.Observation.AllBusy {
		result.Outcome = OutcomeReady
		return result, nil
	}
	if final.FallbackReason != gatewaycapacityrouting.FallbackHighConcurrencyGroupBusy {
		return Result{}, fmt.Errorf("refreshed high concurrency fallback reason is invalid")
	}
	lastFallback, err := s.fallbackFor(input).RequestFallback(ctx, final.FallbackReason)
	if err != nil {
		return Result{}, fmt.Errorf("request final high concurrency fallback: %w", err)
	}
	result.FallbackReason = final.FallbackReason
	if lastFallback.Attempted {
		result.Outcome = OutcomeFallback
		return result, nil
	}
	result.Outcome = OutcomeBusy
	return result, nil
}

func validQueueDecision(value gatewaycapacityrouting.Result) error {
	if !value.Observation.AllBusy || value.FallbackReason != gatewaycapacityrouting.FallbackHighConcurrencyGroupBusy || value.SchedulingPolicy == nil {
		return fmt.Errorf("high concurrency pre-lease queue decision is incomplete")
	}
	return nil
}

func (s *Service) fallbackFor(input Input) FallbackRequester {
	if input.Fallback != nil {
		return input.Fallback
	}
	return s.fallback
}

func (s *Service) validateInput(ctx context.Context, input Input) error {
	if s == nil || s.capacity == nil || s.fallback == nil || s.refresher == nil || s.queue == nil {
		return fmt.Errorf("high concurrency orchestration service is not configured")
	}
	if ctx == nil {
		return fmt.Errorf("high concurrency orchestration context is required")
	}
	if input.Lane != gatewayingress.LaneText && input.Lane != gatewayingress.LaneImage {
		return fmt.Errorf("high concurrency orchestration lane is invalid")
	}
	if strings.TrimSpace(input.Window.Access.GroupType) != "high_concurrency" {
		return fmt.Errorf("high concurrency orchestration group type is invalid")
	}
	if len(input.Window.Candidates) == 0 {
		return fmt.Errorf("high concurrency orchestration candidates are missing")
	}
	return nil
}

func queueInputForWindow(window gatewaycandidatewindow.Window, lane gatewayingress.Lane, apiKeyID string, policy *groupscheduling.Policy, maxWaitMS *int) (gatewayhighconcurrencyqueue.Input, error) {
	if policy == nil {
		return gatewayhighconcurrencyqueue.Input{}, fmt.Errorf("high concurrency queue scheduling policy is missing")
	}
	if err := groupscheduling.Validate(*policy); err != nil {
		return gatewayhighconcurrencyqueue.Input{}, fmt.Errorf("high concurrency queue scheduling policy is invalid: %w", err)
	}
	systemAccountID := strings.TrimSpace(window.Access.CallerSystemAccountID)
	groupID := strings.TrimSpace(window.Access.GroupID)
	if systemAccountID == "" || groupID == "" {
		return gatewayhighconcurrencyqueue.Input{}, fmt.Errorf("high concurrency queue group scope is missing")
	}
	queueLane := gatewayhighconcurrencyqueue.LaneText
	if lane == gatewayingress.LaneImage {
		queueLane = gatewayhighconcurrencyqueue.LaneImage
	}
	accountIDs := make([]string, 0, len(window.Candidates))
	limits := make(map[string]int, len(window.Candidates))
	for _, candidate := range window.Candidates {
		identity := gatewaycandidatewindow.EffectiveAccountIdentity(candidate)
		accountID := strings.TrimSpace(identity.AccountID)
		if accountID == "" {
			return gatewayhighconcurrencyqueue.Input{}, fmt.Errorf("high concurrency queue candidate has no effective account identity")
		}
		limit := effectiveConcurrencyLimit(candidate)
		if limit < 1 {
			limit = 1
		}
		if previous, exists := limits[accountID]; exists {
			if limit < previous {
				limits[accountID] = limit
			}
			continue
		}
		limits[accountID] = limit
		accountIDs = append(accountIDs, accountID)
	}
	return gatewayhighconcurrencyqueue.Input{SystemAccountID: systemAccountID, GroupID: groupID, APIKeyID: apiKeyID, AccountIDs: accountIDs, AccountConcurrencyLimits: limits, Lane: queueLane, Policy: policy, MaxWaitMS: maxWaitMS}, nil
}

func effectiveConcurrencyLimit(candidate gatewaycandidatewindow.Candidate) int {
	if strings.TrimSpace(candidate.Projection.ResourceAccountID) != "" {
		return candidate.Projection.ResourceConcurrencyLimit
	}
	return candidate.Projection.ConcurrencyLimit
}

func sameWindowScope(before, after gatewaycandidatewindow.Window) error {
	if strings.TrimSpace(before.Access.GroupID) != strings.TrimSpace(after.Access.GroupID) ||
		strings.TrimSpace(before.Access.CallerSystemAccountID) != strings.TrimSpace(after.Access.CallerSystemAccountID) ||
		strings.TrimSpace(before.Access.GroupType) != strings.TrimSpace(after.Access.GroupType) ||
		before.Access.SchedulingPolicyJSON != after.Access.SchedulingPolicyJSON {
		return fmt.Errorf("refreshed high concurrency window scope changed")
	}
	return nil
}
