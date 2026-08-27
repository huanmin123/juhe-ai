package modelcheckowner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprobe"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

type Target struct {
	Endpoint, ProviderCode, ConfigRevision, UpstreamModel string
	Protocol                                              modelcheckprofile.Protocol
	Headers                                               http.Header
	Prompt                                                string
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
	var comparisonTarget Target
	if request.TrustedComparison {
		if request.Profile != "full" || strings.TrimSpace(request.TrustedComparisonAccountID) == "" || s.ResolveComparison == nil {
			return RunResult{}, errors.New("resolved J3b trusted comparison contract is incomplete")
		}
		comparisonTarget, err = s.ResolveComparison(ctx, request)
		if err != nil {
			return RunResult{}, fmt.Errorf("resolve J3b trusted comparison: %w", err)
		}
		if comparisonTarget.Endpoint == "" || comparisonTarget.Prompt == "" || comparisonTarget.UpstreamModel == "" {
			return RunResult{}, errors.New("resolved J3b trusted comparison target is incomplete")
		}
	}
	inputID, runID, outcomeID := newID("input"), newID("run"), newID("outcome")
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
	payloadSnapshot := map[string]any{"targetType": request.TargetType, "targetId": request.TargetID, "model": request.Model, "upstreamModel": upstreamModel, "profile": request.Profile, "protocol": target.Protocol, "endpointFingerprint": endpointFingerprint(target.Endpoint), "probeSetVersion": probeSet, "configRevision": request.ConfigRevision, "policyRevision": request.PolicyRevision}
	if request.TrustedComparison {
		payloadSnapshot["trustedComparison"] = map[string]any{"accountId": request.TrustedComparisonAccountID, "configRevision": request.TrustedComparisonConfigRevision, "upstreamModel": comparisonTarget.UpstreamModel, "protocol": comparisonTarget.Protocol, "endpointFingerprint": endpointFingerprint(comparisonTarget.Endpoint)}
	}
	payload, _ := json.Marshal(payloadSnapshot)
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
	policySnapshot, _ := json.Marshal(map[string]any{"revision": request.PolicyRevision, "threshold": request.Threshold, "action": penaltyAction, "recoveryIntervalMinutes": recoveryInterval})
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
	input, err := s.Store.IssueInput(ctx, InputRecord{InputID: inputID, IdentityKey: identity, TargetID: request.TargetID, ConfigRevision: request.ConfigRevision, PolicyRevision: request.PolicyRevision, Trigger: triggerKind, IssuedAt: now, ExpiresAt: now.Add(time.Minute), Payload: payload})
	if err != nil {
		return RunResult{}, err
	}
	accountID := ""
	if request.TargetType == "account" {
		accountID = request.TargetID
	}
	if err := s.Store.CreateRun(ctx, RunRecord{ID: runID, SystemAccountID: request.SystemAccountID, ActorSystemAccountID: request.ActorSystemAccountID, ProviderCode: providerCode, TargetType: request.TargetType, TargetID: request.TargetID, AccountID: accountID, Model: request.Model, Profile: request.Profile, TriggerKind: triggerKind, ScheduleID: request.ScheduleID, ProbeSetVersion: probeSet, StartedAt: now, RequestSummary: payload, PolicySnapshot: policySnapshot}); err != nil {
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
	probeSuite := modelcheckprobe.Suite{Endpoint: target.Endpoint, ProviderCode: target.ProviderCode, Headers: target.Headers, Model: probeModel, Profile: request.Profile, Protocol: target.Protocol, Tokenizer: s.Tokenizer, ModelLimits: s.ModelLimits}
	if request.TrustedComparison {
		probeSuite.Comparison = &modelcheckprobe.Suite{Endpoint: comparisonTarget.Endpoint, Headers: comparisonTarget.Headers, Model: comparisonTarget.UpstreamModel, Profile: request.Profile, Protocol: comparisonTarget.Protocol}
	}
	items, probeErr := modelcheckprobe.RunSuite(ctx, probeSuite, lease)
	if probeErr != nil {
		_ = s.Store.ReleaseClaim(ctx, claim, now)
		return s.finishFailure(ctx, runID, input, outcomeID, now, probeErr)
	}
	itemRecords := make([]ItemRecord, 0, len(items))
	totalScore, totalMax := 0, 0
	level, message := "success", "probe suite completed"
	for index, evaluation := range items {
		status := ItemStatus(evaluation.Status)
		if status == "" {
			status = ItemSkipped
		}
		if status == ItemFailed {
			level, message = "failure", "probe suite reported a failure"
		}
		if status == ItemWarning && level == "success" {
			level, message = "warning", "probe suite reported a warning"
		}
		evidence, _ := json.Marshal(evaluation.Evidence)
		itemRecords = append(itemRecords, ItemRecord{ID: fmt.Sprintf("%s-item-%04d", runID, index+1), RunID: runID, ItemKey: evaluation.Kind, ItemType: evaluation.Kind, Status: status, Score: evaluation.Score, MaxScore: evaluation.MaxScore, EvidenceSummary: string(evidence)})
		totalScore += evaluation.Score
		totalMax += evaluation.MaxScore
	}
	if totalMax == 0 {
		totalMax = 1
	}
	score := totalScore * 100 / totalMax
	status := RunCompleted
	for _, item := range itemRecords {
		if item.Status == ItemFailed {
			status = RunFailed
			break
		}
	}
	mappingStatus := "unmapped"
	if probeModel != request.Model {
		mappingStatus = "mapped"
	}
	resultPayload, _ := json.Marshal(map[string]any{"evaluations": items, "score": score, "maxScore": 100, "level": level})
	evidenceItems := make([]map[string]any, 0, len(items))
	for _, evaluation := range items {
		evidenceItems = append(evidenceItems, map[string]any{"kind": evaluation.Kind, "status": evaluation.Status, "score": evaluation.Score})
	}
	aggregate := AggregateEvidence(evidenceItems)
	trustReport := BuildTrustReport(aggregate, evidenceItems)
	observationStatus, protocolStatus := "partial", "passed"
	if aggregate.Formed {
		observationStatus = "complete"
	}
	if status == RunFailed {
		protocolStatus = "failed"
	}
	identityStatus := observationIdentityStatus(evidenceItems)
	if err := s.Store.AppendObservation(ctx, ObservationRecord{ID: runID + "-observation-0001", RunID: runID, SystemAccountID: request.SystemAccountID, AccountID: request.TargetID, ProviderCode: providerCode, RequestedModel: request.Model, MappedUpstreamModel: probeModel, ProbeFamily: "core-suite", ObservationStatus: observationStatus, IdentityStatus: identityStatus, MappingStatus: mappingStatus, ProtocolStatus: protocolStatus, EvidenceCoverage: len(aggregate.Families), CreatedAt: now}); err != nil {
		return RunResult{}, err
	}
	if request.TrustedComparison {
		comparisonMappingStatus := "unmapped"
		if comparisonTarget.UpstreamModel != request.Model {
			comparisonMappingStatus = "mapped"
		}
		if err := s.Store.AppendObservation(ctx, ObservationRecord{ID: runID + "-observation-0002", RunID: runID, SystemAccountID: request.SystemAccountID, AccountID: request.TrustedComparisonAccountID, ProviderCode: comparisonTarget.ProviderCode, RequestedModel: request.Model, MappedUpstreamModel: comparisonTarget.UpstreamModel, ProbeFamily: "trusted-comparison", ObservationStatus: observationStatus, IdentityStatus: identityStatus, MappingStatus: comparisonMappingStatus, ProtocolStatus: protocolStatus, EvidenceCoverage: len(aggregate.Families), CreatedAt: now}); err != nil {
			return RunResult{}, err
		}
	}
	qualityDecision, _ := json.Marshal(map[string]any{"evidenceFormed": aggregate.Formed, "trustFormed": aggregate.TrustFormed, "missingFamilies": aggregate.Missing, "partialFamilies": aggregate.Partial, "trust": trustReport})
	if err := s.Store.CommitOutcome(ctx, Outcome{OutcomeID: outcomeID, InputID: input.InputID, InputDigest: input.InputDigest, Payload: resultPayload}, claim, now); err != nil {
		return RunResult{}, err
	}
	if err := s.Store.ProjectOutcome(ctx, OutcomeProjection{RunID: runID, Status: status, Level: level, Score: score, MaxScore: 100, Message: message, FinishedAt: now, Items: itemRecords, ResultSummary: resultPayload, QualityDecision: qualityDecision}); err != nil {
		return RunResult{}, err
	}
	if aggregate.Formed && aggregate.TrustFormed && s.Projector != nil && request.Threshold > 0 && request.ProviderCode != "" {
		fact := HealthFact{AccountID: request.TargetID, SystemAccountID: request.SystemAccountID, StatHour: now.Truncate(time.Hour).Format(time.RFC3339Nano), RunID: runID, ProviderCode: request.ProviderCode, Model: request.Model, Profile: request.Profile, ScheduleID: request.ScheduleID, PolicyRevision: request.PolicyRevision, AccountConfigRevision: request.ConfigRevision, PenaltyAction: penaltyAction, RecoveryIntervalMinutes: recoveryInterval, ObservedAt: now, Score: score, Threshold: request.Threshold, Level: level}
		if err := s.Projector.Project(ctx, runID, aggregate, fact); err != nil {
			// The run/outcome is already durable. Keep the health publication
			// retryable instead of reporting a false applied state.
			emit(ProgressEvent{Kind: "health_sync_failed", Data: map[string]any{"runId": runID, "message": err.Error()}})
		}
	}
	emit(ProgressEvent{Kind: "run_completed", Data: map[string]any{"runId": runID, "status": status}})
	return RunResult{RunID: runID, Status: string(status), Data: map[string]any{"level": level, "score": score, "message": message}}, nil
}

func observationIdentityStatus(items []map[string]any) string {
	for _, item := range items {
		if kind, _ := item["kind"].(string); kind != "identity_observation" {
			continue
		}
		switch status, _ := item["status"].(string); status {
		case "passed":
			return "verified"
		case "failed":
			return "suspected_downgrade"
		default:
			return "unknown"
		}
	}
	return "unknown"
}

func (s *Runtime) finishFailure(ctx context.Context, runID string, input InputRecord, outcomeID string, now time.Time, cause error) (RunResult, error) {
	message := cause.Error()
	payload, _ := json.Marshal(map[string]any{"success": false, "error": message})
	item := ItemRecord{ID: runID + "-item-0001", RunID: runID, ItemKey: "target.execution", ItemType: "execution", Status: ItemFailed, Score: 0, MaxScore: 100, EvidenceSummary: fmt.Sprintf(`{"message":%q}`, message), ErrorMessage: message}
	if err := s.Store.ProjectOutcome(ctx, OutcomeProjection{RunID: runID, Status: RunFailed, Level: "unavailable", Score: 0, MaxScore: 100, Message: message, FinishedAt: now, Items: []ItemRecord{item}, ResultSummary: payload, QualityDecision: []byte(`{}`)}); err != nil {
		return RunResult{}, err
	}
	return RunResult{RunID: runID, Status: string(RunFailed), Data: map[string]any{"message": message}}, cause
}

func ownerOrDefault(owner string) string {
	if strings.TrimSpace(owner) == "" {
		return "gateway"
	}
	return owner
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
