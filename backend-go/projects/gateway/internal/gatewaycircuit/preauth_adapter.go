package gatewaycircuit

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// PreAuthRecoverableWait adapts the Node-faithful wait engine in this package
// to the gatewaypreauth.RecoverableWait port (G05). The G05 port keeps the
// wait state opaque to the orchestration via caller closures and returns only
// an error; the adapter maps those closures onto waitForRecoverableUnavailableState
// and preserves the Node audit metadata. The rich result
// (waitedMs / checkCount / timedOut / skippedReason) is intentionally
// reported through the error path as *RecoverableWaitOutcome so callers that
// need the full Node contract can type-assert it.
type PreAuthRecoverableWait struct {
	// Coordinator defaults to DefaultRecoverableWaitCoordinator.
	Coordinator *WaitCoordinator
	// Logger receives the scheduled-wait info log.
	Logger Logger
	// Options override the maxWait/checkInterval defaults.
	Options WaitEngineOptions
}

// RecoverableWaitOutcome carries the Node waitForRecoverableUnavailableState
// result through the error-only G05 port.
type RecoverableWaitOutcome struct {
	WaitedMs      int64
	CheckCount    int
	Ready         bool
	TimedOut      bool
	SkippedReason string
}

// Error implements error; the skipped reason is the machine-readable value.
func (o *RecoverableWaitOutcome) Error() string {
	return o.SkippedReason
}

// NewPreAuthRecoverableWait builds the G05 port implementation.
func NewPreAuthRecoverableWait(coordinator *WaitCoordinator, logger Logger) *PreAuthRecoverableWait {
	return &PreAuthRecoverableWait{Coordinator: coordinator, Logger: logger}
}

// WaitForRecoverableUnavailableState satisfies gatewaypreauth.RecoverableWait.
func (w *PreAuthRecoverableWait) WaitForRecoverableUnavailableState(ctx context.Context, input gatewaypreauth.RecoverableWaitInput) error {
	signal := input.Signal
	if signal == nil {
		signal = ctx
	}
	engineInput := waitInput{
		scopeKey:                input.ScopeKey,
		reason:                  input.Reason,
		refresh:                 input.Refresh,
		isReady:                 func() bool { return input.IsReady(ctx) },
		nextRetryAfterMs:        func() (int64, bool) { return input.NextRetryAfterMs(ctx) },
		signal:                  signal,
		waitWithoutRetryAfter:   true,
		maxWaitMs:               input.MaxWaitMs,
		requestStartedAtMs:      int64Ptr(input.RequestStartedAtMs),
		deadlineAtMs:            int64Ptr(input.DeadlineAtMs),
		coordinator:             w.Coordinator,
		routeCoordinationBudget: input.RouteCoordinationBudget,
		gatewayRequestWallBudget: input.GatewayRequestWallBudget,
		logger:                  w.Logger,
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
	if w.Options.DueRetryDelayMs != 0 {
		settings := DefaultSettings()
		settings.RecoverableUnavailableDueRetryDelayMs = w.Options.DueRetryDelayMs
	}
	outcome, err := waitForRecoverableUnavailableState(ctx, engineInput)
	if err != nil {
		return err
	}
	if outcome.skippedReason != "" {
		return &RecoverableWaitOutcome{
			WaitedMs:      outcome.waitedMs,
			CheckCount:    outcome.checkCount,
			Ready:         outcome.ready,
			TimedOut:      outcome.timedOut,
			SkippedReason: outcome.skippedReason,
		}
	}
	return nil
}

// SuppressionFilterPort is consumed by the dispatch preparation slice (G15);
// it is declared here so the gatewaypreauth CandidatePipeline wiring can type
// against the same store without importing Node naming.
type SuppressionFilterPort = LocalSuppressionStore

var _ gatewaypreauth.RecoverableWait = (*PreAuthRecoverableWait)(nil)
var _ = gatewayrouting.BudgetTransitionApplied
