// Package gatewaycrossgroupowner composes the unregistered W10 cross-group
// continuation. It has no HTTP writer or route registration; a future listener
// still owns public response, audit, usage, and terminal completion.
package gatewaycrossgroupowner

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/modules/gatewayattemptloop"
	"juhe-ai/backend-go/internal/modules/gatewayclientipconcurrency"
	"juhe-ai/backend-go/internal/modules/gatewaycurrentgroupexecution"
	"juhe-ai/backend-go/internal/modules/gatewayhighconcurrencyexecution"
	"juhe-ai/backend-go/internal/modules/gatewayhighconcurrencyorchestration"
	"juhe-ai/backend-go/internal/modules/gatewayhttpcompletion"
	"juhe-ai/backend-go/internal/modules/gatewayrequestexecution"
	"juhe-ai/backend-go/internal/modules/gatewayrequestlifecycle"
	"juhe-ai/backend-go/internal/modules/gatewayrouteplan"
	protocolgateway "juhe-ai/backend-go/internal/protocols/gateway"
)

var (
	ErrCurrentGroupMissing           = errors.New("gateway cross-group owner current-group runner is required")
	ErrTargetPreparerMissing         = errors.New("gateway cross-group owner target preparer is required")
	ErrClientIPMissing               = errors.New("gateway cross-group owner client-IP acquirer is required")
	ErrPendingFailureTransferMissing = errors.New("gateway cross-group owner pending-failure transfer is required")
	ErrTerminalMissing               = errors.New("gateway cross-group owner terminal observer is required")
	ErrLifecycleHookProvided         = errors.New("gateway cross-group owner owns the lifecycle-ready hook")
	ErrLifecycleProvided             = errors.New("gateway cross-group owner must create the request lifecycle")
)

// CurrentGroupRunner is implemented by gatewaycurrentgroupexecution.Service.
// Its two methods make the request lifecycle transition explicit: the first
// group creates it; every later group must validate and reuse it.
type CurrentGroupRunner interface {
	RunWithRequestLifecycle(gatewaycurrentgroupexecution.Input) (gatewaycurrentgroupexecution.Result, *gatewayrequestlifecycle.Lifecycle, error)
	RunWithExistingRequestLifecycle(gatewaycurrentgroupexecution.Input, *gatewayrequestlifecycle.Lifecycle) (gatewaycurrentgroupexecution.Result, error)
}

// DispatchTargetPreparer is implemented by gatewayrouteplan.Service. Its
// result is opaque, so this owner cannot substitute a retained source window
// for a fresh policy-gated target.
type DispatchTargetPreparer interface {
	PrepareDispatchFallbackTarget(context.Context, gatewayrouteplan.FallbackDispatchPreparedInput) (gatewayrouteplan.FallbackDispatchPreparedTarget, error)
}

// ClientIPAcquirer is the same narrow runtime dependency consumed by high
// concurrency execution. The owner uses it only after a target execution has
// passed the opaque routeplan and request-execution fences.
type ClientIPAcquirer interface {
	Acquire(context.Context, gatewayclientipconcurrency.Input) (gatewayclientipconcurrency.Decision, error)
}

// PendingFailureTransfer is the caller-owned request-local transfer seam.
// This owner never receives or stores a Tracker.
type PendingFailureTransfer interface {
	Transfer(context.Context, gatewayclientipconcurrency.Scope, gatewayclientipconcurrency.Scope) (gatewayclientipconcurrency.TransferResult, error)
}

type PendingFailureTransferFunc func(context.Context, gatewayclientipconcurrency.Scope, gatewayclientipconcurrency.Scope) (gatewayclientipconcurrency.TransferResult, error)

func (f PendingFailureTransferFunc) Transfer(ctx context.Context, source, target gatewayclientipconcurrency.Scope) (gatewayclientipconcurrency.TransferResult, error) {
	if f == nil {
		return gatewayclientipconcurrency.TransferResult{}, errors.New("gateway cross-group owner pending-failure transfer function is nil")
	}
	return f(ctx, source, target)
}

type Options struct {
	CurrentGroup CurrentGroupRunner
	Targets      DispatchTargetPreparer
	ClientIP     ClientIPAcquirer
}

type Service struct {
	currentGroup CurrentGroupRunner
	targets      DispatchTargetPreparer
	clientIP     ClientIPAcquirer
}

func NewService(options Options) (*Service, error) {
	if options.CurrentGroup == nil {
		return nil, ErrCurrentGroupMissing
	}
	if options.Targets == nil {
		return nil, ErrTargetPreparerMissing
	}
	if options.ClientIP == nil {
		return nil, ErrClientIPMissing
	}
	return &Service{currentGroup: options.CurrentGroup, targets: options.Targets, clientIP: options.ClientIP}, nil
}

// Input contains the immutable route-only plan and the current finalized
// execution. The terminal belongs to the HTTP owner and is mandatory because
// retained high-concurrency target leases must share its first-terminal fence.
type Input struct {
	Current                gatewaycurrentgroupexecution.Input
	Route                  gatewayrouteplan.RouteOnlyResult
	Policy                 gatewayrouteplan.FallbackCandidatePolicy
	Terminal               *gatewayhttpcompletion.Observer
	PendingFailureTransfer PendingFailureTransfer
}

type Result struct {
	Groups             []gatewaycurrentgroupexecution.Result
	Lifecycle          *gatewayrequestlifecycle.Lifecycle
	EnteredGroupIDs    []string
	ExcludedAccountIDs []string
}

// Run executes one initial group and, only after a full opaque target
// preparation, runs later singleton targets on the same lifecycle. It never
// registers HTTP routes or converts a terminal outcome into a public response.
func (s *Service) Run(input Input) (Result, error) {
	if s == nil || s.currentGroup == nil {
		return Result{}, ErrCurrentGroupMissing
	}
	if s.targets == nil {
		return Result{}, ErrTargetPreparerMissing
	}
	if s.clientIP == nil {
		return Result{}, ErrClientIPMissing
	}
	if input.Terminal == nil {
		return Result{}, ErrTerminalMissing
	}
	if input.PendingFailureTransfer == nil {
		return Result{}, ErrPendingFailureTransferMissing
	}
	if input.Current.Lifecycle != nil {
		return Result{}, ErrLifecycleProvided
	}
	if input.Current.OnRequestLifecycleReady != nil {
		return Result{}, ErrLifecycleHookProvided
	}
	if input.Policy == nil {
		return Result{}, fmt.Errorf("gateway cross-group owner fallback policy is required")
	}
	if input.Current.Context == nil {
		return Result{}, fmt.Errorf("gateway cross-group owner context is required")
	}

	initialBatch, err := firstBatch(input.Current.Execution)
	if err != nil {
		return Result{}, err
	}
	cursor, err := gatewayrouteplan.InitialFallbackCursor(input.Route, initialBatch.BindingID())
	if err != nil {
		return Result{}, fmt.Errorf("initialize gateway fallback cursor: %w", err)
	}
	state := &requestState{
		service: s, input: input, source: input.Current.Execution, cursor: cursor,
		entered: []string{initialBatch.GroupID()}, sourceScope: transferScopeForBatch(initialBatch, input.Current.Execution.APIKeyID(), input.Current.ClientIP),
	}

	current := input.Current
	current.PreserveLifecycleOnCandidatesExhausted = true
	current.Fallback = state
	current.PostSourceLeaseFallback = state
	current.OnRequestLifecycleReady = func(lifecycle *gatewayrequestlifecycle.Lifecycle) {
		state.lifecycle = lifecycle
		input.Terminal.OnTerminal(func(terminal gatewayhttpcompletion.Terminal) {
			if terminal.Reason == gatewayhttpcompletion.TerminalClientCanceled && lifecycle != nil {
				_, _ = lifecycle.CancelClient()
			}
		})
	}
	first, lifecycle, runErr := s.currentGroup.RunWithRequestLifecycle(current)
	result := Result{Groups: []gatewaycurrentgroupexecution.Result{first}, Lifecycle: lifecycle}
	if lifecycle == nil {
		if runErr != nil {
			return result, runErr
		}
		return result, fmt.Errorf("gateway cross-group owner current group did not return lifecycle")
	}
	state.lifecycle = lifecycle
	if runErr != nil {
		return s.failRun(result, input.Terminal, lifecycle, runErr)
	}

	latest := first
	for {
		if state.pending == nil {
			if reason, excluded, ok := fallbackFacts(latest); ok {
				if err := state.prepareAndStore(input.Current.Context, reason, excluded, nil); err != nil {
					return s.failRun(result, input.Terminal, lifecycle, err)
				}
			}
		}
		if state.pending == nil {
			if failure, terminal := terminalFailure(latest); terminal {
				if err := finishTerminalFailure(input.Terminal, lifecycle, failure); err != nil {
					return result, err
				}
			}
			result.EnteredGroupIDs = append([]string(nil), state.entered...)
			result.ExcludedAccountIDs = append([]string(nil), state.excluded...)
			return result, nil
		}

		target := state.takePending()
		if _, completed := input.Terminal.Terminal(); completed {
			target.releaseBeforeTerminal()
			if _, err := settleObservedTerminal(input.Terminal, lifecycle); err != nil {
				return result, err
			}
			return result, fmt.Errorf("gateway cross-group owner terminal completed before target execution")
		}
		if target.clientIP != nil && target.clientIP.Lease != nil {
			input.Terminal.OnTerminal(func(gatewayhttpcompletion.Terminal) { target.clientIP.Lease.Release() })
		}

		state.source, state.cursor, state.entered, state.sourceScope = target.execution, target.cursor, target.entered, target.scope
		targetInput := input.Current
		targetInput.Execution = target.execution
		targetInput.Lifecycle = nil
		targetInput.OnRequestLifecycleReady = nil
		targetInput.PreserveLifecycleOnCandidatesExhausted = true
		targetInput.Fallback = state
		targetInput.PostSourceLeaseFallback = state
		targetInput.PreAcquiredClientIP = target.clientIP
		targetInput.RetainPreAcquiredClientIPLease = target.clientIP != nil

		latest, runErr = s.currentGroup.RunWithExistingRequestLifecycle(targetInput, lifecycle)
		result.Groups = append(result.Groups, latest)
		if runErr != nil {
			return s.failRun(result, input.Terminal, lifecycle, runErr)
		}
	}
}

func (s *Service) failRun(result Result, terminal *gatewayhttpcompletion.Observer, lifecycle *gatewayrequestlifecycle.Lifecycle, cause error) (Result, error) {
	if completed, err := settleObservedTerminal(terminal, lifecycle); err != nil {
		return result, fmt.Errorf("run gateway cross-group owner: %w; settle terminal lifecycle: %v", cause, err)
	} else if completed {
		return result, fmt.Errorf("run gateway cross-group owner: %w", cause)
	}
	if _, err := lifecycle.FinishRequestFailure(gatewayrequestlifecycle.FailureGateway); err != nil && !errors.Is(err, gatewayrequestlifecycle.ErrTerminal) {
		return result, fmt.Errorf("run gateway cross-group owner: %w; terminal gateway lifecycle: %v", cause, err)
	}
	return result, fmt.Errorf("run gateway cross-group owner: %w", cause)
}

type requestState struct {
	service     *Service
	input       Input
	lifecycle   *gatewayrequestlifecycle.Lifecycle
	source      gatewayrequestexecution.Execution
	cursor      gatewayrouteplan.FallbackCursor
	entered     []string
	excluded    []string
	sourceScope gatewayclientipconcurrency.Scope
	pending     *preparedTarget
}

type preparedTarget struct {
	execution gatewayrequestexecution.Execution
	cursor    gatewayrouteplan.FallbackCursor
	entered   []string
	clientIP  *gatewayclientipconcurrency.Decision
	handoff   gatewayclientipconcurrency.Input
	scope     gatewayclientipconcurrency.Scope
}

func (s *requestState) RequestFallback(ctx context.Context, reason string) (gatewayhighconcurrencyorchestration.FallbackResult, error) {
	if err := s.prepareAndStore(ctx, reason, nil, nil); err != nil {
		return gatewayhighconcurrencyorchestration.FallbackResult{}, err
	}
	return gatewayhighconcurrencyorchestration.FallbackResult{Attempted: s.pending != nil}, nil
}

func (s *requestState) PrepareFallbackTarget(ctx context.Context, reason string, handoff gatewayclientipconcurrency.TargetPreparationHandoff) (gatewayhighconcurrencyorchestration.FallbackResult, error) {
	if handoff == nil {
		return gatewayhighconcurrencyorchestration.FallbackResult{}, fmt.Errorf("gateway cross-group owner target handoff is required")
	}
	if err := s.prepareAndStore(ctx, reason, nil, handoff); err != nil {
		return gatewayhighconcurrencyorchestration.FallbackResult{}, err
	}
	return gatewayhighconcurrencyorchestration.FallbackResult{Attempted: s.pending != nil}, nil
}

func (s *requestState) prepareAndStore(ctx context.Context, reason string, excluded []string, handoff gatewayclientipconcurrency.TargetPreparationHandoff) error {
	if s.pending != nil {
		return fmt.Errorf("gateway cross-group owner already has a prepared target")
	}
	merged, err := mergeExcludedAccountIDs(s.excluded, excluded)
	if err != nil {
		return err
	}
	s.excluded = merged
	target, found, err := s.prepare(ctx, reason)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	transferResult, err := s.input.PendingFailureTransfer.Transfer(ctx, s.sourceScope, target.scope)
	if err != nil {
		target.releaseBeforeTerminal()
		return fmt.Errorf("transfer gateway client-IP account pending failures: %w", err)
	}
	if err := validateTransferResult(transferResult); err != nil {
		target.releaseBeforeTerminal()
		return fmt.Errorf("transfer gateway client-IP account pending failures: %w", err)
	}
	if handoff != nil {
		decision := gatewayclientipconcurrency.Decision{Acquired: true}
		if target.clientIP != nil {
			decision = *target.clientIP
		}
		if err := handoff.CompleteTargetPreparation(target.handoff, decision); err != nil {
			target.releaseBeforeTerminal()
			return fmt.Errorf("complete gateway source-to-target lease handoff: %w", err)
		}
	}
	s.pending = target
	return nil
}

func (s *requestState) prepare(ctx context.Context, reason string) (*preparedTarget, bool, error) {
	request, hasRequest := s.source.RequestShape()
	intent, hasIntent := s.source.RequestIntent()
	finalization, hasFinalization := s.source.IngressFinalization()
	lane, hasLane := s.source.FinalLane()
	capabilities := s.source.Capabilities()
	protocol := protocolgateway.ProtocolCode(capabilities.Protocol())
	endpoint := protocolgateway.EndpointFamilyFromPath(protocol, request.Path)
	if !hasRequest || !hasIntent || !hasFinalization || !hasLane || protocol == "" || endpoint == protocolgateway.EndpointUnknown || strings.TrimSpace(reason) == "" {
		return nil, false, fmt.Errorf("gateway cross-group owner source request facts are incomplete")
	}
	preparedInput := gatewayrouteplan.FallbackDispatchPreparedInput{
		FallbackPreparedInput: gatewayrouteplan.FallbackPreparedInput{
			Route: s.input.Route, Current: s.cursor, EnteredGroupIDs: append([]string(nil), s.entered...),
			RequestedModel: request.Model, EndpointFamily: string(endpoint),
		},
		Intent: intent, IngressFinalization: finalization, RequestShape: request, Protocol: protocol, FinalLane: lane,
		Reason: reason, RequestClientCompatibility: string(capabilities.RequestClientCompatibility()), RequestLane: string(lane),
		ExcludedAccountIDs: append([]string(nil), s.excluded...), Policy: s.input.Policy,
	}
	prepared, err := s.service.targets.PrepareDispatchFallbackTarget(ctx, preparedInput)
	if err != nil {
		return nil, false, fmt.Errorf("prepare gateway fallback target: %w", err)
	}
	decision := gatewayrequestexecution.BuildFallbackTarget(gatewayrequestexecution.FallbackTargetInput{
		Source: s.source, Route: s.input.Route, Current: s.cursor, Prepared: prepared, Reason: reason,
		EnteredGroupIDs: append([]string(nil), s.entered...), ExcludedAccountIDs: append([]string(nil), s.excluded...),
	})
	execution, found := decision.Execution()
	if !found {
		if decision.Outcome() == gatewayrequestexecution.OutcomeNoCandidate {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("build gateway fallback target rejected: %s", decision.RejectReason())
	}
	target, _, validated, err := gatewayrouteplan.ValidateFallbackDispatchPreparedTarget(s.input.Route, preparedInput, prepared)
	if err != nil || !validated {
		return nil, false, fmt.Errorf("validate gateway fallback target: %w", err)
	}
	batch, err := firstBatch(execution)
	if err != nil {
		return nil, false, err
	}
	next := &preparedTarget{execution: execution, cursor: target.Cursor(), entered: append(append([]string(nil), s.entered...), batch.GroupID())}
	next.handoff = gatewayclientipconcurrency.Input{SystemAccountID: batch.RuntimeWindow().Access.CallerSystemAccountID, GroupID: batch.GroupID(), APIKeyID: execution.APIKeyID(), ClientIP: s.input.Current.ClientIP}
	next.scope = transferScopeForBatch(batch, execution.APIKeyID(), s.input.Current.ClientIP)
	if batch.RuntimeWindow().Access.GroupType != "high_concurrency" {
		return next, true, nil
	}
	clientInput, err := gatewayhighconcurrencyexecution.ClientIPInputForWindow(batch.RuntimeWindow(), execution.APIKeyID(), s.input.Current.ClientIP)
	if err != nil {
		return nil, false, err
	}
	clientIP, err := s.service.clientIP.Acquire(ctx, clientInput)
	if err != nil {
		if clientIP.Lease != nil {
			clientIP.Lease.Release()
		}
		return nil, false, fmt.Errorf("acquire gateway target client-IP lease: %w", err)
	}
	if err := gatewayclientipconcurrency.ValidateAcquiredDecisionForInput(clientInput, clientIP); err != nil {
		if clientIP.Lease != nil {
			clientIP.Lease.Release()
		}
		return nil, false, fmt.Errorf("validate gateway target client-IP lease: %w", err)
	}
	next.clientIP, next.handoff = &clientIP, clientInput
	return next, true, nil
}

func (s *requestState) takePending() *preparedTarget {
	target := s.pending
	s.pending = nil
	return target
}

func (t *preparedTarget) releaseBeforeTerminal() {
	if t != nil && t.clientIP != nil && t.clientIP.Lease != nil {
		t.clientIP.Lease.Release()
	}
}

func transferScopeForBatch(batch gatewayrequestexecution.Batch, apiKeyID, clientIP string) gatewayclientipconcurrency.Scope {
	return gatewayclientipconcurrency.Scope{
		SystemAccountID: batch.RuntimeWindow().Access.CallerSystemAccountID,
		APIKeyID:        normalizeTransferAPIKey(apiKeyID),
		ClientIP:        strings.TrimSpace(clientIP),
	}
}

func normalizeTransferAPIKey(apiKeyID string) string {
	apiKeyID = strings.TrimSpace(apiKeyID)
	if apiKeyID == "" {
		return gatewayclientipconcurrency.InternalAPIKeyID
	}
	return apiKeyID
}

func validateTransferResult(result gatewayclientipconcurrency.TransferResult) error {
	if len(result.Errors) > 0 {
		return fmt.Errorf("transfer result contains embedded errors: %w", errors.Join(result.Errors...))
	}
	if result.Attempted < 0 || result.Inserted < 0 || result.Replaced < 0 || result.Dropped < 0 || result.CapacityDropped < 0 {
		return errors.New("transfer result contains negative counts")
	}
	if result.NoOp {
		reasons := 0
		if result.InvalidSource {
			reasons++
		}
		if result.InvalidTarget {
			reasons++
		}
		if result.SourceEmpty {
			reasons++
		}
		if reasons != 1 || result.SourceCleared || result.Attempted != 0 || result.Inserted != 0 || result.Replaced != 0 || result.CapacityDropped != 0 || result.Dropped != 0 {
			return errors.New("transfer result no-op reason or counts are inconsistent")
		}
		return nil
	}
	if result.InvalidSource || result.InvalidTarget || result.SourceEmpty {
		return errors.New("transfer result has no-op flags without NoOp")
	}
	if result.Attempted <= 0 || result.Dropped != result.CapacityDropped || !result.SourceCleared {
		return errors.New("transfer result counts or source-cleared state are inconsistent")
	}
	remaining := result.Attempted
	if result.Inserted > remaining {
		return errors.New("transfer result inserted count exceeds attempted count")
	}
	remaining -= result.Inserted
	if result.Replaced > remaining {
		return errors.New("transfer result replaced count exceeds remaining attempts")
	}
	remaining -= result.Replaced
	if result.CapacityDropped != remaining {
		return errors.New("transfer result counts or source-cleared state are inconsistent")
	}
	return nil
}

func firstBatch(execution gatewayrequestexecution.Execution) (gatewayrequestexecution.Batch, error) {
	batches := execution.Batches()
	if len(batches) == 0 || strings.TrimSpace(batches[0].BindingID()) == "" || strings.TrimSpace(batches[0].GroupID()) == "" {
		return gatewayrequestexecution.Batch{}, fmt.Errorf("gateway cross-group owner execution has no current batch")
	}
	return batches[0], nil
}

func fallbackFacts(result gatewaycurrentgroupexecution.Result) (string, []string, bool) {
	var attempts *gatewayattemptloop.Result
	if result.Normal != nil {
		attempts = result.Normal
	} else if result.High != nil {
		attempts = result.High.Attempts
	}
	if attempts == nil || attempts.Outcome != gatewayattemptloop.OutcomeCandidatesExhausted || !attempts.FallbackAccounts.Complete || strings.TrimSpace(attempts.FallbackReason) == "" {
		return "", nil, false
	}
	return attempts.FallbackReason, append([]string(nil), attempts.FallbackAccounts.ExcludedAccountIDs...), true
}

func mergeExcludedAccountIDs(current, next []string) ([]string, error) {
	seen := make(map[string]struct{}, len(current)+len(next))
	merged := make([]string, 0, len(current)+len(next))
	for _, accountID := range current {
		accountID = strings.TrimSpace(accountID)
		if accountID == "" {
			return nil, fmt.Errorf("gateway cross-group owner retained excluded account ID is empty")
		}
		if _, exists := seen[accountID]; exists {
			return nil, fmt.Errorf("gateway cross-group owner retained excluded account ID is duplicated")
		}
		seen[accountID] = struct{}{}
		merged = append(merged, accountID)
	}
	groupSeen := make(map[string]struct{}, len(next))
	for _, accountID := range next {
		accountID = strings.TrimSpace(accountID)
		if accountID == "" {
			return nil, fmt.Errorf("gateway cross-group owner fallback account ID is empty")
		}
		if _, exists := groupSeen[accountID]; exists {
			return nil, fmt.Errorf("gateway cross-group owner fallback account ID is duplicated")
		}
		groupSeen[accountID] = struct{}{}
		if _, exists := seen[accountID]; exists {
			continue
		}
		seen[accountID] = struct{}{}
		merged = append(merged, accountID)
	}
	return merged, nil
}

func terminalFailure(result gatewaycurrentgroupexecution.Result) (gatewayrequestlifecycle.FailureKind, bool) {
	if result.Normal != nil {
		return gatewayrequestlifecycle.FailureUpstream, result.Normal.Outcome == gatewayattemptloop.OutcomeCandidatesExhausted
	}
	if result.High == nil {
		return "", false
	}
	if result.High.Attempts != nil {
		return gatewayrequestlifecycle.FailureUpstream, result.High.Attempts.Outcome == gatewayattemptloop.OutcomeCandidatesExhausted
	}
	if result.High.ClientIP != nil && !result.High.ClientIP.Acquired {
		return gatewayrequestlifecycle.FailureGateway, true
	}
	switch result.High.Orchestration.Outcome {
	case gatewayhighconcurrencyorchestration.OutcomeBusy, gatewayhighconcurrencyorchestration.OutcomeAborted, gatewayhighconcurrencyorchestration.OutcomeFallback:
		return gatewayrequestlifecycle.FailureGateway, true
	default:
		return "", false
	}
}

func finishTerminalFailure(terminal *gatewayhttpcompletion.Observer, lifecycle *gatewayrequestlifecycle.Lifecycle, failure gatewayrequestlifecycle.FailureKind) error {
	if failure != gatewayrequestlifecycle.FailureUpstream && failure != gatewayrequestlifecycle.FailureGateway {
		return fmt.Errorf("gateway cross-group terminal failure kind is invalid")
	}
	if completed, err := settleObservedTerminal(terminal, lifecycle); err != nil {
		return err
	} else if completed {
		return nil
	}
	if _, err := lifecycle.FinishRequestFailure(failure); err != nil && !errors.Is(err, gatewayrequestlifecycle.ErrTerminal) {
		return fmt.Errorf("finish gateway cross-group lifecycle: %w", err)
	}
	return nil
}

func settleObservedTerminal(terminal *gatewayhttpcompletion.Observer, lifecycle *gatewayrequestlifecycle.Lifecycle) (bool, error) {
	terminal.CompleteClientCanceledIfContextDone()
	value, completed := terminal.Terminal()
	if !completed {
		return false, nil
	}
	if value.Reason == gatewayhttpcompletion.TerminalClientCanceled {
		if _, err := lifecycle.CancelClient(); err != nil && !errors.Is(err, gatewayrequestlifecycle.ErrTerminal) {
			return true, err
		}
	}
	return true, nil
}

var _ gatewayhighconcurrencyorchestration.FallbackRequester = (*requestState)(nil)
var _ gatewayhighconcurrencyexecution.PostSourceLeaseFallbackPreparer = (*requestState)(nil)
