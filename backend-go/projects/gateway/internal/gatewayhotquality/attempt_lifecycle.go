package gatewayhotquality

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
)

// Gateway hot quality attempt lifecycle mirroring
// backend/src/modules/gateway/runtime/hot-quality-attempt-lifecycle.ts.
// The Node promise chain (attemptReady → terminal) becomes a goroutine plus a
// closed channel; store errors are swallowed and warned exactly like Node.

// GatewayHotQualityTerminalInput mirrors the recordTerminal argument object of
// the lifecycle. Empty FailureScope/Source fall back to 'none' /
// 'request_lifecycle' like the Node `??` defaults.
type GatewayHotQualityTerminalInput struct {
	OutcomeClass string
	FailureScope string
	Source       string
	FirstByteMs  *float64
}

// GatewayHotQualityAttemptLifecycleInput mirrors the
// createGatewayHotQualityAttemptLifecycle argument object.
type GatewayHotQualityAttemptLifecycleInput struct {
	Runtime     *GatewayHotQualityRuntime
	AttemptID   string
	Account     GatewayHotQualityAccountView
	RequestLane string
	Model       *string
	NowMs       *int64
}

// GatewayHotQualityAttemptLifecycle mirrors GatewayHotQualityAttemptLifecycle.
type GatewayHotQualityAttemptLifecycle struct {
	AttemptID string
	Scope     HotQualityScope

	runtime *GatewayHotQualityRuntime
	nowMs   *int64

	attemptReady     chan struct{}
	attemptReadyOnce sync.Once

	firstByteMu sync.Mutex
	firstByteMs *int64

	terminalOnce sync.Once
	terminalDone chan struct{}
}

// NewGatewayHotQualityAttemptLifecycle mirrors
// createGatewayHotQualityAttemptLifecycle. Unlike the Node module-singleton
// fallback the Go composition root supplies the runtime explicitly.
func NewGatewayHotQualityAttemptLifecycle(input GatewayHotQualityAttemptLifecycleInput) (*GatewayHotQualityAttemptLifecycle, error) {
	attemptID, err := lifecycleRequired(input.AttemptID, "attemptId")
	if err != nil {
		return nil, err
	}
	protocolProfile, err := lifecycleRequired(
		orDefaultString(input.Account.ProviderProtocolProfileID, input.Account.ProtocolCode+":"+input.Account.ProtocolVersion),
		"protocolProfile",
	)
	if err != nil {
		return nil, err
	}
	runtimeKey, err := GatewayAccountRuntimeKey(input.Account)
	if err != nil {
		return nil, err
	}
	if input.Runtime == nil {
		return nil, errors.New("热质量运行时不能为空")
	}
	scope := HotQualityScope{
		AccountRuntimeKey: runtimeKey,
		ProtocolProfile:   protocolProfile,
		RequestLane:       input.RequestLane,
		ModelFamily:       GatewayHotQualityModelFamily(input.Model),
	}
	lifecycle := &GatewayHotQualityAttemptLifecycle{
		AttemptID:    attemptID,
		Scope:        scope,
		runtime:      input.Runtime,
		nowMs:        input.NowMs,
		attemptReady: make(chan struct{}),
		terminalDone: make(chan struct{}),
	}
	go lifecycle.recordAttemptSafely()
	return lifecycle, nil
}

func (lifecycle *GatewayHotQualityAttemptLifecycle) recordAttemptSafely() {
	runtime := lifecycle.runtime
	observeRouting(runtime, RoutingObservation{Kind: "attempt", Outcome: "started"})
	result, err := runtime.HotQualityStore.RecordAttempt(context.Background(), HotQualityRecordAttemptInput{
		AttemptID: lifecycle.AttemptID,
		Scope:     lifecycle.Scope,
		NowMs:     lifecycle.nowMs,
	})
	if err != nil {
		warnLog(runtime, map[string]interface{}{
			"event":     "gateway_hot_quality_attempt_record_failed",
			"attemptId": lifecycle.AttemptID,
		}, "记录热质量 attempt 失败")
	} else {
		observeRouting(runtime, RoutingObservation{
			Kind:      "hot_quality_mutation",
			Operation: "attempt",
			Status:    hotQualityObservationStatus(result.Status),
		})
	}
	lifecycle.attemptReadyOnce.Do(func() { close(lifecycle.attemptReady) })
}

// MarkFirstByte mirrors markFirstByte: first write wins, invalid values are
// ignored.
func (lifecycle *GatewayHotQualityAttemptLifecycle) MarkFirstByte(firstByteMs *float64) {
	lifecycle.firstByteMu.Lock()
	defer lifecycle.firstByteMu.Unlock()
	if lifecycle.firstByteMs != nil {
		return
	}
	if firstByteMs == nil || math.IsNaN(*firstByteMs) || math.IsInf(*firstByteMs, 0) || *firstByteMs < 0 {
		return
	}
	rounded := math.Round(*firstByteMs)
	roundedInt := int64(rounded)
	lifecycle.firstByteMs = &roundedInt
}

// RecordTerminal mirrors recordTerminal: memoized like the Node promise,
// waiting for the attempt record to settle first, never failing the caller
// with a store error (errors are warned through the logger port).
func (lifecycle *GatewayHotQualityAttemptLifecycle) RecordTerminal(ctx context.Context, terminal GatewayHotQualityTerminalInput) {
	lifecycle.terminalOnce.Do(func() {
		lifecycle.firstByteMu.Lock()
		capturedFirstByte := lifecycle.firstByteMs
		lifecycle.firstByteMu.Unlock()
		// Node: terminal.firstByteMs ?? firstByteMs — the explicit terminal
		// value is passed through unvalidated (the store validates it).
		effectiveFirstByte := terminal.FirstByteMs
		if effectiveFirstByte == nil && capturedFirstByte != nil {
			value := float64(*capturedFirstByte)
			effectiveFirstByte = &value
		}
		failureScope := terminal.FailureScope
		if failureScope == "" {
			failureScope = FailureScopeNone
		}
		source := terminal.Source
		if source == "" {
			source = TerminalSourceRequestLifecycle
		}
		<-lifecycle.attemptReady
		lifecycle.recordTerminalSafely(ctx, GatewayHotQualityTerminalInput{
			OutcomeClass: terminal.OutcomeClass,
			FailureScope: failureScope,
			Source:       source,
			FirstByteMs:  effectiveFirstByte,
		})
		close(lifecycle.terminalDone)
	})
	<-lifecycle.terminalDone
}

func (lifecycle *GatewayHotQualityAttemptLifecycle) recordTerminalSafely(ctx context.Context, terminal GatewayHotQualityTerminalInput) {
	runtime := lifecycle.runtime
	observeRouting(runtime, RoutingObservation{
		Kind:    "attempt",
		Outcome: terminalAttemptObservation(terminal.OutcomeClass),
	})
	result, err := runtime.HotQualityStore.RecordTerminal(ctx, HotQualityRecordTerminalInput{
		AttemptID:         lifecycle.AttemptID,
		Scope:             lifecycle.Scope,
		TerminalOutcomeID: lifecycle.AttemptID + ":terminal",
		OutcomeClass:      terminal.OutcomeClass,
		FailureScope:      terminal.FailureScope,
		Source:            terminal.Source,
		FirstByteMs:       terminal.FirstByteMs,
		NowMs:             lifecycle.nowMs,
	})
	if err != nil {
		warnLog(runtime, map[string]interface{}{
			"event":        "gateway_hot_quality_terminal_record_failed",
			"attemptId":    lifecycle.AttemptID,
			"outcomeClass": terminal.OutcomeClass,
		}, "记录热质量终态失败")
		return
	}
	observeRouting(runtime, RoutingObservation{
		Kind:      "hot_quality_mutation",
		Operation: "terminal",
		Status:    hotQualityObservationStatus(result.Status),
	})
}

func warnLog(runtime *GatewayHotQualityRuntime, fields map[string]interface{}, msg string) {
	if runtime != nil && runtime.Logger != nil {
		runtime.Logger.Warn(fields, msg)
	}
}

// hotQualityObservationStatus mirrors hotQualityObservationStatus.
func hotQualityObservationStatus(status string) string {
	if status == "applied" || status == "idempotent" {
		return status
	}
	if strings.Contains(status, "capacity") {
		return "capacity_exhausted"
	}
	if strings.Contains(status, "conflict") {
		return "conflict"
	}
	return "unavailable"
}

// terminalAttemptObservation mirrors terminalAttemptObservation.
func terminalAttemptObservation(outcome string) string {
	if outcome == TerminalOutcomeCompletedResponse || outcome == TerminalOutcomeExplicitPolicyFailure {
		return "completed"
	}
	if outcome == TerminalOutcomeClientCancellation {
		return "client_canceled"
	}
	if outcome == TerminalOutcomeUpstreamResponseFailure || outcome == TerminalOutcomeUnknown {
		return "unknown"
	}
	return "transport_failure"
}

func lifecycleRequired(value string, name string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "", fmt.Errorf("热质量 %s 不能为空", name)
	}
	return normalized, nil
}
