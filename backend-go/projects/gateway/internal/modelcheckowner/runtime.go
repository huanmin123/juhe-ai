package modelcheckowner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	keymodelruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/key_model_runtime"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprobe"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

type Target struct {
	Endpoint, ProviderCode, ConfigRevision, UpstreamModel string
	// ProviderProtocolProfileID is the immutable provider protocol profile
	// selected by Business. Trusted comparison requires this exact profile,
	// in addition to provider and protocol, to match Node's comparison gate.
	ProviderProtocolProfileID                       string
	TargetName, TargetOwnerSystemAccountID, GroupID string
	CredentialType                                  string
	// UpstreamAdapter is selected only by the resolved source-account
	// credential/profile contract. It is never inferred from a public request.
	UpstreamAdapter string
	Client          *http.Client
	// CredentialSourceAccountID identifies the physical account whose secret
	// was used. Authorized instances keep their virtual TargetID for reporting,
	// but admission/circuit identity must remain source-account scoped.
	CredentialSourceAccountID string
	// SourceConfigRevision and SourceDispatchRevision fence the physical
	// credential source used by an authorized instance. The virtual account
	// revisions above remain the public target CAS values.
	SourceConfigRevision   string
	SourceDispatchRevision int64
	DispatchRevision       int64
	OwnPhysicalAccount     bool
	Protocol               modelcheckprofile.Protocol
	// SourceEndpointFamily and UpstreamEndpointFamily are the immutable
	// model-mapping decision. Keep them distinct from Protocol because a
	// supported bridge can change the upstream request family.
	SourceEndpointFamily   modelcheckprofile.EndpointFamily
	UpstreamEndpointFamily modelcheckprofile.EndpointFamily
	// UpstreamProtocol is the protocol selected by model mapping. It may differ
	// from Protocol for a supported bridge (for example Responses -> Chat).
	UpstreamProtocol     modelcheckprofile.Protocol
	UpstreamEndpointMode string
	// EndpointMode is the Business-selected health-check request shape. An
	// empty value preserves compatibility with older resolver fixtures and is
	// deterministically derived from protocol/stream by modelcheckprobe.
	EndpointMode           string
	SupportedEndpointModes []string
	// SupportedModels is the resolved physical account allowlist. An empty
	// slice preserves the historical unrestricted-account behavior.
	SupportedModels []string
	Headers         http.Header
	Prompt          string
}

type Resolver func(context.Context, RunRequest) (Target, error)

// Runtime is the Gateway-owned basic probe execution path. It deliberately
// requires an injected resolver so credential/source reads remain inside the
// Gateway owner and never become an HTTP or Node dependency.
type Runtime struct {
	Store             *Store
	Resolve           Resolver
	ResolveComparison Resolver
	Tokenizer         modelcheckprobe.Tokenizer
	ModelLimits       modelcheckprobe.ModelLimitSnapshot
	Projector         *QualityProjector
	OwnerID           string
	Now               func() time.Time
	Lease             time.Duration
	OnEvent           func(ProgressEvent)
	Dispatcher        modelcheckprobe.DispatcherPort
}

func (s *Runtime) Run(ctx context.Context, request RunRequest) (RunResult, error) {
	return s.run(ctx, request, nil)
}

func (s *Runtime) RunStream(ctx context.Context, request RunRequest, onEvent func(ProgressEvent)) (RunResult, error) {
	return s.run(ctx, request, onEvent)
}

func (s *Runtime) run(ctx context.Context, request RunRequest, onEvent func(ProgressEvent)) (RunResult, error) {
	if s == nil || s.Store == nil || s.Resolve == nil {
		return RunResult{}, errors.New("J3b Gateway runtime is not initialized")
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	if request.SystemAccountID == "" || request.ActorSystemAccountID == "" {
		return RunResult{}, errors.New("J3b runtime scope is incomplete")
	}
	if request.TargetType != "account" || request.TargetID == "" || request.Model == "" {
		return RunResult{}, errors.New("J3b runtime request is incomplete")
	}
	if request.Profile != "quick" && request.Profile != "full" {
		return RunResult{}, errors.New("J3b runtime profile is invalid")
	}
	target, err := s.Resolve(ctx, request)
	if err != nil {
		return RunResult{}, fmt.Errorf("resolve J3b target: %w", err)
	}
	if target.Endpoint == "" || target.Prompt == "" {
		return RunResult{}, errors.New("resolved J3b target is incomplete")
	}
	if target.DispatchRevision < 1 {
		return RunResult{}, errors.New("resolved J3b target dispatch revision is invalid")
	}
	if request.DispatchRevision > 0 && request.DispatchRevision != target.DispatchRevision {
		return RunResult{}, errors.New("resolved J3b target dispatch revision is stale")
	}
	if request.SourceConfigRevision != "" && request.SourceConfigRevision != target.SourceConfigRevision {
		return RunResult{}, errors.New("resolved J3b source account config revision is stale")
	}
	if request.SourceDispatchRevision > 0 && request.SourceDispatchRevision != target.SourceDispatchRevision {
		return RunResult{}, errors.New("resolved J3b source account dispatch revision is stale")
	}
	if request.SourceEndpointFamily != "" && request.SourceEndpointFamily != string(target.SourceEndpointFamily) {
		return RunResult{}, errors.New("resolved J3b source endpoint family is stale")
	}
	if request.UpstreamEndpointFamily != "" && request.UpstreamEndpointFamily != string(target.UpstreamEndpointFamily) {
		return RunResult{}, errors.New("resolved J3b upstream endpoint family is stale")
	}
	if request.UpstreamProtocol != "" && request.UpstreamProtocol != string(target.UpstreamProtocol) {
		return RunResult{}, errors.New("resolved J3b upstream protocol is stale")
	}
	if request.UpstreamEndpointMode != "" && request.UpstreamEndpointMode != target.UpstreamEndpointMode {
		return RunResult{}, errors.New("resolved J3b upstream endpoint mode is stale")
	}
	var comparisonTarget Target
	if request.TrustedComparison {
		if (request.Profile != "quick" && request.Profile != "full") || strings.TrimSpace(request.TrustedComparisonAccountID) == "" || strings.TrimSpace(request.TrustedComparisonSystemAccountID) == "" || s.ResolveComparison == nil {
			return RunResult{}, errors.New("resolved J3b trusted comparison contract is incomplete")
		}
		if strings.TrimSpace(request.TrustedComparisonConfigRevision) == "" || request.TrustedComparisonDispatchRevision < 1 || strings.TrimSpace(request.TrustedComparisonSourceConfigRevision) == "" || request.TrustedComparisonSourceDispatchRevision < 1 {
			return RunResult{}, errors.New("resolved J3b trusted comparison revisions are incomplete")
		}
		comparisonTarget, err = s.ResolveComparison(ctx, request)
		if err != nil {
			return RunResult{}, fmt.Errorf("resolve J3b trusted comparison: %w", err)
		}
		if comparisonTarget.Endpoint == "" || comparisonTarget.Prompt == "" || comparisonTarget.UpstreamModel == "" || comparisonTarget.DispatchRevision < 1 {
			return RunResult{}, errors.New("resolved J3b trusted comparison target is incomplete")
		}
		comparisonProvider := modelcheckprofile.NormalizeToken(comparisonTarget.ProviderCode)
		targetProvider := modelcheckprofile.NormalizeToken(target.ProviderCode)
		if comparisonProvider == "" || targetProvider == "" || comparisonProvider != targetProvider {
			return RunResult{}, errors.New("resolved J3b trusted comparison provider is incompatible")
		}
		comparisonProtocol := modelcheckprofile.NormalizeToken(string(comparisonTarget.Protocol))
		targetProtocol := modelcheckprofile.NormalizeToken(string(target.Protocol))
		if comparisonProtocol == "" || targetProtocol == "" || comparisonProtocol != targetProtocol {
			return RunResult{}, errors.New("resolved J3b trusted comparison protocol is incompatible")
		}
		comparisonProfile := modelcheckprofile.NormalizeToken(comparisonTarget.ProviderProtocolProfileID)
		targetProfile := modelcheckprofile.NormalizeToken(target.ProviderProtocolProfileID)
		if comparisonProfile == "" || targetProfile == "" || comparisonProfile != targetProfile {
			return RunResult{}, errors.New("resolved J3b trusted comparison provider protocol profile is incompatible")
		}
		if comparisonTarget.ConfigRevision != request.TrustedComparisonConfigRevision {
			return RunResult{}, errors.New("resolved J3b trusted comparison config revision is stale")
		}
		if comparisonTarget.DispatchRevision != request.TrustedComparisonDispatchRevision {
			return RunResult{}, errors.New("resolved J3b trusted comparison dispatch revision is stale")
		}
		if comparisonTarget.SourceConfigRevision != request.TrustedComparisonSourceConfigRevision {
			return RunResult{}, errors.New("resolved J3b trusted comparison source account config revision is stale")
		}
		if comparisonTarget.SourceDispatchRevision != request.TrustedComparisonSourceDispatchRevision {
			return RunResult{}, errors.New("resolved J3b trusted comparison source account dispatch revision is stale")
		}
	}
	inputID, runID, outcomeID, traceID := newID("input"), newID("run"), newID("outcome"), newID("trace")
	probeSet := request.ProbeSetVersion
	if probeSet == "" {
		probeSet = modelcheckprofile.QuickProbeSetVersion
	}
	identity := request.IdentityKey
	if identity == "" {
		identity = request.SystemAccountID + ":" + request.TargetID + ":" + request.Model
	}
	// Keep the frozen revisions in the durable request summary. Health retry
	// must reuse the original CAS inputs instead of consulting mutable state.
	// Freeze both the requested identity and the resolved upstream identity.
	// Mapping rules are mutable configuration; retries and audit reads must never
	// silently substitute a later mapping for the model actually probed.
	upstreamModel := target.UpstreamModel
	if upstreamModel == "" {
		upstreamModel = request.Model
	}
	penaltyAction := request.PenaltyAction
	if penaltyAction == "" {
		penaltyAction = "quality_isolate"
	}
	if penaltyAction != "disable" && penaltyAction != "fallback" && penaltyAction != "quality_isolate" {
		return RunResult{}, errors.New("J3b runtime penalty action is invalid")
	}
	recoveryInterval := request.RecoveryIntervalMinutes
	if recoveryInterval == 0 {
		recoveryInterval = 10
	}
	if recoveryInterval < 10 || recoveryInterval > 10080 {
		return RunResult{}, errors.New("J3b runtime recovery interval is invalid")
	}
	providerCode := request.ProviderCode
	if providerCode == "" {
		providerCode = "unknown"
	}
	triggerKind := request.TriggerKind
	if triggerKind == "" {
		triggerKind = "manual"
	}
	if triggerKind != "manual" && triggerKind != "scheduled" && triggerKind != "quality_recovery" {
		return RunResult{}, errors.New("J3b runtime trigger is invalid")
	}
	manualEnforcementEligible := runtimeEnforcementAllowed(triggerKind, request)
	upstreamProtocol := target.UpstreamProtocol
	if upstreamProtocol == "" {
		upstreamProtocol = target.Protocol
	}
	upstreamEndpointMode := target.UpstreamEndpointMode
	if upstreamEndpointMode == "" {
		upstreamEndpointMode = modelcheckprofile.EndpointModeForProtocol(upstreamProtocol, false)
	}
	payloadSnapshot := map[string]any{"targetType": request.TargetType, "targetId": request.TargetID, "targetName": target.TargetName, "targetOwnerSystemAccountId": target.TargetOwnerSystemAccountID, "groupId": target.GroupID, "model": request.Model, "upstreamModel": upstreamModel, "profile": request.Profile, "protocol": target.Protocol, "providerProtocolProfileId": target.ProviderProtocolProfileID, "sourceEndpointFamily": target.SourceEndpointFamily, "upstreamProtocol": upstreamProtocol, "upstreamEndpointFamily": target.UpstreamEndpointFamily, "endpointMode": target.EndpointMode, "upstreamEndpointMode": upstreamEndpointMode, "credentialType": target.CredentialType, "upstreamAdapter": target.UpstreamAdapter, "endpointFingerprint": endpointFingerprint(target.Endpoint), "probeSetVersion": probeSet, "traceId": traceID, "configRevision": request.ConfigRevision, "sourceConfigRevision": request.SourceConfigRevision, "sourceDispatchRevision": request.SourceDispatchRevision, "policyRevision": request.PolicyRevision, "manualEnforcementEnabled": request.ManualEnforcementEnabled, "ownPhysicalAccount": request.OwnPhysicalAccount, "manualEnforcementEligible": manualEnforcementEligible}
	if request.TrustedComparison {
		payloadSnapshot["trustedComparison"] = map[string]any{"accountId": request.TrustedComparisonAccountID, "systemAccountId": request.TrustedComparisonSystemAccountID, "configRevision": request.TrustedComparisonConfigRevision, "dispatchRevision": request.TrustedComparisonDispatchRevision, "sourceConfigRevision": request.TrustedComparisonSourceConfigRevision, "sourceDispatchRevision": request.TrustedComparisonSourceDispatchRevision, "upstreamModel": comparisonTarget.UpstreamModel, "protocol": comparisonTarget.Protocol, "providerProtocolProfileId": comparisonTarget.ProviderProtocolProfileID, "sourceEndpointFamily": comparisonTarget.SourceEndpointFamily, "upstreamProtocol": comparisonTarget.UpstreamProtocol, "upstreamEndpointFamily": comparisonTarget.UpstreamEndpointFamily, "upstreamAdapter": comparisonTarget.UpstreamAdapter, "endpointFingerprint": endpointFingerprint(comparisonTarget.Endpoint)}
	}
	payload, _ := json.Marshal(payloadSnapshot)
	policySnapshot, _ := json.Marshal(map[string]any{"revision": request.PolicyRevision, "threshold": request.Threshold, "action": penaltyAction, "recoveryIntervalMinutes": recoveryInterval, "manualEnforcementEnabled": request.ManualEnforcementEnabled, "ownPhysicalAccount": request.OwnPhysicalAccount, "manualEnforcementEligible": manualEnforcementEligible})
	input, err := s.Store.IssueInput(ctx, InputRecord{InputID: inputID, IdentityKey: identity, TargetID: request.TargetID, ConfigRevision: request.ConfigRevision, PolicyRevision: request.PolicyRevision, Trigger: triggerKind, IssuedAt: now, ExpiresAt: now.Add(time.Minute), Payload: payload})
	if err != nil {
		return RunResult{}, err
	}
	accountID := ""
	if request.TargetType == "account" {
		accountID = request.TargetID
	}
	if err := s.Store.CreateRun(ctx, RunRecord{ID: runID, SystemAccountID: request.SystemAccountID, ActorSystemAccountID: request.ActorSystemAccountID, ProviderCode: providerCode, TargetType: request.TargetType, TargetID: request.TargetID, TargetName: target.TargetName, TargetOwnerSystemAccountID: targetOwnerOrDefault(target.TargetOwnerSystemAccountID, request.SystemAccountID), AccountID: accountID, GroupID: target.GroupID, Model: request.Model, Profile: request.Profile, TriggerKind: triggerKind, ScheduleID: request.ScheduleID, TrustedComparison: request.TrustedComparison, TrustedComparisonAvailable: request.TrustedComparison && comparisonTarget.Endpoint != "", ProbeSetVersion: probeSet, TraceID: traceID, StartedAt: now, RequestSummary: payload, PolicySnapshot: policySnapshot}); err != nil {
		return RunResult{}, err
	}
	lease := s.Lease
	if lease <= 0 {
		lease = time.Minute
	}
	claim, err := s.Store.ClaimInput(ctx, input.InputID, newID("claim"), outcomeID, ownerOrDefault(s.OwnerID), lease, now)
	if err != nil {
		return RunResult{}, err
	}
	// Full probes can outlive the default one-minute claim. Keep the lease
	// alive while the request is in flight, and cancel probing if ownership is
	// lost so stale workers cannot publish a result after a takeover.
	heartbeatCtx, heartbeatCancel := context.WithCancel(ctx)
	heartbeatErrCh := make(chan error, 1)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		interval := lease / 3
		if interval <= 0 {
			interval = time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				// Renewal must advance even when Runtime.Now is a frozen test clock.
				if err := s.Store.RenewClaim(heartbeatCtx, claim, lease, time.Now().UTC()); err != nil {
					select {
					case heartbeatErrCh <- err:
					default:
					}
					heartbeatCancel()
					return
				}
			}
		}
	}()
	// Re-read both targets after claiming the immutable input. Business may
	// revoke an account, rotate credentials, or change routing between the
	// initial admission read and the first upstream request.
	recheckedTarget, resolveErr := s.Resolve(heartbeatCtx, request)
	if resolveErr != nil {
		heartbeatCancel()
		<-heartbeatDone
		return s.finishFailure(ctx, runID, input, claim, now, fmt.Errorf("resolve J3b target after claim: %w", resolveErr))
	}
	if !sameTargetFence(recheckedTarget, target) {
		heartbeatCancel()
		<-heartbeatDone
		return s.finishFailure(ctx, runID, input, claim, now, errors.New("resolved J3b target changed after claim"))
	}
	target = recheckedTarget
	if request.TrustedComparison {
		recheckedComparison, comparisonResolveErr := s.ResolveComparison(heartbeatCtx, request)
		if comparisonResolveErr != nil {
			heartbeatCancel()
			<-heartbeatDone
			return s.finishFailure(ctx, runID, input, claim, now, fmt.Errorf("resolve J3b trusted comparison after claim: %w", comparisonResolveErr))
		}
		if !sameTargetFence(recheckedComparison, comparisonTarget) {
			heartbeatCancel()
			<-heartbeatDone
			return s.finishFailure(ctx, runID, input, claim, now, errors.New("resolved J3b trusted comparison changed after claim"))
		}
		comparisonTarget = recheckedComparison
	}
	emit := func(event ProgressEvent) {
		if onEvent != nil {
			onEvent(event)
		}
		if s.OnEvent != nil {
			s.OnEvent(event)
		}
	}
	emit(ProgressEvent{Kind: "run_started", Data: map[string]any{"runId": runID}})
	probeModel := upstreamModel
	probeSuite := modelcheckprobe.Suite{Endpoint: target.Endpoint, ProviderCode: target.ProviderCode, ProviderProtocolProfileID: target.ProviderProtocolProfileID, Headers: target.Headers, Model: probeModel, RequestModel: request.Model, ModelMappingApplied: probeModel != request.Model, Profile: request.Profile, Protocol: target.Protocol, UpstreamProtocol: target.UpstreamProtocol, EndpointMode: target.EndpointMode, UpstreamEndpointMode: target.UpstreamEndpointMode, SupportedEndpointModes: append([]string(nil), target.SupportedEndpointModes...), SupportedModels: append([]string(nil), target.SupportedModels...), Tokenizer: s.Tokenizer, ModelLimits: s.ModelLimits, Adapter: target.UpstreamAdapter, Retry: modelcheckprobe.RetryOptionsForProfile(request.Profile)}
	probeSuite.Dispatcher = s.Dispatcher
	probeSuite.Client = target.Client
	credentialSourceID := target.CredentialSourceAccountID
	if credentialSourceID == "" {
		credentialSourceID = request.TargetID
	}
	clientEndpointFamily := string(target.SourceEndpointFamily)
	if clientEndpointFamily == "" {
		clientEndpointFamily = string(target.Protocol)
	}
	probeSuite.Capability = keymodelruntime.Capability{CredentialSourceAccountID: credentialSourceID, KeyFingerprint: credentialSourceID, ClientModel: request.Model, ClientEndpointFamily: clientEndpointFamily, FinalUpstreamModel: probeModel, UpstreamEndpointMode: upstreamEndpointMode, DispatchRevision: target.DispatchRevision}
	if request.TrustedComparison {
		comparisonSuite := &modelcheckprobe.Suite{Endpoint: comparisonTarget.Endpoint, ProviderCode: comparisonTarget.ProviderCode, ProviderProtocolProfileID: comparisonTarget.ProviderProtocolProfileID, Headers: comparisonTarget.Headers, Model: comparisonTarget.UpstreamModel, RequestModel: request.Model, ModelMappingApplied: comparisonTarget.UpstreamModel != request.Model, Profile: request.Profile, Protocol: comparisonTarget.Protocol, UpstreamProtocol: comparisonTarget.UpstreamProtocol, EndpointMode: comparisonTarget.EndpointMode, UpstreamEndpointMode: comparisonTarget.UpstreamEndpointMode, SupportedEndpointModes: append([]string(nil), comparisonTarget.SupportedEndpointModes...), SupportedModels: append([]string(nil), comparisonTarget.SupportedModels...), Dispatcher: s.Dispatcher, Adapter: comparisonTarget.UpstreamAdapter, Tokenizer: s.Tokenizer, ModelLimits: s.ModelLimits, Retry: modelcheckprobe.RetryOptionsForProfile(request.Profile)}
		comparisonSuite.Client = comparisonTarget.Client
		comparisonSourceID := comparisonTarget.CredentialSourceAccountID
		if comparisonSourceID == "" {
			comparisonSourceID = request.TrustedComparisonAccountID
		}
		comparisonEndpointMode := comparisonTarget.EndpointMode
		if comparisonEndpointMode == "" {
			comparisonEndpointMode = modelcheckprofile.EndpointModeForProtocol(comparisonTarget.Protocol, false)
		}
		comparisonClientEndpointFamily := string(comparisonTarget.SourceEndpointFamily)
		if comparisonClientEndpointFamily == "" {
			comparisonClientEndpointFamily = string(comparisonTarget.Protocol)
		}
		comparisonSuite.Capability = keymodelruntime.Capability{CredentialSourceAccountID: comparisonSourceID, KeyFingerprint: comparisonSourceID, ClientModel: request.Model, ClientEndpointFamily: comparisonClientEndpointFamily, FinalUpstreamModel: comparisonTarget.UpstreamModel, UpstreamEndpointMode: comparisonEndpointMode, DispatchRevision: comparisonTarget.DispatchRevision}
		probeSuite.Comparison = comparisonSuite
	}
	items, probeErr := modelcheckprobe.RunSuite(heartbeatCtx, probeSuite, lease)
	heartbeatCancel()
	<-heartbeatDone
	var heartbeatErr error
	select {
	case heartbeatErr = <-heartbeatErrCh:
	default:
	}
	if probeErr == nil && heartbeatErr != nil {
		probeErr = fmt.Errorf("J3b claim lease renewal failed: %w", heartbeatErr)
	}
	if probeErr != nil {
		return s.finishFailure(ctx, runID, input, claim, now, probeErr)
	}
	if err := ctx.Err(); err != nil {
		return s.finishFailure(ctx, runID, input, claim, now, err)
	}
	itemRecords := make([]ItemRecord, 0, len(items))
	levelSummary := modelcheckprobe.SummarizeChecks(items, request.TrustedComparison, request.Profile)
	level, message := levelSummary.Level, levelSummary.Message
	for index, evaluation := range items {
		status := ItemStatus(evaluation.Status)
		if status == "" {
			status = ItemSkipped
		}
		evidence, _ := json.Marshal(evaluation.Evidence)
		itemRecords = append(itemRecords, ItemRecord{ID: fmt.Sprintf("%s-item-%04d", runID, index+1), RunID: runID, ItemKey: evaluation.Kind, ItemType: evaluation.Kind, Status: status, Score: evaluation.Score, MaxScore: evaluation.MaxScore, EvidenceSummary: string(evidence)})
	}
	score := levelSummary.Score
	status := RunCompleted
	// A completed HTTP 200 probe with negative quality evidence is still a
	// durable quality result. Transport/execute failures are returned by
	// RunSuite as probeErr and are finalized through finishFailure above;
	// promoting ItemFailed here would bypass the quality/health decision path.
	mappingApplied := probeModel != request.Model
	mappingStatus := "unknown"
	evidenceItems := make([]map[string]any, 0, len(items))
	for _, evaluation := range items {
		// Keep the evaluator's bounded, credential-free evidence available to
		// the trust projector. Dropping it here would make Juice/token/identity
		// anomalies indistinguishable from a successful receipt and would force
		// downstream code to infer trust from score alone.
		evidenceItems = append(evidenceItems, map[string]any{
			"kind": evaluation.Kind, "status": evaluation.Status,
			"score": evaluation.Score, "maxScore": evaluation.MaxScore,
			"evidence": evaluation.Evidence,
		})
	}
	if mappingApplied {
		// A configured mapping may legitimately return either the frozen
		// upstream model or the original request model. It remains configured
		// even if another quality item reports a mismatch; Node never promotes
		// configured mappings to the hard undeclared-mismatch gate.
		mappingStatus = "configured_mapping"
	} else if hasUndeclaredResponseModelMismatch(evidenceItems) {
		mappingStatus = "undeclared_mismatch"
	} else if hasResponseModelEvidence(evidenceItems) {
		mappingStatus = "direct"
	}
	aggregate := AggregateEvidence(evidenceItems)
	trustReport := BuildTrustReport(aggregate, evidenceItems)
	trustReport.MappingStatus = mappingStatus
	trustReport.RequestedModel = request.Model
	trustReport.MappedUpstreamModel = probeModel
	trustReport.MappingApplied = mappingApplied
	trustReport.ProbeSetVersion = probeSet
	if mappingStatus == "configured_mapping" {
		trustReport.ReasonCodes = appendReason(trustReport.ReasonCodes, "configured_model_mapping")
	} else if mappingStatus == "undeclared_mismatch" {
		trustReport.ReasonCodes = appendReason(trustReport.ReasonCodes, "undeclared_response_model_mismatch")
	}
	modelCheckUnverified := hasTerminalEvidence(evidenceItems)
	resultSummary := map[string]any{"evaluations": items, "score": score, "maxScore": 100, "level": level, "trustReport": trustReport, "modelCheckUnverified": modelCheckUnverified}
	if modelCheckUnverified {
		resultSummary["qualityDecisionSuppressedReason"] = "未形成质量判定证据"
	}
	resultPayload, _ := json.Marshal(resultSummary)
	if request.Profile != "quick" {
		if err := appendEvaluationObservations(ctx, s.Store, runID, request.SystemAccountID, request.TargetID, providerCode, request.Model, probeModel, mappingStatus, trustReport.ProtocolStatus, trustReport.IdentityStatus, trustReport.EvidenceCoverage, items, now); err != nil {
			return s.finishFailure(ctx, runID, input, claim, now, err)
		}
	}
	qualityUnavailable := level == "unavailable"
	// Node's hard gate is narrower than the run-level suspicious label: a
	// quality anomaly (for example a single Juice or long-context failure)
	// remains score-driven until it is independently confirmed. Only an
	// explicit, non-empty response model conflict bypasses the threshold here;
	// repeated Juice evidence is not available in this in-process projection.
	hardQualityFailure := mappingStatus == "undeclared_mismatch"
	qualityFailed := status == RunCompleted && !qualityUnavailable && (score < request.Threshold || hardQualityFailure)
	// Recovery validates whether an existing isolation can be cleared; it must
	// not create a second enforcement generation when the recovery probe fails.
	enforcementAllowed := manualEnforcementEligible && qualityFailed && triggerKind != "quality_recovery"
	qualityDecisionPayload := map[string]any{"evidenceFormed": aggregate.Formed, "trustFormed": aggregate.TrustFormed, "missingFamilies": aggregate.Missing, "partialFamilies": aggregate.Partial, "invalidFamilies": aggregate.Invalid, "manualEnforcementEnabled": request.ManualEnforcementEnabled, "ownPhysicalAccount": request.OwnPhysicalAccount, "hardQualityFailure": hardQualityFailure, "enforcementAllowed": enforcementAllowed, "trustReport": trustReport, "modelCheckUnverified": modelCheckUnverified}
	if modelCheckUnverified {
		qualityDecisionPayload["result"] = "not_triggered"
		qualityDecisionPayload["qualityDecisionSuppressedReason"] = "未形成质量判定证据"
		enforcementAllowed = false
		qualityDecisionPayload["enforcementAllowed"] = false
	}
	qualityDecision, _ := json.Marshal(qualityDecisionPayload)
	if err := ctx.Err(); err != nil {
		return s.finishFailure(ctx, runID, input, claim, now, err)
	}
	finalizeCtx, finalizeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer finalizeCancel()
	finishedAt := time.Now().UTC()
	if s.Now != nil {
		finishedAt = s.Now().UTC()
	}
	if err := s.Store.CommitOutcome(finalizeCtx, Outcome{OutcomeID: outcomeID, InputID: input.InputID, InputDigest: input.InputDigest, Payload: resultPayload}, claim, finishedAt); err != nil {
		return RunResult{}, err
	}
	durationMS := finishedAt.Sub(now).Milliseconds()
	if durationMS < 0 {
		durationMS = 0
	}
	if err := s.Store.ProjectOutcome(finalizeCtx, OutcomeProjection{RunID: runID, Status: status, Level: level, Score: score, MaxScore: 100, Message: message, FinishedAt: finishedAt, DurationMS: &durationMS, Items: itemRecords, ResultSummary: resultPayload, QualityDecision: qualityDecision}); err != nil {
		return RunResult{}, err
	}
	// Quick checks are diagnostic-only. They retain their run-local report but
	// must not overwrite durable observations, baselines, or the full-profile
	// latest projection.
	if request.Profile != "quick" {
		// Trust must be receipt-backed before the health/enforcement projector can
		// observe a formed result. A storage failure is returned to the caller and
		// deliberately prevents any downstream effect; the durable terminal run
		// remains available for an idempotent recovery projection.
		if err := s.Store.ProjectTrust(finalizeCtx, TrustProjection{RunID: runID, SystemAccountID: request.SystemAccountID, AccountID: request.TargetID, RequestedModel: request.Model, Report: trustReport}); err != nil {
			return RunResult{}, fmt.Errorf("project J3b trust: %w", err)
		}
	}
	// Node publishes a health failure only for a completed quality failure or
	// unavailable result. Publishing successful probes would make the existing
	// health reader treat a healthy account as failed.
	qualityHealthEligible := !modelCheckUnverified && (qualityUnavailable || request.Profile == "quick" || (aggregate.Formed && aggregate.TrustFormed))
	if status == RunCompleted && (qualityFailed || qualityUnavailable) && qualityHealthEligible && s.Projector != nil && request.Threshold > 0 && request.ProviderCode != "" {
		statHour, err := s.Store.formatHealthStatHour(finishedAt)
		if err != nil {
			if markErr := s.Store.MarkHealthSync(finalizeCtx, runID, "failed"); markErr != nil {
				err = fmt.Errorf("%w; mark J3b health sync failed: %v", err, markErr)
			}
			emit(ProgressEvent{Kind: "health_sync_failed", Data: map[string]any{"runId": runID, "message": err.Error()}})
		} else if err := s.Projector.Project(finalizeCtx, runID, aggregate, HealthFact{AccountID: request.TargetID, SystemAccountID: request.SystemAccountID, StatHour: statHour, RunID: runID, ProviderCode: request.ProviderCode, Model: request.Model, Profile: request.Profile, ScheduleID: request.ScheduleID, PolicyRevision: request.PolicyRevision, AccountConfigRevision: request.ConfigRevision, PenaltyAction: penaltyAction, RecoveryIntervalMinutes: recoveryInterval, EnforcementAllowed: enforcementAllowed, ObservedAt: finishedAt, Score: score, Threshold: request.Threshold, Level: level}); err != nil {
			// The run/outcome is already durable. Keep the health publication
			// retryable instead of reporting a false applied state.
			emit(ProgressEvent{Kind: "health_sync_failed", Data: map[string]any{"runId": runID, "message": err.Error()}})
		}
	}
	emit(ProgressEvent{Kind: "run_completed", Data: map[string]any{
		"runId": runID, "status": status, "level": level, "score": score,
		"maxScore": 100, "message": message,
	}})
	return RunResult{RunID: runID, Status: string(status), Data: map[string]any{
		"level":   level,
		"score":   score,
		"message": message,
		// Scheduler quality recovery consumes these explicit durable quality
		// gates. Omitting either flag must remain fail-closed in the executor.
		"evidenceFormed":       aggregate.Formed,
		"trustFormed":          aggregate.TrustFormed,
		"missingFamilies":      aggregate.Missing,
		"partialFamilies":      aggregate.Partial,
		"invalidFamilies":      aggregate.Invalid,
		"modelCheckUnverified": modelCheckUnverified,
		"qualityDecisionSuppressedReason": func() string {
			if modelCheckUnverified {
				return "未形成质量判定证据"
			}
			return ""
		}(),
	}}, nil
}

// runtimeEnforcementAllowed freezes the Node manual-diagnostics rule before
// any probe runs. Scheduled and recovery checks are policy-owned automation;
// only a manual run additionally requires both the explicit policy flag and a
// physical account, never an authorization instance.
func runtimeEnforcementAllowed(triggerKind string, request RunRequest) bool {
	if triggerKind == "" {
		triggerKind = "manual"
	}
	if triggerKind != "manual" {
		return triggerKind == "scheduled" || triggerKind == "quality_recovery"
	}
	return request.ManualEnforcementEnabled && request.OwnPhysicalAccount
}

// appendEvaluationObservations writes one bounded, durable receipt for every
// evaluation emitted by modelcheckprobe. The receipt intentionally contains
// no evaluation evidence payload: item evidence remains in the run projection
// after its own sanitizer, while observations carry only scope, mapping,
// protocol and formed/partial facts needed by downstream aggregation.
//
// IDs are derived from the run and stable evaluation position. A retry of the
// same projection therefore addresses the same primary keys; the idempotent
// append below treats an exact existing row as success and rejects drift.
func appendEvaluationObservations(ctx context.Context, store *Store, runID, systemAccountID, accountID, providerCode, requestedModel, mappedModel, mappingStatus, protocolStatus, identityStatus string, evidenceCoverage int, evaluations []modelcheckprobe.Evaluation, now time.Time) error {
	if len(evaluations) == 0 {
		return errors.New("J3b evaluation observations are empty")
	}
	observationIndex := 0
	for index, evaluation := range evaluations {
		family := strings.TrimSpace(evaluation.Kind)
		if family == "" {
			return fmt.Errorf("J3b evaluation %d family is empty", index)
		}
		// Trusted comparison checks are durable run items, but they are not
		// observations for the comparison tenant. Long-lived observation and
		// trust projections remain scoped to the primary target, matching Node.
		if strings.HasPrefix(family, "trusted_comparison.") {
			continue
		}
		observationIndex++
		if err := appendObservationIdempotent(ctx, store, ObservationRecord{
			ID:                  fmt.Sprintf("%s-observation-family-%04d", runID, observationIndex),
			RunID:               runID,
			SystemAccountID:     systemAccountID,
			AccountID:           accountID,
			ProviderCode:        providerCode,
			RequestedModel:      requestedModel,
			MappedUpstreamModel: mappedModel,
			ProbeFamily:         family,
			ObservationStatus:   evaluationObservationStatus(evaluation.Status),
			IdentityStatus:      identityStatus,
			MappingStatus:       mappingStatus,
			ProtocolStatus:      protocolStatus,
			EvidenceCoverage:    evidenceCoverage,
			CreatedAt:           now,
		}); err != nil {
			return fmt.Errorf("append J3b %s observation: %w", family, err)
		}
	}
	return nil
}

func hasUndeclaredResponseModelMismatch(items []map[string]any) bool {
	for _, item := range items {
		kind, _ := item["kind"].(string)
		if strings.HasPrefix(kind, "trusted_comparison.") {
			continue
		}
		switch modelcheckprobe.UnscopedKindForOwner(kind) {
		case "cross_model", "comparison", "distribution", "distribution_similarity":
			// Node's hard mapping gate uses target response evidence only. A
			// paired/self comparison is supporting diagnostic evidence, never a
			// configured-model mapping decision.
			continue
		}
		evidence, _ := item["evidence"].(map[string]any)
		success, _ := evidence["success"].(bool)
		if !success {
			continue
		}
		mismatch, _ := evidence["modelMismatch"].(bool)
		if mismatch {
			responseModel, _ := evidence["responseModel"].(string)
			if strings.TrimSpace(responseModel) != "" {
				return true
			}
		}
	}
	return false
}

func hasResponseModelEvidence(items []map[string]any) bool {
	for _, item := range items {
		kind, _ := item["kind"].(string)
		if strings.HasPrefix(kind, "trusted_comparison.") {
			continue
		}
		evidence, _ := item["evidence"].(map[string]any)
		success, _ := evidence["success"].(bool)
		if !success {
			continue
		}
		responseModel, _ := evidence["responseModel"].(string)
		if strings.TrimSpace(responseModel) != "" {
			return true
		}
	}
	return false
}

func hasTerminalEvidence(items []map[string]any) bool {
	for _, item := range items {
		evidence, _ := item["evidence"].(map[string]any)
		if terminal, _ := evidence["terminalFailure"].(bool); terminal {
			return true
		}
	}
	return false
}

func appendObservationIdempotent(ctx context.Context, store *Store, observation ObservationRecord) error {
	if store == nil || store.db == nil {
		return errors.New("J3b store is not open")
	}
	if err := validateObservation(observation); err != nil {
		return err
	}
	if found, err := observationMatches(ctx, store, observation); err != nil {
		return err
	} else if found {
		return nil
	}
	if err := store.AppendObservation(ctx, observation); err == nil {
		return nil
	} else if found, lookupErr := observationMatches(ctx, store, observation); lookupErr == nil && found {
		// Another owner of the same durable run may have won the insert race.
		// Exact row equality makes this a safe replay; any drift remains the
		// original append error and is never silently accepted.
		return nil
	} else {
		return err
	}
}

func observationMatches(ctx context.Context, store *Store, observation ObservationRecord) (bool, error) {
	var existing ObservationRecord
	var created string
	err := store.db.QueryRowContext(ctx, store.bind(`SELECT id,run_id,system_account_id,account_id,provider_code,requested_model,mapped_upstream_model,probe_family,observation_status,identity_status,mapping_status,protocol_status,evidence_coverage,created_at FROM `+store.table("model_check_observations")+` WHERE id=?`), observation.ID).Scan(
		&existing.ID, &existing.RunID, &existing.SystemAccountID, &existing.AccountID, &existing.ProviderCode,
		&existing.RequestedModel, &existing.MappedUpstreamModel, &existing.ProbeFamily, &existing.ObservationStatus,
		&existing.IdentityStatus, &existing.MappingStatus, &existing.ProtocolStatus, &existing.EvidenceCoverage, &created,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read J3b observation idempotency key: %w", err)
	}
	existing.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return false, fmt.Errorf("parse J3b observation created_at: %w", err)
	}
	if existing.RunID != observation.RunID || existing.SystemAccountID != observation.SystemAccountID || existing.AccountID != observation.AccountID || existing.ProviderCode != observation.ProviderCode || existing.RequestedModel != observation.RequestedModel || existing.MappedUpstreamModel != observation.MappedUpstreamModel || existing.ProbeFamily != observation.ProbeFamily || existing.ObservationStatus != observation.ObservationStatus || existing.IdentityStatus != observation.IdentityStatus || existing.MappingStatus != observation.MappingStatus || existing.ProtocolStatus != observation.ProtocolStatus || existing.EvidenceCoverage != observation.EvidenceCoverage || !existing.CreatedAt.UTC().Equal(observation.CreatedAt.UTC()) {
		return false, fmt.Errorf("J3b observation %s conflicts with existing row", observation.ID)
	}
	return true, nil
}

func evaluationObservationStatus(status string) string {
	switch strings.TrimSpace(status) {
	case "passed", "failed", "warning":
		return "complete"
	default:
		// skipped and unknown statuses are retained as partial evidence. They
		// must never be promoted to a formed quality fact by persistence.
		return "partial"
	}
}

func (s *Runtime) finishFailure(ctx context.Context, runID string, input InputRecord, claim Claim, now time.Time, cause error) (RunResult, error) {
	finishNow := time.Now().UTC()
	if s.Now != nil {
		finishNow = s.Now().UTC()
	}
	message := cause.Error()
	status := RunFailed
	if errors.Is(ctx.Err(), context.Canceled) {
		status = RunCanceled
		message = "J3b model check canceled without quality evidence"
	}
	payload, _ := json.Marshal(map[string]any{"success": false, "error": message})
	item := ItemRecord{ID: runID + "-item-0001", RunID: runID, ItemKey: "target.execution", ItemType: "execution", Status: ItemFailed, Score: 0, MaxScore: 100, EvidenceSummary: fmt.Sprintf(`{"message":%q}`, message), ErrorMessage: message}
	finalizeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Store.CommitOutcome(finalizeCtx, Outcome{OutcomeID: claim.OutcomeID, InputID: input.InputID, InputDigest: input.InputDigest, ObservedAt: finishNow, StoredAt: finishNow, Payload: payload}, claim, finishNow); err != nil {
		return RunResult{}, err
	}
	zeroDuration := int64(0)
	if err := s.Store.ProjectOutcome(finalizeCtx, OutcomeProjection{RunID: runID, Status: status, Level: "unavailable", Score: 0, MaxScore: 100, Message: message, FinishedAt: finishNow, DurationMS: &zeroDuration, ErrorCode: "model_check_execution_failed", ErrorMessage: message, Items: []ItemRecord{item}, ResultSummary: payload, QualityDecision: []byte(`{}`)}); err != nil {
		return RunResult{}, err
	}
	_ = s.Store.ReleaseClaim(finalizeCtx, claim, finishNow)
	return RunResult{RunID: runID, Status: string(status), Data: map[string]any{"message": message}}, cause
}

func ownerOrDefault(owner string) string {
	if strings.TrimSpace(owner) == "" {
		return "gateway"
	}
	return owner
}

func targetOwnerOrDefault(owner, fallback string) string {
	if strings.TrimSpace(owner) != "" {
		return strings.TrimSpace(owner)
	}
	return strings.TrimSpace(fallback)
}

func newID(prefix string) string {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return prefix + "-fallback"
	}
	return prefix + "-" + hex.EncodeToString(bytes[:])
}

func endpointFingerprint(endpoint string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(endpoint)))
	return hex.EncodeToString(sum[:])
}
