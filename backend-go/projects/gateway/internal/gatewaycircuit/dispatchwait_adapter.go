package gatewaycircuit

import (
	"context"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// Dispatch-side recoverable wait adapter (D-134, BUG-0175). The dispatch
// suppression filter keeps its typed state (gatewaydispatch
// SuppressionFilterResult); this adapter runs the same wait engine loop over
// caller-owned state closures so the composition root can implement the
// gatewaydispatch.RecoverableSuppressionWaiter port without a package cycle
// (gatewaydispatch already imports this package).

// StateWaitRefresh re-samples the caller-owned wait state. ready mirrors the
// fresh sample's readiness; hasRetryAfter / retryAfterMs carry the sample's
// next-retry hint (waitWithoutRetryAfter semantics stay inside the engine).
type StateWaitRefresh func(ctx context.Context) (ready bool, hasRetryAfter bool, retryAfterMs int64, err error)

// StateWaitInput mirrors the dispatch SuppressionWaitInput subset the wait
// engine consumes.
type StateWaitInput struct {
	ScopeKey string
	Reason   string
	// AuditCapture is optional; nil drops the wait metadata events.
	AuditCapture GatewayMetadataCapture
	MaxWaitMs    int64
	// RequestStartedAtMs / DeadlineAtMs ride the server retry budget.
	RequestStartedAtMs       int64
	DeadlineAtMs             int64
	RouteCoordinationBudget  *gatewayrouting.RouteCoordinationBudget
	GatewayRequestWallBudget *gatewayrouting.GatewayRequestWallBudget
	Signal                   context.Context
	Refresh                  StateWaitRefresh
}

// WaitForStateLoop runs the recoverable wait engine over the caller state and
// returns (waitedMs, skippedReason). skippedReason is empty on a ready exit
// and one of the WaitSkipped* constants otherwise. The initial sample runs
// before the loop so the first readiness check observes fresh state (the
// Node initialState argument).
func (w *PreAuthRecoverableWait) WaitForStateLoop(ctx context.Context, input StateWaitInput) (int64, string, error) {
	signal := input.Signal
	if signal == nil {
		signal = ctx
	}
	var stateMu sync.Mutex
	var ready bool
	var hasRetryAfter bool
	var nextRetryAfterMs int64
	refresh := func(ctx context.Context) error {
		sampleReady, sampleHasRetry, sampleRetryAfterMs, err := input.Refresh(ctx)
		if err != nil {
			return err
		}
		stateMu.Lock()
		ready, hasRetryAfter, nextRetryAfterMs = sampleReady, sampleHasRetry, sampleRetryAfterMs
		stateMu.Unlock()
		return nil
	}
	isReady := func() bool {
		stateMu.Lock()
		defer stateMu.Unlock()
		return ready
	}
	retryAfter := func() (int64, bool) {
		stateMu.Lock()
		defer stateMu.Unlock()
		return nextRetryAfterMs, hasRetryAfter
	}
	if err := refresh(ctx); err != nil {
		return 0, "", err
	}
	engineInput := waitInput{
		scopeKey:                 input.ScopeKey,
		reason:                   input.Reason,
		refresh:                  refresh,
		isReady:                  isReady,
		nextRetryAfterMs:         retryAfter,
		signal:                   signal,
		waitWithoutRetryAfter:    true,
		maxWaitMs:                input.MaxWaitMs,
		requestStartedAtMs:       int64Ptr(input.RequestStartedAtMs),
		deadlineAtMs:             int64Ptr(input.DeadlineAtMs),
		coordinator:              w.Coordinator,
		routeCoordinationBudget:  input.RouteCoordinationBudget,
		gatewayRequestWallBudget: input.GatewayRequestWallBudget,
		logger:                   w.Logger,
	}
	if input.AuditCapture != nil {
		engineInput.auditCapture = input.AuditCapture
	}
	if w.Options.MaxWaitMs != 0 {
		engineInput.maxWaitMs = w.Options.MaxWaitMs
	}
	if w.Options.CheckIntervalMs != 0 {
		engineInput.checkIntervalMs = w.Options.CheckIntervalMs
	}
	outcome, err := waitForRecoverableUnavailableState(ctx, engineInput)
	if err != nil {
		return 0, "", err
	}
	return outcome.waitedMs, outcome.skippedReason, nil
}
