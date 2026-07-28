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

type OAuthProbeSnapshotRuntime interface {
	OAuthCandidateReloader
	Snapshot(gatewaycandidatewindow.Candidate) (OAuthProbeCandidateSnapshot, error)
}

type OAuthProbeCoordinator interface {
	Coordinate(context.Context, OAuthCoordinationInput) OAuthCoordinationResult
}

type CooldownProbe struct {
	Loader           ExactCandidateLoader
	Current          CooldownCandidateReader
	TransportFactory CandidateTransportFactory
	OAuthSnapshots   OAuthProbeSnapshotRuntime
	OAuthCoordinator OAuthProbeCoordinator
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
	isAPIKey := strings.EqualFold(identity.Type, "api_key")
	isOAuth := strings.EqualFold(identity.Type, "oauth") || strings.EqualFold(identity.Type, "google_oauth")
	if !isAPIKey && !isOAuth {
		return taskFailure(fmt.Errorf("unsupported account probe identity type %q", identity.Type))
	}
	workingDirectory := strings.TrimSpace(p.WorkingDirectory)
	if workingDirectory == "" {
		workingDirectory, err = os.Getwd()
		if err != nil {
			return taskFailure(fmt.Errorf("resolve account probe working directory: %w", err))
		}
	}
	clientCompatibility := gatewaycandidatewindow.EffectiveClientCompatibility(candidate)
	if isOAuth && (strings.EqualFold(identity.ProviderCode, "openai") || strings.EqualFold(identity.ProviderCode, "gpt")) {
		clientCompatibility = "codex_responses"
	}
	prepared, err := PrepareRequest(candidate, RequestInput{
		Mode: mode, Model: target.HealthCheckModel, Prompt: p.Prompt,
		OAuth:               isOAuth,
		ClientCompatibility: clientCompatibility,
		SessionID:           uuid.NewString(), Today: now.UTC().Format(time.DateOnly), WorkingDirectory: workingDirectory,
	})
	if err != nil {
		return taskFailure(err)
	}
	var attempt HTTPAttempt
	evidenceMode := mode
	var fence func(context.Context) error
	var oauthAttempt OAuthAttempt
	if isAPIKey {
		apiKeyAttempt, prepareErr := PrepareAPIKeyAttempt(candidate, prepared, now)
		if prepareErr != nil {
			return taskFailure(prepareErr)
		}
		attempt = apiKeyAttempt
		fence = (APIKeyExecutionFence{
			Loader: p.Loader, Current: p.Current, LoadInput: loadInput, Expected: target,
			Candidate: candidate, Prepared: prepared, Attempt: apiKeyAttempt, Now: p.Now,
		}).Recheck
	} else {
		if p.OAuthSnapshots == nil || p.OAuthCoordinator == nil {
			return taskFailure(fmt.Errorf("native OAuth account probe dependencies are required"))
		}
		snapshot, snapshotErr := p.OAuthSnapshots.Snapshot(candidate)
		if snapshotErr != nil {
			return taskFailure(snapshotErr)
		}
		coordination := p.OAuthCoordinator.Coordinate(ctx, OAuthCoordinationInput{
			Snapshot: snapshot, Prepared: prepared, Reload: loadInput, Now: now.UTC(),
		})
		switch coordination.Disposition() {
		case OAuthCoordinationReady:
			var ok bool
			oauthAttempt, ok = coordination.Attempt()
			if !ok {
				return taskFailure(fmt.Errorf("OAuth probe coordinator did not return a ready attempt"))
			}
		case OAuthCoordinationReschedule:
			return taskFailure(fmt.Errorf("OAuth probe credentials changed; defer to a current cooldown task"))
		case OAuthCoordinationTaskFailure:
			if coordination.Err() != nil {
				return taskFailure(coordination.Err())
			}
			return taskFailure(fmt.Errorf("OAuth probe coordination failed"))
		default:
			return taskFailure(fmt.Errorf("OAuth probe coordinator returned an invalid disposition"))
		}
		attempt = oauthAttempt
		evidenceMode = oauthAttempt.EvidenceMode()
		fence = (OAuthExecutionFence{
			Reloader: p.OAuthSnapshots, Current: p.Current, LoadInput: loadInput, Expected: target,
			Candidate: candidate, Prepared: prepared, Attempt: oauthAttempt, Now: p.Now,
		}).Recheck
	}
	transport, err := p.TransportFactory.New(candidate)
	if err != nil {
		return taskFailure(err)
	}
	if closer, ok := transport.(interface{ CloseIdleConnections() }); ok {
		defer closer.CloseIdleConnections()
	}
	outcome, transportResult, executeErr := (Executor{Transport: transport, Fence: fence}).ExecuteAttempt(ctx, evidenceMode, attempt)
	if isOAuth {
		if fallback, ok := oauthAttempt.XAIModelFallback(transportResult.StatusCode, transportResult.Body, transportResult.BodyTruncated); ok {
			fallbackFence := (OAuthExecutionFence{
				Reloader: p.OAuthSnapshots, Current: p.Current, LoadInput: loadInput, Expected: target,
				Candidate: candidate, Prepared: prepared, Attempt: fallback, Fallback: true, Now: p.Now,
			}).Recheck
			fallbackOutcome, fallbackResult, fallbackErr := (Executor{Transport: transport, Fence: fallbackFence}).ExecuteAttempt(ctx, fallback.EvidenceMode(), fallback)
			if fallbackOutcome == accounthealth.ProbeOutcomeCompleteSuccess {
				outcome, transportResult, executeErr = fallbackOutcome, fallbackResult, fallbackErr
			}
		}
	}
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
