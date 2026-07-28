package accountprobe

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"juhe-ai/backend-go/internal/accounthealth"
	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/platform/upstreamtransport"
	"juhe-ai/backend-go/internal/store/port"
)

type CandidateTransportFactory interface {
	New(gatewaycandidatewindow.Candidate) (AttemptTransport, error)
}

type CooldownProbe struct {
	Loader           ExactCandidateLoader
	Current          CooldownCandidateReader
	TransportFactory CandidateTransportFactory
	Prompt           string
	WorkingDirectory string
	Now              func() time.Time
	NewTraceID       func() string
}

func (p CooldownProbe) Probe(ctx context.Context, target port.CooldownAccountRetestCandidate) (port.CooldownAccountRetestProbeResult, error) {
	traceID := uuid.NewString()
	if p.NewTraceID != nil {
		traceID = strings.TrimSpace(p.NewTraceID())
	}
	if traceID == "" {
		traceID = uuid.NewString()
	}
	taskFailure := func(err error) (port.CooldownAccountRetestProbeResult, error) {
		return port.CooldownAccountRetestProbeResult{
			Outcome: string(accounthealth.ProbeOutcomeTaskFailure), ErrorCode: "probe_task_failure",
			Message: err.Error(), TraceID: traceID,
		}, nil
	}
	if p.Loader == nil || p.Current == nil || p.TransportFactory == nil {
		return taskFailure(fmt.Errorf("account probe runtime dependencies are required"))
	}
	mode, ok := ParseEndpointMode(target.HealthCheckEndpointMode)
	if !ok {
		return taskFailure(fmt.Errorf("unsupported account probe endpoint mode %q", target.HealthCheckEndpointMode))
	}
	endpointFamily, _ := EndpointFamilyForMode(mode)
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	loadInput := LoadInput{
		AccountID: target.ID, GroupID: target.GroupID, SystemAccountID: target.SystemAccountID,
		RequestedModel: target.HealthCheckModel, EndpointFamily: string(endpointFamily), Now: now.UTC(),
	}
	candidate, found, err := p.Loader.Load(ctx, loadInput)
	if err != nil {
		return taskFailure(err)
	}
	if !found {
		return taskFailure(fmt.Errorf("account probe target is no longer available"))
	}
	identity := gatewaycandidatewindow.EffectiveAccountIdentity(candidate)
	if !strings.EqualFold(identity.Type, "api_key") {
		return taskFailure(fmt.Errorf("native OAuth account probe is not configured for %q", identity.Type))
	}
	workingDirectory := strings.TrimSpace(p.WorkingDirectory)
	if workingDirectory == "" {
		workingDirectory, err = os.Getwd()
		if err != nil {
			return taskFailure(fmt.Errorf("resolve account probe working directory: %w", err))
		}
	}
	prepared, err := PrepareRequest(candidate, RequestInput{
		Mode: mode, Model: target.HealthCheckModel, Prompt: p.Prompt,
		ClientCompatibility: gatewaycandidatewindow.EffectiveClientCompatibility(candidate),
		SessionID:           uuid.NewString(), Today: now.UTC().Format(time.DateOnly), WorkingDirectory: workingDirectory,
	})
	if err != nil {
		return taskFailure(err)
	}
	attempt, err := PrepareAPIKeyAttempt(candidate, prepared, now)
	if err != nil {
		return taskFailure(err)
	}
	transport, err := p.TransportFactory.New(candidate)
	if err != nil {
		return taskFailure(err)
	}
	if closer, ok := transport.(interface{ CloseIdleConnections() }); ok {
		defer closer.CloseIdleConnections()
	}
	fence := APIKeyExecutionFence{
		Loader: p.Loader, Current: p.Current, LoadInput: loadInput, Expected: target,
		Candidate: candidate, Prepared: prepared, Attempt: attempt, Now: p.Now,
	}
	outcome, transportResult, executeErr := (Executor{Transport: transport, Fence: fence.Recheck}).Execute(ctx, mode, attempt)
	result := port.CooldownAccountRetestProbeResult{
		Outcome: string(outcome), StatusCode: transportResult.StatusCode, TraceID: traceID,
	}
	if outcome == accounthealth.ProbeOutcomeCompleteSuccess {
		return result, nil
	}
	result.ErrorCode, result.Message = probeFailureDiagnostics(outcome, transportResult, executeErr)
	return result, nil
}

func ParseEndpointMode(value string) (EndpointMode, bool) {
	mode := EndpointMode(strings.TrimSpace(value))
	switch mode {
	case ModeChatJSON, ModeChatSSE, ModeResponsesJSON, ModeResponsesSSE, ModeMessagesJSON,
		ModeMessagesSSE, ModeGenerateContentJSON, ModeGenerateContentSSE, ModeInteractionsJSON, ModeInteractionsSSE:
		return mode, true
	default:
		return "", false
	}
}

func probeFailureDiagnostics(outcome accounthealth.ProbeOutcome, result upstreamtransport.Result, err error) (string, string) {
	message := "account probe did not produce protocol completion evidence"
	if err != nil {
		message = err.Error()
	}
	switch outcome {
	case accounthealth.ProbeOutcomeFramingCompleteNeutral:
		if result.StatusCode < 200 || result.StatusCode >= 300 {
			return "upstream_http_error", fmt.Sprintf("upstream returned HTTP %d", result.StatusCode)
		}
		return "invalid_protocol_success_response", message
	case accounthealth.ProbeOutcomeUpstreamFailure:
		if kind, ok := upstreamtransport.FailureKindOf(err); ok {
			switch kind {
			case upstreamtransport.FailureTimeout:
				return "upstream_timeout", message
			case upstreamtransport.FailureConnection:
				return "upstream_connection_error", message
			case upstreamtransport.FailureRead:
				return "upstream_read_error", message
			}
		}
		return "upstream_transport_error", message
	default:
		return "probe_task_failure", message
	}
}

var _ interface {
	Probe(context.Context, port.CooldownAccountRetestCandidate) (port.CooldownAccountRetestProbeResult, error)
} = CooldownProbe{}
